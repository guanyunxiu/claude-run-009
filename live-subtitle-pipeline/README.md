# 直播实时字幕与多语言翻译流水线

浏览器 / RTMP / HLS 音频接入 → 固定 2–4 秒切片 → ASR 输出 partial/final → 多语言翻译 → WebSocket 实时字幕推送 → 音频分片 / 转写 / 字幕索引三层存储的端到端流水线。

```
前端(React+TS+Vite+Tailwind)  ──PCM chunks──▶  Go 网关(Gin)
      ▲                                           │
      │ WebSocket（partial/final，断线重连+回放）   ├─▶ MinIO 音频分片
      │                                           ├─▶ PostgreSQL 元数据/字幕
      └──── Redis Pub/Sub ◀── ASR Worker(Python) ─┴─▶ Redis Stream 任务队列
                   (faster-whisper / stub，LibreTranslate 翻译)
```

## 技术栈

| 层 | 技术 |
| --- | --- |
| 前端 | React 18 + TypeScript + Vite + Tailwind；Web Audio API（AudioWorklet，ScriptProcessor 回退）；WebSocket（指数退避重连、replay 补齐）；DOM 字幕叠加层 |
| 网关 | Go 1.22 + Gin + gorilla/websocket；Redis Stream 入队、Pub/Sub 扇出、ZSET 回放 |
| ASR Worker | Python 3.11 + FastAPI；faster-whisper（可选）/ 离线 stub；LibreTranslate（可选）/ stub |
| 队列 | Redis 7 Stream（消费组 + pending 重投，至少一次） |
| 存储 | MinIO/S3（音频分片）、Redis（热字幕 ZSET，TTL 1h）、PostgreSQL 16（会话/分片/字幕） |
| 部署 | Docker Compose（前端 nginx 反代 API 与 WebSocket） |

## 快速开始

```bash
cd live-subtitle-pipeline
cp .env.example .env

# 默认 ASR_PROVIDER=stub：零模型下载、离线可跑完整链路（含伪转写/伪翻译）
docker compose up --build
```

服务地址：

| 服务 | 地址 |
| --- | --- |
| 前端（入口） | http://localhost:8081 |
| Go 网关 | http://localhost:8080/api/v1/health |
| ASR Worker | http://localhost:8000/health |
| MinIO 控制台 | http://localhost:9001 （minioadmin/minioadmin） |

使用：打开前端 → 新建会话（源语言 `zh`，目标语言勾选 `en/ja`）→ **我是主播** → 授权麦克风开始采集；另开浏览器/无痕窗口进入同一会话的**观众席**即可看到实时双语字幕、延迟指标与断线重连。

> 麦克风采集要求 `localhost` 或 HTTPS。

### 使用真实 ASR（faster-whisper）

stub 用于演示和 CI；接入真实模型：

```bash
# .env 中设置
ASR_PROVIDER=whisper
ASR_MODEL=small          # tiny/base/small/medium/large-v3
ASR_DEVICE=cpu           # 有 GPU 用 cuda
ASR_COMPUTE_TYPE=int8    # cpu: int8；gpu: float16
ASR_LANGUAGE=zh          # 留空自动检测
```

whisper 镜像额外安装 faster-whisper 与 ffmpeg，首次启动会下载模型。Worker 对每个分片先跑 beam=1 的 partial，再跑 beam=5 的 final。

### 使用真实翻译

默认 `TRANSLATE_PROVIDER=stub`（内置语料对照）。自建 [LibreTranslate](https://github.com/LibreTranslate/LibreTranslate) 后：

```bash
TRANSLATE_PROVIDER=libretranslate
LIBRETRANSLATE_URL=http://libretranslate:5000
```

### 扩容 ASR Worker

```bash
docker compose --profile scale up --build     # 额外启动 asr-worker-2，同一消费组分摊分片
```

## RTMP / HLS 接入

浏览器之外的直播源用 ffmpeg 拉 PCM 后复用同一个上传接口（仅需系统 ffmpeg）：

```bash
python examples/ingest_rtmp.py \
  --source "rtmp://live.example.com/app/stream" \
  --gateway http://localhost:8080 \
  --language zh --target en
```

WebRTC 收流可在服务端（mediasoup/LiveKit）取音轨切 PCM 后调用相同 chunk 接口，下游无需改动。

## 本地开发（不用 Docker）

```bash
# 1. 基础设施
docker compose up -d postgres redis minio createbuckets

# 2. 网关
cd gateway
export DATABASE_URL='postgres://subtitle:subtitle@localhost:5432/subtitles?sslmode=disable'
export REDIS_ADDR=localhost:6379 MINIO_ENDPOINT=localhost:9000
go mod tidy && go run ./cmd/server

# 3. Worker
cd worker
pip install -r requirements.txt
REDIS_ADDR=localhost:6379 MINIO_ENDPOINT=localhost:9000 \
DATABASE_URL='postgres://subtitle:subtitle@localhost:5432/subtitles' \
uvicorn app.main:app --port 8000

# 4. 前端
cd frontend
npm install && npm run dev     # http://localhost:5173 （/api 代理到 :8080）
```

Worker 纯逻辑测试：

```bash
cd worker && pip install pytest numpy && pytest -q
```

## 目录结构

```
live-subtitle-pipeline/
├── docker-compose.yml         # postgres/redis/minio/gateway/worker/frontend
├── .env.example
├── gateway/                   # Go 网关
│   ├── cmd/server             # 启动、依赖连接、优雅退出
│   └── internal/
│       ├── api                # REST：会话/分片上传/字幕/时间/健康
│       ├── queue              # Redis Stream 入队 + key 规范
│       ├── ws                 # Hub/Client：订阅、回放、Pub/Sub 扇出
│       ├── storage            # MinIO/S3
│       ├── db                 # 嵌入式 SQL 迁移
│       ├── model              # 跨组件 JSON 契约
│       └── config
├── worker/                    # Python ASR Worker
│   └── app/
│       ├── consumer.py        # 消费组、pending 认领、partial/final 两阶段
│       ├── asr*.py            # ASR 抽象 + stub + faster-whisper
│       ├── translate*.py      # 翻译抽象 + stub + LibreTranslate
│       ├── audio.py           # PCM/WAV/ffmpeg 解码、静音检测
│       ├── services.py        # MinIO/PG/Redis(Redis ZSET+PubSub)
│       └── main.py            # FastAPI 健康检查 + 后台消费线程
├── frontend/
│   └── src/
│       ├── lib/               # api、recorder(Worklet)、useSubtitles(WS 重连)
│       ├── worklets/          # PCM 固定切片 Processor
│       ├── components/        # 字幕叠加层、时间轴、延迟/连接状态
│       └── pages/             # 首页 / 主播台 / 观众席
├── examples/ingest_rtmp.py    # RTMP/HLS 拉流接入
└── docs/                      # ARCHITECTURE.md / API.md
```

## 消息与可靠性要点

- **切片契约**：`{sessionId, seq, startMs, endMs}`，2–4 秒；`(sessionId, seq)` 全链路幂等（上传去重、PG upsert、前端去重）。
- **字幕语义**：同 `seq` 先 partial（仅实时、可多次增长）后 final（权威文本+译文，落 PG/Redis）。
- **至少一次**：Worker 处理成功才 `XACK`；崩溃消息 30s 后 `XAUTOCLAIM` 重投，所有写操作幂等。
- **断线重连**：前端 1→2→4…15s 退避重连，`replay` 参数由网关回放最近 final；REST 支持 `beforeSeq` 翻历史。
- **延迟可观测**：每条字幕带 `queueMs / asrMs / e2eMs`，前端分段着色展示。

## 排障

**切片在涨但没字幕**

1. 确认容器里跑的是新网关包（旧包没有探针路由）：
   ```bash
   curl -s localhost:8080/api/v1/health            # 新包返回含 "version"
   curl -s "localhost:8080/api/v1/sessions/<id>/pipeline-status?token=<viewToken>"
   # 404 = 容器是旧二进制，需要强制重建（见下）
   ```
2. 看 worker 空转写率（轻声/降噪被误判为空时该值接近 1）：
   ```bash
   curl -s localhost:8000/metrics      # stats.processed / stats.empty / emptyRatio
   docker compose logs asr-worker | grep -E "processed|empty final|failed"
   ```
   VAD 已改为自适应语音活动检测（帧能量 + 低频周期性 + 峰均比），低至 -54dBFS 峰值的轻声也会出字幕；真正的数字静音/稳态噪声才判空。
3. 前端主播台在“有分片但 ~12s 无字幕”时会直接显示 stalled 告警与队列积压。

**强制重建，避免容器跑旧包**（Docker 构建缓存或复用了旧镜像时）：
```bash
docker compose build --no-cache gateway asr-worker frontend
docker compose up -d --force-recreate gateway asr-worker frontend
```
网关多阶段构建从源码编译；仓库根目录 `bin/gateway-linux`（amd64）仅为离线预编译产物，不进入 Docker 构建上下文，也不参与镜像内容。

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) 与 [docs/API.md](docs/API.md)。
