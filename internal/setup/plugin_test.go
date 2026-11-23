package setup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var pluginDir = filepath.Join("..", "..", "plugins", "mem-engine")

func isolateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MEM_ENGINE_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func readPluginFile(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(pluginDir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func normalizeJSON(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPluginHooksMatchSpec(t *testing.T) {
	isolateEnv(t)
	data := readPluginFile(t, filepath.Join("hooks", "hooks.json"))
	var got any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("hooks.json: %v", err)
	}
	want := normalizeJSON(t, map[string]any{"hooks": HooksConfig()})
	if !reflect.DeepEqual(got, want) {
		wantJSON, err := encode(want)
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("plugins/mem-engine/hooks/hooks.json drifted from HooksConfig\ngot:\n%s\nwant:\n%s", data, wantJSON)
	}
	if strings.Contains(string(data), "CLAUDE_PLUGIN_ROOT") {
		t.Errorf("hooks.json must call mem from PATH, got %s", data)
	}
}

func TestPluginMCPCommand(t *testing.T) {
	isolateEnv(t)
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(readPluginFile(t, ".mcp.json"), &cfg); err != nil {
		t.Fatalf(".mcp.json: %v", err)
	}
	if len(cfg.MCPServers) != 1 {
		t.Fatalf("mcpServers = %+v, want only %s", cfg.MCPServers, MCPServerName)
	}
	srv, ok := cfg.MCPServers[MCPServerName]
	if !ok {
		t.Fatalf("mcpServers = %+v, missing %s", cfg.MCPServers, MCPServerName)
	}
	if srv.Command != "mem" || !reflect.DeepEqual(srv.Args, []string{"mcp"}) || srv.Env != nil {
		t.Errorf("mem-engine server = %+v, want command mem args [mcp]", srv)
	}
}
