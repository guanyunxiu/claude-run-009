-- 外部直播源拉流任务（RTMP/HLS，预留 WebRTC/WHEP）。
-- 一个会话至多一个活动拉流任务；与浏览器采集可并存（共享同一字幕流）。
CREATE TABLE IF NOT EXISTS ingest_jobs (
    id            TEXT PRIMARY KEY,
    session_id    TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'rtmp',   -- rtmp | hls | webrtc
    source_url    TEXT NOT NULL,
    language      TEXT NOT NULL DEFAULT '',
    targets       JSONB NOT NULL DEFAULT '[]',
    status        TEXT NOT NULL DEFAULT 'starting', -- starting|running|reconnecting|stopped|failed
    pid           INTEGER,                          -- 拉流子进程 PID（运行态回填）
    error         TEXT NOT NULL DEFAULT '',
    chunks        BIGINT NOT NULL DEFAULT 0,
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_ingest_jobs_session ON ingest_jobs (session_id, created_at DESC);
-- 同一会话同一时刻只允许一个非终态（starting/running/reconnecting）任务。
CREATE UNIQUE INDEX IF NOT EXISTS idx_ingest_jobs_one_active
    ON ingest_jobs (session_id)
    WHERE status IN ('starting', 'running', 'reconnecting');

-- 字幕表增强：来源、说话人、句段合并与时间轴。
ALTER TABLE subtitles ADD COLUMN IF NOT EXISTS source        TEXT NOT NULL DEFAULT 'browser';
ALTER TABLE subtitles ADD COLUMN IF NOT EXISTS speaker       TEXT NOT NULL DEFAULT '';
ALTER TABLE subtitles ADD COLUMN IF NOT EXISTS merged_count  INTEGER NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_subtitles_session_start
    ON subtitles (session_id, start_ms);

-- 分片表记录来源（browser/rtmp/hls/webrtc）。拉流分片 seq 使用大基址，
-- 与浏览器采集（从 0 起）隔离，支持并存而不触发 (session_id, seq) 唯一冲突。
ALTER TABLE audio_chunks ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'browser';
