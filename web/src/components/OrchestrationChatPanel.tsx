import { useState, useRef, useCallback, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import {
  createSession, updateSessionTitle, setConfigOption, setSessionMode,
  listSkills, listModes, listCommands, listConfigOptions,
  respondPermission, getSession, listMessages,
} from '../api/sessions'
import { setOrchParentSession, getOrchestration } from '../api/orchestration'
import { probeAgentConfigs, listAgentCommands, listAgentModes } from '../api/agents'
import { streamPrompt, isTimeoutError } from '../api/sse'
import { parsePermissionRequest } from '../utils/permission'
import type { Agent, Message, Session, AgentCommand, ConfigOption, SessionMode, AgentSkill, PermissionRequestPayload } from '../types'
import type { ConvState } from './ConvStatusBar'
import type { PanelCtx } from '../modes/types'
import ChatPanel from '../modes/ChatPanel'
import styles from './OrchestrationChatPanel.module.css'

interface Props {
  agents: Agent[]
  workspaceId: number
  cwd: string
  /** 首选 agent 类型；为空则取 agents[0] */
  defaultAgentType?: string
  /** 指定需恢复的编排管理会话 DB 主键（从侧边栏点击编排记录进入时传入）；
   *  缺省时回退到 tasks.json 登记的 parent_session_id。 */
  restoreSessionId?: number
  /** Agent 改动 tasks.json 后触发（通常刷新编排页任务列表） */
  onTaskChanged: () => void
}

// 注入到首条 prompt 前的系统引导，告知 agent 其职责与可用工具。
function buildSystemPrelude(): string {
  return [
    '你是任务编排助手。请根据用户需求管理当前工作区的任务编排。',
    '编排工具由 opennexus-orchestration 这个 MCP 服务器提供（已从 opennexus-subagent 抽离为独立服务器），已自动注入会话，直接调用即可：',
    '- list_orchestration_tasks：列出任务现状（先了解再操作）',
    '- create_orchestration_task：新增任务（title/detail 必填，即发给 agent 的 prompt；自动生成 id 并置 pending；priority 可选 p0/p1/p2，默认 p1）',
    '- update_orchestration_task：改任务字段（task_id 必填 + 要改的字段，含 priority）',
    '- delete_orchestration_task：删除任务（task_id 必填）',
    '- start_orchestration_task：启动任务（task_id 留空=启动全部待执行）',
    '- stop_orchestration_task：停止任务（task_id 留空=停止全部运行中）',
    '- set_orchestration_max_parallel：调整并发上限（1=串行，1~16）',
    '所有工具都需要 workspace_id 参数。',
    `当前工作区 workspace_id：__WORKSPACE_ID__`,
    '若工具列表里看不到上述名称，再改为直接读写当前目录下 tasks.json 并运行校验：bash .agents/skills/orchestration-tasks/scripts/validate-tasks.sh tasks.json。',
    '任务启动后会基于 git worktree 隔离执行；用户可在编排页点击任务查看其会话并继续对话。',
    '完成后用一句话总结改动。',
    '',
  ].join('\n')
}

/**
 * OrchestrationChatPanel：嵌入编排页右栏的 AI 管理对话面板。
 * 用户用自然语言描述需求，Agent 通过编排 MCP 工具（或读写 tasks.json）
 * 增删改任务、启停、调整并发，完成后通过 onTaskChanged 通知编排页刷新任务列表。
 * 单个任务的对话在其独立的会话页（与普通任务页一致）进行，不在本面板内。
 *
 * 直接复用任务页的 ChatPanel（含配置栏/状态条/权限弹窗），构造最小 PanelCtx。
 */
export default function OrchestrationChatPanel({
  agents, workspaceId, cwd, defaultAgentType, restoreSessionId, onTaskChanged,
}: Props) {
  const { t } = useTranslation()

  // 会话与消息
  const [session, setSession] = useState<Session | null>(null)
  const [messages, setMessages] = useState<Message[]>([])
  const [conv, setConv] = useState<ConvState>('idle')
  const [error, setError] = useState('')

  // 配置：无会话时用 probeConfigs；有会话时用 configOptions（会话级模型/模式等）
  const [selectedAgent, setSelectedAgent] = useState(defaultAgentType || agents[0]?.type || '')
  const [selectedModel, setSelectedModel] = useState('')
  const [probeConfigs, setProbeConfigs] = useState<ConfigOption[]>([])
  const [configOptions, setConfigOptions] = useState<ConfigOption[]>([])
  const [currentModeId, setCurrentModeId] = useState('')
  const [probing, setProbing] = useState(false)
  const [commands, setCommands] = useState<AgentCommand[]>([])
  const [modes, setModes] = useState<SessionMode[]>([])
  const [skills, setSkills] = useState<AgentSkill[]>([])

  // 权限
  const [pendingPermission, setPendingPermission] = useState<PermissionRequestPayload | null>(null)
  const [permissionResponding, setPermissionResponding] = useState(false)
  const permissionQueueRef = useRef<PermissionRequestPayload[]>([])

  const abortRef = useRef<AbortController | null>(null)
  const pendingMessagesRef = useRef<Message[]>([])
  const flushRafRef = useRef<number | null>(null)
  const mountedRef = useRef(true)

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
    if (!session || !pendingPermission) return
    setPermissionResponding(true)
    try {
      await respondPermission(session.id, pendingPermission.request_id, optionId)
    } catch { /* ignore */ }
    setPermissionResponding(false)
    const next = permissionQueueRef.current.shift() || null
    setPendingPermission(next)
  }, [session, pendingPermission])

  const handlePermissionCancel = useCallback(() => {
    if (session && pendingPermission) {
      respondPermission(session.id, pendingPermission.request_id, '', true).catch(() => {})
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
        setProbeConfigs(opts)
        const modelOpt = opts.find((o) => o.category === 'model')
        setSelectedModel(modelOpt?.current_value || '')
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
    return () => {
      mountedRef.current = false
      if (flushRafRef.current != null) cancelAnimationFrame(flushRafRef.current)
      abortRef.current?.abort()
    }
  }, [])

  // 恢复已有的编排管理会话：优先用侧边栏点击传入的 restoreSessionId，否则回退到
  // tasks.json 登记的 parent_session_id。使编排对话在重新进入编排页时可见历史记录，
  // 而非每次都新建会话导致旧对话“丢失”（旧对话虽已落库，但此前 UI 从不回读）。
  const restoredKeyRef = useRef<string>('')
  useEffect(() => {
    if (!workspaceId) return
    const key = `${workspaceId}:${restoreSessionId ?? ''}`
    if (restoredKeyRef.current === key) return
    restoredKeyRef.current = key
    let alive = true
    ;(async () => {
      try {
        let targetId = restoreSessionId
        if (!targetId) {
          const def = await getOrchestration(workspaceId)
          targetId = def.data.parent_session_id || undefined
        }
        if (!targetId) return
        const [sResp, mResp] = await Promise.all([getSession(targetId), listMessages(targetId)])
        if (!alive) return
        setSession(sResp.data)
        setMessages(mResp.data.messages || [])
        setSelectedAgent(sResp.data.agent_type)
        // 侧边栏显式指定会话时，重新登记为父会话，使后续任务子会话关联到当前查看的编排对话。
        if (restoreSessionId) setOrchParentSession(workspaceId, restoreSessionId).catch(() => {})
      } catch { /* 会话可能已删除：忽略，保持空会话，允许重新新建 */ }
    })()
    return () => { alive = false }
  }, [workspaceId, restoreSessionId])

  // ===== 发送 =====
  async function handleSend(prompt: string) {
    const text = prompt.trim()
    if (!text || conv !== 'idle' || !selectedAgent) return
    setError('')

    // 首条消息：惰性建会话(source=orchestration)，下发探测配置，注入系统引导
    let activeSession = session
    let sendText = text
    if (!activeSession) {
      setConv('connecting')
      try {
        const resp = await createSession(selectedAgent, workspaceId, selectedModel || undefined, 'orchestration')
        activeSession = resp.data
        setSession(activeSession)
        updateSessionTitle(activeSession.id, t('orchestration.aiTitle')).catch(() => {})
        // 登记为编排管理（父）会话：后续任务执行时创建的会话将关联为其子会话。
        setOrchParentSession(workspaceId, activeSession.id).catch(() => {})
        const extras = probeConfigs.filter((o) => o.type === 'select' && o.category !== 'model' && o.current_value)
        for (const o of extras) {
          try { await setConfigOption(activeSession.id, o.id, o.current_value) } catch { /* ignore */ }
        }
        sendText = buildSystemPrelude().replace('__WORKSPACE_ID__', String(workspaceId)) + text
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
    sessionKind: 'primary',
    sessionId: session?.id,
    session,
    messages,
    convState: conv,
    sending: conv !== 'idle',
    onSend: handleSend,
    onCancel: handleCancel,
    commands,
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
    selectedAgent,
    onSelectAgent: (val: string) => { setSelectedAgent(val) },
    selectedModel,
    probeConfigs,
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
    docContent: '',
    onDocContentChange: () => {},
  }

  return (
    <div className={styles.panel}>
      {error && (
        <div className={styles.errorWrap}>
          <div className={styles.errorBanner}>{error}</div>
        </div>
      )}
      <div className={styles.chatArea}>
        <ChatPanel
          ctx={ctx}
          configBar="coding"
          emptyTitleKey="orchestration.aiManageTitle"
          emptyHintKey="orchestration.aiHint"
          placeholderKey="orchestration.aiPlaceholder"
        />
      </div>
    </div>
  )
}
