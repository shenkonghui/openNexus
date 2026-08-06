import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { getSession, listMessages, cancelSession, listCommands, listModes, listSkills, listConfigOptions, setConfigOption, setSessionMode, respondPermission, deleteSession, updateSessionTitle, createSession, resumeSession, listSessionExecutions, getInterruptedTasks, getSessionConnection } from '../api/sessions'
import { getWorkspace } from '../api/workspaces'
import { listScheduledTasks, listExecutions } from '../api/scheduledTasks'
import { listAgents, probeAgentConfigs, preconnectAgent, listAgentCommands, listAgentModes } from '../api/agents'
import { listSkillsByPath } from '../api/filesystem'
import { getAgentPrefs, patchAgentPrefs } from '../api/agentPrefs'
import { WORKSPACE_STORAGE_KEY, useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { applyPrefsToConfigs, configsFromProbe, takeLegacyLocalAgentPrefs } from '../utils/agentPrefs'
import { streamPrompt, subscribeStream, streamResumeTask, isTimeoutError, isSessionInactiveError } from '../api/sse'
import { tasksUrl, newTaskUrl, sessionUrl, taskManagerUrl, isNewTaskPath, isTaskManagerPath } from '../utils/routes'
import type { Session, Message, AgentCommand, ConfigOption, ConfigOptionValue, SessionMode, AgentSkill, Execution, Agent, PermissionRequestPayload, RunningTask, AgentPrefs } from '../types'
import { parsePermissionRequest } from '../utils/permission'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import WorkspaceSelector from '../components/WorkspaceSelector'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import { type ConvState as ConvStatusState } from '../components/ConvStatusBar'
import TaskManagerView from '../components/TaskManagerView'
import { AUTO_WORKTREE } from '../components/WorktreePicker'
import { PanelRightClose, PanelRightOpen } from 'lucide-react'
import { saveLastDoc, LAST_DOC_KEY_PREFIX, type DocTarget } from '../utils/docs'
import LayoutRenderer from '../modes/LayoutRenderer'
import { getMode } from '../modes/registry'
import type { LayoutNode, PanelCtx } from '../modes/types'
import styles from './ChatPage.module.css'

/** 收集布局树中所有 leaf 面板 id（用于判断「缩进右侧窗口」要隐藏的面板集合） */
function collectAllPanelIds(node: LayoutNode): string[] {
  switch (node.kind) {
    case 'leaf':
      return [node.panel]
    case 'tabs':
      return [...node.panels, ...(node.optional ?? [])]
    case 'split':
      return node.children.flatMap(collectAllPanelIds)
  }
}

// navigate 时携带的 state：initialPrompt/createdSession 用于新建会话跳转；
// doc 用于侧边栏点击文档时打开指定文档（右侧「文档预览」标签）。
// draftPrompt 用于新建任务页预填输入框。
type NavigateState = { initialPrompt?: string; createdSession?: Session; doc?: DocTarget; draftPrompt?: string; tmSessionId?: number }

type ConvState = ConvStatusState

// 用户未保存过 agent 偏好时的默认选中 agent（仅当该 agent 已启用时生效）。
const DEFAULT_AGENT_TYPE = 'cursor'

export default function ChatPage() {
  const { t } = useTranslation()
  const { wid, sid } = useParams<{ wid?: string; sid?: string }>()
  const urlWorkspaceId = wid ? Number(wid) : NaN
  const sessionId = sid ? Number(sid) : NaN
  const hasSession = !isNaN(sessionId)
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId: storedWorkspaceId, sessions, sessionsWorkspaceId, selectWorkspace, reload: reloadWorkspace, loading: wsLoading } = useCurrentWorkspace(!!user)
  const workspaceId = !isNaN(urlWorkspaceId) ? urlWorkspaceId : storedWorkspaceId
  const navigate = useNavigate()
  const location = useLocation()
  const initialPromptRef = useRef<string>('')
  const bootstrappedSessionIdRef = useRef<number | null>(null)
  // location.state 变化时同步到 ref（navigate 跳转不会重新挂载组件，useRef 不会自动更新）
  const navState = location.state as NavigateState | null
  if (navState?.initialPrompt) {
    initialPromptRef.current = navState.initialPrompt
  }

  // 隐藏侧栏面板，仅保留对话。
  const LEFT_PANELS_HIDDEN_KEY = 'opennexus.layout.sideHidden'
  const [leftHidden, setLeftHidden] = useState<boolean>(() => localStorage.getItem(LEFT_PANELS_HIDDEN_KEY) === '1')
  useEffect(() => {
    localStorage.setItem(LEFT_PANELS_HIDDEN_KEY, leftHidden ? '1' : '0')
  }, [leftHidden])
  // 从当前模式布局动态推导「缩进右侧窗口」要隐藏的面板集合：布局内除对话(chat)外
  // 的其余工具面板全部隐藏。相比硬编码列表，新增面板也不会漏掉导致收起失效。
  const sidePanels = useMemo(
    () => collectAllPanelIds(getMode(null).layout).filter((id) => id !== 'chat'),
    [],
  )
  const hiddenPanels = leftHidden ? new Set(sidePanels) : undefined

  // 当前打开的文档（右侧「文档预览」标签）。侧边栏点击文档时通过 navigate state 传入；否则读 localStorage 上次打开的。
  const [docTarget, setDocTarget] = useState<DocTarget | null>(() => {
    if (navState?.doc) return navState.doc
    try {
      const stored = localStorage.getItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0))
      if (stored) return JSON.parse(stored) as DocTarget
    } catch { /* ignore */ }
    return null
  })
  // 侧边栏点击文档会 navigate 并带 doc state，这里响应 state 变化
  useEffect(() => {
    if (navState?.doc) {
      // 更新预览目标与记忆，并自动激活右侧「文档预览」标签。
      setDocTarget(navState.doc)
      window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'doc-preview' } }))
      localStorage.setItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0), JSON.stringify(navState.doc))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navState?.doc])

  // workspaceId 就绪后恢复上次打开的文档。
  // docTarget 的 useState 初始化在首次渲染执行，此时 workspaceId 可能尚未就绪
  //（URL 无 wid 时依赖 useCurrentWorkspace 异步加载），导致 localStorage key 不匹配而读不到。
  // 这里在 workspaceId 变化且未通过侧边栏点击（无 navState.doc）时，重新从 localStorage 恢复。
  const restoredDocRef = useRef<number | null>(null)
  useEffect(() => {
    if (!workspaceId || navState?.doc) return
    if (restoredDocRef.current === workspaceId) return
    restoredDocRef.current = workspaceId
    try {
      const stored = localStorage.getItem(LAST_DOC_KEY_PREFIX + workspaceId)
      if (stored) {
        const target = JSON.parse(stored) as DocTarget
        if (target && target.filePath) {
          setDocTarget(target)
        }
      }
    } catch { /* ignore */ }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  // AI 直接编辑磁盘文件后，自增此值触发文档预览重新读取。
  const [docReloadKey, setDocReloadKey] = useState(0)

  // 会话相关状态
  const [session, setSession] = useState<Session | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  // 历史消息分页：hasMore 表示还有更早的消息可加载，loadingMore 表示正在加载
  const [hasMore, setHasMore] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [restoreRefreshKey, setRestoreRefreshKey] = useState(0)
  // navState.draftPrompt 优先：从任务管理打开未运行任务时，用任务详情预填新建任务输入框。
  const [restoreInput, setRestoreInput] = useState<string | undefined>(() => navState?.draftPrompt)
  // 侧边栏点击编排任务会 navigate 到新建任务页并带 draftPrompt。当目标就是当前路由时
  //（navigate 不重新挂载组件），仅靠上面的初始化不会生效，表现为“点击无反应”。这里以 location.key
  // 为依赖响应每次跳转，重新应用预填内容。
  useEffect(() => {
    if (navState?.draftPrompt !== undefined) {
      setRestoreInput(navState.draftPrompt)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.key])
  const [commands, setCommands] = useState<AgentCommand[]>([])
  const [modes, setModes] = useState<SessionMode[]>([])
  const [skills, setSkills] = useState<AgentSkill[]>([])
  const [configOptions, setConfigOptions] = useState<ConfigOption[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [convState, setConvState] = useState<ConvState>('idle')
  // 断线重连倒计时：后端推真实退避计划（nextRetryAt 为本地时间戳），本地 1s 递减展示
  const [reconnectPlan, setReconnectPlan] = useState<{ nextRetryAt: number; attempt: number } | null>(null)
  const [reconnectSeconds, setReconnectSeconds] = useState(0)
  // 连接断开期间阻止轮询把 reconnecting 重置回 idle
  const connDownRef = useRef(false)
  // 后端繁忙状态（有活跃 prompt 或生效中的 goal），从 5s 轮询的 Connection 接口获取。
  // 发送队列 flush 前检查此状态，避免 goal 续轮期间队列消息并发打入同一 ACP 会话。
  const [backendBusy, setBackendBusy] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const mountedRef = useRef(true)
  // lastSeqRef 记录最后接收到的消息 sequence，用于断点续传重连时携带 Last-Event-ID
  const lastSeqRef = useRef(0)
  // interruptedTasks 存储因服务重启而中断的任务，用于显示重发横幅
  const [interruptedTasks, setInterruptedTasks] = useState<RunningTask[]>([])
  const [lastFailedPrompt, setLastFailedPrompt] = useState('')
  const [retryable, setRetryable] = useState(false)
  const [executions, setExecutions] = useState<Execution[]>([])
  const [currentModeId, setCurrentModeId] = useState('')
  const [pendingPermission, setPendingPermission] = useState<PermissionRequestPayload | null>(null)
  const [permissionResponding, setPermissionResponding] = useState(false)
  const permissionQueueRef = useRef<PermissionRequestPayload[]>([])
  const waitingPermissionRef = useRef(false)

  // ===== 流式消息按帧合并 =====
  // 每个 SSE 分片直接 setMessages 会让整页（含头部按钮、面板树）按分片频率重渲染，
  // 长对话时主线程被同步渲染饱和，导致头部按钮 / 任务切换点击「失效」。
  // 这里把流入消息缓冲到 ref，用 requestAnimationFrame 每帧最多 flush 一次，
  // 将重渲染频率钳制到 ≤60fps，与分片速率解耦。
  const pendingMessagesRef = useRef<Message[]>([])
  const flushRafRef = useRef<number | null>(null)

  const flushMessages = useCallback(() => {
    flushRafRef.current = null
    const batch = pendingMessagesRef.current
    if (batch.length === 0) return
    pendingMessagesRef.current = []
    setMessages((prev) => {
      // 收到真实流式消息后，移除乐观占位（负 id）
      const base = prev.some((m) => m.id < 0) ? prev.filter((m) => m.id >= 0) : prev
      const seen = new Set(base.filter((m) => m.sequence > 0).map((m) => m.sequence))
      const additions: Message[] = []
      for (const msg of batch) {
        if (msg.sequence > 0) {
          if (seen.has(msg.sequence)) continue // 按 sequence 去重：避免轮询补齐与实时流重复
          seen.add(msg.sequence)
        }
        additions.push(msg)
      }
      return additions.length > 0 ? [...base, ...additions] : base
    })
  }, [])

  const enqueueMessage = useCallback((msg: Message) => {
    pendingMessagesRef.current.push(msg)
    if (flushRafRef.current == null) {
      flushRafRef.current = requestAnimationFrame(flushMessages)
    }
  }, [flushMessages])

  function enqueuePermission(req: PermissionRequestPayload) {
    waitingPermissionRef.current = true
    setPendingPermission((prev) => {
      if (prev?.request_id === req.request_id) return prev
      if (!prev) return req
      if (!permissionQueueRef.current.some((p) => p.request_id === req.request_id)) {
        permissionQueueRef.current = [...permissionQueueRef.current, req]
      }
      return prev
    })
    setConvState('waiting_permission')
  }

  function clearPermissions() {
    permissionQueueRef.current = []
    waitingPermissionRef.current = false
    setPendingPermission(null)
  }

  function advancePermissionQueue() {
    const next = permissionQueueRef.current.shift() || null
    waitingPermissionRef.current = !!next
    setPendingPermission(next)
    if (next) setConvState('waiting_permission')
    else setConvState((s) => (s === 'waiting_permission' ? 'streaming' : s))
  }

  // 无会话模式下的 agent / 模型选择状态
  const [agents, setAgents] = useState<Agent[]>([])
  const [selectedAgent, setSelectedAgent] = useState('')
  const [selectedModel, setSelectedModel] = useState('')
  const [probeConfigs, setProbeConfigs] = useState<ConfigOption[]>([])
  const [probing, setProbing] = useState(false)
  // agent+模型 合并下拉：各 agent 探测到的模型列表与配置的显示过滤正则（config.yaml agents.selector.filters）
  const [agentModelsMap, setAgentModelsMap] = useState<Record<string, ConfigOptionValue[]>>({})
  const [selectorFilters, setSelectorFilters] = useState<string[]>([])
  // 合并下拉选中「另一 agent 的某模型」时暂存目标模型，待该 agent 探测完成后应用
  const pendingModelRef = useRef('')
  const [creating, setCreating] = useState(false)
  const [homeCommands, setHomeCommands] = useState<AgentCommand[]>([])
  const [homeModes, setHomeModes] = useState<SessionMode[]>([])
  const [homeSkills, setHomeSkills] = useState<AgentSkill[]>([])
  const [workspaceCwd, setWorkspaceCwd] = useState('')
  // 新建任务页：用户选择的自定义工作目录（如已存在的 git worktree，或 AUTO_WORKTREE 哨兵值表示 AI 自动创建）；为空则跟随工作区 cwd。
  const [taskCwd, setTaskCwd] = useState('')
  const [agentPrefs, setAgentPrefs] = useState<AgentPrefs>({ last_agent_type: '', prefs: {} })
  const prefsSaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const agentPrefsRef = useRef(agentPrefs)
  agentPrefsRef.current = agentPrefs

  const bootstrapSession = navState?.createdSession?.id === sessionId ? navState.createdSession : null
  const isCreateMode = !hasSession && isNewTaskPath(location.pathname, workspaceId)
  const activeSession = session ?? bootstrapSession

  // 从消息流中提取当前 session mode
  useEffect(() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      const msg = messages[i]
      if (msg.kind !== 'current_mode_update') continue
      try {
        const data = JSON.parse(msg.raw_json)
        const modeId = data?.CurrentModeUpdate?.currentModeId || data?.current_mode_update?.currentModeId
        if (modeId) {
          setCurrentModeId(String(modeId))
          return
        }
      } catch { /* ignore */ }
    }
  }, [messages])

  // 加载会话数据（有会话时）；quiet 模式下不阻塞 UI（用于新建会话后的后台刷新）。
  // skipMessages=true 时跳过消息拉取与 setMessages（流式进行时，避免 DB 数据覆盖实时 SSE 流，
  // 同时避免每 5 秒轮询整体替换数组触发全量重渲染）。
  const loadData = useCallback(async (opts?: { quiet?: boolean; skipMessages?: boolean }) => {
    if (!hasSession) return
    if (!opts?.quiet) { setLoading(true); setError('') }
    try {
      if (opts?.skipMessages) {
        const sessionResp = await getSession(sessionId)
        setSession(sessionResp.data)
        applySessionSideData(sessionResp.data)
      } else {
        const [sessionResp, msgResp] = await Promise.all([
          getSession(sessionId), listMessages(sessionId),
        ])
        setSession(sessionResp.data)
        const msgs = msgResp.data.messages || []
        setMessages(msgs)
        setHasMore(!!msgResp.data.has_more)
        // 同步 lastSeqRef 为当前最大 sequence（用于断点续传重连）
        if (msgs.length > 0) {
          lastSeqRef.current = msgs[msgs.length - 1].sequence
        }
        applySessionSideData(sessionResp.data)
      }
    } catch (err) {
      if (!opts?.quiet) setError(err instanceof Error ? err.message : t('common.failed'))
    } finally { if (!opts?.quiet) setLoading(false) }
  }, [sessionId, hasSession, workspaceId])

  // 加载更早的历史消息：以当前最早可见消息的 sequence 为 before 游标，向前翻一页。
  // 新消息 prepend 到 messages 头部；hasMore 由响应更新。loadingMore 期间禁止重复触发。
  const loadMore = useCallback(async () => {
    if (!hasSession || loadingMore || !hasMore) return
    // 取当前最早消息的 sequence 作为 before 游标（filterDisplay 后的最早条可能与原始 messages 不同，
    // 但 sequence 单调，用原始 messages[0] 即可——更早的消息 sequence 一定更小）
    if (messages.length === 0) return
    const beforeSeq = messages[0].sequence
    setLoadingMore(true)
    try {
      const resp = await listMessages(sessionId, { before: beforeSeq })
      const older = resp.data.messages || []
      setHasMore(!!resp.data.has_more)
      if (older.length > 0) {
        setMessages((prev) => [...older, ...prev])
      }
    } catch {
      /* 忽略：用户可重试 */
    } finally {
      setLoadingMore(false)
    }
  }, [hasSession, loadingMore, hasMore, messages, sessionId])

  // 会话相关的辅助数据加载（executions/commands/modes/skills/configOptions/中断任务）。
  // 抽取出来供 loadData 的两条分支（全量 / skipMessages）共用，避免重复。
  const applySessionSideData = useCallback((sessionData: Session) => {
    // 查询中断任务（服务重启后未完成的 prompt）
    getInterruptedTasks(sessionData.id).then((r) => setInterruptedTasks(r.data.tasks || [])).catch(() => setInterruptedTasks([]))
    // 会话列表由 useCurrentWorkspace 统一加载，会话详情模式下通过 URL workspace 同步 effect 保持一致
    if (sessionData.source === 'scheduled' || sessionData.source === 'classify') {
      loadExecutions(sessionData.id, sessionData.source)
    } else { setExecutions([]) }
    listCommands(sessionData.id).then((r) => setCommands(r.data.commands || [])).catch(() => setCommands([]))
    listModes(sessionData.id).then((r) => {
      const modeList = r.data.modes || []
      setModes(modeList)
      if (modeList.length > 0) {
        setCurrentModeId((prev) => prev || modeList[0].id)
      }
    }).catch(() => setModes([]))
    listSkills(sessionData.id).then((r) => setSkills(r.data.skills || [])).catch(() => setSkills([]))
    listConfigOptions(sessionData.id).then((r) => {
      const opts = r.data.config_options || []
      setConfigOptions(opts)
    }).catch(() => setConfigOptions([]))
  }, [])

  // 恢复检查点后的回调：稳定化引用，避免 MessageList / MessageBubble 因回调每次新建而无谓重渲染。
  const handleRestored = useCallback((promptText: string) => {
    loadData()
    setRestoreRefreshKey((k) => k + 1)
    if (promptText) setRestoreInput(promptText)
  }, [loadData])

  // 加载 agent 列表和会话列表（无会话时）
  const loadHomeData = useCallback(async () => {
    setLoading(true); setError('')
    try {
      const [agentsResp, wsResp, prefsResp] = await Promise.all([
        listAgents(),
        workspaceId ? getWorkspace(workspaceId) : Promise.resolve(null),
        getAgentPrefs().catch(() => ({ data: { last_agent_type: '', prefs: {} } as AgentPrefs })),
      ])
      setAgents(agentsResp.data.agents || [])
      setSelectorFilters(agentsResp.data.selector_filters || [])
      setWorkspaceCwd(wsResp?.data.workspace?.cwd || '')

      let prefs = prefsResp.data
      if (!prefs.last_agent_type && Object.keys(prefs.prefs || {}).length === 0) {
        const legacy = takeLegacyLocalAgentPrefs()
        if (legacy) {
          try {
            for (const [agentType, configs] of Object.entries(legacy.prefs)) {
              prefs = (await patchAgentPrefs({
                last_agent_type: legacy.last_agent_type || undefined,
                agent_type: agentType,
                configs,
              })).data
            }
            if (legacy.last_agent_type && Object.keys(legacy.prefs).length === 0) {
              prefs = (await patchAgentPrefs({ last_agent_type: legacy.last_agent_type })).data
            }
          } catch { /* 迁移失败不影响主流程 */ }
        }
      }
      setAgentPrefs(prefs)

      if (agentsResp.data.agents?.length > 0) {
        const types = agentsResp.data.agents.map((a: Agent) => a.type)
        if (prefs.last_agent_type && types.includes(prefs.last_agent_type)) {
          setSelectedAgent(prefs.last_agent_type)
        } else if (types.includes(DEFAULT_AGENT_TYPE)) {
          // 用户未保存过偏好时，优先默认选中 cursor（若已启用），否则退回首个 agent
          setSelectedAgent(DEFAULT_AGENT_TYPE)
        } else {
          setSelectedAgent(agentsResp.data.agents[0].type)
        }
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally { setLoading(false) }
  }, [workspaceId])

  function schedulePrefsPatch(payload: { last_agent_type?: string; agent_type?: string; configs?: Record<string, string> }) {
    setAgentPrefs((prev) => {
      const next: AgentPrefs = {
        last_agent_type: payload.last_agent_type ?? prev.last_agent_type,
        prefs: { ...prev.prefs },
      }
      if (payload.agent_type && payload.configs) {
        const cur = { ...(next.prefs[payload.agent_type] || {}) }
        for (const [k, v] of Object.entries(payload.configs)) {
          if (!v) delete cur[k]
          else cur[k] = v
        }
        if (Object.keys(cur).length === 0) delete next.prefs[payload.agent_type]
        else next.prefs[payload.agent_type] = cur
      }
      return next
    })
    if (prefsSaveTimer.current) clearTimeout(prefsSaveTimer.current)
    prefsSaveTimer.current = setTimeout(() => {
      patchAgentPrefs(payload)
        .then((r) => setAgentPrefs(r.data))
        .catch(() => { /* 静默 */ })
    }, 300)
  }

  function handleWorkspaceChange(id: number) {
    // 切换工作区时重置自定义工作目录，回退到新工作区的默认 cwd。
    setTaskCwd('')
    localStorage.setItem(WORKSPACE_STORAGE_KEY, String(id))
    // 切换工作区默认进入任务助手页面
    navigate(taskManagerUrl(id))
  }

  function handleWorkspaceRefresh() {
    reloadWorkspace()
      .then(() => (hasSession ? loadData({ quiet: true }) : loadHomeData()))
      .catch((err) => setError(err instanceof Error ? err.message : t('common.failed')))
  }

  // 会话详情页直接打开时 loadHomeData 不执行，这里单独拉一次 selector 过滤正则，
  // 使会话页的 agent+模型 下拉也能应用 config.yaml 的显示过滤。
  const filtersLoadedRef = useRef(false)
  useEffect(() => {
    if (!user || !hasSession || filtersLoadedRef.current) return
    filtersLoadedRef.current = true
    listAgents().then((r) => setSelectorFilters(r.data.selector_filters || [])).catch(() => {})
  }, [user, hasSession])

  // 监听 agent 变化，探测 config options（新建页需要预探供模型选择）
  useEffect(() => {
    if (hasSession || !isCreateMode || !selectedAgent) {
      setProbeConfigs([])
      setSelectedModel('')
      return
    }
    let alive = true
    setProbing(true)
    probeAgentConfigs(selectedAgent)
      .then((r) => {
        if (!alive) return
        const opts = r.data.config_options || []
        let applied = applyPrefsToConfigs(opts, agentPrefsRef.current.prefs[selectedAgent])
        const modelOpt = applied.find((o) => o.category === 'model')
        // 合并下拉选中的目标模型优先于探测/偏好默认值（仅在该模型确实存在时）
        const pending = pendingModelRef.current
        pendingModelRef.current = ''
        let nextModel = modelOpt?.current_value || ''
        if (pending && modelOpt?.options.some((v) => v.value === pending)) {
          nextModel = pending
          applied = applied.map((o) => (o.category === 'model' ? { ...o, current_value: pending } : o))
        }
        setProbeConfigs(applied)
        setSelectedModel(nextModel)
      })
      .catch((err) => {
        if (!alive) return
        setProbeConfigs([])
        const savedModel = agentPrefsRef.current.prefs[selectedAgent]?.model || ''
        setSelectedModel(savedModel)
        setError(err instanceof Error ? err.message : '探测配置失败')
      })
      .finally(() => { if (alive) setProbing(false) })
    return () => { alive = false }
  }, [selectedAgent, hasSession, isCreateMode])

  // 新建页：提前预连接 agent，减少首次发送时的冷启动等待。
  // agent 选中后立即用 probe cwd 预热；workspaceCwd 就绪后若不同则再次预热。
  useEffect(() => {
    if (hasSession || !isCreateMode || !selectedAgent) return
    preconnectAgent(selectedAgent, workspaceCwd)
  }, [selectedAgent, workspaceCwd, hasSession, isCreateMode])

  // 并行探测全部 agent 的模型列表，供 agent+模型 合并下拉展示全部组合。
  // 无论新建页还是会话详情页都执行：会话详情页的 ChatPanel 会用 agentModelsMap 补全
  // 会话级模型列表（会话创建时 agent 可能未完全初始化导致模型不全）。
  // probeAgentConfigs 有前端缓存，当前选中 agent 的探测与上方 effect 共享结果，不会重复请求。
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

  // agent+模型 合并下拉的选择回调：同 agent 切模型直接应用；
  // 跨 agent 切换时暂存目标模型，待该 agent 探测完成后应用（见上方 probe effect）。
  function handleSelectAgentModel(agentType: string, modelValue: string) {
    if (!agentType) return
    if (agentType !== selectedAgent) {
      pendingModelRef.current = modelValue
      setSelectedAgent(agentType)
      schedulePrefsPatch(modelValue
        ? { last_agent_type: agentType, agent_type: agentType, configs: { model: modelValue } }
        : { last_agent_type: agentType })
      return
    }
    if (!modelValue || modelValue === selectedModel) return
    setSelectedModel(modelValue)
    setProbeConfigs((prev) => prev.map((o) => (o.category === 'model' ? { ...o, current_value: modelValue } : o)))
    schedulePrefsPatch({ last_agent_type: agentType, agent_type: agentType, configs: { model: modelValue } })
  }

  // 新建任务页：加载 agent 级 slash command / mode（探测完成后刷新）
  useEffect(() => {
    if (hasSession || !isCreateMode || !selectedAgent || probing) {
      if (hasSession || !isCreateMode) {
        setHomeCommands([])
        setHomeModes([])
      }
      return
    }
    listAgentCommands(selectedAgent, workspaceCwd || undefined).then((r) => setHomeCommands(r.data.commands || [])).catch(() => setHomeCommands([]))
    listAgentModes(selectedAgent).then((r) => setHomeModes(r.data.modes || [])).catch(() => setHomeModes([]))
  }, [selectedAgent, hasSession, isCreateMode, probing, workspaceCwd])

  // 新建任务页：加载 skills（与 agent 无关；cwd 为空时仍扫用户目录）
  useEffect(() => {
    if (hasSession || !isCreateMode) {
      setHomeSkills([])
      return
    }
    listSkillsByPath(workspaceCwd || undefined)
      .then((r) => setHomeSkills(r.data.skills || []))
      .catch(() => setHomeSkills([]))
  }, [workspaceCwd, hasSession, isCreateMode])

  // 将 URL 中的 workspace 同步到 hook，使 sidebar 展示该 workspace 的会话列表。
  // 任务列表页（无会话）与 会话详情页 都需要同步，否则切换工作区时侧边栏会显示其它工作区的会话。
  useEffect(() => {
    if (!user || isNaN(urlWorkspaceId)) return
    if (urlWorkspaceId !== storedWorkspaceId) {
      selectWorkspace(urlWorkspaceId).catch(() => {})
    }
  }, [user, urlWorkspaceId, storedWorkspaceId, selectWorkspace])

  useEffect(() => {
    if (!user) return
    if (hasSession) {
      const created = navState?.createdSession
      if (created?.id === sessionId && bootstrappedSessionIdRef.current !== sessionId) {
        bootstrappedSessionIdRef.current = sessionId
        setSession(created)
        setMessages([])
        setLoading(false)
        setCreating(false)
        // 刷新会话列表，使 sidebar 显示新建的会话
        reloadWorkspace()
        loadData({ quiet: true })
      } else if (bootstrappedSessionIdRef.current !== sessionId) {
        loadData()
      }
    } else {
      bootstrappedSessionIdRef.current = null
      loadHomeData()
    }
  }, [user, hasSession, sessionId, loadData, loadHomeData, reloadWorkspace, navState?.createdSession])

  // 打开首页（任务列表页）时不再显示历史列表：有最近任务则跳转过去，否则跳到新建任务页。
  // 等待 workspace 数据加载完成后再判断，避免在 sessions 尚未就绪时误跳到新建任务页。
  useEffect(() => {
    if (!user || hasSession || isCreateMode) return
    // 任务管理页：按路径短路，否则会被下方「跳最近任务」抢走，表现为点编排进不去。
    if (isTaskManagerPath(location.pathname)) return
    if (wsLoading || !workspaceId) return
    // 切换工作区时新会话为异步加载：sessions 尚未与当前 workspace 匹配时暂不跳转，
    // 否则会用旧工作区的会话跳回原工作区，表现为“无法切换工作区”。
    if (sessionsWorkspaceId !== workspaceId) return
    const manualSessions = sessions.filter((s) => (!s.source || s.source === 'manual') && s.workspace_id === workspaceId)
    if (manualSessions.length > 0) {
      const latest = manualSessions[0] // 后端按 created_at DESC 返回，首个即最近任务
      navigate(sessionUrl(latest.id, latest.workspace_id), { replace: true })
    } else {
      navigate(newTaskUrl(workspaceId), { replace: true })
    }
  }, [user, hasSession, isCreateMode, wsLoading, workspaceId, sessionsWorkspaceId, sessions, navigate, location.pathname])

  // 当组件卸载或切换到不同会话时，中断旧的 SSE 流，防止内存泄漏和 React 警告
  useEffect(() => {
    mountedRef.current = true
    // 切换会话时清除上个会话的重连倒计时状态
    connDownRef.current = false
    setReconnectPlan(null)
    // 切换会话时重置后端繁忙状态，避免上个会话的残留标记阻塞队列
    setBackendBusy(false)
    return () => {
      mountedRef.current = false
      if (flushRafRef.current != null) {
        cancelAnimationFrame(flushRafRef.current)
        flushRafRef.current = null
      }
      pendingMessagesRef.current = []
      if (abortRef.current) {
        abortRef.current.abort()
        abortRef.current = null
      }
    }
  }, [sessionId])

  // 定时轮询执行状态：当会话活跃或为定时/分类任务时，每 5 秒刷新消息和执行列表。
  // 即使前端重启，也能通过 loadData 从数据库获取最新状态。
  // 同时清理「UI 仍显示生成中但已无 SSE」的陈旧状态（ACP 恢复后常见）。
  useEffect(() => {
    if (!hasSession || !session) return
    const needPoll = session.status === 'active' || session.status === 'pending' ||
      session.source === 'scheduled' || session.source === 'classify'
    if (!needPoll) return

    const interval = setInterval(() => {
      if (!mountedRef.current) return
      if (abortRef.current == null && !connDownRef.current) {
        // 后端繁忙（有活跃 prompt 或生效中的 goal）时不把 conv 重置回 idle，
        // 避免发送队列在 goal 续轮期间错误续发。
        setConvState((s) => (
          s === 'streaming' || s === 'reconnecting' || s === 'connecting'
            ? (backendBusy ? 'streaming' : 'idle')
            : s
        ))
      }
      // 流式进行时（abortRef 非空）跳过消息拉取，避免 DB 数据覆盖实时 SSE 流，
      // 也避免每 5 秒整体替换 messages 数组触发全量重渲染。
      loadData({ quiet: true, skipMessages: !!abortRef.current })
      // 顺带拉取连接状态：断开时后端 healthCheckLoop 正在退避重连，
      // 展示“N 秒后自动重连”倒计时；恢复后由上方兜底逻辑自动回到 idle。
      if (session.status === 'active') {
        getSessionConnection(session.id).then(({ data }) => {
          if (!mountedRef.current) return
          const down = data.state === 'disconnected'
          connDownRef.current = down
          // 更新后端繁忙状态：有活跃 prompt 或生效中的 goal
          setBackendBusy(!!data.has_active_prompt || !!data.goal_active)
          if (down) {
            setReconnectPlan({ nextRetryAt: Date.now() + Math.max(0, data.next_retry_in_ms), attempt: data.attempt })
            setConvState((s) => (s === 'waiting_permission' ? s : 'reconnecting'))
          } else {
            setReconnectPlan(null)
          }
        }).catch(() => { /* 忽略：下一轮轮询重试 */ })
      }
    }, 5000)

    return () => clearInterval(interval)
  }, [hasSession, session?.id, session?.status, session?.source, loadData, backendBusy])

  // 重连倒计时本地 1s 递减；到 0 后保持 0（后端尝试中），下轮轮询刷新计划。
  useEffect(() => {
    if (!reconnectPlan) return
    const tick = () => setReconnectSeconds(Math.max(0, Math.ceil((reconnectPlan.nextRetryAt - Date.now()) / 1000)))
    tick()
    const timer = setInterval(tick, 1000)
    return () => clearInterval(timer)
  }, [reconnectPlan])

  // 会话进入非活跃态（agent 进程退出/重连失败等被标 error/closed）时，挂起权限的接收方已失效，
  // 清除权限栏，避免「等待操作确认」与「不在活跃状态」同时存在的死锁状态——否则权限栏永远无法消除、会话无法恢复。
  useEffect(() => {
    if (!session) return
    if (session.status === 'active' || session.status === 'pending') return
    if (waitingPermissionRef.current || pendingPermission) {
      clearPermissions()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [session?.status])

  useEffect(() => {
    if (loading && !bootstrapSession) return // 新建会话有 bootstrap 数据时不等待 loadData
    if (!activeSession) return
    const pending = initialPromptRef.current
    if (!pending) return
    initialPromptRef.current = ''
    if (location.state) navigate(location.pathname, { replace: true, state: null })
    handleSend(pending)
  }, [activeSession, loading, bootstrapSession])

  // 页面可见性恢复时，若会话有进行中的 prompt 但前端未在流式接收，尝试断点续传重连。
  // 使用 subscribeStream 而非发起新 prompt，避免重复执行。
  useEffect(() => {
    if (!hasSession) return
    const handleVisible = () => {
      if (document.visibilityState !== 'visible') return
      if (!mountedRef.current) return
      // 已在流式接收则无需重连；等待权限时也不抢占
      if (abortRef.current) return
      if (convState === 'waiting_permission' || waitingPermissionRef.current) return
      // 尝试订阅会话当前进行中的 prompt 流（若服务端无活跃 prompt 会立即返回）
      setConvState('reconnecting')
      const ac = new AbortController()
      abortRef.current = ac
      subscribeStream(
        sessionId,
        lastSeqRef.current,
        (msg) => {
          if (!mountedRef.current) return
          if (msg.kind === 'permission_request') {
            const req = parsePermissionRequest(msg.raw_json)
            if (req) enqueuePermission(req)
          }
          enqueueMessage(msg)
          setConvState((s) => (s === 'waiting_permission' ? s : 'streaming'))
        },
        () => {
          if (!mountedRef.current) return
          abortRef.current = null
          setConvState(waitingPermissionRef.current ? 'waiting_permission' : 'idle')
          loadData({ quiet: true })
        },
        () => {
          if (!mountedRef.current) return
          abortRef.current = null
          // 静默处理重连失败（服务端可能无活跃 prompt）
          setConvState(waitingPermissionRef.current ? 'waiting_permission' : 'idle')
          loadData({ quiet: true })
        },
        {
          signal: ac.signal,
          onSeq: (seq) => { if (seq > lastSeqRef.current) lastSeqRef.current = seq },
          // 仅真正收到消息才进入 streaming；连接建立本身不改状态，避免误显「生成中」
          onActivity: () => {
            if (mountedRef.current) setConvState((s) => (s === 'idle' ? 'streaming' : s))
          },
          shouldPauseIdleTimeout: () => waitingPermissionRef.current,
        },
      )
    }
    document.addEventListener('visibilitychange', handleVisible)
    return () => document.removeEventListener('visibilitychange', handleVisible)
  }, [hasSession, sessionId, convState, loadData])

  // 组件挂载时也尝试一次订阅（处理页面刷新后服务端仍在输出但前端未连接的情况）
  useEffect(() => {
    if (!hasSession || !session || convState !== 'idle') return
    if (session.status !== 'active') return
    if (abortRef.current) return
    const ac = new AbortController()
    abortRef.current = ac
    setConvState('reconnecting')
    subscribeStream(
      sessionId,
      lastSeqRef.current,
      (msg) => {
        if (!mountedRef.current) return
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) enqueuePermission(req)
        }
        enqueueMessage(msg)
        setConvState((s) => (s === 'waiting_permission' ? s : 'streaming'))
      },
      () => {
        if (!mountedRef.current) return
        abortRef.current = null
        setConvState(waitingPermissionRef.current ? 'waiting_permission' : 'idle')
      },
      () => {
        if (!mountedRef.current) return
        abortRef.current = null
        setConvState(waitingPermissionRef.current ? 'waiting_permission' : 'idle')
      },
      {
        signal: ac.signal,
        onSeq: (seq) => { if (seq > lastSeqRef.current) lastSeqRef.current = seq },
        onActivity: () => { if (mountedRef.current) setConvState((s) => (s === 'idle' ? 'streaming' : s)) },
        shouldPauseIdleTimeout: () => waitingPermissionRef.current,
      },
    )
    return () => { ac.abort() }
    // 仅在 session.id 变化时触发
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasSession, session?.id])

  async function loadExecutions(dbSessionId: number, source?: Session['source']) {
    try {
      if (source === 'classify') {
        const execResp = await listSessionExecutions(dbSessionId)
        setExecutions(execResp.data.executions || [])
        return
      }
      const tasksResp = await listScheduledTasks(workspaceId || undefined)
      const task = (tasksResp.data.tasks || []).find((t) => t.db_session_id === dbSessionId)
      if (task) { const execResp = await listExecutions(task.id); setExecutions(execResp.data.executions || []) }
    } catch { /* silent */ }
  }

  // 无会话时：创建会话并发起首次对话
  async function handleFirstSend(prompt: string) {
    if (!selectedAgent || creating) return
    setCreating(true); setError('')
    try {
      // AI 自动 worktree：不传 cwd，由后端根据首条 prompt 命名并创建 worktree
      const isAutoWt = taskCwd === AUTO_WORKTREE
      const resp = await createSession(
        selectedAgent,
        workspaceId || 0,
        selectedModel || undefined,
        undefined,
        isAutoWt ? undefined : (taskCwd || undefined),
        undefined,
        isAutoWt,
        isAutoWt ? prompt : undefined,
      )
      const extras = probeConfigs.filter((o) => o.type === 'select' && o.category !== 'model' && o.current_value)
      for (const o of extras) {
        try { await setConfigOption(resp.data.id, o.id, o.current_value) } catch { /* 部分失败可接受 */ }
      }
      const configs = configsFromProbe(
        selectedModel
          ? probeConfigs.map((o) => (o.category === 'model' ? { ...o, current_value: selectedModel } : o))
          : probeConfigs,
      )
      try {
        const r = await patchAgentPrefs({
          last_agent_type: selectedAgent,
          agent_type: selectedAgent,
          configs: Object.keys(configs).length > 0 ? configs : undefined,
        })
        setAgentPrefs(r.data)
      } catch { /* 静默 */ }
      navigate(`/workspaces/${resp.data.workspace_id}/sessions/${resp.data.id}`, {
        state: { initialPrompt: prompt, createdSession: resp.data },
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      setCreating(false)
    }
  }

  async function handleSend(prompt: string) {
    if (!activeSession) return
    setConvState('connecting')
    setError('')
    setRetryable(false)
    setLastFailedPrompt('')
    setRestoreInput(undefined)

    // 乐观展示用户消息，避免发送后界面无反馈
    const optimisticId = -Date.now()
    const optimisticMsg: Message = {
      id: optimisticId,
      session_id: activeSession.session_id,
      role: 'user',
      kind: 'user_message_chunk',
      content: prompt,
      raw_json: '',
      sequence: 0,
      execution_id: null,
      created_at: new Date().toISOString(),
    }
    setMessages((prev) => [...prev, optimisticMsg])

    const ac = new AbortController()
    abortRef.current = ac
    let gotAgentReply = false

    await streamPrompt(
      sessionId,
      prompt,
      (msg) => {
        if (!mountedRef.current) return
        // 后端会话重连提示：进入"重新连接中"状态，不入队、不算 agent 回复。
        // 后续 agent 消息会把状态推进到 streaming，状态栏自动消失。
        if (msg.kind === 'reconnecting') {
          setConvState('reconnecting')
          return
        }
        if (msg.role !== 'user') gotAgentReply = true
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) enqueuePermission(req)
        }
        // 缓冲追加：按帧合并，避免每个 SSE 分片触发整页重渲染
        enqueueMessage(msg)
        setConvState((s) => (s === 'waiting_permission' ? s : 'streaming'))
      },
      () => {
        if (!mountedRef.current) return
        abortRef.current = null
        clearPermissions()
        setConvState('idle')
        setLastFailedPrompt('')
        setRetryable(false)
        loadData({ quiet: true })
        // AI 可能已修改磁盘上的文档文件，触发文档预览重新读盘
        setDocReloadKey((k) => k + 1)
      },
      async (err) => {
        if (!mountedRef.current) return
        abortRef.current = null
        setMessages((prev) => prev.filter((m) => m.id !== optimisticId))
        if (isTimeoutError(err)) {
          await recoverFromTimeout(prompt, gotAgentReply)
          return
        }
        clearPermissions()
        setConvState('idle')
        setError(err.message)
      },
      {
        signal: ac.signal,
        onSeq: (seq) => { if (seq > lastSeqRef.current) lastSeqRef.current = seq },
        onActivity: () => { if (mountedRef.current) setConvState((s) => (s === 'waiting_permission' ? s : 'streaming')) },
        shouldPauseIdleTimeout: () => waitingPermissionRef.current,
      },
    )
  }

  // 稳定引用：供 memo 化的 DocPreviewView 比较 props，避免每次重渲染新建导致预览重渲染。
  const handleCloseDoc = useCallback(() => {
    setDocTarget(null)
    saveLastDoc(workspaceId, { folderId: '', filePath: '' })
  }, [workspaceId])

  const displayConvState: ConvState = pendingPermission ? 'waiting_permission' : convState

  async function recoverFromTimeout(prompt: string, gotAgentReply: boolean) {
    setConvState('reconnecting')
    clearPermissions()
    setError('')
    try {
      try { await cancelSession(sessionId) } catch { /* 尽力取消挂起的 prompt */ }
      const resp = await resumeSession(sessionId)
      setSession(resp.data)
      await loadData({ quiet: true })
      setConvState('idle')
      if (!gotAgentReply) {
        setLastFailedPrompt(prompt)
        setRetryable(true)
        setError(t('session.timeoutReconnected'))
      } else {
        setError(t('session.timeoutReconnected'))
      }
    } catch (err) {
      setConvState('idle')
      setError(err instanceof Error ? err.message : t('common.failed'))
      setLastFailedPrompt(prompt)
      setRetryable(true)
    }
  }

  const sending = convState !== 'idle'

  async function handleRetry() { if (!lastFailedPrompt) return; setError(''); setRetryable(false); await handleSend(lastFailedPrompt) }

  // 恢复中断的任务（服务重启后用户手动重发）
  async function handleResumeInterruptedTask(taskId: number) {
    setConvState('connecting')
    setError('')
    const ac = new AbortController()
    abortRef.current = ac
    await streamResumeTask(
      taskId,
      (msg) => {
        if (!mountedRef.current) return
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) enqueuePermission(req)
        }
        enqueueMessage(msg)
        setConvState((s) => (s === 'waiting_permission' ? s : 'streaming'))
      },
      () => {
        if (!mountedRef.current) return
        abortRef.current = null
        clearPermissions()
        setConvState('idle')
        loadData({ quiet: true })
      },
      (err) => {
        if (!mountedRef.current) return
        abortRef.current = null
        clearPermissions()
        setConvState('idle')
        setError(err.message)
      },
      {
        signal: ac.signal,
        onSeq: (seq) => { if (seq > lastSeqRef.current) lastSeqRef.current = seq },
        onActivity: () => { if (mountedRef.current) setConvState((s) => (s === 'waiting_permission' ? s : 'streaming')) },
        shouldPauseIdleTimeout: () => waitingPermissionRef.current,
      },
    )
  }

  async function handleCancel() {
    abortRef.current?.abort()
    abortRef.current = null
    try { await cancelSession(sessionId) } catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
    clearPermissions()
    setConvState('idle')
  }

  async function handleSetMode(modeId: string) {
    if (!modeId || modeId === currentModeId) return
    setError('')
    setCurrentModeId(modeId)
    try {
      await setSessionMode(sessionId, modeId)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      loadData({ quiet: true })
    }
  }

  async function handlePermissionRespond(optionId: string) {
    if (!pendingPermission) return
    setPermissionResponding(true)
    setError('')
    try {
      const current = pendingPermission
      const queued = optionId === 'allow-always' ? [...permissionQueueRef.current] : []
      if (optionId === 'allow-always') permissionQueueRef.current = []
      await respondPermission(sessionId, current.request_id, optionId)
      // allow-always 已由后端批量放行；前端同步清队列，避免弹窗连环弹出
      for (const req of queued) {
        try { await respondPermission(sessionId, req.request_id, optionId) } catch { /* 后端可能已批量处理 */ }
      }
      if (optionId === 'allow-always') {
        waitingPermissionRef.current = false
        setPendingPermission(null)
        setConvState('streaming')
      } else {
        advancePermissionQueue()
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('common.failed')
      // 会话已不活跃（agent 进程退出/重连失败）：挂起权限的接收方已失效，清掉死权限栏，
      // 避免权限栏与"不在活跃状态"提示同时存在、点击无响应的死锁。
      if (isSessionInactiveError(msg)) clearPermissions()
      setError(msg)
    } finally {
      setPermissionResponding(false)
    }
  }

  async function handlePermissionCancel() {
    if (!pendingPermission) return
    setPermissionResponding(true)
    setError('')
    try {
      await respondPermission(sessionId, pendingPermission.request_id, '', true)
      advancePermissionQueue()
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('common.failed')
      if (isSessionInactiveError(msg)) clearPermissions()
      setError(msg)
    } finally {
      setPermissionResponding(false)
    }
  }

  async function handleSetConfigOption(configId: string, value: string) {
    setError('')
    const opt = configOptions.find((o) => o.id === configId)
    setConfigOptions((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
    try {
      await setConfigOption(sessionId, configId, value)
      if (opt?.category && activeSession?.agent_type) {
        schedulePrefsPatch({
          last_agent_type: activeSession.agent_type,
          agent_type: activeSession.agent_type,
          configs: { [opt.category]: value },
        })
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      listConfigOptions(sessionId).then((r) => setConfigOptions(r.data.config_options || [])).catch(() => {})
    }
  }

  async function handleDeleteSession(id: number) {
    setError('')
    try {
      await deleteSession(id)
      if (id === sessionId) {
        localStorage.setItem(WORKSPACE_STORAGE_KEY, String(workspaceId))
        navigate(tasksUrl(workspaceId))
      }
      // 刷新会话列表，使 sidebar 同步删除
      reloadWorkspace()
    } catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
  }

  async function handleRenameSession(id: number, title: string) {
    setError('')
    try {
      const resp = await updateSessionTitle(id, title)
      if (id === sessionId) setSession(resp.data)
      // 刷新会话列表，使 sidebar 同步新标题
      reloadWorkspace()
    }
    catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
  }

  if (authLoading) return <LoadingSpinner text={t('common.loading')} />
  if (!user) return null

  // ============ 任务管理模式：在任务界面内管理编排任务（不走 LayoutRenderer，与 docs 特判并列） ============
  // 以路径为准：侧边栏「任务管理」入口直接路由到 /taskmanager。
  if (isTaskManagerPath(location.pathname)) {
    return (
      <AppLayout sidebarProps={{ sessions, workspaceId, onDelete: handleDeleteSession, onRename: handleRenameSession, onWorkspaceChange: handleWorkspaceChange, onWorkspaceRefresh: handleWorkspaceRefresh }}>
        <div className={styles.main}>
          <div className={styles.header}>
            <div className={styles.sysBar}>
              <SidebarToggleButton />
              {/* 编排是侧边栏独立页，不是任务类型；顶栏显示页标题 */}
              <span className={styles.agentType}>{t('nav.taskmanager')}</span>
              <div className={styles.actions}>
                <WorkspaceSelector
                  variant="header"
                  value={workspaceId ?? 0}
                  onChange={handleWorkspaceChange}
                  onRefresh={handleWorkspaceRefresh}
                />
              </div>
            </div>
          </div>
          <div className={styles.content}>
            {error && <ErrorBanner message={error} onClose={() => setError('')} />}
            <div className={styles.layoutBody}>
              <TaskManagerView workspaceId={workspaceId} cwd={workspaceCwd} agents={agents} restoreSessionId={navState?.tmSessionId} onError={setError} />
            </div>
          </div>
        </div>
      </AppLayout>
    )
  }

  if (hasSession && loading && !activeSession) return <LoadingSpinner text={t('common.loading')} />

  // ============ 统一模式渲染（数据驱动） ============
  // 模式 → 布局树 → LayoutRenderer 递归渲染。新增模式不需改这里。
  // 有会话时渲染会话详情，否则落到下面的任务列表/新建任务分支。
  const modeDef = getMode(null)
  if (hasSession) {
    const ctx: PanelCtx = {
          sessionId,
          session: activeSession,
          messages,
          convState: displayConvState,
          reconnect: reconnectPlan ? { seconds: reconnectSeconds, attempt: reconnectPlan.attempt } : null,
          sending,
          onSend: handleSend,
          onCancel: handleCancel,
          backendBusy,
          commands,
          modes,
          skills,
          currentModeId,
          onSetMode: handleSetMode,
          configOptions,
          onSetConfigOption: handleSetConfigOption,
          agents: agents.map((a) => ({ type: a.type, display_name: a.display_name })),
          selectedAgent,
          onSelectAgent: () => {},
          selectedModel: '',
          probeConfigs: [],
          onSelectModel: () => {},
          agentModelFilters: selectorFilters,
          probing: false,
          pendingPermission,
          permissionResponding,
          onPermissionRespond: handlePermissionRespond,
          onPermissionCancel: handlePermissionCancel,
          executions,
          restoreRefreshKey,
          workspaceId,
          cwd: activeSession?.workspace?.cwd || '',
          onRestored: handleRestored,
          onContextCleared: () => { loadData() },
          onRestoreInputChange: setRestoreInput,
          restoreInput,
          source: activeSession?.source,
          // 打开的文档由右侧「文档预览」标签渲染；会话完成后 docReloadKey 自增触发重新读盘
          docTarget,
          docReloadKey,
          onCloseDoc: handleCloseDoc,
          hasMore,
          loadingMore,
          onLoadMore: loadMore,
          onSkillsUploaded: () => { if (sessionId) listSkills(sessionId).then((r) => setSkills(r.data.skills || [])).catch(() => {}) },
          ...({
            __chatConfig: {
              configBar: 'coding',
              emptyTitleKey: 'codingMode.chatEmptyTitle',
              emptyHintKey: 'codingMode.chatEmptyHint',
            },
          } as object),
        }

    return (
      <AppLayout sidebarProps={{ sessions, workspaceId, currentId: sessionId, onDelete: handleDeleteSession, onRename: handleRenameSession, onWorkspaceChange: handleWorkspaceChange, onWorkspaceRefresh: handleWorkspaceRefresh }}>
        <div className={styles.main}>
          <div className={styles.header}>
            <div className={styles.sysBar}>
              <SidebarToggleButton />
              {activeSession && (
                <span className={styles.agentType}>
                  {activeSession.title || agents.find((a) => a.type === activeSession.agent_type)?.display_name || activeSession.agent_type}
                </span>
              )}
              <div className={styles.actions}>
                <WorkspaceSelector
                  variant="header"
                  value={workspaceId ?? 0}
                  onChange={handleWorkspaceChange}
                  onRefresh={handleWorkspaceRefresh}
                />
                <button
                  type="button"
                  className={styles.iconBtn}
                  onClick={() => setLeftHidden((v) => !v)}
                  title={leftHidden ? t('layout.showPanels') : t('layout.hidePanels')}
                >
                  {leftHidden ? <PanelRightOpen size={16} /> : <PanelRightClose size={16} />}
                </button>
              </div>
            </div>
          </div>

          <div className={styles.content}>
          {error && (
            <ErrorBanner
              message={retryable ? `${error} (${t('common.retry')})` : error}
              onClose={() => { setError(''); setRetryable(false) }}
              onRetry={retryable ? handleRetry : undefined}
            />
          )}

          {interruptedTasks.length > 0 && !sending && (
            <div className={styles.interruptedBanner}>
              <span>
                {t('session.interruptedPrompt', { count: interruptedTasks.length, defaultValue: `上次任务因服务重启中断（共 ${interruptedTasks.length} 个）` })}
              </span>
              {interruptedTasks.map((task) => (
                <button
                  key={task.id}
                  className={styles.resumeBtn}
                  onClick={() => handleResumeInterruptedTask(task.id)}
                  title={task.prompt}
                >
                  {t('session.resendInterrupted', { defaultValue: '重发' })}: {task.prompt.slice(0, 40)}{task.prompt.length > 40 ? '...' : ''}
                </button>
              ))}
            </div>
          )}

          <div className={styles.layoutBody}>
            <LayoutRenderer node={modeDef.layout} ctx={ctx} hiddenPanels={hiddenPanels} />
          </div>
          </div>
        </div>
      </AppLayout>
    )
  }

  // ============ 无会话模式：任务列表 / 新建任务 ============
  if (!hasSession) {
    // 新建任务页（编码模式）复用统一布局：Agent/模式/模型在对话框下方配置栏选择（内置 configBar），
    // 聊天面板的输入框首次发送时创建会话（handleFirstSend）。切模式即切界面，无需先发送。
    const createCtx: PanelCtx = {
      sessionId: undefined,
      session: null,
      messages: [],
      convState: creating ? 'connecting' : 'idle',
      sending: creating,
      onSend: handleFirstSend,
      onCancel: () => {},
      commands: homeCommands,
      modes: homeModes,
      skills: homeSkills,
      currentModeId: probeConfigs.find((o) => o.category === 'mode')?.current_value || '',
      onSetMode: (modeId: string) => {
        setProbeConfigs((prev) => prev.map((o) => (o.category === 'mode' ? { ...o, current_value: modeId } : o)))
        if (selectedAgent) {
          schedulePrefsPatch({ last_agent_type: selectedAgent, agent_type: selectedAgent, configs: { mode: modeId } })
        }
      },
      // 新建任务页无 session，configOptions 直接用探测出的 probeConfigs，
      // 配置变更时更新本地 probeConfigs 并保存偏好（会话创建时随 handleFirstSend 下发）
      configOptions: probeConfigs,
      onSetConfigOption: (configId: string, value: string) => {
        const opt = probeConfigs.find((o) => o.id === configId)
        setProbeConfigs((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
        if (opt?.category === 'model') setSelectedModel(value)
        if (selectedAgent) {
          schedulePrefsPatch({
            last_agent_type: selectedAgent,
            agent_type: selectedAgent,
            configs: { [opt?.category || 'model']: value },
          })
        }
      },
      agents: agents.map((a) => ({ type: a.type, display_name: a.display_name })),
      selectedAgent,
      onSelectAgent: (val) => { setSelectedAgent(val); if (val) schedulePrefsPatch({ last_agent_type: val }) },
      selectedModel,
      probeConfigs,
      agentModelsMap,
      agentModelFilters: selectorFilters,
      onSelectAgentModel: handleSelectAgentModel,
      onSelectModel: (val) => {
        setSelectedModel(val)
        setProbeConfigs((prev) => prev.map((o) => (o.category === 'model' ? { ...o, current_value: val } : o)))
        if (selectedAgent) {
          schedulePrefsPatch({ last_agent_type: selectedAgent, agent_type: selectedAgent, configs: { model: val } })
        }
      },
      probing,
      pendingPermission: null,
      permissionResponding: false,
      onPermissionRespond: () => {},
      onPermissionCancel: () => {},
      executions: [],
      workspaceId,
      cwd: workspaceCwd,
      // 新建任务页的工作目录选择：默认显示工作区 cwd，用户可选已存在的 worktree/目录。
      selectedCwd: taskCwd || workspaceCwd,
      onSelectCwd: setTaskCwd,
      docTarget: null,
      // 新建任务页的输入框采用受控值，支持从任务管理入口预填（draftPrompt）。
      restoreInput,
      onRestoreInputChange: setRestoreInput,
      onSkillsUploaded: () => {
        listSkillsByPath(workspaceCwd || undefined)
          .then((r) => setHomeSkills(r.data.skills || []))
          .catch(() => {})
      },
      // 统一配置栏：Agent + 模式 + 模型（与会话详情页共用同一套内置 configBar）
      ...({
        __chatConfig: {
          configBar: 'coding',
          emptyTitleKey: 'codingMode.chatEmptyTitle',
          emptyHintKey: 'codingMode.chatEmptyHint',
        },
      } as object),
    }

    if (!isCreateMode) {
      // 任务列表页不再展示历史列表：数据就绪后由 effect 自动跳转（最近任务 → 会话详情；无任务 → 新建任务页）。
      // 跳转完成前渲染加载占位，避免闪烁历史列表。
      return (
        <AppLayout sidebarProps={{ sessions, workspaceId, onDelete: handleDeleteSession, onRename: handleRenameSession, onWorkspaceChange: handleWorkspaceChange, onWorkspaceRefresh: handleWorkspaceRefresh }}>
          <LoadingSpinner text={t('common.loading')} />
        </AppLayout>
      )
    }

    return (
      <AppLayout sidebarProps={{ sessions, workspaceId, onDelete: handleDeleteSession, onRename: handleRenameSession, onWorkspaceChange: handleWorkspaceChange, onWorkspaceRefresh: handleWorkspaceRefresh }}>
        <div className={styles.main}>
          <div className={`${styles.header} ${styles.headerSingle}`}>
            <div className={styles.sessionInfo}>
              <SidebarToggleButton />
            </div>
            <div className={styles.actions}>
              <WorkspaceSelector
                variant="header"
                value={workspaceId ?? 0}
                onChange={handleWorkspaceChange}
                onRefresh={handleWorkspaceRefresh}
              />
              <button
                type="button"
                className={styles.iconBtn}
                onClick={() => setLeftHidden((v) => !v)}
                title={leftHidden ? t('layout.showPanels') : t('layout.hidePanels')}
              >
                {leftHidden ? <PanelRightOpen size={16} /> : <PanelRightClose size={16} />}
              </button>
            </div>
          </div>

          <div className={styles.content}>
          {error && <ErrorBanner message={error} onClose={() => setError('')} />}

          <div className={styles.layoutBody}>
            <LayoutRenderer node={modeDef.layout} ctx={createCtx} hiddenPanels={hiddenPanels} />
          </div>
          </div>
        </div>

      </AppLayout>
    )
  }
}
