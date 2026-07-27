import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import type { ConfigOptionValue } from '../types'
import { fullOptionLabel, truncateSelectLabel } from '../utils/selectLabel'
import styles from './AgentModelSelector.module.css'

// 合并下拉项的 value 编码分隔符（agentType 与 modelValue 均不会包含 \u0000）
const SEP = '\u0000'

interface AgentItem {
  type: string
  display_name: string
}

interface AgentModelSelectorProps {
  /** 可选 agent 列表（会话页传当前 agent 单项） */
  agents: AgentItem[]
  /** 各 agent 的模型选项（未探测到/探测中可缺省，此时显示 agent 级单项） */
  modelsByAgent: Record<string, ConfigOptionValue[]>
  /** 显示过滤正则（来自 config.yaml agents.selector.filters），匹配串 "agentType/modelValue" */
  filters: string[]
  selectedAgent: string
  selectedModel: string
  disabled?: boolean
  onSelect: (agentType: string, modelValue: string) => void
}

interface ComboEntry {
  agentType: string
  modelValue: string
  label: string
  title: string
}

function encodeCombo(agentType: string, modelValue: string): string {
  return `${agentType}${SEP}${modelValue}`
}

/**
 * AgentModelSelector：agent 与模型合并为一个下拉框。
 * 每个选项是「agent · 模型」组合；agent 的模型未知时退化为 agent 级单项（使用默认模型）。
 * filters 非空时按正则过滤组合（任一命中即显示），当前选中项始终保留避免 UI 失效。
 */
export default function AgentModelSelector({
  agents,
  modelsByAgent,
  filters,
  selectedAgent,
  selectedModel,
  disabled,
  onSelect,
}: AgentModelSelectorProps) {
  const { t } = useTranslation()

  // 编译过滤正则（非法项忽略；后端启动时已校验，此处仅兜底）
  const regexes = useMemo(() => {
    const out: RegExp[] = []
    for (const f of filters) {
      try { out.push(new RegExp(f)) } catch { /* 非法正则忽略 */ }
    }
    return out
  }, [filters])

  const entries = useMemo(() => {
    const list: ComboEntry[] = []
    const matches = (candidate: string) => regexes.length === 0 || regexes.some((re) => re.test(candidate))

    for (const agent of agents) {
      const models = modelsByAgent[agent.type]
      if (models && models.length > 0) {
        for (const m of models) {
          if (!matches(`${agent.type}/${m.value}`)) continue
          list.push({
            agentType: agent.type,
            modelValue: m.value,
            label: `${agent.display_name} · ${truncateSelectLabel(m.name, 24)}`,
            title: fullOptionLabel(`${agent.display_name} · ${m.name}`, m.description),
          })
        }
      } else if (matches(agent.type)) {
        // 模型未知（探测中/失败/该 agent 无模型配置）：仅显示 agent，使用其默认模型
        list.push({ agentType: agent.type, modelValue: '', label: agent.display_name, title: agent.display_name })
      }
    }

    // 当前选中组合被过滤或尚未出现在列表时补充，保持下拉受控值有效
    if (selectedAgent && !list.some((e) => e.agentType === selectedAgent && e.modelValue === selectedModel)) {
      const agent = agents.find((a) => a.type === selectedAgent)
      const model = (modelsByAgent[selectedAgent] || []).find((m) => m.value === selectedModel)
      const name = agent?.display_name || selectedAgent
      const label = selectedModel ? `${name} · ${truncateSelectLabel(model?.name || selectedModel, 24)}` : name
      list.unshift({ agentType: selectedAgent, modelValue: selectedModel, label, title: label })
    }
    return list
  }, [agents, modelsByAgent, regexes, selectedAgent, selectedModel])

  if (agents.length === 0) {
    return (
      <select className={styles.select} disabled value="">
        <option value="">{t('docMode.noAgent')}</option>
      </select>
    )
  }

  return (
    <select
      className={styles.select}
      value={encodeCombo(selectedAgent, selectedModel)}
      disabled={disabled}
      title={entries.find((e) => e.agentType === selectedAgent && e.modelValue === selectedModel)?.title}
      onChange={(e) => {
        const idx = e.target.value.indexOf(SEP)
        if (idx < 0) return
        onSelect(e.target.value.slice(0, idx), e.target.value.slice(idx + 1))
      }}
    >
      {entries.map((entry) => (
        <option key={encodeCombo(entry.agentType, entry.modelValue)} value={encodeCombo(entry.agentType, entry.modelValue)} title={entry.title}>
          {entry.label}
        </option>
      ))}
    </select>
  )
}
