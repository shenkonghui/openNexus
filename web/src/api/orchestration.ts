import { apiFetch } from './client'

export type OrchTaskPriority = 'p0' | 'p1' | 'p2'

export interface OrchestrationTask {
  id: string
  title: string
  detail: string
  agent_type: string
  model_value?: string
  /** 优先级：p0 / p1 / p2，缺省 p1 */
  priority?: OrchTaskPriority | string
  session_id?: string
  db_session_id?: number
  status: string // pending|queued|running|done|failed|canceled|interrupt
  branch?: string
  worktree_path?: string
  started_at?: string
  finished_at?: string
  error?: string
  depends_on?: string[]
}

export interface OrchestrationDef {
  max_parallel: number
  tasks: OrchestrationTask[]
}

function qs(workspaceId: number | null | undefined): string {
  return workspaceId ? `?workspace_id=${workspaceId}` : ''
}

// 读取当前工作区的编排定义（tasks.json）
export function getOrchestration(workspaceId: number): Promise<{ data: OrchestrationDef }> {
  return apiFetch(`/orchestration${qs(workspaceId)}`)
}

// 整体覆盖保存编排定义
export function saveOrchestration(workspaceId: number, def: OrchestrationDef): Promise<{ data: OrchestrationDef }> {
  return apiFetch(`/orchestration${qs(workspaceId)}`, {
    method: 'PUT',
    body: JSON.stringify(def),
  })
}

// 新增/更新单个任务
export function upsertOrchTask(
  workspaceId: number,
  task: { id: string; title: string; detail: string; agent_type: string; model_value?: string; priority?: string; depends_on?: string[] },
): Promise<{ data: OrchestrationTask }> {
  return apiFetch(`/orchestration/tasks${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(task),
  })
}

// 删除单个任务
export function deleteOrchTask(workspaceId: number, taskId: string): Promise<void> {
  return apiFetch(`/orchestration/tasks/${encodeURIComponent(taskId)}${qs(workspaceId)}`, { method: 'DELETE' })
}

// 设置并发上限
export function setOrchMaxParallel(workspaceId: number, maxParallel: number): Promise<void> {
  return apiFetch(`/orchestration/max-parallel${qs(workspaceId)}`, {
    method: 'PUT',
    body: JSON.stringify({ max_parallel: maxParallel }),
  })
}

// 启动任务（task_id 为空则启动全部待执行任务）
export function startOrchestration(workspaceId: number, taskId?: string): Promise<void> {
  return apiFetch(`/orchestration/start${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
}

// 停止任务（task_id 为空则停止全部运行中任务）
export function stopOrchestration(workspaceId: number, taskId?: string): Promise<void> {
  return apiFetch(`/orchestration/stop${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
}

// 轮询状态
export function getOrchStatus(workspaceId: number): Promise<{ data: OrchestrationDef }> {
  return apiFetch(`/orchestration/status${qs(workspaceId)}`)
}

// 检查编排 cwd 是否为 git 仓库
export function getOrchGitStatus(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/orchestration/git-status${qs(workspaceId)}`)
}

// 初始化 git 仓库（含初始提交）并创建 .worktrees 目录
export function initOrchGitRepo(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/orchestration/git-init${qs(workspaceId)}`, { method: 'POST' })
}
