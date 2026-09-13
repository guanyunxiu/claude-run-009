"""faster-whisper（CTranslate2）转写后端。

仅在 ASR_PROVIDER=whisper 的镜像中可用（镜像内安装 faster-whisper 与 ffmpeg）。
模型在进程启动时加载一次，常驻内存，由 Redis 消费线程串行调用。

空结果防护（修复“切到 whisper 识别结果被吞成空字幕”）：
  1. 主解码失败/返回空时，按 beam=1 贪心 + 放宽 VAD 重试；
  2. 仍为空再尝试默认 compute_type（模型若以 int8 加载在部分 CPU 上会异常）；
  3. 只在确实没有语音时才返回空文本；解码异常向上抛出，由 consumer 决定，
     绝不用空文本静默覆盖。
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
        self._WhisperModel = WhisperModel
        self.model_name = settings.asr_model
        self.device = settings.asr_device
        self.compute_type = settings.asr_compute_type
        self.model = self._load_model(self.compute_type)
        self.default_language = settings.asr_language or None
        self.beam_size = settings.asr_beam_size
        self.partial_beam_size = settings.asr_partial_beam_size
        logger.info("model loaded in %.1fs", time.time() - started)

    def _load_model(self, compute_type: str):
        return self._WhisperModel(
            self.model_name,
            device=self.device,
            compute_type=compute_type,
        )

    def transcribe(self, pcm: np.ndarray, language: str, final: bool,
                   seq: int | None = None) -> Transcript:
        pcm = np.ascontiguousarray(pcm, dtype=np.float32)
        lang = language or self.default_language
        beam = self.beam_size if final else self.partial_beam_size

        text, info = self._transcribe_once(pcm, lang, beam, vad=False)

        # 空结果回退链：先贪心解码，再开 VAD（部分切片能量分布导致整段被判静音）。
        if not text:
            logger.info("whisper empty result seq=%s final=%s beam=%d, retry greedy+vad",
                        seq, final, beam)
            text, info = self._transcribe_once(pcm, lang, 1, vad=True)
        if not text and self.compute_type not in ("default", "auto"):
            logger.warning("whisper still empty seq=%s, retrying with compute_type=default", seq)
            try:
                fallback_model = self._load_model("default")
                segments, info = fallback_model.transcribe(
                    pcm, language=lang, beam_size=1,
                    vad_filter=True, condition_on_previous_text=False,
                )
                text = "".join(s.text for s in segments).strip()
            except Exception:
                logger.exception("whisper fallback decode failed seq=%s", seq)

        detected = language or getattr(info, "language", None) or self.default_language or "unknown"
        confidence = float(getattr(info, "avg_logprob", 0.0) or 0.0)
        return Transcript(text=text, language=detected, confidence=confidence)

    def _transcribe_once(self, pcm: np.ndarray, lang, beam: int, vad: bool):
        # 立即物化生成器：faster-whisper 的真正解码发生在迭代时，
        # 不在这里消费就无法捕获解码异常（会被误当成“空字幕”）。
        segments, info = self.model.transcribe(
            pcm,
            language=lang,
            beam_size=beam,
            # 2-4 秒短切片默认关 VAD 整片出结果；回退时开 VAD 再试。
            vad_filter=vad,
            condition_on_previous_text=False,
            without_timestamps=True,
        )
        text = "".join(segment.text for segment in segments).strip()
        return text, info
