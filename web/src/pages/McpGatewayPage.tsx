import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { useRequireAuth } from '../hooks/useRequireAuth'
import { useCurrentWorkspace } from '../hooks/useCurrentWorkspace'
import AppLayout, { SidebarToggleButton } from '../components/AppLayout'
import LoadingSpinner from '../components/LoadingSpinner'
import McpGatewayCard from '../components/McpGatewayCard'
import { ArrowLeft } from 'lucide-react'
import styles from './McpGatewayPage.module.css'

/**
 * MCP 聚合网关独立页面。
 * 从 SettingsPage 抽离，便于单独分享链接 / 渐进加载。
 */
export default function McpGatewayPage() {
  const { t } = useTranslation()
  const { user, loading: authLoading } = useRequireAuth()
  const { workspaceId, sessions } = useCurrentWorkspace(!!user)

  if (authLoading || !user) return <LoadingSpinner />

  return (
    <AppLayout sidebarProps={{ sessions, workspaceId }}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div className={styles.headerLeft}>
            <SidebarToggleButton />
            <div>
              <h1 className={styles.title}>{t('mcpGatewayPage.title')}</h1>
              <p className={styles.subtitle}>{t('mcpGatewayPage.subtitle')}</p>
            </div>
          </div>
        </header>

        <div className={styles.body}>
          <div className={styles.bodyInner}>
            <Link to="?settings=1&settingsTab=config" className={styles.backLink}>
              <ArrowLeft size={14} />
              <span>{t('mcpGatewayPage.backToSettings')}</span>
            </Link>
            <McpGatewayCard />
          </div>
        </div>
      </div>
    </AppLayout>
  )
}
