import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { buildSessionWSURL } from '../components/TerminalInstance'
import type { AgentTerminalHandle } from '../components/AgentTerminalInstance'
import { applyAgentOp, decodeBase64, type AgentOp, type AgentTermFrame } from '../hooks/useAgentTerminalStream'

/** 单个 agent 终端的元信息（输出内容保存在 provider 内部缓冲，不进 state）。 */
export interface AgentTermMeta {
  terminalId: string
  command: string
  cwd?: string
  exited: boolean
  exitCode: number | null
  signal: string | null
  /** 该终端已被用户移动到右侧终端面板：对话中不再渲染窗口（仅影响这一个终端） */
  moved: boolean
}

interface AgentTerminalsCtxValue {
  entries: ReadonlyMap<string, AgentTermMeta>
  /** 纯展示模式（如多任务实时窗口）：内嵌终端不显示收起/移动按钮 */
  plain: boolean
  /** 内嵌 xterm 挂载完成：回放该终端的全部缓冲并注册为实时写入句柄 */
  attach: (terminalId: string, handle: AgentTerminalHandle) => void
  /** 内嵌 xterm 卸载（滚出可视区/折叠）：解除写入句柄，缓冲保留供重挂回放 */
  detach: (terminalId: string) => void
  /** 把单个终端移动到右侧终端面板（其余终端不受影响） */
  moveToPanel: (terminalId: string) => void
}

const AgentTerminalsContext = createContext<AgentTerminalsCtxValue | null>(null)

/** 无 provider 时返回 null（复用 MessageList 但未包裹 provider 的场景，不渲染内嵌终端）。 */
export function useAgentTerminals(): AgentTerminalsCtxValue | null {
  return useContext(AgentTerminalsContext)
}

/** 单终端输出缓冲字节上限：超出后丢弃最早的 output 段，防止长任务撑爆内存。 */
const MAX_BUFFER_BYTES = 512 * 1024

interface TermBuffer {
  ops: AgentOp[]
  bytes: number
  exited: boolean
}

/** 追加 output 并裁剪超限的最早输出段（保留 ops[0] 的命令头）。 */
function pushOutput(buf: TermBuffer, data: Uint8Array) {
  buf.ops.push({ kind: 'output', data })
  buf.bytes += data.length
  while (buf.bytes > MAX_BUFFER_BYTES) {
    const idx = buf.ops.findIndex((op) => op.kind === 'output')
    if (idx < 0) break
    const dropped = buf.ops.splice(idx, 1)[0]
    if (dropped.kind === 'output') buf.bytes -= dropped.data.length
  }
}

/**
 * 会话级 agent 终端流 provider：单一 WS 订阅 /sessions/:id/agent-terminals，
 * 按 terminalId 维护各自的输出缓冲与元信息，供对话中锚定在 tool_call 输出位置的
 * 内嵌终端窗口（InlineAgentTerminal）实时渲染；虚拟列表卸载重挂时通过 attach 全量回放。
 */
export function AgentTerminalsProvider({ sessionId, plain = false, children }: { sessionId?: number; plain?: boolean; children: ReactNode }) {
  const [entries, setEntries] = useState<Map<string, AgentTermMeta>>(() => new Map())
  const bufsRef = useRef(new Map<string, TermBuffer>())
  const handlesRef = useRef(new Map<string, AgentTerminalHandle>())

  useEffect(() => {
    // 会话切换：清空全部缓冲与元信息（后端会对新连接回放活跃终端快照）
    setEntries(new Map())
    bufsRef.current = new Map()
    handlesRef.current = new Map()
    if (sessionId == null) return
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
      const bufs = bufsRef.current
      switch (frame.type) {
        case 'created': {
          if (!frame.terminalId || bufs.has(frame.terminalId)) return
          const op: AgentOp = { kind: 'command', command: frame.command || '', cwd: frame.cwd }
          bufs.set(frame.terminalId, { ops: [op], bytes: 0, exited: false })
          setEntries((prev) => new Map(prev).set(frame.terminalId, {
            terminalId: frame.terminalId,
            command: frame.command || '',
            cwd: frame.cwd,
            exited: false,
            exitCode: null,
            signal: null,
            moved: false,
          }))
          break
        }
        case 'output': {
          const buf = bufs.get(frame.terminalId)
          if (!buf || !frame.data) return
          const data = decodeBase64(frame.data)
          pushOutput(buf, data)
          handlesRef.current.get(frame.terminalId)?.write(data)
          break
        }
        case 'exit': {
          const buf = bufs.get(frame.terminalId)
          if (!buf || buf.exited) return
          buf.exited = true
          const exitCode = frame.exitCode ?? null
          const signal = frame.signal ?? null
          buf.ops.push({ kind: 'exit', exitCode, signal })
          handlesRef.current.get(frame.terminalId)?.writeExit(exitCode, signal)
          setEntries((prev) => {
            const meta = prev.get(frame.terminalId)
            if (!meta) return prev
            return new Map(prev).set(frame.terminalId, { ...meta, exited: true, exitCode, signal })
          })
          break
        }
        default:
          // released 等：缓冲保留供用户查看，无需处理
          break
      }
    }
    return () => {
      try { ws.close() } catch {}
    }
  }, [sessionId])

  const attach = useCallback((terminalId: string, handle: AgentTerminalHandle) => {
    const buf = bufsRef.current.get(terminalId)
    if (buf) for (const op of buf.ops) applyAgentOp(handle, op)
    handlesRef.current.set(terminalId, handle)
  }, [])

  const detach = useCallback((terminalId: string) => {
    handlesRef.current.delete(terminalId)
  }, [])

  const moveToPanel = useCallback((terminalId: string) => {
    setEntries((prev) => {
      const meta = prev.get(terminalId)
      if (!meta || meta.moved) return prev
      return new Map(prev).set(terminalId, { ...meta, moved: true })
    })
    // 打开右侧终端面板并激活其中的 Agent 终端 tab（面板聚合展示所有命令）
    window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'terminal' } }))
    window.dispatchEvent(new CustomEvent('onx:agent-terminal-focus'))
  }, [])

  const value = useMemo(
    () => ({ entries, plain, attach, detach, moveToPanel }),
    [entries, plain, attach, detach, moveToPanel],
  )
  return <AgentTerminalsContext.Provider value={value}>{children}</AgentTerminalsContext.Provider>
}
