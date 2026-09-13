# ASR_PROVIDER=stub（默认，轻量）| whisper（含 faster-whisper 与 ffmpeg，体积大）
ARG ASR_PROVIDER=stub

FROM python:3.11-slim AS base

ARG ASR_PROVIDER
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    ASR_PROVIDER=${ASR_PROVIDER}

WORKDIR /app

RUN apt-get update && \
    apt-get install -y --no-install-recommends curl && \
    if [ "$ASR_PROVIDER" = "whisper" ]; then apt-get install -y --no-install-recommends ffmpeg; fi && \
    rm -rf /var/lib/apt/lists/*

# 国内构建可传 --build-arg PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple
ARG PIP_INDEX_URL=""

COPY requirements.txt requirements-whisper.txt ./
RUN pip install --upgrade pip && \
    if [ -n "$PIP_INDEX_URL" ]; then PIP_ARGS="-i $PIP_INDEX_URL"; fi && \
    pip install $PIP_ARGS -r requirements.txt && \
    if [ "$ASR_PROVIDER" = "whisper" ]; then pip install $PIP_ARGS -r requirements-whisper.txt; fi

COPY app ./app

EXPOSE 8000

HEALTHCHECK --interval=10s --timeout=3s --retries=12 \
    CMD curl -fsS http://localhost:8000/health || exit 1

CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000"]
