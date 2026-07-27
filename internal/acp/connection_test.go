package acp

import (
	"testing"

	"github.com/coder/acp-go-sdk"
)

func TestClientCapabilities(t *testing.T) {
	caps := clientCapabilities(true)
	if !caps.Fs.ReadTextFile || !caps.Fs.WriteTextFile {
		t.Fatalf("fs 能力应始终声明: %+v", caps.Fs)
	}
	if !caps.Terminal {
		t.Fatal("terminalEnabled=true 时应声明 Terminal 能力")
	}
	if clientCapabilities(false).Terminal {
		t.Fatal("terminalEnabled=false 时不应声明 Terminal 能力")
	}
}

func TestAutoAuthMethodIDs(t *testing.T) {
	methods := []acp.AuthMethod{
		{Agent: &acp.AuthMethodAgent{Id: "claude-login", Name: "Login"}},
		{EnvVar: &acp.AuthMethodEnvVarInline{Id: "api-key", Name: "API Key"}},
		{Terminal: &acp.AuthMethodTerminalInline{Id: "terminal-login", Name: "Terminal"}},
	}
	got := autoAuthMethodIDs(methods)
	if len(got) != 1 || got[0] != "api-key" {
		t.Fatalf("autoAuthMethodIDs() = %v, want [api-key]", got)
	}
	if ids := autoAuthMethodIDs(nil); len(ids) != 0 {
		t.Fatalf("autoAuthMethodIDs(nil) = %v, want empty", ids)
	}
	agentOnly := []acp.AuthMethod{
		{Agent: &acp.AuthMethodAgent{Id: "claude-login", Name: "Login"}},
	}
	if ids := autoAuthMethodIDs(agentOnly); len(ids) != 0 {
		t.Fatalf("autoAuthMethodIDs(agentOnly) = %v, want empty", ids)
	}
}

func TestAuthMethodID(t *testing.T) {
	tests := []struct {
		name string
		m    acp.AuthMethod
		want string
	}{
		{
			name: "agent",
			m:    acp.AuthMethod{Agent: &acp.AuthMethodAgent{Id: "agent-login", Name: "Login"}},
			want: "agent-login",
		},
		{
			name: "env_var",
			m:    acp.AuthMethod{EnvVar: &acp.AuthMethodEnvVarInline{Id: "api-key", Name: "API Key"}},
			want: "api-key",
		},
		{
			name: "empty",
			m:    acp.AuthMethod{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authMethodID(tt.m); got != tt.want {
				t.Fatalf("authMethodID() = %q, want %q", got, tt.want)
			}
		})
	}
}
