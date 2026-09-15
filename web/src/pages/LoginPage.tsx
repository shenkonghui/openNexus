import { useState, useEffect, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../hooks/useAuth'
import { registrationStatus } from '../api/auth'
import LoadingSpinner from '../components/LoadingSpinner'
import styles from './LoginPage.module.css'

export default function LoginPage() {
  const { t } = useTranslation()
  const { user, loading, login, register } = useAuth()
  const navigate = useNavigate()

  useEffect(() => {
    if (!loading && user) {
      navigate('/', { replace: true })
    }
  }, [user, loading, navigate])

  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [canRegister, setCanRegister] = useState(true)
  const [account, setAccount] = useState('')
  const [username, setUsername] = useState('')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    let cancelled = false
    registrationStatus()
      .then(enabled => {
        if (!cancelled) setCanRegister(enabled)
      })
      .catch(() => {
        // 查询失败按开放处理，交由后端注册接口兜底拦截
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (loading || user) {
    return <LoadingSpinner text={t('common.loading')} />
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setSubmitting(true)

    try {
      if (mode === 'login') {
        await login(account, password)
      } else {
        await register(username, email, password)
      }
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <div className={styles.container}>
      <div className={styles.card}>
        <div className={styles.brand}>
          <h1 className={styles.title}>openNexus</h1>
          <p className={styles.subtitle}>{t('auth.subtitle')}</p>
        </div>

        <div className={styles.tabs}>
          <button
            className={`${styles.tab} ${mode === 'login' ? styles.tabActive : ''}`}
            onClick={() => setMode('login')}
            type="button"
          >
            {t('auth.login')}
          </button>
          {canRegister && (
            <button
              className={`${styles.tab} ${mode === 'register' ? styles.tabActive : ''}`}
              onClick={() => setMode('register')}
              type="button"
            >
              {t('auth.register')}
            </button>
          )}
        </div>

        <form className={styles.form} onSubmit={handleSubmit}>
          {mode === 'login' ? (
            <div className={styles.field}>
              <label className={styles.label}>{t('auth.accountLabel')}</label>
              <input
                className={styles.input}
                type="text"
                value={account}
                onChange={(e) => setAccount(e.target.value)}
                required
                placeholder={t('auth.accountPlaceholder')}
              />
            </div>
          ) : (
            <>
              <div className={styles.field}>
                <label className={styles.label}>{t('auth.username')}</label>
                <input
                  className={styles.input}
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  required
                  placeholder={t('auth.usernamePlaceholder')}
                />
              </div>
              <div className={styles.field}>
                <label className={styles.label}>Email</label>
                <input
                  className={styles.input}
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                  placeholder="Email"
                />
              </div>
            </>
          )}

          <div className={styles.field}>
            <label className={styles.label}>{t('auth.password')}</label>
            <input
              className={styles.input}
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              placeholder={t('auth.passwordPlaceholder')}
            />
          </div>

          {error && <div className={styles.error}>{error}</div>}

          <button className={styles.submitBtn} type="submit" disabled={submitting}>
            {submitting ? t('common.saving') : mode === 'login' ? t('auth.loginBtn') : t('auth.registerBtn')}
          </button>
        </form>

        {/* 静态访问令牌提示：管理员配置 auth.static_token 后，带 ?token= 的链接可免密登录 */}
        {mode === 'login' && <p className={styles.tokenHint}>{t('auth.tokenHint')}</p>}
      </div>
    </div>
  )
}
