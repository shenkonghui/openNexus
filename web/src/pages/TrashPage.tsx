import { useState, useEffect, useCallback } from 'react'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { listArchivedTasks, restoreArchivedTask, deleteArchivedTask, type ArchivedTask } from '../api/taskmanager'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import { formatTimeAgo } from '../utils/time'
import { RefreshCw, RotateCcw, Trash2 } from 'lucide-react'
import styles from './TrashPage.module.css'

// 剩余保留天数：归档时间 + 保留天数 - 当前时间，向上取整，最小 0
function remainingDays(archivedAt: string, retentionDays: number): number {
  const expireAt = new Date(archivedAt).getTime() + retentionDays * 24 * 3600 * 1000
  return Math.max(0, Math.ceil((expireAt - Date.now()) / (24 * 3600 * 1000)))
}

export default function TrashPage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

  const [items, setItems] = useState<ArchivedTask[]>([])
  const [retentionDays, setRetentionDays] = useState(3)
  const [loading, setLoading] = useState(true)
  const [busyId, setBusyId] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    if (!workspaceId) return
    setLoading(true)
    try {
      const resp = await listArchivedTasks(workspaceId)
      setItems(resp.data.tasks || [])
      setRetentionDays(resp.data.retention_days || 3)
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    if (!user || !workspaceId) return
    void load()
  }, [user, workspaceId, load])

  async function handleRestore(taskId: string) {
    setBusyId(taskId); setError('')
    try {
      await restoreArchivedTask(workspaceId!, taskId)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusyId('')
    }
  }

  async function handleDelete(task: ArchivedTask) {
    if (!window.confirm(t('trash.deleteConfirm', { title: task.title || task.id }))) return
    setBusyId(task.id); setError('')
    try {
      await deleteArchivedTask(workspaceId!, task.id)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusyId('')
    }
  }

  if (authLoading || !user) return <LoadingSpinner />

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <div>
              <h1 className={styles.title}>{t('trash.title')}</h1>
              <p className={styles.subtitle}>{t('trash.subtitle', { days: retentionDays })}</p>
            </div>
          </div>
          <button type="button" className={styles.refreshBtn} onClick={() => load()} disabled={loading}>
            <RefreshCw size={14} className={loading ? styles.spin : undefined} />
            {t('trash.refresh')}
          </button>
        </header>

        <div className={styles.body}>
          <div className={styles.bodyInner}>
            {error && <ErrorBanner message={error} />}

            {loading ? (
              <LoadingSpinner />
            ) : items.length === 0 ? (
              <p className={styles.empty}>{t('trash.empty')}</p>
            ) : (
              <div className={styles.tableWrap}>
                <table className={styles.table}>
                  <thead>
                    <tr>
                      <th>{t('trash.colTask')}</th>
                      <th className={styles.colStatus}>{t('trash.colStatus')}</th>
                      <th className={styles.colTime}>{t('trash.colArchivedAt')}</th>
                      <th className={styles.colRemain}>{t('trash.colRemaining')}</th>
                      <th className={styles.colActions}>{t('trash.colActions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((it) => (
                      <tr key={it.id}>
                        <td className={styles.colTask}>
                          <span className={styles.taskTitle} title={it.detail || it.title}>{it.title || it.id}</span>
                          <div className={styles.taskId}>{it.id}{it.agent_type ? ` · ${it.agent_type}` : ''}</div>
                        </td>
                        <td className={styles.colStatus}>
                          <span className={styles.statusBadge}>{it.status || '-'}</span>
                        </td>
                        <td className={styles.colTime} title={new Date(it.archived_at).toLocaleString()}>
                          {formatTimeAgo(it.archived_at, t)}
                        </td>
                        <td className={styles.colRemain}>
                          {t('trash.remainingDays', { days: remainingDays(it.archived_at, retentionDays) })}
                        </td>
                        <td className={styles.colActions}>
                          <button type="button" className={styles.actionBtn}
                            disabled={busyId === it.id}
                            onClick={() => handleRestore(it.id)}
                          >
                            <RotateCcw size={13} />
                            {t('trash.restore')}
                          </button>
                          <button type="button" className={`${styles.actionBtn} ${styles.dangerBtn}`}
                            disabled={busyId === it.id}
                            onClick={() => handleDelete(it)}
                          >
                            <Trash2 size={13} />
                            {t('trash.deleteNow')}
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </div>
      </div>
    </AppLayout>
  )
}
