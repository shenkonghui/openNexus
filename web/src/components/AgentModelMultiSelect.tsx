import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronUp, ChevronDown } from 'lucide-react'
import type { ConfigOptionValue } from '../types'
import styles from './AgentModelMultiSelect.module.css'

interface AgentItem {
  type: string
  display_name: string
}

export interface AgentModelGroup {
  agent: AgentItem
  models: ConfigOptionValue[]
}

interface Props {
  /** 按 agent 分组的候选组合（模型未探测到的 agent 为 agent 级单项） */
  groups: AgentModelGroup[]
  /** 勾选状态，key 为 `${agentType}\u0000${modelValue}`（agent 级条目 modelValue 为空） */
  checked: Record<string, boolean>
  onToggle: (key: string) => void
  /** 整组勾选/取消（传该组全部 key） */
  onToggleGroup: (keys: string[]) => void
  disabled?: boolean
  className?: string
}

function groupKeys(g: AgentModelGroup): string[] {
  return g.models.length > 0
    ? g.models.map((m) => `${g.agent.type}\u0000${m.value}`)
    : [`${g.agent.type}\u0000`]
}

/**
 * AgentModelMultiSelect：Agent·模型组合的多选下拉框。
 * 触发按钮显示已选数量摘要；浮层内支持关键字过滤（匹配 agent 名/类型、模型名/值），
 * 每行右侧复选框勾选，组头可整组勾选（部分勾选显示半选态）。
 */
export default function AgentModelMultiSelect({
  groups,
  checked,
  onToggle,
  onToggleGroup,
  disabled,
  className,
}: Props) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [dropUp, setDropUp] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)
  const searchRef = useRef<HTMLInputElement>(null)

  const totalKeys = useMemo(() => groups.flatMap(groupKeys), [groups])
  const checkedCount = totalKeys.filter((k) => checked[k]).length

  // 关键字过滤：agent 名/类型命中显示整组；否则仅显示命中的模型行
  const visibleGroups = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return groups
    const out: AgentModelGroup[] = []
    for (const g of groups) {
      const agentHit = g.agent.display_name.toLowerCase().includes(q)
        || g.agent.type.toLowerCase().includes(q)
      if (agentHit) {
        out.push(g)
        continue
      }
      const models = g.models.filter((m) =>
        m.name.toLowerCase().includes(q) || m.value.toLowerCase().includes(q))
      if (models.length > 0) out.push({ agent: g.agent, models })
    }
    return out
  }, [groups, query])

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
      // 空间不足时向上展开
      const rect = containerRef.current.getBoundingClientRect()
      setDropUp(window.innerHeight - rect.bottom < 380)
    }
    setOpen((v) => !v)
  }

  return (
    <div className={styles.container} ref={containerRef}>
      <button
        type="button"
        className={`${styles.trigger} ${className || ''}`}
        onClick={toggleOpen}
        disabled={disabled}
      >
        <span className={styles.triggerLabel}>
          {t('settings.selectorSelected', { count: checkedCount, total: totalKeys.length })}
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
            onKeyDown={(e) => { if (e.key === 'Escape') setOpen(false) }}
          />
          <div className={styles.list}>
            {visibleGroups.length === 0 && (
              <div className={styles.empty}>{t('session.noModelsFound')}</div>
            )}
            {visibleGroups.map((g) => {
              // 组勾选状态按该 agent 的完整 key 集合计算（不受搜索过滤影响，避免误清未显示项）
              const fullGroup = groups.find((x) => x.agent.type === g.agent.type) || g
              const keys = groupKeys(fullGroup)
              const onCount = keys.filter((k) => checked[k]).length
              return (
                <div key={g.agent.type}>
                  <div className={styles.groupRow} onClick={() => onToggleGroup(keys)}>
                    <span className={styles.groupName}>
                      {g.agent.display_name}（{g.agent.type}）
                      <span className={styles.groupCount}>{onCount}/{keys.length}</span>
                    </span>
                    <input
                      type="checkbox"
                      className={styles.checkbox}
                      checked={onCount === keys.length}
                      ref={(el) => { if (el) el.indeterminate = onCount > 0 && onCount < keys.length }}
                      readOnly
                    />
                  </div>
                  {g.models.map((m) => {
                    const key = `${g.agent.type}\u0000${m.value}`
                    return (
                      <div
                        key={key}
                        className={styles.itemRow}
                        title={m.value}
                        onClick={() => onToggle(key)}
                      >
                        <span className={styles.itemName}>
                          {m.name !== m.value ? `${m.name} (${m.value})` : m.value}
                        </span>
                        <input
                          type="checkbox"
                          className={styles.checkbox}
                          checked={!!checked[key]}
                          readOnly
                        />
                      </div>
                    )
                  })}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}
