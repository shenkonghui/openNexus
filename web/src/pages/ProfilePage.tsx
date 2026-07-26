import { useState, type FormEvent } from 'react'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useAuthContext } from '../context/AuthContext'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import { updateProfile, changePassword } from '../api/auth'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import ErrorBanner from '../components/ErrorBanner'
import LoadingSpinner from '../components/LoadingSpinner'
import styles from './ProfilePage.module.css'

export default function ProfilePage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { refreshUser } = useAuthContext()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

  const [profileForm, setProfileForm] = useState({ username: '', email: '' })
  const [passwordForm, setPasswordForm] = useState({
    old_password: '',
    new_password: '',
    confirm_password: '',
  })
  const [savingProfile, setSavingProfile] = useState(false)
  const [savingPassword, setSavingPassword] = useState(false)
  const [error, setError] = useState('')
  const [profileMsg, setProfileMsg] = useState('')
  const [passwordMsg, setPasswordMsg] = useState('')

  useEffect(() => {
    if (user) {
      setProfileForm({ username: user.username, email: user.email })
    }
  }, [user])

  async function handleProfileSubmit(e: FormEvent) {
    e.preventDefault()
    if (!profileForm.username || !profileForm.email) {
      setError(t('profile.usernameEmailRequired'))
      return
    }
    setSavingProfile(true)
    setError('')
    setProfileMsg('')
    try {
      await updateProfile(profileForm.username, profileForm.email)
      await refreshUser()
      setProfileMsg(t('profile.updateSuccess'))
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSavingProfile(false)
    }
  }

  async function handlePasswordSubmit(e: FormEvent) {
    e.preventDefault()
    if (passwordForm.new_password !== passwordForm.confirm_password) {
      setError(t('profile.passwordMismatch'))
      return
    }
    if (passwordForm.new_password.length < 8) {
      setError(t('profile.passwordRequirements'))
      return
    }
    setSavingPassword(true)
    setError('')
    setPasswordMsg('')
    try {
      await changePassword(passwordForm.old_password, passwordForm.new_password)
      setPasswordMsg(t('profile.passwordChanged'))
      setPasswordForm({ old_password: '', new_password: '', confirm_password: '' })
    } catch (err) {
      setError(err instanceof Error ? err.message : t('common.failed'))
    } finally {
      setSavingPassword(false)
    }
  }

  if (authLoading) return <LoadingSpinner text={t('common.loading')} />
  if (!user) return null

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <div className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <h1 className={styles.title}>{t('profile.title')}</h1>
          </div>
        </div>

        {error && <ErrorBanner message={error} onClose={() => setError('')} />}

        <div className={styles.content}>
          <section className={styles.section}>
            <h2 className={styles.sectionTitle}>{t('profile.basicInfo')}</h2>
            <div className={styles.infoGrid}>
              <div className={styles.infoItem}>
                <span className={styles.infoLabel}>{t('profile.userId')}</span>
                <span className={styles.infoValue}>{user.id}</span>
              </div>
              <div className={styles.infoItem}>
                <span className={styles.infoLabel}>{t('profile.role')}</span>
                <span className={styles.infoValue}>{user.role === 'admin' ? t('profile.admin') : t('profile.user')}</span>
              </div>
              <div className={styles.infoItem}>
                <span className={styles.infoLabel}>{t('profile.status')}</span>
                <span className={styles.infoValue}>{user.status === 'active' ? t('profile.active') : t('profile.disabled')}</span>
              </div>
            </div>
          </section>

          <section className={styles.section}>
            <h2 className={styles.sectionTitle}>{t('profile.editProfile')}</h2>
            {profileMsg && <p className={styles.successMsg}>{profileMsg}</p>}
            <form className={styles.form} onSubmit={handleProfileSubmit}>
              <div className={styles.field}>
                <label className={styles.label}>{t('profile.username')}</label>
                <input
                  className={styles.input}
                  type="text"
                  value={profileForm.username}
                  onChange={(e) => setProfileForm({ ...profileForm, username: e.target.value })}
                  required
                />
              </div>
              <div className={styles.field}>
                <label className={styles.label}>{t('profile.email')}</label>
                <input
                  className={styles.input}
                  type="email"
                  value={profileForm.email}
                  onChange={(e) => setProfileForm({ ...profileForm, email: e.target.value })}
                  required
                />
              </div>
              <button className={styles.submitBtn} type="submit" disabled={savingProfile}>
                {savingProfile ? t('common.saving') : t('common.save')}
              </button>
            </form>
          </section>

          <section className={styles.section}>
            <h2 className={styles.sectionTitle}>{t('profile.changePassword')}</h2>
            {passwordMsg && <p className={styles.successMsg}>{passwordMsg}</p>}
            <form className={styles.form} onSubmit={handlePasswordSubmit}>
              <div className={styles.field}>
                <label className={styles.label}>{t('profile.currentPassword')}</label>
                <input
                  className={styles.input}
                  type="password"
                  value={passwordForm.old_password}
                  onChange={(e) => setPasswordForm({ ...passwordForm, old_password: e.target.value })}
                  required
                  autoComplete="current-password"
                />
              </div>
              <div className={styles.field}>
                <label className={styles.label}>{t('profile.newPassword')}</label>
                <input
                  className={styles.input}
                  type="password"
                  value={passwordForm.new_password}
                  onChange={(e) => setPasswordForm({ ...passwordForm, new_password: e.target.value })}
                  required
                  autoComplete="new-password"
                  placeholder={t('profile.passwordHint')}
                />
              </div>
              <div className={styles.field}>
                <label className={styles.label}>{t('profile.confirmPassword')}</label>
                <input
                  className={styles.input}
                  type="password"
                  value={passwordForm.confirm_password}
                  onChange={(e) => setPasswordForm({ ...passwordForm, confirm_password: e.target.value })}
                  required
                  autoComplete="new-password"
                />
              </div>
              <button className={styles.submitBtn} type="submit" disabled={savingPassword}>
                {savingPassword ? t('common.saving') : t('profile.changePassword')}
              </button>
            </form>
          </section>
        </div>
      </div>
    </AppLayout>
  )
}
