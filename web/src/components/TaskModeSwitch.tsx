import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronUp, ChevronDown } from 'lucide-react'
import { MODES } from '../modes/registry'
import styles from './TaskModeSwitch.module.css'

/**
 * 模式 id 的类型。从注册表派生——新增模式不需要改这里。
 * 保留显式联合让旧代码（localStorage 读写、类型断言）有字面量提示；
 * 任意 string 也接受，向后兼容老数据。
 */
export type TaskMode = string

interface TaskModeSwitchProps {
  value: TaskMode
  onChange: (mode: TaskMode) => void
  /** 禁用切换：任务创建后锁定，不可再更改任务类型。 */
  disabled?: boolean
}

/** 任务类型可选模式：不含「任务管理」（任务管理走侧边栏「任务管理」独立页）。 */
const TASK_TYPE_MODES = MODES.filter((m) => m.id !== 'taskmanager')

/**
 * 模式切换器：下拉框形式，选项为编码/文档等任务类型。
 * 编排不在此列——入口为侧边栏「任务编排」。
 *
 * disabled 时仅展示当前任务类型，不展开下拉、不显示箭头。
 */
export default function TaskModeSwitch({ value, onChange, disabled }: TaskModeSwitchProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  const current = TASK_TYPE_MODES.find((m) => m.id === value) || TASK_TYPE_MODES[0]

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

  function handleSelect(id: string) {
    onChange(id)
    setOpen(false)
  }

  return (
    <div className={styles.container} ref={containerRef}>
      <button
        type="button"
        className={`${styles.trigger} ${disabled ? styles.triggerDisabled : ''}`}
        onClick={() => { if (!disabled) setOpen((v) => !v) }}
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        title={current ? t(current.titleKey) : undefined}
      >
        {current?.icon}
        <span className={styles.label}>{current ? t(current.titleKey) : ''}</span>
        {!disabled && (
          <span className={styles.arrow}>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}</span>
        )}
      </button>

      {open && !disabled && (
        <div className={styles.dropdown} role="listbox">
          {TASK_TYPE_MODES.map((opt) => (
            <button
              key={opt.id}
              type="button"
              role="option"
              aria-selected={value === opt.id}
              className={`${styles.item} ${value === opt.id ? styles.itemActive : ''}`}
              onClick={() => handleSelect(opt.id)}
            >
              {opt.icon}
              <span className={styles.label}>{t(opt.titleKey)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
