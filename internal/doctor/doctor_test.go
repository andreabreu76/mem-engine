package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/setup"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func newEnv(t *testing.T) Env {
	t.Helper()
	userHome := t.TempDir()
	dataHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("MEM_ENGINE_HOME", dataHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	cfg, err := config.LoadFrom(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	return Env{
		Cfg:      cfg,
		UserHome: userHome,
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("fake claude: exit status 1")
		},
		Executable: filepath.Join(userHome, "bin", "mem"),
		Now:        time.Now(),
	}
}

func installHooks(t *testing.T, env Env) {
	t.Helper()
	data, _, err := setup.MergeHooks(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(env.UserHome, ".claude", "settings.json"), string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func createDB(t *testing.T, env Env) {
	t.Helper()
	st, err := store.Open(env.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func find(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing from %+v", name, checks)
	return Check{}
}

func TestDoctorHealthy(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	createDB(t, env)

	checks := Run(context.Background(), env)
	want := []string{"binary", "config", "data-dir", "database", "spool", "capture-errors", "hooks", "hooks-duplicated", "mcp"}
	if len(checks) != len(want) {
		t.Fatalf("got %d checks, want %d: %+v", len(checks), len(want), checks)
	}
	for i, c := range checks {
		if c.Name != want[i] {
			t.Errorf("check %d = %q, want %q", i, c.Name, want[i])
		}
		if c.Name != "binary" && c.Level != OK {
			t.Errorf("%s = %v %q, want OK", c.Name, c.Level, c.Detail)
		}
	}
	if got := find(t, checks, "binary"); got.Level != Warn {
		t.Errorf("binary = %v %q, want Warn", got.Level, got.Detail)
	}
	entries, err := os.ReadDir(env.Cfg.Home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "mem.db") {
			t.Errorf("doctor left %q in the data dir", e.Name())
		}
	}
}

func TestDoctorBinaryOnPath(t *testing.T) {
	env := newEnv(t)
	bin := filepath.Join(env.UserHome, "bin", "mem")
	writeFile(t, bin, "")
	link := filepath.Join(env.UserHome, "mem-link")
	if err := os.Symlink(bin, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	env.Executable = bin
	env.LookPath = func(string) (string, error) { return link, nil }
	if got := find(t, Run(context.Background(), env), "binary"); got.Level != OK {
		t.Errorf("binary = %v %q, want OK", got.Level, got.Detail)
	}

	other := filepath.Join(env.UserHome, "other", "mem")
	writeFile(t, other, "")
	env.LookPath = func(string) (string, error) { return other, nil }
	got := find(t, Run(context.Background(), env), "binary")
	if got.Level != Warn || !strings.Contains(got.Detail, other) {
		t.Errorf("binary = %v %q, want Warn naming %s", got.Level, got.Detail, other)
	}
}

func TestDoctorSpoolPending(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	createDB(t, env)
	for range 3 {
		if err := spool.Append(env.Cfg.SpoolDir(), map[string]string{"kind": "prompt"}); err != nil {
			t.Fatal(err)
		}
	}
	got := find(t, Run(context.Background(), env), "spool")
	if got.Level != Warn || !strings.Contains(got.Detail, "3") {
		t.Errorf("spool = %v %q, want Warn with count 3", got.Level, got.Detail)
	}
}

func TestDoctorCaptureErrors(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	st, err := store.Open(env.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.RecordCaptureError(ctx, "PostToolUse", "old failure", env.Now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordCaptureError(ctx, "PostToolUse", "disk full", env.Now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	got := find(t, Run(ctx, env), "capture-errors")
	if got.Level != Warn || !strings.Contains(got.Detail, "1 ") || !strings.Contains(got.Detail, "disk full") {
		t.Errorf("capture-errors = %v %q, want Warn with 1 and the last message", got.Level, got.Detail)
	}
}

func TestDoctorSchemaTooNew(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	createDB(t, env)
	db, err := sql.Open("sqlite", env.Cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	got := find(t, Run(context.Background(), env), "database")
	if got.Level != Fail || !strings.Contains(got.Detail, "upgrade mem") {
		t.Errorf("database = %v %q, want Fail with upgrade mem", got.Level, got.Detail)
	}
}

func TestDoctorNoDatabaseDoesNotCreate(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	missing := filepath.Join(env.Cfg.Home, "absent")
	env.Cfg.Home = missing

	checks := Run(context.Background(), env)
	if got := find(t, checks, "database"); got.Level != Warn || !strings.Contains(got.Detail, "no database yet") {
		t.Errorf("database = %v %q, want Warn no database yet", got.Level, got.Detail)
	}
	if got := find(t, checks, "data-dir"); got.Level != Warn {
		t.Errorf("data-dir = %v %q, want Warn", got.Level, got.Detail)
	}
	for _, c := range checks {
		if c.Level == Fail {
			t.Errorf("%s = Fail %q", c.Name, c.Detail)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("doctor created %s: %v", missing, err)
	}
}

func TestDoctorBrokenConfig(t *testing.T) {
	env := newEnv(t)
	installHooks(t, env)
	writeFile(t, filepath.Join(env.Cfg.Home, "config.toml"), "retention_days = [")
	if got := find(t, Run(context.Background(), env), "config"); got.Level != Fail {
		t.Errorf("config = %v %q, want Fail", got.Level, got.Detail)
	}
}

func TestDoctorMissingHooks(t *testing.T) {
	env := newEnv(t)
	createDB(t, env)
	settings := filepath.Join(env.UserHome, ".claude", "settings.json")

	got := find(t, Run(context.Background(), env), "hooks")
	if got.Level != Warn || !strings.Contains(got.Detail, "run mem setup") {
		t.Errorf("no settings: hooks = %v %q, want Warn run mem setup", got.Level, got.Detail)
	}

	writeFile(t, settings, `{"theme": "dark"}`)
	got = find(t, Run(context.Background(), env), "hooks")
	if got.Level != Warn || !strings.Contains(got.Detail, "run mem setup") {
		t.Errorf("no mem hooks: hooks = %v %q, want Warn run mem setup", got.Level, got.Detail)
	}

	writeFile(t, settings, `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "mem hook Stop"}]}]}}`)
	got = find(t, Run(context.Background(), env), "hooks")
	if got.Level != Warn || !strings.Contains(got.Detail, "hooks outdated, run mem setup") {
		t.Errorf("partial hooks: hooks = %v %q, want Warn outdated", got.Level, got.Detail)
	}

	writeFile(t, settings, `{"hooks": `)
	got = find(t, Run(context.Background(), env), "hooks")
	if got.Level != Warn || !strings.Contains(got.Detail, "invalid JSON") {
		t.Errorf("invalid settings: hooks = %v %q, want Warn invalid JSON", got.Level, got.Detail)
	}
}

func TestDoctorMCP(t *testing.T) {
	env := newEnv(t)
	got := find(t, Run(context.Background(), env), "mcp")
	if got.Level != OK || got.Detail != "claude not found, skipped" {
		t.Errorf("without claude: mcp = %v %q", got.Level, got.Detail)
	}

	claude := filepath.Join(env.UserHome, "bin", "claude")
	env.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return claude, nil
		}
		return "", errors.New("not found")
	}
	var calls []string
	env.RunCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return []byte("mem-engine:\n  Scope: User config"), nil
	}
	got = find(t, Run(context.Background(), env), "mcp")
	if got.Level != OK || !strings.Contains(got.Detail, "registered") {
		t.Errorf("registered: mcp = %v %q", got.Level, got.Detail)
	}
	if want := claude + " mcp get mem-engine"; len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want [%q]", calls, want)
	}

	env.RunCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("No MCP server found with name: mem-engine"), errors.New("exit status 1")
	}
	got = find(t, Run(context.Background(), env), "mcp")
	if got.Level != Warn || !strings.Contains(got.Detail, "run mem setup") {
		t.Errorf("not registered: mcp = %v %q, want Warn run mem setup", got.Level, got.Detail)
	}
}

func TestDoctorDuplicatedHooks(t *testing.T) {
	env := newEnv(t)
	createDB(t, env)
	settings := filepath.Join(env.UserHome, ".claude", "settings.json")
	hooks, _, err := setup.MergeHooks(nil)
	if err != nil {
		t.Fatal(err)
	}
	withPlugins := func(enabled string) string {
		var m map[string]any
		if err := json.Unmarshal(hooks, &m); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(enabled), &m); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	cases := []struct {
		name, settings string
		want           Level
	}{
		{"no settings", "", OK},
		{"plugin only", `{"enabledPlugins": {"mem-engine@mem-engine": true}}`, OK},
		{"settings hooks only", string(hooks), OK},
		{"plugin disabled", withPlugins(`{"enabledPlugins": {"mem-engine@mem-engine": false}}`), OK},
		{"other plugin", withPlugins(`{"enabledPlugins": {"mem-engine-extra@acme": true, "other@mem-engine": true}}`), OK},
		{"both", withPlugins(`{"enabledPlugins": {"mem-engine@mem-engine": true}}`), Warn},
		{"both from a fork", withPlugins(`{"enabledPlugins": {"mem-engine@acme": true}}`), Warn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(settings)
			if c.settings != "" {
				writeFile(t, settings, c.settings)
			}
			got := find(t, Run(context.Background(), env), "hooks-duplicated")
			if got.Level != c.want {
				t.Fatalf("hooks-duplicated = %v %q, want %v", got.Level, got.Detail, c.want)
			}
			if c.want == Warn && (!strings.Contains(got.Detail, "mem-engine@") || !strings.Contains(got.Detail, "remove")) {
				t.Errorf("detail %q should name the plugin and suggest removing one", got.Detail)
			}
		})
	}
}

func TestDoctorPluginProvidesHooksAndMCP(t *testing.T) {
	env := newEnv(t)
	createDB(t, env)
	settings := filepath.Join(env.UserHome, ".claude", "settings.json")
	env.LookPath = func(name string) (string, error) {
		if name == "claude" {
			return filepath.Join(env.UserHome, "bin", "claude"), nil
		}
		return "", errors.New("not found")
	}
	calls := 0
	env.RunCommand = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("exit status 1")
	}

	writeFile(t, settings, `{"enabledPlugins": {"mem-engine@zeta": true, "mem-engine@acme": true, "mem-engine@beta": false}}`)
	checks := Run(context.Background(), env)
	for _, name := range []string{"hooks", "mcp"} {
		got := find(t, checks, name)
		if got.Level != OK || got.Detail != "provided by plugin mem-engine@acme" {
			t.Errorf("plugin only: %s = %v %q, want OK provided by plugin mem-engine@acme", name, got.Level, got.Detail)
		}
	}
	if got := find(t, checks, "hooks-duplicated"); got.Level != OK {
		t.Errorf("plugin only: hooks-duplicated = %v %q, want OK", got.Level, got.Detail)
	}
	if calls != 0 {
		t.Errorf("plugin only: claude ran %d times, want 0", calls)
	}

	hooks, _, err := setup.MergeHooks([]byte(`{"enabledPlugins": {"mem-engine@zeta": true, "mem-engine@acme": true}}`))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, settings, string(hooks))
	checks = Run(context.Background(), env)
	if got := find(t, checks, "hooks"); got.Level != OK || got.Detail != settings {
		t.Errorf("both: hooks = %v %q, want OK %s", got.Level, got.Detail, settings)
	}
	if got := find(t, checks, "mcp"); got.Level != OK || got.Detail != "provided by plugin mem-engine@acme" {
		t.Errorf("both: mcp = %v %q, want OK provided by plugin", got.Level, got.Detail)
	}
	got := find(t, checks, "hooks-duplicated")
	if got.Level != Warn || !strings.Contains(got.Detail, "plugin mem-engine@acme ") {
		t.Errorf("both: hooks-duplicated = %v %q, want Warn naming mem-engine@acme", got.Level, got.Detail)
	}

	writeFile(t, settings, `{"enabledPlugins": {"mem-engine@acme": false}}`)
	checks = Run(context.Background(), env)
	if got := find(t, checks, "hooks"); got.Level != Warn || !strings.Contains(got.Detail, "run mem setup") {
		t.Errorf("neither: hooks = %v %q, want Warn run mem setup", got.Level, got.Detail)
	}
	if got := find(t, checks, "mcp"); got.Level != Warn || !strings.Contains(got.Detail, "run mem setup") {
		t.Errorf("neither: mcp = %v %q, want Warn run mem setup", got.Level, got.Detail)
	}
	if calls != 1 {
		t.Errorf("claude ran %d times, want 1 (neither only)", calls)
	}
}
