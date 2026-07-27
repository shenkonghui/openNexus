import { apiFetch } from './client'

// 工具调用记录（后端 tool_call_records 表，ACP 流 + 终端桥接双路记录）
export interface ToolCallRecord {
  id: number
  user_id: number
  db_session_id: number
  tool_call_id: string
  kind: string
  title: string
  command: string
  cwd: string
  terminal_id?: string
  exit_code: number | null
  status: string
  started_at: string
  finished_at: string | null
  created_at: string
  updated_at: string
  session_title: string
  agent_type: string
}

export function listToolCalls(opts?: {
  kind?: string
  sessionId?: number
  limit?: number
  offset?: number
}): Promise<{ data: { items: ToolCallRecord[]; total: number } }> {
  const params = new URLSearchParams()
  if (opts?.kind) params.set('kind', opts.kind)
  if (opts?.sessionId) params.set('session_id', String(opts.sessionId))
  if (opts?.limit) params.set('limit', String(opts.limit))
  if (opts?.offset) params.set('offset', String(opts.offset))
  const qs = params.toString()
  return apiFetch(`/tool-calls${qs ? `?${qs}` : ''}`)
}
