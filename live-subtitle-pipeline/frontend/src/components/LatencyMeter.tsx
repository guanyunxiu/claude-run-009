interface Latency {
  queueMs: number;
  asrMs: number;
  e2eMs: number;
}

function barClass(value: number, warn: number, danger: number) {
  if (value >= danger) return "bg-red-400";
  if (value >= warn) return "bg-amber-400";
  return "bg-emerald-400";
}

/** 基础延迟显示：排队 / ASR / 端到端 三段耗时。 */
export function LatencyMeter({ latency }: { latency: Latency | null }) {
  if (!latency) {
    return <span className="text-xs text-slate-500">等待字幕数据…</span>;
  }
  const items = [
    { label: "排队", value: latency.queueMs },
    { label: "识别", value: latency.asrMs },
    { label: "端到端", value: latency.e2eMs },
  ];
  return (
    <div className="flex items-center gap-3 text-xs text-slate-300">
      {items.map((item) => (
        <span key={item.label} className="inline-flex items-center gap-1.5">
          <span className="text-slate-500">{item.label}</span>
          <span className={`h-1.5 w-1.5 rounded-full ${barClass(item.value, 500, 1500)}`} />
          <span className="tabular-nums">{item.value}ms</span>
        </span>
      ))}
    </div>
  );
}
