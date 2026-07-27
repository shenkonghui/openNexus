import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, Plus, X } from 'lucide-react'
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
}

/** agent 聚合终端 tab 的固定 id（同一时刻至多一个 agent tab）。 */
const AGENT_TAB_ID = 'agent'

/** agent 终端写入操作：按事件到达顺序排队，实例挂载前先缓冲，挂载后回放。 */
type AgentOp =
  | { kind: 'command'; command: string; cwd?: string }
  | { kind: 'output'; data: Uint8Array }
  | { kind: 'exit'; exitCode: number | null; signal: string | null }

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
  // agent 聚合终端：写入句柄 + 挂载前的操作缓冲（保持事件到达顺序）
  const agentHandleRef = useRef<AgentTerminalHandle | null>(null)
  const agentPendingRef = useRef<AgentOp[]>([])
  // 已见过的 terminalId（去重 created 帧）与已退出的 terminalId（去重 exit 帧）
  const seenTermsRef = useRef(new Set<string>())
  const exitedTermsRef = useRef(new Set<string>())

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
        agentHandleRef.current = null
        agentPendingRef.current = []
      }
      const idx = prev.findIndex((tab) => tab.id === id)
      const next = prev.filter((tab) => tab.id !== id)
      if (activeId === id) {
        const fallback = next[idx - 1] ?? next[0]
        setActiveId(fallback.id)
      }
      return next
    })
  }, [activeId])

  // agent 聚合终端实例挂载完成：记录写入句柄并按顺序回放挂载前缓冲的操作
  const handleAgentReady = useCallback((handle: AgentTerminalHandle) => {
    agentHandleRef.current = handle
    const pending = agentPendingRef.current
    agentPendingRef.current = []
    for (const op of pending) applyAgentOp(handle, op)
  }, [])

  // 订阅 agent 终端事件：所有 agent shell 命令聚合到同一个只读 tab 展示
  useEffect(() => {
    let ws: WebSocket
    try {
      ws = new WebSocket(buildSessionWSURL(sessionId, 'agent-terminals'))
    } catch {
      return
    }
    const pushOp = (op: AgentOp) => {
      const handle = agentHandleRef.current
      if (handle) applyAgentOp(handle, op)
      else agentPendingRef.current.push(op)
    }
    ws.onmessage = (event) => {
      let frame: AgentTermFrame
      try {
        frame = JSON.parse(event.data)
      } catch {
        return
      }
      switch (frame.type) {
        case 'created': {
          if (!frame.terminalId || seenTermsRef.current.has(frame.terminalId)) return
          seenTermsRef.current.add(frame.terminalId)
          pushOp({ kind: 'command', command: frame.command || '', cwd: frame.cwd })
          // 确保 agent tab 存在（唯一、常驻），激活并弹出终端面板
          setTabs((prev) => (prev.some((tab) => tab.kind === 'agent')
            ? prev
            : [...prev, { id: AGENT_TAB_ID, kind: 'agent' }]))
          setActiveId(AGENT_TAB_ID)
          window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'terminal' } }))
          break
        }
        case 'output': {
          if (!frame.data || !seenTermsRef.current.has(frame.terminalId)) return
          pushOp({ kind: 'output', data: decodeBase64(frame.data) })
          break
        }
        case 'exit': {
          if (!seenTermsRef.current.has(frame.terminalId) || exitedTermsRef.current.has(frame.terminalId)) return
          exitedTermsRef.current.add(frame.terminalId)
          pushOp({ kind: 'exit', exitCode: frame.exitCode ?? null, signal: frame.signal ?? null })
          break
        }
        default:
          // released 等：内容保留在聚合终端中供用户查看，无需处理
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
              <TerminalInstance sessionId={sessionId} active={tab.id === activeId} />
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

/** 把一条写入操作应用到聚合终端实例。 */
function applyAgentOp(handle: AgentTerminalHandle, op: AgentOp) {
  switch (op.kind) {
    case 'command':
      handle.writeCommand(op.command, op.cwd)
      break
    case 'output':
      handle.write(op.data)
      break
    case 'exit':
      handle.writeExit(op.exitCode, op.signal)
      break
  }
}
