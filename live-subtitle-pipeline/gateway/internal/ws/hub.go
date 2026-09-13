package ws

import (
	"context"
	"encoding/json"
	"log"
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

// Hub 维护每个会话的 WebSocket 订阅者，并把 Redis Pub/Sub 的字幕
// （由 ASR worker 发布）扇出给本机连接。网关无状态、可水平扩展。
type Hub struct {
	rdb          *redis.Client
	pubsubPrefix string

	mu          sync.RWMutex
	subscribers map[string]map[*Client]struct{}
}

func NewHub(rdb *redis.Client, pubsubPrefix string) *Hub {
	return &Hub{
		rdb:          rdb,
		pubsubPrefix: pubsubPrefix,
		subscribers:  make(map[string]map[*Client]struct{}),
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
