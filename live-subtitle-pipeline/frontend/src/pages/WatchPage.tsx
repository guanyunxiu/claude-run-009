import { useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../lib/api";
import type { SessionInfo } from "../lib/types";
import { resolveToken } from "../lib/tokens";
import { useSubtitles } from "../lib/useSubtitles";
import { ConnectionBadge } from "../components/ConnectionBadge";
import { LatencyMeter } from "../components/LatencyMeter";
import { SubtitleList } from "../components/SubtitleList";
import { SubtitleOverlay } from "../components/SubtitleOverlay";

export default function WatchPage() {
  const { id = "" } = useParams();
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [error, setError] = useState("");
  const [targetLang, setTargetLang] = useState<string>("");

  // 观众令牌：来自主播分享链接 ?token=<viewToken>，或本机已保存的令牌。
  const token = useMemo(
    () => resolveToken(id, "view", new URLSearchParams(window.location.search).get("token")),
    [id],
  );

  const subtitles = useSubtitles(id, { token });
  const lastFinal = subtitles.finals[subtitles.finals.length - 1] ?? null;

  useEffect(() => {
    if (!token) {
      setError("缺少观众令牌（viewToken）。请使用主播分享的邀请链接进入。");
      return;
    }
    api.getSession(id, { token }).then((info) => {
      setSession(info);
      setTargetLang(info.targetLanguages?.[0] ?? "");
    }).catch((exc) => setError((exc as Error).message));
  }, [id, token]);

  const recentFinals = useMemo(
    () => subtitles.finals.slice(-30).reverse(),
    [subtitles.finals],
  );

  return (
    <div className="mx-auto max-w-6xl px-6 py-8">
      <header className="mb-6 flex flex-wrap items-center gap-3">
        <Link to="/" className="text-sm text-slate-400 hover:text-white">
          ← 首页
        </Link>
        <h1 className="text-xl font-bold text-white">观众席</h1>
        <span className="font-mono text-xs text-slate-500">{id}</span>
        <div className="ml-auto flex items-center gap-3">
          <ConnectionBadge state={subtitles.connection} attempts={subtitles.reconnectAttempts} />
        </div>
      </header>

      {error && <p className="mb-4 rounded-lg bg-red-900/30 px-3 py-2 text-sm text-red-300">{error}</p>}

      {subtitles.errorMessage && (
        <p className="mb-4 rounded-lg bg-red-900/30 px-3 py-2 text-sm text-red-300">
          {subtitles.errorMessage}
        </p>
      )}

      {subtitles.sessionEnded && (
        <p className="mb-4 rounded-lg bg-slate-800 px-3 py-2 text-sm text-slate-300">
          本场直播已结束。
        </p>
      )}

      <div className="grid gap-6 lg:grid-cols-[1fr_360px]">
        <div>
          <div className="relative aspect-video w-full overflow-hidden rounded-2xl border border-slate-800 bg-gradient-to-br from-slate-800 via-slate-900 to-black">
            <div className="absolute left-4 top-4 flex items-center gap-2">
              <span className="inline-flex items-center gap-1.5 rounded-full bg-red-600/80 px-3 py-1 text-xs font-semibold text-white">
                <span className="live-dot h-2 w-2 rounded-full bg-white" /> LIVE
              </span>
            </div>
            <div className="absolute inset-0 flex items-center justify-center text-slate-700">
              <span className="text-sm">实时字幕叠加层（DOM caption overlay）</span>
            </div>
            <SubtitleOverlay
              partial={subtitles.currentPartial}
              lastFinal={lastFinal}
              targetLang={targetLang || undefined}
            />
          </div>

          <div className="mt-4 flex flex-wrap items-center gap-4">
            <div className="flex items-center gap-2 text-xs text-slate-400">
              字幕语言：
              <select
                value={targetLang}
                onChange={(event) => setTargetLang(event.target.value)}
                className="rounded-md border border-slate-700 bg-slate-950 px-2 py-1 text-xs text-white"
              >
                <option value="">原文（{session?.sourceLanguage ?? "?"}）</option>
                {(session?.targetLanguages ?? []).map((code) => (
                  <option key={code} value={code}>
                    {code}
                  </option>
                ))}
              </select>
            </div>
            <div className="ml-auto">
              <LatencyMeter latency={subtitles.lastLatency} />
            </div>
          </div>
        </div>

        <aside className="flex max-h-[70vh] flex-col rounded-2xl border border-slate-800 bg-slate-900/40">
          <div className="border-b border-slate-800 px-4 py-3 text-sm font-semibold text-slate-200">
            字幕时间轴
            <span className="ml-2 text-xs font-normal text-slate-500">
              {subtitles.finals.length} 条 final
            </span>
          </div>
          <div className="flex-1 space-y-2 overflow-y-auto p-3">
            <SubtitleList finals={recentFinals} targetLang={targetLang || undefined} />
          </div>
        </aside>
      </div>
    </div>
  );
}
