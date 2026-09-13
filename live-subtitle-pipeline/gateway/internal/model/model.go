package model

// ChunkTask 投递到 Redis Stream 的 ASR 任务载荷。
// JSON 字段名同时是 Python worker 的契约，改动需两端同步。
type ChunkTask struct {
	TaskID      string   `json:"taskId"`
	SessionID   string   `json:"sessionId"`
	Seq         int64    `json:"seq"`
	StartMs     int64    `json:"startMs"`
	EndMs       int64    `json:"endMs"`
	ObjectKey   string   `json:"objectKey"`
	ContentType string   `json:"contentType"`
	SampleRate  int      `json:"sampleRate"`
	Channels    int      `json:"channels"`
	Language    string   `json:"language"`
	Targets     []string `json:"targets"`
	EnqueuedMs  int64    `json:"enqueuedMs"`
}

// SubtitleMessage worker 回写、网关推送给前端的字幕消息。
type SubtitleMessage struct {
	Type         string            `json:"type"` // subtitle
	SessionID    string            `json:"sessionId"`
	Seq          int64             `json:"seq"`
	StartMs      int64             `json:"startMs"`
	EndMs        int64             `json:"endMs"`
	IsFinal      bool              `json:"isFinal"`
	Language     string            `json:"language"`
	Text         string            `json:"text"`
	Translations map[string]string `json:"translations,omitempty"`
	QueueMs      int64             `json:"queueMs,omitempty"`
	ASRMs        int64             `json:"asrMs,omitempty"`
	E2EMs        int64             `json:"e2eMs,omitempty"`
	WorkerMs     int64             `json:"workerMs,omitempty"`
	EmittedMs    int64             `json:"emittedMs,omitempty"`
}

// SessionEndMessage 会话结束标记。
type SessionEndMessage struct {
	Type      string `json:"type"` // session-end
	SessionID string `json:"sessionId"`
	AtMs      int64  `json:"atMs"`
}
