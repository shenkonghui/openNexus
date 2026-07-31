import { useState, useEffect, useMemo, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { X, SlidersHorizontal, Bot, Wrench, StickyNote, ListTodo, Shield, Monitor, Target } from 'lucide-react'
import { listAgentConfigs, updateAgentConfig, deleteAgentConfig, refreshRegistry, getRegistryDefault, updateAgentFromRegistry } from '../api/agentConfigs'
import type { RegistryRefreshResult } from '../api/agentConfigs'
import { listAgents, getAgentModels, probeAgentConfigs, clearAgentProbeCache } from '../api/agents'
import { getNoteSettings, updateNoteSettings, generateNoteMCPToken } from '../api/notes'
import { getTaskSettings, updateTaskSettings } from '../api/tasks'
import { getGoalSettings, updateGoalSettings, listGoalRoles, type GoalRole } from '../api/goal'
import { readWorkspaceFile, writeWorkspaceFile, deleteWorkspaceEntry } from '../api/filesystem'
import { getPermissionSettings, updatePermissionSettings } from '../api/permissions'
import { reloadProgram, updateSelectorFilters } from '../api/config'
import { getAgentPrefs, patchAgentPrefs } from '../api/agentPrefs'
import type { AgentConfig, Agent, ModelOption, ConfigOption, ConfigOptionValue, TaskSettings, PermissionSettings } from '../types'
import { translateTag } from '../utils/tag'
import { translatePrompt } from '../utils/defaultPrompts'
import EditAgentDialog, { type AgentFormPayload } from './EditAgentDialog'
import AgentAcpCapsPanel from './AgentAcpCapsPanel'
import AgentModelSelector from './AgentModelSelector'
import AgentModelMultiSelect from './AgentModelMultiSelect'
import ConfigEditor from './ConfigEditor'
import RawConfigCard from './RawConfigCard'
import ErrorBanner from './ErrorBanner'
import LoadingSpinner from './LoadingSpinner'
import i18n from '../i18n'
import styles from './SettingsDialog.module.css'

export type SettingsTab = 'language' | 'agent' | 'classify' | 'config' | 'task' | 'goal' | 'permission' | 'system'

export function parseSettingsTab(raw: string | null): SettingsTab {
  if (raw === 'agent' || raw === 'classify' || raw === 'config' || raw === 'task' || raw === 'goal' || raw === 'permission' || raw === 'system') return raw
  return 'language'
}

function modelOptFromConfig(modelOpt: ConfigOption): ModelOption {
  return {
    id: modelOpt.id,
    name: modelOpt.name,
    current_value: modelOpt.current_value,
    options: modelOpt.options,
  }
}

function findModelConfigOption(opts: ConfigOption[]) {
  return opts.find((o) => o.category === 'model' && o.type === 'select')
    || opts.find((o) => o.category === 'model')
}

function buildNoteMcpConfig(endpoint: string, token: string): string {
  return JSON.stringify({
    mcpServers: {
      'opennexus-notes': {
        type: 'http',
        url: endpoint,
        headers: { Authorization: `Bearer ${token}` },
      },
    },
  }, null, 2)
}

interface Props {
  initialTab?: SettingsTab
  onClose: () => void
}

/**
 * 全局设置弹窗：左侧分组图标导航 + 右侧内容区（参照桌面端设置弹窗风格）。
 * 由 AppLayout 根据 URL 参数挂载，任何带侧边栏的页面都可打开。
 */
export default function SettingsDialog({ initialTab = 'language', onClose }: Props) {
  const { t } = useTranslation()
  const [tab, setTab] = useState<SettingsTab>(initialTab)

  const [configs, setConfigs] = useState<AgentConfig[]>([])
  const [configSearch, setConfigSearch] = useState('')
  const [registryRefreshing, setRegistryRefreshing] = useState(false)
  const [registryResult, setRegistryResult] = useState<RegistryRefreshResult | null>(null)
  const [updatingAgentId, setUpdatingAgentId] = useState<number | null>(null)
  const [agentUpdateMsg, setAgentUpdateMsg] = useState('')
  const [agents, setAgents] = useState<Agent[]>([])
  const [defaultAgent, setDefaultAgent] = useState('')
  // 默认 agent 的默认模型（存入 agent-prefs 的 prefs[agent].model，新建任务时自动应用）
  const [defaultModel, setDefaultModel] = useState('')
  // 各 agent 的可用模型列表（供默认 Agent·模型 合并下拉，同新建任务页）
  const [defaultModelsMap, setDefaultModelsMap] = useState<Record<string, ConfigOptionValue[]>>({})
  const [defaultModelsProbing, setDefaultModelsProbing] = useState(false)
  const [noteAgent, setNoteAgent] = useState('')
  const [noteModel, setNoteModel] = useState('')
  const [noteInterval, setNoteInterval] = useState(5)
  const [notePrompt, setNotePrompt] = useState('')
  const [noteClassifySessionId, setNoteClassifySessionId] = useState(0)
  const [noteMcpToken, setNoteMcpToken] = useState('')
  const [noteMcpGenerating, setNoteMcpGenerating] = useState(false)
  const [noteModelOptions, setNoteModelOptions] = useState<ModelOption[]>([])
  const [noteModelProbing, setNoteModelProbing] = useState(false)
  const [noteSettingsSaving, setNoteSettingsSaving] = useState(false)
  // 任务设置状态
  const [taskAutoTag, setTaskAutoTag] = useState(true)
  const [taskAutoTitle, setTaskAutoTitle] = useState(true)
  const [taskAgent, setTaskAgent] = useState('')
  const [taskTags, setTaskTags] = useState<string[]>([])
  const [taskTagInput, setTaskTagInput] = useState('')
  const [taskTagPrompt, setTaskTagPrompt] = useState('')
  const [taskTitlePrompt, setTaskTitlePrompt] = useState('')
  const [taskArchiveDays, setTaskArchiveDays] = useState(3)
  const [taskSettingsSaving, setTaskSettingsSaving] = useState(false)
  const [taskSettingsSaved, setTaskSettingsSaved] = useState(false)
  // goal 设置状态（通用 /goal 循环：评估 agent/模型 + 限制条件）
  const [goalAgent, setGoalAgent] = useState('')
  const [goalModel, setGoalModel] = useState('')
  const [goalMaxTurns, setGoalMaxTurns] = useState(0)
  const [goalMaxDuration, setGoalMaxDuration] = useState(0)
  const [goalSettingsSaving, setGoalSettingsSaving] = useState(false)
  const [goalSettingsSaved, setGoalSettingsSaved] = useState(false)
  // goal 评估角色管理（文件式定义：列表来自 /goal/roles，文件增删改走 /filesystem 通用接口）
  const [goalRoles, setGoalRoles] = useState<GoalRole[]>([])
  const [goalRolesDir, setGoalRolesDir] = useState('')
  const [roleNewName, setRoleNewName] = useState('')
  const [roleEditPath, setRoleEditPath] = useState('')
  const [roleEditContent, setRoleEditContent] = useState('')
  const [roleEditSaving, setRoleEditSaving] = useState(false)
  // 权限规则设置（白名单 / 黑名单 / 询问名单；mode 由侧栏全局 YOLO 开关控制，保存时保留）
  const [permMode, setPermMode] = useState<'normal' | 'yolo'>('normal')
  const [permAllow, setPermAllow] = useState('')
  const [permAsk, setPermAsk] = useState('')
  const [permDeny, setPermDeny] = useState('')
  // 全局沙箱开关（config.yaml sandbox 段，随权限规则一起保存）
  const [sandboxEnabled, setSandboxEnabled] = useState(false)
  const [sandboxMode, setSandboxMode] = useState<'auto' | 'enforce'>('auto')
  const [permSaving, setPermSaving] = useState(false)
  const [permSaved, setPermSaved] = useState(false)
  // agent+模型 合并下拉的显示过滤正则（config.yaml agents.selector.filters，每行一条）
  const [selectorFiltersText, setSelectorFiltersText] = useState('')
  const [selectorSaving, setSelectorSaving] = useState(false)
  const [selectorSaved, setSelectorSaved] = useState(false)
  const [reloadStatus, setReloadStatus] = useState<'idle' | 'reloading' | 'success' | 'failed'>('idle')
  const [reloadError, setReloadError] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [editingConfig, setEditingConfig] = useState<AgentConfig | null>(null)
  // 展开 ACP 能力面板的 agent 类型（同时只展开一个，再次点击收起）
  const [capsAgentType, setCapsAgentType] = useState('')
  const [saving, setSaving] = useState(false)
  const noteMcpEndpoint = `${window.location.origin}/mcp/notes`
  const noteMcpConfig = noteMcpToken ? buildNoteMcpConfig(noteMcpEndpoint, noteMcpToken) : ''

  useEffect(() => { loadData() }, [])

  // Esc 关闭弹窗（编辑子弹窗打开时优先让子弹窗处理）
  useEffect(() => {
    function handleKey(e: KeyboardEvent) {
      if (e.key === 'Escape' && !editingConfig) onClose()
    }
    document.addEventListener('keydown', handleKey)
    return () => document.removeEventListener('keydown', handleKey)
  }, [editingConfig, onClose])

  useEffect(() => {
    if (tab !== 'classify' || !noteAgent) {
      return
    }
    let alive = true
    setNoteModelProbing(true)

    async function loadNoteModels() {
      try {
        const cached = await getAgentModels(noteAgent)
        if (!alive) return
        const fromSession = cached.data.model_options || []
        if (fromSession.length > 0 && fromSession[0].options.length > 0) {
          setNoteModelOptions(fromSession)
          return
        }

        const probed = await probeAgentConfigs(noteAgent)
        if (!alive) return
        const modelOpt = findModelConfigOption(probed.data.config_options || [])
        if (modelOpt && modelOpt.options.length > 0) {
          setNoteModelOptions([modelOptFromConfig(modelOpt)])
        } else {
          setNoteModelOptions([])
        }
      } catch (err) {
        if (!alive) return
        setNoteModelOptions([])
        setError(err instanceof Error ? err.message : t('common.failed'))
      } finally {
        if (alive) setNoteModelProbing(false)
      }
    }

    loadNoteModels()
    return () => { alive = false }
  }, [tab, noteAgent, t])

  // 进入 agent / goal 页时加载各 agent 的可用模型列表（优先会话缓存，回退探测），
  // 供「默认 Agent·模型」与「goal 评估 Agent·模型」合并下拉共用。
  useEffect(() => {
    if ((tab !== 'agent' && tab !== 'goal') || agents.length === 0) return
    let alive = true

    async function loadAgentModels(agentType: string) {
      try {
        const cached = await getAgentModels(agentType)
        if (!alive) return
        const fromSession = cached.data.model_options || []
        if (fromSession.length > 0 && fromSession[0].options.length > 0) {
          setDefaultModelsMap((prev) => ({ ...prev, [agentType]: fromSession[0].options }))
          return
        }
        const probed = await probeAgentConfigs(agentType)
        if (!alive) return
        const modelOpt = findModelConfigOption(probed.data.config_options || [])
        setDefaultModelsMap((prev) => ({ ...prev, [agentType]: modelOpt?.options || [] }))
      } catch {
        // 探测失败：记为空列表，下拉退化为 agent 级单项（使用默认模型）
        if (alive) setDefaultModelsMap((prev) => (agentType in prev ? prev : { ...prev, [agentType]: [] }))
      }
    }

    agents.forEach((a) => { loadAgentModels(a.type) })
    return () => { alive = false }
  }, [tab, agents])

  // 进入 goal 页时加载评估角色列表（用户目录作用域，project 角色随会话 cwd 生效不在此管理）。
  useEffect(() => {
    if (tab !== 'goal') return
    let alive = true
    listGoalRoles().then((resp) => {
      if (!alive) return
      setGoalRoles(resp.data.roles || [])
      setGoalRolesDir(resp.data.user_dir || '')
    }).catch(() => { /* 接口不可用时隐藏角色区的列表内容即可 */ })
    return () => { alive = false }
  }, [tab])

  async function loadData() {
    setLoading(true); setError('')
    try {
      const [cfgResp, agentsResp, noteSettingsResp, taskSettingsResp, permResp, goalSettingsResp] = await Promise.all([
        listAgentConfigs(), listAgents(), getNoteSettings(), getTaskSettings(), getPermissionSettings(),
        getGoalSettings().catch(() => ({ data: { agent_type: '', model_value: '', max_turns: 0, max_duration_minutes: 0 } })),
      ])
      setConfigs(cfgResp.data.agent_configs || [])
      setAgents(agentsResp.data.agents || [])
      setSelectorFiltersText((agentsResp.data.selector_filters || []).join('\n'))
      const prefsResp = await getAgentPrefs().catch(() => ({ data: { last_agent_type: '', prefs: {} } }))
      const prefsMap: Record<string, Record<string, string>> = prefsResp.data.prefs || {}
      const lastAgent = prefsResp.data.last_agent_type || ''
      setDefaultAgent(lastAgent)
      setDefaultModel(prefsMap[lastAgent]?.model || '')
      setNoteAgent(noteSettingsResp.data.agent_type || '')
      setNoteModel(noteSettingsResp.data.model_value || '')
      setNoteInterval(noteSettingsResp.data.classify_interval_minutes || 5)
      setNotePrompt(noteSettingsResp.data.classify_prompt || '')
      setNoteClassifySessionId(noteSettingsResp.data.classify_db_session_id || 0)
      setNoteMcpToken(noteSettingsResp.data.mcp_token || '')
      // 任务设置
      const ts: TaskSettings = taskSettingsResp.data
      setTaskAutoTag(ts.auto_tag_enabled)
      setTaskAutoTitle(ts.auto_title_enabled)
      setTaskAgent(ts.agent_type || (agentsResp.data.agents || [])[0]?.type || '')
      setTaskTags(ts.tags || [])
      setTaskTagPrompt(ts.tag_prompt || '')
      setTaskTitlePrompt(ts.title_prompt || '')
      setTaskArchiveDays(ts.archive_retention_days || 3)
      // goal 设置
      setGoalAgent(goalSettingsResp.data.agent_type || '')
      setGoalModel(goalSettingsResp.data.model_value || '')
      setGoalMaxTurns(goalSettingsResp.data.max_turns || 0)
      setGoalMaxDuration(goalSettingsResp.data.max_duration_minutes || 0)
      // 权限规则设置（保留全局 YOLO mode，避免保存名单时误关）
      const ps = permResp.data
      setPermMode(ps.mode === 'yolo' ? 'yolo' : 'normal')
      setPermAllow((ps.allow || []).join('\n'))
      setPermAsk((ps.ask || []).join('\n'))
      setPermDeny((ps.deny || []).join('\n'))
      setSandboxEnabled(!!ps.sandbox?.enabled)
      setSandboxMode(ps.sandbox?.mode === 'enforce' ? 'enforce' : 'auto')
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally { setLoading(false) }
  }

  // 合并下拉选择默认 Agent·模型：一次 PATCH 同时写 last_agent_type 与 prefs[agent].model；
  // agentType 为空表示清除默认 agent，modelValue 为空表示使用 agent 自身默认模型。
  async function handleSetDefaultCombo(agentType: string, modelValue: string) {
    setDefaultAgent(agentType)
    setDefaultModel(modelValue)
    try {
      if (agentType) {
        await patchAgentPrefs({ last_agent_type: agentType, agent_type: agentType, configs: { model: modelValue } })
      } else {
        await patchAgentPrefs({ last_agent_type: '' })
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    }
  }

  // 手动强制重新探测所有 agent 的可用模型列表。
  async function handleProbeDefaultModels() {
    if (agents.length === 0) return
    setDefaultModelsProbing(true); setError('')
    try {
      await Promise.all(agents.map(async (a) => {
        clearAgentProbeCache(a.type)
        try {
          const r = await probeAgentConfigs(a.type, { force: true })
          const modelOpt = findModelConfigOption(r.data.config_options || [])
          setDefaultModelsMap((prev) => ({ ...prev, [a.type]: modelOpt?.options || [] }))
        } catch {
          // 单个 agent 探测失败不阻断其余，保留旧列表
        }
      }))
    } finally {
      setDefaultModelsProbing(false)
    }
  }

  async function handleProbeNoteModel() {
    if (!noteAgent) return
    setNoteModelProbing(true); setError('')
    try {
      clearAgentProbeCache(noteAgent)
      const r = await probeAgentConfigs(noteAgent, { force: true })
      const modelOpt = findModelConfigOption(r.data.config_options || [])
      if (modelOpt && modelOpt.options.length > 0) {
        setNoteModelOptions([modelOptFromConfig(modelOpt)])
      } else {
        setNoteModelOptions([])
        setError(t('scheduledTask.probeHint'))
      }
    } catch (err) {
      setNoteModelOptions([])
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setNoteModelProbing(false)
    }
  }

  async function handleSaveNoteSettings() {
    setNoteSettingsSaving(true); setError('')
    try {
      const resp = await updateNoteSettings({
        agent_type: noteAgent,
        model_value: noteModel,
        classify_prompt: notePrompt,
        classify_interval_minutes: noteInterval,
      })
      setNoteAgent(resp.data.agent_type || '')
      setNoteModel(resp.data.model_value || '')
      setNoteInterval(resp.data.classify_interval_minutes || 5)
      setNotePrompt(resp.data.classify_prompt || '')
      setNoteClassifySessionId(resp.data.classify_db_session_id || 0)
      setNoteMcpToken(resp.data.mcp_token || '')
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setNoteSettingsSaving(false)
    }
  }

  async function handleGenerateNoteMcpToken() {
    setNoteMcpGenerating(true); setError('')
    try {
      const resp = await generateNoteMCPToken()
      setNoteMcpToken(resp.data.mcp_token || '')
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setNoteMcpGenerating(false)
    }
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text)
    } catch {
      setError(t('settings.noteMcpCopyFailed'))
    }
  }

  function handleAddTaskTag() {
    const v = taskTagInput.trim()
    if (!v) return
    if (!taskTags.includes(v)) {
      setTaskTags([...taskTags, v])
    }
    setTaskTagInput('')
  }

  function handleRemoveTaskTag(tag: string) {
    setTaskTags(taskTags.filter((t2) => t2 !== tag))
  }

  async function handleSaveTaskSettings() {
    setTaskSettingsSaving(true); setError(''); setTaskSettingsSaved(false)
    try {
      const resp = await updateTaskSettings({
        auto_tag_enabled: taskAutoTag,
        auto_title_enabled: taskAutoTitle,
        agent_type: taskAgent,
        model_value: '',
        tags: taskTags,
        tag_prompt: taskTagPrompt,
        title_prompt: taskTitlePrompt,
        archive_retention_days: taskArchiveDays,
      })
      setTaskTags(resp.data.tags || [])
      setTaskTagPrompt(resp.data.tag_prompt || '')
      setTaskTitlePrompt(resp.data.title_prompt || '')
      setTaskArchiveDays(resp.data.archive_retention_days || 3)
      setTaskSettingsSaved(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setTaskSettingsSaving(false)
    }
  }

  async function handleSaveGoalSettings() {
    setGoalSettingsSaving(true); setError(''); setGoalSettingsSaved(false)
    try {
      const resp = await updateGoalSettings({
        agent_type: goalAgent,
        model_value: goalModel,
        max_turns: goalMaxTurns,
        max_duration_minutes: goalMaxDuration,
      })
      setGoalAgent(resp.data.agent_type || '')
      setGoalModel(resp.data.model_value || '')
      setGoalMaxTurns(resp.data.max_turns || 0)
      setGoalMaxDuration(resp.data.max_duration_minutes || 0)
      setGoalSettingsSaved(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setGoalSettingsSaving(false)
    }
  }

  async function refreshGoalRoles() {
    try {
      const resp = await listGoalRoles()
      setGoalRoles(resp.data.roles || [])
      setGoalRolesDir(resp.data.user_dir || '')
    } catch { /* 列表刷新失败不阻断操作结果 */ }
  }

  // 编辑已有角色：读取 .md 原文到内联编辑器
  async function handleEditGoalRole(role: GoalRole) {
    setError('')
    try {
      const resp = await readWorkspaceFile(role.location)
      setRoleEditPath(role.location)
      setRoleEditContent(resp.data.content)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    }
  }

  // 新建角色：生成 frontmatter 模板并打开编辑器，保存时才落盘到用户目录
  function handleNewGoalRole() {
    const name = roleNewName.trim().replace(/\s+/g, '-')
    if (!name || !goalRolesDir) return
    setRoleEditPath(`${goalRolesDir}/${name}.md`)
    setRoleEditContent(`---\nname: ${name}\ndescription: ${t('settings.goalRoleTemplateDesc')}\n# agent: claude-code\n# model: haiku\n# skills: skill-a, skill-b\n---\n\n${t('settings.goalRoleTemplateBody', { condition: '{{condition}}', transcript: '{{transcript}}' })}\n`)
    setRoleNewName('')
  }

  async function handleSaveGoalRole() {
    setRoleEditSaving(true); setError('')
    try {
      await writeWorkspaceFile(roleEditPath, roleEditContent)
      setRoleEditPath(''); setRoleEditContent('')
      await refreshGoalRoles()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setRoleEditSaving(false)
    }
  }

  async function handleDeleteGoalRole(role: GoalRole) {
    if (!window.confirm(t('settings.goalRoleDeleteConfirm', { name: role.name }))) return
    setError('')
    try {
      await deleteWorkspaceEntry(role.location)
      if (roleEditPath === role.location) { setRoleEditPath(''); setRoleEditContent('') }
      await refreshGoalRoles()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    }
  }

  // 把多行文本拆成规则数组（去空白、去空行、去重）
  function linesToList(text: string): string[] {
    const seen = new Set<string>()
    const out: string[] = []
    for (const line of text.split('\n')) {
      const s = line.trim()
      if (!s || seen.has(s)) continue
      seen.add(s)
      out.push(s)
    }
    return out
  }

  // 转义正则元字符：勾选组合时生成精确匹配规则用
  function escapeRegex(s: string): string {
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  }

  // Agent/模型显示过滤的勾选列表：按 agent 分组列出全部已知组合（与 AgentModelSelector 的展示项一致），
  // 模型未探测到的 agent 退化为 agent 级单项。
  const selectorGroups = useMemo(() => agents.map((a) => ({
    agent: a,
    models: defaultModelsMap[a.type] || [],
  })), [agents, defaultModelsMap])

  const selectorComboKeys = useMemo(() => selectorGroups.flatMap((g) =>
    g.models.length > 0
      ? g.models.map((m) => `${g.agent.type}\u0000${m.value}`)
      : [`${g.agent.type}\u0000`],
  ), [selectorGroups])

  // 由当前过滤规则推导各组合的勾选状态（匹配逻辑与下拉框一致：空规则=全部显示，任一正则命中即显示）
  const selectorChecked = useMemo(() => {
    const regexes: RegExp[] = []
    for (const f of linesToList(selectorFiltersText)) {
      try { regexes.push(new RegExp(f, 'i')) } catch { /* 非法正则忽略，保存时后端会校验 */ }
    }
    const matches = (...cands: string[]) =>
      regexes.length === 0 || regexes.some((re) => cands.some((c) => re.test(c)))
    const map: Record<string, boolean> = {}
    for (const g of selectorGroups) {
      if (g.models.length > 0) {
        for (const m of g.models) {
          map[`${g.agent.type}\u0000${m.value}`] = matches(`${g.agent.type}/${m.value}`, `${g.agent.type}/${m.name}`)
        }
      } else {
        map[`${g.agent.type}\u0000`] = matches(g.agent.type)
      }
    }
    return map
  }, [selectorGroups, selectorFiltersText])

  // 勾选变化后把规则重写为精确匹配正则：全选=清空规则（全部显示，含后续新模型），全不选=^$（全部隐藏）
  function applySelectorChecked(next: Record<string, boolean>) {
    const picked = selectorComboKeys.filter((k) => next[k])
    let lines: string[]
    if (picked.length === selectorComboKeys.length) {
      lines = []
    } else if (picked.length === 0) {
      lines = ['^$']
    } else {
      lines = picked.map((k) => {
        const [agentType, modelValue] = k.split('\u0000')
        // agent 级条目允许后续探测到的模型也显示（匹配 "type" 与 "type/xxx"）
        return modelValue ? `^${escapeRegex(`${agentType}/${modelValue}`)}$` : `^${escapeRegex(agentType)}(/|$)`
      })
    }
    setSelectorFiltersText(lines.join('\n'))
    setSelectorSaved(false)
  }

  function toggleSelectorCombo(key: string) {
    applySelectorChecked({ ...selectorChecked, [key]: !selectorChecked[key] })
  }

  // 整组勾选/取消（agent 分组头部的复选框）
  function toggleSelectorGroup(keys: string[]) {
    const allOn = keys.every((k) => selectorChecked[k])
    const next = { ...selectorChecked }
    for (const k of keys) next[k] = !allOn
    applySelectorChecked(next)
  }

  function setAllSelectorCombos(on: boolean) {
    const next: Record<string, boolean> = {}
    for (const k of selectorComboKeys) next[k] = on
    applySelectorChecked(next)
  }

  // 保存 agent+模型 显示过滤：后端校验正则合法性后写回 config.yaml，保存即生效
  async function handleSaveSelectorFilters() {
    setSelectorSaving(true); setError(''); setSelectorSaved(false)
    try {
      const resp = await updateSelectorFilters(linesToList(selectorFiltersText))
      setSelectorFiltersText((resp.data.filters || []).join('\n'))
      setSelectorSaved(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally { setSelectorSaving(false) }
  }

  async function handleSavePermissionSettings() {
    setPermSaving(true); setError(''); setPermSaved(false)
    try {
      const payload: PermissionSettings = {
        mode: permMode,
        allow: linesToList(permAllow),
        ask: linesToList(permAsk),
        deny: linesToList(permDeny),
        sandbox: { enabled: sandboxEnabled, mode: sandboxMode },
      }
      const resp = await updatePermissionSettings(payload)
      setPermMode(resp.data.mode === 'yolo' ? 'yolo' : 'normal')
      setPermAllow((resp.data.allow || []).join('\n'))
      setPermAsk((resp.data.ask || []).join('\n'))
      setPermDeny((resp.data.deny || []).join('\n'))
      setSandboxEnabled(!!resp.data.sandbox?.enabled)
      setSandboxMode(resp.data.sandbox?.mode === 'enforce' ? 'enforce' : 'auto')
      setPermSaved(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setPermSaving(false)
    }
  }

  // 重载程序:桌面版走 IPC 硬重载(主进程 kill+spawn 后端 + 刷新页面);
  // 浏览器访问远程后端走软重载 API(热刷新扫描目录,不杀进程)+ 刷新前端。
  async function handleReloadProgram() {
    if (reloadStatus === 'reloading') return
    setReloadStatus('reloading'); setReloadError('')
    try {
      if (window.opennexus?.isElectron) {
        // 桌面版:IPC 成功后主进程会 webContents.reload(),本页随后整页刷新,无需 setState
        const result = await window.opennexus.reloadBackend!()
        if (!result?.ok) {
          setReloadStatus('failed')
          setReloadError(result?.error || t('system.reloadFailed', { error: '' }).split(':')[0])
        }
        // 成功时页面将被刷新,setState 无意义故不设
      } else {
        // 浏览器:软重载后整页刷新,拉取最新配置
        await reloadProgram()
        setReloadStatus('success')
        // 短暂展示成功后刷新页面,确保所有前端缓存清空
        setTimeout(() => window.location.reload(), 600)
      }
    } catch (err) {
      setReloadStatus('failed')
      const msg = err instanceof Error ? err.message : t('common.failed')
      setReloadError(t('system.reloadFailed', { error: msg }))
    }
  }

  function closeEditDialog() {
    if (saving) return
    setEditingConfig(null)
  }

  async function handleSaveEdit(payload: AgentFormPayload) {
    if (!editingConfig) return
    if (!payload.display_name || !payload.command) {
      setError(t('settings.validationError'))
      return
    }
    setSaving(true); setError('')
    try {
      await updateAgentConfig(editingConfig.id, payload)
      setEditingConfig(null)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSaving(false)
    }
  }

  async function handleDeleteEditing() {
    if (!editingConfig || !window.confirm(t('settings.deleteConfirm'))) return
    setSaving(true); setError('')
    try {
      await deleteAgentConfig(editingConfig.id)
      setEditingConfig(null)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSaving(false)
    }
  }

  // 在线拉取最新 ACP registry 并合并到本地存储。
  // 新 agent 以禁用状态入库（需手动启用），已有 agent 仅刷新名称/描述，对运行中后端零影响。
  async function handleRefreshRegistry() {
    if (registryRefreshing) return
    setRegistryRefreshing(true); setError(''); setRegistryResult(null)
    try {
      const { data } = await refreshRegistry()
      setRegistryResult(data)
      await loadData()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.registryFetchFailed'))
    } finally {
      setRegistryRefreshing(false)
    }
  }

  // 单个 agent 从 CDN 最新 registry 同步：后端原子完成"拉取→更新配置→(binary 类)清缓存触发重下"。
  // env/enabled 保留（env 常含代理/密钥；enabled 是用户意愿）。保存即重新注册 backend 生效。
  async function handleUpdateAgent(cfg: AgentConfig) {
    if (updatingAgentId !== null) return
    setUpdatingAgentId(cfg.id); setError(''); setAgentUpdateMsg('')
    try {
      const { data } = await updateAgentFromRegistry(cfg.id)
      await loadData()
      setAgentUpdateMsg(
        data.redownloaded
          ? t('settings.agentRedownloaded', { version: data.version })
          : t('settings.agentUpdated', { version: data.version })
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.agentUpdateNotFound'))
    } finally {
      setUpdatingAgentId(null)
    }
  }

  function switchLang(lang: string) {
    i18n.changeLanguage(lang)
    localStorage.setItem('opennexus-lang', lang)
  }

  // 左侧导航：分组 + 图标，视觉参照桌面端设置弹窗
  const navGroups: { label: string; items: { key: SettingsTab; icon: ReactNode; label: string }[] }[] = [
    {
      label: t('settings.groupGeneral'),
      items: [
        { key: 'language', icon: <SlidersHorizontal size={15} />, label: t('settings.tabLanguage') },
        { key: 'system', icon: <Monitor size={15} />, label: t('settings.tabSystem') },
      ],
    },
    {
      label: t('settings.groupAgent'),
      items: [
        { key: 'agent', icon: <Bot size={15} />, label: t('settings.tabAgent') },
        { key: 'config', icon: <Wrench size={15} />, label: t('settings.tabConfig') },
        { key: 'permission', icon: <Shield size={15} />, label: t('settings.tabPermission') },
      ],
    },
    {
      label: t('settings.groupAutomation'),
      items: [
        { key: 'task', icon: <ListTodo size={15} />, label: t('settings.tabTask') },
        { key: 'classify', icon: <StickyNote size={15} />, label: t('settings.tabClassify') },
        { key: 'goal', icon: <Target size={15} />, label: t('settings.tabGoal') },
      ],
    },
  ]

  const activeLabel = navGroups.flatMap((g) => g.items).find((i) => i.key === tab)?.label || t('settings.title')

  return (
    <div className={styles.overlay} onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className={styles.dialog} role="dialog" aria-modal="true" aria-label={t('settings.title')}>
        <nav className={styles.nav}>
          <div className={styles.navGroups}>
            {navGroups.map((group) => (
              <div key={group.label} className={styles.navGroup}>
                <div className={styles.navGroupLabel}>{group.label}</div>
                {group.items.map((item) => (
                  <button
                    key={item.key}
                    type="button"
                    className={`${styles.navItem} ${tab === item.key ? styles.navItemActive : ''}`}
                    onClick={() => setTab(item.key)}
                  >
                    <span className={styles.navIcon}>{item.icon}</span>
                    {item.label}
                  </button>
                ))}
              </div>
            ))}
          </div>
          <div className={styles.navFooter}>
            <div className={styles.appName}>openNexus</div>
          </div>
        </nav>

        <div className={styles.contentWrap}>
          <div className={styles.contentHeader}>
            <h2 className={styles.contentTitle}>{activeLabel}</h2>
            <button type="button" className={styles.closeBtn} onClick={onClose} title={t('common.close')}>
              <X size={18} />
            </button>
          </div>
          {error && <ErrorBanner message={error} onClose={() => setError('')} />}
          {loading ? <LoadingSpinner /> : (
            <div className={styles.content}>
              {tab === 'language' && (
                <>
                  <p className={styles.hint}>{t('settings.languageHint')}</p>
                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.language')}</label>
                    <div className={styles.langRow}>
                      <button type="button"
                        className={`${styles.langBtn} ${i18n.language === 'zh' ? styles.langBtnActive : ''}`}
                        onClick={() => switchLang('zh')}
                      >{t('settings.chinese')}</button>
                      <button type="button"
                        className={`${styles.langBtn} ${i18n.language === 'en' ? styles.langBtnActive : ''}`}
                        onClick={() => switchLang('en')}
                      >{t('settings.english')}</button>
                    </div>
                  </div>
                </>
              )}

              {tab === 'agent' && (
                <>
                  <p className={styles.hint}>{t('settings.hint')}</p>
                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.defaultAgentModel')}</label>
                    <div className={styles.defaultRow}>
                      {/* 默认 Agent·模型 合并下拉（与新建任务页同一控件，含 selector.filters 过滤） */}
                      <AgentModelSelector
                        agents={agents}
                        modelsByAgent={defaultModelsMap}
                        filters={linesToList(selectorFiltersText)}
                        selectedAgent={defaultAgent}
                        selectedModel={defaultModel}
                        placeholder={t('common.no')}
                        className={styles.input}
                        onSelect={handleSetDefaultCombo}
                      />
                      <button type="button" className={styles.secondaryBtn}
                        onClick={handleProbeDefaultModels}
                        disabled={defaultModelsProbing}
                        title={t('scheduledTask.probeTitle')}
                      >{defaultModelsProbing ? t('common.loading') : t('scheduledTask.probeConfig')}</button>
                      {defaultAgent && (
                        <button type="button" className={styles.clearDefaultBtn}
                          onClick={() => handleSetDefaultCombo('', '')}
                        >{t('common.cancel')}</button>
                      )}
                    </div>
                    <p className={styles.sectionHint}>{t('settings.defaultAgentModelHint')}</p>
                  </div>
                  {/* agent+模型 合并下拉的显示过滤（写回 config.yaml agents.selector.filters，保存即生效） */}
                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.selectorFilters')}</label>
                    <p className={styles.hint}>{t('settings.selectorFiltersListHint')}</p>
                    <div className={styles.inlineRow}>
                      {/* 多选下拉：搜索过滤 + 行右侧复选框勾选，替代平铺大列表 */}
                      <AgentModelMultiSelect
                        groups={selectorGroups}
                        checked={selectorChecked}
                        onToggle={toggleSelectorCombo}
                        onToggleGroup={toggleSelectorGroup}
                        className={styles.input}
                      />
                      <button type="button" className={styles.secondaryBtn}
                        onClick={() => setAllSelectorCombos(true)}
                      >{t('settings.selectorSelectAll')}</button>
                      <button type="button" className={styles.secondaryBtn}
                        onClick={() => setAllSelectorCombos(false)}
                      >{t('settings.selectorSelectNone')}</button>
                      <button type="button" className={styles.secondaryBtn}
                        onClick={handleProbeDefaultModels}
                        disabled={defaultModelsProbing}
                        title={t('scheduledTask.probeTitle')}
                      >{defaultModelsProbing ? t('common.loading') : t('scheduledTask.probeConfig')}</button>
                    </div>
                    {/* 高级：直接编辑正则规则（勾选操作会覆写此处内容） */}
                    <details className={styles.advancedSection}>
                      <summary className={styles.advancedSummary}>{t('settings.selectorFiltersAdvanced')}</summary>
                      <p className={styles.hint}>{t('settings.selectorFiltersHint')}</p>
                      <textarea
                        className={styles.textarea}
                        rows={4}
                        value={selectorFiltersText}
                        onChange={(e) => { setSelectorFiltersText(e.target.value); setSelectorSaved(false) }}
                        placeholder={'^claude-code/\nsonnet\n^cursor/.*gpt-5.*'}
                        spellCheck={false}
                      />
                    </details>
                    <div className={styles.inlineRow}>
                      <button type="button" className={styles.saveNoteBtn}
                        onClick={handleSaveSelectorFilters}
                        disabled={selectorSaving}
                      >
                        {selectorSaving ? t('common.saving') : t('common.save')}
                      </button>
                      {selectorSaved && (
                        <span className={styles.savedHint}>{t('settings.selectorFiltersSaved')}</span>
                      )}
                    </div>
                  </div>
                  <div className={styles.configList}>
                    <div className={styles.configListHeader}>
                      <h2 className={styles.sectionTitle}>{t('settings.agentList')}（{configs.length}）</h2>
                      <button type="button"
                        className={styles.registryBtn}
                        onClick={handleRefreshRegistry}
                        disabled={registryRefreshing}
                        title={t('settings.refreshRegistry')}
                      >{registryRefreshing ? t('settings.refreshingRegistry') : t('settings.refreshRegistry')}</button>
                    </div>
                    {registryResult && (
                      <p className={styles.registryResult}>
                        {t('settings.registryRefreshed', { added: registryResult.added, updated: registryResult.updated })}
                      </p>
                    )}
                    {agentUpdateMsg && (
                      <p className={styles.registryResult}>{agentUpdateMsg}</p>
                    )}
                    {configs.length > 0 && (
                      <input
                        type="search"
                        className={styles.configSearch}
                        value={configSearch}
                        onChange={(e) => setConfigSearch(e.target.value)}
                        placeholder={t('settings.searchAgent')}
                      />
                    )}
                    {configs.length === 0 ? (
                      <p className={styles.empty}>{t('settings.noAgents')}</p>
                    ) : (
                      configs
                        .filter((cfg) => {
                          const q = configSearch.trim().toLowerCase()
                          if (!q) return true
                          return cfg.display_name.toLowerCase().includes(q)
                            || cfg.type.toLowerCase().includes(q)
                            || (cfg.description || '').toLowerCase().includes(q)
                        })
                        .map((cfg) => (
                        <div key={cfg.id}>
                          <div className={styles.configRow}>
                            <div className={styles.configIcon}>{cfg.display_name.slice(0, 2).toUpperCase()}</div>
                            <div className={styles.configInfo}>
                              <div className={styles.configName}>{cfg.display_name}</div>
                              {cfg.description && <div className={styles.configDesc}>{cfg.description}</div>}
                            </div>
                            {cfg.enabled ? (
                              <button type="button" className={styles.disableBtn}
                                onClick={async () => {
                                  try { await updateAgentConfig(cfg.id, { ...cfg, enabled: false }); await loadData() }
                                  catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
                                }}
                              >{t('settings.disable')}</button>
                            ) : (
                              <button type="button" className={styles.enableBtn}
                                onClick={async () => {
                                  try { await updateAgentConfig(cfg.id, { ...cfg, enabled: true }); await loadData() }
                                  catch (err) { setError(err instanceof Error ? err.message : t('common.failed')) }
                                }}
                              >{t('settings.enable')}</button>
                            )}
                            {/* ACP 能力展示：agent-side / client-side method 支持情况 */}
                            <button type="button" className={styles.updateBtn}
                              onClick={() => setCapsAgentType(capsAgentType === cfg.type ? '' : cfg.type)}
                              title={t('settings.acpCaps.title')}
                            >{t('settings.acpCaps.btn')}</button>
                            <button type="button" className={styles.updateBtn}
                              onClick={() => handleUpdateAgent(cfg)}
                              disabled={updatingAgentId === cfg.id}
                              title={t('settings.updateAgent')}
                            >{updatingAgentId === cfg.id ? t('settings.updatingAgent') : t('settings.updateAgent')}</button>
                            <button type="button" className={styles.editIconBtn} title={t('common.edit')}
                              onClick={() => setEditingConfig(cfg)}
                            >⋯</button>
                          </div>
                          {capsAgentType === cfg.type && <AgentAcpCapsPanel agentType={cfg.type} />}
                        </div>
                      ))
                    )}
                  </div>
                </>
              )}

              {tab === 'classify' && (
                <>
                  <p className={styles.hint}>{t('settings.classifyHint')}</p>

                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.noteClassifyAgent')}</label>
                    <p className={styles.sectionHint}>{t('settings.noteClassifyAgentHint')}</p>
                    <select className={styles.input} value={noteAgent}
                      onChange={(e) => {
                        setNoteAgent(e.target.value)
                        setNoteModel('')
                        setNoteModelOptions([])
                      }}
                    >
                      <option value="">{t('common.no')}</option>
                      {agents.map((a) => (
                        <option key={a.type} value={a.type}>{a.display_name}（{a.type}）</option>
                      ))}
                      {/* 存储值对应的 agent 已停用/删除时，显式列出而非回退显示「否」，
                          避免用户误以为未配置（后台 worker 仍按该值排队分类）。 */}
                      {noteAgent && !agents.some((a) => a.type === noteAgent) && (
                        <option value={noteAgent}>
                          {t('settings.noteClassifyAgentUnavailable', { type: noteAgent })}
                        </option>
                      )}
                    </select>
                    {noteAgent && (
                      <>
                        <label className={styles.label}>{t('settings.noteClassifyModel')}</label>
                        <div className={styles.inlineRow}>
                          {noteModelOptions.length > 0 && noteModelOptions[0].options.length > 0 ? (
                            <select className={styles.input} value={noteModel}
                              onChange={(e) => setNoteModel(e.target.value)}
                            >
                              <option value="">{t('scheduledTask.defaultModel')}</option>
                              {noteModelOptions[0].options.map((o) => (
                                <option key={o.value} value={o.value}>
                                  {o.name !== o.value ? `${o.name} (${o.value})` : o.value}
                                </option>
                              ))}
                            </select>
                          ) : (
                            <input className={styles.input} type="text" value={noteModel}
                              onChange={(e) => setNoteModel(e.target.value)}
                              placeholder={t('scheduledTask.modelValuePlaceholder')}
                            />
                          )}
                          <button type="button" className={styles.secondaryBtn}
                            onClick={handleProbeNoteModel}
                            disabled={noteModelProbing}
                            title={t('scheduledTask.probeTitle')}
                          >{noteModelProbing ? t('common.loading') : t('scheduledTask.probeConfig')}</button>
                        </div>
                        <p className={styles.sectionHint}>
                          {noteModelProbing
                            ? t('common.loading')
                            : noteModelOptions.length === 0
                              ? t('scheduledTask.probeHint')
                              : t('scheduledTask.probeDone')}
                        </p>
                      </>
                    )}
                    <label className={styles.label}>{t('settings.noteClassifyInterval')}</label>
                    <p className={styles.sectionHint}>{t('settings.noteClassifyIntervalHint')}</p>
                    <input
                      className={styles.input}
                      type="number"
                      min={1}
                      max={1440}
                      value={noteInterval}
                      onChange={(e) => setNoteInterval(Math.min(1440, Math.max(1, Number(e.target.value) || 5)))}
                    />
                    <label className={styles.label}>{t('settings.noteClassifyPrompt')}</label>
                    <p className={styles.sectionHint}>{t('settings.noteClassifyPromptHint')}</p>
                    <textarea
                      className={styles.textarea}
                      rows={8}
                      value={translatePrompt(notePrompt)}
                      onChange={(e) => setNotePrompt(e.target.value)}
                    />
                    <button
                      type="button"
                      className={styles.saveNoteBtn}
                      disabled={noteSettingsSaving}
                      onClick={handleSaveNoteSettings}
                    >
                      {noteSettingsSaving ? t('notes.saving') : t('common.save')}
                    </button>
                    {noteClassifySessionId > 0 && (
                      <Link className={styles.classifyTaskLink} to={`/sessions/${noteClassifySessionId}`} onClick={onClose}>
                        {t('settings.viewClassifyTask')}
                      </Link>
                    )}

                    <label className={styles.label}>{t('settings.noteMcpTitle')}</label>
                    <p className={styles.sectionHint}>{t('settings.noteMcpHint')}</p>
                    {!noteMcpToken ? (
                      <button
                        type="button"
                        className={styles.secondaryBtn}
                        disabled={noteMcpGenerating}
                        onClick={handleGenerateNoteMcpToken}
                      >
                        {noteMcpGenerating ? t('common.loading') : t('settings.noteMcpGenerate')}
                      </button>
                    ) : (
                      <>
                        <label className={styles.label}>{t('settings.noteMcpEndpoint')}</label>
                        <div className={styles.inlineRow}>
                          <input
                            className={styles.input}
                            type="text"
                            readOnly
                            value={noteMcpEndpoint}
                          />
                          <button
                            type="button"
                            className={styles.secondaryBtn}
                            onClick={() => copyText(noteMcpEndpoint)}
                          >
                            {t('settings.noteMcpCopy')}
                          </button>
                        </div>
                        <label className={styles.label}>{t('settings.noteMcpToken')}</label>
                        <div className={styles.inlineRow}>
                          <input className={styles.input} type="text" readOnly value={noteMcpToken} />
                          <button
                            type="button"
                            className={styles.secondaryBtn}
                            onClick={() => copyText(noteMcpToken)}
                          >
                            {t('settings.noteMcpCopy')}
                          </button>
                        </div>
                        <label className={styles.label}>{t('settings.noteMcpConfig')}</label>
                        <textarea
                          className={styles.textarea}
                          rows={8}
                          readOnly
                          value={noteMcpConfig}
                        />
                        <button
                          type="button"
                          className={styles.secondaryBtn}
                          onClick={() => copyText(noteMcpConfig)}
                        >
                          {t('settings.noteMcpCopyConfig')}
                        </button>
                        <p className={styles.sectionHint}>{t('settings.noteMcpAuthHint')}</p>
                      </>
                    )}
                  </div>
                </>
              )}

              {tab === 'config' && (
                <>
                  <p className={styles.hint}>{t('settings.configHint')}</p>
                  <ConfigEditor />
                </>
              )}

              {tab === 'task' && (
                <>
                  <p className={styles.hint}>{t('settings.taskHint')}</p>

                  <div className={styles.defaultSection}>
                    {/* 功能开关 */}
                    <label className={styles.label}>
                      <input
                        type="checkbox"
                        checked={taskAutoTag}
                        onChange={(e) => setTaskAutoTag(e.target.checked)}
                        style={{ marginRight: 8, verticalAlign: 'middle' }}
                      />
                      {t('settings.autoTag')}
                    </label>
                    <p className={styles.sectionHint}>{t('settings.autoTagHint')}</p>

                    <label className={styles.label}>
                      <input
                        type="checkbox"
                        checked={taskAutoTitle}
                        onChange={(e) => setTaskAutoTitle(e.target.checked)}
                        style={{ marginRight: 8, verticalAlign: 'middle' }}
                      />
                      {t('settings.autoTitle')}
                    </label>
                    <p className={styles.sectionHint}>{t('settings.autoTitleHint')}</p>

                    {/* 执行 Agent 选择 */}
                    <label className={styles.label}>{t('settings.taskAgent')}</label>
                    <select className={styles.input} value={taskAgent}
                      onChange={(e) => setTaskAgent(e.target.value)}
                    >
                      <option value="">{t('common.no')}</option>
                      {agents.map((a) => (
                        <option key={a.type} value={a.type}>{a.display_name}（{a.type}）</option>
                      ))}
                    </select>

                    {/* 预定义标签管理 */}
                    <label className={styles.label}>{t('settings.predefinedTags')}</label>
                    <p className={styles.sectionHint}>{t('settings.predefinedTagsHint')}</p>
                    <div className={styles.inlineRow}>
                      <input className={styles.input} type="text" value={taskTagInput}
                        onChange={(e) => setTaskTagInput(e.target.value)}
                        placeholder={t('settings.tagPlaceholder')}
                        onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); handleAddTaskTag() } }}
                      />
                      <button type="button" className={styles.secondaryBtn} onClick={handleAddTaskTag}>
                        {t('settings.addTag')}
                      </button>
                    </div>
                    <div className={styles.tagList}>
                      {taskTags.map((tag) => (
                        <span key={tag} className={styles.tagChip}>
                          {translateTag(tag)}
                          <button type="button" className={styles.tagRemove}
                            onClick={() => handleRemoveTaskTag(tag)}
                          >×</button>
                        </span>
                      ))}
                    </div>

                    {/* 归档任务在回收站的保留天数 */}
                    <label className={styles.label}>{t('settings.archiveRetention')}</label>
                    <p className={styles.sectionHint}>{t('settings.archiveRetentionHint')}</p>
                    <input className={styles.input} type="number" min={1} max={365}
                      value={taskArchiveDays}
                      onChange={(e) => setTaskArchiveDays(Math.max(1, Number(e.target.value) || 3))}
                      style={{ maxWidth: 120 }}
                    />

                    {/* 高级：自定义提示词 */}
                    <details className={styles.advancedSection}>
                      <summary className={styles.advancedSummary}>{t('settings.taskAdvanced')}</summary>
                      <label className={styles.label}>{t('settings.tagPrompt')}</label>
                      <p className={styles.sectionHint}>{t('settings.tagPromptHint')}</p>
                      <textarea className={styles.textarea} rows={6}
                        value={translatePrompt(taskTagPrompt)}
                        onChange={(e) => setTaskTagPrompt(e.target.value)}
                      />
                      <label className={styles.label}>{t('settings.titlePrompt')}</label>
                      <p className={styles.sectionHint}>{t('settings.titlePromptHint')}</p>
                      <textarea className={styles.textarea} rows={6}
                        value={translatePrompt(taskTitlePrompt)}
                        onChange={(e) => setTaskTitlePrompt(e.target.value)}
                      />
                    </details>

                    <button type="button" className={styles.saveNoteBtn}
                      disabled={taskSettingsSaving}
                      onClick={handleSaveTaskSettings}
                    >
                      {taskSettingsSaving ? t('common.saving') : t('common.save')}
                    </button>
                    {taskSettingsSaved && (
                      <span className={styles.savedHint}>{t('settings.taskSettingsSaved')}</span>
                    )}
                  </div>
                </>
              )}

              {tab === 'goal' && (
                <>
                  <p className={styles.hint}>{t('settings.goalHint')}</p>

                  <div className={styles.defaultSection}>
                    {/* 评估 Agent·模型 合并下拉（与默认 Agent 设置同一控件，含 selector.filters 过滤）；空 = 会话自身 agent */}
                    <label className={styles.label}>{t('settings.goalAgent')}</label>
                    <p className={styles.sectionHint}>{t('settings.goalAgentHint')}</p>
                    <div className={styles.defaultRow}>
                      <AgentModelSelector
                        agents={agents}
                        modelsByAgent={defaultModelsMap}
                        filters={linesToList(selectorFiltersText)}
                        selectedAgent={goalAgent}
                        selectedModel={goalModel}
                        placeholder={t('settings.goalAgentDefault')}
                        className={styles.input}
                        onSelect={(agentType, modelValue) => {
                          setGoalAgent(agentType)
                          setGoalModel(modelValue)
                          setGoalSettingsSaved(false)
                        }}
                      />
                      <button type="button" className={styles.secondaryBtn}
                        onClick={handleProbeDefaultModels}
                        disabled={defaultModelsProbing}
                        title={t('scheduledTask.probeTitle')}
                      >{defaultModelsProbing ? t('common.loading') : t('scheduledTask.probeConfig')}</button>
                      {goalAgent && (
                        <button type="button" className={styles.clearDefaultBtn}
                          onClick={() => { setGoalAgent(''); setGoalModel(''); setGoalSettingsSaved(false) }}
                        >{t('common.cancel')}</button>
                      )}
                    </div>

                    {/* 限制条件：0 = 使用默认值 */}
                    <label className={styles.label}>{t('settings.goalMaxTurns')}</label>
                    <p className={styles.sectionHint}>{t('settings.goalMaxTurnsHint')}</p>
                    <input
                      className={styles.input}
                      type="number"
                      min={0}
                      max={1000}
                      value={goalMaxTurns}
                      placeholder="20"
                      onChange={(e) => setGoalMaxTurns(Math.min(1000, Math.max(0, Number(e.target.value) || 0)))}
                    />
                    <label className={styles.label}>{t('settings.goalMaxDuration')}</label>
                    <p className={styles.sectionHint}>{t('settings.goalMaxDurationHint')}</p>
                    <input
                      className={styles.input}
                      type="number"
                      min={0}
                      max={10080}
                      value={goalMaxDuration}
                      placeholder="60"
                      onChange={(e) => setGoalMaxDuration(Math.min(10080, Math.max(0, Number(e.target.value) || 0)))}
                    />

                    <button type="button" className={styles.saveNoteBtn}
                      disabled={goalSettingsSaving}
                      onClick={handleSaveGoalSettings}
                    >
                      {goalSettingsSaving ? t('common.saving') : t('common.save')}
                    </button>
                    {goalSettingsSaved && (
                      <span className={styles.savedHint}>{t('settings.taskSettingsSaved')}</span>
                    )}
                  </div>

                  {/* 评估角色管理：文件式定义（用户目录 *.md），goal 首轮评估时按完成条件自动选取 */}
                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.goalRoles')}</label>
                    <p className={styles.sectionHint}>{t('settings.goalRolesHint', { dir: goalRolesDir || '~/.agents/goal-roles', condition: '{{condition}}', transcript: '{{transcript}}' })}</p>

                    {goalRoles.length === 0 && (
                      <p className={styles.hint}>{t('settings.goalRolesEmpty')}</p>
                    )}
                    {goalRoles.length > 0 && (
                      <ul className={styles.roleList}>
                        {goalRoles.map((role) => (
                          <li key={role.location} className={styles.roleItem}>
                            <div className={styles.roleMain}>
                              <div className={styles.roleHead}>
                                <span className={styles.roleName}>{role.name}</span>
                                <span className={styles.roleScope}>{role.scope}</span>
                                {(role.agent || role.model) && (
                                  <span className={styles.roleMeta}>{[role.agent, role.model].filter(Boolean).join(' · ')}</span>
                                )}
                              </div>
                              <p className={styles.roleDesc}>{role.description}</p>
                              {role.skills && role.skills.length > 0 && (
                                <p className={styles.roleMeta}>skills: {role.skills.join(', ')}</p>
                              )}
                            </div>
                            <div className={styles.roleActions}>
                              <button type="button" className={styles.secondaryBtn}
                                onClick={() => handleEditGoalRole(role)}
                              >{t('common.edit')}</button>
                              <button type="button" className={styles.clearDefaultBtn}
                                onClick={() => handleDeleteGoalRole(role)}
                              >{t('common.delete')}</button>
                            </div>
                          </li>
                        ))}
                      </ul>
                    )}

                    {/* 新建：输入角色名 → 生成模板并打开编辑器，保存时写入用户目录 */}
                    <div className={styles.defaultRow}>
                      <input
                        className={styles.input}
                        value={roleNewName}
                        placeholder={t('settings.goalRoleNamePlaceholder')}
                        onChange={(e) => setRoleNewName(e.target.value)}
                        onKeyDown={(e) => { if (e.key === 'Enter') handleNewGoalRole() }}
                      />
                      <button type="button" className={styles.secondaryBtn}
                        disabled={!roleNewName.trim() || !goalRolesDir}
                        onClick={handleNewGoalRole}
                      >{t('settings.goalRoleNew')}</button>
                    </div>

                    {roleEditPath && (
                      <div className={styles.roleEditor}>
                        <p className={styles.sectionHint}>{roleEditPath}</p>
                        <textarea
                          className={styles.textarea}
                          rows={14}
                          value={roleEditContent}
                          spellCheck={false}
                          onChange={(e) => setRoleEditContent(e.target.value)}
                        />
                        <div className={styles.defaultRow}>
                          <button type="button" className={styles.saveNoteBtn}
                            disabled={roleEditSaving}
                            onClick={handleSaveGoalRole}
                          >{roleEditSaving ? t('common.saving') : t('common.save')}</button>
                          <button type="button" className={styles.clearDefaultBtn}
                            onClick={() => { setRoleEditPath(''); setRoleEditContent('') }}
                          >{t('common.cancel')}</button>
                        </div>
                      </div>
                    )}
                  </div>
                </>
              )}

              {tab === 'permission' && (
                <>
                  <p className={styles.hint}>{t('settings.permissionHint')}</p>

                  <div className={styles.defaultSection}>
                    <label className={styles.label}>
                      <input
                        type="checkbox"
                        checked={sandboxEnabled}
                        onChange={(e) => setSandboxEnabled(e.target.checked)}
                        style={{ marginRight: 8, verticalAlign: 'middle' }}
                      />
                      {t('settings.sandboxEnabled')}
                    </label>
                    <p className={styles.sectionHint}>{t('settings.sandboxEnabledHint')}</p>
                    {sandboxEnabled && (
                      <div style={{ marginTop: 8 }}>
                        <label className={styles.label}>{t('settings.sandboxMode')}</label>
                        <select
                          className={styles.input}
                          value={sandboxMode}
                          onChange={(e) => setSandboxMode(e.target.value === 'enforce' ? 'enforce' : 'auto')}
                        >
                          <option value="auto">{t('settings.sandboxModeAuto')}</option>
                          <option value="enforce">{t('settings.sandboxModeEnforce')}</option>
                        </select>
                        <p className={styles.sectionHint}>{t('settings.sandboxModeHint')}</p>
                      </div>
                    )}
                  </div>

                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.permissionAllow')}</label>
                    <p className={styles.hint}>{t('settings.permissionAllowHint')}</p>
                    <textarea
                      className={styles.textarea}
                      rows={6}
                      value={permAllow}
                      onChange={(e) => setPermAllow(e.target.value)}
                      placeholder={'Bash(git status *)\nBash(go test *)\nBash(ls *)'}
                      spellCheck={false}
                    />
                  </div>

                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.permissionAsk')}</label>
                    <p className={styles.hint}>{t('settings.permissionAskHint')}</p>
                    <textarea
                      className={styles.textarea}
                      rows={5}
                      value={permAsk}
                      onChange={(e) => setPermAsk(e.target.value)}
                      placeholder={'Bash(git commit *)\nBash(docker *)\nBash(kubectl *)'}
                      spellCheck={false}
                    />
                  </div>

                  <div className={styles.defaultSection}>
                    <label className={styles.label}>{t('settings.permissionDeny')}</label>
                    <p className={styles.hint}>{t('settings.permissionDenyHint')}</p>
                    <textarea
                      className={styles.textarea}
                      rows={5}
                      value={permDeny}
                      onChange={(e) => setPermDeny(e.target.value)}
                      placeholder={'Bash(rm *)\nBash(shutdown *)\nBash(dd *)'}
                      spellCheck={false}
                    />
                  </div>

                  <button type="button" className={styles.saveNoteBtn}
                    onClick={handleSavePermissionSettings}
                    disabled={permSaving}
                  >
                    {permSaving ? t('common.saving') : t('common.save')}
                  </button>
                  {permSaved && (
                    <span className={styles.savedHint}>{t('settings.taskSettingsSaved')}</span>
                  )}
                </>
              )}

              {tab === 'system' && (
                <>
                  <p className={styles.hint}>{t('system.hint')}</p>
                  <div className={styles.defaultSection}>
                    <button type="button" className={styles.saveNoteBtn}
                      disabled={reloadStatus === 'reloading'}
                      onClick={handleReloadProgram}
                    >
                      {reloadStatus === 'reloading' ? t('system.reloading') : t('system.reloadProgram')}
                    </button>
                    {reloadStatus === 'success' && (
                      <span className={styles.savedHint}>{t('system.reloadSuccess')}</span>
                    )}
                    {reloadStatus === 'failed' && reloadError && (
                      <span className={`${styles.savedHint} ${styles.errorText}`}>{reloadError}</span>
                    )}
                    <p className={styles.sectionHint}>
                      {window.opennexus?.isElectron ? t('system.desktopHint') : t('system.browserHint')}
                    </p>
                  </div>
                  {/* config.yaml 原生编辑（保存前后端强制校验格式） */}
                  <RawConfigCard />
                </>
              )}
            </div>
          )}
        </div>
      </div>

      {editingConfig && (
        <EditAgentDialog
          config={editingConfig}
          saving={saving}
          onSave={handleSaveEdit}
          onDelete={handleDeleteEditing}
          onClose={closeEditDialog}
          onResetToRegistry={async () => {
            try {
              const { data } = await getRegistryDefault(editingConfig.id)
              return data
            } catch (err) {
              setError(err instanceof Error ? err.message : t('settings.resetToRegistryNotFound'))
              return null
            }
          }}
        />
      )}
    </div>
  )
}
