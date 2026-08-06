package handlers

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
	"opennexus/internal/config"
)

// dirEntry 是目录浏览 API 返回的单个目录项。
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// fileEntry 是文件列表 API 返回的单个文件/目录项。
type fileEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// FileSystemHandler 提供本地文件系统目录浏览能力（用于前端目录选择器）。
// 扫描目录配置支持热刷新（SetScanDirs），故用 RWMutex 保护。
type FileSystemHandler struct {
	mu                  sync.RWMutex
	skillUserDirs       []string
	skillProjectDirs    []string
	commandUserDirs     []string
	commandProjectDirs  []string
	ruleUserDirs        []string
	ruleProjectDirs     []string
	subAgentUserDirs    []string
	subAgentProjectDirs []string
}

// NewFileSystemHandler 创建 FileSystemHandler。
func NewFileSystemHandler(skills config.SkillsConfig, commands config.CommandsConfig, rules config.RulesConfig, subAgents config.SubAgentsConfig) *FileSystemHandler {
	return &FileSystemHandler{
		skillUserDirs:       append([]string(nil), skills.UserDirs...),
		skillProjectDirs:    append([]string(nil), skills.ProjectDirs...),
		commandUserDirs:     append([]string(nil), commands.UserDirs...),
		commandProjectDirs:  append([]string(nil), commands.ProjectDirs...),
		ruleUserDirs:        append([]string(nil), rules.UserDirs...),
		ruleProjectDirs:     append([]string(nil), rules.ProjectDirs...),
		subAgentUserDirs:    append([]string(nil), subAgents.UserDirs...),
		subAgentProjectDirs: append([]string(nil), subAgents.ProjectDirs...),
	}
}

// SetScanDirs 热刷新 skill/command/rule/subagent 的扫描目录配置（软重载入口）。
func (h *FileSystemHandler) SetScanDirs(skills config.SkillsConfig, commands config.CommandsConfig, rules config.RulesConfig, subAgents config.SubAgentsConfig) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.skillUserDirs = append([]string(nil), skills.UserDirs...)
	h.skillProjectDirs = append([]string(nil), skills.ProjectDirs...)
	h.commandUserDirs = append([]string(nil), commands.UserDirs...)
	h.commandProjectDirs = append([]string(nil), commands.ProjectDirs...)
	h.ruleUserDirs = append([]string(nil), rules.UserDirs...)
	h.ruleProjectDirs = append([]string(nil), rules.ProjectDirs...)
	h.subAgentUserDirs = append([]string(nil), subAgents.UserDirs...)
	h.subAgentProjectDirs = append([]string(nil), subAgents.ProjectDirs...)
}

// snapshotScanDirs 在读锁下返回当前扫描目录的快照，供 Skills/Commands/Rules/SubAgents handler 安全使用。
func (h *FileSystemHandler) snapshotScanDirs() (skillUser, skillProj, cmdUser, cmdProj, ruleUser, ruleProj, subUser, subProj []string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	skillUser = append([]string(nil), h.skillUserDirs...)
	skillProj = append([]string(nil), h.skillProjectDirs...)
	cmdUser = append([]string(nil), h.commandUserDirs...)
	cmdProj = append([]string(nil), h.commandProjectDirs...)
	ruleUser = append([]string(nil), h.ruleUserDirs...)
	ruleProj = append([]string(nil), h.ruleProjectDirs...)
	subUser = append([]string(nil), h.subAgentUserDirs...)
	subProj = append([]string(nil), h.subAgentProjectDirs...)
	return
}

// resolveDirPath 解析并校验请求路径，返回绝对路径。失败时已写入错误响应。
func resolveDirPath(c *gin.Context) (string, bool) {
	reqPath := strings.TrimSpace(c.Query("path"))

	if reqPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			Fail(c, http.StatusInternalServerError, "HOME_UNAVAILABLE", "无法获取用户主目录")
			return "", false
		}
		reqPath = home
	}

	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return "", false
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			Fail(c, http.StatusNotFound, "PATH_NOT_FOUND", "目录不存在")
			return "", false
		}
		Fail(c, http.StatusForbidden, "PATH_ACCESS_DENIED", "无法访问该目录")
		return "", false
	}
	if !info.IsDir() {
		Fail(c, http.StatusBadRequest, "NOT_A_DIRECTORY", "路径不是目录")
		return "", false
	}
	return absPath, true
}

// ListDirs GET /api/v1/filesystem/dirs?path=...
// 返回指定目录下的子目录列表（仅目录，不含文件）。
// path 为空时默认返回用户主目录及其子目录。
func (h *FileSystemHandler) ListDirs(c *gin.Context) {
	absPath, ok := resolveDirPath(c)
	if !ok {
		return
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取目录内容")
		return
	}

	dirs := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// 跳过隐藏目录（以 . 开头）
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, dirEntry{
			Name: name,
			Path: filepath.Join(absPath, name),
		})
	}

	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})

	Success(c, http.StatusOK, gin.H{
		"current_path": absPath,
		"parent_path":  parentPath(absPath),
		"dirs":         dirs,
	})
}

// ListWorktrees GET /api/v1/filesystem/worktrees?path=<仓库内任意路径>
// 返回 path 所在 git 仓库的全部 worktree（来自 git worktree list）。
// path 必填：前端新建任务页选择 worktree 时，以当前工作区 cwd 定位仓库。
func (h *FileSystemHandler) ListWorktrees(c *gin.Context) {
	reqPath := strings.TrimSpace(c.Query("path"))
	if reqPath == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数")
		return
	}
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		Fail(c, http.StatusNotFound, "PATH_NOT_FOUND", "目录不存在")
		return
	}
	worktrees, err := acplocal.ListWorktrees(absPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "NOT_A_GIT_REPO", "当前目录不是 git 仓库，无法列出 worktree")
		return
	}
	Success(c, http.StatusOK, gin.H{
		"repo_root": func() string {
			root, err := acplocal.GitRoot(absPath)
			if err != nil {
				return absPath
			}
			return root
		}(),
		"worktrees": worktrees,
	})
}

// CreateWorktree POST /api/v1/filesystem/worktrees
// Body: { "path": "<仓库内路径>", "branch": "<新分支名>", "base": "<可选基准引用>" }
// 在 ~/.openNexus/worktrees/<仓库名>/<分支名> 下创建新 worktree（分支名中的 / 替换为 -）。
// 创建成功后返回该 worktree 信息；目录已存在返回 409。
func (h *FileSystemHandler) CreateWorktree(c *gin.Context) {
	var req struct {
		Path   string `json:"path" binding:"required"`
		Branch string `json:"branch" binding:"required"`
		Base   string `json:"base"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" {
		Fail(c, http.StatusBadRequest, "INVALID_BRANCH", "分支名不能为空")
		return
	}
	// 分支名仅允许 ASCII（英文、数字、下划线、连字符、斜杠），禁止中文等非 ASCII 字符。
	// 斜杠用于 feat/、fix/ 前缀，目录名会由 WorktreePath 扁平化为单层。
	for _, r := range branch {
		if r > 127 {
			Fail(c, http.StatusBadRequest, "INVALID_BRANCH",
				"分支名只能包含英文、数字、下划线、连字符和斜杠，请勿使用中文")
			return
		}
	}
	absPath, err := filepath.Abs(strings.TrimSpace(req.Path))
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	repoRoot, err := acplocal.GitRoot(absPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "NOT_A_GIT_REPO", "当前目录不是 git 仓库，无法创建 worktree")
		return
	}
	if err := acplocal.EnsureWorktreesDir(repoRoot); err != nil {
		Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建 worktrees 目录失败")
		return
	}
	// worktree 目录名由 WorktreePath 统一扁平化（分支名中的 / → -，如 feat/x -> feat-x），
	// 这里直接用原始分支名，避免双重替换。
	destPath := acplocal.WorktreePath(repoRoot, branch)
	if err := acplocal.CreateWorktree(repoRoot, branch, destPath, strings.TrimSpace(req.Base)); err != nil {
		if strings.Contains(err.Error(), "已存在") {
			Fail(c, http.StatusConflict, "WORKTREE_EXISTS", err.Error())
			return
		}
		Fail(c, http.StatusBadRequest, "CREATE_WORKTREE_FAILED", err.Error())
		return
	}
	// 重新列出，返回与 ListWorktrees 一致的条目结构
	var created *acplocal.WorktreeInfo
	if list, err := acplocal.ListWorktrees(repoRoot); err == nil {
		for i := range list {
			if list[i].Path == destPath {
				created = &list[i]
				break
			}
		}
	}
	if created == nil {
		created = &acplocal.WorktreeInfo{Path: destPath, Branch: branch}
	}
	Success(c, http.StatusOK, gin.H{"worktree": created})
}

// ListFiles GET /api/v1/filesystem/list?path=...&query=...
// 返回指定目录下的文件和目录列表，支持 query 过滤文件名。
// 用于 @ 文件引用的自动补全。目录排前、文件排后，跳过隐藏文件和常见忽略目录。
func (h *FileSystemHandler) ListFiles(c *gin.Context) {
	absPath, ok := resolveDirPath(c)
	if !ok {
		return
	}

	query := strings.ToLower(strings.TrimSpace(c.Query("query")))

	entries, err := os.ReadDir(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取目录内容")
		return
	}

	// 常见忽略目录名
	ignoreDirs := map[string]bool{
		"node_modules": true, ".git": true, "dist": true, "build": true,
		".next": true, "__pycache__": true, ".venv": true, "vendor": true,
	}

	var dirs, files []fileEntry
	for _, entry := range entries {
		name := entry.Name()
		// 跳过隐藏文件
		if strings.HasPrefix(name, ".") {
			continue
		}
		// 跳过忽略目录
		if entry.IsDir() && ignoreDirs[name] {
			continue
		}
		// query 过滤
		if query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		fe := fileEntry{
			Name:  name,
			Path:  filepath.Join(absPath, name),
			IsDir: entry.IsDir(),
		}
		if entry.IsDir() {
			dirs = append(dirs, fe)
		} else {
			files = append(files, fe)
		}
	}

	// 限制返回数量，避免超大目录
	const maxItems = 100
	result := make([]fileEntry, 0, len(dirs)+len(files))
	result = append(result, dirs...)
	result = append(result, files...)
	if len(result) > maxItems {
		result = result[:maxItems]
	}

	Success(c, http.StatusOK, gin.H{
		"current_path": absPath,
		"parent_path":  parentPath(absPath),
		"entries":      result,
	})
}

// docFileEntry 是文档扫描 API 返回的单个 .md 文件项。
type docFileEntry struct {
	Name    string `json:"name"`     // 文件名（如 foo.md）
	RelPath string `json:"rel_path"` // 相对扫描根目录的路径（如 sub/foo.md），用于前端展示与路由
	AbsPath string `json:"abs_path"` // 绝对路径，用于读取内容
}

// ListDocs GET /api/v1/filesystem/docs?path=<绝对目录>
// 递归扫描指定目录下所有 .md 文件（含子目录），用于侧边栏文档文件夹绑定。
// 跳过与 ListFiles 一致的忽略目录（node_modules/.git 等）和隐藏目录，限制结果数量防超大目录。
func (h *FileSystemHandler) ListDocs(c *gin.Context) {
	absPath, ok := resolveDirPath(c)
	if !ok {
		return
	}

	// 与 ListFiles 保持一致的忽略目录集合
	ignoreDirs := map[string]bool{
		"node_modules": true, ".git": true, "dist": true, "build": true,
		".next": true, "__pycache__": true, ".venv": true, "vendor": true,
	}

	const maxFiles = 500 // 扫描结果上限，防超大目录拖慢
	var files []docFileEntry
	truncated := false

	_ = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过无法访问的项，继续扫描
		}
		name := d.Name()
		// 跳过隐藏文件/目录（. 开头）——注意根目录自身 name 不以 . 开头
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != absPath && ignoreDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}
		// 仅收集 .md 文件
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		if len(files) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(absPath, path)
		if err != nil {
			rel = name
		}
		files = append(files, docFileEntry{
			Name:    name,
			RelPath: filepath.ToSlash(rel),
			AbsPath: path,
		})
		return nil
	})

	// 按相对路径排序，保证展示稳定
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	Success(c, http.StatusOK, gin.H{
		"root":      absPath,
		"files":     files,
		"truncated": truncated,
	})
}

// Skills GET /api/v1/filesystem/skills?path=...
// 扫描指定目录下的 Agent Skills（agentskills.io 规范），用于新建任务页 / 命令补全。
// path 可为空或无效：仍会扫描用户主目录下的 skills。
func (h *FileSystemHandler) Skills(c *gin.Context) {
	scanCwd := strings.TrimSpace(c.Query("path"))
	if scanCwd != "" {
		absPath, err := filepath.Abs(scanCwd)
		if err != nil {
			scanCwd = ""
		} else if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
			scanCwd = ""
		} else {
			scanCwd = absPath
		}
	}
	skillUser, skillProj, _, _, _, _, _, _ := h.snapshotScanDirs()
	skills := acplocal.ScanSkills(scanCwd, skillUser, skillProj)
	items := make([]skillItem, 0, len(skills))
	for _, s := range skills {
		items = append(items, skillItem{
			Name:        s.Name,
			Description: s.Description,
			Location:    s.Location,
			Scope:       s.Scope,
			Path:        s.Path,
		})
	}
	Success(c, http.StatusOK, gin.H{"skills": items})
}

type slashCommandItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Scope       string `json:"scope"`
	Path        string `json:"path"`
}

// Commands GET /api/v1/filesystem/commands?path=...
// 扫描指定目录下的 Slash Commands（Claude Code 规范），用于新建任务页 / 命令补全。
func (h *FileSystemHandler) Commands(c *gin.Context) {
	scanCwd := strings.TrimSpace(c.Query("path"))
	if scanCwd != "" {
		absPath, err := filepath.Abs(scanCwd)
		if err != nil {
			scanCwd = ""
		} else if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
			scanCwd = ""
		} else {
			scanCwd = absPath
		}
	}
	_, _, cmdUser, cmdProj, _, _, _, _ := h.snapshotScanDirs()
	commands := acplocal.ScanSlashCommands(scanCwd, cmdUser, cmdProj)
	items := make([]slashCommandItem, 0, len(commands))
	for _, cmd := range commands {
		items = append(items, slashCommandItem{
			Name:        cmd.Name,
			Description: cmd.Description,
			Location:    cmd.Location,
			Scope:       cmd.Scope,
			Path:        cmd.Path,
		})
	}
	Success(c, http.StatusOK, gin.H{"commands": items})
}

// parentPath 返回父目录路径，根目录时返回自身。
func parentPath(p string) string {
	parent := filepath.Dir(p)
	if parent == p {
		return ""
	}
	return parent
}

type ruleItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Location    string `json:"location"`
	Scope       string `json:"scope"`
	Path        string `json:"path"`
	AlwaysApply bool   `json:"always_apply"`
	Globs       string `json:"globs,omitempty"`
}

// Rules GET /api/v1/filesystem/rules?path=...
// 扫描指定目录下的 Rules（Cursor 规范：递归 *.mdc / *.md）。
func (h *FileSystemHandler) Rules(c *gin.Context) {
	scanCwd := strings.TrimSpace(c.Query("path"))
	if scanCwd != "" {
		absPath, err := filepath.Abs(scanCwd)
		if err != nil {
			scanCwd = ""
		} else if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
			scanCwd = ""
		} else {
			scanCwd = absPath
		}
	}
	_, _, _, _, ruleUser, ruleProj, _, _ := h.snapshotScanDirs()
	rules := acplocal.ScanRules(scanCwd, ruleUser, ruleProj)
	items := make([]ruleItem, 0, len(rules))
	for _, r := range rules {
		items = append(items, ruleItem{
			Name:        r.Name,
			Description: r.Description,
			Location:    r.Location,
			Scope:       r.Scope,
			Path:        r.Location,
			AlwaysApply: r.AlwaysApply,
			Globs:       r.Globs,
		})
	}
	Success(c, http.StatusOK, gin.H{"rules": items})
}

type subAgentItem struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Model       string   `json:"model,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	Location    string   `json:"location"`
	Scope       string   `json:"scope"`
	Path        string   `json:"path"`
}

// SubAgents GET /api/v1/filesystem/sub-agents?path=...
// 扫描指定目录下的 subagent 定义文件（frontmatter 含 name/description/model/tools，正文当 system_prompt）。
// path 可为空或无效：仍会扫描用户主目录下的 subagents。
func (h *FileSystemHandler) SubAgents(c *gin.Context) {
	scanCwd := strings.TrimSpace(c.Query("path"))
	if scanCwd != "" {
		absPath, err := filepath.Abs(scanCwd)
		if err != nil {
			scanCwd = ""
		} else if info, err := os.Stat(absPath); err != nil || !info.IsDir() {
			scanCwd = ""
		} else {
			scanCwd = absPath
		}
	}
	_, _, _, _, _, _, subUser, subProj := h.snapshotScanDirs()
	defs := acplocal.ScanSubAgents(scanCwd, subUser, subProj)
	items := make([]subAgentItem, 0, len(defs))
	for _, d := range defs {
		items = append(items, subAgentItem{
			Name:        d.Name,
			Description: d.Description,
			Model:       d.Model,
			Tools:       d.Tools,
			Location:    d.Location,
			Scope:       d.Scope,
			Path:        d.Path,
		})
	}
	Success(c, http.StatusOK, gin.H{"subagents": items})
}

// ReadFile GET /api/v1/filesystem/file?path=...
// 读取指定文件的文本内容（仅限文本文件，最大 1MB）。
func (h *FileSystemHandler) ReadFile(c *gin.Context) {
	reqPath := strings.TrimSpace(c.Query("path"))
	if reqPath == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数")
		return
	}
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		Fail(c, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在")
		return
	}
	if info.IsDir() {
		Fail(c, http.StatusBadRequest, "NOT_A_FILE", "路径不是文件")
		return
	}
	const maxSize = 1 << 20 // 1MB
	if info.Size() > maxSize {
		Fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", "文件过大（最大 1MB）")
		return
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取文件")
		return
	}
	Success(c, http.StatusOK, gin.H{
		"path":    absPath,
		"content": string(data),
		"size":    info.Size(),
	})
}

// ReadFileBinary GET /api/v1/filesystem/file-binary?path=...
// 读取指定文件的原始字节（用于 Excel 等二进制文件，前端拿 ArrayBuffer 解析）。
// 最大 20MB。直接以 application/octet-stream 返回，前端用 fetch.arrayBuffer() 接收。
func (h *FileSystemHandler) ReadFileBinary(c *gin.Context) {
	reqPath := strings.TrimSpace(c.Query("path"))
	if reqPath == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数")
		return
	}
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		Fail(c, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在")
		return
	}
	if info.IsDir() {
		Fail(c, http.StatusBadRequest, "NOT_A_FILE", "路径不是文件")
		return
	}
	const maxSize = 20 << 20 // 20MB
	if info.Size() > maxSize {
		Fail(c, http.StatusBadRequest, "FILE_TOO_LARGE", "文件过大（最大 20MB）")
		return
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		Fail(c, http.StatusForbidden, "READ_DENIED", "无法读取文件")
		return
	}
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// WriteFile PUT /api/v1/filesystem/file
// 将文本内容写入指定文件。
func (h *FileSystemHandler) WriteFile(c *gin.Context) {
	var req struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}
	absPath, err := filepath.Abs(req.Path)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	// 确保父目录存在
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建目录失败")
		return
	}
	if err := os.WriteFile(absPath, []byte(req.Content), 0o644); err != nil {
		Fail(c, http.StatusInternalServerError, "WRITE_FAILED", "写入文件失败")
		return
	}
	info, _ := os.Stat(absPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	Success(c, http.StatusOK, gin.H{
		"path": absPath,
		"size": size,
	})
}

// CreateEntry POST /api/v1/filesystem/create
// Body: { "path": "<绝对路径>", "is_dir": bool }
// 新建空文件或目录。目标已存在时返回冲突错误。
func (h *FileSystemHandler) CreateEntry(c *gin.Context) {
	var req struct {
		Path  string `json:"path" binding:"required"`
		IsDir bool   `json:"is_dir"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_JSON", "请求参数格式错误")
		return
	}
	absPath, err := filepath.Abs(strings.TrimSpace(req.Path))
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	if _, err := os.Stat(absPath); err == nil {
		Fail(c, http.StatusConflict, "ALREADY_EXISTS", "同名文件或目录已存在")
		return
	}
	if req.IsDir {
		if err := os.MkdirAll(absPath, 0o755); err != nil {
			Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建目录失败")
			return
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建父目录失败")
			return
		}
		f, err := os.OpenFile(absPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			Fail(c, http.StatusInternalServerError, "CREATE_FAILED", "创建文件失败")
			return
		}
		_ = f.Close()
	}
	Success(c, http.StatusOK, gin.H{
		"path":   absPath,
		"is_dir": req.IsDir,
	})
}

// DeleteEntry DELETE /api/v1/filesystem/entry?path=<绝对路径>
// 删除指定文件或目录（目录递归删除）。
func (h *FileSystemHandler) DeleteEntry(c *gin.Context) {
	reqPath := strings.TrimSpace(c.Query("path"))
	if reqPath == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数")
		return
	}
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			Fail(c, http.StatusNotFound, "PATH_NOT_FOUND", "文件或目录不存在")
			return
		}
		Fail(c, http.StatusForbidden, "PATH_ACCESS_DENIED", "无法访问该路径")
		return
	}
	if info.IsDir() {
		if err := os.RemoveAll(absPath); err != nil {
			Fail(c, http.StatusInternalServerError, "DELETE_FAILED", "删除目录失败")
			return
		}
	} else {
		if err := os.Remove(absPath); err != nil {
			Fail(c, http.StatusInternalServerError, "DELETE_FAILED", "删除文件失败")
			return
		}
	}
	Success(c, http.StatusOK, gin.H{
		"path":    absPath,
		"deleted": true,
	})
}

// skillUploadMaxSize 单个上传文件的大小上限（10MB），避免大文件撑爆磁盘。
const skillUploadMaxSize = 10 << 20

// skillUploadMaxFiles 单次上传文件数量上限，避免恶意/误操作上传海量文件。
const skillUploadMaxFiles = 200

// defaultSkillProjectSubdir 上传 skill 时默认写入的项目级子目录（相对 cwd）。
const defaultSkillProjectSubdir = ".agents/skills"

// UploadSkill POST /api/v1/filesystem/skills/upload?path=<项目cwd>&target_subdir=<可选>
// 接收前端通过 <input webkitdirectory> 选择的本地 skill 目录（multipart/form-data），
// 按原始目录结构写入项目 cwd 下的 skills 扫描目录（默认 .agents/skills），
// 使其立即可被 ScanSkills 发现并在能力面板展示。
//
// 落盘策略（安全）：先写入一个临时 staging 目录，全部文件落盘且校验通过（必须含 SKILL.md）
// 后，才把 staging 内的文件移动到 targetRoot。任何校验失败或中途出错时只清理 staging
// 目录（由本请求创建，可安全整体删除），绝不删除 targetRoot——后者是项目共享的 skills
// 目录，可能已存在其他 skill，误删会造成用户数据丢失。
//
// multipart 字段：
//   - files：一个或多个文件部分（与 workspace uploads 一致）
//   - relative_paths：与 files 一一对应的表单字段，值为每个文件在所选目录中的相对路径
//     （来自浏览器 File.webkitRelativePath，如 my-skill/SKILL.md）
//
// 校验：上传内容必须包含 SKILL.md；相对路径不能逃逸目标目录；单文件 ≤10MB；文件数 ≤200。
func (h *FileSystemHandler) UploadSkill(c *gin.Context) {
	// 1. 解析并校验项目 cwd
	cwd := strings.TrimSpace(c.Query("path"))
	if cwd == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数（项目工作目录）")
		return
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return
	}
	if info, err := os.Stat(absCwd); err != nil || !info.IsDir() {
		Fail(c, http.StatusBadRequest, "PATH_NOT_FOUND", "项目目录不存在或不是目录")
		return
	}

	// 2. 解析目标子目录（相对 cwd），默认 .agents/skills
	subdir := strings.TrimSpace(c.DefaultQuery("target_subdir", defaultSkillProjectSubdir))
	if subdir == "" {
		subdir = defaultSkillProjectSubdir
	}
	// 安全校验：子目录不能逃逸 cwd（防止 ../../）
	subdir = filepath.Clean(filepath.FromSlash(subdir))
	if strings.HasPrefix(subdir, "..") || filepath.IsAbs(subdir) {
		Fail(c, http.StatusBadRequest, "INVALID_TARGET", "目标子目录不能为绝对路径或逃逸项目目录")
		return
	}
	targetRoot := filepath.Join(absCwd, subdir)

	// 3. 解析 multipart
	if err := c.Request.ParseMultipartForm(skillUploadMaxSize); err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "解析 multipart 失败: "+err.Error())
		return
	}
	form := c.Request.MultipartForm
	if form == nil {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "未包含任何文件")
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST", "未包含任何文件")
		return
	}
	if len(files) > skillUploadMaxFiles {
		Fail(c, http.StatusBadRequest, "TOO_MANY_FILES",
			fmt.Sprintf("上传文件数量 %d 超过上限 %d", len(files), skillUploadMaxFiles))
		return
	}
	// relative_paths 与 files 一一对应（来自 webkitRelativePath）
	relPaths := form.Value["relative_paths"]
	if len(relPaths) != len(files) {
		Fail(c, http.StatusBadRequest, "INVALID_REQUEST",
			fmt.Sprintf("relative_paths 数量 %d 与 files 数量 %d 不一致", len(relPaths), len(files)))
		return
	}

	// 4. 创建 staging 临时目录：所有文件先落盘到这里，校验通过后再移动到 targetRoot。
	// staging 置于系统临时目录，与 targetRoot 完全隔离，失败时只清理 staging（本请求创建，
	// 可安全整体删除），绝不触碰 targetRoot 中已有的其他 skill。
	stagingRoot, err := os.MkdirTemp("", "nexus-skill-upload-*")
	if err != nil {
		Fail(c, http.StatusInternalServerError, "STAGING_FAILED", "创建临时目录失败: "+err.Error())
		return
	}
	// 无论成功失败，最终都清理 staging（成功时文件已被移走，目录为空）。
	defer func() { _ = os.RemoveAll(stagingRoot) }()

	// 5. 逐个落盘到 staging 并收集结果
	type savedEntry struct {
		RelativePath string `json:"relative_path"`
		Size         int64  `json:"size"`
	}
	saved := make([]savedEntry, 0, len(files))
	hasSkillMD := false
	for i, fh := range files {
		rel := strings.TrimSpace(relPaths[i])
		if rel == "" {
			// 回退到文件名（非目录上传场景）
			rel = fh.Filename
		}
		// 统一为平台分隔符并清洗
		rel = filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			Fail(c, http.StatusBadRequest, "INVALID_PATH", "非法的相对路径: "+relPaths[i])
			return
		}
		if fh.Size > skillUploadMaxSize {
			Fail(c, http.StatusBadRequest, "FILE_TOO_LARGE",
				fmt.Sprintf("文件 %s 过大（%d 字节，上限 %d）", rel, fh.Size, skillUploadMaxSize))
			return
		}
		// 落盘到 staging（与 targetRoot 同结构），二次防护确保仍在 staging 之下
		dst := filepath.Join(stagingRoot, rel)
		if !isWithinDir(stagingRoot, dst) {
			Fail(c, http.StatusBadRequest, "INVALID_PATH", "文件路径逃逸目标目录: "+relPaths[i])
			return
		}
		if filepath.Base(rel) == "SKILL.md" {
			hasSkillMD = true
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建目录失败: "+err.Error())
			return
		}
		if err := c.SaveUploadedFile(fh, dst); err != nil {
			Fail(c, http.StatusInternalServerError, "SAVE_FAILED", "保存文件失败: "+err.Error())
			return
		}
		saved = append(saved, savedEntry{RelativePath: filepath.ToSlash(rel), Size: fh.Size})
	}

	if !hasSkillMD {
		Fail(c, http.StatusBadRequest, "NO_SKILL_MD", "上传内容必须包含 SKILL.md 文件")
		return
	}

	// 6. 校验全部通过：把 staging 内的文件移动到 targetRoot。
	// 仅覆盖本次上传涉及的文件（同路径文件覆盖，与原 SaveUploadedFile 行为一致），
	// 不影响 targetRoot 中已有的其他 skill。
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		Fail(c, http.StatusInternalServerError, "MKDIR_FAILED", "创建目标目录失败: "+err.Error())
		return
	}
	if err := moveTree(stagingRoot, targetRoot); err != nil {
		Fail(c, http.StatusInternalServerError, "MOVE_FAILED", "移动文件到目标目录失败: "+err.Error())
		return
	}

	Success(c, http.StatusOK, gin.H{
		"target_dir": targetRoot,
		"files":      saved,
		"count":      len(saved),
	})
}

// moveTree 把 src 下所有文件/目录移动到 dst（已存在）之下，保留相对结构。
// 同名文件覆盖（与 SaveUploadedFile 行为一致）；同名目录则合并进入。
// src 应为本次上传独占的 staging 目录，dst 可包含其他无关文件，本函数只动 src 中存在的路径。
func moveTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			// 目标目录可能已存在（其他 skill 或本次上传的子目录），MkdirAll 幂等
			return os.MkdirAll(target, 0o755)
		}
		// 文件：先确保父目录存在，再覆盖写入
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		// 目标文件可能已存在，先移除再 Rename（跨文件系统时 Rename 会失败，回退到写拷贝）
		_ = os.Remove(target)
		if err := os.Rename(p, target); err != nil {
			// 回退：读源文件写目标（跨文件系统场景）
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			if werr := os.WriteFile(target, data, 0o644); werr != nil {
				return werr
			}
			_ = os.Remove(p)
		}
		return nil
	})
}
