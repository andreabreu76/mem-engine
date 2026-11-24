package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"
)

const day = 24 * time.Hour

var gcNow = time.Unix(1700000000, 0)

func openGCTemp(t *testing.T) *Store {
	t.Helper()
	userProfile := t.TempDir()
	t.Setenv("HOME", userProfile)
	t.Setenv("USERPROFILE", userProfile)
	t.Setenv("MEM_ENGINE_HOME", t.TempDir())
	s, _ := openTemp(t)
	return s
}

func insertAt(t *testing.T, s *Store, body string, at time.Time) int64 {
	t.Helper()
	id, err := s.InsertObservation(context.Background(), Observation{SessionID: "s1", Project: "billing-api", Kind: "prompt", Title: "t", Body: body, CreatedAt: at})
	if err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	return id
}

func countWhere(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func lastGC(t *testing.T, s *Store) (string, bool) {
	t.Helper()
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = 'last_gc'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return v, true
}

func TestGCDeletesExpired(t *testing.T) {
	ctx := context.Background()
	s := openGCTemp(t)
	insertAt(t, s, "expiredword", gcNow.Add(-100*day))
	keep := insertAt(t, s, "freshword", gcNow.Add(-10*day))
	if err := s.RecordCaptureError(ctx, "PostToolUse", "old failure", gcNow.Add(-100*day)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCaptureError(ctx, "PostToolUse", "recent failure", gcNow.Add(-10*day)); err != nil {
		t.Fatal(err)
	}

	deleted, ran, err := s.GC(ctx, gcNow, 90*day, false)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if !ran || deleted != 1 {
		t.Fatalf("GC = (%d, %v), want (1, true)", deleted, ran)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations WHERE id = ?", keep); n != 1 {
		t.Errorf("fresh observation count = %d, want 1", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations"); n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'expiredword'"); n != 0 {
		t.Errorf("fts still finds the deleted observation (%d rows)", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'freshword'"); n != 1 {
		t.Errorf("fts fresh rows = %d, want 1", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM capture_errors"); n != 1 {
		t.Errorf("capture_errors = %d, want 1", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM capture_errors WHERE message = 'recent failure'"); n != 1 {
		t.Errorf("recent capture error was deleted")
	}
	if v, ok := lastGC(t, s); !ok || v != strconv.FormatInt(gcNow.Unix(), 10) {
		t.Errorf("last_gc = %q (%v), want %d", v, ok, gcNow.Unix())
	}
}

func TestGCOncePerDay(t *testing.T) {
	ctx := context.Background()
	s := openGCTemp(t)
	if _, ran, err := s.GC(ctx, gcNow, 90*day, false); err != nil || !ran {
		t.Fatalf("first GC ran=%v err=%v", ran, err)
	}
	insertAt(t, s, "old", gcNow.Add(-100*day))

	deleted, ran, err := s.GC(ctx, gcNow.Add(time.Hour), 90*day, false)
	if err != nil {
		t.Fatal(err)
	}
	if ran || deleted != 0 {
		t.Errorf("GC within 24h = (%d, %v), want (0, false)", deleted, ran)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations"); n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
	if v, _ := lastGC(t, s); v != strconv.FormatInt(gcNow.Unix(), 10) {
		t.Errorf("last_gc = %q, want unchanged", v)
	}

	later := gcNow.Add(25 * time.Hour)
	deleted, ran, err = s.GC(ctx, later, 90*day, false)
	if err != nil {
		t.Fatal(err)
	}
	if !ran || deleted != 1 {
		t.Errorf("GC after 25h = (%d, %v), want (1, true)", deleted, ran)
	}
	if v, _ := lastGC(t, s); v != strconv.FormatInt(later.Unix(), 10) {
		t.Errorf("last_gc = %q, want %d", v, later.Unix())
	}
}

func TestGCForce(t *testing.T) {
	ctx := context.Background()
	s := openGCTemp(t)
	if _, _, err := s.GC(ctx, gcNow, 90*day, false); err != nil {
		t.Fatal(err)
	}
	insertAt(t, s, "old", gcNow.Add(-100*day))

	at := gcNow.Add(time.Hour)
	deleted, ran, err := s.GC(ctx, at, 90*day, true)
	if err != nil {
		t.Fatal(err)
	}
	if !ran || deleted != 1 {
		t.Errorf("forced GC = (%d, %v), want (1, true)", deleted, ran)
	}
	if v, _ := lastGC(t, s); v != strconv.FormatInt(at.Unix(), 10) {
		t.Errorf("last_gc = %q, want %d", v, at.Unix())
	}
}

func TestGCRetentionDisabled(t *testing.T) {
	ctx := context.Background()
	for _, retention := range []time.Duration{0, -day} {
		s := openGCTemp(t)
		insertAt(t, s, "old", gcNow.Add(-1000*day))
		for _, force := range []bool{false, true} {
			deleted, ran, err := s.GC(ctx, gcNow, retention, force)
			if err != nil {
				t.Fatal(err)
			}
			if ran || deleted != 0 {
				t.Errorf("GC(retention=%v, force=%v) = (%d, %v), want (0, false)", retention, force, deleted, ran)
			}
		}
		if n := countWhere(t, s, "SELECT count(*) FROM observations"); n != 1 {
			t.Errorf("retention=%v: observations = %d, want 1", retention, n)
		}
		if v, ok := lastGC(t, s); ok {
			t.Errorf("retention=%v: last_gc = %q, want unset", retention, v)
		}
	}
}

func TestGCInvalidLastGCRuns(t *testing.T) {
	ctx := context.Background()
	s := openGCTemp(t)
	if _, err := s.db.Exec("INSERT INTO meta (key, value) VALUES ('last_gc', 'garbage')"); err != nil {
		t.Fatal(err)
	}
	insertAt(t, s, "old", gcNow.Add(-100*day))
	deleted, ran, err := s.GC(ctx, gcNow, 90*day, false)
	if err != nil {
		t.Fatal(err)
	}
	if !ran || deleted != 1 {
		t.Errorf("GC = (%d, %v), want (1, true)", deleted, ran)
	}
}

func TestGCLastRunInFutureIsDue(t *testing.T) {
	ctx := context.Background()
	s := openGCTemp(t)
	future := strconv.FormatInt(gcNow.Add(30*day).Unix(), 10)
	if _, err := s.db.Exec("INSERT INTO meta (key, value) VALUES ('last_gc', ?)", future); err != nil {
		t.Fatal(err)
	}
	insertAt(t, s, "old", gcNow.Add(-100*day))
	deleted, ran, err := s.GC(ctx, gcNow, 90*day, false)
	if err != nil {
		t.Fatal(err)
	}
	if !ran || deleted != 1 {
		t.Errorf("GC = (%d, %v), want (1, true)", deleted, ran)
	}
	if v, _ := lastGC(t, s); v != strconv.FormatInt(gcNow.Unix(), 10) {
		t.Errorf("last_gc = %q, want %d", v, gcNow.Unix())
	}
}
