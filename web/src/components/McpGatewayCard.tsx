import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import {
  getMCPGatewayStatus,
  setMCPGatewayEnabled,
  disableGatewayUpstream,
  enableGatewayUpstream,
  addGatewayCustomServer,
  removeGatewayCustomServer,
} from '../api/config'
import type { MCPGatewayStatus, MCPGatewayUpstream, MCPGatewaySkipped } from '../api/config'
import { Network, Plus, Trash2, Power } from 'lucide-react'
import styles from './ConfigEditor.module.css'

// GATEWAY_NAME 与后端 acp.GatewayMCPName 保持一致。
const GATEWAY_NAME = 'opennexus-gateway'

// 通用 mcpServers JSON 片段：Claude Code / Gemini / Qwen / iFlow / Cursor 等均使用该格式。
function buildJsonSnippet(endpoint: string, token: string): string {
  return JSON.stringify({
    mcpServers: {
      [GATEWAY_NAME]: {
        type: 'http',
        url: endpoint,
        headers: { Authorization: `Bearer ${token}` },
      },
    },
  }, null, 2)
}

// Codex 使用 TOML，且 HTTP 传输的 header 走 http_headers。
function buildTomlSnippet(endpoint: string, token: string): string {
  return [
    `[mcp_servers.${GATEWAY_NAME}]`,
    `url = "${endpoint}"`,
    `http_headers = { "Authorization" = "Bearer ${token}" }`,
  ].join('\n')
}

export default function McpGatewayCard() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<MCPGatewayStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [toggling, setToggling] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  useEffect(() => { load() }, [])

  async function load() {
    setLoading(true)
    setError('')
    try {
      const resp = await getMCPGatewayStatus()
      setStatus(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  async function toggle() {
    if (!status) return
    setToggling(true)
    setError('')
    setSuccess('')
    try {
      const resp = await setMCPGatewayEnabled(!status.enabled)
      setStatus(resp.data)
      setSuccess(resp.data.enabled ? t('configEditor.gatewayEnabled') : t('configEditor.gatewayDisabled'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setToggling(false)
    }
  }

  async function copyText(text: string) {
    try {
      await navigator.clipboard.writeText(text)
      setSuccess(t('configEditor.gatewayCopied'))
    } catch {
      setError(t('settings.noteMcpCopyFailed'))
    }
  }

  // 禁用/启用上游后刷新状态
  async function refreshAfterUpstreamChange(msg: string) {
    setSuccess(msg)
    await load()
  }

  const upstreams = status?.upstreams ?? []
  const skipped = status?.skipped ?? []
  const ready = !!status?.endpoint && !!status?.token

  return (
    <>
      {error && <div className={styles.successBanner} style={{ background: 'var(--danger-bg)', color: 'var(--danger)', borderColor: 'var(--danger)' }}>{error}</div>}
      {success && <div className={styles.successBanner}>{success}</div>}

      <div className={styles.card}>
        <CardHeader
          enabled={!!status?.enabled}
          busy={loading || toggling}
          loading={loading}
          canEnable={ready}
          onRefresh={load}
          onToggle={toggle}
        />

        <p className={styles.cardDesc}>{t('configEditor.gatewayDesc')}</p>

        <Summary status={status} upstreamCount={upstreams.length} />

        {!ready && (
          <div className={styles.serverError} style={{ marginTop: 10 }}>{t('configEditor.gatewayNoToken')}</div>
        )}

        <UpstreamList
          upstreams={upstreams}
          onDisable={async (name) => {
            try { await disableGatewayUpstream(name); await refreshAfterUpstreamChange(t('configEditor.gatewayUpstreamDisabled', { name })) }
            catch (e) { setError(e instanceof Error ? e.message : t('common.failed')) }
          }}
          onRemoveCustom={async (name) => {
            try { await removeGatewayCustomServer(name); await refreshAfterUpstreamChange(t('configEditor.gatewayCustomRemoved', { name })) }
            catch (e) { setError(e instanceof Error ? e.message : t('common.failed')) }
          }}
        />

        <AddCustomServerForm
          onAdd={async (name, entry) => {
            try { await addGatewayCustomServer(name, entry); await refreshAfterUpstreamChange(t('configEditor.gatewayCustomAdded', { name })) }
            catch (e) { setError(e instanceof Error ? e.message : t('common.failed')) }
          }}
        />

        <SkippedList
          skipped={skipped}
          onEnable={async (name) => {
            try { await enableGatewayUpstream(name); await refreshAfterUpstreamChange(t('configEditor.gatewayUpstreamEnabled', { name })) }
            catch (e) { setError(e instanceof Error ? e.message : t('common.failed')) }
          }}
        />
        {ready && <SnippetSection endpoint={status!.endpoint} token={status!.token} onCopy={copyText} />}
      </div>
    </>
  )
}

interface CardHeaderProps {
  enabled: boolean
  busy: boolean
  loading: boolean
  canEnable: boolean
  onRefresh: () => void
  onToggle: () => void
}

// CardHeader 是标题栏：刷新 + 启用/停用。未生成 token 时禁止启用。
function CardHeader({ enabled, busy, loading, canEnable, onRefresh, onToggle }: CardHeaderProps) {
  const { t } = useTranslation()
  return (
    <div className={styles.cardHeader}>
      <div className={styles.cardTitle}>
        <span className={styles.cardIcon}><Network size={14} style={{ verticalAlign: '-2px' }} /></span>
        <span>{t('configEditor.gateway')}</span>
      </div>
      <div className={styles.cardActions}>
        <button type="button" className={styles.scanBtn} onClick={onRefresh} disabled={busy}>
          {loading ? t('common.loading') : t('configEditor.gatewayRefresh')}
        </button>
        <button
          type="button"
          className={styles.editBtn}
          onClick={onToggle}
          disabled={busy || (!enabled && !canEnable)}
          title={!canEnable ? t('configEditor.gatewayNoToken') : undefined}
        >
          {enabled ? t('configEditor.gatewayDisable') : t('configEditor.gatewayEnable')}
        </button>
      </div>
    </div>
  )
}

// Summary 是概览行：启用状态 / endpoint / 已聚合的工具与上游数。
function Summary({ status, upstreamCount }: { status: MCPGatewayStatus | null; upstreamCount: number }) {
  const { t } = useTranslation()
  return (
    <div className={styles.dirGroups}>
      <div className={styles.dirGroup}>
        <span className={styles.dirLabel}>{t('configEditor.gatewayState')}</span>
        <div className={styles.dirList}>
          <span className={`${styles.badge} ${status?.enabled ? styles.badgeConnected : styles.badgeFailed}`}>
            {status?.enabled ? t('configEditor.gatewayOn') : t('configEditor.gatewayOff')}
          </span>
        </div>
      </div>
      <div className={styles.dirGroup}>
        <span className={styles.dirLabel}>{t('configEditor.gatewayEndpoint')}</span>
        <div className={styles.dirList}>
          {status?.endpoint
            ? <code className={styles.dirItem}>{status.endpoint}</code>
            : <span className={styles.dirEmpty}>{t('configEditor.gatewayNoEndpoint')}</span>
          }
        </div>
      </div>
      <div className={styles.dirGroup}>
        <span className={styles.dirLabel}>{t('configEditor.gatewayTools')}</span>
        <div className={styles.dirList}>
          <code className={styles.dirItem}>
            {t('configEditor.gatewayToolSummary', { tools: status?.tool_count ?? 0, servers: upstreamCount })}
          </code>
        </div>
      </div>
    </div>
  )
}

interface UpstreamListProps {
  upstreams: MCPGatewayUpstream[]
  onDisable: (name: string) => void
  onRemoveCustom: (name: string) => void
}

// UpstreamList 展示各上游的连接状态与工具数，支持禁用/移除自定义。
function UpstreamList({ upstreams, onDisable, onRemoveCustom }: UpstreamListProps) {
  const { t } = useTranslation()
  if (upstreams.length === 0) return null
  return (
    <div className={styles.scanResult}>
      <div className={styles.scanResultTitle}>{t('configEditor.gatewayUpstreams')}（{upstreams.length}）</div>
      <div className={styles.scanList}>
        {upstreams.map((u) => (
          <div key={u.name} className={styles.serverRow}>
            <div className={styles.serverHeader}>
              <span className={`${styles.badge} ${u.connected ? styles.badgeConnected : styles.badgeFailed}`}>
                {u.connected ? '✓ ' + t('configEditor.mcpConnected') : '✗ ' + t('configEditor.mcpFailed')}
              </span>
              <span className={`${styles.badge} ${styles.badgeType}`}>{u.type}</span>
              {u.source === 'custom' && (
                <span className={`${styles.badge} ${styles.badgeType}`} style={{ background: 'var(--accent-bg)', color: 'var(--accent)' }}>
                  {t('configEditor.gatewaySourceCustom')}
                </span>
              )}
              <span className={styles.serverName}>{u.name}</span>
              <span className={styles.serverMeta}>{t('configEditor.mcpToolsCount', { count: u.tool_count })}</span>
              <div className={styles.cardActions} style={{ marginLeft: 'auto' }}>
                <button
                  type="button"
                  className={styles.scanBtn}
                  title={t('configEditor.gatewayDisableUpstream')}
                  onClick={() => onDisable(u.name)}
                >
                  <Power size={12} style={{ verticalAlign: '-2px', marginRight: 2 }} />
                  {t('configEditor.gatewayDisableUpstream')}
                </button>
                {u.source === 'custom' && (
                  <button
                    type="button"
                    className={styles.scanBtn}
                    title={t('configEditor.gatewayRemoveCustom')}
                    onClick={() => onRemoveCustom(u.name)}
                  >
                    <Trash2 size={12} style={{ verticalAlign: '-2px', marginRight: 2 }} />
                    {t('common.delete')}
                  </button>
                )}
              </div>
            </div>
            {u.error && <div className={styles.serverError}>{t('configEditor.mcpServerError')}: {u.error}</div>}
          </div>
        ))}
      </div>
    </div>
  )
}

interface AddCustomServerFormProps {
  onAdd: (name: string, entry: { type: string; url: string; headers?: Record<string, string> }) => void
}

// AddCustomServerForm 添加自定义上游的表单（不写入 mcp.json）。
// 支持可选 Bearer token 与自定义 headers（很多外部托管 MCP server 需要鉴权）。
function AddCustomServerForm({ onAdd }: AddCustomServerFormProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [type, setType] = useState('http')
  const [bearer, setBearer] = useState('')
  const [extraHeaders, setExtraHeaders] = useState<{ key: string; value: string }[]>([])

  function submit(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim() || !url.trim()) return
    const headers: Record<string, string> = {}
    if (bearer.trim()) {
      headers['Authorization'] = `Bearer ${bearer.trim()}`
    }
    for (const h of extraHeaders) {
      const k = h.key.trim()
      if (k) headers[k] = h.value
    }
    onAdd(name.trim(), { type, url: url.trim(), headers: Object.keys(headers).length ? headers : undefined })
    resetForm()
    setOpen(false)
  }

  function resetForm() {
    setName('')
    setUrl('')
    setType('http')
    setBearer('')
    setExtraHeaders([])
  }

  function addHeaderRow() {
    setExtraHeaders([...extraHeaders, { key: '', value: '' }])
  }

  function updateHeaderRow(idx: number, field: 'key' | 'value', val: string) {
    const next = [...extraHeaders]
    next[idx] = { ...next[idx], [field]: val }
    setExtraHeaders(next)
  }

  function removeHeaderRow(idx: number) {
    setExtraHeaders(extraHeaders.filter((_, i) => i !== idx))
  }

  if (!open) {
    return (
      <div className={styles.scanResult}>
        <button type="button" className={styles.scanBtn} onClick={() => setOpen(true)}>
          <Plus size={12} style={{ verticalAlign: '-2px', marginRight: 4 }} />
          {t('configEditor.gatewayAddCustom')}
        </button>
      </div>
    )
  }

  return (
    <div className={styles.scanResult}>
      <div className={styles.scanResultTitle}>{t('configEditor.gatewayAddCustomTitle')}</div>
      <p className={styles.cardDesc}>{t('configEditor.gatewayAddCustomHint')}</p>
      <form onSubmit={submit} className={styles.dirGroups}>
        <div className={styles.dirGroup}>
          <span className={styles.dirLabel}>{t('configEditor.gatewayCustomName')}</span>
          <input className={styles.input} value={name} onChange={(e) => setName(e.target.value)} placeholder="my-server" />
        </div>
        <div className={styles.dirGroup}>
          <span className={styles.dirLabel}>{t('configEditor.gatewayCustomType')}</span>
          <select className={styles.input} value={type} onChange={(e) => setType(e.target.value)}>
            <option value="http">http</option>
            <option value="sse">sse</option>
          </select>
        </div>
        <div className={styles.dirGroup}>
          <span className={styles.dirLabel}>{t('configEditor.gatewayCustomUrl')}</span>
          <input className={styles.input} value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://mcp.example.com/sse" />
        </div>
        <div className={styles.dirGroup}>
          <span className={styles.dirLabel}>{t('configEditor.gatewayCustomBearer')}</span>
          <input
            className={styles.input}
            type="password"
            value={bearer}
            onChange={(e) => setBearer(e.target.value)}
            placeholder={t('configEditor.gatewayCustomBearerPlaceholder')}
          />
        </div>
        {extraHeaders.length > 0 && (
          <div className={styles.dirGroup}>
            <span className={styles.dirLabel}>{t('configEditor.gatewayCustomHeaders')}</span>
            <div className={styles.dirList}>
              {extraHeaders.map((h, i) => (
                <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 4 }}>
                  <input className={styles.input} value={h.key} onChange={(e) => updateHeaderRow(i, 'key', e.target.value)} placeholder="X-API-Key" style={{ flex: 1 }} />
                  <input className={styles.input} value={h.value} onChange={(e) => updateHeaderRow(i, 'value', e.target.value)} placeholder="value" style={{ flex: 1 }} />
                  <button type="button" className={styles.scanBtn} onClick={() => removeHeaderRow(i)} title={t('common.delete')}>
                    <Trash2 size={12} />
                  </button>
                </div>
              ))}
            </div>
          </div>
        )}
        <div className={styles.cardActions}>
          <button type="button" className={styles.scanBtn} onClick={addHeaderRow}>
            <Plus size={12} style={{ verticalAlign: '-2px', marginRight: 4 }} />
            {t('configEditor.gatewayCustomAddHeader')}
          </button>
          <button type="submit" className={styles.editBtn} disabled={!name.trim() || !url.trim()}>
            {t('common.create')}
          </button>
          <button type="button" className={styles.scanBtn} onClick={() => { resetForm(); setOpen(false) }}>
            {t('common.cancel')}
          </button>
        </div>
      </form>
    </div>
  )
}

// SkippedList 展示未被网关接管的条目及原因（如 stdio 仍走会话直连，或被用户禁用）。
// 被用户禁用的条目（disabled=true）显示"启用"按钮。
function SkippedList({ skipped, onEnable }: { skipped: MCPGatewaySkipped[]; onEnable: (name: string) => void }) {
  const { t } = useTranslation()
  if (skipped.length === 0) return null
  return (
    <div className={styles.scanResult}>
      <div className={styles.scanResultTitle}>{t('configEditor.gatewaySkipped')}（{skipped.length}）</div>
      <div className={styles.scanList}>
        {skipped.map((s) => (
          <div key={s.name} className={styles.serverRow}>
            <div className={styles.serverHeader}>
              <span className={`${styles.badge} ${styles.badgeType}`}>{s.type}</span>
              <span className={styles.serverName}>{s.name}</span>
              {s.disabled && (
                <div className={styles.cardActions} style={{ marginLeft: 'auto' }}>
                  <button
                    type="button"
                    className={styles.scanBtn}
                    title={t('configEditor.gatewayEnableUpstream')}
                    onClick={() => onEnable(s.name)}
                  >
                    <Power size={12} style={{ verticalAlign: '-2px', marginRight: 2 }} />
                    {t('configEditor.gatewayEnableUpstream')}
                  </button>
                </div>
              )}
            </div>
            <div className={styles.serverError} style={{ color: 'var(--text-muted)' }}>{s.reason}</div>
          </div>
        ))}
      </div>
    </div>
  )
}

// SnippetSection 给出各 Agent 原生配置文件里该写什么（只需配置一次）。
function SnippetSection({ endpoint, token, onCopy }: { endpoint: string; token: string; onCopy: (text: string) => void }) {
  const { t } = useTranslation()
  return (
    <div className={styles.scanResult}>
      <div className={styles.scanResultTitle}>{t('configEditor.gatewaySnippets')}</div>
      <p className={styles.cardDesc}>{t('configEditor.gatewaySnippetsDesc')}</p>
      <Snippet
        label={t('configEditor.gatewaySnippetJson')}
        hint="~/.gemini/settings.json · ~/.qwen/settings.json · ~/.iflow/settings.json · .mcp.json · ~/.cursor/mcp.json"
        code={buildJsonSnippet(endpoint, token)}
        copyLabel={t('configEditor.mcpCopy')}
        onCopy={onCopy}
      />
      <Snippet
        label={t('configEditor.gatewaySnippetToml')}
        hint="~/.codex/config.toml"
        code={buildTomlSnippet(endpoint, token)}
        copyLabel={t('configEditor.mcpCopy')}
        onCopy={onCopy}
      />
    </div>
  )
}

interface SnippetProps {
  label: string
  hint: string
  code: string
  copyLabel: string
  onCopy: (text: string) => void
}

function Snippet({ label, hint, code, copyLabel, onCopy }: SnippetProps) {
  return (
    <div className={styles.snippetBlock}>
      <div className={styles.snippetHeader}>
        <span className={styles.snippetLabel}>{label}</span>
        <button type="button" className={styles.scanBtn} onClick={() => onCopy(code)}>{copyLabel}</button>
      </div>
      <div className={styles.snippetHint}>{hint}</div>
      <pre className={styles.snippetCode}>{code}</pre>
    </div>
  )
}
