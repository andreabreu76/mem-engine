package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreabreu76/mem-engine/internal/bench"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const nativeFixtures = "../../internal/importer/testdata/native"

func importHome(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := setHome(t)
	if err := os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func copyNative(t *testing.T, dst string) string {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(nativeFixtures)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(nativeFixtures, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func countMemories(t *testing.T, home, key string) int {
	t.Helper()
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ms, err := st.ListMemories(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return len(ms)
}

func TestImportCommandProject(t *testing.T) {
	home := importHome(t)
	src := copyNative(t, filepath.Join(t.TempDir(), "memory"))
	repo := filepath.Join(t.TempDir(), "billing-api")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := project.Resolve(repo, config.Config{})
	if err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"import", src, "--project", repo}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if want := "imported 4, skipped 0\nunresolved [[missing]] in broken-link.md\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errb.Len() != 0 {
		t.Errorf("stderr = %q", errb.String())
	}
	if n := countMemories(t, home, p.Key); n != 4 {
		t.Errorf("memories in %s = %d, want 4", p.Key, n)
	}

	out.Reset()
	if code := run([]string{"import", "--project=billing-api", src}, nil, &out, &errb); code != 0 {
		t.Fatalf("rerun by name: code = %d, stderr = %q", code, errb.String())
	}
	if want := "imported 0, skipped 4\n"; out.String() != want {
		t.Errorf("rerun stdout = %q, want %q", out.String(), want)
	}
}

func TestImportCommandDefaultsToUserForHomeMemoryDir(t *testing.T) {
	home := importHome(t)
	userHome := os.Getenv("HOME")
	src := copyNative(t, filepath.Dir(bench.NativeMemoryPath(userHome, userHome)))
	if err := os.WriteFile(filepath.Join(src, "plain.md"), []byte("no frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	if code := run([]string{"import", src}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	want := "imported 4, skipped 1\nunresolved [[missing]] in broken-link.md\nplain.md: skipped, no frontmatter\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if n := countMemories(t, home, project.User.Key); n != 4 {
		t.Errorf("user memories = %d, want 4", n)
	}
}

func TestImportCommandRequiresProjectOutsideHomeMemoryDir(t *testing.T) {
	home := importHome(t)
	src := copyNative(t, filepath.Join(t.TempDir(), "memory"))
	var out, errb bytes.Buffer
	if code := run([]string{"import", src}, nil, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "--project") {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
	if _, err := os.Stat(filepath.Join(home, "mem.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("mem.db created: %v", err)
	}
}

func TestImportCommandDryRun(t *testing.T) {
	home := importHome(t)
	src := copyNative(t, filepath.Join(t.TempDir(), "memory"))
	var out, errb bytes.Buffer
	if code := run([]string{"import", src, "--user", "--dry-run"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	want := "dry run: would import 4 into user, skip 0\n" +
		"  broken link\n  deploy window\n  editor preference\n  retry policy\n" +
		"unresolved [[missing]] in broken-link.md\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %v into the data dir", entries)
	}
}

func TestImportCommandFatal(t *testing.T) {
	importHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"import", filepath.Join(t.TempDir(), "absent"), "--user"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.HasPrefix(errb.String(), "mem import: ") {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestImportCommandUnknownProject(t *testing.T) {
	importHome(t)
	src := copyNative(t, filepath.Join(t.TempDir(), "memory"))
	var out, errb bytes.Buffer
	if code := run([]string{"import", src, "--project", "billing-api"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errb.String(), `unknown project "billing-api"`) {
		t.Errorf("stderr = %q", errb.String())
	}
}

func TestImportCommandBadArgs(t *testing.T) {
	importHome(t)
	for _, args := range [][]string{
		{"import"},
		{"import", "a", "b"},
		{"import", "a", "--user", "--project", "x"},
		{"import", "a", "--project"},
		{"import", "a", "--project="},
		{"import", "--bogus", "a"},
	} {
		var out, errb bytes.Buffer
		if code := run(args, nil, &out, &errb); code != 2 {
			t.Errorf("%q: code = %d, want 2", args, code)
		}
	}
}
