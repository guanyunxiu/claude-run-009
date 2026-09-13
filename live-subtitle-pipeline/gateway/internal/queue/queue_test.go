package queue

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/model"
)

func newMiniRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return mr, rdb
}

func TestEnqueueAndConsumeStreamTask(t *testing.T) {
	_, rdb := newMiniRedis(t)
	q := New(rdb, "asr:tasks")
	ctx := context.Background()

	task := model.ChunkTask{
		TaskID: "task-1", SessionID: "sess-1", Seq: 42,
		StartMs: 1000, EndMs: 4000, ObjectKey: "sess-1/000000000042.pcm",
		ContentType: "audio/pcm", SampleRate: 16000, Channels: 1,
		Language: "zh", Targets: []string{"en"}, EnqueuedMs: 1234,
	}

	id, err := q.Enqueue(ctx, task)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == "" {
		t.Fatal("empty stream id")
	}

	// 消费组路径与 worker 一致：建组 + XREADGROUP。
	if err := rdb.XGroupCreate(ctx, "asr:tasks", "asr-workers", "0").Err(); err != nil {
		t.Fatalf("xgroup create: %v", err)
	}
	streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group: "asr-workers", Consumer: "test-consumer",
		Streams: []string{"asr:tasks", ">"}, Count: 1, Block: 0,
	}).Result()
	if err != nil {
		t.Fatalf("xreadgroup: %v", err)
	}
	if len(streams) != 1 || len(streams[0].Messages) != 1 {
		t.Fatalf("unexpected stream result: %+v", streams)
	}

	var got model.ChunkTask
	if err := json.Unmarshal([]byte(streams[0].Messages[0].Values["data"].(string)), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TaskID != "task-1" || got.Seq != 42 || got.Language != "zh" || len(got.Targets) != 1 {
		t.Fatalf("task roundtrip mismatch: %+v", got)
	}
}

func TestKeyNaming(t *testing.T) {
	if got, want := HotKey("abc"), "subtitles:hot:abc"; got != want {
		t.Errorf("HotKey = %s, want %s", got, want)
	}
	if got, want := PubSubChannel("abc"), "subtitles:abc"; got != want {
		t.Errorf("PubSubChannel = %s, want %s", got, want)
	}
}
