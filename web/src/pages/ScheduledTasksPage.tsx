import { useState, useEffect, type FormEvent } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { listAgents, probeAgentConfigs, listAgentCommands, listAgentModes } from '../api/agents'
import { listSkillsByPath } from '../api/filesystem'
import { getWorkspace } from '../api/workspaces'
import {
  listScheduledTasks, createScheduledTask, updateScheduledTask, deleteScheduledTask, runScheduledTask,
} from '../api/scheduledTasks'
import type { Agent, ScheduledTask, ConfigOptionValue, AgentCommand, SessionMode, AgentSkill } from '../types'
import AgentModelSelector from '../components/AgentModelSelector'
import PromptInput from '../components/PromptInput'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import styles from './ScheduledTasksPage.module.css'
import { Calendar, Plus, CheckCircle2, XCircle, Loader2, Clock3, CircleDashed } from 'lucide-react'

interface FormState {
  name: string; agent_type: string; prompt: string
  cron_expr: string; enabled: boolean; preset: string; model_value: string; timeout_minutes: number
}

export default function ScheduledTasksPage() {
  const { t, i18n } = useTranslation()
  const location = useLocation()
  const navigate = useNavigate()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions, selectWorkspace, reload: reloadWorkspace } = useCurrentWorkspace(!!user)

  const [agents, setAgents] = useState<Agent[]>([])
  const [tasks, setTasks] = useState<ScheduledTask[]>([])
  // agent+模型 合并下拉：各 agent 探测到的模型列表与显示过滤正则（与新建任务页同一套）
  const [agentModelsMap, setAgentModelsMap] = useState<Record<string, ConfigOptionValue[]>>({})
  const [selectorFilters, setSelectorFilters] = useState<string[]>([])
  const [probing, setProbing] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [showForm, setShowForm] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [form, setForm] = useState<FormState>({
    name: '', agent_type: '', prompt: '', cron_expr: '*/5 * * * *',
    enabled: true, preset: '每 5 分钟', model_value: '', timeout_minutes: 5,
  })
  const [saving, setSaving] = useState(false)
  const [taskCommands, setTaskCommands] = useState<AgentCommand[]>([])
  const [taskModes, setTaskModes] = useState<SessionMode[]>([])
  const [taskSkills, setTaskSkills] = useState<AgentSkill[]>([])
  const [workspaceCwd, setWorkspaceCwd] = useState('')
  const [workspaceName, setWorkspaceName] = useState('')

  useEffect(() => { if (!user || !workspaceId) return; loadData() }, [user, workspaceId])

  useEffect(() => {
    const state = location.state as { openCreate?: boolean } | null
    if (!state?.openCreate) return
    openCreate()
    navigate(location.pathname, { replace: true, state: null })
  }, [location.state, location.pathname, navigate])

  useEffect(() => {
    if (!workspaceId) { setWorkspaceCwd(''); setWorkspaceName(''); return }
    getWorkspace(workspaceId)
      .then((r) => {
        setWorkspaceCwd(r.data.workspace.cwd || '')
        setWorkspaceName(r.data.workspace.name || '')
      })
      .catch(() => { setWorkspaceCwd(''); setWorkspaceName('') })
  }, [workspaceId])

  // 表单打开时并行探测全部 agent 的模型列表，供合并下拉展示全部组合（probeAgentConfigs 有前端缓存）
  useEffect(() => {
    if (!showForm || agents.length === 0) return
    let alive = true
    setProbing(true)
    Promise.allSettled(agents.map((a) =>
      probeAgentConfigs(a.type)
        .then((r) => {
          if (!alive) return
          const modelOpt = (r.data.config_options || []).find((o) => o.category === 'model')
          setAgentModelsMap((prev) => ({ ...prev, [a.type]: modelOpt?.options || [] }))
        })
        .catch(() => {
          // 探测失败：记为空列表，下拉中退化为 agent 级单项（使用默认模型）
          if (alive) setAgentModelsMap((prev) => (a.type in prev ? prev : { ...prev, [a.type]: [] }))
        }),
    )).finally(() => { if (alive) setProbing(false) })
    return () => { alive = false }
  }, [showForm, agents])

  useEffect(() => {
    if (!showForm || !form.agent_type || probing) {
      if (!showForm) {
        setTaskCommands([])
        setTaskModes([])
      }
      return
    }
    listAgentCommands(form.agent_type).then((r) => setTaskCommands(r.data.commands || [])).catch(() => setTaskCommands([]))
    listAgentModes(form.agent_type).then((r) => setTaskModes(r.data.modes || [])).catch(() => setTaskModes([]))
  }, [showForm, form.agent_type, probing])

  useEffect(() => {
    if (!showForm) {
      setTaskSkills([])
      return
    }
    listSkillsByPath(workspaceCwd.trim() || undefined)
      .then((r) => setTaskSkills(r.data.skills || []))
      .catch(() => setTaskSkills([]))
  }, [showForm, workspaceCwd])

  async function loadData() {
    setLoading(true); setError('')
    try {
      const [agentsResp, tasksResp] = await Promise.all([listAgents(), listScheduledTasks(workspaceId || undefined)])
      setAgents(agentsResp.data.agents || [])
      setSelectorFilters(agentsResp.data.selector_filters || [])
      setTasks(tasksResp.data.tasks || [])
      if (agentsResp.data.agents?.length > 0 && !form.agent_type) setForm((prev) => ({ ...prev, agent_type: agentsResp.data.agents[0].type }))
    } catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
    finally { setLoading(false) }
  }

  function openCreate() {
    setEditingId(null)
    setForm({
      name: '', agent_type: agents[0]?.type || '', prompt: '', cron_expr: '*/5 * * * *',
      enabled: true, preset: t('scheduledTask.every5min'), model_value: '', timeout_minutes: 5,
    })
    setShowForm(true)
  }

  function openEdit(task: ScheduledTask) {
    const customLabel = t('scheduledTask.custom')
    const presets = [
      { label: t('scheduledTask.every5min'), value: '*/5 * * * *' },
      { label: t('scheduledTask.everyHour'), value: '0 * * * *' },
      { label: t('scheduledTask.daily0'), value: '0 0 * * *' },
      { label: t('scheduledTask.daily9'), value: '0 9 * * *' },
      { label: t('scheduledTask.weeklyMon9'), value: '0 9 * * 1' },
      { label: customLabel, value: '' },
    ]
    const presetMatch = presets.find((p) => p.value === task.cron_expr)
    setEditingId(task.id)
    setForm({
      name: task.name, agent_type: task.agent_type, prompt: task.prompt, cron_expr: task.cron_expr,
      enabled: task.enabled, preset: presetMatch ? presetMatch.label : customLabel,
      model_value: task.model_value || '', timeout_minutes: task.timeout_minutes || 5,
    })
    setShowForm(true)
  }

  function handlePresetChange(label: string) {
    const customLabel = t('scheduledTask.custom')
    const presets = [
      { label: t('scheduledTask.every5min'), value: '*/5 * * * *' },
      { label: t('scheduledTask.everyHour'), value: '0 * * * *' },
      { label: t('scheduledTask.daily0'), value: '0 0 * * *' },
      { label: t('scheduledTask.daily9'), value: '0 9 * * *' },
      { label: t('scheduledTask.weeklyMon9'), value: '0 9 * * 1' },
      { label: customLabel, value: '' },
    ]
    const preset = presets.find((p) => p.label === label)
    if (preset && label !== customLabel) setForm((prev) => ({ ...prev, preset: label, cron_expr: preset.value }))
    else setForm((prev) => ({ ...prev, preset: customLabel }))
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (!workspaceId || !form.agent_type || !form.prompt || !form.cron_expr) {
      setError(t('scheduledTask.validationError')); return
    }
    setSaving(true); setError('')
    const payload = {
      name: form.name, agent_type: form.agent_type, workspace_id: workspaceId, prompt: form.prompt,
      cron_expr: form.cron_expr, enabled: form.enabled, model_value: form.model_value, timeout_minutes: form.timeout_minutes,
    }
    try {
      if (editingId) await updateScheduledTask(editingId, payload)
      else await createScheduledTask(payload)
      setShowForm(false); setEditingId(null); await loadData()
    } catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
    finally { setSaving(false) }
  }

  async function handleDelete(task: ScheduledTask) {
    if (!window.confirm(t('scheduledTask.deleteConfirm'))) return; setError('')
    try { await deleteScheduledTask(task.id); await loadData() }
    catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
  }

  async function handleRun(task: ScheduledTask) {
    setError('')
    try { await runScheduledTask(task.id); setTimeout(loadData, 500) }
    catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
  }

  async function handleToggleEnabled(task: ScheduledTask) {
    setError('')
    try { await updateScheduledTask(task.id, { enabled: !task.enabled }); await loadData() }
    catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
  }

  if (authLoading) return <LoadingSpinner text={t('common.loading')} />
  if (!user) return null

  const locale = i18n.language.startsWith('zh') ? 'zh-CN' : 'en-US'
  const visibleTasks = tasks

  const taskStatusLabels: Record<string, string> = {
    success: t('scheduledTask.statusSuccess'),
    running: t('scheduledTask.statusRunning'),
    failed: t('scheduledTask.statusFailed'),
    skipped: t('scheduledTask.statusCancelled'),
    '': t('scheduledTask.noTasks'),
  }

  function StatusIcon({ status }: { status: string }) {
    const size = 12
    if (status === 'running') return <Loader2 size={size} className={styles.statusIconSpin} />
    if (status === 'success') return <CheckCircle2 size={size} />
    if (status === 'failed') return <XCircle size={size} />
    if (status === 'skipped') return <Clock3 size={size} />
    return <CircleDashed size={size} />
  }

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId, onNewScheduledTask: openCreate, onWorkspaceChange: selectWorkspace, onWorkspaceRefresh: reloadWorkspace }}>
      <div className={styles.main}>
        <div className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <h1 className={styles.title}><Calendar size={20} style={{ verticalAlign: '-4px', marginRight: 6 }} />{t('scheduledTask.title')}</h1>
          </div>
          <div className={styles.headerActions}>
          </div>
        </div>
        {error && <ErrorBanner message={error} onClose={() => setError('')} />}
        {loading ? <LoadingSpinner /> : (
          <div className={styles.content}>
            <button className={styles.createBtn} onClick={openCreate} type="button"><Plus size={14} style={{ verticalAlign: '-2px' }} /> {t('scheduledTask.newTask')}</button>

            {showForm && (
              <form className={styles.form} onSubmit={handleSubmit}>
                <div className={styles.formHeader}>
                  <h2 className={styles.formTitle}>{editingId ? t('scheduledTask.editTask') : t('scheduledTask.newTask')}</h2>
                  <p className={styles.workspaceHint}>
                    {t('scheduledTask.workspaceHint')}：{workspaceName || t('workspace.default')}
                  </p>
                </div>
                <div className={styles.field}>
                  <label className={styles.label}>{t('scheduledTask.name')}</label>
                  <input className={styles.input} type="text" value={form.name}
                    onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder={t('scheduledTask.namePlaceholder')} required
                  />
                </div>
                {/* agent+模型 合并下拉（与新建任务页同一控件，含 selector.filters 过滤） */}
                <div className={styles.field}>
                  <label className={styles.label}>{t('scheduledTask.agentModel')}</label>
                  <AgentModelSelector
                    agents={agents}
                    modelsByAgent={agentModelsMap}
                    filters={selectorFilters}
                    selectedAgent={form.agent_type}
                    selectedModel={form.model_value}
                    className={styles.input}
                    onSelect={(agentType, modelValue) => setForm({ ...form, agent_type: agentType, model_value: modelValue })}
                  />
                  {probing && <span className={styles.hint}>{t('common.loading')}</span>}
                </div>
                <div className={styles.fieldRow}>
                  <div className={styles.field}>
                    <label className={styles.label}>{t('scheduledTask.schedule')}</label>
                    <select className={styles.input} value={form.preset} onChange={(e) => handlePresetChange(e.target.value)}>
                      {[
                        { label: t('scheduledTask.every5min'), value: '*/5 * * * *' },
                        { label: t('scheduledTask.everyHour'), value: '0 * * * *' },
                        { label: t('scheduledTask.daily0'), value: '0 0 * * *' },
                        { label: t('scheduledTask.daily9'), value: '0 9 * * *' },
                        { label: t('scheduledTask.weeklyMon9'), value: '0 9 * * 1' },
                        { label: t('scheduledTask.custom'), value: '' },
                      ].map((p) => (<option key={p.label} value={p.label}>{p.label}</option>))}
                    </select>
                  </div>
                  <div className={styles.field}>
                    <label className={styles.label}>{t('scheduledTask.cronExpr')}</label>
                    <input className={styles.input} type="text" value={form.cron_expr}
                      onChange={(e) => setForm({ ...form, cron_expr: e.target.value })}
                      placeholder={t('scheduledTask.cronExprPlaceholder')} required
                    />
                  </div>
                  <div className={styles.field}>
                    <label className={styles.label}>{t('scheduledTask.timeout')}</label>
                    <input className={styles.input} type="number" min={1} max={1440}
                      value={form.timeout_minutes}
                      onChange={(e) => setForm({ ...form, timeout_minutes: Number(e.target.value) || 5 })} required
                    />
                  </div>
                </div>
                <span className={styles.hint}>{t('scheduledTask.cronHint')} · {t('scheduledTask.timeoutHint')}</span>
                <div className={styles.field}>
                  <label className={styles.label}>{t('scheduledTask.prompt')}</label>
                  <div className={styles.promptWrap}>
                    <PromptInput
                      embedded
                      rows={4}
                      value={form.prompt}
                      onValueChange={(prompt) => setForm({ ...form, prompt })}
                      onSend={() => {}}
                      disabled={saving}
                      commands={taskCommands}
                      modes={taskModes}
                      skills={taskSkills}
                      cwd={workspaceCwd}
                      placeholder={t('scheduledTask.promptPlaceholder')}
                    />
                  </div>
                </div>
                <label className={styles.checkboxRow}>
                  <input type="checkbox" checked={form.enabled}
                    onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
                  /> <span>{t('scheduledTask.enabled')}</span>
                </label>
                <div className={styles.formActions}>
                  <button className={styles.submitBtn} type="submit" disabled={saving}>
                    {saving ? t('common.saving') : editingId ? t('common.save') : t('common.create')}
                  </button>
                  <button type="button" className={styles.cancelBtn} onClick={() => { setShowForm(false); setEditingId(null) }}>{t('common.cancel')}</button>
                </div>
              </form>
            )}

            <div className={styles.taskList}>
              {visibleTasks.length === 0 ? (
                <p className={styles.empty}>{t('scheduledTask.noTasks')}</p>
              ) : (
                visibleTasks.map((task) => (
                  <div key={task.id} className={styles.taskCard}>
                    <div className={styles.taskHeader}>
                      <span className={styles.taskName}>{task.name}</span>
                      <span className={`${styles.taskStatus} ${styles[`status_${task.last_status}`] || ''}`}>
                        <StatusIcon status={task.last_status} />
                        {taskStatusLabels[task.last_status] || t('scheduledTask.noTasks')}
                      </span>
                    </div>
                    <div className={styles.taskMeta}>
                      <span>{t('scheduledTask.agentType')}: {task.agent_type}</span>
                      <span>Cron: {task.cron_expr}</span>
                      <span>{t('scheduledTask.enabled')}: {task.enabled ? t('common.yes') : t('common.no')}</span>
                    </div>
                    <p className={styles.taskPrompt}>{task.prompt}</p>
                    <div className={styles.taskFooter}>
                      <span className={styles.taskTime}>
                        {task.last_run_at
                          ? `${t('scheduledTask.lastRun')}: ${new Date(task.last_run_at).toLocaleString(locale)}`
                          : t('scheduledTask.noTasks')}
                      </span>
                      <div className={styles.taskActions}>
                        <button type="button" className={styles.runBtn} onClick={() => handleRun(task)}>{t('scheduledTask.runNow')}</button>
                        <button type="button" className={styles.toggleBtn} onClick={() => handleToggleEnabled(task)}>
                          {task.enabled ? t('scheduledTask.disabled') : t('scheduledTask.enabled')}
                        </button>
                        <button type="button" className={styles.editBtn} onClick={() => openEdit(task)}>{t('common.edit')}</button>
                        <button type="button" className={styles.deleteBtn} onClick={() => handleDelete(task)}>{t('common.delete')}</button>
                      </div>
                    </div>
                  </div>
                ))
              )}
            </div>
          </div>
        )}
      </div>
    </AppLayout>
  )
}
