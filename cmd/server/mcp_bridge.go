package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
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

	if *url == "" || *token == "" {
		log.Fatal("mcp-bridge 需要网关地址与 token（OPENNEXUS_GATEWAY_URL / OPENNEXUS_GATEWAY_TOKEN 或 -url / -token）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := gatewaymcp.RunStdioBridge(ctx, *url, *token); err != nil && ctx.Err() == nil {
		log.Fatalf("mcp-bridge 退出: %v", err)
	}
}
