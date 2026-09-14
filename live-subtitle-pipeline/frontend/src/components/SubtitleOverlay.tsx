import type { Subtitle } from "../lib/types";

interface Props {
  partial: Subtitle | null;
  lastFinal: Subtitle | null;
  /** 选中的目标语言；空串表示只看原文。 */
  targetLang?: string;
  /** 源/译对照：译文为主、原文为副；关闭时只显示译文。 */
  dual?: boolean;
}

/**
 * 字幕叠加层（DOM 层，绝对定位在“直播画面”底部）。
 *  - targetLang 为空：显示原文；
 *  - targetLang 非空：显示译文；dual=true 时下方附原文对照；
 *  - 底部滚动当前 partial。
 */
export function SubtitleOverlay({ partial, lastFinal, targetLang, dual }: Props) {
  const translation =
    targetLang && lastFinal?.translations ? lastFinal.translations[targetLang] : undefined;
  const showTranslation = Boolean(targetLang && translation);
  const main = showTranslation ? translation : lastFinal?.text;
  const showSourceUnder = showTranslation && dual;

  return (
    <div className="pointer-events-none absolute inset-x-0 bottom-0 flex flex-col items-center gap-1 px-6 pb-8">
      {lastFinal && (
        <div className="caption-shadow max-w-[90%] text-center text-2xl font-semibold leading-snug text-white">
          {main}
        </div>
      )}
      {lastFinal && showSourceUnder && (
        <div className="caption-shadow max-w-[85%] text-center text-lg text-slate-200">
          {lastFinal.text}
        </div>
      )}
      {partial && partial.seq !== lastFinal?.seq && (
        <div className="caption-shadow caption-partial max-w-[90%] text-center text-xl text-slate-100">
          {targetLang && partial.translations?.[targetLang]
            ? partial.translations[targetLang]
            : partial.text}
        </div>
      )}
    </div>
  );
}
