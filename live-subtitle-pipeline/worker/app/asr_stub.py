"""离线确定性伪转写：不依赖任何模型，用语音活动检测 + 会话序号驱动语料库，
输出稳定、可复现的文本，便于本地端到端演示整条流水线。

设计要点（修复“轻声说话几乎全空转写”）：
  - 不再用偏高的 RMS 硬阈值（0.005/0.01），浏览器降噪+AGC 后轻声说话的
    RMS 常只有 0.001~0.01，旧逻辑会把它们全判成空。
  - 语音活动判断交给 audio.has_speech（自适应底噪 + 帧起伏 + 峰值），
    对真正的数字静音仍返回空，对有声切片一定给出非空 final。
  - 文本按 seq 顺序推进，partial 是同一句的前缀（随能量增长），
    避免旧实现“同一片 partial/final 文本对不上”的割裂感。
"""
from __future__ import annotations

import math

import numpy as np

from .audio import has_speech
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
        "即使说话声音比较轻，字幕也应当能够正常出现。",
        "感谢观看，下一段内容马上开始。",
    ],
    "en": [
        "Welcome to this live stream, we are testing real-time captions.",
        "Audio is sliced into three second chunks for the recognition pipeline.",
        "Final captions are persisted and pushed to viewers over the web.",
        "If the network glitches, the client reconnects and replays missed lines.",
        "Translations are delivered together with the source transcript.",
        "Latency metrics cover queue time, recognition time, and end to end delay.",
        "Even quiet speech should still produce captions reliably.",
        "Thanks for watching, the next segment is coming up.",
    ],
    "ja": [
        "このライブ配信へようこそ、リアルタイム字幕をテストしています。",
        "音声は三秒ごとのチャンクに分割されて認識されます。",
        "確定字幕はデータベースに保存され、視聴者へ配信されます。",
        "通信が途切れても、再接続して字幕を取り戻せます。",
        "翻訳結果は原文と一緒に配信されます。",
        "小さな声でも字幕は正しく表示されるはずです。",
    ],
}

# 数字静音/无语音时的置信地板
_SILENCE_RMS = 1.0e-4


class StubASR:
    name = "stub"

    def __init__(self, settings) -> None:
        self.language = settings.asr_language or "zh"

    def _phrase(self, lang: str, seq: int | None) -> str:
        bank = _PHRASES.get(lang) or _PHRASES["en"]
        # 有 seq 时按顺序推进（连续口播）；seq 缺失时退化为按长度取模。
        idx = (seq if seq is not None else 0) % len(bank)
        return bank[idx]

    def transcribe(self, pcm: np.ndarray, language: str, final: bool,
                   seq: int | None = None) -> Transcript:
        lang = language or self.language or "zh"
        sentence = self._phrase(lang, seq)

        pcm = np.asarray(pcm, dtype=np.float32)
        rms = float(np.sqrt(np.mean(np.square(pcm)))) if pcm.size else 0.0

        # 真正没有语音活动（数字静音 / 稳态极弱底噪）才返回空。
        if rms < _SILENCE_RMS or not has_speech(pcm):
            return Transcript(text="", language=lang, confidence=0.15)

        if final:
            # 任何通过语音活动判断的切片都给完整句，不再因“能量低”而吞字。
            return Transcript(text=sentence, language=lang,
                              confidence=min(0.98, 0.55 + 5.0 * rms))

        # partial：按响度模拟“逐字上屏”，取同一句前缀；
        # 轻声也至少给出一个开头字，保证有反馈。
        ratio = min(1.0, max(0.15, 0.25 + rms * 6.0))
        cut = max(1, math.ceil(len(sentence) * ratio))
        return Transcript(text=sentence[:cut], language=lang, confidence=0.5)
