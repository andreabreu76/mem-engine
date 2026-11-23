package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	mergeFrom = "/home/dev/src/billing-api-old"
	mergeTo   = "/home/dev/src/billing-api"
)

func seedMergeProjects(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []ProjectRow{
		{Key: mergeFrom, Name: "billing-api-old", Dir: "billing-api-old-aaaaaa"},
		{Key: mergeTo, Name: "billing-api", Dir: "billing-api-bbbbbb"},
		{Key: "user", Name: "user", Dir: "user"},
	} {
		if err := s.EnsureProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMergeProjectMovesObservations(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	seedMergeProjects(t, s)
	start := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	seedSession(t, s, mergeFrom, "s-old", start,
		Observation{Kind: "prompt", Title: "retry", Body: "add retry to the invoice worker"},
		Observation{Kind: "stop", Title: "done", Body: "retry added"})
	seedSession(t, s, mergeTo, "s-new", start.Add(time.Hour),
		Observation{Kind: "prompt", Title: "deploy", Body: "deploy window"})

	m := sampleMemory("m-000000000001", "Retry with backoff")
	m.Project = mergeFrom
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	moved, err := s.MergeProject(ctx, mergeFrom, mergeTo)
	if err != nil {
		t.Fatalf("MergeProject: %v", err)
	}
	if moved != 2 {
		t.Errorf("moved = %d, want 2", moved)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations WHERE project = ?", mergeTo); n != 3 {
		t.Errorf("observations in %s = %d, want 3", mergeTo, n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations WHERE project = ?", mergeFrom); n != 0 {
		t.Errorf("observations left in %s = %d", mergeFrom, n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM sessions WHERE project = ?", mergeTo); n != 2 {
		t.Errorf("sessions in %s = %d, want 2", mergeTo, n)
	}
	hits, err := s.Search(ctx, SearchParams{Query: "invoice", Scope: CurrentScope(mergeTo)})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("search after merge = %+v, want the moved observation", hits)
	}
	if _, err := s.ProjectByKey(ctx, mergeFrom); err != nil {
		t.Errorf("project %s deleted while it still has memories: %v", mergeFrom, err)
	}

	m.Project = mergeTo
	if err := s.UpsertMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	moved, err = s.MergeProject(ctx, mergeFrom, mergeTo)
	if err != nil {
		t.Fatalf("second MergeProject: %v", err)
	}
	if moved != 0 {
		t.Errorf("second merge moved = %d, want 0", moved)
	}
	if _, err := s.ProjectByKey(ctx, mergeFrom); !errors.Is(err, ErrNotFound) {
		t.Errorf("project %s after merge: %v, want ErrNotFound", mergeFrom, err)
	}
}

func TestMergeProjectRejectsInvalidPairs(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	seedMergeProjects(t, s)
	cases := []struct{ from, to string }{
		{mergeTo, mergeTo},
		{"user", mergeTo},
		{mergeFrom, "user"},
		{mergeFrom, "/home/dev/src/unknown"},
		{"/home/dev/src/unknown", mergeTo},
	}
	for _, c := range cases {
		if _, err := s.MergeProject(ctx, c.from, c.to); err == nil {
			t.Errorf("MergeProject(%q, %q) succeeded, want error", c.from, c.to)
		}
	}
}
