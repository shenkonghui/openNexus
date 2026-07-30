import { useState, useEffect, useRef, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { listMessages } from '../api/sessions'
import { subscribeStream } from '../api/sse'
import type { Message } from '../types'
import type { TaskManagerTask } from '../api/taskmanager'
import MessageList from './MessageList'
import { AgentTerminalsProvider } from '../context/AgentTerminalsContext'
import { GitBranch, ExternalLink, Target } from 'lucide-react'
import styles from './TaskLiveWindow.module.css'

const ACTIVE_STATUSES = new Set(['queued', 'running'])
const GOAL_STATUSES = new Set(['active', 'evaluating', 'achieved', 'stopped'])

// 空闲时两次订阅之间的间隔：subscribeStream 在会话无活跃 prompt 时会立即返回 [DONE]，
// 通过短暂停顿避免对后端形成高频空转轮询。
const RESUBSCRIBE_DELAY_MS = 2500

interface Props {
  task: TaskManagerTask
  /** 点击标题/打开按钮：跳转任务会话页（未运行任务由父组件决定行为） */
  onOpen: (task: TaskManagerTask) => void
  /** 鼠标焦点是否落在本窗口（高亮边框，输入框 @task 引用已指向本任务） */
  focused?: boolean
  /** 鼠标按下窗口任意位置时上报焦点：父组件据此把助手输入框的 @task 引用切到本任务 */
  onFocus?: (task: TaskManagerTask) => void
}

/**
 * TaskLiveWindow：编排「多任务模式」网格中的单任务实时输出窗口。
 * 加载任务会话最近历史后，通过 /sessions/:id/stream 断点续传订阅实时输出；
 * 流结束（无活跃 prompt）后延时重订阅，保证任意来源（编排器启动、/task 直发、
 * 会话页操作）触发的新输出都能实时出现，无需用户手动刷新。
 */
export default function TaskLiveWindow({ task, onOpen, focused, onFocus }: Props) {
  const { t } = useTranslation()
  const [messages, setMessages] = useState<Message[]>([])
  const [loaded, setLoaded] = useState(false)

  // 与 TaskManagerChatPanel 相同的 rAF 批量 flush，避免每个 chunk 一次 re-render
  const pendingRef = useRef<Message[]>([])
  const flushRafRef = useRef<number | null>(null)

  const flushMessages = useCallback(() => {
    flushRafRef.current = null
    const batch = pendingRef.current
    if (batch.length === 0) return
    pendingRef.current = []
    setMessages((prev) => {
      const seen = new Set(prev.filter((m) => m.sequence > 0).map((m) => m.sequence))
      const additions: Message[] = []
      for (const msg of batch) {
        if (msg.sequence > 0) {
          if (seen.has(msg.sequence)) continue
          seen.add(msg.sequence)
        }
        additions.push(msg)
      }
      return additions.length > 0 ? [...prev, ...additions] : prev
    })
  }, [])

  const enqueueMessage = useCallback((msg: Message) => {
    pendingRef.current.push(msg)
    if (flushRafRef.current == null) {
      flushRafRef.current = requestAnimationFrame(flushMessages)
    }
  }, [flushMessages])

  useEffect(() => {
    const sid = task.db_session_id
    setMessages([])
    setLoaded(false)
    pendingRef.current = []
    if (!sid) { setLoaded(true); return }

    let alive = true
    const ac = new AbortController()
    let lastSeq = 0

    ;(async () => {
      // 1) 回读最近历史，并记录已见最大 sequence 作为断点续传起点
      try {
        const resp = await listMessages(sid)
        if (!alive) return
        const hist = resp.data.messages || []
        for (const m of hist) if (m.sequence > lastSeq) lastSeq = m.sequence
        setMessages(hist)
      } catch { /* 历史加载失败：仍尝试订阅实时流 */ }
      if (alive) setLoaded(true)

      // 2) 持续订阅循环：流结束后延时重连（新一轮 prompt 可能随时开始）
      while (alive) {
        await new Promise<void>((resolve) => {
          subscribeStream(
            sid,
            lastSeq,
            (msg) => {
              if (!alive) return
              if (msg.sequence > 0) {
                if (msg.sequence <= lastSeq) return
                lastSeq = msg.sequence
              }
              enqueueMessage(msg)
            },
            resolve,
            () => resolve(),
            { signal: ac.signal },
          )
        })
        if (!alive) break
        await new Promise((r) => setTimeout(r, RESUBSCRIBE_DELAY_MS))
      }
    })()

    return () => {
      alive = false
      ac.abort()
      if (flushRafRef.current != null) {
        cancelAnimationFrame(flushRafRef.current)
        flushRafRef.current = null
      }
    }
  }, [task.db_session_id, enqueueMessage])

  const isActive = ACTIVE_STATUSES.has(task.status)

  return (
    <div
      className={`${styles.window} ${focused ? styles.windowFocused : ''}`}
      onMouseDown={() => onFocus?.(task)}
    >
      <div className={styles.header}>
        <span
          className={styles.title}
          role="button"
          tabIndex={0}
          title={t('taskmanager.openTask')}
          onClick={() => onOpen(task)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(task) }
          }}
        >{task.title}</span>
        <span className={styles.headerRight}>
          {task.branch && (
            <span className={styles.branch} title={task.worktree_path || task.branch}>
              <GitBranch size={11} />
              <span className={styles.branchName}>{task.branch}</span>
            </span>
          )}
          {/* goal 徽标：未运行展示定义侧条件，运行后展示生命周期状态（与列表视图一致） */}
          {!task.goal && task.goal_condition && (
            <span className={styles.goal} title={task.goal_condition}>
              <Target size={11} />
              goal
            </span>
          )}
          {task.goal && GOAL_STATUSES.has(task.goal.status) && (
            <span
              className={`${styles.goal} ${styles[`goal_${task.goal.status}`] || ''}`}
              title={`${task.goal.condition}${task.goal.last_reason ? `\n${task.goal.last_reason}` : ''}`}
            >
              <Target size={11} />
              {t(`taskmanager.goal_${task.goal.status}`)}
              {task.goal.status === 'active' && task.goal.turns > 0 ? ` ×${task.goal.turns}` : ''}
            </span>
          )}
          <span className={`${styles.status} ${styles[`status_${task.status}`] || ''}`}>
            {t(`taskmanager.status_${task.status}`)}
          </span>
          <button
            type="button"
            className={styles.openBtn}
            onClick={() => onOpen(task)}
            title={t('taskmanager.openChat')}
          >
            <ExternalLink size={13} />
          </button>
        </span>
      </div>
      <div className={styles.body}>
        {!task.db_session_id ? (
          <div className={styles.placeholder}>{t('taskmanager.taskNotRun')}</div>
        ) : !loaded ? (
          <div className={styles.placeholder}>{t('common.loading')}</div>
        ) : messages.length === 0 ? (
          <div className={styles.placeholder}>{t('taskmanager.liveEmpty')}</div>
        ) : (
          // 包裹终端 provider：多任务窗口中也能在输出位置查看实时终端（纯展示，无收起/移动按钮）
          <AgentTerminalsProvider sessionId={task.db_session_id} plain>
            <MessageList
              messages={messages}
              loading={isActive}
              sessionId={task.db_session_id}
              cwd={task.worktree_path}
            />
          </AgentTerminalsProvider>
        )}
      </div>
    </div>
  )
}
