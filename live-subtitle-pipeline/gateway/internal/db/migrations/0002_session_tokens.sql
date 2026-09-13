-- 会话级鉴权令牌（创建会话时由网关生成，仅服务端可见原文）。
-- host_token: 主播（可上传分片、结束会话、看字幕）
-- view_token: 观众（只能看字幕、挂 WebSocket）
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS host_token TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS view_token TEXT NOT NULL DEFAULT '';

-- 令牌列建唯一索引（函数索引，避免空串相互冲突）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_host_token
    ON sessions (host_token) WHERE host_token <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_view_token
    ON sessions (view_token) WHERE view_token <> '';
