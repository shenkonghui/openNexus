# openNexus Documentation

This directory contains project-level documentation for **openNexus**, a multi-Agent orchestration and conversation platform based on the [Agent Client Protocol (ACP)](https://github.com/coder/acp-go-sdk).

For end-user facing guides, see the root [`README.md`](../README.md) (English) and [`README.zh-CN.md`](../README.zh-CN.md) (中文).

Also available in: [中文](README.md)

## Documentation Index

| Document | Description |
|----------|-------------|
| [`architecture.en.md`](architecture.en.md) | System architecture, component responsibilities, and data flow |
| [`development.en.md`](development.en.md) | Development workflow, build commands, and debugging tips |
| [`acp-skills-commands-conclusion.md`](acp-skills-commands-conclusion.md) | Research note: ACP protocol scope regarding skills and slash commands |
| [`superpowers/specs/`](superpowers/specs/) | Feature design specifications (by date) |
| [`superpowers/plans/`](superpowers/plans/) | Implementation plans and task breakdowns (by date) |

## Quick Links

- **Backend**: Go 1.25 + Gin + GORM + SQLite
- **Frontend**: React 18 + TypeScript + Vite
- **Protocols**: ACP, MCP
- **Entry point**: `cmd/server/main.go`
- **Configuration**: `config.yaml`
- **Build commands**: `Makefile`

## Contributing to Docs

- Add new project-wide guides directly under `docs/`.
- Add feature design documents under `docs/superpowers/specs/` and implementation plans under `docs/superpowers/plans/`.
- Keep the index above updated when adding new top-level documents.
