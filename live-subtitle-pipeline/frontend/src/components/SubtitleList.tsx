import type { Subtitle } from "../lib/types";

interface Props {
  finals: Subtitle[];
  targetLang?: string;
}

function fmtClock(startMs: number): string {
  const date = new Date(startMs);
  return date.toLocaleTimeString("zh-CN", { hour12: false }) +
    "." + String(date.getMilliseconds()).padStart(3, "0");
}

/** 字幕时间轴列表：按 seq/startMs 已排序，展示双语与延迟。 */
export function SubtitleList({ finals, targetLang }: Props) {
  return (
    <div className="space-y-1">
      {finals.length === 0 && (
        <p className="py-10 text-center text-sm text-slate-600">暂无 final 字幕</p>
      )}
      {finals.map((item) => {
        const translation = targetLang ? item.translations?.[targetLang] : undefined;
        return (
          <div
            key={`${item.seq}-${item.startMs}`}
            className="rounded-lg border border-slate-800 bg-slate-900/40 px-3 py-2"
          >
            <div className="mb-0.5 flex items-center gap-2 text-[11px] text-slate-500">
              <span className="rounded bg-slate-800 px-1.5 py-0.5 font-mono">
                #{item.seq}
              </span>
              <span className="font-mono tabular-nums">{fmtClock(item.startMs)}</span>
              <span>{item.language}</span>
              {item.e2eMs !== undefined && (
                <span className="ml-auto tabular-nums text-slate-600">
                  端到端 {item.e2eMs}ms
                </span>
              )}
            </div>
            <p className="text-sm text-slate-100">{translation || item.text}</p>
            {translation && <p className="mt-0.5 text-xs text-slate-400">{item.text}</p>}
          </div>
        );
      })}
    </div>
  );
}
