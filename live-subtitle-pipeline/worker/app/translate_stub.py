"""离线伪翻译：对内置语料给出整句对照，其它文本使用带语种前缀的标注。
仅用于演示流水线，不追求译文质量；真实部署使用 libretranslate 等后端。"""
from __future__ import annotations

# 键: (源语言, 目标语言, 原文) -> 译文
_PHRASE_MAP: dict[tuple[str, str, str], str] = {
    ("zh", "en", "欢迎来到本次直播，我们正在测试实时字幕系统。"):
        "Welcome to this live stream; we are testing the real-time captioning system.",
    ("zh", "en", "音频以三秒为一个切片进入识别流水线。"):
        "Audio enters the recognition pipeline in three-second slices.",
    ("zh", "en", "最终字幕会写入数据库，并通过网页实时推送给观众。"):
        "Final captions are stored in the database and pushed to viewers in real time.",
    ("zh", "en", "如果网络出现抖动，前端会自动重连并补齐缺失的字幕。"):
        "If the network jitters, the frontend reconnects and fills in the missing captions.",
    ("zh", "en", "多语言翻译结果会和原文一起下发，可以同时显示双语字幕。"):
        "Multilingual translations are delivered with the source text for bilingual subtitles.",
    ("zh", "en", "延迟统计包括排队耗时、识别耗时和端到端耗时。"):
        "Latency metrics include queue time, recognition time, and end-to-end time.",
    ("zh", "en", "感谢观看，下一段内容马上开始。"):
        "Thanks for watching; the next segment starts soon.",
    ("zh", "ja", "欢迎来到本次直播，我们正在测试实时字幕系统。"):
        "ライブ配信へようこそ。リアルタイム字幕システムをテストしています。",
    ("en", "zh", "Welcome to this live stream, we are testing real-time captions."):
        "欢迎来到本次直播，我们正在测试实时字幕。",
    ("en", "ja", "Welcome to this live stream, we are testing real-time captions."):
        "このライブ配信へようこそ。リアルタイム字幕をテストしています。",
    ("ja", "en", "このライブ配信へようこそ、リアルタイム字幕をテストしています。"):
        "Welcome to this live stream; we are testing real-time captions.",
}

_TAGS = {
    "en": "[EN]", "zh": "[中]", "ja": "[日]", "ko": "[한]",
    "fr": "[FR]", "de": "[DE]", "es": "[ES]",
}


class StubTranslator:
    def translate(self, text: str, source: str, targets: list[str]) -> dict[str, str]:
        result: dict[str, str] = {}
        for target in targets:
            target = target.strip().lower()
            if not target or target == source:
                continue
            mapped = _PHRASE_MAP.get((source, target, text))
            if mapped is not None:
                result[target] = mapped
            elif text:
                result[target] = f"{_TAGS.get(target, f'[{target}]')} {text}"
        return result
