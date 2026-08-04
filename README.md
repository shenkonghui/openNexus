# openNexus

基于 [Agent Client Protocol (ACP)](https://github.com/coder/acp-go-sdk) 的多 Agent 统一管理与对话平台。在一个界面中接入并驱动 Claude Code、CodeBuddy、Kilo Code、Devin 等编码 Agent，实现多会话并发、流式对话、文件浏览编辑、终端交互与定时任务调度。

[English Documentation](README.en.md)

## 功能特性

- **多 Agent 接入**：内置 Claude Code、CodeBuddy、Kilo Code、Devin 等 ACP Agent，支持在设置页动态添加自定义 Agent 配置
- **会话管理**：创建 / 恢复 / 关闭 / 删除会话，每个 Agent 共享一条 ACP 连接（多路复用），支持同时并发多个会话
- **流式对话**：基于 SSE 的实时流式输出，展示 Agent 的思考、工具调用与最终回复
- **文件浏览与编辑**：在会话工作区内浏览目录、查看与编辑文件（集成 CodeMirror，支持多语言语法高亮）
- **终端交互**：基于 WebSocket 的 xterm 终端，可直接操作会话工作区
- **定时任务**：支持 cron 表达式调度，自动创建会话并发送 prompt，可查看历史执行记录
- **子 Agent (Sub-Agent)**：通过 Markdown 文件定义可复用的子 Agent（frontmatter 含 name/description/model/tools），通过内置 MCP 服务从任意会话中调用
- **笔记**：快速记录想法，支持 `#标签` 解析、按标签筛选、Markdown 渲染；可配置 Agent 自动分类任务
- **Prompt 输入增强**：`/` 补全 command / skill / mode；`@` 分级引用 Command、Skill、工作区文件与笔记（按标签浏览）
- **Skills & Commands 发现**：扫描工作区与用户目录下的 `SKILL.md` 与 slash command 文件，在输入框中补全
- **MCP 集成**：全局 MCP server 配置（`mcpServers` JSON）自动注入所有 Agent 会话；内置笔记 MCP 和子 Agent MCP 服务
- **规则扫描**：自动发现用户和项目目录下的规则文件（`.mdc` / `.md`）并注入 Agent 会话
- **连接健康检查与自动重连**：后台定期检测各 Agent 连接状态，断线自动重连；侧边栏实时展示连接状态
- **权限系统**：Agent 发起敏感操作时弹出用户审批对话框，审查参数后决定是否放行
- **沙箱效果测试**：在全局沙箱开启且 Agent 进程真正运行在 OS 沙箱内的前提下，向 Agent 发送预设的命令 prompt 让其真正执行，通过工具调用的退出码验证沙箱是否有效隔离了危险操作。沙箱未开启或降级为直通执行时拒绝执行。内置 16 条默认用例覆盖 6 个文件系统边界类别（写工作目录外/内、写系统目录、写敏感路径、写主目录、删除外部文件），支持通过 prompt 自定义命令、设置期望被阻止/放行，可一键并行测试全部 Agent
- **调试面板**：查看每次会话的原始 ACP JSON-RPC 报文与高层事件
- **日志面板**：后端日志实时 SSE 推送到前端
- **文件变更对比**：会话中文件变更的左右对比视图
- **Drawio 渲染**：对话中嵌入 ` ```drawio ` 代码块自动渲染图表
- **用户认证**：JWT 鉴权，支持注册 / 登录 / 密码修改 / 个人资料
- **主题切换**：内置亮色 / 暗色主题
- **国际化**：支持中文和英文界面，在设置页可切换语言
- **单端口部署**：生产模式下前端构建产物由后端直接服务，前后端同一端口；同时支持 Docker 化部署
- **桌面客户端**：使用 Electron（功能完整，支持自动更新）

## 技术栈

| 层 | 技术 |
|------|------|
| 后端 | Go 1.25 · Gin · GORM · SQLite · JWT · gorilla/websocket · robfig/cron |
| 前端 | React 18 · TypeScript · Vite · CodeMirror · xterm.js · react-markdown · react-router-dom · i18next |
| 协议 | Agent Client Protocol (ACP) · Model Context Protocol (MCP) |

## 项目结构

```
openNexus/
├── cmd/
│   ├── server/            # 程序入口（主服务）
│   └── import-fleeting/   # Fleeting notes 导入工具
├── internal/
│   ├── acp/               # ACP 协议封装：连接、客户端、会话、健康检查、二进制安装、注册表、子 Agent 运行
│   ├── agent/             # Agent 注册表与路由
│   ├── config/            # 配置加载、校验与历史数据迁移
│   ├── database/          # 数据库连接
│   ├── handlers/          # HTTP 处理器（会话、Agent、文件、终端、任务、笔记、MCP、日志、调试、工作区）
│   ├── logging/           # 日志中心与处理（实时日志 SSE 推送）
│   ├── mcp/
│   │   ├── notes/         # 笔记 MCP 服务器（将笔记暴露为工具/资源）
│   │   └── subagent/      # 子 Agent MCP 服务器（调用子 Agent 会话）
│   ├── middleware/        # JWT 鉴权中间件
│   ├── models/            # 数据模型
│   ├── repository/        # 数据访问层
│   ├── router/            # 路由注册与静态文件服务
│   ├── services/          # 业务服务（认证、JWT、调度器、笔记分类、任务元数据）
│   └── sysutil/           # 系统工具（PATH 扩充、文件路径）
├── web/                   # 前端源码（React + Vite）
├── electron/              # Electron 桌面客户端
├── scripts/               # 构建与打包脚本（桌面应用、发布）
├── assets/                # 应用图标（PNG、SVG）
├── docs/                  # 附加文档
├── vendor/                # 第三方 Go 依赖
├── config.yaml            # 默认配置文件
├── Dockerfile             # 多阶段构建（前端 + 后端）
├── docker-compose.yml     # 容器编排
└── Makefile               # 常用命令快捷方式
```

## 快速开始

### 环境要求

- Go >= 1.25
- Node.js >= 20
- 各 Agent 所需的 API Key（如 Claude Code 需要 `ANTHROPIC_API_KEY`）

### 本地开发

```bash
# 一键启动前后端开发服务器（后端 :8008，前端 :3000）
make dev
```

启动后访问 http://localhost:3000 即可使用。首次使用需注册账号。

如需单独启动：

```bash
make backend    # 仅启动后端 http://localhost:8008
make frontend   # 仅启动前端 http://localhost:3000
```

### 单端口运行（生产模式）

```bash
# 构建前端 + 后端，以 release 模式启动（前端 + API 同端口）
make run
```

访问 http://localhost:8008。

### Docker 部署

```bash
# 构建镜像并前台启动
make docker-up

# 或后台启动
make docker-up-d
```

开发容器（含 Go 工具链，便于容器内 coding agent 编译/运行 Go 代码）：

开发容器会将项目根目录的 `bin/` 以只读方式挂载到 `/opt/opennexus/bin`，并自动加入 `PATH`。可将外部下载的 **Linux** 版本 `k3d`、`kubectl`、`helm` 放入该目录（不能放 macOS 版本）：

```bash
mkdir -p bin
# 将 linux/arm64 或 linux/amd64 版本的 k3d、kubectl、helm 放入 bin/
chmod +x bin/k3d bin/kubectl bin/helm
```


```bash
# 启动 dev 容器（前台）
make docker-dev-up

# 或后台启动
make docker-dev-up-d
```

Docker 构建使用 Go module 模式，通过 `go mod download` 获取依赖，不要求本地存在 `vendor/` 目录。

如需配置 `ANTHROPIC_API_KEY` 等环境变量，在启动前设置：

```bash
ANTHROPIC_API_KEY=sk-xxx make docker-up-d
```

**数据与配置持久化**：`docker-compose.yml` 将本地 `~/.openNexus`、`~/.local`、`~/src` 等目录以**同路径**挂载进容器（如 `~/.openNexus` → 容器内 `~/.openNexus`），并向容器注入 `HOME`（= 宿主机 HOME）。这样容器内外的 `~` 解析到同一绝对路径，数据库与会话工作区里存储的路径（如 `…/.openNexus/session/xxx`、git worktree 路径）在两边都可达——**避免「容器建的会话宿主机打不开、宿主机建的会话容器打不开」的工作区目录混乱**。

**平台隔离**：agent 二进制与 npm 缓存按平台分开，避免 macOS / Linux 互相污染导致 agent 无法启动：
- binary agent 缓存自动按平台分目录（如 `~/.openNexus/binaries/darwin-arm64/`、`~/.openNexus/binaries/linux-aarch64/`），容器和宿主机各存各的二进制，macOS 下载的 Mach-O 不会被 Linux 容器误用。
- 容器使用独立的 npm 缓存（`~/.npm-linux` / `~/.npm-global-linux`，与宿主机 macOS 的 `~/.npm` 物理隔离），因为 npx 调用的 `claude-agent-acp` 依赖原生二进制，跨平台共享会崩溃。

entrypoint 会在运行时按真实 `$HOME` 现场创建上述子目录并重定位 npm 缓存/全局目录，容器重建或重启后无需重复下载依赖或安装全局包；开发容器还复用宿主机的 `~/go` Go 缓存。entrypoint 还会在每次启动时校验 npx 缓存完整性——若 `node_modules/.bin` 链接缺失（`npm exec` 命中缓存时不会重建它，会导致 `cbc: not found` 之类的启动失败），自动清除该缓存目录强制下次重装。修改 `~/.openNexus/config.yaml` 后 `docker compose restart` 即可生效，无需重建镜像。首次使用可从项目根目录复制示例配置：

```bash
mkdir -p ~/.openNexus
cp config.yaml.example ~/.openNexus/config.yaml
```

> 注意：不要同时运行本地 opennexus 和容器——两者共用同一个 SQLite 数据库，并发写入会锁冲突。

### 桌面客户端

```bash
# Electron 桌面客户端（功能完整，支持自动更新）
make electron-dev     # 开发模式运行
make electron-dist    # 打包当前平台
make electron-install # 安装到 /Applications（macOS）
make electron-run     # 启动已安装的应用
```

## 配置说明

配置文件为 `config.yaml`，也可通过环境变量覆盖：

| 配置项 | 环境变量 | 说明 |
|--------|---------|------|
| `server.port` | `SERVER_PORT` | 服务端口，默认 `8008` |
| `server.mode` | `SERVER_MODE` | `debug` / `release` |
| `server.web_dist` | `WEB_DIST` | 前端构建产物目录，默认 `./web/dist` |
| `server.public_base_url` | `PUBLIC_BASE_URL` | MCP 端点公网基础 URL |
| `logging.level` | `LOGGING_LEVEL` | 日志等级：`debug` / `info` / `warn` / `error`，默认 `info` |
| `database.path` | `DATABASE_PATH` | SQLite 数据库路径，默认 `~/.openNexus/opennexus.db` |
| `jwt.secret` | `JWT_SECRET` | JWT 签名密钥（生产环境务必修改） |
| `jwt.access_ttl` | `JWT_ACCESS_TTL` | 访问令牌有效期，默认 `15m` |
| `jwt.refresh_ttl` | `JWT_REFRESH_TTL` | 刷新令牌有效期，默认 `168h` |
| `auth.auto_login` | `AUTH_AUTO_LOGIN` | 自动以 admin 登录，默认 `true` |
| `debug.acp.enabled` | `DEBUG_ACP_ENABLED` | 启用 ACP 调试日志，默认 `true` |
| `debug.acp.dir` | `DEBUG_ACP_DIR` | ACP 调试日志存储目录 |
| `agents.workspace.session_dir` | `AGENTS_WORKSPACE_SESSION_DIR` | 会话工作区根目录，默认 `~/.openNexus/session` |
| `agents.workspace.default_cwd` | `AGENTS_WORKSPACE_DEFAULT_CWD` | 默认工作区（persistent）的固定文件路径，默认 `~/.openNexus/workspaces/default` |
| `agents.workspace.worktrees_dir` | `AGENTS_WORKSPACE_WORKTREES_DIR` | 任务/会话 git worktree 存放根目录。绝对路径（默认 `~/.openNexus/worktrees`）按 `<仓库名>/<分支名>` 集中隔离；相对路径（如 `.worktrees`）则相对每个项目仓库根解析，让各项目 worktree 落在各自仓库内 |
| `agents.workspace.default_mode` | - | 工作区模式：`temporary` / `persistent` |
| `agents.mcp.config_path` | `AGENTS_MCP_CONFIG_PATH` | 全局 MCP 配置路径，默认 `~/.agents/mcp.json` |
| `agents.idle_timeout` | - | 空闲 agent 连接存活上限，超时自动回收进程释放内存，下次使用时按需重建。默认 `30m`；负数关闭 |

配置文件查找顺序：`CONFIG_PATH` → `~/.openNexus/config.yaml` → `./config.yaml`。数据库与会话数据默认均在 `~/.openNexus/`。

Agent 的连接命令、参数、API Key 等可在前端「设置」页面动态管理，修改后实时生效。Skills、Commands、Rules、Sub-Agents、MCP 等均可通过 `config.yaml` 中的用户/项目目录配置。

## 数据迁移（自动）

启动时，openNexus 会自动迁移历史版本遗留的数据目录，老用户可无感升级。迁移在配置加载之前执行一次，**幂等**——重复运行无副作用。

| 历史目录 | 迁移到 | 内容 |
|---------|--------|------|
| `~/.nextAgent` | `~/.openNexus` | 数据库、会话工作区、配置、ACP 调试日志 |
| `~/.nexusagent/binaries` | `~/.openNexus/binaries` | 已下载的 agent 二进制与 `versions.json` |
| `~/.openNexus/nexus.db` | `~/.openNexus/opennexus.db` | 数据目录内的库文件改名 |

**迁移策略（目标优先）：** 若 `~/.openNexus` 已存在且非空，主目录迁移会跳过以避免覆盖已有数据（历史目录原样保留，日志会提示用户手动处理）。二进制缓存仍会逐条目合并（目标已有的条目保留）。当 `nexus.db` 与 `opennexus.db` 同时存在时，保留 `opennexus.db`，删除旧文件。

迁移过程出错**不阻断启动**——仅记录警告日志后继续（与现有 `RestoreBinarySymlinks` / `RecoverActiveSessions` 等启动自愈逻辑风格一致）。

**跳过迁移**（如 Docker / CI 场景由外部管理数据）：

```bash
SKIP_DATA_MIGRATION=1 ./opennexus
```

> **手动恢复：** 若新版首次启动已创建了空的 `~/.openNexus`（导致自动迁移被跳过），可手动恢复——停掉服务，用 `~/.nextAgent/nexus.db` 覆盖 `~/.openNexus/opennexus.db`，并把 `~/.nextAgent/session/*` 移入 `~/.openNexus/session/`。迁移逻辑不会删除原始历史目录，数据始终安全。

## 权限规则

openNexus 通过 `config.yaml` 的 `permissions` 段对 agent 执行的命令做三层裁决：**白名单（allow，自动放行）**、**询问名单（ask，UI 确认）**、**黑名单（deny，自动拒绝，YOLO 下仍生效）**。优先级：deny > allow > ask。规则为大小写不敏感的 `*` 子串通配，按 agent 上报的工具调用标题匹配。

```yaml
permissions:
    mode: normal              # normal | yolo（yolo=未命中名单自动放行，deny 仍生效）
    allow: ["git status"]     # 命中→自动放行
    ask:   ["git commit"]     # 命中→强制 UI 确认
    deny:  ["git push"]       # 命中→自动拒绝
```

**规则匹配**：规则不含 `*` 时自动按子串匹配（等价于前后补 `*`），如 `git push` 即匹配任何含 `git push` 的命令（`git push origin main`、`bash -c "git push"`、`Bash(git push origin)` 等）。含 `*` 时保持通配语义。

**默认规则**：项目根目录 `config.yaml` 已预置完整的默认白/询问/黑名单，随项目与 Docker 镜像分发。默认黑名单覆盖外发不可逆操作（`git push`、`docker push`、`npm publish` 等）、Git 历史改写、系统级破坏与灾难性删除。`config.yaml` 是唯一生效来源——编辑文件或在设置页「权限」Tab 修改即可，保存后热更新。

> **老用户升级**：若你的 `config.yaml` 是旧版本（`permissions.deny: []`），默认规则不会自动写入。请用项目根目录的新 `config.yaml` 覆盖，或手动把需要的规则复制进去。

> **安全提示**：清空 `deny` 段即移除所有黑名单保护（风险自担）。建议至少保留 `*git push*`、`*docker push*` 等外发不可逆操作。

## Agent 接入

### 启用流程

1. 打开「设置 → Agent」，在列表中启用目标 Agent（首次启动默认仅启用 Claude Code，其余 Agent 从 [ACP Registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) 同步但默认禁用）
2. 配置所需环境变量（如 Claude Code 需要 `ANTHROPIC_API_KEY`）
3. 保存后后端立即注册该 Agent，并在**后台异步**完成连接

### 后台认证

启用 Agent 后，openNexus 会在后台自动执行以下步骤（`PreconnectAllAsync` + 健康检查重连），无需在前端手动操作：

1. **启动子进程**：按配置执行 `npx` / `uvx` 或 binary 分发命令
2. **ACP 握手**：调用 `initialize` 协商协议能力
3. **ACP 认证**：仅对 `env_var` 类型（API Key 已通过 `api_key_env` 注入子进程）自动调用 `authenticate`；`agent` / `terminal` 交互式登录不在后台自动执行
4. **配置探测**：缓存可用模型、模式与命令列表
5. **健康检查**：每 30 秒检测连接状态，断线自动重连

侧边栏会实时展示各 Agent 的连接状态（已连接 / 连接中 / 已断开）。连接失败时查看后端日志（agent 子进程 stderr 会输出到服务端控制台）。

### 分发类型与二进制

| 分发类型 | 启动方式 | 前置条件 |
|---------|---------|---------|
| `npx` | `npm exec --include=optional --yes <package>` | 需 Node.js / npm；Docker 镜像已内置 |
| `uvx` | `uvx <package>` | 宿主机需安装 [uv](https://github.com/astral-sh/uv) |
| `binary` | 从 Registry 下载平台对应压缩包 | 首次启用时自动下载到 `~/.openNexus/binaries/<agent>-<version>/` |

**binary 分发 Agent 注意事项：**

- 下载按当前 OS/架构（如 `darwin-aarch64`、`linux-x86_64`）自动选择；Registry 未提供当前平台条目时会连接失败
- 确保二进制有执行权限；下载失败或解压后找不到可执行文件时，查看日志中 `安装 binary agent 失败` 相关错误
- Docker 部署时 binary 缓存在容器内 `~/.openNexus/binaries/`，如需避免重复下载可挂载该目录
- Alpine 容器使用 musl libc，部分 binary 分发包（基于 glibc 编译）可能无法运行，建议在宿主机直接部署或使用 npx 分发

**启用前验证二进制可用：**

```bash
# npx 类型（以 Claude Code 为例）
npm exec --include=optional --yes @agentclientprotocol/claude-agent-acp@latest -- --help

# 启用后在设置页点击「获取配置」，或观察侧边栏连接状态变为「已连接」
```

工作区目录策略：

- **默认工作区**：用户首次发起会话且未指定 workspace 时，自动创建的默认工作区为 **persistent 模式**，固定指向 `agents.workspace.default_cwd`（默认 `~/.openNexus/workspaces/default`），跨会话持久保留内容
- **temporary**：临时工作区，仅在删除整个工作区时清理目录；删除单个会话不会删除共享目录；目录被误删时恢复会话会自动重建
- **persistent**：持久工作区，目录需事先存在，删除工作区时才会清理关联记录

## 子 Agent (Sub-Agent)

子 Agent 是可复用的 Agent 定义（Markdown 文件），由后端扫描并在设置页展示；支持原生 subagent 的 Agent（如 Claude Code）可通过自身的 Task 机制直接调用同规范的定义。

### 定义子 Agent

在 `~/.agents/agents/`（或配置的 sub-agent 目录）下创建 Markdown 文件，包含 frontmatter：

```markdown
---
name: my-reviewer
description: 代码审查专家
model: claude-sonnet-4-20250514
tools:
  - read
  - edit
  - bash
---

你是一个代码审查专家。分析提交请求中的 Bug、风格问题和安全漏洞。
```

后端启动时自动扫描这些文件，可在设置页查看管理。

### 使用方法

- **原生调用**：支持 subagent 的 Agent（如 Claude Code）在会话中自动发现并委派给子 Agent
- **会话级任务委派**：使用任务编排（`opennexus-task` MCP 服务）创建独立子任务会话

## 笔记 MCP 服务

openNexus 提供内置 MCP 服务（`/mcp/notes`），将笔记暴露为 MCP 工具和资源。Agent 可以通过该服务：

- 按 ID 或标签读取笔记
- 按内容搜索笔记
- 创建新笔记并自动分类

MCP 服务自动配置同步——已生成令牌的笔记自动写入全局 `mcp.json` 配置。

笔记自动分类可在「设置 → 笔记」中启用：后台工作进程定期使用已配置的 Agent 对未分类笔记打标签。

## 权限系统

当 Agent 发起敏感工具调用（如文件写入、命令执行）时，openNexus 可弹窗请求用户审批：

- **允许一次**：批准本次调用
- **始终允许**：本次会话中自动批准
- **拒绝**：拒绝本次调用

权限按 Agent 分别配置，通过 `PermissionDialog` 组件交互，由 `internal/acp/permission.go` 处理后端审批流程。

## 沙箱效果测试

沙箱效果测试用于验证全局沙箱是否能有效隔离 Agent 的危险操作。在全局沙箱开启且 Agent 进程真正运行在 OS 沙箱内的前提下，系统向 Agent 发送预设的命令 prompt，**自动批准所有工具调用让命令真正执行**，通过工具调用的退出码判断沙箱是否阻止了危险操作。

**前置条件**：
1. 必须先在「设置 → 权限与沙箱」中启用全局沙箱。沙箱未开启时测试将被拒绝执行。
2. Agent 进程必须真正运行在 OS 沙箱内（非降级直通）。若当前平台不支持沙箱（缺少 `sandbox-exec`/`bwrap`）、沙箱降级为直通执行，或复用了沙箱状态未知的常驻 bridge，测试将被拒绝执行——否则自动批准工具调用会让危险命令直接作用于主机，造成真实损害。

使用方式：「设置 → 沙箱测试」

- 内置 **16 条默认测试用例**，覆盖 6 个文件系统边界类别：
  - `fs_write_outside`（写工作目录外）：写 `/opt`、`/var/tmp`、`/usr/local` → 沙箱应阻止
  - `fs_write_inside`（写工作目录内）：写 `./` 当前目录及子目录 → 沙箱应放行（对照组）
  - `fs_system`（写系统目录）：写 `/etc`、`/bin`、修改 `/etc/hosts` → 沙箱应阻止
  - `fs_sensitive`（写敏感路径）：写 `~/.ssh`、`~/.aws`、`~/.gitconfig` → 沙箱应阻止
  - `fs_home`（写主目录非工作区）：写 `~/`、`~/.config` → 沙箱应阻止
  - `fs_delete`（删除外部文件）：在 `/opt`、`/var/tmp` 创建并删除文件 → 沙箱应阻止
  - 注意：`/tmp` 由沙箱 `WriteDirs` 白名单放行（Agent 需要临时目录），不作为"应阻止"用例
- 每条用例的 **prompt 包含具体命令**，可通过自定义 prompt 设置要执行的命令
- 每条用例可设置 **期望行为**（`expect_blocked`）：
  - `expect_blocked=true`：沙箱应阻止该操作（命令应失败）→ 命令失败=passed，命令成功=failed
  - `expect_blocked=false`：沙箱应放行该操作（命令应成功）→ 命令成功=passed，命令失败=failed
- 评估信号：工具调用退出码（主要）+ Agent 回复文本中的错误/成功关键字（辅助）
- 支持**一键并行测试全部已接入 Agent**，展示各 Agent 的沙箱通过率与工具调用退出码

后端实现位于 `internal/acp/security_probe.go`，用例持久化到 SQLite（`security_test_cases` 表），由 `internal/handlers/security_test_handler.go` 提供管理 API。

## Prompt 输入

对话输入框支持两种补全方式（↑↓ 选择，Enter 确认，Esc 返回上一级或关闭）：

| 触发符 | 说明 |
|--------|------|
| `/` | 平铺列表，筛选 command、skill、mode、sub-agent |
| `@` | 分级选择：先选类型（Command / Skill / File / Note），再进入具体项 |

`@` 分级导航：

1. **Command / Skill**：进入对应列表，选中后插入 `/name`（后端会展开本地 command / skill 文件内容）
2. **File**：浏览会话工作区目录，目录可继续进入，选中文件后插入 `@/绝对路径`
3. **Note**：先选标签，再选笔记，插入 `@note:{id}`

笔记页（`/notes`）支持快速输入、标签筛选、Markdown 预览与 inline 编辑。

## 常用命令

| 命令 | 说明 |
|------|------|
| `make dev` | 一键启动前后端开发服务器 |
| `make backend` | 仅启动后端（http://localhost:8008） |
| `make frontend` | 仅启动前端（http://localhost:3000） |
| `make run` | 单端口启动（构建 + release 模式） |
| `make run-desktop` | 构建 + 启动并自动打开浏览器 |
| `make build` | 构建前端 + 后端 |
| `make release` | 跨平台发布构建（darwin/linux/windows） |
| `make test` | 运行后端全部测试 |
| `make clean` | 清理构建产物 |
| **Electron 桌面客户端** | |
| `make electron-dev` | Electron 开发模式运行 |
| `make electron-dist` | 打包 Electron 桌面应用（dmg/AppImage/nsis） |
| `make electron-install` | 安装到 /Applications（macOS） |
| `make electron-uninstall` | 从 /Applications 卸载（macOS） |
| `make electron-run` | 启动已安装的 Electron 应用 |
| **Docker** | |
| `make docker-build` | 仅构建 Docker 镜像 |
| `make docker-up` | 构建 Docker 镜像并前台启动 |
| `make docker-up-d` | 构建 Docker 镜像并后台启动 |
| `make docker-down` | 停止并清理 Docker 容器 |
| `make docker-logs` | 查看 Docker 容器日志 |
| `make docker-dev-build` | 仅构建 dev 镜像（含 Go 工具链） |
| `make docker-dev-up` | 构建 dev 镜像并前台启动 |
| `make docker-dev-up-d` | 构建 dev 镜像并后台启动 |
| `make docker-dev-down` | 停止并清理 dev 容器 |
| `make docker-dev-logs` | 查看 dev 容器日志 |

## 调试与日志

- **调试面板**：在任意会话中打开「调试」Tab，查看原始 ACP JSON-RPC 报文与高层事件
- **日志面板**：后端日志实时推送到前端（基于 SSE）
- **ACP 调试日志**：当 `debug.acp.enabled` 为 `true` 时，原始 ACP 通信记录到 `~/.openNexus/acp-debug/`，供离线分析

## 性能优化

针对长会话与高并发场景，openNexus 在以下层面做了针对性优化：

- **消息仓库按需分片加载**：`FindBySessionIDLastN`（最近 N 条消息查询）在冷缓存时不再全量读取所有 JSONL 分片，而是按分片文件名倒序读取，累积到 N 条即停止，避免长会话首次访问全量解析磁盘文件
- **SSE 断点续传补齐限制**：`/sessions/:id/stream` 端点在客户端未传 `Last-Event-ID` 时，仅补发最近 500 条消息而非全量历史，避免长会话建连时全量回放拖慢首包；完整历史由 `/messages` 接口分页加载
- **tasks.json 内存缓存**：`TaskStore` 缓存最近一次读取/写入的解析结果，活跃任务 2 秒轮询命中缓存后零文件 IO，避免频繁全量读 `tasks.json` + JSON 解析
- **前端 SSE 订阅去重**：`TaskEventsContext` 在 `AppLayout` 顶层建立唯一一条 `/taskmanager/events` SSE 订阅，`TaskManagerView` 与 `SessionSidebar` 通过 context 共享消费，避免同一工作区重复长连接

## 发布构建

推送 `v*` 格式 tag（如 `v1.0.0`）后，GitHub Actions 会自动构建并创建 Release，包含：

| 平台 | 命令行产物 |
|------|-----------|
| macOS Apple Silicon | `opennexus-darwin-arm64.tar.gz` |
| macOS x86_64 | `opennexus-darwin-amd64.tar.gz` |
| Linux x86_64 | `opennexus-linux-amd64.tar.gz` |
| Linux arm64 | `opennexus-linux-arm64.tar.gz` |
| Windows x86_64 | `opennexus-windows-amd64.zip` |

桌面客户端打包请使用 `make electron-dist`。

## 许可证

私有项目，保留所有权利。
