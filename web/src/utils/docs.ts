import type { DocFolder } from '../types'

// localStorage key：所有文档文件夹绑定（跨工作区存储）
export const DOCS_KEY = 'opennexus.documents'

// 「上次打开的文档」按工作区记忆的前缀
export const LAST_DOC_KEY_PREFIX = 'opennexus.lastDoc.'

// 从 localStorage 加载所有文档文件夹绑定
export function loadDocFolders(): DocFolder[] {
  try {
    const raw = localStorage.getItem(DOCS_KEY)
    if (raw) return JSON.parse(raw) as DocFolder[]
  } catch { /* ignore */ }
  return []
}

// 保存文档文件夹绑定到 localStorage
export function saveDocFolders(docs: DocFolder[]): void {
  try { localStorage.setItem(DOCS_KEY, JSON.stringify(docs)) } catch { /* ignore */ }
}

// 文档目标（folderId + filePath），用于任务页打开指定文档
export interface DocTarget {
  folderId: string
  filePath: string
}

// 读取某工作区上次打开的文档
export function loadLastDoc(workspaceId: number): DocTarget | null {
  try {
    const raw = localStorage.getItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0))
    if (raw) return JSON.parse(raw) as DocTarget
  } catch { /* ignore */ }
  return null
}

// 记忆某工作区上次打开的文档
export function saveLastDoc(workspaceId: number, doc: DocTarget): void {
  try {
    localStorage.setItem(LAST_DOC_KEY_PREFIX + (workspaceId || 0), JSON.stringify(doc))
  } catch { /* ignore */ }
}
