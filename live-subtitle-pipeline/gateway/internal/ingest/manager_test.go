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
	n, err := SplitPCM(stream, 9000, func(_ int64, c Chunk) error {
		got = append(got, c)
		return nil
	})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("chunks=%d want 3", len(got))
	}
	if n != 3 {
		t.Fatalf("count=%d want 3", n)
	}
	// 本段相对序号从 0 起；anchor=9000，首片 start=9000、end=12000。
	if got[0].Seq != 0 || got[0].StartMs != 9000 || got[0].EndMs != 12000 {
		t.Fatalf("first chunk timing wrong: %+v", got[0])
	}
	if got[1].Seq != 1 {
		t.Fatalf("second local seq=%d want 1", got[1].Seq)
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
	_, err := SplitPCM(stream, 0, func(int64, Chunk) error { return stopErr })
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
	// 全局 seq = SeqBase + 本段相对序号，且时间戳从本段锚点起、单调。
	if up.chunks[0].Seq != SeqBase+0 || up.chunks[1].Seq != SeqBase+1 {
		t.Fatalf("global seq = %d,%d want %d,%d",
			up.chunks[0].Seq, up.chunks[1].Seq, SeqBase, SeqBase+1)
	}
	if up.chunks[1].StartMs < up.chunks[0].StartMs || up.chunks[0].StartMs < 0 {
		t.Fatalf("timestamps not monotonic from anchor: %+v", up.chunks)
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

// 直接验证 SplitPCM 时间轴：任意调用（模拟重连）都以当前 anchorMs 起、
// 本段相对序号计，绝不因历史累计片数而漂到未来。
func TestReconnectTimelineResetsToWallClock(t *testing.T) {
	// 第二次连接：假设之前已累计 1000 片。
	anchorMs := int64(1_000_000)
	stream := bytes.NewReader(make([]byte, ChunkBytes*2))
	var times []int64
	n, err := SplitPCM(stream, anchorMs, func(localSeq int64, c Chunk) error {
		times = append(times, c.StartMs)
		if localSeq >= 2 {
			t.Fatalf("localSeq should restart at 0 per connection, got %d", localSeq)
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Fatalf("split n=%d err=%v", n, err)
	}
	// 首片精确等于本段锚点；第二片 +3s。绝不能是 anchor + 1000*3000。
	if times[0] != anchorMs {
		t.Fatalf("first start=%d want anchor %d (no cumulative drift)", times[0], anchorMs)
	}
	if times[1] != anchorMs+3000 {
		t.Fatalf("second start=%d want %d", times[1], anchorMs+3000)
	}
}
