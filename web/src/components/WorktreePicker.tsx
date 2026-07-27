import { useState, useEffect, useCallback, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { listWorktrees, createWorktree, type WorktreeEntry } from '../api/filesystem'
import LoadingSpinner from './LoadingSpinner'
import { X, GitBranch, Plus, Sparkles } from 'lucide-react'
import styles from './WorktreePicker.module.css'

/** 哨兵值：表示首次发送时由后端 AI 根据对话内容自动命名并创建 worktree */
export const AUTO_WORKTREE = '__auto_worktree__'

interface WorktreePickerProps {
  /** 用于定位 git 仓库的路径（通常是当前工作区 cwd） */
  repoPath: string
  /** 当前选中的 worktree 路径（用于高亮） */
  selectedPath?: string
  /** 选择 worktree 后回调 */
  onSelect: (path: string) => void
  /** 关闭弹窗 */
  onClose: () => void
}

/** 取路径最后一段作为显示名（如 /a/b/.worktrees/task-1 -> task-1） */
function baseName(path: string): string {
  const parts = path.replace(/\/+$/, '').split('/')
  return parts[parts.length - 1] || path
}

export default function WorktreePicker({ repoPath, selectedPath, onSelect, onClose }: WorktreePickerProps) {
  const { t } = useTranslation()
  const [worktrees, setWorktrees] = useState<WorktreeEntry[]>([])
  const [repoRoot, setRepoRoot] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  // 新建 worktree 表单状态
  const [newBranch, setNewBranch] = useState('')
  const [creating, setCreating] = useState(false)

  const load = useCallback(async () => {
    if (!repoPath) return
    setLoading(true)
    setError('')
    try {
      const resp = await listWorktrees(repoPath)
      setWorktrees(resp.data.worktrees || [])
      setRepoRoot(resp.data.repo_root)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('session.worktreeLoadFailed'))
    } finally {
      setLoading(false)
    }
  }, [repoPath, t])

  useEffect(() => {
    load()
  }, [load])

  // ESC 关闭
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  // 新建 worktree：成功后直接选中并关闭
  const handleCreate = async (e: FormEvent) => {
    e.preventDefault()
    const branch = newBranch.trim()
    if (!branch || creating) return
    setCreating(true)
    setError('')
    try {
      const resp = await createWorktree(repoPath, branch)
      onSelect(resp.data.worktree.path)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setCreating(false)
    }
  }

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div className={styles.modal} onClick={(e) => e.stopPropagation()}>
        <div className={styles.header}>
          <h3 className={styles.title}>{t('session.worktreePickerTitle')}</h3>
          <button className={styles.closeBtn} onClick={onClose} type="button"><X size={16} /></button>
        </div>

        {/* 新建 worktree：输入分支名，回车或点按钮创建到 .worktrees/<branch> */}
        <form className={styles.createBar} onSubmit={handleCreate}>
          <input
            className={styles.createInput}
            value={newBranch}
            onChange={(e) => setNewBranch(e.target.value)}
            placeholder={t('session.worktreeCreatePlaceholder')}
            disabled={creating}
            autoFocus
          />
          <button
            className={styles.createBtn}
            type="submit"
            disabled={!newBranch.trim() || creating}
          >
            <Plus size={13} style={{ verticalAlign: '-2px' }} />
            {creating ? t('session.worktreeCreating') : t('session.worktreeCreate')}
          </button>
        </form>

        {error && <div className={styles.error}>{error}</div>}

        <div className={styles.list}>
          {/* AI 自动：不预先命名，首次发送时由 AI 根据任务内容生成分支名并创建 worktree */}
          <button
            type="button"
            className={`${styles.item} ${selectedPath === AUTO_WORKTREE ? styles.itemActive : ''}`}
            onClick={() => onSelect(AUTO_WORKTREE)}
            title={t('session.worktreeAutoHint')}
          >
            <Sparkles size={13} className={styles.icon} />
            <span className={styles.itemName}>{t('session.worktreeAuto')}</span>
            <span className={styles.itemBranch}>{t('session.worktreeAutoHint')}</span>
          </button>
          {loading ? (
            <LoadingSpinner />
          ) : worktrees.length === 0 ? (
            <p className={styles.empty}>{error ? '' : t('session.worktreeEmpty')}</p>
          ) : (
            worktrees.map((w) => {
              const name = baseName(w.path)
              // 分支名与目录名相同时不重复展示
              const branchLabel = w.detached
                ? `${t('session.worktreeDetached')} @${w.head.slice(0, 7)}`
                : w.branch && w.branch !== name
                  ? w.branch
                  : ''
              return (
                <button
                  key={w.path}
                  type="button"
                  className={`${styles.item} ${w.path === selectedPath ? styles.itemActive : ''}`}
                  onClick={() => onSelect(w.path)}
                  title={w.path}
                >
                  <GitBranch size={13} className={styles.icon} />
                  <span className={styles.itemName}>{name}</span>
                  {w.is_main && <span className={styles.badge}>{t('session.worktreeMain')}</span>}
                  {branchLabel && <span className={styles.itemBranch}>{branchLabel}</span>}
                </button>
              )
            })
          )}
        </div>

        {repoRoot && (
          <div className={styles.repoBar}>
            <span className={styles.repoPath} title={repoRoot}>{repoRoot}</span>
          </div>
        )}
      </div>
    </div>
  )
}
