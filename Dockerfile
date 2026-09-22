# 多阶段构建：前端 → Go 二进制 → 运行镜像
# ---------- 阶段 1：前端 ----------
FROM node:20-alpine AS web
WORKDIR /web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ---------- 阶段 2：Go ----------
FROM golang:1.26-alpine AS build
WORKDIR /src
# 国内网络构建：docker compose build --build-arg GOPROXY=https://goproxy.cn,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/token-gateway .

# ---------- 阶段 3：运行 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata sqlite && adduser -D -u 10001 tg
WORKDIR /app
COPY --from=build /out/token-gateway ./token-gateway
# 镜像内置默认配置（与 example 一致）；敏感项一律由 TG_* 环境变量覆盖（见 docker-compose.yml）
COPY config.example.yaml ./config.yaml
USER tg
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["./token-gateway"]
CMD ["-config", "config.yaml"]
