import type { ConnectionState } from "../lib/useSubtitles";

const LABELS: Record<ConnectionState, { text: string; cls: string; dot: string }> = {
  idle: { text: "未连接", cls: "bg-slate-700/60 text-slate-300", dot: "bg-slate-400" },
  connecting: { text: "连接中…", cls: "bg-amber-500/15 text-amber-300", dot: "bg-amber-400" },
  open: { text: "已连接", cls: "bg-emerald-500/15 text-emerald-300", dot: "bg-emerald-400" },
  reconnecting: { text: "断线重连中…", cls: "bg-red-500/15 text-red-300", dot: "bg-red-400 live-dot" },
  closed: { text: "直播已结束", cls: "bg-slate-700/60 text-slate-300", dot: "bg-slate-400" },
  denied: { text: "无访问权限", cls: "bg-red-600/20 text-red-200", dot: "bg-red-500" },
};

export function ConnectionBadge({ state, attempts }: { state: ConnectionState; attempts: number }) {
  const meta = LABELS[state];
  return (
    <span className={`inline-flex items-center gap-2 rounded-full px-3 py-1 text-xs font-medium ${meta.cls}`}>
      <span className={`h-2 w-2 rounded-full ${meta.dot}`} />
      {meta.text}
      {state === "reconnecting" && attempts > 0 ? ` (第 ${attempts} 次)` : ""}
    </span>
  );
}
