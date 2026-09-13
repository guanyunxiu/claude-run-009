package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 网关运行配置，全部通过环境变量注入。
type Config struct {
	Addr          string
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	StreamName    string
	PubSubPrefix  string

	MinIOEndpoint  string
	MinIOAccessKey string
	MinIOSecretKey string
	MinIOBucket    string
	MinIOSecure    bool

	CORSOrigins  []string
	MaxAudioSize int64

	// WebSocket 握手允许的浏览器 Origin（scheme://host[:port]）；
	// "*" 放行全部（仅开发），空表示仅允许同源。
	WSAllowedOrigins []string

	// 全局运维 API Key（RTMP 拉流脚本 / 列会话）；为空则禁用相关全局接口。
	APIKey string

	ShutdownTimeout time.Duration
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func Load() Config {
	origins := getenv("CORS_ORIGINS", "*")
	wsOrigins := getenv("WS_ALLOWED_ORIGINS", "")
	if wsOrigins == "" {
		// 默认与 CORS 同源策略保持一致。
		wsOrigins = origins
	}
	return Config{
		Addr:          getenv("GATEWAY_ADDR", ":8080"),
		DatabaseURL:   getenv("DATABASE_URL", "postgres://subtitle:subtitle@localhost:5432/subtitles?sslmode=disable"),
		RedisAddr:     getenv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: os.Getenv("REDIS_PASSWORD"),
		RedisDB:       getenvInt("REDIS_DB", 0),
		StreamName:    getenv("REDIS_STREAM", "asr:tasks"),
		PubSubPrefix:  getenv("PUBSUB_PREFIX", "subtitles"),

		MinIOEndpoint:  getenv("MINIO_ENDPOINT", "localhost:9000"),
		MinIOAccessKey: getenv("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecretKey: getenv("MINIO_SECRET_KEY", "minioadmin"),
		MinIOBucket:    getenv("MINIO_BUCKET", "audio-chunks"),
		MinIOSecure:    getenv("MINIO_SECURE", "false") == "true",

		CORSOrigins:      strings.Split(origins, ","),
		WSAllowedOrigins: strings.Split(wsOrigins, ","),
		MaxAudioSize:     int64(getenvInt("MAX_AUDIO_BYTES", 1<<20)),

		APIKey: getenv("GATEWAY_API_KEY", ""),

		ShutdownTimeout: 10 * time.Second,
	}
}
