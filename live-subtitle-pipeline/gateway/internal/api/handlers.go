package api

import (
	"bytes"
	"context"
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

	"github.com/livesub/gateway/internal/auth"
	"github.com/livesub/gateway/internal/config"
	"github.com/livesub/gateway/internal/ingest"
	"github.com/livesub/gateway/internal/model"
	"github.com/livesub/gateway/internal/queue"
	"github.com/livesub/gateway/internal/storage"
	"github.com/livesub/gateway/internal/version"
	"github.com/livesub/gateway/internal/ws"
)

type Server struct {
	cfg     config.Config
	db      *sql.DB
	store   *storage.ObjectStore
	tasks   *queue.TaskQueue
	rdb     *redis.Client
	hub     *ws.Hub
	ingests *ingest.Manager
	router  *gin.Engine

	// tokenLookupOverride 仅供测试注入鉴权身份（避免依赖真实 PostgreSQL）。
	tokenLookupOverride auth.TokenLookup
}

// SetTokenLookupForTest 仅供测试：替换令牌反查实现。
func (s *Server) SetTokenLookupForTest(lookup auth.TokenLookup) {
	s.tokenLookupOverride = lookup
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

	s.buildRoutes()
	return s
}

// SetHub 注入 Hub（解决 Server 与 Hub 的构造循环：Server 实现
// ws.SessionStatusChecker，Hub 又需要在路由中处理 WS）。
func (s *Server) SetHub(hub *ws.Hub) {
	s.hub = hub
}

func (s *Server) requireHub() *ws.Hub {
	if s.hub == nil {
		// 正常启动顺序下不会发生：main 在 ListenAndServe 前完成注入。
		panic("ws.Hub not injected")
	}
	return s.hub
}

// SetIngestManager 注入外部拉流管理器（main 在对象存储就绪后创建）。
func (s *Server) SetIngestManager(m *ingest.Manager) {
	s.ingests = m
}

func (s *Server) buildRoutes() {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
	allowAll := contains(s.cfg.CORSOrigins, "*")
	corsConfig := cors.Config{
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		AllowHeaders: []string{"Origin", "Content-Type", "Authorization"},
	}
	if allowAll {
		// 通配模式：只能开 AllowAllOrigins，不能同时给 AllowOrigins 或凭证。
		corsConfig.AllowAllOrigins = true
	} else {
		corsConfig.AllowOrigins = s.cfg.CORSOrigins
		corsConfig.AllowCredentials = true
	}
	router.Use(cors.New(corsConfig))

	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", s.handleHealth)
		v1.GET("/time", s.handleServerTime)
		// 创建会话本身不鉴权：任何人都可开播，创建后返回仅自己可见的 hostToken。
		v1.POST("/sessions", s.handleCreateSession)

		// 列会话是全局运维接口：仅持有 GATEWAY_API_KEY 可访问（未配置则 404）。
		v1.GET("/sessions", auth.GlobalAPIKeyOnly(s.cfg.APIKey), s.handleListSessions)

		// 会话读路径：声明最低角色 view（host 天然满足 view）。
		// 注意：不要同时传 RoleHost——host 已在 roleSatisfies 内被赋予 view 权限，
		// 同时声明两者会导致 wantHost=true 而误拒 view（曾造成观众 403）。
		sess := v1.Group("/sessions/:id",
			auth.Middleware(s, s.cfg.APIKey, true, auth.RoleView))
		{
			sess.GET("", s.handleGetSession)
			sess.GET("/subtitles", s.handleListSubtitles)
			// 外部拉流任务状态（host/view 均可看）。
			sess.GET("/ingests", s.handleListIngests)
			// 流水线探针：最近分片/字幕时间与队列积压，供前端在“无字幕”时提示原因。
			sess.GET("/pipeline-status", s.handlePipelineStatus)
			// WebSocket：浏览器无法设置请求头，允许 query token（仅读）。
			// Origin 在 Hub.HandleWS 内做白名单/同源校验。
			sess.GET("/subtitles/ws", func(c *gin.Context) {
				s.requireHub().HandleWS(c)
			})
		}

		// 主播专属：上传分片、结束会话（不接受 query token，只认 Authorization 头，
		// 防止令牌出现在 URL/历史记录中被滥用）。
		host := v1.Group("/sessions/:id",
			auth.Middleware(s, s.cfg.APIKey, false, auth.RoleHost))
		{
			host.POST("/end", s.handleEndSession)
			host.POST("/chunks", s.handleUploadChunk)
			// 外部直播源（RTMP/HLS）拉流任务：启动/停止（host 专属）。
			host.POST("/ingests", s.handleCreateIngest)
			host.POST("/ingests/stop", s.handleStopIngest)
		}
	}

	s.router = router
}

func (s *Server) Router() *gin.Engine { return s.router }

func (s *Server) handleHealth(ctx *gin.Context) {
	resp := map[string]any{
		"status":        "ok",
		"version":       version.Version,
		"migrations":    version.Migrations,
		"ingestEnabled": s.ingests != nil,
	}

	if s.db != nil {
		if err := s.db.PingContext(ctx.Request.Context()); err != nil {
			resp["postgres"] = "down"
		} else {
			resp["postgres"] = "ok"
			// 核对 ingest_jobs / 字幕增强列是否已随迁移落地（0003）。
			var hasIngestTable bool
			_ = s.db.QueryRowContext(ctx.Request.Context(),
				`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='ingest_jobs')`,
			).Scan(&hasIngestTable)
			resp["ingestTable"] = hasIngestTable
		}
	}
	if s.rdb != nil {
		if err := s.rdb.Ping(ctx.Request.Context()).Err(); err != nil {
			resp["redis"] = "down"
		} else {
			resp["redis"] = "ok"
		}
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
	hostToken, viewToken, err := auth.NewTokenPair()
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "generate tokens: " + err.Error()})
		return
	}
	targetsJSON, _ := json.Marshal(normalizeTargets(req.TargetLanguages))

	_, err = s.db.ExecContext(ctx.Request.Context(),
		`INSERT INTO sessions (id, source_language, target_languages, media_type, sample_rate, channels, host_token, view_token)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		sessionID, req.SourceLanguage, targetsJSON, req.MediaType, req.SampleRate, req.Channels,
		hostToken, viewToken)
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
		// 令牌仅在创建时返回一次；服务端不再回显（GET /sessions/:id 不含令牌）。
		"hostToken": hostToken,
		"viewToken": viewToken,
		"wsUrl":     fmt.Sprintf("/api/v1/sessions/%s/subtitles/ws", sessionID),
	})
}

// IdentityByToken 实现 auth.TokenLookup：按令牌反查会话身份。
func (s *Server) IdentityByToken(token string) (*auth.SessionIdentity, error) {
	if s.tokenLookupOverride != nil {
		return s.tokenLookupOverride.IdentityByToken(token)
	}
	var sessionID, role string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT id,
		        CASE
		          WHEN host_token = $1 THEN 'host'
		          WHEN view_token = $1 THEN 'view'
		          ELSE ''
		        END AS role
		 FROM sessions
		 WHERE host_token = $1 OR view_token = $1`, token).Scan(&sessionID, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if role == "" {
		return nil, nil
	}
	return &auth.SessionIdentity{SessionID: sessionID, Role: auth.Role(role)}, nil
}

// SessionStatus 实现 ws.SessionStatusChecker，供 WS 握手时判断会话是否已结束。
func (s *Server) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM sessions WHERE id=$1`, sessionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
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
	// 幂等：无论之前是 active 还是已结束都返回 200，避免主播端重试报错。
	tag, err := s.db.ExecContext(ctx.Request.Context(),
		`UPDATE sessions SET status='ended', ended_at=COALESCE(ended_at, now())
		 WHERE id=$1`, id)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows, _ := tag.RowsAffected(); rows == 0 {
		ctx.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if s.hub != nil {
		s.hub.PublishSessionEnd(ctx.Request.Context(), id)
	}
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

	// 先落元数据（幂等 ON CONFLICT），再入队：避免“入队成功但写库失败”后
	// 客户端重试造成重复入队。若这里写库失败，返回 5xx 让客户端重试即可，
	// 对象已上传、重试走最前面的幂等短路，不会产生重复任务。
	dbResult, err := s.db.ExecContext(ctx.Request.Context(),
		`INSERT INTO audio_chunks (session_id, seq, start_ms, end_ms, object_key, size_bytes, content_type, source)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'browser')
		 ON CONFLICT (session_id, seq) DO NOTHING`,
		sessionID, seq, startMs, endMs, objectKey, len(body), contentType)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if rows, _ := dbResult.RowsAffected(); rows == 0 {
		// 并发/重试导致已存在：幂等成功，不再重复入队。
		ctx.JSON(http.StatusOK, gin.H{
			"sessionId": sessionID, "seq": seq, "deduped": true, "objectKey": objectKey,
		})
		return
	}

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
		// 入队失败：标记该分片待补偿（删除元数据，使重试可重新入队），
		// 避免“有分片记录却永不处理”。
		_, _ = s.db.ExecContext(ctx.Request.Context(),
			`DELETE FROM audio_chunks WHERE session_id=$1 AND seq=$2 AND object_key=$3`,
			sessionID, seq, objectKey)
		ctx.JSON(http.StatusBadGateway, gin.H{"error": "enqueue task: " + err.Error()})
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
		"source":    "browser",
		"status":    "queued",
	})
}

// UploadIngestChunk 实现 ingest.ChunkUploader：外部拉流（RTMP/HLS；webrtc 预留）
// 切好的 PCM 走与浏览器完全相同的“对象存储 -> 元数据 -> Stream”路径。
func (s *Server) UploadIngestChunk(ctx context.Context, sessionID string, seq, startMs, endMs int64, pcm []byte, source string) error {
	if len(pcm) == 0 {
		return nil
	}
	contentType := "audio/pcm"
	objectKey := fmt.Sprintf("%s/%s-%012d.pcm", sessionID, source, seq)
	if err := s.store.Put(ctx, objectKey, contentType, bytes.NewReader(pcm), int64(len(pcm))); err != nil {
		return fmt.Errorf("object storage: %w", err)
	}

	// 幂等：同 (session, seq, objectKey) 已存在则跳过（断流重连重投）。
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM audio_chunks WHERE session_id=$1 AND seq=$2 AND object_key=$3`,
		sessionID, seq, objectKey).Scan(&exists); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	var targets []string
	var language string
	var rawTargets []byte
	qerr := s.db.QueryRowContext(ctx,
		`SELECT source_language, target_languages FROM sessions WHERE id=$1 AND status='active'`,
		sessionID).Scan(&language, &rawTargets)
	if errors.Is(qerr, sql.ErrNoRows) {
		return fmt.Errorf("session %s not active", sessionID)
	}
	if qerr != nil {
		return qerr
	}
	_ = json.Unmarshal(rawTargets, &targets)

	// 先落元数据（幂等：前置 exists 查询 + 唯一约束兜底），再入队，避免重复入队。
	dbResult, err := s.db.ExecContext(ctx,
		`INSERT INTO audio_chunks (session_id, seq, start_ms, end_ms, object_key, size_bytes, content_type, source)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (session_id, seq) DO NOTHING`,
		sessionID, seq, startMs, endMs, objectKey, len(pcm), contentType, source)
	if err != nil {
		return err
	}
	if rows, _ := dbResult.RowsAffected(); rows == 0 {
		return nil // 已存在，幂等
	}

	task := model.ChunkTask{
		TaskID:      uuid.NewString(),
		SessionID:   sessionID,
		Seq:         seq,
		StartMs:     startMs,
		EndMs:       endMs,
		ObjectKey:   objectKey,
		ContentType: contentType,
		SampleRate:  16000,
		Channels:    1,
		Language:    language,
		Targets:     targets,
		EnqueuedMs:  time.Now().UnixMilli(),
	}
	if _, err := s.tasks.Enqueue(ctx, task); err != nil {
		// 入队失败：回滚元数据以便重连/重试可重新处理。
		_, _ = s.db.ExecContext(ctx,
			`DELETE FROM audio_chunks WHERE session_id=$1 AND seq=$2 AND object_key=$3`,
			sessionID, seq, objectKey)
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

// handlePipelineStatus 返回会话流水线探针：
// 最近上传分片时间、最近落库字幕时间、任务队列积压，用于区分
// “WS 没连上”与“worker 没在出结果”。
func (s *Server) handlePipelineStatus(ctx *gin.Context) {
	sessionID := ctx.Param("id")

	var chunkCount int64
	if err := s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT count(*) FROM audio_chunks WHERE session_id=$1`, sessionID).Scan(&chunkCount); err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var lastChunkAt, lastSubAt sql.NullTime
	_ = s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT max(created_at) FROM audio_chunks WHERE session_id=$1`, sessionID).Scan(&lastChunkAt)
	_ = s.db.QueryRowContext(ctx.Request.Context(),
		`SELECT max(created_at) FROM subtitles WHERE session_id=$1`, sessionID).Scan(&lastSubAt)

	// 真实消费积压 = 消费组待处理（pending，已投递未 ACK）数量。
	// 不能用 XLEN：它是 Stream 历史总长度（默认 MAXLEN 未裁剪前一直增长），
	// lag 已为 0 时仍可能很大，会误报“积压很高”。
	var backlog int64
	res, err := s.rdb.XPending(ctx.Request.Context(), s.cfg.StreamName, s.cfg.ConsumerGroup).Result()
	if err == nil && res != nil {
		backlog = res.Count
	}
	streamLen, _ := s.rdb.XLen(ctx.Request.Context(), s.cfg.StreamName).Result()

	millis := func(t sql.NullTime) int64 {
		if !t.Valid {
			return 0
		}
		return t.Time.UnixMilli()
	}
	ctx.JSON(http.StatusOK, gin.H{
		"sessionId":      sessionID,
		"chunks":         chunkCount,
		"lastChunkMs":    millis(lastChunkAt),
		"lastSubtitleMs": millis(lastSubAt),
		// pending：已投递未 ACK 的真实积压；streamLen 仅作诊断参考。
		"streamBacklog": backlog,
		"streamLen":     streamLen,
		"serverMs":      time.Now().UnixMilli(),
	})
}

func (s *Server) handleListSubtitles(ctx *gin.Context) {
	sessionID := ctx.Param("id")
	limit, _ := strconv.Atoi(ctx.DefaultQuery("limit", "100"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	beforeSeq, _ := strconv.ParseInt(ctx.Query("beforeSeq"), 10, 64)
	fromMs, _ := strconv.ParseInt(ctx.Query("fromMs"), 10, 64)
	toMs, _ := strconv.ParseInt(ctx.Query("toMs"), 10, 64)

	query := `SELECT seq, start_ms, end_ms, language, text, translations, source, speaker, created_at
	          FROM subtitles WHERE session_id=$1`
	args := []any{sessionID}
	argN := 1
	if beforeSeq > 0 {
		argN++
		query += fmt.Sprintf(" AND seq < $%d", argN)
		args = append(args, beforeSeq)
	}
	if fromMs > 0 {
		argN++
		query += fmt.Sprintf(" AND end_ms >= $%d", argN)
		args = append(args, fromMs)
	}
	if toMs > 0 {
		argN++
		query += fmt.Sprintf(" AND start_ms <= $%d", argN)
		args = append(args, toMs)
	}
	scrub := fromMs > 0 || toMs > 0
	argN++
	if scrub {
		// 时间轴 scrub：按时间升序返回窗口内字幕。
		query += fmt.Sprintf(" ORDER BY start_ms ASC, seq ASC LIMIT $%d", argN)
	} else {
		// 默认：最近 N 条（倒序取，随后翻正）。
		query += fmt.Sprintf(" ORDER BY start_ms DESC, seq DESC LIMIT $%d", argN)
	}
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
		var language, text, source, speaker string
		var translations []byte
		var createdAt time.Time
		if err := rows.Scan(&seq, &startMs, &endMs, &language, &text, &translations,
			&source, &speaker, &createdAt); err != nil {
			ctx.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		item := gin.H{
			"seq": seq, "startMs": startMs, "endMs": endMs,
			"isFinal": true, "language": language, "text": text,
			"translations": json.RawMessage(translations),
			"source":       source,
			"createdAt":    createdAt.UTC().Format(time.RFC3339Nano),
		}
		if speaker != "" {
			item["speaker"] = speaker
		}
		subtitles = append(subtitles, item)
	}
	// 默认查询为倒序取最近 N 条，翻正为时间升序；scrub 窗口本就升序。
	if !scrub {
		for i, j := 0, len(subtitles)-1; i < j; i, j = i+1, j-1 {
			subtitles[i], subtitles[j] = subtitles[j], subtitles[i]
		}
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
