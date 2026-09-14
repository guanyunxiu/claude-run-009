# API 与消息契约

所有接口前缀：`/api/v1`。容器内由前端 nginx 同源反代；本地开发由 Vite 代理到 `http://localhost:8080`。

## 鉴权

采用**会话级随机令牌**（192-bit 随机数的 hex，创建会话时一次性返回，服务端不回显）：

| 令牌 | 能力 |
| --- | --- |
| `hostToken` | 主播：上传分片、结束会话、查看字幕、挂 WebSocket |
| `viewToken` | 观众：查看字幕、挂 WebSocket |

- 除 `POST /sessions`、`GET /health`、`GET /time` 外，所有接口都需要令牌。
- REST 请求头：`Authorization: Bearer <token>`。
- 浏览器 WebSocket 无法设置请求头，允许 `?token=<token>`（仅读路径；上传/结束**不**接受 query token）。
- 令牌与会话绑定：A 会话令牌访问 B 会话返回 401；观众令牌访问主播接口返回 403。
- WebSocket 握手校验 `Origin`：`WS_ALLOWED_ORIGINS=*` 放行全部（开发默认）；生产配置具体来源，默认仅允许与 `Host` 同源，跨站被拒（403）。无 `Origin` 头的非浏览器客户端（curl/服务端）放行，由令牌鉴权。
- 全局 `GATEWAY_API_KEY`：运维密钥，持有者等价 host（供 RTMP 拉流脚本跨会话使用）。未配置时 `GET /sessions` 直接返回 404。

```json
// POST /sessions 响应（令牌只在此处出现一次）
{ "id": "…", "hostToken": "…", "viewToken": "…", "…": "…" }
```

观众邀请链接形如 `/watch/<sessionId>?token=<viewToken>`。

## REST

### 健康检查 / 时钟

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 网关与 Postgres/Redis 连通性，含 `version`（新包标识；旧包无此字段） |
| GET | `/time` | 返回 `{serverMs, iso}`，前端用于估算时钟偏差 |
| GET | `/sessions/:id/pipeline-status` | 流水线探针：`chunks/lastChunkMs/lastSubtitleMs/streamBacklog`，需 view+ 令牌；旧版网关无此路由（返回 404 即说明容器跑的是旧二进制） |

### 会话

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/sessions` | 创建直播会话 |
| GET | `/sessions?limit=50` | 最近会话列表 |
| GET | `/sessions/:id` | 会话详情 |
| POST | `/sessions/:id/end` | 结束会话（广播 `session-end`） |

`POST /sessions` 请求体（均有默认值）：

```json
{
  "sourceLanguage": "zh",
  "targetLanguages": ["en", "ja"],
  "mediaType": "audio/pcm",
  "sampleRate": 16000,
  "channels": 1
}
```

### 音频分片上传

```
POST /sessions/:id/chunks?seq=12&startMs=1726200000000&endMs=1726200003000
Content-Type: audio/pcm
<body: 裸 S16LE 单声道 16kHz PCM 字节；也支持 multipart 字段 audio>
```

- 切片要求：固定 2–4 秒（浏览器默认 3 秒）。
- `seq` 单调递增，`(sessionId, seq)` 幂等：重复上传返回 `{"deduped": true}`，不会重复入队。
- 网关行为：对象存 MinIO（key：`<sessionId>/<seq:012d>.pcm`）→ 元数据写 PostgreSQL → 任务写 Redis Stream。

响应：

```json
{
  "sessionId": "…", "seq": 12, "startMs": 1726200000000, "endMs": 1726200003000,
  "objectKey": "…/000000000012.pcm", "bytes": 96000,
  "streamId": "1726…-0", "status": "queued"
}
```

### 字幕查询

```
GET /sessions/:id/subtitles?limit=100&beforeSeq=50
```

只返回 **final** 字幕（partial 仅通过 WebSocket 实时推送，不落库）。

## WebSocket

```
GET /sessions/:id/subtitles/ws?replay=20&token=<viewToken|hostToken>
```

- 需要会话令牌：浏览器用 query `token`（无法设置请求头）；服务端非浏览器客户端可用 `Authorization: Bearer`。
- 握手校验 `Origin`（见鉴权一节），跨站来源返回 403。
- 连接建立时回放该会话最近 N 条 final（Redis ZSET，按 `startMs` 顺序）。
- 服务端定时发送 Ping；客户端断线后指数退避重连，重连带 `replay=50` 补齐缺口。
- **错过 session-end 不死循环重连**：若连接时会话已 `ended`，服务端仍完成握手，先补发一条 `session-end` 再关闭；客户端在每次重连前也会调用 `GET /sessions/:id` 复核状态，发现 ended 即停止重连。
- 服务端推送两条消息类型：

### `subtitle`（partial 与 final 同构）

```json
{
  "type": "subtitle",
  "sessionId": "…",
  "seq": 12,
  "startMs": 1726200000000,
  "endMs": 1726200003000,
  "isFinal": false,
  "language": "zh",
  "text": "欢迎来到本次直播",
  "translations": { "en": "Welcome to this live stream" },
  "queueMs": 12,
  "asrMs": 180,
  "e2eMs": 245,
  "workerMs": 180,
  "emittedMs": 1726200003245
}
```

约定：

- 同一个 `seq` 先收到 `isFinal=false`（partial，可多次、文本逐步增长），后收到 `isFinal=true`（最终文本 + 译文）。
- final 覆盖该 seq 的 partial；前端按 `(seq, startMs)` 去重、按 `seq, startMs` 排序。
- partial 没有 `translations`（翻译只在 final 通道执行）。
- 延迟字段：`queueMs`（入队到被消费）、`asrMs`（模型转写耗时）、`e2eMs`（入队到推送）。

### `session-end`

```json
{ "type": "session-end", "sessionId": "…", "atMs": 1726203600000 }
```

## Redis 键规范

| Key / Stream | 类型 | 内容 |
| --- | --- | --- |
| `asr:tasks` | Stream | ASR 任务，field `data` 为任务 JSON |
| `asr:deadletter` | Stream | 处理失败超过 5 次的毒消息（含 originalId/error，裁剪保留 1000 条） |
| `asr-workers` | Consumer Group | 所有 worker 实例同组（`XREADGROUP` + `XAUTOCLAIM`） |
| `subtitles:<sessionId>` | Pub/Sub | 实时字幕扇出频道 |
| `subtitles:hot:<sessionId>` | ZSET | 最近 final 字幕，score=`startMs`，member=消息 JSON；TTL 1h，裁剪 100 条 |

## PostgreSQL 表

- `sessions`：会话（源语言、目标语言 JSONB、媒体参数、状态）。
- `audio_chunks`：分片元数据 + 对象 key，`(session_id, seq)` 唯一。
- `subtitles`：final 字幕与译文 JSONB，`(session_id, seq)` 唯一（幂等 upsert）。
