import { useState, useEffect, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { listConversationRecords, type ConversationRecord } from '../api/conversations'
import { listSessions } from '../api/sessions'
import type { Session } from '../types'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import { sessionUrl } from '../utils/routes'
import { formatTimeAgo } from '../utils/time'
import { RefreshCw, User, Bot } from 'lucide-react'
import styles from './ConversationsPage.module.css'

const PAGE_SIZE = 50

// 超过该行数的内容折叠展示，点击「展开」查看全文
const COLLAPSE_THRESHOLD = 6

function RecordContent({ content }: { content: string }) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const lines = content.split('\n')
  const collapsible = lines.length > COLLAPSE_THRESHOLD || content.length > 600
  const shown = expanded || !collapsible
    ? content
    : lines.slice(0, COLLAPSE_THRESHOLD).join('\n').slice(0, 600)
  return (
    <div className={styles.content}>
      <pre className={styles.contentText}>{shown}{!expanded && collapsible ? '…' : ''}</pre>
      {collapsible && (
        <button type="button" className={styles.expandBtn} onClick={() => setExpanded((v) => !v)}>
          {expanded ? t('conversations.collapse') : t('conversations.expand')}
        </button>
      )}
    </div>
  )
}

export default function ConversationsPage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

  const [items, setItems] = useState<ConversationRecord[]>([])
  const [total, setTotal] = useState(0)
  const [sessionId, setSessionId] = useState(0)
  const [allSessions, setAllSessions] = useState<Session[]>([])
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async (sid: number, offset: number, append: boolean) => {
    if (append) setLoadingMore(true)
    else setLoading(true)
    try {
      const resp = await listConversationRecords({ sessionId: sid || undefined, limit: PAGE_SIZE, offset })
      setItems((prev) => (append ? [...prev, ...(resp.data.items || [])] : resp.data.items || []))
      setTotal(resp.data.total)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }, [])

  useEffect(() => {
    if (!user) return
    load(sessionId, 0, false)
  }, [user, sessionId, load])

  // 任务过滤下拉：取用户全部会话（不限当前工作区，记录本身是跨工作区的）
  useEffect(() => {
    if (!user) return
    listSessions()
      .then((resp) => setAllSessions(resp.data.sessions || []))
      .catch(() => {})
  }, [user])

  const hasMore = items.length < total

  if (authLoading || !user) return <LoadingSpinner />

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <div>
              <h1 className={styles.title}>{t('conversations.title')}</h1>
              <p className={styles.subtitle}>{t('conversations.subtitle')}</p>
            </div>
          </div>
          <button type="button" className={styles.refreshBtn} onClick={() => load(sessionId, 0, false)} disabled={loading}>
            <RefreshCw size={14} className={loading ? styles.spin : undefined} />
            {t('conversations.refresh')}
          </button>
        </header>

        <div className={styles.body}>
          <div className={styles.bodyInner}>
            {error && <ErrorBanner message={error} />}

            <div className={styles.filters}>
              <label className={styles.filterLabel}>{t('conversations.filterTask')}</label>
              <select
                className={styles.taskSelect}
                value={sessionId}
                onChange={(e) => setSessionId(Number(e.target.value))}
              >
                <option value={0}>{t('conversations.taskAll')}</option>
                {allSessions.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.title || `#${s.id}`}
                  </option>
                ))}
              </select>
              <span className={styles.totalHint}>{t('conversations.total', { count: total })}</span>
            </div>

            {loading ? (
              <LoadingSpinner />
            ) : items.length === 0 ? (
              <p className={styles.empty}>{t('conversations.empty')}</p>
            ) : (
              <div className={styles.list}>
                {items.map((it) => (
                  <div key={`${it.db_session_id}-${it.sequence}`} className={styles.card}>
                    <div className={styles.cardHeader}>
                      <span className={`${styles.roleBadge} ${it.role === 'user' ? styles.roleUser : styles.roleAgent}`}>
                        {it.role === 'user' ? <User size={12} /> : <Bot size={12} />}
                        {it.role === 'user' ? t('conversations.roleUser') : t('conversations.roleAgent')}
                      </span>
                      <Link to={sessionUrl(it.db_session_id, workspaceId)} className={styles.sessionLink} title={it.session_title}>
                        {it.session_title || `#${it.db_session_id}`}
                      </Link>
                      {it.agent_type && <span className={styles.agentType}>{it.agent_type}</span>}
                      <span className={styles.time} title={new Date(it.created_at).toLocaleString()}>
                        {formatTimeAgo(it.created_at, t)}
                      </span>
                    </div>
                    <RecordContent content={it.content} />
                  </div>
                ))}
                {hasMore && (
                  <button
                    type="button"
                    className={styles.loadMoreBtn}
                    onClick={() => load(sessionId, items.length, true)}
                    disabled={loadingMore}
                  >
                    {loadingMore ? t('conversations.loading') : t('conversations.loadMore')}
                  </button>
                )}
              </div>
            )}
          </div>
        </div>
      </div>
    </AppLayout>
  )
}
