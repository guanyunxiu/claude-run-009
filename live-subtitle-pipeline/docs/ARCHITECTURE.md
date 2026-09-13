# 架构设计

## 总览

```
                          ┌────────────────────────────────────────────┐
  主播浏览器               │              Go 网关 (Gin) :8080            │
  ┌──────────┐  3s PCM     │  ┌──────────┐  ┌──────────┐  ┌─────────┐  │
  │ Web Audio├────POST─────┼─▶│ 鉴权/参数 │─▶│ MinIO 存 │  │ PG 元数据│ │
  │ Worklet  │  chunk      │  └──────────┘  │ 音频分片 │  └─────────┘  │
  └──────────┘             │       │        └──────────┘               │
  RTMP/HLS (ffmpeg 示例) ──┘       │ XADD                             ▲
                                   ▼                                  │
                          ┌──────────────────┐  XREADGROUP   ┌────────┴───────┐
                          │ Redis Stream     │──────────────▶│ ASR Workers    │
                          │ asr:tasks        │  消费组        │ Python/FastAPI │
                          └──────────────────┘               │                │
                                                             │ partial: 贪心  │
  观众浏览器                          ┌───────────────────────│ final: beam+MT │
  ┌────────────────────┐   WS 扇出    │ Redis                 │ PG 幂等写入     │
  │ 字幕叠加层/时间轴   │◀─────────────┤ Pub/Sub subtitles:*  └───────┬────────┘
  │ 指数退避重连+回放   │              │ ZSET  hot:* (replay)         │
  └────────────────────┘              └───────────────┬──────────────┘
                                                       │ final 同时写
                                              ┌────────▼─────────┐
                                              │ PostgreSQL       │
                                              │ sessions/chunks/ │
                                              │ subtitles        │
                                              └──────────────────┘
```

## 端到端流水线（单分片时序）

1. **采集**：浏览器 `getUserMedia` → `AudioContext(16kHz)` → `AudioWorklet` 累积固定 3 秒 → 下混单声道 → Float32 转 S16LE。不支持 Worklet 时回退 `ScriptProcessorNode`。
2. **上传**：`POST /sessions/:id/chunks?seq&startMs&endMs`，裸 PCM 作为 body。
3. **网关落存储**：音频字节存 MinIO（key 含 seq），分片元数据写 PG，任务 JSON `XADD` 到 `asr:tasks`。
4. **Worker 消费**：消费组 `XREADGROUP`（阻塞 5s），`XACK` 在处理成功后；worker 崩溃留下的 pending 消息由 `XAUTOCLAIM`（闲置 30s）重投 → **至少一次**，下游全部幂等。
5. **两阶段 ASR**：
   - partial：低算力（贪心 / beam=1）转写 → 只发 Pub/Sub，不翻译、不落库；
   - final：高质量（beam search）转写 → 逐目标语言翻译 → 先 upsert PG，再 Pub/Sub + ZSET 热字幕。
6. **网关扇出**：网关自身订阅 Redis 模式 `subtitles:*`，把消息扇出给本机该会话的所有 WebSocket 连接。网关无状态，可多副本（每个副本只持有自己机器上的连接）。
7. **前端展示**：final 覆盖同 seq 的 partial；按 `(seq,startMs)` 去重、排序；显示 queue/asr/e2e 延迟。
8. **断线重连**：WS 断开后指数退避（1s→2s→4s… 上限 15s），重连 URL 带 `replay=50`，网关回放 ZSET 最近 final 补齐缺口；也可用 REST 按 `beforeSeq` 翻历史。

## 关键取舍

- **为什么选 Redis Stream 而不用 RabbitMQ**：单中间件同时提供任务队列（消费组 + pending 重投）、Pub/Sub 扇出、ZSET 热字幕与 TTL，运维面最小；Stream 持久化 + AOF 满足“任务不丢”。需要更复杂路由/死信时可在 `queue` 包后替换为 RabbitMQ。
- **partial 为什么不翻译、不落库**：partial 会被同 seq 多次刷新，翻译成本高且价值低；final 是权威文本，落库与译文只在 final 通道发生。
- **为什么字幕回放放 Redis 而不是只查 PG**：重连是高频路径，ZSET 天然按时间排序、带 TTL 自动淘汰，避免热会话反复扫库；PG 作为长期/完整索引支持分页回溯。
- **时间戳语义**：`startMs/endMs` 来自采集端墙钟（直播相对时间线），worker 另发 `queueMs/asrMs/e2eMs` 表示流水线时延；前端用 `/time` 估算本机与服务端时钟偏差。
- **存储边界**：MinIO 只存音频对象；PG 只存元数据与 final；Redis 只存热数据与实时消息。三者职责不交叉。

## 水平扩展

- **ASR Worker**：所有实例使用同一消费组，Stream 在组内分摊分区；`docker compose --profile scale up` 可启动第二个 worker。
- **网关**：无状态，Pub/Sub 模式订阅让任意副本都能收到全量消息，再各自扇出本机连接。
- **前端**：nginx 静态文件 + API/WS 反代，可直接多副本。

## 扩展点（已预留接口）

- ASR：实现 `app/asr.py::BaseASR` 即可接入 FunASR / Azure Speech（`asr_funasr.py` / Azure SDK）。
- 翻译：实现 `translate.py::BaseTranslator`；已内置 LibreTranslate HTTP 后端。
- 接流：`examples/ingest_rtmp.py` 演示 RTMP/HLS 经 ffmpeg 拉 PCM 复用同一 HTTP 上传契约；如需服务端主动拉流，把它部署成 sidecar 即可。
- WebRTC：可用 mediasoup/LiveKit 收音频轨后，在服务端同样切 PCM 调 chunk 接口，下游零改动。
