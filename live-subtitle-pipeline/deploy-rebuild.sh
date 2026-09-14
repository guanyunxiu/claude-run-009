#!/usr/bin/env bash
# 强制重新构建并重启所有服务，确保容器/进程加载最新源码
# （解决“磁盘已是新代码、线上仍是旧二进制/旧 uvicorn”的问题）。
set -euo pipefail
cd "$(dirname "$0")"

echo "==> 无缓存构建 gateway / asr-worker / frontend"
docker compose build --no-cache gateway asr-worker frontend

echo "==> 强制重建容器"
docker compose up -d --force-recreate gateway asr-worker frontend

echo "==> 等待网关就绪并核对版本"
for i in $(seq 1 30); do
  if curl -fsS http://localhost:${GATEWAY_PORT:-8080}/api/v1/health >/tmp/gw-health.json 2>/dev/null; then
    break
  fi
  sleep 1
done
echo "gateway /health:"; cat /tmp/gw-health.json 2>/dev/null || echo "(unreachable)"
echo

echo "==> worker 版本 + VAD 自检（应 ok:true, quietSpeechDetected:true）"
curl -fsS http://localhost:8000/health  | sed 's/.*/health:  &/' || true
curl -fsS http://localhost:8000/selftest | sed 's/.*/selftest: &/' || true
echo

echo "期望：gateway version=2026.09.14-ingest、migrations=3、ingestTable=true；"
echo "      worker version=2026.09.14-vad2、selftest ok=true。"
