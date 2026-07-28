import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Terminal, FileText, Pencil, Search, Globe, Wrench, Target, Loader2, CheckCircle2, XCircle, CircleDashed, History } from 'lucide-react'
import { listToolCalls, type ToolCallRecord } from '../api/toolCalls'
import { formatTimeAgo } from '../utils/time'
import styles from './SessionRecordsPanel.module.css'

const PAGE_SIZE = 50

// kind 过滤项：常见 ACP ToolKind + goal（goal 循环事件由后端以 kind=goal 落库）
const KIND_FILTERS = ['', 'execute', 'read', 'edit', 'search', 'fetch', 'goal'] as const

interface SessionRecordsPanelProps {
  sessionId: number
  /** 变化时防抖自动刷新（ChatPage 传入消息条数，agent 产生新工具调用后列表跟进） */
  refreshSignal?: number
}

function KindIcon({ kind }: { kind: string }) {
  const size = 13
  if (kind === 'execute') return <Terminal size={size} />
  if (kind === 'read') return <FileText size={size} />
  if (kind === 'edit' || kind === 'delete' || kind === 'move') return <Pencil size={size} />
  if (kind === 'search') return <Search size={size} />
  if (kind === 'fetch') return <Globe size={size} />
  if (kind === 'goal') return <Target size={size} />
  return <Wrench size={size} />
}

function StatusIcon({ status }: { status: string }) {
  const size = 13
  if (status === 'completed') return <CheckCircle2 size={size} className={styles.stDone} />
  if (status === 'failed') return <XCircle size={size} className={styles.stFailed} />
  if (status === 'in_progress') return <Loader2 size={size} className={`${styles.stRunning} ${styles.spin}`} />
  return <CircleDashed size={size} className={styles.stPending} />
}

/**
 * 「记录」面板：当前会话的工具调用记录（shell 命令 / 文件读写 / MCP 等），
 * 含 goal 循环事件（设定 / 评估 / 续轮 / 终止）。数据来自 GET /tool-calls?session_id=。
 */
export default function SessionRecordsPanel({ sessionId, refreshSignal }: SessionRecordsPanelProps) {
  const { t } = useTranslation()
  const [items, setItems] = useState<ToolCallRecord[]>([])
  const [total, setTotal] = useState(0)
  const [kind, setKind] = useState('')
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async (k: string, offset: number, append: boolean) => {
    if (append) setLoadingMore(true)
    else setLoading(true)
    try {
      const resp = await listToolCalls({ kind: k || undefined, sessionId, limit: PAGE_SIZE, offset })
      setItems((prev) => (append ? [...prev, ...(resp.data.items || [])] : resp.data.items || []))
      setTotal(resp.data.total)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [sessionId])

  useEffect(() => {
    load(kind, 0, false)
  }, [kind, load])

  // 会话流式输出期间消息持续到达：防抖 2s 静默刷新（不闪 loading）
  const refreshTimer = useRef<ReturnType<typeof setTimeout>>()
  useEffect(() => {
    if (refreshSignal === undefined) return
    if (refreshTimer.current) clearTimeout(refreshTimer.current)
    refreshTimer.current = setTimeout(() => {
      listToolCalls({ kind: kind || undefined, sessionId, limit: PAGE_SIZE, offset: 0 })
        .then((resp) => {
          setItems(resp.data.items || [])
          setTotal(resp.data.total)
        })
        .catch(() => { /* 静默失败，保留旧列表 */ })
    }, 2000)
    return () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshSignal])

  const hasMore = items.length < total

  return (
    <div className={styles.panel}>
      <div className={styles.toolbar}>
        <div className={styles.filters}>
          {KIND_FILTERS.map((k) => (
            <button
              key={k || 'all'}
              type="button"
              className={`${styles.filterBtn} ${kind === k ? styles.filterBtnActive : ''}`}
              onClick={() => setKind(k)}
            >
              {k === '' ? t('toolCalls.kindAll') : t(`toolCalls.kind_${k}`)}
            </button>
          ))}
        </div>
        <span className={styles.totalHint}>{t('toolCalls.total', { count: total })}</span>
        <button
          type="button"
          className={styles.iconBtn}
          onClick={() => load(kind, 0, false)}
          disabled={loading}
          title={t('toolCalls.refresh')}
        >
          <RefreshCw size={13} className={loading ? styles.spin : undefined} />
        </button>
      </div>

      {error && <div className={styles.error}>{error}</div>}

      <div className={styles.body}>
        {loading && items.length === 0 ? (
          <div className={styles.empty}>{t('toolCalls.loading')}</div>
        ) : items.length === 0 ? (
          <div className={styles.empty}>
            <History size={20} style={{ opacity: 0.4, marginBottom: 8 }} />
            <div>{t('toolCalls.empty')}</div>
          </div>
        ) : (
          <>
            {items.map((it) => (
              <div key={it.id} className={styles.entry}>
                <span className={styles.kindIcon} title={it.kind || 'other'}>
                  <KindIcon kind={it.kind} />
                </span>
                <div className={styles.entryMain}>
                  <div className={styles.entryTitle}>
                    {it.kind === 'execute' && (it.command || it.title) ? (
                      <code className={styles.command} title={it.command || it.title}>{it.command || it.title}</code>
                    ) : (
                      <span className={styles.titleText} title={it.title}>{it.title || it.tool_call_id}</span>
                    )}
                  </div>
                  <div className={styles.entrySub}>
                    <span title={new Date(it.started_at).toLocaleString()}>{formatTimeAgo(it.started_at, t)}</span>
                    {it.kind === 'execute' && it.cwd && <span className={styles.cwd} title={it.cwd}>{it.cwd}</span>}
                    {it.exit_code != null && (
                      <span className={it.exit_code === 0 ? styles.exitOk : styles.exitBad}>exit {it.exit_code}</span>
                    )}
                  </div>
                </div>
                <span className={styles.statusIcon} title={it.status}>
                  <StatusIcon status={it.status} />
                </span>
              </div>
            ))}
            {hasMore && (
              <button
                type="button"
                className={styles.loadMoreBtn}
                onClick={() => load(kind, items.length, true)}
                disabled={loadingMore}
              >
                {loadingMore ? t('toolCalls.loading') : t('toolCalls.loadMore')}
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}
