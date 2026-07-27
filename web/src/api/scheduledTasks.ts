import type { ScheduledTask, Execution } from '../types'
import { apiFetch } from './client'

// 创建定时任务
export function createScheduledTask(payload: {
  name: string
  agent_type: string
  workspace_id: number
  prompt: string
  cron_expr: string
  enabled?: boolean
  model_value?: string
  timeout_minutes?: number
}): Promise<{ data: ScheduledTask }> {
  return apiFetch('/scheduled-tasks', {
    method: 'POST',
    body: JSON.stringify(payload),
  })
}

// 列出定时任务，支持按 workspace_id 过滤
export function listScheduledTasks(workspaceId?: number): Promise<{ data: { tasks: ScheduledTask[] } }> {
  const query = workspaceId ? `?workspace_id=${workspaceId}` : ''
  return apiFetch(`/scheduled-tasks${query}`)
}

// 获取单个定时任务
export function getScheduledTask(id: string): Promise<{ data: ScheduledTask }> {
  return apiFetch(`/scheduled-tasks/${encodeURIComponent(id)}`)
}

// 更新定时任务
export function updateScheduledTask(
  id: string,
  payload: Partial<{
    name: string
    agent_type: string
    workspace_id: number
    prompt: string
    cron_expr: string
    enabled: boolean
    model_value: string
    timeout_minutes: number
  }>,
): Promise<{ data: ScheduledTask }> {
  return apiFetch(`/scheduled-tasks/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}

// 删除定时任务
export function deleteScheduledTask(id: string): Promise<void> {
  return apiFetch(`/scheduled-tasks/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

// 手动触发一次执行
export function runScheduledTask(id: string): Promise<void> {
  return apiFetch(`/scheduled-tasks/${encodeURIComponent(id)}/run`, { method: 'POST' })
}

// 获取定时任务执行历史
export function listExecutions(id: string): Promise<{ data: { executions: Execution[] } }> {
  return apiFetch(`/scheduled-tasks/${encodeURIComponent(id)}/executions`)
}
