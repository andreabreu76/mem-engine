package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleMemory(id, title string) Memory {
	at := time.Date(2026, 11, 23, 10, 30, 0, 0, time.UTC)
	return Memory{
		ID:            id,
		Project:       "/home/dev/src/billing-api",
		Type:          "decision",
		Title:         title,
		Summary:       "summary of " + title,
		Body:          "body of " + title,
		Tags:          []string{"pinned", "retry"},
		Path:          "billing-api-abc123/" + strings.ToLower(id) + ".md",
		SourceSession: "s1",
		Created:       at,
		Updated:       at.Add(time.Hour),
	}
}

func ftsIDs(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, err := s.db.Query("SELECT m.id FROM memories_fts JOIN memories m ON m.rowid = memories_fts.rowid WHERE memories_fts MATCH ? ORDER BY m.id", query)
	if err != nil {
		t.Fatalf("fts query %q: %v", query, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func checkFTSIntegrity(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec("INSERT INTO memories_fts(memories_fts, rank) VALUES('integrity-check', 1)"); err != nil {
		t.Fatalf("fts integrity-check: %v", err)
	}
}

func TestUpsertAndGetMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	m := sampleMemory("m-000000000001", "Retry with backoff")
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatalf("UpsertMemory: %v", err)
	}
	got, err := s.GetMemory(ctx, m.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Errorf("GetMemory = %+v, want %+v", got, m)
	}
	var tags string
	if err := s.db.QueryRow("SELECT tags FROM memories WHERE id = ?", m.ID).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	if tags != `["pinned","retry"]` {
		t.Errorf("tags column = %q", tags)
	}
}

func TestUpsertMemoryNilTags(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	m := sampleMemory("m-000000000001", "No tags")
	m.Tags = nil
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMemory(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Errorf("Tags = %#v, want empty non-nil", got.Tags)
	}
}

func TestUpsertMemoryUpdatesInPlaceAndKeepsFTSInSync(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := sampleMemory("m-00000000000a", "Retry with backoff")
	b := sampleMemory("m-00000000000b", "Cache invalidation")
	for _, m := range []Memory{a, b} {
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	var rowidBefore int64
	if err := s.db.QueryRow("SELECT rowid FROM memories WHERE id = ?", a.ID).Scan(&rowidBefore); err != nil {
		t.Fatal(err)
	}
	a.Title = "Circuit breaker"
	a.Summary = "breaker summary"
	a.Body = "breaker body"
	a.Tags = []string{"resilience"}
	if err := s.UpsertMemory(ctx, a); err != nil {
		t.Fatalf("UpsertMemory update: %v", err)
	}
	var rowidAfter int64
	if err := s.db.QueryRow("SELECT rowid FROM memories WHERE id = ?", a.ID).Scan(&rowidAfter); err != nil {
		t.Fatal(err)
	}
	if rowidAfter != rowidBefore {
		t.Errorf("rowid changed %d -> %d", rowidBefore, rowidAfter)
	}
	if ids := ftsIDs(t, s, "backoff"); len(ids) != 0 {
		t.Errorf("old title still indexed: %v", ids)
	}
	if ids := ftsIDs(t, s, "breaker"); !reflect.DeepEqual(ids, []string{a.ID}) {
		t.Errorf("breaker -> %v", ids)
	}
	if ids := ftsIDs(t, s, "resilience"); !reflect.DeepEqual(ids, []string{a.ID}) {
		t.Errorf("resilience -> %v", ids)
	}
	if ids := ftsIDs(t, s, "cache"); !reflect.DeepEqual(ids, []string{b.ID}) {
		t.Errorf("cache -> %v", ids)
	}
	checkFTSIntegrity(t, s)
}

func TestGetMemoryNotFound(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.GetMemory(context.Background(), "m-missing")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	m := sampleMemory("m-000000000001", "Retry with backoff")
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMemory(ctx, m.ID); err != nil {
		t.Fatalf("DeleteMemory: %v", err)
	}
	if _, err := s.GetMemory(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMemory after delete err = %v", err)
	}
	if ids := ftsIDs(t, s, "backoff"); len(ids) != 0 {
		t.Errorf("deleted memory still indexed: %v", ids)
	}
	if err := s.DeleteMemory(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second DeleteMemory err = %v, want ErrNotFound", err)
	}
	checkFTSIntegrity(t, s)
}

func TestReplaceAllMemories(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, m := range []Memory{sampleMemory("m-00000000000a", "Old alpha"), sampleMemory("m-00000000000b", "Old beta")} {
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO memories_fts(memories_fts) VALUES('delete-all')"); err != nil {
		t.Fatal(err)
	}
	next := []Memory{sampleMemory("m-00000000000b", "New beta"), sampleMemory("m-00000000000c", "New gamma")}
	if err := s.ReplaceAllMemories(ctx, next); err != nil {
		t.Fatalf("ReplaceAllMemories: %v", err)
	}
	if _, err := s.GetMemory(ctx, "m-00000000000a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("m-a survived: %v", err)
	}
	for _, want := range next {
		got, err := s.GetMemory(ctx, want.ID)
		if err != nil {
			t.Fatalf("GetMemory %s: %v", want.ID, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("GetMemory %s = %+v, want %+v", want.ID, got, want)
		}
	}
	if ids := ftsIDs(t, s, "old"); len(ids) != 0 {
		t.Errorf("old -> %v", ids)
	}
	if ids := ftsIDs(t, s, "new"); !reflect.DeepEqual(ids, []string{"m-00000000000b", "m-00000000000c"}) {
		t.Errorf("new -> %v", ids)
	}
	checkFTSIntegrity(t, s)
}

func TestReplaceAllMemoriesRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	keep := sampleMemory("m-00000000000a", "Keep me")
	if err := s.UpsertMemory(ctx, keep); err != nil {
		t.Fatal(err)
	}
	dup := sampleMemory("m-00000000000b", "Dup")
	if err := s.ReplaceAllMemories(ctx, []Memory{dup, dup}); err == nil {
		t.Fatal("ReplaceAllMemories with duplicate ids succeeded")
	}
	if _, err := s.GetMemory(ctx, keep.ID); err != nil {
		t.Errorf("original memory lost after failed replace: %v", err)
	}
}

func TestProjectByKey(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	p := ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"}
	if err := s.EnsureProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectByKey(ctx, p.Key)
	if err != nil {
		t.Fatalf("ProjectByKey: %v", err)
	}
	if got != p {
		t.Errorf("ProjectByKey = %+v, want %+v", got, p)
	}
	if _, err := s.ProjectByKey(ctx, "billing-api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ProjectByKey by name err = %v, want ErrNotFound", err)
	}
}

func TestFindProject(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	rows := []ProjectRow{
		{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"},
		{Key: "/home/dev/src/acme/web", Name: "web", Dir: "web-111111"},
		{Key: "/home/dev/src/other/web", Name: "web", Dir: "web-222222"},
		{Key: "/home/dev/src/tricky", Name: "/home/dev/src/billing-api", Dir: "tricky-333333"},
	}
	for _, r := range rows {
		if err := s.EnsureProject(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.FindProject(ctx, "billing-api"); err != nil || got != rows[0] {
		t.Errorf("FindProject(name) = %+v, %v", got, err)
	}
	if got, err := s.FindProject(ctx, "/home/dev/src/billing-api"); err != nil || got != rows[0] {
		t.Errorf("FindProject(key) = %+v, %v; key must win over name", got, err)
	}
	if got, err := s.FindProject(ctx, "/home/dev/src/acme/web"); err != nil || got != rows[1] {
		t.Errorf("FindProject(ambiguous name by key) = %+v, %v", got, err)
	}
	_, err := s.FindProject(ctx, "web")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("FindProject(ambiguous) err = %v", err)
	}
	for _, key := range []string{rows[1].Key, rows[2].Key} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("ambiguity error %q does not list %s", err, key)
		}
	}
	if _, err := s.FindProject(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindProject(missing) err = %v, want ErrNotFound", err)
	}
}

func TestListMemories(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	late := sampleMemory("m-00000000000a", "Late")
	late.Created = late.Created.Add(time.Hour)
	tieB := sampleMemory("m-00000000000c", "Tie c")
	tieA := sampleMemory("m-00000000000b", "Tie b")
	other := sampleMemory("m-00000000000d", "Other project")
	other.Project = "/home/dev/src/acme/web"
	for _, m := range []Memory{late, tieB, tieA, other} {
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListMemories(ctx, "/home/dev/src/billing-api")
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if want := []Memory{tieA, tieB, late}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListMemories = %+v, want %+v", got, want)
	}
	empty, err := s.ListMemories(ctx, "user")
	if err != nil || len(empty) != 0 {
		t.Errorf("ListMemories(user) = %+v, %v", empty, err)
	}
}
