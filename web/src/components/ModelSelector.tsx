import { useState, useRef, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Settings2 } from 'lucide-react'
import type { ConfigOption } from '../types'
import { formatOptionLabel, fullOptionLabel } from '../utils/selectLabel'
import styles from './ModelSelector.module.css'

interface ModelSelectorProps {
  options: ConfigOption[]
  onApply: (configId: string, value: string) => void
  disabled?: boolean
  /** 隐藏模型下拉（模型已合并进 AgentModelSelector 时只保留「更多选项」） */
  hideModel?: boolean
}

function optionLabel(opt: ConfigOption, name: string, description?: string): string {
  const isModel = opt.category === 'model'
  return formatOptionLabel(name, description, isModel ? undefined : 10)
}

function currentSelectTitle(opt: ConfigOption): string | undefined {
  const current = opt.options.find((o) => o.value === opt.current_value)
  if (!current) return undefined
  return fullOptionLabel(current.name, current.description)
}

// 单个配置项下拉框
function ConfigItem({ opt, onApply, disabled }: { opt: ConfigOption; onApply: (id: string, v: string) => void; disabled?: boolean }) {
  const isModel = opt.category === 'model'
  return (
    <div className={styles.item}>
      <select
        className={`${styles.select} ${isModel ? '' : styles.selectCompact}`}
        value={opt.current_value}
        disabled={disabled}
        title={currentSelectTitle(opt)}
        onChange={(e) => onApply(opt.id, e.target.value)}
      >
        {opt.options.map((v) => (
          <option key={v.value} value={v.value} title={fullOptionLabel(v.name, v.description)}>
            {optionLabel(opt, v.name, v.description)}
          </option>
        ))}
      </select>
    </div>
  )
}

// ModelSelector：默认只显示模型，其余配置收进「更多选项」下拉
export default function ModelSelector({ options, onApply, disabled, hideModel }: ModelSelectorProps) {
  const { t } = useTranslation()
  const [moreOpen, setMoreOpen] = useState(false)
  const moreRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (moreRef.current && !moreRef.current.contains(e.target as Node)) {
        setMoreOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [])

  const selectable = options.filter((o) => o.type === 'select' && o.options.length > 0 && o.category !== 'mode')
  const modelOpt = hideModel ? undefined : selectable.find((o) => o.category === 'model')
  const others = selectable.filter((o) => o.category !== 'model')

  if (!modelOpt && others.length === 0) return null

  return (
    <>
      {modelOpt && <ConfigItem opt={modelOpt} onApply={onApply} disabled={disabled} />}
      {others.length > 0 && (
        <div className={styles.moreWrap} ref={moreRef}>
          <button
            type="button"
            className={styles.moreBtn}
            disabled={disabled}
            title={t('chat.moreOptions')}
            onClick={() => setMoreOpen((v) => !v)}
          >
            <Settings2 size={14} />
            {t('chat.moreOptions')}
          </button>
          {moreOpen && (
            <div className={styles.dropdown}>
              {others.map((opt) => (
                <ConfigItem key={opt.id} opt={opt} onApply={onApply} disabled={disabled} />
              ))}
            </div>
          )}
        </div>
      )}
    </>
  )
}
