import type { Subtitle } from "../lib/types";

interface Props {
  partial: Subtitle | null;
  lastFinal: Subtitle | null;
  targetLang?: string;
}

/**
 * 字幕叠加层（DOM 层，绝对定位在“直播画面”底部）。
 * 上行显示最近 final，下行滚动当前 partial；可切换目标语言译文。
 */
export function SubtitleOverlay({ partial, lastFinal, targetLang }: Props) {
  const translation =
    targetLang && lastFinal?.translations ? lastFinal.translations[targetLang] : undefined;

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 flex flex-col items-center gap-1 px-6 pb-8">
      {lastFinal && (
        <div className="caption-shadow max-w-[90%] text-center text-2xl font-semibold leading-snug text-white">
          {translation || lastFinal.text}
        </div>
      )}
      {lastFinal && targetLang && translation && (
        <div className="caption-shadow max-w-[85%] text-center text-lg text-slate-200">
          {lastFinal.text}
        </div>
      )}
      {partial && partial.seq !== lastFinal?.seq && (
        <div className="caption-shadow caption-partial max-w-[90%] text-center text-xl text-slate-100">
          {partial.text}
        </div>
      )}
    </div>
  );
}
