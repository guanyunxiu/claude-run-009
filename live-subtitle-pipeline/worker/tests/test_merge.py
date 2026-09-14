"""句级合并纯逻辑测试。"""
from app.merge import SentenceMerger


def test_flushes_on_sentence_punctuation():
    m = SentenceMerger()
    assert m.add("你好", 0, 0, 3000) is None          # 未到句末
    out = m.add("世界。", 1, 3000, 6000)               # 句末标点封口
    assert out is not None
    assert out.text == "你好世界。"
    assert out.seq_start == 0 and out.seq_end == 1
    assert out.start_ms == 0 and out.end_ms == 6000


def test_flushes_on_max_chars():
    m = SentenceMerger(max_chars=10)
    out = m.add("一二三四五六七八九十十一", 0, 0, 3000)
    assert out is not None
    assert len(out.text) >= 10


def test_flushes_on_max_span():
    m = SentenceMerger(max_span_ms=6000)
    assert m.add("短句", 0, 0, 3000) is None
    out = m.add("继续", 1, 3000, 9000)  # 跨度 9s
    assert out is not None
    assert out.text.startswith("短句")


def test_seq_gap_closes_previous():
    m = SentenceMerger()
    m.add("第一句片段", 0, 0, 3000)
    # seq 跳到 5（断流/重连），应先把旧缓冲作为一句返回。
    out = m.add("新片段", 5, 15000, 18000)
    assert out is not None
    assert out.text == "第一句片段"
    # 新片段仍在缓冲里，flush 可取到。
    tail = m.flush()
    assert tail is not None and tail.text == "新片段"


def test_latin_join_space():
    m = SentenceMerger()
    assert m.add("hello", 0, 0, 3000) is None
    out = m.add("world!", 1, 3000, 6000)
    assert out.text == "hello world!"


def test_flush_remaining():
    m = SentenceMerger()
    m.add("没标点的半句", 0, 0, 3000)
    out = m.flush()
    assert out is not None and out.text == "没标点的半句"
    assert m.flush() is None


def test_empty_text_ignored():
    m = SentenceMerger()
    assert m.add("   ", 0, 0, 3000) is None
    assert m.flush() is None
