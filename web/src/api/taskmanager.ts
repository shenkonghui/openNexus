import { apiFetch, getBaseURL, getAuthHeaders } from './client'

export type TaskPriority = 'p0' | 'p1' | 'p2'

/** goal 生命周期状态（后端 GoalStateChanged 写回 tasks.json） */
export interface TaskGoalState {
  condition: string
  status: string // active|evaluating|achieved|stopped
  turns: number
  /** 已执行的达成审计（会签评估）次数 */
  eval_count?: number
  last_reason?: string
  roles?: string[]
  updated_at: string
}

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
  /** 任务定义的 goal 完成条件（输入侧）；非空时运行自动开启 goal 模式 */
  goal_condition?: string
  /** true 时不建独立 worktree，直接在工作区目录运行 */
  no_worktree?: boolean
  /** goal 状态（会话设定 /goal 后同步） */
  goal?: TaskGoalState
}

export interface TaskManagerDef {
  max_parallel: number
  tasks: TaskManagerTask[]
}

function qs(workspaceId: number | null | undefined): string {
  return workspaceId ? `?workspace_id=${workspaceId}` : ''
}

// 生成一个不与现有任务冲突的短 id（客户端新建任务用）。
export function genTaskId(): string {
  return `t${Date.now().toString(36)}${Math.floor(Math.random() * 36).toString(36)}`
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
  task: { id: string; title: string; detail: string; agent_type: string; model_value?: string; priority?: string; depends_on?: string[]; goal_condition?: string },
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

// 订阅 tasks.json 变更事件（SSE，用 fetch 实现以携带认证头）。
// 任意来源（左侧 UI、任务助手 MCP 工具、定时调度器）写入都会推送 changed 事件，
// 收到后回调 onChanged；连接断开后指数退避自动重连，直到 signal 中止。
export async function subscribeTaskEvents(
  workspaceId: number,
  onChanged: () => void,
  signal: AbortSignal,
): Promise<void> {
  let retryDelay = 1000
  while (!signal.aborted) {
    try {
      const resp = await fetch(`${getBaseURL()}/taskmanager/events${qs(workspaceId)}`, {
        headers: getAuthHeaders(),
        signal,
      })
      if (!resp.ok || !resp.body) throw new Error(`events 订阅失败 (${resp.status})`)
      retryDelay = 1000
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        const events = buffer.split('\n\n')
        buffer = events.pop() || ''
        // 心跳行以 ":" 开头，仅 data 行视为变更事件
        if (events.some((ev) => ev.split('\n').some((l) => l.startsWith('data: ')))) {
          onChanged()
        }
      }
    } catch {
      if (signal.aborted) return
    }
    await new Promise((r) => setTimeout(r, retryDelay))
    retryDelay = Math.min(retryDelay * 2, 15000)
  }
}

// 检查任务管理 cwd 是否为 git 仓库
export function getTaskGitStatus(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/taskmanager/git-status${qs(workspaceId)}`)
}

// 初始化 git 仓库（含初始提交）并确保 worktree 存放目录（~/.openNexus/worktrees）存在
export function initTaskGitRepo(workspaceId: number): Promise<{ data: { cwd: string; is_git_repo: boolean } }> {
  return apiFetch(`/taskmanager/git-init${qs(workspaceId)}`, { method: 'POST' })
}

/** 归档到回收站的任务条目（保留原任务字段 + 归档时间） */
export type ArchivedTask = TaskManagerTask & { archived_at: string }

// 归档任务（taskId 为空或 '*' 表示归档全部），返回归档数量
export function archiveTask(workspaceId: number, taskId?: string): Promise<{ data: { archived: number } }> {
  return apiFetch(`/taskmanager/archive${qs(workspaceId)}`, {
    method: 'POST',
    body: JSON.stringify(taskId ? { task_id: taskId } : {}),
  })
}

// 回收站列表（服务端先清理过期归档再返回）
export function listArchivedTasks(workspaceId: number): Promise<{ data: { tasks: ArchivedTask[]; retention_days: number } }> {
  return apiFetch(`/taskmanager/archived${qs(workspaceId)}`)
}

// 从回收站恢复任务到 tasks.json
export function restoreArchivedTask(workspaceId: number, taskId: string): Promise<void> {
  return apiFetch(`/taskmanager/archived/${encodeURIComponent(taskId)}/restore${qs(workspaceId)}`, { method: 'POST' })
}

// 彻底删除回收站中的任务（同时清理 worktree 与关联会话）
export function deleteArchivedTask(workspaceId: number, taskId: string): Promise<void> {
  return apiFetch(`/taskmanager/archived/${encodeURIComponent(taskId)}${qs(workspaceId)}`, { method: 'DELETE' })
}
