import { useState, useEffect, useMemo } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { formatTimeAgo } from '../utils/time'
import { sessionUrl, newTaskUrl, orchestrationUrl } from '../utils/routes'
import type { Session, ScheduledTask, AgentStatus } from '../types'
import { listScheduledTasks } from '../api/scheduledTasks'
import { listAgentStatus } from '../api/agents'
import { listSessions, listRunningSessions } from '../api/sessions'
import { getOrchestration, getOrchStatus, startOrchestration, type OrchestrationTask } from '../api/orchestration'
import { PanelLeftClose, Star, Pencil, X, Check, SquarePlus, FileText, Calendar, Settings, Zap, ScrollText, Loader2, CheckCircle2, XCircle, Clock3, CircleDashed, Network, MoreHorizontal, ChevronDown } from 'lucide-react'
import styles from './SessionSidebar.module.css'
import LogPanel from './LogPanel'
import NexusLogoIcon from './NexusLogoIcon'

interface SessionSidebarProps {
  sessions: Session[]
  workspaceId?: number
  currentId?: number
  onDelete?: (id: number) => void
  onRename?: (id: number, title: string) => void
  onCollapse?: () => void
  onNewScheduledTask?: () => void
  /** 由 AppLayout 统一渲染顶栏 Logo 时隐藏，避免重复 */
  hideLogo?: boolean
}

const STORAGE_KEY = 'opennexus.sidebar.collapsed'
const FAVS_KEY = 'opennexus.favorites'

function loadCollapsed(): { favorites: boolean; manual: boolean; scheduled: boolean; orchestration: boolean; footer: boolean } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      // footer 每次进入都收起，把纵向空间留给任务列表；其余分组仍记忆折叠状态
      return { favorites: false, manual: false, scheduled: false, orchestration: true, ...parsed, footer: true }
    }
  } catch { /* ignore */ }
  return { favorites: false, manual: false, scheduled: false, orchestration: true, footer: true }
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

// OrchStatusDot 编排任务的状态小圆点：running→蓝色旋转；queued→黄色；done→绿色；failed→红色；其余→灰色。
function OrchStatusDot({ status }: { status: string }) {
  const size = 13
  const cls = styles.taskStatusIcon
  if (status === 'running') return <Loader2 size={size} className={`${cls} ${styles.taskStatusIconSpin}`} />
  if (status === 'queued') return <Clock3 size={size} className={`${cls} ${styles.taskStatusIconSkipped}`} />
  if (status === 'done') return <CheckCircle2 size={size} className={`${cls} ${styles.taskStatusIconSuccess}`} />
  if (status === 'failed') return <XCircle size={size} className={`${cls} ${styles.taskStatusIconFailed}`} />
  return <CircleDashed size={size} className={`${cls} ${styles.taskStatusIconIdle}`} />
}

export default function SessionSidebar({ sessions, workspaceId, currentId, onDelete, onRename, onCollapse, onNewScheduledTask, hideLogo }: SessionSidebarProps) {
  const { t } = useTranslation()
  const [editingId, setEditingId] = useState<number | null>(null)
  const [showLogs, setShowLogs] = useState(false)
  // Agent 连接状态默认折叠，点 footer 按钮展开
  const [showAgentStatus, setShowAgentStatus] = useState(false)
  const [editTitle, setEditTitle] = useState('')
  const location = useLocation()
  const navigate = useNavigate()

  const [collapsed, setCollapsed] = useState(loadCollapsed)
  const [favorites, setFavorites] = useState<number[]>(loadFavorites)
  const [tasks, setTasks] = useState<ScheduledTask[]>([])
  const [agentStatuses, setAgentStatuses] = useState<AgentStatus[]>([])
  const [runningIds, setRunningIds] = useState<Set<number>>(() => new Set())
  const [orchTasks, setOrchTasks] = useState<OrchestrationTask[]>([])
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

  // 加载编排任务（侧边栏「任务编排」分组展开时显示）
  useEffect(() => {
    if (!workspaceId) { setOrchTasks([]); return }
    let alive = true
    getOrchestration(workspaceId)
      .then((r) => { if (alive) setOrchTasks(r.data.tasks || []) })
      .catch(() => { if (alive) setOrchTasks([]) })
    return () => { alive = false }
  }, [workspaceId, location.pathname])

  useEffect(() => {
    let alive = true
    const load = () => {
      listAgentStatus()
        .then((r) => { if (alive) setAgentStatuses(r.data.agents || []) })
        .catch(() => { if (alive) setAgentStatuses([]) })
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

  // 手动会话：编排会话(source=orchestration)不在此列，编排任务改由 orchTasks 以「编排-」前缀
  // 合并进「任务」分组展示（见下方 groupList），避免与已运行任务的会话重复。
  const manualSessions = sessions.filter((s) => !s.source || s.source === 'manual')
  // 编排管理会话（AI 编排面板对话）：source=orchestration 且无父会话（顶级）。
  // 作为「编排对话」记录展示在「任务」分组，点击回到编排页恢复其历史；
  // 编排子任务会话带 parent_session_id，不在此列（已由 orchTasks 以「编排-」前缀展示）。
  const orchSessions = sessions.filter((s) => s.source === 'orchestration' && !s.parent_session_id)
  const favoriteSessions = useMemo(
    () => sessions.filter((s) => favorites.includes(s.id)),
    [sessions, favorites],
  )
  const recentTask = [...tasks]
    .filter((t) => t.last_run_at)
    .sort((a, b) => (a.last_run_at! < b.last_run_at! ? 1 : -1))[0]

  function toggleGroup(group: 'favorites' | 'manual' | 'scheduled' | 'orchestration' | 'footer') {
    setCollapsed((prev) => {
      const nextCollapsed = !prev[group]
      // 收起 footer 时一并隐藏 Agent 状态，避免只剩状态条
      if (group === 'footer' && nextCollapsed) setShowAgentStatus(false)
      return { ...prev, [group]: nextCollapsed }
    })
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

  // 点击编排任务：
  // - 已有会话（db_session_id）：直接打开该会话。
  // - 未运行任务：通过编排引擎启动（引擎会自动创建 git worktree 并在其中运行，
  //   把 worktree 的 cwd 带入会话），随后轮询任务状态，拿到 db_session_id 后跳转到会话。
  //   引擎对已在运行的任务是幂等的（跳过），故 pending/queued 均可安全触发。
  async function openOrchTask(task: OrchestrationTask) {
    if (task.db_session_id) {
      navigate(sessionUrl(task.db_session_id, workspaceId), { state: { taskMode: 'coding' } })
      return
    }
    if (!workspaceId || startingTaskId) return
    setStartingTaskId(task.id)
    try {
      await startOrchestration(workspaceId, task.id)
      const deadline = Date.now() + 20000
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 800))
        const r = await getOrchStatus(workspaceId)
        const list = r.data.tasks || []
        setOrchTasks(list) // 顺带刷新侧边栏状态
        const fresh = list.find((x) => x.id === task.id)
        if (fresh?.db_session_id) {
          navigate(sessionUrl(fresh.db_session_id, workspaceId), { state: { taskMode: 'coding' } })
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
              {orchSessions.map((session) => {
                const goOrch = () => navigate(orchestrationUrl(session.workspace_id ?? workspaceId), { state: { taskMode: 'orchestration', orchSessionId: session.id } })
                return (
                  <div key={`orchsess-${session.id}`} className={styles.item}>
                    <div
                      className={styles.itemLink}
                      role="button"
                      tabIndex={0}
                      title={t('orchestration.openConversation')}
                      style={{ cursor: 'pointer' }}
                      onClick={goOrch}
                      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); goOrch() } }}
                    >
                      <div className={styles.itemRow}>
                        <span className={styles.itemTitle}>
                          <Network size={13} className={styles.taskStatusIcon} style={{ marginRight: 2 }} />
                          {session.title || t('orchestration.aiTitle')}
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
              {orchTasks.map((task) => (
                <div key={`orch-${task.id}`} className={styles.item}>
                  <div
                    className={styles.itemLink}
                    role="button"
                    tabIndex={0}
                    title={t('orchestration.openTask')}
                    style={{ cursor: 'pointer' }}
                    onClick={() => openOrchTask(task)}
                    onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openOrchTask(task) } }}
                  >
                    <div className={styles.itemRow}>
                      <span className={styles.itemTitle}>
                        <OrchStatusDot status={startingTaskId === task.id ? 'running' : task.status} />
                        {t('orchestration.taskPrefix')}{task.title}
                      </span>
                    </div>
                  </div>
                </div>
              ))}
              {orchTasks.length === 0 && orchSessions.length === 0 && manualSessions.length === 0 ? (
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
                  onClick={() => navigate(sessionUrl(recentTask.db_session_id, recentTask.workspace_id))}
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

      </div>

      {showAgentStatus && agentStatuses.length > 0 && (
        <div className={styles.agentStatus}>
          {agentStatuses.map((s) => {
            const statusLabel = s.status === 'connected' ? t('status.connected') : s.status === 'connecting' ? t('status.connecting') : t('status.disconnected')
            const dotClass = s.status === 'connected' ? styles.agentDotOn : s.status === 'connecting' ? styles.agentDotConnecting : styles.agentDotOff
            const statusClass = s.status === 'connected' ? styles.agentStatusConnected : s.status === 'connecting' ? styles.agentStatusConnecting : styles.agentStatusDisconnected
            return (
              <div key={s.agent_type} className={styles.agentStatusItem}>
                <span className={`${styles.agentDot} ${dotClass}`} />
                <span className={styles.agentName}>{s.agent_type}</span>
                <span className={`${styles.agentStatusText} ${statusClass}`}>{statusLabel}</span>
                <span className={styles.agentCount}>{s.active_count}</span>
              </div>
            )
          })}
        </div>
      )}

      <div className={styles.footer}>
        <div className={styles.footerBar}>
          {!collapsed.footer && (
            <>
              <Link
                to={orchestrationUrl(workspaceId)}
                className={`${styles.footerIcon} ${location.pathname.endsWith('/orchestration') ? styles.footerIconActive : ''}`}
                title={t('nav.orchestration')}
              >
                <Network size={15} />
              </Link>
              <Link
                to="/settings"
                className={`${styles.footerIcon} ${location.pathname === '/settings' ? styles.footerIconActive : ''}`}
                title={t('common.settings')}
              >
                <Settings size={15} />
              </Link>
              <button
                type="button"
                className={`${styles.footerIcon} ${showLogs ? styles.footerIconActive : ''}`}
                title={t('log.openLogs')}
                onClick={() => setShowLogs((v) => !v)}
              >
                <ScrollText size={15} />
              </button>
              <button
                type="button"
                className={`${styles.footerIcon} ${showAgentStatus ? styles.footerIconActive : ''}`}
                title={t('status.agentStatus')}
                onClick={() => setShowAgentStatus((v) => !v)}
              >
                <Zap size={15} />
              </button>
            </>
          )}
          <button
            type="button"
            className={styles.footerIcon}
            title={t('common.more')}
            onClick={() => toggleGroup('footer')}
          >
            {collapsed.footer ? <MoreHorizontal size={15} /> : <ChevronDown size={15} />}
          </button>
        </div>
      </div>

      {showLogs && <LogPanel onClose={() => setShowLogs(false)} />}
    </div>
  )
}
