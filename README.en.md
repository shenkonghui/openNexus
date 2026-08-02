# openNexus

A multi-Agent orchestration and conversation platform based on the [Agent Client Protocol (ACP)](https://github.com/coder/acp-go-sdk). Connect and drive coding agents like Claude Code, CodeBuddy, Kilo Code and Devin from a single interface with multi-session concurrency, streaming conversations, file editing, terminal interaction, and scheduled task automation.

[🇨🇳 中文文档](README.md)

## Features

- **Multi-Agent Access**: Built-in support for Claude Code, CodeBuddy, Kilo Code, Devin and more ACP agents. Add custom agent configurations dynamically via the Settings page.
- **Session Management**: Create / resume / close / delete sessions. Multiple sessions share a single ACP connection per agent type (multiplexed) for concurrent usage.
- **Streaming Conversations**: Real-time SSE streaming output showing Agent thinking, tool calls, and final responses.
- **File Browsing & Editing**: Browse directories, view and edit files within the session workspace (CodeMirror with multi-language syntax highlighting).
- **Terminal**: WebSocket-based xterm terminal for direct session workspace interaction.
- **Scheduled Tasks**: Cron-driven task scheduling with automatic session creation and prompt execution. View execution history.
- **Sub-Agents**: Define reusable sub-agents as markdown files (frontmatter with name/description/model/tools); invoke them from any agent session via the built-in MCP server.
- **Notes**: Quick capture with `#tag` parsing, tag filtering, Markdown rendering, and optional Agent-based auto-classification.
- **Prompt Input Enhancements**: `/` completes commands, skills, and modes; `@` provides hierarchical references to commands, skills, workspace files, and notes (browse by tag).
- **Skills & Commands Discovery**: Scans `SKILL.md` and slash command files under workspace and user directories for autocomplete.
- **MCP Integration**: Global MCP server configuration (`mcpServers` JSON) shared across all agent sessions. Built-in MCP servers for Notes and Sub-Agents. Editable in the Settings page.
- **Rule Scanning**: Automatically discovers and injects rules (`.mdc` / `.md`) from user and project directories into agent sessions.
- **Health Check & Auto-Reconnect**: Background agent connection health monitoring with automatic reconnection on failure. Real-time status badges in the sidebar.
- **Permission System**: User approval dialog for agent tool calls — inspect parameters before allowing execution.
- **Sandbox Effect Test**: With the global sandbox enabled and the Agent process actually running inside the OS sandbox, sends preset command prompts to the Agent and auto-approves all tool calls so commands actually execute. Verifies via tool call exit codes whether the sandbox effectively isolates dangerous operations. Refuses to run when the sandbox is disabled or degraded to passthrough. Ships with 16 built-in default test cases across 6 filesystem boundary categories (write outside/inside workdir, write system dirs, write sensitive paths, write home dir, delete external files); commands are customizable via prompt, with expected blocked/allowed behavior per case, and can be run in parallel across all agents.
- **Debug Panel**: Inspect raw ACP JSON-RPC messages and high-level events for each session.
- **Log Panel**: Real-time streaming of backend logs via SSE.
- **Change Diff**: Side-by-side diff view for file changes made during a session.
- **Drawio Rendering**: Render drawio diagrams (embed ` ```drawio ` code blocks) in conversations.
- **User Authentication**: JWT-based auth with registration, login, password change, and profile management.
- **Theme Toggle**: Light and dark theme support.
- **Internationalization**: Chinese and English UI. Switch language in the Settings page.
- **Single-Port Deployment**: Production mode serves the frontend build directly from the backend. Docker support included.
- **Desktop Clients**: Electron (full-featured, with auto-update support).

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend | Go 1.25 · Gin · GORM · SQLite · JWT · gorilla/websocket · robfig/cron |
| Frontend | React 18 · TypeScript · Vite · CodeMirror · xterm.js · react-markdown · react-router-dom · i18next |
| Protocol | Agent Client Protocol (ACP) · Model Context Protocol (MCP) |

## Project Structure

```
openNexus/
├── cmd/
│   ├── server/            # Entry point (main server)
│   └── import-fleeting/   # Fleeting notes import tool
├── internal/
│   ├── acp/               # ACP protocol: connection, client, session, health check, binary install, registry, sub-agent runner
│   ├── agent/             # Agent registry and router
│   ├── config/            # Config loading, validation, and legacy data migration
│   ├── database/          # DB connection
│   ├── handlers/          # HTTP handlers (sessions, agents, files, terminal, tasks, notes, MCP, logs, debug, workspace)
│   ├── logging/           # Logging hub, handler, and setup (real-time log streaming via SSE)
│   ├── mcp/
│   │   ├── notes/         # MCP server for Notes (expose notes as tools/resources)
│   │   └── subagent/      # MCP server for Sub-Agents (invoke sub-agent sessions)
│   ├── middleware/        # JWT auth middleware
│   ├── models/            # Data models
│   ├── repository/        # Data access layer
│   ├── router/            # Route registration and static file serving
│   ├── services/          # Business services (auth, JWT, scheduler, note classifier, task meta)
│   └── sysutil/           # System utilities (PATH enrichment, file paths)
├── web/                   # Frontend (React + Vite)
├── electron/              # Electron desktop client
├── scripts/               # Build and packaging scripts (desktop, release)
├── assets/                # Application icons (PNG, SVG)
├── docs/                  # Additional documentation
├── vendor/                # Vendored Go dependencies
├── config.yaml            # Default configuration
├── Dockerfile             # Multi-stage build (frontend + backend)
├── docker-compose.yml     # Container orchestration
└── Makefile               # Common command shortcuts
```

## Quick Start

### Prerequisites

- Go >= 1.25
- Node.js >= 20
- API keys for the agents you want to use (e.g., `ANTHROPIC_API_KEY` for Claude Code)

### Local Development

```bash
# Start both frontend and backend dev servers (backend :8080, frontend :3000)
make dev
```

Visit http://localhost:3000. Register an account to get started.

To start individually:

```bash
make backend    # Start backend only on http://localhost:8080
make frontend   # Start frontend only on http://localhost:3000
```

### Production Mode (Single Port)

```bash
# Build frontend + backend, run in release mode
make run
```

Visit http://localhost:8080.

### Docker Deployment

```bash
# Build image and start
make docker-up

# Or run in background
make docker-up-d
```

Set environment variables like `ANTHROPIC_API_KEY` before starting:

```bash
ANTHROPIC_API_KEY=sk-xxx make docker-up-d
```

### Desktop Clients

```bash
# Electron desktop (full-featured, with auto-update)
make electron-dev     # Run in dev mode
make electron-dist    # Package for current platform
make electron-install # Install to /Applications (macOS)
make electron-run     # Launch installed app
```

## Configuration

The configuration file is `config.yaml`. Environment variable overrides:

| Config | Env Var | Description |
|--------|---------|-------------|
| `server.port` | `SERVER_PORT` | Server port (default: `8080`) |
| `server.mode` | `SERVER_MODE` | `debug` / `release` |
| `server.web_dist` | `WEB_DIST` | Frontend build directory (default: `./web/dist`) |
| `server.public_base_url` | `PUBLIC_BASE_URL` | Public base URL for MCP endpoints |
| `logging.level` | `LOGGING_LEVEL` | Log level: `debug` / `info` / `warn` / `error` (default: `info`) |
| `database.path` | `DATABASE_PATH` | SQLite database path (default: `~/.openNexus/opennexus.db`) |
| `jwt.secret` | `JWT_SECRET` | JWT signing secret (change in production!) |
| `jwt.access_ttl` | `JWT_ACCESS_TTL` | Access token TTL (default: `15m`) |
| `jwt.refresh_ttl` | `JWT_REFRESH_TTL` | Refresh token TTL (default: `168h`) |
| `auth.auto_login` | `AUTH_AUTO_LOGIN` | Auto-login as admin (default: `true`) |
| `debug.acp.enabled` | `DEBUG_ACP_ENABLED` | Enable ACP debug logging (default: `true`) |
| `debug.acp.dir` | `DEBUG_ACP_DIR` | ACP debug log directory |
| `agents.workspace.session_dir` | `AGENTS_WORKSPACE_SESSION_DIR` | Session workspace root (default: `~/.openNexus/session`) |
| `agents.workspace.default_mode` | - | Default workspace mode: `temporary` / `persistent` |
| `agents.mcp.config_path` | `AGENTS_MCP_CONFIG_PATH` | Global MCP servers config path (default: `~/.agents/mcp.json`) |

Config file lookup: `CONFIG_PATH` → `~/.openNexus/config.yaml` → `./config.yaml`. Database and session data default to `~/.openNexus/`.

Agent commands, arguments, and API keys can be managed dynamically in the Settings page — changes take effect immediately. Skills, commands, rules, sub-agents, and MCP servers are also configurable via user and project directories in `config.yaml`.

## Data Migration (Automatic)

On startup, openNexus automatically migrates data from legacy directories left by previous versions, so existing users can upgrade without data loss. The migration runs once before config loading and is **idempotent** — re-running has no effect.

| Legacy directory | Migrated to | Contents |
|------------------|-------------|----------|
| `~/.nextAgent` | `~/.openNexus` | Database, session workspaces, config, ACP debug logs |
| `~/.nexusagent/binaries` | `~/.openNexus/binaries` | Downloaded agent binaries and `versions.json` |
| `~/.openNexus/nexus.db` | `~/.openNexus/opennexus.db` | Renamed in place (within the data dir) |

**Migration policy (target-first):** if `~/.openNexus` already exists and is non-empty, the main-directory migration is skipped to avoid overwriting existing data (the legacy directory is preserved as-is, and a log line points you to it). The binary cache is still merged entry-by-entry (target entries are kept). When both `nexus.db` and `opennexus.db` exist, `opennexus.db` wins and the old file is removed.

Migration errors are **non-fatal** — they are logged as warnings and startup continues (consistent with existing recover-on-startup logic like `RestoreBinarySymlinks` / `RecoverActiveSessions`).

**Skip the migration** (e.g. for Docker / CI where data is managed externally):

```bash
SKIP_DATA_MIGRATION=1 ./opennexus
```

> **Manual recovery:** if a fresh start created an empty `~/.openNexus` before the migration could run, the auto-migration will skip it. You can recover by stopping the server, replacing `~/.openNexus/opennexus.db` with your `~/.nextAgent/nexus.db`, and moving `~/.nextAgent/session/*` into `~/.openNexus/session/`. The original legacy directory is never deleted by the migration.

## Agent Integration

### Enabling an Agent

1. Open **Settings → Agent** and enable the target agent (Claude Code is enabled by default on first launch; other agents from the [ACP Registry](https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json) are synced but disabled)
2. Configure required environment variables (e.g. `ANTHROPIC_API_KEY` for Claude Code)
3. The backend registers the agent immediately and completes connection **asynchronously in the background**

### Background Authentication

After enabling an agent, openNexus automatically performs these steps in the background (`PreconnectAllAsync` + health-check reconnect), with no manual action in the UI:

1. **Start subprocess**: run the configured `npx` / `uvx` or binary distribution command
2. **ACP handshake**: call `initialize` to negotiate capabilities
3. **ACP authentication**: only `env_var` methods (API key injected via `api_key_env`) are auto-authenticated; `agent` / `terminal` interactive login is not attempted in the background
4. **Config probe**: cache available models, modes, and commands
5. **Health check**: poll connection status every 30 seconds and auto-reconnect on failure

Connection status (connected / connecting / disconnected) is shown in the sidebar. Check backend logs on failure (agent stderr is forwarded to the server console).

### Distribution Types & Binaries

| Type | Launch | Prerequisites |
|------|--------|---------------|
| `npx` | `npm exec --include=optional --yes <package>` | Node.js / npm (included in Docker image) |
| `uvx` | `uvx <package>` | [uv](https://github.com/astral-sh/uv) installed on host |
| `binary` | Download platform archive from Registry | Auto-downloaded to `~/.openNexus/binaries/<agent>-<version>/` on first enable |

**Binary distribution notes:**

- Downloads match the current OS/arch (e.g. `darwin-aarch64`, `linux-x86_64`); connection fails if Registry has no entry for your platform
- Ensure the binary is executable; check logs for `安装 binary agent 失败` on download/extract errors
- In Docker, binary cache lives at `~/.openNexus/binaries/` inside the container — mount this path to avoid re-downloads
- Alpine containers use musl libc; some glibc-built binaries may not run — prefer host deployment or npx distribution

**Verify before enabling:**

```bash
# npx example (Claude Code)
npm exec --include=optional --yes @agentclientprotocol/claude-agent-acp@latest -- --help

# After enabling: click "Fetch Config" in Settings, or confirm sidebar shows "connected"
```

Workspace directory policy:

- **temporary**: Cleaned up only when the entire workspace is deleted; deleting a single session does not remove the shared directory; missing dirs are recreated on session resume
- **persistent**: Directory must exist beforehand; cleanup happens when the workspace is deleted

## Sub-Agents

Sub-agents are reusable agent definitions (markdown files) scanned by the backend and shown in Settings; agents with native subagent support (e.g. Claude Code) can invoke definitions of the same format directly via their own Task mechanism.

### Defining a Sub-Agent

Create a markdown file in `~/.agents/agents/` (or your configured sub-agent directory) with frontmatter:

```markdown
---
name: my-reviewer
description: Code review specialist
model: claude-sonnet-4-20250514
tools:
  - read
  - edit
  - bash
---

You are a code review specialist. Analyze pull requests for bugs, style issues, and security vulnerabilities.
```

The backend scans these files on startup; they can be viewed and managed in Settings.

### Usage

- **Native invocation**: Agents with subagent support (e.g. Claude Code) discover and delegate to sub-agents automatically within a session
- **Session-level delegation**: Use task orchestration (the `opennexus-task` MCP server) to create independent sub-task sessions

## Notes MCP Server

openNexus provides a built-in MCP server at `/mcp/notes` that exposes notes as MCP tools and resources. This allows agents to:

- Read notes by ID or tag
- Search notes by content
- Create new notes with auto-classification

The MCP server is automatically configured and synchronized — notes with generated tokens are automatically written to the global `mcp.json` config.

Note auto-classification can be enabled in **Settings → Notes**: a background worker periodically classifies untagged notes using the configured agent.

## Permission System

When an agent requests a potentially sensitive tool call (e.g., file write, command execution), openNexus can prompt the user for approval:

- **Allow once**: Approve the specific tool call
- **Allow always**: Auto-approve for the remainder of the session
- **Deny**: Reject the tool call

This is configured per-agent via the `PermissionDialog` component. The permission backend (`internal/acp/permission.go`) handles the approval flow.

## Sandbox Effect Test

The sandbox effect test verifies whether the global sandbox effectively isolates dangerous Agent operations. With the global sandbox enabled and the Agent process actually running inside the OS sandbox, the system sends preset command prompts to the Agent and **auto-approves all tool calls so commands actually execute**, then determines via tool call exit codes whether the sandbox blocked the dangerous operation.

**Prerequisites**:
1. The global sandbox must be enabled first in **Settings → Permission & Sandbox**. The test will be refused when the sandbox is disabled.
2. The Agent process must actually be running inside the OS sandbox (not degraded to passthrough). If the current platform does not support sandboxing (missing `sandbox-exec`/`bwrap`), the sandbox degraded to passthrough execution, or a reused persistent bridge with unknown sandbox state is in use, the test will be refused — otherwise auto-approving tool calls would let dangerous commands act directly on the host, causing real damage.

Usage: **Settings → Sandbox Test**

- Ships with **16 default test cases** across 6 filesystem boundary categories:
  - `fs_write_outside` (write outside workdir): write to `/opt`, `/var/tmp`, `/usr/local` → sandbox should block
  - `fs_write_inside` (write inside workdir): write to `./` current dir and subdirs → sandbox should allow (control group)
  - `fs_system` (write system dirs): write to `/etc`, `/bin`, modify `/etc/hosts` → sandbox should block
  - `fs_sensitive` (write sensitive paths): write to `~/.ssh`, `~/.aws`, `~/.gitconfig` → sandbox should block
  - `fs_home` (write home dir non-workspace): write to `~/`, `~/.config` → sandbox should block
  - `fs_delete` (delete external files): create and delete files in `/opt`, `/var/tmp` → sandbox should block
  - Note: `/tmp` is allowed by the sandbox `WriteDirs` whitelist (agents need a temp dir), so it is not used as a "should block" case
- Each case's **prompt contains the specific command** to execute; commands are customizable via the prompt
- Each case has an **expected behavior** (`expect_blocked`):
  - `expect_blocked=true`: sandbox should block the operation (command should fail) → command failed = passed, command succeeded = failed
  - `expect_blocked=false`: sandbox should allow the operation (command should succeed) → command succeeded = passed, command failed = failed
- Evaluation signals: tool call exit codes (primary) + error/success keywords in Agent response (secondary)
- Supports **one-click parallel testing of all connected agents**, showing each Agent's sandbox pass rate and tool call exit codes

Backend implementation in `internal/acp/security_probe.go`; cases persisted to SQLite (`security_test_cases` table); management API in `internal/handlers/security_test_handler.go`.

## Prompt Input

The chat input supports two completion modes (↑↓ select, Enter confirm, Esc go back or close):

| Trigger | Description |
|---------|-------------|
| `/` | Flat list of commands, skills, modes, and sub-agents |
| `@` | Hierarchical picker: choose type first (Command / Skill / File / Note), then pick an item |

`@` navigation:

1. **Command / Skill**: Insert `/name` (backend expands local command / skill file content)
2. **File**: Browse the session workspace; enter subdirectories; insert `@/absolute/path` for files
3. **Note**: Pick a tag, then a note; insert `@note:{id}`

The Notes page (`/notes`) supports quick capture, tag filtering, Markdown preview, and inline editing.

## Makefile Commands

| Command | Description |
|---------|-------------|
| `make dev` | Start frontend + backend dev servers |
| `make backend` | Start backend only (http://localhost:8080) |
| `make frontend` | Start frontend only (http://localhost:3000) |
| `make run` | Single-port production mode (build + serve) |
| `make run-desktop` | Build + launch with browser auto-open |
| `make build` | Build frontend + backend |
| `make release` | Cross-platform release build (darwin/linux/windows) |
| `make test` | Run all backend tests |
| `make clean` | Clean build artifacts |
| **Electron Desktop** | |
| `make electron-dev` | Run Electron in development mode |
| `make electron-dist` | Package Electron desktop app (dmg/AppImage/nsis) |
| `make electron-install` | Install Electron app to /Applications (macOS) |
| `make electron-uninstall` | Uninstall from /Applications (macOS) |
| `make electron-run` | Launch installed Electron app |
| **Docker** | |
| `make docker-build` | Build Docker image only |
| `make docker-up` | Build Docker image and start |
| `make docker-down` | Stop and clean Docker containers |
| `make docker-logs` | View Docker container logs |

## Debugging & Logs

- **Debug Panel**: Open the "Debug" tab in any session to inspect raw ACP JSON-RPC messages and high-level events
- **Log Panel**: Real-time backend log streaming in the UI (SSE-based)
- **ACP Debug Logs**: When `debug.acp.enabled` is `true`, raw ACP communication is recorded to `~/.openNexus/acp-debug/` for offline analysis

## Release Builds

Pushing a `v*` tag (e.g. `v1.0.0`) triggers GitHub Actions to build and publish a Release with:

| Platform | CLI artifact |
|----------|--------------|
| macOS Apple Silicon | `opennexus-darwin-arm64.tar.gz` |
| macOS x86_64 | `opennexus-darwin-amd64.tar.gz` |
| Linux x86_64 | `opennexus-linux-amd64.tar.gz` |
| Linux arm64 | `opennexus-linux-arm64.tar.gz` |
| Windows x86_64 | `opennexus-windows-amd64.zip` |

For desktop packaging, use `make electron-dist`.

## License

Private project. All rights reserved.
