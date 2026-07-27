import { useState, useEffect, useMemo } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { formatTimeAgo } from '../utils/time'
import { sessionUrl, newTaskUrl, taskManagerUrl } from '../utils/routes'
import type { Session, ScheduledTask } from '../types'
import { listScheduledTasks } from '../api/scheduledTasks'
import { listSessions, listRunningSessions } from '../api/sessions'
import { getTaskManager, getTaskStatus, startTaskManager, type TaskManagerTask } from '../api/taskmanager'
import { PanelLeftClose, Star, Pencil, X, Check, SquarePlus, FileText, Calendar, Settings, Zap, Loader2, CheckCircle2, XCircle, Clock3, CircleDashed, Network } from 'lucide-react'
import styles from './SessionSidebar.module.css'
import NexusLogoIcon from './NexusLogoIcon'
import UserMenu from './UserMenu'

interface SessionSidebarProps {
  sessions: Session[]
  workspaceId?: number
  currentId?: number
  onDelete?: (id: number) => void
  onRename?: (id: number, title: string) => void
  onCollapse?: () => void
  onNewScheduledTask?: () => void
  /** 工作区相关回调由 AppLayout 顶部工作区选择器消费，SessionSidebar 仅作类型透传 */
  onWorkspaceChange?: (id: number) => void
  onWorkspaceRefresh?: () => void
  /** 由 AppLayout 统一渲染顶栏 Logo 时隐藏，避免重复 */
  hideLogo?: boolean
  /** 全局 YOLO 开关状态与回调（由 AppLayout 注入） */
  yoloEnabled?: boolean
  yoloSaving?: boolean
  onToggleYolo?: () => void
}

const STORAGE_KEY = 'opennexus.sidebar.collapsed'
const FAVS_KEY = 'opennexus.favorites'

function loadCollapsed(): { favorites: boolean; manual: boolean; scheduled: boolean; taskmanager: boolean } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      return { favorites: false, manual: false, scheduled: false, taskmanager: true, ...parsed }
    }
  } catch { /* ignore */ }
  return { favorites: false, manual: false, scheduled: false, taskmanager: true }
}

function loadFavorites(): number[] {
  try {
    const raw = localStorage.getItem(FAVS_KEY)
    if (raw) return JSON.parse(raw)
  } catch { /* ignore */ }
  return []
}

function saveFavorites(ids: number[]) {
  try { localStorage.setItem(FAVS_KEY, JSON.stringify(ids)) } catch { /* ignore */ }
}

function TaskStatusIcon({ status }: { status: string }) {
  const size = 13
  const cls = styles.taskStatusIcon
  if (status === 'running') return <Loader2 size={size} className={`${cls} ${styles.taskStatusIconSpin}`} />
  if (status === 'success') return <CheckCircle2 size={size} className={`${cls} ${styles.taskStatusIconSuccess}`} />
  if (status === 'failed') return <XCircle size={size} className={`${cls} ${styles.taskStatusIconFailed}`} />
  if (status === 'skipped') return <Clock3 size={size} className={`${cls} ${styles.taskStatusIconSkipped}`} />
  return <CircleDashed size={size} className={`${cls} ${styles.taskStatusIconIdle}`} />
}

// SessionStatusIcon 普通会话的实时运行状态图标：
// 运行中(agent 正在生成)→ 蓝色旋转；error → 红色叉；否则 → 灰色对勾(完成/空闲)。
function SessionStatusIcon({ running, status }: { running: boolean; status: string }) {
  const size = 13
  const cls = styles.taskStatusIcon
  if (running) return <Loader2 size={size} className={`${cls} ${styles.taskStatusIconSpin}`} />
  if (status === 'error') return <XCircle size={size} className={`${cls} ${styles.taskStatusIconFailed}`} />
  return <CheckCircle2 size={size} className={`${cls} ${styles.taskStatusIconIdle}`} />
}

// TaskStatusDot 任务管理任务的状态小圆点：running→蓝色旋转；queued→黄色；done→绿色；failed→红色；其余→灰色。
function TaskStatusDot({ status }: { status: string }) {
  const size = 13
  const cls = styles.taskStatusIcon
  if (status === 'running') return <Loader2 size={size} className={`${cls} ${styles.taskStatusIconSpin}`} />
  if (status === 'queued') return <Clock3 size={size} className={`${cls} ${styles.taskStatusIconSkipped}`} />
  if (status === 'done') return <CheckCircle2 size={size} className={`${cls} ${styles.taskStatusIconSuccess}`} />
  if (status === 'failed') return <XCircle size={size} className={`${cls} ${styles.taskStatusIconFailed}`} />
  return <CircleDashed size={size} className={`${cls} ${styles.taskStatusIconIdle}`} />
}

export default function SessionSidebar({ sessions, workspaceId, currentId, onDelete, onRename, onCollapse, onNewScheduledTask, hideLogo, yoloEnabled, yoloSaving, onToggleYolo }: SessionSidebarProps) {
  const { t } = useTranslation()
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editTitle, setEditTitle] = useState('')
  const location = useLocation()
  const navigate = useNavigate()

  const [collapsed, setCollapsed] = useState(loadCollapsed)
  const [favorites, setFavorites] = useState<number[]>(loadFavorites)
  const [tasks, setTasks] = useState<ScheduledTask[]>([])
  const [runningIds, setRunningIds] = useState<Set<number>>(() => new Set())
  const [tmTasks, setOrchTasks] = useState<TaskManagerTask[]>([])
  // 正在通过编排引擎启动的任务 id（点击未运行任务时置位），用于展示运行中状态并避免重复点击。
  const [startingTaskId, setStartingTaskId] = useState<string | null>(null)

  useEffect(() => {
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(collapsed)) } catch { /* ignore */ }
  }, [collapsed])

  useEffect(() => {
    let alive = true
    listScheduledTasks(workspaceId || undefined)
      .then((r) => { if (alive) setTasks(r.data.tasks || []) })
      .catch(() => { if (alive) setTasks([]) })
    return () => { alive = false }
  }, [location.pathname, workspaceId])

  // 加载任务管理任务（侧边栏「任务管理」分组展开时显示）。
  // 依赖 sessions：删除/新建会话会同步增删 tasks.json 登记条目，需重新拉取保持一致。
  useEffect(() => {
    if (!workspaceId) { setOrchTasks([]); return }
    let alive = true
    getTaskManager(workspaceId)
      .then((r) => { if (alive) setOrchTasks(r.data.tasks || []) })
      .catch(() => { if (alive) setOrchTasks([]) })
    return () => { alive = false }
  }, [workspaceId, location.pathname, sessions])

  useEffect(() => {
    let alive = true
    const load = () => {
      listRunningSessions()
        .then((r) => { if (alive) setRunningIds(new Set(r.data.db_session_ids || [])) })
        .catch(() => {})
    }
    load()
    const timer = setInterval(load, 3000)
    return () => { alive = false; clearInterval(timer) }
  }, [])

  useEffect(() => {
    let alive = true
    listSessions()
      .then((r) => {
        if (!alive) return
        const ids = new Set((r.data.sessions || []).map((s) => s.id))
        setFavorites((prev) => {
          const next = prev.filter((id) => ids.has(id))
          if (next.length !== prev.length) saveFavorites(next)
          return next.length !== prev.length ? next : prev
        })
      })
      .catch(() => {})
    return () => { alive = false }
  }, [])

  // 手动会话：任务管理会话(source=orchestration)不在此列，任务管理任务改由 tmTasks 以「任务管理-」前缀
  // 合并进「任务」分组展示（见下方 groupList），避免与已运行任务的会话重复。
  const manualSessions = sessions.filter((s) => !s.source || s.source === 'manual')
  // 任务管理会话（AI 编排面板对话）：source=orchestration 且无父会话（顶级）。
  // 作为「编排对话」记录展示在「任务」分组，点击回到编排页恢复其历史；
  // 任务管理子任务会话带 parent_session_id，不在此列（已由 tmTasks 以「任务管理-」前缀展示）。
  // 每个工作区只展示最近一条：编排定义（tasks.json）本就按工作区唯一，历史遗留的多条
  // 任务管理会话若全部列出会造成重复入口。
  const tmSessions = useMemo(() => {
    const tops = sessions
      .filter((s) => s.source === 'orchestration' && !s.parent_session_id)
      .sort((a, b) => (a.created_at < b.created_at ? 1 : -1))
    const seen = new Set<number>()
    return tops.filter((s) => {
      const wsID = s.workspace_id ?? 0
      if (seen.has(wsID)) return false
      seen.add(wsID)
      return true
    })
  }, [sessions])
  const favoriteSessions = useMemo(
    () => sessions.filter((s) => favorites.includes(s.id)),
    [sessions, favorites],
  )
  // 只展示已启动的任务管理任务：pending（仅在编排页定义、尚未入队）不占用任务列表，
  // 启动后（queued/running/done/... 或已生成会话）才作为实际任务出现。
  // 手动新建会话首次发送时会被后端登记进 tasks.json（db_session_id 指向该会话），
  // 这类任务与 manualSessions 是同一对话，按 db_session_id 去重，避免双重条目。
  const startedTMTasks = useMemo(() => {
    const manualIds = new Set(
      sessions.filter((s) => !s.source || s.source === 'manual').map((s) => s.id),
    )
    return tmTasks.filter(
      (t) => (t.status !== 'pending' || !!t.db_session_id)
        && !(t.db_session_id && manualIds.has(t.db_session_id)),
    )
  }, [tmTasks, sessions])
  const recentTask = [...tasks]
    .filter((t) => t.last_run_at)
    .sort((a, b) => (a.last_run_at! < b.last_run_at! ? 1 : -1))[0]

  function toggleGroup(group: 'favorites' | 'manual' | 'scheduled' | 'taskmanager') {
    setCollapsed((prev) => ({ ...prev, [group]: !prev[group] }))
  }

  function toggleFavorite(id: number, e: React.MouseEvent) {
    e.preventDefault(); e.stopPropagation()
    setFavorites((prev) => {
      const next = prev.includes(id) ? prev.filter((fid) => fid !== id) : [...prev, id]
      saveFavorites(next)
      return next
    })
  }

  function handleNewScheduledTask(e: React.MouseEvent | React.KeyboardEvent) {
    e.stopPropagation()
    if (onNewScheduledTask) {
      onNewScheduledTask()
      return
    }
    navigate('/scheduled-tasks', { state: { openCreate: true } })
  }

  // 点击任务管理任务：
  // - 已有会话（db_session_id）：直接打开该会话。
  // - 未运行任务：通过编排引擎启动（引擎会自动创建 git worktree 并在其中运行，
  //   把 worktree 的 cwd 带入会话），随后轮询任务状态，拿到 db_session_id 后跳转到会话。
  //   引擎对已在运行的任务是幂等的（跳过），故 pending/queued 均可安全触发。
  async function openTMTask(task: TaskManagerTask) {
    if (task.db_session_id) {
      navigate(sessionUrl(task.db_session_id, workspaceId))
      return
    }
    if (!workspaceId || startingTaskId) return
    setStartingTaskId(task.id)
    try {
      await startTaskManager(workspaceId, task.id)
      const deadline = Date.now() + 20000
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 800))
        const r = await getTaskStatus(workspaceId)
        const list = r.data.tasks || []
        setOrchTasks(list) // 顺带刷新侧边栏状态
        const fresh = list.find((x) => x.id === task.id)
        if (fresh?.db_session_id) {
          navigate(sessionUrl(fresh.db_session_id, workspaceId))
          return
        }
        if (fresh?.status === 'failed') break // 启动失败：停止轮询，状态点会显示失败
      }
    } catch { /* ignore：状态点仍会反映实际结果 */ }
    finally {
      setStartingTaskId(null)
    }
  }

  return (
    <div className={styles.sidebar}>
      {!hideLogo && (
        <Link to={newTaskUrl(workspaceId)} className={styles.logo} title={t('session.newSession')}>
          <NexusLogoIcon size={22} />
          {onCollapse && (
            <button
              type="button"
              className={styles.collapseBtn}
              onClick={(e) => { e.preventDefault(); e.stopPropagation(); onCollapse() }}
              title={t('common.close') + ' (⌘B)'}
            >
              <PanelLeftClose size={16} />
            </button>
          )}
        </Link>
      )}

      <div className={styles.groups}>
        {/* 收藏任务 */}
        <div className={styles.group}>
          <button type="button" className={styles.groupHeader} onClick={() => toggleGroup('favorites')}>
            <span className={styles.groupTitle}><Star size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />{t('session.favGroup')}</span>

          </button>
          {!collapsed.favorites && (
            <div className={styles.groupList}>
              {favoriteSessions.length === 0 ? (
                <p className={styles.empty}>{t('session.noFavorites')}</p>
              ) : (
                favoriteSessions.map((session) => (
                  <div key={session.id} className={`${styles.item} ${currentId === session.id ? styles.itemActive : ''}`}>
                    <Link to={sessionUrl(session.id, session.workspace_id)} className={styles.itemLink}>
                      <div className={styles.itemRow}>
                        <span className={styles.itemTitle}>
                          <SessionStatusIcon running={runningIds.has(session.id)} status={session.status} />
                          {session.title || session.agent_type}
                        </span>
                        <span className={styles.itemTime}>{formatTimeAgo(session.created_at, t)}</span>
                      </div>
                    </Link>
                    <div className={styles.itemActions}>
                      <button type="button" className={styles.favBtnActive}
                        title={t('session.favorited')}
                        onClick={(e) => toggleFavorite(session.id, e)}
                      ><Star size={13} fill="currentColor" strokeWidth={0} /></button>
                    </div>
                  </div>
                ))
              )}
            </div>
          )}
        </div>

        {/* 任务管理入口：置于「任务」分组上方（原在左下角 footer） */}
        <div className={styles.group}>
          <Link
            to={taskManagerUrl(workspaceId)}
            className={`${styles.groupHeader} ${location.pathname.endsWith('/taskmanager') ? styles.itemActive : ''}`}
          >
            <span className={styles.groupTitle}>
              <Network size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />
              {t('nav.taskmanager')}
            </span>
          </Link>
        </div>

        <div className={styles.group}>
          <button type="button" className={styles.groupHeader} onClick={() => toggleGroup('manual')}>
            <span className={styles.groupTitle}><FileText size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />{t('session.title')}</span>

            <span
              className={styles.addBtn} role="button" tabIndex={0}
              title={t('session.newSession')}
              onClick={(e) => { e.stopPropagation(); navigate(newTaskUrl(workspaceId)) }}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.stopPropagation(); navigate(newTaskUrl(workspaceId)) } }}
            ><SquarePlus size={14} /></span>
          </button>
          {!collapsed.manual && (
            <div className={styles.groupList}>
              {tmSessions.map((session) => {
                                const goTM = () => navigate(taskManagerUrl(session.workspace_id ?? workspaceId), { state: { tmSessionId: session.id } })
                return (
                  <div key={`orchsess-${session.id}`} className={styles.item}>
                    <div
                      className={styles.itemLink}
                      role="button"
                      tabIndex={0}
                      title={t('taskmanager.openConversation')}
                      style={{ cursor: 'pointer' }}
                      onClick={goTM}
                      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goTM() } }}
                    >
                      <div className={styles.itemRow}>
                        <span className={styles.itemTitle}>
                          <Network size={13} className={styles.taskStatusIcon} style={{ marginRight: 2 }} />
                          {session.title || t('taskmanager.aiTitle')}
                        </span>
                        <span className={styles.itemTime}>{formatTimeAgo(session.created_at, t)}</span>
                      </div>
                    </div>
                    {onDelete && (
                      <div className={styles.itemActions}>
                        <button type="button" className={styles.deleteBtn}
                          title={t('common.delete')} aria-label={t('common.delete')}
                          onClick={(e) => {
                            e.preventDefault(); e.stopPropagation()
                            if (window.confirm(t('session.deleteConfirm'))) onDelete(session.id)
                          }}
                        ><X size={13} /></button>
                      </div>
                    )}
                  </div>
                )
              })}
              {startedTMTasks.map((task) => (
                <div key={`orch-${task.id}`} className={styles.item}>
                  <div
                    className={styles.itemLink}
                    role="button"
                    tabIndex={0}
                    title={t('taskmanager.openTask')}
                    style={{ cursor: 'pointer' }}
                    onClick={() => openTMTask(task)}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openTMTask(task) } }}
                  >
                    <div className={styles.itemRow}>
                      <span className={styles.itemTitle}>
                        <TaskStatusDot status={startingTaskId === task.id ? 'running' : task.status} />
                        {t('taskmanager.taskPrefix')}{task.title}
                      </span>
                    </div>
                  </div>
                </div>
              ))}
              {startedTMTasks.length === 0 && tmSessions.length === 0 && manualSessions.length === 0 ? (
                <p className={styles.empty}>{t('session.noSessions')}</p>
              ) : (
                manualSessions.map((session) => (
                  <div key={session.id} className={`${styles.item} ${currentId === session.id ? styles.itemActive : ''}`}>
                    {editingId === session.id ? (
                      <div className={styles.editRow}>
                        <input className={styles.editInput} value={editTitle}
                          onChange={(e) => setEditTitle(e.target.value)}
                          onKeyDown={(e) => {
                            if (e.key === 'Enter') { const t = editTitle.trim(); if (t && onRename) onRename(session.id, t); setEditingId(null) }
                            else if (e.key === 'Escape') setEditingId(null)
                          }} autoFocus />
                        <button type="button" className={styles.editOkBtn}
                          onClick={() => { const t = editTitle.trim(); if (t && onRename) onRename(session.id, t); setEditingId(null) }}
                        ><Check size={13} /></button>
                      </div>
                    ) : (
                      <>
                        <Link to={sessionUrl(session.id, session.workspace_id)} className={styles.itemLink}>
                          <div className={styles.itemRow}>
                            <span className={styles.itemTitle}>
                              <SessionStatusIcon running={runningIds.has(session.id)} status={session.status} />
                              {session.title || session.agent_type}
                            </span>
                            <span className={styles.itemTime}>{formatTimeAgo(session.created_at, t)}</span>
                          </div>
                        </Link>
                        <div className={styles.itemActions}>
                          <button type="button" className={favorites.includes(session.id) ? styles.favBtnActive : styles.favBtn}
                            title={favorites.includes(session.id) ? t('session.favorited') : t('session.unfavorited')}
                            onClick={(e) => toggleFavorite(session.id, e)}
                          >{favorites.includes(session.id) ? <Star size={13} fill="currentColor" strokeWidth={0} /> : <Star size={13} />}</button>
                          {onRename && (
                            <button type="button" className={styles.renameBtn}
                              title={t('common.rename')} aria-label={t('common.rename')}
                              onClick={(e) => { e.preventDefault(); e.stopPropagation(); setEditTitle(session.title || session.agent_type); setEditingId(session.id) }}
                            ><Pencil size={13} /></button>
                          )}
                          {onDelete && (
                            <button type="button" className={styles.deleteBtn}
                              title={t('common.delete')} aria-label={t('common.delete')}
                              onClick={(e) => {
                                e.preventDefault(); e.stopPropagation()
                                if (window.confirm(t('session.deleteConfirm'))) {
                                  setFavorites((prev) => {
                                    const next = prev.filter((fid) => fid !== session.id)
                                    saveFavorites(next)
                                    return next
                                  })
                                  onDelete(session.id)
                                }
                              }}
                            ><X size={13} /></button>
                          )}
                        </div>
                      </>
                    )}
                  </div>
                ))
              )}
            </div>
          )}
        </div>

        <div className={styles.group}>
          <button type="button" className={styles.groupHeader} onClick={() => toggleGroup('scheduled')}>
            <span className={styles.groupTitle}><Calendar size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />{t('nav.scheduledTasks')}</span>

            <span
              className={styles.addBtn} role="button" tabIndex={0}
              title={t('scheduledTask.newTask')}
              onClick={handleNewScheduledTask}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') handleNewScheduledTask(e) }}
            ><SquarePlus size={14} /></span>
          </button>
          {!collapsed.scheduled && (
            <div className={styles.groupList}>
              {recentTask && recentTask.db_session_id ? (
                <button type="button" className={styles.recentEntry}
                  onClick={() => navigate(sessionUrl(recentTask.db_session_id!, recentTask.workspace_id))}
                  title={`${t('nav.recentRun')}: ${recentTask.name}`}
                >
                  <span className={styles.recentIcon}><Zap size={13} /></span>
                  <span className={styles.recentText}>{t('nav.recentRun')} · {recentTask.name}</span>
                </button>
              ) : null}
              {tasks.length === 0 ? (
                <p className={styles.empty}>{t('scheduledTask.noTasks')}</p>
              ) : (
                tasks.map((task) => (
                  <div key={task.id} className={`${styles.item} ${currentId === task.db_session_id ? styles.itemActive : ''}`}>
                    <button type="button" className={styles.itemLink}
                      onClick={() => task.db_session_id ? navigate(sessionUrl(task.db_session_id, task.workspace_id)) : undefined}
                      disabled={!task.db_session_id}
                      style={!task.db_session_id ? { cursor: 'default', opacity: 0.6 } : undefined}
                    >
                      <div className={styles.itemRow}>
                        <span className={styles.itemTitle}>
                          <TaskStatusIcon status={task.last_status} />
                          {task.name}
                        </span>
                        {task.last_run_at && (
                          <span className={styles.itemTime}>{formatTimeAgo(task.last_run_at, t)}</span>
                        )}
                      </div>
                    </button>
                  </div>
                ))
              )}
            </div>
          )}
        </div>

        <div className={styles.group}>
          <Link
            to="/notes"
            className={`${styles.groupHeader} ${location.pathname === '/notes' ? styles.itemActive : ''}`}
          >
            <span className={styles.groupTitle}>
              <FileText size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />
              {t('nav.notes')}
            </span>
          </Link>
        </div>

        <div className={styles.group}>
          <Link
            to="/mcp-gateway"
            className={`${styles.groupHeader} ${location.pathname === '/mcp-gateway' ? styles.itemActive : ''}`}
          >
            <span className={styles.groupTitle}>
              <Network size={13} style={{ marginRight: 4, verticalAlign: '-2px' }} />
              {t('nav.mcpGateway')}
            </span>
          </Link>
        </div>

      </div>

      {/* 左下角：用户信息 + YOLO + 设置，全部并为一行 */}
      <div className={styles.footer}>
        <div className={styles.footerBar}>
          <UserMenu variant="sidebar" />
          <div className={styles.footerActions}>

            {onToggleYolo && (
              <button
                type="button"
                className={`${styles.footerIcon} ${yoloEnabled ? styles.footerIconYolo : ''}`}
                onClick={onToggleYolo}
                disabled={yoloSaving}
                title={t('sidebar.yoloHint')}
              >
                <Zap size={15} />
              </button>
            )}
            <button
              type="button"
              className={`${styles.footerIcon} ${new URLSearchParams(location.search).has('settings') ? styles.footerIconActive : ''}`}
              title={t('common.settings')}
              onClick={() => {
                // 在当前页面上叠加设置弹窗（AppLayout 根据 URL 参数渲染）
                const params = new URLSearchParams(location.search)
                params.set('settings', '1')
                navigate({ pathname: location.pathname, search: params.toString() })
              }}
            >
              <Settings size={15} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
