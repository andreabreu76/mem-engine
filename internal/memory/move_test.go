package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
	"github.com/andreabreu76/mem-engine/internal/wiki"
)

var billingOld = project.Project{
	Key:  "/home/dev/src/billing-api-old",
	Name: "billing-api-old",
	Dir:  project.DirName("billing-api-old", "/home/dev/src/billing-api-old"),
}

func TestMoveProjectMovesFilesAndCommits(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	in := defaultInput()
	in.Tags = []string{"imported", "native:retry.md"}
	m, err := f.svc.Save(ctx, billingOld, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Save(ctx, billing, in); err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join(f.svc.WikiRoot, billingOld.Dir)
	dstDir := filepath.Join(f.svc.WikiRoot, billing.Dir)
	srcCommits := gitOut(t, srcDir, "rev-list", "--count", "HEAD")
	dstCommits := gitOut(t, dstDir, "rev-list", "--count", "HEAD")

	moved, err := f.svc.MoveProject(ctx, billingOld, billing)
	if err != nil {
		t.Fatalf("MoveProject: %v", err)
	}
	if moved != 1 {
		t.Errorf("moved = %d, want 1", moved)
	}
	if _, err := os.Stat(filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path))); !os.IsNotExist(err) {
		t.Errorf("source file still there: %v", err)
	}
	wantPath := billing.Dir + "/retry-with-exponential-backoff-2.md"
	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := m
	want.Project, want.Path = billing.Key, wantPath
	if !reflect.DeepEqual(got, want) {
		t.Errorf("index row = %+v, want %+v", got, want)
	}
	d := parseFile(t, filepath.Join(f.svc.WikiRoot, filepath.FromSlash(wantPath)))
	if d.ID != m.ID || d.Project != billing.Key || !reflect.DeepEqual(d.Tags, in.Tags) || !d.Created.Equal(m.Created) || !d.Updated.Equal(m.Updated) {
		t.Errorf("moved doc = %+v", d)
	}
	if n := gitOut(t, srcDir, "rev-list", "--count", "HEAD"); n != inc(t, srcCommits) {
		t.Errorf("source commits = %s, want %s + 1", n, srcCommits)
	}
	if n := gitOut(t, dstDir, "rev-list", "--count", "HEAD"); n != inc(t, dstCommits) {
		t.Errorf("destination commits = %s, want %s + 1", n, dstCommits)
	}
	for _, dir := range []string{srcDir, dstDir} {
		if st := gitOut(t, dir, "status", "--porcelain", "--", "*.md"); st != "" {
			t.Errorf("%s not clean: %q", dir, st)
		}
	}
	if left, err := f.svc.Store.ListMemories(ctx, billingOld.Key); err != nil || len(left) != 0 {
		t.Errorf("memories left in source = %v, %v", left, err)
	}

	again, err := f.svc.MoveProject(ctx, billingOld, billing)
	if err != nil || again != 0 {
		t.Errorf("second MoveProject = %d, %v; want 0, nil", again, err)
	}
	rep, err := f.svc.Reindex(ctx)
	if err != nil || len(rep.Skipped) != 0 || rep.Indexed != 2 {
		t.Errorf("Reindex after move = %+v, %v", rep, err)
	}
}

func TestMoveProjectResumesAfterPartialMove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first, err := f.svc.Save(ctx, billingOld, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	in := defaultInput()
	in.Title = "Deploy window"
	second, err := f.svc.Save(ctx, billingOld, in)
	if err != nil {
		t.Fatal(err)
	}
	dstDir := filepath.Join(f.svc.WikiRoot, billing.Dir)
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, m := range []store.Memory{first, second} {
		src := filepath.Join(f.svc.WikiRoot, filepath.FromSlash(m.Path))
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dstDir, filepath.Base(src)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(f.svc.WikiRoot, filepath.FromSlash(second.Path))); err != nil {
		t.Fatal(err)
	}

	moved, err := f.svc.MoveProject(ctx, billingOld, billing)
	if err != nil || moved != 2 {
		t.Fatalf("MoveProject = %d, %v", moved, err)
	}
	entries, err := os.ReadDir(dstDir)
	if err != nil {
		t.Fatal(err)
	}
	var md []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".md" {
			md = append(md, e.Name())
		}
	}
	if !reflect.DeepEqual(md, []string{"deploy-window.md", "retry-with-exponential-backoff.md"}) {
		t.Errorf("destination files = %v, want the existing copies reused", md)
	}
	for _, m := range []store.Memory{first, second} {
		got, err := f.svc.Store.GetMemory(ctx, m.ID)
		if err != nil || got.Path != billing.Dir+"/"+filepath.Base(m.Path) || got.Project != billing.Key {
			t.Errorf("index row = %+v, %v", got, err)
		}
		if d := parseFile(t, filepath.Join(dstDir, filepath.Base(m.Path))); d.Project != billing.Key {
			t.Errorf("%s project = %q", m.ID, d.Project)
		}
	}
	srcDir := filepath.Join(f.svc.WikiRoot, billingOld.Dir)
	if st := gitOut(t, srcDir, "status", "--porcelain", "--", "*.md"); st != "" {
		t.Errorf("source not clean: %q", st)
	}
}

func TestMoveProjectRejectsInvalidPairs(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	for _, c := range [][2]project.Project{{billing, billing}, {project.User, billing}, {billing, project.User}} {
		if _, err := f.svc.MoveProject(ctx, c[0], c[1]); err == nil {
			t.Errorf("MoveProject(%s, %s) succeeded, want error", c[0].Key, c[1].Key)
		}
	}
}

func inc(t *testing.T, n string) string {
	t.Helper()
	v, err := strconv.Atoi(n)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.Itoa(v + 1)
}

func TestMoveProjectRemovesSourceCopyAfterCrashAndReindex(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	from := project.Project{Key: "/home/dev/src/billing-api-old", Name: "billing-api-old", Dir: "zz-billing-api-old"}
	to := project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "aa-billing-api"}
	crashed, err := f.svc.Save(ctx, from, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	in := defaultInput()
	in.Title = "Deploy window"
	unindexed, err := f.svc.Save(ctx, from, in)
	if err != nil {
		t.Fatal(err)
	}

	srcDir := filepath.Join(f.svc.WikiRoot, from.Dir)
	name := filepath.Base(crashed.Path)
	doc := parseFile(t, filepath.Join(srcDir, name))
	doc.Project = to.Key
	dst, err := wiki.OpenRepo(f.svc.WikiRoot, to.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Write(name, wiki.Marshal(doc)); err != nil {
		t.Fatal(err)
	}
	if err := dst.Commit("mem: move "+crashed.ID, name); err != nil {
		t.Fatal(err)
	}
	rep, err := f.svc.Reindex(ctx)
	if err != nil || len(rep.Skipped) != 1 {
		t.Fatalf("Reindex after crash = %+v, %v; want the source copy skipped as duplicate", rep, err)
	}
	if err := f.svc.Store.DeleteMemory(ctx, unindexed.ID); err != nil {
		t.Fatal(err)
	}

	moved, err := f.svc.MoveProject(ctx, from, to)
	if err != nil || moved != 2 {
		t.Fatalf("MoveProject = %d, %v; want 2, nil", moved, err)
	}
	names, err := (&wiki.Repo{Dir: srcDir}).List()
	if err != nil || len(names) != 0 {
		t.Errorf("source files left = %v, %v", names, err)
	}
	if st := gitOut(t, srcDir, "status", "--porcelain", "--", "*.md"); st != "" {
		t.Errorf("source not clean: %q", st)
	}
	for _, id := range []string{crashed.ID, unindexed.ID} {
		got, err := f.svc.Store.GetMemory(ctx, id)
		if err != nil || got.Project != to.Key || filepath.Dir(got.Path) != to.Dir {
			t.Errorf("index row %s = %+v, %v", id, got, err)
		}
	}
	rep, err = f.svc.Reindex(ctx)
	if err != nil || len(rep.Skipped) != 0 || rep.Indexed != 2 {
		t.Errorf("Reindex after rerun = %+v, %v", rep, err)
	}
}

func TestMoveProjectFailsWhenSourceKeepsFiles(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if _, err := f.svc.Save(ctx, billingOld, defaultInput()); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(f.svc.WikiRoot, billingOld.Dir, "notes.md")
	if err := os.WriteFile(junk, []byte("no frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := f.svc.MoveProject(ctx, billingOld, billing)
	if err == nil || !strings.Contains(err.Error(), "notes.md") {
		t.Errorf("MoveProject = %d, %v; want an error naming notes.md", moved, err)
	}
	if _, statErr := os.Stat(junk); statErr != nil {
		t.Errorf("unparseable file touched: %v", statErr)
	}
}

func TestForgetRefusesMemoryMovedToAnotherProjectWhileWaitingForLock(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	m, err := f.svc.Save(ctx, billingOld, defaultInput())
	if err != nil {
		t.Fatal(err)
	}
	src, err := wiki.OpenRepo(f.svc.WikiRoot, billingOld.Dir)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := wiki.OpenRepo(f.svc.WikiRoot, billing.Dir)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := src.Lock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.svc.Forget(ctx, m.ID) }()
	time.Sleep(300 * time.Millisecond)

	name := filepath.Base(m.Path)
	doc := parseFile(t, filepath.Join(src.Dir, name))
	doc.Project = billing.Key
	if err := dst.Write(name, wiki.Marshal(doc)); err != nil {
		t.Fatal(err)
	}
	if err := dst.Commit("mem: move "+m.ID, name); err != nil {
		t.Fatal(err)
	}
	if err := src.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := src.Commit("mem: move "+m.ID, name); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Store.UpsertMemory(ctx, toMemory(doc, billing.Dir+"/"+name)); err != nil {
		t.Fatal(err)
	}
	unlock()

	if err := <-done; !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Forget = %v; want ErrNotFound for a memory moved to another project", err)
	}
	if _, err := os.Stat(filepath.Join(dst.Dir, name)); err != nil {
		t.Errorf("moved file removed by Forget: %v", err)
	}
	got, err := f.svc.Store.GetMemory(ctx, m.ID)
	if err != nil || got.Project != billing.Key {
		t.Errorf("index row = %+v, %v; want it kept in %s", got, err, billing.Key)
	}
}
