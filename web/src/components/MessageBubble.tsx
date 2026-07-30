import { useState, useEffect, useMemo, useRef, memo } from 'react'
import { useTranslation } from 'react-i18next'
import type { Message } from '../types'
import { parseDiffsFromMessage, shortPath } from '../utils/diff'
import { restoreToCheckpoint } from '../api/filesystem'
import DiffView from './DiffView'
import MarkdownContent from './MarkdownContent'
import InlineAgentTerminal from './InlineAgentTerminal'
import { ChevronDown, ChevronRight, MoreHorizontal } from 'lucide-react'
import styles from './MessageBubble.module.css'

interface MessageBubbleProps {
  message: Message
  defaultOpen?: boolean
  forceCollapsed?: boolean
  streaming?: boolean
  sessionId?: number
  cwd?: string
  canRestore?: boolean // 是否显示恢复按钮（仅历史用户消息）
  onRestored?: (promptText: string) => void // 恢复完成后回调，返回被恢复消息的文本
  /** 去掉水平 padding（分组展开内容与标题左对齐） */
  flush?: boolean
}

const kindLabels: Record<string, string> = {
  user_message_chunk: 'chat.user',
  agent_message_chunk: 'chat.assistant',
  agent_thought_chunk: 'chat.thinking',
  tool_call: 'chat.toolCall',
  tool_call_update: 'chat.toolCallUpdate',
  plan: 'chat.plan',
  usage_update: 'chat.usage',
}

// 从工具调用 content 中提取首行作为摘要，回退到默认标签
export function toolSummary(content: string): string {
  const firstLine = content.split('\n')[0]?.trim() || ''
  if (firstLine) return firstLine
  return 'chat.toolCall'
}

// CodeBuddy 等常先发 title=工具名（如 Bash），命令在后续 update 的 rawInput.command 里流式到达。
// raw_json 可能含多行 JSON（合并消息）；取其中最长的 command。
export function toolCommandFromRaw(rawJSON: string): string {
  if (!rawJSON) return ''
  let best = ''
  for (const part of rawJSON.split('\n')) {
    const line = part.trim()
    if (!line) continue
    try {
      const raw = JSON.parse(line) as { rawInput?: Record<string, unknown> }
      const ri = raw.rawInput
      if (!ri || typeof ri !== 'object') continue
      const cmd = ri.command
      if (typeof cmd === 'string' && cmd.trim().length >= best.length) best = cmd.trim()
    } catch {
      /* 跳过无法解析的行 */
    }
  }
  return best
}

export function isBareToolName(title: string): boolean {
  return /^(bash|shell|read|write|edit|grep|glob|search|execute|other|(read|write|edit|create)\s+file)$/i.test(title.trim())
}

// Write/Read 等无 command 的工具：从 rawInput 常见字段、locations 或 diff content
// 中提取目标文件路径，用于标题与摘要展示。raw_json 兼容多行 NDJSON。
const PATH_INPUT_KEYS = ['path', 'file_path', 'filePath', 'abs_path', 'absPath', 'target_file']
export function toolPathFromRaw(rawJSON: string): string {
  if (!rawJSON) return ''
  for (const part of rawJSON.split('\n')) {
    const line = part.trim()
    if (!line) continue
    try {
      const raw = JSON.parse(line) as Record<string, any>
      const ri = raw.rawInput
      if (ri && typeof ri === 'object') {
        for (const k of PATH_INPUT_KEYS) {
          if (typeof ri[k] === 'string' && ri[k]) return ri[k]
        }
      }
      const loc = raw.locations
      if (Array.isArray(loc) && typeof loc[0]?.path === 'string' && loc[0].path) return loc[0].path
      if (Array.isArray(raw.content)) {
        for (const item of raw.content) {
          if (item?.type === 'diff' && typeof item.path === 'string' && item.path) return item.path
        }
      }
    } catch {
      /* 跳过无法解析的行 */
    }
  }
  return ''
}

// 从 raw_json 提取工具输出文本（rawOutput.content / content[].content.text），
// 用于工具调用展开时展示执行结果。
export function toolOutputFromRaw(rawJSON: string): string {
  if (!rawJSON) return ''
  const parts: string[] = []
  for (const part of rawJSON.split('\n')) {
    const line = part.trim()
    if (!line) continue
    try {
      const raw = JSON.parse(line) as Record<string, any>
      const ro = raw.rawOutput
      if (typeof ro === 'string' && ro.trim()) {
        parts.push(ro)
      } else if (ro && typeof ro === 'object' && typeof ro.content === 'string' && ro.content.trim()) {
        parts.push(ro.content)
      }
      if (Array.isArray(raw.content)) {
        for (const item of raw.content) {
          const text = item?.type === 'content' ? item?.content?.text : undefined
          if (typeof text === 'string' && text.trim()) parts.push(text)
        }
      }
    } catch {
      /* 跳过无法解析的行 */
    }
  }
  return parts.join('\n').trim()
}

// 提取 tool_call 关联的 ACP 终端 ID（content 中的 {type:'terminal', terminalId} 项），
// 用于在对话输出位置内嵌实时终端窗口。raw_json 兼容多行 NDJSON。
export function terminalIdsFromRaw(rawJSON: string): string[] {
  if (!rawJSON) return []
  const ids: string[] = []
  for (const part of rawJSON.split('\n')) {
    const line = part.trim()
    if (!line) continue
    try {
      const raw = JSON.parse(line) as Record<string, any>
      if (!Array.isArray(raw.content)) continue
      for (const item of raw.content) {
        if (item?.type === 'terminal' && typeof item.terminalId === 'string' && item.terminalId && !ids.includes(item.terminalId)) {
          ids.push(item.terminalId)
        }
      }
    } catch {
      /* 跳过无法解析的行 */
    }
  }
  return ids
}

/** 工具调用展示文案：有具体命令时优先显示命令，避免只显示 "Bash"；
 *  Write/Read 等文件工具补充目标路径，避免只显示裸工具名 */
export function toolLabel(msg: Message): string {
  const cmd = toolCommandFromRaw(msg.raw_json)
  const content = (msg.content || '').trim().split('\n')[0] || ''
  if (cmd && (!content || isBareToolName(content))) return `\`${cmd}\``
  if (content && !isBareToolName(content)) return content
  const path = toolPathFromRaw(msg.raw_json)
  if (content && path) return `${content} ${shortPath(path)}`
  if (cmd) return `\`${cmd}\``
  if (content) return content
  if (path) return shortPath(path)
  return 'chat.toolCall'
}

// 用 memo 包裹：虚拟化后仅可视区几个气泡参与比较，配合 ChatPage 稳定的 onRestored 回调，
// 历史气泡（其 message 引用在 groupMessages 后仍可能变化，但虚拟化路径下它们多不在可视区）
// 与未变化的可视区气泡可被跳过，减少 reconcile。
function MessageBubble({ message, defaultOpen = false, forceCollapsed = false, streaming = false, sessionId, cwd, canRestore, onRestored, flush }: MessageBubbleProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(defaultOpen && !forceCollapsed)
  const [restoring, setRestoring] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)

  // 点击菜单外部关闭
  useEffect(() => {
    if (!menuOpen) return
    function handleClick(e: MouseEvent) {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        setMenuOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [menuOpen])

  // 检测 tool_call 消息是否携带文件 diff
  const hasDiff = useMemo(
    () => (message.kind === 'tool_call' || message.kind === 'tool_call_update')
      && parseDiffsFromMessage(message).length > 0,
    [message],
  )

  // 工具输出文本（rawOutput / content 文本块），展开时显示
  const toolOutput = useMemo(
    () => (message.role === 'tool' ? toolOutputFromRaw(message.raw_json) : ''),
    [message],
  )

  // 该 tool_call 关联的 ACP 终端：在气泡输出位置内嵌实时终端窗口
  const terminalIds = useMemo(
    () => (message.role === 'tool' ? terminalIdsFromRaw(message.raw_json) : []),
    [message],
  )

  // 流式思考：进行中展开，本轮结束后强制折叠
  useEffect(() => {
    if (forceCollapsed) {
      setOpen(false)
    } else {
      setOpen(defaultOpen)
    }
  }, [defaultOpen, forceCollapsed])

  const isUser = message.role === 'user'
  const isThought = message.kind === 'agent_thought_chunk'
  const isTool = message.role === 'tool'
  const isPlan = message.kind === 'plan'
  // 助手正文/工具不显示角色标签（工具只显示命令，避免「工具调用 Bash Bash」）
  const showRole = !isUser && message.kind !== 'agent_message_chunk' && !isTool
  const toolText = isTool ? toolLabel(message) : ''
  const toolTextDisplay = toolText.startsWith('chat.') ? t(toolText) : toolText

  // 思考和工具调用可折叠
  const collapsible = isThought || isTool

  const bubbleClass = isUser
    ? styles.userBubble
    : isThought
      ? styles.thoughtBubble
      : isTool
        ? styles.toolBubble
        : styles.assistantBubble

  const headerClick = collapsible
    ? () => setOpen((v) => !v)
    : undefined

  // 恢复到此处：确认后反向应用该消息之后所有轮次的文件改动 + 删除后续消息
  const handleRestore = async () => {
    setMenuOpen(false)
    if (!sessionId || restoring) return
    if (!window.confirm(t('chat.restoreConfirm'))) return
    setRestoring(true)
    try {
      const resp = await restoreToCheckpoint(sessionId, message.sequence)
      onRestored?.(resp.data.prompt_text || '')
    } catch {
      window.alert(t('chat.restoreFailed'))
    } finally {
      setRestoring(false)
    }
  }

  // ⋯ 菜单（用户消息上的更多操作）
  const restoreMenu = isUser && canRestore && (
    <div className={styles.menuWrap} ref={menuRef}>
      <button
        className={styles.menuTrigger}
        onClick={(e) => {
          e.stopPropagation()
          setMenuOpen((v) => !v)
        }}
        disabled={restoring}
        type="button"
        title={t('common.more')}
      >
        <MoreHorizontal size={14} />
      </button>
      {menuOpen && (
        <div className={styles.menuDropdown} onClick={(e) => e.stopPropagation()}>
          <button
            className={styles.menuItem}
            onClick={() => handleRestore()}
            disabled={restoring}
            type="button"
          >
            {restoring ? t('chat.restoring') : t('chat.restoreHere')}
          </button>
        </div>
      )}
    </div>
  )

  // 用户/助手正文无角色行时不渲染空 header，避免多出一截行距
  const showHeader = showRole || isPlan || isTool || collapsible

  return (
    <div className={`${styles.container} ${flush ? styles.containerFlush : ''} ${isUser ? styles.containerUser : ''}`}>
      <div className={`${styles.bubble} ${bubbleClass}`}>
        {showHeader && (
          <div
            className={`${styles.header} ${collapsible ? styles.headerClickable : ''}`}
            onClick={headerClick}
          >
            {showRole && <span className={styles.role}>{t(kindLabels[message.kind] || message.role)}</span>}
            {isPlan && <span className={styles.badge}>{t('chat.plan')}</span>}
            {isTool && <span className={styles.summary}>{toolTextDisplay}</span>}
            {collapsible && (
              <span className={styles.toggle}>{open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</span>
            )}
          </div>
        )}
        {/* 内嵌终端窗口渲染在折叠体之外：运行中即使气泡折叠也可见 */}
        {terminalIds.map((tid) => (
          <InlineAgentTerminal key={tid} terminalId={tid} bubbleOpen={!collapsible || open} />
        ))}
        {!collapsible || open ? (
          <>
            {isUser ? (
              <div className={styles.inlineRow}>
                {message.content ? (
                  <div className={styles.content}>{message.content}</div>
                ) : (
                  !isPlan && <div className={styles.contentMuted}>{t('common.noData')}</div>
                )}
                {restoreMenu}
              </div>
            ) : isTool ? (
              toolOutput ? (
                <pre className={styles.toolOutput}>
                  {toolOutput.length > 4000 ? `${toolOutput.slice(0, 4000)}\n…` : toolOutput}
                </pre>
              ) : !hasDiff ? (
                <div className={styles.contentMuted}>{t('common.noData')}</div>
              ) : null
            ) : (
              <>
                {message.content && (
                  message.kind === 'agent_message_chunk' && !streaming ? (
                    <MarkdownContent
                      content={message.content}
                      className={`markdown-body ${styles.mdContent}`}
                    />
                  ) : (
                    <div className={styles.content}>{message.content}</div>
                  )
                )}
                {!message.content && !isPlan && (
                  <div className={styles.contentMuted}>{t('common.noData')}</div>
                )}
              </>
            )}
            {hasDiff && sessionId != null && cwd != null && (
              <DiffView
                message={message}
                sessionId={sessionId}
                cwd={cwd}
                defaultExpanded={defaultOpen && !forceCollapsed}
              />
            )}
          </>
        ) : null}
      </div>
    </div>
  )
}

export default memo(MessageBubble)
