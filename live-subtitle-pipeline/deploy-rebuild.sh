#!/usr/bin/env bash
# 强制重新构建并重启所有服务，确保容器/进程加载最新源码
# （解决“磁盘已是新代码、线上仍是旧二进制/旧 uvicorn”的问题）。
#
# 关键点：仅 `docker compose restart` 或 `up -d` 不会重新构建镜像，
# 旧二进制会继续运行；必须先 build 再 force-recreate。
set -euo pipefail
cd "$(dirname "$0")"

GW_PORT="${GATEWAY_PORT:-8080}"

echo "==> 无缓存构建 gateway / asr-worker / frontend（gateway 镜像内含 ffmpeg）"
docker compose build --no-cache gateway asr-worker frontend

echo "==> 强制重建容器"
docker compose up -d --force-recreate gateway asr-worker frontend

echo "==> 等待网关就绪"
GW_OK=0
for _ in $(seq 1 40); do
  if curl -fsS "http://localhost:${GW_PORT}/api/v1/health" >/tmp/gw-health.json 2>/dev/null; then
    GW_OK=1; break
  fi
  sleep 1
done
echo "gateway /health:"; cat /tmp/gw-health.json 2>/dev/null || echo "(unreachable)"; echo

echo "==> worker 版本 + VAD 自检（应 ok:true, quietSpeechDetected:true）"
curl -fsS http://localhost:8000/health   2>/dev/null | sed 's/.*/health:   &/' || echo "worker unreachable"
curl -fsS http://localhost:8000/selftest 2>/dev/null | sed 's/.*/selftest:  &/' || echo "selftest 404/不可达 => 仍是旧 worker"
echo

echo "==> 生效核对（期望值）"
echo "  gateway : version=2026.09.14-ingest2, migrations=3, ingestTable=true, ingestEnabled=true"
echo "  worker  : version=2026.09.14-vad2, /selftest ok=true"
echo
echo "  ingest 接口存在性（未带令牌应返回 401，而不是 404）："
	code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "http://localhost:${GW_PORT}/api/v1/sessions/probe/ingests" || true)
echo "    POST /sessions/:id/ingests -> HTTP $code （401=路由存在；404=仍是旧网关）"

if [ "$GW_OK" = 1 ]; then
  if grep -q '"ingestTable":true' /tmp/gw-health.json && grep -q '2026.09.14-ingest2' /tmp/gw-health.json; then
    echo
    echo "✅ 网关新版本 + 迁移 0003 已生效。"
  else
    echo
    echo "⚠️  网关版本/迁移未达预期，请查看上方 /health 与 docker compose logs gateway。"
  fi
fi
