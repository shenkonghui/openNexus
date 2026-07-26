package gatewaymcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"opennexus/internal/acp"
)

// GatewayState 是网关的持久化运行时状态，独立于 mcp.json：
//   - Disabled：被用户禁用的上游名（网关不再连接/暴露其工具，但不修改 mcp.json 原文）
//   - CustomServers：用户在网关层面添加的自定义上游（不写入 mcp.json）
//
// 默认存储于 ~/.openNexus/gateway-state.json，主程序与独立二进制共用同一份。
type GatewayState struct {
	Disabled      []string                    `json:"disabled,omitempty"`
	CustomServers []acp.NamedMCPServerEntry   `json:"custom_servers,omitempty"`
}

// stateManager 负责 GatewayState 的内存缓存与磁盘持久化。
// 所有方法均线程安全；磁盘写入采用原子替换（写临时文件后 rename）。
type stateManager struct {
	mu       sync.Mutex
	path     string
	state    GatewayState
	loaded   bool
	disabled map[string]bool // 由 state.Disabled 派生，O(1) 查找
}

// newStateManager 创建状态管理器。path 为空时使用默认路径。
func newStateManager(path string) (*stateManager, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("无法定位用户主目录: %w", err)
		}
		path = filepath.Join(home, ".openNexus", "gateway-state.json")
	}
	return &stateManager{path: path, disabled: map[string]bool{}}, nil
}

// load 从磁盘读取状态到内存。文件不存在时视为空状态（不报错）。
func (m *stateManager) load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		return nil
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			m.loaded = true
			return nil
		}
		return fmt.Errorf("读取网关状态失败 (%s): %w", m.path, err)
	}
	var st GatewayState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("解析网关状态失败 (%s): %w", m.path, err)
	}
	m.state = st
	m.rebuildIndexLocked()
	m.loaded = true
	return nil
}

// rebuildIndexLocked 由 state.Disabled 重建 disabled 集合。调用方须持锁。
func (m *stateManager) rebuildIndexLocked() {
	m.disabled = make(map[string]bool, len(m.state.Disabled))
	for _, n := range m.state.Disabled {
		m.disabled[strings.TrimSpace(n)] = true
	}
}

// IsDisabled 返回指定上游是否被禁用。未加载时先 load。
func (m *stateManager) IsDisabled(name string) (bool, error) {
	if err := m.load(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.disabled[name], nil
}

// DisabledSet 返回当前禁用集合的副本（供 Aggregator refresh 用）。
func (m *stateManager) DisabledSet() (map[string]bool, error) {
	if err := m.load(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]bool, len(m.disabled))
	for k := range m.disabled {
		out[k] = true
	}
	return out, nil
}

// CustomServers 返回自定义上游列表的副本。
func (m *stateManager) CustomServers() ([]acp.NamedMCPServerEntry, error) {
	if err := m.load(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]acp.NamedMCPServerEntry, len(m.state.CustomServers))
	copy(out, m.state.CustomServers)
	return out, nil
}

// SetDisabled 启用或禁用指定上游，并持久化。
func (m *stateManager) SetDisabled(name string, disabled bool) error {
	if err := m.load(); err != nil {
		return err
	}
	m.mu.Lock()
	name = strings.TrimSpace(name)
	if name == "" {
		m.mu.Unlock()
		return fmt.Errorf("上游名不能为空")
	}
	changed := false
	if disabled {
		if !m.disabled[name] {
			m.disabled[name] = true
			m.state.Disabled = append(m.state.Disabled, name)
			sort.Strings(m.state.Disabled)
			changed = true
		}
	} else {
		if m.disabled[name] {
			delete(m.disabled, name)
			m.state.Disabled = removeString(m.state.Disabled, name)
			changed = true
		}
	}
	m.mu.Unlock()
	if !changed {
		return nil
	}
	return m.save()
}

// AddCustomServer 添加一个自定义上游。name 重复时返回错误。
func (m *stateManager) AddCustomServer(name string, entry acp.MCPServerEntry) error {
	if err := m.load(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("上游名不能为空")
	}
	if name == GatewayMCPName {
		return fmt.Errorf("不能使用网关自身的名称 %q", GatewayMCPName)
	}
	m.mu.Lock()
	for _, cs := range m.state.CustomServers {
		if cs.Name == name {
			m.mu.Unlock()
			return fmt.Errorf("自定义上游 %q 已存在", name)
		}
	}
	m.state.CustomServers = append(m.state.CustomServers, acp.NamedMCPServerEntry{Name: name, Entry: entry})
	m.mu.Unlock()
	return m.save()
}

// RemoveCustomServer 按名移除自定义上游。不存在时无操作。
func (m *stateManager) RemoveCustomServer(name string) error {
	if err := m.load(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	m.mu.Lock()
	before := len(m.state.CustomServers)
	m.state.CustomServers = removeNamed(m.state.CustomServers, name)
	changed := len(m.state.CustomServers) != before
	m.mu.Unlock()
	if !changed {
		return nil
	}
	return m.save()
}

// save 把当前状态原子写入磁盘。调用方不应持锁（此方法内部加锁）。
func (m *stateManager) save() error {
	m.mu.Lock()
	st := m.state
	m.mu.Unlock()

	if dir := filepath.Dir(m.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建状态目录失败: %w", err)
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入状态文件失败: %w", err)
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return fmt.Errorf("替换状态文件失败: %w", err)
	}
	return nil
}

func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, v := range slice {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func removeNamed(slice []acp.NamedMCPServerEntry, name string) []acp.NamedMCPServerEntry {
	out := make([]acp.NamedMCPServerEntry, 0, len(slice))
	for _, v := range slice {
		if v.Name != name {
			out = append(out, v)
		}
	}
	return out
}
