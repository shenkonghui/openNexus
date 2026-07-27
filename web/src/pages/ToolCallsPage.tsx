import { useState, useEffect, useCallback } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { listToolCalls, type ToolCallRecord } from '../api/toolCalls'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import { sessionUrl } from '../utils/routes'
import { formatTimeAgo } from '../utils/time'
import { RefreshCw, Terminal, FileText, Pencil, Search, Globe, Wrench, Loader2, CheckCircle2, XCircle, CircleDashed } from 'lucide-react'
import styles from './ToolCallsPage.module.css'

const PAGE_SIZE = 50

// kind 过滤项（对齐 ACP ToolKind，仅列常见类别）
const KIND_FILTERS = ['', 'execute', 'read', 'edit', 'search', 'fetch', 'other'] as const

function KindIcon({ kind }: { kind: string }) {
  const size = 14
  if (kind === 'execute') return <Terminal size={size} />
  if (kind === 'read') return <FileText size={size} />
  if (kind === 'edit' || kind === 'delete' || kind === 'move') return <Pencil size={size} />
  if (kind === 'search') return <Search size={size} />
  if (kind === 'fetch') return <Globe size={size} />
  return <Wrench size={size} />
}

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation()
  const size = 13
  if (status === 'completed')
    return <span className={`${styles.status} ${styles.statusDone}`}><CheckCircle2 size={size} />{t('toolCalls.statusCompleted')}</span>
  if (status === 'failed')
    return <span className={`${styles.status} ${styles.statusFailed}`}><XCircle size={size} />{t('toolCalls.statusFailed')}</span>
  if (status === 'in_progress')
    return <span className={`${styles.status} ${styles.statusRunning}`}><Loader2 size={size} className={styles.spin} />{t('toolCalls.statusInProgress')}</span>
  return <span className={`${styles.status} ${styles.statusPending}`}><CircleDashed size={size} />{t('toolCalls.statusPending')}</span>
}

export default function ToolCallsPage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

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
      const resp = await listToolCalls({ kind: k || undefined, limit: PAGE_SIZE, offset })
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
    load(kind, 0, false)
  }, [user, kind, load])

  const hasMore = items.length < total

  if (authLoading || !user) return <LoadingSpinner />

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <div>
              <h1 className={styles.title}>{t('toolCalls.title')}</h1>
              <p className={styles.subtitle}>{t('toolCalls.subtitle')}</p>
            </div>
          </div>
          <button type="button" className={styles.refreshBtn} onClick={() => load(kind, 0, false)} disabled={loading}>
            <RefreshCw size={14} className={loading ? styles.spin : undefined} />
            {t('toolCalls.refresh')}
          </button>
        </header>

        <div className={styles.body}>
          <div className={styles.bodyInner}>
            {error && <ErrorBanner message={error} />}

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
              <span className={styles.totalHint}>{t('toolCalls.total', { count: total })}</span>
            </div>

            {loading ? (
              <LoadingSpinner />
            ) : items.length === 0 ? (
              <p className={styles.empty}>{t('toolCalls.empty')}</p>
            ) : (
              <div className={styles.tableWrap}>
                <table className={styles.table}>
                  <thead>
                    <tr>
                      <th className={styles.colTime}>{t('toolCalls.colTime')}</th>
                      <th className={styles.colKind}>{t('toolCalls.colKind')}</th>
                      <th>{t('toolCalls.colDetail')}</th>
                      <th className={styles.colSession}>{t('toolCalls.colSession')}</th>
                      <th className={styles.colStatus}>{t('toolCalls.colStatus')}</th>
                      <th className={styles.colExit}>{t('toolCalls.colExitCode')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((it) => (
                      <tr key={it.id}>
                        <td className={styles.colTime} title={new Date(it.started_at).toLocaleString()}>
                          {formatTimeAgo(it.started_at, t)}
                        </td>
                        <td className={styles.colKind}>
                          <span className={styles.kindBadge}><KindIcon kind={it.kind} />{it.kind || 'other'}</span>
                        </td>
                        <td className={styles.colDetail}>
                          {it.kind === 'execute' && (it.command || it.title) ? (
                            <>
                              <code className={styles.command} title={it.command || it.title}>{it.command || it.title}</code>
                              {it.cwd && <div className={styles.cwd} title={it.cwd}>{it.cwd}</div>}
                            </>
                          ) : (
                            <span className={styles.detailTitle} title={it.title}>{it.title || it.tool_call_id}</span>
                          )}
                        </td>
                        <td className={styles.colSession}>
                          <Link to={sessionUrl(it.db_session_id, workspaceId)} className={styles.sessionLink} title={it.session_title}>
                            {it.session_title || `#${it.db_session_id}`}
                          </Link>
                          {it.agent_type && <div className={styles.agentType}>{it.agent_type}</div>}
                        </td>
                        <td className={styles.colStatus}><StatusBadge status={it.status} /></td>
                        <td className={styles.colExit}>
                          {it.exit_code != null ? (
                            <span className={it.exit_code === 0 ? styles.exitOk : styles.exitBad}>{it.exit_code}</span>
                          ) : (
                            <span className={styles.exitNone}>-</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
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
              </div>
            )}
          </div>
        </div>
      </div>
    </AppLayout>
  )
}
