-- 直播会话
CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    source_language   VARCHAR(16) NOT NULL DEFAULT 'zh',
    target_languages  JSONB NOT NULL DEFAULT '[]',
    media_type    VARCHAR(32) NOT NULL DEFAULT 'audio/pcm',
    sample_rate   INTEGER NOT NULL DEFAULT 16000,
    channels      SMALLINT NOT NULL DEFAULT 1,
    status        VARCHAR(16) NOT NULL DEFAULT 'active',  -- active / ended
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_created_at ON sessions (created_at DESC);

-- 音频分片元数据（对象本体在 MinIO/S3）
CREATE TABLE IF NOT EXISTS audio_chunks (
    id           BIGSERIAL PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq          BIGINT NOT NULL,
    start_ms     BIGINT NOT NULL,
    end_ms       BIGINT NOT NULL,
    object_key   TEXT NOT NULL,
    size_bytes   INTEGER NOT NULL,
    content_type VARCHAR(64) NOT NULL DEFAULT 'audio/pcm',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_chunks_session_seq ON audio_chunks (session_id, seq);

-- 转写结果 / 字幕索引（只持久化 final；partial 仅实时推送）
CREATE TABLE IF NOT EXISTS subtitles (
    id           BIGSERIAL PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq          BIGINT NOT NULL,
    start_ms     BIGINT NOT NULL,
    end_ms       BIGINT NOT NULL,
    language     VARCHAR(16) NOT NULL,
    text         TEXT NOT NULL,
    translations JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_subtitles_session_seq ON subtitles (session_id, seq);
CREATE INDEX IF NOT EXISTS idx_subtitles_session_time ON subtitles (session_id, start_ms);
