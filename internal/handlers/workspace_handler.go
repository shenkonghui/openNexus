package handlers

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"opennexus/internal/models"
	"opennexus/internal/workspacemeta"
)

// WorkspaceStore 暴露 workspace 相关能力。
type WorkspaceStore interface {
	CreateWorkspace(ws *models.Workspace) error
	FindWorkspaceByID(id uint) (*models.Workspace, error)
	FindWorkspacesByUserID(userID uint) ([]models.Workspace, error)
	FindWorkspaceByUserIDAndCwd(userID uint, cwd string) (*models.Workspace, error)
	FindDefaultWorkspaceByUserID(userID uint) (*models.Workspace, error)
	UpdateWorkspace(id uint, updates map[string]interface{}) error
	DeleteWorkspace(id uint) error
	WorkspaceSessionCount(workspaceID uint) (int64, error)
	FindSessionsByWorkspaceID(workspaceID uint) ([]models.Session, error)
	DeleteSessionWithMessages(session *models.Session) error
	GetWorkspaceCwd(workspaceID uint) (string, error)
}

// WorkspaceHandler 处理 workspace 相关请求。
type WorkspaceHandler struct {
	store WorkspaceStore
}

// NewWorkspaceHandler 创建 WorkspaceHandler。
func NewWorkspaceHandler(store WorkspaceStore) *WorkspaceHandler {
	return &WorkspaceHandler{store: store}
}

func parseWorkspaceID(c *gin.Context) (uint, bool) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil || id == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "无效的工作区 ID")
		return 0, false
	}
	return uint(id), true
}

// Create POST /api/v1/workspaces
func (h *WorkspaceHandler) Create(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Cwd         string   `json:"cwd" binding:"required"`
		Directories []string `json:"directories"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Cwd = strings.TrimSpace(req.Cwd)
	if req.Name == "" || req.Cwd == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "名称和目录不能为空")
		return
	}
	info, err := os.Stat(req.Cwd)
	if err != nil || !info.IsDir() {
		Fail(c, http.StatusBadRequest, "CWD_NOT_FOUND", "目录不存在: "+req.Cwd)
		return
	}
	// 校验附加目录均存在
	dirs, dirErr := validateDirectories(req.Directories)
	if dirErr != nil {
		Fail(c, http.StatusBadRequest, "DIR_NOT_FOUND", dirErr.Error())
		return
	}
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	if existing, _ := h.store.FindWorkspaceByUserIDAndCwd(uid, req.Cwd); existing != nil {
		Fail(c, http.StatusConflict, "DUPLICATE", "该目录已绑定工作区")
		return
	}
	ws := &models.Workspace{
		UserID:      uid,
		Name:        req.Name,
		Cwd:         req.Cwd,
		Directories: dirs,
		Mode:        models.WorkspaceModePersistent,
	}
	if err := h.store.CreateWorkspace(ws); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	Success(c, http.StatusCreated, ws)
}

// List GET /api/v1/workspaces
func (h *WorkspaceHandler) List(c *gin.Context) {
	uid, ok := currentUserID(c)
	if !ok {
		Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "未认证")
		return
	}
	workspaces, err := h.store.FindWorkspacesByUserID(uid)
	if err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if workspaces == nil {
		workspaces = []models.Workspace{}
	}
	type wsWithCount struct {
		models.Workspace
		SessionCount int64 `json:"session_count"`
	}
	result := make([]wsWithCount, 0, len(workspaces))
	for _, ws := range workspaces {
		count, _ := h.store.WorkspaceSessionCount(ws.ID)
		result = append(result, wsWithCount{Workspace: ws, SessionCount: count})
	}
	Success(c, http.StatusOK, gin.H{"workspaces": result})
}

// Get GET /api/v1/workspaces/:id
func (h *WorkspaceHandler) Get(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	ws, err := h.store.FindWorkspaceByID(id)
	if err != nil || ws == nil {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	uid, _ := currentUserID(c)
	if ws.UserID != uid {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	sessions, err := h.store.FindSessionsByWorkspaceID(ws.ID)
	if err != nil {
		sessions = []models.Session{}
	}
	Success(c, http.StatusOK, gin.H{"workspace": ws, "sessions": sessions})
}

// Update PUT /api/v1/workspaces/:id
func (h *WorkspaceHandler) Update(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	ws, err := h.store.FindWorkspaceByID(id)
	if err != nil || ws == nil {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	uid, _ := currentUserID(c)
	if ws.UserID != uid {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Directories []string `json:"directories"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "名称不能为空")
		return
	}
	updates := map[string]interface{}{"name": req.Name}
	var validatedDirs models.StringArray
	// directories 字段存在时（包括空数组）才更新
	if req.Directories != nil {
		dirs, dirErr := validateDirectories(req.Directories)
		if dirErr != nil {
			Fail(c, http.StatusBadRequest, "DIR_NOT_FOUND", dirErr.Error())
			return
		}
		validatedDirs = models.StringArray(dirs)
		updates["directories"] = validatedDirs
	}
	if err := h.store.UpdateWorkspace(id, updates); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ws.Name = req.Name
	if req.Directories != nil {
		ws.Directories = validatedDirs
	}
	Success(c, http.StatusOK, ws)
}

// Delete DELETE /api/v1/workspaces/:id
func (h *WorkspaceHandler) Delete(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	ws, err := h.store.FindWorkspaceByID(id)
	if err != nil || ws == nil {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	uid, _ := currentUserID(c)
	if ws.UserID != uid {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	sessions, _ := h.store.FindSessionsByWorkspaceID(ws.ID)
	for _, sess := range sessions {
		_ = h.store.DeleteSessionWithMessages(&sess)
	}
	if err := h.store.DeleteWorkspace(id); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	// 同步清理该工作区的管理数据目录（tasks.json、执行记录、上传文件等）；
	// 失败仅记日志，不影响删除结果。回退模式（meta 目录即 cwd）下不清理，避免误删工作目录。
	if cwd := strings.TrimSpace(ws.Cwd); cwd != "" {
		if metaDir := workspacemeta.DirFor(cwd); metaDir != cwd {
			if err := os.RemoveAll(metaDir); err != nil {
				slog.Warn("清理工作区管理数据目录失败", "dir", metaDir, "err", err)
			}
		}
	}
	Success(c, http.StatusOK, struct{}{})
}

// Save POST /api/v1/workspaces/:id/save
func (h *WorkspaceHandler) Save(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	ws, err := h.store.FindWorkspaceByID(id)
	if err != nil || ws == nil {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	uid, _ := currentUserID(c)
	if ws.UserID != uid {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	if ws.Mode != models.WorkspaceModeTemporary {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "只能保存临时工作区")
		return
	}
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Cwd         string   `json:"cwd" binding:"required"`
		Directories []string `json:"directories"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "请求参数无效")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Cwd = strings.TrimSpace(req.Cwd)
	if req.Name == "" || req.Cwd == "" {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "名称和目录不能为空")
		return
	}
	info, err := os.Stat(req.Cwd)
	if err != nil || !info.IsDir() {
		Fail(c, http.StatusBadRequest, "CWD_NOT_FOUND", "目录不存在: "+req.Cwd)
		return
	}
	dirs, dirErr := validateDirectories(req.Directories)
	if dirErr != nil {
		Fail(c, http.StatusBadRequest, "DIR_NOT_FOUND", dirErr.Error())
		return
	}
	if err := h.store.UpdateWorkspace(id, map[string]interface{}{
		"name":        req.Name,
		"cwd":         req.Cwd,
		"directories": models.StringArray(dirs),
		"mode":        models.WorkspaceModePersistent,
	}); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ws.Name = req.Name
	ws.Cwd = req.Cwd
	ws.Mode = models.WorkspaceModePersistent
	ws.Directories = dirs
	Success(c, http.StatusOK, ws)
}

// Upload POST /api/v1/workspaces/:id/uploads （multipart/form-data）
// 接收前端拖拽的文件,落盘到工作区管理数据目录的 uploads/ 子目录,返回落盘后的绝对路径。
// 用于"远程运行"场景下把浏览器端文件接入对话——前端拿到绝对路径后以 @<path> 引用,
// 复用与本地场景相同的 @ 引用协议(后端无需感知附件概念)。
//
// 字段名 "files" 对应 multipart 各部分;每部分可有多个。
func (h *WorkspaceHandler) Upload(c *gin.Context) {
	id, ok := parseWorkspaceID(c)
	if !ok {
		return
	}
	ws, err := h.store.FindWorkspaceByID(id)
	if err != nil || ws == nil {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	uid, _ := currentUserID(c)
	if ws.UserID != uid {
		Fail(c, http.StatusNotFound, "NOT_FOUND", "工作区不存在")
		return
	}
	cwd := strings.TrimSpace(ws.Cwd)
	if cwd == "" {
		Fail(c, http.StatusBadRequest, "NO_CWD", "工作区未配置目录")
		return
	}
	// 落盘到工作区管理数据目录的 uploads/ 下，与 agent 工作目录分离，避免污染用户仓库。
	// 额外按上传时间建一层日期目录,便于清理与隔离。
	uploadDir := filepath.Join(workspacemeta.UploadsDirFor(cwd), time.Now().Format("20060102"))
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		Fail(c, http.StatusInternalServerError, "INTERNAL", "创建上传目录失败: "+err.Error())
		return
	}

	form, err := c.MultipartForm()
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "解析 multipart 失败: "+err.Error())
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "未包含任何文件")
		return
	}

	type savedFile struct {
		Name string `json:"name"`
		Path string `json:"path"` // 服务器侧绝对路径
		Size int64  `json:"size"`
	}
	saved := make([]savedFile, 0, len(files))
	for _, fh := range files {
		name := sanitizeUploadName(fh.Filename)
		if name == "" {
			continue
		}
		dst := filepath.Join(uploadDir, name)
		// 二次防护:确保最终路径仍位于 uploadDir 之下(防 ../)。
		if !isWithinDir(uploadDir, dst) {
			Fail(c, http.StatusBadRequest, "INVALID_PATH", "非法的文件路径: "+fh.Filename)
			return
		}
		if err := c.SaveUploadedFile(fh, dst); err != nil {
			Fail(c, http.StatusInternalServerError, "INTERNAL", "保存文件失败: "+err.Error())
			return
		}
		saved = append(saved, savedFile{Name: fh.Filename, Path: dst, Size: fh.Size})
	}
	Success(c, http.StatusOK, gin.H{"files": saved})
}

// sanitizeUploadName 清洗上传文件名:
//   - 取 basename,去掉任何路径前缀
//   - 空名/全点目录名视为非法
//   - 追加 时间戳-随机数 前缀避免同名覆盖
func sanitizeUploadName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == ".." {
		return ""
	}
	// 控制字符 / 路径分隔符已由 Base 处理;这里再过滤掉 Windows 非法字符以免跨平台问题。
	name = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		if r < 0x20 {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf("%d-%04d-%s", time.Now().UnixNano(), rand.Intn(10000), name)
}

// isWithinDir 判断 target 是否位于 base 目录之下(均需为已 Clean 的绝对/相对路径)。
func isWithinDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel != ".." && !strings.HasPrefix(rel, "../") && rel != "."
}

// validateDirectories 去重并校验每个附加目录存在且是目录。
// 返回去重后的路径切片。
func validateDirectories(dirs []string) ([]string, error) {
	seen := make(map[string]bool)
	result := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("附加目录不存在: %s", d)
		}
		result = append(result, d)
	}
	return result, nil
}
