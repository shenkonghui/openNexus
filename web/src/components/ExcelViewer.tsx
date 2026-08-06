import { useState, useEffect, useMemo, useRef, useCallback, memo } from 'react'
import { useTranslation } from 'react-i18next'
import { X, Table2, Send, Loader2, AlertCircle } from 'lucide-react'
import * as XLSX from 'xlsx'
import { readWorkspaceFileBinary } from '../api/filesystem'
import { useFileViewer } from '../context/FileViewerContext'
import styles from './ExcelViewer.module.css'

/** 单元格坐标（0-based 行列） */
interface CellPos {
  r: number
  c: number
}

/** 选区：起止行列，自动归一化 */
interface Selection {
  start: CellPos
  end: CellPos
}

function normalizeSelection(sel: Selection): { r0: number; r1: number; c0: number; c1: number } {
  return {
    r0: Math.min(sel.start.r, sel.end.r),
    r1: Math.max(sel.start.r, sel.end.r),
    c0: Math.min(sel.start.c, sel.end.c),
    c1: Math.max(sel.start.c, sel.end.c),
  }
}

function inSelection(sel: Selection | null, r: number, c: number): boolean {
  if (!sel) return false
  const { r0, r1, c0, c1 } = normalizeSelection(sel)
  return r >= r0 && r <= r1 && c >= c0 && c <= c1
}

/** 把单元格值渲染为字符串（保留 SheetJS 的格式化结果） */
function cellText(v: unknown): string {
  if (v === null || v === undefined) return ''
  if (typeof v === 'boolean') return v ? 'TRUE' : 'FALSE'
  return String(v)
}

/**
 * Excel 查看器：解析 .xlsx/.xlsm/.xls/.csv，sheet 标签切换 + 表格渲染 + 鼠标拖选单元格区域。
 * 选中区域一键插入坐标引用到对话输入框（通过全局事件 onx:insert-to-prompt），
 * 格式：`@<绝对路径>#<sheet>!<选区>`，agent 自行读取对应区域，不把表格数据塞进上下文。
 * 文件来源：FileViewerContext 的 openFilePath（点击文件树选中 .xlsx 时触发）。
 */
function ExcelViewer() {
  const { t } = useTranslation()
  const { openFilePath, closeFile, registerEmbedded } = useFileViewer()
  useEffect(() => registerEmbedded(), [registerEmbedded])

  const [workbook, setWorkbook] = useState<XLSX.WorkBook | null>(null)
  const [activeSheet, setActiveSheet] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selection, setSelection] = useState<Selection | null>(null)
  const draggingRef = useRef(false)

  // 当前 sheet 的二维数据（sheet_to_json 的 header:1 模式返回数组的数组）
  const sheetData = useMemo<unknown[][]>(() => {
    if (!workbook || !workbook.SheetNames.length) return []
    const name = workbook.SheetNames[activeSheet]
    const ws = workbook.Sheets[name]
    if (!ws) return []
    return XLSX.utils.sheet_to_json<unknown[]>(ws, { header: 1, raw: true, defval: null })
  }, [workbook, activeSheet])

  // 列数：取所有行的最大长度，保证不规则 sheet 也能完整渲染
  const colCount = useMemo(() => {
    let max = 0
    for (const row of sheetData) {
      if (row.length > max) max = row.length
    }
    return max
  }, [sheetData])

  // 文件路径变化时重新加载
  useEffect(() => {
    if (!openFilePath) {
      setWorkbook(null)
      setError('')
      setSelection(null)
      return
    }
    // 仅处理 Excel 类文件；非 Excel 文件不接管（留给 files 面板）
    if (!/\.(xlsx|xlsm|xls|csv)$/i.test(openFilePath)) {
      setWorkbook(null)
      setError('')
      setSelection(null)
      return
    }
    let alive = true
    setLoading(true)
    setError('')
    setSelection(null)
    readWorkspaceFileBinary(openFilePath)
      .then((buf) => {
        if (!alive) return
        const wb = XLSX.read(buf, { type: 'array' })
        setWorkbook(wb)
        setActiveSheet(0)
      })
      .catch((err) => {
        if (!alive) return
        setError(err instanceof Error ? err.message : t('fileBrowser.readFailed'))
        setWorkbook(null)
      })
      .finally(() => { if (alive) setLoading(false) })
    return () => { alive = false }
  }, [openFilePath, t])

  // 打开 Excel 文件时自动激活本面板（让 tab 切到 excel）
  useEffect(() => {
    if (openFilePath && /\.(xlsx|xlsm|xls|csv)$/i.test(openFilePath)) {
      window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'excel' } }))
    }
  }, [openFilePath])

  const handleMouseDown = useCallback((r: number, c: number) => {
    draggingRef.current = true
    setSelection({ start: { r, c }, end: { r, c } })
  }, [])

  const handleMouseEnter = useCallback((r: number, c: number) => {
    if (!draggingRef.current) return
    setSelection((prev) => prev ? { ...prev, end: { r, c } } : null)
  }, [])

  useEffect(() => {
    const onUp = () => { draggingRef.current = false }
    document.addEventListener('mouseup', onUp)
    return () => document.removeEventListener('mouseup', onUp)
  }, [])

  const handleInsert = useCallback(() => {
    if (!selection || !openFilePath) return
    const { r0, r1, c0, c1 } = normalizeSelection(selection)
    const sheetName = workbook?.SheetNames[activeSheet] || ''
    const range = `${XLSX.utils.encode_cell({ r: r0, c: c0 })}:${XLSX.utils.encode_cell({ r: r1, c: c1 })}`
    // 只插入坐标引用，不塞表格数据：agent 据此自行读取对应区域
    const ref = `@${openFilePath}#${sheetName}!${range} `
    window.dispatchEvent(new CustomEvent('onx:insert-to-prompt', { detail: { text: ref } }))
    // 切回对话面板让用户看到插入结果
    window.dispatchEvent(new CustomEvent('onx:activate-panel', { detail: { panelId: 'chat' } }))
  }, [selection, openFilePath, workbook, activeSheet])

  const fileName = openFilePath ? openFilePath.split(/[\\/]/).pop() || openFilePath : ''
  const isExcelFile = !!openFilePath && /\.(xlsx|xlsm|xls|csv)$/i.test(openFilePath)

  if (!openFilePath || !isExcelFile) {
    return (
      <div className={styles.placeholder}>
        <Table2 size={40} style={{ opacity: 0.4 }} />
        <div>{t('excel.emptyTitle')}</div>
        <div className={styles.hint}>{t('excel.emptyHint')}</div>
      </div>
    )
  }

  const selInfo = selection ? (() => {
    const { r0, r1, c0, c1 } = normalizeSelection(selection)
    const rows = r1 - r0 + 1
    const cols = c1 - c0 + 1
    return `${t('excel.selection')}: ${rows}×${cols}`
  })() : ''

  return (
    <div className={styles.panel}>
      <div className={styles.toolbar}>
        <span className={styles.fileName} title={openFilePath}>{fileName}</span>
        {selInfo && <span className={styles.selectionInfo}>{selInfo}</span>}
        <button
          type="button"
          className={styles.insertBtn}
          onClick={handleInsert}
          disabled={!selection}
          title={t('excel.insertToPrompt')}
        >
          <Send size={12} />
          {t('excel.insertToPrompt')}
        </button>
        <button className={styles.closeBtn} onClick={closeFile} type="button" title={t('common.close')}>
          <X size={16} />
        </button>
      </div>

      {error && <div className={styles.error}>{error}</div>}

      {workbook && workbook.SheetNames.length > 1 && (
        <div className={styles.sheetTabs}>
          {workbook.SheetNames.map((name, i) => (
            <button
              key={name}
              type="button"
              className={`${styles.sheetTab} ${i === activeSheet ? styles.sheetTabActive : ''}`}
              onClick={() => { setActiveSheet(i); setSelection(null) }}
            >
              {name}
            </button>
          ))}
        </div>
      )}

      <div className={styles.tableWrap}>
        {loading ? (
          <div className={styles.placeholder}>
            <Loader2 size={20} />
            <span>{t('common.loading')}</span>
          </div>
        ) : workbook && sheetData.length > 0 ? (
          <table className={styles.table}>
            <thead>
              <tr>
                <th className={styles.rowHeader}>#</th>
                {Array.from({ length: colCount }, (_, c) => (
                  <th key={c}>{XLSX.utils.encode_col(c)}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sheetData.map((row, r) => (
                <tr key={r}>
                  <td className={styles.rowHeader}>{r + 1}</td>
                  {Array.from({ length: colCount }, (_, c) => (
                    <td
                      key={c}
                      className={`${styles.cell} ${inSelection(selection, r, c) ? styles.cellSelected : ''}`}
                      onMouseDown={() => handleMouseDown(r, c)}
                      onMouseEnter={() => handleMouseEnter(r, c)}
                    >
                      {cellText(row[c])}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        ) : workbook ? (
          <div className={styles.placeholder}>
            <AlertCircle size={28} style={{ opacity: 0.4 }} />
            <span>{t('excel.emptySheet')}</span>
          </div>
        ) : null}
      </div>
    </div>
  )
}

export default memo(ExcelViewer)
