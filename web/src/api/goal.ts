import type { GoalSettings } from '../types'
import { apiFetch } from './client'

export function getGoalSettings(): Promise<{ data: GoalSettings }> {
  return apiFetch('/goal/settings')
}

export function updateGoalSettings(payload: GoalSettings): Promise<{ data: GoalSettings }> {
  return apiFetch('/goal/settings', {
    method: 'PUT',
    body: JSON.stringify(payload),
  })
}

// goal 评估角色（文件式定义：markdown + frontmatter，首轮评估时自动选取）
export interface GoalRole {
  name: string
  description: string
  agent?: string
  model?: string
  skills?: string[]
  location: string
  scope: string
  path: string
}

// 扫描评估角色列表；user_dir 为主用户目录（新建角色文件的落盘位置）
export function listGoalRoles(): Promise<{ data: { roles: GoalRole[]; user_dir: string } }> {
  return apiFetch('/goal/roles')
}
