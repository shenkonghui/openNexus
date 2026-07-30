import { useCallback, useEffect, useRef } from 'react'
import { buildSessionWSURL } from '../components/TerminalInstance'
import type { AgentTerminalHandle } from '../components/AgentTerminalInstance'

/** agent 终端写入操作：按事件到达顺序排队，实例挂载前先缓冲，挂载后回放。 */
export type AgentOp =
  | { kind: 'command'; command: string; cwd?: string }
  | { kind: 'output'; data: Uint8Array }
  | { kind: 'exit'; exitCode: number | null; signal: string | null }

/** 后端 HandleAgentTerminal 下发的 JSON 帧。 */
export interface AgentTermFrame {
  type: string
  terminalId: string
  command?: string
  cwd?: string
  data?: string
  exitCode?: number | null
  signal?: string | null
}

export function decodeBase64(s: string): Uint8Array {
  const bin = atob(s)
  const buf = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i)
  return buf
}

/** 把一条写入操作应用到终端实例。 */
export function applyAgentOp(handle: AgentTerminalHandle, op: AgentOp) {
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

export interface UseAgentTerminalStreamOptions {
  sessionId?: number
  /** 为 false 时不建立订阅（如内嵌窗口已移动到终端面板） */
  enabled?: boolean
  /** 每条新命令（created 帧，已去重）到达时回调：用于弹出 tab / 显示内嵌窗口 */
  onCommand?: (command: string, cwd?: string) => void
}

/**
 * 订阅会话的 agent 终端事件流（/sessions/:id/agent-terminals）：
 * 后端连接后先回放活跃终端快照，再持续推送实时事件；本 hook 负责按事件顺序
 * 写入 AgentTerminalInstance——实例挂载前先缓冲，挂载后（handleReady）回放。
 * 终端面板的 agent tab 与对话内嵌窗口共用此逻辑（各自独立订阅）。
 */
export function useAgentTerminalStream({ sessionId, enabled = true, onCommand }: UseAgentTerminalStreamOptions) {
  const handleRef = useRef<AgentTerminalHandle | null>(null)
  const pendingRef = useRef<AgentOp[]>([])
  // 已见过的 terminalId（去重 created 帧）与已退出的 terminalId（去重 exit 帧）
  const seenTermsRef = useRef(new Set<string>())
  const exitedTermsRef = useRef(new Set<string>())
  const onCommandRef = useRef(onCommand)
  onCommandRef.current = onCommand

  // 聚合终端实例挂载完成：记录写入句柄并按顺序回放挂载前缓冲的操作
  const handleReady = useCallback((handle: AgentTerminalHandle) => {
    handleRef.current = handle
    const pending = pendingRef.current
    pendingRef.current = []
    for (const op of pending) applyAgentOp(handle, op)
  }, [])

  /** 丢弃句柄与缓冲（如关闭 agent tab），下次命令到达时重建实例后再回放。 */
  const reset = useCallback(() => {
    handleRef.current = null
    pendingRef.current = []
  }, [])

  useEffect(() => {
    if (sessionId == null || !enabled) return
    let ws: WebSocket
    try {
      ws = new WebSocket(buildSessionWSURL(sessionId, 'agent-terminals'))
    } catch {
      return
    }
    const pushOp = (op: AgentOp) => {
      const handle = handleRef.current
      if (handle) applyAgentOp(handle, op)
      else pendingRef.current.push(op)
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
          onCommandRef.current?.(frame.command || '', frame.cwd)
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
  }, [sessionId, enabled])

  return { handleReady, reset }
}
