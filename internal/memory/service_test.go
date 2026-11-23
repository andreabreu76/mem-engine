package memory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
	"github.com/andreabreu76/mem-engine/internal/wiki"
)

var billing = project.Project{
	Key:  "/home/dev/src/billing-api",
	Name: "billing-api",
	Dir:  project.DirName("billing-api", "/home/dev/src/billing-api"),
}

type fixture struct {
	svc    *Service
	dbPath string
	clock  *time.Time
}

func newFixture(t *testing.T) *fixture {
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
	f := &fixture{dbPath: filepath.Join(dataDir, "mem.db")}
	clock := time.Date(2026, 11, 23, 10, 30, 0, 123456789, time.UTC)
	f.clock = &clock
	f.svc = &Service{WikiRoot: filepath.Join(dataDir, "wiki"), Now: func() time.Time { return *f.clock }}
	f.open(t)
	return f
}

func (f *fixture) open(t *testing.T) {
	t.Helper()
	st, err := store.Open(f.dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	f.svc.Store = st
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func parseFile(t *testing.T, path string) wiki.Doc {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	d, err := wiki.Parse(b)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return d
}

func defaultInput() Input {
	return Input{
		Type:          "decision",
		Title:         "Retry with exponential backoff",
		Summary:       "Billing retries use exponential backoff",
		Body:          "Start at 1s, cap at 5 minutes.\n",
		Tags:          []string{"pinned", "retry"},
		SourceSession: "s1",
	}
}

func TestSaveWritesFileCommitAndIndex(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m, err := f.svc.Save(ctx, billing, defaultInput())
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !regexp.MustCompile(`^m-[0-9a-f]{12}$`).MatchString(m.ID) {
		t.Errorf("ID = %q", m.ID)
	}
	want := time.Date(2026, 11, 23, 10, 30, 0, 0, time.UTC)
	wantPath := billing.Dir + "/retry-with-exponential-backoff.md"
	if m.Path != wantPath {
		t.Errorf("Path = %q, want %q", m.Path, wantPath)
	}
	if m.Project != billing.Key || m.Type != "decision" || m.SourceSession != "s1" || !m.Created.Equal(want) || !m.Updated.Equal(want) {
		t.Errorf("memory = %+v", m)
	}

	dir := filepath.Join(f.svc.WikiRoot, billing.Dir)
	d := parseFile(t, filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path)))
	wantDoc := wiki.Doc{
		ID: m.ID, Type: "decision", Title: "Retry with exponential backoff",
		Summary: "Billing retries use exponential backoff", Project: billing.Key,
		Tags: []string{"pinned", "retry"}, Created: want, Updated: want,
		SourceSession: "s1", Body: "Start at 1s, cap at 5 minutes.\n",
	}
	if !reflect.DeepEqual(d, wantDoc) {
		t.Errorf("file doc = %+v, want %+v", d, wantDoc)
	}
	if got := gitOut(t, dir, "log", "--format=%s"); got != "mem: save "+m.ID+" Retry with exponential backoff" {
		t.Errorf("git log = %q", got)
	}
	if got := gitOut(t, dir, "status", "--porcelain"); got != ".gitignore" && got != "?? .gitignore" {
		t.Errorf("git status = %q", got)
	}

	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("index = %+v, want %+v", got, m)
	}
	p, err := f.svc.Store.ProjectByKey(ctx, billing.Key)
	if err != nil || p != (store.ProjectRow{Key: billing.Key, Name: billing.Name, Dir: billing.Dir}) {
		t.Errorf("ProjectByKey = %+v, %v", p, err)
	}

	again, err := f.svc.Save(ctx, billing, defaultInput())
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if again.ID == m.ID || again.Path != billing.Dir+"/retry-with-exponential-backoff-2.md" {
		t.Errorf("second save = %+v", again)
	}
}

func TestSaveValidatesType(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	cases := map[string]func(*Input){
		"empty type":     func(in *Input) { in.Type = "" },
		"upper type":     func(in *Input) { in.Type = "Decision" },
		"leading digit":  func(in *Input) { in.Type = "1fact" },
		"space in type":  func(in *Input) { in.Type = "a fact" },
		"long type":      func(in *Input) { in.Type = "a" + strings.Repeat("b", 32) },
		"empty title":    func(in *Input) { in.Title = " " },
		"empty summary":  func(in *Input) { in.Summary = "" },
		"underscore":     func(in *Input) { in.Type = "my_type" },
		"trailing space": func(in *Input) { in.Type = "fact " },
	}
	for name, mutate := range cases {
		in := defaultInput()
		mutate(&in)
		if _, err := f.svc.Save(ctx, billing, in); err == nil {
			t.Errorf("%s: Save succeeded", name)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(f.svc.WikiRoot, billing.Dir))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			t.Errorf("invalid save left file %s", e.Name())
		}
	}
	for _, typ := range []string{"fact", "gotcha", "my-type-2", "a" + strings.Repeat("b", 31)} {
		in := defaultInput()
		in.Type = typ
		if _, err := f.svc.Save(ctx, billing, in); err != nil {
			t.Errorf("type %q rejected: %v", typ, err)
		}
	}
}

func TestUpdateChangesFileAndIndex(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m, err := f.svc.Save(ctx, billing, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	*f.clock = f.clock.Add(2 * time.Hour)
	title := "Retry with jitter"
	tags := []string{"resilience"}
	u, err := f.svc.Update(ctx, m.ID, Patch{Title: &title, Tags: &tags})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if u.Path != m.Path {
		t.Errorf("Path changed %q -> %q", m.Path, u.Path)
	}
	if u.Title != title || !reflect.DeepEqual(u.Tags, tags) || u.Summary != m.Summary || u.Body != m.Body || u.Type != m.Type {
		t.Errorf("updated = %+v", u)
	}
	if !u.Created.Equal(m.Created) || !u.Updated.Equal(time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("Created = %v, Updated = %v", u.Created, u.Updated)
	}
	d := parseFile(t, filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path)))
	if d.Title != title || !reflect.DeepEqual(d.Tags, tags) || !d.Updated.Equal(u.Updated) || !d.Created.Equal(m.Created) {
		t.Errorf("file doc = %+v", d)
	}
	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil || !reflect.DeepEqual(got, u) {
		t.Errorf("index = %+v, %v; want %+v", got, err, u)
	}
	dir := filepath.Join(f.svc.WikiRoot, billing.Dir)
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "mem: update "+m.ID+" "+title {
		t.Errorf("last commit = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	var mds []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") {
			mds = append(mds, e.Name())
		}
	}
	if !reflect.DeepEqual(mds, []string{"retry-with-exponential-backoff.md"}) {
		t.Errorf("files = %v", mds)
	}

	same, err := f.svc.Update(ctx, m.ID, Patch{})
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if !reflect.DeepEqual(same, u) {
		t.Errorf("no-op update = %+v, want %+v", same, u)
	}

	bad := "Not Valid"
	if _, err := f.svc.Update(ctx, m.ID, Patch{Type: &bad}); err == nil {
		t.Error("Update with invalid type succeeded")
	}
	if _, err := f.svc.Update(ctx, "m-000000000000", Patch{Title: &title}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Update unknown err = %v, want ErrNotFound", err)
	}
}

func TestForgetRemovesFileAndIndex(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m, err := f.svc.Save(ctx, billing, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Forget(ctx, m.ID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still there: %v", err)
	}
	if _, err := f.svc.Store.GetMemory(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("index still has memory: %v", err)
	}
	dir := filepath.Join(f.svc.WikiRoot, billing.Dir)
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "mem: forget "+m.ID+" "+m.Title {
		t.Errorf("last commit = %q", got)
	}
	if got := gitOut(t, dir, "ls-files"); got != "" {
		t.Errorf("tracked files = %q", got)
	}
	if err := f.svc.Forget(ctx, m.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second Forget err = %v, want ErrNotFound", err)
	}
}

func TestReindexRebuildsFromWiki(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var saved []store.Memory
	for i, title := range []string{"Retry with backoff", "Cache invalidation"} {
		in := defaultInput()
		in.Title = title
		m, err := f.svc.Save(ctx, billing, in)
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, m)
		*f.clock = f.clock.Add(time.Duration(i+1) * time.Minute)
	}
	userIn := defaultInput()
	userIn.Type = "preference"
	userIn.Title = "Prefer table-driven tests"
	userIn.Tags = nil
	um, err := f.svc.Save(ctx, project.User, userIn)
	if err != nil {
		t.Fatal(err)
	}
	saved = append(saved, um)

	f.svc.Store.Close()
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(f.dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	f.open(t)
	if _, err := f.svc.Store.GetMemory(ctx, saved[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fresh db already has memory: %v", err)
	}

	rep, err := f.svc.Reindex(ctx)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if rep.Indexed != 3 || len(rep.Skipped) != 0 {
		t.Errorf("report = %+v", rep)
	}
	for _, want := range saved {
		got, err := f.svc.Store.GetMemory(ctx, want.ID)
		if err != nil {
			t.Fatalf("GetMemory %s: %v", want.ID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("reindexed %+v, want %+v", got, want)
		}
	}
	p, err := f.svc.Store.FindProject(ctx, "billing-api")
	if err != nil || p != (store.ProjectRow{Key: billing.Key, Name: billing.Name, Dir: billing.Dir}) {
		t.Errorf("FindProject(billing-api) = %+v, %v", p, err)
	}
	u, err := f.svc.Store.ProjectByKey(ctx, "user")
	if err != nil || u != (store.ProjectRow{Key: "user", Name: "user", Dir: "user"}) {
		t.Errorf("ProjectByKey(user) = %+v, %v", u, err)
	}

	if err := f.svc.Store.UpsertMemory(ctx, store.Memory{ID: "m-stale0000000", Project: billing.Key, Type: "fact", Title: "stale", Summary: "s", Path: billing.Dir + "/stale.md"}); err != nil {
		t.Fatal(err)
	}
	if rep, err := f.svc.Reindex(ctx); err != nil || rep.Indexed != 3 {
		t.Fatalf("second Reindex = %+v, %v", rep, err)
	}
	if _, err := f.svc.Store.GetMemory(ctx, "m-stale0000000"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("stale row survived reindex: %v", err)
	}
}

func TestReindexSkipsBrokenFrontmatter(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	for _, title := range []string{"First", "Second"} {
		in := defaultInput()
		in.Title = title
		if _, err := f.svc.Save(ctx, billing, in); err != nil {
			t.Fatal(err)
		}
	}
	broken := filepath.Join(f.svc.WikiRoot, billing.Dir, "broken.md")
	if err := os.WriteFile(broken, []byte("---\nid: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.svc.WikiRoot, billing.Dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.svc.WikiRoot, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.svc.WikiRoot, ".hidden", "x.md"), []byte("---\nid: [\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := f.svc.Reindex(ctx)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if rep.Indexed != 2 {
		t.Errorf("Indexed = %d, want 2", rep.Indexed)
	}
	if len(rep.Skipped) != 1 || !strings.HasPrefix(rep.Skipped[0], billing.Dir+"/broken.md: ") {
		t.Errorf("Skipped = %q", rep.Skipped)
	}
}

func TestReindexSkipsDuplicateID(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m, err := f.svc.Save(ctx, billing, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path))
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.svc.WikiRoot, billing.Dir, "zz-copy.md"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := f.svc.Reindex(ctx)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if rep.Indexed != 1 || len(rep.Skipped) != 1 || !strings.HasPrefix(rep.Skipped[0], billing.Dir+"/zz-copy.md: ") {
		t.Errorf("report = %+v", rep)
	}
	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil || got.Path != m.Path {
		t.Errorf("GetMemory = %+v, %v", got, err)
	}
}

func TestReindexEmptyWiki(t *testing.T) {
	f := newFixture(t)
	rep, err := f.svc.Reindex(context.Background())
	if err != nil || rep.Indexed != 0 || len(rep.Skipped) != 0 {
		t.Errorf("Reindex = %+v, %v", rep, err)
	}
}

func TestUserScope(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	in := defaultInput()
	in.Type = "preference"
	in.Title = "Prefer table-driven tests"
	m, err := f.svc.Save(ctx, project.User, in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if m.Project != "user" || m.Path != "user/prefer-table-driven-tests.md" {
		t.Errorf("memory = %+v", m)
	}
	path := filepath.Join(f.svc.WikiRoot, "user", "prefer-table-driven-tests.md")
	if d := parseFile(t, path); d.Project != "user" || d.ID != m.ID {
		t.Errorf("doc = %+v", d)
	}
	if got := gitOut(t, filepath.Join(f.svc.WikiRoot, "user"), "log", "--format=%s"); got != "mem: save "+m.ID+" Prefer table-driven tests" {
		t.Errorf("git log = %q", got)
	}
}

func TestSaveTitleWithoutSlug(t *testing.T) {
	f := newFixture(t)
	in := defaultInput()
	in.Title = "日本語"
	m, err := f.svc.Save(context.Background(), billing, in)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if m.Path != billing.Dir+"/"+m.ID+".md" {
		t.Errorf("Path = %q", m.Path)
	}
}
