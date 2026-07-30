import { apiFetch } from './client'

// 对话记录（后端从会话消息流提炼：用户消息 + agent 每轮最终回复，思考与工具调用不含）
export interface ConversationRecord {
  db_session_id: number
  role: 'user' | 'assistant'
  content: string
  sequence: number
  created_at: string
  session_title: string
  agent_type: string
}

export function listConversationRecords(opts?: {
  sessionId?: number
  limit?: number
  offset?: number
}): Promise<{ data: { items: ConversationRecord[]; total: number } }> {
  const params = new URLSearchParams()
  if (opts?.sessionId) params.set('session_id', String(opts.sessionId))
  if (opts?.limit) params.set('limit', String(opts.limit))
  if (opts?.offset) params.set('offset', String(opts.offset))
  const qs = params.toString()
  return apiFetch(`/conversation-records${qs ? `?${qs}` : ''}`)
}
