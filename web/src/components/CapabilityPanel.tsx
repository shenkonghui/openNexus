import { useState, useEffect, useCallback, useRef, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ChevronRight, ChevronDown, RefreshCw, Cpu, SlashSquare, Sparkles, Plug, CheckCircle2, XCircle, Upload, Loader2 } from 'lucide-react'
import { getAgentCapabilities } from '../api/agents'
import { getMCPStatus, type MCPServerStatus } from '../api/config'
import { uploadSkillDirectory } from '../api/filesystem'
import type { AgentAcpCapabilities, AgentCommand, AgentSkill } from '../types'
import styles from './CapabilityPanel.module.css'

interface CapabilityPanelProps {
  agentType: string
  commands: AgentCommand[]
  skills: AgentSkill[]
  cwd?: string
  /** Skill 上传成功后的回调（父组件重新拉取 skills 列表） */
  onSkillsUploaded?: () => void
}

/** 可折叠分组：标题 + 数量徽标 + 内容 */
function Section({
  icon,
  title,
  count,
  defaultOpen,
  children,
  actions,
}: {
  icon: ReactNode
  title: string
  count?: number
  defaultOpen?: boolean
  children: ReactNode
  actions?: ReactNode
}) {
  const [open, setOpen] = useState(!!defaultOpen)
  return (
    <div className={styles.section}>
      <div className={styles.sectionHeadRow}>
        <button type="button" className={styles.sectionHead} onClick={() => setOpen((v) => !v)}>
          {open ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          <span className={styles.sectionIcon}>{icon}</span>
          <span className={styles.sectionTitle}>{title}</span>
          {count !== undefined && <span className={styles.countBadge}>{count}</span>}
        </button>
        {actions && <div className={styles.sectionActions}>{actions}</div>}
      </div>
      {open && <div className={styles.sectionBody}>{children}</div>}
    </div>
  )
}

/** 布尔能力标记 */
function CapFlag({ label, on }: { label: string; on: boolean }) {
  return (
    <span className={`${styles.capFlag} ${on ? styles.capFlagOn : styles.capFlagOff}`}>
      {on ? <CheckCircle2 size={11} /> : <XCircle size={11} />}
      {label}
    </span>
  )
}

/**
 * 「能力」面板：展示当前会话 agent 支持的能力全貌——
 * ACP 握手能力、slash 命令（含内置命令与 skill 命令）、技能、MCP server 及其工具。
 */
export default function CapabilityPanel({ agentType, commands, skills, cwd, onSkillsUploaded }: CapabilityPanelProps) {
  const { t } = useTranslation()
  const [caps, setCaps] = useState<AgentAcpCapabilities | null>(null)
  const [mcpServers, setMcpServers] = useState<MCPServerStatus[]>([])
  const [mcpLoading, setMcpLoading] = useState(false)
  const [mcpLoaded, setMcpLoaded] = useState(false)
  const [error, setError] = useState('')

  // Skill 上传状态
  const [skillUploading, setSkillUploading] = useState(false)
  const [skillUploadMsg, setSkillUploadMsg] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const skillInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!agentType) return
    let cancelled = false
    getAgentCapabilities(agentType)
      .then((resp) => { if (!cancelled) setCaps(resp.data) })
      .catch(() => { if (!cancelled) setCaps(null) })
    return () => { cancelled = true }
  }, [agentType])

  // MCP 探测可能较慢（逐个连接 server），挂载后异步加载一次，可手动刷新
  const loadMcp = useCallback(async () => {
    setMcpLoading(true)
    try {
      const resp = await getMCPStatus()
      setMcpServers(resp.data.servers || [])
      setError('')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setMcpLoading(false)
      setMcpLoaded(true)
    }
  }, [])

  useEffect(() => {
    void loadMcp()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const mcpToolCount = mcpServers.reduce((n, s) => n + (s.tools?.length || 0), 0)

  // 处理 skill 目录上传：通过隐藏的 <input webkitdirectory> 选择目录后上传到项目 .agents/skills
  const handleSkillUpload = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = e.target.files
    if (!fileList || fileList.length === 0) return
    if (!cwd) {
      setSkillUploadMsg({ type: 'error', text: t('capPanel.skillUploadNoCwd') })
      e.target.value = ''
      return
    }
    setSkillUploading(true)
    setSkillUploadMsg(null)
    try {
      const files = Array.from(fileList)
      const resp = await uploadSkillDirectory(cwd, files)
      setSkillUploadMsg({
        type: 'success',
        text: t('capPanel.skillUploadOk', { count: resp.data.count, dir: resp.data.target_dir }),
      })
      onSkillsUploaded?.()
    } catch (err) {
      setSkillUploadMsg({
        type: 'error',
        text: err instanceof Error ? err.message : String(err),
      })
    } finally {
      setSkillUploading(false)
      e.target.value = ''
    }
  }, [cwd, onSkillsUploaded, t])

  return (
    <div className={styles.panel}>
      <div className={styles.toolbar}>
        <div className={styles.meta}>
          <Cpu size={13} />
          <span>{agentType || t('capPanel.noAgent')}</span>
          {caps?.agent_version && <span className={styles.version}>v{caps.agent_version}</span>}
        </div>
        <button
          type="button"
          className={styles.iconBtn}
          onClick={() => void loadMcp()}
          disabled={mcpLoading}
          title={t('capPanel.refreshMcp')}
        >
          <RefreshCw size={13} className={mcpLoading ? styles.spin : undefined} />
        </button>
      </div>

      {error && <div className={styles.error}>{error}</div>}

      <div className={styles.body}>
        {/* ACP 能力 */}
        <Section icon={<Cpu size={13} />} title={t('capPanel.acpCaps')} defaultOpen>
          {caps?.available ? (
            <div className={styles.flagRow}>
              <CapFlag label={t('capPanel.capImage')} on={!!caps.prompt_capabilities?.image} />
              <CapFlag label={t('capPanel.capAudio')} on={!!caps.prompt_capabilities?.audio} />
              <CapFlag label={t('capPanel.capEmbeddedContext')} on={!!caps.prompt_capabilities?.embedded_context} />
              <CapFlag label="MCP HTTP" on={!!caps.mcp_capabilities?.http} />
              <CapFlag label="MCP SSE" on={!!caps.mcp_capabilities?.sse} />
            </div>
          ) : (
            <div className={styles.hint}>{t('capPanel.notConnected')}</div>
          )}
        </Section>

        {/* Slash 命令（agent 原生 + 内置 + 配置目录扫描） */}
        <Section icon={<SlashSquare size={13} />} title={t('capPanel.commands')} count={commands.length} defaultOpen>
          {commands.length === 0 ? (
            <div className={styles.hint}>{t('capPanel.emptyCommands')}</div>
          ) : (
            commands.map((c) => (
              <div key={`${c.kind || 'command'}-${c.name}`} className={styles.item}>
                <span className={styles.itemName}>/{c.name}</span>
                {c.scope && <span className={styles.scopeBadge}>{c.scope}</span>}
                {c.description && <span className={styles.itemDesc} title={c.description}>{c.description}</span>}
              </div>
            ))
          )}
        </Section>

        {/* 技能 */}
        <Section
          icon={<Sparkles size={13} />}
          title={t('capPanel.skills')}
          count={skills.length}
          actions={
            <>
              <input
                ref={skillInputRef}
                type="file"
                // @ts-expect-error webkitdirectory 是非标准属性，浏览器支持目录选择
                webkitdirectory=""
                directory=""
                multiple
                style={{ display: 'none' }}
                onChange={(e) => void handleSkillUpload(e)}
              />
              <button
                type="button"
                className={styles.iconBtn}
                onClick={() => skillInputRef.current?.click()}
                disabled={skillUploading || !cwd}
                title={cwd ? t('capPanel.uploadSkill') : t('capPanel.skillUploadNoCwd')}
              >
                {skillUploading ? <Loader2 size={13} className={styles.spin} /> : <Upload size={13} />}
              </button>
            </>
          }
        >
          {skillUploadMsg && (
            <div className={skillUploadMsg.type === 'success' ? styles.uploadOk : styles.uploadErr}>
              {skillUploadMsg.text}
            </div>
          )}
          {skills.length === 0 ? (
            <div className={styles.hint}>{t('capPanel.emptySkills')}</div>
          ) : (
            skills.map((s) => (
              <div key={`${s.scope}-${s.name}`} className={styles.item}>
                <span className={styles.itemName}>{s.name}</span>
                <span className={styles.scopeBadge}>{s.scope}</span>
                {s.description && <span className={styles.itemDesc} title={s.description}>{s.description}</span>}
              </div>
            ))
          )}
        </Section>

        {/* MCP server 及工具 */}
        <Section
          icon={<Plug size={13} />}
          title={t('capPanel.mcp')}
          count={mcpLoaded ? mcpToolCount : undefined}
        >
          {mcpLoading && !mcpLoaded ? (
            <div className={styles.hint}>{t('capPanel.mcpProbing')}</div>
          ) : mcpServers.length === 0 ? (
            <div className={styles.hint}>{t('capPanel.emptyMcp')}</div>
          ) : (
            mcpServers.map((s) => (
              <div key={s.name} className={styles.mcpServer}>
                <div className={styles.mcpServerHead}>
                  <span className={s.connected ? styles.dotOk : styles.dotBad} />
                  <span className={styles.itemName}>{s.name}</span>
                  <span className={styles.scopeBadge}>{s.type}</span>
                  {!s.connected && s.error && (
                    <span className={styles.itemDesc} title={s.error}>{s.error}</span>
                  )}
                </div>
                {(s.tools || []).map((tool) => (
                  <div key={tool.name} className={`${styles.item} ${styles.mcpTool}`}>
                    <span className={styles.itemName}>{tool.name}</span>
                    {tool.description && (
                      <span className={styles.itemDesc} title={tool.description}>{tool.description}</span>
                    )}
                  </div>
                ))}
              </div>
            ))
          )}
        </Section>
      </div>
    </div>
  )
}
