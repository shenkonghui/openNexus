import type { SecurityTestCase } from '../types'
import { apiFetch } from './client'

// 获取用户的安全测试用例列表（首次访问时后端种子默认用例）
export function getSecurityTestCases(): Promise<{ data: { cases: SecurityTestCase[] } }> {
  return apiFetch('/security-tests/cases')
}

// 替换用户全部安全测试用例（整体替换）
export function updateSecurityTestCases(cases: SecurityTestCase[]): Promise<{ data: { cases: SecurityTestCase[] } }> {
  return apiFetch('/security-tests/cases', {
    method: 'PUT',
    body: JSON.stringify({ cases }),
  })
}
