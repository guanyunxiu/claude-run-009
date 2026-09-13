"""跨组件消息契约（与 Go 网关、前端保持一致的 JSON 字段名）。"""
from __future__ import annotations

from typing import Optional

from pydantic import BaseModel, Field


class ChunkTask(BaseModel):
    task_id: str = Field(alias="taskId")
    session_id: str = Field(alias="sessionId")
    seq: int
    start_ms: int = Field(alias="startMs")
    end_ms: int = Field(alias="endMs")
    object_key: str = Field(alias="objectKey")
    content_type: str = Field(default="audio/pcm", alias="contentType")
    sample_rate: int = Field(default=16000, alias="sampleRate")
    channels: int = 1
    language: str = ""
    targets: list[str] = Field(default_factory=list)
    enqueued_ms: int = Field(alias="enqueuedMs")

    model_config = {"populate_by_name": True}


class Transcript(BaseModel):
    text: str
    language: str
    confidence: Optional[float] = None


class SubtitlePayload(BaseModel):
    type: str = "subtitle"
    sessionId: str
    seq: int
    startMs: int
    endMs: int
    isFinal: bool
    language: str
    text: str
    translations: dict[str, str] = Field(default_factory=dict)
    queueMs: int = 0
    asrMs: int = 0
    e2eMs: int = 0
    workerMs: int = 0
    emittedMs: int
