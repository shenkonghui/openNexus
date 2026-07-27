package services

import "sync"

// taskEventHub 是 tasks.json 变更事件中心（包级单例）。
// 所有写路径（REST handler、MCP 工具、编排器状态更新、定时调度器）最终都经
// TaskStore.Save 落盘，因此在 Save 成功后统一广播，订阅方（SSE 端点）按 cwd
// 收到通知后驱动前端刷新，保证任意来源的任务变更两侧界面都能自动同步。
type taskEventHub struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{} // cwd -> 订阅者集合
}

var taskEvents = &taskEventHub{subs: make(map[string]map[chan struct{}]struct{})}

// SubscribeTaskChanges 订阅指定 cwd 的 tasks.json 变更通知。
// 返回的 channel 容量为 1（通知合并：连续多次变更至少触发一次接收）；
// 用完必须调用 cancel 注销，否则泄漏订阅。
func SubscribeTaskChanges(cwd string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	taskEvents.mu.Lock()
	if taskEvents.subs[cwd] == nil {
		taskEvents.subs[cwd] = make(map[chan struct{}]struct{})
	}
	taskEvents.subs[cwd][ch] = struct{}{}
	taskEvents.mu.Unlock()

	cancel := func() {
		taskEvents.mu.Lock()
		if set := taskEvents.subs[cwd]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(taskEvents.subs, cwd)
			}
		}
		taskEvents.mu.Unlock()
	}
	return ch, cancel
}

// notifyTaskChanged 向指定 cwd 的所有订阅者广播变更（非阻塞，channel 满则合并）。
func notifyTaskChanged(cwd string) {
	taskEvents.mu.Lock()
	defer taskEvents.mu.Unlock()
	for ch := range taskEvents.subs[cwd] {
		select {
		case ch <- struct{}{}:
		default: // 已有未消费的通知，合并
		}
	}
}
