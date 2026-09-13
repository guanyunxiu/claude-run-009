"""离线确定性伪转写：不依赖任何模型，用音频 RMS 能量和会话序号驱动语料库，
输出稳定、可复现的文本，便于本地端到端演示与测试整条流水线。"""
from __future__ import annotations

import hashlib
import math

import numpy as np

from .models import Transcript

# 每个语种准备一个语料环，按 seq 轮转，模拟连续口播。
_PHRASES: dict[str, list[str]] = {
    "zh": [
        "欢迎来到本次直播，我们正在测试实时字幕系统。",
        "音频以三秒为一个切片进入识别流水线。",
        "最终字幕会写入数据库，并通过网页实时推送给观众。",
        "如果网络出现抖动，前端会自动重连并补齐缺失的字幕。",
        "多语言翻译结果会和原文一起下发，可以同时显示双语字幕。",
        "延迟统计包括排队耗时、识别耗时和端到端耗时。",
        "感谢观看，下一段内容马上开始。",
    ],
    "en": [
        "Welcome to this live stream, we are testing real-time captions.",
        "Audio is sliced into three second chunks for the recognition pipeline.",
        "Final captions are persisted and pushed to viewers over the web.",
        "If the network glitches, the client reconnects and replays missed lines.",
        "Translations are delivered together with the source transcript.",
        "Latency metrics cover queue time, ASR time, and end to end delay.",
        "Thanks for watching, the next segment is coming up.",
    ],
    "ja": [
        "このライブ配信へようこそ、リアルタイム字幕をテストしています。",
        "音声は三秒ごとのチャンクに分割されて認識されます。",
        "確定字幕はデータベースに保存され、視聴者へ配信されます。",
        "通信が途切れても、再接続して字幕を取り戻せます。",
        "翻訳結果は原文と一緒に配信されます。",
    ],
}

_FALLBACK = {
    "zh": "（这一段没有检测到清晰的语音）",
    "en": "(no clear speech detected in this segment)",
    "ja": "（この区間にはっきりした音声はありません）",
}


class StubASR:
    name = "stub"

    def __init__(self, settings) -> None:
        self.language = settings.asr_language or "zh"

    def transcribe(self, pcm: np.ndarray, language: str, final: bool,
                   seq: int | None = None) -> Transcript:
        lang = language or self.language or "zh"
        bank = _PHRASES.get(lang) or _PHRASES["en"]
        quiet = _FALLBACK.get(lang, _FALLBACK["en"])

        # 用内容哈希决定选句，保证 partial/final 对同一分片选到同一句。
        digest = hashlib.sha1(np.ascontiguousarray(pcm[:512]).tobytes()).digest()
        pick_index = digest[0] % len(bank)
        sentence = bank[pick_index]

        rms = float(np.sqrt(np.mean(np.square(pcm)))) if pcm.size else 0.0
        if rms < 0.005:
            return Transcript(text="", language=lang, confidence=0.2)

        if final:
            # 极弱能量给一句兜底文本，正常情况输出整句。
            text = sentence if rms >= 0.01 else quiet
            return Transcript(text=text, language=lang, confidence=min(0.99, 0.6 + rms))

        # partial：按能量模拟“逐字上屏”，取句子前缀。
        ratio = min(1.0, max(0.25, rms * 4.0))
        cut = max(1, math.ceil(len(sentence) * ratio))
        return Transcript(text=sentence[:cut], language=lang, confidence=0.55)
