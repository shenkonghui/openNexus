package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
)

// 文件读写大小上限（1MB），避免传输超大文件。
const maxFileRWSize = 1 << 20

// sessionFileEntry 文件树节点。
type sessionFileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // 相对 session cwd 的路径
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size,omitempty"`
}

// SessionFileHandler 处理会话工作目录内的文件浏览与编辑。
// 所有路径均限制在 session.cwd 之内，防止路径穿越。
type SessionFileHandler struct {
	store SessionStore
}

// NewSessionFileHandler 创建 SessionFileHandler。
func NewSessionFileHandler(store SessionStore) *SessionFileHandler {
	return &SessionFileHandler{store: store}
}

// resolveSessionPath 加载会话并将请求的相对路径解析为 cwd 内的绝对路径。
// relPath 为空时返回 cwd 本身。失败时已写入错误响应。
func (h *SessionFileHandler) resolveSessionPath(c *gin.Context) (*models.Session, string, bool) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return nil, "", false
	}
	cwd := resolveSessionWorkingDir(h.store, sess)
	if cwd == "" {
		Fail(c, http.StatusBadRequest, "NO_CWD", "该会话没有工作目录")
		return nil, "", false
	}

	relPath := strings.TrimSpace(c.Query("path"))
	absPath, err := safeJoin(cwd, relPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", err.Error())
		return nil, "", false
	}
	return sess, absPath, true
}

// loadOwnedSession 加载 :id 对应会话并校验归属（复用 SessionHandler 的逻辑）。
func (h *SessionFileHandler) loadOwnedSession(c *gin.Context) (*models.Session, bool) {
	id, ok := parseSessionID(c)
	if !ok {
		return nil, false
	}
	sess, err := h.store.GetSessionByDBID(id)
	if err != nil || sess == nil {
		Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "会话不存在")
		return nil, false
	}
	uid, ok := currentUserID(c)
	if !ok || sess.UserID != uid {
		Fail(c, http.StatusNotFound, "SESSION_NOT_FOUND", "会话不存在")
		return nil, false
	}
	return sess, true
}

// safeJoin 将 relPath 安全地拼接在 root 下，返回绝对路径。
// 解析后的路径必须在 root 之内，否则返回错误。
func safeJoin(root, relPath string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if relPath == "" || relPath == "." {
		return rootAbs, nil
	}
	// 清理相对路径，防止 ../ 穿越到 root 之外
	cleaned := filepath.Clean("/" + relPath) // 前置 / 使其无法逃逸 root
	joined := filepath.Join(rootAbs, cleaned)
	if !isWithin(rootAbs, joined) {
		return "", errPathOutsideCwd
	}
	return joined, nil
}

// isWithin 判断 target 是否在 root 目录内（或等于 root）。
func isWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var errPathOutsideCwd = &pathError{"路径超出会话工作目录范围"}

type pathError struct{ msg string }

func (e *pathError) Error() string { return e.msg }

// ListFiles GET /api/v1/sessions/:id/files?path=...
// 列出 session cwd 下指定子路径的文件和目录（单层，懒加载）。
// path 为空或 "." 时列出 cwd 根目录。目录排前、文件排后。
func (h *SessionFileHandler) ListFiles(c *gin.Context) {
	sess, absPath, ok := h.resolveSessionPath(c)
	if !ok {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			Fail(c, http.StatusNotFound, "PATH_NOT_FOUND", "路径不存在")
			return
		}
		Fail(c, http.StatusForbidden, "ACCESS_DENIED", "无法访问该路径")
		return
	}
	if !info.IsDir() {
		Fail(c, http.StatusBadRequest, "NOT_A_DIRECTORY", "路径不是目录")
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取目录内容")
		return
	}

	result := make([]sessionFileEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		// 跳过隐藏文件
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(absPath, name)
		rel, _ := filepath.Rel(sess.Cwd, full)
		fe := sessionFileEntry{
			Name:  name,
			Path:  rel,
			IsDir: entry.IsDir(),
		}
		if !entry.IsDir() {
			if fi, err := entry.Info(); err == nil {
				fe.Size = fi.Size()
			}
		}
		result = append(result, fe)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir // 目录排前
		}
		return result[i].Name < result[j].Name
	})

	Success(c, http.StatusOK, gin.H{
		"cwd":     sess.Cwd,
		"path":    strings.TrimSpace(c.Query("path")),
		"entries": result,
	})
}

// ReadFile GET /api/v1/sessions/:id/files/content?path=...
// 读取 session cwd 下的文件内容（文本）。超过 maxFileRWSize 时拒绝。
func (h *SessionFileHandler) ReadFile(c *gin.Context) {
	_, absPath, ok := h.resolveSessionPath(c)
	if !ok {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			Fail(c, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在")
			return
		}
		Fail(c, http.StatusForbidden, "ACCESS_DENIED", "无法访问该文件")
		return
	}
	if info.IsDir() {
		Fail(c, http.StatusBadRequest, "IS_A_DIRECTORY", "路径是目录，不是文件")
		return
	}
	if info.Size() > maxFileRWSize {
		Fail(c, http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			"文件过大，最大支持 1MB")
		return
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取文件")
		return
	}

	Success(c, http.StatusOK, gin.H{
		"path":    strings.TrimSpace(c.Query("path")),
		"content": string(data),
		"size":    info.Size(),
	})
}

// WriteFile PUT /api/v1/sessions/:id/files/content
// Body: { "path": "relative/path", "content": "..." }
// 保存文件内容到 session cwd 下。文件不存在则创建，存在则覆盖。
func (h *SessionFileHandler) WriteFile(c *gin.Context) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return
	}
	if sess.Cwd == "" {
		Fail(c, http.StatusBadRequest, "NO_CWD", "该会话没有工作目录")
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求体格式错误")
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		Fail(c, http.StatusBadRequest, "PATH_REQUIRED", "path 不能为空")
		return
	}
	if len(req.Content) > maxFileRWSize {
		Fail(c, http.StatusRequestEntityTooLarge, "CONTENT_TOO_LARGE",
			"内容过大，最大支持 1MB")
		return
	}

	absPath, err := safeJoin(sess.Cwd, req.Path)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", err.Error())
		return
	}

	// 确保父目录存在
	parent := filepath.Dir(absPath)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "无法创建父目录")
		return
	}

	if err := os.WriteFile(absPath, []byte(req.Content), 0o644); err != nil {
		Fail(c, http.StatusInternalServerError, "WRITE_FAILED", "写入文件失败")
		return
	}

	Success(c, http.StatusOK, gin.H{
		"path": req.Path,
		"size": len(req.Content),
	})
}

// undoDiffItem 表示快照消息 raw_json.content[] 中单个 diff 项。
type undoDiffItem struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	OldText string `json:"oldText"` // 修改前内容；新文件时为空（字段缺失）
	NewText string `json:"newText"`
}

// parseSnapshotDiffs 从消息 raw_json 中解析 content[] 的 diff 项。
// 仅返回 type=="diff" 且 path 非空的项。
func parseSnapshotDiffs(rawJSON string) []undoDiffItem {
	var payload struct {
		Content []undoDiffItem `json:"content"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &payload); err != nil {
		return nil
	}
	var items []undoDiffItem
	for _, item := range payload.Content {
		if item.Type == "diff" && item.Path != "" {
			items = append(items, item)
		}
	}
	return items
}

// fileChangeEntry 是 ListFileChanges 返回的单个文件改动项。
type fileChangeEntry struct {
	Path    string `json:"path"`     // 相对 cwd 的路径
	OldText string `json:"old_text"` // 修改前内容（新文件为空）
	NewText string `json:"new_text"` // 修改后内容
	IsNew   bool   `json:"is_new"`   // 是否为新建文件
}

// ListFileChanges GET /api/v1/sessions/:id/files/changes
// 从持久化的快照消息中聚合当前会话所有文件改动（按路径去重，保留最新）。
// 数据来源是 DB 中的 snapshot diff 消息，不依赖前端内存。
// 路径统一归一化为相对 cwd 的相对路径，避免绝对/相对路径混用导致重复。
func (h *SessionFileHandler) ListFileChanges(c *gin.Context) {
	sess, cwd, ok := h.resolveSessionCwd(c)
	if !ok {
		return
	}

	// 仅加载 tool_call_update 消息，避免为文件变更列表全量载入历史 raw_json
	snapshotMsgs, err := h.store.ListMessagesByKind(sess.SessionID, "tool_call_update")
	if err != nil {
		Fail(c, http.StatusInternalServerError, "LOAD_FAILED", "加载消息失败")
		return
	}

	// 按归一化路径去重，保留最新（sequence 最大）的 diff 项
	latest := make(map[string]undoDiffItem)
	for i := range snapshotMsgs {
		m := &snapshotMsgs[i]
		items := parseSnapshotDiffs(m.RawJSON)
		for _, item := range items {
			relPath := normalizeRelPath(item.Path, cwd)
			// 更新 item.Path 为归一化后的相对路径
			item.Path = relPath
			latest[relPath] = item // 后出现的覆盖前者（消息按 sequence 升序）
		}
	}

	entries := make([]fileChangeEntry, 0, len(latest))
	for _, item := range latest {
		entries = append(entries, fileChangeEntry{
			Path:    item.Path,
			OldText: item.OldText,
			NewText: item.NewText,
			IsNew:   item.OldText == "",
		})
	}

	Success(c, http.StatusOK, gin.H{
		"changes": entries,
		"count":   len(entries),
	})
}

// normalizeRelPath 将路径归一化为相对 cwd 的路径。
// 绝对路径转为相对；已经是相对路径的保持不变。
func normalizeRelPath(path, cwd string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil {
		return path
	}
	return rel
}

// applyUndoDiffs 反向应用一组 diff 项，将文件恢复到修改前状态。
// 有 oldText → 覆盖回去；无 oldText（新建文件）→ 删除。
// 返回恢复的文件数、删除的文件数和错误列表。
func applyUndoDiffs(cwd string, items []undoDiffItem) (restored, deleted int, errs []string) {
	for _, item := range items {
		absPath, err := safeJoin(cwd, item.Path)
		if err != nil {
			errs = append(errs, item.Path+": 路径非法")
			continue
		}

		if item.OldText != "" {
			// 修改的文件：恢复修改前内容
			parent := filepath.Dir(absPath)
			if err := os.MkdirAll(parent, 0o755); err != nil {
				errs = append(errs, item.Path+": 无法创建父目录")
				continue
			}
			if err := os.WriteFile(absPath, []byte(item.OldText), 0o644); err != nil {
				errs = append(errs, item.Path+": 写入失败")
				continue
			}
			restored++
		} else {
			// 新建的文件：删除（忽略 notexist）
			if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, item.Path+": 删除失败")
				continue
			}
			deleted++
		}
	}
	return
}

// resolveSessionCwd 加载会话并解析工作目录（普通会话取工作区 cwd，编排任务取 worktree 路径）。
func (h *SessionFileHandler) resolveSessionCwd(c *gin.Context) (*models.Session, string, bool) {
	sess, ok := h.loadOwnedSession(c)
	if !ok {
		return nil, "", false
	}
	cwd := resolveSessionWorkingDir(h.store, sess)
	if cwd == "" {
		Fail(c, http.StatusBadRequest, "NO_CWD", "该会话没有工作目录")
		return nil, "", false
	}
	return sess, cwd, true
}

// UndoFileChanges POST /api/v1/sessions/:id/files/undo
// Body: { "message_id": 123 }
// 根据单条快照消息中记录的文件改动，反向恢复：有 oldText 的覆盖回去，新文件（无 oldText）则删除。
func (h *SessionFileHandler) UndoFileChanges(c *gin.Context) {
	sess, cwd, ok := h.resolveSessionCwd(c)
	if !ok {
		return
	}

	var req struct {
		MessageID uint `json:"message_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.MessageID == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "message_id 不能为空")
		return
	}

	// 加载快照消息并校验归属
	msg, err := h.store.FindMessageByID(req.MessageID)
	if err != nil || msg == nil {
		Fail(c, http.StatusNotFound, "MESSAGE_NOT_FOUND", "消息不存在")
		return
	}
	if msg.DBSessionID != sess.ID {
		Fail(c, http.StatusNotFound, "MESSAGE_NOT_FOUND", "消息不存在")
		return
	}

	items := parseSnapshotDiffs(msg.RawJSON)
	if items == nil {
		Fail(c, http.StatusBadRequest, "INVALID_MESSAGE", "消息不是有效的文件改动记录")
		return
	}

	restored, deleted, errs := applyUndoDiffs(cwd, items)

	Success(c, http.StatusOK, gin.H{
		"restored": restored,
		"deleted":  deleted,
		"errors":   errs,
	})
}

// RestoreToCheckpoint POST /api/v1/sessions/:id/files/restore
// Body: { "sequence": 20 }  // 目标用户消息的 sequence
// 将工作区恢复到该消息发送之前的状态，删除该消息及其之后的所有消息（会话回滚），
// 并返回该消息的文本内容供前端填充到输入框。
// 文件恢复：反向应用该消息及之后所有轮次的快照 diff（按 sequence 降序）。
// 消息回滚：删除 sequence >= 目标 的全部消息。
func (h *SessionFileHandler) RestoreToCheckpoint(c *gin.Context) {
	sess, cwd, ok := h.resolveSessionCwd(c)
	if !ok {
		return
	}

	var req struct {
		Sequence int `json:"sequence"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Sequence <= 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "sequence 不能为空")
		return
	}

	// 加载会话全部消息
	allMsgs, err := h.store.ListMessages(sess.SessionID)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "LOAD_FAILED", "加载消息失败")
		return
	}

	// 提取目标用户消息的文本内容（恢复后填充到输入框）
	promptText := ""
	for i := range allMsgs {
		m := &allMsgs[i]
		if m.Sequence == req.Sequence && m.Role == "user" {
			promptText = m.Content
			break
		}
	}

	// 收集 sequence >= 目标 的 snapshot 消息（tool_call_update 且 raw_json 含 snapshot-）
	var snapshots []models.Message
	for i := range allMsgs {
		m := &allMsgs[i]
		if m.Sequence >= req.Sequence &&
			m.Kind == "tool_call_update" &&
			strings.Contains(m.RawJSON, "snapshot-") {
			snapshots = append(snapshots, *m)
		}
	}

	// 按 sequence 降序排列（最新优先撤销）
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Sequence > snapshots[j].Sequence
	})

	// 1. 文件恢复：反向应用每轮的快照 diff
	totalRestored := 0
	totalDeleted := 0
	var allErrs []string
	turnsReverted := 0

	for i := range snapshots {
		items := parseSnapshotDiffs(snapshots[i].RawJSON)
		if len(items) == 0 {
			continue
		}
		r, d, errs := applyUndoDiffs(cwd, items)
		totalRestored += r
		totalDeleted += d
		allErrs = append(allErrs, errs...)
		turnsReverted++
	}

	// 2. 消息回滚：删除目标消息及其之后的所有消息
	msgsDeleted, err := h.store.DeleteMessagesFromSequence(sess.SessionID, req.Sequence)
	if err != nil {
		allErrs = append(allErrs, "消息回滚失败: "+err.Error())
	}

	Success(c, http.StatusOK, gin.H{
		"restored":         totalRestored,
		"deleted":          totalDeleted,
		"turns_reverted":   turnsReverted,
		"messages_deleted": msgsDeleted,
		"prompt_text":      promptText,
		"errors":           allErrs,
	})
}
