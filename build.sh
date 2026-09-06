#!/bin/sh
# 构建单二进制：前端 build → go build（前端 embed 进二进制）
set -e
cd "$(dirname "$0")"

if [ ! -d web/node_modules ]; then
  (cd web && pnpm install)
fi
(cd web && pnpm build)

mkdir -p dist
CGO_ENABLED=0 go build -ldflags="-s -w" -o dist/token-gateway .
echo "构建完成: dist/token-gateway"
