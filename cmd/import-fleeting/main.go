package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"opennexus/internal/config"
	"opennexus/internal/database"
	"opennexus/internal/models"
	"opennexus/internal/repository"
)

var (
	noteHeaderRe = regexp.MustCompile(`(?m)^## (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2})\s*\n`)
	noteTagRe    = regexp.MustCompile(`#([^\s#]+)`)
	fileNameRe   = regexp.MustCompile(`^\d{4}-\d{2}\.md$`)
)

func main() {
	dir := flag.String("dir", "", "fleeting 笔记目录")
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	user := flag.String("user", "admin", "导入目标用户名")
	dryRun := flag.Bool("dry-run", false, "仅预览，不写入数据库")
	flag.Parse()
	if *dir == "" {
		log.Fatal("请指定 -dir 参数")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	db, err := database.Connect(cfg.Database.Path, cfg.Agents.Workspace.DefaultCwd)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}

	userRepo := repository.NewUserRepository(db)
	u, err := userRepo.FindByUsername(*user)
	if err != nil {
		log.Fatalf("查找用户 %q 失败: %v", *user, err)
	}

	notes, err := parseFleetingDir(*dir)
	if err != nil {
		log.Fatalf("解析笔记失败: %v", err)
	}
	existing, err := loadExistingContents(db, u.ID)
	if err != nil {
		log.Fatalf("读取已有笔记失败: %v", err)
	}

	var imported, skipped int
	for _, n := range notes {
		n.UserID = u.ID
		key := strings.TrimSpace(n.Content)
		if _, ok := existing[key]; ok {
			skipped++
			continue
		}
		if *dryRun {
			fmt.Printf("[dry-run] %s | %s\n", n.CreatedAt.Format("2006-01-02 15:04:05"), n.Title)
			imported++
			continue
		}
		if err := db.Create(&n.Note).Error; err != nil {
			log.Fatalf("写入笔记失败: %v", err)
		}
		existing[key] = struct{}{}
		imported++
	}
	fmt.Printf("完成：解析 %d 条，导入 %d 条，跳过重复 %d 条\n", len(notes), imported, skipped)
}

type parsedNote struct {
	models.Note
}

func parseFleetingDir(dir string) ([]parsedNote, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []parsedNote
	for _, e := range entries {
		if e.IsDir() || !fileNameRe.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, parseFleetingFile(string(data))...)
	}
	return out, nil
}

func parseFleetingFile(text string) []parsedNote {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	idx := noteHeaderRe.FindAllStringSubmatchIndex(text, -1)
	if len(idx) == 0 {
		return nil
	}
	var out []parsedNote
	for i, m := range idx {
		tsStr := text[m[2]:m[3]]
		start := m[1]
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		block := strings.TrimSpace(text[start:end])
		block = strings.TrimSuffix(block, "---**---")
		block = strings.TrimSpace(block)
		ts, err := time.ParseInLocation("2006-01-02 15:04:05", tsStr, time.Local)
		if err != nil {
			continue
		}
		title, tags := parseNoteMeta(block)
		out = append(out, parsedNote{Note: models.Note{
			Title:     title,
			Content:   block,
			Tags:      tagsToJSON(tags),
			CreatedAt: ts,
			UpdatedAt: ts,
		}})
	}
	return out
}

func parseNoteMeta(content string) (string, []string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return "无标题", nil
	}
	first := lines[0]
	var tags []string
	for _, m := range noteTagRe.FindAllStringSubmatch(first, -1) {
		if len(m) > 1 {
			tags = append(tags, m[1])
		}
	}
	titlePart := strings.TrimSpace(noteTagRe.ReplaceAllString(first, ""))
	if titlePart != "" && !strings.HasPrefix(titlePart, "## ") {
		return truncateTitle(titlePart), tags
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "## ") {
			continue
		}
		clean := strings.TrimSpace(noteTagRe.ReplaceAllString(line, ""))
		if clean != "" {
			return truncateTitle(clean), tags
		}
	}
	if strings.HasPrefix(titlePart, "## ") {
		return titlePart, tags
	}
	return "无标题", tags
}

func truncateTitle(s string) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= 80 {
		return s
	}
	runes := []rune(s)
	return string(runes[:80]) + "…"
}

func tagsToJSON(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func loadExistingContents(db *gorm.DB, userID uint) (map[string]struct{}, error) {
	var contents []string
	if err := db.Model(&models.Note{}).Where("user_id = ?", userID).Pluck("content", &contents).Error; err != nil {
		return nil, err
	}
	m := make(map[string]struct{}, len(contents))
	for _, c := range contents {
		m[strings.TrimSpace(c)] = struct{}{}
	}
	return m, nil
}
