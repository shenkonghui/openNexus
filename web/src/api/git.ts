import { apiFetch } from './client'

// Git 只读查询 API（Git 管理面板：worktree 提交记录与文件 diff）

// 单条提交记录
export interface GitCommit {
  hash: string
  short_hash: string
  author: string
  date: number // unix 秒
  subject: string
}

// 提交（或未提交改动）中的单个文件变更
export interface GitFileEntry {
  path: string
  status: string // A/M/D/R/C/?（? 表示未跟踪）
  added: number // 增行数；二进制为 -1
  removed: number
}

// 单文件 diff 内容
export interface GitDiffResponse {
  old_text: string | null // null 表示新文件
  new_text: string
  new_exists: boolean // false 表示文件被删除
  is_binary: boolean
  too_large: boolean
}

// 提交记录（path 为 worktree 内任意路径）
export function gitLog(path: string, limit = 100): Promise<{ data: { branch: string; commits: GitCommit[] } }> {
  return apiFetch(`/git/log?path=${encodeURIComponent(path)}&limit=${limit}`)
}

// 未提交改动文件列表
export function gitStatus(path: string): Promise<{ data: { files: GitFileEntry[] } }> {
  return apiFetch(`/git/status?path=${encodeURIComponent(path)}`)
}

// 单个提交的文件变更列表
export function gitCommitFiles(path: string, hash: string): Promise<{ data: { files: GitFileEntry[] } }> {
  return apiFetch(`/git/commit-files?path=${encodeURIComponent(path)}&hash=${encodeURIComponent(hash)}`)
}

// 单文件 diff（hash 为空 = 未提交改动 vs HEAD）
export function gitDiff(path: string, file: string, hash?: string): Promise<{ data: GitDiffResponse }> {
  const h = hash ? `&hash=${encodeURIComponent(hash)}` : ''
  return apiFetch(`/git/diff?path=${encodeURIComponent(path)}&file=${encodeURIComponent(file)}${h}`)
}
