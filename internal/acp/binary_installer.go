package acp

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const binaryDownloadRetries = 3

// versionsMutex 保护 versions.json 的并发读写（多个 agent 同时更新）。
var versionsMutex sync.Mutex

// binariesCacheDir 是二进制分发 agent 的下载缓存根目录。
// 按平台（os-arch，如 darwin-arm64 / linux-aarch64）分子目录：
// 容器与宿主机可能共享同一份 ~/.openNexus（HOME 对齐挂载），但二进制是平台特定的，
// 若不隔离，macOS 下载的 Mach-O 会被 Linux 容器复用导致 Exec format error。
// 平台 key 复用 registry 的 platformKey()，保证下载与缓存目录判定一致。
func binariesCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录: %w", err)
	}
	return filepath.Join(home, ".openNexus", "binaries", platformKey()), nil
}

// VersionRecord 记录单个 agent 当前激活的版本信息，持久化到 versions.json。
// 重启时读它恢复 symlink，使版本选择不依赖内存 BinaryRegistry（后者从内嵌 registry 重填会回退）。
type VersionRecord struct {
	Version    string `json:"version"`
	ArchiveURL string `json:"archive_url"`
	UpdatedAt  string `json:"updated_at"`
}

// versionsFile 返回 versions.json 的绝对路径。
func versionsFile() (string, error) {
	root, err := binariesCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "versions.json"), nil
}

// loadVersions 读取 versions.json。文件不存在或解析失败时返回空 map（不报错，降级兼容）。
func loadVersions() (map[string]VersionRecord, error) {
	path, err := versionsFile()
	if err != nil {
		return map[string]VersionRecord{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]VersionRecord{}, nil
		}
		return map[string]VersionRecord{}, err
	}
	var records map[string]VersionRecord
	if err := json.Unmarshal(data, &records); err != nil {
		slog.Warn("解析 versions.json 失败，降级为空", "err", err)
		return map[string]VersionRecord{}, nil
	}
	return records, nil
}

// saveVersions 原子写入 versions.json（临时文件 + rename，避免半成品）。
func saveVersions(records map[string]VersionRecord) error {
	versionsMutex.Lock()
	defer versionsMutex.Unlock()

	root, err := binariesCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("创建缓存根目录: %w", err)
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 versions.json: %w", err)
	}
	path := filepath.Join(root, "versions.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写临时文件: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename versions.json: %w", err)
	}
	return nil
}

// recordVersion 更新单个 agent 的版本记录并持久化。
func recordVersion(agentID, version, archiveURL string) error {
	records, err := loadVersions()
	if err != nil {
		records = map[string]VersionRecord{}
	}
	records[agentID] = VersionRecord{
		Version:    version,
		ArchiveURL: archiveURL,
		UpdatedAt:  time.Now().Format(time.RFC3339),
	}
	return saveVersions(records)
}

// resolveViaSymlink 通过稳定 symlink 路径解析 binary 完整路径。
// stableLink 是 <cacheRoot>/<agentID>（symlink 入口），cmd 是相对路径（如 "./dist-package/cursor-agent"）。
// symlink 不存在、悬空或 binary 缺失时返回 error，调用方据此决定是否重建。
func resolveViaSymlink(stableLink, cmd string) (string, error) {
	// ReadLink 检查是否是 symlink；非 symlink（普通目录）也允许（降级兼容旧缓存）
	cleanCmd := filepath.Clean(strings.TrimPrefix(cmd, "./"))
	binaryPath := filepath.Join(stableLink, cleanCmd)
	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}
	return "", fmt.Errorf("symlink 路径 %s 下找不到 %s", stableLink, cleanCmd)
}

// switchSymlink 原子地把 stableLink 指向 targetDir。
// Unix：先 symlink 到临时名再 rename 覆盖（原子）。
// Windows 或 rename 失败：降级为先 Remove 再 Symlink（非原子，短暂窗口期）。
// targetDir 必须是绝对路径或相对 cacheRoot 的路径。
func switchSymlink(stableLink, targetDir string) error {
	// 确保目标目录存在
	if _, err := os.Stat(targetDir); err != nil {
		return fmt.Errorf("目标版本目录不存在: %w", err)
	}
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("解析目标绝对路径: %w", err)
	}

	// 原子路径：symlink 到临时名，再 rename 覆盖
	tmpLink := stableLink + ".switching.tmp"
	_ = os.Remove(tmpLink) // 清理可能的残留
	if err := os.Symlink(absTarget, tmpLink); err != nil {
		// 降级：直接替换（Windows 可能需要管理员权限创建 symlink）
		_ = os.Remove(stableLink)
		if err := os.Symlink(absTarget, stableLink); err != nil {
			return fmt.Errorf("创建 symlink 失败（可能需要权限）: %w", err)
		}
		slog.Info("已切换 symlink（降级非原子）", "link", stableLink, "target", absTarget)
		return nil
	}
	if err := os.Rename(tmpLink, stableLink); err != nil {
		_ = os.Remove(tmpLink)
		// rename 失败（如 stableLink 是已存在的非空目录），降级为 Remove + Symlink
		_ = os.Remove(stableLink)
		if err := os.Symlink(absTarget, stableLink); err != nil {
			return fmt.Errorf("rename 与降级 symlink 均失败: %w", err)
		}
	}
	slog.Info("已切换 symlink（原子）", "link", stableLink, "target", absTarget)
	return nil
}

// ensureBinaryInstalled 确保指定 agent 的二进制已下载解压，返回稳定 symlink 路径下的完整 binary 路径。
// 设计：运行时永远走稳定路径 <cacheRoot>/<agentID>/（symlink），不依赖调用方传入的 version 是否"正确"。
//  1. symlink 有效（指向的版本目录里有 binary）→ 直接返回 symlink 路径
//  2. symlink 失效 → 按 version 下载到 <agentID>-<version>/，切换 symlink 指向它，记录到 versions.json
//
// 这样重启后即使内存 BinaryRegistry 回退到旧 version，symlink 仍指向 versions.json 记录的激活版本。
func ensureBinaryInstalled(agentID, version, archiveURL, cmd string) (string, error) {
	cacheRoot, err := binariesCacheDir()
	if err != nil {
		return "", err
	}
	stableLink := filepath.Join(cacheRoot, agentID) // symlink 稳定入口

	// 1. symlink 有效 → 直接用
	if path, err := resolveViaSymlink(stableLink, cmd); err == nil {
		slog.Info("binary 命中 symlink 缓存", "agent", agentID, "path", path)
		return path, nil
	}

	// 2. symlink 失效 → 按 version 目录找/下载
	versionDir := filepath.Join(cacheRoot, agentID+"-"+version)
	binaryPath, err := resolveBinaryPath(versionDir, cmd)
	if err != nil {
		// 版本目录没有 → 下载
		slog.Info("开始下载 binary", "agent", agentID, "version", version, "dir", versionDir, "url", archiveURL)
		if err := downloadAndExtract(archiveURL, versionDir); err != nil {
			_ = os.RemoveAll(versionDir)
			return "", fmt.Errorf("下载解压 binary: %w", err)
		}
		binaryPath, err = resolveBinaryPath(versionDir, cmd)
		if err != nil {
			_ = os.RemoveAll(versionDir)
			return "", err
		}
	} else {
		// 版本目录已存在且含 binary → 命中缓存，跳过下载
		slog.Info("binary 命中版本目录缓存，跳过下载", "agent", agentID, "version", version, "dir", versionDir, "path", binaryPath)
	}
	_ = os.Chmod(binaryPath, 0o755)

	// 3. 切换 symlink 指向新版本目录 + 记录版本
	if err := switchSymlink(stableLink, versionDir); err != nil {
		slog.Warn("切换 symlink 失败（仍返回版本目录路径）", "agent", agentID, "err", err)
		// 降级：直接返回版本目录路径（功能可用，只是重启后可能回退）
		return binaryPath, nil
	}
	if err := recordVersion(agentID, version, archiveURL); err != nil {
		slog.Warn("记录版本到 versions.json 失败（不影响运行）", "agent", agentID, "err", err)
	}

	// 返回 symlink 路径（稳定，重启后仍指向该版本）
	stablePath := filepath.Join(stableLink, filepath.Clean(strings.TrimPrefix(cmd, "./")))
	slog.Info("binary 下载完成并切换 symlink", "agent", agentID, "version", version, "path", stablePath)
	return stablePath, nil
}

// RemoveBinaryCache 删除指定 agent 的所有版本缓存目录（形如 <cacheRoot>/<agentID>-*）。
// 用于"更新"时强制 binary agent 在下次 Prepare() 时重新下载。
// 返回实际删除的目录数。缓存根目录不存在视为 0，不报错。
func RemoveBinaryCache(agentID string) (int, error) {
	cacheRoot, err := binariesCacheDir()
	if err != nil {
		return 0, err
	}
	pattern := filepath.Join(cacheRoot, agentID+"-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("匹配缓存目录: %w", err)
	}
	removed := 0
	for _, m := range matches {
		if err := os.RemoveAll(m); err != nil {
			slog.Warn("清理 binary 缓存失败", "dir", m, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.Info("已清理 binary 缓存", "agent", agentID, "removed", removed)
	}
	return removed, nil
}

// RemoveBinaryCacheExcept 删除指定 agent 除 keepVersion 外的所有版本缓存目录。
// 用于新版下载成功后清理旧版本（保留刚下载的新版）。
func RemoveBinaryCacheExcept(agentID, keepVersion string) (int, error) {
	cacheRoot, err := binariesCacheDir()
	if err != nil {
		return 0, err
	}
	keepDir := filepath.Join(cacheRoot, agentID+"-"+keepVersion)
	pattern := filepath.Join(cacheRoot, agentID+"-*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("匹配缓存目录: %w", err)
	}
	removed := 0
	for _, m := range matches {
		if m == keepDir || m == keepDir+".downloading" {
			continue
		}
		if err := os.RemoveAll(m); err != nil {
			slog.Warn("清理旧版本缓存失败", "dir", m, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.Info("已清理旧版本缓存", "agent", agentID, "keep", keepVersion, "removed", removed)
	}
	return removed, nil
}

// RestoreBinarySymlinks 启动时调用：读 versions.json，为每个 agent 重建 symlink 指向记录的激活版本。
// 这是重启不回退的关键——即使内存 BinaryRegistry 从内嵌 registry 重填了旧 version，
// symlink 仍指向用户上次通过"更新"选定的版本。
// 版本目录不存在（被手动删）→ 跳过该 agent，运行时 Prepare 会重新下载。
// 返回成功恢复的 agent 数。
func RestoreBinarySymlinks() (int, error) {
	records, err := loadVersions()
	if err != nil {
		return 0, fmt.Errorf("读取 versions.json: %w", err)
	}
	if len(records) == 0 {
		slog.Info("versions.json 为空或不存在，跳过 symlink 恢复")
		return 0, nil
	}
	cacheRoot, err := binariesCacheDir()
	if err != nil {
		return 0, err
	}
	restored := 0
	for agentID, rec := range records {
		stableLink := filepath.Join(cacheRoot, agentID)
		versionDir := filepath.Join(cacheRoot, agentID+"-"+rec.Version)
		// 版本目录必须存在，否则跳过（Prepare 会重下）
		if _, statErr := os.Stat(versionDir); statErr != nil {
			slog.Warn("跳过 symlink 恢复：版本目录不存在", "agent", agentID, "version", rec.Version)
			continue
		}
		// symlink 已正确指向 → 跳过
		if existing, _ := os.Readlink(stableLink); existing == versionDir || existing != "" {
			if abs, _ := filepath.Abs(versionDir); existing == abs {
				continue
			}
		}
		if err := switchSymlink(stableLink, versionDir); err != nil {
			slog.Warn("恢复 symlink 失败", "agent", agentID, "err", err)
			continue
		}
		restored++
	}
	slog.Info("symlink 恢复完成", "restored", restored, "total", len(records))
	return restored, nil
}

// EnsureBinaryDownloaded 预下载并验证新版本 binary 可用，用于"更新"前的安全检查。
// 与 ensureBinaryInstalled 的区别：此函数强制重新下载（无视缓存），且失败时清理半成品目录，
// 避免留下空目录导致后续 Prepare() 无限重试下载失败的版本。
// 成功返回 nil（新版本已就位）；失败返回 error（旧缓存未受影响，调用方可保留旧版继续工作）。
func EnsureBinaryDownloaded(agentID, version, archiveURL, cmd string) error {
	cacheRoot, err := binariesCacheDir()
	if err != nil {
		return err
	}
	// 下载到临时目录，验证成功后才替换目标，避免半成品污染
	tmpDir := filepath.Join(cacheRoot, agentID+"-"+version+".downloading")
	targetDir := filepath.Join(cacheRoot, agentID+"-"+version)
	// 清理可能残留的临时目录
	_ = os.RemoveAll(tmpDir)

	slog.Info("预下载验证 binary", "agent", agentID, "version", version, "url", archiveURL)
	if err := downloadAndExtract(archiveURL, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir) // 清理半成品
		return fmt.Errorf("下载新版本 %s 失败: %w", version, err)
	}
	// 验证下载产物包含目标 binary
	if _, err := resolveBinaryPath(tmpDir, cmd); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("新版本 %s 产物缺少 binary %s: %w", version, cmd, err)
	}
	// 验证通过：替换目标目录（先删旧的，再重命名）
	_ = os.RemoveAll(targetDir)
	if err := os.Rename(tmpDir, targetDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return fmt.Errorf("替换缓存目录失败: %w", err)
	}
	// 切换 symlink 指向新版本 + 记录到 versions.json（使重启后仍用此版本）
	stableLink := filepath.Join(cacheRoot, agentID)
	if err := switchSymlink(stableLink, targetDir); err != nil {
		slog.Warn("切换 symlink 失败（新版本已就位，下次 Prepare 会重新指向）", "agent", agentID, "err", err)
	}
	if err := recordVersion(agentID, version, archiveURL); err != nil {
		slog.Warn("记录版本失败（不影响本次切换）", "agent", agentID, "err", err)
	}
	slog.Info("预下载验证通过并已激活", "agent", agentID, "version", version)
	return nil
}

// downloadAndExtract 下载 archiveURL 并解压到 destDir。
func downloadAndExtract(archiveURL, destDir string) error {
	// 创建目标目录
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("创建缓存目录: %w", err)
	}

	// 下载
	tmpFile, err := downloadArchive(archiveURL)
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	// 根据后缀解压
	lower := strings.ToLower(archiveURL)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(tmpFile, destDir)
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return extractTarBz2(tmpFile, destDir)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(tmpFile, destDir)
	case strings.HasSuffix(lower, ".tar"):
		return extractTar(tmpFile, destDir)
	default:
		return fmt.Errorf("不支持的压缩格式: %s", archiveURL)
	}
}

// downloadArchive 下载文件到临时文件，返回临时文件路径。
func downloadArchive(url string) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
		},
	}

	var lastErr error
	for attempt := 1; attempt <= binaryDownloadRetries; attempt++ {
		if attempt > 1 {
			wait := time.Duration(attempt) * 2 * time.Second
			slog.Info("重试下载 binary", "url", url, "attempt", attempt, "wait", wait)
			time.Sleep(wait)
		}
		tmpFile, err := downloadArchiveOnce(client, url)
		if err == nil {
			return tmpFile, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("下载 %s 失败（已重试 %d 次）: %w", url, binaryDownloadRetries, lastErr)
}

func downloadArchiveOnce(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("请求 %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 %s 返回状态码 %d", url, resp.StatusCode)
	}

	f, err := os.CreateTemp("", "opennexus-binary-*")
	if err != nil {
		return "", fmt.Errorf("创建临时文件: %w", err)
	}

	// 包装响应体，每 5 秒打印一次下载进度（已下载字节 / 总大小 / 百分比）
	pr := newProgressReader(resp.Body, resp.ContentLength, url)
	defer pr.Stop()

	if _, err := io.Copy(f, pr); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", fmt.Errorf("下载写入: %w", err)
	}
	f.Close()
	pr.logDone()
	return f.Name(), nil
}

// progressReader 包装底层 io.Reader，累计已读字节，并由后台 ticker 每 5 秒打印一次进度。
type progressReader struct {
	r     io.Reader
	url   string
	total int64        // Content-Length，<=0 表示未知
	read  atomic.Int64 // 已读字节数
	stop  chan struct{}
	once  sync.Once
}

func newProgressReader(r io.Reader, total int64, url string) *progressReader {
	pr := &progressReader{
		r:     r,
		url:   url,
		total: total,
		stop:  make(chan struct{}),
	}
	go pr.loop()
	return pr
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read.Add(int64(n))
	}
	return n, err
}

func (p *progressReader) loop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.log()
		}
	}
}

func (p *progressReader) log() {
	read := p.read.Load()
	if p.total > 0 {
		percent := float64(read) / float64(p.total) * 100
		slog.Info("下载进度", "url", p.url,
			"downloaded", formatBytes(read), "total", formatBytes(p.total),
			"percent", fmt.Sprintf("%.1f%%", percent))
	} else {
		slog.Info("下载进度", "url", p.url, "downloaded", formatBytes(read), "total", "未知")
	}
}

// logDone 打印下载完成的最终进度。
func (p *progressReader) logDone() {
	read := p.read.Load()
	slog.Info("下载完成", "url", p.url, "downloaded", formatBytes(read))
}

func (p *progressReader) Stop() {
	p.once.Do(func() { close(p.stop) })
}

// formatBytes 将字节数格式化为易读单位（B/KB/MB/GB）。
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// resolveBinaryPath 在缓存目录中定位二进制文件，支持压缩包内嵌子目录。
func resolveBinaryPath(cacheDir, cmd string) (string, error) {
	binaryPath := filepath.Join(cacheDir, filepath.Clean(strings.TrimPrefix(cmd, "./")))
	if _, err := os.Stat(binaryPath); err == nil {
		return binaryPath, nil
	}

	base := filepath.Base(filepath.Clean(strings.TrimPrefix(cmd, "./")))
	var found string
	_ = filepath.WalkDir(cacheDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == base {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", fmt.Errorf("解压后未找到二进制文件: %s", binaryPath)
	}
	return found, nil
}

// extractTarGz 解压 .tar.gz 到 destDir。
func extractTarGz(filePath, destDir string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip 解压: %w", err)
	}
	defer gz.Close()

	return untar(gz, destDir)
}

// extractTarBz2 解压 .tar.bz2 到 destDir。
func extractTarBz2(filePath, destDir string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	return untar(bzip2.NewReader(f), destDir)
}

// extractTar 解压 .tar 到 destDir。
func extractTar(filePath, destDir string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	return untar(f, destDir)
}

// untar 解压 tar 到 destDir。
func untar(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 条目: %w", err)
		}

		target := filepath.Join(destDir, filepath.Clean(header.Name))

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			_ = os.Symlink(header.Linkname, target)
		}
	}
	return nil
}

// extractZip 解压 .zip 到 destDir。
func extractZip(filePath, destDir string) error {
	r, err := zip.OpenReader(filePath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		target := filepath.Join(destDir, filepath.Clean(f.Name))

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		src, err := f.Open()
		if err != nil {
			return err
		}

		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			src.Close()
			return err
		}

		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
