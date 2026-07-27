import { useState, useEffect, useMemo, useRef, Fragment, type MouseEvent as ReactMouseEvent } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus, X } from 'lucide-react'
import type { LayoutNode, PanelCtx } from './types'
import { getPANELS } from './registry'
import styles from './LayoutRenderer.module.css'

/**
 * 递归渲染布局树。
 * - leaf → 查 PANELS 注册表渲染单个面板
 * - split → 按 dir 方向排列子节点，子节点之间插入可拖拽分隔条调整大小
 * - tabs → 标签组：所有面板保持挂载、用 display 切换可见性（保活终端 WS）
 *
 * hiddenPanels：需要隐藏的面板 id 集合。当某棵子树的面板全部在集合中时，
 * 整棵子树（连同其拖拽分隔条）都不渲染——用于"隐藏左侧列只保留对话"。
 */
export default function LayoutRenderer({ node, ctx, hiddenPanels }: { node: LayoutNode; ctx: PanelCtx; hiddenPanels?: Set<string> }) {
  return <RenderNode node={node} ctx={ctx} hiddenPanels={hiddenPanels} />
}

/** 收集某棵子树下所有 leaf 面板 id（用于判断子树是否整体隐藏） */
function collectPanels(node: LayoutNode): string[] {
  switch (node.kind) {
    case 'leaf':
      return [node.panel]
    case 'tabs':
      return [...node.panels, ...(node.optional ?? [])]
    case 'split':
      return node.children.flatMap(collectPanels)
  }
}

/** 子树是否整体隐藏：其包含的面板全部都在 hiddenPanels 中 */
function isSubtreeHidden(node: LayoutNode, hiddenPanels?: Set<string>): boolean {
  if (!hiddenPanels || hiddenPanels.size === 0) return false
  return collectPanels(node).every((p) => hiddenPanels.has(p))
}

/** flex 覆盖值：由父级 split 拖拽后下发，优先于节点自身 flex */
function RenderNode({ node, ctx, flex, hiddenPanels }: { node: LayoutNode; ctx: PanelCtx; flex?: number; hiddenPanels?: Set<string> }) {
  // 隐藏的子树直接不渲染（父级 SplitView 也会跳过它的分隔条）
  if (isSubtreeHidden(node, hiddenPanels)) return null
  switch (node.kind) {
    case 'leaf':
      return <LeafView node={node} ctx={ctx} flex={flex} />
    case 'split':
      return <SplitView node={node} ctx={ctx} flex={flex} hiddenPanels={hiddenPanels} />
    case 'tabs':
      return <TabsView node={node} ctx={ctx} flex={flex} hiddenPanels={hiddenPanels} />
  }
}

/* ============ split：可拖拽调整子节点大小 ============ */

const SPLIT_STORE_PREFIX = 'opennexus.split.'
const MIN_FLEX = 0.15

/** 为 split 生成稳定 key：方向 + 各子节点结构签名（用于持久化拖拽后的比例） */
function splitSignature(node: Extract<LayoutNode, { kind: 'split' }>): string {
  return node.dir + ':' + node.children.map(childSignature).join('|')
}

function childSignature(n: LayoutNode): string {
  switch (n.kind) {
    case 'leaf':
      return 'L(' + n.panel + ')'
    case 'tabs':
      return 'T(' + [...n.panels, ...(n.optional ?? [])].join(',') + ')'
    case 'split':
      return 'S(' + n.dir + '[' + n.children.map(childSignature).join('|') + '])'
  }
}

function defaultFlexes(node: Extract<LayoutNode, { kind: 'split' }>): number[] {
  return node.children.map((c) => (typeof c.flex === 'number' ? c.flex : 1))
}

function loadFlexes(key: string, node: Extract<LayoutNode, { kind: 'split' }>): number[] {
  try {
    const raw = localStorage.getItem(SPLIT_STORE_PREFIX + key)
    if (raw) {
      const arr = JSON.parse(raw)
      if (Array.isArray(arr) && arr.length === node.children.length && arr.every((v) => typeof v === 'number' && v > 0)) {
        return arr
      }
    }
  } catch {
    /* ignore */
  }
  return defaultFlexes(node)
}

function saveFlexes(key: string, flexes: number[]) {
  try {
    localStorage.setItem(SPLIT_STORE_PREFIX + key, JSON.stringify(flexes))
  } catch {
    /* ignore */
  }
}

function SplitView({ node, ctx, flex, hiddenPanels }: { node: Extract<LayoutNode, { kind: 'split' }>; ctx: PanelCtx; flex?: number; hiddenPanels?: Set<string> }) {
  const isRow = node.dir === 'row'
  const containerRef = useRef<HTMLDivElement>(null)
  const key = useMemo(() => splitSignature(node), [node])
  const [flexes, setFlexes] = useState<number[]>(() => loadFlexes(key, node))

  // 布局结构变化（模式/面板列表改变）时重置为存储值或默认值
  useEffect(() => {
    setFlexes(loadFlexes(key, node))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  // 仅渲染可见子节点（隐藏子树被过滤），resizer 只插在相邻可见子节点之间
  const visible = node.children
    .map((child, i) => ({ child, i }))
    .filter(({ child }) => !isSubtreeHidden(child, hiddenPanels))

  function startDrag(index: number, e: ReactMouseEvent) {
    e.preventDefault()
    const container = containerRef.current
    if (!container) return
    const rect = container.getBoundingClientRect()
    const size = isRow ? rect.width : rect.height
    if (size <= 0) return
    const startPos = isRow ? e.clientX : e.clientY
    const startFlexes = flexes.slice()
    const total = startFlexes.reduce((a, b) => a + b, 0)
    const flexPerPx = total / size
    let current = startFlexes

    function onMove(ev: MouseEvent) {
      const pos = isRow ? ev.clientX : ev.clientY
      let delta = (pos - startPos) * flexPerPx
      // 夹紧：两侧都不小于 MIN_FLEX
      delta = Math.max(delta, MIN_FLEX - startFlexes[index])
      delta = Math.min(delta, startFlexes[index + 1] - MIN_FLEX)
      const next = startFlexes.slice()
      next[index] = startFlexes[index] + delta
      next[index + 1] = startFlexes[index + 1] - delta
      current = next
      setFlexes(next)
    }
    function onUp() {
      document.removeEventListener('mousemove', onMove)
      document.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
      document.body.style.userSelect = ''
      saveFlexes(key, current)
    }
    document.addEventListener('mousemove', onMove)
    document.addEventListener('mouseup', onUp)
    document.body.style.cursor = isRow ? 'col-resize' : 'row-resize'
    document.body.style.userSelect = 'none'
  }

  return (
    <div
      ref={containerRef}
      className={isRow ? styles.row : styles.col}
      style={{ flex: flex ?? node.flex ?? 1 }}
    >
      {visible.map(({ child, i }, vi) => (
        <Fragment key={i}>
          <RenderNode node={child} ctx={ctx} flex={flexes[i] ?? 1} hiddenPanels={hiddenPanels} />
          {vi < visible.length - 1 && (
            <div
              className={isRow ? styles.resizerV : styles.resizerH}
              onMouseDown={(e) => startDrag(i, e)}
              role="separator"
              aria-orientation={isRow ? 'vertical' : 'horizontal'}
            />
          )}
        </Fragment>
      ))}
    </div>
  )
}

function LeafView({ node, ctx, flex }: { node: Extract<LayoutNode, { kind: 'leaf' }>; ctx: PanelCtx; flex?: number }) {
  const panel = getPANELS().find((p) => p.id === node.panel)
  if (!panel) {
    return (
      <div className={styles.missing} style={{ flex: flex ?? node.flex ?? 1 }}>
        未知面板: {node.panel}
      </div>
    )
  }
  return (
    <div className={styles.leaf} style={{ flex: flex ?? node.flex ?? 1 }}>
      {panel.render(ctx)}
    </div>
  )
}

/**
 * 标签组视图。所有可见子面板一次性挂载，用 display 切换可见性——这样切换 tab 时
 * 终端的 WebSocket、xterm 实例、文件树展开状态等都得以保留。
 * optional 面板默认不展示，通过标签栏右侧「+」打开（可关闭，选择存 localStorage）。
 */

const TABS_OPEN_STORE_PREFIX = 'opennexus.tabs.open.'

function loadOpenOptional(key: string, optional: string[]): string[] {
  try {
    const raw = localStorage.getItem(TABS_OPEN_STORE_PREFIX + key)
    if (raw) {
      const arr = JSON.parse(raw)
      if (Array.isArray(arr)) return arr.filter((v) => typeof v === 'string' && optional.includes(v))
    }
  } catch {
    /* ignore */
  }
  return []
}

function saveOpenOptional(key: string, opened: string[]) {
  try {
    localStorage.setItem(TABS_OPEN_STORE_PREFIX + key, JSON.stringify(opened))
  } catch {
    /* ignore */
  }
}

function TabsView({ node, ctx, flex }: { node: Extract<LayoutNode, { kind: 'tabs' }>; ctx: PanelCtx; flex?: number; hiddenPanels?: Set<string> }) {
  const { t } = useTranslation()
  const PANELS = getPANELS()
  const optional = useMemo(() => node.optional ?? [], [node.optional])
  const storeKey = useMemo(() => [...node.panels, ...optional].join(','), [node.panels, optional])
  // 已打开的 optional 面板（持久化，按打开顺序追加在固定标签之后）
  const [openOptional, setOpenOptional] = useState<string[]>(() => loadOpenOptional(storeKey, optional))
  const visiblePanels = useMemo(() => [...node.panels, ...openOptional], [node.panels, openOptional])
  const initial = node.defaultTab && visiblePanels.includes(node.defaultTab) ? node.defaultTab : visiblePanels[0]
  const [active, setActive] = useState(initial)
  // 「+」下拉列表开关（点击外部关闭）
  const [addOpen, setAddOpen] = useState(false)
  const addRef = useRef<HTMLDivElement>(null)

  function openPanel(id: string) {
    setOpenOptional((prev) => {
      if (prev.includes(id)) return prev
      const next = [...prev, id]
      saveOpenOptional(storeKey, next)
      return next
    })
    setActive(id)
    setAddOpen(false)
  }

  function closePanel(id: string) {
    setOpenOptional((prev) => {
      const next = prev.filter((p) => p !== id)
      saveOpenOptional(storeKey, next)
      return next
    })
    if (active === id) {
      setActive(node.defaultTab && node.panels.includes(node.defaultTab) ? node.defaultTab : node.panels[0])
    }
  }

  // 切换模式/布局时如果当前激活 tab 不在新列表里，回到默认
  useEffect(() => {
    if (!visiblePanels.includes(active)) {
      const next = node.defaultTab && visiblePanels.includes(node.defaultTab) ? node.defaultTab : visiblePanels[0]
      setActive(next)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visiblePanels.join(','), node.defaultTab])

  // 监听全局面板激活事件（如 agent 执行 shell 时自动弹出终端面板）；
  // 目标是未打开的 optional 面板时自动打开（如侧边栏点文档激活 doc-preview）
  useEffect(() => {
    const onActivate = (e: Event) => {
      const panelId = (e as CustomEvent<{ panelId?: string }>).detail?.panelId
      if (!panelId) return
      if (visiblePanels.includes(panelId)) {
        setActive(panelId)
      } else if (optional.includes(panelId)) {
        openPanel(panelId)
      }
    }
    window.addEventListener('onx:activate-panel', onActivate)
    return () => window.removeEventListener('onx:activate-panel', onActivate)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visiblePanels.join(','), optional.join(',')])

  // 点击下拉外部时关闭
  useEffect(() => {
    if (!addOpen) return
    const onDocClick = (e: MouseEvent) => {
      if (addRef.current && !addRef.current.contains(e.target as Node)) setAddOpen(false)
    }
    document.addEventListener('mousedown', onDocClick)
    return () => document.removeEventListener('mousedown', onDocClick)
  }, [addOpen])

  const closable = optional.filter((id) => !openOptional.includes(id))

  return (
    <div className={styles.tabs} style={{ flex: flex ?? node.flex ?? 1 }}>
      <div className={styles.tabNav}>
        {visiblePanels.map((id) => {
          const def = PANELS.find((p) => p.id === id)
          if (!def) return null
          const isOptional = optional.includes(id)
          return (
            <button
              key={id}
              type="button"
              className={`${styles.tab} ${active === id ? styles.tabActive : ''}`}
              onClick={() => setActive(id)}
              title={t(def.titleKey)}
            >
              <span className={styles.tabIcon}>{def.icon}</span>
              <span className={styles.tabLabel}>{t(def.titleKey)}</span>
              {isOptional && (
                <span
                  className={styles.tabClose}
                  role="button"
                  aria-label={t('panel.closeTab')}
                  onClick={(e) => {
                    e.stopPropagation()
                    closePanel(id)
                  }}
                >
                  <X size={11} />
                </span>
              )}
            </button>
          )
        })}
        {closable.length > 0 && (
          <div className={styles.tabAddWrap} ref={addRef}>
            <button
              type="button"
              className={styles.tabAdd}
              onClick={() => setAddOpen((v) => !v)}
              title={t('panel.addTab')}
              aria-label={t('panel.addTab')}
            >
              <Plus size={14} />
            </button>
            {addOpen && (
              <div className={styles.tabAddMenu}>
                {closable.map((id) => {
                  const def = PANELS.find((p) => p.id === id)
                  if (!def) return null
                  return (
                    <button key={id} type="button" className={styles.tabAddItem} onClick={() => openPanel(id)}>
                      <span className={styles.tabIcon}>{def.icon}</span>
                      <span className={styles.tabLabel}>{t(def.titleKey)}</span>
                    </button>
                  )
                })}
              </div>
            )}
          </div>
        )}
      </div>
      <div className={styles.tabBody}>
        {visiblePanels.map((id) => {
          const def = PANELS.find((p) => p.id === id)
          if (!def) return null
          return (
            <div
              key={id}
              className={styles.tabPane}
              style={{ display: active === id ? 'flex' : 'none' }}
            >
              {def.render(ctx)}
            </div>
          )
        })}
      </div>
    </div>
  )
}
