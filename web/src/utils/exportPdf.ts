import { createElement } from 'react'
import { createRoot } from 'react-dom/client'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { renderDrawioXmlToSvg } from './drawioViewerRender'

/**
 * 把 Markdown 文档导出为 PDF（点击直接下载 .pdf，不弹浏览器打印框）。
 *
 * 架构（绕开浏览器原生打印）：
 *   1. drawio 块：用 draw.io 官方查看器（viewer-static.min.js，内置完整 shape 库）
 *      在本地把 XML 高保真渲染成 SVG 字符串（离线可用，几乎不失真；
 *      html2canvas 无法截图跨域 iframe，所以必须先转成内联 SVG 再嵌入，
 *      否则图表会是空白）。
 *   2. 正文 + 嵌入的 SVG：渲染到一个屏幕外的白底容器（独立 DOM，不依赖当前
 *      是 view/edit 模式，不受主应用布局污染）。
 *   3. html2pdf.js（html2canvas + jsPDF）对该容器截图生成 PDF，直接下载。
 *
 * drawio 渲染失败时降级为显示 XML 源码，不阻塞导出。
 */

const REMARK_PLUGINS = [remarkGfm]

// 匹配 fenced ```drawio / ```draw 代码块，捕获其 XML 源码
const DRAWIO_BLOCK_RE = /```(?:drawio|draw)\s*\n([\s\S]*?)```/g

/** 从 Markdown 中提取所有 drawio 块的 XML（去重，保持出现顺序） */
function extractDrawioBlocks(content: string): string[] {
  const seen = new Set<string>()
  const list: string[] = []
  let m: RegExpExecArray | null
  DRAWIO_BLOCK_RE.lastIndex = 0
  while ((m = DRAWIO_BLOCK_RE.exec(content)) !== null) {
    const xml = m[1].replace(/\n$/, '').trim()
    if (xml && !seen.has(xml)) {
      seen.add(xml)
      list.push(xml)
    }
  }
  return list
}

/**
 * 批量把 drawio XML 本地高保真渲染为 SVG。
 * 用 draw.io 官方查看器在浏览器端渲染，不依赖公网服务；单张失败留空，
 * 渲染时降级为源码，保证导出流程永不卡死。
 */
async function convertDrawioBlocks(
  xmls: string[],
  onProgress?: (done: number, total: number) => void,
): Promise<Map<string, string>> {
  const map = new Map<string, string>()
  const total = xmls.length
  for (let i = 0; i < xmls.length; i++) {
    const xml = xmls[i]
    try {
      const svg = await renderDrawioXmlToSvg(xml)
      if (svg) map.set(xml, svg)
    } catch {
      /* 单张失败：留空，渲染时降级为源码 */
    }
    onProgress?.(i + 1, total)
  }
  return map
}

/**
 * 把 SVG 字符串处理成可用于 <img src> 的 data URI。
 *
 * html2canvas 对内联 <svg> 支持很差（常渲染空白），但对 <img> 里加载的
 * SVG 能完整光栅化。因此把 SVG 转成 data URI 用 <img> 嵌入更可靠。
 * 同时确保 SVG 有明确 width/height（drawio 导出的 SVG 有时只有 viewBox），
 * 否则 <img> 可能渲染为 0 尺寸。
 */
function svgToImgDataUri(svg: string): string {
  let s = svg.trim()
  // 若无 width/height 但有 viewBox，从 viewBox 提取尺寸写回
  const hasWH = /\swidth=/.test(s) && /\sheight=/.test(s)
  if (!hasWH) {
    const vb = /viewBox=["']([^"']+)["']/.exec(s)
    if (vb) {
      const parts = vb[1].trim().split(/[\s,]+/)
      const w = parts[2] ? Math.ceil(parseFloat(parts[2])) : 800
      const h = parts[3] ? Math.ceil(parseFloat(parts[3])) : 600
      s = s.replace(/<svg/, `<svg width="${w}" height="${h}"`)
    } else {
      // 兜底默认尺寸
      s = s.replace(/<svg/, '<svg width="800" height="600"')
    }
  }
  // 用 encodeURIComponent 处理 SVG（支持 Unicode/特殊字符），避免 btoa 报错
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(s)}`
}

/** 渲染阶段用的 components：drawio 块嵌入真实 SVG（用 <img> data URI，
 *  html2canvas 对此支持远好于内联 <svg>），无 SVG 时降级显示源码 */
function makePrintComponents(svgMap: Map<string, string>): Components {
  return {
    code({ className: langClass, children, ...props }) {
      const match = /language-(\w+)/.exec(langClass || '')
      const lang = match?.[1]
      if (lang === 'drawio' || lang === 'draw') {
        const xml = String(children).replace(/\n$/, '').trim()
        const svg = svgMap.get(xml)
        if (svg) {
          return createElement('div', { className: 'drawio-diagram' }, createElement('img', {
            src: svgToImgDataUri(svg),
            alt: 'draw.io diagram',
            style: { maxWidth: '100%', height: 'auto' },
          }))
        }
        return createElement(
          'div',
          null,
          createElement(
            'p',
            { style: { color: '#8a8a98', fontSize: '12px', margin: '0 0 4px' } },
            'draw.io diagram (source):',
          ),
          createElement('pre', null, xml),
        )
      }
      return createElement('code', { className: langClass, ...props }, children)
    },
  }
}

/** PDF 专用白底浅色样式（独立于 App 主题） */
const PDF_STYLE = `
  .pdf-export-root {
    background: #ffffff;
    color: #1a1d23;
    font-family: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI',
      'PingFang SC', 'Hiragino Sans GB', 'Noto Sans SC',
      'Microsoft YaHei', sans-serif;
    /* 内容宽度对齐整页 A4，页边距靠这里的 padding 留白（html2pdf margin 设为 0） */
    padding: 32px 28px;
  }
  .pdf-export-title {
    font-size: 20px;
    font-weight: 700;
    color: #1a1d23;
    margin-bottom: 16px;
    padding-bottom: 8px;
    border-bottom: 2px solid #e0e0e6;
  }
  .markdown-body {
    font-size: 14px;
    line-height: 1.7;
    color: #1a1d23;
  }
  .markdown-body h1 {
    font-size: 24px; font-weight: 700; margin: 20px 0 12px;
    padding-bottom: 6px; border-bottom: 1px solid #e0e0e6;
  }
  .markdown-body h2 {
    font-size: 20px; font-weight: 600; margin: 18px 0 10px;
    padding-bottom: 4px; border-bottom: 1px solid #eeeeee;
  }
  .markdown-body h3 { font-size: 17px; font-weight: 600; margin: 14px 0 8px; }
  .markdown-body h4 { font-size: 15px; font-weight: 600; margin: 12px 0 6px; }
  .markdown-body p { margin: 8px 0; }
  .markdown-body ul, .markdown-body ol { margin: 8px 0; padding-left: 24px; }
  .markdown-body li { margin: 3px 0; }
  .markdown-body code {
    background: #f3f4f6; padding: 2px 6px; border-radius: 4px;
    font-size: 13px; color: #1f2937;
    font-family: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Monaco, monospace;
  }
  .markdown-body pre {
    background: #f6f8fa; border: 1px solid #e0e0e6; border-radius: 6px;
    padding: 12px; overflow-x: auto; margin: 12px 0;
  }
  .markdown-body pre code { background: none; padding: 0; }
  .markdown-body blockquote {
    border-left: 3px solid #d4a718; padding: 4px 14px; margin: 12px 0;
    color: #4b5563; background: #f8f8fa;
  }
  .markdown-body table {
    border-collapse: collapse; width: 100%; margin: 12px 0;
  }
  .markdown-body th, .markdown-body td {
    border: 1px solid #e0e0e6; padding: 6px 10px; text-align: left; font-size: 13px;
  }
  .markdown-body th { background: #f8f8fa; font-weight: 600; }
  .markdown-body hr { border: none; border-top: 1px solid #e0e0e6; margin: 20px 0; }
  .markdown-body img { max-width: 100%; border-radius: 4px; }
  .markdown-body a { color: #b8941a; text-decoration: none; }
  .drawio-diagram {
    margin: 12px 0;
    text-align: center;
    /* 避免图表在分页处被一分为二（html2pdf css 模式会读取） */
    page-break-inside: avoid;
    break-inside: avoid;
  }
  .drawio-diagram svg { max-width: 100%; height: auto; }
  .drawio-diagram img { page-break-inside: avoid; break-inside: avoid; }
`

export interface ExportOptions {
  /** drawio 图表转换进度回调（done / total） */
  onDrawioProgress?: (done: number, total: number) => void
}

/**
 * 把 Markdown 内容导出为 PDF 并下载。
 *
 * 流程：转换 drawio → 渲染到屏幕外白底容器 → html2pdf 截图生成 PDF → 清理容器。
 *
 * @param title 文档标题（用于 PDF 文件名和顶部标题）
 * @param content Markdown 原文
 */
export async function exportMarkdownToPdf(
  title: string,
  content: string,
  options: ExportOptions = {},
): Promise<void> {
  // 1. 提取并转换 drawio 块为 SVG（若有）
  const drawioXmls = extractDrawioBlocks(content)
  const svgMap =
    drawioXmls.length > 0
      ? await convertDrawioBlocks(drawioXmls, options.onDrawioProgress)
      : new Map<string, string>()

  // 2. 创建屏幕外容器。
  // 关键：外层 host 负责屏幕外定位（position:fixed），内层 pageEl 保持普通文档流
  // （默认 static），只有 pageEl 交给 html2pdf。
  // 原因：html2pdf 会 deepClone 传入元素并塞进自己的 container 里，且不会重置克隆体
  // 的 position。若传入元素本身是 position:fixed，克隆体会脱离 container 的文档流并被
  // 定位到屏幕外，container 高度塌陷为 0，html2canvas 截到空白 → PDF 全空。
  const host = document.createElement('div')
  host.style.position = 'fixed'
  host.style.left = '-99999px'
  host.style.top = '0'
  host.style.zIndex = '-1'

  const pageEl = document.createElement('div')
  pageEl.style.width = '794px' // A4 @ 96dpi 整页宽度（与 html2pdf 容器宽度对齐）
  pageEl.style.background = '#ffffff'
  host.appendChild(pageEl)
  document.body.appendChild(host)

  // 注入样式
  const styleEl = document.createElement('style')
  styleEl.setAttribute('data-pdf-export', 'true')
  styleEl.textContent = PDF_STYLE
  document.head.appendChild(styleEl)

  const root = createRoot(pageEl)
  try {
    // 3. 渲染 React（MarkdownContent + 嵌入的 drawio SVG as <img>）
    await new Promise<void>((resolve) => {
      root.render(
        createElement(
          'div',
          { className: 'pdf-export-root' },
          title ? createElement('div', { className: 'pdf-export-title' }, title) : null,
          createElement(
            'div',
            { className: 'markdown-body' },
            createElement(
              ReactMarkdown,
              { remarkPlugins: REMARK_PLUGINS, components: makePrintComponents(svgMap) },
              content,
            ),
          ),
        ),
      )
      // 等 React 渲染完成 + 一帧布局
      requestAnimationFrame(() =>
        requestAnimationFrame(() => resolve()),
      )
    })

    // 3.5 等待容器内所有 <img>（drawio SVG）加载完成，否则 html2canvas 会截到空白
    await waitForImages(pageEl)

    // 4. html2pdf 生成 PDF 并下载（动态导入，避免拖慢首屏）
    // margin 设为 0：内容宽度已按整页 A4 排版，页边距由 .pdf-export-root 的 padding 承担，
    // 避免 html2pdf 容器内宽（A4 内宽）与内容宽（整页宽）不一致导致右侧内容被裁切。
    const filename = `${sanitizeFilename(title) || 'document'}.pdf`
    const { default: html2pdf } = await import('html2pdf.js')
    // pagebreak 字段 html2pdf.js 运行时支持，但类型定义未涵盖，整体 cast 绕过
    const pdfOptions = {
      margin: [0, 0, 0, 0],
      filename,
      image: { type: 'jpeg', quality: 0.98 },
      html2canvas: {
        scale: 2,
        useCORS: true,
        backgroundColor: '#ffffff',
        logging: false,
      },
      jsPDF: { unit: 'mm', format: 'a4', orientation: 'portrait' as const },
      // 让 drawio 图表不被分页切开：css 模式读取 break-inside:avoid，
      // avoid 选择器在图表会被截断时把整个块下推到下一页
      pagebreak: { mode: ['css', 'legacy'], avoid: ['.drawio-diagram', '.drawio-diagram img'] },
    }
    await html2pdf()
      .set(pdfOptions as unknown as never)
      .from(pageEl)
      .save()
  } finally {
    // 5. 清理
    root.unmount()
    if (host.parentNode) host.parentNode.removeChild(host)
    styleEl.remove()
  }
}

function sanitizeFilename(name: string): string {
  return name.replace(/[\\/:*?"<>|]/g, '_').trim()
}

/** 等待容器内所有 <img> 加载完成（drawio SVG data URI），超时后也放行 */
function waitForImages(container: HTMLElement, timeoutMs = 5000): Promise<void> {
  const imgs = Array.from(container.querySelectorAll('img'))
  if (imgs.length === 0) return Promise.resolve()
  return new Promise<void>((resolve) => {
    let remaining = imgs.length
    let done = false
    const finish = () => {
      if (!done) {
        done = true
        resolve()
      }
    }
    const timer = window.setTimeout(finish, timeoutMs)
    imgs.forEach((img) => {
      if (img.complete) {
        remaining -= 1
        if (remaining === 0) {
          window.clearTimeout(timer)
          finish()
        }
      } else {
        const onDone = () => {
          remaining -= 1
          if (remaining === 0) {
            window.clearTimeout(timer)
            finish()
          }
        }
        img.addEventListener('load', onDone, { once: true })
        img.addEventListener('error', onDone, { once: true })
      }
    })
    // 若全部已 complete
    if (remaining === 0) finish()
  })
}
