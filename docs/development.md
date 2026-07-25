# openNexus 开发指南

本指南介绍如何搭建本地开发环境、常用命令以及调试技巧。

## 前置条件

- Go >= 1.25
- Node.js >= 20
- npm 或 pnpm
- （可选）Docker 与 Docker Compose
- （可选）Rust + pnpm + `pake-cli@3.13.0`，用于打包桌面端
- 目标 Agent 所需的 API Key（例如 Claude Code 需要 `ANTHROPIC_API_KEY`）

## 快速开始

```bash
# 同时启动前后端开发服务器
make dev
```

- 后端：http://localhost:8080
- 前端：http://localhost:3000

首次使用需注册账号。

## 常用命令

| 命令 | 说明 |
|------|------|
| `make dev` | 同时启动前后端开发服务器 |
| `make backend` | 仅启动后端（http://localhost:8080） |
| `make frontend` | 仅启动前端（http://localhost:3000） |
| `make run` | 构建并以单端口生产模式运行 |
| `make build` | 构建前端和后端 |
| `make test` | 运行所有后端测试 |
| `make clean` | 清理构建产物 |

### Docker

| 命令 | 说明 |
|------|------|
| `make docker-build` | 构建 Docker 镜像 |
| `make docker-up` | 构建并启动容器（前台） |
| `make docker-up-d` | 构建并启动容器（后台） |
| `make docker-down` | 停止容器 |
| `make docker-logs` | 跟踪容器日志 |

### 桌面端

| 命令 | 说明 |
|------|------|
| `make pake` | 构建 Pake 桌面壳 |
| `make desktop` | 构建 macOS 桌面应用 |
| `make electron-dev` | 以开发模式运行 Electron |
| `make electron-dist` | 为当前平台打包 Electron 应用 |

## 项目结构

```
openNexus/
├── cmd/
│   ├── server/            # 后端入口
│   └── import-fleeting/   #  fleeting 笔记导入工具
├── internal/
│   ├── acp/               # ACP 协议集成
│   ├── agent/             # Agent 注册表与路由
│   ├── config/            # 配置加载与迁移
│   ├── database/          # 数据库连接
│   ├── handlers/          # HTTP/WebSocket 处理器
│   ├── logging/           # 日志中心与 SSE 流式推送
│   ├── mcp/               # MCP 服务器（笔记、子 Agent）
│   ├── middleware/        # JWT 认证中间件
│   ├── models/            # GORM 数据模型
│   ├── repository/        # 数据访问层
│   ├── router/            # 路由注册
│   ├── services/          # 业务服务
│   └── sysutil/           # 系统工具
├── web/                   # 前端（React + Vite）
├── electron/              # Electron 桌面客户端
├── scripts/               # 构建与打包脚本
├── docs/                  # 文档
├── config.yaml            # 默认配置
├── Dockerfile             # 多阶段构建
├── docker-compose.yml     # 容器编排
└── Makefile               # 常用命令
```

## 配置

后端读取 `config.yaml`。查找顺序：

1. 环境变量 `CONFIG_PATH`
2. `~/.openNexus/config.yaml`
3. `./config.yaml`（项目根目录）

常用环境变量覆盖：

| 变量 | 说明 |
|------|------|
| `SERVER_PORT` | 服务端口（默认 `8080`） |
| `SERVER_MODE` | `debug` 或 `release` |
| `DATABASE_PATH` | SQLite 数据库路径 |
| `JWT_SECRET` | JWT 签名密钥 |
| `ANTHROPIC_API_KEY` | Claude Code 的 API Key |
| `SKIP_DATA_MIGRATION` | 设为 `1` 跳过旧数据迁移 |

## 后端开发

```bash
# 仅运行后端
go run ./cmd/server

# 运行测试
go test ./...

# 运行指定包测试
go test ./internal/handlers
```

后端使用标准 Go 项目布局。处理器文件位于 `internal/handlers`，路由注册在 `internal/router/router.go`。

## 前端开发

```bash
cd web
npm install
npm run dev
```

前端源码位于 `web/src`。Vite 开发服务器会将 API 请求代理到后端。

## 调试

- **后端日志**：输出到 stdout；同时通过 SSE 推送到 UI 的日志面板
- **ACP 调试**：在 `config.yaml` 中设置 `debug.acp.enabled: true`（默认开启）。原始 JSON-RPC 流量保存到 `~/.openNexus/acp-debug/`
- **调试面板**：在会话中打开 Debug 标签，可查看 ACP 事件和原始消息
- **浏览器 DevTools**：开发模式下前端启用 source map

## 数据迁移

openNexus 启动时会自动将 `~/.nextAgent` 和 `~/.nexusagent` 的旧数据迁移到 `~/.openNexus`。迁移是幂等的，且不会中断启动。如需跳过：

```bash
SKIP_DATA_MIGRATION=1 ./opennexus
```

## 添加 Agent

1. 打开 **设置 → Agent**，启用目标 Agent。
2. 设置所需的环境变量（例如 `ANTHROPIC_API_KEY`）。
3. 后端会异步连接。查看侧边栏状态和后端日志确认。

## 常见问题

| 问题 | 解决方案 |
|------|----------|
| 8080/3000 端口被占用 | 运行 `make backend-stop` 或手动结束进程 |
| Agent 显示断开 | 检查 API Key 环境变量和后端 stderr 日志 |
| Alpine Docker 中 binary Agent 无法运行 | 使用 `npx` 分发方式，或选择基于 glibc 的基础镜像 |
| 前端无法访问后端 | 确认前后端开发服务器均启动，且 Vite 代理已配置 |

## 发布构建

推送 `v*` 标签会触发 GitHub Actions 发布 release 产物。本地发布构建：

```bash
make release
```

桌面打包请参考 Makefile 中的 desktop 和 electron 相关 target。
