import { useState, useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { FileText, X } from 'lucide-react'
import { getRawConfig, validateRawConfig, updateRawConfig } from '../api/config'
import type { RawConfigResponse } from '../api/config'
import styles from './ConfigEditor.module.css'

export default function RawConfigCard() {
  const { t } = useTranslation()
  const [data, setData] = useState<RawConfigResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState<'saving' | 'validating' | null>(null)

  useEffect(() => {
    load()
  }, [])

  async function load() {
    setLoading(true)
    setError('')
    try {
      const resp = await getRawConfig()
      setData(resp.data)
    } catch (err) {
      setError(err instanceof Error ? err.message : t('settings.loadFailed'))
    } finally {
      setLoading(false)
    }
  }

  function openEditor() {
    if (!data) return
    setDraft(data.content || '')
    setEditing(true)
    setSuccess('')
    setError('')
  }

  function closeEditor() {
    if (busy) return
    setEditing(false)
  }

  async function handleValidate() {
    setBusy('validating')
    setError('')
    setSuccess('')
    try {
      await validateRawConfig(draft)
      setSuccess(t('configEditor.rawValid'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setBusy(null)
    }
  }

  async function handleSave() {
    setBusy('saving')
    setError('')
    setSuccess('')
    try {
      const resp = await updateRawConfig(draft)
      setData({ content: draft, path: resp.data.path })
      setEditing(false)
      setSuccess(t('configEditor.rawSaved'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setBusy(null)
    }
  }

  return (
    <>
      {error && (
        <div className={styles.successBanner} style={{ background: 'var(--danger-bg)', color: 'var(--danger)', borderColor: 'var(--danger)' }}>
          {error}
        </div>
      )}
      {success && <div className={styles.successBanner}>{success}</div>}

      <div className={styles.card}>
        <div className={styles.cardHeader}>
          <div className={styles.cardTitle}>
            <span className={styles.cardIcon}><FileText size={14} style={{ verticalAlign: '-2px' }} /></span>
            <span>{t('configEditor.rawConfig')}</span>
          </div>
          <div className={styles.cardActions}>
            <button type="button" className={styles.editBtn} onClick={openEditor} disabled={loading || !data}>
              {t('common.edit')}
            </button>
          </div>
        </div>

        <p className={styles.cardDesc}>{t('configEditor.rawConfigDesc')}</p>

        <div className={styles.dirGroups}>
          <div className={styles.dirGroup}>
            <span className={styles.dirLabel}>{t('configEditor.mcpConfigPath')}</span>
            <div className={styles.dirList}>
              {data?.path
                ? <code className={styles.dirItem}>{data.path}</code>
                : <span className={styles.dirEmpty}>{t('common.loading')}</span>}
            </div>
          </div>
        </div>
      </div>

      {editing && (
        <div className={styles.overlay} onClick={closeEditor}>
          <div className={styles.dialog} onClick={(e) => e.stopPropagation()} style={{ width: '720px' }}>
            <div className={styles.dialogHeader}>
              <h3 className={styles.dialogTitle}>
                <FileText size={16} style={{ verticalAlign: '-3px', marginRight: 6 }} />
                {t('common.edit')} {t('configEditor.rawConfig')}
              </h3>
              <button type="button" className={styles.closeBtn} onClick={closeEditor} disabled={!!busy}>
                <X size={16} />
              </button>
            </div>

            <div className={styles.dialogBody}>
              <p className={styles.cardDesc}>{t('configEditor.rawEditorHint')}</p>
              <textarea
                style={{
                  width: '100%',
                  minHeight: '420px',
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
                spellCheck={false}
                disabled={!!busy}
              />
            </div>

            <div className={styles.dialogFooter}>
              <button type="button" className={styles.cancelBtn} onClick={closeEditor} disabled={!!busy}>
                {t('common.cancel')}
              </button>
              <button type="button" className={styles.scanBtn} onClick={handleValidate} disabled={!!busy}>
                {busy === 'validating' ? t('configEditor.rawValidating') : t('configEditor.rawValidate')}
              </button>
              <button type="button" className={styles.saveBtn} onClick={handleSave} disabled={!!busy}>
                {busy === 'saving' ? t('common.saving') : t('common.save')}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
