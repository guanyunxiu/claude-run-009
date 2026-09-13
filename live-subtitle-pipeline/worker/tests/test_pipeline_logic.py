"""不依赖外部服务的纯逻辑测试：音频解码、stub ASR、stub 翻译。"""
import numpy as np

from app.asr_stub import StubASR
from app.audio import decode_audio, is_silence
from app.config import Settings
from app.translate_stub import StubTranslator


def _sine_pcm_s16le(seconds: float = 0.5, rate: int = 16000, freq: float = 440.0) -> bytes:
    t = np.linspace(0, seconds, int(rate * seconds), endpoint=False)
    wave = (np.sin(2 * np.pi * freq * t) * 20000).astype("<i2")
    return wave.tobytes()


def test_decode_pcm_s16le():
    raw = _sine_pcm_s16le()
    pcm, rate = decode_audio(raw, "audio/pcm", 16000, 1)
    assert rate == 16000
    assert pcm.dtype == np.float32
    assert pcm.shape == (8000,)
    assert -1.0 <= pcm.min() and pcm.max() <= 1.0


def test_decode_wav_roundtrip(tmp_path):
    import io
    import wave

    raw = _sine_pcm_s16le()
    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(16000)
        wav.writeframes(raw)
    pcm, rate = decode_audio(buf.getvalue(), "audio/wav")
    assert rate == 16000
    assert pcm.shape[0] == 8000


def test_silence_detection():
    assert is_silence(np.zeros(1600, dtype=np.float32))
    loud = (np.sin(np.linspace(0, 50, 1600)) * 0.3).astype(np.float32)
    assert not is_silence(loud)


def test_stub_asr_partial_and_final_consistent():
    settings = Settings(asr_language="zh")
    asr = StubASR(settings)
    pcm = (np.sin(np.linspace(0, 80, 16000)) * 0.2).astype(np.float32)

    partial = asr.transcribe(pcm, "zh", final=False, seq=1)
    final = asr.transcribe(pcm, "zh", final=True, seq=1)

    assert final.text
    assert final.language == "zh"
    assert final.text.startswith(partial.text[:4])
    assert len(final.text) >= len(partial.text)


def test_stub_asr_silence_returns_empty():
    asr = StubASR(Settings(asr_language="en"))
    result = asr.transcribe(np.zeros(16000, dtype=np.float32), "en", final=True)
    assert result.text == ""


def test_stub_translator_builtin_phrase():
    translator = StubTranslator()
    out = translator.translate(
        "欢迎来到本次直播，我们正在测试实时字幕系统。", "zh", ["en", "ja", "zh"]
    )
    assert "en" in out and out["en"].startswith("Welcome")
    assert "ja" in out
    # 源语言 == 目标语言时跳过
    assert "zh" not in out
