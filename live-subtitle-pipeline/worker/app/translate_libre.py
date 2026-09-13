"""LibreTranslate HTTP 后端（可完全自建、离线运行的机器翻译服务）。"""
from __future__ import annotations

import logging

import httpx

logger = logging.getLogger(__name__)


class LibreTranslateTranslator:
    def __init__(self, base_url: str, timeout: float = 4.0) -> None:
        if not base_url:
            raise ValueError("LIBRETRANSLATE_URL is required when TRANSLATE_PROVIDER=libretranslate")
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

    def translate(self, text: str, source: str, targets: list[str]) -> dict[str, str]:
        if not text:
            return {}
        result: dict[str, str] = {}
        with httpx.Client(timeout=self.timeout) as client:
            for target in targets:
                target = target.strip().lower()
                if not target or target == source:
                    continue
                try:
                    resp = client.post(
                        f"{self.base_url}/translate",
                        json={"q": text, "source": source, "target": target, "format": "text"},
                    )
                    resp.raise_for_status()
                    result[target] = resp.json()["translatedText"]
                except (httpx.HTTPError, KeyError) as exc:
                    # 翻译是增强能力，单个目标语言失败不应阻断字幕主链路。
                    logger.warning("translate %s->%s failed: %s", source, target, exc)
        return result
