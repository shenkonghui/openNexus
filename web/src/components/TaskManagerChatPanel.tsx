import { useState, useRef, useCallback, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import {
  createSession, updateSessionTitle, setConfigOption, setSessionMode,
  listSkills, listModes, listCommands, listConfigOptions,
  respondPermission, getSession, listMessages,
  getLatestSessionByWorkspace, deleteSession,
} from '../api/sessions'
import { probeAgentConfigs, listAgentCommands, listAgentModes, listAgents } from '../api/agents'
import { streamPrompt, isTimeoutError } from '../api/sse'
import { parsePermissionRequest } from '../utils/permission'
import { Eraser } from 'lucide-react'
import type { Agent, Message, Session, AgentCommand, ConfigOption, ConfigOptionValue, SessionMode, AgentSkill, PermissionRequestPayload } from '../types'
import { archiveTask, upsertTask, genTaskId, startTaskManager, type TaskManagerTask } from '../api/taskmanager'
import type { ConvState } from './ConvStatusBar'
import type { PanelCtx } from '../modes/types'
import ChatPanel from '../modes/ChatPanel'
import styles from './TaskManagerChatPanel.module.css'

interface Props {
  agents: Agent[]
  workspaceId: number
  cwd: string
  /** 首选 agent 类型；为空则取 agents[0] */
  defaultAgentType?: string
  /** 指定需恢复的编排管理会话 DB 主键（从侧边栏点击编排记录进入时传入）；
   *  缺省时回退到 tasks.json 登记的 parent_session_id。 */
  restoreSessionId?: number
  /** 当前任务列表：用于 @task:<id> 引用把消息直发到对应任务会话 */
  tasks?: TaskManagerTask[]
  /** 多任务模式下鼠标焦点所在的任务：变化时自动把输入框的 @task 引用切到该任务 */
  focusTaskId?: string | null
  /** Agent 改动 tasks.json 后触发（通常刷新编排页任务列表） */
  onTaskChanged: () => void
}

// @task:<id> 任务引用（输入框 @ 菜单选任务插入）：可出现在消息任意位置，
// 发送时剔除引用标记、将剩余内容直发到该任务会话；多个引用则逐个直发。
const TASK_MENTION_RE = /@task:([^\s(]+)(?:\([^)]*\))?/g

// 输入框开头的 @task 引用（含连续多个）：鼠标焦点切换任务窗口时仅替换这部分，保留用户正文
const LEADING_TASK_MENTION_RE = /^\s*(?:@task:[^\s(]+(?:\([^)]*\))?\s*)+/

// @archive-task:<id> 归档引用（@ 菜单「归档任务」分类插入）：id 为 * 表示归档全部任务
const ARCHIVE_MENTION_RE = /@archive-task:([^\s(]+)(?:\([^)]*\))?/g

// @task-create 标记（/task-create 命令插入）：发送时解析剩余文本作为任务详情直接创建
const TASK_CREATE_RE = /@task-create\b/

// @run-task:<id> 运行引用（/task-run 命令插入）：id 为 * 表示启动全部待执行任务
const RUN_MENTION_RE = /@run-task:([^\s(]+)(?:\([^)]*\))?/g

// 生成命令名里的标题注释：去掉空白与括号（避免破坏 \S+ 命令解析），过长截断
function taskTitleNote(title: string): string {
  return title.replace(/[\s()（）]/g, '').slice(0, 20)
}

// 编排工具调用特征：MCP 工具名或直接读写 tasks.json。命中即认为任务定义可能已变更。
const TM_TOOL_RE = /(create|update|delete|start|stop)_task|set_max_parallel|tasks\.json/i

// 判断消息是否为编排相关的工具调用（tool_call / tool_call_update）。
function isTMToolMessage(msg: Message): boolean {
  if (msg.kind !== 'tool_call' && msg.kind !== 'tool_call_update') return false
  return TM_TOOL_RE.test(msg.raw_json || '') || TM_TOOL_RE.test(msg.content || '')
}

// 注入到首条 prompt 前的系统引导，告知 agent 其职责与可用工具。
function buildSystemPrelude(): string {
  return [
    '你是任务管理助手。请根据用户需求管理当前工作区的任务管理。',
    '编排工具由 opennexus-task 这个 MCP 服务器提供，已自动注入会话，请使用以下工具操作任务：',
    '- opennexus-task_list_tasks：列出任务现状（先了解再操作）',
    '- opennexus-task_create_task：新增任务（title/detail 必填，即发给 agent 的 prompt；自动生成 id 并置 pending；priority 可选 p0/p1/p2，默认 p1）',
    '- opennexus-task_update_task：改任务字段（task_id 必填 + 要改的字段，含 priority）',
    '- opennexus-task_delete_task：删除任务（task_id 必填，支持 glob 模式如 t12* 批量删除，默认开启，传 glob=false 关闭）',
    '- opennexus-task_start_task：启动任务（task_id 留空=启动全部待执行，支持 glob 模式批量启动）',
    '- opennexus-task_stop_task：停止任务（task_id 留空=停止全部运行中，支持 glob 模式批量停止）',
    '- opennexus-task_send_prompt：向指定任务已有会话发送新 prompt，在原上下文继续',
    '- opennexus-task_set_max_parallel：调整并发上限（1=串行，1~16）',
    '若工具列表中没有 opennexus-task_ 前缀的名称，改用不带前缀的同名工具（list_tasks/create_task 等）。',
    '所有工具都需要 workspace_id 参数。',
    `当前工作区 workspace_id：__WORKSPACE_ID__`,
    '若工具列表里看不到上述名称，直接告知用户编排工具不可用；不要尝试直接编辑 tasks.json（该文件不在当前目录，而在工作区管理数据目录，直接改写不会生效）。',
    '任务启动后会基于 git worktree 隔离执行；用户可在编排页点击任务查看其会话并继续对话。',
    '完成后用一句话总结改动。',
    '',
  ].join('\n')
}

/**
 * TaskManagerChatPanel：嵌入编排页右栏的 AI 管理对话面板。
 * 用户用自然语言描述需求，Agent 通过编排 MCP 工具（或读写 tasks.json）
 * 增删改任务、启停、调整并发，完成后通过 onTaskChanged 通知编排页刷新任务列表。
 * 单个任务的对话在其独立的会话页（与普通任务页一致）进行，不在本面板内。
 *
 * 直接复用任务页的 ChatPanel（含配置栏/状态条/权限弹窗），构造最小 PanelCtx。
 */
export default function TaskManagerChatPanel({
  agents, workspaceId, cwd, defaultAgentType, restoreSessionId, tasks, focusTaskId, onTaskChanged,
}: Props) {
  const { t } = useTranslation()

  // 会话与消息
  const [session, setSession] = useState<Session | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  // 输入框受控值：多任务模式点击任务窗口时自动注入/切换 @task 引用
  const [inputText, setInputText] = useState('')
  // 历史消息分页：hasMore 表示还有更早的消息可加载
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [conv, setConv] = useState<ConvState>('idle')
  const [error, setError] = useState('')

  // 配置：无会话时用 probeConfigs；有会话时用 configOptions（会话级模型/模式等）
  // 初始值优先级：defaultAgentType > localStorage(上次使用) > agents[0]
  const LAST_AGENT_KEY = 'opennexus.lastAgent'
  const [selectedAgent, setSelectedAgent] = useState(
    defaultAgentType
    || (() => { try { return localStorage.getItem(LAST_AGENT_KEY) || '' } catch { return '' } })()
    || agents[0]?.type
    || '',
  )

  // agents 异步加载完成后补全 selectedAgent（刷新后 agents 初始为空）
  useEffect(() => {
    if (selectedAgent || agents.length === 0) return
    let saved = ''
    try { saved = localStorage.getItem(LAST_AGENT_KEY) || '' } catch { /* ignore */ }
    if (saved && agents.some((a) => a.type === saved)) {
      setSelectedAgent(saved)
    } else {
      setSelectedAgent(agents[0]?.type || '')
    }
  }, [agents, selectedAgent])

  // 切换 agent 时持久化
  const handleSelectAgent = useCallback((val: string) => {
    setSelectedAgent(val)
    try { localStorage.setItem(LAST_AGENT_KEY, val) } catch { /* ignore */ }
  }, [])
  const [selectedModel, setSelectedModel] = useState('')
  const [probeConfigs, setProbeConfigs] = useState<ConfigOption[]>([])
  const [configOptions, setConfigOptions] = useState<ConfigOption[]>([])
  const [currentModeId, setCurrentModeId] = useState('')
  const [probing, setProbing] = useState(false)
  const [commands, setCommands] = useState<AgentCommand[]>([])
  const [modes, setModes] = useState<SessionMode[]>([])
  const [skills, setSkills] = useState<AgentSkill[]>([])
  // Agent·模型合并下拉的显示过滤正则（config.yaml agents.selector.filters），
  // 与新建任务页/会话详情页同一套规则，避免任务助手下拉显示内容不一致。
  const [selectorFilters, setSelectorFilters] = useState<string[]>([])
  // 各 agent 探测到的模型列表：供合并下拉展示全部 agent·模型组合（参考 ChatPage 新建任务页）
  const [agentModelsMap, setAgentModelsMap] = useState<Record<string, ConfigOptionValue[]>>({})
  // 合并下拉选中「另一 agent 的某模型」时暂存目标模型，待该 agent 探测完成后应用
  const pendingModelRef = useRef('')

  useEffect(() => {
    listAgents().then((r) => setSelectorFilters(r.data.selector_filters || [])).catch(() => {})
  }, [])

  // 并行探测全部 agent 的模型列表，供合并下拉展示全部组合。
  // 无论有无会话都执行：有会话时 ChatPanel 会用 agentModelsMap 补全会话级模型列表，
  // 避免会话创建时 agent 未完全初始化导致模型不全。
  // probeAgentConfigs 有前端缓存，不会重复请求。
  useEffect(() => {
    if (agents.length === 0) return
    let alive = true
    for (const a of agents) {
      probeAgentConfigs(a.type)
        .then((r) => {
          if (!alive) return
          const modelOpt = (r.data.config_options || []).find((o) => o.category === 'model')
          setAgentModelsMap((prev) => ({ ...prev, [a.type]: modelOpt?.options || [] }))
        })
        .catch(() => {
          // 探测失败：记为空列表，下拉中退化为 agent 级单项（使用默认模型）
          if (alive) setAgentModelsMap((prev) => (a.type in prev ? prev : { ...prev, [a.type]: [] }))
        })
    }
    return () => { alive = false }
  }, [agents])

  // 权限
  const [pendingPermission, setPendingPermission] = useState<PermissionRequestPayload | null>(null)
  const [permissionResponding, setPermissionResponding] = useState(false)
  const permissionQueueRef = useRef<PermissionRequestPayload[]>([])
  // /task 直发的权限请求来自任务会话而非助手会话：request_id → 任务 db_session_id 路由表
  const permissionSessionRef = useRef<Map<string, number>>(new Map())

  // /task 直发的后台流：taskId → AbortController（避免同一任务并发直发，卸载时统一中止）
  const taskStreamsRef = useRef<Map<string, AbortController>>(new Map())

  const abortRef = useRef<AbortController | null>(null)
  const pendingMessagesRef = useRef<Message[]>([])
  const flushRafRef = useRef<number | null>(null)
  const mountedRef = useRef(true)
  // 流式过程中检测到编排工具调用时防抖刷新任务列表，使左栏实时同步，
  // 而非等整轮对话结束才刷新（agent 建完任务后可能继续长时间执行其他步骤）。
  const refreshTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  function scheduleTaskRefresh() {
    if (refreshTimerRef.current) clearTimeout(refreshTimerRef.current)
    refreshTimerRef.current = setTimeout(() => {
      refreshTimerRef.current = null
      if (mountedRef.current) onTaskChanged()
    }, 400)
  }

  // ===== 消息批量 flush（照搬 ChatPage，避免每个 chunk 一次 re-render）=====
  const flushMessages = useCallback(() => {
    flushRafRef.current = null
    const batch = pendingMessagesRef.current
    if (batch.length === 0) return
    pendingMessagesRef.current = []
    setMessages((prev) => {
      const seen = new Set(prev.filter((m) => m.sequence > 0).map((m) => m.sequence))
      const additions: Message[] = []
      for (const msg of batch) {
        if (msg.sequence > 0) {
          if (seen.has(msg.sequence)) continue
          seen.add(msg.sequence)
        }
        additions.push(msg)
      }
      return additions.length > 0 ? [...prev, ...additions] : prev
    })
  }, [])

  const enqueueMessage = useCallback((msg: Message) => {
    pendingMessagesRef.current.push(msg)
    if (flushRafRef.current == null) {
      flushRafRef.current = requestAnimationFrame(flushMessages)
    }
  }, [flushMessages])

  // ===== 权限队列 =====
  const enqueuePermission = useCallback((req: PermissionRequestPayload) => {
    setPendingPermission((cur) => {
      if (!cur) return req
      permissionQueueRef.current.push(req)
      return cur
    })
  }, [])

  const clearPermissions = useCallback(() => {
    permissionQueueRef.current = []
    setPendingPermission(null)
  }, [])

  const handlePermissionRespond = useCallback(async (optionId: string) => {
    if (!pendingPermission) return
    // /task 直发的权限请求需回给对应任务会话；否则回给助手会话
    const sid = permissionSessionRef.current.get(pendingPermission.request_id) ?? session?.id
    if (!sid) return
    setPermissionResponding(true)
    try {
      await respondPermission(sid, pendingPermission.request_id, optionId)
    } catch { /* ignore */ }
    permissionSessionRef.current.delete(pendingPermission.request_id)
    setPermissionResponding(false)
    const next = permissionQueueRef.current.shift() || null
    setPendingPermission(next)
  }, [session, pendingPermission])

  const handlePermissionCancel = useCallback(() => {
    if (pendingPermission) {
      const sid = permissionSessionRef.current.get(pendingPermission.request_id) ?? session?.id
      if (sid) {
        respondPermission(sid, pendingPermission.request_id, '', true).catch(() => {})
      }
      permissionSessionRef.current.delete(pendingPermission.request_id)
    }
    const next = permissionQueueRef.current.shift() || null
    setPendingPermission(next)
  }, [session, pendingPermission])

  // ===== 无会话：按 agent 探测配置/命令/模式（复刻 ChatPage 新建页）=====
  useEffect(() => {
    if (!selectedAgent || session) return
    let alive = true
    setProbing(true)
    probeAgentConfigs(selectedAgent)
      .then((r) => {
        if (!alive) return
        const opts = r.data.config_options || []
        const modelOpt = opts.find((o) => o.category === 'model')
        // 合并下拉跨 agent 选中的目标模型优先于探测默认值（仅在该模型确实存在时）
        const pending = pendingModelRef.current
        pendingModelRef.current = ''
        if (pending && modelOpt?.options.some((v) => v.value === pending)) {
          setProbeConfigs(opts.map((o) => (o.category === 'model' ? { ...o, current_value: pending } : o)))
          setSelectedModel(pending)
        } else {
          setProbeConfigs(opts)
          setSelectedModel(modelOpt?.current_value || '')
        }
      })
      .catch(() => { if (alive) { setProbeConfigs([]); setSelectedModel('') } })
      .finally(() => { if (alive) setProbing(false) })
    return () => { alive = false }
  }, [selectedAgent, session])

  useEffect(() => {
    if (!selectedAgent || session) return
    listAgentCommands(selectedAgent).then((r) => setCommands(r.data.commands || [])).catch(() => setCommands([]))
    listAgentModes(selectedAgent).then((r) => setModes(r.data.modes || [])).catch(() => setModes([]))
  }, [selectedAgent, session])

  // ===== 有会话：拉取会话级 config/modes/commands/skills（否则配置栏只剩只读 Agent）=====
  useEffect(() => {
    if (!session) {
      setConfigOptions([])
      setSkills([])
      return
    }
    let alive = true
    listConfigOptions(session.id).then((r) => {
      if (!alive) return
      const opts = r.data.config_options || []
      setConfigOptions(opts)
      const modelOpt = opts.find((o) => o.category === 'model')
      if (modelOpt) setSelectedModel(modelOpt.current_value || '')
    }).catch(() => { if (alive) setConfigOptions([]) })
    listModes(session.id).then((r) => {
      if (!alive) return
      const modeList = r.data.modes || []
      setModes(modeList)
      if (modeList.length > 0) setCurrentModeId((prev) => prev || modeList[0].id)
    }).catch(() => { if (alive) setModes([]) })
    listCommands(session.id).then((r) => { if (alive) setCommands(r.data.commands || []) }).catch(() => { if (alive) setCommands([]) })
    listSkills(session.id).then((r) => { if (alive) setSkills(r.data.skills || []) }).catch(() => { if (alive) setSkills([]) })
    return () => { alive = false }
  }, [session])

  // 卸载清理
  useEffect(() => {
    mountedRef.current = true
    const taskStreams = taskStreamsRef.current
    return () => {
      mountedRef.current = false
      if (flushRafRef.current != null) cancelAnimationFrame(flushRafRef.current)
      if (refreshTimerRef.current != null) clearTimeout(refreshTimerRef.current)
      abortRef.current?.abort()
      // 中止所有 /task 直发的后台流（任务会话本身继续执行，仅断开本面板的接收）
      for (const ac of taskStreams.values()) ac.abort()
      taskStreams.clear()
    }
  }, [])

  // 恢复已有的任务管理会话：优先用侧边栏点击传入的 restoreSessionId，否则回退到
  // 该工作区最近的一条会话（通过 /sessions/latest 精确按 workspace 查询）。
  // 使任务对话在重新进入任务页时可见历史记录，而非每次都新建会话导致旧对话"丢失"。
  // 恢复期间置 conv='connecting' 阻止发送，避免恢复未完成时用户发送触发新建会话，
  // 从而保证“一个工作区只有一个任务助手管理会话”。
  const restoredKeyRef = useRef<string>('')
  useEffect(() => {
    if (!workspaceId) return
    const key = `${workspaceId}:${restoreSessionId ?? ''}`
    if (restoredKeyRef.current === key) return
    restoredKeyRef.current = key
    let alive = true
    // 恢复是否已走完（含成功/失败/超时）；未走完就被 cleanup 中断时需回滚去重 key，
    // 否则 StrictMode 双挂载下第二次 effect 会因 key 命中直接 return，conv 永远卡在 connecting。
    let done = false
    setConv('connecting')
    // 避免后端接口异常/缓慢时恢复状态一直卡“等待响应”
    const timeoutId = setTimeout(() => {
      if (!alive) return
      alive = false
      done = true
      setConv('idle')
      setError(t('common.timeout'))
    }, 10000)
    ;(async () => {
      try {
        let targetId = restoreSessionId
        if (!targetId) {
          // 精确查询该 workspace 最近一条管理会话（source=orchestration，不复用普通对话）
          const latest = await getLatestSessionByWorkspace(workspaceId, 'orchestration')
          targetId = latest.data?.id
        }
        if (!targetId) return
        const [sResp, mResp] = await Promise.all([getSession(targetId), listMessages(targetId)])
        if (!alive) return
        setSession(sResp.data)
        setMessages(mResp.data.messages || [])
        setHasMore(!!mResp.data.has_more)
        handleSelectAgent(sResp.data.agent_type)
      } catch { /* 会话可能已删除：忽略，保持空会话，允许重新新建 */ }
      finally {
        clearTimeout(timeoutId)
        if (alive) {
          done = true
          setConv('idle')
        }
      }
    })()
    return () => {
      alive = false
      clearTimeout(timeoutId)
      // 恢复被中断（如 StrictMode 卸载重挂）：回滚 key 并复位状态，允许下次重新恢复
      if (!done && restoredKeyRef.current === key) {
        restoredKeyRef.current = ''
        setConv('idle')
      }
    }
  }, [workspaceId, restoreSessionId])

  // ===== 加载更早的历史消息（向前翻页） =====
  const handleLoadMore = useCallback(async () => {
    if (!session || loadingMore || !hasMore || messages.length === 0) return
    const beforeSeq = messages[0].sequence
    setLoadingMore(true)
    try {
      const resp = await listMessages(session.id, { before: beforeSeq })
      const older = resp.data.messages || []
      setHasMore(!!resp.data.has_more)
      if (older.length > 0) {
        setMessages((prev) => [...older, ...prev])
      }
    } catch { /* 忽略：用户可重试 */ }
    finally { setLoadingMore(false) }
  }, [session, loadingMore, hasMore, messages])

  // ===== 清空：删除当前管理会话并重置状态，下次发送强制新建会话 =====
  // forceNew 标记：跳过 handleSend 的 latest 复用查询，保证真正开新会话，
  // 而不是被"一个工作区只复用一条管理会话"逻辑再次命中旧会话。
  const forceNewRef = useRef(false)
  // 弃置当前管理会话并重置状态（清空 / 会话中切换 agent 共用）
  async function discardSession() {
    abortRef.current?.abort()
    abortRef.current = null
    clearPermissions()
    const old = session
    setSession(null)
    setMessages([])
    setHasMore(false)
    setError('')
    setConv('idle')
    setConfigOptions([])
    setCurrentModeId('')
    forceNewRef.current = true
    if (old) {
      // 删除旧会话，避免 latest 查询/重进页面时又恢复它；失败不阻断（forceNew 仍生效）
      try { await deleteSession(old.id) } catch { /* ignore */ }
    }
  }
  async function handleClear() {
    if (!session && messages.length === 0) return
    if (!window.confirm(t('taskmanager.clearConfirm'))) return
    await discardSession()
  }

  // @task 引用候选：列出全部任务（含未启动），desc 附状态便于辨认；
  // 直发到未启动（无会话）的任务时由 handleSendToTask 报错提示。
  // @task 引用候选：列出全部任务（含未启动），desc 附状态便于辨认；
  // 直发到未启动（无会话）的任务时由 handleSendToTask 报错提示。
  const taskMentions = useMemo(
    () => (tasks || []).map((tk) => ({
      id: tk.id,
      title: tk.title,
      desc: `${tk.id} · ${t(`taskmanager.status_${tk.status}`)}`,
    })),
    [tasks, t],
  )

  // 多任务模式焦点联动：
  // - focusTaskId 有值（点击了任务窗口）：把输入框开头的 @task 引用切到该任务
  // - focusTaskId 从有值变 null（点击了助手区域）：移除开头 @task 引用，恢复与助手对话
  // 保留用户已输入的正文。任务列表通过 ref 读取，避免轮询刷新 tasks 时重复注入。
  const tasksRef = useRef(tasks)
  tasksRef.current = tasks
  const prevFocusRef = useRef<string | null | undefined>(undefined)
  useEffect(() => {
    const hadFocus = !!prevFocusRef.current
    prevFocusRef.current = focusTaskId
    if (focusTaskId) {
      const task = (tasksRef.current || []).find((tk) => tk.id === focusTaskId)
      if (!task) return
      const note = taskTitleNote(task.title)
      const mention = `@task:${task.id}${note ? `(${note})` : ''} `
      setInputText((prev) => mention + prev.replace(LEADING_TASK_MENTION_RE, ''))
    } else if (hadFocus) {
      // 从任务窗口焦点切回助手：移除开头 @task 引用
      setInputText((prev) => prev.replace(LEADING_TASK_MENTION_RE, ''))
    }
  }, [focusTaskId])

  // 追加一条仅本地展示的消息（负 id、sequence=0，不入库，刷新后消失）
  function appendLocalMessage(role: 'user' | 'assistant', kind: string, content: string) {
    const msg: Message = {
      id: -Date.now() - Math.floor(Math.random() * 1000), session_id: '', role,
      kind, content, raw_json: '', sequence: 0,
      execution_id: null, created_at: new Date().toISOString(),
    }
    setMessages((prev) => [...prev, msg])
  }

  // 后台直发到任务会话：不占用助手对话的 conv 状态（输入框保持可用），
  // 实时输出由「多任务模式」网格窗口 / 任务会话页通过 /stream 订阅呈现。
  async function handleSendToTask(taskId: string, body: string) {
    setError('')
    const task = (tasks || []).find((tk) => tk.id === taskId)
    if (!task) { setError(t('taskmanager.taskCmdNotFound', { id: taskId })); return }
    if (!task.db_session_id) { setError(t('taskmanager.taskCmdNoSession', { title: task.title })); return }
    if (!body) { setError(t('taskmanager.taskCmdEmpty')); return }
    if (taskStreamsRef.current.has(task.id)) { setError(t('taskmanager.taskCmdBusy', { title: task.title })); return }

    const sid = task.db_session_id
    const note = taskTitleNote(task.title)
    appendLocalMessage('user', 'user_message_chunk', `/task:${task.id}${note ? `(${note})` : ''} ${body}`)
    appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskPromptSent', { title: task.title }))

    const ac = new AbortController()
    taskStreamsRef.current.set(task.id, ac)
    await streamPrompt(
      sid,
      body,
      (msg) => {
        if (!mountedRef.current) return
        // 任务会话内的权限请求路由回该任务会话，由本面板权限弹窗代为响应
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) {
            permissionSessionRef.current.set(req.request_id, sid)
            enqueuePermission(req)
          }
        }
      },
      () => {
        taskStreamsRef.current.delete(task.id)
        if (mountedRef.current) onTaskChanged()
      },
      (err) => {
        taskStreamsRef.current.delete(task.id)
        if (!mountedRef.current) return
        setError(`${task.title}: ${isTimeoutError(err) ? t('common.timeout') : err.message}`)
        onTaskChanged()
      },
      { signal: ac.signal },
    )
  }

  // @archive-task 归档任务到回收站（taskId 为 '*' 表示全部），可在回收站恢复
  async function handleArchiveTask(taskId: string) {
    setError('')
    if (taskId !== '*') {
      const task = (tasks || []).find((tk) => tk.id === taskId)
      if (!task) { setError(t('taskmanager.taskCmdNotFound', { id: taskId })); return }
    }
    try {
      const resp = await archiveTask(workspaceId, taskId === '*' ? undefined : taskId)
      const n = resp.data?.archived ?? 0
      if (taskId === '*') {
        appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskArchivedAll', { count: n }))
      } else {
        const task = (tasks || []).find((tk) => tk.id === taskId)
        appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskArchived', { title: task?.title || taskId }))
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
    onTaskChanged()
  }

  // @run-task 启动任务（taskId 为 '*' 表示启动全部待执行任务）
  async function handleRunTask(taskId: string) {
    setError('')
    if (taskId !== '*') {
      const task = (tasks || []).find((tk) => tk.id === taskId)
      if (!task) { setError(t('taskmanager.taskCmdNotFound', { id: taskId })); return }
    }
    try {
      await startTaskManager(workspaceId, taskId === '*' ? undefined : taskId)
      if (taskId === '*') {
        appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskRunStartedAll'))
      } else {
        const task = (tasks || []).find((tk) => tk.id === taskId)
        appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskRunStarted', { title: task?.title || taskId }))
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
    onTaskChanged()
  }

  // @task-create 直接创建任务：detail 取剩余文本，标题取首行，默认 goal 模式
  async function handleCreateTaskFromInput(detail: string) {
    const agentType = (selectedAgent || agents[0]?.type || '').trim()
    const title = detail.split('\n')[0].slice(0, 40) || t('taskmanager.untitledTask')
    try {
      await upsertTask(workspaceId, {
        id: genTaskId(),
        title,
        detail,
        agent_type: agentType,
        priority: 'p1',
        goal_condition: detail,
      })
      appendLocalMessage('assistant', 'agent_message_chunk', t('taskmanager.taskCreated', { title }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
    onTaskChanged()
  }

  // ===== 发送 =====
  async function handleSend(prompt: string) {
    const text = prompt.trim()
    if (!text) return
    // @task-create：剔除标记后剩余文本作为任务详情直接创建
    if (TASK_CREATE_RE.test(text)) {
      setInputText('')
      const detail = text.replace(TASK_CREATE_RE, '').trim()
      if (detail) {
        appendLocalMessage('user', 'user_message_chunk', text)
        void handleCreateTaskFromInput(detail)
      }
      return
    }
    // @run-task 运行引用：逐个启动选中任务（* = 全部待执行），不进入助手会话
    const runIds = [...text.matchAll(RUN_MENTION_RE)].map((m) => m[1])
    if (runIds.length > 0) {
      setInputText('')
      appendLocalMessage('user', 'user_message_chunk', text)
      const ids = runIds.includes('*') ? ['*'] : [...new Set(runIds)]
      for (const id of ids) void handleRunTask(id)
      return
    }
    // @archive-task 归档引用：逐个归档选中任务（* = 全部），不进入助手会话
    const archiveIds = [...text.matchAll(ARCHIVE_MENTION_RE)].map((m) => m[1])
    if (archiveIds.length > 0) {
      setInputText('')
      appendLocalMessage('user', 'user_message_chunk', text)
      const ids = archiveIds.includes('*') ? ['*'] : [...new Set(archiveIds)]
      for (const id of ids) void handleArchiveTask(id)
      return
    }
    // @task 引用：剔除引用标记后把剩余内容直发到引用的任务会话（多个引用则逐个直发）
    const mentionIds = [...text.matchAll(TASK_MENTION_RE)].map((m) => m[1])
    if (mentionIds.length > 0) {
      setInputText('')
      const body = text.replace(TASK_MENTION_RE, '').trim()
      for (const id of new Set(mentionIds)) void handleSendToTask(id, body)
      return
    }
    if (conv !== 'idle' || !selectedAgent) return
    setError('')
    // 受控输入框：真正进入发送后才清空（PromptInput 受控模式不自行清空）
    setInputText('')

    // 首条消息：保证“一个工作区只有一个任务助手管理会话”。
    // 先通过 /sessions/latest 查询该 workspace 是否已有会话：
    //   - 命中：复用该会话，回读历史消息，跳过系统引导与配置下发（会话已有自己的配置）。
    //   - 未命中：创建新会话(manual)，下发探测配置，注入系统引导。
    // 此处双重检查避免恢复逻辑未完成或未命中时竞态创建多个会话。
    let activeSession = session
    let sendText = text
    if (!activeSession) {
      setConv('connecting')
      try {
        // 1) 双重检查：查询工作区是否已有管理会话（清空后强制跳过复用，直接新建）
        const latest = forceNewRef.current ? { data: null } : await getLatestSessionByWorkspace(workspaceId, 'orchestration')
        if (latest.data) {
          activeSession = latest.data
          setSession(activeSession)
          handleSelectAgent(activeSession.agent_type)
          try {
            const hist = await listMessages(activeSession.id)
            setMessages(hist.data.messages || [])
            setHasMore(!!hist.data.has_more)
          } catch { /* 回读失败：保留空消息，仍复用会话 */ }
        } else {
          // 2) 未命中：创建新会话（source=orchestration：不登记 tasks.json、不在任务列表出现）
          const resp = await createSession(selectedAgent, workspaceId, selectedModel || undefined, 'orchestration')
          activeSession = resp.data
          forceNewRef.current = false
          setSession(activeSession)
          updateSessionTitle(activeSession.id, t('taskmanager.aiTitle')).catch(() => {})
          const extras = probeConfigs.filter((o) => o.type === 'select' && o.category !== 'model' && o.current_value)
          for (const o of extras) {
            try { await setConfigOption(activeSession.id, o.id, o.current_value) } catch { /* ignore */ }
          }
          sendText = buildSystemPrelude().replace('__WORKSPACE_ID__', String(workspaceId)) + text
        }
      } catch (e) {
        setConv('idle')
        setError(String((e as Error)?.message || e))
        return
      }
    }

    // 乐观用户消息（负 id，便于流结束后由真实 sequence 消息替换/去重）
    const optimisticId = -Date.now()
    const optimistic: Message = {
      id: optimisticId, session_id: activeSession!.session_id, role: 'user',
      kind: 'user_message_chunk', content: text, raw_json: '', sequence: 0,
      execution_id: null, created_at: new Date().toISOString(),
    }
    setMessages((prev) => [...prev, optimistic])

    const ac = new AbortController()
    abortRef.current = ac
    setConv('streaming')

    await streamPrompt(
      activeSession!.id,
      sendText,
      (msg) => {
        if (!mountedRef.current) return
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) enqueuePermission(req)
        }
        // 编排工具调用（如 create_task）落地后即时刷新左栏任务列表
        if (isTMToolMessage(msg)) scheduleTaskRefresh()
        if (msg.role !== 'user') enqueueMessage(msg)
        setConv((s) => (s === 'idle' ? 'streaming' : s))
      },
      () => {
        if (!mountedRef.current) return
        abortRef.current = null
        clearPermissions()
        setConv('idle')
        if (mountedRef.current) onTaskChanged()
      },
      (err) => {
        if (!mountedRef.current) return
        abortRef.current = null
        setMessages((prev) => prev.filter((m) => m.id !== optimisticId))
        clearPermissions()
        setConv('idle')
        setError(isTimeoutError(err) ? t('common.timeout') : err.message)
        // 出错前 agent 可能已改动 tasks.json，仍需同步一次任务列表
        onTaskChanged()
      },
      { signal: ac.signal },
    )
  }

  function handleCancel() {
    abortRef.current?.abort()
    setConv('idle')
  }

  // ===== 构造 PanelCtx（复刻 ChatPage 新建页 createCtx）=====
  const ctx: PanelCtx = {
    sessionId: session?.id,
    session,
    messages,
    convState: conv,
    sending: conv !== 'idle',
    onSend: handleSend,
    onCancel: handleCancel,
    // 输入框受控：支持多任务模式鼠标焦点自动注入 @task 引用
    restoreInput: inputText,
    onRestoreInputChange: setInputText,
    commands,
    // @ 菜单「任务」分类：选中任务插入 @task 引用，发送时直发到该任务会话
    taskMentions,
    modes,
    skills,
    currentModeId: session
      ? currentModeId
      : (probeConfigs.find((o) => o.category === 'mode')?.current_value || ''),
    onSetMode: (modeId: string) => {
      if (session) {
        setCurrentModeId(modeId)
        setSessionMode(session.id, modeId).catch(() => {})
        return
      }
      setProbeConfigs((prev) => prev.map((o) => (o.category === 'mode' ? { ...o, current_value: modeId } : o)))
    },
    configOptions: session ? configOptions : probeConfigs,
    onSetConfigOption: (configId: string, value: string) => {
      const opts = session ? configOptions : probeConfigs
      const opt = opts.find((o) => o.id === configId)
      if (session) {
        setConfigOptions((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
        if (opt?.category === 'model') setSelectedModel(value)
        setConfigOption(session.id, configId, value).catch(() => {})
        return
      }
      setProbeConfigs((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
      if (opt?.category === 'model') setSelectedModel(value)
    },
    agents: agents.map((a) => ({ type: a.type, display_name: a.display_name })),
    agentModelsMap,
    agentModelFilters: selectorFilters,
    // 有会话时仍展示全部 agent·模型组合（管理会话可弃，跨 agent 确认后开新会话）
    agentSwitchable: true,
    selectedAgent,
    onSelectAgent: handleSelectAgent,
    selectedModel,
    probeConfigs,
    // 合并下拉选择回调：
    // - 无会话：同 agent 切模型直接应用；跨 agent 暂存目标模型，待探测完成后应用（同 ChatPage 新建页）。
    // - 有会话：同 agent 切模型写入会话级 model config option；
    //   跨 agent 需确认弃置当前管理会话（会话与 agent 绑定，无法原地切换）。
    onSelectAgentModel: (agentType: string, modelValue: string) => {
      if (!agentType) return
      if (session) {
        if (agentType === session.agent_type) {
          if (!modelValue || modelValue === selectedModel) return
          const modelOpt = configOptions.find((o) => o.category === 'model')
          setSelectedModel(modelValue)
          if (modelOpt) {
            setConfigOptions((prev) => prev.map((o) => (o.id === modelOpt.id ? { ...o, current_value: modelValue } : o)))
            setConfigOption(session.id, modelOpt.id, modelValue).catch(() => {})
          }
          return
        }
        if (!window.confirm(t('taskmanager.switchAgentConfirm'))) return
        pendingModelRef.current = modelValue
        setSelectedAgent(agentType)
        void discardSession()
        return
      }
      if (agentType !== selectedAgent) {
        pendingModelRef.current = modelValue
        setSelectedAgent(agentType)
        return
      }
      if (!modelValue || modelValue === selectedModel) return
      setSelectedModel(modelValue)
      setProbeConfigs((prev) => prev.map((o) => (o.category === 'model' ? { ...o, current_value: modelValue } : o)))
    },
    onSelectModel: (val: string) => {
      setSelectedModel(val)
      setProbeConfigs((prev) => prev.map((o) => (o.category === 'model' ? { ...o, current_value: val } : o)))
    },
    probing,
    pendingPermission,
    permissionResponding,
    onPermissionRespond: handlePermissionRespond,
    onPermissionCancel: handlePermissionCancel,
    executions: [],
    workspaceId,
    cwd,
    docTarget: null,
    hasMore,
    loadingMore,
    onLoadMore: handleLoadMore,
  }

  return (
    <div className={styles.panel}>
      {/* 顶部工具条：清空当前对话并开启新会话（仅有会话/消息时展示） */}
      {(session || messages.length > 0) && (
        <div className={styles.toolbar}>
          <button
            type="button"
            className={styles.clearBtn}
            onClick={handleClear}
            title={t('taskmanager.clearHint')}
          >
            <Eraser size={13} />
            {t('taskmanager.clear')}
          </button>
        </div>
      )}
      {error && (
        <div className={styles.errorWrap}>
          <div className={styles.errorBanner}>{error}</div>
        </div>
      )}
      <div className={styles.chatArea}>
        <ChatPanel
          ctx={ctx}
          configBar="coding"
          emptyTitleKey="taskmanager.aiManageTitle"
          emptyHintKey="taskmanager.aiHint"
          placeholderKey="taskmanager.aiPlaceholder"
        />
      </div>
    </div>
  )
}
