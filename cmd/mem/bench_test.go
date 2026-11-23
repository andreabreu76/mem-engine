package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/bench"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/handoff"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func benchRepo(t *testing.T) (string, project.Project) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "billing-api")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := project.Resolve(dir, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return dir, p
}

func writeNative(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBenchCommandWithoutDatabase(t *testing.T) {
	home := setHome(t)
	if err := os.Remove(home); err != nil {
		t.Fatal(err)
	}
	dir, p := benchRepo(t)
	writeNative(t, bench.NativeMemoryPath(os.Getenv("HOME"), p.Key), strings.Repeat(strings.Repeat("n", 39)+"\n", 100))

	var out, errb bytes.Buffer
	if code := run([]string{"bench", dir}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	want := "native MEMORY.md: 4000 bytes loaded (~1000 tokens)\n" +
		"mem-engine handoff: 98 bytes (~24 tokens)\n" +
		"saving: ~976 tokens per session (98%)\n"
	if out.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), want)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("data dir created: %v", err)
	}
}

func TestBenchCommandUsesLastSession(t *testing.T) {
	home := setHome(t)
	dir, p := benchRepo(t)
	ctx := context.Background()
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	row := store.ProjectRow{Key: p.Key, Name: p.Name, Dir: p.Dir}
	at := time.Date(2026, 11, 20, 14, 2, 0, 0, time.UTC)
	if err := st.EnsureProject(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, store.SessionEvent{SessionID: "s1", Project: p.Key, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: p.Key, Kind: "prompt", Title: "prompt", Body: "fix the retry loop", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	in, err := handoff.Collect(ctx, st, row, "", "startup")
	if err != nil {
		t.Fatal(err)
	}
	text := handoff.Build(in)
	st.Close()
	if !strings.Contains(text, "fix the retry loop") {
		t.Fatalf("handoff = %q", text)
	}
	native := filepath.Join(t.TempDir(), "MEMORY.md")
	writeNative(t, native, strings.Repeat("- note\n", 300))

	var out, errb bytes.Buffer
	if code := run([]string{"bench", "--native", native, dir}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	wantLine := fmt.Sprintf("mem-engine handoff: %d bytes (~%d tokens)\n", len(text), len(text)/4)
	if !strings.HasPrefix(out.String(), "native MEMORY.md: 1400 bytes loaded (~350 tokens)\n"+wantLine) {
		t.Errorf("stdout = %q", out.String())
	}
}

func TestBenchCommandNativeNotFound(t *testing.T) {
	home := setHome(t)
	dir, p := benchRepo(t)
	t.Chdir(dir)

	var out, errb bytes.Buffer
	if code := run([]string{"bench"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	want := "native MEMORY.md: not found at " + bench.NativeMemoryPath(os.Getenv("HOME"), p.Key) + "\n" +
		"mem-engine handoff: 98 bytes (~24 tokens)\n"
	if out.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), want)
	}
	if _, err := os.Stat(filepath.Join(home, "mem.db")); !os.IsNotExist(err) {
		t.Errorf("database created: %v", err)
	}
}

func TestBenchCommandNegativeSaving(t *testing.T) {
	setHome(t)
	dir, _ := benchRepo(t)
	native := filepath.Join(t.TempDir(), "MEMORY.md")
	writeNative(t, native, strings.Repeat("n", 60))

	var out, errb bytes.Buffer
	if code := run([]string{"bench", dir, "--native=" + native}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	want := "native MEMORY.md: 60 bytes loaded (~15 tokens)\n" +
		"mem-engine handoff: 98 bytes (~24 tokens)\n" +
		"saving: ~-9 tokens per session (-63%)\n"
	if out.String() != want {
		t.Errorf("stdout =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestBenchCommandBadArgs(t *testing.T) {
	setHome(t)
	dir, _ := benchRepo(t)
	for _, args := range [][]string{
		{"bench", dir, dir},
		{"bench", "--native"},
		{"bench", "--verbose"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, nil, &out, &errb); code != 2 || out.Len() != 0 {
			t.Errorf("run(%q) = %d, stdout = %q", args, code, out.String())
		}
	}
	var out, errb bytes.Buffer
	if code := run([]string{"bench", filepath.Join(dir, "missing")}, nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "mem bench:") {
		t.Errorf("missing path: code = %d, stderr = %q", code, errb.String())
	}
}
