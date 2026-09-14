import type {
  CreateSessionRequest,
  PipelineStatus,
  ServerTime,
  SessionInfo,
  Subtitle,
  UploadChunkResult,
} from "./types";

// 容器部署时由 nginx 同源反代，默认走相对路径；本地 vite dev 走代理。
export const API_BASE = import.meta.env.VITE_API_BASE ?? "";

interface CallOptions {
  /** 会话级令牌（hostToken/viewToken），以 Authorization: Bearer 携带。 */
  token?: string;
}

function authHeaders(token?: string): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

async function request<T>(path: string, init?: RequestInit, opts?: CallOptions): Promise<T> {
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
      ...authHeaders(opts?.token),
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
    const error = new Error(`HTTP ${response.status}: ${detail || response.statusText}`) as Error & {
      status?: number;
    };
    error.status = response.status;
    throw error;
  }
  return (await response.json()) as T;
}

export const api = {
  serverTime: () => request<ServerTime>("/api/v1/time"),

  // 创建会话无需令牌；响应一次性返回 hostToken/viewToken。
  createSession: (body: CreateSessionRequest = {}) =>
    request<SessionInfo>("/api/v1/sessions", {
      method: "POST",
      body: JSON.stringify(body),
    }),

  getSession: (id: string, opts?: CallOptions) =>
    request<SessionInfo>(`/api/v1/sessions/${id}`, undefined, opts),

  // 全局运维接口：需 GATEWAY_API_KEY（普通前端不使用）。
  listSessions: (limit = 50, opts?: CallOptions) =>
    request<{ sessions: SessionInfo[] }>(`/api/v1/sessions?limit=${limit}`, undefined, opts),

  endSession: (id: string, opts?: CallOptions) =>
    request<{ id: string; status: string }>(`/api/v1/sessions/${id}/end`, {
      method: "POST",
    }, opts),

  listSubtitles: (id: string, limit = 100, opts?: CallOptions) =>
    request<{ sessionId: string; subtitles: Subtitle[] }>(
      `/api/v1/sessions/${id}/subtitles?limit=${limit}`,
      undefined,
      opts,
    ),

  pipelineStatus: (id: string, opts?: CallOptions) =>
    request<PipelineStatus>(
      `/api/v1/sessions/${id}/pipeline-status`,
      undefined,
      opts,
    ),

  /**
   * 上传一个原始字节音频分片（PCM S16LE / WAV）。
   * seq/startMs/endMs 通过 query 传递；主播令牌走 Authorization 头（不放 URL）。
   */
  uploadChunk: (
    id: string,
    chunk: { seq: number; startMs: number; endMs: number },
    body: ArrayBuffer | Blob,
    opts?: CallOptions & { contentType?: string },
  ) =>
    request<UploadChunkResult>(
      `/api/v1/sessions/${id}/chunks?seq=${chunk.seq}&startMs=${chunk.startMs}&endMs=${chunk.endMs}`,
      {
        method: "POST",
        headers: { "Content-Type": opts?.contentType ?? "audio/pcm" },
        body: body as Blob,
      },
      opts,
    ),

  /** WebSocket 地址：浏览器无法设置请求头，view/host 令牌通过 query 传递。 */
  wsUrl: (id: string, token: string, replay = 20) => {
    const path =
      `/api/v1/sessions/${id}/subtitles/ws?replay=${replay}` +
      `&token=${encodeURIComponent(token)}`;
    if (API_BASE.startsWith("http")) {
      return `${API_BASE.replace(/^http/, "ws")}${path}`;
    }
    const scheme = window.location.protocol === "https:" ? "wss:" : "ws:";
    return `${scheme}//${window.location.host}${path}`;
  },
};
