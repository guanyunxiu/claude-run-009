package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/config"
	"github.com/livesub/gateway/internal/model"
	"github.com/livesub/gateway/internal/queue"
	"github.com/livesub/gateway/internal/storage"
	"github.com/livesub/gateway/internal/ws"
)

type Server struct {
	cfg    config.Config
	db     *sql.DB
	store  *storage.ObjectStore
	tasks  *queue.TaskQueue
	rdb    *redis.Client
	hub    *ws.Hub
	router *gin.Engine
}

func NewServer(cfg config.Config, database *sql.DB, rdb *redis.Client, store *storage.ObjectStore, hub *ws.Hub) *Server {
	s := &Server{
		cfg:   cfg,
		db:    database,
		rdb:   rdb,
		store: store,
		tasks: queue.New(rdb, cfg.StreamName),
		hub:   hub,
	}

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	allowAll := contains(cfg.CORSOrigins, "*")
	corsConfig := cors.Config{
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Origin", "Content-Type", "Authorization"},
	}
	if allowAll {
		// 通配模式：只能开 AllowAllOrigins，不能同时给 AllowOrigins 或凭证。
		corsConfig.AllowAllOrigins = true
	} else {
		corsConfig.AllowOrigins = cfg.CORSOrigins
		corsConfig.AllowCredentials = true
	}
	router.Use(cors.New(corsConfig))

	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", s.handleHealth)
		v1.GET("/time", s.handleServerTime)
		v1.POST("/sessions", s.handleCreateSession)
		v1.GET("/sessions", s.handleListSessions)
		v1.GET("/sessions/:id", s.handleGetSession)
		v1.POST("/sessions/:id/end", s.handleEndSession)
		v1.POST("/sessions/:id/chunks", s.handleUploadChunk)
		v1.GET("/sessions/:id/subtitles", s.handleListSubtitles)
		v1.GET("/sessions/:id/subtitles/ws", hub.HandleWS)
	}

	s.router = router
	return s
}

func (s *Server) Router() *gin.Engine { return s.router }

func (s *Server) handleHealth(ctx *gin.Context) {
	resp := map[string]string{"status": "ok"}

	if err := s.db.PingContext(ctx.Request.Context()); err != nil {
		resp["postgres"] = "down"
	} else {
		resp["postgres"] = "ok"
	}
	if err := s.rdb.Ping(ctx.Request.Context()).Err(); err != nil {
		resp["redis"] = "down"
	} else {
		resp["redis"] = "ok"
	}

	status := http.StatusOK
	if resp["postgres"] == "down" || resp["redis"] == "down" {
		status = http.StatusServiceUnavailable
	}
	ctx.JSON(status, resp)
}

func (s *Server) handleServerTime(ctx *gin.Context) {
	now := time.Now()
	ctx.JSON(http.StatusOK, gin.H{
		"serverMs": now.UnixMilli(),
		"iso":      now.UTC().Format(time.RFC3339),
	})
}

type createSessionRequest struct {
	SourceLanguage  string   `json:"sourceLanguage"`
	TargetLanguages []string `json:"targetLanguages"`
	MediaType       string   `json:"mediaType"`
	SampleRate      int      `json:"sampleRate"`
	Channels        int      `json:"channels"`
}

func (s *Server) handleCreateSession(ctx *gin.Context) {
	req := createSessionRequest{
		SourceLanguage: "zh",
		MediaType:      "audio/pcm",
		SampleRate:     16000,
		Channels:       1,
	}
	if err := ctx.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SampleRate <= 0 {
		req.SampleRate = 16000
	}
	if req.Channels <= 0 {
		req.Channels = 1
	}

	sessionID := uuid.NewString()
	targetsJSON, _ := json.Marshal(normalizeTargets(req.TargetLanguages))

	_, err := s.db.ExecContext(ctx.Request.Context(),
		`INSERT INTO sessions (id, source_language, target_languages, media_type, sample_rate, channels)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, req.SourceLanguage, targetsJSON, req.MediaType, req.SampleRate, req.Channels)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusCreated, gin.H{
		"id":              sessionID,
		"sourceLanguage":  req.SourceLanguage,
		"targetLanguages": json.RawMessage(targetsJSON),
		"mediaType":       req.MediaType,
		"sampleRate":      req.SampleRate,
		"channels":        req.Channels,
		"status":          "active",
		"createdAt":       time.Now().UTC().Format(time.RFC3339),
		"wsUrl":           fmt.Sprintf("/api/v1/sessions/%s/subtitles/ws", sessionID),
	})
}

func (s *Server) handleListSessions(ctx *gin.Context) {
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx.Request.Context(),
		`SELECT id, source_language, target_languages, media_type, sample_rate, channels, status, created_at
		 FROM sessions ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	sessions := make([]gin.H, 0)
	for rows.Next() {
		var id, lang, mediaType, status string
		var targets []byte
		var sampleRate, channels int
		var createdAt time.Time
		if err := rows.Scan(&id, &lang, &targets, &mediaType, &sampleRate, &channels, &status, &createdAt); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		sessions = append(sessions, gin.H{
			"id": id, "sourceLanguage": lang, "targetLanguages": json.RawMessage(targets),
			"mediaType": mediaType, "sampleRate": sampleRate, "channels": channels,
			"status": status, "createdAt": createdAt.UTC().Format(time.RFC3339),
		})
	}
	ctx.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

func (s *Server) handleGetSession(ctx *gin.Context) {
	id := ctx.Param("id")
	var sourceLang, mediaType, status string
	var targets []byte
	var sampleRate, channels int
	var createdAt time.Time
	var endedAt sql.NullTime

	err := s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT source_language, target_languages, media_type, sample_rate, channels, status, created_at, ended_at
		 FROM sessions WHERE id=$1`, id).
		Scan(&sourceLang, &targets, &mediaType, &sampleRate, &channels, &status, &createdAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	resp := gin.H{
		"id": id, "sourceLanguage": sourceLang, "targetLanguages": json.RawMessage(targets),
		"mediaType": mediaType, "sampleRate": sampleRate, "channels": channels,
		"status": status, "createdAt": createdAt.UTC().Format(time.RFC3339),
		"wsUrl": fmt.Sprintf("/api/v1/sessions/%s/subtitles/ws", id),
	}
	if endedAt.Valid {
		resp["endedAt"] = endedAt.Time.UTC().Format(time.RFC3339)
	}
	ctx.JSON(http.StatusOK, resp)
}

func (s *Server) handleEndSession(ctx *gin.Context) {
	id := ctx.Param("id")
	tag, err := s.db.ExecContext(ctx.Request.Context(),
		`UPDATE sessions SET status='ended', ended_at=now() WHERE id=$1 AND status='active'`, id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows, _ := tag.RowsAffected(); rows == 0 {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "active session not found"})
		return
	}
	s.hub.PublishSessionEnd(ctx.Request.Context(), id)
	ctx.JSON(http.StatusOK, gin.H{"id": id, "status": "ended"})
}

// handleUploadChunk 接收 2-4 秒音频分片。
// 支持两种上传形式：
//  1. 原始字节：POST body 为裸 PCM/WAV 字节，元数据走 query/header；
//  2. multipart：字段 audio 为文件。
func (s *Server) handleUploadChunk(ctx *gin.Context) {
	sessionID := ctx.Param("id")

	seq, err := strconv.ParseInt(ctx.Query("seq"), 10, 64)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid query param seq"})
		return
	}
	startMs, err := strconv.ParseInt(ctx.Query("startMs"), 10, 64)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid query param startMs"})
		return
	}
	endMs, err := strconv.ParseInt(ctx.Query("endMs"), 10, 64)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "missing or invalid query param endMs"})
		return
	}
	if endMs <= startMs {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "endMs must be greater than startMs"})
		return
	}

	var session struct {
		Language   string
		Targets    []byte
		SampleRate int
		Channels   int
		MediaType  string
		Status     string
	}
	err = s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT source_language, target_languages, sample_rate, channels, media_type, status
		 FROM sessions WHERE id=$1`, sessionID).
		Scan(&session.Language, &session.Targets, &session.SampleRate, &session.Channels, &session.MediaType, &session.Status)
	if errors.Is(err, sql.ErrNoRows) {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if session.Status != "active" {
		ctx.JSON(http.StatusConflict, gin.H{"error": "session already ended"})
		return
	}

	// 幂等：同一 (sessionId, seq) 重复上传直接返回，不读 body / 不重复入队。
	var existingKey string
	findErr := s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT object_key FROM audio_chunks WHERE session_id=$1 AND seq=$2`, sessionID, seq).Scan(&existingKey)
	if findErr == nil {
		ctx.JSON(http.StatusOK, gin.H{"sessionId": sessionID, "seq": seq, "deduped": true, "objectKey": existingKey})
		return
	} else if !errors.Is(findErr, sql.ErrNoRows) {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": findErr.Error()})
		return
	}

	var reader io.Reader
	contentType := session.MediaType
	if contentType == "" {
		contentType = "audio/pcm"
	}

	if ctx.ContentType() == "multipart/form-data" {
		fileHeader, err := ctx.FormFile("audio")
		if err != nil {
			ctx.JSON(http.StatusBadRequest, gin.H{"error": "multipart field 'audio' is required"})
			return
		}
		if fileHeader.Size > s.cfg.MaxAudioSize {
			ctx.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "audio chunk too large"})
			return
		}
		file, err := fileHeader.Open()
		if err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer file.Close()
		reader = file
		if fileHeader.Header.Get("Content-Type") != "" {
			contentType = fileHeader.Header.Get("Content-Type")
		}
	} else {
		ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, s.cfg.MaxAudioSize)
		reader = ctx.Request.Body
		if ct := ctx.GetHeader("Content-Type"); ct != "" && ct != "application/octet-stream" {
			contentType = ct
		}
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "read audio body: " + err.Error()})
		return
	}
	if len(body) == 0 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "empty audio chunk"})
		return
	}

	// 幂等检查已在读取 body 之前完成；这里直接落对象存储。
	objectKey := fmt.Sprintf("%s/%012d.%s", sessionID, seq, extensionFor(contentType))
	if err := s.store.Put(ctx.Request.Context(), objectKey, contentType, bytes.NewReader(body), int64(len(body))); err != nil {
		ctx.JSON(http.StatusBadGateway, gin.H{"error": "object storage: " + err.Error()})
		return
	}

	var targets []string
	_ = json.Unmarshal(session.Targets, &targets)

	task := model.ChunkTask{
		TaskID:      uuid.NewString(),
		SessionID:   sessionID,
		Seq:         seq,
		StartMs:     startMs,
		EndMs:       endMs,
		ObjectKey:   objectKey,
		ContentType: contentType,
		SampleRate:  session.SampleRate,
		Channels:    session.Channels,
		Language:    session.Language,
		Targets:     targets,
		EnqueuedMs:  time.Now().UnixMilli(),
	}
	messageID, err := s.tasks.Enqueue(ctx.Request.Context(), task)
	if err != nil {
		ctx.JSON(http.StatusBadGateway, gin.H{"error": "enqueue task: " + err.Error()})
		return
	}

	_, err = s.db.ExecContext(ctx.Request.Context(),
		`INSERT INTO audio_chunks (session_id, seq, start_ms, end_ms, object_key, size_bytes, content_type)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (session_id, seq) DO NOTHING`,
		sessionID, seq, startMs, endMs, objectKey, len(body), contentType)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	ctx.JSON(http.StatusAccepted, gin.H{
		"sessionId": sessionID,
		"seq":       seq,
		"startMs":   startMs,
		"endMs":     endMs,
		"objectKey": objectKey,
		"bytes":     len(body),
		"streamId":  messageID,
		"status":    "queued",
	})
}

func (s *Server) handleListSubtitles(ctx *gin.Context) {
	sessionID := ctx.Param("id")
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	beforeSeq, _ := strconv.ParseInt(ctx.Query("beforeSeq"), 10, 64)

	query := `SELECT seq, start_ms, end_ms, language, text, translations, created_at
	          FROM subtitles WHERE session_id=$1`
	args := []any{sessionID}
	if beforeSeq > 0 {
		query += " AND seq < $2"
		args = append(args, beforeSeq)
	}
	query += fmt.Sprintf(" ORDER BY seq DESC LIMIT $%d", len(args)+1)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx.Request.Context(), query, args...)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer rows.Close()

	subtitles := make([]gin.H, 0, limit)
	for rows.Next() {
		var seq, startMs, endMs int64
		var language, text string
		var translations []byte
		var createdAt time.Time
		if err := rows.Scan(&seq, &startMs, &endMs, &language, &text, &translations, &createdAt); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		subtitles = append(subtitles, gin.H{
			"seq": seq, "startMs": startMs, "endMs": endMs,
			"isFinal": true, "language": language, "text": text,
			"translations": json.RawMessage(translations),
			"createdAt":    createdAt.UTC().Format(time.RFC3339Nano),
		})
	}
	// 逆序查出后翻正为 seq 升序。
	for i, j := 0, len(subtitles)-1; i < j; i, j = i+1, j-1 {
		subtitles[i], subtitles[j] = subtitles[j], subtitles[i]
	}
	ctx.JSON(http.StatusOK, gin.H{"sessionId": sessionID, "subtitles": subtitles})
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func normalizeTargets(targets []string) []string {
	if targets == nil {
		return []string{}
	}
	return targets
}

func extensionFor(contentType string) string {
	switch contentType {
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/webm":
		return "webm"
	case "audio/ogg":
		return "ogg"
	case "audio/mpeg":
		return "mp3"
	case "audio/pcm", "audio/l16":
		return "pcm"
	default:
		return "raw"
	}
}
