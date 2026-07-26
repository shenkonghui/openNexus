package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"opennexus/internal/acp"
	"opennexus/internal/repository"
)

// watchdog 默认参数。
//   - interval: 扫描心跳表的周期
//   - serverStale: 主 server 心跳多久未更新即判定主程序已死
const (
	defaultWatchdogInterval = 60 * time.Second
	defaultServerStale      = 90 * time.Second
)

// runWatchdog 是独立 watchdog 进程的入口。
//
// 职责：
//  周期性检测主 server 心跳：若全部行的 server_heartbeat 超过 serverStale 未更新，
//  判定主程序已死，杀掉表中记录的全部 acp 进程，然后自身退出。
//
// 注意：watchdog 只在主程序失联时才会干掉 acp 进程，主程序存活期间不会主动回收任何连接。
//
// 与主 server 解耦：watchdog 是独立 OS 进程（主 server 用 Setsid 拉起），
// 主 server 崩溃后 watchdog 仍在运行，由它完成最后的清理。
func runWatchdog() {
	fs := flag.NewFlagSet("watchdog", flag.ExitOnError)
	dbPath := fs.String("db", "", "SQLite 数据库路径（必填，与主 server 共享）")
	interval := fs.Duration("interval", defaultWatchdogInterval, "扫描周期")
	serverStale := fs.Duration("server-stale", defaultServerStale, "主 server 心跳失效阈值")
	_ = fs.Parse(os.Args[2:])

	if *dbPath == "" {
		log.Fatal("watchdog: 缺少 --db 参数")
	}
	log.SetPrefix("[watchdog] ")
	log.Printf("启动：db=%s interval=%s serverStale=%s", *dbPath, *interval, *serverStale)

	// 以只读 GORM 连接同一 SQLite（不开 AutoMigrate，避免与主 server 迁移竞争）。
	// silent logger：watchdog 频繁查询不应污染日志。
	db, err := gorm.Open(sqlite.Open(*dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_txlock=immediate&_foreign_keys=on"),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		log.Fatalf("watchdog: 打开数据库失败: %v", err)
	}
	repo := repository.NewACPConnectionRepository(db)

	// 接收 SIGINT/SIGTERM 优雅退出
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			log.Println("watchdog: 收到退出信号，结束")
			return
		case <-ticker.C:
			if exited := watchdogTick(repo, *serverStale); exited {
				log.Println("watchdog: 主程序已失联且已清理全部 acp 进程，退出")
				return
			}
		}
	}
}

// watchdogTick 执行一次扫描，返回 true 表示主程序已死、watchdog 应退出。
//
// 监听程序只有在检测到主程序不在时才会干掉 acp 进程；主程序存活期间不做任何回收。
func watchdogTick(repo *repository.ACPConnectionRepository, serverStale time.Duration) bool {
	now := time.Now()

	// 主程序存活检查：取最近一次 server 心跳
	hb, hasRow, err := repo.MaxHeartbeat()
	if err != nil {
		log.Printf("watchdog: 查询心跳失败: %v", err)
		return false
	}
	// 表空：无连接在管，无需操作
	if !hasRow {
		return false
	}
	// 主程序心跳超时 → 判定主程序已死
	if now.Sub(hb) > serverStale {
		log.Printf("watchdog: 主 server 心跳过期（最近 %s 前），清理全部 acp 进程", hb.Format(time.RFC3339))
		rows, err := repo.FindAll()
		if err != nil {
			log.Printf("watchdog: 查询全部连接失败: %v", err)
			return true
		}
		for _, row := range rows {
			if row.Pid > 0 {
				log.Printf("watchdog: 清理 acp 进程组 pid=%d agent=%s", row.Pid, row.AgentType)
				if err := acp.KillProcessGroup(row.Pid); err != nil {
					log.Printf("watchdog: 杀进程组 pid=%d 失败: %v", row.Pid, err)
				}
			}
		}
		// 兜底：扫杀可能未被记录的孤儿（上次崩溃残留）
		if n, err := acp.KillOrphanACPProcesses(); err != nil {
			log.Printf("watchdog: 扫杀孤儿失败: %v", err)
		} else if n > 0 {
			log.Printf("watchdog: 扫杀孤儿 %d 个", n)
		}
		return true
	}
	return false
}
