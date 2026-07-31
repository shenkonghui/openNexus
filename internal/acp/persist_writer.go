package acp

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/coder/acp-go-sdk"

	"opennexus/internal/models"
	"opennexus/internal/repository"
)

const (
	// persistFlushInterval 是攒批落盘的时间触发周期（P1-6：替代「异类消息互相触发」）。
	persistFlushInterval = 200 * time.Millisecond
	// persistBatchMaxCount 是攒批落盘的条数触发阈值。
	persistBatchMaxCount = 50
	// persistQueueSize 是落盘队列容量：writer 只做文件缓冲追加（快），
	// 队列写满时消费循环短暂阻塞形成有界背压。
	persistQueueSize = 1024
)

// persistOp 是一条待落盘操作：消息本体 + 触发它的原始 update 增量（可选）。
type persistOp struct {
	msg models.Message
	// tu 是 tool_call_update 的原始增量，writer 用于按 toolCallId 去重、
	// 合并工具记录 meta、判断 terminal 锚点。
	tu *acp.SessionToolCallUpdate
	// tc 是 tool_call 开始事件，writer 据此创建工具调用记录。
	tc *acp.SessionUpdateToolCall
}

// promptPersister 是单次 prompt 的落盘 writer（P1-7）：
// 消费循环只做 map + broadcast，把持久化操作经有界队列交给本 goroutine，
// 由它完成 thought 攒批合并、tool_call_update 覆盖去重、工具记录批量落库。
// flush 由时间（200ms）/ 条数（50）/ terminal 锚点 / barrier / 关闭触发。
type promptPersister struct {
	svc        *Service
	session    *models.Session
	sessionCwd string
	q          chan persistOp
	flushReq   chan chan struct{}
	done       chan struct{}
}

func (s *Service) newPromptPersister(session *models.Session, sessionCwd string) *promptPersister {
	p := &promptPersister{
		svc:        s,
		session:    session,
		sessionCwd: sessionCwd,
		q:          make(chan persistOp, persistQueueSize),
		flushReq:   make(chan chan struct{}),
		done:       make(chan struct{}),
	}
	go p.run()
	return p
}

// enqueue 提交一条落盘操作。队列满时阻塞（有界背压，writer 无 sqlite 热写、追赶很快）。
func (p *promptPersister) enqueue(op persistOp) {
	select {
	case <-p.done:
		// writer 已退出（close 后），直接同步落库兜底，不丢消息
		p.createMsg(op.msg)
	default:
		p.q <- op
	}
}

// barrier 同步等待 writer 排空当前队列并 flush 全部攒批。
// 用于断点续传订阅：保证订阅时刻之前广播过的消息都已可从仓库读到。
func (p *promptPersister) barrier() {
	ack := make(chan struct{})
	select {
	case p.flushReq <- ack:
		<-ack
	case <-p.done:
	}
}

// close 关闭队列并等待 writer 排空、flush 全部攒批后返回。
func (p *promptPersister) close() {
	close(p.q)
	<-p.done
}

func (p *promptPersister) createMsg(m models.Message) {
	if err := p.svc.messages.Create(&m); err != nil {
		slog.Error("持久化消息失败", "session", m.SessionID, "sequence", m.Sequence, "kind", m.Kind, "err", err)
	}
}

func (p *promptPersister) run() {
	defer close(p.done)
	ticker := time.NewTicker(persistFlushInterval)
	defer ticker.Stop()

	// thought_chunk 攒批降频：思考片段已在消费循环实时广播/流出，
	// 仅落库合并为一行：拼接相邻 delta 文本，RawJSON 用 \n 连接
	//（与前端 groupMessages 合并格式一致）。取批末尾 sequence 作为合并记录：
	// 断线续传（sequence > lastSeq）即便落在批次中间，也能取回完整合并文本
	//（重复优于缺口，thought 可折叠且重复无副作用）。
	var thoughtBatch []models.Message
	flushThoughts := func() {
		if len(thoughtBatch) == 0 {
			return
		}
		merged := thoughtBatch[len(thoughtBatch)-1]
		var sb strings.Builder
		raws := make([]string, 0, len(thoughtBatch))
		for _, m := range thoughtBatch {
			sb.WriteString(m.Content)
			if m.RawJSON != "" {
				raws = append(raws, m.RawJSON)
			}
		}
		merged.Content = sb.String()
		merged.RawJSON = strings.Join(raws, "\n")
		p.createMsg(merged)
		thoughtBatch = nil
	}

	// tool_call_update 覆盖去重：按 toolCallId 只保留最新一条延迟落库
	//（前端 parseToolCalls 本就按 id 合并，历史只需终态）。
	// 例外：内嵌 terminal 锚点的 update 每个终端仅出现一次，覆盖去重会把它
	// 冲掉——覆盖时保留锚点行（NDJSON），且锚点 update 到达时立即 flush 落库。
	pendingToolUpdates := map[string]models.Message{}
	var toolUpdateOrder []string
	// pendingToolMeta 同窗口攒批工具调用记录的增量（状态/退出码等），flush 时合并为单事务落库
	pendingToolMeta := map[string]*toolCallMeta{}
	flushToolUpdates := func() {
		if len(toolUpdateOrder) == 0 {
			return
		}
		metaBatch := make([]repository.ToolCallBatchUpdate, 0, len(toolUpdateOrder))
		for _, id := range toolUpdateOrder {
			m := pendingToolUpdates[id]
			p.createMsg(m)
			if meta := pendingToolMeta[id]; meta != nil {
				metaBatch = append(metaBatch, repository.ToolCallBatchUpdate{
					ToolCallID: id,
					Fields: repository.ToolCallUpdateFields{
						Status:   meta.status,
						ExitCode: meta.exitCode,
						Title:    meta.title,
						Command:  meta.command,
					},
				})
			}
		}
		p.svc.applyToolCallMetaBatch(p.session.ID, metaBatch)
		pendingToolUpdates = map[string]models.Message{}
		toolUpdateOrder = nil
		pendingToolMeta = map[string]*toolCallMeta{}
	}
	flushPending := func() {
		flushThoughts()
		flushToolUpdates()
	}
	defer flushPending()

	handle := func(op persistOp) {
		switch op.msg.Kind {
		case models.MessageKindAgentThoughtChunk:
			thoughtBatch = append(thoughtBatch, op.msg)
			if len(thoughtBatch) >= persistBatchMaxCount {
				flushThoughts()
			}
		case models.MessageKindToolCallUpdate:
			msg := op.msg
			id := ""
			if op.tu != nil {
				id = string(op.tu.ToolCallId)
			}
			if id == "" {
				id = fmt.Sprintf("seq-%d", msg.Sequence)
			}
			if prev, exists := pendingToolUpdates[id]; !exists {
				toolUpdateOrder = append(toolUpdateOrder, id)
			} else {
				// 覆盖前保留旧 raw 中的 terminal 锚点行（前端按多行合并 raw_json）
				msg.RawJSON = keepTerminalAnchorRaw(prev.RawJSON, msg.RawJSON)
			}
			pendingToolUpdates[id] = msg
			if op.tu != nil && p.svc.toolCallRecords != nil {
				// 合并本条增量到记录攒批；内嵌 terminal content 时立即写关联，
				// 保证终端退出回调能按 terminal_id 命中记录（每终端仅一次，低频）
				if pendingToolMeta[id] == nil {
					pendingToolMeta[id] = &toolCallMeta{}
				}
				mergeToolCallDelta(pendingToolMeta[id], op.tu)
				p.svc.linkToolCallTerminal(p.session.ID, op.tu)
			}
			if hasTerminalContent(op.tu) {
				// 锚点 update 立即落库（每终端一次，低频）：中途订阅的
				// 多任务窗口 / 断线续传按仓库回放时才能拿到终端锚点
				flushToolUpdates()
			} else if len(toolUpdateOrder) >= persistBatchMaxCount {
				flushToolUpdates()
			}
		default:
			// 低频消息逐条落库（读路径按 sequence 排序，文件内顺序无关，无需先 flush 攒批）
			p.createMsg(op.msg)
			if op.tc != nil {
				// 工具调用历史：创建记录（shell 类解析 rawInput 命令/目录）
				p.svc.recordToolCallStart(p.session, p.sessionCwd, op.tc)
			}
		}
	}

	for {
		select {
		case op, ok := <-p.q:
			if !ok {
				return // defer 已兜底 flushPending
			}
			handle(op)
		case ack := <-p.flushReq:
			// barrier：先排空队列中已提交的操作，再 flush 攒批
			for {
				select {
				case op, ok := <-p.q:
					if !ok {
						flushPending()
						close(ack)
						return
					}
					handle(op)
					continue
				default:
				}
				break
			}
			flushPending()
			close(ack)
		case <-ticker.C:
			flushPending()
		}
	}
}

// applyToolCallMetaBatch 把攒批合并的工具记录增量在单事务中落库（P2-11）。
func (s *Service) applyToolCallMetaBatch(dbSessionID uint, updates []repository.ToolCallBatchUpdate) {
	if s.toolCallRecords == nil || len(updates) == 0 {
		return
	}
	if err := s.toolCallRecords.ApplyUpdatesBatch(dbSessionID, updates); err != nil {
		slog.Warn("批量更新工具调用记录失败", "db_session", dbSessionID, "count", len(updates), "err", err)
	}
}
