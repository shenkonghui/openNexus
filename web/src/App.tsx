import { useEffect, lazy, Suspense } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useParams, useNavigate, useSearchParams } from 'react-router-dom'
import { AuthProvider } from './context/AuthContext'
import { ThemeProvider } from './context/ThemeContext'
import { FileViewerProvider } from './context/FileViewerContext'
import { WORKSPACE_STORAGE_KEY } from './hooks/useCurrentWorkspace'
import { loadDocFolders } from './utils/docs'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'
import ScheduledTasksPage from './pages/ScheduledTasksPage'
import NotesPage from './pages/NotesPage'
import ProfilePage from './pages/ProfilePage'
import SessionRedirect from './components/SessionRedirect'
import LoadingSpinner from './components/LoadingSpinner'

// MCP 网关页独立代码分割：访问 /mcp-gateway 时才加载，减小首屏 bundle。
const McpGatewayPage = lazy(() => import('./pages/McpGatewayPage'))

function WorkspaceHomeRedirect() {
  const { wid } = useParams<{ wid: string }>()
  useEffect(() => {
    if (wid) localStorage.setItem(WORKSPACE_STORAGE_KEY, wid)
  }, [wid])
  // 重定向到该工作区的任务列表
  return <Navigate to={wid ? `/workspaces/${wid}/tasks` : '/'} replace />
}

// 兼容旧的 /docs/:folderId/* 分享链接：定位到对应工作区后跳转到任务页文档模式。
function DocRedirect() {
  const { folderId, '*': filePath } = useParams<{ folderId: string; '*': string }>()
  const navigate = useNavigate()
  useEffect(() => {
    const folders = loadDocFolders()
    const found = folders.find((d) => d.id === folderId)
    const wid = found?.workspaceId
    navigate(`/workspaces/${wid || ''}/tasks`, {
      replace: true,
      state: folderId && filePath ? { doc: { folderId, filePath } } : undefined,
    })
  }, [folderId, filePath, navigate])
  return null
}

// 兼容旧的 /settings?tab=x 链接：设置已改为全局弹窗，重定向到首页并带弹窗参数。
function SettingsRedirect() {
  const [searchParams] = useSearchParams()
  const tab = searchParams.get('tab')
  return <Navigate to={`/?settings=1${tab ? `&settingsTab=${tab}` : ''}`} replace />
}

export default function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <FileViewerProvider>
        <BrowserRouter>
        <Routes>
          <Route path="/" element={<ChatPage />} />
          <Route path="/new" element={<ChatPage />} />
          <Route path="/login" element={<LoginPage />} />
          <Route path="/workspaces/:wid" element={<WorkspaceHomeRedirect />} />
          <Route path="/workspaces/:wid/tasks" element={<ChatPage />} />
          <Route path="/workspaces/:wid/tasks/new" element={<ChatPage />} />
          <Route path="/workspaces/:wid/sessions/:sid" element={<ChatPage />} />
          <Route path="/sessions/:id" element={<SessionRedirect />} />
          <Route path="/scheduled-tasks" element={<ScheduledTasksPage />} />
          <Route path="/orchestration" element={<ChatPage />} />
          <Route path="/workspaces/:wid/orchestration" element={<ChatPage />} />
          <Route path="/notes" element={<NotesPage />} />
          <Route path="/docs/:folderId/*" element={<DocRedirect />} />
          <Route path="/profile" element={<ProfilePage />} />
          <Route path="/settings" element={<SettingsRedirect />} />
          <Route
            path="/mcp-gateway"
            element={
              <Suspense fallback={<LoadingSpinner />}>
                <McpGatewayPage />
              </Suspense>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
        </BrowserRouter>
        </FileViewerProvider>
      </AuthProvider>
    </ThemeProvider>
  )
}
