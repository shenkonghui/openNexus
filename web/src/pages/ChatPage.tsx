import { useState, useEffect, useCallback, useRef } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { getSession, listMessages, cancelSession, listCommands, listModes, listSkills, listConfigOptions, setConfigOption, setSessionMode, respondPermission, deleteSession, updateSessionTitle, createSession, resumeSession, listSessionExecutions, getInterruptedTasks, setSessionYolo } from '../api/sessions'
import { getWorkspace } from '../api/workspaces'
import { listScheduledTasks, listExecutions } from '../api/scheduledTasks'
import { listAgents, probeAgentConfigs, preconnectAgent, listAgentCommands, listAgentModes } from '../api/agents'
import { listSkillsByPath } from '../api/filesystem'
import { getAgentPrefs, patchAgentPrefs } from '../api/agentPrefs'
import { WORKSPACE_STORAGE_KEY, useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { applyPrefsToConfigs, configsFromProbe, takeLegacyLocalAgentPrefs } from '../utils/agentPrefs'
import { streamPrompt, subscribeStream, streamResumeTask, isTimeoutError, isSessionInactiveError } from '../api/sse'
import { tasksUrl, newTaskUrl, sessionUrl, isNewTaskPath, isOrchestrationPath } from '../utils/routes'
import type { Session, Message, AgentCommand, ConfigOption, SessionMode, AgentSkill, Execution, Agent, PermissionRequestPayload, RunningTask, AgentPrefs } from '../types'
import { parsePermissionRequest } from '../utils/permission'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import { type ConvState as ConvStatusState } from '../components/ConvStatusBar'
import TaskModeSwitch, { type TaskMode } from '../components/TaskModeSwitch'
import OrchestrationView from '../components/OrchestrationView'
import { Plus, PanelRightClose, PanelRightOpen, Zap } from 'lucide-react'
import { useFileViewer } from '../context/FileViewerContext'
import { saveLastDoc, loadDocFolders, loadDocSession, saveDocSession, clearDocSession, TASK_MODE_KEY, LAST_DOC_KEY_PREFIX, type DocTarget } from '../utils/docs'
import { docEditSystemPrompt, docSessionTitle } from '../utils/docPrompt'
import LayoutRenderer from '../modes/LayoutRenderer'
import { getMode } from '../modes/registry'
import type { PanelCtx } from '../modes/types'
import styles from './ChatPage.module.css'

// navigate 时携带的 state：initialPrompt/createdSession 用于新建会话跳转；
// doc 用于侧边栏点击文档时，切到文档模式并打开指定文档。
// taskMode 用于外部入口（如任务编排）强制指定顶层模式；draftPrompt 用于新建任务页预填输入框。
type NavigateState = { initialPrompt?: string; createdSession?: Session; doc?: DocTarget; taskMode?: TaskMode; draftPrompt?: string; orchSessionId?: number }

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
  // 文档模式下，左侧「文件」浏览器选中的文件（绝对路径），优先作为当前文档。
  const { openFilePath } = useFileViewer()
  const initialPromptRef = useRef<string>('')
  const bootstrappedSessionIdRef = useRef<number | null>(null)
  // location.state 变化时同步到 ref（navigate 跳转不会重新挂载组件，useRef 不会自动更新）
  const navState = location.state as NavigateState | null
  if (navState?.initialPrompt) {
    initialPromptRef.current = navState.initialPrompt
  }

  // 顶层 UI 模式（与 ACP SessionMode 正交）。localStorage 记忆，默认 coding。
  // 模式来自 registry；未识别的值回退到首个模式。
  // navState.taskMode 优先：从任务编排等入口打开时强制指定（如“默认编程模式”）。
  // 编排深链接（/orchestration）：检测路径初始化为编排模式。
  // 编排不是「新建任务」可选类型：非编排路径下忽略持久化的 orchestration，回退 coding。
  const [taskMode, setTaskMode] = useState<TaskMode>(() => {
    if (navState?.taskMode) return navState.taskMode
    if (isOrchestrationPath(location.pathname)) return 'orchestration'
    const stored = localStorage.getItem(TASK_MODE_KEY) || 'coding'
    return stored === 'orchestration' ? 'coding' : stored
  })
  const handleTaskModeChange = useCallback((m: TaskMode) => {
    setTaskMode(m)
    localStorage.setItem(TASK_MODE_KEY, m)
  }, [])

  // 编排深链接 ↔ 其他路径：ChatPage 同实例复用时需随 pathname 同步 taskMode。
  // 进入编排路径 → 强制 orchestration；离开后若仍停在 orchestration → 回退 coding，
  // 否则「新建任务」仍会命中编排分支，顶栏锁死为「编排」。
  useEffect(() => {
    if (isOrchestrationPath(location.pathname)) {
      if (taskMode !== 'orchestration') {
        setTaskMode('orchestration')
        localStorage.setItem(TASK_MODE_KEY, 'orchestration')
      }
      return
    }
    if (taskMode === 'orchestration') {
      setTaskMode('coding')
      localStorage.setItem(TASK_MODE_KEY, 'coding')
    }
  }, [location.pathname, taskMode])

  // 隐藏侧栏面板，仅保留对话。编码隐藏文件/终端等；文档隐藏预览。
  const LEFT_PANELS_HIDDEN_KEY = 'opennexus.layout.sideHidden'
  const [leftHidden, setLeftHidden] = useState<boolean>(() => localStorage.getItem(LEFT_PANELS_HIDDEN_KEY) === '1')
  useEffect(() => {
    localStorage.setItem(LEFT_PANELS_HIDDEN_KEY, leftHidden ? '1' : '0')
  }, [leftHidden])
  const sidePanelsByMode: Record<string, string[]> = {
    coding: ['files', 'terminal', 'changes', 'debug', 'browser'],
    docs: ['doc-preview'],
  }
  const sidePanels = sidePanelsByMode[taskMode]
  const hiddenPanels = leftHidden && sidePanels ? new Set(sidePanels) : undefined
  const canTogglePanels = !!sidePanels

  // 文档模式当前打开的文档。侧边栏点击文档时通过 navigate state 传入；否则读 localStorage 上次打开的。
  const [docTarget, setDocTarget] = useState<DocTarget | null>(() => {
    if (navState?.doc) return navState.doc
    try {
      const stored = localStorage.getItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0))
      if (stored) return JSON.parse(stored) as DocTarget
    } catch { /* ignore */ }
    return null
  })
  // 侧边栏点击文档会 navigate 到 tasks 页并带 doc state，这里响应 state 变化
  useEffect(() => {
    if (navState?.doc) {
      // 同一工作区的所有文档共用同一个会话，切换文档不再重置对话；
      // 仅更新预览目标与记忆。会话的加载/重置由「工作区共享会话恢复」effect 负责。
      setDocTarget(navState.doc)
      // 自动切到文档模式
      if (taskMode !== 'docs') {
        setTaskMode('docs')
        localStorage.setItem(TASK_MODE_KEY, 'docs')
      }
      localStorage.setItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0), JSON.stringify(navState.doc))
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [navState?.doc])

  // workspaceId 就绪后恢复上次打开的文档。
  // docTarget 的 useState 初始化在首次渲染执行，此时 workspaceId 可能尚未就绪
  //（URL 无 wid 时依赖 useCurrentWorkspace 异步加载），导致 localStorage key 不匹配而读不到。
  // 这里在 workspaceId 变化且未通过侧边栏点击（无 navState.doc）时，重新从 localStorage 恢复。
  // 仅在当前已是文档模式时恢复 docTarget——避免用户主动切到编码模式后被强制切回。
  const restoredDocRef = useRef<number | null>(null)
  useEffect(() => {
    if (!workspaceId || navState?.doc) return
    if (restoredDocRef.current === workspaceId) return
    restoredDocRef.current = workspaceId
    if (taskMode !== 'docs') return
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

  // ===== 文档模式专用状态（独立于编码模式的 session/messages） =====
  // 文档模式复用原生 PromptInput + MessageList，但维护自己的 session，
  // 这样编码会话和文档对话互不干扰。
  const [docMessages, setDocMessages] = useState<Message[]>([])
  const [docSession, setDocSession] = useState<Session | null>(null)
  const [docConvState, setDocConvState] = useState<ConvState>('idle')
  // AI 直接编辑磁盘文件后，自增此值触发文档预览重新读取。
  const [docReloadKey, setDocReloadKey] = useState(0)
  // 文档会话激活后加载的模式 / 配置项（复用编码对话框的模式/模型选择器）。
  const [docModes, setDocModes] = useState<SessionMode[]>([])
  const [docConfigOptions, setDocConfigOptions] = useState<ConfigOption[]>([])
  const [docCurrentModeId, setDocCurrentModeId] = useState('')
  // 文档模式独立的权限队列（doc AI 直接编辑磁盘会触发权限请求）。
  const [docPendingPermission, setDocPendingPermission] = useState<PermissionRequestPayload | null>(null)
  const [docPermissionResponding, setDocPermissionResponding] = useState(false)
  const docPermissionQueueRef = useRef<PermissionRequestPayload[]>([])
  const docWaitingPermissionRef = useRef(false)
  const docAbortRef = useRef<AbortController | null>(null)
  const docMountedRef = useRef(true)
  useEffect(() => { docMountedRef.current = true; return () => { docMountedRef.current = false } }, [])
  // 上一次发送时的文档路径：共享会话跨文档时，文档变化需向 AI 重新图的当前目标。
  const lastDocPathRef = useRef('')
  // 已为哪个工作区恢复过文档共享会话，避免重复拉取与覆盖流式中的消息。
  const docRestoredWorkspaceRef = useRef<number | null>(null)

  // 会话相关状态
  const [session, setSession] = useState<Session | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [restoreRefreshKey, setRestoreRefreshKey] = useState(0)
  // navState.draftPrompt 优先：从任务编排打开未运行任务时，用任务详情预填新建任务输入框。
  const [restoreInput, setRestoreInput] = useState<string | undefined>(() => navState?.draftPrompt)
  // 侧边栏点击编排任务会 navigate 到新建任务页并带 taskMode/draftPrompt。当目标就是当前路由时
  // （navigate 不重新挂载组件），仅靠上面的初始化不会生效，表现为“点击无反应”。这里以 location.key
  // 为依赖响应每次跳转，重新应用模式与预填内容。
  useEffect(() => {
    if (navState?.taskMode) {
      setTaskMode(navState.taskMode)
      localStorage.setItem(TASK_MODE_KEY, navState.taskMode)
    }
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
  const [creating, setCreating] = useState(false)
  // 新建任务页：YOLO 草稿，创建会话时写入
  const [yoloDraft, setYoloDraft] = useState(false)
  const [yoloSaving, setYoloSaving] = useState(false)
  const [homeCommands, setHomeCommands] = useState<AgentCommand[]>([])
  const [homeModes, setHomeModes] = useState<SessionMode[]>([])
  const [homeSkills, setHomeSkills] = useState<AgentSkill[]>([])
  const [workspaceCwd, setWorkspaceCwd] = useState('')
  // 新建任务页：用户选择的自定义工作目录（如已存在的 git worktree）；为空则跟随工作区 cwd。
  const [taskCwd, setTaskCwd] = useState('')
  const [agentPrefs, setAgentPrefs] = useState<AgentPrefs>({ last_agent_type: '', prefs: {} })
  const prefsSaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const agentPrefsRef = useRef(agentPrefs)
  agentPrefsRef.current = agentPrefs

  const bootstrapSession = navState?.createdSession?.id === sessionId ? navState.createdSession : null
  const isCreateMode = !hasSession && isNewTaskPath(location.pathname, workspaceId)
  const isDocMode = taskMode === 'docs' && !hasSession
  const activeSession = session ?? bootstrapSession

  // 当前文档的绝对路径：优先取左侧文件浏览器选中项，否则由 docTarget 解析。
  const activeDocAbsPath: string = (() => {
    if (openFilePath) return openFilePath
    if (docTarget) {
      const found = loadDocFolders().find((d) => d.id === docTarget.folderId)
      if (found) return `${found.path.replace(/\/$/, '')}/${docTarget.filePath}`
    }
    return ''
  })()

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
    if (hasSession) {
      localStorage.setItem(WORKSPACE_STORAGE_KEY, String(id))
      navigate(tasksUrl(id))
      return
    }
    // 无会话模式：同步更新 URL，使刷新/书签/后退按钮都能定位到正确工作区
    navigate(isCreateMode ? newTaskUrl(id) : tasksUrl(id))
  }

  function handleWorkspaceRefresh() {
    reloadWorkspace()
      .then(() => (hasSession ? loadData({ quiet: true }) : loadHomeData()))
      .catch((err) => setError(err instanceof Error ? err.message : t('common.failed')))
  }

  // 监听 agent 变化，探测 config options（新建页与文档模式都需要预探供模型选择）
  useEffect(() => {
    if (hasSession || (!isCreateMode && !isDocMode) || !selectedAgent) {
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
        const applied = applyPrefsToConfigs(opts, agentPrefsRef.current.prefs[selectedAgent])
        setProbeConfigs(applied)
        const modelOpt = applied.find((o) => o.category === 'model')
        setSelectedModel(modelOpt?.current_value || '')
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
  }, [selectedAgent, hasSession, isCreateMode, isDocMode])

  // 新建页：提前预连接 agent，减少首次发送时的冷启动等待。
  // agent 选中后立即用 probe cwd 预热；workspaceCwd 就绪后若不同则再次预热。
  useEffect(() => {
    if (hasSession || (!isCreateMode && !isDocMode) || !selectedAgent) return
    preconnectAgent(selectedAgent, workspaceCwd)
  }, [selectedAgent, workspaceCwd, hasSession, isCreateMode, isDocMode])

  // 新建任务页：加载 agent 级 slash command / mode（探测完成后刷新）
  useEffect(() => {
    if (hasSession || (!isCreateMode && !isDocMode) || !selectedAgent || probing) {
      if (hasSession || (!isCreateMode && !isDocMode)) {
        setHomeCommands([])
        setHomeModes([])
      }
      return
    }
    listAgentCommands(selectedAgent, workspaceCwd || undefined).then((r) => setHomeCommands(r.data.commands || [])).catch(() => setHomeCommands([]))
    listAgentModes(selectedAgent).then((r) => setHomeModes(r.data.modes || [])).catch(() => setHomeModes([]))
  }, [selectedAgent, hasSession, isCreateMode, isDocMode, probing, workspaceCwd])

  // 新建任务页：加载 skills（与 agent 无关；cwd 为空时仍扫用户目录）
  useEffect(() => {
    if (hasSession || (!isCreateMode && !isDocMode)) {
      setHomeSkills([])
      return
    }
    listSkillsByPath(workspaceCwd || undefined)
      .then((r) => setHomeSkills(r.data.skills || []))
      .catch(() => setHomeSkills([]))
  }, [workspaceCwd, hasSession, isCreateMode, isDocMode])

  // 文档助手会话创建后加载其模式 / 配置项，供对话框的模式/模型选择器使用（编码对话框形式）。
  useEffect(() => {
    if (!docSession) { setDocModes([]); setDocConfigOptions([]); return }
    const id = docSession.id
    listModes(id).then((r) => {
      const list = r.data.modes || []
      setDocModes(list)
      if (list.length > 0) setDocCurrentModeId((prev) => prev || list[0].id)
    }).catch(() => setDocModes([]))
    listConfigOptions(id).then((r) => setDocConfigOptions(r.data.config_options || [])).catch(() => setDocConfigOptions([]))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [docSession?.id])

  // 文档模式：会话进入非活跃态时清除挂起权限（与编码模式同因——接收方已失效，避免死权限栏）。
  useEffect(() => {
    if (!docSession) return
    if (docSession.status === 'active' || docSession.status === 'pending') return
    if (docWaitingPermissionRef.current || docPendingPermission) {
      clearDocPermissions()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [docSession?.status])

  // 工作区共享会话恢复：进入文档模式 / 切换工作区时，加载该工作区持久化的
  // 文档会话及其历史，使同一工作区的文档对话可跨文档 / 跨刷新恢复。
  useEffect(() => {
    if (!user || !isDocMode || !workspaceId) return
    if (docRestoredWorkspaceRef.current === workspaceId) return
    docRestoredWorkspaceRef.current = workspaceId
    const persisted = loadDocSession(workspaceId)
    if (!persisted) { setDocSession(null); setDocMessages([]); lastDocPathRef.current = ''; return }
    Promise.all([getSession(persisted), listMessages(persisted)])
      .then(([s, m]) => {
        if (!docMountedRef.current) return
        setDocSession(s.data)
        setDocMessages(m.data.messages || [])
      })
      .catch(() => {
        // 会话已被删除 / 失效：清除记忆，回到空对话（首次发送时重建）
        clearDocSession(workspaceId)
        if (!docMountedRef.current) return
        setDocSession(null)
        setDocMessages([])
        lastDocPathRef.current = ''
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [user, isDocMode, workspaceId])

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
    // 编排页：路径已是 /orchestration 但 taskMode 可能尚未同步（同实例路由切换），
    // 必须按路径短路，否则会被下方「跳最近任务」抢走，表现为点编排进不去。
    if (taskMode === 'orchestration' || isOrchestrationPath(location.pathname)) return
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
  }, [user, hasSession, isCreateMode, wsLoading, workspaceId, sessionsWorkspaceId, sessions, navigate, taskMode, location.pathname])

  // 当组件卸载或切换到不同会话时，中断旧的 SSE 流，防止内存泄漏和 React 警告
  useEffect(() => {
    mountedRef.current = true
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
      if (abortRef.current == null) {
        setConvState((s) => (
          s === 'streaming' || s === 'reconnecting' || s === 'connecting' ? 'idle' : s
        ))
      }
      // 流式进行时（abortRef 非空）跳过消息拉取，避免 DB 数据覆盖实时 SSE 流，
      // 也避免每 5 秒整体替换 messages 数组触发全量重渲染。
      loadData({ quiet: true, skipMessages: !!abortRef.current })
    }, 5000)

    return () => clearInterval(interval)
  }, [hasSession, session?.id, session?.status, session?.source, loadData])

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
      const resp = await createSession(selectedAgent, workspaceId || 0, selectedModel || undefined, undefined, taskCwd || undefined, yoloDraft)
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

  // ===== 文档模式权限队列（与编码模式隔离，绑定 docSession.id） =====
  function enqueueDocPermission(req: PermissionRequestPayload) {
    docWaitingPermissionRef.current = true
    setDocPendingPermission((prev) => {
      if (prev?.request_id === req.request_id) return prev
      if (!prev) return req
      if (!docPermissionQueueRef.current.some((p) => p.request_id === req.request_id)) {
        docPermissionQueueRef.current = [...docPermissionQueueRef.current, req]
      }
      return prev
    })
    setDocConvState('waiting_permission')
  }

  function clearDocPermissions() {
    docPermissionQueueRef.current = []
    docWaitingPermissionRef.current = false
    setDocPendingPermission(null)
  }

  function advanceDocPermissionQueue() {
    const next = docPermissionQueueRef.current.shift() || null
    docWaitingPermissionRef.current = !!next
    setDocPendingPermission(next)
    if (next) setDocConvState('waiting_permission')
    else setDocConvState((s) => (s === 'waiting_permission' ? 'streaming' : s))
  }

  async function handleDocPermissionRespond(optionId: string) {
    if (!docPendingPermission || !docSession) return
    setDocPermissionResponding(true)
    setError('')
    try {
      const current = docPendingPermission
      const queued = optionId === 'allow-always' ? [...docPermissionQueueRef.current] : []
      if (optionId === 'allow-always') docPermissionQueueRef.current = []
      await respondPermission(docSession.id, current.request_id, optionId)
      for (const req of queued) {
        try { await respondPermission(docSession.id, req.request_id, optionId) } catch { /* 后端可能已批量处理 */ }
      }
      if (optionId === 'allow-always') {
        docWaitingPermissionRef.current = false
        setDocPendingPermission(null)
        setDocConvState('streaming')
      } else {
        advanceDocPermissionQueue()
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('common.failed')
      if (isSessionInactiveError(msg)) clearDocPermissions()
      setError(msg)
    } finally {
      setDocPermissionResponding(false)
    }
  }

  async function handleDocPermissionCancel() {
    if (!docPendingPermission || !docSession) return
    setDocPermissionResponding(true)
    setError('')
    try {
      await respondPermission(docSession.id, docPendingPermission.request_id, '', true)
      advanceDocPermissionQueue()
    } catch (err) {
      const msg = err instanceof Error ? err.message : t('common.failed')
      if (isSessionInactiveError(msg)) clearDocPermissions()
      setError(msg)
    } finally {
      setDocPermissionResponding(false)
    }
  }

  // ===== 文档模式发送：复用原生 streamPrompt 链路，维护独立的 docSession/docMessages =====
  // doc AI 像编码模式一样直接读写磁盘上的 .md 文件；完成后自增 docReloadKey 让预览重新读盘。
  // 仍可在文档中嵌入 ```drawio XML 代码块，预览会自动渲染。
  async function handleDocSend(prompt: string) {
    const docPath = activeDocAbsPath
    if (!docPath || !workspaceId) return
    const text = prompt.trim()
    if (!text) return
    setDocConvState('connecting')
    setError('')

    // 首次发送：创建工作区共享 session，并前置注入文档编辑 system prompt
    let sid = docSession?.id
    let promptToSend = text
    if (!sid) {
      try {
        let agentType = selectedAgent || 'claude-code'
        if (!selectedAgent) {
          try {
            const prefs = await getAgentPrefs()
            if (prefs.data.last_agent_type) agentType = prefs.data.last_agent_type
          } catch { /* 回退默认 */ }
        }
        const resp = await createSession(agentType, workspaceId, selectedModel || undefined, undefined, undefined, yoloDraft)
        sid = resp.data.id
        setDocSession(resp.data)
        // 持久化为该工作区的文档共享会话，后续打开任意文档都复用它
        saveDocSession(workspaceId, sid)
        docRestoredWorkspaceRef.current = workspaceId
        const docName = docPath.split('/').pop() || docPath
        try { await updateSessionTitle(sid, docSessionTitle(docName)) } catch { /* 忽略 */ }
        promptToSend = `${docEditSystemPrompt(docPath)}\n\n===\n\n用户请求：${text}`
        lastDocPathRef.current = docPath
      } catch (err) {
        setError(t('docAI.initError', { message: err instanceof Error ? err.message : String(err) }))
        setDocConvState('idle')
        return
      }
    } else if (docPath !== lastDocPathRef.current) {
      // 共享会话内切换到另一篇文档：提示 AI 当前操作目标已变（携带完整编辑约定）
      promptToSend = `${docEditSystemPrompt(docPath)}\n\n===\n\n用户请求：${text}`
      lastDocPathRef.current = docPath
    }

    // 乐观追加用户消息（sequence=0，实时流到达后按 sequence 去重替换）
    const optimisticId = -Date.now()
    const userMsg: Message = {
      id: optimisticId, session_id: String(sid), role: 'user', kind: 'user_message_chunk',
      content: text, raw_json: '', sequence: 0, execution_id: null, created_at: new Date().toISOString(),
    }
    setDocMessages((prev) => [...prev, userMsg])

    const ac = new AbortController()
    docAbortRef.current = ac
    setDocConvState('streaming')

    await streamPrompt(
      sid,
      promptToSend,
      (msg) => {
        if (!docMountedRef.current) return
        if (msg.kind === 'permission_request') {
          const req = parsePermissionRequest(msg.raw_json)
          if (req) enqueueDocPermission(req)
        }
        // 按 sequence 去重：避免轮询补齐与实时流重复
        setDocMessages((prev) => {
          const rest = prev.filter((m) => m.id === optimisticId || m.sequence < msg.sequence)
          const noOptimistic = rest.filter((m) => m.id !== optimisticId)
          return [...noOptimistic, msg]
        })
        setDocConvState((s) => (s === 'waiting_permission' ? s : 'streaming'))
      },
      () => {
        if (!docMountedRef.current) return
        docAbortRef.current = null
        clearDocPermissions()
        setDocConvState('idle')
        // AI 可能已直接修改磁盘文件，触发文档预览重新读盘
        setDocReloadKey((k) => k + 1)
      },
      (err) => {
        if (!docMountedRef.current) return
        docAbortRef.current = null
        clearDocPermissions()
        setDocConvState('idle')
        setError(t('docAI.sendFailed', { message: err.message }))
      },
      {
        signal: ac.signal,
        shouldPauseIdleTimeout: () => docWaitingPermissionRef.current,
      },
    )
  }

  async function handleDocCancel() {
    if (!docSession) return
    try { await cancelSession(docSession.id) } catch { /* 尽力 */ }
    docAbortRef.current?.abort()
    clearDocPermissions()
    setDocConvState('idle')
  }

  // 文档模式「新建」：清空当前工作区共享的文档会话与历史，下次发送时重建新会话。
  // 与编码模式的新建任务对应，使顶部栏在不同任务下保持一致。
  function handleNewDocSession() {
    if (workspaceId) clearDocSession(workspaceId)
    docAbortRef.current?.abort()
    docAbortRef.current = null
    clearDocPermissions()
    setDocSession(null)
    setDocMessages([])
    setDocModes([])
    setDocConfigOptions([])
    setDocCurrentModeId('')
    lastDocPathRef.current = ''
    // 已恢复标记指向当前工作区，避免恢复 effect 又拉回旧会话
    docRestoredWorkspaceRef.current = workspaceId ?? null
    setDocConvState('idle')
    setError('')
  }

  // 文档对话框模式选择（编码对话框形式）：会话未创建时仅本地记录，已创建则下发。
  async function handleDocSetMode(modeId: string) {
    if (!modeId || modeId === docCurrentModeId) return
    setError('')
    setDocCurrentModeId(modeId)
    if (docSession) {
      try { await setSessionMode(docSession.id, modeId) } catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
    }
  }

  // 文档对话框模型/配置选择：会话未创建时等同于预探模型选择，已创建则下发到会话。
  async function handleDocSetConfigOption(configId: string, value: string) {
    setError('')
    if (!docSession) {
      setSelectedModel(value)
      setProbeConfigs((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
      if (selectedAgent) {
        const opt = probeConfigs.find((o) => o.id === configId)
        schedulePrefsPatch({
          last_agent_type: selectedAgent,
          agent_type: selectedAgent,
          configs: { [opt?.category || 'model']: value },
        })
      }
      return
    }
    // docConfigOptions 为空时界面回退展示 probeConfigs（见 docEffectiveConfigOptions），
    // 故在两处都查找 opt 并同步更新，确保回退态下用户的选择也能即时反映。
    const opt = docConfigOptions.find((o) => o.id === configId) || probeConfigs.find((o) => o.id === configId)
    setDocConfigOptions((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
    setProbeConfigs((prev) => prev.map((o) => (o.id === configId ? { ...o, current_value: value } : o)))
    if (opt?.category === 'model') setSelectedModel(value)
    try {
      await setConfigOption(docSession.id, configId, value)
      if (opt?.category && docSession.agent_type) {
        schedulePrefsPatch({
          last_agent_type: docSession.agent_type,
          agent_type: docSession.agent_type,
          configs: { [opt.category]: value },
        })
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
      listConfigOptions(docSession.id).then((r) => setDocConfigOptions(r.data.config_options || [])).catch(() => {})
    }
  }

  const displayDocConvState: ConvState = docConvState

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

  // 按当前会话开关 YOLO：编码会话 / 文档会话各自独立；尚无会话时改草稿，创建时写入。
  async function handleToggleYolo() {
    const target = taskMode === 'docs' ? docSession : (hasSession ? activeSession : null)
    if (!target) {
      setYoloDraft((v) => !v)
      return
    }
    if (yoloSaving) return
    setYoloSaving(true)
    setError('')
    try {
      const resp = await setSessionYolo(target.id, !target.yolo)
      if (taskMode === 'docs') setDocSession(resp.data)
      else setSession(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setYoloSaving(false)
    }
  }

  const yoloTarget = taskMode === 'docs' ? docSession : (hasSession ? activeSession : null)
  const yoloEnabled = yoloTarget ? !!yoloTarget.yolo : yoloDraft

  if (authLoading) return <LoadingSpinner text={t('common.loading')} />
  if (!user) return null

  // ============ 编排模式：在任务界面内管理编排任务（不走 LayoutRenderer，与 docs 特判并列） ============
  // 以路径为准：同实例从会话页切到 /orchestration 时 taskMode 可能尚未同步，不能只看 state。
  if (isOrchestrationPath(location.pathname)) {
    return (
      <AppLayout sidebarProps={{ sessions, workspaceId, onDelete: handleDeleteSession, onRename: handleRenameSession, onWorkspaceChange: handleWorkspaceChange, onWorkspaceRefresh: handleWorkspaceRefresh }}>
        <div className={styles.main}>
          <div className={styles.header}>
            <div className={styles.sysBar}>
              <SidebarToggleButton />
              {/* 编排是侧边栏独立页，不是任务类型；顶栏显示页标题，不走 TaskModeSwitch */}
              <span className={styles.agentType}>{t('nav.orchestration')}</span>
              <div className={styles.actions}>
              </div>
            </div>
          </div>
          <div className={styles.content}>
            {error && <ErrorBanner message={error} onClose={() => setError('')} />}
            <div className={styles.layoutBody}>
              <OrchestrationView workspaceId={workspaceId} cwd={workspaceCwd} agents={agents} restoreSessionId={navState?.orchSessionId} onError={setError} />
            </div>
          </div>
        </div>
      </AppLayout>
    )
  }

  if (hasSession && loading && !activeSession) return <LoadingSpinner text={t('common.loading')} />

  // ============ 统一模式渲染：编码 / 文档（数据驱动） ============
  // 模式 → 布局树 → LayoutRenderer 递归渲染。新增模式不需改这里。
  // 文档模式优先于"无会话"分支：它有独立的 docSession，不依赖编码会话存在。
  // 编码模式要求有会话，否则落到下面的任务列表/新建任务分支。
  const modeDef = getMode(taskMode)
  const shouldRenderLayout = modeDef.sessionKind === 'docs' || hasSession
  if (shouldRenderLayout) {
    // 构造 PanelCtx：按模式选择对应的会话生命周期
    const isDocs = modeDef.sessionKind === 'docs'
    // 文档会话的模式/配置项按需从会话级缓存获取；服务重启或 agent 预探尚未完成时可能为空，
    // 且该拉取仅在 docSession.id 变化时执行一次、不重试，会导致「文档任务下模式/模型选项出不来」。
    // 这里回退到 agent 级的 homeModes/probeConfigs（其 effect 随 isDocMode 持续加载），保证选项稳定出现。
    const docEffectiveModes = docSession && docModes.length > 0 ? docModes : homeModes
    const docEffectiveConfigOptions = docSession && docConfigOptions.length > 0 ? docConfigOptions : probeConfigs
    const docEffectiveModeId = docCurrentModeId || docEffectiveModes[0]?.id || ''
    const ctx: PanelCtx = isDocs
      ? {
          sessionKind: 'docs',
          sessionId: docSession?.id,
          session: docSession,
          messages: docMessages,
          convState: displayDocConvState,
          sending: displayDocConvState !== 'idle',
          onSend: handleDocSend,
          onCancel: handleDocCancel,
          commands: homeCommands,
          modes: docEffectiveModes,
          skills: homeSkills,
          currentModeId: docEffectiveModeId,
          onSetMode: handleDocSetMode,
          configOptions: docEffectiveConfigOptions,
          onSetConfigOption: handleDocSetConfigOption,
          agents: agents.map((a) => ({ type: a.type, display_name: a.display_name })),
          selectedAgent,
          onSelectAgent: (val) => {
            setSelectedAgent(val)
            if (val) schedulePrefsPatch({ last_agent_type: val })
          },
          selectedModel,
          probeConfigs,
          onSelectModel: (val) => {
            setSelectedModel(val)
            setProbeConfigs((prev) => prev.map((o) => (o.category === 'model' ? { ...o, current_value: val } : o)))
            if (selectedAgent) {
              schedulePrefsPatch({
                last_agent_type: selectedAgent,
                agent_type: selectedAgent,
                configs: { model: val },
              })
            }
          },
          probing,
          pendingPermission: docPendingPermission,
          permissionResponding: docPermissionResponding,
          onPermissionRespond: handleDocPermissionRespond,
          onPermissionCancel: handleDocPermissionCancel,
          executions: [],
          workspaceId,
          cwd: workspaceCwd,
          // 文件浏览器选中的文件也视为当前文档（使输入框可用、预览可渲染）
          docTarget: docTarget || (openFilePath ? { folderId: '', filePath: openFilePath } : null),
          docContent: '',
          onDocContentChange: () => {},
          docReloadKey,
          onCloseDoc: handleCloseDoc,
          // 文档对话列复用编码对话框形式（模式/模型选择器），仅空态文案不同
          ...({
            __chatConfig: {
              configBar: 'coding',
              emptyTitleKey: 'docMode.chatEmptyTitle',
              emptyHintKey: 'docMode.chatEmptyHint',
              placeholderKey: 'docAI.placeholder',
              selectDocFirstKey: 'docMode.selectDocFirst',
            },
          } as object),
        }
      : {
          sessionKind: 'primary',
          sessionId,
          session: activeSession,
          messages,
          convState: displayConvState,
          sending,
          onSend: handleSend,
          onCancel: handleCancel,
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
          docTarget: null,
          docContent: '',
          onDocContentChange: () => {},
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
              {/* 无编码会话（新建任务 / 文档待选）时可切换编码·文档；已有会话后锁定。编排走侧边栏。 */}
              <TaskModeSwitch
                value={taskMode}
                onChange={handleTaskModeChange}
                disabled={hasSession}
              />
              {canTogglePanels && (
                <div className={styles.centerToggle}>
                  <button
                    type="button"
                    className={styles.iconBtn}
                    onClick={() => setLeftHidden((v) => !v)}
                    title={leftHidden ? t('layout.showPanels') : t('layout.hidePanels')}
                  >
                    {leftHidden ? <PanelRightOpen size={16} /> : <PanelRightClose size={16} />}
                  </button>
                </div>
              )}
              <div className={styles.actions}>
                <button
                  type="button"
                  className={`${styles.yoloBtn} ${yoloEnabled ? styles.yoloBtnOn : ''}`}
                  onClick={handleToggleYolo}
                  disabled={yoloSaving}
                  title={t('session.yoloHint')}
                >
                  <Zap size={14} />
                  {yoloEnabled ? t('session.yoloOn') : t('session.yoloOff')}
                </button>
                <button
                  type="button"
                  className={styles.newTaskBtn}
                  onClick={() => (isDocs ? handleNewDocSession() : navigate(newTaskUrl(workspaceId)))}
                  title={t('session.newSession')}
                >
                  <Plus size={16} />
                </button>
              </div>
            </div>
          </div>

          <div className={styles.content}>
          {error && (
            <ErrorBanner
              message={retryable && !isDocs ? `${error} (${t('common.retry')})` : error}
              onClose={() => { setError(''); setRetryable(false) }}
              onRetry={!isDocs && retryable ? handleRetry : undefined}
            />
          )}

          {!isDocs && interruptedTasks.length > 0 && !sending && (
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
      sessionKind: 'primary',
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
      docContent: '',
      onDocContentChange: () => {},
      // 新建任务页的输入框采用受控值，支持从任务编排入口预填（draftPrompt）。
      restoreInput,
      onRestoreInputChange: setRestoreInput,
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
              {/* 新建任务可选编码/文档；编排走侧边栏「任务编排」 */}
              <TaskModeSwitch value={taskMode} onChange={handleTaskModeChange} />
            </div>
            {canTogglePanels && (
              <div className={styles.centerToggle}>
                <button
                  type="button"
                  className={styles.iconBtn}
                  onClick={() => setLeftHidden((v) => !v)}
                  title={leftHidden ? t('layout.showPanels') : t('layout.hidePanels')}
                >
                  {leftHidden ? <PanelRightOpen size={16} /> : <PanelRightClose size={16} />}
                </button>
              </div>
            )}
            <div className={styles.actions}>
              <button
                type="button"
                className={`${styles.yoloBtn} ${yoloEnabled ? styles.yoloBtnOn : ''}`}
                onClick={handleToggleYolo}
                disabled={yoloSaving}
                title={t('session.yoloHint')}
              >
                <Zap size={14} />
                {yoloEnabled ? t('session.yoloOn') : t('session.yoloOff')}
              </button>
              <button type="button" className={styles.newTaskBtn} onClick={() => navigate(newTaskUrl(workspaceId))} title={t('session.newSession')}><Plus size={16} /></button>
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
