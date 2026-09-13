import type {
  CreateSessionRequest,
  ServerTime,
  SessionInfo,
  Subtitle,
  UploadChunkResult,
} from "./types";

// 容器部署时由 nginx 同源反代，默认走相对路径；本地 vite dev 走代理。
export const API_BASE = import.meta.env.VITE_API_BASE ?? "";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const hasExplicitContentType = Object.keys(init?.headers ?? {}).some(
    (key) => key.toLowerCase() === "content-type",
  );
  const isBinaryBody =
    init?.body instanceof Blob ||
    init?.body instanceof ArrayBuffer ||
    ArrayBuffer.isView(init?.body as ArrayBufferView);

  const response = await fetch(`${API_BASE}${path}`, {
    ...init,
    headers: {
      // 二进制 body 由调用方显式指定 Content-Type（如 audio/pcm）；
      // 其余带 body 的请求默认 JSON。
      ...(init?.body && !isBinaryBody && !hasExplicitContentType
        ? { "Content-Type": "application/json" }
        : {}),
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const detail = await response.text();
    throw new Error(`HTTP ${response.status}: ${detail || response.statusText}`);
  }
  return (await response.json()) as T;
}

export const api = {
  serverTime: () => request<ServerTime>("/api/v1/time"),

  createSession: (body: CreateSessionRequest = {}) =>
    request<SessionInfo>("/api/v1/sessions", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  getSession: (id: string) => request<SessionInfo>(`/api/v1/sessions/${id}`),

  listSessions: (limit = 50) =>
    request<{ sessions: SessionInfo[] }>(`/api/v1/sessions?limit=${limit}`),

  endSession: (id: string) =>
    request<{ id: string; status: string }>(`/api/v1/sessions/${id}/end`, {
      method: "POST",
    }),

  listSubtitles: (id: string, limit = 100) =>
    request<{ sessionId: string; subtitles: Subtitle[] }>(
      `/api/v1/sessions/${id}/subtitles?limit=${limit}`,
    ),

  /**
   * 上传一个原始字节音频分片（PCM S16LE / WAV）。
   * seq/startMs/endMs 通过 query 传递，符合网关契约。
   */
  uploadChunk: (
    id: string,
    chunk: { seq: number; startMs: number; endMs: number },
    body: ArrayBuffer | Blob,
    contentType = "audio/pcm",
  ) =>
    request<UploadChunkResult>(
      `/api/v1/sessions/${id}/chunks?seq=${chunk.seq}&startMs=${chunk.startMs}&endMs=${chunk.endMs}`,
      {
        method: "POST",
        headers: { "Content-Type": contentType },
        body: body as Blob,
      },
    ),

  wsUrl: (id: string, replay = 20) => {
    const path = `/api/v1/sessions/${id}/subtitles/ws?replay=${replay}`;
    if (API_BASE.startsWith("http")) {
      return `${API_BASE.replace(/^http/, "ws")}${path}`;
    }
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${scheme}//${window.location.host}${path}`;
  },
};
