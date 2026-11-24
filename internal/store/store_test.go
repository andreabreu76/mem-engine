package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestOpenCreatesSchema(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	v, err := s.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != 1 {
		t.Errorf("Version = %d, want 1", v)
	}
	if err := s.CheckFTS5(ctx); err != nil {
		t.Errorf("CheckFTS5: %v", err)
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mem.db")
	for i := range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open #%d: %v", i+1, err)
		}
		v, err := s.Version(ctx)
		if err != nil {
			t.Fatalf("Version #%d: %v", i+1, err)
		}
		if v != SchemaVersion {
			t.Errorf("Version #%d = %d, want %d", i+1, v, SchemaVersion)
		}
		var rows int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_version").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Errorf("schema_version rows #%d = %d, want 1", i+1, rows)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close #%d: %v", i+1, err)
		}
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.db.Exec("UPDATE schema_version SET version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err == nil {
		again.Close()
		t.Fatal("Open succeeded on a newer schema")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestConcurrentOpen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mem.db")
	const n = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, n)
	for range n {
		wg.Go(func() {
			<-start
			s, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			if _, err := s.Version(ctx); err != nil {
				errs <- err
			}
			if err := s.Close(); err != nil {
				errs <- err
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestFTSTriggersFollowWrites(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	count := func(table, query string) int {
		t.Helper()
		var c int
		q := "SELECT count(*) FROM " + table + " WHERE " + table + " MATCH ?"
		if err := s.db.QueryRowContext(ctx, q, query).Scan(&c); err != nil {
			t.Fatalf("%s MATCH %q: %v", table, query, err)
		}
		return c
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	integrity := func(table string) {
		t.Helper()
		exec("INSERT INTO " + table + "(" + table + ") VALUES ('integrity-check')")
	}

	exec(`INSERT INTO observations (session_id, project, kind, title, body, created_at)
		VALUES ('s1', 'billing-api', 'edit', 'Configuração inicial', 'retry backoff', 1)`)
	if got := count("observations_fts", "configuracao"); got != 1 {
		t.Errorf("observation insert: matches = %d, want 1", got)
	}
	exec("UPDATE observations SET body = 'circuit breaker' WHERE session_id = 's1'")
	if got := count("observations_fts", "backoff"); got != 0 {
		t.Errorf("observation update: old term matches = %d, want 0", got)
	}
	if got := count("observations_fts", "breaker"); got != 1 {
		t.Errorf("observation update: new term matches = %d, want 1", got)
	}
	integrity("observations_fts")
	exec("DELETE FROM observations")
	if got := count("observations_fts", "breaker"); got != 0 {
		t.Errorf("observation delete: matches = %d, want 0", got)
	}
	integrity("observations_fts")

	upsert := `INSERT INTO memories (id, project, type, title, summary, body, tags, path, created_at, updated_at)
		VALUES (?, 'billing-api', 'decision', ?, 'summary', ?, '["db"]', 'wiki/m.md', 1, ?)
		ON CONFLICT(id) DO UPDATE SET title = excluded.title, body = excluded.body, updated_at = excluded.updated_at`
	exec(upsert, "m1", "Use WAL", "concurrent readers", 1)
	exec(upsert, "m2", "Other", "unrelated", 1)
	if got := count("memories_fts", "readers"); got != 1 {
		t.Errorf("memory insert: matches = %d, want 1", got)
	}
	exec(upsert, "m1", "Use WAL mode", "single writer", 2)
	if got := count("memories_fts", "readers"); got != 0 {
		t.Errorf("memory upsert: old term matches = %d, want 0", got)
	}
	if got := count("memories_fts", "writer"); got != 1 {
		t.Errorf("memory upsert: new term matches = %d, want 1", got)
	}
	integrity("memories_fts")
	exec("DELETE FROM memories WHERE id = 'm1'")
	if got := count("memories_fts", "writer OR wal"); got != 0 {
		t.Errorf("memory delete: matches = %d, want 0", got)
	}
	if got := count("memories_fts", "unrelated"); got != 1 {
		t.Errorf("memory delete: remaining matches = %d, want 1", got)
	}
	integrity("memories_fts")
}

func TestOpenPathWithURIMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a#b%20c")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mem.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database not created at %s: %v", path, err)
	}
}
