package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"opennexus/internal/config"
)

// newFileSystemTestRouter 构造仅注册 UploadSkill 路由的测试引擎。
func newFileSystemTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewFileSystemHandler(config.SkillsConfig{}, config.CommandsConfig{}, config.RulesConfig{}, config.SubAgentsConfig{})
	v1 := r.Group("/api/v1")
	v1.POST("/filesystem/skills/upload", h.UploadSkill)
	return r
}

// buildSkillUploadRequest 构造一个 multipart/form-data 请求，模拟浏览器
// <input webkitdirectory> 选择目录后的上传：files 字段为文件部分，
// relative_paths 字段为对应的相对路径（来自 webkitRelativePath）。
func buildSkillUploadRequest(t *testing.T, targetPath string, files map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// relative_paths 与 files 按插入顺序一一对应
	var relPaths []string
	for rel, content := range files {
		fw, err := w.CreateFormFile("files", filepath.Base(rel))
		if err != nil {
			t.Fatalf("创建 form file 失败: %v", err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("写入文件内容失败: %v", err)
		}
		relPaths = append(relPaths, rel)
	}
	for _, rp := range relPaths {
		if err := w.WriteField("relative_paths", rp); err != nil {
			t.Fatalf("写入 relative_paths 失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 multipart writer 失败: %v", err)
	}
	q := url.Values{"path": {targetPath}}
	req := httptest.NewRequest("POST", "/api/v1/filesystem/skills/upload?"+q.Encode(), &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestUploadSkill_WritesDirectoryTree 验证上传含 SKILL.md 的目录树能正确
// 按相对路径写入项目 .agents/skills 下，且返回文件数量。
func TestUploadSkill_WritesDirectoryTree(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	files := map[string]string{
		"my-skill/SKILL.md":       "---\nname: my-skill\ndescription: 测试技能\n---\n正文\n",
		"my-skill/scripts/run.sh": "#!/bin/bash\necho hi\n",
	}
	req := buildSkillUploadRequest(t, cwd, files)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body=%s", w.Code, w.Body.String())
	}

	// 验证文件落盘
	skillMD := filepath.Join(cwd, ".agents", "skills", "my-skill", "SKILL.md")
	if data, err := os.ReadFile(skillMD); err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	} else if !bytes.Contains(data, []byte("name: my-skill")) {
		t.Errorf("SKILL.md 内容不正确: %s", data)
	}
	script := filepath.Join(cwd, ".agents", "skills", "my-skill", "scripts", "run.sh")
	if _, err := os.ReadFile(script); err != nil {
		t.Fatalf("scripts/run.sh 未落盘: %v", err)
	}
}

// TestUploadSkill_NoSkillMDRejected 验证上传内容不含 SKILL.md 时被拒绝，
// 且不会在目标目录留下任何文件（落盘走 staging，校验失败时 staging 被清理，
// targetRoot 从未被触碰）。
func TestUploadSkill_NoSkillMDRejected(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	files := map[string]string{
		"bad-skill/readme.md": "# no skill md here\n",
	}
	req := buildSkillUploadRequest(t, cwd, files)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400, body=%s", w.Code, w.Body.String())
	}
	// 目标目录不应被创建（校验失败时不移动 staging → targetRoot）
	targetDir := filepath.Join(cwd, ".agents", "skills")
	if entries, err := os.ReadDir(targetDir); err == nil && len(entries) > 0 {
		t.Errorf("无 SKILL.md 时目标目录不应有残留，仍有 %d 个条目", len(entries))
	}
}

// TestUploadSkill_NoSkillMDPreservesExisting 验证上传不含 SKILL.md 的内容时，
// targetRoot 中已有的其他 skill 不会被破坏。这是关键数据安全属性：
// 原实现在校验失败时 os.RemoveAll(targetRoot) 会误删整个项目 skills 目录，
// 现改为 staging 落盘 + 校验通过后才移动，杜绝误删。
func TestUploadSkill_NoSkillMDPreservesExisting(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	// 预置一个已存在的 skill
	existingDir := filepath.Join(cwd, ".agents", "skills", "existing-skill")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatalf("预置目录失败: %v", err)
	}
	existingSkill := filepath.Join(existingDir, "SKILL.md")
	existingContent := "---\nname: existing-skill\ndescription: 已存在\n---\n正文\n"
	if err := os.WriteFile(existingSkill, []byte(existingContent), 0o644); err != nil {
		t.Fatalf("预置 SKILL.md 失败: %v", err)
	}

	// 上传一个不含 SKILL.md 的目录
	files := map[string]string{
		"bad-skill/readme.md": "# no skill md here\n",
	}
	req := buildSkillUploadRequest(t, cwd, files)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400, body=%s", w.Code, w.Body.String())
	}

	// 已存在的 skill 必须完好无损
	data, err := os.ReadFile(existingSkill)
	if err != nil {
		t.Fatalf("已存在的 SKILL.md 被误删: %v", err)
	}
	if string(data) != existingContent {
		t.Errorf("已存在的 SKILL.md 内容被篡改:\n期望 %q\n实际 %q", existingContent, string(data))
	}
	// bad-skill 不应被写入 targetRoot
	badDir := filepath.Join(cwd, ".agents", "skills", "bad-skill")
	if _, err := os.Stat(badDir); err == nil {
		t.Error("校验失败的上传内容不应残留到目标目录")
	}
}

// TestUploadSkill_OverwritesExistingSkill 验证上传同名 skill 时覆盖旧文件，
// 同时不影响 targetRoot 中其他无关 skill。
func TestUploadSkill_OverwritesExistingSkill(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	// 预置一个已存在的 skill 和一个无关 skill
	existingDir := filepath.Join(cwd, ".agents", "skills", "my-skill")
	os.MkdirAll(existingDir, 0o755)
	os.WriteFile(filepath.Join(existingDir, "SKILL.md"), []byte("old content"), 0o644)
	os.WriteFile(filepath.Join(existingDir, "old-extra.txt"), []byte("old extra"), 0o644)

	otherDir := filepath.Join(cwd, ".agents", "skills", "other-skill")
	os.MkdirAll(otherDir, 0o755)
	os.WriteFile(filepath.Join(otherDir, "SKILL.md"), []byte("other"), 0o644)

	// 上传同名 skill（覆盖 SKILL.md，新增 new.txt，不包含 old-extra.txt）
	files := map[string]string{
		"my-skill/SKILL.md": "---\nname: my-skill\ndescription: 新版本\n---\nnew\n",
		"my-skill/new.txt":  "new file\n",
	}
	req := buildSkillUploadRequest(t, cwd, files)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body=%s", w.Code, w.Body.String())
	}

	// SKILL.md 应被覆盖为新内容
	data, err := os.ReadFile(filepath.Join(existingDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("SKILL.md 未落盘: %v", err)
	}
	if !bytes.Contains(data, []byte("新版本")) {
		t.Errorf("SKILL.md 未被覆盖: %s", data)
	}
	// new.txt 应存在
	if _, err := os.ReadFile(filepath.Join(existingDir, "new.txt")); err != nil {
		t.Errorf("new.txt 未落盘: %v", err)
	}
	// 无关 skill 不受影响
	otherData, err := os.ReadFile(filepath.Join(otherDir, "SKILL.md"))
	if err != nil || string(otherData) != "other" {
		t.Errorf("无关 skill 被篡改: data=%q err=%v", otherData, err)
	}
}

// TestUploadSkill_PathTraversalBlocked 验证相对路径包含 ../ 时被拒绝。
func TestUploadSkill_PathTraversalBlocked(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	files := map[string]string{
		"my-skill/SKILL.md": "---\nname: x\ndescription: y\n---\n",
		"../escape.txt":     "evil\n",
	}
	req := buildSkillUploadRequest(t, cwd, files)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400, body=%s", w.Code, w.Body.String())
	}
	// escape.txt 不应存在于 cwd 根目录
	if _, err := os.Stat(filepath.Join(cwd, "escape.txt")); err == nil {
		t.Error("路径穿越文件不应被写入")
	}
}

// TestUploadSkill_MissingPath 验证缺少 path 参数时返回 400。
func TestUploadSkill_MissingPath(t *testing.T) {
	r := newFileSystemTestRouter()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("files", "SKILL.md")
	fw.Write([]byte("---\nname: x\ndescription: y\n---\n"))
	w.WriteField("relative_paths", "x/SKILL.md")
	w.Close()
	req := httptest.NewRequest("POST", "/api/v1/filesystem/skills/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400", resp.Code)
	}
}

// TestUploadSkill_CustomTargetSubdir 验证可通过 target_subdir 指定目标子目录。
func TestUploadSkill_CustomTargetSubdir(t *testing.T) {
	r := newFileSystemTestRouter()
	cwd := t.TempDir()

	files := map[string]string{
		"custom/SKILL.md": "---\nname: custom\ndescription: 自定义目录\n---\n",
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	var relPaths []string
	for rel, content := range files {
		fw, _ := mw.CreateFormFile("files", filepath.Base(rel))
		fw.Write([]byte(content))
		relPaths = append(relPaths, rel)
	}
	for _, rp := range relPaths {
		mw.WriteField("relative_paths", rp)
	}
	mw.Close()
	q := url.Values{"path": {cwd}, "target_subdir": {".claude/skills"}}
	req := httptest.NewRequest("POST", "/api/v1/filesystem/skills/upload?"+q.Encode(), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body=%s", w.Code, w.Body.String())
	}
	skillMD := filepath.Join(cwd, ".claude", "skills", "custom", "SKILL.md")
	if _, err := os.ReadFile(skillMD); err != nil {
		t.Fatalf("SKILL.md 未落盘到自定义目录: %v", err)
	}
}
