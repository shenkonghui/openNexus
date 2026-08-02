import { useState, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, XCircle, AlertTriangle, MinusCircle, ShieldAlert, ShieldCheck, ChevronDown, ChevronRight, Layers, Plus, Trash2, Pencil, Save, X } from 'lucide-react'
import { runSecurityTest, getLastSecurityTest, runSecurityTestAll, getAgentModels } from '../api/agents'
import { getSecurityTestCases, updateSecurityTestCases } from '../api/securityTests'
import type { Agent, SecurityTestCase, SecurityTestReport, SecurityTestBatchResult, ConfigOptionValue } from '../types'
import { compileFilters, filterAgentModels } from '../utils/modelFilter'
import LoadingSpinner from './LoadingSpinner'
import styles from './AgentCapabilityTest.module.css'

interface Props {
  agents: Agent[]
  filters?: string[]
}

const CATEGORIES = ['fs_write_outside', 'fs_write_inside', 'fs_delete', 'fs_system', 'fs_sensitive', 'fs_home']

/** 单项状态徽标：图标 + 状态文案 */
function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const map: Record<string, { icon: JSX.Element; cls: string }> = {
    passed: { icon: <CheckCircle2 size={13} />, cls: styles.stPassed },
    failed: { icon: <XCircle size={13} />, cls: styles.stFailed },
    partial: { icon: <AlertTriangle size={13} />, cls: styles.stPartial },
    skipped: { icon: <MinusCircle size={13} />, cls: styles.stSkipped },
    error: { icon: <XCircle size={13} />, cls: styles.stFailed },
  }
  const m = map[status] || map.error
  return (
    <span className={`${styles.badge} ${m.cls}`}>
      {m.icon}
      {t(`secTest.status.${status}`, status)}
    </span>
  )
}

export default function AgentSecurityTest({ agents, filters }: Props) {
  const { t } = useTranslation()
  const [agentType, setAgentType] = useState('')
  const [report, setReport] = useState<SecurityTestReport | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState('')
  const [showRaw, setShowRaw] = useState(false)
  const [batchResult, setBatchResult] = useState<SecurityTestBatchResult | null>(null)
  const [runningAll, setRunningAll] = useState(false)
  const [models, setModels] = useState<ConfigOptionValue[]>([])
  const [modelValue, setModelValue] = useState('')
  // 测试用例管理
  const [cases, setCases] = useState<SecurityTestCase[]>([])
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editDraft, setEditDraft] = useState<SecurityTestCase | null>(null)
  const [casesDirty, setCasesDirty] = useState(false)

  const filterRegexes = useMemo(() => compileFilters(filters), [filters])
  const visibleModels = useMemo(
    () => filterAgentModels(agentType, models, filterRegexes, modelValue),
    [agentType, models, filterRegexes, modelValue],
  )

  useEffect(() => {
    if (!agentType && agents.length > 0) setAgentType(agents[0].type)
  }, [agents, agentType])

  // 加载测试用例
  useEffect(() => {
    let cancelled = false
    getSecurityTestCases()
      .then((resp) => {
        if (!cancelled) setCases(resp.data.cases || [])
      })
      .catch(() => { /* 静默 */ })
    return () => { cancelled = true }
  }, [])

  // 切换 agent 时加载最近一次报告
  useEffect(() => {
    if (!agentType) return
    let cancelled = false
    setReport(null)
    setError('')
    getLastSecurityTest(agentType)
      .then((resp) => {
        if (!cancelled && resp.data.available && resp.data.report) setReport(resp.data.report)
      })
      .catch(() => { /* 无缓存报告时静默 */ })
    return () => { cancelled = true }
  }, [agentType])

  // 切换 agent 时加载可用模型
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
      .catch(() => { })
    return () => { cancelled = true }
  }, [agentType])

  async function run() {
    if (!agentType || running || runningAll) return
    if (!window.confirm(t('secTest.confirm'))) return
    setRunning(true)
    setError('')
    try {
      const resp = await runSecurityTest(agentType, modelValue)
      setReport(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  async function runAll() {
    if (running || runningAll) return
    if (!window.confirm(t('secTest.allConfirm'))) return
    setRunningAll(true)
    setError('')
    try {
      const resp = await runSecurityTestAll()
      setBatchResult(resp.data)
      const mine = resp.data.reports.find((r) => r.agent_type === agentType)
      if (mine) setReport(mine)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunningAll(false)
    }
  }

  // ===== 用例管理 =====
  async function saveCases() {
    try {
      const resp = await updateSecurityTestCases(cases)
      setCases(resp.data.cases || [])
      setCasesDirty(false)
      setEditingId(null)
      setEditDraft(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  function toggleEnabled(id: number) {
    setCases((prev) => prev.map((c) => (c.id === id ? { ...c, enabled: !c.enabled } : c)))
    setCasesDirty(true)
  }

  function startEdit(tc: SecurityTestCase) {
    setEditingId(tc.id)
    setEditDraft({ ...tc })
  }

  function applyEdit() {
    if (!editDraft) return
    setCases((prev) => prev.map((c) => (c.id === editingId ? { ...editDraft } : c)))
    setCasesDirty(true)
    setEditingId(null)
    setEditDraft(null)
  }

  function deleteCase(id: number) {
    setCases((prev) => prev.filter((c) => c.id !== id))
    setCasesDirty(true)
  }

  function addCase() {
    const newId = Math.max(0, ...cases.map((c) => c.id)) + 1
    const newCase: SecurityTestCase = {
      id: newId,
      name: '',
      category: 'fs_write_outside',
      prompt: '',
      enabled: true,
      sort_order: cases.length,
      expect_blocked: true,
    }
    setCases((prev) => [...prev, newCase])
    setEditingId(newId)
    setEditDraft(newCase)
    setCasesDirty(true)
  }

  const agentName = (type: string) => agents.find((a) => a.type === type)?.display_name || type

  const busy = running || runningAll
  const enabledCount = cases.filter((c) => c.enabled).length

  return (
    <div className={styles.wrap}>
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
            title={t('secTest.modelHint')}
          >
            <option value="">{t('secTest.modelAuto')}</option>
            {visibleModels.map((m) => (
              <option key={m.value} value={m.value}>{m.name || m.value}</option>
            ))}
          </select>
        )}
        <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} onClick={() => void run()} disabled={!agentType || busy || enabledCount === 0}>
          <ShieldAlert size={13} />
          {running ? t('secTest.running') : t('secTest.run')}
        </button>
      </div>

      {/* 一键测试全部 */}
      <div className={styles.batchBar}>
        <button
          type="button"
          className={`${styles.btn} ${styles.btnBatch}`}
          onClick={() => void runAll()}
          disabled={busy || agents.length === 0 || enabledCount === 0}
        >
          <Layers size={14} />
          {runningAll ? t('secTest.runningAll') : t('secTest.runAll')}
        </button>
        {agents.length > 0 && (
          <span className={styles.batchHint}>
            {t('secTest.runAllHint', { count: agents.length, cases: enabledCount })}
          </span>
        )}
      </div>

      {busy && (
        <div className={styles.progress}>
          <LoadingSpinner />
          <span>{runningAll ? t('secTest.allProgress') : t('secTest.progress')}</span>
        </div>
      )}
      {error && <div className={styles.error}>{error}</div>}

      {/* 测试用例管理 */}
      <div className={styles.report}>
        <div className={styles.itemRow}>
          <div className={styles.itemHead}>
            <span className={styles.itemName}>{t('secTest.casesTitle')}（{enabledCount}/{cases.length}）</span>
            {casesDirty && (
              <div style={{ display: 'flex', gap: 6 }}>
                <button type="button" className={styles.btn} onClick={() => void saveCases()} disabled={busy}>
                  <Save size={12} />
                  {t('secTest.saveCases')}
                </button>
                <button type="button" className={styles.rawToggle} onClick={() => { setCasesDirty(false); setEditingId(null); setEditDraft(null) }}>
                  <X size={12} />
                </button>
              </div>
            )}
          </div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, marginTop: 8 }}>
            {cases.map((tc) => (
              <div key={tc.id} style={{ border: '1px solid var(--border-color, #e4e7ec)', borderRadius: 6, padding: 8 }}>
                {editingId === tc.id && editDraft ? (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      <input
                        style={{ flex: 1, minWidth: 120, padding: '4px 8px', borderRadius: 4, border: '1px solid var(--border-color, #d0d5dd)', background: 'var(--bg-primary, #fff)', color: 'inherit', fontSize: 13 }}
                        value={editDraft.name}
                        onChange={(e) => setEditDraft({ ...editDraft, name: e.target.value })}
                        placeholder={t('secTest.caseName')}
                      />
                      <select
                        style={{ padding: '4px 8px', borderRadius: 4, border: '1px solid var(--border-color, #d0d5dd)', background: 'var(--bg-primary, #fff)', color: 'inherit', fontSize: 13 }}
                        value={editDraft.category}
                        onChange={(e) => setEditDraft({ ...editDraft, category: e.target.value })}
                      >
                        {CATEGORIES.map((c) => (
                          <option key={c} value={c}>{t(`secTest.category.${c}`, c)}</option>
                        ))}
                      </select>
                      <button type="button" className={styles.btn} onClick={() => applyEdit()}><Save size={12} /></button>
                      <button type="button" className={styles.rawToggle} onClick={() => { setEditingId(null); setEditDraft(null) }}><X size={12} /></button>
                    </div>
                    <textarea
                      style={{ width: '100%', minHeight: 60, padding: '6px 8px', borderRadius: 4, border: '1px solid var(--border-color, #d0d5dd)', background: 'var(--bg-primary, #fff)', color: 'inherit', fontSize: 12, resize: 'vertical', boxSizing: 'border-box' }}
                      value={editDraft.prompt}
                      onChange={(e) => setEditDraft({ ...editDraft, prompt: e.target.value })}
                      placeholder={t('secTest.casePromptHint')}
                    />
                    <label style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 12, color: 'var(--text-secondary, #667085)', cursor: 'pointer' }} title={t('secTest.expectBlockedHint')}>
                      <input type="checkbox" checked={editDraft.expect_blocked} onChange={(e) => setEditDraft({ ...editDraft, expect_blocked: e.target.checked })} style={{ cursor: 'pointer' }} />
                      {t('secTest.expectBlocked')}
                    </label>
                  </div>
                ) : (
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <label style={{ display: 'flex', alignItems: 'center', cursor: 'pointer' }}>
                      <input type="checkbox" checked={tc.enabled} onChange={() => toggleEnabled(tc.id)} style={{ cursor: 'pointer' }} />
                    </label>
                    <span style={{ fontSize: 11, padding: '1px 6px', borderRadius: 8, background: 'var(--bg-secondary, #f2f4f7)', color: 'var(--text-secondary, #667085)' }}>
                      {t(`secTest.category.${tc.category}`, tc.category)}
                    </span>
                    <span style={{ fontSize: 10, padding: '1px 5px', borderRadius: 8, background: tc.expect_blocked ? '#fef3c7' : '#dcfce7', color: tc.expect_blocked ? '#92400e' : '#166534', flexShrink: 0 }} title={t('secTest.expectBlockedHint')}>
                      {tc.expect_blocked ? 'BLOCK' : 'ALLOW'}
                    </span>
                    <span style={{ fontSize: 13, fontWeight: 600 }}>{tc.name || t('secTest.untitled')}</span>
                    <span style={{ flex: 1, fontSize: 11, color: 'var(--text-secondary, #667085)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{tc.prompt}</span>
                    <button type="button" className={styles.rawToggle} onClick={() => startEdit(tc)} title={t('secTest.edit')}><Pencil size={12} /></button>
                    <button type="button" className={styles.rawToggle} onClick={() => deleteCase(tc.id)} title={t('secTest.delete')}><Trash2 size={12} /></button>
                  </div>
                )}
              </div>
            ))}
            <button type="button" className={styles.btn} onClick={() => addCase()} disabled={busy}>
              <Plus size={12} />
              {t('secTest.addCase')}
            </button>
          </div>
        </div>
      </div>

      {/* 批量对比表格 */}
      {batchResult && !runningAll && (
        <div className={styles.batchResult}>
          <div className={styles.reportMeta}>
            <span className={styles.modeBadge}>{t('secTest.modeBadge')}</span>
            <span>{new Date(batchResult.tested_at).toLocaleString()}</span>
            <span>{(batchResult.duration_ms / 1000).toFixed(1)}s</span>
            <span>{batchResult.total} agents</span>
          </div>
          <table className={styles.batchTable}>
            <thead>
              <tr>
                <th>{t('secTest.colAgent')}</th>
                <th>{t('secTest.colPassed')}</th>
                <th>{t('secTest.colFailed')}</th>
                <th>{t('secTest.colScore')}</th>
                <th>{t('secTest.colDuration')}</th>
              </tr>
            </thead>
            <tbody>
              {batchResult.reports.map((rep) => {
                const passed = rep.items?.filter((it) => it.status === 'passed').length || 0
                const failed = rep.items?.filter((it) => it.status === 'failed').length || 0
                const total = rep.items?.filter((it) => it.status !== 'skipped' && it.status !== 'error').length || 0
                const score = total > 0 ? Math.round((passed / total) * 100) : 0
                return (
                  <tr
                    key={rep.agent_type}
                    className={rep.agent_type === agentType ? styles.rowActive : styles.rowClickable}
                    onClick={() => setAgentType(rep.agent_type)}
                  >
                    <td className={styles.colAgent}>{agentName(rep.agent_type)}</td>
                    <td className={styles.colDot}><span style={{ color: '#16a34a', fontWeight: 600 }}>{passed}</span></td>
                    <td className={styles.colDot}><span style={{ color: '#dc2626', fontWeight: 600 }}>{failed}</span></td>
                    <td className={styles.colDot}>
                      <span style={{ fontWeight: 600, color: score >= 80 ? '#16a34a' : score >= 50 ? '#d97706' : '#dc2626' }}>{score}%</span>
                    </td>
                    <td className={styles.colDur}>{(rep.duration_ms / 1000).toFixed(1)}s</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* 单 agent 详情 */}
      {report && !busy && (
        <div className={styles.report}>
          <div className={styles.reportMeta}>
            <span className={styles.modeBadge}>{t('secTest.modeBadge')}</span>
            <span>{agentName(report.agent_type)}</span>
            <span>{new Date(report.tested_at).toLocaleString()}</span>
            <span>{(report.duration_ms / 1000).toFixed(1)}s</span>
            {report.model && <span className={styles.modelTag}>{report.model}</span>}
          </div>
          {report.error && <div className={styles.error}>{report.error}</div>}
          <div className={styles.items}>
            {report.items?.map((item, idx) => (
              <div key={`${item.case_id}-${idx}`} className={styles.itemRow}>
                <div className={styles.itemHead}>
                  <span className={styles.itemName}>{item.name}</span>
                  <StatusBadge status={item.status} />
                </div>
                {item.detail && <div className={styles.itemDetail}>{item.detail}</div>}
                {item.tool_calls && item.tool_calls.length > 0 && (
                  <div style={{ marginTop: 6 }}>
                    <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text-secondary, #667085)', marginBottom: 4 }}>
                      {t('secTest.toolCalls')}（{item.tool_calls.length}）
                    </div>
                    {item.tool_calls.map((tc, i) => (
                      <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 6, fontFamily: 'var(--font-mono, monospace)', fontSize: 11, padding: '2px 0' }}>
                        {tc.exit_code != null ? (
                          tc.exit_code === 0 ? (
                            <CheckCircle2 size={11} style={{ color: '#16a34a', flexShrink: 0 }} />
                          ) : (
                            <XCircle size={11} style={{ color: '#dc2626', flexShrink: 0 }} />
                          )
                        ) : (
                          <AlertTriangle size={11} style={{ color: '#d97706', flexShrink: 0 }} />
                        )}
                        <span style={{ wordBreak: 'break-all' }}>{tc.title}</span>
                        {tc.exit_code != null && (
                          <span style={{ color: tc.exit_code === 0 ? '#16a34a' : '#dc2626', fontWeight: 600, flexShrink: 0 }}>
                            exit={tc.exit_code}
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
          {report.raw_response && (
            <div className={styles.rawWrap}>
              <button type="button" className={styles.rawToggle} onClick={() => setShowRaw((v) => !v)}>
                {showRaw ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                {t('secTest.rawResponse')}
              </button>
              {showRaw && <pre className={styles.rawText}>{report.raw_response}</pre>}
            </div>
          )}
        </div>
      )}

      {!report && !busy && !error && !batchResult && (
        <div className={styles.empty}>
          <ShieldCheck size={24} style={{ marginBottom: 8, opacity: 0.4 }} />
          {t('secTest.empty')}
        </div>
      )}
    </div>
  )
}
