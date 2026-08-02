import type { Agent, ModelOption, ConfigOption, AgentStatus, AgentCommand, SessionMode, AgentAcpCapabilities, CapabilityTestReport, CapabilityTestBatchResult, SecurityTestReport, SecurityTestBatchResult } from '../types'
import { apiFetch } from './client'

// 获取可用 agent 列表（selector_filters 为 agent+模型 合并下拉的显示过滤正则，来自 config.yaml）
export function listAgents(): Promise<{ data: { agents: Agent[]; selector_filters?: string[] } }> {
  return apiFetch('/agents')
}

// 获取所有 agent 类型的 ACP 连接状态
export function listAgentStatus(): Promise<{ data: { agents: AgentStatus[] } }> {
  return apiFetch('/agents/status')
}

// 获取指定 agent 类型最近一次 ACP 握手的能力信息（从未握手时 available=false）
export function getAgentCapabilities(agentType: string): Promise<{ data: AgentAcpCapabilities }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/capabilities`)
}

// 执行 rule/skill/mcp 能力接入测试；e2e=true 时创建临时会话并发送验证 prompt（耗时较长）
// modelValue 指定测试模型；省略/空=后端自动选取 agent 当前运行模型
export function runCapabilityTest(agentType: string, e2e: boolean, modelValue?: string): Promise<{ data: CapabilityTestReport }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/capability-test`, {
    method: 'POST',
    body: JSON.stringify({ e2e, model_value: modelValue || '' }),
  })
}

// 获取最近一次能力测试报告（后端内存缓存，从未测试时 available=false）
export function getLastCapabilityTest(agentType: string): Promise<{ data: { available: boolean; report?: CapabilityTestReport } }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/capability-test`)
}

// 一键并行测试全部已接入 agent 的能力（e2e=true 时每个 agent 真实消耗一次调用，并行执行）
export function runCapabilityTestAll(e2e: boolean): Promise<{ data: CapabilityTestBatchResult }> {
  return apiFetch('/agents/capability-test-all', {
    method: 'POST',
    body: JSON.stringify({ e2e }),
  })
}

// 获取指定 agent 类型的可用模型列表（从已有会话缓存获取，可能为空）
export function getAgentModels(agentType: string): Promise<{ data: { model_options: ModelOption[] } }> {
  return apiFetch<{ data: { model_options: ModelOption[] } }>(`/agents/${encodeURIComponent(agentType)}/models`)
    .then((resp) => {
      resp.data.model_options = normalizeOptionsField(resp.data.model_options)
      return resp
    })
}

// 后端 nil 切片会序列化为 null：统一把 options 字段规范化为数组，
// 避免调用方 o.options.length / .map 等直接访问时渲染崩溃。
export function normalizeOptionsField<T extends { options: unknown }>(list: T[] | null | undefined): T[] {
  return (list || []).map((o) => (o.options ? o : { ...o, options: [] }))
}

// 探测指定 agent 类型的全部 config options（服务端预连接时已缓存）。
const probeCache = new Map<string, ConfigOption[]>()

export function clearAgentProbeCache(agentType?: string) {
  if (agentType) probeCache.delete(agentType)
  else probeCache.clear()
}

export function probeAgentConfigs(
  agentType: string,
  options?: { force?: boolean },
): Promise<{ data: { config_options: ConfigOption[] } }> {
  if (!options?.force) {
    const cached = probeCache.get(agentType)
    if (cached) {
      return Promise.resolve({ data: { config_options: cached } })
    }
  }
  return apiFetch<{ data: { config_options: ConfigOption[] } }>(
    `/agents/${encodeURIComponent(agentType)}/probe`,
    { method: 'POST' },
  ).then((resp) => {
    resp.data.config_options = normalizeOptionsField(resp.data.config_options)
    probeCache.set(agentType, resp.data.config_options)
    return resp
  })
}

// 异步预连接 agent（新建会话页提前预热，失败静默忽略）。
// cwd 非空时作为工作目录；为空时后端自动使用 probeCwd。
export function preconnectAgent(agentType: string, cwd?: string): void {
  if (!agentType) return
  const body = cwd ? JSON.stringify({ cwd }) : '{}'
  apiFetch(`/agents/${encodeURIComponent(agentType)}/preconnect`, {
    method: 'POST',
    body,
  }).catch(() => { })
}

// 获取指定 agent 类型 slash command（Agent 原生 + 配置 commands；可选 cwd 扫描项目级）
export function listAgentCommands(agentType: string, cwd?: string): Promise<{ data: { commands: AgentCommand[] } }> {
  const qs = cwd ? `?path=${encodeURIComponent(cwd)}` : ''
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/commands${qs}`)
}

// 获取指定 agent 类型缓存的 session mode（新建任务页用）
export function listAgentModes(agentType: string): Promise<{ data: { modes: SessionMode[] } }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/modes`)
}

// ===== 安全测试 =====

// 执行沙箱效果测试：在沙箱开启前提下发送命令 prompt 让 agent 真正执行，
// 全部工具调用自动批准（由沙箱负责阻止危险操作），通过退出码验证沙箱隔离效果。
// modelValue 指定测试模型；省略/空=后端自动选取 agent 当前运行模型。
export function runSecurityTest(agentType: string, modelValue?: string): Promise<{ data: SecurityTestReport }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/security-test`, {
    method: 'POST',
    body: JSON.stringify({ model_value: modelValue || '' }),
  })
}

// 获取最近一次安全测试报告（后端内存缓存，从未测试时 available=false）
export function getLastSecurityTest(agentType: string): Promise<{ data: { available: boolean; report?: SecurityTestReport } }> {
  return apiFetch(`/agents/${encodeURIComponent(agentType)}/security-test`)
}

// 一键并行安全测试全部已接入 agent
export function runSecurityTestAll(): Promise<{ data: SecurityTestBatchResult }> {
  return apiFetch('/agents/security-test-all', {
    method: 'POST',
  })
}
