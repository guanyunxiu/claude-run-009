package ingest

import (
	"context"
	"sync"
	"time"
)

// Store 持久化任务元数据（网关用 DB 实现；测试可用内存桩）。
type Store interface {
	Persist(ctx context.Context, job *Job) error
}

// noopStore 默认不持久化（仅内存态）。
type noopStore struct{}

func (noopStore) Persist(context.Context, *Job) error { return nil }

// Manager 管理每个会话最多一个活动拉流任务，负责断流自动重连。
type Manager struct {
	uploader  ChunkUploader
	store     Store
	newPuller PullerFactory
	config    Config

	mu   sync.Mutex
	jobs map[string]*managedJob
}

type managedJob struct {
	job    *Job
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.RWMutex // 保护 job 全部可变字段（status/chunks/pid/error/时间戳）
}

// Config Manager 可调参数。
type Config struct {
	MaxRestarts    int           // 单任务最大自动重连次数
	RestartBackoff time.Duration // 重连初始退避
}

func DefaultConfig() Config {
	return Config{MaxRestarts: 10, RestartBackoff: 2 * time.Second}
}

func NewManager(uploader ChunkUploader, store Store) *Manager {
	if store == nil {
		store = noopStore{}
	}
	return &Manager{
		uploader:  uploader,
		store:     store,
		newPuller: defaultPullerFactory,
		jobs:      make(map[string]*managedJob),
		config:    DefaultConfig(),
	}
}

// WithPullerFactory 注入自定义 Puller（测试用）。
func (m *Manager) WithPullerFactory(f PullerFactory) *Manager {
	m.newPuller = f
	return m
}

// Start 为会话启动拉流任务；已有活动任务返回 ErrJobAlreadyActive。
func (m *Manager) Start(ctx context.Context, j *Job) error {
	m.mu.Lock()
	if existing, ok := m.jobs[j.SessionID]; ok {
		existing.mu.RLock()
		active := !existing.job.Status.IsTerminal()
		existing.mu.RUnlock()
		if active {
			m.mu.Unlock()
			return ErrJobAlreadyActive
		}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	mj := &managedJob{
		job:    j,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	m.jobs[j.SessionID] = mj
	m.mu.Unlock()

	// 所有 job 字段写入都在 mj.mu 下，避免与 Get/run goroutine 竞争。
	now := nowMs()
	mj.mu.Lock()
	j.Status = StatusStarting
	j.StartedAt = now
	j.UpdatedAt = now
	mj.mu.Unlock()
	_ = m.store.Persist(ctx, snapshot(mj))

	go m.run(runCtx, mj)
	return nil
}

// snapshot 在持锁状态下复制一份 job，供持久化/返回，避免外部与运行态共享。
func snapshot(mj *managedJob) *Job {
	mj.mu.Lock()
	defer mj.mu.Unlock()
	cp := *mj.job
	return &cp
}

// Stop 停止会话的拉流任务。
func (m *Manager) Stop(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	mj, ok := m.jobs[sessionID]
	m.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}
	mj.cancel()
	mj.mu.Lock()
	mj.job.Status = StatusStopped
	mj.job.UpdatedAt = nowMs()
	mj.mu.Unlock()
	_ = m.store.Persist(ctx, snapshot(mj))
	select {
	case <-mj.done:
	case <-ctx.Done():
	}
	return nil
}

// Get 返回任务快照（副本），不存在返回 nil。
func (m *Manager) Get(sessionID string) *Job {
	m.mu.Lock()
	mj, ok := m.jobs[sessionID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	return snapshot(mj)
}

// run 单次任务的执行循环：拉流 -> 切片上传；进程退出则退避重连，直到上限/停止。
func (m *Manager) run(ctx context.Context, mj *managedJob) {
	defer close(mj.done)

	// 不支持的来源（如尚未实现的 WebRTC/WHEP）立即失败，不进入重连循环。
	if mj.job.Kind == KindWebRTC {
		m.setStatus(mj, StatusFailed, ErrWebRTCNotSupported.Error())
		return
	}

	backoff := m.config.RestartBackoff
	for attempt := 0; attempt <= m.config.MaxRestarts; attempt++ {
		if ctx.Err() != nil {
			return
		}
		if attempt > 0 {
			m.setStatus(mj, StatusReconnecting, "")
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		err := m.runOnce(ctx, mj)
		if ctx.Err() != nil {
			return // 主动停止
		}
		if err != nil {
			mj.mu.Lock()
			mj.job.Error = err.Error()
			mj.job.UpdatedAt = nowMs()
			mj.mu.Unlock()
			_ = m.store.Persist(context.Background(), snapshot(mj))
		}
	}

	m.setStatus(mj, StatusFailed, "max restarts exceeded")
}

// runOnce 启动一次 ffmpeg，读取 stdout 并切片上传；返回进程退出原因。
func (m *Manager) runOnce(ctx context.Context, mj *managedJob) error {
	puller := m.newPuller(mj.job.SourceURL, mj.job.Kind)
	stdout, err := puller.Start(ctx)
	if err != nil {
		return err
	}
	mj.mu.Lock()
	mj.job.Status = StatusRunning
	mj.job.PID = puller.PID()
	mj.job.Error = ""
	mj.job.UpdatedAt = nowMs()
	mj.mu.Unlock()
	_ = m.store.Persist(ctx, snapshot(mj))

	// 每次连接都用“当前墙钟”作为本段锚点；时间戳按本段相对序号计算，
	// 全局 seq 另加 Chunks 基数，避免重连后时间跳到未来。
	anchorMs := nowMs()
	mj.mu.RLock()
	baseChunks := mj.job.Chunks
	mj.mu.RUnlock()

	_, splitErr := SplitPCM(stdout, anchorMs, func(localSeq int64, c Chunk) error {
		globalSeq := SeqBase + baseChunks + localSeq // 与浏览器 seq 命名空间隔离
		if err := m.uploader.UploadIngestChunk(ctx, mj.job.SessionID, globalSeq, c.StartMs, c.EndMs, c.PCM, string(mj.job.Kind)); err != nil {
			return err
		}
		mj.mu.Lock()
		mj.job.Chunks = baseChunks + localSeq + 1
		mj.job.UpdatedAt = nowMs()
		mj.mu.Unlock()
		_ = m.store.Persist(context.Background(), snapshot(mj))
		return nil
	})
	_ = puller.Kill()
	waitErr := puller.Wait()
	if splitErr != nil {
		return splitErr
	}
	return waitErr
}

func (m *Manager) setStatus(mj *managedJob, status JobStatus, errMsg string) {
	mj.mu.Lock()
	mj.job.Status = status
	mj.job.Error = errMsg
	mj.job.UpdatedAt = nowMs()
	mj.mu.Unlock()
	_ = m.store.Persist(context.Background(), snapshot(mj))
}
