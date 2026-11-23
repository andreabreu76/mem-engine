package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestInsertObservationSearchableByFTS(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	id, err := s.InsertObservation(ctx, Observation{
		SessionID: "s1",
		Project:   "billing-api",
		Kind:      "tool",
		Title:     "Edit config",
		Body:      "não usar retry infinito",
		ToolName:  "Edit",
		Files:     []string{"/home/dev/src/billing-api/config.go"},
		CreatedAt: time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}

	var rowid int64
	if err := s.db.QueryRowContext(ctx, "SELECT rowid FROM observations_fts WHERE observations_fts MATCH 'nao'").Scan(&rowid); err != nil {
		t.Fatalf("fts lookup: %v", err)
	}
	if rowid != id {
		t.Errorf("fts rowid = %d, want %d", rowid, id)
	}

	var files, toolName string
	var createdAt int64
	err = s.db.QueryRowContext(ctx, "SELECT files, tool_name, created_at FROM observations WHERE id = ?", id).Scan(&files, &toolName, &createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if files != `["/home/dev/src/billing-api/config.go"]` {
		t.Errorf("files = %s", files)
	}
	if toolName != "Edit" {
		t.Errorf("tool_name = %q, want Edit", toolName)
	}
	if createdAt != 1700000000 {
		t.Errorf("created_at = %d, want 1700000000", createdAt)
	}
}

func TestInsertObservationEmptyFiles(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	id, err := s.InsertObservation(ctx, Observation{SessionID: "s1", Project: "billing-api", Kind: "prompt", Title: "t", Body: "b", CreatedAt: time.Now()})
	if err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	var files string
	if err := s.db.QueryRowContext(ctx, "SELECT files FROM observations WHERE id = ?", id).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != "[]" {
		t.Errorf("files = %s, want []", files)
	}
}

func TestInsertObservationRejectsUnknownKind(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.InsertObservation(context.Background(), Observation{SessionID: "s1", Project: "billing-api", Kind: "edit", Title: "t", Body: "b", CreatedAt: time.Now()})
	if err == nil {
		t.Fatal("InsertObservation accepted unknown kind")
	}
}

func TestEnsureProjectIgnoresDuplicate(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	if err := s.EnsureProject(ctx, ProjectRow{Key: "k1", Name: "billing-api", Dir: "/home/dev/src/billing-api"}); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if err := s.EnsureProject(ctx, ProjectRow{Key: "k1", Name: "renamed", Dir: "/home/dev/src/other"}); err != nil {
		t.Fatalf("EnsureProject again: %v", err)
	}
	var name, dir string
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT name, dir, (SELECT count(*) FROM projects) FROM projects WHERE key = 'k1'").Scan(&name, &dir, &n); err != nil {
		t.Fatal(err)
	}
	if name != "billing-api" || dir != "/home/dev/src/billing-api" || n != 1 {
		t.Errorf("project = %q %q (rows %d), want first insert kept", name, dir, n)
	}
}

func TestTouchSessionUpsert(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	start := time.Unix(1700000000, 0)
	end := start.Add(time.Hour)

	if err := s.TouchSession(ctx, SessionEvent{SessionID: "s1", Project: "billing-api", Cwd: "/home/dev/src/billing-api", Source: "startup", At: start}); err != nil {
		t.Fatalf("TouchSession start: %v", err)
	}
	var endedAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT ended_at FROM sessions WHERE id = 's1'").Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if endedAt.Valid {
		t.Errorf("ended_at = %d after start, want NULL", endedAt.Int64)
	}

	if err := s.TouchSession(ctx, SessionEvent{SessionID: "s1", Project: "billing-api", Source: "resume", EndReason: "logout", At: end, End: true}); err != nil {
		t.Fatalf("TouchSession end: %v", err)
	}

	var source, cwd, reason string
	var startedAt int64
	err := s.db.QueryRowContext(ctx, "SELECT source, cwd, started_at, ended_at, end_reason FROM sessions WHERE id = 's1'").
		Scan(&source, &cwd, &startedAt, &endedAt, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if source != "startup" {
		t.Errorf("source = %q, want startup", source)
	}
	if cwd != "/home/dev/src/billing-api" {
		t.Errorf("cwd = %q", cwd)
	}
	if startedAt != start.Unix() {
		t.Errorf("started_at = %d, want %d", startedAt, start.Unix())
	}
	if !endedAt.Valid || endedAt.Int64 != end.Unix() {
		t.Errorf("ended_at = %v, want %d", endedAt, end.Unix())
	}
	if reason != "logout" {
		t.Errorf("end_reason = %q, want logout", reason)
	}
}

func TestTouchSessionFillsEmptySource(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	at := time.Unix(1700000000, 0)

	if err := s.TouchSession(ctx, SessionEvent{SessionID: "s1", Project: "billing-api", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, SessionEvent{SessionID: "s1", Project: "billing-api", Source: "compact", At: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	var source string
	if err := s.db.QueryRowContext(ctx, "SELECT source FROM sessions WHERE id = 's1'").Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "compact" {
		t.Errorf("source = %q, want compact", source)
	}
}

func TestCaptureErrorsSince(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	now := time.Unix(1700000000, 0)

	for _, e := range []struct {
		msg string
		at  time.Time
	}{
		{"old failure", now.Add(-2 * time.Hour)},
		{"disk full", now.Add(-30 * time.Minute)},
		{"database is locked", now.Add(-10 * time.Minute)},
	} {
		if err := s.RecordCaptureError(ctx, "PostToolUse", e.msg, e.at); err != nil {
			t.Fatalf("RecordCaptureError: %v", err)
		}
	}

	count, last, err := s.CaptureErrorsSince(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CaptureErrorsSince: %v", err)
	}
	if count != 2 || last != "database is locked" {
		t.Errorf("CaptureErrorsSince = %d %q, want 2 %q", count, last, "database is locked")
	}

	count, last, err = s.CaptureErrorsSince(ctx, now)
	if err != nil {
		t.Fatalf("CaptureErrorsSince: %v", err)
	}
	if count != 0 || last != "" {
		t.Errorf("CaptureErrorsSince(now) = %d %q, want 0 \"\"", count, last)
	}
}

func TestConcurrentInserts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mem.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { first.Close() })

	const workers, perWorker = 8, 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for w := range workers {
		wg.Go(func() {
			s, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			<-start
			for i := range perWorker {
				_, err := s.InsertObservation(ctx, Observation{
					SessionID: fmt.Sprintf("s%d", w),
					Project:   "billing-api",
					Kind:      "tool",
					Title:     fmt.Sprintf("worker %d insert %d", w, i),
					Body:      "concurrent body",
					CreatedAt: time.Now(),
				})
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	var rows, indexed int
	if err := first.db.QueryRowContext(ctx, "SELECT count(*) FROM observations").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRowContext(ctx, "SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'concurrent'").Scan(&indexed); err != nil {
		t.Fatal(err)
	}
	if rows != workers*perWorker || indexed != workers*perWorker {
		t.Errorf("rows = %d, indexed = %d, want %d", rows, indexed, workers*perWorker)
	}
}
