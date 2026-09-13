export interface SessionInfo {
  id: string;
  sourceLanguage: string;
  targetLanguages: string[];
  mediaType: string;
  sampleRate: number;
  channels: number;
  status: "active" | "ended";
  createdAt: string;
  endedAt?: string;
  wsUrl?: string;
}

export interface CreateSessionRequest {
  sourceLanguage?: string;
  targetLanguages?: string[];
  mediaType?: string;
  sampleRate?: number;
  channels?: number;
}

export interface UploadChunkResult {
  sessionId: string;
  seq: number;
  startMs: number;
  endMs: number;
  objectKey: string;
  bytes: number;
  streamId: string;
  status: string;
  deduped?: boolean;
}

export interface Subtitle {
  type?: "subtitle" | "session-end";
  sessionId: string;
  seq: number;
  startMs: number;
  endMs: number;
  isFinal: boolean;
  language: string;
  text: string;
  translations?: Record<string, string>;
  /** 队列排队耗时（毫秒） */
  queueMs?: number;
  /** ASR 识别耗时（毫秒） */
  asrMs?: number;
  /** 入队到推送的端到端耗时（毫秒） */
  e2eMs?: number;
  workerMs?: number;
  emittedMs?: number;
}

export interface ServerTime {
  serverMs: number;
  iso: string;
}
