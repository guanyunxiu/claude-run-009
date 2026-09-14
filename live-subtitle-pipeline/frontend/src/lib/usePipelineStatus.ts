import { useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { PipelineStatus } from "./types";

export interface PipelineWarning {
  level: "ok" | "waiting" | "stalled";
  message: string;
}

/**
 * 轮询会话流水线状态，检测“切片在涨但字幕不出”这类静默故障：
 *  - 已有分片、但从未产出字幕，且距最近分片超过宽限期 -> stalled；
 *  - 刚开始（分片少、时间短）-> waiting（正常启动中）；
 *  - 否则 ok。
 */
export function usePipelineStatus(
  sessionId: string | undefined,
  token: string,
  active: boolean,
  hasSubtitle: boolean,
) {
  const [status, setStatus] = useState<PipelineStatus | null>(null);
  const [warning, setWarning] = useState<PipelineWarning>({ level: "ok", message: "" });
  const hasSubtitleRef = useRef(hasSubtitle);
  hasSubtitleRef.current = hasSubtitle;

  useEffect(() => {
    if (!sessionId || !token || !active) {
      setStatus(null);
      setWarning({ level: "ok", message: "" });
      return;
    }

    let cancelled = false;
    const GRACE_MS = 12_000; // 允许 worker 处理的宽限时间

    async function poll() {
      try {
        const s = await api.pipelineStatus(sessionId!, { token });
        if (cancelled) return;
        setStatus(s);

        if (hasSubtitleRef.current) {
          setWarning({ level: "ok", message: "" });
          return;
        }

        if (s.chunks === 0) {
          setWarning({ level: "ok", message: "" });
        } else {
          const refMs = s.lastChunkMs || s.serverMs;
          const sinceChunk = s.serverMs - refMs;
          if (sinceChunk > GRACE_MS) {
            setWarning({
              level: "stalled",
              message:
                `已上传 ${s.chunks} 个分片但 ${Math.round(sinceChunk / 1000)} 秒内没有任何字幕产出，` +
                "ASR Worker 可能未运行或处理失败（队列积压 " +
                `${s.streamBacklog}）。请检查 asr-worker 状态/日志。`,
            });
          } else {
            setWarning({
              level: "waiting",
              message: `首个分片已上传，等待 ASR 识别…（${Math.round(sinceChunk / 1000)}s）`,
            });
          }
        }
      } catch {
        /* 单次轮询失败忽略，下一轮重试 */
      }
    }

    void poll();
    const timer = window.setInterval(poll, 4000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [sessionId, token, active]);

  return { status, warning };
}
