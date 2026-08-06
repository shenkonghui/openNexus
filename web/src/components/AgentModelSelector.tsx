import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronUp, ChevronDown } from 'lucide-react'
import type { ConfigOptionValue } from '../types'
import { fullOptionLabel, truncateSelectLabel } from '../utils/selectLabel'
import { compileFilters, matchFilters } from '../utils/modelFilter'
import styles from './AgentModelSelector.module.css'

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
  /** 未选择时的占位项文案（提供后允许空选择：选中占位项回调 onSelect('','')） */
  placeholder?: string
  /** 额外的触发按钮样式类（如设置页表单风格） */
  className?: string
  onSelect: (agentType: string, modelValue: string) => void
}

interface ComboEntry {
  agentType: string
  modelValue: string
  /** 下拉列表项展示：完整「agent · 模型」 */
  label: string
  /** 触发按钮（收起态）展示：仅模型名（无模型时回退 agent 名） */
  modelLabel: string
  title: string
}

/**
 * AgentModelSelector：agent 与模型合并为一个可搜索下拉框。
 * 每个选项是「agent · 模型」组合；agent 的模型未知时退化为 agent 级单项（使用默认模型）。
 * filters 非空时按正则过滤组合（任一命中即显示），当前选中项始终保留避免 UI 失效。
 * 下拉展开后支持输入关键字实时过滤（匹配 agent 名、模型名、模型值）。
 */
export default function AgentModelSelector({
  agents,
  modelsByAgent,
  filters,
  selectedAgent,
  selectedModel,
  disabled,
  placeholder,
  className,
  onSelect,
}: AgentModelSelectorProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [dropUp, setDropUp] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)

  // 编译过滤正则（忽略大小写；非法项忽略，后端启动时已校验，此处仅兜底）
  const regexes = useMemo(() => compileFilters(filters), [filters])

  const entries = useMemo(() => {
    const list: ComboEntry[] = []
    // 任一候选串被任一正则命中即显示；候选串同时覆盖模型值与显示名称，
    // 避免用户按下拉里看到的名称（如 "SWE 1.5"）过滤时因 value 写法不同（如 "swe-1.5"）而漏配。
    const matches = (...candidates: string[]) => matchFilters(regexes, ...candidates)

    for (const agent of agents) {
      const models = modelsByAgent[agent.type]
      if (models && models.length > 0) {
        for (const m of models) {
          if (!matches(`${agent.type}/${m.value}`, `${agent.type}/${m.name}`)) continue
          list.push({
            agentType: agent.type,
            modelValue: m.value,
            label: `${agent.display_name} · ${truncateSelectLabel(m.name, 24)}`,
            modelLabel: truncateSelectLabel(m.name, 24),
            title: fullOptionLabel(`${agent.display_name} · ${m.name}`, m.description),
          })
        }
      } else if (matches(agent.type)) {
        // 模型未知（探测中/失败/该 agent 无模型配置）：仅显示 agent，使用其默认模型
        list.push({ agentType: agent.type, modelValue: '', label: agent.display_name, modelLabel: agent.display_name, title: agent.display_name })
      }
    }
    return list
  }, [agents, modelsByAgent, regexes])

  // 输入关键字实时过滤（匹配组合标签、agent 类型、模型值，忽略大小写）
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return entries
    return entries.filter((e) =>
      e.label.toLowerCase().includes(q)
      || e.title.toLowerCase().includes(q)
      || e.agentType.toLowerCase().includes(q)
      || e.modelValue.toLowerCase().includes(q),
    )
  }, [entries, query])

  // 触发按钮显示当前选中组合（即使不在下拉过滤范围内，也保留触发态可读性）
  const selectedEntry = useMemo(() => {
    if (!selectedAgent) return null
    const agent = agents.find((a) => a.type === selectedAgent)
    const model = (modelsByAgent[selectedAgent] || []).find((m) => m.value === selectedModel)
    const name = agent?.display_name || selectedAgent
    const modelName = selectedModel ? truncateSelectLabel(model?.name || selectedModel, 24) : name
    const label = selectedModel ? `${name} · ${modelName}` : name
    return {
      agentType: selectedAgent,
      modelValue: selectedModel,
      label,
      modelLabel: modelName,
      title: fullOptionLabel(label, model?.description),
    }
  }, [agents, modelsByAgent, selectedAgent, selectedModel])
  // 收起态触发按钮仅显示模型名（展开后列表项才显示完整 agent · 模型）
  const triggerLabel = selectedAgent ? (selectedEntry?.modelLabel || selectedModel || selectedAgent) : ''

  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  useEffect(() => {
    if (open) {
      setQuery('')
      requestAnimationFrame(() => searchRef.current?.focus())
    }
  }, [open])

  function toggleOpen() {
    if (disabled) return
    if (!open && containerRef.current) {
      // 空间不足时向上展开（如聊天输入栏底部）
      const rect = containerRef.current.getBoundingClientRect()
      setDropUp(window.innerHeight - rect.bottom < 340)
    }
    setOpen((v) => !v)
  }

  function handleSelect(agentType: string, modelValue: string) {
    onSelect(agentType, modelValue)
    setOpen(false)
  }

  if (agents.length === 0) {
    return (
      <button type="button" className={`${styles.trigger} ${className || ''}`} disabled>
        <span className={`${styles.triggerLabel} ${styles.triggerPlaceholder}`}>{t('docMode.noAgent')}</span>
      </button>
    )
  }

  return (
    <div className={styles.container} ref={containerRef}>
      <button
        type="button"
        className={`${styles.trigger} ${className || ''}`}
        onClick={toggleOpen}
        disabled={disabled}
        title={selectedEntry?.title}
      >
        <span className={`${styles.triggerLabel} ${!triggerLabel ? styles.triggerPlaceholder : ''}`}>
          {triggerLabel || placeholder || t('docMode.noAgent')}
        </span>
        <span className={styles.arrow}>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}</span>
      </button>

      {open && (
        <div className={`${styles.dropdown} ${dropUp ? styles.dropdownUp : ''}`}>
          <input
            ref={searchRef}
            className={styles.search}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t('session.searchModels')}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                setOpen(false)
                return
              }
              if (e.key === 'Enter' && filtered.length === 1) {
                handleSelect(filtered[0].agentType, filtered[0].modelValue)
              }
            }}
          />
          <div className={styles.list}>
            {placeholder !== undefined && !query.trim() && (
              <div
                className={`${styles.item} ${!selectedAgent ? styles.itemActive : ''}`}
                onClick={() => handleSelect('', '')}
              >
                <span className={`${styles.itemName} ${styles.triggerPlaceholder}`}>{placeholder}</span>
              </div>
            )}
            {filtered.length === 0 && (
              <div className={styles.empty}>{t('session.noModelsFound')}</div>
            )}
            {filtered.map((entry) => (
              <div
                key={`${entry.agentType}\u0000${entry.modelValue}`}
                className={`${styles.item} ${entry.agentType === selectedAgent && entry.modelValue === selectedModel ? styles.itemActive : ''}`}
                onClick={() => handleSelect(entry.agentType, entry.modelValue)}
                title={entry.title}
              >
                <span className={styles.itemName}>{entry.label}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}
