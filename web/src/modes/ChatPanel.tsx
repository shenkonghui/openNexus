import type { ReactNode } from 'react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import MessageList from '../components/MessageList'
import PromptInput from '../components/PromptInput'
import ConvStatusBar from '../components/ConvStatusBar'
import PermissionDialog from '../components/PermissionDialog'
import ModelSelector from '../components/ModelSelector'
import AgentModelSelector from '../components/AgentModelSelector'
import SessionModeSelector from '../components/SessionModeSelector'
import ContextStats from '../components/ContextStats'
import WorktreePicker, { AUTO_WORKTREE } from '../components/WorktreePicker'
import { BookOpenText, Code2, FolderGit2, Sparkles, Zap } from 'lucide-react'
import type { PanelCtx, ConfigBarKind } from './types'
import styles from './ChatPanel.module.css'

interface ChatPanelProps {
  ctx: PanelCtx
  configBar: ConfigBarKind
  emptyTitleKey?: string
  emptyHintKey?: string
  placeholderKey?: string
  selectDocFirstKey?: string
  /** 外部注入的自定义配置栏节点，渲染在 PromptInput 下方（优先于内置 configBar） */
  configBarNode?: ReactNode
}

/**
 * 取路径最后一段作为紧凑显示（如 /a/b/.worktrees/task-1 -> task-1）。
 * 空路径返回空字符串。
 */
function cwdBaseName(path?: string): string {
  if (!path) return ''
  const parts = path.replace(/\/+$/, '').split('/')
  return parts[parts.length - 1] || path
}

/**
 * 通用对话列：configBar + 消息列表 + ConvStatusBar + PromptInput。
 * 编码/文档两种模式共用此组件，差异仅在 configBar 样式与 onSend 处理器（由 ctx 传入）。
 */
export default function ChatPanel({
  ctx,
  configBar,
  emptyTitleKey,
  emptyHintKey,
  placeholderKey,
  selectDocFirstKey,
  configBarNode,
}: ChatPanelProps) {
  const { t } = useTranslation()
  const isEmpty = ctx.messages.length === 0
  const conv = ctx.convState
  const disabled = ctx.sessionKind === 'docs' && !ctx.docTarget
  // 新建任务页的工作目录选择器弹窗开关
  const [showDirPicker, setShowDirPicker] = useState(false)

  const placeholder = disabled && selectDocFirstKey
    ? t(selectDocFirstKey)
    : conv !== 'idle'
      ? t(`session.conv_${conv}`)
      : placeholderKey
        ? t(placeholderKey)
        : t('session.promptPlaceholder')

  // 统一配置栏：Agent·模型 合并下拉 + 模式 + 其余配置，所有模式复用同一套控件。
  // 数据源优先用会话级 configOptions（会话详情页，可切换运行时配置），
  // 回退到 probeConfigs（新建任务页，探测出的配置）。
  const builtInConfigBar = configBar !== 'none' ? (() => {
    // 配置选项：会话级 configOptions（会话详情页）或 probeConfigs（新建任务页，已映射为 configOptions）
    const cfgOpts = ctx.configOptions.length > 0 ? ctx.configOptions : ctx.probeConfigs
    const onApplyCfg = ctx.onSetConfigOption
    // Agent：新建任务页可选（有 agents 列表），会话详情页锁定为当前 agent（仅可切模型）
    const hasAgentSelect = ctx.agents.length > 0 && ctx.session === null
    const modelOpt = cfgOpts.find((o) => o.category === 'model' && o.type === 'select' && o.options.length > 0)
    // 合并下拉数据源：
    // - 新建任务页：全部 agent + 各 agent 探测到的模型（agentModelsMap）；无 map 时退化为当前 agent 的探测模型
    // - 会话详情页：仅当前 agent，模型来自会话级 model config option
    const sessionAgentType = ctx.session?.agent_type || ''
    const comboAgents = hasAgentSelect
      ? ctx.agents
      : sessionAgentType
        ? [{ type: sessionAgentType, display_name: sessionAgentType }]
        : []
    const comboSelectedAgent = hasAgentSelect ? ctx.selectedAgent : sessionAgentType
    const comboSelectedModel = hasAgentSelect ? ctx.selectedModel : (modelOpt?.current_value || '')
    const comboModels = hasAgentSelect
      ? (ctx.agentModelsMap ?? (ctx.selectedAgent && modelOpt ? { [ctx.selectedAgent]: modelOpt.options } : {}))
      : (sessionAgentType && modelOpt ? { [sessionAgentType]: modelOpt.options } : {})
    const handleComboSelect = (agentType: string, modelValue: string) => {
      if (hasAgentSelect) {
        if (ctx.onSelectAgentModel) {
          ctx.onSelectAgentModel(agentType, modelValue)
        } else {
          ctx.onSelectAgent(agentType)
          if (modelValue) ctx.onSelectModel(modelValue)
        }
      } else if (modelOpt && modelValue) {
        // 会话详情页：agent 锁定，切换组合等价于切换模型 config option
        onApplyCfg(modelOpt.id, modelValue)
      }
    }

    return (
      <div className={styles.configBar}>
        <div className={styles.configOptions}>
          {/* Agent·模型 合并下拉（支持 config.yaml 正则过滤组合） */}
          <AgentModelSelector
            agents={comboAgents}
            modelsByAgent={comboModels}
            filters={ctx.agentModelFilters || []}
            selectedAgent={comboSelectedAgent}
            selectedModel={comboSelectedModel}
            onSelect={handleComboSelect}
            disabled={ctx.sending || ctx.probing}
          />

          {/* 模式：统一用 SessionModeSelector */}
          <SessionModeSelector
            modes={ctx.modes}
            currentModeId={ctx.currentModeId}
            onChange={ctx.onSetMode}
            disabled={ctx.sending}
          />

          {/* 其余配置项（模型已合并进上方下拉，此处只剩「更多选项」） */}
          <ModelSelector
            options={cfgOpts}
            onApply={onApplyCfg}
            disabled={ctx.sending || ctx.probing}
            hideModel
          />

          {/* 工作目录：仅新建任务页（无会话）可选，可选择已存在的 worktree/目录或 AI 自动创建作为本次任务 cwd */}
          {ctx.session === null && ctx.onSelectCwd && (
            <button
              type="button"
              className={styles.cwdBtn}
              onClick={() => setShowDirPicker(true)}
              disabled={ctx.sending || ctx.probing}
              title={ctx.selectedCwd === AUTO_WORKTREE ? t('session.worktreeAutoHint') : (ctx.selectedCwd || ctx.cwd || t('session.selectWorktree'))}
            >
              {ctx.selectedCwd === AUTO_WORKTREE ? <Sparkles size={13} /> : <FolderGit2 size={13} />}
              <span className={styles.cwdBtnLabel}>
                {ctx.selectedCwd === AUTO_WORKTREE
                  ? t('session.worktreeAuto')
                  : cwdBaseName(ctx.selectedCwd || ctx.cwd) || t('session.selectWorktree')}
              </span>
            </button>
          )}

          {/* 会话级 YOLO：放在任务配置栏末尾，开启时高亮 */}
          {ctx.onToggleYolo && (
            <button
              type="button"
              className={`${styles.yoloBtn} ${ctx.yoloEnabled ? styles.yoloBtnOn : ''}`}
              onClick={ctx.onToggleYolo}
              disabled={ctx.yoloSaving}
              title={t('session.yoloHint')}
            >
              <Zap size={13} />
              {ctx.yoloEnabled ? t('session.yoloOn') : t('session.yoloOff')}
            </button>
          )}
        </div>
        {ctx.session && (
          <div className={styles.statsArea}>
            <ContextStats messages={ctx.messages} sessionId={ctx.sessionId} onCleared={ctx.onContextCleared} />
          </div>
        )}
      </div>
    )
  })() : null

  return (
    <div className={styles.chat}>
      {showDirPicker && ctx.onSelectCwd && (
        <WorktreePicker
          repoPath={(ctx.selectedCwd !== AUTO_WORKTREE ? ctx.selectedCwd : '') || ctx.cwd || ''}
          selectedPath={ctx.selectedCwd || ctx.cwd || undefined}
          onSelect={(path) => {
            ctx.onSelectCwd?.(path)
            setShowDirPicker(false)
          }}
          onClose={() => setShowDirPicker(false)}
        />
      )}
      {isEmpty && emptyTitleKey ? (
        <div className={styles.empty}>
          {emptyTitleKey.startsWith('codingMode.')
            ? <Code2 size={36} className={styles.emptyIcon} />
            : <BookOpenText size={36} className={styles.emptyIcon} />}
          <h3 className={styles.emptyTitle}>{t(emptyTitleKey)}</h3>
          {emptyHintKey && <p className={styles.emptyHint}>{t(emptyHintKey)}</p>}
        </div>
      ) : (
        <MessageList
          messages={ctx.messages}
          loading={conv === 'streaming' || conv === 'connecting'}
          scheduled={ctx.session?.source === 'scheduled' || ctx.session?.source === 'classify'}
          executions={ctx.executions}
          sessionId={ctx.sessionId}
          cwd={ctx.cwd}
          onRestored={ctx.onRestored}
        />
      )}

      <div className={styles.bottomArea}>
        <ConvStatusBar state={conv}>
          {ctx.pendingPermission && (
            <PermissionDialog
              request={ctx.pendingPermission}
              responding={ctx.permissionResponding}
              onRespond={ctx.onPermissionRespond}
              onCancel={ctx.onPermissionCancel}
            />
          )}
        </ConvStatusBar>
        {ctx.source === 'classify' ? (
          <p className={styles.classifyHint}>{t('notes.classifyTaskHint')}</p>
        ) : (
          // 统一 composer：输入框 + 配置栏合并为一个圆角卡片（参考 Cursor 输入区）
          <div className={styles.composer}>
            <PromptInput
              onSend={ctx.onSend}
              onCancel={ctx.onCancel}
              sending={conv !== 'idle'}
              disabled={disabled}
              value={ctx.restoreInput}
              onValueChange={ctx.onRestoreInputChange}
              commands={ctx.commands}
              modes={ctx.modes}
              skills={ctx.skills}
              cwd={ctx.cwd}
              workspaceId={ctx.workspaceId}
              placeholder={placeholder}
            />
            {configBarNode ?? builtInConfigBar}
          </div>
        )}
      </div>
    </div>
  )
}
