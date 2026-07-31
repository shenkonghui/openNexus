# 消息流持久化性能优化方案

本文档分析 `ACP sessionUpdate 订阅者 buffer 满，丢弃消息` 告警的根因，并给出分级优化方案。

## 1. 问题现象

```
level=WARN source=internal/acp/client.go:164 msg="ACP sessionUpdate 订阅者 buffer 满，丢弃消息" session=sharp-silence kind=agent_thought_chunk
```

告警密集出现（相邻两条间隔 2ms），集中在 `agent_thought_chunk`。

## 2. 数据链路

```
agent 进程 stdout (JSON-RPC)
  │
  ▼  acp-go-sdk 读循环 → Client.SessionUpdate()          ← 生产者，无背压
  │
  ▼  sub.ch (buffered chan, 容量 4096)                   ← 唯一缓冲
  │
  ▼  Service prompt 消费 goroutine（单个）
  │     ├─ 落盘 messages.jsonl
  │     ├─ runningTasks.UpdateLastSeq()  → sqlite
  │     ├─ bc.broadcast(msg)             → 断点续传订阅者
  │     └─ out <- msg
  │
  ▼  out (容量 256) → SSE handler 写 socket → 浏览器
```

`Client.SessionUpdate` 使用 `select { case sub.ch <- ...; default: 丢弃 }`，即**故意不阻塞**，见
`internal/acp/client.go:159-166`。因此 4096 是整条链路唯一的缓冲；一旦
「单条消息处理耗时 × 消息速率」超过缓冲吸收能力，就开始丢消息。这是设计中的泄压阀，
不是 bug，但触发说明消费端已严重跟不上。

## 3. 根因分析

### 3.1 thought 分支是唯一的阻塞式发送

`internal/acp/service.go:1907-1969` 中：

| kind | 送入 out 的方式 |
| --- | --- |
| `agent_thought_chunk` | `out <- msg`，**无 default，会阻塞** |
| `tool_call_update` | `select { case out <- msg: default: }` |
| `usage_update` / `session_info_update` | `select { case out <- msg: default: }` |

`out` 容量 256。当前端/网络慢（SSE 每条一次 write + flush 系统调用）导致 `out` 写满时，
thought 分支会卡在 `out <-` 上，整个消费 goroutine 停摆 → 4096 缓冲被填满 → 后续所有
update 开始被丢弃。

注意：**buffer 满时丢弃的不只是 thought**。同一时刻到达的 `agent_message_chunk`（真正的
回答内容）走 `persistMsg` 路径，被丢等于既没推给前端也没落盘，断点续传也无法补回，
表现为「输出到一半突然没了」。

### 3.2 攒批机制在交替流下失效

thought 与 tool_call_update 的攒批 flush 由「遇到异类消息」触发：

- 收到 thought → `flushToolUpdates()`（落盘）
- 收到 tool_call_update → `flushThoughts()`（落盘）

当两者**交替到达**（边执行 shell/read 边思考，非常常见）时，攒批完全失效，退化为
每条消息一次落盘。

### 3.3 文件层写入过重（核心）

消息**已经不在 sqlite**，而是按会话追加的 JSONL 文件（`internal/repository/message_repository.go`）。
但当前实现每条消息的开销是：

| 动作 | 成本 |
| --- | --- |
| `allocIDLocked()`：ReadFile(`_next_id`) + WriteFile(`_next_id`) | ~6 syscall，每条消息重写一个文件 |
| jsonl `OpenFile` + `Write` + `Close` | ~3 syscall，无缓冲、每条重新开关句柄 |
| `runningTasks.UpdateLastSeq()` | **一次 gorm + sqlite 写事务** |
| 全局 `r.mu` | 一把锁串行化**所有会话**的读与写 |

读路径同样是瓶颈：`readSessionLocked` 每次**全文件 JSON 解析**且持同一把全局锁，
`MaxSequence`、`AggregateExecutions`、`FindBySessionIDLastN`、`FindBySessionIDAfter`
全部 O(N)。前端周期性拉取历史时，这些读会直接阻塞热路径的写。

### 3.4 `LastSeq` 是纯白写

`RunningTask.LastSeq` 在**生产代码中从未被读取**：

- 全仓库只有 `internal/acp/service_test.go` 读取它
- `web/src/types.ts:221` 只有一行类型声明，前端无任何使用

即每条消息一次 sqlite 写事务是完全无用的开销。

## 4. 当前落库策略（现状记录）

并非全部消息落盘，按 kind 分档处理：

| kind | 推前端 | 落盘 |
| --- | --- | --- |
| `user_message_chunk`（服务端合成的用户输入） | 是 | 逐条 |
| `agent_message_chunk` / `plan` / `plan_update` / `tool_call` / `current_mode_update` / 权限请求 / 文件改动 | 是 | 逐条（`persistMsg`） |
| `agent_thought_chunk` | 逐条实时推 | 合并落盘：N 条 delta 拼成 1 行，取批末尾 sequence |
| `tool_call_update` | 逐条实时推（非阻塞） | 按 `toolCallId` 去重覆盖，一个 flush 窗口内只落最新一条；含 terminal 锚点的立即 flush |
| `usage_update` / `session_info_update` | 是 | 不落盘 |
| `reconnecting` 状态帧 | 是 | 不落盘，sequence=0 |
| buffer 满被丢弃的 | 否 | 不落盘（含 `agent_message_chunk`） |

由此产生两个既有特性（设计接受）：

1. **jsonl 中的 `sequence` 不连续**：`seq++` 对每条 update 都执行，但只有部分行落盘。
2. **断点续传补回的历史比实时流粗**：`SubscribeSession` 从文件补齐 `lastSeq` 之后的消息，
   拿到的是合并后的 thought 与只剩终态的 tool_call_update。

## 5. 优化方案

### P0：低风险、高收益（建议先做）

| # | 优化项 | 位置 | 说明 |
| --- | --- | --- | --- |
| 1 | **删掉热路径的 `UpdateLastSeq`** | `internal/acp/service.go:1749/1782/1856/1881` | 生产代码从未读 `LastSeq`。改为仅在 `finishTask` 时写一次，或直接删除 4 处调用。省掉每条消息一次 sqlite 写事务。 |
| 2 | **`_next_id` 内存化** | `message_repository.go:356-371` | 启动读一次，进程内 atomic 自增，定期/退出时落盘；崩溃后按目录扫描 `max(id)+1` 恢复。`messages.FindByID` 全仓库仅 1 个调用点（`service.go:2453`），id 语义很弱，亦可考虑改用 `sessionID+sequence` 作身份后彻底删除该文件。省 ~6 syscall/条。 |
| 3 | **每会话常开 append 句柄 + `bufio.Writer`** | `message_repository.go:38-67` | `sessionID → *os.File + bufio.Writer`，按 100ms 或缓冲满 flush，空闲句柄 LRU 关闭，替代 open-write-close。 |
| 4 | **全局锁改分片锁** | `message_repository.go:24` | `sync.Map[sessionID]*sessionFile`，每会话一把锁，消除跨会话与读写互相阻塞。 |

预期：单条消息落盘成本下降一个数量级，告警基本消失。

### P1：结构性解耦

| # | 优化项 | 说明 |
| --- | --- | --- |
| 5 | thought 分支 `out <- msg` 改非阻塞 | 与 usage / tool_update 一致，杜绝单个慢 SSE 客户端拖垮整条流。 |
| 6 | flush 改为时间/条数触发 | 如 200ms 或 50 条，替代「异类消息互相触发」，修复 3.2 的攒批失效。 |
| 7 | 落盘移出消费循环 | 独立 writer goroutine + 有界队列，消费循环只做 map + broadcast。 |
| 8 | 会话级读缓存 | 内存保存已解析切片 + 文件 offset，追加时增量更新；`MaxSequence` 反向读最后一行即可，不再全文解析。 |

### P2：真正减少落盘量

| # | 优化项 | 说明 |
| --- | --- | --- |
| 9 | **大 `raw_json` 旁路存储** | scanner buffer 开到 8MB（`message_repository.go:389-390`）说明 tool_call_update 的 shell 输出确实巨大。超阈值内容写到 `{sessionID}.blobs/<id>` 或截断，jsonl 只留引用/摘要。jsonl 瘦身后所有 O(N) 读路径同步变快，收益全局。 |
| 10 | jsonl 按 execution 分片 | `{sessionID}/{executionID}.jsonl`，历史只读需要的分片，彻底摆脱全文件解析。 |
| 11 | 工具调用记录攒批 | `applyToolCallMeta` / `recordToolCallStart` 的 sqlite 写在 flush 时合并为单事务。 |

## 6. 验证方式

- `go test ./internal/repository/...`：`message_repository_test.go` 已覆盖 Create / FindByID /
  DeleteFromSequence / MaxSequence / AggregateExecutions 等主要行为。
- `go test ./internal/acp/...`：覆盖 broadcaster、prompt 流与 running_task 状态流转。
- 运行时观察：长 thinking 任务下 `bridge.log` / `opennexus.log` 中
  「订阅者 buffer 满」与「msgBroadcaster 订阅者 buffer 满」告警是否归零。

## 7. 相关代码位置

| 文件 | 关注点 |
| --- | --- |
| `internal/acp/client.go:143-168` | `SessionUpdate` fan-out 与丢弃逻辑 |
| `internal/acp/connection.go:217-236` | `Subscribe(sid, 4096)` |
| `internal/acp/service.go:1723` | `out := make(chan models.Message, 256)` |
| `internal/acp/service.go:1820-1990` | `consumeStream` 消费循环、攒批与落盘 |
| `internal/acp/broadcaster.go:36-56` | 广播器的非阻塞丢弃 |
| `internal/handlers/session_handler.go:910-956` | SSE 循环、客户端断开后台排空 |
| `internal/repository/message_repository.go` | JSONL 消息仓库（本次优化主战场） |
| `internal/repository/running_task_repository.go:33` | `UpdateLastSeq` |
