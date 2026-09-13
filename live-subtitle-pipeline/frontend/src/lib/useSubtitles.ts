import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { Subtitle } from "./types";

export type ConnectionState =
  | "idle"
  | "connecting"
  | "open"
  | "reconnecting"
  | "closed"
  | "denied";

export interface SubtitleState {
  /** 已确认 final 字幕，按 seq+startMs 排序并去重 */
  finals: Subtitle[];
  /** 当前 seq 上滚动的 partial 文本 */
  partialBySeq: Map<number, Subtitle>;
  /** 最近一条 partial（用于叠加层大字显示） */
  currentPartial: Subtitle | null;
  connection: ConnectionState;
  /** 最近一次重连后的延迟指标（ms） */
  lastLatency: { queueMs: number; asrMs: number; e2eMs: number } | null;
  reconnectAttempts: number;
  sessionEnded: boolean;
  /** 鉴权被拒（401/403）等不可恢复错误的说明 */
  errorMessage: string;
}

const MAX_FINALS = 500;
const MAX_RECONNECT_ATTEMPTS = 10;

/**
 * 订阅会话字幕：
 *  - WebSocket 实时推送 partial / final
 *  - 指数退避断线重连，重连时用 replay 让网关回放缺失 final
 *  - 按 seq+startMs 排序、去重；final 覆盖同 seq 的 partial
 */
export interface UseSubtitlesOptions {
  /** 访问令牌（hostToken 或 viewToken）。 */
  token?: string;
}

export function useSubtitles(sessionId: string | undefined, options?: UseSubtitlesOptions) {
  const token = options?.token ?? "";
  const [state, setState] = useState<SubtitleState>({
    finals: [],
    partialBySeq: new Map(),
    currentPartial: null,
    connection: "idle",
    lastLatency: null,
    reconnectAttempts: 0,
    sessionEnded: false,
    errorMessage: "",
  });

  const wsRef = useRef<WebSocket | null>(null);
  const timerRef = useRef<number | null>(null);
  const attemptsRef = useRef(0);
  const closedByUserRef = useRef(false);
  const finalsRef = useRef<Subtitle[]>([]);
  const partialRef = useRef<Map<number, Subtitle>>(new Map());

  const applyMessage = useCallback((raw: string) => {
    let msg: Subtitle;
    try {
      msg = JSON.parse(raw) as Subtitle;
    } catch {
      return;
    }

    if (msg.type === "session-end") {
      setState((prev) => ({ ...prev, sessionEnded: true, connection: "closed" }));
      closedByUserRef.current = true;
      return;
    }
    if (msg.type && msg.type !== "subtitle") return;

    const latency = {
      queueMs: msg.queueMs ?? 0,
      asrMs: msg.asrMs ?? 0,
      e2eMs: msg.e2eMs ?? 0,
    };

    if (msg.isFinal) {
      const existing = finalsRef.current;
      // 基础去重：同 (seq, startMs) 已存在则用新内容覆盖（重连 replay 场景）。
      const without = existing.filter(
        (item) => !(item.seq === msg.seq && item.startMs === msg.startMs),
      );
      const next = [...without, msg]
        .sort((a, b) => a.seq - b.seq || a.startMs - b.startMs)
        .slice(-MAX_FINALS);
      finalsRef.current = next;

      const partials = new Map(partialRef.current);
      partials.delete(msg.seq);
      partialRef.current = partials;

      setState((prev) => ({
        ...prev,
        finals: next,
        partialBySeq: partials,
        currentPartial: latestPartial(partials),
        lastLatency: latency,
      }));
    } else {
      const partials = new Map(partialRef.current);
      partials.set(msg.seq, msg);
      // partial 不无限累积，只保留最近 5 个 seq。
      for (const key of [...partials.keys()].sort((a, b) => a - b).slice(0, -5)) {
        partials.delete(key);
      }
      partialRef.current = partials;
      setState((prev) => ({
        ...prev,
        partialBySeq: partials,
        currentPartial: latestPartial(partials),
        lastLatency: latency,
      }));
    }
  }, []);

  const connect = useCallback(() => {
    if (!sessionId) return;
    // 无令牌直接判定为拒绝，避免 401 触发指数退避无限重连。
    if (!token) {
      setState((prev) => ({
        ...prev,
        connection: "denied",
        errorMessage: "缺少访问令牌，请使用有效链接进入（主播从主播台、观众用邀请链接）。",
      }));
      return;
    }
    closedByUserRef.current = false;

    const isReconnect = attemptsRef.current > 0;
    setState((prev) => ({
      ...prev,
      connection: isReconnect ? "reconnecting" : "connecting",
      reconnectAttempts: attemptsRef.current,
    }));

    // 重连时通过 replay 补齐断线期间落库的 final（网关返回最近 50 条）。
    const ws = new WebSocket(api.wsUrl(sessionId, token, isReconnect ? 50 : 20));
    wsRef.current = ws;

    ws.onopen = () => {
      attemptsRef.current = 0;
      setState((prev) => ({ ...prev, connection: "open", reconnectAttempts: 0 }));
    };

    ws.onmessage = (event) => {
      if (typeof event.data === "string") applyMessage(event.data);
    };

    ws.onerror = () => {
      // onclose 负责重连。
    };

    ws.onclose = () => {
      wsRef.current = null;
      if (closedByUserRef.current) {
        setState((prev) => ({ ...prev, connection: "closed" }));
        return;
      }
      attemptsRef.current += 1;
      const attempt = attemptsRef.current;

      // 重连上限：避免任何异常情况下无限重连刷屏。
      if (attempt > MAX_RECONNECT_ATTEMPTS) {
        closedByUserRef.current = true;
        setState((prev) => ({
          ...prev,
          connection: "closed",
          errorMessage: `已连续 ${MAX_RECONNECT_ATTEMPTS} 次无法连接，已停止重连。请检查网关后刷新页面。`,
        }));
        return;
      }

      // 指数退避：1s, 2s, 4s ... 上限 15s。
      const delay = Math.min(15_000, 1_000 * 2 ** Math.min(attempt - 1, 4));
      setState((prev) => ({ ...prev, connection: "reconnecting" }));
      timerRef.current = window.setTimeout(async () => {
        // 重连前用 REST 复核：
        //  - 会话已结束：停止（错过 session-end 的兜底）；
        //  - 401/403：令牌无效/越权，停止重连，明确提示而不是无限 403。
        try {
          const info = await api.getSession(sessionId, { token });
          if (info.status === "ended") {
            closedByUserRef.current = true;
            setState((prev) => ({ ...prev, connection: "closed", sessionEnded: true }));
            return;
          }
        } catch (exc) {
          const status = (exc as { status?: number }).status;
          if (status === 401 || status === 403) {
            closedByUserRef.current = true;
            setState((prev) => ({
              ...prev,
              connection: "denied",
              errorMessage:
                status === 403
                  ? "无权访问该会话（角色不足，如观众令牌访问主播接口）。"
                  : "访问令牌无效或已失效，请使用有效链接重新进入。",
            }));
            return;
          }
          // 其它（网关重启/网络）：继续退避重连，由 WS replay 补发。
        }
        connect();
      }, delay);
    };
  }, [sessionId, token, applyMessage]);

  useEffect(() => {
    if (!sessionId) return;
    finalsRef.current = [];
    partialRef.current = new Map();
    attemptsRef.current = 0;
    closedByUserRef.current = false;
    setState((prev) => ({ ...prev, errorMessage: "", sessionEnded: false }));
    connect();
    return () => {
      closedByUserRef.current = true;
      if (timerRef.current) window.clearTimeout(timerRef.current);
      wsRef.current?.close();
    };
  }, [sessionId, connect]);

  const close = useCallback(() => {
    closedByUserRef.current = true;
    wsRef.current?.close();
  }, []);

  return { ...state, close };
}

function latestPartial(partials: Map<number, Subtitle>): Subtitle | null {
  const keys = [...partials.keys()].sort((a, b) => a - b);
  const last = keys[keys.length - 1];
  return last === undefined ? null : partials.get(last) ?? null;
}
