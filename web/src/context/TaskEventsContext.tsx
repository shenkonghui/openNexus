import { createContext, useContext, useEffect, useRef, type ReactNode } from 'react'
import { subscribeTaskEvents } from '../api/taskmanager'

/**
 * TaskEventsContext：统一管理 tasks.json 变更 SSE 订阅。
 *
 * 背景：TaskManagerView 与 SessionSidebar 各自订阅同一 workspace 的 SSE，
 * 导致同一工作区始终存在两条到 /taskmanager/events 的长连接。此 Context
 * 在 AppLayout 顶层建立唯一一条订阅，子组件通过 useTaskEventsChanged
 * 注册回调，收到变更事件时统一分发，避免重复连接。
 *
 * 设计要点：
 *   - SSE 订阅仅依赖 workspaceId，回调引用变化不触发重连（回调存 ref）；
 *   - 多个消费者回调以列表维护，变更事件逐个通知；
 *   - AbortController 在 workspaceId 变化/卸载时中止旧连接。
 */
interface TaskEventsCtxValue {
  /** 注册一个变更回调，返回取消注册函数。回调在收到 tasks.json 变更事件时被调用。 */
  onChanged: (cb: () => void) => () => void
}

const TaskEventsContext = createContext<TaskEventsCtxValue | null>(null)

export function useTaskEventsChanged(): TaskEventsCtxValue | null {
  return useContext(TaskEventsContext)
}

export function TaskEventsProvider({ workspaceId, children }: { workspaceId: number | undefined; children: ReactNode }) {
  // 消费者回调列表（存 ref，避免 effect 依赖变化导致重连）
  const callbacksRef = useRef<Set<() => void>>(new Set())

  useEffect(() => {
    if (!workspaceId) return
    const ac = new AbortController()
    subscribeTaskEvents(
      workspaceId,
      () => {
        // 复制一份再遍历，防止回调内部取消注册时修改集合
        const cbs = [...callbacksRef.current]
        for (const cb of cbs) {
          try { cb() } catch { /* 单个回调异常不影响其他消费者 */ }
        }
      },
      ac.signal,
    ).catch(() => {})
    return () => { ac.abort() }
  }, [workspaceId])

  function onChanged(cb: () => void): () => void {
    callbacksRef.current.add(cb)
    return () => { callbacksRef.current.delete(cb) }
  }

  return (
    <TaskEventsContext.Provider value={{ onChanged }}>
      {children}
    </TaskEventsContext.Provider>
  )
}
