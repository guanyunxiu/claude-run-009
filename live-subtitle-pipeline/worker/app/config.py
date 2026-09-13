"""ASR worker 配置：全部通过环境变量注入。"""
from functools import lru_cache

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_file=None, extra="ignore")

    # 服务
    worker_id: str = "worker-1"
    http_host: str = "0.0.0.0"
    http_port: int = 8000

    # Redis
    redis_addr: str = "localhost:6379"
    redis_password: str = ""
    redis_db: int = 0
    redis_stream: str = "asr:tasks"
    redis_group: str = "asr-workers"
    pubsub_prefix: str = "subtitles"
    hot_ttl_seconds: int = 3600
    hot_max_entries: int = 100
    block_ms: int = 5000

    # PostgreSQL
    database_url: str = "postgres://subtitle:subtitle@localhost:5432/subtitles"

    # MinIO / S3
    minio_endpoint: str = "localhost:9000"
    minio_access_key: str = "minioadmin"
    minio_secret_key: str = "minioadmin"
    minio_bucket: str = "audio-chunks"
    minio_secure: bool = False

    # ASR
    asr_provider: str = "stub"  # stub / whisper / funasr / azure
    asr_model: str = "small"
    asr_device: str = "cpu"
    asr_compute_type: str = "int8"
    asr_language: str = ""           # 空串 = 自动检测
    asr_beam_size: int = 5
    asr_partial_beam_size: int = 1   # partial 通道用贪心解码换取低延迟
    enable_partial: bool = True

    # 翻译
    translate_provider: str = "stub"  # stub / libretranslate
    libretranslate_url: str = ""

    @property
    def hot_key_prefix(self) -> str:
        return f"{self.pubsub_prefix}:hot"

    def hot_key(self, session_id: str) -> str:
        return f"{self.hot_key_prefix}:{session_id}"

    def channel(self, session_id: str) -> str:
        return f"{self.pubsub_prefix}:{session_id}"


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    return Settings()
