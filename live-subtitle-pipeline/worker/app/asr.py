"""ASR 提供者抽象与工厂。

- stub          : 离线确定性伪转写，零模型依赖，用于本地开发 / CI / 演示流水线
- whisper       : faster-whisper（CTranslate2，CPU/GPU）
- funasr/azure  : 预留扩展点（实现 BaseASR.transcribe 即可接入）
"""
from __future__ import annotations

import logging
from abc import ABC, abstractmethod

import numpy as np

from .config import Settings
from .models import Transcript

logger = logging.getLogger(__name__)


class BaseASR(ABC):
    """单语言转写器。final 与 partial 用不同解码代价区分延迟。"""

    name: str = "base"

    @abstractmethod
    def transcribe(self, pcm: np.ndarray, language: str, final: bool,
                   seq: int | None = None) -> Transcript:
        """对一段 16kHz 单声道 float32 PCM 做转写。

        final=True  使用 beam search 等高质量解码；
        final=False 使用贪心/低算力解码，输出 partial 结果。
        seq 仅用于 stub 等无状态提供者做确定性输出。
        """

    def close(self) -> None:  # pragma: no cover
        pass


def build_asr(settings: Settings) -> BaseASR:
    provider = settings.asr_provider.lower()
    if provider == "stub":
        from .asr_stub import StubASR
        return StubASR(settings)
    if provider == "whisper":
        from .asr_whisper import WhisperASR
        return WhisperASR(settings)
    if provider in ("funasr", "azure"):
        raise NotImplementedError(
            f"ASR provider '{provider}' is a reserved extension point; "
            f"implement BaseASR.transcribe in app/asr_{provider}.py"
        )
    raise ValueError(f"unknown ASR provider: {provider}")
