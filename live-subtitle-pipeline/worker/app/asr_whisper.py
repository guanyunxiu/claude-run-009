"""faster-whisper（CTranslate2）转写后端。

仅在 ASR_PROVIDER=whisper 的镜像中可用（镜像内安装 faster-whisper 与 ffmpeg）。
模型在进程启动时加载一次，常驻内存，由 Redis 消费线程串行调用。
"""
from __future__ import annotations

import logging
import time

import numpy as np

from .models import Transcript

logger = logging.getLogger(__name__)


class WhisperASR:
    name = "whisper"

    def __init__(self, settings) -> None:
        try:
            from faster_whisper import WhisperModel
        except ImportError as exc:  # pragma: no cover
            raise RuntimeError(
                "ASR_PROVIDER=whisper requires faster-whisper; "
                "build the worker image with --build-arg ASR_PROVIDER=whisper"
            ) from exc

        logger.info(
            "loading faster-whisper model=%s device=%s compute=%s",
            settings.asr_model, settings.asr_device, settings.asr_compute_type,
        )
        started = time.time()
        self.model = WhisperModel(
            settings.asr_model,
            device=settings.asr_device,
            compute_type=settings.asr_compute_type,
        )
        self.default_language = settings.asr_language or None
        self.beam_size = settings.asr_beam_size
        self.partial_beam_size = settings.asr_partial_beam_size
        logger.info("model loaded in %.1fs", time.time() - started)

    def transcribe(self, pcm: np.ndarray, language: str, final: bool,
                   seq: int | None = None) -> Transcript:
        segments, info = self.model.transcribe(
            pcm,
            language=language or self.default_language,
            beam_size=self.beam_size if final else self.partial_beam_size,
            # 2-4 秒的切片本身很短，关闭 VAD 切分，整片一次性出结果，延迟最低。
            vad_filter=False,
            condition_on_previous_text=False,
            without_timestamps=True,
        )
        text = "".join(segment.text for segment in segments).strip()
        detected = language or info.language or self.default_language or "unknown"
        return Transcript(text=text, language=detected, confidence=float(info.avg_logprob or 0.0))
