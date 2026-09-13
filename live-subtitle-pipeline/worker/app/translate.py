"""翻译提供者抽象与工厂。

- stub          : 离线词典伪翻译（仅保证流水线可演示，不追求译文质量）
- libretranslate: 自建 LibreTranslate（HTTP），可离线部署真实翻译
"""
from __future__ import annotations

from abc import ABC, abstractmethod

from .config import Settings


class BaseTranslator(ABC):
    @abstractmethod
    def translate(self, text: str, source: str, targets: list[str]) -> dict[str, str]:
        """返回 {目标语言: 译文}；失败或源=目标时跳过对应 key。"""


def build_translator(settings: Settings) -> BaseTranslator:
    provider = settings.translate_provider.lower()
    if provider == "stub":
        from .translate_stub import StubTranslator
        return StubTranslator()
    if provider == "libretranslate":
        from .translate_libre import LibreTranslateTranslator
        return LibreTranslateTranslator(settings.libretranslate_url)
    raise ValueError(f"unknown translate provider: {provider}")
