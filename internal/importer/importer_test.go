package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

const nativeDir = "testdata/native"

var billing = project.Project{
	Key:  "/home/dev/src/billing-api",
	Name: "billing-api",
	Dir:  project.DirName("billing-api", "/home/dev/src/billing-api"),
}

type env struct {
	svc *memory.Service
	st  *store.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".mem-engine")
	t.Setenv("MEM_ENGINE_HOME", dataDir)
	gitConfig := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dataDir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return env{svc: &memory.Service{Store: st, WikiRoot: filepath.Join(dataDir, "wiki"), Now: time.Now}, st: st}
}

func (e env) run(t *testing.T, o Options) Report {
	t.Helper()
	rep, err := Run(context.Background(), e.svc, e.st, o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return rep
}

func (e env) byTitle(t *testing.T, key string) map[string]store.Memory {
	t.Helper()
	ms, err := e.st.ListMemories(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Memory{}
	for _, m := range ms {
		out[m.Title] = m
	}
	return out
}

func nativeBody(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(nativeDir, file))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(b), "---\n", 3)
	return strings.TrimPrefix(parts[2], "\n")
}

func TestImportPreservesTypeAndDescription(t *testing.T) {
	e := newEnv(t)
	e.run(t, Options{Dir: nativeDir, Target: billing})
	got := e.byTitle(t, billing.Key)
	want := map[string][2]string{
		"retry policy":      {"project", "Outbound calls retry three times with exponential backoff"},
		"deploy window":     {"project", "Production deploys run on weekdays between 10:00 and 16:00"},
		"editor preference": {"user", "Prefers gofmt defaults and two-space indentation in YAML"},
		"broken link":       {"feedback", "Points to a memory that was never written"},
	}
	if len(got) != len(want) {
		t.Fatalf("imported %d memories, want %d: %v", len(got), len(want), got)
	}
	for title, w := range want {
		m, ok := got[title]
		if !ok {
			t.Errorf("missing memory %q", title)
			continue
		}
		if m.Type != w[0] || m.Summary != w[1] {
			t.Errorf("%q: type %q summary %q, want %q %q", title, m.Type, m.Summary, w[0], w[1])
		}
		if !reflect.DeepEqual(m.Tags, []string{"imported"}) {
			t.Errorf("%q: tags %v", title, m.Tags)
		}
	}
	if b := got["deploy window"].Body; b != nativeBody(t, "deploy-window.md") {
		t.Errorf("deploy window body = %q", b)
	}
	if b := got["editor preference"].Body; b != nativeBody(t, "editor-preference.md") {
		t.Errorf("editor preference body = %q", b)
	}
}

func TestImportRewritesLinks(t *testing.T) {
	e := newEnv(t)
	e.run(t, Options{Dir: nativeDir, Target: billing})
	got := e.byTitle(t, billing.Key)
	deploy := got["deploy window"].ID
	want := strings.Replace(nativeBody(t, "retry-policy.md"), "during the [[deploy-window]]", "during the [["+deploy+"]]", 1)
	if b := got["retry policy"].Body; b != want {
		t.Errorf("retry policy body = %q, want %q", b, want)
	}
	if !strings.Contains(want, "[[deploy-window]] and [[not-a-link]] inside a fence") {
		t.Fatal("fixture lost the fenced link")
	}
	stored, err := os.ReadFile(filepath.Join(e.svc.WikiRoot, got["retry policy"].Path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(stored), want) {
		t.Errorf("wiki file not rewritten: %q", stored)
	}
}

func TestImportResolvesLinksToEarlierImports(t *testing.T) {
	e := newEnv(t)
	first := t.TempDir()
	copyFile(t, filepath.Join(nativeDir, "deploy-window.md"), filepath.Join(first, "deploy-window.md"))
	e.run(t, Options{Dir: first, Target: billing})
	deploy := e.byTitle(t, billing.Key)["deploy window"].ID

	rep := e.run(t, Options{Dir: nativeDir, Target: billing})
	if rep.Imported != 3 || rep.Skipped != 1 {
		t.Errorf("Imported %d Skipped %d, want 3 and 1", rep.Imported, rep.Skipped)
	}
	if b := e.byTitle(t, billing.Key)["retry policy"].Body; !strings.Contains(b, "during the [["+deploy+"]]") {
		t.Errorf("retry policy body = %q, want link to %s", b, deploy)
	}
}

func TestImportReportsUnresolved(t *testing.T) {
	e := newEnv(t)
	rep := e.run(t, Options{Dir: nativeDir, Target: billing})
	if want := []string{"[[missing]] in broken-link.md"}; !reflect.DeepEqual(rep.Unresolved, want) {
		t.Errorf("Unresolved = %q, want %q", rep.Unresolved, want)
	}
	if b := e.byTitle(t, billing.Key)["broken link"].Body; b != nativeBody(t, "broken-link.md") {
		t.Errorf("broken link body = %q", b)
	}
}

func TestImportSkipsMemoryIndex(t *testing.T) {
	e := newEnv(t)
	rep := e.run(t, Options{Dir: nativeDir, Target: billing})
	if rep.Imported != 4 || rep.Skipped != 0 || len(rep.Notes) != 0 {
		t.Errorf("report = %+v, want 4 imported, nothing skipped, no notes", rep)
	}
	for title := range e.byTitle(t, billing.Key) {
		if strings.Contains(strings.ToLower(title), "memory") {
			t.Errorf("index imported as %q", title)
		}
	}
}

func TestImportIsRerunnable(t *testing.T) {
	e := newEnv(t)
	e.run(t, Options{Dir: nativeDir, Target: billing})
	before := e.byTitle(t, billing.Key)
	rep := e.run(t, Options{Dir: nativeDir, Target: billing})
	if rep.Imported != 0 || rep.Skipped != 4 || len(rep.Unresolved) != 0 {
		t.Errorf("rerun report = %+v, want Imported 0 Skipped 4", rep)
	}
	if after := e.byTitle(t, billing.Key); !reflect.DeepEqual(after, before) {
		t.Errorf("rerun changed memories:\n%v\n%v", before, after)
	}
}

func TestImportTargetsUserScope(t *testing.T) {
	e := newEnv(t)
	e.run(t, Options{Dir: nativeDir, Target: billing})
	rep := e.run(t, Options{Dir: nativeDir, Target: project.User})
	if rep.Imported != 4 || rep.Skipped != 0 {
		t.Errorf("user import report = %+v, want 4 imported", rep)
	}
	if n := len(e.byTitle(t, project.User.Key)); n != 4 {
		t.Errorf("user memories = %d, want 4", n)
	}
}

func TestImportDoesNotTouchSource(t *testing.T) {
	e := newEnv(t)
	before := hashDir(t, nativeDir)
	e.run(t, Options{Dir: nativeDir, Target: billing})
	e.run(t, Options{Dir: nativeDir, Target: billing})
	e.run(t, Options{Dir: nativeDir, Target: project.User, DryRun: true})
	if after := hashDir(t, nativeDir); after != before {
		t.Errorf("source changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestImportDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	rep := e.run(t, Options{Dir: nativeDir, Target: billing, DryRun: true})
	if rep.Imported != 4 || rep.Skipped != 0 {
		t.Errorf("dry run report = %+v, want 4 to import", rep)
	}
	if want := []string{"broken link", "deploy window", "editor preference", "retry policy"}; !reflect.DeepEqual(rep.Titles, want) {
		t.Errorf("Titles = %q, want %q", rep.Titles, want)
	}
	if want := []string{"[[missing]] in broken-link.md"}; !reflect.DeepEqual(rep.Unresolved, want) {
		t.Errorf("Unresolved = %q, want %q", rep.Unresolved, want)
	}
	if _, err := os.Stat(e.svc.WikiRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wiki dir exists after dry run: %v", err)
	}
	if ms, err := e.st.ListMemories(context.Background(), billing.Key); err != nil || len(ms) != 0 {
		t.Errorf("memories after dry run = %v, %v", ms, err)
	}
	if _, err := e.st.ProjectByKey(context.Background(), billing.Key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("project row after dry run: %v", err)
	}
}

func TestImportDryRunWithoutStore(t *testing.T) {
	rep, err := Run(context.Background(), nil, nil, Options{Dir: nativeDir, Target: project.User, DryRun: true})
	if err != nil || rep.Imported != 4 {
		t.Errorf("Run = %+v, %v", rep, err)
	}
}

func TestImportSkipsInvalidFiles(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	files := map[string]string{
		"plain.md":       "No frontmatter here, see [[deploy-window]].\n",
		"nameless.md":    "---\ndescription: d\ntype: project\n---\n\nbody\n",
		"bad-type.md":    "---\nname: bad-type\ndescription: d\ntype: Project Notes\n---\n\nbody\n",
		"no-summary.md":  "---\nname: no-summary\ntype: project\n---\n\nbody\n",
		"broken-yaml.md": "---\nname: [\n---\n\nbody\n",
		"unclosed.md":    "---\nname: unclosed\n",
		"notes.txt":      "---\nname: notes\ndescription: d\ntype: project\n---\n",
		"valid.md":       "---\nname: valid\ndescription: d\ntype: reference\n---\n\nbody\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	rep := e.run(t, Options{Dir: dir, Target: billing})
	if rep.Imported != 1 || rep.Skipped != 6 {
		t.Errorf("Imported %d Skipped %d, want 1 and 6", rep.Imported, rep.Skipped)
	}
	if len(rep.Notes) != 6 {
		t.Fatalf("Notes = %q, want 6", rep.Notes)
	}
	for i, file := range []string{"bad-type.md", "broken-yaml.md", "nameless.md", "no-summary.md", "plain.md", "unclosed.md"} {
		if !strings.HasPrefix(rep.Notes[i], file+": skipped, ") {
			t.Errorf("Notes[%d] = %q, want note for %s", i, rep.Notes[i], file)
		}
	}
	if _, ok := e.byTitle(t, billing.Key)["valid"]; !ok {
		t.Error("valid file not imported")
	}
}

func TestImportSkipsDuplicateNames(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	for _, name := range []string{"a.md", "b.md"} {
		content := "---\nname: same-name\ndescription: from " + name + "\ntype: project\n---\n\nbody\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rep := e.run(t, Options{Dir: dir, Target: billing})
	if rep.Imported != 1 || rep.Skipped != 1 || len(rep.Notes) != 1 || !strings.HasPrefix(rep.Notes[0], "b.md: skipped, ") {
		t.Errorf("report = %+v", rep)
	}
	if m := e.byTitle(t, billing.Key)["same name"]; m.Summary != "from a.md" {
		t.Errorf("kept %q, want the first file", m.Summary)
	}
}

func TestImportMissingDir(t *testing.T) {
	e := newEnv(t)
	if _, err := Run(context.Background(), e.svc, e.st, Options{Dir: filepath.Join(t.TempDir(), "absent"), Target: billing}); err == nil {
		t.Error("Run on a missing dir succeeded")
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func hashDir(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %v %d\n", path, info.Mode(), info.ModTime().UnixNano())
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
