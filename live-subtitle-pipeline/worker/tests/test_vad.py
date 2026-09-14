"""语音活动检测回归：覆盖“轻声说话 / 浏览器降噪压低响度”与稳态噪声/静音。"""
import numpy as np

from app.audio import has_speech, is_silence, frame_rms

SR = 16000


def _quiet_speech(amp: float, dur: float = 3.0, seed: int = 0) -> np.ndarray:
    """合成带基频抖动、音节包络和轻微底噪的低响度人声样信号。"""
    rng = np.random.default_rng(seed)
    n = int(SR * dur)
    t = np.arange(n) / SR
    f0 = 130 + 40 * np.sin(2 * np.pi * 1.5 * t)
    phase = 2 * np.pi * np.cumsum(f0) / SR
    src = np.sin(phase) * 0.6 + 0.3 * np.sin(2 * phase)
    env = 0.5 + 0.5 * np.abs(np.sin(2 * np.pi * (2.0 + rng.random()) * t))
    mic = src * env + 0.02 * rng.standard_normal(n)
    return (mic / np.max(np.abs(mic)) * amp).astype(np.float32)


def _white_noise(amp: float, dur: float = 3.0, seed: int = 1) -> np.ndarray:
    rng = np.random.default_rng(seed)
    return (rng.standard_normal(int(SR * dur)) * amp).astype(np.float32)


def test_digital_silence_is_not_speech():
    assert not has_speech(np.zeros(SR * 3, dtype=np.float32))
    assert is_silence(np.zeros(SR * 3, dtype=np.float32))


def test_very_quiet_speech_detected():
    # RMS 低至 ~0.0007（远低于旧阈值 0.01）的轻声必须被判为有语音。
    for amp in (0.0015, 0.002, 0.005, 0.01, 0.02, 0.1):
        x = _quiet_speech(amp)
        assert has_speech(x), f"quiet speech amp={amp} rms={np.sqrt(np.mean(x**2)):.5f} missed"


def test_steady_noise_rejected():
    # 各档稳态白噪声（普通环境底噪）不应被判成语音。
    for amp in (0.0002, 0.001, 0.003, 0.01):
        x = _white_noise(amp)
        assert not has_speech(x), f"steady noise amp={amp} false positive"
        assert is_silence(x), f"steady noise amp={amp} should be treated as silence"


def test_speech_in_second_half_detected():
    # 切片前半静音、后半轻声说话：取最响窗口分析，不应漏掉。
    rng = np.random.default_rng(1)
    n = SR * 3
    t = np.arange(n) / SR
    noise = rng.standard_normal(n) * 0.001
    phase = 2 * np.pi * np.cumsum(120 + 30 * np.sin(2 * np.pi * t)) / SR
    speech = np.where(t > 1.5, np.sin(phase) * 0.006, 0.0)
    mix = (noise + speech).astype(np.float32)
    assert has_speech(mix)


def test_frame_rms_shapes():
    x = np.zeros(SR, dtype=np.float32)
    energies = frame_rms(x, 480)  # 30ms 帧
    assert energies.shape[0] == SR // 480
    assert np.all(energies == 0)
