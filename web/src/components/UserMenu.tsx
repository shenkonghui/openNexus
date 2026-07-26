import { useState, useRef, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuthContext } from '../context/AuthContext'
import { useTheme } from '../context/ThemeContext'
import { ChevronUp, ChevronDown, User, Sun, Moon, Languages, LogOut } from 'lucide-react'
import styles from './UserMenu.module.css'

export default function UserMenu({ variant = 'header' }: { variant?: 'header' | 'sidebar' }) {
  const { t, i18n } = useTranslation()
  const { user, logout } = useAuthContext()
  const { theme, toggleTheme } = useTheme()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function handleClick(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClick)
    return () => document.removeEventListener('mousedown', handleClick)
  }, [])

  if (!user) return null

  async function handleLogout() {
    setOpen(false)
    await logout()
    navigate('/login')
  }

  function handleProfile() {
    setOpen(false)
    navigate('/profile')
  }

  function handleToggleTheme() {
    toggleTheme()
  }

  // 中英文切换：在 zh / en 间切换并持久化
  function handleToggleLanguage() {
    const next = i18n.language.startsWith('zh') ? 'en' : 'zh'
    localStorage.setItem('opennexus-lang', next)
    i18n.changeLanguage(next)
  }

  return (
    <div className={`${styles.container} ${variant === 'sidebar' ? styles.containerSidebar : ''}`} ref={ref}>
      <button
        type="button"
        className={`${styles.trigger} ${variant === 'sidebar' ? styles.triggerSidebar : ''}`}
        onClick={() => setOpen((v) => !v)}
      >
        <span className={styles.avatar}>{user.username.charAt(0).toUpperCase()}</span>
        <span className={styles.username}>{user.username}</span>
        {variant === 'header' && (
          <span className={styles.arrow}>{open ? <ChevronUp size={12} /> : <ChevronDown size={12} />}</span>
        )}
      </button>
      {open && (
        <div className={`${styles.dropdown} ${variant === 'sidebar' ? styles.dropdownUp : ''}`}>
          <button type="button" className={styles.menuItem} onClick={handleProfile}>
            <span className={styles.menuIcon}><User size={14} /></span>
            {t('nav.profile')}
          </button>
          <button type="button" className={styles.menuItem} onClick={handleToggleTheme}>
            <span className={styles.menuIcon}>{theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}</span>
            {theme === 'dark' ? t('theme.light') : t('theme.dark')}
          </button>
          <button type="button" className={styles.menuItem} onClick={handleToggleLanguage}>
            <span className={styles.menuIcon}><Languages size={14} /></span>
            {t('language.switchTo')}
          </button>
          <div className={styles.divider} />
          <button type="button" className={styles.menuItem} onClick={handleLogout}>
            <span className={styles.menuIcon}><LogOut size={14} /></span>
            {t('nav.logout')}
          </button>
        </div>
      )}
    </div>
  )
}
