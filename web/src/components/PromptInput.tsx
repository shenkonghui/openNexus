import { useState, useMemo, useRef, useEffect, useCallback, type FormEvent, type KeyboardEvent, type DragEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Send, Square, ListPlus } from 'lucide-react'
import type { AgentCommand, SessionMode, AgentSkill, Note } from '../types'
import { listFiles, type FileEntry, uploadFilesToWorkspace } from '../api/filesystem'
import { listNotes, listNoteTags } from '../api/notes'
import styles from './PromptInput.module.css'

interface PromptInputProps {
  onSend: (prompt: string) => void
  onCancel?: () => void
  disabled?: boolean
  sending?: boolean
  // 发送队列：sending 期间不锁输入框，提交由调用方（ChatPanel）入队，任务结束后自动续发
  queueEnabled?: boolean
  placeholder?: string
  embedded?: boolean
  value?: string
  onValueChange?: (text: string) => void
  rows?: number
  commands?: AgentCommand[]
  modes?: SessionMode[]
  skills?: AgentSkill[]
  cwd?: string
  // 远程(浏览器)场景拖拽上传所需;本地(Electron)场景不依赖此值(直接取绝对路径)。
  // 不传时,远程拖拽会提示"该页面不支持上传"。
  workspaceId?: number
  // 任务列表（任务助手面板传入，传入即使为空数组也展示分类）：@ 菜单增加「任务」分类，
  // 选中后插入 @task:<id>(标题) 引用，整条消息直发到该任务会话。
  tasks?: TaskMentionItem[]
}

/** @task 引用候选任务（id/标题必传，desc 可附状态等说明） */
export interface TaskMentionItem {
  id: string
  title: string
  desc?: string
}

type TriggerType = 'slash' | 'mention' | null
type MentionCategory = 'command' | 'skill' | 'file' | 'note' | 'task' | 'archive'
type MentionType = MentionCategory | 'mode' | 'tag'
type NavigateAction = 'back' | 'file-up' | 'tag-up'

interface CategoryRow {
  kind: 'category'
  category: MentionCategory
  label: string
  desc: string
  kindLabel: string
}

interface ItemRow {
  kind: 'item'
  type: MentionType
  label: string
  desc: string
  insertText: string
  path: string
  kindLabel: string
  isDir?: boolean
  filePath?: string
  navigate?: NavigateAction
  /** agent 类型命令：排序时优先于普通命令 */
  isAgent?: boolean
}

type MenuRow = CategoryRow | ItemRow

function detectTrigger(text: string, cursorPos: number): { type: TriggerType; query: string; startPos: number } {
  for (let i = cursorPos - 1; i >= 0; i--) {
    const ch = text[i]
    if (ch === ' ' || ch === '\n') break
    if (ch === '/' || ch === '@') {
      if (i === 0 || text[i - 1] === ' ' || text[i - 1] === '\n') {
        return { type: ch === '/' ? 'slash' : 'mention', query: text.slice(i + 1, cursorPos), startPos: i }
      }
      break
    }
  }
  return { type: null, query: '', startPos: -1 }
}

function matchesQuery(fields: string[], query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const tokens = q.split('/').filter(Boolean)
  const haystack = fields.join(' ').toLowerCase()
  return tokens.every((token) => haystack.includes(token))
}

function backRow(desc: string, kindLabel: string, navigate: NavigateAction): ItemRow {
  return { kind: 'item', type: 'command', label: '..', path: '..', desc, insertText: '', kindLabel, navigate }
}

// 生成 @task 引用里的标题注释：去掉空白与括号（避免破坏引用解析），过长截断。
// 与 TaskManagerChatPanel 的 /task 命令标题注释规则一致。
function taskTitleNote(title: string): string {
  return title.replace(/[\s()（）]/g, '').slice(0, 20)
}

function buildSlashItems(
  query: string,
  commands: AgentCommand[],
  modes: SessionMode[],
  skills: AgentSkill[],
  t: (key: string) => string,
): ItemRow[] {
  const items: ItemRow[] = []

  for (const cmd of commands) {
    const path = cmd.path || cmd.name
    const isAgent = cmd.kind === 'agent'
    const kindLabel = isAgent ? t('prompt.slashKindAgent') : t('prompt.slashKindCommand')
    if (!matchesQuery([cmd.name, path, cmd.description, kindLabel], query)) continue
    items.push({
      kind: 'item',
      type: 'command',
      label: path.includes('/') ? path : `/${cmd.name}`,
      path,
      desc: cmd.description,
      insertText: `/${cmd.name} `,
      kindLabel,
      isAgent,
    })
  }

  for (const mode of modes) {
    const path = mode.id
    if (!matchesQuery([mode.name, mode.id, mode.description || '', t('prompt.slashKindMode')], query)) continue
    items.push({
      kind: 'item',
      type: 'mode',
      label: mode.name,
      path,
      desc: mode.description || t('prompt.slashKindMode'),
      insertText: `/${mode.id} `,
      kindLabel: t('prompt.slashKindMode'),
    })
  }

  for (const skill of skills) {
    const path = skill.path || skill.name
    if (!matchesQuery([skill.name, path, skill.description, t('prompt.slashKindSkill')], query)) continue
    items.push({
      kind: 'item',
      type: 'skill',
      label: path.includes('/') ? path : skill.name,
      path,
      desc: skill.description,
      insertText: `/${skill.name} `,
      kindLabel: t('prompt.slashKindSkill'),
    })
  }

  items.sort((a, b) => {
    // agent 命令优先于一切
    const agentDiff = Number(b.isAgent ?? false) - Number(a.isAgent ?? false)
    if (agentDiff !== 0) return agentDiff
    const kindOrder = { command: 0, skill: 1, mode: 2, file: 3, note: 4, tag: 5, task: 6, archive: 7 }
    const ka = kindOrder[a.type] - kindOrder[b.type]
    if (ka !== 0) return ka
    return a.path.localeCompare(b.path)
  })
  return items
}

export default function PromptInput({
  onSend,
  onCancel,
  disabled = false,
  sending = false,
  queueEnabled = false,
  placeholder = '输入 prompt...',
  embedded = false,
  value: controlledValue,
  onValueChange,
  rows = 1,
  commands = [],
  modes = [],
  skills = [],
  cwd = '',
  workspaceId,
  tasks,
}: PromptInputProps) {
  // 队列模式下 sending 不锁定输入：用户可继续输入并提交入队
  const locked = sending && !queueEnabled
  const { t } = useTranslation()
  const [internalText, setInternalText] = useState('')
  const isControlled = controlledValue !== undefined
  const text = isControlled ? controlledValue : internalText
  const setText = (next: string) => {
    if (isControlled) onValueChange?.(next)
    else setInternalText(next)
  }
  const [selectedIdx, setSelectedIdx] = useState(0)
  const [cursorPos, setCursorPos] = useState(0)
  const [mentionCategory, setMentionCategory] = useState<MentionCategory | null>(null)
  const [fileEntries, setFileEntries] = useState<FileEntry[]>([])
  const [fileLoading, setFileLoading] = useState(false)
  const [fileBrowsePath, setFileBrowsePath] = useState('')
  const [fileParentPath, setFileParentPath] = useState('')
  const [noteTags, setNoteTags] = useState<string[]>([])
  const [notes, setNotes] = useState<Note[]>([])
  const [noteBrowseTag, setNoteBrowseTag] = useState('')
  const [notesLoading, setNotesLoading] = useState(false)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // 拖拽接入外部文件:本地(Electron)直接取绝对路径,远程(浏览器)上传到 workspace。
  // isElectron 在浏览器下整个 window.opennexus 为 undefined,安全降级为 false。
  const isElectron = window.opennexus?.isElectron === true
  const [dragOver, setDragOver] = useState(false)
  const [uploading, setUploading] = useState(false)
  const dragDepthRef = useRef(0) // dragenter/leave 嵌套计数,避免子元素抖动

  const trigger = useMemo(() => detectTrigger(text, cursorPos), [text, cursorPos])
  const fileNavigating = !!cwd && !!fileBrowsePath && fileBrowsePath !== cwd

  const slashItems = useMemo<ItemRow[]>(() => {
    if (trigger.type !== 'slash') return []
    return buildSlashItems(trigger.query, commands, modes, skills, t)
  }, [trigger, commands, modes, skills, t])

  const rootCategories = useMemo<CategoryRow[]>(() => {
    if (trigger.type !== 'mention') return []
    const query = trigger.query
    const cats: CategoryRow[] = []

    // 任务引用（任务助手面板专属）：传入 tasks 即展示（含空列表），
    // 选中任务后整条消息直发到该任务会话
    if (tasks !== undefined && matchesQuery([t('prompt.slashKindTask'), 'task'], query)) {
      cats.push({
        kind: 'category',
        category: 'task',
        label: t('prompt.slashKindTask'),
        desc: t('prompt.categoryTaskDesc', { count: tasks.length }),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    // 归档任务（任务助手面板专属）：选中后把任务移入回收站，支持全部
    if (tasks !== undefined && matchesQuery([t('prompt.slashKindArchive'), 'archive-task', 'archive'], query)) {
      cats.push({
        kind: 'category',
        category: 'archive',
        label: t('prompt.slashKindArchive'),
        desc: t('prompt.categoryArchiveDesc', { count: tasks.length }),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    if (commands.length > 0 && matchesQuery([t('prompt.slashKindCommand'), 'command'], query)) {
      cats.push({
        kind: 'category',
        category: 'command',
        label: t('prompt.slashKindCommand'),
        desc: t('prompt.categoryCommandDesc', { count: commands.length }),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    if (skills.length > 0 && matchesQuery([t('prompt.slashKindSkill'), 'skill'], query)) {
      cats.push({
        kind: 'category',
        category: 'skill',
        label: t('prompt.slashKindSkill'),
        desc: t('prompt.categorySkillDesc', { count: skills.length }),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    if (cwd && matchesQuery([t('prompt.slashKindFile'), 'file', 'dir'], query)) {
      cats.push({
        kind: 'category',
        category: 'file',
        label: t('prompt.slashKindFile'),
        desc: t('prompt.categoryFileDesc'),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    if (matchesQuery([t('prompt.slashKindNote'), 'note', 'tag'], query)) {
      cats.push({
        kind: 'category',
        category: 'note',
        label: t('prompt.slashKindNote'),
        desc: t('prompt.categoryNoteDesc'),
        kindLabel: t('prompt.categoryLabel'),
      })
    }
    return cats
  }, [trigger, commands.length, skills.length, cwd, tasks, t])

  const mentionItems = useMemo<MenuRow[]>(() => {
    if (trigger.type !== 'mention' || !mentionCategory) return []
    const query = trigger.query

    if (mentionCategory === 'task') {
      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindTask'), 'back')]
      for (const tk of tasks ?? []) {
        if (!matchesQuery([tk.id, tk.title], query)) continue
        const note = taskTitleNote(tk.title)
        items.push({
          kind: 'item',
          type: 'task',
          label: tk.title,
          path: tk.id,
          desc: tk.desc || tk.id,
          // 引用带标题注释，插入后可直接辨认目标任务，如 @task:a1b2(修复登录)
          insertText: `@task:${tk.id}${note ? `(${note})` : ''} `,
          kindLabel: t('prompt.slashKindTask'),
        })
      }
      return items
    }

    if (mentionCategory === 'archive') {
      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindArchive'), 'back')]
      // 首条「全部任务」：插入 @archive-task:* 一次性归档所有任务
      if (matchesQuery([t('prompt.archiveAllLabel'), 'all', '*'], query)) {
        items.push({
          kind: 'item',
          type: 'task',
          label: t('prompt.archiveAllLabel'),
          path: '*',
          desc: t('prompt.archiveAllDesc', { count: (tasks ?? []).length }),
          insertText: `@archive-task:*(${t('prompt.archiveAllLabel')}) `,
          kindLabel: t('prompt.slashKindArchive'),
        })
      }
      for (const tk of tasks ?? []) {
        if (!matchesQuery([tk.id, tk.title], query)) continue
        const note = taskTitleNote(tk.title)
        items.push({
          kind: 'item',
          type: 'task',
          label: tk.title,
          path: tk.id,
          desc: tk.desc || tk.id,
          insertText: `@archive-task:${tk.id}${note ? `(${note})` : ''} `,
          kindLabel: t('prompt.slashKindArchive'),
        })
      }
      return items
    }

    if (mentionCategory === 'command') {
      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindCommand'), 'back')]
      for (const cmd of commands) {
        const path = cmd.path || cmd.name
        const kindLabel = cmd.kind === 'agent' ? t('prompt.slashKindAgent') : t('prompt.slashKindCommand')
        if (!matchesQuery([cmd.name, path, cmd.description], query)) continue
        items.push({
          kind: 'item',
          type: 'command',
          label: path.includes('/') ? path : `/${cmd.name}`,
          path,
          desc: cmd.description,
          insertText: `/${cmd.name} `,
          kindLabel,
        })
      }
      return items
    }

    if (mentionCategory === 'skill') {
      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindSkill'), 'back')]
      for (const skill of skills) {
        const path = skill.path || skill.name
        if (!matchesQuery([skill.name, path, skill.description], query)) continue
        items.push({
          kind: 'item',
          type: 'skill',
          label: path.includes('/') ? path : skill.name,
          path,
          desc: skill.description,
          insertText: `/${skill.name} `,
          kindLabel: t('prompt.slashKindSkill'),
        })
      }
      return items
    }

    if (mentionCategory === 'file') {
      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindFile'), 'back')]
      if (fileNavigating && fileParentPath) {
        items.push({
          kind: 'item',
          type: 'file',
          label: '..',
          path: '..',
          desc: t('prompt.fileDirUp'),
          insertText: '',
          kindLabel: t('prompt.slashKindFile'),
          navigate: 'file-up',
          filePath: fileParentPath,
        })
      }
      const q = query.toLowerCase()
      for (const e of fileEntries) {
        if (q !== '' && !e.name.toLowerCase().includes(q)) continue
        items.push({
          kind: 'item',
          type: 'file',
          label: e.name,
          path: e.name,
          desc: e.is_dir ? t('prompt.fileDir') : t('prompt.fileFile'),
          insertText: '',
          kindLabel: t('prompt.slashKindFile'),
          isDir: e.is_dir,
          filePath: e.path,
        })
      }
      return items
    }

    if (mentionCategory === 'note') {
      if (noteBrowseTag) {
        const items: MenuRow[] = [backRow(t('prompt.noteTagBack'), t('prompt.slashKindTag'), 'tag-up')]
        for (const note of notes) {
          if (!matchesQuery([note.title, ...note.tags, String(note.id)], query)) continue
          items.push({
            kind: 'item',
            type: 'note',
            label: note.title || t('prompt.noteUntitled'),
            path: String(note.id),
            desc: note.tags.length ? note.tags.map((tag) => `#${tag}`).join(' ') : t('prompt.noteNoTag'),
            insertText: `@note:${note.id} `,
            kindLabel: t('prompt.slashKindNote'),
          })
        }
        return items
      }

      const items: MenuRow[] = [backRow(t('prompt.categoryBack'), t('prompt.slashKindNote'), 'back')]
      for (const tag of noteTags) {
        if (!matchesQuery([tag, `#${tag}`], query)) continue
        items.push({
          kind: 'item',
          type: 'tag',
          label: `#${tag}`,
          path: tag,
          desc: t('prompt.noteTagDesc'),
          insertText: '',
          kindLabel: t('prompt.slashKindTag'),
        })
      }
      return items
    }

    return []
  }, [
    trigger, mentionCategory, commands, skills, fileEntries, fileNavigating,
    fileParentPath, noteTags, notes, noteBrowseTag, tasks, t,
  ])

  const activeRows: MenuRow[] = trigger.type === 'slash'
    ? slashItems
    : trigger.type === 'mention'
      ? (mentionCategory ? mentionItems : rootCategories)
      : []

  const showMenu = trigger.type === 'slash' || trigger.type === 'mention'

  const loadFileEntries = useCallback(async (path: string, query: string) => {
    if (!path) return
    setFileLoading(true)
    try {
      const resp = await listFiles(path, query)
      setFileEntries(resp.data.entries)
      setFileBrowsePath(resp.data.current_path)
      setFileParentPath(resp.data.parent_path)
    } catch {
      setFileEntries([])
    } finally {
      setFileLoading(false)
    }
  }, [])

  const loadNoteData = useCallback(async (tag?: string) => {
    setNotesLoading(true)
    try {
      const [tagsResp, notesResp] = await Promise.all([
        tag ? Promise.resolve(null) : listNoteTags(),
        listNotes(tag, { limit: 100 }),
      ])
      if (tagsResp) setNoteTags(tagsResp.data.tags || [])
      setNotes(notesResp.data.notes || [])
    } catch {
      if (!tag) setNoteTags([])
      setNotes([])
    } finally {
      setNotesLoading(false)
    }
  }, [])

  useEffect(() => {
    if (trigger.type !== 'mention') {
      setMentionCategory(null)
      setFileBrowsePath('')
      setNoteBrowseTag('')
    }
  }, [trigger.type])

  useEffect(() => {
    if (trigger.type !== 'mention' || mentionCategory !== 'file' || !cwd) {
      setFileEntries([])
      return
    }
    const basePath = fileBrowsePath || cwd
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      loadFileEntries(basePath, fileNavigating ? trigger.query : '')
    }, 200)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [trigger.type, trigger.query, mentionCategory, cwd, fileBrowsePath, fileNavigating, loadFileEntries])

  useEffect(() => {
    if (trigger.type !== 'mention' || mentionCategory !== 'note') return
    loadNoteData(noteBrowseTag || undefined)
  }, [trigger.type, mentionCategory, noteBrowseTag, loadNoteData])

  useEffect(() => {
    setSelectedIdx(0)
  }, [activeRows.length, trigger.query, mentionCategory, noteBrowseTag, fileBrowsePath])

  // 根据内容自动撑高 textarea：始终至少容纳 rows 行，内容增多时随之增长，上限由 CSS max-height 控制
  useEffect(() => {
    const el = textareaRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }, [text])

  function goBack() {
    if (noteBrowseTag) {
      setNoteBrowseTag('')
      return
    }
    if (mentionCategory === 'file' && fileNavigating) {
      loadFileEntries(fileParentPath || cwd, '')
      return
    }
    if (mentionCategory) {
      setMentionCategory(null)
      setFileBrowsePath('')
      return
    }
    const before = text.slice(0, trigger.startPos)
    const after = text.slice(cursorPos)
    setText(before + after)
  }

  function applyRow(row: MenuRow) {
    if (trigger.type === null) return

    if (row.kind === 'category') {
      setMentionCategory(row.category)
      setSelectedIdx(0)
      if (row.category === 'file') setFileBrowsePath('')
      if (row.category === 'note') setNoteBrowseTag('')
      return
    }

    if (row.navigate === 'back') {
      goBack()
      return
    }
    if (row.navigate === 'file-up') {
      loadFileEntries(row.filePath || cwd, '')
      return
    }
    if (row.navigate === 'tag-up') {
      setNoteBrowseTag('')
      return
    }
    if (row.type === 'file' && row.isDir) {
      loadFileEntries(row.filePath || '', '')
      return
    }
    if (row.type === 'tag') {
      setNoteBrowseTag(row.path)
      return
    }

    const before = text.slice(0, trigger.startPos)
    const after = text.slice(cursorPos)
    const insertText = row.type === 'file' && row.filePath
      ? `@${row.filePath} `
      : row.insertText

    const newText = before + insertText + after
    setText(newText)
    const newCursor = before.length + insertText.length
    setCursorPos(newCursor)
    requestAnimationFrame(() => {
      const el = textareaRef.current
      if (el) {
        el.focus()
        el.setSelectionRange(newCursor, newCursor)
      }
    })
  }

  // 在当前光标位置插入文本并聚焦。供拖拽文件插入 @<path> 复用,与 applyRow 的插入行为保持一致。
  function insertAtCursor(insertText: string) {
    const before = text.slice(0, cursorPos)
    const after = text.slice(cursorPos)
    const newText = before + insertText + after
    setText(newText)
    const newCursor = before.length + insertText.length
    setCursorPos(newCursor)
    requestAnimationFrame(() => {
      const el = textareaRef.current
      if (el) {
        el.focus()
        el.setSelectionRange(newCursor, newCursor)
      }
    })
  }

  // 拖拽进入/经过:必须 preventDefault 才能触发 drop,且可阻止 Electron 默认把文件当导航跳走。
  function handleDragOver(e: DragEvent<HTMLDivElement>) {
    if (disabled || locked || uploading) return
    if (!e.dataTransfer?.types?.includes('Files')) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }

  function handleDragEnter(e: DragEvent<HTMLDivElement>) {
    if (disabled || locked || uploading) return
    if (!e.dataTransfer?.types?.includes('Files')) return
    e.preventDefault()
    dragDepthRef.current += 1
    setDragOver(true)
  }

  function handleDragLeave() {
    if (disabled || locked || uploading) return
    // 用计数法判断是否真正离开 container(避免子元素 enter/leave 抖动)
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) setDragOver(false)
  }

  async function handleDrop(e: DragEvent<HTMLDivElement>) {
    if (disabled || locked || uploading) return
    const files = e.dataTransfer?.files
    if (!files || files.length === 0) return
    e.preventDefault()
    dragDepthRef.current = 0
    setDragOver(false)

    const fileArr = Array.from(files)

    if (isElectron) {
      // 本地:同步取绝对路径,直接以 @<path> 引用,无需上传。
      const getPath = window.opennexus?.getPathForFile
      for (const f of fileArr) {
        const absPath = getPath ? getPath(f) : ''
        if (absPath) insertAtCursor(`@${absPath} `)
      }
      return
    }

    // 远程:上传到 workspace 后拿到服务器侧绝对路径。
    if (!workspaceId) {
      alert(t('prompt.dropUnsupported'))
      return
    }
    setUploading(true)
    try {
      const resp = await uploadFilesToWorkspace(workspaceId, fileArr)
      for (const f of resp.data.files) {
        insertAtCursor(`@${f.path} `)
      }
    } catch {
      alert(t('prompt.uploadFailed'))
    } finally {
      setUploading(false)
    }
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault()
    const trimmed = text.trim()
    if (!trimmed || disabled || locked) return
    onSend(trimmed)
    if (!isControlled) {
      setInternalText('')
      setCursorPos(0)
    }
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (showMenu && activeRows.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelectedIdx((i) => (i + 1) % activeRows.length)
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelectedIdx((i) => (i - 1 + activeRows.length) % activeRows.length)
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault()
        applyRow(activeRows[selectedIdx])
        return
      }
    }

    if (showMenu && e.key === 'Escape') {
      e.preventDefault()
      goBack()
      return
    }

    if (e.key === 'Enter' && !e.shiftKey && !embedded) {
      e.preventDefault()
      handleSubmit(e as unknown as FormEvent)
    }
  }

  function handleChange(e: React.ChangeEvent<HTMLTextAreaElement>) {
    setText(e.target.value)
    setCursorPos(e.target.selectionStart ?? e.target.value.length)
  }

  function handleSelect(e: React.SyntheticEvent<HTMLTextAreaElement>) {
    setCursorPos(e.currentTarget.selectionStart ?? 0)
  }

  const menuTitle = trigger.type === 'slash'
    ? t('prompt.slashMenuTitle')
    : !mentionCategory
      ? t('prompt.mentionMenuTitle')
      : mentionCategory === 'command'
        ? t('prompt.commandMenuTitle')
        : mentionCategory === 'skill'
          ? t('prompt.skillMenuTitle')
          : mentionCategory === 'task'
            ? t('prompt.taskMenuTitle')
            : mentionCategory === 'archive'
              ? t('prompt.archiveMenuTitle')
              : mentionCategory === 'file'
              ? t('prompt.fileMenuTitle', { path: fileBrowsePath || cwd })
              : noteBrowseTag
                ? t('prompt.noteMenuTitle', { tag: noteBrowseTag })
                : t('prompt.noteTagMenuTitle')

  const menuLoading = trigger.type === 'mention' && (
    (mentionCategory === 'file' && fileLoading) ||
    (mentionCategory === 'note' && notesLoading)
  )

  return (
    <div
      className={`${styles.container} ${embedded ? styles.embedded : ''} ${dragOver ? styles.dragOver : ''}`}
      onDragEnter={handleDragEnter}
      onDragOver={handleDragOver}
      onDragLeave={handleDragLeave}
      onDrop={handleDrop}
    >
      <form className={styles.form} onSubmit={handleSubmit}>
        <div className={styles.inputWrap}>
          {(dragOver || uploading) && (
            <div className={styles.dropOverlay}>
              {uploading ? t('prompt.uploadingFiles') : t('prompt.dropHint')}
            </div>
          )}
          <textarea
            ref={textareaRef}
            className={styles.input}
            value={text}
            onChange={handleChange}
            onKeyDown={handleKeyDown}
            onSelect={handleSelect}
            onClick={handleSelect}
            placeholder={placeholder}
            disabled={disabled || locked}
            rows={rows}
          />
          {showMenu && (
            <div className={styles.commandMenu}>
              <div className={styles.commandMenuHeader}>{menuTitle}</div>
              {menuLoading && <div className={styles.loadingHint}>{t('common.loading')}</div>}
              {!menuLoading && activeRows.length === 0 && (
                <div className={styles.loadingHint}>{t('prompt.slashNoMatch')}</div>
              )}
              {activeRows.map((row, idx) => {
                const isCategory = row.kind === 'category'
                const kindClass = isCategory ? styles.kind_category : styles[`kind_${row.kind === 'item' ? row.type : ''}`]
                const kindLabel = row.kindLabel
                return (
                  <div
                    key={isCategory ? `cat-${row.category}` : `item-${row.type}-${row.path}-${row.navigate || ''}-${idx}`}
                    className={`${styles.commandItem} ${idx === selectedIdx ? styles.commandItemActive : ''}`}
                    onMouseEnter={() => setSelectedIdx(idx)}
                    onMouseDown={(e) => {
                      e.preventDefault()
                      applyRow(row)
                    }}
                  >
                    <span className={`${styles.itemKind} ${kindClass}`}>{kindLabel}</span>
                    <div className={styles.itemMain}>
                      <span className={styles.commandName}>{row.label}</span>
                    </div>
                    <span className={styles.commandDesc}>{row.desc}</span>
                  </div>
                )
              })}
            </div>
          )}
        </div>
        {!embedded && (sending && onCancel ? (
          <>
            {queueEnabled && (
              <button
                className={styles.sendBtn}
                type="submit"
                disabled={disabled || !text.trim()}
                title={t('prompt.queueSend')}
                aria-label={t('prompt.queueSend')}
              >
                <ListPlus size={18} />
              </button>
            )}
            <button
              className={styles.cancelBtn}
              type="button"
              onClick={onCancel}
              title={t('session.cancelPrompt')}
              aria-label={t('session.cancelPrompt')}
            >
              <Square size={18} fill="currentColor" strokeWidth={0} />
            </button>
          </>
        ) : (
          <button
            className={styles.sendBtn}
            type="submit"
            disabled={disabled || sending || !text.trim()}
            title={t('prompt.send')}
            aria-label={t('prompt.send')}
          >
            <Send size={18} />
          </button>
        ))}
      </form>
    </div>
  )
}
