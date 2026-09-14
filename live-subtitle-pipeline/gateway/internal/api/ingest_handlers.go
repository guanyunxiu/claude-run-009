package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/livesub/gateway/internal/ingest"
)

type createIngestRequest struct {
	Kind     string   `json:"kind"`
	Source   string   `json:"source"`
	Language string   `json:"language"`
	Targets  []string `json:"targets"`
}

// POST /sessions/:id/ingests — 为会话启动一个外部拉流任务（host 专属）。
func (s *Server) handleCreateIngest(ctx *gin.Context) {
	sessionID := ctx.Param("id")
	var req createIngestRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Source == "" {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "source url is required"})
		return
	}
	kind, err := ingest.NormalizeKind(req.Kind, req.Source)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// WebRTC/WHEP 服务端收流本轮未实现（数据模型已预留）：明确返回 400，
	// 不静默走 ffmpeg 去拉 ws。
	if kind == ingest.KindWebRTC {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": ingest.ErrWebRTCNotSupported.Error()})
		return
	}

	// 会话须存在且未结束。
	var status string
	if err := s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT status FROM sessions WHERE id=$1`, sessionID).Scan(&status); err != nil {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if status != "active" {
		ctx.JSON(http.StatusConflict, gin.H{"error": "session already ended"})
		return
	}

	language := req.Language
	if language == "" {
		_ = s.db.QueryRowContext(ctx.Request.Context(),
			`SELECT source_language FROM sessions WHERE id=$1`, sessionID).Scan(&language)
	}
	targets := req.Targets

	job := &ingest.Job{
		ID:        uuid.NewString(),
		SessionID: sessionID,
		Kind:      kind,
		SourceURL: req.Source,
		Language:  language,
		Targets:   targets,
		Status:    ingest.StatusStarting,
	}
	if s.ingests == nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "ingest subsystem disabled (ffmpeg unavailable?)"})
		return
	}
	if err := s.ingests.Start(ctx.Request.Context(), job); err != nil {
		if err == ingest.ErrJobAlreadyActive {
			ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusCreated, job)
}

// Persist 实现 ingest.Store：manager 每次状态变化都会回调落库。
func (s *Server) Persist(ctx context.Context, job *ingest.Job) error {
	if s.db == nil {
		return nil
	}
	// targets 是 JSONB，必须序列化为 JSON 文本，不能把 []string 直接交给驱动。
	targetsJSON, err := json.Marshal(job.Targets)
	if err != nil {
		return err
	}
	var pid any
	if job.PID > 0 {
		pid = job.PID
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO ingest_jobs (id, session_id, kind, source_url, language, targets, status, pid, error, chunks, started_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
		         to_timestamp($11/1000.0), to_timestamp($12/1000.0))
		 ON CONFLICT (id) DO UPDATE SET
		   status=EXCLUDED.status, pid=EXCLUDED.pid, error=EXCLUDED.error,
		   chunks=EXCLUDED.chunks, updated_at=EXCLUDED.updated_at,
		   stopped_at=CASE WHEN EXCLUDED.status IN ('stopped','failed')
		                   THEN COALESCE(ingest_jobs.stopped_at, now())
		                   ELSE ingest_jobs.stopped_at END`,
		job.ID, job.SessionID, string(job.Kind), job.SourceURL, job.Language,
		targetsJSON, string(job.Status), pid, job.Error, job.Chunks,
		job.StartedAt, job.UpdatedAt)
	return err
}

// GET /sessions/:id/ingests — 当前拉流任务状态（host/view 均可看）。
func (s *Server) handleListIngests(ctx *gin.Context) {
	sessionID := ctx.Param("id")
	if s.ingests == nil {
		ctx.JSON(http.StatusOK, gin.H{"jobs": []any{}})
		return
	}
	if job := s.ingests.Get(sessionID); job != nil {
		ctx.JSON(http.StatusOK, gin.H{"jobs": []*ingest.Job{job}})
		return
	}
	// 回退：查库中的历史/非本实例任务。
	rows, err := s.db.QueryContext(ctx.Request.Context(),
		`SELECT id, kind, source_url, language, status, COALESCE(pid,0), error, COALESCE(chunks,0),
		        EXTRACT(EPOCH FROM started_at)*1000, EXTRACT(EPOCH FROM updated_at)*1000
		 FROM ingest_jobs WHERE session_id=$1 ORDER BY started_at DESC LIMIT 20`, sessionID)
	if err != nil {
		// 不再静默吞错返回空：明确告知（通常是迁移未应用/旧库无该表）。
		ctx.JSON(http.StatusInternalServerError, gin.H{
			"error": "query ingest jobs failed (migration 0003 applied?): " + err.Error(),
			"jobs":  []any{},
		})
		return
	}
	defer rows.Close()
	jobs := make([]*ingest.Job, 0)
	for rows.Next() {
		j := &ingest.Job{}
		var kind, lang, st, src, errMsg string
		if err := rows.Scan(&j.ID, &kind, &src, &lang, &st, &j.PID, &errMsg, &j.Chunks,
			&j.StartedAt, &j.UpdatedAt); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		j.SessionID = sessionID
		j.Kind = ingest.SourceKind(kind)
		j.SourceURL = src
		j.Language = lang
		j.Status = ingest.JobStatus(st)
		j.Error = errMsg
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

// POST /sessions/:id/ingests/stop — 停止拉流（host 专属）。
func (s *Server) handleStopIngest(ctx *gin.Context) {
	sessionID := ctx.Param("id")
	if s.ingests == nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "ingest subsystem disabled"})
		return
	}
	if err := s.ingests.Stop(ctx.Request.Context(), sessionID); err != nil {
		if err == ingest.ErrJobNotFound {
			ctx.JSON(http.StatusNotFound, gin.H{"error": "no active ingest job"})
			return
		}
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{"status": "stopped"})
}
