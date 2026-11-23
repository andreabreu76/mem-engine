package wiki

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MEM_ENGINE_HOME", filepath.Join(home, ".mem-engine"))
	gitConfig := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func newRepo(t *testing.T) *Repo {
	t.Helper()
	home := isolate(t)
	r, err := OpenRepo(filepath.Join(home, ".mem-engine", "wiki"), "billing-api")
	if err != nil {
		t.Fatalf("OpenRepo: %v", err)
	}
	return r
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestMarshalParseRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 23, 10, 30, 0, 0, time.FixedZone("BRT", -3*3600))
	updated := time.Date(2026, 11, 24, 8, 0, 5, 0, time.UTC)
	docs := []Doc{
		{
			ID: "01JDXYZ", Type: "decision", Title: "Retry de cobrança: usar backoff exponencial",
			Summary: `Decisão "final" sobre o retry`, Project: "billing-api", Tags: []string{"billing", "retry"},
			Created: created, Updated: updated, SourceSession: "sess-1",
			Body: "Ver [[billing-retry-2]] e [[gateway]].\n\nAção: não repetir após 5 tentativas.\n",
		},
		{
			ID: "a", Type: "gotcha", Title: "--- começa com delimitador", Summary: "linha 1\n---\nlinha 3",
			Project: "acme", Created: created, Updated: updated,
			Body: "---\nfalso frontmatter: sim\n---\n\ncorpo com --- no meio\n---",
		},
		{
			ID: "b", Type: "note", Title: "---", Summary: strings.Repeat("palavra longa: ", 20),
			Created: created, Updated: updated, Body: "\n\ncomeça com linhas em branco e sem quebra no fim",
		},
		{
			ID: "c", Type: "note", Title: `aspas 'simples' e "duplas" # não é comentário`,
			Summary: "  espaços nas pontas  ", Tags: []string{"a: b", "- c", "[[d]]"},
			Created: created, Updated: updated, Body: "",
		},
	}
	for _, d := range docs {
		raw := Marshal(d)
		if !bytes.HasPrefix(raw, []byte("---\n")) {
			t.Fatalf("missing opening delimiter:\n%s", raw)
		}
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v\n%s", err, raw)
		}
		want := d
		want.Created = d.Created.UTC()
		want.Updated = d.Updated.UTC()
		if want.Tags == nil {
			want.Tags = []string{}
		}
		if !got.Created.Equal(want.Created) || got.Created.Location() != time.UTC {
			t.Errorf("Created = %v, want %v", got.Created, want.Created)
		}
		if !got.Updated.Equal(want.Updated) || got.Updated.Location() != time.UTC {
			t.Errorf("Updated = %v, want %v", got.Updated, want.Updated)
		}
		got.Created, got.Updated, want.Created, want.Updated = time.Time{}, time.Time{}, time.Time{}, time.Time{}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip mismatch\n got: %#v\nwant: %#v\nraw:\n%s", got, want, raw)
		}
	}

	raw := string(Marshal(docs[0]))
	for _, want := range []string{"\ncreated: 2026-11-23T13:30:00Z\n", "\nupdated: 2026-11-24T08:00:05Z\n", "\nsource_session: sess-1\n"} {
		if !strings.Contains(raw, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, raw)
		}
	}
	if raw := string(Marshal(docs[1])); !strings.Contains(raw, "\ntags: []\n") {
		t.Errorf("empty tags not written as []:\n%s", raw)
	}
}

func TestParseRejectsMissingID(t *testing.T) {
	cases := map[string]string{
		"id":       "---\ntype: note\ntitle: x\n---\nbody",
		"type":     "---\nid: a\ntitle: x\n---\nbody",
		"title":    "---\nid: a\ntype: note\n---\nbody",
		"frontmat": "no frontmatter here",
		"unclosed": "---\nid: a\ntype: note\ntitle: x\n",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: Parse accepted %q", name, raw)
		}
	}
	d, err := Parse([]byte("---\r\nid: a\r\ntype: note\r\ntitle: x\r\n---\r\nbody\r\n"))
	if err != nil || d.ID != "a" || d.Body != "body\r\n" {
		t.Errorf("CRLF: got %#v, %v", d, err)
	}
}

func TestUniqueName(t *testing.T) {
	r := newRepo(t)
	if got := r.UniqueName("billing-retry"); got != "billing-retry.md" {
		t.Fatalf("first = %q", got)
	}
	if err := r.Write("billing-retry.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := r.UniqueName("billing-retry"); got != "billing-retry-2.md" {
		t.Fatalf("second = %q", got)
	}
	if err := r.Write("Billing-Retry-2.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := r.UniqueName("billing-retry"); got != "billing-retry-3.md" {
		t.Fatalf("third = %q", got)
	}
	if got := r.UniqueName("BILLING-RETRY"); got != "BILLING-RETRY-3.md" {
		t.Fatalf("case-insensitive = %q", got)
	}
}

func TestWriteCommitCreatesRepo(t *testing.T) {
	needGit(t)
	r := newRepo(t)
	if err := r.Write("billing-retry.md", []byte("conteúdo\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := r.Commit("save billing-retry", "billing-retry.md"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if log := lines(gitOut(t, r.Dir, "log", "--oneline")); len(log) != 1 || !strings.Contains(log[0], "save billing-retry") {
		t.Fatalf("log = %q", log)
	}
	if files := lines(gitOut(t, r.Dir, "show", "--name-only", "--format=", "HEAD")); !reflect.DeepEqual(files, []string{"billing-retry.md"}) {
		t.Fatalf("committed files = %q", files)
	}
	if got := gitOut(t, r.Dir, "log", "-1", "--format=%an <%ae>"); got != "mem-engine <mem-engine@localhost>" {
		t.Errorf("author = %q", got)
	}
	ignore, err := os.ReadFile(filepath.Join(r.Dir, ".gitignore"))
	if err != nil || !strings.Contains(string(ignore), ".mem.lock") {
		t.Errorf(".gitignore = %q, %v", ignore, err)
	}
	data, err := os.ReadFile(filepath.Join(r.Dir, "billing-retry.md"))
	if err != nil || string(data) != "conteúdo\n" {
		t.Errorf("content = %q, %v", data, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(r.Dir, "billing-retry.md"))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("file perm = %o, want 600", perm)
		}
		for _, dir := range []string{r.Dir, filepath.Dir(r.Dir)} {
			di, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := di.Mode().Perm(); perm != 0o700 {
				t.Errorf("%s perm = %o, want 700", dir, perm)
			}
		}
	}
	names, err := r.List()
	if err != nil || !reflect.DeepEqual(names, []string{"billing-retry.md"}) {
		t.Errorf("List = %q, %v", names, err)
	}
}

func TestCommitOnlyOwnFiles(t *testing.T) {
	needGit(t)
	r := newRepo(t)
	if err := os.WriteFile(filepath.Join(r.Dir, "stranger.md"), []byte("not mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Write("a.md", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("save a", "a.md"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if files := lines(gitOut(t, r.Dir, "ls-files")); !reflect.DeepEqual(files, []string{"a.md"}) {
		t.Fatalf("tracked after first commit = %q", files)
	}

	gitOut(t, r.Dir, "add", "stranger.md")
	if err := r.Write("b.md", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("save b", "b.md"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if files := lines(gitOut(t, r.Dir, "show", "--name-only", "--format=", "HEAD")); !reflect.DeepEqual(files, []string{"b.md"}) {
		t.Fatalf("second commit files = %q", files)
	}
	if files := lines(gitOut(t, r.Dir, "ls-tree", "-r", "--name-only", "HEAD")); !reflect.DeepEqual(files, []string{"a.md", "b.md"}) {
		t.Fatalf("HEAD tree = %q", files)
	}
	names, err := r.List()
	if err != nil || !reflect.DeepEqual(names, []string{"a.md", "b.md", "stranger.md"}) {
		t.Errorf("List = %q, %v", names, err)
	}
}

func TestRemoveCommits(t *testing.T) {
	needGit(t)
	r := newRepo(t)
	for _, name := range []string{"keep.md", "gone.md"} {
		if err := r.Write(name, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Commit("save", "keep.md", "gone.md"); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("gone.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := r.Commit("forget gone", "gone.md"); err != nil {
		t.Fatalf("Commit removal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, "gone.md")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present: %v", err)
	}
	if log := lines(gitOut(t, r.Dir, "log", "--oneline")); len(log) != 2 {
		t.Fatalf("log = %q", log)
	}
	if files := lines(gitOut(t, r.Dir, "ls-tree", "-r", "--name-only", "HEAD")); !reflect.DeepEqual(files, []string{"keep.md"}) {
		t.Fatalf("HEAD tree = %q", files)
	}
	if status := gitOut(t, r.Dir, "status", "--porcelain"); status != "?? .gitignore" {
		t.Errorf("status = %q", status)
	}
}

func TestCommitIgnoresUserHooks(t *testing.T) {
	needGit(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell hooks")
	}
	r := newRepo(t)
	hooks := t.TempDir()
	marker := filepath.Join(t.TempDir(), "leaked")
	hook := "#!/bin/sh\ncat \"$1\" > " + marker + " 2>/dev/null; touch " + marker + "\nexit 1\n"
	for _, name := range []string{"pre-commit", "commit-msg", "post-commit"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte(hook), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte("[core]\n\thooksPath = "+hooks+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.Write("a.md", []byte("segredo")); err != nil {
		t.Fatal(err)
	}
	if err := r.Commit("save a", "a.md"); err != nil {
		t.Fatalf("Commit blocked by user hook: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("user hook ran: %v", err)
	}
}

func TestCommitErrorCarriesStderr(t *testing.T) {
	needGit(t)
	r := newRepo(t)
	err := r.Commit("nothing", "missing.md")
	if err == nil || !strings.Contains(err.Error(), "missing.md") {
		t.Fatalf("err = %v, want git stderr mentioning missing.md", err)
	}
}

func TestLockSerializes(t *testing.T) {
	r := newRepo(t)
	unlock, err := r.Lock(context.Background())
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	var entered atomic.Bool
	done := make(chan error, 1)
	go func() {
		second, err := r.Lock(context.Background())
		if err != nil {
			done <- err
			return
		}
		entered.Store(true)
		second()
		done <- nil
	}()
	time.Sleep(200 * time.Millisecond)
	if entered.Load() {
		t.Fatal("second goroutine entered while lock held")
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second goroutine never acquired the lock")
	}
	if !entered.Load() {
		t.Fatal("second goroutine did not enter")
	}

	held, err := r.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.Lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock with expired ctx = %v, want DeadlineExceeded", err)
	}
}

func TestOpenRepoRejectsBadDirName(t *testing.T) {
	home := isolate(t)
	for _, name := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := OpenRepo(filepath.Join(home, "wiki"), name); err == nil {
			t.Errorf("OpenRepo accepted %q", name)
		}
	}
}

func TestWriteRejectsBadName(t *testing.T) {
	r := newRepo(t)
	for _, name := range []string{"", "../x.md", "sub/x.md", ".gitignore", "x.txt"} {
		if err := r.Write(name, []byte("x")); err == nil {
			t.Errorf("Write accepted %q", name)
		}
	}
}
