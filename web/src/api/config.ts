import { apiFetch } from './client'

// 单个 dir 配置视图
export interface DirConfigView {
  user_dirs: string[]
  project_dirs: string[]
}

// agents 配置视图（skills/commands/rules/subagents）
export interface AgentsConfigView {
  skills: DirConfigView
  commands: DirConfigView
  rules: DirConfigView
  subagents: DirConfigView
}

// 获取 agents 配置（skills/commands/rules 目录路径）
export function getAgentsConfig(): Promise<{ data: AgentsConfigView }> {
  return apiFetch('/config/agents')
}

// 更新 agents 配置
export function updateAgentsConfig(config: AgentsConfigView): Promise<{ data: { message: string } }> {
  return apiFetch('/config/agents', {
    method: 'PUT',
    body: JSON.stringify(config),
  })
}

// 软重载：重新读取 config.yaml 并热刷新 skill/command/rule 扫描目录（不杀进程）。
// 浏览器访问远程后端时使用；桌面版走 IPC 硬重载（window.opennexus.reloadBackend）。
export function reloadProgram(): Promise<{ data: { message: string; restarted: boolean } }> {
  return apiFetch('/config/reload', { method: 'POST' })
}

// config.yaml 原生内容（全文编辑）
export interface RawConfigResponse {
  content: string
  path: string
}

// 读取 config.yaml 原始内容
export function getRawConfig(): Promise<{ data: RawConfigResponse }> {
  return apiFetch('/config/raw')
}

// 仅校验不写盘（YAML 语法 + 配置规则），校验失败时 reject
export function validateRawConfig(content: string): Promise<{ data: { valid: boolean } }> {
  return apiFetch('/config/raw/validate', {
    method: 'POST',
    body: JSON.stringify({ content }),
  })
}

// 校验通过后整体写回 config.yaml（后端校验失败不会写盘）
export function updateRawConfig(content: string): Promise<{ data: { message: string; path: string } }> {
  return apiFetch('/config/raw', {
    method: 'PUT',
    body: JSON.stringify({ content }),
  })
}

// 读取 agent+模型 合并下拉的显示过滤正则（config.yaml agents.selector.filters）
export function getSelectorFilters(): Promise<{ data: { filters: string[] } }> {
  return apiFetch('/config/selector')
}

// 保存显示过滤正则：后端校验正则合法性，写回 config.yaml 并立即生效（无需重启）
export function updateSelectorFilters(filters: string[]): Promise<{ data: { filters: string[]; message: string } }> {
  return apiFetch('/config/selector', {
    method: 'PUT',
    body: JSON.stringify({ filters }),
  })
}

// 扫描到的文件项
export interface ScannedFileItem {
  name: string
  description: string
  location: string
  scope: string
  path: string
  always_apply?: boolean
  globs?: string
  model?: string
  tools?: string[]
}

// 扫描技能文件（外部 skill.md）
export function scanSkillFiles(path?: string): Promise<{ data: { skills: ScannedFileItem[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/skills${qs}`)
}

// 扫描命令文件
export function scanCommandFiles(path?: string): Promise<{ data: { commands: ScannedFileItem[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/commands${qs}`)
}

// 扫描规则文件
export function scanRuleFiles(path?: string): Promise<{ data: { rules: ScannedFileItem[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/rules${qs}`)
}

// 扫描 subagent 定义文件
export function scanSubAgentFiles(path?: string): Promise<{ data: { subagents: ScannedFileItem[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/sub-agents${qs}`)
}

// 读取文件内容
export interface FileContentResponse {
  path: string
  content: string
  size: number
}

export function readFileContent(filePath: string): Promise<{ data: FileContentResponse }> {
  return apiFetch(`/filesystem/file?path=${encodeURIComponent(filePath)}`)
}

// 保存文件内容
export function writeFileContent(filePath: string, content: string): Promise<{ data: { path: string; size: number } }> {
  return apiFetch('/filesystem/file', {
    method: 'PUT',
    body: JSON.stringify({ path: filePath, content }),
  })
}

// MCP 配置（全局共享 mcpServers JSON）
export interface MCPConfigResponse {
  config: string // 文件原始文本
  path: string   // 配置文件绝对路径
  count: number  // 解析到的 server 数量
}

// 获取 MCP 配置（mcp.json 原始内容）
export function getMCPConfig(): Promise<{ data: MCPConfigResponse }> {
  return apiFetch('/config/mcp')
}

// 更新 MCP 配置（保存即生效，新建会话自动注入）
export function updateMCPConfig(config: string): Promise<{ data: MCPConfigResponse }> {
  return apiFetch('/config/mcp', {
    method: 'PUT',
    body: JSON.stringify({ config }),
  })
}

// 单个 MCP 工具信息
export interface MCPToolInfo {
  name: string
  title?: string
  description?: string
}

// 单个 MCP server 探测结果
export interface MCPServerStatus {
  name: string
  type: string         // stdio | http | sse
  connected: boolean
  error?: string
  server_info?: string // InitializeResult.ServerInfo.Name
  tools: MCPToolInfo[]
}

// 探测所有 MCP server 的连接状态与工具列表
export function getMCPStatus(): Promise<{ data: { servers: MCPServerStatus[]; error?: string } }> {
  return apiFetch('/config/mcp/status')
}

// MCP 聚合网关：单个上游的聚合状态
export interface MCPGatewayUpstream {
  name: string
  type: string
  source: string // "mcp.json" | "custom" | "disabled"
  connected: boolean
  tool_count: number
  error?: string
}

// MCP 聚合网关：未被接管的条目及原因
export interface MCPGatewaySkipped {
  name: string
  type: string
  reason: string
  disabled?: boolean
}

// MCP 聚合网关整体状态
export interface MCPGatewayStatus {
  enabled: boolean
  endpoint: string
  path: string
  token: string
  tool_count: number
  upstreams: MCPGatewayUpstream[] | null
  skipped: MCPGatewaySkipped[] | null
}

// 获取聚合网关状态（会实时探测上游）
export function getMCPGatewayStatus(): Promise<{ data: MCPGatewayStatus }> {
  return apiFetch('/config/mcp/gateway')
}

// 启用/停用聚合网关（写入或移除 mcp.json 中的 opennexus-gateway 条目）
export function setMCPGatewayEnabled(enabled: boolean): Promise<{ data: MCPGatewayStatus }> {
  return apiFetch('/config/mcp/gateway', {
    method: 'POST',
    body: JSON.stringify({ enabled }),
  })
}

// 禁用指定上游（网关层面，不修改 mcp.json）
export function disableGatewayUpstream(name: string): Promise<{ data: { name: string; disabled: boolean } }> {
  return apiFetch(`/config/mcp/gateway/upstreams/${encodeURIComponent(name)}/disable`, { method: 'POST' })
}

// 启用指定上游（解除禁用）
export function enableGatewayUpstream(name: string): Promise<{ data: { name: string; disabled: boolean } }> {
  return apiFetch(`/config/mcp/gateway/upstreams/${encodeURIComponent(name)}/enable`, { method: 'POST' })
}

// 添加自定义上游（不写入 mcp.json）
export interface CustomServerEntry {
  type: string
  url: string
  headers?: Record<string, string>
}
export function addGatewayCustomServer(name: string, entry: CustomServerEntry): Promise<{ data: { name: string; added: boolean } }> {
  return apiFetch('/config/mcp/gateway/custom-servers', {
    method: 'POST',
    body: JSON.stringify({ name, entry }),
  })
}

// 移除自定义上游
export function removeGatewayCustomServer(name: string): Promise<{ data: { name: string; removed: boolean } }> {
  return apiFetch(`/config/mcp/gateway/custom-servers/${encodeURIComponent(name)}`, { method: 'DELETE' })
}
