"""持久化与消息出口：MinIO 取音频、PostgreSQL 写字幕、Redis 热字幕 + Pub/Sub。"""
from __future__ import annotations

import json
import logging
from typing import Any

import boto3
import psycopg
import redis
from botocore.client import Config
from botocore.exceptions import ClientError
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from .config import Settings
from .models import SubtitlePayload

logger = logging.getLogger(__name__)


class ObjectStorage:
    def __init__(self, settings: Settings) -> None:
        self.client = boto3.client(
            "s3",
            endpoint_url=(
                f"https://{settings.minio_endpoint}" if settings.minio_secure
                else f"http://{settings.minio_endpoint}"
            ),
            aws_access_key_id=settings.minio_access_key,
            aws_secret_access_key=settings.minio_secret_key,
            region_name="us-east-1",
            # MinIO/S3 兼容存储必须使用 path-style（<endpoint>/<bucket>/<key>）。
            config=Config(
                signature_version="s3v4",
                s3={"addressing_style": "path"},
            ),
        )
        self.bucket = settings.minio_bucket

    def get(self, key: str) -> bytes:
        try:
            obj = self.client.get_object(Bucket=self.bucket, Key=key)
            return obj["Body"].read()
        except ClientError as exc:
            raise RuntimeError(f"get object {key} from minio: {exc}") from exc


class Database:
    def __init__(self, settings: Settings) -> None:
        # psycopg3 接受 postgres:// 与 postgresql:// 两种 scheme。
        self.conn = psycopg.connect(settings.database_url, row_factory=dict_row, autocommit=True)

    def fetch_session(self, session_id: str) -> dict[str, Any] | None:
        with self.conn.cursor() as cur:
            cur.execute(
                "SELECT id, source_language, target_languages, sample_rate, channels, "
                "media_type, status FROM sessions WHERE id=%s",
                (session_id,),
            )
            return cur.fetchone()

    def upsert_subtitle(self, payload: SubtitlePayload) -> None:
        """幂等写入 final 字幕；重复投递（at-least-once）按 (session_id, seq) 合并，
        译文采用后者覆盖前者。"""
        with self.conn.cursor() as cur:
            cur.execute(
                """
                INSERT INTO subtitles (session_id, seq, start_ms, end_ms, language, text, translations)
                VALUES (%(session_id)s, %(seq)s, %(start_ms)s, %(end_ms)s, %(language)s, %(text)s, %(translations)s)
                ON CONFLICT (session_id, seq) DO UPDATE
                  SET text = EXCLUDED.text,
                      translations = subtitles.translations || EXCLUDED.translations
                """,
                {
                    "session_id": payload.sessionId,
                    "seq": payload.seq,
                    "start_ms": payload.startMs,
                    "end_ms": payload.endMs,
                    "language": payload.language,
                    "text": payload.text,
                    "translations": Jsonb(payload.translations or {}),
                },
            )

    def close(self) -> None:
        self.conn.close()


class EventBus:
    """Redis 出口：Pub/Sub 实时扇出 + ZSET 热字幕（重连 replay / REST 拉取）。"""

    def __init__(self, client: redis.Redis, settings: Settings) -> None:
        self.redis = client
        self.settings = settings

    def publish_subtitle(self, payload: SubtitlePayload) -> None:
        body = payload.model_dump_json()
        channel = self.settings.channel(payload.sessionId)
        pipe = self.redis.pipeline()
        pipe.publish(channel, body)
        if payload.isFinal:
            hot_key = self.settings.hot_key(payload.sessionId)
            member = json.dumps(payload.model_dump(), ensure_ascii=False)
            pipe.zadd(hot_key, {member: payload.startMs})
            # 只保留分数（startMs）最大的 N 条：删除排名 0 .. -N-1。
            pipe.zremrangebyrank(hot_key, 0, -(self.settings.hot_max_entries + 1))
            pipe.expire(hot_key, self.settings.hot_ttl_seconds)
        pipe.execute()

    def close(self) -> None:
        self.redis.close()
