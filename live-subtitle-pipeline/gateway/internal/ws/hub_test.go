package ws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/model"
	"github.com/livesub/gateway/internal/queue"
)

func newHubWithMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client, *Hub, context.CancelFunc) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hub := NewHub(rdb, "subtitles")
	ctx, cancel := context.WithCancel(context.Background())
	go hub.RunPubSub(ctx)
	t.Cleanup(func() {
		cancel()
		_ = rdb.Close()
		mr.Close()
	})
	return mr, rdb, hub, cancel
}

func newBufferedClient() *Client {
	return &Client{send: make(chan []byte, 16)}
}

// 验证 worker 经 Redis Pub/Sub 发布的字幕被扇出到同会话的所有连接，
// 而不会串到其它会话。
func TestHubFanoutFromPubSub(t *testing.T) {
	_, rdb, hub, _ := newHubWithMiniRedis(t)
	ctx := context.Background()

	alice := newBufferedClient()
	bob := newBufferedClient()
	other := newBufferedClient()
	hub.Subscribe("sess-1", alice)
	hub.Subscribe("sess-1", bob)
	hub.Subscribe("sess-2", other)

	// 等待后台 PSubscribe 完成订阅后再发布。
	deadline := time.Now().Add(2 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		probe, _ := json.Marshal(model.SubtitleMessage{Type: "subtitle", SessionID: "sess-1"})
		_ = rdb.Publish(ctx, queue.PubSubChannel("sess-1"), probe).Err()
		select {
		case <-alice.send:
			ready = true
		case <-time.After(20 * time.Millisecond):
		}
		if ready {
			break
		}
	}
	if !ready {
		t.Fatal("pubsub subscription never became ready")
	}
	// 清空就绪探测期间缓存到各客户端的消息。
	drain := func(ch chan []byte) {
		for {
			select {
			case <-ch:
			default:
				return
			}
		}
	}
	drain(alice.send)
	drain(bob.send)
	drain(other.send)

	payload, _ := json.Marshal(model.SubtitleMessage{
		Type: "subtitle", SessionID: "sess-1", Seq: 1, IsFinal: true, Text: "你好",
	})
	if err := rdb.Publish(ctx, queue.PubSubChannel("sess-1"), payload).Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}

	for name, ch := range map[string]chan []byte{"alice": alice.send, "bob": bob.send} {
		select {
		case got := <-ch:
			if string(got) != string(payload) {
				t.Errorf("%s got %s, want %s", name, got, payload)
			}
		case <-time.After(time.Second):
			t.Errorf("%s did not receive fanout message", name)
		}
	}
	select {
	case got := <-other.send:
		t.Errorf("other session should not receive, got %s", got)
	default:
	}
}

// 验证重连回放：ZSET 按 startMs 逆序写入后，Replay 返回时间正序。
func TestHubReplayOrderedAscending(t *testing.T) {
	_, rdb, hub, _ := newHubWithMiniRedis(t)
	ctx := context.Background()

	mk := func(seq int64, startMs int64, text string) string {
		b, _ := json.Marshal(model.SubtitleMessage{
			Type: "subtitle", SessionID: "sess-1", Seq: seq, StartMs: startMs,
			IsFinal: true, Text: text,
		})
		return string(b)
	}

	members := []redis.Z{
		{Score: 3000, Member: mk(3, 3000, "三")},
		{Score: 1000, Member: mk(1, 1000, "一")},
		{Score: 2000, Member: mk(2, 2000, "二")},
	}
	if err := rdb.ZAdd(ctx, queue.HotKey("sess-1"), members...).Err(); err != nil {
		t.Fatalf("zadd: %v", err)
	}

	replay, err := hub.Replay(ctx, "sess-1", 50)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(replay) != 3 {
		t.Fatalf("replay len = %d, want 3", len(replay))
	}
	wantOrder := []int64{1000, 2000, 3000}
	for i, raw := range replay {
		var msg model.SubtitleMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal replay item: %v", err)
		}
		if msg.StartMs != wantOrder[i] {
			t.Errorf("replay[%d] startMs = %d, want %d", i, msg.StartMs, wantOrder[i])
		}
	}
}
