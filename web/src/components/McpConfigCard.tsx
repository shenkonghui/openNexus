import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { getMCPConfig, updateMCPConfig, getMCPStatus } from '../api/config'
import type { MCPConfigResponse, MCPServerStatus } from '../api/config'
import { Plug, X, ChevronRight, ChevronDown } from 'lucide-react'
import styles from './ConfigEditor.module.css'

export default function McpConfigCard() {
  const { t } = useTranslation()
  const [data, setData] = useState<MCPConfigResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [saving, setSaving] = useState(false)

  // 探测状态
  const [statuses, setStatuses] = useState<MCPServerStatus[] | null>(null)
  const [probing, setProbing] = useState(false)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  useEffect(() => {
    load()
  }, [])

  async function load() {
    setLoading(true)
    setError('')
    try {
      const resp = await getMCPConfig()
      setData(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  function openEditor() {
    if (!data) return
    setDraft(data.config || defaultPlaceholder())
    setEditing(true)
    setSuccess('')
    setError('')
  }

  function closeEditor() {
    if (saving) return
    setEditing(false)
  }

  async function handleSave() {
    setSaving(true)
    setError('')
    setSuccess('')
    try {
      const resp = await updateMCPConfig(draft)
      setData(resp.data)
      setEditing(false)
      setSuccess(t('configEditor.mcpSaved'))
      // 配置变更后清空旧探测结果
      setStatuses(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSaving(false)
    }
  }

  async function copyConfig() {
    if (!data?.config) return
    try {
      await navigator.clipboard.writeText(data.config)
    } catch {
      setError(t('settings.noteMcpCopyFailed'))
    }
  }

  async function handleProbe() {
    setProbing(true)
    setError('')
    setSuccess('')
    setExpanded(new Set())
    try {
      const resp = await getMCPStatus()
      const list = resp.data.servers || []
      setStatuses(list)
      setSuccess(t('configEditor.mcpProbeDone'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setProbing(false)
    }
  }

  function toggleExpand(name: string) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(name)) {
        next.delete(name)
      } else {
        next.add(name)
      }
      return next
    })
  }

  const count = data?.count ?? 0

  return (
    <>
      {error && <div className={styles.successBanner} style={{ background: 'var(--danger-bg)', color: 'var(--danger)', borderColor: 'var(--danger)' }}>{error}</div>}
      {success && <div className={styles.successBanner}>{success}</div>}

      <div className={styles.card}>
        <div className={styles.cardHeader}>
          <div className={styles.cardTitle}>
            <span className={styles.cardIcon}><Plug size={14} style={{ verticalAlign: '-2px' }} /></span>
            <span>{t('configEditor.mcp')}</span>
          </div>
          <div className={styles.cardActions}>
            <button
              type="button"
              className={styles.scanBtn}
              onClick={handleProbe}
              disabled={loading || probing || count === 0}
              title={t('configEditor.mcpProbe')}
            >
              {probing ? t('configEditor.mcpProbing') : t('configEditor.mcpProbe')}
            </button>
            <button
              type="button"
              className={styles.scanBtn}
              onClick={copyConfig}
              disabled={loading || !data?.config}
              title={t('configEditor.mcpCopy')}
            >
              {t('configEditor.mcpCopy')}
            </button>
            <button
              type="button"
              className={styles.editBtn}
              onClick={openEditor}
              disabled={loading}
            >
              {t('common.edit')}
            </button>
          </div>
        </div>

        <p className={styles.cardDesc}>{t('configEditor.mcpDesc')}</p>

        <div className={styles.dirGroups}>
          <div className={styles.dirGroup}>
            <span className={styles.dirLabel}>{t('configEditor.mcpConfigPath')}</span>
            <div className={styles.dirList}>
              {data?.path
                ? <code className={styles.dirItem}>{data.path}</code>
                : <span className={styles.dirEmpty}>{t('common.loading')}</span>
              }
            </div>
          </div>
          <div className={styles.dirGroup}>
            <span className={styles.dirLabel}>{t('configEditor.mcpServers')}</span>
            <div className={styles.dirList}>
              {count > 0
                ? <code className={styles.dirItem}>{t('configEditor.mcpServerCount', { count })}</code>
                : <span className={styles.dirEmpty}>{t('configEditor.mcpServerCountZero')}</span>
              }
            </div>
          </div>
        </div>

        {/* 探测结果 */}
        {statuses && statuses.length > 0 && (
          <div className={styles.scanResult}>
            <div className={styles.scanResultTitle}>
              {t('configEditor.mcpProbeDone')}（{statuses.length}）
            </div>
            <div className={styles.scanList}>
              {statuses.map((s) => {
                const isOpen = expanded.has(s.name)
                const hasTools = s.connected && s.tools && s.tools.length > 0
                return (
                  <div key={s.name} className={styles.serverRow}>
                    <div
                      className={styles.serverHeader}
                      onClick={() => hasTools && toggleExpand(s.name)}
                      role={hasTools ? 'button' : undefined}
                    >
                      <span className={`${styles.badge} ${s.connected ? styles.badgeConnected : styles.badgeFailed}`}>
                        {s.connected ? '✓ ' + t('configEditor.mcpConnected') : '✗ ' + t('configEditor.mcpFailed')}
                      </span>
                      <span className={`${styles.badge} ${styles.badgeType}`}>{s.type}</span>
                      <span className={styles.serverName}>{s.name}</span>
                      {s.server_info && (
                        <span className={styles.serverMeta}>{s.server_info}</span>
                      )}
                      {hasTools && (
                        <>
                          <span className={styles.serverMeta}>
                            {t('configEditor.mcpToolsCount', { count: s.tools.length })}
                          </span>
                          {isOpen
                            ? <ChevronDown size={14} style={{ flexShrink: 0, color: 'var(--text-muted)' }} />
                            : <ChevronRight size={14} style={{ flexShrink: 0, color: 'var(--text-muted)' }} />
                          }
                        </>
                      )}
                    </div>

                    {s.error && (
                      <div className={styles.serverError}>
                        {t('configEditor.mcpServerError')}: {s.error}
                      </div>
                    )}

                    {s.connected && (!s.tools || s.tools.length === 0) && (
                      <div className={styles.serverError} style={{ color: 'var(--text-muted)' }}>
                        {t('configEditor.mcpNoTools')}
                      </div>
                    )}

                    {isOpen && hasTools && (
                      <div className={styles.toolList}>
                        {s.tools.map((tool) => (
                          <div key={tool.name} className={styles.toolItem}>
                            <div className={styles.toolName}>{tool.title || tool.name}</div>
                            {tool.description && (
                              <div className={styles.toolDesc}>{tool.description}</div>
                            )}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>

      {/* MCP 配置编辑弹窗 */}
      {editing && (
        <div className={styles.overlay} onClick={closeEditor}>
          <div className={styles.dialog} onClick={(e) => e.stopPropagation()} style={{ width: '640px' }}>
            <div className={styles.dialogHeader}>
              <h3 className={styles.dialogTitle}>
                <Plug size={16} style={{ verticalAlign: '-3px', marginRight: 6 }} />
                {t('common.edit')} {t('configEditor.mcp')}
              </h3>
              <button type="button" className={styles.closeBtn} onClick={closeEditor} disabled={saving}>
                <X size={16} />
              </button>
            </div>

            <div className={styles.dialogBody}>
              <p className={styles.cardDesc}>{t('configEditor.mcpEditorHint')}</p>
              <textarea
                style={{
                  width: '100%',
                  minHeight: '320px',
                  padding: '12px',
                  border: '1px solid var(--border)',
                  borderRadius: '6px',
                  fontSize: '13px',
                  fontFamily: 'var(--font-mono)',
                  background: 'var(--bg-input)',
                  color: 'var(--text-primary)',
                  resize: 'vertical',
                }}
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                placeholder={defaultPlaceholder()}
                spellCheck={false}
                disabled={saving}
              />
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
    </>
  )
}

function defaultPlaceholder(): string {
  return JSON.stringify({
    mcpServers: {
      example: {
        command: 'npx',
        args: ['-y', '@some/mcp-server'],
      },
    },
  }, null, 2)
}
