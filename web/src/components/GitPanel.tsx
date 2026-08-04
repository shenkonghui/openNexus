import { useState, useEffect, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { GitBranch, RefreshCw, FileWarning, CircleDot } from 'lucide-react'
import { listWorktrees, type WorktreeEntry } from '../api/filesystem'
import {
  gitLog,
  gitStatus,
  gitCommitFiles,
  gitDiff,
  type GitCommit,
  type GitFileEntry,
  type GitDiffResponse,
} from '../api/git'
import { diffLines, shortPath, type DiffLine } from '../utils/diff'
import { DiffTable } from './DiffView'
import LoadingSpinner from './LoadingSpinner'
import styles from './GitPanel.module.css'

interface GitPanelProps {
  cwd: string
}

/** 取路径最后一段作为紧凑显示（如 ~/.openNexus/worktrees/repo/task-1 -> task-1） */
function baseName(path: string): string {
  const parts = path.replace(/\/+$/, '').split('/')
  return parts[parts.length - 1] || path
}

/** 相对时间（分钟/小时/天前），超过 30 天显示日期 */
function relTime(unixSec: number, t: (k: string, o?: Record<string, unknown>) => string): string {
  const diff = Date.now() / 1000 - unixSec
  if (diff < 60) return t('gitPanel.justNow')
  if (diff < 3600) return t('gitPanel.minutesAgo', { n: Math.floor(diff / 60) })
  if (diff < 86400) return t('gitPanel.hoursAgo', { n: Math.floor(diff / 3600) })
  if (diff < 30 * 86400) return t('gitPanel.daysAgo', { n: Math.floor(diff / 86400) })
  return new Date(unixSec * 1000).toLocaleDateString()
}

/** 状态字母 → 样式类（A 绿 / D 红 / 其他黄） */
function statusClass(status: string): string {
  if (status === 'A' || status === '?') return styles.stAdd
  if (status === 'D') return styles.stDel
  return styles.stMod
}

// 选中的「提交」：null=尚未选择，'' 空串=未提交改动，其余为 commit hash
type SelectedRef = string | null

export default function GitPanel({ cwd }: GitPanelProps) {
  const { t } = useTranslation()
  const [worktrees, setWorktrees] = useState<WorktreeEntry[]>([])
  const [selectedWt, setSelectedWt] = useState('')
  const [branch, setBranch] = useState('')
  const [commits, setCommits] = useState<GitCommit[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [refreshTick, setRefreshTick] = useState(0)

  const [selectedRef, setSelectedRef] = useState<SelectedRef>(null)
  const [files, setFiles] = useState<GitFileEntry[]>([])
  const [filesLoading, setFilesLoading] = useState(false)
  const [selectedFile, setSelectedFile] = useState('')
  const [diffData, setDiffData] = useState<GitDiffResponse | null>(null)
  const [diffLoading, setDiffLoading] = useState(false)
  const [diffError, setDiffError] = useState('')

  // 拉取 worktree 列表；默认选中包含当前会话 cwd 的 worktree
  useEffect(() => {
    if (!cwd) return
    let cancelled = false
    listWorktrees(cwd)
      .then((resp) => {
        if (cancelled) return
        const list = resp.data.worktrees || []
        setWorktrees(list)
        setSelectedWt((prev) => {
          if (prev && list.some((w) => w.path === prev)) return prev
          const norm = cwd.replace(/\/+$/, '')
          // 最长前缀匹配：cwd 本身可能就在某个 worktree 内
          const match = list
            .filter((w) => norm === w.path || norm.startsWith(w.path + '/'))
            .sort((a, b) => b.path.length - a.path.length)[0]
          return match?.path || list[0]?.path || ''
        })
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
    return () => { cancelled = true }
  }, [cwd, refreshTick])

  // 拉取选中 worktree 的提交记录，默认选中「未提交改动」
  useEffect(() => {
    if (!selectedWt) return
    let cancelled = false
    setLoading(true)
    setError('')
    gitLog(selectedWt)
      .then((resp) => {
        if (cancelled) return
        setBranch(resp.data.branch)
        setCommits(resp.data.commits || [])
        setSelectedRef((prev) => (prev == null ? '' : prev))
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [selectedWt, refreshTick])

  // 拉取选中提交（或未提交改动）的文件列表
  useEffect(() => {
    if (!selectedWt || selectedRef == null) return
    let cancelled = false
    setFilesLoading(true)
    setSelectedFile('')
    setDiffData(null)
    const req = selectedRef === '' ? gitStatus(selectedWt) : gitCommitFiles(selectedWt, selectedRef)
    req
      .then((resp) => {
        if (!cancelled) setFiles(resp.data.files || [])
      })
      .catch(() => {
        if (!cancelled) setFiles([])
      })
      .finally(() => {
        if (!cancelled) setFilesLoading(false)
      })
    return () => { cancelled = true }
  }, [selectedWt, selectedRef, refreshTick])

  // 拉取选中文件的 diff
  const handleSelectFile = useCallback(
    (file: string) => {
      setSelectedFile(file)
      setDiffLoading(true)
      setDiffError('')
      gitDiff(selectedWt, file, selectedRef || undefined)
        .then((resp) => setDiffData(resp.data))
        .catch((err) => {
          setDiffData(null)
          setDiffError(err instanceof Error ? err.message : String(err))
        })
        .finally(() => setDiffLoading(false))
    },
    [selectedWt, selectedRef],
  )

  const lines = useMemo<DiffLine[]>(() => {
    if (!diffData || diffData.is_binary || diffData.too_large) return []
    return diffLines(diffData.old_text, diffData.new_text)
  }, [diffData])

  if (!cwd) {
    return <div className={styles.empty}>{t('panel.requireSession')}</div>
  }

  return (
    <div className={styles.container}>
      {/* 顶栏：worktree 切换 + 当前分支 + 刷新 */}
      <div className={styles.toolbar}>
        <GitBranch size={14} className={styles.branchIcon} />
        <select
          className={styles.wtSelect}
          value={selectedWt}
          onChange={(e) => {
            setSelectedWt(e.target.value)
            setSelectedRef(null)
          }}
        >
          {worktrees.map((w) => (
            <option key={w.path} value={w.path}>
              {w.is_main ? t('gitPanel.mainWorktree', { name: baseName(w.path) }) : baseName(w.path)}
              {w.branch ? ` · ${w.branch}` : ''}
            </option>
          ))}
        </select>
        {branch && <span className={styles.branchTag}>{branch}</span>}
        <button
          type="button"
          className={styles.refreshBtn}
          onClick={() => setRefreshTick((v) => v + 1)}
          title={t('gitPanel.refresh')}
        >
          <RefreshCw size={13} />
        </button>
      </div>

      {error ? (
        <div className={styles.error}>{error}</div>
      ) : (
        <div className={styles.body}>
          {/* 左列：未提交改动 + 提交流 */}
          <div className={styles.commitList}>
            <button
              type="button"
              className={`${styles.commitItem} ${selectedRef === '' ? styles.commitActive : ''}`}
              onClick={() => setSelectedRef('')}
            >
              <CircleDot size={12} className={styles.uncommittedIcon} />
              <span className={styles.commitSubject}>{t('gitPanel.uncommitted')}</span>
            </button>
            {loading ? (
              <LoadingSpinner />
            ) : commits.length === 0 ? (
              <div className={styles.emptyHint}>{t('gitPanel.noCommits')}</div>
            ) : (
              commits.map((c) => (
                <button
                  key={c.hash}
                  type="button"
                  className={`${styles.commitItem} ${selectedRef === c.hash ? styles.commitActive : ''}`}
                  onClick={() => setSelectedRef(c.hash)}
                  title={`${c.subject}\n${c.author} · ${c.short_hash}`}
                >
                  <span className={styles.commitSubject}>{c.subject}</span>
                  <span className={styles.commitMeta}>
                    <code>{c.short_hash}</code> {c.author} · {relTime(c.date, t)}
                  </span>
                </button>
              ))
            )}
          </div>

          {/* 右列：文件列表 + diff */}
          <div className={styles.main}>
            <div className={styles.fileList}>
              {filesLoading ? (
                <LoadingSpinner />
              ) : files.length === 0 ? (
                <div className={styles.emptyHint}>
                  {selectedRef === '' ? t('gitPanel.clean') : t('gitPanel.noFiles')}
                </div>
              ) : (
                files.map((f) => (
                  <button
                    key={f.path}
                    type="button"
                    className={`${styles.fileItem} ${selectedFile === f.path ? styles.fileActive : ''}`}
                    onClick={() => handleSelectFile(f.path)}
                    title={f.path}
                  >
                    <span className={`${styles.fileStatus} ${statusClass(f.status)}`}>{f.status}</span>
                    <span className={styles.filePath}>{shortPath(f.path, 3)}</span>
                    {f.added >= 0 && (
                      <span className={styles.fileStats}>
                        <span className={styles.added}>+{f.added}</span>
                        <span className={styles.removed}>-{f.removed}</span>
                      </span>
                    )}
                  </button>
                ))
              )}
            </div>
            <div className={styles.diffArea}>
              {diffLoading ? (
                <LoadingSpinner />
              ) : diffError ? (
                <div className={styles.error}>{diffError}</div>
              ) : !diffData ? (
                <div className={styles.emptyHint}>{t('gitPanel.selectFileHint')}</div>
              ) : diffData.is_binary ? (
                <div className={styles.emptyHint}>
                  <FileWarning size={14} /> {t('gitPanel.binaryFile')}
                </div>
              ) : diffData.too_large ? (
                <div className={styles.emptyHint}>
                  <FileWarning size={14} /> {t('gitPanel.tooLarge')}
                </div>
              ) : (
                <DiffTable lines={lines} />
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
