import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Bot, ChevronDown, ChevronRight, PanelRight } from 'lucide-react'
import AgentTerminalInstance, { type AgentTerminalHandle } from './AgentTerminalInstance'
import { useAgentTerminals } from '../context/AgentTerminalsContext'
import styles from './InlineAgentTerminal.module.css'

interface InlineAgentTerminalProps {
  terminalId: string
  /** 所在 tool_call 气泡是否展开：终端运行中始终显示，退出后跟随气泡展开状态 */
  bubbleOpen: boolean
}

/**
 * 内嵌在 tool_call 气泡输出位置的单终端窗口：实时渲染该 terminalId 的输出，
 * 右上角按钮可把「这一个」终端移动到右侧终端面板（其余终端不受影响）。
 * 输出缓冲保存在 AgentTerminalsProvider，虚拟列表卸载重挂时 attach 全量回放。
 */
export default function InlineAgentTerminal({ terminalId, bubbleOpen }: InlineAgentTerminalProps) {
  const { t } = useTranslation()
  const ctx = useAgentTerminals()
  const [collapsed, setCollapsed] = useState(false)
  const meta = ctx?.entries.get(terminalId)
  const attach = ctx?.attach
  const detach = ctx?.detach
  // 纯展示模式（多任务实时窗口）：不提供收起/移动操作
  const plain = !!ctx?.plain

  // 运行中始终显示；退出后跟随气泡展开状态（折叠气泡时隐藏，展开可回看）
  const visible = !!meta && !meta.moved && (bubbleOpen || !meta.exited)
  const showBody = visible && (plain || !collapsed)

  // 终端体卸载（折叠/隐藏/组件卸载）时解除写入句柄，缓冲保留供重挂回放
  useEffect(() => {
    if (!showBody || !detach) return
    return () => detach(terminalId)
  }, [showBody, terminalId, detach])

  // 无 provider（如编排实时窗口复用 MessageList）或终端未知时不渲染
  if (!ctx || !meta) return null

  // 已移到终端面板：留一行提示，避免用户找不到输出去向
  if (meta.moved) {
    return bubbleOpen ? <div className={styles.movedHint}>{t('agentTerminal.movedHint')}</div> : null
  }
  if (!visible) return null

  return (
    <div className={styles.window}>
      <div className={styles.header}>
        <Bot size={13} className={styles.icon} />
        <span className={styles.command} title={meta.command}>{meta.command}</span>
        {!plain && (
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.actionBtn}
            title={collapsed ? t('agentTerminal.expand') : t('agentTerminal.collapse')}
            onClick={() => setCollapsed((v) => !v)}
          >
            {collapsed ? <ChevronRight size={13} /> : <ChevronDown size={13} />}
          </button>
          <button
            type="button"
            className={styles.actionBtn}
            title={t('agentTerminal.moveToPanel')}
            onClick={() => ctx.moveToPanel(terminalId)}
          >
            <PanelRight size={13} />
          </button>
        </div>
        )}
      </div>
      {showBody && (
        <div className={styles.body}>
          <AgentTerminalInstance
            active
            onReady={(handle: AgentTerminalHandle) => attach?.(terminalId, handle)}
          />
        </div>
      )}
    </div>
  )
}
