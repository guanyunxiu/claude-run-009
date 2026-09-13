#!/usr/bin/env python3
"""RTMP / HLS 拉流接入示例。

用 ffmpeg 从直播源拉流，重采样为 16kHz 单声道 S16LE PCM，
按固定 3 秒切片后通过网关 HTTP 接口上传，复用同一条 ASR 流水线。

依赖：系统安装 ffmpeg。仅使用 Python 标准库。

用法:
    python ingest_rtmp.py \
        --source "rtmp://live.example.com/app/stream" \
        --gateway http://localhost:8080 \
        --language zh --target en --target ja

    python ingest_rtmp.py \
        --source "https://example.com/live/index.m3u8" \
        --gateway http://localhost:8080
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request
from urllib.parse import quote

SAMPLE_RATE = 16000
CHANNELS = 1
BYTES_PER_SAMPLE = 2
CHUNK_SECONDS = 3
CHUNK_BYTES = SAMPLE_RATE * CHANNELS * BYTES_PER_SAMPLE * CHUNK_SECONDS


def http_json(method: str, url: str, body: bytes | None = None,
              content_type: str = "application/json",
              token: str | None = None) -> dict:
    request = urllib.request.Request(url, data=body, method=method)
    request.add_header("Content-Type", content_type)
    if token:
        # 写接口只走 Authorization 头，令牌不出现在 URL/日志中。
        request.add_header("Authorization", f"Bearer {token}")
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.loads(response.read())
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")
        raise SystemExit(f"HTTP {exc.code} for {url}: {detail}") from exc


def create_session(gateway: str, language: str, targets: list[str]) -> tuple[str, str]:
    """返回 (session_id, host_token)。"""
    payload = json.dumps({
        "sourceLanguage": language,
        "targetLanguages": targets,
        "mediaType": "audio/pcm",
        "sampleRate": SAMPLE_RATE,
        "channels": CHANNELS,
    }).encode()
    result = http_json("POST", f"{gateway}/api/v1/sessions", payload)
    print(f"session created: {result['id']}")
    if not result.get("hostToken"):
        raise SystemExit("gateway did not return hostToken (incompatible version)")
    return result["id"], result["hostToken"]


def upload_chunk(gateway: str, session_id: str, host_token: str, seq: int, start_ms: int,
                 end_ms: int, pcm: bytes) -> None:
    url = (
        f"{gateway}/api/v1/sessions/{quote(session_id)}/chunks"
        f"?seq={seq}&startMs={start_ms}&endMs={end_ms}"
    )
    http_json("POST", url, pcm, content_type="audio/pcm", token=host_token)


def ingest(source: str, gateway: str, language: str, targets: list[str]) -> None:
    session_id, host_token = create_session(gateway, language, targets)

    # -fflags +genpts 处理 HLS；-re 按实时速率（拉直播源时源本身即实时）。
    command = [
        "ffmpeg", "-hide_banner", "-loglevel", "error",
        "-fflags", "+genpts",
        "-i", source,
        "-vn", "-ac", str(CHANNELS), "-ar", str(SAMPLE_RATE),
        "-f", "s16le", "-acodec", "pcm_s16le", "pipe:1",
    ]
    proc = subprocess.Popen(command, stdout=subprocess.PIPE)
    assert proc.stdout is not None

    seq = 0
    anchor_ms = time.time_ns() // 1_000_000
    print("pulling stream, uploading 3s chunks ... (Ctrl+C to stop)")
    try:
        while True:
            pcm = _read_exact(proc.stdout, CHUNK_BYTES)
            if not pcm:
                break
            start_ms = anchor_ms + seq * CHUNK_SECONDS * 1000
            end_ms = start_ms + round(len(pcm) / SAMPLE_RATE / BYTES_PER_SAMPLE * 1000)
            if len(pcm) < CHUNK_BYTES:
                # 最后一小段不足 3s，仍上传后结束。
                upload_chunk(gateway, session_id, host_token, seq, start_ms, end_ms, pcm)
                break
            upload_chunk(gateway, session_id, host_token, seq, start_ms, end_ms, pcm)
            print(f"  uploaded seq={seq} bytes={len(pcm)}")
            seq += 1
    except KeyboardInterrupt:
        print("\ninterrupted, ending session ...")
    finally:
        proc.terminate()
        try:
            http_json("POST", f"{gateway}/api/v1/sessions/{session_id}/end",
                      token=host_token)
        except Exception as exc:  # noqa: BLE001
            print(f"end session failed: {exc}", file=sys.stderr)
        print(f"session ended: {session_id}, total chunks={seq}")


def _read_exact(stream, size: int) -> bytes:
    chunks: list[bytes] = []
    remaining = size
    while remaining > 0:
        part = stream.read(remaining)
        if not part:
            break
        chunks.append(part)
        remaining -= len(part)
    return b"".join(chunks)


def main() -> None:
    parser = argparse.ArgumentParser(description="RTMP/HLS -> PCM chunks -> gateway")
    parser.add_argument("--source", required=True, help="rtmp:// 或 https://...m3u8 地址")
    parser.add_argument("--gateway", default="http://localhost:8080")
    parser.add_argument("--language", default="zh")
    parser.add_argument("--target", action="append", default=[], help="目标语言，可多次指定")
    args = parser.parse_args()
    ingest(args.source, args.gateway.rstrip("/"), args.language, args.target)


if __name__ == "__main__":
    main()
