"""ASR Worker 入口：FastAPI（健康检查/指标）+ 后台 Redis Stream 消费线程。"""
from __future__ import annotations

import logging
import threading
import time
from contextlib import asynccontextmanager

import psycopg
import redis
from fastapi import FastAPI

from .asr import build_asr
from .config import get_settings
from .consumer import Consumer
from .services import Database, EventBus, ObjectStorage
from .translate import build_translator
from .version import VERSION

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s [%(name)s] %(message)s",
)
logger = logging.getLogger("asr-worker")

settings = get_settings()
_state: dict = {}


def _connect_redis_with_retry(client: redis.Redis, timeout: float = 30.0) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            client.ping()
            return
        except redis.RedisError as exc:
            logger.info("waiting for redis: %s", exc)
            time.sleep(1.0)
    raise RuntimeError("redis not ready in time")


def _connect_postgres_with_retry(timeout: float = 30.0) -> Database:
    deadline = time.time() + timeout
    last_exc: Exception | None = None
    while time.time() < deadline:
        try:
            return Database(settings)
        except psycopg.Error as exc:
            last_exc = exc
            logger.info("waiting for postgres: %s", exc)
            time.sleep(1.0)
    raise RuntimeError(f"postgres not ready in time: {last_exc}")


def _connect_storage_with_retry(timeout: float = 40.0) -> ObjectStorage:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            store = ObjectStorage(settings)
            store.client.head_bucket(Bucket=settings.minio_bucket)
            return store
        except Exception as exc:
            logger.info("waiting for minio/bucket: %s", exc)
            time.sleep(2.0)
    raise RuntimeError("minio not ready in time")


@asynccontextmanager
async def lifespan(app: FastAPI):
    logger.info("worker %s booting version=%s (asr=%s, translate=%s)",
                settings.worker_id, VERSION, settings.asr_provider,
                settings.translate_provider)

    rdb = redis.Redis(
        host=settings.redis_addr.split(":")[0],
        port=int(settings.redis_addr.split(":")[1]) if ":" in settings.redis_addr else 6379,
        password=settings.redis_password or None,
        db=settings.redis_db,
        decode_responses=False,
    )
    _connect_redis_with_retry(rdb)
    database = _connect_postgres_with_retry()
    store = _connect_storage_with_retry()
    bus = EventBus(rdb, settings)

    asr = build_asr(settings)
    translator = build_translator(settings)

    consumer = Consumer(settings, rdb, store, database, bus, asr, translator)
    thread = threading.Thread(target=consumer.run_forever, name="stream-consumer", daemon=True)
    thread.start()

    _state.update(consumer=consumer, redis=rdb, db=database, bus=bus, asr=asr, thread=thread,
                  started_at=time.time())
    logger.info("worker ready")
    yield

    logger.info("worker shutting down")
    database.close()
    bus.close()


app = FastAPI(title="Live Subtitle ASR Worker", version="1.0.0", lifespan=lifespan)


@app.get("/health")
def health():
    rdb: redis.Redis | None = _state.get("redis")
    checks = {"status": "ok", "version": VERSION}
    if rdb is not None:
        try:
            checks["redis"] = "ok" if rdb.ping() else "down"
        except redis.RedisError:
            checks["redis"] = "down"
            checks["status"] = "degraded"
    consumer = _state.get("consumer")
    if consumer is not None:
        checks["stats"] = consumer.stats
    checks["workerId"] = settings.worker_id
    checks["asrProvider"] = settings.asr_provider
    return checks


@app.get("/metrics")
def metrics():
    consumer = _state.get("consumer")
    stats = consumer.stats if consumer else {}
    processed = int(stats.get("processed", 0))
    empty = int(stats.get("empty", 0))
    return {
        "version": VERSION,
        "workerId": settings.worker_id,
        "uptimeS": int(time.time() - _state.get("started_at", time.time())),
        "asr": settings.asr_provider,
        "translator": settings.translate_provider,
        "stats": stats,
        # 空 final 占比：接近 1 说明大量切片被判无语音（麦克风/降噪/VAD 问题）。
        "emptyRatio": round(empty / processed, 3) if processed else 0.0,
    }


@app.get("/selftest")
def vad_selftest():
    """不依赖麦克风/队列，直接验证当前进程的 VAD 是否为新版。

    用合成的低响度浊音（峰值约 0.005）与数字静音做判定：
    旧进程（RMS 0.01 硬阈值）会把轻声判空，新进程应判为有语音。
    部署后可直接 curl :8000/selftest 确认新代码已生效。
    """
    import numpy as np
    from .audio import has_speech

    sr = 16000
    n = sr
    rng = np.random.default_rng(0)
    t = np.arange(n) / sr
    phase = 2 * np.pi * np.cumsum(120 + 25 * np.sin(2 * np.pi * 1.3 * t)) / sr
    voiced = np.sin(phase) * 0.7 + 0.2 * np.sin(2 * phase)
    env = 0.4 + 0.6 * np.abs(np.sin(2 * np.pi * 2.0 * t))
    mic = voiced * env + 0.03 * rng.standard_normal(n)
    quiet = (mic / np.max(np.abs(mic)) * 0.005).astype(np.float32)  # 轻声
    silence = np.zeros(n, dtype=np.float32)

    quiet_voiced = bool(has_speech(quiet))
    silence_silent = not bool(has_speech(silence))
    ok = quiet_voiced and silence_silent
    return {
        "version": VERSION,
        "quietSpeechDetected": quiet_voiced,
        "silenceRejected": silence_silent,
        "quietPeak": float(np.max(np.abs(quiet))),
        "ok": ok,
    }
