package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func TestAliasAppendsAndResolves(t *testing.T) {
	home := setHome(t)
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("retention_days = 30"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldKey := filepath.Join(t.TempDir(), `billing "api"\old`)
	newPath := t.TempDir()

	var out, errb bytes.Buffer
	if code := run([]string{"project", "alias", oldKey, newPath}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	cfg, err := config.LoadFrom(home)
	if err != nil {
		t.Fatalf("config after alias: %v", err)
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("retention_days = %d, existing content lost", cfg.RetentionDays)
	}
	p, err := project.Resolve(newPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != oldKey {
		t.Errorf("Resolve(%s).Key = %q, want %q", newPath, p.Key, oldKey)
	}

	errb.Reset()
	if code := run([]string{"project", "alias", oldKey, newPath}, nil, &out, &errb); code == 0 {
		t.Error("duplicate alias accepted")
	}
	if cfg, err := config.LoadFrom(home); err != nil || len(cfg.Aliases) != 1 {
		t.Errorf("aliases after duplicate = %+v, %v", cfg.Aliases, err)
	}
	for _, args := range [][]string{
		{"project", "alias", "billing-api", newPath},
		{"project", "alias", oldKey, "relative/path"},
		{"project", "alias", oldKey},
	} {
		if code := run(args, nil, &out, &errb); code == 0 {
			t.Errorf("run(%v) = 0, want error", args)
		}
	}
}

func TestAliasRefusesConfigItCannotExtend(t *testing.T) {
	home := setHome(t)
	cfgPath := filepath.Join(home, "config.toml")
	old := "alias = [{path = \"/home/dev/src/acme/a\", key = \"/home/dev/src/acme/b\"}]\n"
	if err := os.WriteFile(cfgPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadFrom(home); err != nil {
		t.Fatalf("fixture not accepted by LoadFrom: %v", err)
	}
	var out, errb bytes.Buffer
	code := run([]string{"project", "alias", filepath.Join(t.TempDir(), "old"), t.TempDir()}, nil, &out, &errb)
	if code != 1 || !strings.Contains(errb.String(), "add the alias by hand") {
		t.Errorf("code = %d, stderr = %q", code, errb.String())
	}
	if data, _ := os.ReadFile(cfgPath); string(data) != old {
		t.Errorf("config changed:\n%s", data)
	}
	if _, err := config.LoadFrom(home); err != nil {
		t.Errorf("config unreadable after refused alias: %v", err)
	}
}

func TestAliasCreatesConfigPrivate(t *testing.T) {
	home := setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"project", "alias", filepath.Join(t.TempDir(), "old"), t.TempDir()}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	fi, err := os.Stat(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func seedMerge(t *testing.T, home string) (from, to project.Project, id string) {
	t.Helper()
	ctx := context.Background()
	from = project.Project{Key: "/home/dev/src/billing-api-old", Name: "billing-api-old"}
	from.Dir = project.DirName(from.Name, from.Key)
	to = project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api"}
	to.Dir = project.DirName(to.Name, to.Key)
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &memory.Service{Store: st, WikiRoot: filepath.Join(home, "wiki"), Now: time.Now}
	m, err := svc.Save(ctx, from, memory.Input{Type: "decision", Title: "Retry", Summary: "retry with backoff"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureProject(ctx, store.ProjectRow{Key: to.Key, Name: to.Name, Dir: to.Dir}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	if err := st.TouchSession(ctx, store.SessionEvent{SessionID: "s1", Project: from.Key, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: from.Key, Kind: "prompt", Title: "p", Body: "fix invoices", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	return from, to, m.ID
}

func TestProjectMergeCommand(t *testing.T) {
	home := importHome(t)
	from, to, id := seedMerge(t, home)

	var out, errb bytes.Buffer
	if code := run([]string{"project", "merge", from.Name, to.Key}, strings.NewReader("n\n"), &out, &errb); code != 1 {
		t.Fatalf("declined merge code = %d, stderr = %q", code, errb.String())
	}
	if countMemories(t, home, from.Key) != 1 {
		t.Fatal("declined merge moved memories")
	}
	if !strings.Contains(out.String(), "1 memories, 1 observations and 1 sessions") {
		t.Errorf("prompt = %q, want the counts", out.String())
	}
	errb.Reset()
	if code := run([]string{"project", "merge", from.Key, "billing-api-new", "--yes"}, nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "open a Claude Code session") {
		t.Errorf("unknown destination code = %d, stderr = %q", code, errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"project", "merge", from.Name, to.Name, "--yes"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "moved 1 observations, 1 memories") {
		t.Errorf("stdout = %q", out.String())
	}
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, err := st.GetMemory(context.Background(), id)
	if err != nil || m.Project != to.Key || !strings.HasPrefix(m.Path, to.Dir+"/") {
		t.Errorf("memory after merge = %+v, %v", m, err)
	}
	if _, err := st.ProjectByKey(context.Background(), from.Key); err == nil {
		t.Error("source project still listed after merge")
	}

	for _, args := range [][]string{
		{"project", "merge", to.Name, to.Key, "--yes"},
		{"project", "merge", "user", to.Key, "--yes"},
		{"project", "merge", "unknown", to.Key, "--yes"},
		{"project", "merge", to.Key},
		{"project", "nope"},
	} {
		if code := run(args, nil, &out, &errb); code == 0 {
			t.Errorf("run(%v) = 0, want error", args)
		}
	}
}

func TestSetupDisableNativeMemory(t *testing.T) {
	path := setUserHome(t)
	old := `{"model": "opus", "autoMemoryEnabled": true}`
	writeSettings(t, path, old)

	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--disable-native-memory"}, strings.NewReader("n\n"), &out, &errb); code != 1 {
		t.Fatalf("declined code = %d, stderr = %q", code, errb.String())
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Fatalf("declined run changed settings: %s", data)
	}

	out.Reset()
	if code := run([]string{"setup", "--disable-native-memory"}, strings.NewReader("y\n"), &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"autoMemoryEnabled": false`) || !strings.Contains(string(data), `"model": "opus"`) || strings.Contains(string(data), "mem hook") {
		t.Errorf("settings = %s", data)
	}
	if bak, err := os.ReadFile(path + ".bak"); err != nil || string(bak) != old {
		t.Errorf("backup = %q, %v", bak, err)
	}

	out.Reset()
	if code := run([]string{"setup", "--disable-native-memory"}, nil, &out, &errb); code != 0 {
		t.Fatalf("second run code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "already disabled") {
		t.Errorf("second run stdout = %q", out.String())
	}
}
