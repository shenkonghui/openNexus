import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, Copy, ExternalLink } from 'lucide-react'
import { getTunnel, updateTunnelConfig, startTunnel, stopTunnel } from '../api/tunnel'
import type { TunnelStatus } from '../types'
import styles from './SettingsDialog.module.css'

/**
 * 公网隧道配置卡（设置 → 公网访问）：cloudflared / ngrok / VS Code 隧道的启停与配置。
 * 配置写回 config.yaml 的 tunnel 段；启停实时作用于后端 TunnelService。
 */
type TunnelMode = 'quick' | 'token' | 'ngrok' | 'vscode'

export default function TunnelCard() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<TunnelStatus | null>(null)
  const [enabled, setEnabled] = useState(false)
  const [mode, setMode] = useState<TunnelMode>('quick')
  const [token, setToken] = useState('')
  const [hostname, setHostname] = useState('')
  const [provider, setProvider] = useState<'github' | 'microsoft'>('github')
  const [binPath, setBinPath] = useState('')
  const [ngrokPath, setNgrokPath] = useState('')
  const [vscodePath, setVscodePath] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => { load() }, [])

  // starting 状态轮询，等待隧道就绪/失败
  useEffect(() => {
    if (status?.state !== 'starting') return
    const timer = setInterval(() => {
      getTunnel().then((r) => setStatus(r.data)).catch(() => {})
    }, 2000)
    return () => clearInterval(timer)
  }, [status?.state])

  async function load() {
    try {
      const r = await getTunnel()
      setStatus(r.data)
      setEnabled(r.data.enabled)
      setMode(r.data.mode === 'token' || r.data.mode === 'ngrok' || r.data.mode === 'vscode' ? r.data.mode : 'quick')
      setHostname(r.data.hostname || '')
      setProvider(r.data.provider === 'microsoft' ? 'microsoft' : 'github')
      setBinPath(r.data.cloudflared_path || '')
      setNgrokPath(r.data.ngrok_path || '')
      setVscodePath(r.data.vscode_path || '')
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    }
  }

  async function handleSave() {
    setSaving(true); setError(''); setSaved(false)
    try {
      await updateTunnelConfig({
        enabled,
        mode,
        // token 留空=保留现有值；has_token 时给提示，未配置时为空即提交空
        token: token === '' ? undefined : token,
        hostname: hostname.trim(),
        provider,
        cloudflared_path: binPath.trim(),
        ngrok_path: ngrokPath.trim(),
        vscode_path: vscodePath.trim(),
      })
      setToken('')
      setSaved(true)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSaving(false)
    }
  }

  async function handleToggleRunning() {
    if (busy) return
    setBusy(true); setError('')
    try {
      const r = status?.state === 'running' || status?.state === 'starting'
        ? await stopTunnel()
        : await startTunnel()
      setStatus(r.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setBusy(false)
    }
  }

  async function copyUrl() {
    if (!status?.url) return
    try { await navigator.clipboard.writeText(status.url) } catch { /* 剪贴板不可用忽略 */ }
  }

  async function copyDeviceCode() {
    if (!status?.device_code) return
    try { await navigator.clipboard.writeText(status.device_code) } catch { /* 剪贴板不可用忽略 */ }
  }

  const running = status?.state === 'running'
  const starting = status?.state === 'starting'
  // vscode 模式等待设备授权可能较久，允许用户在启动中取消
  const vscodeStarting = starting && status?.mode === 'vscode'

  return (
    <div className={styles.defaultSection}>
      <label className={styles.label}>
        <Globe size={14} style={{ marginRight: 6, verticalAlign: '-2px' }} />
        {t('system.tunnelTitle')}
      </label>
      <p className={styles.hint}>{t('system.tunnelHint')}</p>

      {status && !status.installed && (
        <p className={`${styles.sectionHint} ${styles.errorText}`}>
          {mode === 'ngrok' ? t('system.tunnelNgrokNotInstalled')
            : mode === 'vscode' ? t('system.tunnelVSCodeNotInstalled')
            : t('system.tunnelNotInstalled')}
        </p>
      )}
      {status?.state === 'error' && status.error && (
        <p className={`${styles.sectionHint} ${styles.errorText}`} style={{ whiteSpace: 'pre-wrap' }}>{status.error}</p>
      )}

      <div className={styles.inlineRow}>
        <button type="button" className={styles.saveNoteBtn}
          onClick={handleToggleRunning}
          disabled={busy || (starting && !vscodeStarting) || (status !== null && !status.installed)}
        >
          {starting && !vscodeStarting ? t('system.tunnelStarting')
            : running || starting ? t('system.tunnelStop') : t('system.tunnelStart')}
        </button>
        {status && (
          <span className={styles.sectionHint}>
            {running ? t('system.tunnelRunning')
              : starting ? t('system.tunnelStarting')
              : status.state === 'error' ? t('system.tunnelError')
              : t('system.tunnelStopped')}
          </span>
        )}
      </div>

      {vscodeStarting && status.login_url && (
        <div className={styles.inlineRow} style={{ flexWrap: 'wrap' }}>
          <span className={styles.sectionHint}>{t('system.tunnelDeviceAuth')}</span>
          <code className={styles.sectionHint} style={{ wordBreak: 'break-all' }}>{status.login_url}</code>
          <button type="button" className={styles.secondaryBtn} onClick={() => window.open(status.login_url, '_blank')} title={t('sidebar.tunnelOpen')}>
            <ExternalLink size={12} />
          </button>
          {status.device_code && (
            <>
              <code className={styles.sectionHint}>{status.device_code}</code>
              <button type="button" className={styles.secondaryBtn} onClick={copyDeviceCode} title={t('sidebar.tunnelCopy')}>
                <Copy size={12} />
              </button>
            </>
          )}
        </div>
      )}

      {running && status.url && (
        <div className={styles.inlineRow}>
          <code className={styles.sectionHint} style={{ wordBreak: 'break-all' }}>{status.url}</code>
          <button type="button" className={styles.secondaryBtn} onClick={copyUrl} title={t('sidebar.tunnelCopy')}>
            <Copy size={12} />
          </button>
          <button type="button" className={styles.secondaryBtn} onClick={() => window.open(status.url, '_blank')} title={t('sidebar.tunnelOpen')}>
            <ExternalLink size={12} />
          </button>
        </div>
      )}

      <div style={{ marginTop: 12 }}>
        <label className={styles.label}>
          <input type="checkbox" checked={enabled}
            onChange={(e) => { setEnabled(e.target.checked); setSaved(false) }}
            style={{ marginRight: 8, verticalAlign: 'middle' }}
          />
          {t('system.tunnelAutostart')}
        </label>
      </div>

      <div style={{ marginTop: 8 }}>
        <label className={styles.label}>{t('system.tunnelMode')}</label>
        <select className={styles.input} value={mode}
          onChange={(e) => {
            const v = e.target.value
            setMode(v === 'token' || v === 'ngrok' || v === 'vscode' ? v : 'quick')
            setSaved(false)
          }}
        >
          <option value="quick">{t('system.tunnelModeQuick')}</option>
          <option value="token">{t('system.tunnelModeToken')}</option>
          <option value="ngrok">{t('system.tunnelModeNgrok')}</option>
          <option value="vscode">{t('system.tunnelModeVSCode')}</option>
        </select>
        <p className={styles.sectionHint}>
          {mode === 'token' ? t('system.tunnelModeTokenHint')
            : mode === 'ngrok' ? t('system.tunnelModeNgrokHint')
            : mode === 'vscode' ? t('system.tunnelModeVSCodeHint')
            : t('system.tunnelModeQuickHint')}
        </p>
      </div>

      {mode === 'vscode' && (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>{t('system.tunnelProvider')}</label>
          <select className={styles.input} value={provider}
            onChange={(e) => { setProvider(e.target.value === 'microsoft' ? 'microsoft' : 'github'); setSaved(false) }}
          >
            <option value="github">GitHub</option>
            <option value="microsoft">Microsoft</option>
          </select>
          <p className={styles.sectionHint}>{t('system.tunnelProviderHint')}</p>
        </div>
      )}

      {(mode === 'token' || mode === 'ngrok') && (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>
            {mode === 'ngrok' ? t('system.tunnelAuthtoken') : t('system.tunnelToken')}
          </label>
          <input type="password" className={styles.input} value={token}
            onChange={(e) => { setToken(e.target.value); setSaved(false) }}
            placeholder={status?.has_token ? t('system.tunnelTokenKeep') : 'eyJhIjoi...'}
            spellCheck={false}
          />
          {mode === 'ngrok' && <p className={styles.sectionHint}>{t('system.tunnelAuthtokenHint')}</p>}
        </div>
      )}

      {mode !== 'quick' && (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>
            {mode === 'vscode' ? t('system.tunnelVSCodeName') : t('system.tunnelHostname')}
          </label>
          <input className={styles.input} value={hostname}
            onChange={(e) => { setHostname(e.target.value); setSaved(false) }}
            placeholder={mode === 'ngrok' ? 'nexus.ngrok-free.app' : mode === 'vscode' ? 'my-machine' : 'https://nexus.example.com'}
            spellCheck={false}
          />
          <p className={styles.sectionHint}>
            {mode === 'ngrok' ? t('system.tunnelNgrokHostnameHint')
              : mode === 'vscode' ? t('system.tunnelVSCodeNameHint')
              : t('system.tunnelHostnameHint')}
          </p>
        </div>
      )}

      {mode === 'ngrok' ? (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>{t('system.tunnelNgrokBinPath')}</label>
          <input className={styles.input} value={ngrokPath}
            onChange={(e) => { setNgrokPath(e.target.value); setSaved(false) }}
            placeholder={status?.bin_path || '/opt/homebrew/bin/ngrok'}
            spellCheck={false}
          />
          <p className={styles.sectionHint}>{t('system.tunnelNgrokBinPathHint')}</p>
        </div>
      ) : mode === 'vscode' ? (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>{t('system.tunnelVSCodeBinPath')}</label>
          <input className={styles.input} value={vscodePath}
            onChange={(e) => { setVscodePath(e.target.value); setSaved(false) }}
            placeholder={status?.bin_path || '/usr/local/bin/code'}
            spellCheck={false}
          />
          <p className={styles.sectionHint}>{t('system.tunnelVSCodeBinPathHint')}</p>
        </div>
      ) : (
        <div style={{ marginTop: 8 }}>
          <label className={styles.label}>{t('system.tunnelBinPath')}</label>
          <input className={styles.input} value={binPath}
            onChange={(e) => { setBinPath(e.target.value); setSaved(false) }}
            placeholder={status?.bin_path || '/opt/homebrew/bin/cloudflared'}
            spellCheck={false}
          />
          <p className={styles.sectionHint}>{t('system.tunnelBinPathHint')}</p>
        </div>
      )}

      <div className={styles.inlineRow} style={{ marginTop: 8 }}>
        <button type="button" className={styles.saveNoteBtn} onClick={handleSave} disabled={saving}>
          {saving ? t('common.saving') : t('common.save')}
        </button>
        {saved && <span className={styles.savedHint}>{t('settings.taskSettingsSaved')}</span>}
      </div>
      {error && <p className={`${styles.sectionHint} ${styles.errorText}`}>{error}</p>}
    </div>
  )
}
