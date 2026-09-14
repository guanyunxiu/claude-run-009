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
// 这是纯函数（不依赖 ffmpeg）。每一段（一次 ffmpeg 连接）的时间戳都从
// anchorMs 起、按“本段内相对序号 0,1,2…”递增；调用方在回调中自行加上全局
// seq 基数。这样断流重连后 anchorMs 重置为当前墙钟，时间轴不会因累计 seq
// 而跳到未来。
//
// 返回本段切出的片数。回调 onChunk 返回错误则中止。
func SplitPCM(r io.Reader, anchorMs int64, onChunk func(localSeq int64, c Chunk) error) (int64, error) {
	buf := make([]byte, ChunkBytes)
	var localSeq int64

	for {
		// readFull 读到 EOF 时若有残留会返回 io.ErrUnexpectedEOF。
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			chunk := makeChunk(buf[:n], anchorMs, localSeq)
			if cbErr := onChunk(localSeq, chunk); cbErr != nil {
				return localSeq, cbErr
			}
			localSeq++
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return localSeq, nil
		}
		if err != nil {
			return localSeq, err
		}
	}
}

func makeChunk(pcm []byte, anchorMs, localSeq int64) Chunk {
	startMs := anchorMs + localSeq*ChunkSeconds*1000
	// 先乘后除，避免 1.5s 这类时长被整数整除截断。
	durationMs := int64(len(pcm)) * 1000 / (SampleRate * Channels * BytesSample)
	return Chunk{
		// Seq 字段此处填本段相对序号；全局 seq 由调用方（Manager）加基数。
		Seq:     localSeq,
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
