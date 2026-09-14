package ingest

import (
	"encoding/binary"
	"io"
	"time"
)

// 与前端采集一致：16kHz 单声道 S16LE，3 秒一片。
const (
	SampleRate   = 16000
	Channels     = 1
	BytesSample  = 2
	ChunkSeconds = 3
	ChunkBytes   = SampleRate * Channels * BytesSample * ChunkSeconds
)

// SplitPCM 把连续 S16LE 字节流切成定长 Chunk。
//
// 这是纯函数（不依赖 ffmpeg），seq 从给定初值递增，时间戳基于 anchorMs：
//   - 常规片长度恰为 ChunkBytes；
//   - 流结束时不足一片的尾巴作为最后一片（仍上传）。
//
// 回调 onChunk 返回错误则中止（例如会话已结束）。
func SplitPCM(r io.Reader, anchorMs int64, seqStart int64, onChunk func(Chunk) error) (int64, error) {
	buf := make([]byte, ChunkBytes)
	seq := seqStart

	for {
		// readFull 读到 EOF 时若有残留会返回 io.ErrUnexpectedEOF。
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			chunk := makeChunk(buf[:n], anchorMs, seq)
			if cbErr := onChunk(chunk); cbErr != nil {
				return seq, cbErr
			}
			seq++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// ErrUnexpectedEOF 的残留已在上面处理；纯 EOF 时 n==0。
			if n == 0 {
				return seq, nil
			}
			return seq, nil
		}
		if err != nil {
			return seq, err
		}
	}
}

func makeChunk(pcm []byte, anchorMs, seq int64) Chunk {
	startMs := anchorMs + seq*ChunkSeconds*1000
	// 先乘后除，避免 1.5s 这类时长被整数整除截断。
	durationMs := int64(len(pcm)) * 1000 / (SampleRate * Channels * BytesSample)
	return Chunk{
		Seq:     seq,
		StartMs: startMs,
		EndMs:   startMs + durationMs,
		PCM:     append([]byte(nil), pcm...),
	}
}

// LevelRMS 估算一段 S16LE PCM 的 RMS（0~1），用于拉流侧健康度/调试，
// 与 worker 的语音活动判断解耦（worker 仍为权威）。
func LevelRMS(pcm []byte) float64 {
	n := len(pcm) / BytesSample
	if n == 0 {
		return 0
	}
	var sumSq float64
	for i := 0; i < n; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768.0
		sumSq += s * s
	}
	var rms float64
	if sumSq > 0 {
		rms = sqrtf(sumSq / float64(n))
	}
	return rms
}

func sqrtf(x float64) float64 {
	// 避免为单一用途引入 math（保持函数内聚）；牛顿迭代足够。
	z := x
	for i := 0; i < 12; i++ {
		z = (z + x/z) / 2
	}
	return z
}

// nowMs 当前毫秒时间戳。
func nowMs() int64 { return time.Now().UnixMilli() }
