package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/model"
)

// TaskQueue 基于 Redis Stream 的 ASR 任务队列。
type TaskQueue struct {
	rdb        *redis.Client
	streamName string
}

func New(rdb *redis.Client, streamName string) *TaskQueue {
	return &TaskQueue{rdb: rdb, streamName: streamName}
}

// Enqueue 将分片任务写入 Stream（近似持久化，worker 用消费组 + pending 保证至少一次）。
func (q *TaskQueue) Enqueue(ctx context.Context, task model.ChunkTask) (string, error) {
	payload, err := json.Marshal(task)
	if err != nil {
		return "", fmt.Errorf("marshal task: %w", err)
	}
	id, err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: q.streamName,
		Values: map[string]interface{}{"data": payload},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("xadd: %w", err)
	}
	return id, nil
}

// HotKey / PubSubChannel 与 worker 共享的 key 规范。
func HotKey(sessionID string) string { return fmt.Sprintf("subtitles:hot:%s", sessionID) }
func PubSubChannel(sessionID string) string {
	return fmt.Sprintf("subtitles:%s", sessionID)
}
