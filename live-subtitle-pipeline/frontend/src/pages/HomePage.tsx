import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import { buildViewerLink, saveTokens } from "../lib/tokens";

const LANGUAGES: Record<string, string> = {
  zh: "中文",
  en: "English",
  ja: "日本語",
  ko: "한국어",
  fr: "Français",
  de: "Deutsch",
  es: "Español",
};

interface RecentSession {
  id: string;
  sourceLanguage: string;
  targetLanguages: string[];
  createdAt: string;
  hasHostToken: boolean;
}

const RECENT_KEY = "lsp:recent";

function loadRecent(): RecentSession[] {
  try {
    return JSON.parse(localStorage.getItem(RECENT_KEY) ?? "[]") as RecentSession[];
  } catch {
    return [];
  }
}

function pushRecent(session: RecentSession) {
  const all = [session, ...loadRecent().filter((item) => item.id !== session.id)].slice(0, 10);
  try {
    localStorage.setItem(RECENT_KEY, JSON.stringify(all));
  } catch {
    /* ignore */
  }
}

export default function HomePage() {
  const navigate = useNavigate();
  const [sourceLanguage, setSourceLanguage] = useState("zh");
  const [targets, setTargets] = useState<string[]>(["en"]);
  const [creating, setCreating] = useState(false);
  const [sessions, setSessions] = useState<RecentSession[]>([]);
  const [error, setError] = useState("");
  const [viewerLink, setViewerLink] = useState("");

  useEffect(() => {
    setSessions(loadRecent());
  }, []);

  function toggleTarget(code: string) {
    setTargets((prev) =>
      prev.includes(code) ? prev.filter((item) => item !== code) : [...prev, code],
    );
  }

  async function createSession(role: "broadcaster" | "viewer") {
    setError("");
    setCreating(true);
    try {
      const session = await api.createSession({
        sourceLanguage,
        targetLanguages: targets.filter((code) => code !== sourceLanguage),
        mediaType: "audio/pcm",
        sampleRate: 16000,
        channels: 1,
      });
      if (!session.hostToken || !session.viewToken) {
        throw new Error("网关未返回访问令牌");
      }
      saveTokens(session.id, {
        hostToken: session.hostToken,
        viewToken: session.viewToken,
      });
      pushRecent({
        id: session.id,
        sourceLanguage: session.sourceLanguage,
        targetLanguages: session.targetLanguages ?? [],
        createdAt: session.createdAt,
        hasHostToken: true,
      });
      setViewerLink(
        buildViewerLink(session.id, session.viewToken),
      );
      navigate(
        role === "broadcaster"
          ? `/broadcast/${session.id}`
          : `/watch/${session.id}?token=${encodeURIComponent(session.viewToken)}`,
      );
    } catch (exc) {
      setError((exc as Error).message);
    } finally {
      setCreating(false);
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-6 py-12">
      <header className="mb-10">
        <h1 className="text-3xl font-bold text-white">直播实时字幕与多语言翻译</h1>
        <p className="mt-2 text-slate-400">
          Web Audio 采集 · 固定切片 · ASR partial/final · WebSocket 实时推送 · 断线重连回放
        </p>
      </header>

      <section className="rounded-2xl border border-slate-800 bg-slate-900/60 p-6">
        <h2 className="mb-4 text-lg font-semibold text-white">新建直播会话</h2>

        <label className="mb-2 block text-sm text-slate-400">源语言（ASR 单语言转写）</label>
        <select
          value={sourceLanguage}
          onChange={(event) => setSourceLanguage(event.target.value)}
          className="mb-5 w-full rounded-lg border border-slate-700 bg-slate-950 px-3 py-2 text-sm text-white"
        >
          {Object.entries(LANGUAGES).map(([code, name]) => (
            <option key={code} value={code}>
              {name} ({code})
            </option>
          ))}
        </select>

        <label className="mb-2 block text-sm text-slate-400">翻译目标语言（可多选）</label>
        <div className="mb-6 flex flex-wrap gap-2">
          {Object.entries(LANGUAGES)
            .filter(([code]) => code !== sourceLanguage)
            .map(([code, name]) => (
              <button
                key={code}
                type="button"
                onClick={() => toggleTarget(code)}
                className={`rounded-full border px-3 py-1 text-sm transition ${
                  targets.includes(code)
                    ? "border-brand-500 bg-brand-600/30 text-white"
                    : "border-slate-700 text-slate-400 hover:border-slate-500"
                }`}
              >
                {name}
              </button>
            ))}
        </div>

        {error && <p className="mb-4 text-sm text-red-400">{error}</p>}

        <div className="flex gap-3">
          <button
            disabled={creating}
            onClick={() => createSession("broadcaster")}
            className="flex-1 rounded-lg bg-brand-600 px-4 py-2.5 text-sm font-semibold text-white hover:bg-brand-500 disabled:opacity-50"
          >
            🎙 我是主播（开始采集）
          </button>
          <button
            disabled={creating}
            onClick={() => createSession("viewer")}
            className="flex-1 rounded-lg border border-slate-600 px-4 py-2.5 text-sm font-semibold text-slate-200 hover:border-slate-400 disabled:opacity-50"
          >
            👀 以观众身份进入
          </button>
        </div>
        {viewerLink && (
          <p className="mt-3 break-all text-xs text-slate-500">
            观众邀请链接：{viewerLink}
          </p>
        )}
      </section>

      <section className="mt-8">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-slate-500">
          本机最近会话（令牌保存在本浏览器）
        </h2>
        <div className="space-y-2">
          {sessions.length === 0 && (
            <p className="text-sm text-slate-600">暂无本机会话记录。</p>
          )}
          {sessions.map((session) => (
            <div
              key={session.id}
              className="flex items-center justify-between rounded-xl border border-slate-800 bg-slate-900/40 px-4 py-3"
            >
              <div>
                <span className="font-mono text-sm text-slate-300">{session.id.slice(0, 8)}</span>
                <span className="ml-3 text-xs text-slate-500">
                  {session.sourceLanguage} → {(session.targetLanguages ?? []).join(", ") || "—"}
                </span>
              </div>
              <div className="flex gap-2 text-xs">
                <Link
                  className="rounded bg-slate-800 px-2.5 py-1 text-slate-300 hover:bg-slate-700"
                  to={`/broadcast/${session.id}`}
                >
                  主播台
                </Link>
                <Link
                  className="rounded bg-slate-800 px-2.5 py-1 text-slate-300 hover:bg-slate-700"
                  to={`/watch/${session.id}`}
                >
                  观众席
                </Link>
              </div>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}
