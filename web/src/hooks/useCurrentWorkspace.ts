import { useState, useEffect, useCallback } from 'react'
import { listWorkspaces, getWorkspace } from '../api/workspaces'
import type { Workspace, Session } from '../types'

export const WORKSPACE_STORAGE_KEY = 'opennexus.current.workspace'

export function resolveWorkspaceId(
  workspaces: (Workspace & { task_count?: number })[],
  stored: string | null,
): number {
  if (stored) {
    const id = Number(stored)
    if (workspaces.some((w) => w.id === id)) return id
  }
  const persistent = workspaces.find((w) => w.mode === 'persistent')
  return persistent?.id ?? workspaces[0]?.id ?? 0
}

export function useCurrentWorkspace(enabled = true) {
  const [workspaces, setWorkspaces] = useState<(Workspace & { task_count?: number })[]>([])
  const [workspaceId, setWorkspaceIdState] = useState(0)
  const [sessions, setSessions] = useState<Session[]>([])
  // sessions 当前归属的 workspace id。切换工作区时新会话为异步加载，
  // 该值用于让消费方判断 sessions 是否已与目标 workspace 匹配，避免用旧数据误判。
  const [sessionsWorkspaceId, setSessionsWorkspaceId] = useState(0)
  const [loading, setLoading] = useState(true)

  const persistId = useCallback((id: number) => {
    setWorkspaceIdState(id)
    localStorage.setItem(WORKSPACE_STORAGE_KEY, String(id))
  }, [])

  const loadSessions = useCallback(async (id: number) => {
    if (!id) { setSessions([]); setSessionsWorkspaceId(id); return }
    const detail = await getWorkspace(id)
    setSessions(detail.data.sessions || [])
    setSessionsWorkspaceId(id)
  }, [])

  const reload = useCallback(async () => {
    if (!enabled) return
    setLoading(true)
    try {
      const list = (await listWorkspaces()).data.workspaces || []
      setWorkspaces(list)
      const id = resolveWorkspaceId(list, localStorage.getItem(WORKSPACE_STORAGE_KEY))
      persistId(id)
      await loadSessions(id)
    } finally {
      setLoading(false)
    }
  }, [enabled, loadSessions, persistId])

  useEffect(() => { reload() }, [reload])

  // 定期刷新会话列表，使侧边栏的会话状态（error→active、标题等）不再永久过期。
  useEffect(() => {
    if (!enabled) return
    const timer = setInterval(() => { reload() }, 10000)
    return () => clearInterval(timer)
  }, [enabled, reload])

  const selectWorkspace = useCallback(async (id: number) => {
    persistId(id)
    await loadSessions(id)
  }, [loadSessions, persistId])

  return { workspaces, workspaceId, sessions, sessionsWorkspaceId, loading, reload, selectWorkspace }
}
