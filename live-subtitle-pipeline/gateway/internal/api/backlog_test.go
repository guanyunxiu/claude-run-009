package api

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newBacklogRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// 组不存在：两者皆 0、lagKnown=false（不应误报积压）。
func TestStreamBacklogNoGroup(t *testing.T) {
	c := newBacklogRedis(t)
	ctx := context.Background()
	c.XAdd(ctx, &redis.XAddArgs{Stream: "asr:tasks", Values: []string{"data", "x"}})
	pending, lag, known := streamBacklog(ctx, c, "asr:tasks", "asr-workers")
	if pending != 0 || lag != 0 || known {
		t.Fatalf("no group: pending=%d lag=%d known=%v want 0,0,false", pending, lag, known)
	}
}

// 关键回归：消息已 XADD 但消费组尚未读取 -> lag>0、pending=0。
// 旧实现只看 pending 会误报“积压 0”，掩盖 worker 没在消费。
func TestStreamBacklogLagWhenNobodyConsumes(t *testing.T) {
	c := newBacklogRedis(t)
	ctx := context.Background()
	// 直接在 miniredis 建组（id=0, mkstream），并投递 3 条。
	if err := c.XGroupCreateMkStream(ctx, "asr:tasks", "asr-workers", "0").Err(); err != nil {
		t.Fatalf("group create: %v", err)
	}
	for i := 0; i < 3; i++ {
		c.XAdd(ctx, &redis.XAddArgs{Stream: "asr:tasks", Values: []string{"data", "x"}})
	}

	pending, lag, known := streamBacklog(ctx, c, "asr:tasks", "asr-workers")
	if !known {
		t.Fatalf("group exists, lag should be known")
	}
	if lag < 3 {
		t.Fatalf("lag=%d want >=3 (unread messages), pending=%d", lag, pending)
	}
	if pending != 0 {
		t.Fatalf("nobody consumed yet, pending=%d want 0", pending)
	}
}

// 已投递未 ACK：pending>0。
func TestStreamBacklogPendingWhenDelivered(t *testing.T) {
	c := newBacklogRedis(t)
	ctx := context.Background()
	c.XGroupCreateMkStream(ctx, "asr:tasks", "asr-workers", "0")
	c.XAdd(ctx, &redis.XAddArgs{Stream: "asr:tasks", Values: []string{"data", "x"}})
	// 用 ">" 投递一条给消费者但不 ACK -> pending=1。
	res, err := c.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: "asr-workers", Consumer: "c1", Streams: []string{"asr:tasks", ">"}, Count: 1,
	}).Result()
	if err != nil || len(res) == 0 {
		t.Fatalf("xreadgroup: %v %v", err, res)
	}
	pending, _, known := streamBacklog(ctx, c, "asr:tasks", "asr-workers")
	if !known || pending != 1 {
		t.Fatalf("pending=%d known=%v want 1,true", pending, known)
	}
}
