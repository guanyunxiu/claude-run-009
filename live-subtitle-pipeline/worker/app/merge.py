"""句级合并：把 worker 的短 final 片段合并成更可读的句子。

背景：2-4 秒切片对应的 stub/whisper 文本往往是短句/片段，逐条推送会让字幕
时间轴很碎。这里在“同一会话、同一来源”上维护一个滑动缓冲：
  - 新片段追加到当前句；
  - 出现句末标点（。！？.!? 换行）或长度/时长超阈值时，输出一句完整字幕；
  - 输出的合并句覆盖此前缓冲内 seq 的碎句（前端按 startMs 去重/覆盖）。

合并只影响“句子级 final 展示”，原始片段不落库（由调用方决定）。
"""
from __future__ import annotations

from dataclasses import dataclass, field

# 句末标点（中英文）。
_SENTENCE_END = set("。！？!?…\n")
_MAX_CHARS = 80
_MAX_SPAN_MS = 15_000


@dataclass
class MergedItem:
    text: str
    seq_start: int
    seq_end: int
    start_ms: int
    end_ms: int


@dataclass
class _Buffer:
    text: str = ""
    seq_start: int = 0
    seq_end: int = 0
    start_ms: int = 0
    end_ms: int = 0
    parts: int = 0


@dataclass
class SentenceMerger:
    """每个 (session, source) 一个实例，非线程安全（worker 单消费线程）。"""

    max_chars: int = _MAX_CHARS
    max_span_ms: int = _MAX_SPAN_MS

    _buf: _Buffer | None = field(default=None)

    def add(self, text: str, seq: int, start_ms: int, end_ms: int) -> MergedItem | None:
        """追加一个片段；若凑成完整句则返回合并结果，否则返回 None。"""
        text = (text or "").strip()
        if not text:
            return None

        if self._buf is None:
            self._buf = _Buffer(
                text=text, seq_start=seq, seq_end=seq,
                start_ms=start_ms, end_ms=end_ms, parts=1,
            )
        else:
            b = self._buf
            # 顺序不连续（seq 跳跃）时先封口旧句，避免跨断流错误合并。
            if seq != b.seq_end + 1:
                flushed = self._flush()
                self._buf = _Buffer(
                    text=text, seq_start=seq, seq_end=seq,
                    start_ms=start_ms, end_ms=end_ms, parts=1,
                )
                # 调用方一次只处理一个片段，这里把旧句先返回（新片段留在缓冲）。
                if flushed is not None:
                    return flushed
            else:
                b.text = _join(b.text, text)
                b.seq_end = seq
                b.end_ms = end_ms
                b.parts += 1

        b = self._buf
        if self._is_sentence_end(b.text) or len(b.text) >= self.max_chars or (b.end_ms - b.start_ms) >= self.max_span_ms:
            return self._flush()
        return None

    def flush(self) -> MergedItem | None:
        """会话结束/强制收口时取出残余半句。"""
        return self._flush()

    def _flush(self) -> MergedItem | None:
        b = self._buf
        if b is None or not b.text:
            self._buf = None
            return None
        item = MergedItem(
            text=b.text.strip(),
            seq_start=b.seq_start,
            seq_end=b.seq_end,
            start_ms=b.start_ms,
            end_ms=b.end_ms,
        )
        self._buf = None
        return item

    @staticmethod
    def _is_sentence_end(text: str) -> bool:
        return bool(text) and text[-1] in _SENTENCE_END


def _join(a: str, b: str) -> str:
    """中文不需要空格；含拉丁字母片段之间用空格连接。"""
    if not a:
        return b
    if _needs_space(a[-1], b[0]):
        return a + " " + b
    return a + b


def _needs_space(prev_last: str, next_first: str) -> bool:
    def latin(ch: str) -> bool:
        return ch.isascii() and ch.isalnum()
    return latin(prev_last) and latin(next_first)
