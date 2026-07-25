# openNexus Development Guide

This guide covers how to set up a local development environment, common commands, and debugging tips.

## Prerequisites

- Go >= 1.25
- Node.js >= 20
- npm or pnpm
- (Optional) Docker and Docker Compose
- (Optional) Rust + pnpm + `pake-cli@3.13.0` for desktop builds
- API keys for the agents you want to use (e.g. `ANTHROPIC_API_KEY` for Claude Code)

## Quick Start

```bash
# Start both frontend and backend dev servers
make dev
```

- Backend: http://localhost:8080
- Frontend: http://localhost:3000

Register an account on first use.

## Common Commands

| Command | Description |
|---------|-------------|
| `make dev` | Start frontend + backend dev servers |
| `make backend` | Start backend only (http://localhost:8080) |
| `make frontend` | Start frontend only (http://localhost:3000) |
| `make run` | Build and run in single-port production mode |
| `make build` | Build frontend and backend |
| `make test` | Run all backend tests |
| `make clean` | Clean build artifacts |

### Docker

| Command | Description |
|---------|-------------|
| `make docker-build` | Build the Docker image |
| `make docker-up` | Build and start containers (foreground) |
| `make docker-up-d` | Build and start containers (background) |
| `make docker-down` | Stop containers |
| `make docker-logs` | Tail container logs |

### Desktop

| Command | Description |
|---------|-------------|
| `make pake` | Build Pake desktop wrapper |
| `make desktop` | Build macOS desktop app |
| `make electron-dev` | Run Electron in dev mode |
| `make electron-dist` | Package Electron app for current platform |

## Project Layout

```
openNexus/
├── cmd/
│   ├── server/            # Backend entry point
│   └── import-fleeting/   # Fleeting notes import tool
├── internal/
│   ├── acp/               # ACP protocol integration
│   ├── agent/             # Agent registry and router
│   ├── config/            # Config loading and migration
│   ├── database/          # DB connection
│   ├── handlers/          # HTTP/WebSocket handlers
│   ├── logging/           # Logging hub and SSE streaming
│   ├── mcp/               # MCP servers (notes, subagent)
│   ├── middleware/        # JWT auth middleware
│   ├── models/            # GORM data models
│   ├── repository/        # Data access layer
│   ├── router/            # Route registration
│   ├── services/          # Business services
│   └── sysutil/           # System utilities
├── web/                   # Frontend (React + Vite)
├── electron/              # Electron desktop client
├── scripts/               # Build and packaging scripts
├── docs/                  # Documentation
├── config.yaml            # Default configuration
├── Dockerfile             # Multi-stage build
├── docker-compose.yml     # Container orchestration
└── Makefile               # Common commands
```

## Configuration

The backend reads `config.yaml`. Lookup order:

1. `CONFIG_PATH` environment variable
2. `~/.openNexus/config.yaml`
3. `./config.yaml` (project root)

Common environment overrides:

| Variable | Description |
|----------|-------------|
| `SERVER_PORT` | Server port (default: `8080`) |
| `SERVER_MODE` | `debug` or `release` |
| `DATABASE_PATH` | SQLite database path |
| `JWT_SECRET` | JWT signing secret |
| `ANTHROPIC_API_KEY` | API key for Claude Code |
| `SKIP_DATA_MIGRATION` | Set to `1` to skip legacy data migration |

## Backend Development

```bash
# Run backend only
go run ./cmd/server

# Run tests
go test ./...

# Run a specific package's tests
go test ./internal/handlers
```

The backend uses standard Go project layout. Handler files are in `internal/handlers` and route registration is in `internal/router/router.go`.

## Frontend Development

```bash
cd web
npm install
npm run dev
```

Frontend source is in `web/src`. Vite dev server proxies API calls to the backend.

## Debugging

- **Backend logs**: printed to stdout; also streamed to the UI Log panel via SSE
- **ACP debug**: set `debug.acp.enabled: true` in `config.yaml` (default). Raw JSON-RPC traffic is saved to `~/.openNexus/acp-debug/`
- **Debug Panel**: in a session, open the Debug tab to inspect ACP events and raw messages
- **Browser DevTools**: frontend runs on Vite with source maps enabled in dev mode

## Data Migration

openNexus automatically migrates legacy data from `~/.nextAgent` and `~/.nexusagent` to `~/.openNexus` on startup. The migration is idempotent and non-fatal. To skip:

```bash
SKIP_DATA_MIGRATION=1 ./opennexus
```

## Adding an Agent

1. Open **Settings → Agent** and enable the agent.
2. Set the required environment variable (e.g. `ANTHROPIC_API_KEY`).
3. The backend connects asynchronously. Check the sidebar status and backend logs.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Port 8080/3000 already in use | Run `make backend-stop` or kill the process manually |
| Agent shows disconnected | Verify the API key env var and check backend stderr logs |
| Binary agent fails to run on Alpine Docker | Use `npx` distribution or a glibc-based base image |
| Frontend cannot reach backend | Confirm both dev servers are running and Vite proxy is configured |

## Release Builds

Pushing a `v*` tag triggers GitHub Actions to publish release artifacts. For local release builds:

```bash
make release
```

For desktop packaging, see the Makefile desktop and electron targets.
