import { useState, useEffect, useRef, createContext, useContext, type ReactNode, type ComponentProps, type MouseEvent as ReactMouseEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { PanelLeftOpen, PanelLeftClose, Menu, FolderTree } from 'lucide-react'
import SessionSidebar from './SessionSidebar'
import FileExplorer from './FileExplorer'
import WorkspaceFileEditor from './WorkspaceFileEditor'
import StartupWarmup from './StartupWarmup'
import SettingsDialog, { parseSettingsTab } from './SettingsDialog'
import { getWorkspace } from '../api/workspaces'
import { getPermissionSettings, updatePermissionSettings } from '../api/permissions'
import type { PermissionSettings } from '../types'
import { useFileViewer } from '../context/FileViewerContext'
import { newTaskUrl } from '../utils/routes'
import NexusLogoIcon from './NexusLogoIcon'
import styles from './AppLayout.module.css'

// 整体隐藏/展开侧边栏的状态，独立于 SessionSidebar 内部分组折叠状态
const STORAGE_KEY = 'opennexus.sidebar.hidden'
// 侧边栏宽度（可拖拽调整）
const WIDTH_KEY = 'opennexus.sidebar.width'
const DEFAULT_WIDTH = 240
const MIN_WIDTH = 180
const MAX_WIDTH = 520

function loadHidden(): boolean {
  try { return localStorage.getItem(STORAGE_KEY) === '1' } catch { return false }
}

function loadWidth(): number {
  try {
    const raw = localStorage.getItem(WIDTH_KEY)
    const n = raw ? Number(raw) : NaN
    if (!isNaN(n) && n >= MIN_WIDTH && n <= MAX_WIDTH) return n
  } catch { /* ignore */ }
  return DEFAULT_WIDTH
}

interface SidebarContextValue {
  collapsed: boolean
  toggle: () => void
}

const SidebarContext = createContext<SidebarContextValue>({ collapsed: false, toggle: () => {} })

export function useSidebar() {
  return useContext(SidebarContext)
}

/**
 * 折叠状态下显示的展开按钮（放在各页面 header 左侧）。
 * 仅在侧边栏被隐藏时渲染，与原 ChatPage 行为一致。
 */
export function SidebarToggleButton() {
  const { t } = useTranslation()
  const { collapsed, toggle } = useSidebar()
  if (!collapsed) return null
  return (
    <button className={styles.iconBtn} onClick={toggle} type="button" title={t('common.open') + ' (⌘B)'}>
      <PanelLeftOpen size={18} />
    </button>
  )
}

interface AppLayoutProps {
  // 透传给 SessionSidebar 的 props（onCollapse 由本组件自动注入，不可外部覆盖）
  sidebarProps: Omit<ComponentProps<typeof SessionSidebar>, 'onCollapse'>
  children: ReactNode
}

/**
 * 全局共享布局：统一渲染左侧侧边栏、管理折叠/展开状态与 ⌘B 快捷键。
 * 各页面通过 sidebarProps 透传数据/回调，children 即右侧主内容区。
 */
export default function AppLayout({ sidebarProps, children }: AppLayoutProps) {
  const { t } = useTranslation()
  const { openFilePath, openFile, closeFile, hasEmbedded } = useFileViewer()
  const [collapsed, setCollapsed] = useState(loadHidden)
  const [width, setWidth] = useState(loadWidth)
  // 侧栏视图永远以「菜单」为默认，手动切到文件仅在本次会话内有效（不持久化）
  const [view, setView] = useState<'menu' | 'files'>('menu')
  // 当前工作区 cwd，作为文件浏览器的根目录
  const [cwd, setCwd] = useState('')
  const workspaceId = sidebarProps.workspaceId
  // 设置弹窗：由 URL 参数 ?settings=1&settingsTab=xxx 控制，任何页面可打开且支持深链/后退关闭
  const [searchParams, setSearchParams] = useSearchParams()
  const settingsOpen = searchParams.has('settings')
  const settingsTab = parseSettingsTab(searchParams.get('settingsTab'))

  function closeSettings() {
    const next = new URLSearchParams(searchParams)
    next.delete('settings')
    next.delete('settingsTab')
    setSearchParams(next, { replace: true })
  }

  // 全局 YOLO（permissions.mode），侧栏左下角拨动开关
  const [globalYolo, setGlobalYolo] = useState(false)
  const [yoloBusy, setYoloBusy] = useState(false)
  const permRef = useRef<PermissionSettings>({ mode: 'normal', allow: [], ask: [], deny: [] })

  // 仅当不存在内嵌文件面板（如编码模式 files 面板）时，才用主区域覆盖层显示文件
  const showOverlay = !!openFilePath && !hasEmbedded

  // 加载全局 YOLO 状态
  useEffect(() => {
    let alive = true
    getPermissionSettings()
      .then((r) => {
        if (!alive) return
        permRef.current = {
          mode: r.data.mode === 'yolo' ? 'yolo' : 'normal',
          allow: r.data.allow || [],
          ask: r.data.ask || [],
          deny: r.data.deny || [],
        }
        setGlobalYolo(permRef.current.mode === 'yolo')
      })
      .catch(() => {})
    return () => { alive = false }
  }, [])

  async function handleToggleGlobalYolo() {
    if (yoloBusy) return
    const next = !globalYolo
    setYoloBusy(true)
    setGlobalYolo(next) // 乐观更新
    try {
      const payload: PermissionSettings = {
        ...permRef.current,
        mode: next ? 'yolo' : 'normal',
      }
      const resp = await updatePermissionSettings(payload)
      permRef.current = {
        mode: resp.data.mode === 'yolo' ? 'yolo' : 'normal',
        allow: resp.data.allow || [],
        ask: resp.data.ask || [],
        deny: resp.data.deny || [],
      }
      setGlobalYolo(permRef.current.mode === 'yolo')
    } catch {
      setGlobalYolo(!next) // 回滚
    } finally {
      setYoloBusy(false)
    }
  }

  // 持久化整体折叠状态，使各页面切换后保持一致
  useEffect(() => {
    try { localStorage.setItem(STORAGE_KEY, collapsed ? '1' : '0') } catch { /* ignore */ }
  }, [collapsed])

  // 获取当前工作区 cwd，作为文件浏览器根目录；切换工作区时关闭已打开文件
  useEffect(() => {
    closeFile()
    if (!workspaceId) { setCwd(''); return }
    let alive = true
    getWorkspace(workspaceId)
      .then((r) => { if (alive) setCwd(r.data.workspace.cwd || '') })
      .catch(() => { if (alive) setCwd('') })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [workspaceId])

  // Cmd/Ctrl+B 切换侧边栏隐藏/展开（由各页面统一收口至此）
  useEffect(() => {
    function handleToggle(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'b') {
        e.preventDefault()
        setCollapsed((v) => !v)
      }
    }
    document.addEventListener('keydown', handleToggle)
    return () => document.removeEventListener('keydown', handleToggle)
  }, [])

  function toggle() { setCollapsed((v) => !v) }

  // 拖拽调整侧边栏宽度
  function startResize(e: ReactMouseEvent) {
    e.preventDefault()
    const startX = e.clientX
    const startWidth = width
    let current = startWidth
    function onMove(ev: MouseEvent) {
      const next = Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, startWidth + (ev.clientX - startX)))
      current = next
      setWidth(next)
    }
    function onUp() {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      try { localStorage.setItem(WIDTH_KEY, String(current)) } catch { /* ignore */ }
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
  }

  return (
    <SidebarContext.Provider value={{ collapsed, toggle }}>
      <StartupWarmup />
      <div className={styles.layout}>
        {!collapsed && (
          <div className={styles.sidebarWrap} style={{ width }}>
            {/* Logo 与菜单/文件小开关同一行；模式变化会自动切默认视图，此处可手动覆盖 */}
            <div className={styles.header}>
              <Link to={newTaskUrl(workspaceId)} className={styles.logo} title={t('session.newSession')}>
                <NexusLogoIcon size={22} />
              </Link>
              <div className={styles.viewSwitch} role="tablist" aria-label={t('sidebar.menuTab') + '/' + t('sidebar.filesTab')}>
                <button
                  type="button"
                  role="tab"
                  aria-selected={view === 'menu'}
                  className={`${styles.viewBtn} ${view === 'menu' ? styles.viewBtnActive : ''}`}
                  onClick={() => setView('menu')}
                  title={t('sidebar.menuTab')}
                >
                  <Menu size={15} />
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={view === 'files'}
                  className={`${styles.viewBtn} ${view === 'files' ? styles.viewBtnActive : ''}`}
                  onClick={() => setView('files')}
                  title={t('sidebar.filesTab')}
                >
                  <FolderTree size={15} />
                </button>
              </div>
              <button
                type="button"
                className={styles.collapseBtn}
                onClick={toggle}
                title={t('common.close') + ' (⌘B)'}
              >
                <PanelLeftClose size={16} />
              </button>
            </div>
            <div className={styles.sidebarBody}>
              <div className={styles.viewPane} style={{ display: view === 'menu' ? 'flex' : 'none' }}>
                <SessionSidebar {...sidebarProps} hideLogo />
              </div>
              <div className={styles.viewPane} style={{ display: view === 'files' ? 'flex' : 'none' }}>
                {cwd ? (
                  <FileExplorer rootPath={cwd} onSelectFile={openFile} selectedPath={openFilePath ?? undefined} />
                ) : (
                  <div className={styles.filesEmpty}>{t('sidebar.noWorkspace')}</div>
                )}
              </div>
            </div>
            {/* 左下角全局 YOLO 拨动开关 */}
            <div className={styles.yoloBar}>
              <span className={`${styles.yoloLabel} ${globalYolo ? styles.yoloLabelOn : ''}`}>YOLO</span>
              <button
                type="button"
                role="switch"
                aria-checked={globalYolo}
                className={`${styles.yoloSwitch} ${globalYolo ? styles.yoloSwitchOn : ''}`}
                onClick={handleToggleGlobalYolo}
                disabled={yoloBusy}
                title={t('sidebar.yoloHint')}
              >
                <span className={styles.yoloKnob} />
              </button>
            </div>
          </div>
        )}
        {!collapsed && (
          <div className={styles.sidebarResizer} onMouseDown={startResize} role="separator" aria-orientation="vertical" />
        )}
        <div className={styles.main}>
          <div className={styles.mainPane} style={{ display: showOverlay ? 'none' : 'flex' }}>
            {children}
          </div>
          {showOverlay && (
            <div className={styles.mainPane} style={{ display: 'flex' }}>
              <WorkspaceFileEditor key={openFilePath} path={openFilePath!} onClose={closeFile} />
            </div>
          )}
        </div>
      </div>
      {settingsOpen && <SettingsDialog key={settingsTab} initialTab={settingsTab} onClose={closeSettings} />}
    </SidebarContext.Provider>
  )
}
