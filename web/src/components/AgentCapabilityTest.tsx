import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, XCircle, AlertTriangle, MinusCircle, CircleDashed, FlaskConical, Zap, ChevronDown, ChevronRight } from 'lucide-react'
import { runCapabilityTest, getLastCapabilityTest } from '../api/agents'
import type { Agent, CapabilityTestReport, CapabilityTestItem } from '../types'
import LoadingSpinner from './LoadingSpinner'
import styles from './AgentCapabilityTest.module.css'

interface Props {
  agents: Agent[]
}

/** 单项状态徽标：图标 + 状态文案 */
function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const map: Record<string, { icon: JSX.Element; cls: string }> = {
    passed: { icon: <CheckCircle2 size={13} />, cls: styles.stPassed },
    failed: { icon: <XCircle size={13} />, cls: styles.stFailed },
    partial: { icon: <AlertTriangle size={13} />, cls: styles.stPartial },
    injected: { icon: <CircleDashed size={13} />, cls: styles.stInjected },
    skipped: { icon: <MinusCircle size={13} />, cls: styles.stSkipped },
    error: { icon: <XCircle size={13} />, cls: styles.stFailed },
  }
  const m = map[status] || map.error
  return (
    <span className={`${styles.badge} ${m.cls}`}>
      {m.icon}
      {t(`capTest.status.${status}`, status)}
    </span>
  )
}

/**
 * Agent 能力接入测试：验证指定 agent（acp-server）能否识别 openNexus 注入的
 * rule（Meta.systemPrompt）、skill（AdditionalDirectories）与 MCP server。
 * 两级验证：静态检查（秒级）与端到端行为测试（临时会话 + 验证 prompt，真实消耗一次调用）。
 */
export default function AgentCapabilityTest({ agents }: Props) {
  const { t } = useTranslation()
  const [agentType, setAgentType] = useState('')
  const [report, setReport] = useState<CapabilityTestReport | null>(null)
  const [running, setRunning] = useState<'static' | 'e2e' | null>(null)
  const [error, setError] = useState('')
  const [showRaw, setShowRaw] = useState(false)

  useEffect(() => {
    if (!agentType && agents.length > 0) setAgentType(agents[0].type)
  }, [agents, agentType])

  // 切换 agent 时加载最近一次测试报告（后端内存缓存）
  useEffect(() => {
    if (!agentType) return
    let cancelled = false
    setReport(null)
    setError('')
    getLastCapabilityTest(agentType)
      .then((resp) => {
        if (!cancelled && resp.data.available && resp.data.report) setReport(resp.data.report)
      })
      .catch(() => { /* 无缓存报告时静默 */ })
    return () => { cancelled = true }
  }, [agentType])

  async function run(e2e: boolean) {
    if (!agentType || running) return
    if (e2e && !window.confirm(t('capTest.e2eConfirm'))) return
    setRunning(e2e ? 'e2e' : 'static')
    setError('')
    try {
      const resp = await runCapabilityTest(agentType, e2e)
      setReport(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(null)
    }
  }

  const itemLabel = (item: CapabilityTestItem) => t(`capTest.item.${item.id}`, item.id)

  return (
    <div className={styles.wrap}>
      <div className={styles.toolbar}>
        <select
          className={styles.agentSelect}
          value={agentType}
          onChange={(e) => setAgentType(e.target.value)}
          disabled={!!running}
        >
          {agents.map((a) => (
            <option key={a.type} value={a.type}>{a.display_name || a.type}</option>
          ))}
        </select>
        <button type="button" className={styles.btn} onClick={() => void run(false)} disabled={!agentType || !!running}>
          <Zap size={13} />
          {running === 'static' ? t('capTest.running') : t('capTest.runStatic')}
        </button>
        <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} onClick={() => void run(true)} disabled={!agentType || !!running}>
          <FlaskConical size={13} />
          {running === 'e2e' ? t('capTest.running') : t('capTest.runE2E')}
        </button>
      </div>

      {running && (
        <div className={styles.progress}>
          <LoadingSpinner />
          <span>{running === 'e2e' ? t('capTest.e2eProgress') : t('capTest.staticProgress')}</span>
        </div>
      )}
      {error && <div className={styles.error}>{error}</div>}

      {report && !running && (
        <div className={styles.report}>
          <div className={styles.reportMeta}>
            <span className={styles.modeBadge}>{report.mode === 'e2e' ? t('capTest.modeE2E') : t('capTest.modeStatic')}</span>
            <span>{new Date(report.tested_at).toLocaleString()}</span>
            <span>{(report.duration_ms / 1000).toFixed(1)}s</span>
          </div>
          {report.error && <div className={styles.error}>{report.error}</div>}
          <div className={styles.items}>
            {report.items?.map((item) => (
              <div key={item.id} className={styles.itemRow}>
                <div className={styles.itemHead}>
                  <span className={styles.itemName}>{itemLabel(item)}</span>
                  <StatusBadge status={item.status} />
                </div>
                {item.detail && <div className={styles.itemDetail}>{item.detail}</div>}
              </div>
            ))}
          </div>
          {report.raw_response && (
            <div className={styles.rawWrap}>
              <button type="button" className={styles.rawToggle} onClick={() => setShowRaw((v) => !v)}>
                {showRaw ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                {t('capTest.rawResponse')}
              </button>
              {showRaw && <pre className={styles.rawText}>{report.raw_response}</pre>}
            </div>
          )}
        </div>
      )}

      {!report && !running && !error && (
        <div className={styles.empty}>{t('capTest.empty')}</div>
      )}
    </div>
  )
}
