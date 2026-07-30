import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, Plus, X } from 'lucide-react'
import TerminalInstance from './TerminalInstance'
import AgentTerminalInstance from './AgentTerminalInstance'
import { useAgentTerminalStream } from '../hooks/useAgentTerminalStream'
import styles from './Terminal.module.css'

interface TerminalProps {
  /** 会话 ID；未提供时需提供 workspaceId（任务尚未开始的工作区终端） */
  sessionId?: number
  /** 工作区 ID：无会话时在工作区 cwd 下启动 shell */
  workspaceId?: number
  onClose: () => void
}

interface TerminalTab {
  id: string
  kind: 'user' | 'agent'
}

/** agent 聚合终端 tab 的固定 id（同一时刻至多一个 agent tab）。 */
const AGENT_TAB_ID = 'agent'

function makeTabName(base: string, index: number): string {
  return index === 0 ? base : `${base} ${index + 1}`
}

export default function TerminalPanel({ sessionId, workspaceId, onClose }: TerminalProps) {
  const { t } = useTranslation()
  const baseName = t('panel.terminal')
  const counterRef = useRef(0)
  const [tabs, setTabs] = useState<TerminalTab[]>([{ id: 't-0', kind: 'user' }])
  const [activeId, setActiveId] = useState<string>('t-0')

  // 订阅 agent 终端事件流：新命令到达时确保 agent tab 存在（不自动激活，
  // 默认由对话内嵌窗口实时展示，不强行弹出/切换终端面板）
  const { handleReady: handleAgentReady, reset: resetAgentStream } = useAgentTerminalStream({
    sessionId,
    onCommand: () => {
      setTabs((prev) => (prev.some((tab) => tab.kind === 'agent')
        ? prev
        : [...prev, { id: AGENT_TAB_ID, kind: 'agent' }]))
    },
  })

  const addTab = useCallback(() => {
    const id = `t-${++counterRef.current}`
    setTabs((prev) => [...prev, { id, kind: 'user' }])
    setActiveId(id)
  }, [])

  const closeTab = useCallback((e: React.MouseEvent, id: string) => {
    e.stopPropagation()
    setTabs((prev) => {
      const target = prev.find((tab) => tab.id === id)
      if (!target) return prev
      // 至少保留一个用户终端 tab；agent tab 随时可关
      if (target.kind === 'user' && prev.filter((tab) => tab.kind === 'user').length <= 1) return prev
      if (target.kind === 'agent') {
        // 关闭 agent tab：丢弃句柄与缓冲，下次 agent 执行命令时重建
        resetAgentStream()
      }
      const idx = prev.findIndex((tab) => tab.id === id)
      const next = prev.filter((tab) => tab.id !== id)
      if (activeId === id) {
        const fallback = next[idx - 1] ?? next[0]
        setActiveId(fallback.id)
      }
      return next
    })
  }, [activeId, resetAgentStream])

  // 「移到终端面板」：对话内嵌窗口点击移动按钮后，确保 agent tab 存在并激活
  useEffect(() => {
    const onFocus = () => {
      setTabs((prev) => (prev.some((tab) => tab.kind === 'agent')
        ? prev
        : [...prev, { id: AGENT_TAB_ID, kind: 'agent' }]))
      setActiveId(AGENT_TAB_ID)
    }
    window.addEventListener('onx:agent-terminal-focus', onFocus)
    return () => window.removeEventListener('onx:agent-terminal-focus', onFocus)
  }, [])

  const handleClosePanel = useCallback(() => {
    onClose()
  }, [onClose])

  const userTabIds = tabs.filter((tab) => tab.kind === 'user').map((tab) => tab.id)
  const tabLabel = (tab: TerminalTab): string =>
    tab.kind === 'user' ? makeTabName(baseName, userTabIds.indexOf(tab.id)) : t('agentTerminal.tabName')
  const closable = (tab: TerminalTab): boolean =>
    tab.kind === 'agent' || userTabIds.length > 1

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <div className={styles.tabs}>
          {tabs.map((tab) => (
            <div
              key={tab.id}
              className={`${styles.tab} ${tab.kind === 'agent' ? styles.agentTab : ''} ${tab.id === activeId ? styles.activeTab : ''}`}
              onClick={() => setActiveId(tab.id)}
              role="tab"
              aria-selected={tab.id === activeId}
              title={tab.kind === 'agent' ? t('agentTerminal.tabHint') : undefined}
            >
              {tab.kind === 'agent' && <Bot size={13} className={styles.agentTabIcon} />}
              <span className={styles.tabName}>{tabLabel(tab)}</span>
              {closable(tab) && (
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
            {tab.kind === 'user' ? (
              <TerminalInstance sessionId={sessionId} workspaceId={workspaceId} active={tab.id === activeId} />
            ) : (
              <AgentTerminalInstance
                active={tab.id === activeId}
                onReady={handleAgentReady}
              />
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
