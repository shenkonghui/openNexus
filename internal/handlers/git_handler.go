package handlers

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	acplocal "opennexus/internal/acp"
)

// GitHandler 提供 git 只读查询能力（Git 管理面板：worktree 提交记录与文件 diff）。
// 全部为只读操作（log/status/show），不提供 commit/checkout 等写操作。
type GitHandler struct{}

// NewGitHandler 创建 GitHandler。
func NewGitHandler() *GitHandler {
	return &GitHandler{}
}

// 单侧文件内容大小上限：超过则不返回文本（前端提示文件过大）。
const gitDiffMaxBytes = 2 << 20 // 2MB

// gitCommit 是提交记录 API 返回的单条提交。
type gitCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"short_hash"`
	Author    string `json:"author"`
	Date      int64  `json:"date"` // unix 秒
	Subject   string `json:"subject"`
}

// gitFileEntry 是单个提交（或未提交改动）中的文件变更项。
type gitFileEntry struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // A/M/D/R/C/?（? 表示未跟踪）
	Added   int    `json:"added"`  // 增行数；二进制为 -1
	Removed int    `json:"removed"`
}

// runGitOut 在 dir 下执行 git 命令并返回 stdout；失败时返回 stderr 摘要。
func runGitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", &gitError{msg: msg}
	}
	return stdout.String(), nil
}

type gitError struct{ msg string }

func (e *gitError) Error() string { return e.msg }

// resolveGitRepo 校验 path 查询参数并解析所在仓库根目录。失败时已写入错误响应。
func resolveGitRepo(c *gin.Context) (string, bool) {
	reqPath := strings.TrimSpace(c.Query("path"))
	if reqPath == "" {
		Fail(c, http.StatusBadRequest, "MISSING_PATH", "缺少 path 参数")
		return "", false
	}
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "INVALID_PATH", "路径无效")
		return "", false
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.IsDir() {
		Fail(c, http.StatusNotFound, "PATH_NOT_FOUND", "目录不存在")
		return "", false
	}
	root, err := acplocal.GitRoot(absPath)
	if err != nil {
		Fail(c, http.StatusBadRequest, "NOT_A_GIT_REPO", "当前目录不是 git 仓库")
		return "", false
	}
	return root, true
}

// safeRepoFile 校验 file 参数为仓库内相对路径（拒绝绝对路径与 .. 越界）。
func safeRepoFile(c *gin.Context) (string, bool) {
	file := strings.TrimSpace(c.Query("file"))
	if file == "" {
		Fail(c, http.StatusBadRequest, "MISSING_FILE", "缺少 file 参数")
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(file))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		Fail(c, http.StatusBadRequest, "INVALID_FILE", "file 必须是仓库内相对路径")
		return "", false
	}
	return clean, true
}

// Log GET /api/v1/git/log?path=<worktree 内路径>&limit=100
// 返回该 worktree 当前分支的提交记录与分支名。空仓库（无 HEAD）返回空列表。
func (h *GitHandler) Log(c *gin.Context) {
	root, ok := resolveGitRepo(c)
	if !ok {
		return
	}
	limit := 100
	if v, err := strconv.Atoi(c.Query("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	branch := ""
	if out, err := runGitOut(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		branch = strings.TrimSpace(out)
	}
	commits := []gitCommit{}
	// %x1f 单元分隔符切字段，%x1e 记录分隔符切提交（subject 内不会出现控制字符）
	out, err := runGitOut(root, "log", "--format=%H%x1f%h%x1f%an%x1f%at%x1f%s%x1e", "-n", strconv.Itoa(limit))
	if err != nil {
		// 空仓库无 HEAD：返回空列表而非报错
		Success(c, http.StatusOK, gin.H{"branch": branch, "commits": commits})
		return
	}
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 5 {
			continue
		}
		ts, _ := strconv.ParseInt(f[3], 10, 64)
		commits = append(commits, gitCommit{Hash: f[0], ShortHash: f[1], Author: f[2], Date: ts, Subject: f[4]})
	}
	Success(c, http.StatusOK, gin.H{"branch": branch, "commits": commits})
}

// parseNameStatus 解析 name-status 输出（-z 分隔）：状态与路径交替，R/C 带两个路径。
func parseNameStatus(out string) []gitFileEntry {
	fields := strings.Split(out, "\x00")
	entries := []gitFileEntry{}
	for i := 0; i < len(fields); i++ {
		status := strings.TrimSpace(fields[i])
		if status == "" {
			continue
		}
		code := status[:1]
		if (code == "R" || code == "C") && i+2 < len(fields) {
			// rename/copy：old \x00 new，展示新路径
			entries = append(entries, gitFileEntry{Path: fields[i+2], Status: code})
			i += 2
		} else if i+1 < len(fields) {
			entries = append(entries, gitFileEntry{Path: fields[i+1], Status: code})
			i++
		}
	}
	return entries
}

// applyNumstat 把 numstat 输出（-z 分隔）的增减行数合并进 entries（按路径匹配）。
// 二进制文件 numstat 为 "-"，记为 -1。
func applyNumstat(out string, entries []gitFileEntry) {
	byPath := make(map[string]*gitFileEntry, len(entries))
	for i := range entries {
		byPath[entries[i].Path] = &entries[i]
	}
	// numstat -z 格式：added\tremoved\tpath\x00；rename 时为 added\tremoved\t\x00old\x00new\x00
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) {
			// rename：路径在后两个字段（old, new）
			path = fields[i+2]
			i += 2
		}
		e, ok := byPath[path]
		if !ok {
			continue
		}
		if parts[0] == "-" || parts[1] == "-" {
			e.Added, e.Removed = -1, -1
			continue
		}
		e.Added, _ = strconv.Atoi(parts[0])
		e.Removed, _ = strconv.Atoi(parts[1])
	}
}

// CommitFiles GET /api/v1/git/commit-files?path=&hash=
// 返回单个提交的文件变更列表（状态 + 增减行数），rename 检测开启。
func (h *GitHandler) CommitFiles(c *gin.Context) {
	root, ok := resolveGitRepo(c)
	if !ok {
		return
	}
	hash := strings.TrimSpace(c.Query("hash"))
	if hash == "" || !isGitHash(hash) {
		Fail(c, http.StatusBadRequest, "INVALID_HASH", "hash 参数无效")
		return
	}
	nameStatus, err := runGitOut(root, "show", "--format=", "--name-status", "-M", "-z", hash)
	if err != nil {
		Fail(c, http.StatusBadRequest, "GIT_SHOW_FAILED", err.Error())
		return
	}
	entries := parseNameStatus(nameStatus)
	if numstat, err := runGitOut(root, "show", "--format=", "--numstat", "-M", "-z", hash); err == nil {
		applyNumstat(numstat, entries)
	}
	Success(c, http.StatusOK, gin.H{"files": entries})
}

// Status GET /api/v1/git/status?path=
// 返回 worktree 未提交改动（含未跟踪文件），增减行数来自 git diff HEAD --numstat。
func (h *GitHandler) Status(c *gin.Context) {
	root, ok := resolveGitRepo(c)
	if !ok {
		return
	}
	out, err := runGitOut(root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		Fail(c, http.StatusBadRequest, "GIT_STATUS_FAILED", err.Error())
		return
	}
	entries := []gitFileEntry{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if len(rec) < 4 {
			continue
		}
		xy := rec[:2]
		path := rec[3:]
		status := "M"
		switch {
		case xy == "??":
			status = "?"
		case strings.Contains(xy, "D"):
			status = "D"
		case strings.Contains(xy, "A"):
			status = "A"
		case strings.Contains(xy, "R"):
			status = "R"
			// rename：下一个字段是 old path，跳过
			i++
		}
		entries = append(entries, gitFileEntry{Path: path, Status: status})
	}
	if numstat, err := runGitOut(root, "diff", "HEAD", "--numstat", "-z"); err == nil {
		applyNumstat(numstat, entries)
	}
	Success(c, http.StatusOK, gin.H{"files": entries})
}

// isGitHash 报告 s 是否形如合法的 git 提交引用（十六进制，防注入 git 参数）。
func isGitHash(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// showFileAt 返回 <ref>:<file> 的文本内容。二进制返回 isBinary=true；不存在返回 exists=false。
func showFileAt(root, ref, file string) (text string, exists, isBinary, tooLarge bool) {
	out, err := runGitOut(root, "show", ref+":"+file)
	if err != nil {
		return "", false, false, false
	}
	if len(out) > gitDiffMaxBytes {
		return "", true, false, true
	}
	if strings.ContainsRune(out, '\x00') {
		return "", true, true, false
	}
	return out, true, false, false
}

// Diff GET /api/v1/git/diff?path=&file=&hash=
// 返回单文件某提交前后（或未提交改动 vs HEAD）的 old/new 文本，供前端行级 diff 渲染。
// hash 为空表示未提交改动：old=HEAD 版本，new=工作区磁盘内容。
func (h *GitHandler) Diff(c *gin.Context) {
	root, ok := resolveGitRepo(c)
	if !ok {
		return
	}
	file, ok := safeRepoFile(c)
	if !ok {
		return
	}
	hash := strings.TrimSpace(c.Query("hash"))
	var oldText, newText string
	var oldExists, newExists bool
	var binary, tooLarge bool

	if hash != "" {
		if !isGitHash(hash) {
			Fail(c, http.StatusBadRequest, "INVALID_HASH", "hash 参数无效")
			return
		}
		var ob, nb, ol, nl bool
		oldText, oldExists, ob, ol = showFileAt(root, hash+"^", file)
		newText, newExists, nb, nl = showFileAt(root, hash, file)
		binary, tooLarge = ob || nb, ol || nl
	} else {
		var ob, ol bool
		oldText, oldExists, ob, ol = showFileAt(root, "HEAD", file)
		abs := filepath.Join(root, filepath.FromSlash(file))
		if info, err := os.Stat(abs); err == nil && !info.IsDir() {
			if info.Size() > gitDiffMaxBytes {
				newExists, tooLarge = true, true
			} else if data, err := os.ReadFile(abs); err == nil {
				newExists = true
				if bytes.ContainsRune(data, '\x00') {
					binary = true
				} else {
					newText = string(data)
				}
			}
		}
		binary = binary || ob
		tooLarge = tooLarge || ol
	}

	Success(c, http.StatusOK, gin.H{
		// old_text 为 null 表示新文件；new_exists=false 表示文件被删除
		"old_text":   nullable(oldText, oldExists),
		"new_text":   newText,
		"new_exists": newExists,
		"is_binary":  binary,
		"too_large":  tooLarge,
	})
}

// nullable 在 exists=false 时返回 nil（JSON null），否则返回文本。
func nullable(s string, exists bool) any {
	if !exists {
		return nil
	}
	return s
}
