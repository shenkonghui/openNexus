package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	gatewaymcp "opennexus/internal/mcp/gateway"
)

// runMCPBridge 以 stdio MCP server 形态运行：把主程序聚合网关的 Streamable HTTP
// endpoint 桥接给不支持 http mcpCapabilities 的 ACP agent（如 devin）。
// 由主 server 在 session/new 注入的 stdio 条目拉起（`opennexus mcp-bridge`），
// endpoint/token 经环境变量传递（避免 token 暴露在进程参数里），也可用 flag 覆盖调试。
func runMCPBridge() {
	fs := flag.NewFlagSet("mcp-bridge", flag.ExitOnError)
	url := fs.String("url", os.Getenv("OPENNEXUS_GATEWAY_URL"), "聚合网关 endpoint（默认取 OPENNEXUS_GATEWAY_URL）")
	token := fs.String("token", os.Getenv("OPENNEXUS_GATEWAY_TOKEN"), "网关 Bearer token（默认取 OPENNEXUS_GATEWAY_TOKEN）")
	_ = fs.Parse(os.Args[2:])

	// stdout 已被 MCP stdio 传输占用，所有日志必须走 stderr。
	log.SetOutput(os.Stderr)

	// 探针日志：agent 侧对桥的拉起/工具同步行为对主程序不可见（stderr 被 agent
	// 吞掉），落盘文件可留下每次桥生命周期的确凿记录。默认写系统临时目录，
	// 不依赖 env 透传——部分 agent 会丢弃 stdio 条目的 env 字段，此时也能留痕。
	logPath := os.Getenv("OPENNEXUS_BRIDGE_LOG")
	if logPath == "" {
		logPath = filepath.Join(os.TempDir(), "opennexus-mcp-bridge.log")
	}
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
		slog.SetDefault(slog.New(slog.NewTextHandler(f, nil)))
	}
	// 先记录进程被拉起本身（含 env 是否就绪），再做参数校验：
	// env 缺失导致的立即退出也要留痕，否则与“从未被拉起”无法区分。
	slog.Info("mcp-bridge 进程被拉起", "pid", os.Getpid(), "ppid", os.Getppid(), "has_url", *url != "", "has_token", *token != "")

	if *url == "" || *token == "" {
		log.Fatal("mcp-bridge 需要网关地址与 token（OPENNEXUS_GATEWAY_URL / OPENNEXUS_GATEWAY_TOKEN 或 -url / -token）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := gatewaymcp.RunStdioBridge(ctx, *url, *token); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp-bridge 退出: %v", err)
	}
}
