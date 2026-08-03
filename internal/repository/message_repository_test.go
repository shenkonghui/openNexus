package repository

import (
	"testing"

	"opennexus/internal/models"
)

func newTestMessageRepo(t *testing.T) *MessageRepository {
	t.Helper()
	return NewMessageRepository(t.TempDir())
}

func TestMessageRepo_Create(t *testing.T) {
	repo := newTestMessageRepo(t)

	m := &models.Message{
		SessionID:   "acp-create-1",
		DBSessionID: 10,
		Role:        models.MessageRoleUser,
		Kind:        models.MessageKindUserMessageChunk,
		Content:     "hello",
		RawJSON:     `{"x":1}`,
		Sequence:    1,
	}
	if err := repo.Create(m); err != nil {
		t.Fatalf("Create 返回错误: %v", err)
	}
	if m.ID == 0 {
		t.Error("期望创建后 ID 非零")
	}
}

func TestMessageRepo_CreateBatch(t *testing.T) {
	repo := newTestMessageRepo(t)

	msgs := []models.Message{
		{SessionID: "batch-1", DBSessionID: 20, Role: models.MessageRoleUser, Kind: models.MessageKindUserMessageChunk, Content: "q", RawJSON: "{}", Sequence: 1},
		{SessionID: "batch-1", DBSessionID: 20, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "a", RawJSON: "{}", Sequence: 2},
		{SessionID: "batch-1", DBSessionID: 20, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "b", RawJSON: "{}", Sequence: 3},
	}
	if err := repo.CreateBatch(msgs); err != nil {
		t.Fatalf("CreateBatch 返回错误: %v", err)
	}

	got, err := repo.FindBySessionID("batch-1")
	if err != nil {
		t.Fatalf("FindBySessionID 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("期望 3 条消息，实际 %d", len(got))
	}
}

func TestMessageRepo_FindBySessionID_OrderedBySequence(t *testing.T) {
	repo := newTestMessageRepo(t)

	_ = repo.Create(&models.Message{SessionID: "order-1", DBSessionID: 30, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "third", RawJSON: "{}", Sequence: 3})
	_ = repo.Create(&models.Message{SessionID: "order-1", DBSessionID: 30, Role: models.MessageRoleUser, Kind: models.MessageKindUserMessageChunk, Content: "first", RawJSON: "{}", Sequence: 1})
	_ = repo.Create(&models.Message{SessionID: "order-1", DBSessionID: 30, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "second", RawJSON: "{}", Sequence: 2})

	got, err := repo.FindBySessionID("order-1")
	if err != nil {
		t.Fatalf("FindBySessionID 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条消息，实际 %d", len(got))
	}
	if got[0].Content != "first" || got[1].Content != "second" || got[2].Content != "third" {
		t.Errorf("期望按 sequence 升序排列，实际 %s, %s, %s", got[0].Content, got[1].Content, got[2].Content)
	}
}

func TestMessageRepo_FindBySessionID_Empty(t *testing.T) {
	repo := newTestMessageRepo(t)

	got, err := repo.FindBySessionID("missing")
	if err != nil {
		t.Fatalf("空结果不应返回错误: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("期望 0 条消息，实际 %d", len(got))
	}
}

func TestMessageRepo_DeleteBySessionID(t *testing.T) {
	repo := newTestMessageRepo(t)

	_ = repo.Create(&models.Message{SessionID: "del-1", DBSessionID: 40, Role: models.MessageRoleUser, Kind: models.MessageKindUserMessageChunk, Content: "x", RawJSON: "{}", Sequence: 1})
	_ = repo.Create(&models.Message{SessionID: "del-1", DBSessionID: 40, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "y", RawJSON: "{}", Sequence: 2})

	if err := repo.DeleteBySessionID("del-1"); err != nil {
		t.Fatalf("DeleteBySessionID 返回错误: %v", err)
	}

	got, _ := repo.FindBySessionID("del-1")
	if len(got) != 0 {
		t.Errorf("期望删除后 0 条消息，实际 %d", len(got))
	}
}

func TestMessageRepo_MaxSequence_Empty(t *testing.T) {
	repo := newTestMessageRepo(t)

	max, err := repo.MaxSequence("empty")
	if err != nil {
		t.Fatalf("MaxSequence 返回错误: %v", err)
	}
	if max != 0 {
		t.Errorf("空表期望 max=0，实际 %d", max)
	}
}

func TestMessageRepo_MaxSequence(t *testing.T) {
	repo := newTestMessageRepo(t)

	_ = repo.Create(&models.Message{SessionID: "max-1", DBSessionID: 60, Role: models.MessageRoleUser, Kind: models.MessageKindUserMessageChunk, Content: "a", RawJSON: "{}", Sequence: 5})
	_ = repo.Create(&models.Message{SessionID: "max-1", DBSessionID: 60, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "b", RawJSON: "{}", Sequence: 12})
	_ = repo.Create(&models.Message{SessionID: "max-1", DBSessionID: 60, Role: models.MessageRoleAssistant, Kind: models.MessageKindAgentMessageChunk, Content: "c", RawJSON: "{}", Sequence: 8})

	max, err := repo.MaxSequence("max-1")
	if err != nil {
		t.Fatalf("MaxSequence 返回错误: %v", err)
	}
	if max != 12 {
		t.Errorf("期望 max=12，实际 %d", max)
	}
}

func TestMessageRepo_FindByID(t *testing.T) {
	repo := newTestMessageRepo(t)
	m := &models.Message{
		SessionID: "id-1", DBSessionID: 1, Role: models.MessageRoleUser,
		Kind: models.MessageKindUserMessageChunk, Content: "hi", RawJSON: "{}", Sequence: 1,
	}
	if err := repo.Create(m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.FindByID(m.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Content != "hi" {
		t.Errorf("Content = %q", got.Content)
	}
}

func TestMessageRepo_DeleteFromSequence(t *testing.T) {
	repo := newTestMessageRepo(t)
	for seq := 1; seq <= 5; seq++ {
		_ = repo.Create(&models.Message{
			SessionID: "trunc-1", DBSessionID: 1, Role: models.MessageRoleAssistant,
			Kind: models.MessageKindAgentMessageChunk, Content: "m", RawJSON: "{}", Sequence: seq,
		})
	}
	n, err := repo.DeleteFromSequence("trunc-1", 3)
	if err != nil {
		t.Fatalf("DeleteFromSequence: %v", err)
	}
	if n != 3 {
		t.Errorf("期望删除 3 条，实际 %d", n)
	}
	got, _ := repo.FindBySessionID("trunc-1")
	if len(got) != 2 || got[1].Sequence != 2 {
		t.Errorf("期望保留 seq 1,2，实际 %+v", got)
	}
}

func TestMessageRepo_FindBySessionIDAfter(t *testing.T) {
	repo := newTestMessageRepo(t)
	for seq := 1; seq <= 4; seq++ {
		_ = repo.Create(&models.Message{
			SessionID: "after-1", DBSessionID: 1, Role: models.MessageRoleAssistant,
			Kind: models.MessageKindAgentMessageChunk, Content: "m", RawJSON: "{}", Sequence: seq,
		})
	}
	got, err := repo.FindBySessionIDAfter("after-1", 2)
	if err != nil {
		t.Fatalf("FindBySessionIDAfter: %v", err)
	}
	if len(got) != 2 || got[0].Sequence != 3 || got[1].Sequence != 4 {
		t.Errorf("期望 [3,4]，实际 %+v", got)
	}
}

// 插入 seq=1..5 共 5 条消息，供分页/限量/按 kind 查询测试共用。
func seedPagingMessages(t *testing.T, repo *MessageRepository) {
	t.Helper()
	for seq := 1; seq <= 5; seq++ {
		kind := models.MessageKindAgentMessageChunk
		if seq == 3 {
			kind = models.MessageKindUsageUpdate
		}
		_ = repo.Create(&models.Message{
			SessionID: "pg-1", DBSessionID: 70, Role: models.MessageRoleAssistant,
			Kind: kind, Content: "m" + itoa(seq), RawJSON: `{"seq":` + itoa(seq) + `}`, Sequence: seq,
		})
	}
}

func TestMessageRepo_FindBySessionIDLastN(t *testing.T) {
	repo := newTestMessageRepo(t)
	seedPagingMessages(t, repo)

	got, err := repo.FindBySessionIDLastN("pg-1", 3)
	if err != nil {
		t.Fatalf("FindBySessionIDLastN 返回错误: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("期望 3 条，实际 %d", len(got))
	}
	if got[0].Sequence != 3 || got[2].Sequence != 5 {
		t.Errorf("期望升序 [3,4,5]，实际 %d,%d,%d", got[0].Sequence, got[1].Sequence, got[2].Sequence)
	}

	all, _ := repo.FindBySessionIDLastN("pg-1", 100)
	if len(all) != 5 {
		t.Errorf("n>total 时期望 5 条，实际 %d", len(all))
	}

	zero, _ := repo.FindBySessionIDLastN("pg-1", 0)
	if len(zero) != 0 {
		t.Errorf("n<=0 时期望空，实际 %d", len(zero))
	}
}

func TestMessageRepo_FindBySessionIDBeforeLastN(t *testing.T) {
	repo := newTestMessageRepo(t)
	seedPagingMessages(t, repo)

	// before=4：取 sequence<4 的最近 2 条 → [2,3]
	got, err := repo.FindBySessionIDBeforeLastN("pg-1", 4, 2)
	if err != nil {
		t.Fatalf("FindBySessionIDBeforeLastN 返回错误: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(got))
	}
	if got[0].Sequence != 2 || got[1].Sequence != 3 {
		t.Errorf("期望 [2,3]，实际 %d,%d", got[0].Sequence, got[1].Sequence)
	}

	// before=4：n 超过可用数 → 返回全部更早的 [1,2,3]
	all, _ := repo.FindBySessionIDBeforeLastN("pg-1", 4, 100)
	if len(all) != 3 || all[0].Sequence != 1 || all[2].Sequence != 3 {
		t.Errorf("期望 [1,2,3]，实际 %v", sequencesOf(all))
	}

	// before<=0：等价于 LastN
	last, _ := repo.FindBySessionIDBeforeLastN("pg-1", 0, 3)
	if len(last) != 3 || last[0].Sequence != 3 || last[2].Sequence != 5 {
		t.Errorf("before<=0 期望最近 3 条 [3,4,5]，实际 %v", sequencesOf(last))
	}

	// n<=0：空
	zero, _ := repo.FindBySessionIDBeforeLastN("pg-1", 4, 0)
	if len(zero) != 0 {
		t.Errorf("n<=0 期望空，实际 %d", len(zero))
	}

	// before=1：没有更早的消息 → 空
	none, _ := repo.FindBySessionIDBeforeLastN("pg-1", 1, 10)
	if len(none) != 0 {
		t.Errorf("before=1 期望空，实际 %d", len(none))
	}
}

func sequencesOf(msgs []models.Message) []int {
	out := make([]int, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Sequence)
	}
	return out
}

func TestMessageRepo_FindBySessionIDPaged(t *testing.T) {
	repo := newTestMessageRepo(t)
	seedPagingMessages(t, repo)

	page1, err := repo.FindBySessionIDPaged("pg-1", 2, 0)
	if err != nil {
		t.Fatalf("FindBySessionIDPaged page1 返回错误: %v", err)
	}
	if len(page1) != 2 || page1[0].Sequence != 1 || page1[1].Sequence != 2 {
		t.Errorf("page1 错误: %+v", page1)
	}
	page2, _ := repo.FindBySessionIDPaged("pg-1", 2, 2)
	if len(page2) != 2 || page2[0].Sequence != 3 || page2[1].Sequence != 4 {
		t.Errorf("page2 错误: %+v", page2)
	}
	all, _ := repo.FindBySessionIDPaged("pg-1", 0, 0)
	if len(all) != 5 {
		t.Errorf("limit<=0 期望全量 5 条，实际 %d", len(all))
	}
}

func TestMessageRepo_FindByKind(t *testing.T) {
	repo := newTestMessageRepo(t)
	seedPagingMessages(t, repo)

	got, err := repo.FindByKind("pg-1", models.MessageKindUsageUpdate)
	if err != nil {
		t.Fatalf("FindByKind 返回错误: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("期望 1 条 usage_update，实际 %d", len(got))
	}
	if got[0].Sequence != 3 {
		t.Errorf("期望 seq=3，实际 %d", got[0].Sequence)
	}
}

func TestMessageRepo_FindLastByKind(t *testing.T) {
	repo := newTestMessageRepo(t)
	seedPagingMessages(t, repo)

	_ = repo.Create(&models.Message{
		SessionID: "pg-1", DBSessionID: 70, Role: models.MessageRoleAssistant,
		Kind: models.MessageKindUsageUpdate, Content: "u2", RawJSON: `{"seq":6}`, Sequence: 6,
	})

	got, err := repo.FindLastByKind("pg-1", models.MessageKindUsageUpdate)
	if err != nil {
		t.Fatalf("FindLastByKind 返回错误: %v", err)
	}
	if got == nil {
		t.Fatal("期望非 nil")
	}
	if got.Sequence != 6 {
		t.Errorf("期望最后一条 seq=6，实际 %d", got.Sequence)
	}

	none, err := repo.FindLastByKind("pg-1", "nonexistent-kind")
	if err != nil {
		t.Fatalf("不存在的 kind 不应返回错误: %v", err)
	}
	if none != nil {
		t.Errorf("期望 nil，实际 %+v", none)
	}
}

// TestMessageRepo_FindBySessionIDLastN_ColdCache 验证冷缓存（未触发全量加载）时
// FindBySessionIDLastN 仍能正确返回最近 N 条，且不污染会话缓存（loaded 保持 false）。
func TestMessageRepo_FindBySessionIDLastN_ColdCache(t *testing.T) {
	repo := newTestMessageRepo(t)
	// 写入跨多个 execution 分片的消息：e0 含 seq 1-3，e1 含 seq 4-6
	exec0 := uint(0)
	exec1 := uint(1)
	for seq := 1; seq <= 6; seq++ {
		m := &models.Message{
			SessionID: "cold-1", DBSessionID: 1, Role: models.MessageRoleAssistant,
			Kind: models.MessageKindAgentMessageChunk, Content: "m", RawJSON: "{}", Sequence: seq,
		}
		if seq > 3 {
			m.ExecutionID = &exec1
		} else {
			m.ExecutionID = &exec0
		}
		if err := repo.Create(m); err != nil {
			t.Fatalf("Create seq=%d: %v", seq, err)
		}
	}

	// 新建独立 repo 模拟冷缓存（原 repo 的 Create 已触发加载）
	// 先关闭原 repo 刷盘 bufio 缓冲，确保冷 repo 能从磁盘读到完整数据
	repo.Close()
	coldRepo := NewMessageRepository(repo.dir)
	got, err := coldRepo.FindBySessionIDLastN("cold-1", 4)
	if err != nil {
		t.Fatalf("FindBySessionIDLastN 冷缓存: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("期望 4 条，实际 %d", len(got))
	}
	if got[0].Sequence != 3 || got[3].Sequence != 6 {
		t.Errorf("期望升序 [3,4,5,6]，实际 %v", sequencesOf(got))
	}

	// 验证冷缓存未触发全量加载（loaded 仍为 false）
	st := coldRepo.store("cold-1")
	st.mu.Lock()
	loaded := st.loaded
	st.mu.Unlock()
	if loaded {
		t.Error("冷缓存 FindBySessionIDLastN 不应触发全量加载")
	}

	// n 超过总量时返回全部
	all, _ := coldRepo.FindBySessionIDLastN("cold-1", 100)
	if len(all) != 6 {
		t.Errorf("n>total 期望 6 条，实际 %d", len(all))
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
