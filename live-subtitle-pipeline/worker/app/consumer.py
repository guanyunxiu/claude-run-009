"""Redis Stream 消费者：消费组 + pending 认领保证至少一次投递。

处理顺序：
  1. XREADGROUP 阻塞读取新分片任务
  2. 从 MinIO 拉取音频字节并解码
  3. 先跑低算力转写，推送 partial 字幕（仅 Pub/Sub，不持久化）
  4. 再跑高质量转写 + 翻译，推送 final 字幕（Pub/Sub + Redis ZSET + PostgreSQL）
  5. XACK 确认；崩溃未确认的消息由 XAUTOCLAIM 重投
"""
from __future__ import annotations

import logging
import time
from typing import Any

import redis

from .audio import AudioDecodeError, decode_audio, is_silence
from .config import Settings
from .models import ChunkTask, SubtitlePayload, Transcript
from .services import Database, EventBus, ObjectStorage
logger = logging.getLogger(__name__)

MIN_IDLE_MS = 30_000
CLAIM_COUNT = 10


class Consumer:
    def __init__(self, settings: Settings, rdb: redis.Redis, store: ObjectStorage,
                 database: Database, bus: EventBus, asr, translator) -> None:
        self.settings = settings
        self.redis = rdb
        self.store = store
        self.database = database
        self.bus = bus
        self.asr = asr
        self.translator = translator
        self.consumer_name = f"{settings.worker_id}-{int(time.time()) % 100000}"
        self.stats = {"processed": 0, "failed": 0, "partial": 0, "lastSeq": -1}

    def ensure_group(self) -> None:
        try:
            self.redis.xgroup_create(
                self.settings.redis_stream,
                self.settings.redis_group,
                id="0",
                mkstream=True,
            )
            logger.info("consumer group '%s' created", self.settings.redis_group)
        except redis.ResponseError as exc:
            if "BUSYGROUP" not in str(exc):
                raise
            logger.info("consumer group '%s' already exists", self.settings.redis_group)

    def run_forever(self) -> None:
        self.ensure_group()
        logger.info(
            "consumer %s listening stream=%s group=%s asr=%s translate=%s",
            self.consumer_name, self.settings.redis_stream, self.settings.redis_group,
            self.settings.asr_provider, self.settings.translate_provider,
        )

        last_claim = time.time()
        while True:
            try:
                # 周期性认领卡死超过 30s 的 pending 消息（worker 崩溃恢复）。
                now = time.time()
                if now - last_claim > MIN_IDLE_MS / 1000:
                    self._claim_stale()
                    last_claim = now

                resp = self.redis.xreadgroup(
                    groupname=self.settings.redis_group,
                    consumername=self.consumer_name,
                    streams={self.settings.redis_stream: ">"},
                    count=1,
                    block=self.settings.block_ms,
                )
                if not resp:
                    continue
                for _stream, messages in resp:
                    for message_id, fields in messages:
                        self._handle(message_id, fields)
            except redis.RedisError as exc:
                logger.warning("redis error, retry in 2s: %s", exc)
                time.sleep(2)
            except Exception:
                logger.exception("unexpected consumer loop error")
                time.sleep(1)

    def _claim_stale(self) -> None:
        try:
            _, claimed, *_ = self.redis.xautoclaim(
                self.settings.redis_stream,
                self.settings.redis_group,
                self.consumer_name,
                min_idle_time=MIN_IDLE_MS,
                start_id="0",
                count=CLAIM_COUNT,
            )
            for message_id, fields in claimed:
                logger.warning("reclaimed stale message %s", message_id)
                self._handle(message_id, fields)
        except redis.RedisError as exc:
            logger.debug("xautoclaim skipped: %s", exc)

    def _handle(self, message_id: str, fields: dict[Any, Any]) -> None:
        raw = fields.get(b"data", fields.get("data"))
        if isinstance(raw, bytes):
            raw = raw.decode("utf-8")
        try:
            task = ChunkTask.model_validate_json(raw)
        except Exception:
            logger.exception("invalid task payload in %s, dropping: %r", message_id, raw)
            self.redis.xack(self.settings.redis_stream, self.settings.redis_group, message_id)
            return

        started = time.time()
        try:
            self.process(task)
            self.redis.xack(self.settings.redis_stream, self.settings.redis_group, message_id)
            self.stats["processed"] += 1
            self.stats["lastSeq"] = task.seq
            logger.info(
                "processed session=%s seq=%d in %.0fms (ack %s)",
                task.session_id, task.seq, (time.time() - started) * 1000, message_id,
            )
        except Exception:
            self.stats["failed"] += 1
            logger.exception("process failed session=%s seq=%s", task.session_id, task.seq)

    def process(self, task: ChunkTask) -> None:
        received_ms = time.time_ns() // 1_000_000
        queue_ms = max(0, received_ms - task.enqueued_ms)

        audio_bytes = self.store.get(task.object_key)
        pcm, _rate = decode_audio(
            audio_bytes, task.content_type, task.sample_rate, task.channels
        )

        # --- partial 通道：低算力转写，尽快上屏（不翻译、不落库）---
        if self.settings.enable_partial and not is_silence(pcm):
            partial_started = time.time()
            partial = self._transcribe_safe(pcm, task.language, final=False, seq=task.seq)
            partial_ms = int((time.time() - partial_started) * 1000)
            if partial.text.strip():
                emitted = time.time_ns() // 1_000_000
                self.bus.publish_subtitle(SubtitlePayload(
                    sessionId=task.session_id,
                    seq=task.seq,
                    startMs=task.start_ms,
                    endMs=task.end_ms,
                    isFinal=False,
                    language=partial.language or task.language,
                    text=partial.text.strip(),
                    queueMs=queue_ms,
                    asrMs=partial_ms,
                    e2eMs=max(0, emitted - task.enqueued_ms),
                    workerMs=partial_ms,
                    emittedMs=emitted,
                ))
                self.stats["partial"] += 1

        # --- final 通道：高质量转写 + 翻译 + 持久化 ---
        final_started = time.time()
        final = self._transcribe_safe(pcm, task.language, final=True, seq=task.seq)
        asr_ms = int((time.time() - final_started) * 1000)

        translations: dict[str, str] = {}
        text = final.text.strip()
        if text and task.targets:
            translations = self.translator.translate(text, final.language, task.targets)

        emitted = time.time_ns() // 1_000_000
        payload = SubtitlePayload(
            sessionId=task.session_id,
            seq=task.seq,
            startMs=task.start_ms,
            endMs=task.end_ms,
            isFinal=True,
            language=final.language or task.language,
            text=text,
            translations=translations,
            queueMs=queue_ms,
            asrMs=asr_ms,
            e2eMs=max(0, emitted - task.enqueued_ms),
            workerMs=asr_ms,
            emittedMs=emitted,
        )

        # 先持久化再广播：REST/重连回放与实时推送看到的状态一致。
        self.database.upsert_subtitle(payload)
        self.bus.publish_subtitle(payload)

    def _transcribe_safe(self, pcm, language: str, final: bool, seq: int) -> Transcript:
        try:
            return self.asr.transcribe(pcm, language, final=final, seq=seq)
        except AudioDecodeError:
            raise
        except Exception as exc:
            logger.exception("ASR transcription failed seq=%s final=%s", seq, final)
            return Transcript(text="", language=language, confidence=0.0)
