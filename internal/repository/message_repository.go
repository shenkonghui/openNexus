package repository

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"opennexus/internal/models"
)

const nextIDFile = "_next_id"

// MessageRepository 是消息持久化仓库：按会话 UUID 追加写入 JSONL 文件。
// 布局：{dir}/{sessionID}.jsonl，全局自增 id 存于 {dir}/_next_id。
type MessageRepository struct {
	dir string
	mu  sync.Mutex
}

// NewMessageRepository 创建文件型 MessageRepository，dir 不存在时自动创建。
func NewMessageRepository(dir string) *MessageRepository {
	_ = os.MkdirAll(dir, 0o755)
	return &MessageRepository{dir: dir}
}

func (r *MessageRepository) filePath(sessionID string) string {
	return filepath.Join(r.dir, sessionID+".jsonl")
}

// Create 追加写入单条消息，并分配全局自增 ID。
func (r *MessageRepository) Create(m *models.Message) error {
	if m.SessionID == "" {
		return fmt.Errorf("session_id 不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	id, err := r.allocIDLocked()
	if err != nil {
		return err
	}
	m.ID = id
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}

	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(r.filePath(m.SessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// CreateBatch 批量写入消息。
func (r *MessageRepository) CreateBatch(messages []models.Message) error {
	for i := range messages {
		if err := r.Create(&messages[i]); err != nil {
			return err
		}
	}
	return nil
}

// FindByID 按消息主键查询；扫描全部 jsonl（本地体量可接受）。
func (r *MessageRepository) FindByID(id uint) (*models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("message not found")
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		msgs, err := r.readFileLocked(filepath.Join(r.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			if msgs[i].ID == id {
				m := msgs[i]
				return &m, nil
			}
		}
	}
	return nil, fmt.Errorf("message not found")
}

// FindBySessionID 查询会话全部消息，按 sequence 升序。
func (r *MessageRepository) FindBySessionID(sessionID string) ([]models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readSessionLocked(sessionID)
}

// FindBySessionIDPaged 分页查询，按 sequence 升序。limit<=0 时不分页。
func (r *MessageRepository) FindBySessionIDPaged(sessionID string, limit, offset int) ([]models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	if offset > len(msgs) {
		return []models.Message{}, nil
	}
	msgs = msgs[offset:]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[:limit]
	}
	return msgs, nil
}

// FindBySessionIDLastN 返回最近 n 条（升序）。n<=0 返回空切片。
func (r *MessageRepository) FindBySessionIDLastN(sessionID string, n int) ([]models.Message, error) {
	if n <= 0 {
		return []models.Message{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	if n >= len(msgs) {
		return msgs, nil
	}
	return msgs[len(msgs)-n:], nil
}

// FindBySessionIDBeforeLastN 返回 sequence < beforeSeq 的最近 n 条（升序）。
// beforeSeq<=0 时不限制 sequence（等价于 LastN）。n<=0 返回空切片。
// 用于「加载更多更早消息」：以当前最早可见消息的 sequence 为游标，向前翻页。
func (r *MessageRepository) FindBySessionIDBeforeLastN(sessionID string, beforeSeq int, n int) ([]models.Message, error) {
	if n <= 0 {
		return []models.Message{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	var out []models.Message
	if beforeSeq > 0 {
		for _, m := range msgs {
			if m.Sequence < beforeSeq {
				out = append(out, m)
			}
		}
	} else {
		out = msgs
	}
	if n >= len(out) {
		return out, nil
	}
	return out[len(out)-n:], nil
}

// FindByKind 查询指定 kind 的消息，按 sequence 升序。
func (r *MessageRepository) FindByKind(sessionID, kind string) ([]models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Message, 0)
	for _, m := range msgs {
		if m.Kind == kind {
			out = append(out, m)
		}
	}
	return out, nil
}

// FindLastByKind 返回指定 kind 的最新一条；无匹配时返回 nil, nil。
func (r *MessageRepository) FindLastByKind(sessionID, kind string) (*models.Message, error) {
	msgs, err := r.FindByKind(sessionID, kind)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	m := msgs[len(msgs)-1]
	return &m, nil
}

// FindBySessionIDAfter 返回 sequence > afterSeq 的消息，按 sequence 升序。
func (r *MessageRepository) FindBySessionIDAfter(sessionID string, afterSeq int) ([]models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Message, 0)
	for _, m := range msgs {
		if m.Sequence > afterSeq {
			out = append(out, m)
		}
	}
	return out, nil
}

// DeleteBySessionID 删除指定会话的消息文件。
func (r *MessageRepository) DeleteBySessionID(sessionID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := os.Remove(r.filePath(sessionID))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteFromSequence 删除 sequence >= fromSeq 的消息并重写文件。
func (r *MessageRepository) DeleteFromSequence(sessionID string, fromSeq int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return 0, err
	}
	kept := make([]models.Message, 0, len(msgs))
	var deleted int64
	for _, m := range msgs {
		if m.Sequence >= fromSeq {
			deleted++
			continue
		}
		kept = append(kept, m)
	}
	if err := r.writeAllLocked(sessionID, kept); err != nil {
		return 0, err
	}
	return deleted, nil
}

// MaxSequence 返回当前最大 sequence，无消息时返回 0。
func (r *MessageRepository) MaxSequence(sessionID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, m := range msgs {
		if m.Sequence > max {
			max = m.Sequence
		}
	}
	return max, nil
}

// ExecutionAggregate 是按 execution_id 聚合的执行块统计。
type ExecutionAggregate struct {
	ExecutionID  uint   `json:"execution_id"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	MessageCount int    `json:"message_count"`
	Status       string `json:"status"`
	Error        string `json:"error"`
}

// AggregateExecutions 按 execution_id 聚合，按 started_at 降序。
func (r *MessageRepository) AggregateExecutions(sessionID string) ([]ExecutionAggregate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return nil, err
	}
	type agg struct {
		id    uint
		start time.Time
		end   time.Time
		count int
	}
	byID := map[uint]*agg{}
	order := make([]uint, 0)
	for _, m := range msgs {
		if m.ExecutionID == nil {
			continue
		}
		id := *m.ExecutionID
		a, ok := byID[id]
		if !ok {
			a = &agg{id: id, start: m.CreatedAt, end: m.CreatedAt}
			byID[id] = a
			order = append(order, id)
		}
		a.count++
		if m.CreatedAt.Before(a.start) {
			a.start = m.CreatedAt
		}
		if m.CreatedAt.After(a.end) {
			a.end = m.CreatedAt
		}
	}
	sort.Slice(order, func(i, j int) bool {
		return byID[order[i]].start.After(byID[order[j]].start)
	})
	out := make([]ExecutionAggregate, 0, len(order))
	for _, id := range order {
		a := byID[id]
		out = append(out, ExecutionAggregate{
			ExecutionID:  a.id,
			StartedAt:    a.start.Format("2006-01-02 15:04:05.999999999-07:00"),
			FinishedAt:   a.end.Format("2006-01-02 15:04:05.999999999-07:00"),
			MessageCount: a.count,
		})
	}
	return out, nil
}

// MaxExecutionID 返回最大 execution_id，无则 0。
func (r *MessageRepository) MaxExecutionID(sessionID string) (uint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	msgs, err := r.readSessionLocked(sessionID)
	if err != nil {
		return 0, err
	}
	var max uint
	for _, m := range msgs {
		if m.ExecutionID != nil && *m.ExecutionID > max {
			max = *m.ExecutionID
		}
	}
	return max, nil
}

func (r *MessageRepository) allocIDLocked() (uint, error) {
	path := filepath.Join(r.dir, nextIDFile)
	var cur uint
	data, err := os.ReadFile(path)
	if err == nil {
		n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		cur = uint(n)
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	cur++
	if err := os.WriteFile(path, []byte(strconv.FormatUint(uint64(cur), 10)), 0o644); err != nil {
		return 0, err
	}
	return cur, nil
}

func (r *MessageRepository) readSessionLocked(sessionID string) ([]models.Message, error) {
	return r.readFileLocked(r.filePath(sessionID))
}

func (r *MessageRepository) readFileLocked(path string) ([]models.Message, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []models.Message{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var msgs []models.Message
	sc := bufio.NewScanner(f)
	// tool_call_update 的 raw_json 可能很大
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m models.Message
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("解析消息行失败: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].Sequence < msgs[j].Sequence
	})
	return msgs, nil
}

func (r *MessageRepository) writeAllLocked(sessionID string, msgs []models.Message) error {
	path := r.filePath(sessionID)
	if len(msgs) == 0 {
		err := os.Remove(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for i := range msgs {
		line, err := json.Marshal(&msgs[i])
		if err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
		if _, err := w.Write(line); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			f.Close()
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
