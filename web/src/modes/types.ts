import type { ReactNode } from 'react'
import type {
  Session,
  Message,
  AgentCommand,
  ConfigOption,
  ConfigOptionValue,
  SessionMode,
  AgentSkill,
  Execution,
  PermissionRequestPayload,
} from '../types'
import type { ConvState } from '../components/ConvStatusBar'
import type { DocEditMode } from '../components/DocWorkspace'

/**
 * 布局树节点。一个模式的界面 = 一棵 LayoutNode 树。
 * - leaf：渲染单个面板（查 PANELS 注册表）
 * - split：沿 row/col 方向按 flex 比例排列子节点
 * - tabs：标签组，所有子面板保持挂载、用 display 切换可见性（终端 WS 等不中断）。
 *   optional：不默认展示的面板，用户通过标签栏右侧「+」按需打开（可关闭，选择持久化）
 */
export type LayoutNode =
  | { kind: 'leaf'; panel: string; flex?: number }
  | { kind: 'split'; dir: 'row' | 'col'; children: LayoutNode[]; flex?: number }
  | { kind: 'tabs'; panels: string[]; defaultTab?: string; flex?: number; optional?: string[] }

/** 便捷构造器 */
export const leaf = (panel: string, flex = 1): LayoutNode => ({ kind: 'leaf', panel, flex })
export const split = (dir: 'row' | 'col', children: LayoutNode[], flex = 1): LayoutNode => ({
  kind: 'split',
  dir,
  children,
  flex,
})
export const tabs = (panels: string[], flex = 1, defaultTab?: string, optional?: string[]): LayoutNode => ({
  kind: 'tabs',
  panels,
  defaultTab,
  flex,
  optional,
})

/**
 * 面板渲染时拿到的共享上下文。ChatPage 构造、向下传递。
 * 任一面板需要的数据都从 PanelCtx 取，避免各面板各自从 store/hook 拉。
 */
export interface PanelCtx {
  // ===== 会话生命周期（统一主会话） =====
  sessionId: number | undefined
  session: Session | null
  messages: Message[]
  convState: ConvState
  // 断线自动重连倒计时（reconnecting 态展示；null/缺省时回退默认文案）
  reconnect?: { seconds: number; attempt: number } | null
  sending: boolean

  onSend: (prompt: string) => void
  onCancel: () => void

  /** 后端是否有进行中的 prompt 或生效中的 goal。发送队列 flush 前检查：为 true 时挂起队列。 */
  backendBusy?: boolean

  // ===== 对话相关元数据 =====
  commands: AgentCommand[]
  modes: SessionMode[]
  skills: AgentSkill[]
  currentModeId: string
  onSetMode: (modeId: string) => void

  // @task 引用候选任务（任务助手面板传入）：输入框 @ 菜单展示「任务」分类，
  // 选中后插入 @task:<id>(标题) 引用，发送时由调用方直发到该任务会话。
  taskMentions?: { id: string; title: string }[]

  // 编码模式配置（ModelSelector + ContextStats）
  configOptions: ConfigOption[]
  onSetConfigOption: (configId: string, value: string) => void

  // Agent + 模型下拉（新建任务页预探）
  agents: { type: string; display_name: string }[]
  selectedAgent: string
  onSelectAgent: (type: string) => void
  selectedModel: string
  probeConfigs: ConfigOption[]
  onSelectModel: (value: string) => void
  probing: boolean

  // agent+模型 合并下拉（新建任务页）：各 agent 的模型列表、显示过滤正则与组合选择回调。
  // 可选：未提供时 ChatPanel 回退到 onSelectAgent + onSelectModel。
  agentModelsMap?: Record<string, ConfigOptionValue[]>
  agentModelFilters?: string[]
  onSelectAgentModel?: (agentType: string, modelValue: string) => void
  // 有会话时是否仍允许切换 agent（任务助手：管理会话可弃，切 agent 走 onSelectAgentModel 开新会话）。
  // 缺省 false：会话中锁定当前 agent（普通任务会话页行为）。
  agentSwitchable?: boolean

  // 权限
  pendingPermission: PermissionRequestPayload | null
  permissionResponding: boolean
  onPermissionRespond: (optionId: string) => void
  onPermissionCancel: () => void

  // 执行记录（scheduled/classify 会话用）
  executions: Execution[]

  // 恢复检查点后的刷新触发器（变化时让 changes 面板重新拉取）
  restoreRefreshKey?: number

  // 路由/工作区上下文
  workspaceId: number | undefined
  cwd: string
  // 新建任务页：用户选择的自定义工作目录（如已存在的 git worktree），为空则跟随工作区 cwd。
  selectedCwd?: string
  onSelectCwd?: (path: string) => void
  onRestored?: (promptText: string) => void
  // 清理会话上下文成功后回调（重新拉取消息，刷新 token 占用）
  onContextCleared?: () => void

  // ===== 文档预览面板专用 =====
  docTarget: { folderId: string; filePath: string } | null
  docForceMode?: DocEditMode
  onCloseDoc?: () => void
  // 文档预览重新读取磁盘的触发器（AI 直接编辑文件后自增，使预览刷新）
  docReloadKey?: number

  // 输入框受控值（恢复输入场景）
  restoreInput?: string
  onRestoreInputChange?: (v: string) => void

  // 会话来源标记（classify 会话隐藏输入框）
  source?: string

  // ===== 历史消息分页（「加载更多」） =====
  /** 是否还有更早的消息可加载 */
  hasMore?: boolean
  /** 正在加载更早的消息 */
  loadingMore?: boolean
  /** 加载更早消息的回调 */
  onLoadMore?: () => void

  /** Skill 上传成功后的回调（能力面板上传本地 skill 目录后刷新 skills 列表） */
  onSkillsUploaded?: () => void
}

/** 面板注册项 */
export interface PanelDef {
  id: string
  titleKey: string
  icon: ReactNode
  render: (ctx: PanelCtx) => ReactNode
}

/** 配置栏样式（对话列顶部） */
export type ConfigBarKind = 'coding' | 'none'

/** 模式注册项 */
export interface ModeDef {
  id: string
  titleKey: string
  icon: ReactNode
  /** 该模式的面板布局树 */
  layout: LayoutNode
}
