VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags="-s -w -X main.version=$(VERSION)"

.PHONY: dev backend frontend build run run-bg stop test clean release release-dry docker-build docker-up docker-down docker-logs electron-dev electron-dist electron-install electron-uninstall

# 一键启动前后端开发服务器
# 用法: make dev
dev: backend-stop
	@bash -c 'trap "kill 0 2>/dev/null" INT TERM; \
		go run ./cmd/server </dev/null & \
		(cd web && npm run dev) </dev/null & \
		wait'

# 启动后端 (Go, :8080)
backend:
	@echo "==> 启动后端 http://localhost:8080"
	@go run ./cmd/server

# 启动前端 (Vite, :3000)
frontend:
	@echo "==> 启动前端 http://localhost:3000"
	@cd web && npm run dev

# 尝试停止已运行的后端进程（避免端口占用）
backend-stop:
	@-lsof -ti:8080 | xargs kill -9 2>/dev/null || true
	@-lsof -ti:3000 | xargs kill -9 2>/dev/null || true

# 构建前后端
build:
	@echo "==> 构建前端"
	@cd web && npm run build
	@echo "==> 构建后端 ($(VERSION))"
	@CGO_ENABLED=1 go build $(LDFLAGS) -o opennexus ./cmd/server

# 构建 MCP 聚合网关独立二进制（不依赖主 server / sqlite，可单独部署）
# 用法: make gateway
gateway:
	@echo "==> 构建 MCP 网关 ($(VERSION))"
	@CGO_ENABLED=0 go build $(LDFLAGS) -o opennexus-gateway ./cmd/gateway

# 使用 Pake (Tauri) 打包桌面客户端壳子
# 依赖: Rust + Node + pake-cli (pnpm install -g pake-cli@3.13.0)
# 用法: make pake
pake:
	@echo "==> 使用 Pake 打包桌面客户端"
	@./scripts/build-pake.sh dist $(VERSION)

# 完整 macOS 桌面应用打包（Go 后端 + Pake 客户端 → app bundle）
# 用法: make desktop
desktop:
	@echo "==> 1/3 构建前端"
	@cd web && npm run build
	@echo "==> 2/3 构建后端 binary"
	@mkdir -p dist
	@CGO_ENABLED=1 go build $(LDFLAGS) -o dist/opennexus ./cmd/server
	@echo "==> 3/3 Pake 客户端 + 组装 app bundle"
	@./scripts/build-pake.sh dist $(VERSION) app
	@./scripts/package-darwin.sh dist opennexus dist
	@echo ""
	@echo "✅ 桌面应用打包完成"
	@du -sh dist/openNexus.app
	@echo "   双击 dist/openNexus.app 启动"

# Linux amd64 桌面应用打包
# 用法: make desktop-linux
desktop-linux:
	@echo "==> 1/3 构建前端"
	@cd web && npm run build
	@echo "==> 2/3 构建后端 binary"
	@mkdir -p dist
	@CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/opennexus-linux-amd64 ./cmd/server
	@echo "==> 3/3 Pake AppImage + 组装桌面目录"
	@./scripts/build-pake.sh dist $(VERSION) appimage
	@./scripts/package-linux.sh dist opennexus-linux-amd64 dist
	@cd dist && tar czf opennexus-linux-desktop.tar.gz openNexus
	@echo ""
	@echo "✅ Linux 桌面应用打包完成: dist/opennexus-linux-desktop.tar.gz"

# Windows amd64 桌面应用打包（需在 Windows 或交叉编译环境运行 Pake 步骤）
# 用法: make desktop-windows
desktop-windows:
	@echo "==> 1/3 构建前端"
	@cd web && npm run build
	@echo "==> 2/3 构建后端 binary"
	@mkdir -p dist
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags=sqlite_nocgo $(LDFLAGS) -o dist/opennexus-windows-amd64.exe ./cmd/server
	@echo "==> 3/3 Pake 客户端 + 组装桌面目录（需在 Windows 上执行 Pake）"
	@./scripts/build-pake.sh dist $(VERSION) x64
	@pwsh -File ./scripts/package-windows.ps1 -OutDir dist -BinName opennexus-windows-amd64.exe -PakeDir dist
	@echo ""
	@echo "✅ Windows 桌面应用打包完成: dist/openNexus/"

# 开发模式：构建后以 release 模式启动并打开浏览器
# 用法: make run-desktop
run-desktop: build
	@echo "==> 单端口模式启动 http://localhost:8080"
	@SERVER_MODE=release ./opennexus --open

# 单端口运行：先构建前端，再以 release 模式启动后端（前端 + API 同端口）
# 用法: make run
run: build
	@echo "==> 单端口启动 http://localhost:8080"
	@SERVER_MODE=release ./opennexus

# 单端口后台运行：构建后以 release 模式后台启动，日志写入 opennexus.log
# 用法: make run-bg   停止: make stop
run-bg: build
	@-lsof -ti:8080 | xargs kill -9 2>/dev/null || true
	@echo "==> 后台启动 http://localhost:8080 (日志: opennexus.log)"
	@SERVER_MODE=release nohup ./opennexus > opennexus.log 2>&1 & echo $$! > opennexus.pid
	@sleep 1
	@if kill -0 $$(cat opennexus.pid) 2>/dev/null; then \
		echo "✅ 已后台启动 (PID: $$(cat opennexus.pid))"; \
	else \
		echo "❌ 启动失败，日志如下:"; tail -20 opennexus.log; rm -f opennexus.pid; exit 1; \
	fi

# 停止后台运行的服务
# 用法: make stop
stop:
	@if [ -f opennexus.pid ]; then \
		kill $$(cat opennexus.pid) 2>/dev/null && echo "✅ 已停止 (PID: $$(cat opennexus.pid))" || echo "ℹ️  进程已不存在"; \
		rm -f opennexus.pid; \
	else \
		lsof -ti:8080 | xargs kill 2>/dev/null && echo "✅ 已停止 8080 端口进程" || echo "ℹ️  无运行中的服务"; \
	fi

# 生产环境发布构建：跨平台编译
# 用法: make release
release:
	@echo "==> 构建前端"
	@cd web && npm run build
	@echo "==> 交叉编译"
	@mkdir -p dist
	@CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o "dist/opennexus-darwin-amd64" ./cmd/server
	@CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o "dist/opennexus-darwin-arm64" ./cmd/server
	@CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o "dist/opennexus-linux-amd64" ./cmd/server
	@CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o "dist/opennexus-linux-arm64" ./cmd/server
	@CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags=sqlite_nocgo $(LDFLAGS) -o "dist/opennexus-windows-amd64.exe" ./cmd/server
	@echo "==> 打包"
	cd dist && tar czf opennexus-darwin-amd64.tar.gz opennexus-darwin-amd64 && tar czf opennexus-darwin-arm64.tar.gz opennexus-darwin-arm64 && tar czf opennexus-linux-amd64.tar.gz opennexus-linux-amd64 && tar czf opennexus-linux-arm64.tar.gz opennexus-linux-arm64 && zip opennexus-windows-amd64.zip opennexus-windows-amd64.exe
	@echo "==> 创建 macOS 桌面应用包"
	@if command -v pake &>/dev/null; then \
		./scripts/build-pake.sh dist $(VERSION) app && \
		./scripts/package-darwin.sh dist opennexus-darwin-arm64 dist && \
		cd dist && tar czf opennexus-darwin-desktop.tar.gz openNexus.app; \
	else \
		echo "⚠️  未安装 pake-cli，跳过桌面客户端打包"; \
		echo "   安装: pnpm install -g pake-cli"; \
	fi
	@echo "==> 发布文件已生成到 dist/"
	@ls -lh dist/

# 运行后端全部测试
test:
	@go test ./...

# 清理构建产物
clean:
	@-rm -f opennexus
	@-rm -rf web/dist dist

# 构建 Docker 镜像（多阶段构建，含前端 + 后端）
# 用法: make docker-build
docker-build:
	@echo "==> 构建 Docker 镜像 opennexus:latest"
	@docker build -t opennexus:latest .

# 启动 docker-compose（前台，Ctrl+C 停止）
# 用法: make docker-up
docker-up: docker-build
	@echo "==> 启动容器 http://localhost:8080"
	@docker compose up

# 后台启动 docker-compose
# 用法: make docker-up-d
docker-up-d: docker-build
	@echo "==> 后台启动容器 http://localhost:8080"
	@docker compose up -d
	@docker compose ps

# 停止并清理 docker-compose 容器（保留数据卷）
# 用法: make docker-down
docker-down:
	@echo "==> 停止容器"
	@docker compose down

# 查看 docker-compose 日志（实时跟踪）
# 用法: make docker-logs
docker-logs:
	@docker compose logs -f

# ============================================================================
# Electron 桌面客户端（与 Pake 并存）
# 前端/后端均无改动：Electron 壳启动 Go 后端(release 模式)并加载同源页面。
# 依赖: Node >= 20 (首次运行会 npm install electron + electron-builder)
# ============================================================================

# 开发运行：先 make build 出后端二进制 + web/dist，再拉起 Electron 窗口
# 用法: make electron-dev
electron-dev: build
	@echo "==> 启动 Electron 客户端（开发模式）"
	@cd electron && npm install --no-audit --no-fund && npm start

# 打包当前平台桌面应用（macOS 产出 dmg / Linux 产出 AppImage / Windows 产出 nsis）
# 用法: make electron-dist
electron-dist: build
	@echo "==> 打包 Electron 桌面应用"
	@cd electron && npm install --no-audit --no-fund && npm run dist
	@echo "✅ Electron 桌面应用: electron/dist/"

# 安装到本机（macOS：复制到 /Applications；未签名应用会自动清除隔离属性）
# 用法: make electron-install
electron-install: electron-dist
ifeq ($(shell uname -s),Darwin)
	@echo "==> 安装 openNexus 到 /Applications"
	@osascript -e 'quit app "openNexus"' 2>/dev/null || true
	@sleep 1
	@bash -c '\
		APP_SRC=$$(ls -d electron/dist/mac-*/openNexus.app 2>/dev/null | head -1); \
		if [ -z "$$APP_SRC" ]; then echo "❌ 未找到打包产物，请先 make electron-dist"; exit 1; fi; \
		echo "   源: $$APP_SRC"; \
		if [ -d /Applications/openNexus.app ]; then \
		  if [ -w /Applications ]; then rm -rf /Applications/openNexus.app; \
		  else sudo rm -rf /Applications/openNexus.app; fi; \
		fi; \
		if [ -w /Applications ]; then cp -R "$$APP_SRC" /Applications/; \
		else echo "   /Applications 需要管理员权限:"; sudo cp -R "$$APP_SRC" /Applications/; fi; \
		xattr -cr /Applications/openNexus.app 2>/dev/null || sudo xattr -cr /Applications/openNexus.app; \
		/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister \
		  -f /Applications/openNexus.app 2>/dev/null || true; \
		echo "✅ 已安装到 /Applications/openNexus.app"; \
		echo "   启动: make electron-run  或  open /Applications/openNexus.app"'
else
	@echo "❌ make electron-install 仅支持 macOS，Linux 请用 make electron-dist 产出 AppImage 后直接运行"
endif

# 卸载已安装的 openNexus
# 用法: make electron-uninstall
electron-uninstall:
ifeq ($(shell uname -s),Darwin)
	@-osascript -e 'quit app "openNexus"' 2>/dev/null || true
	@if [ -d /Applications/openNexus.app ]; then \
	  if [ -w /Applications ]; then rm -rf /Applications/openNexus.app; \
	  else sudo rm -rf /Applications/openNexus.app; fi; \
	  echo "✅ 已卸载 /Applications/openNexus.app"; \
	else echo "ℹ️  /Applications/openNexus.app 不存在，无需卸载"; fi
else
	@echo "❌ make electron-uninstall 仅支持 macOS"
endif

# 启动已安装到 /Applications 的 openNexus
# 用法: make electron-run
electron-run:
ifeq ($(shell uname -s),Darwin)
	@if [ -d /Applications/openNexus.app ]; then \
	  open /Applications/openNexus.app; \
	  echo "✅ 已启动 /Applications/openNexus.app"; \
	else echo "❌ 未安装，请先 make electron-install"; fi
else
	@echo "❌ make electron-run 仅支持 macOS"
endif
