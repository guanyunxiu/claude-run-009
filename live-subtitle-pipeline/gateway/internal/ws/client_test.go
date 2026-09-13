package ws

import (
	"fmt"
	"sync"
	"testing"
)

// 并发 fanout + Unsubscribe 不得 panic（send on closed channel / close of closed channel）。
func TestClientDeliverAndUnsubscribeConcurrentSafe(t *testing.T) {
	mr, rdb, hub, _ := newHubWithMiniRedis(t)
	_ = mr
	_ = rdb

	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		client := newBufferedClient()
		// 每个 client 独占一个会话，避免互相取消订阅。
		sessionID := fmt.Sprintf("sess-%d", i)
		hub.Subscribe(sessionID, client)
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				hub.fanout(id, []byte("msg"))
			}
		}(sessionID)
		go func() {
			defer wg.Done()
			hub.Unsubscribe(sessionID, client)
			hub.Unsubscribe(sessionID, client) // 幂等
		}()
	}
	wg.Wait()
}

// 混合会话：大量 client 共享同一会话，fanout 与各自退订并发。
func TestSharedSessionFanoutConcurrentSafe(t *testing.T) {
	mr, rdb, hub, _ := newHubWithMiniRedis(t)
	_ = mr
	_ = rdb

	const n = 40
	clients := make([]*Client, n)
	for i := range clients {
		clients[i] = newBufferedClient()
		hub.Subscribe("shared", clients[i])
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			hub.fanout("shared", []byte("broadcast"))
		}
	}()
	go func() {
		defer wg.Done()
		for _, c := range clients {
			hub.Unsubscribe("shared", c)
		}
	}()
	wg.Wait()
}

func TestShutdownIdempotent(t *testing.T) {
	c := newBufferedClient()
	c.shutdown()
	c.shutdown() // 不得 panic
}

func TestDeliverAfterShutdownReturnsFalse(t *testing.T) {
	c := newBufferedClient()
	if !c.deliver([]byte("x")) {
		t.Fatal("deliver before shutdown should succeed")
	}
	c.shutdown()
	if c.deliver([]byte("y")) {
		t.Fatal("deliver after shutdown must return false")
	}
}
