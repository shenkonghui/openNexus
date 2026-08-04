import { useEffect, memo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import TerminalPanel from '../components/Terminal'
import ChangesPanel from '../components/ChangesPanel'
import DebugPanel from '../components/DebugPanel'
import BrowserPanel from '../components/BrowserPanel'
import DocWorkspace, { type DocEditMode } from '../components/DocWorkspace'
import WorkspaceFileEditor from '../components/WorkspaceFileEditor'
import FileExplorer from '../components/FileExplorer'
import GitPanel from '../components/GitPanel'
import CapabilityPanel from '../components/CapabilityPanel'
import SessionRecordsPanel from '../components/SessionRecordsPanel'
import { useFileViewer } from '../context/FileViewerContext'
import { Folder, SquareTerminal, Pencil, Bug, MessageSquare, BookOpenText, Globe, GitBranch, Blocks, History } from 'lucide-react'
import type { PanelDef, PanelCtx } from './types'
import ChatPanel from './ChatPanel'

/** 通用空占位（要求会话 / 提示选文档等） */
function EmptyPanel({
  hintKey,
  subKey,
  icon,
}: {
  hintKey: string
  subKey?: string
  icon?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div
      style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        gap: 8,
        padding: 24,
        color: 'var(--text-muted)',
        textAlign: 'center',
        background: 'var(--bg-base)',
      }}
    >
      {icon && <div style={{ opacity: 0.45 }}>{icon}</div>}
      <div style={{ fontSize: 14 }}>{t(hintKey)}</div>
      {subKey && <div style={{ fontSize: 12, maxWidth: 280 }}>{t(subKey)}</div>}
    </div>
  )
}

/**
 * files 面板：左侧文件树（工作区 cwd）+ 右侧选中文件内容。
 * 挂载时向 FileViewer 注册为内嵌查看器，使 AppLayout 不再用主区域覆盖层显示文件。
 */
function FilesView({ cwd }: { cwd: string }) {
  const { openFilePath, openFile, closeFile, registerEmbedded } = useFileViewer()
  useEffect(() => registerEmbedded(), [registerEmbedded])

  const content = openFilePath ? (
    <WorkspaceFileEditor key={openFilePath} path={openFilePath} onClose={closeFile} />
  ) : (
    <EmptyPanel hintKey="fileBrowser.selectHint" icon={<Folder size={40} />} />
  )

  if (!cwd) return content

  return (
    <div style={{ flex: 1, display: 'flex', minHeight: 0, minWidth: 0 }}>
      <div
        style={{
          width: 230,
          flexShrink: 0,
          display: 'flex',
          flexDirection: 'column',
          minHeight: 0,
          borderRight: '1px solid var(--border-subtle)',
          background: 'var(--bg-base)',
        }}
      >
        <FileExplorer rootPath={cwd} onSelectFile={openFile} selectedPath={openFilePath ?? undefined} />
      </div>
      <div style={{ flex: 1, display: 'flex', minWidth: 0, minHeight: 0 }}>{content}</div>
    </div>
  )
}

function renderFiles(ctx: PanelCtx) {
  return <FilesView cwd={ctx.cwd || ''} />
}

function renderTerminal(ctx: PanelCtx) {
  // 任务（会话）尚未开始时降级为工作区终端；会话出现后换 key 重建，切到会话工作目录
  if (!ctx.sessionId && !ctx.workspaceId) return <EmptyPanel hintKey="panel.requireSession" />
  return (
    <TerminalPanel
      key={ctx.sessionId ?? `ws-${ctx.workspaceId}`}
      sessionId={ctx.sessionId}
      workspaceId={ctx.workspaceId}
      onClose={() => {}}
    />
  )
}

function renderChanges(ctx: PanelCtx) {
  if (!ctx.sessionId) return <EmptyPanel hintKey="panel.requireSession" />
  return <ChangesPanel sessionId={ctx.sessionId} onClose={() => {}} refreshKey={ctx.restoreRefreshKey ?? 0} />
}

function renderDebug(ctx: PanelCtx) {
  if (!ctx.sessionId) return <EmptyPanel hintKey="panel.requireSession" />
  return <DebugPanel sessionId={ctx.sessionId} />
}

function renderDocPreview(ctx: PanelCtx) {
  // 仅把渲染相关的原始类型下传，配合 memo 切断 ChatPage 高频重渲染时 ctx 每次重建的级联。
  return (
    <DocPreviewView
      folderId={ctx.docTarget?.folderId ?? ''}
      filePath={ctx.docTarget?.filePath ?? ''}
      reloadKey={ctx.docReloadKey ?? 0}
      forceMode={ctx.docForceMode}
      onCloseDoc={ctx.onCloseDoc}
    />
  )
}

interface DocPreviewViewProps {
  folderId: string
  filePath: string
  reloadKey: number
  forceMode?: DocEditMode
  onCloseDoc?: () => void
}

/**
 * doc-preview 面板：注册为内嵌查看器（抵消 AppLayout 的全屏覆盖层），
 * 使在文档模式下点击左侧「文件」浏览器里的文件也在本面板渲染、不覆盖 AI 对话。
 * 优先级：文件浏览器选中的绝对路径 > 侧边栏「文档」分组的 docTarget。
 * memo：props 均为原始类型且引用稳定，folderId/filePath/reloadKey 不变时跳过重渲染；
 * openFilePath 来自 context，其变化仍会正常触发更新（context 更新不受 memo 阻断）。
 */
const DocPreviewView = memo(function DocPreviewView({
  folderId,
  filePath,
  reloadKey,
  forceMode,
  onCloseDoc,
}: DocPreviewViewProps) {
  const { openFilePath, closeFile, registerEmbedded } = useFileViewer()
  useEffect(() => registerEmbedded(), [registerEmbedded])

  if (openFilePath) {
    return (
      <DocWorkspace
        key={`abs:${openFilePath}`}
        absPath={openFilePath}
        reloadKey={reloadKey}
        onClose={closeFile}
      />
    )
  }
  if (filePath) {
    return (
      <DocWorkspace
        key={`${folderId}:${filePath}`}
        folderId={folderId}
        filePath={filePath}
        reloadKey={reloadKey}
        forceMode={forceMode}
        onClose={onCloseDoc}
      />
    )
  }
  return <EmptyPanel hintKey="docMode.emptyTitle" subKey="docMode.emptyHint" icon={<BookOpenText size={40} />} />
})

/**
 * 对话面板：configBar 样式与空态文案由 ChatPage 通过 ctx.__chatConfig 注入。
 * 这样 PanelDef.render 签名不变（仅依赖 ctx），同时允许不同模式定制对话列外观。
 */
function renderChat(ctx: PanelCtx) {
  type ChatConfig = {
    configBar: 'coding' | 'none'
    emptyTitleKey?: string
    emptyHintKey?: string
    placeholderKey?: string
    configBarNode?: ReactNode
  }
  const cfg = (ctx as PanelCtx & { __chatConfig?: ChatConfig }).__chatConfig
  return (
    <ChatPanel
      ctx={ctx}
      configBar={cfg?.configBar ?? 'none'}
      emptyTitleKey={cfg?.emptyTitleKey}
      emptyHintKey={cfg?.emptyHintKey}
      placeholderKey={cfg?.placeholderKey}
      configBarNode={cfg?.configBarNode}
    />
  )
}

function renderBrowser(ctx: PanelCtx) {
  return <BrowserPanel ctx={ctx} />
}

function renderGit(ctx: PanelCtx) {
  return <GitPanel cwd={ctx.cwd || ''} />
}

function renderCapabilities(ctx: PanelCtx) {
  // 会话已存在时取会话 agent；新建任务页回退到下拉选中的 agent
  const agentType = ctx.session?.agent_type || ctx.selectedAgent || ''
  if (!agentType) return <EmptyPanel hintKey="panel.requireSession" />
  return <CapabilityPanel agentType={agentType} commands={ctx.commands} skills={ctx.skills} cwd={ctx.cwd || ''} onSkillsUploaded={ctx.onSkillsUploaded} />
}

function renderRecords(ctx: PanelCtx) {
  if (!ctx.sessionId) return <EmptyPanel hintKey="panel.requireSession" />
  // 消息条数作为刷新信号：agent 产生新工具调用后列表防抖跟进
  return <SessionRecordsPanel sessionId={ctx.sessionId} refreshSignal={ctx.messages.length} />
}

/** 面板注册表。新增面板在此加一条；新增模式只需在 MODES 引用面板 id。 */
export const PANELS: PanelDef[] = [
  { id: 'chat', titleKey: 'panel.chat', icon: <MessageSquare size={14} />, render: renderChat },
  { id: 'files', titleKey: 'panel.files', icon: <Folder size={14} />, render: renderFiles },
  { id: 'terminal', titleKey: 'panel.terminal', icon: <SquareTerminal size={14} />, render: renderTerminal },
  { id: 'changes', titleKey: 'panel.changes', icon: <Pencil size={14} />, render: renderChanges },
  { id: 'git', titleKey: 'panel.git', icon: <GitBranch size={14} />, render: renderGit },
  { id: 'debug', titleKey: 'panel.debug', icon: <Bug size={14} />, render: renderDebug },
  { id: 'browser', titleKey: 'panel.browser', icon: <Globe size={14} />, render: renderBrowser },
  { id: 'doc-preview', titleKey: 'panel.docPreview', icon: <BookOpenText size={14} />, render: renderDocPreview },
  { id: 'capabilities', titleKey: 'panel.capabilities', icon: <Blocks size={14} />, render: renderCapabilities },
  { id: 'records', titleKey: 'panel.records', icon: <History size={14} />, render: renderRecords },
]
