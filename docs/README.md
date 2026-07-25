# openNexus 文档

本目录包含 **openNexus** 的项目级文档。openNexus 是一个基于 [Agent Client Protocol (ACP)](https://github.com/coder/acp-go-sdk) 的多 Agent 编排与会话平台。

面向终端用户的指南请查看根目录的 [`README.md`](../README.md)（英文）和 [`README.zh-CN.md`](../README.zh-CN.md)（中文）。

其他语言：[English](README.en.md)

## 文档索引

| 文档 | 说明 |
|------|------|
| [`architecture.md`](architecture.md) | 系统架构、组件职责与数据流 |
| [`development.md`](development.md) | 开发工作流、构建命令与调试技巧 |
| [`acp-skills-commands-conclusion.md`](acp-skills-commands-conclusion.md) | 调研笔记：ACP 协议在 skills 和 slash commands 方面的范围 |
| [`superpowers/specs/`](superpowers/specs/) | 功能设计规格（按日期组织） |
| [`superpowers/plans/`](superpowers/plans/) | 实现计划与任务拆分（按日期组织） |

## 快速链接

- **后端**：Go 1.25 + Gin + GORM + SQLite
- **前端**：React 18 + TypeScript + Vite
- **协议**：ACP、MCP
- **入口**：`cmd/server/main.go`
- **配置**：`config.yaml`
- **构建命令**：`Makefile`

## 贡献文档

- 新增项目级指南直接放在 `docs/` 下。
- 新增功能设计文档放在 `docs/superpowers/specs/`，实现计划放在 `docs/superpowers/plans/`。
- 在上方索引中同步更新新增文档。
