import { apiFetch } from './client'

export type TaskPriority = 'p0' | 'p1' | 'p2'

export interface TaskManagerTask {
  id: string
  title: string
  detail: string
  agent_type: string
  model_value?: string
  /** 优先级：p0 / p1 / p2，缺省 p1 */
  priority?: TaskPriority | string
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

export interface TaskManagerDef {
  max_parallel: number
  tasks: TaskManagerTask[]
}

function qs(workspaceId: number | null | undefined): string {
  return workspaceId ? `?workspace_id=${workspaceId}` : ''
}

// 读取当前工作区的任务管理定义（tasks.json）
export function getTaskManager(workspaceId: number): Promise<{ data: TaskManagerDef }> {
  return apiFetch(`/taskmanager${qs(workspaceId)}`)
}

// 整体覆盖保存任务管理定义
export function saveTaskManager(workspaceId: number, def: TaskManagerDef): Promise<{ data: TaskManagerDef }> {
  return apiFetch(`/taskmanager${qs(workspaceId)}`, {
    method: 'PUT',
    body: JSON.stringify(def),
  })
}

// 新增/更新单个任务
export function upsertTask(
  workspaceId: number,
  task: { id: string; title: string; detail: string; agent_type: string; model_value?: string; priority?: string; depends_on?: string[] },
): Promise<{ data: TaskManagerTask }> {
  return apiFetch(`/taskmanager/tasks${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(task),
  })
}

// 删除单个任务
export function deleteTask(workspaceId: number, taskId: string): Promise<void> {
  return apiFetch(`/taskmanager/tasks/${encodeURIComponent(taskId)}${qs(workspaceId)}`, { method: 'DELETE' })
}

// 设置并发上限
export function setTaskMaxParallel(workspaceId: number, maxParallel: number): Promise<void> {
  return apiFetch(`/taskmanager/max-parallel${qs(workspaceId)}`, {
    method: 'PUT',
    body: JSON.stringify({ max_parallel: maxParallel }),
  })
}

// 启动任务（task_id 为空则启动全部待执行任务）
export function startTaskManager(workspaceId: number, taskId?: string): Promise<void> {
  return apiFetch(`/taskmanager/start${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
}

// 停止任务（task_id 为空则停止全部运行中任务）
export function stopTaskManager(workspaceId: number, taskId?: string): Promise<void> {
  return apiFetch(`/taskmanager/stop${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
}

// 轮询状态
export function getTaskStatus(workspaceId: number): Promise<{ data: TaskManagerDef }> {
  return apiFetch(`/taskmanager/status${qs(workspaceId)}`)
}

// 检查任务管理 cwd 是否为 git 仓库
export function getTaskGitStatus(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/taskmanager/git-status${qs(workspaceId)}`)
}

// 初始化 git 仓库（含初始提交）并创建 .worktrees 目录
export function initTaskGitRepo(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/taskmanager/git-init${qs(workspaceId)}`, { method: 'POST' })
}
