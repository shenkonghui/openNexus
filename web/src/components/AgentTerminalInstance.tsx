import { useEffect, useRef } from 'react'
import { Terminal as XTerm } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { useTranslation } from 'react-i18next'
import '@xterm/xterm/css/xterm.css'
import styles from './Terminal.module.css'

/** agent 终端实例的写入句柄：由父组件（TerminalPanel）按 terminalId 路由事件调用。 */
export interface AgentTerminalHandle {
  write: (data: Uint8Array) => void
  writeExit: (exitCode: number | null, signal: string | null) => void
}

export interface AgentTerminalInstanceProps {
  command: string
  cwd?: string
  active: boolean
  /** 挂载完成后回调写入句柄（父组件负责回放实例挂载前缓冲的输出/退出事件） */
  onReady: (handle: AgentTerminalHandle) => void
}

/** 只读 xterm：展示 agent 通过 ACP terminal 能力执行的命令输出，不支持输入。 */
export default function AgentTerminalInstance({ command, cwd, active, onReady }: AgentTerminalInstanceProps) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const fitRef = useRef<FitAddon | null>(null)
  // onReady / t 用 ref 持有最新值，避免作为挂载 effect 依赖导致 xterm 重建
  const onReadyRef = useRef(onReady)
  onReadyRef.current = onReady
  const tRef = useRef(t)
  tRef.current = t

  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    const term = new XTerm({
      fontSize: 13,
      fontFamily: "'Monaco', 'Menlo', 'Courier New', monospace",
      cursorBlink: false,
      disableStdin: true,
      theme: { background: '#1e1e2e', foreground: '#cdd6f4', cursor: '#f5e0dc' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.loadAddon(new WebLinksAddon())
    term.open(container)
    try { fit.fit() } catch {}
    fitRef.current = fit

    // 首行展示工作目录与执行的命令
    if (cwd) term.writeln(`\x1b[90m${cwd}\x1b[0m`)
    term.writeln(`\x1b[1;32m$\x1b[0m \x1b[1m${command}\x1b[0m`)

    const resizeObserver = new ResizeObserver(() => { try { fit.fit() } catch {} })
    resizeObserver.observe(container)

    onReadyRef.current({
      write: (data) => term.write(data),
      writeExit: (exitCode, signal) => {
        const msg = signal
          ? tRef.current('agentTerminal.signal', { signal })
          : tRef.current('agentTerminal.exitCode', { code: exitCode ?? 0 })
        const color = !signal && exitCode === 0 ? '32' : '31'
        term.write(`\r\n\x1b[${color}m${msg}\x1b[0m\r\n`)
      },
    })

    return () => {
      resizeObserver.disconnect()
      term.dispose()
      fitRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!active) return
    const timer = setTimeout(() => { try { fitRef.current?.fit() } catch {} }, 50)
    return () => clearTimeout(timer)
  }, [active])

  return <div ref={containerRef} className={styles.terminal} />
}
