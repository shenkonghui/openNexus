import { Fragment, forwardRef, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Virtuoso, type VirtuosoHandle } from 'react-virtuoso'
import type { Message, Execution } from '../types'
import { aggregateChanges, type FileChangeItem } from '../utils/diff'
import MessageBubble, { toolLabel, toolCommandFromRaw, isBareToolName } from './MessageBubble'
import ChangesSummary from './ChangesSummary'
import { ChevronDown, ChevronRight } from 'lucide-react'
import styles from './MessageList.module.css'

interface MessageListProps {
  messages: Message[]
  loading?: boolean
  scheduled?: boolean
  executions?: Execution[]
  sessionId?: number
  cwd?: string
  onRestored?: (promptText: string) => void
  /** 是否还有更早的消息可加载（前端据此显示「加载更多」） */
  hasMore?: boolean
  /** 正在加载更早的消息（顶部显示转圈） */
  loadingMore?: boolean
  /** 点击「加载更多」时触发 */
  onLoadMore?: () => void
}

const MERGEABLE_KINDS = new Set([
  'user_message_chunk',
  'agent_message_chunk',
  'agent_thought_chunk',
])

function groupMessages(messages: Message[]): Message[] {
  const grouped: Message[] = []
  for (const msg of messages) {
    const last = grouped[grouped.length - 1]
    if (
      last &&
      MERGEABLE_KINDS.has(msg.kind) &&
      last.kind === msg.kind &&
      last.role === msg.role
    ) {
      grouped[grouped.length - 1] = {
        ...last,
        content: last.content + msg.content,
        raw_json: last.raw_json && msg.raw_json
          ? `${last.raw_json}\n${msg.raw_json}`
          : last.raw_json || msg.raw_json,
      }
      continue
    }
    grouped.push(msg)
  }
  return collapseToolCalls(grouped)
}

/** 解析 tool_call / tool_call_update 的 toolCallId（兼容多行 raw_json） */
function toolCallIdOf(msg: Message): string | null {
  if (msg.kind !== 'tool_call' && msg.kind !== 'tool_call_update') return null
  for (const part of (msg.raw_json || '').split('\n')) {
    const line = part.trim()
    if (!line) continue
    try {
      const raw = JSON.parse(line) as { toolCallId?: string }
      if (raw.toolCallId) return raw.toolCallId
    } catch { /* continue */ }
  }
  return null
}

/** 合并 raw_json：保留带有更长 command 的那份，避免后期空 update 冲掉命令 */
function mergeToolRaw(a: string, b: string): string {
  const ca = toolCommandFromRaw(a)
  const cb = toolCommandFromRaw(b)
  if (cb.length > ca.length) return b || a
  return a || b
}

/** 同 toolCallId 的 tool_call + update 合并为一条，content 取更具体的命令/标题 */
function collapseToolCalls(messages: Message[]): Message[] {
  const out: Message[] = []
  const indexById = new Map<string, number>()
  const better = (a: string, b: string) => {
    const at = a.trim()
    const bt = b.trim()
    if (!bt) return at
    if (!at || isBareToolName(at)) return bt || at
    if (isBareToolName(bt)) return at
    if (bt.length >= at.length) return bt
    return at
  }
  for (const msg of messages) {
    const id = toolCallIdOf(msg)
    if (!id) {
      out.push(msg)
      continue
    }
    const label = toolLabel(msg)
    const content = label.startsWith('chat.') ? (msg.content || '') : label
    const prevIdx = indexById.get(id)
    if (prevIdx === undefined) {
      indexById.set(id, out.length)
      out.push({ ...msg, kind: 'tool_call', role: 'tool', content: content || msg.content })
      continue
    }
    const prev = out[prevIdx]
    out[prevIdx] = {
      ...prev,
      content: better(prev.content || '', content || msg.content || ''),
      raw_json: mergeToolRaw(prev.raw_json || '', msg.raw_json || ''),
    }
  }
  return out
}

function filterDisplay(messages: Message[]): Message[] {
  return groupMessages(messages).filter(
    (msg) =>
      (msg.content.trim() !== '' || msg.kind === 'plan') &&
      msg.kind !== 'permission_request' &&
      msg.kind !== 'reconnecting',
  )
}

function findLastUserIndex(messages: Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].role === 'user') return i
  }
  return -1
}

function isCollapsibleMessage(msg: Message): boolean {
  return msg.kind === 'agent_thought_chunk' || msg.role === 'tool'
}

function bubbleCollapseState(
  msg: Message,
  idx: number,
  lastUserIdx: number,
  loading: boolean,
  lastThoughtKey: string | number | null,
  key: string | number,
) {
  const inCurrentTurn = idx > lastUserIdx
  const collapsible = isCollapsibleMessage(msg)
  const isLastThoughtStreaming =
    !!loading &&
    inCurrentTurn &&
    msg.kind === 'agent_thought_chunk' &&
    key === lastThoughtKey
  const forceCollapsed = collapsible && (!inCurrentTurn || !loading)
  return { defaultOpen: isLastThoughtStreaming, forceCollapsed }
}

// 将连续的可折叠消息（思考 + 工具调用）聚合成一个分组段。
// 单条可折叠消息仍作为独立段渲染，仅当连续出现 2 条及以上时才分组，
// 这样既能压缩冗长的工具/思考序列，又不破坏单条消息的原有展示。
type Segment =
  | { type: 'single'; message: Message; idx: number }
  | { type: 'group'; messages: Message[]; firstIdx: number }

function segmentMessages(messages: Message[]): Segment[] {
  const segments: Segment[] = []
  let group: Message[] = []
  let groupStart = 0
  const flushGroup = () => {
    if (group.length >= 2) {
      segments.push({ type: 'group', messages: group, firstIdx: groupStart })
    } else if (group.length === 1) {
      segments.push({ type: 'single', message: group[0], idx: groupStart })
    }
    group = []
  }
  messages.forEach((msg, idx) => {
    if (isCollapsibleMessage(msg)) {
      if (group.length === 0) groupStart = idx
      group.push(msg)
    } else {
      flushGroup()
      segments.push({ type: 'single', message: msg, idx })
    }
  })
  flushGroup()
  return segments
}

interface ExecutionBlock {
  executionId: number
  startedAt: string
  messages: Message[]
}

function groupByExecution(messages: Message[]): ExecutionBlock[] {
  const map = new Map<number, ExecutionBlock>()
  const order: number[] = []
  for (const msg of messages) {
    if (msg.execution_id == null) continue
    let block = map.get(msg.execution_id)
    if (!block) {
      block = {
        executionId: msg.execution_id,
        startedAt: msg.created_at,
        messages: [],
      }
      map.set(msg.execution_id, block)
      order.push(msg.execution_id)
    }
    block.messages.push(msg)
    if (msg.created_at < block.startedAt) block.startedAt = msg.created_at
  }
  return order
    .sort((a, b) => a - b)
    .map((id) => map.get(id)!)
}

// 计算分组内思考与工具调用的数量，以及最后一个工具调用的摘要
function groupStats(messages: Message[]) {
  let thoughtCount = 0
  let toolCount = 0
  let lastToolSummary = ''
  for (const m of messages) {
    if (m.kind === 'agent_thought_chunk') {
      thoughtCount++
    } else if (m.role === 'tool') {
      toolCount++
      lastToolSummary = toolLabel(m)
    }
  }
  return { thoughtCount, toolCount, lastToolSummary }
}

function findLastThoughtKey(messages: Message[]): string | number | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i].kind === 'agent_thought_chunk') {
      return messages[i].id || messages[i].sequence
    }
  }
  return null
}

// 计算每条消息所属的对话轮次索引（用户消息开启新一轮）。
// 返回与 messages 等长的数组，turnOfMsg[i] = 第 i 条消息的轮次号（从 0 起）。
function computeTurnOfMsg(messages: Message[]): number[] {
  const turns: number[] = []
  let cur = -1
  for (const msg of messages) {
    if (msg.role === 'user') cur++
    turns.push(cur < 0 ? 0 : cur)
  }
  return turns
}

// 计算每个 segment 所属的轮次号，以及「每个轮次的最后一个 segment 索引」。
// segment 的轮次由其首条消息决定；turnEndSeg 是 turn -> 最后一个 segment idx 的映射。
function mapSegmentsToTurns(
  segments: Segment[],
  turnOfMsg: number[],
): { turnOfSeg: number[]; turnEndSeg: Map<number, number> } {
  const turnOfSeg: number[] = segments.map((seg) => {
    const firstMsgIdx = seg.type === 'single' ? seg.idx : seg.firstIdx
    return turnOfMsg[firstMsgIdx] ?? 0
  })
  const turnEndSeg = new Map<number, number>()
  turnOfSeg.forEach((turn, segIdx) => {
    turnEndSeg.set(turn, segIdx) // 同一轮次后出现的 segment 覆盖前者
  })
  return { turnOfSeg, turnEndSeg }
}

export default function MessageList({ messages, loading, scheduled, executions, sessionId, cwd, onRestored, hasMore, loadingMore, onLoadMore }: MessageListProps) {
  const { t } = useTranslation()
  // scheduled 模式仍用 endRef + scrollIntoView 的传统渲染；主聊天路径已改用 Virtuoso，
  // 其自动滚动由 followOutput（流式跟随）与 scrollToIndex（切会话滚底）接管。
  const endRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (scheduled) endRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, scheduled])

  if (!scheduled) {
    return (
      <PlainList
        messages={messages}
        loading={loading}
        sessionId={sessionId}
        cwd={cwd}
        onRestored={onRestored}
        hasMore={hasMore}
        loadingMore={loadingMore}
        onLoadMore={onLoadMore}
      />
    )
  }

  const allBlocks = groupByExecution(messages)
  const blocks = allBlocks.slice(-10)
  const unblocked = messages.filter((m) => m.execution_id == null)
  const scheduledLoading = !!loading
  const execStatusMap = new Map<number, Execution>()
  if (executions) {
    for (const e of executions) {
      execStatusMap.set(e.execution_id, e)
    }
  }

  return (
    <div className={styles.container}>
      {blocks.length === 0 && unblocked.length === 0 && !loading && (
        <div className={styles.empty}>
          <p>{t('scheduledTask.noTasks')}</p>
        </div>
      )}
      {unblocked.length > 0 && (
        <PlainList messages={unblocked} loading={scheduledLoading} sessionId={sessionId} cwd={cwd} onRestored={onRestored} />
      )}
      {blocks.map((block, idx) => {
        const exec = execStatusMap.get(block.executionId)
        const execStatus = exec?.status || ''
        // 执行状态已进入终态（成功/失败/跳过）时，即便 SSE 订阅仍保持开启（convState 仍为
        // streaming/connecting），也不再显示「运行中 / ...」效果——以权威的执行状态为准，
        // 避免定时/分类任务出现「状态页已成功却仍在转圈」的矛盾表现。
        const isTerminal =
          execStatus === 'success' || execStatus === 'failed' || execStatus === 'skipped'
        const index = allBlocks.length - blocks.length + idx + 1
        return (
          <ExecutionBlockView
            key={block.executionId}
            block={block}
            index={index}
            total={allBlocks.length}
            loading={scheduledLoading && idx === blocks.length - 1 && !isTerminal}
            status={execStatus}
            errorMsg={exec?.error || ''}
            sessionId={sessionId}
            cwd={cwd}
          />
        )
      })}
      {scheduledLoading && blocks.length === 0 && (
        <div className={styles.loading}>
          <span className={styles.dot} />
          <span className={styles.dot} />
          <span className={styles.dot} />
        </div>
      )}
      <div ref={endRef} />
    </div>
  )
}

function ExecutionBlockView({
  block,
  index,
  total,
  loading,
  status,
  errorMsg,
  sessionId,
  cwd,
}: {
  block: ExecutionBlock
  index: number
  total: number
  loading: boolean
  status: string
  errorMsg: string
  sessionId?: number
  cwd?: string
}) {
  const { t, i18n } = useTranslation()
  const [open, setOpen] = useState(index === total)
  const displayMessages = filterDisplay(block.messages)
  const lastThoughtKey = findLastThoughtKey(displayMessages)

  const locale = i18n.language.startsWith('zh') ? 'zh-CN' : 'en-US'
  const execStatusMap: Record<string, string> = {
    success: t('scheduledTask.statusSuccess'),
    running: t('scheduledTask.statusRunning'),
    failed: t('scheduledTask.statusFailed'),
    skipped: t('scheduledTask.statusCancelled'),
  }

  return (
    <div className={styles.executionBlock}>
      <button
        type="button"
        className={styles.executionHeader}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={styles.executionArrow}>{open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}</span>
        <span className={styles.executionTitle}>{t('scheduledTask.execution')} #{index}</span>
        <span className={styles.executionTime}>
          {new Date(block.startedAt).toLocaleString(locale)}
        </span>
        {status && (
          <span className={`${styles.execStatusBadge} ${styles[`execStatus_${status}`] || ''}`}>
            {execStatusMap[status] || status}
          </span>
        )}
        {loading && <span className={styles.executionRunning}>{t('scheduledTask.statusRunning')}</span>}
      </button>
      {open && (
        <div className={styles.executionBody}>
          <SegmentList
            messages={displayMessages}
            lastUserIdx={findLastUserIndex(displayMessages)}
            loading={loading}
            lastThoughtKey={lastThoughtKey}
            sessionId={sessionId}
            cwd={cwd}
          />
          {status === 'failed' && errorMsg && (
            <div className={styles.execError}>{t('status.error')}：{errorMsg}</div>
          )}
        </div>
      )}
    </div>
  )
}

function PlainList({
  messages,
  loading,
  sessionId,
  cwd,
  onRestored,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  messages: Message[]
  loading?: boolean
  sessionId?: number
  cwd?: string
  onRestored?: (promptText: string) => void
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
}) {
  const { t } = useTranslation()
  const displayMessages = useMemo(() => filterDisplay(messages), [messages])
  const lastUserIdx = useMemo(() => findLastUserIndex(displayMessages), [displayMessages])
  const lastThoughtKey = useMemo(() => findLastThoughtKey(displayMessages), [displayMessages])

  // Virtuoso 滚动控制：流式时由 followOutput 自动跟随底部；
  // 切换会话（sessionId 变化）时滚到最后一条。
  const virtuosoRef = useRef<VirtuosoHandle>(null)
  useEffect(() => {
    virtuosoRef.current?.scrollToIndex({ index: 'LAST', behavior: 'auto' })
  }, [sessionId])

  // 「加载更多」头部：hasMore 为 true 时显示。
  // loadingMore 时显示转圈；否则显示可点击按钮。点击/滚动到顶部均触发 onLoadMore。
  const showLoadMore = !!hasMore && displayMessages.length > 0
  const headerContent = showLoadMore ? (
    loadingMore ? (
      <div className={styles.loadingMoreBar}>
        <span className={styles.dot} />
        <span className={styles.dot} />
        <span className={styles.dot} />
      </div>
    ) : (
      <button
        type="button"
        className={styles.loadMoreBtn}
        onClick={onLoadMore}
        disabled={!onLoadMore}
      >
        {t('chat.loadMore')}
      </button>
    )
  ) : null

  return (
    <div className={styles.container}>
      {displayMessages.length === 0 && !loading && (
        <div className={styles.empty}>
          <p>{t('chat.noMessages')}</p>
        </div>
      )}
      {displayMessages.length > 0 && (
        <VirtuosoSegmentList
          ref={virtuosoRef}
          messages={displayMessages}
          lastUserIdx={lastUserIdx}
          loading={!!loading}
          lastThoughtKey={lastThoughtKey}
          sessionId={sessionId}
          cwd={cwd}
          onRestored={onRestored}
          loadingFooter={!!loading}
          headerContent={headerContent}
          onStartReached={showLoadMore && !loadingMore ? onLoadMore : undefined}
        />
      )}
      {/* Virtuoso 为空时 loading 点单独渲染（避免空列表 + Footer 的空白） */}
      {displayMessages.length === 0 && loading && (
        <div className={styles.loading}>
          <span className={styles.dot} />
          <span className={styles.dot} />
          <span className={styles.dot} />
        </div>
      )}
    </div>
  )
}

// Virtuoso 虚拟化的 segment 列表，仅用于主聊天路径（PlainList）。
// 每个虚拟行 = 一个 segment 主体（MessageBubble 或 CollapsibleGroup）+ 可选的轮次末尾 ChangesSummary。
// 高度完全动态（折叠头部 ~24px → 展开 diff 数千 px），由 react-virtuoso 内置 ResizeObserver 自动测量。
type SegmentItem = {
  seg: Segment
  // 该 segment 若处于某轮的末尾，附带该轮的文件改动汇总（无改动则 undefined）
  changes?: FileChangeItem[]
  messageId?: number
}

// 虚拟列表渲染主体。把「segment 划分 + 轮次文件改动汇总」预计算成扁平 data 数组，
// 并对 turnChanges 做跨渲染记忆化：历史轮次（已被后续用户消息封闭）的结果复用缓存，
// 仅重算当前进行中的最后一轮，把流式时 O(总轮数 × 单轮diff) 降为 O(单轮diff)。
const VirtuosoSegmentList = forwardRef<VirtuosoHandle, {
  messages: Message[]
  lastUserIdx: number
  loading: boolean
  lastThoughtKey: string | number | null
  sessionId?: number
  cwd?: string
  onRestored?: (promptText: string) => void
  loadingFooter: boolean
  headerContent?: React.ReactNode
  /** 滚动到顶部时触发（用于自动加载更早消息） */
  onStartReached?: () => void
}>(function VirtuosoSegmentList({
  messages, lastUserIdx, loading, lastThoughtKey, sessionId, cwd, onRestored, loadingFooter,
  headerContent, onStartReached,
}, ref) {
  const segments = useMemo(() => segmentMessages(messages), [messages])
  const lastMsg = messages[messages.length - 1]
  const lastKey = lastMsg ? (lastMsg.id || lastMsg.sequence) : null

  // followOutput 只在 data 条数变化时触发；流式 chunk 多数被合并进最后一条消息
  // （条数不变、仅高度增长），需要手动跟随：用户在底部附近时，内容一变就滚到底。
  const innerRef = useRef<VirtuosoHandle>(null)
  useImperativeHandle(ref, () => innerRef.current!, [])
  const atBottomRef = useRef(true)
  const lastContentLen = lastMsg ? lastMsg.content.length : 0
  useEffect(() => {
    if (!loading || !atBottomRef.current) return
    innerRef.current?.scrollToIndex({ index: 'LAST', align: 'end', behavior: 'auto' })
  }, [loading, lastKey, lastContentLen, messages.length])

  const { turnOfSeg, turnEndSeg } = useMemo(
    () => mapSegmentsToTurns(segments, computeTurnOfMsg(messages)),
    [segments, messages],
  )

  // turnChanges 记忆化：以「每轮首条用户消息的 sequence」作为该轮的稳定缓存 key。
  // sequence 单调且不变，历史轮次一旦封闭就不再变化，可安全复用。
  // 缓存结构：{ cwd, entries: Map<turnKey, {changes, msgId}>, turnToKey: Map<turn, turnKey> }
  const cacheRef = useRef<{ cwd: string; entries: Map<number, { changes?: FileChangeItem[]; msgId?: number }>; turnToKey: Map<number, number> }>(
    { cwd: '', entries: new Map(), turnToKey: new Map() },
  )

  const turnChanges = useMemo(() => {
    const cache = cacheRef.current
    // cwd 变化导致相对路径改变，整体失效重来
    if (cache.cwd !== (cwd || '')) {
      cache.cwd = cwd || ''
      cache.entries.clear()
      cache.turnToKey.clear()
    }
    if (!cwd) return { changesMap: new Map<number, FileChangeItem[]>(), msgIdMap: new Map<number, number>() }

    const turnOfMsg = computeTurnOfMsg(messages)
    const byTurn = new Map<number, Message[]>()
    messages.forEach((msg, i) => {
      const turn = turnOfMsg[i]
      const arr = byTurn.get(turn)
      if (arr) arr.push(msg)
      else byTurn.set(turn, [msg])
    })

    const changesMap = new Map<number, FileChangeItem[]>()
    const msgIdMap = new Map<number, number>()
    for (const [turn, msgs] of byTurn) {
      // 该轮首条用户消息的 sequence 作为稳定 key
      const firstUser = msgs.find((m) => m.role === 'user')
      const turnKey = firstUser ? firstUser.sequence : -1 - turn // 无用户消息的兜底 key

      // 缓存命中（历史封闭轮次）：直接复用，跳过昂贵的 aggregateChanges（含 LCS diff）
      const prevKey = cache.turnToKey.get(turn)
      if (prevKey === turnKey && cache.entries.has(turnKey)) {
        const cached = cache.entries.get(turnKey)!
        if (cached.changes && cached.changes.length > 0) {
          changesMap.set(turn, cached.changes)
          if (cached.msgId != null) msgIdMap.set(turn, cached.msgId)
        }
        continue
      }

      // 未命中（当前进行中的轮次或首次计算）：执行重算并写入缓存
      cache.turnToKey.set(turn, turnKey)
      const changes = aggregateChanges(msgs, cwd)
      let msgId: number | undefined
      if (changes.length > 0) {
        changesMap.set(turn, changes)
        for (let i = msgs.length - 1; i >= 0; i--) {
          if (msgs[i].kind === 'tool_call_update' && msgs[i].id) { msgId = msgs[i].id; break }
        }
        if (msgId != null) msgIdMap.set(turn, msgId)
      }
      cache.entries.set(turnKey, { changes, msgId })
    }
    return { changesMap, msgIdMap }
  }, [messages, cwd])

  // 把 segments + turnChanges 合成 Virtuoso 的扁平 data 数组
  const items: SegmentItem[] = useMemo(() => {
    return segments.map((seg, segIdx) => {
      const turn = turnOfSeg[segIdx]
      const isTurnEnd = turnEndSeg.get(turn) === segIdx
      const changes = isTurnEnd ? turnChanges.changesMap.get(turn) : undefined
      const messageId = isTurnEnd ? turnChanges.msgIdMap.get(turn) : undefined
      return { seg, changes, messageId }
    })
  }, [segments, turnOfSeg, turnEndSeg, turnChanges])

  return (
    <Virtuoso
      ref={innerRef}
      data={items}
      computeItemKey={(_, item) => {
        const seg = item.seg
        return seg.type === 'single'
          ? `s-${seg.message.id || seg.message.sequence}`
          : `g-${seg.firstIdx}`
      }}
      // 流式追加 data 时：用户在底部附近才自动跟随，向上看历史时不打断
      followOutput={(isAtBottom) => (isAtBottom ? 'auto' : false)}
      // 记录是否位于底部附近，供上方手动跟随 effect 判断（离底不打断用户回看）
      atBottomStateChange={(atBottom) => { atBottomRef.current = atBottom }}
      atBottomThreshold={80}
      // 列表初始即滚到底部（首次进入会话）
      initialTopMostItemIndex={items.length - 1}
      // 滚动到顶部时自动加载更早消息（onStartReached 由 PlainList 仅在可加载时传入）
      startReached={onStartReached}
      style={{ height: '100%' }}
      itemContent={(index, item) => {
        const seg = item.seg
        let body: React.ReactNode
        if (seg.type === 'single') {
          const msg = seg.message
          const key = msg.id || msg.sequence
          const { defaultOpen, forceCollapsed } = bubbleCollapseState(
            msg, seg.idx, lastUserIdx, loading, lastThoughtKey, key,
          )
          const isStreamingAssistant =
            !!loading &&
            msg.kind === 'agent_message_chunk' &&
            key === lastKey
          body = (
            <MessageBubble
              message={msg}
              defaultOpen={defaultOpen}
              forceCollapsed={forceCollapsed}
              streaming={isStreamingAssistant}
              sessionId={sessionId}
              cwd={cwd}
              canRestore={msg.role === 'user' && !!sessionId}
              onRestored={onRestored}
            />
          )
        } else {
          const isLastSegment = index === segments.length - 1
          const inCurrentTurn = seg.firstIdx > lastUserIdx
          body = (
            <CollapsibleGroup
              messages={seg.messages}
              inCurrentTurn={inCurrentTurn}
              isLastSegment={isLastSegment}
              loading={loading}
              sessionId={sessionId}
              cwd={cwd}
            />
          )
        }
        return (
          <>
            {body}
            {item.changes && item.changes.length > 0 && (
              <ChangesSummary
                changes={item.changes}
                sessionId={sessionId}
                cwd={cwd}
                messageId={item.messageId}
              />
            )}
          </>
        )
      }}
      // react-virtuoso 对 optional props 仅在「键存在于 props」时才发布；
      // 若 loading 结束后省略 components，上次的 Footer 会残留（... 一直转）。
      // 必须始终传入 components：有 Footer/Header 时挂上，否则传 {} 清掉。
      // 不要传 components={undefined}（会把内部默认覆盖坏）。
      components={
        {
          ...(headerContent ? { Header: () => headerContent } : {}),
          ...(loadingFooter
            ? {
                Footer: () => (
                  <div className={styles.loading}>
                    <span className={styles.dot} />
                    <span className={styles.dot} />
                    <span className={styles.dot} />
                  </div>
                ),
              }
            : {}),
        }
      }
    />
  )
})

// 根据 segment 列表渲染：单条消息走 MessageBubble，连续可折叠消息走 CollapsibleGroup
function SegmentList({
  messages,
  lastUserIdx,
  loading,
  lastThoughtKey,
  sessionId,
  cwd,
  onRestored,
}: {
  messages: Message[]
  lastUserIdx: number
  loading: boolean
  lastThoughtKey: string | number | null
  sessionId?: number
  cwd?: string
  onRestored?: (promptText: string) => void
}) {
  const segments = segmentMessages(messages)
  const lastMsg = messages[messages.length - 1]
  const lastKey = lastMsg ? (lastMsg.id || lastMsg.sequence) : null

  // 对话轮次检测：按用户消息切分轮次，找出每个轮次的末尾 segment。
  const { turnOfSeg, turnEndSeg } = useMemo(
    () => mapSegmentsToTurns(segments, computeTurnOfMsg(messages)),
    [segments, messages],
  )

  // 每轮的文件改动汇总（cwd 缺失时无法计算相对路径，跳过）。
  // 同时记录该轮快照消息的 id（toolCallId 含 snapshot- 的 tool_call_update），供撤销使用。
  const turnChanges = useMemo(() => {
    const changesMap = new Map<number, ReturnType<typeof aggregateChanges>>()
    const msgIdMap = new Map<number, number>()
    if (!cwd) return { changesMap, msgIdMap }
    const turnOfMsg = computeTurnOfMsg(messages)
    const byTurn = new Map<number, Message[]>()
    messages.forEach((msg, i) => {
      const turn = turnOfMsg[i]
      const arr = byTurn.get(turn)
      if (arr) arr.push(msg)
      else byTurn.set(turn, [msg])
    })
    for (const [turn, msgs] of byTurn) {
      const changes = aggregateChanges(msgs, cwd)
      if (changes.length > 0) {
        changesMap.set(turn, changes)
        // 找到该轮的快照消息 id（最后一条含 diff 的 tool_call_update）
        for (let i = msgs.length - 1; i >= 0; i--) {
          const m = msgs[i]
          if (m.kind === 'tool_call_update' && m.id) {
            msgIdMap.set(turn, m.id)
            break
          }
        }
      }
    }
    return { changesMap, msgIdMap }
  }, [messages, cwd])

  return (
    <>
      {segments.map((seg, segIdx) => {
        // 计算该 segment 的主体（单条消息气泡 或 可折叠分组）
        let body: React.ReactNode
        if (seg.type === 'single') {
          const msg = seg.message
          const key = msg.id || msg.sequence
          const { defaultOpen, forceCollapsed } = bubbleCollapseState(
            msg, seg.idx, lastUserIdx, loading, lastThoughtKey, key,
          )
          const isStreamingAssistant =
            !!loading &&
            msg.kind === 'agent_message_chunk' &&
            key === lastKey
          body = (
            <MessageBubble
              message={msg}
              defaultOpen={defaultOpen}
              forceCollapsed={forceCollapsed}
              streaming={isStreamingAssistant}
              sessionId={sessionId}
              cwd={cwd}
              canRestore={msg.role === 'user' && !!sessionId}
              onRestored={onRestored}
            />
          )
        } else {
          const isLastSegment = segIdx === segments.length - 1
          const inCurrentTurn = seg.firstIdx > lastUserIdx
          body = (
            <CollapsibleGroup
              messages={seg.messages}
              inCurrentTurn={inCurrentTurn}
              isLastSegment={isLastSegment}
              loading={loading}
              sessionId={sessionId}
              cwd={cwd}
            />
          )
        }

        // 该轮次末尾追加文件改动汇总卡片（仅有改动时显示）
        const turn = turnOfSeg[segIdx]
        const isTurnEnd = turnEndSeg.get(turn) === segIdx
        const changes = isTurnEnd ? turnChanges.changesMap.get(turn) : undefined
        const messageId = isTurnEnd ? turnChanges.msgIdMap.get(turn) : undefined

        return (
          <Fragment key={`seg-${segIdx}`}>
            {body}
            {changes && changes.length > 0 && (
              <ChangesSummary
                changes={changes}
                sessionId={sessionId}
                cwd={cwd}
                messageId={messageId}
              />
            )}
          </Fragment>
        )
      })}
    </>
  )
}

// 连续思考 + 工具调用消息的折叠分组。
// 流式中（本轮末尾分组）默认展开以展示实时进度；
// 助手返回回复或流式结束后自动压缩为一行摘要（如「思考中 ×2 · 工具调用 ×3」）。
function CollapsibleGroup({
  messages,
  inCurrentTurn,
  isLastSegment,
  loading,
  sessionId,
  cwd,
}: {
  messages: Message[]
  inCurrentTurn: boolean
  isLastSegment: boolean
  loading: boolean
  sessionId?: number
  cwd?: string
}) {
  const { t } = useTranslation()

  const forceCollapsed = !inCurrentTurn || !loading
  const streaming = loading && inCurrentTurn && isLastSegment
  const defaultOpen = streaming

  const [open, setOpen] = useState(defaultOpen && !forceCollapsed)
  useEffect(() => {
    if (forceCollapsed) {
      setOpen(false)
    } else {
      setOpen(defaultOpen)
    }
  }, [defaultOpen, forceCollapsed])

  const { thoughtCount, toolCount, lastToolSummary } = groupStats(messages)
  const parts: string[] = []
  if (thoughtCount > 0) parts.push(`${t('chat.thinking')} ×${thoughtCount}`)
  if (toolCount > 0) parts.push(`${t('chat.toolCall')} ×${toolCount}`)
  const summary = parts.join(' · ') || t('chat.toolCall')

  const localLastThoughtKey = findLastThoughtKey(messages)

  return (
    <div className={styles.groupContainer}>
      <div
        className={styles.groupHeader}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={styles.groupToggle}>{open ? <ChevronDown size={12} /> : <ChevronRight size={12} />}</span>
        <span className={styles.groupSummary}>
          {summary}
          {toolCount > 0 && lastToolSummary && (
            <span className={styles.groupLastTool}>
              {' · '}
              {lastToolSummary.startsWith('chat.') ? t(lastToolSummary) : lastToolSummary}
            </span>
          )}
        </span>
        {streaming && !forceCollapsed && <span className={styles.groupSpinner} />}
        <span className={styles.groupToggleHint}>
          {open ? t('chat.collapse') : t('chat.expand')}
        </span>
      </div>
      {open && (
        <div className={styles.groupBody}>
          {messages.map((msg) => {
            const key = msg.id || msg.sequence
            const isLocalLastThought =
              msg.kind === 'agent_thought_chunk' && key === localLastThoughtKey
            const innerDefaultOpen = streaming && isLocalLastThought
            return (
              <MessageBubble
                key={key}
                message={msg}
                defaultOpen={innerDefaultOpen}
                forceCollapsed={false}
                sessionId={sessionId}
                cwd={cwd}
                flush
              />
            )
          })}
        </div>
      )}
    </div>
  )
}
