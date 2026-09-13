# ---- build stage ----
FROM golang:1.22-alpine AS builder

# 国内构建可传 --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} \
    CGO_ENABLED=0 \
    GOOS=linux

WORKDIR /src

# 先复制依赖清单以利用层缓存
COPY go.mod go.sum ./
RUN apk add --no-cache git && go mod download

COPY . .

RUN go build -ldflags="-s -w" -o /out/gateway ./cmd/server

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=builder /out/gateway /app/gateway

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/gateway"]
