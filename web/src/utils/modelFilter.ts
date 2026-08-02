import type { ConfigOptionValue } from '../types'

/**
 * Agent+模型「显示过滤」工具。过滤规则来自 config.yaml 的 agents.selector.filters，
 * 与 AgentModelSelector 的过滤语义保持一致：
 * - filters 为空 → 全部放行（不过滤）；
 * - filters 非空 → 任一正则命中任一候选串即放行，大小写不敏感；
 * - 候选串约定为 "agentType/modelValue" 与 "agentType/modelName"，既能按内部值
 *   （如 swe-1.5）也能按下拉展示名（如 "SWE 1.5"）过滤；
 * - 非法正则忽略（后端启动时已校验，此处仅兜底）。
 *
 * 抽离自 AgentModelSelector，供「能力测试」等只关心模型维度的场景复用。
 */

/** 把字符串形式 filters 编译成正则数组（忽略大小写；非法项忽略） */
export function compileFilters(filters: string[] | null | undefined): RegExp[] {
  const out: RegExp[] = []
  for (const f of filters || []) {
    try { out.push(new RegExp(f, 'i')) } catch { /* 非法正则忽略 */ }
  }
  return out
}

/** 任一候选串被任一正则命中即放行；regexes 为空时全部放行 */
export function matchFilters(regexes: RegExp[], ...candidates: string[]): boolean {
  return regexes.length === 0 || regexes.some((re) => candidates.some((c) => re.test(c)))
}

/**
 * 按 selector.filters 过滤指定 agent 的模型列表。
 * - 对每个模型用 "agentType/value" 与 "agentType/name" 两个候选串匹配；
 * - filters 为空时原样返回；
 * - selectedValue 非空且被过滤掉时，将其补回结果首位（保证当前选择在下拉中可见，
 *   避免选中项「凭空消失」导致 UI 失效，行为对齐 AgentModelSelector）。
 */
export function filterAgentModels(
  agentType: string,
  models: ConfigOptionValue[],
  regexes: RegExp[],
  selectedValue?: string,
): ConfigOptionValue[] {
  if (regexes.length === 0) return models
  const out = models.filter((m) => matchFilters(regexes, `${agentType}/${m.value}`, `${agentType}/${m.name}`))
  if (selectedValue && !out.some((m) => m.value === selectedValue)) {
    const sel = models.find((m) => m.value === selectedValue)
    if (sel) out.unshift(sel)
  }
  return out
}
