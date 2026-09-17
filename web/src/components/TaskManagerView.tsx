import { useState, useEffect, useRef, useCallback } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  getTaskManager, getTaskStatus, getTaskGitStatus, initTaskGitRepo,
  upsertTask, deleteTask, startTaskManager, stopTaskManager, saveTaskManager,
  setTaskMaxParallel, genTaskId, archiveTask, listArchivedTasks,
  restoreArchivedTask, deleteArchivedTask,
  type TaskManagerDef, type TaskManagerTask, type TaskPriority, type ArchivedTask,
} from '../api/taskmanager'
import { listWorkspaces } from '../api/workspaces'
import { useTaskEventsChanged } from '../context/TaskEventsContext'
import { sessionUrl, newTaskUrl } from '../utils/routes'
import { ALL_WORKSPACES_ID } from '../hooks/useCurrentWorkspace'
import type { Agent, Workspace } from '../types'
import LoadingSpinner from './LoadingSpinner'
import TaskManagerChatPanel from './TaskManagerChatPanel'
import SplitPane from './SplitPane'
import styles from './TaskManagerView.module.css'
import { ChevronRight, ChevronDown, MessagesSquare, GitBranch, Plus, FileJson, MoreHorizontal, Play, PlayCircle, Square, Trash2, Target, Gauge, Archive, ArchiveRestore, Layers, Pencil } from 'lucide-react'

const ACTIVE_STATUSES = new Set(['queued', 'running'])

// 已完成（终态）状态：这些任务折叠为紧凑卡片，放到「已完成」分栏。
const COMPLETED_STATUSES = new Set(['done', 'failed', 'canceled', 'interrupt'])

const GOAL_STATUSES = new Set(['active', 'evaluating', 'achieved', 'stopped'])

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

  // 「全部工作区」聚合模式：workspaceId 为哨兵值 -1，并行拉取所有工作区的任务合并展示。
  const isAll = workspaceId === ALL_WORKSPACES_ID

  const [def, setDef] = useState<TaskManagerDef>({ max_parallel: 3, tasks: [] })
  const [loading, setLoading] = useState(true)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  // 全部工作区模式下缓存工作区列表（任务卡片显示来源工作区名、新建任务选择目标工作区）
  const [wsList, setWsList] = useState<Workspace[]>([])
  // 新建任务目标工作区（仅全部工作区模式使用）
  const [newWorkspaceId, setNewWorkspaceId] = useState(0)
  // 展开/折叠的任务卡片 id 集合
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set())
  // git 仓库状态：null=未知/加载中；true=是仓库；false=需初始化
  const [gitRepo, setGitRepo] = useState<boolean | null>(null)
  const [gitInitializing, setGitInitializing] = useState(false)

  // 新建任务内联表单（editingTask 非空时复用同一表单编辑未开始的任务）
  const [showNewForm, setShowNewForm] = useState(false)
  const [editingTask, setEditingTask] = useState<TaskManagerTask | null>(null)
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
  // 已归档任务（回收站，服务端已清理过期条目）
  const [archived, setArchived] = useState<ArchivedTask[]>([])
  // 已归档分栏默认隐藏，可在「更多」菜单中开启（选择持久化，刷新后保持）
  const [showArchived, setShowArchived] = useState(() => localStorage.getItem('opennexus.taskmanager.archived') === '1')
  const toggleArchived = useCallback(() => {
    setShowArchived((v) => {
      const next = !v
      try { localStorage.setItem('opennexus.taskmanager.archived', next ? '1' : '0') } catch { /* ignore */ }
      return next
    })
  }, [])
  // 「更多」下拉菜单（JSON 视图 / 并发设置 / 显示归档）
  const [moreOpen, setMoreOpen] = useState(false)
  const moreRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!moreOpen) return
    function handleClick(e: MouseEvent) {
      if (moreRef.current && !moreRef.current.contains(e.target as Node)) setMoreOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [moreOpen])

  // 检查当前工作目录是否为 git 仓库（编排任务需基于 worktree 隔离）。全部工作区模式跳过。
  useEffect(() => {
    if (!workspaceId || isAll) { setGitRepo(null); return }
    let alive = true
    getTaskGitStatus(workspaceId)
      .then((r) => { if (alive) setGitRepo(!!r.data.is_git_repo) })
      .catch(() => { if (alive) setGitRepo(null) })
    return () => { alive = false }
  }, [workspaceId, isAll])

  // 全部工作区模式：并行拉取所有工作区任务状态并合并，任务附带 workspace_id/workspace_name 标签。
  const fetchAllStatuses = useCallback(async (list?: Workspace[]) => {
    let targets = list
    if (!targets) {
      try { targets = (await listWorkspaces()).data.workspaces || [] } catch { return }
    }
    if (targets.length === 0) { setDef({ max_parallel: 3, tasks: [] }); return }
    const results = await Promise.all(targets.map(async (ws) => {
      try {
        const r = await getTaskStatus(ws.id)
        return (r.data.tasks || []).map((tk) => ({ ...tk, workspace_id: ws.id, workspace_name: ws.name }))
      } catch { return [] as TaskManagerTask[] }
    }))
    setDef((prev) => ({ max_parallel: prev.max_parallel, tasks: results.flat() }))
  }, [])

  // 初始加载编排定义
  useEffect(() => {
    if (isAll) {
      let alive = true
      setLoading(true)
      listWorkspaces()
        .then(async (r) => {
          const list = r.data.workspaces || []
          if (!alive) return
          setWsList(list)
          setNewWorkspaceId((prev) => prev || list[0]?.id || 0)
          await fetchAllStatuses(list)
        })
        .catch((e) => alive && onError(String((e as Error)?.message || e)))
        .finally(() => alive && setLoading(false))
      return () => { alive = false }
    }
    if (!workspaceId) { setLoading(false); return }
    let alive = true
    setLoading(true)
    getTaskManager(workspaceId)
      .then((r) => { if (alive) setDef(normalizeDef(r.data)) })
      .catch((e) => alive && onError(String((e as Error)?.message || e)))
      .finally(() => alive && setLoading(false))
    return () => { alive = false }
  }, [workspaceId, onError, isAll, fetchAllStatuses])

  // 有活跃任务时轮询状态（左侧列表实时反映运行状态）
  useEffect(() => {
    const hasActive = def.tasks.some((tk) => ACTIVE_STATUSES.has(tk.status))
    if (!hasActive || !workspaceId) {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
      return
    }
    if (pollRef.current) return
    pollRef.current = setInterval(() => {
      if (isAll) { fetchAllStatuses(); return }
      getTaskStatus(workspaceId)
        .then((r) => setDef(normalizeDef(r.data)))
        .catch(() => {})
    }, 2000)
    return () => {
      if (pollRef.current) { clearInterval(pollRef.current); pollRef.current = null }
    }
  }, [def.tasks, workspaceId, isAll, fetchAllStatuses])

  // 订阅后端 tasks.json 变更事件：通过 TaskEventsContext 统一消费 SSE（AppLayout 层唯一订阅），
  // 避免与 SessionSidebar 各自建立到 /taskmanager/events 的重复长连接。
  // 任意来源（左侧操作、任务管理 MCP 工具、定时调度器）写入后都会推送，
  // 收到即防抖刷新列表——保证两侧操作后左栏自动同步。
  const taskEventsCtx = useTaskEventsChanged()
  useEffect(() => {
    // 全部工作区模式：SSE 按工作区订阅，这里退化为轮询（有活跃任务时上方 effect 已覆盖）。
    if (!workspaceId || isAll || !taskEventsCtx) return
    let timer: ReturnType<typeof setTimeout> | null = null
    const unregister = taskEventsCtx.onChanged(() => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = null
        reloadStatus()
        reloadArchived()
      }, 300)
    })
    return () => {
      unregister()
      if (timer) clearTimeout(timer)
    }
  }, [workspaceId, taskEventsCtx, isAll])

  async function reloadStatus() {
    if (!workspaceId) return
    if (isAll) { await fetchAllStatuses(); return }
    try {
      const r = await getTaskStatus(workspaceId)
      setDef(normalizeDef(r.data))
    } catch { /* ignore */ }
  }

  // 已归档任务列表（回收站）
  async function reloadArchived() {
    // 回收站按工作区隔离，全部工作区模式下不加载（分栏亦隐藏）。
    if (!workspaceId || isAll) return
    try {
      const r = await listArchivedTasks(workspaceId)
      setArchived(r.data.tasks || [])
    } catch { /* ignore */ }
  }

  useEffect(() => { reloadArchived() }, [workspaceId])

  // 变更任务（新建/删除/保存）后用完整 def 刷新，保证 JSON 视图与 parent_session_id 准确。
  async function reloadDef() {
    if (!workspaceId) return
    if (isAll) { await fetchAllStatuses(); return }
    try {
      const r = await getTaskManager(workspaceId)
      setDef(normalizeDef(r.data))
    } catch { /* ignore */ }
  }

  // 提交新建/编辑任务：prompt 必填（作为 detail），标题缺省取 prompt 首行。
  // 与 MCP create_task 行为一致：默认开启 goal 模式，完成条件取任务详情。
  // 全部工作区模式下需先选择目标工作区（newWorkspaceId）。
  // 编辑模式：复用 upsert 按 id 更新，后端保留 status/session 等运行时字段。
  async function handleCreateTask() {
    const targetWs = editingTask ? taskWs(editingTask) : (isAll ? newWorkspaceId : workspaceId)
    if (!targetWs || busy) return
    const prompt = newPrompt.trim()
    if (!prompt) { onError(t('taskmanager.promptRequired')); return }
    const title = newTitle.trim() || prompt.split('\n')[0].slice(0, 40)
    const agentType = editingTask ? editingTask.agent_type : (newAgent || agents[0]?.type || '').trim()
    setBusy(true)
    try {
      await upsertTask(targetWs, { id: editingTask ? editingTask.id : genTaskId(), title, detail: prompt, agent_type: agentType, priority: newPriority, goal_condition: prompt })
      setShowNewForm(false)
      setEditingTask(null)
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

  // 编辑未开始的任务：复用新建表单预填当前值，保存走 upsert 更新。
  function openEditTask(task: TaskManagerTask) {
    setEditingTask(task)
    setNewTitle(task.title)
    setNewPrompt(task.detail)
    setNewPriority((task.priority as TaskPriority) || 'p1')
    setShowNewForm(true)
  }

  function cancelNewForm() {
    setShowNewForm(false)
    setEditingTask(null)
    setNewTitle('')
    setNewPrompt('')
    setNewPriority('p1')
  }

  // 任务所属工作区：聚合模式下取任务自带 workspace_id，单工作区模式即当前工作区。
  function taskWs(task: TaskManagerTask): number | undefined {
    return task.workspace_id ?? workspaceId
  }

  // 手动启动单个任务。
  async function handleStartTask(task: TaskManagerTask) {
    const wsId = taskWs(task)
    if (!wsId || busy) return
    setBusy(true)
    try {
      await startTaskManager(wsId, task.id)
      await reloadStatus()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 启动全部待执行任务（不传 task_id，由后端启动所有待执行任务）。需确认。
  // 全部工作区模式：逐个工作区启动其全部待执行任务。
  async function handleStartAll() {
    if (busy) return
    if (!window.confirm(t('taskmanager.confirmStartAll'))) return
    if (isAll) {
      setBusy(true)
      try {
        await Promise.all(wsList.map((ws) => startTaskManager(ws.id).catch(() => {})))
        await fetchAllStatuses()
      } finally {
        setBusy(false)
      }
      return
    }
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
    const wsId = taskWs(task)
    if (!wsId || busy) return
    setBusy(true)
    try {
      await stopTaskManager(wsId, task.id)
      await reloadStatus()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 设置默认并发数量（max_parallel）：乐观更新本地 def，失败时回读还原。
  // 与任务管理对话中的 set_max_parallel 工具写同一个 tasks.json 字段，SSE 事件会同步两侧。
  async function handleSetMaxParallel(n: number) {
    if (!workspaceId || busy) return
    setDef((prev) => ({ ...prev, max_parallel: n }))
    try {
      await setTaskMaxParallel(workspaceId, n)
    } catch (e) {
      onError(String((e as Error)?.message || e))
      await reloadDef()
    }
  }

  // 删除单个任务（需确认）。有 worktree 的任务默认保留 worktree，用户可选一并删除。
  async function handleDeleteTask(task: TaskManagerTask) {
    const wsId = taskWs(task)
    if (!wsId || busy) return
    const hasWorktree = !!task.worktree_path
    // 有 worktree 时提示用户选择：确认=仅删任务保留 worktree；取消=不删。
    // 浏览器 confirm 无法做三态，这里用 confirm 表达"删任务保留 worktree"，
    // 需要连同删 worktree 时用下面的二次确认。
    let removeWorktree = false
    if (hasWorktree) {
      const keep = window.confirm(t('taskmanager.confirmDeleteKeepWorktree', { branch: task.branch || task.worktree_path }))
      if (!keep) return
      // 再问是否一并删除 worktree
      removeWorktree = window.confirm(t('taskmanager.confirmAlsoRemoveWorktree', { branch: task.branch || task.worktree_path }))
    } else {
      if (!window.confirm(t('taskmanager.confirmDelete'))) return
    }
    setBusy(true)
    try {
      await deleteTask(wsId, task.id, removeWorktree)
      await reloadDef()
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 归档单个任务到回收站（已归档分栏可恢复）。全部工作区模式下隐藏入口。
  async function handleArchiveTask(task: TaskManagerTask) {
    const wsId = taskWs(task)
    if (!wsId || busy || isAll) return
    if (!window.confirm(t('taskmanager.confirmArchive', { title: task.title }))) return
    setBusy(true)
    try {
      await archiveTask(wsId, task.id)
      await Promise.all([reloadDef(), reloadArchived()])
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 从回收站恢复任务到任务列表。
  async function handleRestoreArchived(task: ArchivedTask) {
    if (!workspaceId || busy) return
    setBusy(true)
    try {
      await restoreArchivedTask(workspaceId, task.id)
      await Promise.all([reloadDef(), reloadArchived()])
    } catch (e) {
      onError(String((e as Error)?.message || e))
    } finally {
      setBusy(false)
    }
  }

  // 彻底删除回收站中的任务（含 worktree 与关联会话，不可恢复）。
  async function handleDeleteArchived(task: ArchivedTask) {
    if (!workspaceId || busy) return
    if (!window.confirm(t('trash.deleteConfirm', { title: task.title }))) return
    setBusy(true)
    try {
      await deleteArchivedTask(workspaceId, task.id)
      await reloadArchived()
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
    const wsId = taskWs(task)
    if (task.db_session_id) {
      navigate(sessionUrl(task.db_session_id, wsId))
    } else {
      navigate(newTaskUrl(wsId), { state: { draftPrompt: task.detail } })
    }
  }

  // 初始化 git 仓库（含初始提交）并确保 worktree 存放目录（~/.openNexus/worktrees）存在，成功后刷新状态。
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

  // 右侧 AI 管理对话默认隐藏，可从工具栏展开（选择持久化，刷新后保持）
  const [chatOpen, setChatOpen] = useState(() => localStorage.getItem('opennexus.taskmanager.chat') === '1')
  const toggleChat = useCallback(() => {
    setChatOpen((v) => {
      const next = !v
      try { localStorage.setItem('opennexus.taskmanager.chat', next ? '1' : '0') } catch { /* ignore */ }
      return next
    })
  }, [])

  if (loading) return <LoadingSpinner />

  // 默认并发数量选择器：写入 tasks.json 的 max_parallel（与任务管理 set_max_parallel 工具同源）。
  const maxParallelCtl = (
    <label className={styles.parallelCtl} title={t('taskmanager.maxParallelHint')}>
      <Gauge size={13} />
      <span className={styles.parallelLabel}>{t('taskmanager.maxParallel')}</span>
      <select
        className={styles.parallelSelect}
        value={def.max_parallel > 0 ? def.max_parallel : 3}
        onChange={(e) => handleSetMaxParallel(Number(e.target.value))}
        disabled={busy}
      >
        {Array.from({ length: 16 }, (_, i) => i + 1).map((n) => (
          <option key={n} value={n}>{n}</option>
        ))}
      </select>
    </label>
  )

  // 单个任务卡片（状态分栏列表中复用）。
  const renderTaskCard = (task: TaskManagerTask) => {
    const isOpen = expanded.has(task.id)
    const isActive = ACTIVE_STATUSES.has(task.status)
    return (
      <div key={task.id} className={`${styles.taskCard} ${task.status === 'done' ? styles.taskCardDone : COMPLETED_STATUSES.has(task.status) ? styles.taskCardAbnormal : ''}`}>
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
            {/* 全部工作区模式：来源工作区徽标置于第二行 local/worktree 标记左侧 */}
            {isAll && task.workspace_name && (
              <span className={styles.taskWs} title={task.workspace_name}>
                <Layers size={11} />
                <span>{task.workspace_name}</span>
              </span>
            )}
            {/* 执行模式标记：已创建 worktree 显示 worktree，否则为 local（启动后自动切换） */}
            <span
              className={`${styles.taskMode} ${task.worktree_path || task.branch ? styles.modeWorktree : styles.modeLocal}`}
              title={task.worktree_path || t(task.worktree_path || task.branch ? 'taskmanager.modeWorktreeHint' : 'taskmanager.modeLocalHint')}
            >
              {t(task.worktree_path || task.branch ? 'taskmanager.modeWorktree' : 'taskmanager.modeLocal')}
            </span>
            {task.branch && (
              <span className={styles.taskBranch} title={task.worktree_path || task.branch}>
                <GitBranch size={11} />
                <span className={styles.taskBranchName}>{task.branch}</span>
              </span>
            )}
            {/* 未运行仅有定义侧条件时也在任务行直接展示 goal 徽标，悬停可看完整条件 */}
            {!task.goal && task.goal_condition && (
              <span className={styles.taskGoal} title={task.goal_condition}>
                <Target size={11} />
                goal
              </span>
            )}
            {task.goal && GOAL_STATUSES.has(task.goal.status) && (
              <span
                className={`${styles.taskGoal} ${styles[`goal_${task.goal.status}`] || ''}`}
                title={`${task.goal.condition}${task.goal.last_reason ? `\n${task.goal.last_reason}` : ''}`}
              >
                <Target size={11} />
                {t(`taskmanager.goal_${task.goal.status}`)}
                {task.goal.status === 'active' && task.goal.turns > 0 ? ` ×${task.goal.turns}` : ''}
                {(task.goal.status === 'stopped' || task.goal.status === 'achieved') && (task.goal.eval_count ?? 0) > 0
                  ? ` · ${t('taskmanager.goalEvalCount', { count: task.goal.eval_count })}`
                  : ''}
              </span>
            )}
            {/* 已完成任务不显示状态文字，用颜色区分（绿色=正常完成，红色=异常终止） */}
            {!COMPLETED_STATUSES.has(task.status) && (
              <span className={`${styles.taskStatus} ${styles[`status_${task.status}`] || ''}`}>
                {t(`taskmanager.status_${task.status}`)}
              </span>
            )}
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
              {/* 未开始的任务可编辑（复用新建表单，upsert 按 id 更新）；已启动/已结束不可编辑 */}
              {task.status === 'pending' && (
                <button
                  type="button"
                  className={styles.taskActionBtn}
                  onClick={() => openEditTask(task)}
                  disabled={busy}
                  title={t('taskmanager.edit')}
                >
                  <Pencil size={13} />
                </button>
              )}
              {COMPLETED_STATUSES.has(task.status) && (
                <button
                  type="button"
                  className={styles.taskActionBtn}
                  onClick={() => handleArchiveTask(task)}
                  disabled={busy}
                  title={t('taskmanager.archive')}
                >
                  <Archive size={13} />
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
            {/* 未运行过的任务展示定义侧 goal 条件；运行后由下方 goal 状态块接管 */}
            {!task.goal && task.goal_condition && (
              <div className={styles.taskGoalDetail}>
                <div className={styles.taskGoalDetailRow}>
                  <Target size={12} style={{ flexShrink: 0 }} />
                  <span>goal</span>
                </div>
                <div className={styles.taskGoalCondition}>{task.goal_condition}</div>
              </div>
            )}
            {task.goal && GOAL_STATUSES.has(task.goal.status) && (
              <div className={styles.taskGoalDetail}>
                <div className={styles.taskGoalDetailRow}>
                  <Target size={12} style={{ flexShrink: 0 }} />
                  <span>
                    {t(`taskmanager.goal_${task.goal.status}`)}
                    {task.goal.turns > 0 ? ` · ${t('taskmanager.goalTurns', { count: task.goal.turns })}` : ''}
                    {(task.goal.eval_count ?? 0) > 0 ? ` · ${t('taskmanager.goalEvalCount', { count: task.goal.eval_count })}` : ''}
                  </span>
                </div>
                <div className={styles.taskGoalCondition}>{task.goal.condition}</div>
                {task.goal.roles && task.goal.roles.length > 0 && (
                  <div className={styles.taskGoalMeta}>{t('taskmanager.goalRoles')}: {task.goal.roles.join('、')}</div>
                )}
                {task.goal.last_reason && (
                  <div className={styles.taskGoalMeta}>{t('taskmanager.goalLastEval')}: {task.goal.last_reason}</div>
                )}
              </div>
            )}
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
  }

  const taskListCol = (
    // 左栏：任务列表。顶部工具栏可新建任务 / 切换 JSON 视图；每个任务右侧可手动启停/删除。
    // 全部工作区模式：隐藏单工作区专属功能（并发设置 / JSON 视图 / 归档分栏）。
    <div className={isAll ? `${styles.taskCol} ${styles.taskColFull}` : styles.taskCol}>
        {gitRepo !== false && (
          <div className={styles.toolbar}>
            <span className={styles.toolbarTitle}>{isAll ? t('workspace.all') : t('taskmanager.groupTitle')}</span>
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
                  onClick={() => { setEditingTask(null); setShowNewForm((v) => !v); setNewAgent(agents[0]?.type || '') }}
                  disabled={busy}
                  title={t('taskmanager.newTask')}
                >
                  <Plus size={14} /> {t('taskmanager.newTask')}
                </button>
              )}
              {/* 助手对话按工作区隔离，聚合视图不展示右栏（见下方 isAll 分支），按钮一并隐藏避免点了没反应 */}
              {!isAll && (
                <button
                  type="button"
                  className={styles.toolbarBtn}
                  onClick={toggleChat}
                  title={chatOpen ? t('taskmanager.hideChat') : t('taskmanager.showChat')}
                >
                  <MessagesSquare size={14} /> {chatOpen ? t('taskmanager.hideChat') : t('taskmanager.showChat')}
                </button>
              )}
              {!isAll && (
              <div className={styles.moreWrap} ref={moreRef}>
                <button
                  type="button"
                  className={styles.toolbarBtn}
                  onClick={() => setMoreOpen((v) => !v)}
                  title={t('taskmanager.more')}
                >
                  <MoreHorizontal size={14} /> {t('taskmanager.more')}
                </button>
                {moreOpen && (
                  <div className={styles.moreDropdown}>
                    <div className={styles.moreItem} onClick={(e) => e.stopPropagation()}>
                      {maxParallelCtl}
                    </div>
                    <button
                      type="button"
                      className={styles.moreItem}
                      onClick={() => { setMoreOpen(false); if (jsonMode) { setJsonMode(false) } else { enterJsonMode() } }}
                    >
                      <FileJson size={13} /> {jsonMode ? t('taskmanager.viewList') : t('taskmanager.viewJson')}
                    </button>
                    <button type="button" className={styles.moreItem} onClick={toggleArchived}>
                      <Archive size={13} /> {showArchived ? t('taskmanager.hideArchived') : t('taskmanager.showArchived')}
                    </button>
                  </div>
                )}
              </div>
              )}
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
          ) : jsonMode && !isAll ? (
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
                  {/* 全部工作区模式：新建任务需先选择目标工作区（编辑时不可改目标工作区） */}
                  {isAll && !editingTask && (
                    <select
                      className={styles.formSelect}
                      value={newWorkspaceId}
                      onChange={(e) => setNewWorkspaceId(Number(e.target.value))}
                      aria-label={t('taskmanager.targetWorkspace')}
                    >
                      {wsList.map((ws) => (
                        <option key={ws.id} value={ws.id}>
                          {ws.name === '默认工作区' ? t('workspace.default') : ws.name}
                        </option>
                      ))}
                    </select>
                  )}
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
                      {editingTask ? t('taskmanager.save') : t('taskmanager.create')}
                    </button>
                  </div>
                </div>
              )}
              {def.tasks.length === 0 && archived.length === 0 && !showNewForm ? (
                <div className={styles.empty}>{t('taskmanager.empty')}</div>
              ) : (
                <div className={styles.taskColumns}>
                  {([
                    { key: 'pending', title: t('taskmanager.colPending'), tasks: def.tasks.filter((tk) => !ACTIVE_STATUSES.has(tk.status) && !COMPLETED_STATUSES.has(tk.status)) },
                    { key: 'running', title: t('taskmanager.colRunning'), tasks: def.tasks.filter((tk) => tk.status === 'running') },
                    { key: 'done', title: t('taskmanager.completedTitle'), tasks: def.tasks.filter((tk) => COMPLETED_STATUSES.has(tk.status)) },
                  ] as const).map((col) => (
                    <div key={col.key} className={styles.taskColumn}>
                      <div className={styles.taskColumnHeader}>
                        <span className={styles.taskColumnTitle}>{col.title}</span>
                        <span className={styles.taskColumnCount}>{col.tasks.length}</span>
                      </div>
                      <div className={styles.taskColumnBody}>
                        {col.tasks.length === 0 ? (
                          <div className={styles.completedEmpty}>{t('taskmanager.colEmpty')}</div>
                        ) : (
                          col.tasks.map(renderTaskCard)
                        )}
                      </div>
                    </div>
                  ))}
                  {showArchived && !isAll && (
                  <div className={styles.taskColumn}>
                    <div className={styles.taskColumnHeader}>
                      <span className={styles.taskColumnTitle}>{t('taskmanager.colArchived')}</span>
                      <span className={styles.taskColumnCount}>{archived.length}</span>
                    </div>
                    <div className={styles.taskColumnBody}>
                      {archived.length === 0 ? (
                        <div className={styles.completedEmpty}>{t('taskmanager.archivedEmpty')}</div>
                      ) : (
                        archived.map((task) => (
                          <div key={task.id} className={`${styles.completedCard} ${task.status === 'done' ? styles.taskCardDone : styles.taskCardAbnormal}`}>
                            <div className={styles.completedCardHeader}>
                              <span
                                className={styles.completedCardName}
                                role="button"
                                tabIndex={0}
                                title={t('taskmanager.openTask')}
                                onClick={() => openTask(task)}
                                onKeyDown={(e) => {
                                  if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openTask(task) }
                                }}
                              >{task.title}</span>
                            </div>
                            <div className={styles.completedCardMeta}>
                              <span className={styles.taskActions}>
                                <button
                                  type="button"
                                  className={styles.taskActionBtn}
                                  onClick={() => handleRestoreArchived(task)}
                                  disabled={busy}
                                  title={t('trash.restore')}
                                >
                                  <ArchiveRestore size={13} />
                                </button>
                                <button
                                  type="button"
                                  className={`${styles.taskActionBtn} ${styles.taskActionDanger}`}
                                  onClick={() => handleDeleteArchived(task)}
                                  disabled={busy}
                                  title={t('trash.deleteNow')}
                                >
                                  <Trash2 size={13} />
                                </button>
                              </span>
                            </div>
                          </div>
                        ))
                      )}
                    </div>
                  </div>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
  )

  const chatCol = (
    // 右栏：AI 管理对话（默认隐藏，工具栏按钮展开，通过工具建/改/删任务、启停、调并发）。单任务在其独立会话页打开。
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
              tasks={def.tasks}
              onTaskChanged={reloadStatus}
            />
          </div>
        )}
      </div>
  )

  // 全部工作区模式：任务助手对话按工作区隔离，聚合视图不展示右栏，任务列表占满整行。
  if (isAll) return taskListCol
  if (!chatOpen) return taskListCol
  return (
    <SplitPane dir="row" storageKey="taskmanager" defaultFlexes={[1, 1]}>
      {taskListCol}
      {chatCol}
    </SplitPane>
  )
}
