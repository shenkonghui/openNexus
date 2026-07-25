import { useCallback, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, X } from 'lucide-react'
import TerminalInstance from './TerminalInstance'
import styles from './Terminal.module.css'

interface TerminalProps {
  sessionId: number
  onClose: () => void
}

interface TerminalTab {
  id: string
}

function makeTabName(base: string, index: number): string {
  return index === 0 ? base : `${base} ${index + 1}`
}

export default function TerminalPanel({ sessionId, onClose }: TerminalProps) {
  const { t } = useTranslation()
  const baseName = t('panel.terminal')
  const counterRef = useRef(0)
  const [tabs, setTabs] = useState<TerminalTab[]>([{ id: 't-0' }])
  const [activeId, setActiveId] = useState<string>('t-0')

  const addTab = useCallback(() => {
    const id = `t-${++counterRef.current}`
    setTabs((prev) => [...prev, { id }])
    setActiveId(id)
  }, [])

  const closeTab = useCallback((e: React.MouseEvent, id: string) => {
    e.stopPropagation()
    setTabs((prev) => {
      if (prev.length <= 1) return prev
      const idx = prev.findIndex((t) => t.id === id)
      const next = prev.filter((t) => t.id !== id)
      if (activeId === id) {
        const fallback = next[idx - 1] ?? next[0]
        setActiveId(fallback.id)
      }
      return next
    })
  }, [activeId])

  const handleClosePanel = useCallback(() => {
    onClose()
  }, [onClose])

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <div className={styles.tabs}>
          {tabs.map((tab, idx) => (
            <div
              key={tab.id}
              className={`${styles.tab} ${tab.id === activeId ? styles.activeTab : ''}`}
              onClick={() => setActiveId(tab.id)}
              role="tab"
              aria-selected={tab.id === activeId}
            >
              <span className={styles.tabName}>{makeTabName(baseName, idx)}</span>
              {tabs.length > 1 && (
                <button
                  type="button"
                  className={styles.tabClose}
                  onClick={(e) => closeTab(e, tab.id)}
                  title={t('common.close')}
                >
                  <X size={12} />
                </button>
              )}
            </div>
          ))}
          <button
            type="button"
            className={styles.addBtn}
            onClick={addTab}
            title={t('common.create')}
          >
            <Plus size={14} />
          </button>
        </div>
        <button
          type="button"
          className={styles.closeBtn}
          onClick={handleClosePanel}
          title={t('common.close')}
        >
          <X size={16} />
        </button>
      </div>
      <div className={styles.terminals}>
        {tabs.map((tab) => (
          <div
            key={tab.id}
            className={styles.terminalWrapper}
            style={{ display: tab.id === activeId ? 'flex' : 'none' }}
          >
            <TerminalInstance sessionId={sessionId} active={tab.id === activeId} />
          </div>
        ))}
      </div>
    </div>
  )
}
