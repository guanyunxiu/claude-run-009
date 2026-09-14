package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestSplitPCMChunksAndTiming(t *testing.T) {
	// 2 个完整片 + 半片尾巴。
	total := ChunkBytes*2 + ChunkBytes/2
	stream := bytes.NewReader(make([]byte, total))

	var got []Chunk
	next, err := SplitPCM(stream, 9000, 5, func(c Chunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("chunks=%d want 3", len(got))
	}
	if next != 8 {
		t.Fatalf("next seq=%d want 8", next)
	}
	// seq 从 5 起，anchor=9000；首片 start = 9000 + 5*3000 = 24000。
	if got[0].Seq != 5 || got[0].StartMs != 24000 || got[0].EndMs != 27000 {
		t.Fatalf("first chunk timing wrong: %+v", got[0])
	}
	if len(got[1].PCM) != ChunkBytes {
		t.Fatalf("second chunk bytes=%d", len(got[1].PCM))
	}
	if len(got[2].PCM) != ChunkBytes/2 {
		t.Fatalf("tail bytes=%d want %d", len(got[2].PCM), ChunkBytes/2)
	}
	if got[2].EndMs-got[2].StartMs != 1500 {
		t.Fatalf("tail duration = %d want 1500", got[2].EndMs-got[2].StartMs)
	}
}

func TestSplitPCMPropagatesCallbackError(t *testing.T) {
	stream := bytes.NewReader(make([]byte, ChunkBytes*2))
	stopErr := errors.New("session ended")
	_, err := SplitPCM(stream, 0, 0, func(Chunk) error { return stopErr })
	if !errors.Is(err, stopErr) {
		t.Fatalf("err=%v want stopErr", err)
	}
}

func TestLevelRMS(t *testing.T) {
	if got := LevelRMS(nil); got != 0 {
		t.Fatalf("empty rms=%v want 0", got)
	}
	// 满幅 S16LE（两个采样即可）。
	full := []byte{0xff, 0x7f, 0xff, 0x7f} // 32767 LE
	if rms := LevelRMS(full); rms < 0.99 {
		t.Fatalf("full-scale rms=%.3f want ~1", rms)
	}
}

// ---- Manager 用假 Puller 验证重连与停止 ----

type fakePuller struct {
	mu       sync.Mutex
	src      []byte
	failNext bool
	started  int
	pid      int
}

func (f *fakePuller) Start(ctx context.Context) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	f.pid = 1000 + f.started
	if f.failNext {
		f.failNext = false
		return nil, errors.New("simulated connection refused")
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), f.src...))), nil
}
func (f *fakePuller) Wait() error { return nil }
func (f *fakePuller) PID() int    { return f.pid }
func (f *fakePuller) Kill() error { return nil }

type fakeUploader struct {
	mu     sync.Mutex
	chunks []Chunk
	source string
}

func (u *fakeUploader) UploadIngestChunk(_ context.Context, _ string, seq, startMs, endMs int64, pcm []byte, source string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.source = source
	u.chunks = append(u.chunks, Chunk{Seq: seq, StartMs: startMs, EndMs: endMs, PCM: pcm})
	return nil
}

func newTestManager(t *testing.T, puller Puller, cfg Config) (*Manager, *fakeUploader) {
	t.Helper()
	up := &fakeUploader{}
	m := NewManager(up, nil)
	m.config = cfg
	m.WithPullerFactory(func(string, SourceKind) Puller { return puller })
	return m, up
}

func TestManagerStartsStreamsAndStops(t *testing.T) {
	src := make([]byte, ChunkBytes*2)
	fp := &fakePuller{src: src}
	m, up := newTestManager(t, fp, Config{MaxRestarts: 0, RestartBackoff: time.Millisecond})

	job := &Job{ID: "j1", SessionID: "s1", Kind: KindRTMP, SourceURL: "rtmp://x", Status: StatusStarting, Targets: []string{"en"}}
	if err := m.Start(context.Background(), job); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 等待两片上传完成。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		up.mu.Lock()
		n := len(up.chunks)
		up.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	up.mu.Lock()
	if len(up.chunks) != 2 {
		up.mu.Unlock()
		t.Fatalf("uploaded chunks=%d want 2", len(up.chunks))
	}
	if up.source != "rtmp" {
		t.Fatalf("source tag=%q want rtmp", up.source)
	}
	up.mu.Unlock()

	// EOF 后 MaxRestarts=0 会进入 failed/stopped；主动 Stop 应幂等可调用。
	_ = m.Stop(context.Background(), "s1")
}

func TestManagerReconnectsAfterFailure(t *testing.T) {
	src := make([]byte, ChunkBytes)
	fp := &fakePuller{src: src, failNext: true}
	m, _ := newTestManager(t, fp, Config{MaxRestarts: 2, RestartBackoff: time.Millisecond})

	job := &Job{ID: "j2", SessionID: "s2", Kind: KindHLS, SourceURL: "http://x/index.m3u8", Status: StatusStarting}
	if err := m.Start(context.Background(), job); err != nil {
		t.Fatalf("start: %v", err)
	}
	// 首次连接失败 + 退避后应再次尝试。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fp.mu.Lock()
		n := fp.started
		fp.mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fp.mu.Lock()
	if fp.started < 2 {
		fp.mu.Unlock()
		t.Fatalf("puller started %d times, expected reconnect (>=2)", fp.started)
	}
	fp.mu.Unlock()
	_ = m.Stop(context.Background(), "s2")
}

func TestManagerRejectsSecondActiveJob(t *testing.T) {
	src := bytes.Repeat([]byte{1}, ChunkBytes*100) // 持续有数据，保持 running
	fp := &fakePuller{src: src}
	m, _ := newTestManager(t, fp, DefaultConfig())
	job := &Job{ID: "j3", SessionID: "s3", Kind: KindRTMP, SourceURL: "rtmp://a", Status: StatusStarting}
	if err := m.Start(context.Background(), job); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	job2 := &Job{ID: "j4", SessionID: "s3", Kind: KindRTMP, SourceURL: "rtmp://b"}
	if err := m.Start(context.Background(), job2); !errors.Is(err, ErrJobAlreadyActive) {
		t.Fatalf("second start err=%v want ErrJobAlreadyActive", err)
	}
	_ = m.Stop(context.Background(), "s3")
}
