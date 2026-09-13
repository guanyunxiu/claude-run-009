package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/livesub/gateway/internal/api"
	"github.com/livesub/gateway/internal/config"
	"github.com/livesub/gateway/internal/db"
	"github.com/livesub/gateway/internal/storage"
	"github.com/livesub/gateway/internal/ws"
)

func main() {
	cfg := config.Load()
	log.Printf("gateway starting on %s", cfg.Addr)

	database, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer database.Close()

	if err := waitForDB(database, 30*time.Second); err != nil {
		log.Fatalf("database not ready: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("database migrated")

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := waitForRedis(rdb, 30*time.Second); err != nil {
		log.Fatalf("redis not ready: %v", err)
	}
	log.Println("redis connected")

	store, err := storage.New(cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey,
		cfg.MinIOBucket, cfg.MinIOSecure)
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	if err := waitForBucket(store, 30*time.Second); err != nil {
		log.Fatalf("minio not ready: %v", err)
	}
	log.Printf("object storage ready, bucket=%s", cfg.MinIOBucket)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := ws.NewHub(rdb, cfg.PubSubPrefix)
	go hub.RunPubSub(rootCtx)

	server := api.NewServer(cfg, database, rdb, store, hub)
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	_ = rdb.Close()
	log.Println("gateway stopped")
}

func waitForDB(database interface{ PingContext(context.Context) error }, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = database.PingContext(context.Background()); lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func waitForRedis(rdb *redis.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = rdb.Ping(context.Background()).Err(); lastErr == nil {
			return nil
		}
		time.Sleep(time.Second)
	}
	return lastErr
}

func waitForBucket(store *storage.ObjectStore, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = store.EnsureBucket(context.Background()); lastErr == nil {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return lastErr
}
