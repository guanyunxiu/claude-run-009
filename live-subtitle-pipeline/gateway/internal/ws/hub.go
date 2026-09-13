package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/model"
	"github.com/livesub/gateway/internal/queue"
)

const (
	sendBufferSize = 64
	writeWait      = 8 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 50 * time.Second
)

// SessionStatusChecker 查询会话当前状态（active/ended）。
type SessionStatusChecker interface {
	SessionStatus(ctx context.Context, sessionID string) (status string, err error)
}

// Hub 维护每个会话的 WebSocket 订阅者，并把 Redis Pub/Sub 的字幕
// （由 ASR worker 发布）扇出给本机连接。网关无状态、可水平扩展。
type Hub struct {
	rdb           *redis.Client
	pubsubPrefix  string
	allowedOrigin map[string]bool // 显式允许的 Origin（host:port）；为空表示同源放行
	statusChecker SessionStatusChecker

	mu          sync.RWMutex
	subscribers map[string]map[*Client]struct{}
}

// NewHub 创建 Hub。origins 为允许的浏览器 Origin 主机列表；
// 传 "*" 或空切片表示仅允许与 Host 同源的 Origin（拒绝跨站 WebSocket 握手）。
func NewHub(rdb *redis.Client, pubsubPrefix string, origins []string, checker SessionStatusChecker) *Hub {
	allowed := make(map[string]bool, len(origins))
	for _, origin := range origins {
		allowed[strings.ToLower(strings.TrimSpace(origin))] = true
	}
	return &Hub{
		rdb:           rdb,
		pubsubPrefix:  pubsubPrefix,
		allowedOrigin: allowed,
		statusChecker: checker,
		subscribers:   make(map[string]map[*Client]struct{}),
	}
}

func (h *Hub) Subscribe(sessionID string, client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[sessionID] == nil {
		h.subscribers[sessionID] = make(map[*Client]struct{})
	}
	h.subscribers[sessionID][client] = struct{}{}
}

func (h *Hub) Unsubscribe(sessionID string, client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subscribers[sessionID]
	if set == nil {
		return
	}
	// 仅在确实移除时关闭，避免慢消费者路径与 readPump 重复调用导致 close panic。
	if _, ok := set[client]; !ok {
		return
	}
	delete(set, client)
	client.shutdown()
	if len(set) == 0 {
		delete(h.subscribers, sessionID)
	}
}

// Publish 向某会话的 Pub/Sub 频道发布一条原始消息（如 session-end）。
func (h *Hub) Publish(ctx context.Context, sessionID string, payload []byte) error {
	return h.rdb.Publish(ctx, h.channel(sessionID), payload).Err()
}

func (h *Hub) PublishSessionEnd(ctx context.Context, sessionID string) {
	msg := model.SessionEndMessage{
		Type: "session-end", SessionID: sessionID, AtMs: time.Now().UnixMilli(),
	}
	payload, _ := json.Marshal(msg)
	if err := h.Publish(ctx, sessionID, payload); err != nil {
		log.Printf("publish session-end: %v", err)
	}
}

// Replay 返回 Redis 中该会话最近的 final 字幕（ZSET 按 startMs 排序），
// 供新接入 / 重连的客户端补齐上下文。
func (h *Hub) Replay(ctx context.Context, sessionID string, limit int64) ([][]byte, error) {
	if limit <= 0 {
		limit = 50
	}
	members, err := h.rdb.ZRevRange(ctx, queue.HotKey(sessionID), 0, limit-1).Result()
	if err != nil {
		return nil, err
	}
	out := make([][]byte, 0, len(members))
	// ZRevRange 是逆序，回放时翻正为时间顺序。
	for i := len(members) - 1; i >= 0; i-- {
		out = append(out, []byte(members[i]))
	}
	return out, nil
}

func (h *Hub) channel(sessionID string) string {
	return h.pubsubPrefix + ":" + sessionID
}

// originAllowed 校验 WebSocket 握手来源，拒绝浏览器里的跨站脚本。
//   - 无 Origin 头：非浏览器客户端（curl/服务端）放行；
//   - 配置了 "*"：显式放行全部（仅限开发，与 CORS 语义一致）；
//   - 配置了显式白名单：必须精确匹配 scheme://host[:port]；
//   - 默认：只允许与请求 Host 同源（scheme 跟随 X-Forwarded-Proto/请求 TLS）。
func (h *Hub) originAllowed(req *http.Request) bool {
	origin := req.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if h.allowedOrigin["*"] {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if h.allowedOrigin[strings.ToLower(origin)] {
		return true
	}
	// 同源：Origin 的 host 与 Host 头一致。
	if !strings.EqualFold(parsed.Host, req.Host) {
		return false
	}
	return true
}

// SessionStatus 透传给连接建立时的会话状态检查（handler 使用）。
func (h *Hub) SessionStatus(ctx context.Context, sessionID string) (string, error) {
	if h.statusChecker == nil {
		return "active", nil
	}
	return h.statusChecker.SessionStatus(ctx, sessionID)
}

// RunPubSub 订阅所有会话频道（模式订阅），把消息扇出给本地连接。
// 阻塞运行直到 ctx 取消，内部自动处理重订阅。
func (h *Hub) RunPubSub(ctx context.Context) {
	for {
		if err := h.relay(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("pubsub relay error, retry in 2s: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (h *Hub) relay(ctx context.Context) error {
	sub := h.rdb.PSubscribe(ctx, h.pubsubPrefix+":*")
	defer sub.Close()

	channel := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-channel:
			if !ok {
				return redis.ErrClosed
			}
			sessionID := strings.TrimPrefix(msg.Channel, h.pubsubPrefix+":")
			h.fanout(sessionID, []byte(msg.Payload))
		}
	}
}

func (h *Hub) fanout(sessionID string, payload []byte) {
	h.mu.RLock()
	clients := h.subscribers[sessionID]
	snapshot := make([]*Client, 0, len(clients))
	for client := range clients {
		snapshot = append(snapshot, client)
	}
	h.mu.RUnlock()

	slow := make([]*Client, 0)
	for _, client := range snapshot {
		if !client.deliver(payload) {
			// 缓冲已满或已关闭：收集起来，在锁外断开，让客户端重连后走 replay。
			slow = append(slow, client)
		}
	}
	if len(slow) > 0 {
		log.Printf("dropping %d slow subscriber(s) session=%s", len(slow), sessionID)
		for _, client := range slow {
			h.Unsubscribe(sessionID, client)
		}
	}
}
