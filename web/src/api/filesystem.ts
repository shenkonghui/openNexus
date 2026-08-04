import { apiFetch, apiFetchRaw } from './client'
import type { AgentCommand, AgentSkill, DocFileEntry } from '../types'

// 目录项
export interface DirEntry {
  name: string
  path: string
}

// 目录列表响应
export interface DirListResponse {
  current_path: string
  parent_path: string
  dirs: DirEntry[]
}

// 文件/目录项（用于 @ 文件引用）
export interface FileEntry {
  name: string
  path: string
  is_dir: boolean
}

// 文件列表响应
export interface FileListResponse {
  current_path: string
  parent_path: string
  entries: FileEntry[]
}

// 列出指定路径下的子目录（path 为空时从用户主目录开始）
export function listDirs(path?: string): Promise<{ data: DirListResponse }> {
  const query = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/dirs${query}`)
}

// git worktree 项（来自 git worktree list）
export interface WorktreeEntry {
  path: string
  branch: string
  head: string
  is_main: boolean
  detached: boolean
  bare: boolean
}

// worktree 列表响应
export interface WorktreeListResponse {
  repo_root: string
  worktrees: WorktreeEntry[]
}

// 列出 path 所在 git 仓库的全部 worktree（path 为仓库内任意目录，通常是工作区 cwd）
export function listWorktrees(path: string): Promise<{ data: WorktreeListResponse }> {
  return apiFetch(`/filesystem/worktrees?path=${encodeURIComponent(path)}`)
}

// 在 path 所在仓库创建新 worktree：新分支 branch 检出到 ~/.openNexus/worktrees/<repo>/<branch>
// base 为空时从当前 HEAD 创建。返回新建的 worktree 项。
export function createWorktree(path: string, branch: string, base?: string): Promise<{ data: { worktree: WorktreeEntry } }> {
  return apiFetch(`/filesystem/worktrees`, {
    method: 'POST',
    body: JSON.stringify({ path, branch, base }),
  })
}

// 上传文件项（拖拽接入对话 —— 远程运行场景）
export interface UploadedFile {
  name: string // 原始文件名
  path: string // 服务器侧绝对路径，前端以 @<path> 引用
  size: number
}

// 把浏览器端拖拽的文件上传到 workspace.Cwd/.uploads/ 下，返回服务器侧绝对路径。
// 仅在"远程运行"(浏览器)场景下调用;本地(Electron)场景直接用 window.opennexus.getPathForFile 取绝对路径。
// 注意:不能复用 apiFetch(它强制设 Content-Type: application/json),FormData 必须让浏览器自动设置 boundary。
export async function uploadFilesToWorkspace(
  workspaceId: number,
  files: File[],
): Promise<{ data: { files: UploadedFile[] } }> {
  const form = new FormData()
  for (const f of files) {
    form.append('files', f, f.name)
  }
  const resp = await apiFetchRaw(`/workspaces/${workspaceId}/uploads`, {
    method: 'POST',
    body: form,
    // 不设 Content-Type，让 fetch 根据 FormData 自动带 multipart boundary
  })
  return resp.json()
}

// 列出指定路径下的文件和目录（用于 @ 文件引用补全）
export function listFiles(path?: string, query?: string): Promise<{ data: FileListResponse }> {
  const params = new URLSearchParams()
  if (path) params.set('path', path)
  if (query) params.set('query', query)
  const qs = params.toString()
  return apiFetch(`/filesystem/list${qs ? `?${qs}` : ''}`)
}

// 扫描指定目录下的 Slash Commands（Claude Code 规范；path 为空时仅扫用户目录）
export function listCommandsByPath(path?: string): Promise<{ data: { commands: AgentCommand[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/commands${qs}`)
}

// 扫描指定目录下的 Agent Skills（新建任务页 / 命令补全用；path 为空时仅扫用户目录）
export function listSkillsByPath(path?: string): Promise<{ data: { skills: AgentSkill[] } }> {
  const qs = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/filesystem/skills${qs}`)
}

// ===== 会话工作目录文件 API =====

// 会话文件树节点
export interface SessionFileEntry {
  name: string
  path: string // 相对 session cwd 的路径
  is_dir: boolean
  size?: number
}

// 会话文件树响应
export interface SessionFileTreeResponse {
  cwd: string
  path: string
  entries: SessionFileEntry[]
}

// 会话文件内容响应
export interface SessionFileContentResponse {
  path: string
  content: string
  size: number
}

// 列出会话工作目录下的文件和目录（单层，懒加载）
export function listSessionFiles(sessionId: number, path?: string): Promise<{ data: SessionFileTreeResponse }> {
  const query = path ? `?path=${encodeURIComponent(path)}` : ''
  return apiFetch(`/sessions/${sessionId}/files${query}`)
}

// 读取会话工作目录下的文件内容
export function readSessionFile(sessionId: number, path: string): Promise<{ data: SessionFileContentResponse }> {
  return apiFetch(`/sessions/${sessionId}/files/content?path=${encodeURIComponent(path)}`)
}

// 保存文件内容到会话工作目录
export function writeSessionFile(sessionId: number, path: string, content: string): Promise<{ data: { path: string; size: number } }> {
  return apiFetch(`/sessions/${sessionId}/files/content`, {
    method: 'PUT',
    body: JSON.stringify({ path, content }),
  })
}

// 撤销指定快照消息记录的文件改动（恢复修改前内容 / 删除新建文件）
export function undoFileChanges(
  sessionId: number,
  messageId: number,
): Promise<{ data: { restored: number; deleted: number; errors: string[] } }> {
  return apiFetch(`/sessions/${sessionId}/files/undo`, {
    method: 'POST',
    body: JSON.stringify({ message_id: messageId }),
  })
}

// 恢复工作区到指定用户消息发送前的状态，删除该消息及后续消息，返回该消息文本
export function restoreToCheckpoint(
  sessionId: number,
  sequence: number,
): Promise<{ data: { restored: number; deleted: number; turns_reverted: number; messages_deleted: number; prompt_text: string; errors: string[] } }> {
  return apiFetch(`/sessions/${sessionId}/files/restore`, {
    method: 'POST',
    body: JSON.stringify({ sequence }),
  })
}

// 文件改动项（后端从持久化快照消息聚合）
export interface FileChangeEntry {
  path: string
  old_text: string
  new_text: string
  is_new: boolean
}

// 从后端获取会话所有文件改动（基于持久化快照消息，不依赖前端内存）
export function listFileChanges(
  sessionId: number,
): Promise<{ data: { changes: FileChangeEntry[]; count: number } }> {
  return apiFetch(`/sessions/${sessionId}/files/changes`)
}

// ===== 工作区文件读取 API（文档查看器用） =====

// 工作区文件内容响应
export interface WorkspaceFileContentResponse {
  path: string
  content: string
  size: number
}

// 读取工作区中的文件（通过绝对路径）
export function readWorkspaceFile(path: string): Promise<{ data: WorkspaceFileContentResponse }> {
  return apiFetch(`/filesystem/file?path=${encodeURIComponent(path)}`)
}

// 写入工作区中的文件（通过绝对路径）。文档编辑器保存用。
export function writeWorkspaceFile(path: string, content: string): Promise<{ data: { path: string; size: number } }> {
  return apiFetch(`/filesystem/file`, {
    method: 'PUT',
    body: JSON.stringify({ path, content }),
  })
}

// 在工作区中新建文件或目录（通过绝对路径）。
export function createWorkspaceEntry(path: string, isDir: boolean): Promise<{ data: { path: string; is_dir: boolean } }> {
  return apiFetch(`/filesystem/create`, {
    method: 'POST',
    body: JSON.stringify({ path, is_dir: isDir }),
  })
}

// 删除工作区中的文件或目录（通过绝对路径，目录递归删除）。
export function deleteWorkspaceEntry(path: string): Promise<{ data: { path: string; deleted: boolean } }> {
  return apiFetch(`/filesystem/entry?path=${encodeURIComponent(path)}`, {
    method: 'DELETE',
  })
}

// 文档扫描响应：递归列出目录下所有 .md 文件
export interface DocScanResponse {
  root: string
  files: DocFileEntry[]
  truncated: boolean
}

// 递归扫描指定目录下所有 .md 文件（含子目录），用于侧边栏文档文件夹绑定后展开列出文档。
export function listDocs(path: string): Promise<{ data: DocScanResponse }> {
  return apiFetch(`/filesystem/docs?path=${encodeURIComponent(path)}`)
}
