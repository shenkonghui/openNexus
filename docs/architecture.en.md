# openNexus Architecture

This document describes the high-level architecture of openNexus: the major components, their responsibilities, and how they interact.

## Overview

openNexus is a web-based multi-Agent orchestration platform. It connects to ACP-compatible agents (Claude Code, CodeBuddy, Kilo Code, Devin, etc.) and exposes a unified UI for session management, streaming conversations, file editing, terminal interaction, scheduled tasks, notes, and sub-agents.

```
┌─────────────────────────────────────────────────────────────────────────┐
│                              Web UI                                     │
│                  (React + Vite + TypeScript, web/)                      │
└─────────────────────────────────┬───────────────────────────────────────┘
                                  │ HTTP / WebSocket / SSE
┌─────────────────────────────────▼───────────────────────────────────────┐
│                           Go Backend (Gin)                              │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐  │
│  │  Auth    │ │  Agent   │ │ Session  │ │ Workspace│ │  Scheduler   │  │
│  │ Handlers │ │ Handlers │ │ Handlers │ │ Handlers │ │   Tasks      │  │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────┬───────┘  │
│       └─────────────┴─────────────┴─────────────┴────────────┘          │
│                              Services                                   │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────────┐  │
│  │   Auth   │ │   JWT    │ │ Agent    │ │  Task    │ │    Notes     │  │
│  │  Service │ │  Service │ │  Router  │ │Scheduler │ │  Classifier  │  │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘ └──────┬───────┘  │
│       └─────────────┴─────────────┴─────────────┴────────────┘          │
│                         Data Access (GORM)                              │
│                              SQLite                                     │
└─────────────────────────────────────────────────────────────────────────┘
                                  │
                    ┌─────────────┼─────────────┐
                    ▼             ▼             ▼
              ┌─────────┐   ┌─────────┐   ┌──────────┐
              │ ACP     │   │  MCP    │   │  Files   │
              │ Agents  │   │ Servers │   │ Terminal │
              └─────────┘   └─────────┘   └──────────┘
```

## Backend Layers

### Entry Point

`cmd/server/main.go` bootstraps the application:

1. Loads configuration (`internal/config`)
2. Runs automatic legacy data migration (`internal/config/migrate.go`)
3. Initializes SQLite via GORM (`internal/database`)
4. Builds service instances (auth, JWT, scheduler, agent router, MCP registry)
5. Registers HTTP routes and starts the Gin server (`internal/router`)

### HTTP Layer (`internal/handlers`)

RESTful and WebSocket handlers are grouped by domain:

- **`auth_handler.go`** — registration, login, refresh, logout, profile
- **`agent_handler.go`** — list agents, status, models, commands, modes, probe, preconnect
- **`agent_config_handler.go`** — CRUD for agent configurations, registry sync
- **`session_handler.go`** — create/resume/delete sessions, send prompts, stream responses, manage permissions
- **`session_file_handler.go`** — read/write files within the session workspace, diff/undo/restore
- **`workspace_handler.go`** — workspace CRUD, save state, file uploads
- **`filesystem_handler.go`** — directory/file browsing, skill/command/rule/sub-agent scanning
- **`scheduled_task_handler.go`** — cron-driven task scheduling and execution history
- **`note_handler.go`** — note CRUD, tagging, Markdown rendering
- **`mcp_handler.go`** — global MCP server configuration
- **`terminal_handler.go`** — WebSocket-based xterm terminal
- **`debug_handler.go`** — ACP debug metadata, events, raw messages

### Services (`internal/services`)

Business logic shared by handlers:

- **Auth / JWT** — password hashing, token issuance/validation
- **Agent Router** (`internal/agent`) — registry and routing for ACP agent connections
- **Scheduler** — cron job management for scheduled tasks
- **Note Classifier** — optional agent-based note auto-classification

### ACP Integration (`internal/acp`)

- Manages per-agent subprocesses (npx, uvx, binary)
- Performs ACP `initialize` handshake and authentication
- Multiplexes sessions over a single ACP connection per agent type
- Streams tool calls, thinking, and responses via SSE
- Handles permission requests and user approvals
- Health-checks connections with automatic reconnection

### MCP Integration (`internal/mcp`)

- Built-in MCP servers:
  - **`notes/`** — exposes notes as tools/resources
  - **`subagent/`** — exposes sub-agents as invokable tools
- Global `mcpServers` config stored in `~/.agents/mcp.json` (or configured path)

### Data Layer (`internal/models`, `internal/repository`)

- GORM models for users, sessions, workspaces, scheduled tasks, notes, etc.
- SQLite is the default database (configurable via `database.path`)

## Frontend

The frontend lives in `web/` and is built with Vite.

Key areas:

- **Session UI** — chat, streaming output, debug/log panels, diff viewer
- **File Explorer** — directory tree, CodeMirror editor
- **Terminal** — xterm.js over WebSocket
- **Settings** — agent config, MCP servers, theme, language
- **Notes** — quick capture, tag filtering, Markdown preview
- **Scheduled Tasks** — cron editor, execution history

## Protocols

### Agent Client Protocol (ACP)

openNexus acts as an ACP client. It launches agent subprocesses, negotiates capabilities over `initialize`, and drives sessions through JSON-RPC messages. Session output is streamed to the browser via Server-Sent Events (SSE).

### Model Context Protocol (MCP)

MCP servers extend what agents can do. openNexus provides built-in MCP servers for Notes and Sub-Agents, and supports user-configured global MCP servers (`mcpServers`).

## Deployment Modes

- **Development** — separate backend (`:8008`) and Vite frontend (`:3000`)
- **Single-port production** — backend serves the built frontend from `web/dist`
- **Docker** — multi-stage build with both frontend and backend
- **Desktop** — Electron wrapper around the web UI

## Configuration

Configuration is loaded from `config.yaml` (search order: `CONFIG_PATH` → `~/.openNexus/config.yaml` → `./config.yaml`). Environment variables can override most values. See [`development.en.md`](development.en.md) and root `README.md` for details.
