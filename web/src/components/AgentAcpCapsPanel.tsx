import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { getAgentCapabilities, preconnectAgent, authenticateAgent, streamAgentAuthEvents } from '../api/agents'
import type { AgentAcpCapabilities, AcpMethodItem, AcpAuthMethodItem } from '../types'
import AgentAuthTerminal from './AgentAuthTerminal'
import styles from './AgentAcpCapsPanel.module.css'

interface Props {
  agentType: string
}

/** 单张方法表：方法名 + 支持状态 + 门控 capability（unstable 加标记）。 */
function MethodTable({ title, items }: { title: string; items: AcpMethodItem[] }) {
  const { t } = useTranslation()
  return (
    <div className={styles.methodSection}>
      <div className={styles.methodTitle}>{title}</div>
      <table className={styles.methodTable}>
        <thead>
          <tr>
            <th>{t('settings.acpCaps.method')}</th>
            <th>{t('settings.acpCaps.supported')}</th>
            <th>{t('settings.acpCaps.gate')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((m) => (
            <tr key={m.method} className={m.supported ? '' : styles.rowUnsupported}>
              <td><code className={styles.methodName}>{m.method}</code></td>
              <td className={m.supported ? styles.yes : styles.no}>{m.supported ? '✓' : '✗'}</td>
              <td className={styles.gateCell}>
                {m.gate ? <code className={styles.gate}>{m.gate}</code> : <span className={styles.baseline}>{t('settings.acpCaps.baseline')}</span>}
                {m.unstable && <span className={styles.unstableBadge}>unstable</span>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

/**
 * ACP 能力面板：展示某 agent 类型最近一次握手声明的能力，
 * 包括 agent-side method（client → agent）与 client-side method（agent → client）支持情况。
 * 尚未握手时可一键预连接后自动重取。
 */
export default function AgentAcpCapsPanel({ agentType }: Props) {
  const { t } = useTranslation()
  const [caps, setCaps] = useState<AgentAcpCapabilities | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [connecting, setConnecting] = useState(false)
  // 当前打开登录终端的 terminal 认证方式（null 表示未打开）
  const [loginMethod, setLoginMethod] = useState<AcpAuthMethodItem | null>(null)
  // agent 类型认证的触发状态（agent 自行拉起浏览器登录）
  const [agentAuthPending, setAgentAuthPending] = useState('')
  // agent 类型认证完成后展示的成功提示（elicitation/complete 收到后设置）
  const [agentAuthDone, setAgentAuthDone] = useState(false)
  const pollTimer = useRef<number | null>(null)
  // auth-events SSE 断开函数（组件卸载或重新触发时调用）
  const authEventsCleanup = useRef<(() => void) | null>(null)

  const load = useCallback(async () => {
    try {
      const { data } = await getAgentCapabilities(agentType)
      setCaps(data)
      setError('')
      return data
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      return null
    } finally {
      setLoading(false)
    }
  }, [agentType, t])

  useEffect(() => {
    setCaps(null); setLoading(true); setError(''); setConnecting(false); setLoginMethod(null); setAgentAuthDone(false)
    load()
    return () => {
      if (pollTimer.current !== null) window.clearTimeout(pollTimer.current)
      if (authEventsCleanup.current) { authEventsCleanup.current(); authEventsCleanup.current = null }
    }
  }, [load])

  // 触发预连接并轮询能力（握手完成后 available 变 true）
  function handleConnect() {
    if (connecting) return
    setConnecting(true)
    preconnectAgent(agentType)
    let attempts = 0
    const poll = async () => {
      attempts++
      const data = await load()
      if (data?.available || attempts >= 15) {
        setConnecting(false)
        return
      }
      pollTimer.current = window.setTimeout(poll, 2000)
    }
    pollTimer.current = window.setTimeout(poll, 2000)
  }

  // 触发 agent 类型认证：后端调用 ACP authenticate，agent 自行打开浏览器 PKCE 流程。
  // 触发后订阅 auth-events SSE，收到 elicitation/complete 后刷新能力面板并提示登录完成。
  async function handleAgentAuth(method: AcpAuthMethodItem) {
    if (agentAuthPending) return
    setAgentAuthPending(method.id)
    setAgentAuthDone(false)
    // 断开上一次未完成的 SSE 订阅
    if (authEventsCleanup.current) { authEventsCleanup.current(); authEventsCleanup.current = null }
    try {
      await authenticateAgent(agentType, method.id)
      setError('')
      // 订阅 elicitation 完成事件：浏览器登录结束后 agent 发 complete 通知
      authEventsCleanup.current = streamAgentAuthEvents(
        agentType,
        () => {
          setAgentAuthPending('')
          setAgentAuthDone(true)
          load()
          if (authEventsCleanup.current) { authEventsCleanup.current(); authEventsCleanup.current = null }
        },
        (err) => { setError(err.message); setAgentAuthPending('') },
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      setAgentAuthPending('')
    }
  }

  if (loading) return <div className={styles.panel}><span className={styles.hint}>{t('common.loading')}</span></div>
  if (error) return <div className={styles.panel}><span className={styles.error}>{error}</span></div>

  if (!caps?.available) {
    return (
      <div className={styles.panel}>
        <span className={styles.hint}>{t('settings.acpCaps.notAvailable')}</span>
        <button type="button" className={styles.connectBtn} onClick={handleConnect} disabled={connecting}>
          {connecting ? t('settings.acpCaps.connecting') : t('settings.acpCaps.connectAndFetch')}
        </button>
      </div>
    )
  }

  const pc = caps.prompt_capabilities
  const mc = caps.mcp_capabilities
  const flag = (v: boolean | undefined) => (v ? '✓' : '✗')

  return (
    <div className={styles.panel}>
      {/* 握手元信息：agent 名称/版本、协议版本、认证方式 */}
      <div className={styles.metaRow}>
        {caps.agent_name && (
          <span className={styles.metaItem}>
            <span className={styles.metaLabel}>{t('settings.acpCaps.agentInfo')}</span>
            {caps.agent_name}{caps.agent_version ? ` v${caps.agent_version}` : ''}
          </span>
        )}
        <span className={styles.metaItem}>
          <span className={styles.metaLabel}>{t('settings.acpCaps.protocol')}</span>
          {caps.protocol_version}
        </span>
        <span className={styles.metaItem}>
          <span className={styles.metaLabel}>{t('settings.acpCaps.promptCaps')}</span>
          image {flag(pc?.image)} · audio {flag(pc?.audio)} · embeddedContext {flag(pc?.embedded_context)}
        </span>
        <span className={styles.metaItem}>
          <span className={styles.metaLabel}>{t('settings.acpCaps.mcpCaps')}</span>
          http {flag(mc?.http)} · sse {flag(mc?.sse)} · acp {flag(mc?.acp)}
        </span>
        {(caps.auth_methods?.length ?? 0) > 0 && (
          <span className={styles.metaItem}>
            <span className={styles.metaLabel}>{t('settings.acpCaps.authMethods')}</span>
            {caps.auth_methods!.map((a) => (
              <span key={a.id} className={styles.authMethodItem}>
                {a.name || a.id}（{a.type}）
                {a.type === 'terminal' && (
                  <button
                    type="button"
                    className={styles.loginBtn}
                    onClick={() => setLoginMethod(loginMethod?.id === a.id ? null : a)}
                  >
                    {t('settings.acpCaps.login')}
                  </button>
                )}
                {a.type === 'agent' && (
                  <button
                    type="button"
                    className={styles.loginBtn}
                    disabled={agentAuthPending === a.id}
                    onClick={() => handleAgentAuth(a)}
                  >
                    {agentAuthPending === a.id ? t('settings.acpCaps.connecting') : t('settings.acpCaps.login')}
                  </button>
                )}
                {a.type === 'agent' && agentAuthDone && !agentAuthPending && (
                  <span className={styles.hint}>✓</span>
                )}
              </span>
            ))}
          </span>
        )}
      </div>
      {loginMethod && (
        <AgentAuthTerminal
          agentType={agentType}
          methodId={loginMethod.id}
          methodName={loginMethod.name}
          onClose={() => setLoginMethod(null)}
        />
      )}
      <div className={styles.tables}>
        <MethodTable title={t('settings.acpCaps.agentMethods')} items={caps.agent_methods || []} />
        <MethodTable title={t('settings.acpCaps.clientMethods')} items={caps.client_methods || []} />
      </div>
    </div>
  )
}
