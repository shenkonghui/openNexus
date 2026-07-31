package acp

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// TestStartAndHandshake_ReusedStuckAgent_FastFail 验证复用常驻 bridge 时，
// 若 agent 卡死（不响应重复 initialize），握手在 reusedHandshakeTimeout 附近
// 快速失败并返回 reused=true（触发调用方的 forceNew 销毁重建），
// 而非干等调用方 ctx（分钟级）到期。
func TestStartAndHandshake_ReusedStuckAgent_FastFail(t *testing.T) {
	if testing.Short() {
		t.Skip("需等待 reusedHandshakeTimeout（10s），short 模式跳过")
	}

	svc := newTestService(t)
	// socketDir 用系统临时目录下的短路径，避免 UDS 路径超长（macOS 约 104 字节上限）
	socketDir, err := os.MkdirTemp("", "acphs")
	if err != nil {
		t.Fatalf("创建 socket 目录: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	svc.SetBridgeMode(true, socketDir)

	backend := NewMockBackend()
	cwd := t.TempDir()
	socketPath := svc.bridgeSocketPath(backend.Name(), cwd)

	// 模拟卡死的常驻 bridge：监听 UDS 并接受连接，读走请求但永不回复
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("监听 UDS: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()

	// 调用方 ctx 远大于 reusedHandshakeTimeout，用于验证快速失败不依赖调用方超时
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	start := time.Now()
	_, _, reused, err := svc.startAndHandshake(ctx, backend, backend.Name(), cwd, false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望复用卡死 agent 时握手返回错误")
	}
	if !reused {
		t.Errorf("期望 reused=true（拨号复用了已存在的 socket），实际 false")
	}
	// 应在 reusedHandshakeTimeout 附近失败：给调度余量，上限取 2 倍
	if elapsed < reusedHandshakeTimeout-time.Second {
		t.Errorf("失败过早（%v），期望约 %v（应等满短超时而非立即失败）", elapsed, reusedHandshakeTimeout)
	}
	if elapsed > 2*reusedHandshakeTimeout {
		t.Errorf("失败过晚（%v），期望约 %v（不应等到调用方 ctx 到期）", elapsed, reusedHandshakeTimeout)
	}
}
