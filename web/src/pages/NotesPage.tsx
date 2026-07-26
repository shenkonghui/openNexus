import { useState, useEffect, useRef, useCallback, useMemo, type KeyboardEvent } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { listNotes, listNoteTags, createNote, updateNote, deleteNote, exportNotes, importNotes, classifyNoteNow } from '../api/notes'
import type { Note } from '../types'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import MarkdownContent from '../components/MarkdownContent'
import { formatTimeAgo } from '../utils/time'
import { sessionUrl } from '../utils/routes'
import { Pencil, X, Tag, Download, Upload, Sparkles } from 'lucide-react'
import styles from './NotesPage.module.css'

/** 解析导出 frontmatter；失败则整段当 content。 */
function parseImportedChunk(raw: string): { content: string; title?: string; tags?: string[] } {
  const trimmed = raw.trim()
  const m = trimmed.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/)
  if (!m) return { content: trimmed }
  const fm = m[1]
  const content = m[2].replace(/^\r?\n/, '')
  const titleMatch = fm.match(/^title:\s*(.*)$/m)
  let title = titleMatch ? titleMatch[1].trim() : ''
  if ((title.startsWith('"') && title.endsWith('"')) || (title.startsWith("'") && title.endsWith("'"))) {
    title = title.slice(1, -1)
  }
  const tags: string[] = []
  const tagsBlock = fm.match(/^tags:\s*\n((?:\s*-\s+.+\n?)*)/m)
  if (tagsBlock) {
    for (const line of tagsBlock[1].split('\n')) {
      const tm = line.match(/^\s*-\s+(.+)$/)
      if (tm) tags.push(tm[1].trim())
    }
  } else {
    const inline = fm.match(/^tags:\s*\[(.*)\]\s*$/m)
    if (inline) {
      for (const part of inline[1].split(',')) {
        const t = part.trim().replace(/^["']|["']$/g, '')
        if (t) tags.push(t)
      }
    }
  }
  return { content, title: title || undefined, tags: tags.length ? tags : undefined }
}

const NOTE_PAGE_SIZE = 20

export default function NotesPage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

  const [notes, setNotes] = useState<Note[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [allTags, setAllTags] = useState<string[]>([])
  const [activeTag, setActiveTag] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [debouncedQ, setDebouncedQ] = useState('')
  const [input, setInput] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const feedRef = useRef<HTMLDivElement>(null)
  const importInputRef = useRef<HTMLInputElement>(null)

  const classifySession = sessions.find((s) => s.source === 'classify')
  const hasMore = notes.length < total

  useEffect(() => {
    const tmr = window.setTimeout(() => setDebouncedQ(searchQuery.trim()), 300)
    return () => window.clearTimeout(tmr)
  }, [searchQuery])

  const loadNotes = useCallback(async (opts: {
    tag?: string
    q?: string
    page?: number
    append?: boolean
  } = {}) => {
    const p = opts.page ?? 1
    const resp = await listNotes(opts.tag || undefined, {
      q: opts.q || undefined,
      page: p,
      limit: NOTE_PAGE_SIZE,
    })
    const list = resp.data.notes || []
    setTotal(resp.data.total || 0)
    setPage(resp.data.page || p)
    setNotes((prev) => (opts.append ? [...prev, ...list] : list))
  }, [])

  const reloadFirstPage = useCallback(async () => {
    const tagsResp = await listNoteTags()
    setAllTags(tagsResp.data.tags || [])
    await loadNotes({ tag: activeTag, q: debouncedQ, page: 1, append: false })
  }, [activeTag, debouncedQ, loadNotes])

  useEffect(() => {
    if (!user) return
    setLoading(true)
    setError('')
    reloadFirstPage()
      .catch((err) => setError(err instanceof Error ? err.message : t('notes.loadFailed')))
      .finally(() => setLoading(false))
  }, [user, activeTag, debouncedQ, reloadFirstPage, t])

  async function handleLoadMore() {
    if (loadingMore || !hasMore) return
    setLoadingMore(true)
    try {
      await loadNotes({ tag: activeTag, q: debouncedQ, page: page + 1, append: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.loadFailed'))
    } finally {
      setLoadingMore(false)
    }
  }

  const hasPendingClassify = useMemo(
    () => notes.some((n) => n.classify_pending),
    [notes],
  )

  useEffect(() => {
    if (!user || !hasPendingClassify) return
    const timer = window.setInterval(() => {
      reloadFirstPage().catch(() => {})
    }, 30_000)
    return () => window.clearInterval(timer)
  }, [user, hasPendingClassify, reloadFirstPage])

  async function handleCreate() {
    const content = input.trim()
    if (!content || submitting) return
    setError('')
    setSubmitting(true)
    try {
      await createNote(content)
      setInput('')
      await reloadFirstPage()
      feedRef.current?.scrollTo({ top: 0 })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.createFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  function handleInputKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && e.shiftKey) {
      e.preventDefault()
      handleCreate()
    }
  }

  async function handleUpdate(id: number, content: string) {
    setError('')
    try {
      await updateNote(id, content)
      await reloadFirstPage()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.saveFailed'))
      throw err
    }
  }

  async function handleDelete(id: number) {
    if (!window.confirm(t('notes.deleteConfirm'))) return
    setError('')
    try {
      await deleteNote(id)
      await reloadFirstPage()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.deleteFailed'))
    }
  }

  async function handleClassifyNow(id: number) {
    setError('')
    try {
      const resp = await classifyNoteNow(id)
      setNotes((prev) => prev.map((n) => (n.id === id ? resp.data : n)))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.classifyNowFailed'))
      throw err
    }
  }

  async function handleExport() {
    setError('')
    try {
      const blob = await exportNotes()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `notes-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.md`
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.exportFailed'))
    }
  }

  async function handleImportFiles(files: FileList | null) {
    if (!files || files.length === 0) return
    setError('')
    const mdFiles = Array.from(files).filter((f) => f.name.toLowerCase().endsWith('.md'))
    if (mdFiles.length === 0) {
      setError(t('notes.importNoFiles'))
      return
    }
    try {
      const contents = await Promise.all(mdFiles.map((f) => f.text()))
      const notes = contents
        .flatMap((c) => c.split(/\n*=+\n/))   // 按独立成行的 === 拆分（兼容导出格式）
        .map((c) => c.trim())
        .filter((c) => c.length > 0)
        .map((c) => parseImportedChunk(c))
      if (notes.length === 0) {
        setError(t('notes.importNoFiles'))
        return
      }
      const resp = await importNotes(notes)
      const { imported, skipped } = resp.data
      setError('')
      await reloadFirstPage()
      window.alert(t('notes.importSuccess', { imported, skipped }))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notes.importFailed'))
    } finally {
      // 重置 input 以便同一文件/文件夹可再次选择
      if (importInputRef.current) importInputRef.current.value = ''
    }
  }

  if (authLoading || !user) return <LoadingSpinner />

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <h1 className={styles.title}>{t('notes.title')}</h1>
          </div>
          <div className={styles.headerRight}>
            {classifySession && (
              <Link
                to={sessionUrl(classifySession.id, classifySession.workspace_id)}
                className={styles.classifyBtn}
              >
                <Tag size={14} />
                <span>{classifySession.title || t('notes.classifyTask')}</span>
              </Link>
            )}
            <button
              type="button"
              className={styles.importBtn}
              onClick={handleExport}
              title={t('notes.export')}
            >
              <Download size={14} />
              <span>{t('notes.export')}</span>
            </button>
            <button
              type="button"
              className={styles.importBtn}
              onClick={() => importInputRef.current?.click()}
              title={t('notes.import')}
            >
              <Upload size={14} />
              <span>{t('notes.import')}</span>
            </button>
            <input
              ref={importInputRef}
              type="file"
              multiple
              accept=".md"
              style={{ display: 'none' }}
              onChange={(e) => handleImportFiles(e.target.files)}
            />
          </div>
        </header>
        {error && <ErrorBanner message={error} onClose={() => setError('')} />}
        <div className={styles.statusBar}>
          <input
            type="search"
            className={styles.searchInput}
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder={t('notes.searchPlaceholder')}
          />
          <div className={styles.tagBar}>
            <button
              type="button"
              className={`${styles.tagChip} ${activeTag === '' ? styles.tagChipActive : ''}`}
              onClick={() => setActiveTag('')}
            >
              {t('notes.allTags')}
            </button>
            {allTags.map((tag) => (
              <button
                key={tag}
                type="button"
                className={`${styles.tagChip} ${activeTag === tag ? styles.tagChipActive : ''}`}
                onClick={() => setActiveTag(tag)}
              >
                #{tag}
              </button>
            ))}
          </div>
        </div>
        {loading ? (
          <LoadingSpinner />
        ) : (
          <>
            <div className={styles.feed} ref={feedRef}>
              {notes.length === 0 ? (
                <p className={styles.empty}>{t('notes.empty')}</p>
              ) : (
                <>
                  {notes.map((note) => (
                    <NoteCard
                      key={note.id}
                      note={note}
                      onUpdate={(content) => handleUpdate(note.id, content)}
                      onClassify={() => handleClassifyNow(note.id)}
                      onDelete={() => handleDelete(note.id)}
                    />
                  ))}
                  {hasMore && (
                    <button
                      type="button"
                      className={styles.loadMoreBtn}
                      onClick={handleLoadMore}
                      disabled={loadingMore}
                    >
                      {loadingMore ? t('common.loading') : t('notes.loadMore')}
                    </button>
                  )}
                </>
              )}
            </div>
            <div className={styles.inputBar}>
              <div className={styles.inputRow}>
                <textarea
                  className={styles.input}
                  value={input}
                  onChange={(e) => setInput(e.target.value)}
                  onKeyDown={handleInputKeyDown}
                  placeholder={t('notes.quickInputPlaceholder')}
                  rows={4}
                  disabled={submitting}
                />
                <button
                  type="button"
                  className={styles.sendBtn}
                  disabled={!input.trim() || submitting}
                  onClick={handleCreate}
                >
                  {t('prompt.send')}
                </button>
              </div>
              <div className={styles.inputHint}>{t('notes.quickInputHint')}</div>
            </div>
          </>
        )}
      </div>
    </AppLayout>
  )
}

function NoteCard({
  note,
  onUpdate,
  onClassify,
  onDelete,
}: {
  note: Note
  onUpdate: (content: string) => Promise<void>
  onClassify: () => Promise<void>
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const [expanded, setExpanded] = useState(false)
  const [editing, setEditing] = useState(false)
  const [editContent, setEditContent] = useState(note.content)
  const [saving, setSaving] = useState(false)
  const [classifying, setClassifying] = useState(false)
  const contentRef = useRef<HTMLDivElement>(null)
  const editRef = useRef<HTMLTextAreaElement>(null)
  const [truncated, setTruncated] = useState(false)

  useEffect(() => {
    if (!editing) setEditContent(note.content)
  }, [note.content, editing])

  useEffect(() => {
    if (editing) editRef.current?.focus()
  }, [editing])

  useEffect(() => {
    const el = contentRef.current
    if (!el || expanded || editing) return
    setTruncated(el.scrollHeight > el.clientHeight + 1)
  }, [note.content, expanded, editing])

  async function handleSave() {
    const content = editContent.trim()
    if (!content || saving) return
    setSaving(true)
    try {
      await onUpdate(content)
      setEditing(false)
      setExpanded(false)
    } catch {
      // error handled by parent
    } finally {
      setSaving(false)
    }
  }

  async function handleClassify() {
    if (classifying || note.classify_pending) return
    setClassifying(true)
    try {
      await onClassify()
    } catch {
      // error handled by parent
    } finally {
      setClassifying(false)
    }
  }

  function handleCancel() {
    setEditContent(note.content)
    setEditing(false)
  }

  function handleEditKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Escape') {
      e.preventDefault()
      handleCancel()
    }
  }

  return (
    <article className={styles.noteCard}>
      <div className={styles.noteHeader}>
        <div className={styles.noteHeaderMain}>
          <h2 className={styles.noteTitle}>{note.title || t('notes.titlePending')}</h2>
          <div className={styles.noteMeta}>
            {note.tags.length > 0 && (
              <span className={styles.noteTags}>
                {note.tags.map((tag) => (
                  <span key={tag} className={styles.noteTag}>#{tag}</span>
                ))}
              </span>
            )}
            {(note.classify_pending || classifying) && (
              <span className={styles.classifyingBadge}>{t('notes.classifying')}</span>
            )}
            <time className={styles.noteTime} title={note.updated_at}>
              {formatTimeAgo(note.updated_at, t)}
            </time>
          </div>
        </div>
        <div className={styles.noteActions}>
          {!editing && (
            <>
              <button
                type="button"
                className={styles.classifyNowBtn}
                onClick={handleClassify}
                title={t('notes.classifyNow')}
                disabled={classifying || note.classify_pending}
              >
                <Sparkles size={14} />
              </button>
              <button
                type="button"
                className={styles.editBtn}
                onClick={() => setEditing(true)}
                title={t('common.edit')}
              >
                <Pencil size={14} />
              </button>
            </>
          )}
          <button
            type="button"
            className={styles.deleteBtn}
            onClick={onDelete}
            title={t('common.delete')}
            disabled={editing}
          >
            <X size={14} />
          </button>
        </div>
      </div>
      {editing ? (
        <>
          <textarea
            ref={editRef}
            className={styles.editInput}
            value={editContent}
            onChange={(e) => setEditContent(e.target.value)}
            onKeyDown={handleEditKeyDown}
            rows={6}
            disabled={saving}
          />
          <div className={styles.editActions}>
            <button
              type="button"
              className={styles.editCancelBtn}
              onClick={handleCancel}
              disabled={saving}
            >
              {t('common.cancel')}
            </button>
            <button
              type="button"
              className={styles.editSaveBtn}
              onClick={handleSave}
              disabled={!editContent.trim() || saving}
            >
              {saving ? t('notes.saving') : t('common.save')}
            </button>
          </div>
        </>
      ) : (
        <>
          <div
            ref={contentRef}
            className={`${styles.noteContent} markdown-body ${expanded ? '' : styles.noteContentCollapsed}`}
            onClick={() => { if (truncated || expanded) setExpanded((v) => !v) }}
            role={(truncated || expanded) ? 'button' : undefined}
            tabIndex={(truncated || expanded) ? 0 : undefined}
            onKeyDown={(e) => {
              if ((truncated || expanded) && (e.key === 'Enter' || e.key === ' ')) {
                e.preventDefault()
                setExpanded((v) => !v)
              }
            }}
          >
            <MarkdownContent content={note.content} />
          </div>
          {(truncated || expanded) && (
            <button
              type="button"
              className={styles.expandBtn}
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? t('notes.collapse') : t('notes.expand')}
            </button>
          )}
        </>
      )}
    </article>
  )
}
