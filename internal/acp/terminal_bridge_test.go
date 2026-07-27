package acp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
)

// waitEvent 从事件 channel 中等待下一个指定类型的事件（跳过其他类型），超时报错。
func waitEvent(t *testing.T, ch <-chan TerminalEvent, evType string) TerminalEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("等待 %s 事件时 channel 已关闭", evType)
			}
			if ev.Type == evType {
				return ev
			}
		case <-deadline:
			t.Fatalf("等待 %s 事件超时", evType)
		}
	}
}

// newTestBridge 创建把所有 ACP session 都映射到 dbID=1 的 bridge。
func newTestBridge() *TerminalBridge {
	return NewTerminalBridge(func(acp.SessionId) uint { return 1 })
}

func TestTerminalBridgeCreateOutputWait(t *testing.T) {
	b := newTestBridge()
	_, events, cancel := b.Subscribe(1)
	defer cancel()

	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId: "sess-1",
		Command:   "echo hello-bridge",
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if !strings.HasPrefix(resp.TerminalId, "term-") {
		t.Fatalf("terminalId 格式错误: %q", resp.TerminalId)
	}

	created := waitEvent(t, events, TerminalEventCreated)
	if created.TerminalID != resp.TerminalId || created.Command != "echo hello-bridge" {
		t.Fatalf("created 事件不符: %+v", created)
	}

	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	exit, err := b.WaitForExit(ctx, acp.WaitForTerminalExitRequest{SessionId: "sess-1", TerminalId: resp.TerminalId})
	if err != nil {
		t.Fatalf("WaitForExit 失败: %v", err)
	}
	if exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("期望退出码 0，实际: %+v", exit)
	}

	out, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId})
	if err != nil {
		t.Fatalf("Output 失败: %v", err)
	}
	if !strings.Contains(out.Output, "hello-bridge") {
		t.Fatalf("输出中未包含命令结果: %q", out.Output)
	}
	if out.ExitStatus == nil || out.ExitStatus.ExitCode == nil || *out.ExitStatus.ExitCode != 0 {
		t.Fatalf("Output 退出状态不符: %+v", out.ExitStatus)
	}

	exitEv := waitEvent(t, events, TerminalEventExit)
	if exitEv.TerminalID != resp.TerminalId || exitEv.ExitCode == nil || *exitEv.ExitCode != 0 {
		t.Fatalf("exit 事件不符: %+v", exitEv)
	}
}

func TestTerminalBridgeShellFallback(t *testing.T) {
	// command 含 shell 特殊字符且无 args 时应经 shell 解释执行
	b := newTestBridge()
	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId: "sess-1",
		Command:   "echo a && echo b",
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	if _, err := b.WaitForExit(ctx, acp.WaitForTerminalExitRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("WaitForExit 失败: %v", err)
	}
	out, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId})
	if err != nil {
		t.Fatalf("Output 失败: %v", err)
	}
	if !strings.Contains(out.Output, "a") || !strings.Contains(out.Output, "b") {
		t.Fatalf("shell 回退执行输出不符: %q", out.Output)
	}
}

func TestTerminalBridgeOutputByteLimit(t *testing.T) {
	b := newTestBridge()
	limit := 64
	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId:       "sess-1",
		Command:         "seq 1 200",
		OutputByteLimit: &limit,
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	if _, err := b.WaitForExit(ctx, acp.WaitForTerminalExitRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("WaitForExit 失败: %v", err)
	}
	out, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId})
	if err != nil {
		t.Fatalf("Output 失败: %v", err)
	}
	if len(out.Output) > limit {
		t.Fatalf("输出超过 limit：len=%d limit=%d", len(out.Output), limit)
	}
	if !out.Truncated {
		t.Fatal("期望 Truncated=true")
	}
	// 从头部截断：应保留末尾的 200
	if !strings.Contains(out.Output, "200") {
		t.Fatalf("截断后应保留末尾输出: %q", out.Output)
	}
}

func TestTerminalBridgeKillAndRelease(t *testing.T) {
	b := newTestBridge()
	_, events, cancel := b.Subscribe(1)
	defer cancel()

	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId: "sess-1",
		Command:   "sleep",
		Args:      []string{"30"},
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	waitEvent(t, events, TerminalEventCreated)

	if _, err := b.Kill(acp.KillTerminalRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("Kill 失败: %v", err)
	}
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	exit, err := b.WaitForExit(ctx, acp.WaitForTerminalExitRequest{SessionId: "sess-1", TerminalId: resp.TerminalId})
	if err != nil {
		t.Fatalf("WaitForExit 失败: %v", err)
	}
	if exit.Signal == nil && exit.ExitCode == nil {
		t.Fatalf("kill 后应有退出状态: %+v", exit)
	}
	// kill 后输出缓冲仍可查询
	if _, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("kill 后 Output 应可用: %v", err)
	}

	if _, err := b.Release(acp.ReleaseTerminalRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("Release 失败: %v", err)
	}
	relEv := waitEvent(t, events, TerminalEventReleased)
	if relEv.TerminalID != resp.TerminalId {
		t.Fatalf("released 事件不符: %+v", relEv)
	}
	// release 后终端不可再查询
	if _, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err == nil {
		t.Fatal("release 后 Output 应报错")
	}
	// release 幂等
	if _, err := b.Release(acp.ReleaseTerminalRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("重复 Release 应幂等: %v", err)
	}
}

func TestTerminalBridgeSubscribeSnapshot(t *testing.T) {
	b := newTestBridge()
	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId: "sess-1",
		Command:   "echo snapshot-data",
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	ctx, ctxCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctxCancel()
	if _, err := b.WaitForExit(ctx, acp.WaitForTerminalExitRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err != nil {
		t.Fatalf("WaitForExit 失败: %v", err)
	}

	// 退出后订阅：快照应包含完整输出与退出状态
	snapshots, _, cancel := b.Subscribe(1)
	defer cancel()
	if len(snapshots) != 1 {
		t.Fatalf("期望 1 个快照，实际 %d", len(snapshots))
	}
	snap := snapshots[0]
	if snap.TerminalID != resp.TerminalId || snap.Command != "echo snapshot-data" {
		t.Fatalf("快照基本信息不符: %+v", snap)
	}
	if !strings.Contains(string(snap.Output), "snapshot-data") {
		t.Fatalf("快照输出不符: %q", snap.Output)
	}
	if !snap.Exited || snap.ExitCode == nil || *snap.ExitCode != 0 {
		t.Fatalf("快照退出状态不符: %+v", snap)
	}

	// 未知 DB 会话订阅：无快照
	empty, _, cancel2 := b.Subscribe(99)
	defer cancel2()
	if len(empty) != 0 {
		t.Fatalf("未知会话不应有快照: %d", len(empty))
	}
}

func TestTerminalBridgeReleaseSession(t *testing.T) {
	b := newTestBridge()
	resp, err := b.Create(context.Background(), acp.CreateTerminalRequest{
		SessionId: "sess-1",
		Command:   "sleep",
		Args:      []string{"30"},
	})
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	b.ReleaseSession(1)
	if _, err := b.Output(acp.TerminalOutputRequest{SessionId: "sess-1", TerminalId: resp.TerminalId}); err == nil {
		t.Fatal("ReleaseSession 后终端应已移除")
	}
	// 快照应为空
	snapshots, _, cancel := b.Subscribe(1)
	defer cancel()
	if len(snapshots) != 0 {
		t.Fatalf("ReleaseSession 后不应有快照: %d", len(snapshots))
	}
}
