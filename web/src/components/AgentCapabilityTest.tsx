import { useState, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, XCircle, AlertTriangle, MinusCircle, CircleDashed, FlaskConical, Zap, ChevronDown, ChevronRight, Layers } from 'lucide-react'
import { runCapabilityTest, getLastCapabilityTest, runCapabilityTestAll, getAgentModels } from '../api/agents'
import type { Agent, CapabilityTestReport, CapabilityTestItem, CapabilityTestBatchResult, ConfigOptionValue } from '../types'
import { compileFilters, filterAgentModels } from '../utils/modelFilter'
import LoadingSpinner from './LoadingSpinner'
import styles from './AgentCapabilityTest.module.css'

interface Props {
  agents: Agent[]
  /** 显示过滤正则（来自 config.yaml agents.selector.filters），匹配串 "agentType/modelValue" */
  filters?: string[]
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

/** 紧凑状态圆点（表格单元格用，仅图标 + title） */
function StatusDot({ status }: { status: string }) {
  const { t } = useTranslation()
  const map: Record<string, string> = {
    passed: styles.stPassed,
    failed: styles.stFailed,
    partial: styles.stPartial,
    injected: styles.stInjected,
    skipped: styles.stSkipped,
    error: styles.stFailed,
  }
  const cls = map[status] || styles.stFailed
  return (
    <span className={`${styles.dot} ${cls}`} title={t(`capTest.status.${status}`, status)}>
      {status === 'passed' && <CheckCircle2 size={16} />}
      {(status === 'failed' || status === 'error') && <XCircle size={16} />}
      {status === 'partial' && <AlertTriangle size={16} />}
      {status === 'injected' && <CircleDashed size={16} />}
      {status === 'skipped' && <MinusCircle size={16} />}
    </span>
  )
}

/**
 * Agent 能力接入测试：验证指定 agent（acp-server）能否识别 openNexus 注入的
 * rule（Meta.systemPrompt）、skill（AdditionalDirectories）与 MCP server。
 * 两级验证：静态检查（秒级）与端到端行为测试（临时会话 + 验证 prompt，真实消耗一次调用）。
 * 支持一键并行测试全部已接入 agent。
 */
export default function AgentCapabilityTest({ agents, filters }: Props) {
  const { t } = useTranslation()
  const [agentType, setAgentType] = useState('')
  const [report, setReport] = useState<CapabilityTestReport | null>(null)
  const [running, setRunning] = useState<'static' | 'e2e' | null>(null)
  const [error, setError] = useState('')
  const [showRaw, setShowRaw] = useState(false)
  const [batchResult, setBatchResult] = useState<CapabilityTestBatchResult | null>(null)
  const [runningAll, setRunningAll] = useState(false)
  const [models, setModels] = useState<ConfigOptionValue[]>([])
  const [modelValue, setModelValue] = useState('') // 空=自动选取 agent 运行模型

  // 编译 selector.filters（与 AgentModelSelector 同语义），用于过滤测试模型下拉
  const filterRegexes = useMemo(() => compileFilters(filters), [filters])
  // 按 selector.filters 过滤后的可选项（当前选中项被过滤掉时补回，避免下拉里凭空消失）
  const visibleModels = useMemo(
    () => filterAgentModels(agentType, models, filterRegexes, modelValue),
    [agentType, models, filterRegexes, modelValue],
  )

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

  // 切换 agent 时加载可用模型列表（用于指定测试模型），并重置为自动
  useEffect(() => {
    if (!agentType) return
    let cancelled = false
    setModels([])
    setModelValue('')
    getAgentModels(agentType)
      .then((resp) => {
        if (cancelled) return
        const opts = resp.data.model_options?.flatMap((m) => m.options || []) || []
        setModels(opts)
      })
      .catch(() => { /* 模型列表不可用时仅隐藏下拉框 */ })
    return () => { cancelled = true }
  }, [agentType])

  async function run(e2e: boolean) {
    if (!agentType || running || runningAll) return
    if (e2e && !window.confirm(t('capTest.e2eConfirm'))) return
    setRunning(e2e ? 'e2e' : 'static')
    setError('')
    try {
      const resp = await runCapabilityTest(agentType, e2e, modelValue)
      setReport(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(null)
    }
  }

  async function runAll(e2e: boolean) {
    if (running || runningAll) return
    if (e2e && !window.confirm(t('capTest.e2eAllConfirm'))) return
    setRunningAll(true)
    setError('')
    try {
      const resp = await runCapabilityTestAll(e2e)
      setBatchResult(resp.data)
      // 当前选中的 agent 若在结果中，同步更新详情面板
      const mine = resp.data.reports.find((r) => r.agent_type === agentType)
      if (mine) setReport(mine)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunningAll(false)
    }
  }

  const itemLabel = (item: CapabilityTestItem) => t(`capTest.item.${item.id}`, item.id)
  const itemStatus = (rep: CapabilityTestReport, id: string) =>
    rep.items?.find((it) => it.id === id)?.status || 'error'
  const agentName = (type: string) => agents.find((a) => a.type === type)?.display_name || type

  const busy = !!running || runningAll

  return (
    <div className={styles.wrap}>
      {/* 单 agent 测试工具栏 */}
      <div className={styles.toolbar}>
        <select
          className={styles.agentSelect}
          value={agentType}
          onChange={(e) => setAgentType(e.target.value)}
          disabled={busy}
        >
          {agents.map((a) => (
            <option key={a.type} value={a.type}>{a.display_name || a.type}</option>
          ))}
        </select>
        {models.length > 0 && (
          <select
            className={styles.agentSelect}
            value={modelValue}
            onChange={(e) => setModelValue(e.target.value)}
            disabled={busy}
            title={t('capTest.modelHint')}
          >
            <option value="">{t('capTest.modelAuto')}</option>
            {visibleModels.map((m) => (
              <option key={m.value} value={m.value}>{m.name || m.value}</option>
            ))}
          </select>
        )}
        <button type="button" className={styles.btn} onClick={() => void run(false)} disabled={!agentType || busy}>
          <Zap size={13} />
          {running === 'static' ? t('capTest.running') : t('capTest.runStatic')}
        </button>
        <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} onClick={() => void run(true)} disabled={!agentType || busy}>
          <FlaskConical size={13} />
          {running === 'e2e' ? t('capTest.running') : t('capTest.runE2E')}
        </button>
      </div>

      {/* 一键测试全部 */}
      <div className={styles.batchBar}>
        <button
          type="button"
          className={`${styles.btn} ${styles.btnBatch}`}
          onClick={() => void runAll(true)}
          disabled={busy || agents.length === 0}
        >
          <Layers size={14} />
          {runningAll ? t('capTest.runningAll') : t('capTest.runAllE2E')}
        </button>
        {agents.length > 0 && (
          <span className={styles.batchHint}>
            {t('capTest.runAllHint', { count: agents.length })}
          </span>
        )}
      </div>

      {busy && (
        <div className={styles.progress}>
          <LoadingSpinner />
          <span>{runningAll ? t('capTest.allProgress') : (running === 'e2e' ? t('capTest.e2eProgress') : t('capTest.staticProgress'))}</span>
        </div>
      )}
      {error && <div className={styles.error}>{error}</div>}

      {/* 批量对比表格 */}
      {batchResult && !runningAll && (
        <div className={styles.batchResult}>
          <div className={styles.reportMeta}>
            <span className={styles.modeBadge}>{batchResult.e2e ? t('capTest.modeE2E') : t('capTest.modeStatic')}</span>
            <span>{new Date(batchResult.tested_at).toLocaleString()}</span>
            <span>{(batchResult.duration_ms / 1000).toFixed(1)}s</span>
            <span>{batchResult.total} agents</span>
          </div>
          <table className={styles.batchTable}>
            <thead>
              <tr>
                <th>{t('capTest.colAgent')}</th>
                <th>RULE</th>
                <th>SKILL</th>
                <th>MCP</th>
                <th>AGENT</th>
                <th>{t('capTest.colDuration')}</th>
              </tr>
            </thead>
            <tbody>
              {batchResult.reports.map((rep) => (
                <tr
                  key={rep.agent_type}
                  className={rep.agent_type === agentType ? styles.rowActive : styles.rowClickable}
                  onClick={() => setAgentType(rep.agent_type)}
                >
                  <td className={styles.colAgent}>{agentName(rep.agent_type)}</td>
                  <td className={styles.colDot}><StatusDot status={itemStatus(rep, 'rule')} /></td>
                  <td className={styles.colDot}><StatusDot status={itemStatus(rep, 'skill')} /></td>
                  <td className={styles.colDot}><StatusDot status={itemStatus(rep, 'mcp')} /></td>
                  <td className={styles.colDot}><StatusDot status={itemStatus(rep, 'subagent')} /></td>
                  <td className={styles.colDur}>{(rep.duration_ms / 1000).toFixed(1)}s</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* 单 agent 详情 */}
      {report && !busy && (
        <div className={styles.report}>
          <div className={styles.reportMeta}>
            <span className={styles.modeBadge}>{report.mode === 'e2e' ? t('capTest.modeE2E') : t('capTest.modeStatic')}</span>
            <span>{agentName(report.agent_type)}</span>
            <span>{new Date(report.tested_at).toLocaleString()}</span>
            <span>{(report.duration_ms / 1000).toFixed(1)}s</span>
            {report.model && <span className={styles.modelTag}>{report.model}</span>}
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

      {!report && !busy && !error && !batchResult && (
        <div className={styles.empty}>{t('capTest.empty')}</div>
      )}
    </div>
  )
}
