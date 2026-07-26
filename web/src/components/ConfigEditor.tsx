import { useState, useEffect } from 'react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { getAgentsConfig, updateAgentsConfig, scanSkillFiles, scanCommandFiles, scanRuleFiles, scanSubAgentFiles, writeFileContent } from '../api/config'
import type { DirConfigView, AgentsConfigView, ScannedFileItem } from '../api/config'
import ErrorBanner from './ErrorBanner'
import FileEditor from './FileEditor'
import McpConfigCard from './McpConfigCard'
import { Zap, SquareTerminal, ClipboardList, Bot, X, Trash2, Plus, Network, ArrowRight } from 'lucide-react'
import styles from './ConfigEditor.module.css'

type ConfigSection = 'skills' | 'commands' | 'rules' | 'subagents'

export default function ConfigEditor() {
  const { t } = useTranslation()
  const [config, setConfig] = useState<AgentsConfigView | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [editingSection, setEditingSection] = useState<ConfigSection | null>(null)
  const [editDirs, setEditDirs] = useState<DirConfigView | null>(null)

  // 扫描结果
  const [scanningSection, setScanningSection] = useState<ConfigSection | null>(null)
  const [scannedSkills, setScannedSkills] = useState<ScannedFileItem[]>([])
  const [scannedCommands, setScannedCommands] = useState<ScannedFileItem[]>([])
  const [scannedRules, setScannedRules] = useState<ScannedFileItem[]>([])
  const [scannedSubAgents, setScannedSubAgents] = useState<ScannedFileItem[]>([])

  // 文件编辑
  const [editingFile, setEditingFile] = useState<ScannedFileItem | null>(null)
  const [fileSaving, setFileSaving] = useState(false)

  useEffect(() => {
    loadConfig()
  }, [])

  async function loadConfig() {
    setLoading(true)
    setError('')
    try {
      const resp = await getAgentsConfig()
      setConfig(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  function openEditor(section: ConfigSection) {
    if (!config) return
    setEditingSection(section)
    setEditDirs({ ...config[section], user_dirs: [...config[section].user_dirs], project_dirs: [...config[section].project_dirs] })
    setSuccess('')
    setError('')
  }

  function closeEditor() {
    setEditingSection(null)
    setEditDirs(null)
  }

  function addDir(type: 'user_dirs' | 'project_dirs') {
    if (!editDirs) return
    setEditDirs({
      ...editDirs,
      [type]: [...editDirs[type], ''],
    })
  }

  function updateDir(type: 'user_dirs' | 'project_dirs', index: number, value: string) {
    if (!editDirs) return
    const newDirs = [...editDirs[type]]
    newDirs[index] = value
    setEditDirs({ ...editDirs, [type]: newDirs })
  }

  function removeDir(type: 'user_dirs' | 'project_dirs', index: number) {
    if (!editDirs) return
    setEditDirs({
      ...editDirs,
      [type]: editDirs[type].filter((_, i) => i !== index),
    })
  }

  async function handleSave() {
    if (!config || !editDirs || !editingSection) return
    setSaving(true)
    setError('')
    setSuccess('')
    try {
      const updated = { ...config, [editingSection]: editDirs }
      await updateAgentsConfig(updated)
      setConfig(updated)
      closeEditor()
      setSuccess(t('configEditor.saveSuccess'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setSaving(false)
    }
  }

  async function handleScan(section: ConfigSection) {
    if (!config) return
    setScanningSection(section)
    setError('')
    setSuccess('')

    try {
      if (section === 'skills') {
        const resp = await scanSkillFiles()
        setScannedSkills(resp.data.skills || [])
      } else if (section === 'commands') {
        const resp = await scanCommandFiles()
        setScannedCommands(resp.data.commands || [])
      } else if (section === 'rules') {
        const resp = await scanRuleFiles()
        setScannedRules(resp.data.rules || [])
      } else if (section === 'subagents') {
        const resp = await scanSubAgentFiles()
        setScannedSubAgents(resp.data.subagents || [])
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setScanningSection(null)
    }
  }

  // rules 扫描（保留旧入口以最小化 UI 改动）
  async function handleScanRules() {
    await handleScan('rules')
  }

  // 打开文件编辑
  function openFileEditor(file: ScannedFileItem) {
    setEditingFile(file)
    setError('')
    setSuccess('')
  }

  function closeFileEditor() {
    if (fileSaving) return
    setEditingFile(null)
  }

  // 保存文件
  async function handleFileSave(filePath: string, content: string) {
    setFileSaving(true)
    setError('')
    try {
      await writeFileContent(filePath, content)
      closeFileEditor()
      setSuccess(t('configEditor.fileSaved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setFileSaving(false)
    }
  }

  function sectionIcon(section: ConfigSection): ReactNode {
    switch (section) {
      case 'skills': return <Zap size={14} style={{ verticalAlign: '-2px' }} />
      case 'commands': return <SquareTerminal size={14} style={{ verticalAlign: '-2px' }} />
      case 'rules': return <ClipboardList size={14} style={{ verticalAlign: '-2px' }} />
      case 'subagents': return <Bot size={14} style={{ verticalAlign: '-2px' }} />
    }
  }

  function sectionTitle(section: ConfigSection): string {
    switch (section) {
      case 'skills': return t('configEditor.skills')
      case 'commands': return t('configEditor.commands')
      case 'rules': return t('configEditor.rules')
      case 'subagents': return t('configEditor.subagents')
    }
  }

  function sectionDesc(section: ConfigSection): string {
    switch (section) {
      case 'skills': return t('configEditor.skillsDesc')
      case 'commands': return t('configEditor.commandsDesc')
      case 'rules': return t('configEditor.rulesDesc')
      case 'subagents': return t('configEditor.subagentsDesc')
    }
  }

  function scanHint(section: ConfigSection): string {
    switch (section) {
      case 'skills': return t('configEditor.scanSkillsHint')
      case 'commands': return t('configEditor.scanCommandsHint')
      case 'subagents': return t('configEditor.scanSubAgentsHint')
      default: return ''
    }
  }

  if (loading) {
    return <div className={styles.loading}>{t('common.loading')}</div>
  }

  if (!config) {
    return <div className={styles.empty}>{t('common.noData')}</div>
  }

  const sections: ConfigSection[] = ['skills', 'commands', 'rules', 'subagents']

  return (
    <>
      {error && <ErrorBanner message={error} onClose={() => setError('')} />}
      {success && <div className={styles.successBanner}>{success}</div>}

      {/* 配置卡片列表 */}
      <div className={styles.cards}>
        {sections.map((section) => {
          const cfg = config[section]

          return (
            <div key={section} className={styles.card}>
              <div className={styles.cardHeader}>
                <div className={styles.cardTitle}>
                  <span className={styles.cardIcon}>{sectionIcon(section)}</span>
                  <span>{sectionTitle(section)}</span>
                </div>
                <div className={styles.cardActions}>
                  {section === 'rules' ? (
                    <button
                      type="button"
                      className={styles.scanBtn}
                      disabled={scanningSection === section}
                      onClick={handleScanRules}
                      title={t('configEditor.scanRulesHint')}
                    >
                      {scanningSection === section ? t('common.loading') : t('configEditor.scanFiles')}
                    </button>
                  ) : (
                    <button
                      type="button"
                      className={styles.scanBtn}
                      disabled={scanningSection === section}
                      onClick={() => handleScan(section)}
                      title={scanHint(section)}
                    >
                      {scanningSection === section ? t('common.loading') : t('configEditor.scanFiles')}
                    </button>
                  )}
                  <button
                    type="button"
                    className={styles.editBtn}
                    onClick={() => openEditor(section)}
                  >
                    {t('common.edit')}
                  </button>
                </div>
              </div>

              <p className={styles.cardDesc}>{sectionDesc(section)}</p>

              <div className={styles.dirGroups}>
                <div className={styles.dirGroup}>
                  <span className={styles.dirLabel}>{t('configEditor.userDirs')}</span>
                  <div className={styles.dirList}>
                    {cfg.user_dirs && cfg.user_dirs.length > 0
                      ? cfg.user_dirs.map((dir, i) => (
                        <code key={`user-${i}`} className={styles.dirItem}>{dir}</code>
                      ))
                      : <span className={styles.dirEmpty}>{t('common.noData')}</span>
                    }
                  </div>
                </div>

                {section !== 'rules' && (
                  <div className={styles.dirGroup}>
                    <span className={styles.dirLabel}>{t('configEditor.projectDirs')}</span>
                    <div className={styles.dirList}>
                      {cfg.project_dirs && cfg.project_dirs.length > 0
                        ? cfg.project_dirs.map((dir, i) => (
                          <code key={`proj-${i}`} className={styles.dirItem}>{dir}</code>
                        ))
                        : <span className={styles.dirEmpty}>{t('common.noData')}</span>
                      }
                    </div>
                  </div>
                )}
              </div>

              {/* 扫描结果 - Skills */}
              {section === 'skills' && scannedSkills.length > 0 && (
                <div className={styles.scanResult}>
                  <div className={styles.scanResultTitle}>
                    {t('configEditor.scannedSkills')}（{scannedSkills.length}）— {t('configEditor.clickToEdit')}
                  </div>
                  <div className={styles.scanList}>
                    {scannedSkills.map((s, i) => (
                      <div
                        key={i}
                        className={styles.scanItem}
                        onClick={() => openFileEditor(s)}
                        title={t('configEditor.clickToEdit')}
                      >
                        <div className={styles.scanItemName}>{s.name}</div>
                        {s.description && <div className={styles.scanItemDesc}>{s.description}</div>}
                        <div className={styles.scanItemMeta}>
                          <span className={styles.scanItemScope}>{s.scope}</span>
                          {s.path && <span className={styles.scanItemPath}>{s.path}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* 扫描结果 - Commands */}
              {section === 'commands' && scannedCommands.length > 0 && (
                <div className={styles.scanResult}>
                  <div className={styles.scanResultTitle}>
                    {t('configEditor.scannedCommands')}（{scannedCommands.length}）— {t('configEditor.clickToEdit')}
                  </div>
                  <div className={styles.scanList}>
                    {scannedCommands.map((c, i) => (
                      <div
                        key={i}
                        className={styles.scanItem}
                        onClick={() => openFileEditor(c)}
                        title={t('configEditor.clickToEdit')}
                      >
                        <div className={styles.scanItemName}>{c.name}</div>
                        {c.description && <div className={styles.scanItemDesc}>{c.description}</div>}
                        <div className={styles.scanItemMeta}>
                          <span className={styles.scanItemScope}>{c.scope}</span>
                          {c.path && <span className={styles.scanItemPath}>{c.path}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* 扫描结果 - Rules */}
              {section === 'rules' && scannedRules.length > 0 && (
                <div className={styles.scanResult}>
                  <div className={styles.scanResultTitle}>
                    {t('configEditor.scannedRules')}（{scannedRules.length}）— {t('configEditor.clickToEdit')}
                  </div>
                  <div className={styles.scanList}>
                    {scannedRules.map((r, i) => (
                      <div
                        key={i}
                        className={styles.scanItem}
                        onClick={() => openFileEditor(r)}
                        title={t('configEditor.clickToEdit')}
                      >
                        <div className={styles.scanItemName}>{r.name}</div>
                        {r.description && <div className={styles.scanItemDesc}>{r.description}</div>}
                        <div className={styles.scanItemMeta}>
                          <span className={styles.scanItemScope}>{r.scope}</span>
                          {r.always_apply && <span className={styles.scanItemScope}>{t('configEditor.alwaysApply')}</span>}
                          {r.path && <span className={styles.scanItemPath}>{r.path}</span>}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* 扫描结果 - SubAgents */}
              {section === 'subagents' && scannedSubAgents.length > 0 && (
                <div className={styles.scanResult}>
                  <div className={styles.scanResultTitle}>
                    {t('configEditor.scannedSubAgents')}（{scannedSubAgents.length}）— {t('configEditor.clickToEdit')}
                  </div>
                  <div className={styles.scanList}>
                    {scannedSubAgents.map((s, i) => (
                      <div
                        key={i}
                        className={styles.scanItem}
                        onClick={() => openFileEditor(s)}
                        title={t('configEditor.clickToEdit')}
                      >
                        <div className={styles.scanItemName}>{s.name}</div>
                        {s.description && <div className={styles.scanItemDesc}>{s.description}</div>}
                        <div className={styles.scanItemMeta}>
                          <span className={styles.scanItemScope}>{s.scope}</span>
                          {s.model && <span className={styles.scanItemPath}>{t('configEditor.model')}: {s.model}</span>}
                          {s.path && <span className={styles.scanItemPath}>{s.path}</span>}
                        </div>
                        {s.tools && s.tools.length > 0 && (
                          <div className={styles.scanItemTools}>
                            {s.tools.map((tool) => (
                              <span key={tool} className={styles.toolChip}>{tool}</span>
                            ))}
                          </div>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* MCP 配置卡片（全局共享，注入给所有 agent 会话）。
        聚合网关已抽到独立页面 /mcp-gateway，这里只留跳转入口。 */}
      <div className={styles.cards}>
        <McpConfigCard />
        <Link to="/mcp-gateway" className={styles.linkCard}>
          <div className={styles.linkCardBody}>
            <span className={styles.cardIcon}><Network size={16} style={{ verticalAlign: '-2px' }} /></span>
            <div>
              <div className={styles.cardTitle}>{t('configEditor.gateway')}</div>
              <div className={styles.linkCardDesc}>{t('configEditor.gatewayLinkDesc')}</div>
            </div>
          </div>
          <ArrowRight size={16} className={styles.linkCardArrow} />
        </Link>
      </div>

      {/* 路径配置编辑弹窗 */}
      {editingSection && editDirs && (
        <div className={styles.overlay} onClick={closeEditor}>
          <div className={styles.dialog} onClick={(e) => e.stopPropagation()}>
            <div className={styles.dialogHeader}>
              <h3 className={styles.dialogTitle}>
                {sectionIcon(editingSection)} {t('common.edit')} {sectionTitle(editingSection)}
              </h3>
              <button type="button" className={styles.closeBtn} onClick={closeEditor} disabled={saving}>
                <X size={16} />
              </button>
            </div>

            <div className={styles.dialogBody}>
              <div className={styles.editSection}>
                <div className={styles.editSectionHeader}>
                  <span className={styles.editSectionTitle}>{t('configEditor.userDirs')}</span>
                  <button type="button" className={styles.addBtn} onClick={() => addDir('user_dirs')} disabled={saving}>
                    <Plus size={13} style={{ verticalAlign: '-2px', marginRight: 2 }} />{t('configEditor.addPath')}
                  </button>
                </div>
                {editDirs.user_dirs.map((dir, i) => (
                  <div key={`user-${i}`} className={styles.dirInputRow}>
                    <input
                      className={styles.input}
                      type="text"
                      value={dir}
                      onChange={(e) => updateDir('user_dirs', i, e.target.value)}
                      placeholder={editingSection === 'subagents' ? '~/.agents/agents' : '~/.claude/skills'}
                      disabled={saving}
                    />
                    <button
                      type="button"
                      className={styles.removeBtn}
                      onClick={() => removeDir('user_dirs', i)}
                      disabled={saving}
                      title={t('common.delete')}
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                ))}
              </div>

              {editingSection !== 'rules' && (
                <div className={styles.editSection}>
                  <div className={styles.editSectionHeader}>
                    <span className={styles.editSectionTitle}>{t('configEditor.projectDirs')}</span>
                    <button type="button" className={styles.addBtn} onClick={() => addDir('project_dirs')} disabled={saving}>
                      <Plus size={13} style={{ verticalAlign: '-2px', marginRight: 2 }} />{t('configEditor.addPath')}
                    </button>
                  </div>
                  {editDirs.project_dirs.map((dir, i) => (
                    <div key={`proj-${i}`} className={styles.dirInputRow}>
                      <input
                        className={styles.input}
                        type="text"
                        value={dir}
                        onChange={(e) => updateDir('project_dirs', i, e.target.value)}
                        placeholder={editingSection === 'subagents' ? '.agents/agents' : '.claude/skills'}
                        disabled={saving}
                      />
                      <button
                        type="button"
                        className={styles.removeBtn}
                        onClick={() => removeDir('project_dirs', i)}
                        disabled={saving}
                        title={t('common.delete')}
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div className={styles.dialogFooter}>
              <button type="button" className={styles.cancelBtn} onClick={closeEditor} disabled={saving}>
                {t('common.cancel')}
              </button>
              <button type="button" className={styles.saveBtn} onClick={handleSave} disabled={saving}>
                {saving ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* 文件编辑弹窗 */}
      <FileEditor
        file={editingFile}
        saving={fileSaving}
        onSave={handleFileSave}
        onClose={closeFileEditor}
      />
    </>
  )
}
