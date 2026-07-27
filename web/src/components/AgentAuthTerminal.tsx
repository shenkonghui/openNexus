import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import type { Terminal as XTerm } from '@xterm/xterm'
import type { FitAddon } from '@xterm/addon-fit'
import { buildWSURL, startTerminal } from './TerminalInstance'
import styles from './AgentAcpCapsPanel.module.css'

interface Props {
  agentType: string
  methodId: string
  methodName: string
  onClose: () => void
}

/**
 * terminal 类型认证的交互式登录终端：在 PTY 中运行 agent 的登录 TUI（OAuth 等），
 * 用户可点击终端中的授权 URL 在浏览器完成登录，凭证落盘后重连 agent 即生效。
 */
export default function AgentAuthTerminal({ agentType, methodId, methodName, onClose }: Props) {
  const { t } = useTranslation()
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<XTerm | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const fitRef = useRef<FitAddon | null>(null)

  useEffect(() => {
    if (!containerRef.current) return
    const url = buildWSURL(`/agents/${encodeURIComponent(agentType)}/auth-terminal?method_id=${encodeURIComponent(methodId)}`)
    return startTerminal(url, containerRef.current, termRef, wsRef, fitRef)
  }, [agentType, methodId])

  return (
    <div className={styles.authTerm}>
      <div className={styles.authTermHeader}>
        <span className={styles.authTermTitle}>
          {t('settings.acpCaps.loginTerminal')} · {methodName || methodId}
        </span>
        <span className={styles.authTermHint}>{t('settings.acpCaps.loginHint')}</span>
        <button type="button" className={styles.authTermClose} onClick={onClose}>
          {t('settings.acpCaps.loginClose')}
        </button>
      </div>
      <div ref={containerRef} className={styles.authTermBody} />
    </div>
  )
}
