.PHONY: dev web-dev web-install build cross-build clean

dev: ## 启动后端（读取 ./config.yaml）
	go run .

web-install: ## 安装前端依赖
	cd web && pnpm install

web-dev: ## 启动前端开发服务器（代理到 :8080）
	cd web && pnpm dev

build: ## 构建单二进制（前端 embed）
	sh build.sh

cross-build: ## 交叉编译 linux/amd64
	cd web && pnpm install && pnpm build && cd ..
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/token-gateway-linux-amd64 .

clean:
	rm -rf dist
