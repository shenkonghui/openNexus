import { useState, useEffect, useCallback, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { readWorkspaceFile, writeWorkspaceFile } from '../api/filesystem'
import CodeEditor from './CodeEditor'
import { X, Table2 } from 'lucide-react'
import styles from './FilePanel.module.css'

interface WorkspaceFileEditorProps {
  /** 文件绝对路径 */
  path: string
  onClose: () => void
}

const AUTO_SAVE_DELAY = 800 // 编辑停顿后自动保存延迟（ms）
const POLL_INTERVAL = 3000 // 后台文件变更轮询间隔（ms）

/** Excel 类文件：在 files 面板不加载二进制，提示用户切到 Excel 面板查看 */
const EXCEL_RE = /\.(xlsx|xlsm|xls|csv)$/i

/**
 * 主内容区的工作区文件编辑器：按绝对路径读写，与会话无关。
 * 供侧边栏「文件」视图选中文件后在主区域打开使用。
 *
 * 交互约定：无保存按钮，编辑后自动保存；后台文件被外部更新时，
 * 在本地无未保存编辑的前提下自动拉取最新内容。
 */
export default function WorkspaceFileEditor({ path, onClose }: WorkspaceFileEditorProps) {
  const { t } = useTranslation()
  const [content, setContent] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  // 已落盘内容（同时代表已知的后台内容）；用 ref 供轮询/防抖回调读取最新值而不重建定时器
  const savedRef = useRef('')
  const contentRef = useRef('')
  const saveTimer = useRef<number | undefined>(undefined)

  useEffect(() => {
    contentRef.current = content
  }, [content])

  // 路径变化时重新读取文件内容
  useEffect(() => {
    let alive = true
    setLoading(true)
    setError('')
    window.clearTimeout(saveTimer.current)
    readWorkspaceFile(path)
      .then((r) => {
        if (!alive) return
        setContent(r.data.content)
        savedRef.current = r.data.content
      })
      .catch((err) => {
        if (!alive) return
        setError(err instanceof Error ? err.message : t('fileBrowser.readFailed'))
        setContent('')
        savedRef.current = ''
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [path, t])

  // 将当前内容落盘（仅在与已落盘内容不同时执行）
  const doSave = useCallback(async () => {
    const next = contentRef.current
    if (next === savedRef.current) return
    setSaving(true)
    setError('')
    try {
      await writeWorkspaceFile(path, next)
      savedRef.current = next
    } catch (err) {
      setError(err instanceof Error ? err.message : t('fileBrowser.saveFailed'))
    } finally {
      setSaving(false)
    }
  }, [path, t])

  // 编辑触发防抖自动保存
  const handleChange = useCallback((v: string) => {
    setContent(v)
    window.clearTimeout(saveTimer.current)
    saveTimer.current = window.setTimeout(() => { doSave() }, AUTO_SAVE_DELAY)
  }, [doSave])

  // Ctrl/Cmd+S 立即保存（自动保存之外的手动兜底）
  function handleKeyDown(e: React.KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && e.key === 's') {
      e.preventDefault()
      window.clearTimeout(saveTimer.current)
      doSave()
    }
  }

  // 轮询后台文件变更：仅在本地无未保存编辑时应用远端更新，避免覆盖用户输入
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (contentRef.current !== savedRef.current) return
      readWorkspaceFile(path)
        .then((r) => {
          const remote = r.data.content
          // 拉取期间用户可能已开始编辑，再次校验后再应用
          if (remote !== savedRef.current && contentRef.current === savedRef.current) {
            savedRef.current = remote
            setContent(remote)
          }
        })
        .catch(() => { /* 轮询失败静默处理 */ })
    }, POLL_INTERVAL)
    return () => window.clearInterval(timer)
  }, [path])

  // 卸载 / 路径切换前，若仍有待保存内容立即落盘
  useEffect(() => {
    return () => {
      window.clearTimeout(saveTimer.current)
      if (contentRef.current !== savedRef.current) {
        writeWorkspaceFile(path, contentRef.current).catch(() => {})
      }
    }
  }, [path])

  const fileName = path.split(/[\\/]/).pop() || path
  const isExcel = EXCEL_RE.test(path)

  // Excel 文件：不在 files 面板加载二进制，提示用户切到 Excel 面板
  if (isExcel) {
    return (
      <div className={styles.panel}>
        <div className={styles.toolbar}>
          <span className={styles.fileName} title={path}>{fileName}</span>
          <div className={styles.toolbarActions}>
            <button className={styles.closeBtn} onClick={onClose} type="button" title={t('common.close')}>
              <X size={16} />
            </button>
          </div>
        </div>
        <div className={styles.placeholder} style={{ flexDirection: 'column', gap: 8 }}>
          <Table2 size={32} style={{ opacity: 0.4 }} />
          <span>{t('excel.openInExcelPanel')}</span>
        </div>
      </div>
    )
  }

  return (
    <div className={styles.panel} onKeyDown={handleKeyDown}>
      <div className={styles.toolbar}>
        <span className={styles.fileName} title={path}>
          {fileName}
        </span>
        <div className={styles.toolbarActions}>
          {saving && <span className={styles.status}>{t('common.saving')}</span>}
          <button className={styles.closeBtn} onClick={onClose} type="button" title={t('common.close')}>
            <X size={16} />
          </button>
        </div>
      </div>

      {error && <div className={styles.error}>{error}</div>}

      <div className={styles.editorArea}>
        {loading ? (
          <div className={styles.placeholder}>{t('common.loading')}</div>
        ) : (
          <CodeEditor value={content} onChange={handleChange} filePath={path} />
        )}
      </div>
    </div>
  )
}
