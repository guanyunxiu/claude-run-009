import { useEffect, useState } from "react";
import { api } from "../lib/api";
import type { IngestJob } from "../lib/types";

interface Props {
  sessionId: string;
  token: string;
  language: string;
  targets: string[];
}

const STATUS_LABEL: Record<IngestJob["status"], { text: string; cls: string }> = {
  starting: { text: "启动中…", cls: "bg-amber-500/15 text-amber-300" },
  running: { text: "拉流中", cls: "bg-emerald-500/15 text-emerald-300" },
  reconnecting: { text: "断流重连中…", cls: "bg-red-500/15 text-red-300 live-dot" },
  stopped: { text: "已停止", cls: "bg-slate-700/40 text-slate-300" },
  failed: { text: "失败", cls: "bg-red-600/20 text-red-200" },
};

/** 外部直播源（RTMP/HLS）拉流任务的启停与状态展示。 */
export function IngestPanel({ sessionId, token, language, targets }: Props) {
  const [source, setSource] = useState("");
  const [kind, setKind] = useState<"auto" | "rtmp" | "hls">("auto");
  const [job, setJob] = useState<IngestJob | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function refresh() {
    try {
      const { jobs } = await api.listIngests(sessionId, { token });
      setJob(jobs[0] ?? null);
    } catch {
      /* 网关旧版无此接口时静默 */
    }
  }

  useEffect(() => {
    void refresh();
    const t = window.setInterval(refresh, 3000);
    return () => window.clearInterval(t);
  }, [sessionId, token]);

  const active = job && ["starting", "running", "reconnecting"].includes(job.status);

  async function start() {
    setError("");
    if (!source.trim()) {
      setError("请填写 RTMP/HLS 地址");
      return;
    }
    setBusy(true);
    try {
      await api.startIngest(
        sessionId,
        { kind: kind === "auto" ? undefined : kind, source: source.trim(), language, targets },
        { token },
      );
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function stop() {
    setBusy(true);
    try {
      await api.stopIngest(sessionId, { token });
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="rounded-2xl border border-slate-800 bg-slate-900/40 p-4">
      <h3 className="mb-3 text-sm font-semibold text-slate-200">
        外部直播源拉流（RTMP / HLS）
      </h3>

      {active ? (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span
              className={`rounded-full px-2.5 py-1 text-xs font-medium ${STATUS_LABEL[job!.status].cls}`}
            >
              {STATUS_LABEL[job!.status].text}
            </span>
            <span className="font-mono text-xs text-slate-400">{job!.kind.toUpperCase()}</span>
            <span className="text-xs text-slate-500">已切 {job!.chunks} 片</span>
            <button
              onClick={stop}
              disabled={busy}
              className="ml-auto rounded-lg border border-red-800 px-3 py-1.5 text-xs font-semibold text-red-300 hover:bg-red-900/30 disabled:opacity-40"
            >
              停止拉流
            </button>
          </div>
          <p className="break-all text-xs text-slate-500">{job!.sourceUrl}</p>
          {job!.status === "reconnecting" && (
            <p className="text-xs text-amber-400">源流中断，正在自动重连…</p>
          )}
          {job!.error && <p className="text-xs text-red-400">{job!.error}</p>}
        </div>
      ) : (
        <div className="space-y-2">
          <div className="flex gap-2">
            <select
              value={kind}
              onChange={(e) => setKind(e.target.value as typeof kind)}
              className="rounded-lg border border-slate-700 bg-slate-950 px-2 py-2 text-xs text-white"
            >
              <option value="auto">自动</option>
              <option value="rtmp">RTMP</option>
              <option value="hls">HLS</option>
            </select>
            <input
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder="rtmp://… 或 https://…/index.m3u8"
              className="min-w-0 flex-1 rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-xs text-white placeholder:text-slate-600"
            />
            <button
              onClick={start}
              disabled={busy}
              className="shrink-0 rounded-lg bg-brand-600 px-3 py-2 text-xs font-semibold text-white hover:bg-brand-500 disabled:opacity-40"
            >
              开始拉流
            </button>
          </div>
          <p className="text-xs text-slate-600">
            服务端用 ffmpeg 拉流并切成与浏览器相同的 3 秒 PCM 分片，可与麦克风采集并存；断流自动重连。
            （WebRTC/WHEP 服务端收流为预留能力，暂不可用。）
          </p>
        </div>
      )}

      {error && <p className="mt-2 text-xs text-red-400">{error}</p>}
    </section>
  );
}
