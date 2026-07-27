import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  getTaskManager, getTaskStatus, getTaskGitStatus, initTaskGitRepo,
  upsertTask, deleteTask, startTaskManager, stopTaskManager, saveTaskManager,
  subscribeTaskEvents,
  type TaskManagerDef, type TaskManagerTask, type TaskPriority,
} from '../api/taskmanager'
import { sessionUrl, newTaskUrl } from '../utils/routes'
import type { Agent } from '../types'
import LoadingSpinner from './LoadingSpinner'
import TaskManagerChatPanel from './TaskManagerChatPanel'
import SplitPane from './SplitPane'
import styles from './TaskManagerView.module.css'
import { ChevronRight, ChevronDown, MessagesSquare, GitBranch, Plus, FileJson, List, Play, PlayCircle, Square, Trash2 } from 'lucide-react'

const ACTIVE_STATUSES = new Set(['queued', 'running'])

// 生成一个不与现有任务冲突的短 id（客户端新建任务用）。
function genTaskId(): string {
  return `t${Date.now().toString(36)}${Math.floor(Math.random() * 36).toString(36)}`
}

// 规范化后端返回的 def：确保 tasks 为数组（tasks.json 不存在/为空时后端可能省略 tasks 字段）。
function normalizeDef(d: TaskManagerDef | null | undefined): TaskManagerDef {
  const tasks = d?.tasks ?? []
  return { max_parallel: d?.max_parallel || 3, tasks }
}

interface Props {
  workspaceId: number | undefined
  cwd: string
  agents: Agent[]
  /** 从侧边栏点击编排对话记录进入时，指定需恢复的管理会话 DB 主键。 */
  restoreSessionId?: number
  onError: (message: string) => void
}

/**
 * TaskManagerView：编排模式主体（嵌入 ChatPage 编排模式，不走 LayoutRenderer）。
 * 左栏任务列表（含 git 检测/初始化提示、轮询），右栏 AI 管理对话（TaskManagerChatPanel）。
 * 逻辑与原独立编排页一致：点击任务打开其子会话；未运行任务则打开新建任务页预填详情。
 */
export default function TaskManagerView({ workspaceId, cwd, agents, restoreSessionId, onError }: Props) {
  const { t } = useTranslation()
  const navigate = useNavigate()

  const [def, setDef] = useState<TaskManagerDef>({ max_parallel: 3, tasks: [] })
  const [loading, setLoading] = useState(true)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  // 展开/折叠的任务卡片 id 集合
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  // git 仓库状态：null=未知/加载中；true=是仓库；false=需初始化
  const [gitRepo, setGitRepo] = useState<boolean | null>(null)
  const [gitInitializing, setGitInitializing] = useState(false)

  // 新建任务内联表单
  const [showNewForm, setShowNewForm] = useState(false)
  const [newTitle, setNewTitle] = useState('')
  const [newPrompt, setNewPrompt] = useState('')
  const [newPriority, setNewPriority] = useState<TaskPriority>('p1')
  const [newAgent, setNewAgent] = useState('')
  // JSON 查看/编辑模式
  const [jsonMode, setJsonMode] = useState(false)
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  // 单任务操作（新建/启停/删除/保存）进行中，避免并发点击
  const [busy, setBusy] = useState(false)

  // 检查当前工作目录是否为 git 仓库（编排任务需基于 worktree 隔离）
  useEffect(() => {
    if (!workspaceId) { setGitRepo(null); return }
    let alive = true
    getTaskGitStatus(workspaceId)
      .then((r) => { if (alive) setGitRepo(!!r.data.is_git_repo) })
      .catch(() => { if (alive) setGitRepo(null) })
    return () => { alive = false }
  }, [workspaceId])

  // 初始加载编排定义
  useEffect(() => {
    if (!workspaceId) { setLoading(false); return }
    let alive = true
    setLoading(true)
    getTaskManager(workspaceId)
      .then((r) => { if (alive) setDef(normalizeDef(r.data)) })
      .catch((e) => alive && onError(String((e as Error)?.message || e)))
      .finally(() => alive && setLoading(false))
    return () => { alive = false }
  }, [workspaceId, onError])

  // 有活跃任务时轮询状态（左侧列表实时反映运行状态）
  useEffect(() => {
    const hasActive = def.tasks.some((tk) => ACTIVE_STATUSES.has(tk.status))
    if (!hasActive || !workspaceId) {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
      return
    }
    if (pollRef.current) return
    pollRef.current = setInterval(() => {
      getTaskStatus(workspaceId)
        .then((r) => setDef(normalizeDef(r.data)))
        .catch(() => {})
    }, 2000)
    return () => {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
    }
  }, [def.tasks, workspaceId])

  // 订阅后端 tasks.json 变更事件：任意来源（左侧操作、任务助手 MCP 工具、定时调度器）
  // 写入后都会推送，收到即防抖刷新列表——保证两侧操作后左栏自动同步，
  // 不再依赖前端对工具调用的正则检测或轮询兼容。
  useEffect(() => {
    if (!workspaceId) return
    const ac = new AbortController()
    let timer: ReturnType<typeof setTimeout> | null = null
    subscribeTaskEvents(workspaceId, () => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = null
        getTaskStatus(workspaceId)
          .then((r) => setDef(normalizeDef(r.data)))
          .catch(() => {})
      }, 300)
    }, ac.signal)
    return () => {
      ac.abort()
      if (timer) clearTimeout(timer)
    }
  }, [workspaceId])

  async function reloadStatus() {
    if (!workspaceId) return
    try {
      const r = await getTaskStatus(workspaceId)
      setDef(normalizeDef(r.data))
    } catch { /* ignore */ }
  }

  // 变更任务（新建/删除/保存）后用完整 def 刷新，保证 JSON 视图与 parent_session_id 准确。
  async function reloadDef() {
    if (!workspaceId) return
    try {
      const r = await getTaskManager(workspaceId)
      setDef(normalizeDef(r.data))
    } catch { /* ignore */ }
  }

  // 提交新建任务：prompt 必填（作为 detail），标题缺省取 prompt 首行。
  async function handleCreateTask() {
    if (!workspaceId || busy) return
    const prompt = newPrompt.trim()
    if (!prompt) { onError(t('taskmanager.promptRequired')); return }
    const title = newTitle.trim() || prompt.split('\n')[0].slice(0, 40)
    const agentType = (newAgent || agents[0]?.type || '').trim()
    setBusy(true)
    try {
      await upsertTask(workspaceId, { id: genTaskId(), title, detail: prompt, agent_type: agentType, priority: newPriority })
      setShowNewForm(false)
      setNewTitle('')
      setNewPrompt('')
      setNewPriority('p1')
      await reloadDef()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  function cancelNewForm() {
    setShowNewForm(false)
    setNewTitle('')
    setNewPrompt('')
    setNewPriority('p1')
  }

  // 手动启动单个任务。
  async function handleStartTask(task: TaskManagerTask) {
    if (!workspaceId || busy) return
    setBusy(true)
    try {
      await startTaskManager(workspaceId, task.id)
      await reloadStatus()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 启动全部待执行任务（不传 task_id，由后端启动所有待执行任务）。
  async function handleStartAll() {
    if (!workspaceId || busy) return
    setBusy(true)
    try {
      await startTaskManager(workspaceId)
      await reloadStatus()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 停止单个运行中的任务。
  async function handleStopTask(task: TaskManagerTask) {
    if (!workspaceId || busy) return
    setBusy(true)
    try {
      await stopTaskManager(workspaceId, task.id)
      await reloadStatus()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 删除单个任务（需确认）。
  async function handleDeleteTask(task: TaskManagerTask) {
    if (!workspaceId || busy) return
    if (!window.confirm(t('taskmanager.confirmDelete'))) return
    setBusy(true)
    try {
      await deleteTask(workspaceId, task.id)
      await reloadDef()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 进入 JSON 编辑模式：用当前 def 初始化文本。
  function enterJsonMode() {
    setJsonText(JSON.stringify(def, null, 2))
    setJsonError('')
    setJsonMode(true)
  }

  // 保存 JSON：解析后整体覆盖 tasks.json。
  async function saveJson() {
    if (!workspaceId || busy) return
    let parsed: TaskManagerDef
    try {
      parsed = JSON.parse(jsonText)
    } catch (e) {
      setJsonError(t('taskmanager.jsonInvalid') + ': ' + String((e as Error)?.message || e))
      return
    }
    setBusy(true)
    try {
      await saveTaskManager(workspaceId, parsed)
      await reloadDef()
      setJsonMode(false)
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  function toggleExpand(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  // 点击任务名称：打开任务界面（与普通任务界面一致）。
  // 已运行的任务打开其关联会话；尚未运行的任务打开新建任务页并用任务详情预填 prompt。
  function openTask(task: TaskManagerTask) {
    if (task.db_session_id) {
      navigate(sessionUrl(task.db_session_id, workspaceId))
    } else {
      navigate(newTaskUrl(workspaceId), { state: { draftPrompt: task.detail } })
    }
  }

  // 初始化 git 仓库（含初始提交）并创建 .worktrees 目录，成功后刷新状态。
  async function handleGitInit() {
    if (!workspaceId || gitInitializing) return
    setGitInitializing(true)
    try {
      const r = await initTaskGitRepo(workspaceId)
      setGitRepo(!!r.data.is_git_repo)
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setGitInitializing(false)
    }
  }

  if (loading) return <LoadingSpinner />

  return (
    <SplitPane dir="row" storageKey="taskmanager" defaultFlexes={[1, 1]}>
      {/* 左栏：任务列表。顶部工具栏可新建任务 / 切换 JSON 视图；每个任务右侧可手动启停/删除。 */}
      <div className={styles.taskCol}>
        {gitRepo !== false && (
          <div className={styles.toolbar}>
            <span className={styles.toolbarTitle}>{t('taskmanager.groupTitle')}</span>
            <div className={styles.toolbarActions}>
              {!jsonMode && def.tasks.some((tk) => !ACTIVE_STATUSES.has(tk.status)) && (
                <button
                  type="button"
                  className={styles.toolbarBtn}
                  onClick={handleStartAll}
                  disabled={busy}
                  title={t('taskmanager.startAll')}
                >
                  <PlayCircle size={14} /> {t('taskmanager.startAll')}
                </button>
              )}
              {!jsonMode && (
                <button
                  type="button"
                  className={styles.toolbarBtn}
                  onClick={() => { setShowNewForm((v) => !v); setNewAgent(agents[0]?.type || '') }}
                  disabled={busy}
                  title={t('taskmanager.newTask')}
                >
                  <Plus size={14} /> {t('taskmanager.newTask')}
                </button>
              )}
              <button
                type="button"
                className={styles.toolbarBtn}
                onClick={() => { if (jsonMode) { setJsonMode(false) } else { enterJsonMode() } }}
                disabled={busy}
                title={jsonMode ? t('taskmanager.viewList') : t('taskmanager.viewJson')}
              >
                {jsonMode ? <><List size={14} /> {t('taskmanager.viewList')}</> : <><FileJson size={14} /> {t('taskmanager.viewJson')}</>}
              </button>
            </div>
          </div>
        )}
        <div className={styles.taskScroll}>
          {gitRepo === false ? (
            <div className={styles.gitPrompt}>
              <GitBranch size={32} className={styles.gitPromptIcon} />
              <h3 className={styles.gitPromptTitle}>{t('taskmanager.gitRequiredTitle')}</h3>
              <p className={styles.gitPromptHint}>{t('taskmanager.gitRequiredHint')}</p>
              {cwd && <code className={styles.gitPromptCwd}>{cwd}</code>}
              <button
                type="button"
                className={styles.gitInitBtn}
                onClick={handleGitInit}
                disabled={gitInitializing}
              >
                {gitInitializing ? t('taskmanager.gitInitializing') : t('taskmanager.gitInit')}
              </button>
            </div>
          ) : jsonMode ? (
            <div className={styles.jsonEditor}>
              <textarea
                className={styles.jsonTextarea}
                value={jsonText}
                onChange={(e) => { setJsonText(e.target.value); setJsonError('') }}
                spellCheck={false}
              />
              {jsonError && <div className={styles.jsonError}>{jsonError}</div>}
              <div className={styles.jsonActions}>
                <button type="button" className={styles.formCancel} onClick={() => setJsonMode(false)} disabled={busy}>
                  {t('taskmanager.cancel')}
                </button>
                <button type="button" className={styles.formConfirm} onClick={saveJson} disabled={busy}>
                  {t('taskmanager.save')}
                </button>
              </div>
            </div>
          ) : (
            <div className={styles.taskList}>
              {showNewForm && (
                <div className={styles.newForm}>
                  <input
                    className={styles.formInput}
                    value={newTitle}
                    onChange={(e) => setNewTitle(e.target.value)}
                    placeholder={t('taskmanager.titlePlaceholder')}
                  />
                  <textarea
                    className={styles.formTextarea}
                    value={newPrompt}
                    onChange={(e) => setNewPrompt(e.target.value)}
                    placeholder={t('taskmanager.promptPlaceholder')}
                    autoFocus
                  />
                  <select
                    className={styles.formSelect}
                    value={newPriority}
                    onChange={(e) => setNewPriority(e.target.value as TaskPriority)}
                    aria-label={t('taskmanager.priority')}
                  >
                    <option value="p0">{t('taskmanager.priority_p0')}</option>
                    <option value="p1">{t('taskmanager.priority_p1')}</option>
                    <option value="p2">{t('taskmanager.priority_p2')}</option>
                  </select>
                  <div className={styles.formActions}>
                    <button type="button" className={styles.formCancel} onClick={cancelNewForm} disabled={busy}>
                      {t('taskmanager.cancel')}
                    </button>
                    <button type="button" className={styles.formConfirm} onClick={handleCreateTask} disabled={busy || !newPrompt.trim()}>
                      {t('taskmanager.create')}
                    </button>
                  </div>
                </div>
              )}
              {def.tasks.length === 0 && !showNewForm ? (
                <div className={styles.empty}>{t('taskmanager.empty')}</div>
              ) : (
                def.tasks.map((task) => {
                const isOpen = expanded.has(task.id)
                const isActive = ACTIVE_STATUSES.has(task.status)
                return (
                  <div key={task.id} className={styles.taskCard}>
                    <div className={styles.taskHeader} onClick={() => toggleExpand(task.id)}>
                      <span className={styles.taskHeaderLeft}>
                        {isOpen
                          ? <ChevronDown size={14} className={styles.taskChevron} />
                          : <ChevronRight size={14} className={styles.taskChevron} />}
                        <span
                          className={styles.taskName}
                          role="button"
                          tabIndex={0}
                          title={t('taskmanager.openTask')}
                          onClick={(e) => { e.stopPropagation(); openTask(task) }}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); openTask(task) }
                          }}
                        >{task.title}</span>
                      </span>
                      <span className={styles.taskHeaderRight}>
                        {task.branch && (
                          <span className={styles.taskBranch} title={task.worktree_path || task.branch}>
                            <GitBranch size={11} />
                            <span className={styles.taskBranchName}>{task.branch}</span>
                          </span>
                        )}
                        <span className={`${styles.taskPriority} ${styles[`priority_${task.priority || 'p1'}`] || ''}`}>
                          {t(`taskmanager.priority_${task.priority || 'p1'}`)}
                        </span>
                        <span className={`${styles.taskStatus} ${styles[`status_${task.status}`] || ''}`}>
                          {t(`taskmanager.status_${task.status}`)}
                        </span>
                        <span className={styles.taskActions} onClick={(e) => e.stopPropagation()}>
                          {isActive ? (
                            <button
                              type="button"
                              className={styles.taskActionBtn}
                              onClick={() => handleStopTask(task)}
                              disabled={busy}
                              title={t('taskmanager.stop')}
                            >
                              <Square size={13} />
                            </button>
                          ) : (
                            <button
                              type="button"
                              className={styles.taskActionBtn}
                              onClick={() => handleStartTask(task)}
                              disabled={busy}
                              title={t('taskmanager.start')}
                            >
                              <Play size={13} />
                            </button>
                          )}
                          <button
                            type="button"
                            className={`${styles.taskActionBtn} ${styles.taskActionDanger}`}
                            onClick={() => handleDeleteTask(task)}
                            disabled={busy}
                            title={t('taskmanager.delete')}
                          >
                            <Trash2 size={13} />
                          </button>
                        </span>
                      </span>
                    </div>
                    {isOpen && (
                      <div className={styles.taskBody}>
                        <div className={styles.taskDetail}>{task.detail}</div>
                        {task.branch && (
                          <div className={styles.taskCwd}>
                            <span className={styles.cwdLabel}>{t('taskmanager.branch')}:</span>
                            <code className={styles.cwdValue}>{task.branch}</code>
                          </div>
                        )}
                        {task.worktree_path && (
                          <div className={styles.taskCwd}>
                            <span className={styles.cwdLabel}>{t('taskmanager.cwd')}:</span>
                            <code className={styles.cwdValue}>{task.worktree_path}</code>
                          </div>
                        )}
                        {task.error && <div className={styles.taskError}>{task.error}</div>}
                        {task.started_at && (
                          <div className={styles.taskTime}>
                            {t('taskmanager.startedAt')}: {new Date(task.started_at).toLocaleString()}
                            {task.finished_at && ` · ${t('taskmanager.finishedAt')}: ${new Date(task.finished_at).toLocaleString()}`}
                          </div>
                        )}
                        {task.db_session_id && (
                          <button
                            type="button"
                            className={styles.openChatLink}
                            onClick={() => openTask(task)}
                            title={t('taskmanager.openChat')}
                          >
                            <MessagesSquare size={13} style={{ verticalAlign: '-2px' }} /> {t('taskmanager.openChat')}
                          </button>
                        )}
                      </div>
                    )}
                  </div>
                )
                })
              )}
            </div>
          )}
        </div>
      </div>

      {/* 右栏：AI 管理对话（常驻，通过工具建/改/删任务、启停、调并发）。单任务在其独立会话页打开。 */}
      <div className={styles.chatCol}>
        {!workspaceId ? (
          <div className={styles.empty}>{t('taskmanager.empty')}</div>
        ) : (
          <div className={styles.chatBody}>
            <TaskManagerChatPanel
              agents={agents}
              workspaceId={workspaceId}
              cwd={cwd}
              restoreSessionId={restoreSessionId}
              onTaskChanged={reloadStatus}
            />
          </div>
        )}
      </div>
    </SplitPane>
  )
}
