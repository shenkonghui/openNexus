import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, X } from 'lucide-react'
import TerminalInstance, { buildSessionWSURL } from './TerminalInstance'
import AgentTerminalInstance, { type AgentTerminalHandle } from './AgentTerminalInstance'
import styles from './Terminal.module.css'

interface TerminalProps {
  sessionId: number
  onClose: () => void
}

interface TerminalTab {
  id: string
  kind: 'user' | 'agent'
  /** agent tab：对应后端 TerminalBridge 的 terminalId */
  terminalId?: string
  /** agent tab：标签标题（命令首词） */
  title?: string
  command?: string
  cwd?: string
}

/** agent 终端事件缓冲：xterm 实例挂载前先缓存 output/exit，挂载后由 onReady 回放。 */
interface AgentTermState {
  handle?: AgentTerminalHandle
  pending: Uint8Array[]
  exit?: { exitCode: number | null; signal: string | null }
  exitFlushed?: boolean
}

/** 后端 HandleAgentTerminal 下发的 JSON 帧。 */
interface AgentTermFrame {
  type: string
  terminalId: string
  command?: string
  cwd?: string
  data?: string
  exitCode?: number | null
  signal?: string | null
}

function decodeBase64(s: string): Uint8Array {
  const bin = atob(s)
  const buf = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i)
  return buf
}

function makeTabName(base: string, index: number): string {
  return index === 0 ? base : `${base} ${index + 1}`
}

export default function TerminalPanel({ sessionId, onClose }: TerminalProps) {
  const { t } = useTranslation()
  const baseName = t('panel.terminal')
  const counterRef = useRef(0)
  const [tabs, setTabs] = useState<TerminalTab[]>([{ id: 't-0', kind: 'user' }])
  const [activeId, setActiveId] = useState<string>('t-0')
  // terminalId → 事件缓冲/写入句柄
  const agentTermsRef = useRef(new Map<string, AgentTermState>())

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
      if (target.terminalId) agentTermsRef.current.delete(target.terminalId)
      const idx = prev.findIndex((tab) => tab.id === id)
      const next = prev.filter((tab) => tab.id !== id)
      if (activeId === id) {
        const fallback = next[idx - 1] ?? next[0]
        setActiveId(fallback.id)
      }
      return next
    })
  }, [activeId])

  // agent 终端实例挂载完成：记录写入句柄并回放挂载前缓冲的输出/退出事件
  const handleAgentReady = useCallback((terminalId: string, handle: AgentTerminalHandle) => {
    const st = agentTermsRef.current.get(terminalId)
    if (!st) return
    st.handle = handle
    for (const chunk of st.pending) handle.write(chunk)
    st.pending = []
    if (st.exit && !st.exitFlushed) {
      st.exitFlushed = true
      handle.writeExit(st.exit.exitCode, st.exit.signal)
    }
  }, [])

  // 订阅 agent 终端事件：agent 执行 shell 时自动新增只读 tab 并弹出终端面板
  useEffect(() => {
    let ws: WebSocket
    try {
      ws = new WebSocket(buildSessionWSURL(sessionId, 'agent-terminals'))
    } catch {
      return
    }
    ws.onmessage = (event) => {
      let frame: AgentTermFrame
      try {
        frame = JSON.parse(event.data)
      } catch {
        return
      }
      const terms = agentTermsRef.current
      switch (frame.type) {
        case 'created': {
          if (!frame.terminalId || terms.has(frame.terminalId)) return
          terms.set(frame.terminalId, { pending: [] })
          const id = `t-${++counterRef.current}`
          const firstWord = (frame.command || '').trim().split(/\s+/)[0] || 'agent'
          setTabs((prev) => [...prev, {
            id,
            kind: 'agent',
            terminalId: frame.terminalId,
            title: `▶ ${firstWord}`,
            command: frame.command || '',
            cwd: frame.cwd,
          }])
          setActiveId(id)
          window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'terminal' } }))
          break
        }
        case 'output': {
          const st = terms.get(frame.terminalId)
          if (!st || !frame.data) return
          const bytes = decodeBase64(frame.data)
          if (st.handle) st.handle.write(bytes)
          else st.pending.push(bytes)
          break
        }
        case 'exit': {
          const st = terms.get(frame.terminalId)
          if (!st) return
          st.exit = { exitCode: frame.exitCode ?? null, signal: frame.signal ?? null }
          if (st.handle && !st.exitFlushed) {
            st.exitFlushed = true
            st.handle.writeExit(st.exit.exitCode, st.exit.signal)
          }
          break
        }
        default:
          // released 等：保留 tab 供用户查看，无需处理
          break
      }
    }
    return () => {
      try { ws.close() } catch {}
    }
  }, [sessionId])

  const handleClosePanel = useCallback(() => {
    onClose()
  }, [onClose])

  const userTabIds = tabs.filter((tab) => tab.kind === 'user').map((tab) => tab.id)
  const tabLabel = (tab: TerminalTab): string =>
    tab.kind === 'user' ? makeTabName(baseName, userTabIds.indexOf(tab.id)) : (tab.title || '▶')
  const closable = (tab: TerminalTab): boolean =>
    tab.kind === 'agent' || userTabIds.length > 1

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <div className={styles.tabs}>
          {tabs.map((tab) => (
            <div
              key={tab.id}
              className={`${styles.tab} ${tab.id === activeId ? styles.activeTab : ''}`}
              onClick={() => setActiveId(tab.id)}
              role="tab"
              aria-selected={tab.id === activeId}
              title={tab.kind === 'agent' ? tab.command : undefined}
            >
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
              <TerminalInstance sessionId={sessionId} active={tab.id === activeId} />
            ) : (
              <AgentTerminalInstance
                command={tab.command || ''}
                cwd={tab.cwd}
                active={tab.id === activeId}
                onReady={(h) => handleAgentReady(tab.terminalId!, h)}
              />
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
