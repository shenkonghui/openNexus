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
