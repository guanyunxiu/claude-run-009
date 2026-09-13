"""跨语言契约：Go 网关 XADD 的任务 JSON 与字幕广播 JSON 的字段名一致性。

模拟 Go 端 model.ChunkTask 序列化出的 camelCase JSON，验证 Python 能解析；
再验证 Python 产出的字幕 JSON 字段名正是前端/Go 期望的 camelCase。
"""
import json

from app.models import ChunkTask, SubtitlePayload


GO_STYLE_TASK = {
    "taskId": "5c8a1f3e-0000-4000-9000-000000000001",
    "sessionId": "sess-abc",
    "seq": 7,
    "startMs": 1_700_000_000_000,
    "endMs": 1_700_000_003_000,
    "objectKey": "sess-abc/000000000007.pcm",
    "contentType": "audio/pcm",
    "sampleRate": 16000,
    "channels": 1,
    "language": "zh",
    "targets": ["en", "ja"],
    "enqueuedMs": 1_700_000_000_050,
}


def test_parse_go_style_chunk_task():
    task = ChunkTask.model_validate(GO_STYLE_TASK)
    assert task.task_id == GO_STYLE_TASK["taskId"]
    assert task.session_id == "sess-abc"
    assert task.seq == 7
    assert task.start_ms == 1_700_000_000_000
    assert task.object_key.endswith(".pcm")
    assert task.targets == ["en", "ja"]
    assert task.enqueued_ms == 1_700_000_000_050


def test_subtitle_serializes_camel_case():
    payload = SubtitlePayload(
        sessionId="sess-abc",
        seq=7,
        startMs=1_700_000_000_000,
        endMs=1_700_000_003_000,
        isFinal=True,
        language="zh",
        text="你好",
        translations={"en": "Hello"},
        queueMs=4,
        asrMs=210,
        e2eMs=300,
        workerMs=210,
        emittedMs=1_700_000_000_350,
    )
    body = json.loads(payload.model_dump_json())

    expected_keys = {
        "type", "sessionId", "seq", "startMs", "endMs", "isFinal", "language",
        "text", "translations", "queueMs", "asrMs", "e2eMs", "workerMs", "emittedMs",
    }
    assert expected_keys.issubset(body.keys())
    assert body["type"] == "subtitle"
    assert body["isFinal"] is True
    assert body["translations"] == {"en": "Hello"}
    # 不应泄漏 snake_case 字段
    assert not any("_" in key for key in body)
