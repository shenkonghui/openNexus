import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, Copy, ExternalLink } from 'lucide-react'
import { getTunnel, updateTunnelConfig, startTunnel, stopTunnel } from '../api/tunnel'
import type { TunnelStatus } from '../types'
import styles from './SettingsDialog.module.css'

/**
 * 公网隧道配置卡（设置 → 系统）：Cloudflare cloudflared 隧道的启停与配置。
 * 配置写回 config.yaml 的 tunnel 段；启停实时作用于后端 TunnelService。
 */
export default function TunnelCard() {
  const { t } = useTranslation()
  const [status, setStatus] = useState<TunnelStatus | null>(null)
  const [enabled, setEnabled] = useState(false)
  const [mode, setMode] = useState<'quick' | 'token'>('quick')
  const [token, setToken] = useState('')
  const [hostname, setHostname] = useState('')
  const [binPath, setBinPath] = useState('')
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
      setMode(r.data.mode === 'token' ? 'token' : 'quick')
      setHostname(r.data.hostname || '')
      setBinPath(r.data.cloudflared_path || '')
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
        cloudflared_path: binPath.trim(),
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

  const running = status?.state === 'running'
  const starting = status?.state === 'starting'

  return (
    <div className={styles.defaultSection}>
      <label className={styles.label}>
        <Globe size={14} style={{ marginRight: 6, verticalAlign: '-2px' }} />
        {t('system.tunnelTitle')}
      </label>
      <p className={styles.hint}>{t('system.tunnelHint')}</p>

      {status && !status.installed && (
        <p className={`${styles.sectionHint} ${styles.errorText}`}>{t('system.tunnelNotInstalled')}</p>
      )}
      {status?.state === 'error' && status.error && (
        <p className={`${styles.sectionHint} ${styles.errorText}`} style={{ whiteSpace: 'pre-wrap' }}>{status.error}</p>
      )}

      <div className={styles.inlineRow}>
        <button type="button" className={styles.saveNoteBtn}
          onClick={handleToggleRunning}
          disabled={busy || starting || (status !== null && !status.installed)}
        >
          {starting ? t('system.tunnelStarting')
            : running ? t('system.tunnelStop') : t('system.tunnelStart')}
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
          onChange={(e) => { setMode(e.target.value === 'token' ? 'token' : 'quick'); setSaved(false) }}
        >
          <option value="quick">{t('system.tunnelModeQuick')}</option>
          <option value="token">{t('system.tunnelModeToken')}</option>
        </select>
        <p className={styles.sectionHint}>
          {mode === 'token' ? t('system.tunnelModeTokenHint') : t('system.tunnelModeQuickHint')}
        </p>
      </div>

      {mode === 'token' && (
        <>
          <div style={{ marginTop: 8 }}>
            <label className={styles.label}>{t('system.tunnelToken')}</label>
            <input type="password" className={styles.input} value={token}
              onChange={(e) => { setToken(e.target.value); setSaved(false) }}
              placeholder={status?.has_token ? t('system.tunnelTokenKeep') : 'eyJhIjoi...'}
              spellCheck={false}
            />
          </div>
          <div style={{ marginTop: 8 }}>
            <label className={styles.label}>{t('system.tunnelHostname')}</label>
            <input className={styles.input} value={hostname}
              onChange={(e) => { setHostname(e.target.value); setSaved(false) }}
              placeholder="https://nexus.example.com"
              spellCheck={false}
            />
            <p className={styles.sectionHint}>{t('system.tunnelHostnameHint')}</p>
          </div>
        </>
      )}

      <div style={{ marginTop: 8 }}>
        <label className={styles.label}>{t('system.tunnelBinPath')}</label>
        <input className={styles.input} value={binPath}
          onChange={(e) => { setBinPath(e.target.value); setSaved(false) }}
          placeholder={status?.bin_path || '/opt/homebrew/bin/cloudflared'}
          spellCheck={false}
        />
        <p className={styles.sectionHint}>{t('system.tunnelBinPathHint')}</p>
      </div>

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
