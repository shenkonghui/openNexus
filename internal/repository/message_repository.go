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

const (
	// idReserveBlock 是 ID 块预留大小：内存自增，每耗尽一块才写一次 _next_id 文件。
	// 崩溃最多浪费一块 ID（留缺口），不会重复分配。
	idReserveBlock = 128
	// rawBlobThreshold 是 raw_json 旁路存储阈值：超过该大小的 raw_json 写入
	// blobs/ 子目录独立文件，jsonl 行内只留引用，避免大 shell 输出拖垮全文解析。
	rawBlobThreshold = 64 * 1024
	// blobRefPrefix 是 jsonl 行内 raw_json 旁路引用的标记前缀，后接消息 ID。
	blobRefPrefix = "__opennexus_blob__:"
	// writerFlushInterval 是后台 flush 周期：dirty 的 bufio.Writer 每周期落盘一次。
	writerFlushInterval = 100 * time.Millisecond
	// handleIdleTimeout 是 append 句柄空闲关闭时限。
	handleIdleTimeout = time.Minute
	// cacheIdleTimeout 是会话读缓存空闲淘汰时限（LRU）。
	cacheIdleTimeout = 5 * time.Minute
)

// sessionStore 是单个会话的持久化状态：读缓存 + 常开 append 句柄，一把独立锁。
type sessionStore struct {
	mu sync.Mutex
	// msgs 是会话全部消息的内存缓存（按 sequence 升序）；大 raw_json 以 blob 引用形式存放。
	msgs   []models.Message
	loaded bool
	// handle/w 是当前活跃分片的常开 append 句柄与缓冲写入器。
	handle     *os.File
	w          *bufio.Writer
	handlePath string
	dirty      bool
	lastUsed   time.Time
}

// MessageRepository 是消息持久化仓库：按会话分目录、按 execution 分片追加 JSONL。
// 布局：
//   - 旧版（只读兼容）：{dir}/{sessionID}.jsonl
//   - 分片：{dir}/{sessionID}/e{executionID}.jsonl（手动会话 executionID 记 0）
//   - 大 raw_json 旁路：{dir}/{sessionID}/blobs/{messageID}
//   - 全局自增 ID：{dir}/_next_id（块预留式，仅块耗尽时写）
//
// 每会话一把锁 + 内存读缓存：热路径读写互不阻塞其他会话；写入经 bufio 缓冲，
// 由后台 goroutine 周期 flush 并 LRU 回收空闲句柄/缓存。
type MessageRepository struct {
	dir string

	mu       sync.Mutex // 仅保护 sessions map
	sessions map[string]*sessionStore

	idMu       sync.Mutex
	idNext     uint64
	idReserved uint64
	idLoaded   bool

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewMessageRepository 创建文件型 MessageRepository，dir 不存在时自动创建，
// 并启动后台 flush/LRU 回收 goroutine（进程退出前应调用 Close 落盘残留缓冲）。
func NewMessageRepository(dir string) *MessageRepository {
	_ = os.MkdirAll(dir, 0o755)
	r := &MessageRepository{
		dir:      dir,
		sessions: map[string]*sessionStore{},
		stopCh:   make(chan struct{}),
	}
	go r.flushLoop()
	return r
}

// Close 停止后台 flush goroutine 并把所有缓冲落盘。
func (r *MessageRepository) Close() {
	r.stopOnce.Do(func() { close(r.stopCh) })
	r.mu.Lock()
	stores := make([]*sessionStore, 0, len(r.sessions))
	for _, st := range r.sessions {
		stores = append(stores, st)
	}
	r.mu.Unlock()
	for _, st := range stores {
		st.mu.Lock()
		st.closeHandleLocked()
		st.mu.Unlock()
	}
}

// flushLoop 周期性 flush dirty 缓冲，并按空闲时间回收句柄与读缓存。
func (r *MessageRepository) flushLoop() {
	ticker := time.NewTicker(writerFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			r.mu.Lock()
			type entry struct {
				id string
				st *sessionStore
			}
			stores := make([]entry, 0, len(r.sessions))
			for id, st := range r.sessions {
				stores = append(stores, entry{id, st})
			}
			r.mu.Unlock()
			for _, e := range stores {
				st := e.st
				st.mu.Lock()
				if st.dirty && st.w != nil {
					if err := st.w.Flush(); err == nil {
						st.dirty = false
					}
				}
				idle := now.Sub(st.lastUsed)
				if st.handle != nil && idle > handleIdleTimeout {
					st.closeHandleLocked()
				}
				evict := st.loaded && idle > cacheIdleTimeout
				if evict {
					st.closeHandleLocked()
					st.msgs = nil
					st.loaded = false
				}
				st.mu.Unlock()
				if evict {
					r.mu.Lock()
					if cur, ok := r.sessions[e.id]; ok && cur == st {
						delete(r.sessions, e.id)
					}
					r.mu.Unlock()
				}
			}
		}
	}
}

// ---- 路径与分片 ----

func (r *MessageRepository) legacyPath(sessionID string) string {
	return filepath.Join(r.dir, sessionID+".jsonl")
}

func (r *MessageRepository) sessionDir(sessionID string) string {
	return filepath.Join(r.dir, sessionID)
}

func (r *MessageRepository) blobDir(sessionID string) string {
	return filepath.Join(r.sessionDir(sessionID), "blobs")
}

// shardName 返回消息所属分片文件名：手动会话（executionID 为 nil）记 e0。
func shardName(executionID *uint) string {
	var id uint
	if executionID != nil {
		id = *executionID
	}
	return fmt.Sprintf("e%d.jsonl", id)
}

func (r *MessageRepository) shardPath(sessionID string, executionID *uint) string {
	return filepath.Join(r.sessionDir(sessionID), shardName(executionID))
}

// ---- 会话 store 获取与加载 ----

func (r *MessageRepository) store(sessionID string) *sessionStore {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.sessions[sessionID]
	if !ok {
		st = &sessionStore{}
		r.sessions[sessionID] = st
	}
	return st
}

// ensureLoadedLocked 惰性加载会话全部消息到缓存（旧版单文件 + 全部分片合并，按 sequence 升序）。
// 调用方需持有 st.mu。
func (r *MessageRepository) ensureLoadedLocked(st *sessionStore, sessionID string) error {
	if st.loaded {
		return nil
	}
	msgs, err := r.loadSessionFiles(sessionID)
	if err != nil {
		return err
	}
	st.msgs = msgs
	st.loaded = true
	return nil
}

// loadSessionFiles 从磁盘读取会话全部消息（不触碰缓存）：旧版单文件 + 分片目录。
func (r *MessageRepository) loadSessionFiles(sessionID string) ([]models.Message, error) {
	msgs, err := readMessagesFile(r.legacyPath(sessionID))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.sessionDir(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return msgs, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		part, err := readMessagesFile(filepath.Join(r.sessionDir(sessionID), e.Name()))
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, part...)
	}
	sort.SliceStable(msgs, func(i, j int) bool {
		return msgs[i].Sequence < msgs[j].Sequence
	})
	return msgs, nil
}

// loadLastNFromFilesLocked 按需从磁盘读取最近 n 条消息，不触发全量缓存加载。
// 调用方需持有 st.mu 且 st 未加载（loaded=false）。
//
// 策略：枚举会话目录下的 .jsonl 分片 + 旧版单文件，按文件名倒序排列
// （e10 > e2 > e0 > legacy），逐个解析并累积消息。当累积量 >= n 时停止读取更早分片。
// 最终按 sequence 升序排序并截取最近 n 条。分片内消息本就按 sequence 追加写入，
// 倒序读分片能以最少的 IO 覆盖最新消息。
func (r *MessageRepository) loadLastNFromFilesLocked(sessionID string, n int) ([]models.Message, error) {
	// 收集所有分片路径（旧版单文件 + 分片目录）
	var paths []string
	legacy := r.legacyPath(sessionID)
	if _, err := os.Stat(legacy); err == nil {
		paths = append(paths, legacy)
	}
	entries, err := os.ReadDir(r.sessionDir(sessionID))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(r.sessionDir(sessionID), e.Name()))
	}
	// 按文件名倒序：分片名 e{N}.jsonl，N 越大越新；旧版单文件排在最前（最旧）
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))

	var collected []models.Message
	for _, p := range paths {
		part, err := readMessagesFile(p)
		if err != nil {
			return nil, err
		}
		collected = append(collected, part...)
		if len(collected) >= n {
			break
		}
	}
	// 按 sequence 升序排序后截取最近 n 条
	sort.SliceStable(collected, func(i, j int) bool {
		return collected[i].Sequence < collected[j].Sequence
	})
	if n < len(collected) {
		collected = collected[len(collected)-n:]
	}
	return r.rehydrateAll(collected), nil
}

func readMessagesFile(path string) ([]models.Message, error) {
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
	// 旧版文件中 tool_call_update 的 raw_json 可能很大（新写入已旁路 blob）
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
	return msgs, nil
}

// ---- blob 旁路 ----

// offloadRawLocked 把超阈值的 raw_json 写入旁路 blob 文件，返回行内存引用的副本。
func (r *MessageRepository) offloadRawLocked(m models.Message) models.Message {
	if len(m.RawJSON) <= rawBlobThreshold {
		return m
	}
	dir := r.blobDir(m.SessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return m // 旁路失败退回行内存储
	}
	name := strconv.FormatUint(uint64(m.ID), 10)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(m.RawJSON), 0o644); err != nil {
		return m
	}
	m.RawJSON = blobRefPrefix + name
	return m
}

// rehydrate 把 blob 引用还原为完整 raw_json（返回副本，缓存内保持引用形态省内存）。
func (r *MessageRepository) rehydrate(m models.Message) models.Message {
	name, ok := strings.CutPrefix(m.RawJSON, blobRefPrefix)
	if !ok {
		return m
	}
	data, err := os.ReadFile(filepath.Join(r.blobDir(m.SessionID), filepath.Base(name)))
	if err != nil {
		m.RawJSON = ""
		return m
	}
	m.RawJSON = string(data)
	return m
}

func (r *MessageRepository) rehydrateAll(msgs []models.Message) []models.Message {
	out := make([]models.Message, len(msgs))
	for i := range msgs {
		out[i] = r.rehydrate(msgs[i])
	}
	return out
}

// removeBlobLocked 删除消息对应的旁路 blob（若为引用形态）。
func (r *MessageRepository) removeBlobLocked(m models.Message) {
	if name, ok := strings.CutPrefix(m.RawJSON, blobRefPrefix); ok {
		_ = os.Remove(filepath.Join(r.blobDir(m.SessionID), filepath.Base(name)))
	}
}

// ---- 写路径 ----

// Create 追加写入单条消息，并分配全局自增 ID。
// 写入进入 bufio 缓冲（最长 writerFlushInterval 后落盘）；读缓存同步更新，
// 因此 Create 返回后本仓库的所有读方法立即可见该消息。
func (r *MessageRepository) Create(m *models.Message) error {
	if m.SessionID == "" {
		return fmt.Errorf("session_id 不能为空")
	}
	// sessionID 参与文件路径拼接，拒绝路径穿越字符
	if strings.ContainsAny(m.SessionID, `/\`) || strings.Contains(m.SessionID, "..") {
		return fmt.Errorf("非法 session_id: %q", m.SessionID)
	}
	id, err := r.allocID()
	if err != nil {
		return err
	}
	m.ID = id
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}

	st := r.store(m.SessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := r.ensureLoadedLocked(st, m.SessionID); err != nil {
		return err
	}

	stored := r.offloadRawLocked(*m)
	line, err := json.Marshal(&stored)
	if err != nil {
		return err
	}
	if err := r.ensureHandleLocked(st, r.shardPath(m.SessionID, m.ExecutionID)); err != nil {
		return err
	}
	if _, err := st.w.Write(append(line, '\n')); err != nil {
		return err
	}
	st.dirty = true
	st.lastUsed = time.Now()

	// 追加到缓存并维持 sequence 升序（乱序落库极少见，仅此时局部重排）
	st.msgs = append(st.msgs, stored)
	if n := len(st.msgs); n > 1 && st.msgs[n-2].Sequence > stored.Sequence {
		sort.SliceStable(st.msgs, func(i, j int) bool {
			return st.msgs[i].Sequence < st.msgs[j].Sequence
		})
	}
	return nil
}

// ensureHandleLocked 保证当前活跃分片的 append 句柄已打开；切换分片时先落盘旧句柄。
func (r *MessageRepository) ensureHandleLocked(st *sessionStore, path string) error {
	if st.handle != nil && st.handlePath == path {
		return nil
	}
	st.closeHandleLocked()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	st.handle = f
	st.w = bufio.NewWriterSize(f, 64*1024)
	st.handlePath = path
	return nil
}

// closeHandleLocked flush 并关闭当前 append 句柄。调用方需持有 st.mu。
func (st *sessionStore) closeHandleLocked() {
	if st.w != nil {
		_ = st.w.Flush()
	}
	if st.handle != nil {
		_ = st.handle.Close()
	}
	st.handle = nil
	st.w = nil
	st.handlePath = ""
	st.dirty = false
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

// allocID 分配全局自增 ID：内存自增 + 块预留，仅块耗尽时写 _next_id 文件。
func (r *MessageRepository) allocID() (uint, error) {
	r.idMu.Lock()
	defer r.idMu.Unlock()
	if !r.idLoaded {
		data, err := os.ReadFile(filepath.Join(r.dir, nextIDFile))
		if err == nil {
			n, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
			r.idNext = n
		} else if !os.IsNotExist(err) {
			return 0, err
		}
		r.idReserved = r.idNext
		r.idLoaded = true
	}
	r.idNext++
	if r.idNext > r.idReserved {
		r.idReserved = r.idNext + idReserveBlock - 1
		if err := os.WriteFile(filepath.Join(r.dir, nextIDFile),
			[]byte(strconv.FormatUint(r.idReserved, 10)), 0o644); err != nil {
			return 0, err
		}
	}
	return uint(r.idNext), nil
}

// ---- 读路径（全部经会话缓存，加载一次后 O(1)/O(N) 内存操作） ----

// snapshotSession 返回会话消息缓存的引用切片（升序）。调用方不得修改元素。
func (r *MessageRepository) snapshotSession(sessionID string) ([]models.Message, error) {
	st := r.store(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := r.ensureLoadedLocked(st, sessionID); err != nil {
		return nil, err
	}
	st.lastUsed = time.Now()
	return st.msgs, nil
}

// FindByID 按消息主键查询；优先命中已加载缓存，未加载的会话直接扫文件（不污染缓存）。
func (r *MessageRepository) FindByID(id uint) (*models.Message, error) {
	// 先查已加载的缓存
	r.mu.Lock()
	loaded := make(map[string]*sessionStore, len(r.sessions))
	for sid, st := range r.sessions {
		loaded[sid] = st
	}
	r.mu.Unlock()
	for _, st := range loaded {
		st.mu.Lock()
		for i := range st.msgs {
			if st.msgs[i].ID == id {
				m := r.rehydrate(st.msgs[i])
				st.mu.Unlock()
				return &m, nil
			}
		}
		st.mu.Unlock()
	}
	// 再扫磁盘上未加载的会话（未加载即无缓冲中数据，直读文件安全）
	for _, sid := range r.listSessionIDs() {
		if st, ok := loaded[sid]; ok {
			st.mu.Lock()
			isLoaded := st.loaded
			st.mu.Unlock()
			if isLoaded {
				continue
			}
		}
		msgs, err := r.loadSessionFiles(sid)
		if err != nil {
			return nil, err
		}
		for i := range msgs {
			if msgs[i].ID == id {
				m := r.rehydrate(msgs[i])
				return &m, nil
			}
		}
	}
	return nil, fmt.Errorf("message not found")
}

// listSessionIDs 枚举磁盘上存在消息数据的会话 ID（旧版单文件 + 分片目录）。
func (r *MessageRepository) listSessionIDs() []string {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
			continue
		}
		if sid, ok := strings.CutSuffix(name, ".jsonl"); ok && sid != "" {
			if !seen[sid] {
				seen[sid] = true
				out = append(out, sid)
			}
		}
	}
	return out
}

// FindBySessionID 查询会话全部消息，按 sequence 升序。
func (r *MessageRepository) FindBySessionID(sessionID string) ([]models.Message, error) {
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return nil, err
	}
	return r.rehydrateAll(msgs), nil
}

// FindBySessionIDPaged 分页查询，按 sequence 升序。limit<=0 时不分页。
func (r *MessageRepository) FindBySessionIDPaged(sessionID string, limit, offset int) ([]models.Message, error) {
	msgs, err := r.snapshotSession(sessionID)
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
	return r.rehydrateAll(msgs), nil
}

// FindBySessionIDLastN 返回最近 n 条（升序）。n<=0 返回空切片。
//
// 冷缓存优化：若会话缓存尚未加载，不触发全量加载，而是按分片文件名倒序读取，
// 累积到 >= n 条即停止，避免长会话全量解析所有 JSONL 分片。
// 热缓存（已加载）仍走 snapshotSession 内存切片，O(1) 无 IO。
func (r *MessageRepository) FindBySessionIDLastN(sessionID string, n int) ([]models.Message, error) {
	if n <= 0 {
		return []models.Message{}, nil
	}
	st := r.store(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	// 热缓存：已加载则直接切片
	if st.loaded {
		st.lastUsed = time.Now()
		msgs := st.msgs
		if n < len(msgs) {
			msgs = msgs[len(msgs)-n:]
		}
		return r.rehydrateAll(msgs), nil
	}
	// 冷缓存：按需从最新分片倒序读取，避免全量加载
	return r.loadLastNFromFilesLocked(sessionID, n)
}

// FindBySessionIDBeforeLastN 返回 sequence < beforeSeq 的最近 n 条（升序）。
// beforeSeq<=0 时不限制 sequence（等价于 LastN）。n<=0 返回空切片。
// 用于「加载更多更早消息」：以当前最早可见消息的 sequence 为游标，向前翻页。
func (r *MessageRepository) FindBySessionIDBeforeLastN(sessionID string, beforeSeq int, n int) ([]models.Message, error) {
	if n <= 0 {
		return []models.Message{}, nil
	}
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return nil, err
	}
	if beforeSeq > 0 {
		// msgs 升序：二分找到第一个 >= beforeSeq 的位置即可截断
		idx := sort.Search(len(msgs), func(i int) bool { return msgs[i].Sequence >= beforeSeq })
		msgs = msgs[:idx]
	}
	if n < len(msgs) {
		msgs = msgs[len(msgs)-n:]
	}
	return r.rehydrateAll(msgs), nil
}

// FindByKind 查询指定 kind 的消息，按 sequence 升序。
func (r *MessageRepository) FindByKind(sessionID, kind string) ([]models.Message, error) {
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]models.Message, 0)
	for i := range msgs {
		if msgs[i].Kind == kind {
			out = append(out, r.rehydrate(msgs[i]))
		}
	}
	return out, nil
}

// FindLastByKind 返回指定 kind 的最新一条；无匹配时返回 nil, nil。
func (r *MessageRepository) FindLastByKind(sessionID, kind string) (*models.Message, error) {
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return nil, err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Kind == kind {
			m := r.rehydrate(msgs[i])
			return &m, nil
		}
	}
	return nil, nil
}

// FindBySessionIDAfter 返回 sequence > afterSeq 的消息，按 sequence 升序。
func (r *MessageRepository) FindBySessionIDAfter(sessionID string, afterSeq int) ([]models.Message, error) {
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return nil, err
	}
	idx := sort.Search(len(msgs), func(i int) bool { return msgs[i].Sequence > afterSeq })
	out := r.rehydrateAll(msgs[idx:])
	if out == nil {
		out = []models.Message{}
	}
	return out, nil
}

// MaxSequence 返回当前最大 sequence，无消息时返回 0。
// 缓存按升序维护，加载后为 O(1)。
func (r *MessageRepository) MaxSequence(sessionID string) (int, error) {
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	return msgs[len(msgs)-1].Sequence, nil
}

// ---- 删除路径 ----

// DeleteBySessionID 删除指定会话的全部消息数据（旧版单文件 + 分片目录 + blob）。
func (r *MessageRepository) DeleteBySessionID(sessionID string) error {
	st := r.store(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	st.closeHandleLocked()
	if err := os.Remove(r.legacyPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(r.sessionDir(sessionID)); err != nil {
		return err
	}
	st.msgs = nil
	st.loaded = true
	st.lastUsed = time.Now()
	return nil
}

// DeleteFromSequence 删除 sequence >= fromSeq 的消息并重写分片文件。
// 重写时旧版单文件一并迁移为分片布局；被删消息的旁路 blob 同步清理。
func (r *MessageRepository) DeleteFromSequence(sessionID string, fromSeq int) (int64, error) {
	st := r.store(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := r.ensureLoadedLocked(st, sessionID); err != nil {
		return 0, err
	}
	st.closeHandleLocked()

	kept := make([]models.Message, 0, len(st.msgs))
	var deleted int64
	for _, m := range st.msgs {
		if m.Sequence >= fromSeq {
			deleted++
			r.removeBlobLocked(m)
			continue
		}
		kept = append(kept, m)
	}
	if deleted == 0 {
		st.lastUsed = time.Now()
		return 0, nil
	}
	if err := r.rewriteShardsLocked(sessionID, kept); err != nil {
		return 0, err
	}
	st.msgs = kept
	st.lastUsed = time.Now()
	return deleted, nil
}

// rewriteShardsLocked 用 kept 重建会话的分片文件（tmp+rename 原子替换），
// 删除旧版单文件与不再存在的分片。调用方需持有 st.mu 且已关闭 append 句柄。
func (r *MessageRepository) rewriteShardsLocked(sessionID string, kept []models.Message) error {
	byShard := map[string][]models.Message{}
	for _, m := range kept {
		name := shardName(m.ExecutionID)
		byShard[name] = append(byShard[name], m)
	}
	dir := r.sessionDir(sessionID)
	if len(byShard) > 0 {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for name, msgs := range byShard {
		if err := writeMessagesFile(filepath.Join(dir, name), msgs); err != nil {
			return err
		}
	}
	// 清理不再有内容的旧分片
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if _, ok := byShard[e.Name()]; !ok {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	// 旧版单文件已合并进分片，删除避免重复加载
	if err := os.Remove(r.legacyPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeMessagesFile(path string, msgs []models.Message) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for i := range msgs {
		line, err := json.Marshal(&msgs[i])
		if err == nil {
			_, err = w.Write(line)
		}
		if err == nil {
			err = w.WriteByte('\n')
		}
		if err != nil {
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

// ---- 聚合 ----

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
	msgs, err := r.snapshotSession(sessionID)
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
	for i := range msgs {
		m := &msgs[i]
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
	msgs, err := r.snapshotSession(sessionID)
	if err != nil {
		return 0, err
	}
	var max uint
	for i := range msgs {
		if msgs[i].ExecutionID != nil && *msgs[i].ExecutionID > max {
			max = *msgs[i].ExecutionID
		}
	}
	return max, nil
}
