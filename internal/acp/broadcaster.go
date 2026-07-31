package acp

import (
	"log/slog"
	"sync"

	"opennexus/internal/models"
)

// msgBroadcaster 将一个进行中的 prompt 产生的消息分发给多个订阅者。
// 主要用途：支持断点续传——原发起 prompt 的客户端断开后，重连的客户端可订阅同一广播器继续接收。
type msgBroadcaster struct {
	mu         sync.RWMutex
	subs       []chan models.Message
	currentSeq int // 当前已广播的最新 sequence
	// persister 是本 prompt 的落盘 writer（注册广播器前设置，之后只读）。
	// 断点续传订阅时先经 barrier 排空落盘队列，再从仓库补齐遗漏消息。
	persister *promptPersister
}

// newMsgBroadcaster 创建广播器，记录起始 sequence（用于订阅时计算需要从 DB 补齐的缺口）。
func newMsgBroadcaster(startSeq int) *msgBroadcaster {
	return &msgBroadcaster{currentSeq: startSeq}
}

// subscribe 注册一个新订阅者，返回其专属 channel 与订阅时刻的 currentSeq。
// 订阅者应自行从 DB 补齐 lastSeq+1..currentSeq 的消息后再开始消费 channel。
func (b *msgBroadcaster) subscribe(bufSize int) (<-chan models.Message, int) {
	ch := make(chan models.Message, bufSize)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	snap := b.currentSeq
	b.mu.Unlock()
	return ch, snap
}

// broadcast 将消息发送给所有订阅者，更新 currentSeq。
// 对 buffer 满的慢订阅者丢弃消息（记日志），避免阻塞 prompt 消费循环。
func (b *msgBroadcaster) broadcast(msg models.Message) {
	b.mu.RLock()
	subs := make([]chan models.Message, len(b.subs))
	copy(subs, b.subs)
	b.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			slog.Warn("msgBroadcaster 订阅者 buffer 满，丢弃消息",
				"session", msg.SessionID, "sequence", msg.Sequence, "kind", msg.Kind)
		}
	}

	b.mu.Lock()
	if msg.Sequence > b.currentSeq {
		b.currentSeq = msg.Sequence
	}
	b.mu.Unlock()
}

// close 移除并关闭所有订阅者。在 prompt goroutine 结束时调用。
func (b *msgBroadcaster) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		close(ch)
	}
	b.subs = nil
}

// subscriberCount 返回当前订阅者数量（用于调试/测试）。
func (b *msgBroadcaster) subscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
