package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func TestReindexCommand(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := setHome(t)
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dbPath := filepath.Join(home, "mem.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := &memory.Service{Store: st, WikiRoot: filepath.Join(home, "wiki")}
	p := project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: project.DirName("billing-api", "/home/dev/src/billing-api")}
	for _, title := range []string{"First", "Second"} {
		if _, err := svc.Save(context.Background(), p, memory.Input{Type: "fact", Title: title, Summary: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if err := os.WriteFile(filepath.Join(home, "wiki", p.Dir, "broken.md"), []byte("---\nid: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(dbPath + suffix)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"reindex"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "indexed 2, skipped 1" || !strings.HasPrefix(lines[1], p.Dir+"/broken.md: ") {
		t.Errorf("stdout = %q", out.String())
	}
	if errb.Len() != 0 {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestReindexCommandEmptyHome(t *testing.T) {
	home := setHome(t)
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"reindex"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if out.String() != "indexed 0, skipped 0\n" {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestReindexCommandFatal(t *testing.T) {
	home := setHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("retention_days = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"reindex"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if out.Len() != 0 || !strings.HasPrefix(errb.String(), "mem reindex: ") {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}

func TestReindexCommandRejectsArgs(t *testing.T) {
	setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"reindex", "now"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
