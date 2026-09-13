import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { AudioRecorder, type PcmChunk } from "../lib/recorder";
import type { SessionInfo } from "../lib/types";
import { resolveToken } from "../lib/tokens";
import { useSubtitles } from "../lib/useSubtitles";
import { ConnectionBadge } from "../components/ConnectionBadge";
import { LatencyMeter } from "../components/LatencyMeter";
import { SubtitleOverlay } from "../components/SubtitleOverlay";

interface UploadStat {
  chunks: number;
  bytes: number;
  failed: number;
  lastSeq: number | null;
  inFlight: number;
}

export default function BroadcastPage() {
  const { id = "" } = useParams();
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [recording, setRecording] = useState(false);
  const [error, setError] = useState("");
  const [stat, setStat] = useState<UploadStat>({
    chunks: 0,
    bytes: 0,
    failed: 0,
    lastSeq: null,
    inFlight: 0,
  });

  // 主播台使用 hostToken（本地存储；也接受 query 里的 token 用于直接进入）。
  const token = useMemo(
    () => resolveToken(id, "host", new URLSearchParams(window.location.search).get("token")),
    [id],
  );

  const recorderRef = useRef<AudioRecorder | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const [videoActive, setVideoActive] = useState(false);
  const tokenRef = useRef(token);
  tokenRef.current = token;

  const subtitles = useSubtitles(session?.id, { token });
  const lastFinal = subtitles.finals[subtitles.finals.length - 1] ?? null;

  useEffect(() => {
    if (!token) {
      setError("缺少主播令牌（hostToken）。请从本机创建会话后进入主播台。");
      return;
    }
    api
      .getSession(id, { token })
      .then(setSession)
      .catch((exc) => setError((exc as Error).message));
  }, [id, token]);

  const handleChunk = useCallback(
    async (chunk: PcmChunk) => {
      setStat((prev) => ({ ...prev, inFlight: prev.inFlight + 1 }));
      try {
        const result = await api.uploadChunk(
          id,
          { seq: chunk.seq, startMs: chunk.startMs, endMs: chunk.endMs },
          chunk.pcm,
          { token: tokenRef.current, contentType: "audio/pcm" },
        );
        setStat((prev) => ({
          chunks: prev.chunks + 1,
          bytes: prev.bytes + chunk.pcm.byteLength,
          failed: prev.failed,
          lastSeq: result.seq,
          inFlight: Math.max(0, prev.inFlight - 1),
        }));
      } catch (exc) {
        // 单片上传失败不中断直播：浏览器切片节奏继续，失败计入统计。
        console.error("upload chunk failed", exc);
        setStat((prev) => ({
          ...prev,
          failed: prev.failed + 1,
          inFlight: Math.max(0, prev.inFlight - 1),
        }));
      }
    },
    [id],
  );

  async function start() {
    setError("");
    try {
      const recorder = new AudioRecorder();
      await recorder.start(handleChunk, { video: true });
      recorderRef.current = recorder;
      // 绑定摄像头流到预览 <video>（静音，避免本地回声）。
      const stream = recorder.mediaStream;
      if (videoRef.current && stream) {
        videoRef.current.srcObject = stream;
        await videoRef.current.play().catch(() => undefined);
      }
      setVideoActive(recorder.videoActive);
      setRecording(true);
    } catch (exc) {
      setError(
        "采集启动失败：" +
          (exc as Error).message +
          "（需要 HTTPS 或 localhost，并授权麦克风/摄像头权限）",
      );
    }
  }

  function stopMic() {
    recorderRef.current?.stop();
    recorderRef.current = null;
    if (videoRef.current) {
      videoRef.current.srcObject = null;
    }
    setVideoActive(false);
    setRecording(false);
  }

  async function endSession() {
    stopMic();
    try {
      await api.endSession(id, { token: tokenRef.current });
      const refreshed = await api.getSession(id, { token: tokenRef.current });
      setSession(refreshed);
    } catch (exc) {
      setError((exc as Error).message);
    }
  }

  useEffect(() => () => recorderRef.current?.stop(), []);

  const targetLang = session?.targetLanguages?.[0];

  return (
    <div className="mx-auto max-w-5xl px-6 py-8">
      <header className="mb-6 flex flex-wrap items-center gap-3">
        <Link to="/" className="text-sm text-slate-400 hover:text-white">
          ← 首页
        </Link>
        <h1 className="text-xl font-bold text-white">主播台</h1>
        <span className="font-mono text-xs text-slate-500">{id}</span>
        <div className="ml-auto flex items-center gap-3">
          <ConnectionBadge state={subtitles.connection} attempts={subtitles.reconnectAttempts} />
        </div>
      </header>

      {/* 模拟直播画面：字幕叠加层 */}
      <div className="relative mb-6 aspect-video w-full overflow-hidden rounded-2xl border border-slate-800 bg-black">
        {/* 真实摄像头画面：镜像显示更符合主播自拍习惯；无视频轨时显示占位。 */}
        <video
          ref={videoRef}
          autoPlay
          playsInline
          muted
          className={`h-full w-full object-cover ${recording && videoActive ? "-scale-x-100" : "hidden"}`}
        />
        {!(recording && videoActive) && (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-gradient-to-br from-slate-800 via-slate-900 to-black text-slate-600">
            <span className="text-3xl">🎥</span>
            <span className="text-sm">
              {recording ? "无摄像头（纯音频字幕模式）" : "点击下方“开始采集并推流”开启摄像头直播"}
            </span>
            <span className="text-xs text-slate-700">Web Audio · 16kHz PCM · 3s 切片</span>
          </div>
        )}

        <div className="absolute left-4 top-4 flex items-center gap-2">
          {recording ? (
            <span className="inline-flex items-center gap-1.5 rounded-full bg-red-600/90 px-3 py-1 text-xs font-semibold text-white">
              <span className="live-dot h-2 w-2 rounded-full bg-white" /> LIVE
            </span>
          ) : (
            <span className="rounded-full bg-slate-700/80 px-3 py-1 text-xs text-slate-300">
              未开播
            </span>
          )}
        </div>
        <SubtitleOverlay
          partial={subtitles.currentPartial}
          lastFinal={lastFinal}
          targetLang={targetLang}
        />
      </div>

      <div className="mb-6 flex flex-wrap items-center gap-3">
        {!recording ? (
          <button
            onClick={start}
            disabled={session?.status === "ended"}
            className="rounded-lg bg-brand-600 px-5 py-2.5 text-sm font-semibold text-white hover:bg-brand-500 disabled:opacity-40"
          >
            🎙 开始采集并推流
          </button>
        ) : (
          <button
            onClick={stopMic}
            className="rounded-lg bg-slate-700 px-5 py-2.5 text-sm font-semibold text-white hover:bg-slate-600"
          >
            ⏹ 停止采集
          </button>
        )}
        <button
          onClick={endSession}
          disabled={session?.status === "ended"}
          className="rounded-lg border border-red-800 px-5 py-2.5 text-sm font-semibold text-red-300 hover:bg-red-900/30 disabled:opacity-40"
        >
          结束直播
        </button>
        <Link
          to={`/watch/${id}`}
          className="rounded-lg border border-slate-700 px-5 py-2.5 text-sm text-slate-300 hover:border-slate-500"
        >
          打开观众视角 ↗
        </Link>
        <div className="ml-auto">
          <LatencyMeter latency={subtitles.lastLatency} />
        </div>
      </div>

      {error && <p className="mb-4 rounded-lg bg-red-900/30 px-3 py-2 text-sm text-red-300">{error}</p>}

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-5">
        <Metric label="已传分片" value={String(stat.chunks)} />
        <Metric label="传输中" value={String(stat.inFlight)} />
        <Metric label="失败" value={String(stat.failed)} danger={stat.failed > 0} />
        <Metric label="最近 seq" value={stat.lastSeq === null ? "—" : `#${stat.lastSeq}`} />
        <Metric label="上传字节" value={formatBytes(stat.bytes)} />
      </div>
    </div>
  );
}

function Metric({ label, value, danger }: { label: string; value: string; danger?: boolean }) {
  return (
    <div className="rounded-xl border border-slate-800 bg-slate-900/40 px-4 py-3">
      <p className="text-[11px] uppercase tracking-wide text-slate-500">{label}</p>
      <p className={`mt-1 text-lg font-semibold tabular-nums ${danger ? "text-red-400" : "text-white"}`}>
        {value}
      </p>
    </div>
  );
}

function formatBytes(value: number): string {
  if (value < 1024) return `${value}B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)}KB`;
  return `${(value / 1024 / 1024).toFixed(2)}MB`;
}
