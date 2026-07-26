import {
  useState,
  useRef,
  useCallback,
  useEffect,
  useMemo,
  createElement,
  type KeyboardEvent,
  type RefObject,
} from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, Search, FileCode, AlignLeft, ExternalLink, Eye, Code, ArrowLeft, MousePointerClick } from 'lucide-react'
import { fetchBrowserPage, type BrowserPage } from '../api/browser'
import type { PanelCtx } from '../modes/types'
import styles from './BrowserPanel.module.css'

interface BrowserPanelProps {
  ctx: PanelCtx
}

interface Selection {
  text: string
  html: string
}

interface PickedElement {
  selector: string
  text: string
  html: string
  fullText: string
}

/** 把选中元素格式化为简短引用（不显示完整内容）。 */
function formatElementQuote(el: PickedElement, url: string, title: string): string {
  const source = title || url
  const body = el.text || ''
  return `> 来源：${source}\n> 元素：${el.selector}\n> URL：${url}\n>\n> ${body}`
}

export default function BrowserPanel({ ctx }: BrowserPanelProps) {
  const insert = useCallback(
    (value: string) => {
      if (!value || !ctx.onRestoreInputChange) return
      const current = ctx.restoreInput || ''
      ctx.onRestoreInputChange(current ? `${current}\n\n${value}` : value)
    },
    [ctx],
  )

  const isElectron = window.opennexus?.isElectron === true
  return (
    <div className={styles.panel}>
      {isElectron ? (
        <ElectronBrowser onInsert={insert} />
      ) : (
        <FallbackBrowser onInsert={insert} />
      )}
    </div>
  )
}

interface BrowserChildProps {
  onInsert: (value: string) => void
}

function useIframeMessaging(
  contentWindowRef: RefObject<WindowProxy | null>,
  fetchUrl: (url: string) => Promise<unknown>,
  setSelection: (v: Selection | null) => void,
  onElementPicked: (el: PickedElement) => void,
) {
  useEffect(() => {
    function onMessage(event: MessageEvent) {
      if (event.source !== contentWindowRef.current) return
      const data = event.data
      if (!data || typeof data !== 'object') return
      if (data.type === 'opennexus-browser-selection') {
        if (data.text || data.html) {
          setSelection({ text: data.text || '', html: data.html || '' })
        } else {
          setSelection(null)
        }
      } else if (data.type === 'opennexus-browser-element-picked' && data.element) {
        onElementPicked(data.element)
      } else if (data.type === 'opennexus-browser-navigate' && data.url) {
        fetchUrl(data.url)
      }
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [contentWindowRef, fetchUrl, onElementPicked, setSelection])
}

type BrowserMode = 'direct' | 'proxy'

/**
 * 代理模式靠服务端抓到的 HTML 源码渲染，纯前端渲染（SPA）的页面源码里只有空壳，
 * 直接渲染必然白屏。用抓到的正文长度粗略判定是否值得代理渲染。
 */
function isProxyRenderable(page: BrowserPage): boolean {
  if (!page.html) return false
  return (page.text || '').trim().length >= 40
}

function FallbackBrowser({ onInsert }: BrowserChildProps) {
  const { t } = useTranslation()
  const [input, setInput] = useState('')
  const [mode, setMode] = useState<BrowserMode>('direct')
  const [directUrl, setDirectUrl] = useState('')
  const [page, setPage] = useState<BrowserPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [pickerActive, setPickerActive] = useState(false)
  const [selection, setSelection] = useState<Selection | null>(null)
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const contentWindowRef = useRef<WindowProxy | null>(null)
  const historyStack = useRef<string[]>([])

  const fetchUrl = useCallback(async (url: string): Promise<BrowserPage | null> => {
    setLoading(true)
    setError('')
    setNotice('')
    setSelection(null)
    setPage(null)
    try {
      const resp = await fetchBrowserPage(url)
      setPage(resp.data)
      setInput(resp.data.url)
      return resp.data
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      return null
    } finally {
      setLoading(false)
    }
  }, [])

  const onElementPicked = useCallback(
    (el: PickedElement) => {
      setPickerActive(false)
      onInsert(formatElementQuote(el, page?.url || directUrl, page?.title || ''))
    },
    [onInsert, page?.url, page?.title, directUrl],
  )

  useIframeMessaging(contentWindowRef, fetchUrl, setSelection, onElementPicked)

  const pushHistory = useCallback((url: string) => {
    if (url && (historyStack.current.length === 0 || historyStack.current[historyStack.current.length - 1] !== url)) {
      historyStack.current.push(url)
    }
  }, [])

  const handleFetch = useCallback(() => {
    const url = normalizeUrl(input.trim())
    if (!url) return
    setInput(url)
    setNotice('')
    if (mode === 'direct') {
      pushHistory(directUrl)
      setDirectUrl(url)
      setPage(null)
      setError('')
    } else {
      pushHistory(page?.url || '')
      fetchUrl(url)
    }
  }, [input, mode, fetchUrl, directUrl, page?.url, pushHistory])

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleFetch()
    }
  }

  const handleLoad = useCallback(() => {
    const w = iframeRef.current?.contentWindow || null
    contentWindowRef.current = w
    // 代理模式下页面（含注入脚本）加载完成后，补发一次选择器状态：
    // 用户可能在页面就绪前就点了「元素选择器」。
    if (w && mode === 'proxy' && pickerActive) {
      w.postMessage({ type: 'opennexus-browser-toggle-picker', active: true }, '*')
    }
  }, [mode, pickerActive])

  const handleGoBack = useCallback(() => {
    const prev = historyStack.current.pop()
    if (!prev) return
    setInput(prev)
    if (mode === 'direct') {
      setDirectUrl(prev)
      setPage(null)
    } else {
      fetchUrl(prev)
    }
  }, [mode, fetchUrl])

  useEffect(() => {
    if (mode === 'proxy' && !page && !loading && directUrl) {
      fetchUrl(directUrl)
    }
    if (mode === 'direct' && !directUrl && page?.url) {
      setDirectUrl(page.url)
    }
  }, [mode, page, loading, directUrl, page?.url, fetchUrl])

  const sendToIframe = useCallback((type: string, payload?: any) => {
    const w = contentWindowRef.current
    if (w) w.postMessage(Object.assign({ type: `opennexus-browser-${type}` }, payload), '*')
  }, [])

  const handleTogglePicker = useCallback(async () => {
    if (pickerActive) {
      setPickerActive(false)
      sendToIframe('toggle-picker', { active: false })
      return
    }
    if (mode === 'proxy') {
      setPickerActive(true)
      sendToIframe('toggle-picker', { active: true })
      return
    }
    // 直连模式是跨域 iframe，无法注入脚本。先抓一次源码探测能否代理渲染：
    // 纯前端渲染（SPA）的页面源码里没有正文，切过去只会白屏，此时不切、给出提示。
    const fetched = await fetchUrl(directUrl)
    if (!fetched) return
    if (!isProxyRenderable(fetched)) {
      setNotice(t('browser.pickerUnsupported'))
      return
    }
    setMode('proxy')
    setPickerActive(true)
  }, [pickerActive, mode, sendToIframe, fetchUrl, directUrl, t])

  const handleModeChange = useCallback(
    (m: BrowserMode) => {
      setMode(m)
      setNotice('')
      if (m === 'direct' && pickerActive) setPickerActive(false)
    },
    [pickerActive],
  )

  const onInsertText = useCallback(
    () => onInsert(selection?.text || page?.text || ''),
    [onInsert, selection?.text, page?.text],
  )

  const onInsertHtml = useCallback(
    () => onInsert(selection?.html || page?.html || ''),
    [onInsert, selection?.html, page?.html],
  )

  const srcDoc = useMemo(
    () => (page ? buildSrcDoc(page.html || '', page.url) : ''),
    [page],
  )

  return (
    <>
      <BrowserToolbar
        input={input}
        loading={loading}
        mode={mode}
        onModeChange={handleModeChange}
        onChange={setInput}
        onKeyDown={handleKeyDown}
        onFetch={handleFetch}
        onGoBack={handleGoBack}
        pickerActive={pickerActive}
        onTogglePicker={handleTogglePicker}
        pickerDisabled={!directUrl && !page}
      />
      {error && <div className={styles.error}>{error}</div>}
      {notice && (
        <div className={styles.notice}>
          <span>{notice}</span>
          <button type="button" className={styles.noticeClose} onClick={() => setNotice('')}>
            {t('common.close')}
          </button>
        </div>
      )}
      {/* 手动切到代理模式却抓不到正文（SPA）时白屏，给出原因和退路 */}
      {mode === 'proxy' && page && !isProxyRenderable(page) && (
        <div className={styles.notice}>
          <span>{t('browser.proxyBlankHint')}</span>
          <button
            type="button"
            className={styles.noticeClose}
            onClick={() => handleModeChange('direct')}
          >
            {t('browser.modeDirect')}
          </button>
        </div>
      )}
      {!directUrl && !page && !loading && !error && <BrowserEmpty />}
      {mode === 'direct' && directUrl && (
        <DirectContent
          url={directUrl}
          iframeRef={iframeRef}
          onLoad={handleLoad}
          loading={loading}
          onFetchText={() => fetchUrl(directUrl)}
          page={page}
          selection={selection}
          onInsertText={onInsertText}
          onInsertHtml={onInsertHtml}
        />
      )}
      {mode === 'proxy' && page && (
        <ProxyContent
          title={page.title}
          url={page.url}
          srcDoc={srcDoc}
          iframeRef={iframeRef}
          onLoad={handleLoad}
          selection={selection}
          onInsertText={onInsertText}
          onInsertHtml={onInsertHtml}
        />
      )}
    </>
  )
}

interface WebviewHandlers {
  setViewUrl: (v: string) => void
  setInput: (v: string) => void
  setError: (v: string) => void
  setTitle: (v: string) => void
  setSelection: (v: Selection | null) => void
  onElementPicked: (el: PickedElement) => void
  onLoadStart: () => void
  onLoadEnd: () => void
  onDomReady: () => void
}

/**
 * 绑定 <webview> 事件。
 * 注意入参是元素实例而非 ref：webview 是条件渲染的，用 ref 的话副作用首次执行时元素还不存在，
 * 事件就永远绑不上。回调 ref + state 能保证元素挂载后重新执行。
 * 处理函数放进 ref，避免每次重渲染都解绑重绑。
 */
function useWebview(wv: any, handlers: WebviewHandlers) {
  const hRef = useRef(handlers)
  hRef.current = handlers

  useEffect(() => {
    if (!wv) return
    const onIpc = (e: any) => {
      const h = hRef.current
      const arg = (e.args || [])[0]
      if (e.channel === 'selection' && arg) h.setSelection(arg)
      else if (e.channel === 'element-picked' && arg) h.onElementPicked(arg)
      else if (e.channel === 'navigate' && arg) {
        h.setViewUrl(arg)
        h.setInput(arg)
      }
    }
    const onFail = (e: any) => {
      if (e.isMainFrame) hRef.current.setError(e.errorDescription || '加载失败')
    }
    const onNav = (e: any) => {
      const h = hRef.current
      h.setViewUrl(e.url)
      h.setInput(e.url)
      h.setError('')
    }
    const events: [string, (e: any) => void][] = [
      ['ipc-message', onIpc],
      ['did-start-loading', () => hRef.current.onLoadStart()],
      ['did-stop-loading', () => hRef.current.onLoadEnd()],
      ['did-fail-load', onFail],
      ['did-navigate', onNav],
      ['page-title-updated', (e: any) => hRef.current.setTitle(e.title)],
      ['dom-ready', () => hRef.current.onDomReady()],
    ]
    events.forEach(([name, fn]) => wv.addEventListener(name, fn))
    return () => events.forEach(([name, fn]) => wv.removeEventListener(name, fn))
  }, [wv])
}

function ElectronBrowser({ onInsert }: BrowserChildProps) {
  const [input, setInput] = useState('')
  const [viewUrl, setViewUrl] = useState('')
  const [title, setTitle] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [pickerActive, setPickerActive] = useState(false)
  const [selection, setSelection] = useState<Selection | null>(null)
  const [wv, setWv] = useState<any>(null)
  // webview 未 dom-ready 时调用 send() 会抛异常，导航期间也会失效，需要跟踪就绪状态
  const domReadyRef = useRef(false)
  const pickerActiveRef = useRef(false)
  pickerActiveRef.current = pickerActive

  const handleFetch = useCallback(() => {
    const url = normalizeUrl(input.trim())
    if (!url) return
    setInput(url)
    setViewUrl(url)
    setError('')
  }, [input])

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleFetch()
    }
  }

  const sendToWebview = useCallback(
    (channel: string, ...args: any[]) => {
      if (!wv || !domReadyRef.current || !wv.send) return
      wv.send(channel, ...args)
    },
    [wv],
  )

  const handleGoBack = useCallback(() => {
    sendToWebview('go-back')
  }, [sendToWebview])

  const handleTogglePicker = useCallback(() => {
    const next = !pickerActive
    setPickerActive(next)
    sendToWebview('toggle-picker', next)
  }, [pickerActive, sendToWebview])

  const onElementPicked = useCallback(
    (el: PickedElement) => {
      setPickerActive(false)
      onInsert(formatElementQuote(el, viewUrl, title))
    },
    [onInsert, viewUrl, title],
  )

  const handleDomReady = useCallback(() => {
    domReadyRef.current = true
    // preload 就绪后补发选择器状态：用户可能在页面加载完成前就点了按钮，也可能刚发生页内跳转
    if (pickerActiveRef.current && wv?.send) wv.send('toggle-picker', true)
  }, [wv])

  useWebview(wv, {
    setViewUrl,
    setInput,
    setError,
    setTitle,
    setSelection,
    onElementPicked,
    onLoadStart: () => {
      domReadyRef.current = false
      setLoading(true)
    },
    onLoadEnd: () => setLoading(false),
    onDomReady: handleDomReady,
  })

  const onInsertText = useCallback(
    () => onInsert(selection?.text || ''),
    [onInsert, selection?.text],
  )

  const onInsertHtml = useCallback(
    () => onInsert(selection?.html || ''),
    [onInsert, selection?.html],
  )

  const preloadPath = window.opennexus?.webviewPreload || ''

  return (
    <>
      <BrowserToolbar
        input={input}
        loading={loading}
        onChange={setInput}
        onKeyDown={handleKeyDown}
        onFetch={handleFetch}
        onGoBack={handleGoBack}
        pickerActive={pickerActive}
        onTogglePicker={handleTogglePicker}
      />
      {error && <div className={styles.error}>{error}</div>}
      {!viewUrl && !loading && !error && <BrowserEmpty />}
      {viewUrl && (
        <WebviewContent
          src={viewUrl}
          preload={preloadPath}
          title={title}
          url={viewUrl}
          elementRef={setWv}
          selection={selection}
          onInsertText={onInsertText}
          onInsertHtml={onInsertHtml}
        />
      )}
    </>
  )
}

interface ToolbarProps {
  input: string
  loading: boolean
  mode?: BrowserMode
  onModeChange?: (m: BrowserMode) => void
  onChange: (v: string) => void
  onKeyDown: (e: KeyboardEvent<HTMLInputElement>) => void
  onFetch: () => void
  onGoBack?: () => void
  pickerActive?: boolean
  onTogglePicker?: () => void
  pickerDisabled?: boolean
}

function BrowserToolbar({
  input,
  loading,
  mode,
  onModeChange,
  onChange,
  onKeyDown,
  onFetch,
  onGoBack,
  pickerActive,
  onTogglePicker,
  pickerDisabled,
}: ToolbarProps) {
  const { t } = useTranslation()
  return (
    <div className={styles.toolbar}>
      {onGoBack && (
        <button
          type="button"
          className={styles.iconBtn}
          onClick={onGoBack}
          title={t('browser.back')}
        >
          <ArrowLeft size={14} />
        </button>
      )}
      <Globe size={14} className={styles.toolbarIcon} />
      <input
        type="text"
        className={styles.urlInput}
        value={input}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={t('browser.urlPlaceholder')}
        disabled={loading}
      />
      {onModeChange && mode && (
        <div className={styles.modeSwitch}>
          <button
            type="button"
            className={`${styles.modeBtn} ${mode === 'direct' ? styles.modeBtnActive : ''}`}
            onClick={() => onModeChange('direct')}
            title={t('browser.modeDirect')}
          >
            <Eye size={12} />
          </button>
          <button
            type="button"
            className={`${styles.modeBtn} ${mode === 'proxy' ? styles.modeBtnActive : ''}`}
            onClick={() => onModeChange('proxy')}
            title={t('browser.modeProxy')}
          >
            <Code size={12} />
          </button>
        </div>
      )}
      {onTogglePicker && (
        <button
          type="button"
          className={`${styles.iconBtn} ${pickerActive ? styles.iconBtnActive : ''}`}
          onClick={onTogglePicker}
          disabled={pickerDisabled}
          title={t('browser.togglePicker')}
        >
          <MousePointerClick size={14} />
        </button>
      )}
      <button
        type="button"
        className={styles.fetchBtn}
        onClick={onFetch}
        disabled={loading || !input.trim()}
        title={t('browser.fetch')}
      >
        {loading ? t('common.loading') : <Search size={14} />}
      </button>
    </div>
  )
}

function BrowserEmpty() {
  const { t } = useTranslation()
  return (
    <div className={styles.empty}>
      <Globe size={40} />
      <div>{t('browser.emptyHint')}</div>
    </div>
  )
}

interface FooterProps {
  title: string
  url: string
  selection: Selection | null
  onInsertText: () => void
  onInsertHtml: () => void
  extraAction?: React.ReactNode
}

function BrowserFooter({
  title,
  url,
  selection,
  onInsertText,
  onInsertHtml,
  extraAction,
}: FooterProps) {
  const { t } = useTranslation()
  return (
    <div className={styles.footer}>
      <div className={styles.footerInfo}>
        <span className={styles.pageTitle} title={url}>
          {title || t('browser.untitled')}
        </span>
        <a
          className={styles.pageLink}
          href={url}
          target="_blank"
          rel="noreferrer"
          title={url}
        >
          <ExternalLink size={12} />
        </a>
      </div>
      <div className={styles.footerActions}>
        <span className={styles.selectionInfo}>
          {selection
            ? t('browser.selectionInfo', {
                text: selection.text.length,
                html: selection.html.length,
              })
            : t('browser.noSelection')}
        </span>
        {extraAction}
        <button
          type="button"
          className={styles.insertBtn}
          onClick={onInsertText}
          disabled={!selection?.text}
          title={t('browser.insertText')}
        >
          <AlignLeft size={12} />
          {t('browser.insertText')}
        </button>
        <button
          type="button"
          className={styles.insertBtn}
          onClick={onInsertHtml}
          disabled={!selection?.html}
          title={t('browser.insertHtml')}
        >
          <FileCode size={12} />
          {t('browser.insertHtml')}
        </button>
      </div>
    </div>
  )
}

interface DirectContentProps {
  url: string
  iframeRef: RefObject<HTMLIFrameElement>
  onLoad: () => void
  loading: boolean
  onFetchText: () => void
  page: BrowserPage | null
  selection: Selection | null
  onInsertText: () => void
  onInsertHtml: () => void
}

function DirectContent({
  url,
  iframeRef,
  onLoad,
  loading,
  onFetchText,
  page,
  selection,
  onInsertText,
  onInsertHtml,
}: DirectContentProps) {
  const { t } = useTranslation()
  return (
    <div className={styles.content}>
      <iframe
        ref={iframeRef}
        className={styles.frame}
        src={url}
        sandbox="allow-scripts allow-same-origin allow-forms allow-popups"
        title={url}
        onLoad={onLoad}
      />
      <BrowserFooter
        title={page?.title || url}
        url={url}
        selection={selection}
        onInsertText={onInsertText}
        onInsertHtml={onInsertHtml}
        extraAction={
          <button
            type="button"
            className={styles.insertBtn}
            onClick={onFetchText}
            disabled={loading}
            title={t('browser.fetchText')}
          >
            <AlignLeft size={12} />
            {loading ? t('common.loading') : t('browser.fetchText')}
          </button>
        }
      />
    </div>
  )
}

interface ProxyContentProps {
  title: string
  url: string
  srcDoc: string
  iframeRef: RefObject<HTMLIFrameElement>
  onLoad: () => void
  selection: Selection | null
  onInsertText: () => void
  onInsertHtml: () => void
}

function ProxyContent({
  title,
  url,
  srcDoc,
  iframeRef,
  onLoad,
  selection,
  onInsertText,
  onInsertHtml,
}: ProxyContentProps) {
  return (
    <div className={styles.content}>
      <iframe
        ref={iframeRef}
        className={styles.frame}
        srcDoc={srcDoc}
        sandbox="allow-scripts allow-same-origin allow-forms"
        title={title}
        onLoad={onLoad}
      />
      <BrowserFooter
        title={title}
        url={url}
        selection={selection}
        onInsertText={onInsertText}
        onInsertHtml={onInsertHtml}
      />
    </div>
  )
}

interface WebviewContentProps {
  src: string
  preload: string
  title: string
  url: string
  elementRef: (el: HTMLElement | null) => void
  selection: Selection | null
  onInsertText: () => void
  onInsertHtml: () => void
}

function WebviewContent({
  src,
  preload,
  title,
  url,
  elementRef,
  selection,
  onInsertText,
  onInsertHtml,
}: WebviewContentProps) {
  return (
    <div className={styles.content}>
      {createElement('webview' as any, {
        ref: elementRef,
        src,
        preload,
        className: styles.frame,
        title,
      })}
      <BrowserFooter
        title={title}
        url={url}
        selection={selection}
        onInsertText={onInsertText}
        onInsertHtml={onInsertHtml}
      />
    </div>
  )
}

function buildSrcDoc(html: string, baseURL: string): string {
  const parser = new DOMParser()
  const doc = parser.parseFromString(
    html || '<!DOCTYPE html><html><head></head><body></body></html>',
    'text/html',
  )
  if (!doc.querySelector('base')) {
    const base = doc.createElement('base')
    base.setAttribute('href', baseURL)
    if (doc.head) doc.head.insertBefore(base, doc.head.firstChild)
  }
  doc.querySelectorAll('meta[http-equiv]').forEach((el) => {
    const meta = el as HTMLMetaElement
    if (meta.httpEquiv.toLowerCase().startsWith('content-security-policy')) {
      meta.remove()
    }
  })
  const script = doc.createElement('script')
  script.textContent = getInjectedScript(baseURL)
  if (doc.body) doc.body.appendChild(script)
  else doc.documentElement.appendChild(script)
  const doctype = doc.doctype
    ? `<!DOCTYPE ${doc.doctype.name}>`
    : '<!DOCTYPE html>'
  return `${doctype}${doc.documentElement.outerHTML}`
}

function getInjectedScript(baseURL: string): string {
  const base = JSON.stringify(baseURL)
  return `(function(){
  const baseURL=${base};
  function post(type,payload){window.parent.postMessage(Object.assign({type},payload),'*');}
  function selectedText(){return window.getSelection().toString();}
  function selectedHTML(){
    const s=window.getSelection();
    if(!s.rangeCount)return'';
    const d=document.createElement('div');
    d.appendChild(s.getRangeAt(0).cloneContents());
    return d.innerHTML;
  }
  function report(){post('opennexus-browser-selection',{text:selectedText(),html:selectedHTML()});}
  function abs(url){try{return new URL(url,baseURL).href;}catch{return url;}}

  // ===== 元素选择器 =====
  let pickerActive=false,lastEl=null;
  function clearHL(){if(lastEl){lastEl.style.outline=lastEl._oo||'';lastEl.style.outlineOffset=lastEl._ooo||'';lastEl.style.backgroundColor=lastEl._obg||'';lastEl=null;}}
  function describe(el){
    const tag=el.tagName.toLowerCase();
    const id=el.id?'#'+el.id:'';
    const cls=el.className&&typeof el.className==='string'?'.'+el.className.trim().split(/\\s+/).slice(0,2).join('.'):'';
    const text=(el.innerText||'').trim().replace(/\\s+/g,' ');
    const summary=text.length>200?text.slice(0,200)+'...':text;
    return {selector:tag+id+cls,text:summary,html:el.outerHTML.length>2000?el.outerHTML.slice(0,2000)+'...':el.outerHTML,fullText:text};
  }
  function onMove(e){if(!pickerActive)return;if(e.target===lastEl)return;clearHL();const el=e.target;if(!el||el===document.body||el===document.documentElement)return;el._oo=el.style.outline;el._ooo=el.style.outlineOffset;el._obg=el.style.backgroundColor;el.style.outline='2px solid #ff6b35';el.style.outlineOffset='-2px';el.style.backgroundColor='rgba(255,107,53,0.12)';lastEl=el;}
  function onPick(e){if(!pickerActive)return;e.preventDefault();e.stopPropagation();clearHL();post('opennexus-browser-element-picked',{element:describe(e.target)});setPicker(false);}
  function onKey(e){if(e.key==='Escape'&&pickerActive){clearHL();setPicker(false);}}
  function setPicker(active){pickerActive=active;if(active){document.body.style.cursor='crosshair';document.addEventListener('mousemove',onMove,true);document.addEventListener('click',onPick,true);document.addEventListener('keyup',onKey,true);}else{clearHL();document.body.style.cursor='';document.removeEventListener('mousemove',onMove,true);document.removeEventListener('click',onPick,true);document.removeEventListener('keyup',onKey,true);}}

  function onClick(e){
    if(pickerActive)return;
    const a=e.target.closest&&e.target.closest('a[href]');
    if(!a)return;
    const href=a.getAttribute('href')||'';
    if(href.startsWith('#')||href.startsWith('javascript:'))return;
    e.preventDefault();e.stopPropagation();
    post('opennexus-browser-navigate',{url:abs(href)});
  }
  document.addEventListener('selectionchange',report);
  document.addEventListener('mouseup',report);
  document.addEventListener('click',onClick,true);
  window.addEventListener('message',function(e){
    if(e.data&&e.data.type==='opennexus-browser-toggle-picker'){setPicker(!!e.data.active);}
    if(e.data&&e.data.type==='opennexus-browser-go-back'){if(history.length>1)history.back();}
  });
})();`
}

function normalizeUrl(url: string): string {
  if (!url) return ''
  if (/^https?:\/\//i.test(url)) return url
  if (/^\/\//.test(url)) return `https:${url}`
  return `https://${url}`
}
