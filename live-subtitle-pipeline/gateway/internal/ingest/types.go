// Package ingest 负责从 RTMP/HLS（及预留 WebRTC/WHEP）等外部直播源
// 拉取音频、重采样为 16kHz 单声道 S16LE PCM，并按固定时长切片，
// 复用与浏览器采集完全相同的“上传分片 -> ASR -> 字幕”流水线。
package ingest

import (
	"context"
	"errors"
	"strings"
)

// SourceKind 拉流来源类型。
type SourceKind string

const (
	KindRTMP   SourceKind = "rtmp"
	KindHLS    SourceKind = "hls"
	KindWebRTC SourceKind = "webrtc"
)

// JobStatus 拉流任务状态。
type JobStatus string

const (
	StatusStarting     JobStatus = "starting"
	StatusRunning      JobStatus = "running"
	StatusReconnecting JobStatus = "reconnecting"
	StatusStopped      JobStatus = "stopped"
	StatusFailed       JobStatus = "failed"
)

// IsTerminal 终态：不再自动重连。
func (s JobStatus) IsTerminal() bool {
	return s == StatusStopped || s == StatusFailed
}

// Job 拉流任务的运行时视图。
type Job struct {
	ID        string     `json:"id"`
	SessionID string     `json:"sessionId"`
	Kind      SourceKind `json:"kind"`
	SourceURL string     `json:"sourceUrl"`
	Language  string     `json:"language"`
	Targets   []string   `json:"targets"`
	Status    JobStatus  `json:"status"`
	PID       int        `json:"pid,omitempty"`
	Error     string     `json:"error,omitempty"`
	Chunks    int64      `json:"chunks"`
	StartedAt int64      `json:"startedAt"`
	UpdatedAt int64      `json:"updatedAt"`
}

// Chunk 一个切好的 PCM 分片，语义与浏览器上传一致。
type Chunk struct {
	Seq     int64
	StartMs int64
	EndMs   int64
	PCM     []byte // 16kHz mono S16LE
}

// ChunkUploader 由网关实现：把拉流切片投递进同一条 ASR 流水线。
type ChunkUploader interface {
	UploadIngestChunk(ctx context.Context, sessionID string, seq, startMs, endMs int64, pcm []byte, source string) error
}

// 常见错误。
var (
	ErrJobNotFound      = errors.New("ingest job not found")
	ErrJobAlreadyActive = errors.New("an active ingest job already exists for this session")
	ErrUnsupportedKind  = errors.New("unsupported ingest source kind")
)

// SeqBase 拉流分片 seq 大基址：浏览器采集从 0 起，拉流从此基址起，
// 使两者可并存而不触发 (session_id, seq) 唯一冲突。
const SeqBase int64 = 1_000_000_000

// NormalizeKind 从 URL 推断/校验来源类型。
func NormalizeKind(kind, rawURL string) (SourceKind, error) {
	switch SourceKind(kind) {
	case KindRTMP, KindHLS, KindWebRTC:
		return SourceKind(kind), nil
	case "":
		switch {
		case strings.HasPrefix(rawURL, "rtmp://"), strings.HasPrefix(rawURL, "rtmps://"):
			return KindRTMP, nil
		case strings.HasSuffix(rawURL, ".m3u8") || strings.Contains(rawURL, "m3u8"):
			return KindHLS, nil
		default:
			return "", ErrUnsupportedKind
		}
	default:
		return "", ErrUnsupportedKind
	}
}
