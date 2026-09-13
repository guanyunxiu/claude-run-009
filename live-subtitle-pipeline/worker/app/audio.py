"""音频解码：把分片字节转换为 16kHz 单声道 float32 numpy 数组。"""
from __future__ import annotations

import io
import wave

import numpy as np


class AudioDecodeError(ValueError):
    pass


def decode_audio(data: bytes, content_type: str, sample_rate: int = 16000,
                 channels: int = 1) -> tuple[np.ndarray, int]:
    """返回 (float32[-1,1] 一维 PCM, 采样率)。"""
    ct = (content_type or "").lower().split(";")[0].strip()

    if ct in ("audio/wav", "audio/wave", "audio/x-wav") or data[:4] == b"RIFF":
        return _decode_wav(data)

    if ct in ("audio/pcm", "audio/l16", "application/octet-stream", ""):
        return _decode_pcm_s16le(data, sample_rate or 16000, channels), sample_rate or 16000

    if ct in ("audio/webm", "audio/ogg", "audio/mpeg", "audio/mp4", "audio/m4a"):
        return _decode_with_ffmpeg(data)

    # 兜底：先试 WAV 头，再按裸 PCM 处理。
    if data[:4] == b"RIFF":
        return _decode_wav(data)
    return _decode_pcm_s16le(data, sample_rate or 16000, channels), sample_rate or 16000


def _decode_pcm_s16le(data: bytes, sample_rate: int, channels: int) -> np.ndarray:
    # 浏览器 PCM 通常为 16-bit little-endian；长度不对齐时丢弃最后一个残缺采样。
    usable = (len(data) // 2) * 2
    if usable == 0:
        raise AudioDecodeError("empty PCM payload")
    pcm = np.frombuffer(data[:usable], dtype="<i2").astype(np.float32) / 32768.0
    if channels > 1:
        pcm = pcm.reshape(-1, channels).mean(axis=1)
    return np.ascontiguousarray(pcm, dtype=np.float32)


def _decode_wav(data: bytes) -> tuple[np.ndarray, int]:
    with wave.open(io.BytesIO(data), "rb") as wav:
        nchannels = wav.getnchannels()
        sampwidth = wav.getsampwidth()
        rate = wav.getframerate()
        frames = wav.readframes(wav.getnframes())

    if sampwidth == 2:
        pcm = np.frombuffer(frames, dtype="<i2").astype(np.float32) / 32768.0
    elif sampwidth == 1:
        pcm = (np.frombuffer(frames, dtype=np.uint8).astype(np.float32) - 128.0) / 128.0
    elif sampwidth == 4:
        pcm = np.frombuffer(frames, dtype="<i4").astype(np.float32) / 2147483648.0
    else:
        raise AudioDecodeError(f"unsupported wav sample width: {sampwidth}")

    if nchannels > 1:
        pcm = pcm.reshape(-1, nchannels).mean(axis=1)
    return np.ascontiguousarray(pcm, dtype=np.float32), rate


def _decode_with_ffmpeg(data: bytes) -> tuple[np.ndarray, int]:
    """压缩格式（webm-opus 等）通过 ffmpeg 可执行文件解码。

    完整 whisper 镜像自带 ffmpeg；stub 镜像默认不包含，此处给出明确错误。
    """
    try:
        import subprocess

        proc = subprocess.run(
            ["ffmpeg", "-hide_banner", "-loglevel", "error", "-i", "pipe:0",
             "-f", "s16le", "-ac", "1", "-ar", "16000", "pipe:1"],
            input=data, capture_output=True, check=True,
        )
    except FileNotFoundError as exc:
        raise AudioDecodeError(
            "compressed audio requires ffmpeg in worker image (use the whisper image)"
        ) from exc
    except Exception as exc:  # pragma: no cover - ffmpeg 运行时错误
        raise AudioDecodeError(f"ffmpeg decode failed: {exc}") from exc

    pcm = np.frombuffer(proc.stdout, dtype="<i2").astype(np.float32) / 32768.0
    return np.ascontiguousarray(pcm, dtype=np.float32), 16000


def is_silence(pcm: np.ndarray, threshold: float = 0.01) -> bool:
    """RMS 静音判断，用于跳过空分片的重计算。"""
    if pcm.size == 0:
       	return True
    return float(np.sqrt(np.mean(np.square(pcm)))) < threshold
