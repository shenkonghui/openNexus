// Command opennexus-gateway 是 MCP 聚合网关的独立二进制入口，
// 可脱离主 server 单独部署。配置来自 ~/.openNexus/gateway.yaml，
// 不依赖 sqlite、不依赖主程序 config.yaml，只读全局 mcp.json 作为上游来源。
//
// 用法：
//
//	# 生成 token
//	echo "token: $(openssl rand -hex 32)" > ~/.openNexus/gateway.yaml
//	echo "public_base_url: http://127.0.0.1:8090" >> ~/.openNexus/gateway.yaml
//
//	# 启动
//	opennexus-gateway
//	# 或指定配置文件
//	opennexus-gateway -config /path/to/gateway.yaml
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	gatewaymcp "opennexus/internal/mcp/gateway"
)

// ldflags 注入
var version = "dev"

func main() {
	configPath := flag.String("config", "", "gateway.yaml 路径（默认 ~/.openNexus/gateway.yaml）")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()

	if *showVersion {
		fmt.Printf("opennexus-gateway %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	cfg, err := gatewaymcp.LoadStandaloneConfig(*configPath)
	if err != nil {
		var bootErr *gatewaymcp.ConfigBootstrapError
		if errors.As(err, &bootErr) {
			log.Printf("✅ %v", err)
			log.Printf("   编辑配置文件后再次运行 opennexus-gateway 即可启动")
			return
		}
		log.Fatalf("加载配置失败: %v", err)
	}

	auth := gatewaymcp.NewStaticAuthenticator(cfg.Token)
	gw, err := gatewaymcp.New(cfg.MCPConfigPath, auth, "")
	if err != nil {
		log.Fatalf("创建网关失败: %v", err)
	}
	gw.SetPublicBaseURL(cfg.PublicBaseURL)
	defer gw.Close()

	if cfg.AutoEnable {
		if err := gw.EnableEntry(); err != nil {
			log.Printf("自动写入 mcp.json 条目失败（不影响网关服务）: %v", err)
		} else {
			log.Printf("已刷新 mcp.json 中的 %s 条目", gatewaymcp.GatewayMCPName)
		}
	} else {
		// 独立模式默认不写 mcp.json：mcp.json 可能由主程序管理，
		// 重复写入会与主程序的 SyncEntry 互相覆盖。
		gw.SyncEntry()
	}

	mux := http.NewServeMux()
	// MCP 聚合 endpoint（与主程序同路径，便于 agent 配置复用）
	mux.Handle(gatewaymcp.GatewayPath, gw.Handler())
	mux.Handle(gatewaymcp.GatewayPath+"/", gw.Handler())
	// 状态查询 endpoint：独立模式无 JWT，用同一 Bearer token 鉴权
	mux.HandleFunc("/api/v1/config/mcp/gateway", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uid, err := auth.Authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": gw.Status(ctx, uid),
		})
	})
	// 健康检查（无鉴权，供反代/容器探活）
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("opennexus-gateway %s 监听 %s（mcp.json=%s）", version, cfg.Listen, cfg.MCPConfigPath)
		log.Printf("MCP endpoint: %s%s", cfg.PublicBaseURL, gatewaymcp.GatewayPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("启动失败: %v", err)
		}
	}()

	// 优雅退出
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("收到退出信号，正在关闭……")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP server 关闭: %v", err)
	}
}
