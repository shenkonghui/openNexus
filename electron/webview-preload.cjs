const { ipcRenderer } = require('electron')

// ===== 选区（鼠标拖选文本） =====
function selectedText() {
  return window.getSelection().toString()
}

function selectedHTML() {
  const sel = window.getSelection()
  if (!sel || sel.rangeCount === 0) return ''
  const div = document.createElement('div')
  div.appendChild(sel.getRangeAt(0).cloneContents())
  return div.innerHTML
}

function reportSelection() {
  ipcRenderer.sendToHost('selection', {
    text: selectedText(),
    html: selectedHTML(),
  })
}

// ===== 元素选择器（箭头模式） =====
let pickerActive = false
let lastHighlight = null

function clearHighlight() {
  if (lastHighlight && lastHighlight.parentNode) {
    // 还原原节点
    lastHighlight.parentNode.replaceChild(lastHighlight._origNode || lastHighlight, lastHighlight)
  }
  lastHighlight = null
}

function describeElement(el) {
  const tag = el.tagName.toLowerCase()
  const id = el.id ? `#${el.id}` : ''
  const cls = el.className && typeof el.className === 'string'
    ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.')
    : ''
  const text = (el.innerText || '').trim().replace(/\s+/g, ' ')
  const summary = text.length > 200 ? text.slice(0, 200) + '...' : text
  return {
    selector: `${tag}${id}${cls}`,
    text: summary,
    html: el.outerHTML.length > 2000 ? el.outerHTML.slice(0, 2000) + '...' : el.outerHTML,
    fullText: text,
  }
}

function onMouseMove(e) {
  if (!pickerActive) return
  if (e.target === lastHighlight) return
  clearHighlight()
  const el = e.target
  if (!el || el === document.body || el === document.documentElement) return
  // 用 outline 高亮，不破坏布局
  el._origOutline = el.style.outline
  el._origOutlineOffset = el.style.outlineOffset
  el._origBg = el.style.backgroundColor
  el.style.outline = '2px solid #ff6b35'
  el.style.outlineOffset = '-2px'
  el.style.backgroundColor = 'rgba(255,107,53,0.12)'
  lastHighlight = el
}

function onPickerClick(e) {
  if (!pickerActive) return
  e.preventDefault()
  e.stopPropagation()
  const el = e.target
  clearHighlight()
  const info = describeElement(el)
  ipcRenderer.sendToHost('element-picked', info)
  setPickerActive(false)
}

function onKeyUp(e) {
  if (e.key === 'Escape' && pickerActive) {
    clearHighlight()
    setPickerActive(false)
  }
}

function setPickerActive(active) {
  pickerActive = active
  if (active) {
    document.body.style.cursor = 'crosshair'
    document.addEventListener('mousemove', onMouseMove, true)
    document.addEventListener('click', onPickerClick, true)
    document.addEventListener('keyup', onKeyUp, true)
  } else {
    clearHighlight()
    document.body.style.cursor = ''
    document.removeEventListener('mousemove', onMouseMove, true)
    document.removeEventListener('click', onPickerClick, true)
    document.removeEventListener('keyup', onKeyUp, true)
  }
}

// ===== 链接拦截（页内跳转） =====
function absoluteURL(url) {
  try {
    return new URL(url, location.href).href
  } catch {
    return url
  }
}

function onClick(e) {
  if (pickerActive) return // 选择器模式下不拦截链接
  const a = e.target.closest && e.target.closest('a[href]')
  if (!a) return
  const href = a.getAttribute('href') || ''
  if (href.startsWith('#') || href.startsWith('javascript:')) return
  e.preventDefault()
  e.stopPropagation()
  ipcRenderer.sendToHost('navigate', absoluteURL(href))
}

function onContextMenu() {
  reportSelection()
}

document.addEventListener('selectionchange', reportSelection)
document.addEventListener('mouseup', reportSelection)
document.addEventListener('click', onClick, true)
document.addEventListener('contextmenu', onContextMenu)

ipcRenderer.on('request-selection', () => reportSelection())
ipcRenderer.on('toggle-picker', (_, active) => setPickerActive(active))
ipcRenderer.on('go-back', () => {
  if (history.length > 1) history.back()
})
