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
    """兼容旧调用名；实际语音活动判断见 has_speech。threshold 参数保留但忽略。"""
    return not has_speech(pcm)


def frame_rms(pcm: np.ndarray, frame_samples: int) -> np.ndarray:
    """把一维 PCM 切成定长帧，返回每帧 RMS（无重叠）。"""
    if pcm.size == 0:
        return np.empty(0, dtype=np.float32)
    n_frames = pcm.size // frame_samples
    if n_frames == 0:
        return np.array([float(np.sqrt(np.mean(np.square(pcm))))], dtype=np.float32)
    frames = pcm[: n_frames * frame_samples].reshape(n_frames, frame_samples)
    return np.sqrt(np.mean(np.square(frames), axis=1)).astype(np.float32)


def has_speech(
    pcm: np.ndarray,
    sample_rate: int = 16000,
    frame_ms: int = 30,
    rms_floor: float = 5.0e-4,
    min_voiced_frames: int = 3,
) -> bool:
    """判断切片是否含语音活动，专门覆盖“轻声说话 / 浏览器降噪压低响度”的场景。

    单一 RMS 阈值会把轻声人声误判为空，又会把稳态底噪误判为有声。这里组合：
      1. 帧能量：高于绝对地板 rms_floor（约 -66dBFS）的帧数足够多；
      2. 峰值：出现瞬态/持续响度（辅音、爆破）；
      3. 帧能量起伏：语音有音节强弱，稳态底噪各帧接近；
      4. 低频周期性：语音浊音在 70~400Hz 有显著能量，而白噪声平坦。
    满足帧计数后，峰值/起伏/周期性任一成立即判为有语音。
    """
    if pcm.size == 0:
        return False

    frame_len = max(1, int(sample_rate * frame_ms / 1000))
    energies = frame_rms(pcm, frame_len)
    if energies.size == 0:
        return False

    n_voiced = int(np.count_nonzero(energies > rms_floor))
    if n_voiced < min_voiced_frames:
        return False

    peak = float(np.max(np.abs(pcm)))
    rms = float(np.sqrt(np.mean(np.square(pcm)))) if pcm.size else 0.0
    median_e = float(np.median(energies)) + 1e-12
    variation = float(np.max(energies)) / median_e
    periodicity = _low_frequency_ratio(pcm, sample_rate)

    # 峰均比：辅音/爆破音有高尖峰，稳态高斯噪声约 3~5。
    crest = peak / (rms + 1e-12)
    # 1) 浊音：人声基频带能量集中（轻声说话也成立），白噪声该比值很低。
    voiced = periodicity >= 0.30 and peak >= 1.5e-3
    # 2) 瞬态辅音：峰均比高且帧能量有起伏。
    transient = crest >= 6.0 and variation >= 1.5 and peak >= 0.01
    # 3) 很响且带语音特征（周期或瞬态），避免把稳态大噪声当语音。
    loud = peak >= 0.03 and (periodicity >= 0.30 or crest >= 6.0)
    return voiced or transient or loud


def _low_frequency_ratio(pcm: np.ndarray, sample_rate: int) -> float:
    """估计 70~400Hz（人声基频带）能量占 70~2000Hz 能量的比例。

    浊音该比值明显偏高，白噪声能量平坦、比值低。为覆盖“切片前半静音、
    后半才说话”的情况，取整段中能量最高的 1 秒窗口做 FFT，而不是固定开头。
    """
    win_len = min(pcm.size, sample_rate)
    if win_len < 256:
        return 0.0

    if pcm.size > win_len:
        energies = frame_rms(pcm, win_len)
        # 取最响的整秒窗口；不足整秒的尾部不参与（避免静音尾部稀释）。
        start = int(np.argmax(energies)) * win_len
        win = pcm[start:start + win_len].astype(np.float32)
        if win.size < 256:
            win = pcm[:win_len].astype(np.float32)
    else:
        win = pcm.astype(np.float32)

    win = win - float(np.mean(win))
    spectrum = np.abs(np.fft.rfft(win * np.hanning(win.size)))
    freqs = np.fft.rfftfreq(win.size, d=1.0 / sample_rate)
    band_voice = (freqs >= 70) & (freqs <= 400)
    band_ref = (freqs >= 70) & (freqs <= 2000)
    e_voice = float(np.sum(spectrum[band_voice] ** 2))
    e_ref = float(np.sum(spectrum[band_ref] ** 2)) + 1e-12
    return e_voice / e_ref
