# openNexus 架构

本文档描述 openNexus 的高层架构：主要组件、职责划分以及交互方式。

## 概述

openNexus 是一个基于 Web 的多 Agent 编排平台。它连接各类 ACP 兼容 Agent（如 Claude Code、CodeBuddy、Kilo Code、Devin 等），并提供统一的界面来管理会话、流式对话、文件编辑、终端交互、定时任务、笔记和子 Agent。

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Web UI                                     │
│                  (React + Vite + TypeScript, web/)                      │
└─────────────────────────────────┬───────────────────────────────────────┘
                                  │ HTTP / WebSocket / SSE
┌─────────────────────────────────▼───────────────────────────────────────┐
│                           Go Backend (Gin)                              │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐  │
│  │  认证    │ │  Agent   │ │  会话    │ │ 工作空间 │ │   定时任务   │  │
│  │ 处理层   │ │ 处理层   │ │ 处理层   │ │ 处理层   │ │   处理层     │  │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────┬───────┘  │
│       └─────────────┴─────────────┴─────────────┴────────────┘          │
│                              业务服务层                                 │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐  │
│  │  认证    │ │   JWT    │ │  Agent   │ │  任务    │ │    笔记      │  │
│  │  服务    │ │  服务    │ │  路由    │ │ 调度器   │ │   分类器     │  │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────┬───────┘  │
│       └─────────────┴─────────────┴─────────────┴────────────┘          │
│                         数据访问层 (GORM)                               │
│                              SQLite                                     │
└─────────────────────────────────────────────────────────────────────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼             ▼             ▼
              ┌─────────┐   ┌─────────┐   ┌──────────┐
              │ ACP     │   │  MCP    │   │  文件    │
              │ Agents  │   │ Servers │   │ 终端     │
              └─────────┘   └─────────┘   └──────────┘
```

## 后端分层

### 入口

`cmd/server/main.go` 负责启动应用：

1. 加载配置（`internal/config`）
2. 执行旧数据自动迁移（`internal/config/migrate.go`）
3. 通过 GORM 初始化 SQLite（`internal/database`）
4. 构建服务实例（认证、JWT、调度器、Agent 路由、MCP 注册表）
5. 注册 HTTP 路由并启动 Gin 服务（`internal/router`）

### HTTP 层（`internal/handlers`）

按领域划分的 RESTful 与 WebSocket 处理器：

- **`auth_handler.go`** — 注册、登录、刷新、登出、个人资料
- **`agent_handler.go`** — Agent 列表、状态、模型、命令、模式、探测、预连接
- **`agent_config_handler.go`** — Agent 配置的增删改查、Registry 同步
- **`session_handler.go`** — 创建/恢复/删除会话、发送提示词、流式响应、权限管理
- **`session_file_handler.go`** — 会话工作区内文件的读写、对比、撤销、恢复
- **`workspace_handler.go`** — 工作空间的增删改查、状态保存、文件上传
- **`filesystem_handler.go`** — 目录/文件浏览，skill/command/rule/sub-agent 扫描
- **`scheduled_task_handler.go`** — 基于 Cron 的定时任务与执行历史
- **`note_handler.go`** — 笔记的增删改查、标签、Markdown 渲染
- **`mcp_handler.go`** — 全局 MCP 服务器配置
- **`terminal_handler.go`** — 基于 WebSocket 的 xterm 终端
- **`debug_handler.go`** — ACP 调试元数据、事件、原始消息

### 服务层（`internal/services`）

Handler 共享的业务逻辑：

- **认证 / JWT** — 密码哈希、Token 签发与校验
- **Agent 路由**（`internal/agent`）— ACP Agent 连接的注册与路由
- **调度器** — 定时任务的 Cron 管理
- **笔记分类器** — 可选的基于 Agent 的笔记自动分类

### ACP 集成（`internal/acp`）

- 管理每个 Agent 的子进程（npx、uvx、binary）
- 执行 ACP `initialize` 握手与认证
- 在每个 Agent 类型的单一 ACP 连接上多路复用会话
- 通过 SSE 流式输出工具调用、思考过程和最终回复
- 处理权限请求与用户审批
- 连接健康检查与自动重连

### MCP 集成（`internal/mcp`）

内置 MCP 服务器：

- **`notes/`** — 将笔记暴露为工具/资源
- **`subagent/`** — 将子 Agent 暴露为可调用的工具

全局 `mcpServers` 配置存储在 `~/.agents/mcp.json`（或配置路径）。

### 数据层（`internal/models`、`internal/repository`）

- GORM 模型：用户、会话、工作空间、定时任务、笔记等
- 默认数据库为 SQLite（可通过 `database.path` 配置）

## 前端

前端位于 `web/`，使用 Vite 构建。

主要模块：

- **会话 UI** — 聊天、流式输出、调试/日志面板、差异对比视图
- **文件浏览器** — 目录树、CodeMirror 编辑器
- **终端** — 通过 WebSocket 使用 xterm.js
- **设置** — Agent 配置、MCP 服务器、主题、语言
- **笔记** — 快速记录、标签筛选、Markdown 预览
- **定时任务** — Cron 编辑器、执行历史

## 协议

### Agent Client Protocol (ACP)

openNexus 作为 ACP 客户端运行。它启动 Agent 子进程，通过 `initialize` 协商能力，并通过 JSON-RPC 消息驱动会话。会话输出通过 Server-Sent Events (SSE) 流式推送到浏览器。

### Model Context Protocol (MCP)

MCP 服务器扩展了 Agent 的能力。openNexus 为笔记和子 Agent 提供内置 MCP 服务器，并支持用户配置的全局 MCP 服务器（`mcpServers`）。

## 部署模式

- **开发模式** — 后端（`:8008`）与 Vite 前端（`:3000`）分开运行
- **单端口生产模式** — 后端直接托管构建后的前端 `web/dist`
- **Docker** — 多阶段构建，同时包含前端与后端
- **桌面端** — 使用 Electron 包装 Web UI

## 配置

配置从 `config.yaml` 加载（搜索顺序：`CONFIG_PATH` → `~/.openNexus/config.yaml` → `./config.yaml`）。环境变量可覆盖大部分配置项。详见 [`development.md`](development.md) 和根目录 `README.zh-CN.md`。
