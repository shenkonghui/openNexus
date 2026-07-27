import { useState, useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { listWorkspaces, createWorkspace, deleteWorkspace, updateWorkspace, saveWorkspace } from '../api/workspaces'
import type { Workspace } from '../types'
import CreateWorkspaceDialog from './CreateWorkspaceDialog'
import { ChevronUp, ChevronDown, Folder, Building2, Clock, Plus, MoreHorizontal } from 'lucide-react'
import styles from './WorkspaceSelector.module.css'

interface Props {
  value: number
  onChange: (id: number) => void
  onRefresh?: () => void
  onError?: (message: string) => void
  /** sidebar：侧边栏底部形态，无边框占满宽度、下拉向上弹出
   *  compact：图标按钮形态，默认向下弹出，可在底部传 menuUp 向上弹出
   *  topbar：顶部栏形态，flex 占满剩余空间、显示完整名称、下拉向下 */
  variant?: 'header' | 'sidebar' | 'compact' | 'topbar'
  /** 是否向上弹出下拉菜单；未指定时 sidebar 默认向上，其余默认向下 */
  menuUp?: boolean
}

export default function WorkspaceSelector({ value, onChange, onRefresh, onError, variant = 'header', menuUp }: Props) {
  const { t } = useTranslation()
  const [workspaces, setWorkspaces] = useState<(Workspace & { session_count?: number })[]>([])
  const [open, setOpen] = useState(false)
  const [showCreate, setShowCreate] = useState(false)
  const [editTarget, setEditTarget] = useState<Workspace | null>(null)
  const [contextMenu, setContextMenu] = useState<{ id: number; x: number; y: number } | null>(null)
  const [renaming, setRenaming] = useState<number | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)

  const current = workspaces.find((w) => w.id === value)

  // 工作区名称显示：后端默认工作区名为中文"默认工作区"，按当前语言显示；其余用原名。
  const displayName = (name: string) => (name === '默认工作区' ? t('workspace.default') : name)

  async function loadWorkspaces() {
    const list = (await listWorkspaces()).data.workspaces || []
    setWorkspaces(list)
    onRefresh?.()
    return list
  }

  useEffect(() => { loadWorkspaces().catch((e) => onError?.(e instanceof Error ? e.message : t('common.failed'))) }, [])

  useEffect(() => {
    if (!open) return
    function handleClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [open])

  useEffect(() => {
    if (!contextMenu) return
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setContextMenu(null)
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [contextMenu])

  async function handleSelect(id: number) {
    onChange(id)
    setOpen(false)
  }

  async function handleCreate(name: string, cwd: string, directories: string[]) {
    try {
      const resp = await createWorkspace(name, cwd, directories)
      await loadWorkspaces()
      onChange(resp.data.id)
      setShowCreate(false)
    } catch (e) {
      onError?.(e instanceof Error ? e.message : t('common.failed'))
    }
  }

  async function handleEdit(name: string, _cwd: string, directories: string[]) {
    if (!editTarget) return
    try {
      await updateWorkspace(editTarget.id, name, directories)
      await loadWorkspaces()
      setEditTarget(null)
    } catch (e) {
      onError?.(e instanceof Error ? e.message : t('common.failed'))
    }
  }

  async function handleDelete(id: number) {
    if (!window.confirm(t('workspace.deleteConfirm'))) return
    try {
      await deleteWorkspace(id)
      const list = await loadWorkspaces()
      if (id === value) onChange(list[0]?.id ?? 0)
      setContextMenu(null)
    } catch (e) {
      onError?.(e instanceof Error ? e.message : t('common.failed'))
    }
  }

  async function handleRenameSubmit(id: number) {
    const name = renameValue.trim()
    if (!name) { setRenaming(null); return }
    try {
      await updateWorkspace(id, name)
      await loadWorkspaces()
    } catch (e) {
      onError?.(e instanceof Error ? e.message : t('common.failed'))
    }
    setRenaming(null)
  }

  return (
    <div className={`${styles.container} ${variant === 'sidebar' ? styles.containerSidebar : ''} ${variant === 'compact' ? styles.containerCompact : ''} ${variant === 'topbar' ? styles.containerTopbar : ''}`} ref={ref}>
      {variant === 'compact' ? (
        <button
          type="button"
          className={`${styles.triggerCompact} ${open ? styles.triggerCompactActive : ''}`}
          onClick={() => setOpen((v) => !v)}
          title={displayName(current?.name || t('workspace.default'))}
        >
          {current?.mode === 'temporary' ? <Clock size={15} /> : <Building2 size={15} />}
        </button>
      ) : (
        <button type="button" className={`${styles.trigger} ${variant === 'sidebar' ? styles.triggerSidebar : ''} ${variant === 'topbar' ? styles.triggerTopbar : ''}`} onClick={() => setOpen((v) => !v)} title={t('workspace.title')}>
          <span className={styles.icon}>{current?.mode === 'temporary' ? <Clock size={14} /> : <Folder size={14} />}</span>
          <span className={styles.label}>{displayName(current?.name || t('workspace.default'))}</span>
          <span className={styles.arrow}>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}</span>
        </button>
      )}

      {open && (
        <div className={`${styles.dropdown} ${(menuUp ?? variant === 'sidebar') ? styles.dropdownUp : ''}`}>
          {workspaces.length === 0 ? (
            <div className={styles.item}><span className={styles.itemName}>{t('workspace.empty')}</span></div>
          ) : workspaces.map((ws) => (
            <div key={ws.id}
              className={`${styles.item} ${ws.id === value ? styles.itemActive : ''}`}
              onClick={() => renaming !== ws.id && handleSelect(ws.id)}
            >
              <span>{ws.mode === 'temporary' ? <Clock size={14} /> : <Folder size={14} />}</span>
              {renaming === ws.id ? (
                <input className={styles.renameInput} value={renameValue} autoFocus
                  onChange={(e) => setRenameValue(e.target.value)}
                  onBlur={() => handleRenameSubmit(ws.id)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') handleRenameSubmit(ws.id)
                    if (e.key === 'Escape') setRenaming(null)
                  }}
                  onClick={(e) => e.stopPropagation()}
                />
              ) : (
                <span className={styles.itemName}>{displayName(ws.name)}</span>
              )}
              {ws.directories && ws.directories.length > 0 && (
                <span className={styles.itemBadge} title={`${ws.directories.length} 个附加目录`}>+{ws.directories.length}</span>
              )}
              {ws.session_count !== undefined && <span className={styles.itemCount}>{ws.session_count}</span>}
              <button type="button" className={styles.menuBtn}
                onClick={(e) => { e.stopPropagation(); setContextMenu({ id: ws.id, x: e.clientX, y: e.clientY }) }}
              ><MoreHorizontal size={14} /></button>
            </div>
          ))}
          <button
            type="button"
            className={styles.createItem}
            onClick={() => { setOpen(false); setShowCreate(true) }}
          >
            <Plus size={14} />
            <span>{t('workspace.create')}</span>
          </button>
        </div>
      )}

      {contextMenu && (
        <div ref={menuRef} className={styles.contextMenu} style={{ top: contextMenu.y, left: contextMenu.x }}>
          <div className={styles.menuItem} onClick={() => {
            const ws = workspaces.find((w) => w.id === contextMenu.id)
            if (ws) { setRenaming(ws.id); setRenameValue(ws.name) }
            setContextMenu(null)
          }}>{t('workspace.rename')}</div>
          <div className={styles.menuItem} onClick={() => {
            const ws = workspaces.find((w) => w.id === contextMenu.id)
            if (ws) { setEditTarget(ws) }
            setContextMenu(null)
          }}>编辑目录</div>
          <div className={styles.menuItem} onClick={async () => {
            const ws = workspaces.find((w) => w.id === contextMenu.id)
            if (ws?.mode === 'temporary') {
              try { await saveWorkspace(contextMenu.id, ws.name, ws.cwd, ws.directories || []); await loadWorkspaces() }
              catch (e) { onError?.(e instanceof Error ? e.message : t('common.failed')) }
            }
            setContextMenu(null)
          }}>{t('workspace.save')}</div>
          <div className={`${styles.menuItem} ${styles.danger}`}
            onClick={() => handleDelete(contextMenu.id)}
          >{t('workspace.delete')}</div>
        </div>
      )}

      {showCreate && (
        <CreateWorkspaceDialog onSubmit={handleCreate} onClose={() => setShowCreate(false)} />
      )}

      {editTarget && (
        <CreateWorkspaceDialog
          onSubmit={handleEdit}
          onClose={() => setEditTarget(null)}
          initialName={editTarget.name}
          initialCwd={editTarget.cwd}
          initialDirectories={editTarget.directories || []}
        />
      )}
    </div>
  )
}
