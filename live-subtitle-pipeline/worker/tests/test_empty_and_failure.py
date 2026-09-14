"""验证空 final（静音/无语音）不落库、不推送，避免空字幕覆盖时间轴。"""
import numpy as np
import time
from unittest.mock import MagicMock

from app.config import Settings
from app.consumer import Consumer
from app.models import ChunkTask
from app.translate_stub import StubTranslator


class _EmptyASR:
    """模拟 whisper 在某分片解码出空文本（无语音）。"""
    name = "empty"

    def transcribe(self, pcm, language, final, seq=None):
        from app.models import Transcript
        # partial 给点文本，final 为空（模拟该段最终判定无语音）。
        text = "临时" if not final else ""
        return Transcript(text=text, language=language)


def _task(seq=0):
    return ChunkTask(
        taskId="t", sessionId="s", seq=seq, startMs=0, endMs=3000,
        objectKey="s/0.pcm", contentType="audio/pcm", sampleRate=16000,
        channels=1, language="zh", targets=["en"],
        enqueuedMs=int(time.time() * 1000),
    )


def _silence_pcm():
    # S16LE 静音字节。
    return (np.zeros(16000 * 3, dtype="<i2")).tobytes()


def test_empty_final_not_persisted_or_published():
    settings = Settings(enable_partial=True)
    store = MagicMock(); store.get.return_value = _silence_pcm()
    db = MagicMock()
    bus = MagicMock()
    consumer = Consumer(settings, MagicMock(), store, db, bus, _EmptyASR(), StubTranslator())

    consumer.process(_task())

    # 空 final：绝不写库、绝不推 final。
    db.upsert_subtitle.assert_not_called()
    published = [call.args[0] for call in bus.publish_subtitle.call_args_list]
    assert not any(p.isFinal for p in published), "empty final must not be published"


class _RaisingASR:
    """模拟 whisper 解码抛异常（如 compute_type 不兼容）。"""
    name = "raising"

    def transcribe(self, pcm, language, final, seq=None):
        raise RuntimeError("decode failed")


def test_final_exception_propagates_not_swallowed():
    """final 异常必须向上抛（触发重投），而不是被吞成空字幕。"""
    import pytest
    settings = Settings(enable_partial=False)
    store = MagicMock()
    t = np.linspace(0, 3, 48000, endpoint=False)
    store.get.return_value = ((np.sin(2 * np.pi * 300 * t) * 20000).astype("<i2")).tobytes()
    consumer = Consumer(settings, MagicMock(), store, MagicMock(), MagicMock(),
                        _RaisingASR(), StubTranslator())

    with pytest.raises(RuntimeError, match="decode failed"):
        consumer.process(_task())


class _FixedASR:
    """final 固定产出非空文本。"""
    name = "fixed"

    def transcribe(self, pcm, language, final, seq=None):
        from app.models import Transcript
        return Transcript(text="固定字幕" if final else "固", language=language)


def _speech_pcm():
    t = np.linspace(0, 3, 16000 * 3, endpoint=False)
    return ((np.sin(2 * np.pi * 300 * t) * 0.2) * 32767).astype("<i2").tobytes()


def test_persist_failure_still_publishes_realtime():
    """PG 写失败不能阻断实时字幕（修复“切片在涨却等不到字幕”）。"""
    settings = Settings(enable_partial=False)
    store = MagicMock(); store.get.return_value = _speech_pcm()
    db = MagicMock()
    db.upsert_subtitle.side_effect = RuntimeError("pg unavailable")
    bus = MagicMock()
    consumer = Consumer(settings, MagicMock(), store, db, bus, _FixedASR(), StubTranslator())

    # 不应抛出：实时推送优先，持久化失败被吞掉并计数。
    consumer.process(_task())

    published = [c.args[0] for c in bus.publish_subtitle.call_args_list]
    finals = [p for p in published if p.isFinal]
    assert len(finals) == 1 and finals[0].text == "固定字幕"
