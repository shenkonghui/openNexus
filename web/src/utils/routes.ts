/**
 * 统一构造带工作区前缀的页面 URL，避免各处散落字符串拼接。
 * 任务列表：/workspaces/:wid/tasks
 * 新建任务：/workspaces/:wid/tasks/new
 * 会话详情：/workspaces/:wid/sessions/:sid
 */

// 任务列表页
export function tasksUrl(workspaceId?: number | null): string {
  if (workspaceId) return `/workspaces/${workspaceId}/tasks`
  return '/'
}

// 新建任务页
export function newTaskUrl(workspaceId?: number | null): string {
  if (workspaceId) return `/workspaces/${workspaceId}/tasks/new`
  return '/new'
}

// 会话详情页（直接构造最终 URL，避免经过 SessionRedirect 中间跳转）
export function sessionUrl(sessionId: number, workspaceId?: number | null): string {
  if (workspaceId) return `/workspaces/${workspaceId}/sessions/${sessionId}`
  return `/sessions/${sessionId}`
}

// 任务管理页
export function taskManagerUrl(workspaceId?: number | null): string {
  if (workspaceId) return `/workspaces/${workspaceId}/taskmanager`
  return '/taskmanager'
}

// 判断当前路径是否为任务列表页（兼容新旧两种 URL）
export function isTasksPath(pathname: string, workspaceId?: number | null): boolean {
  if (workspaceId) return pathname === `/workspaces/${workspaceId}/tasks`
  return pathname === '/'
}

// 判断当前路径是否为新建任务页（兼容新旧两种 URL）
export function isNewTaskPath(pathname: string, workspaceId?: number | null): boolean {
  if (workspaceId) return pathname === `/workspaces/${workspaceId}/tasks/new`
  return pathname === '/new'
}

// 判断当前路径是否为任务管理页（兼容新旧两种 URL）
export function isTaskManagerPath(pathname: string): boolean {
  return pathname === '/taskmanager' || /^\/workspaces\/\d+\/taskmanager$/.test(pathname)
}
