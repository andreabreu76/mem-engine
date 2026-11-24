package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func openFeedTemp(t *testing.T) *Store {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	return openSearchTemp(t)
}

func TestFeedReturnsNewerObservationsAscending(t *testing.T) {
	ctx := context.Background()
	s := openFeedTemp(t)
	first := addObservation(t, s, "s1", "billing-api", "first", "body one", searchNow.Add(-3*day))
	second := addObservation(t, s, "s1", "billing-api", "second", "body two\nmore", searchNow.Add(-2*day))
	other := addObservation(t, s, "s2", "ledger", "other", "body other", searchNow.Add(-2*day))
	user := addObservation(t, s, "s3", "user", "user wide", "body user", searchNow.Add(-1*day))
	addMemory(t, s, "m-000000000001", "billing-api", "memory", "body", "s1", searchNow)

	none, err := s.Feed(ctx, 0, nil, 10)
	if err != nil || len(none) != 0 {
		t.Fatalf("Feed without projects = %v, %v, want no rows", hitIDs(none), err)
	}
	every := []string{"billing-api", "ledger", "user"}
	all, err := s.Feed(ctx, 0, every, 10)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if got, want := hitIDs(all), []string{first, second, other, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed all = %v, want %v", got, want)
	}
	if all[1].Kind != kindObservation || all[1].Type != "tool" || all[1].Title != "second" || all[1].Summary != "body two" ||
		all[1].Project != "billing-api" || all[1].SessionID != "s1" || !all[1].At.Equal(searchNow.Add(-2*day)) {
		t.Fatalf("Feed hit = %+v", all[1])
	}

	firstID, _ := strconv.ParseInt(first[2:], 10, 64)
	scoped, err := s.Feed(ctx, firstID, CurrentScope("billing-api").Projects, 10)
	if err != nil {
		t.Fatalf("Feed scoped: %v", err)
	}
	if got, want := hitIDs(scoped), []string{second, user}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed scoped = %v, want %v", got, want)
	}

	limited, err := s.Feed(ctx, 0, every, 2)
	if err != nil {
		t.Fatalf("Feed limited: %v", err)
	}
	if got, want := hitIDs(limited), []string{first, second}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed limited = %v, want %v", got, want)
	}
}

func TestDeleteObservationRemovesFromFTS(t *testing.T) {
	ctx := context.Background()
	s := openFeedTemp(t)
	id := addObservation(t, s, "s1", "billing-api", "doomed", "uniquedoomedword", searchNow)
	rowid, _ := strconv.ParseInt(id[2:], 10, 64)

	if err := s.DeleteObservation(ctx, rowid); err != nil {
		t.Fatalf("DeleteObservation: %v", err)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations WHERE id = ?", rowid); n != 0 {
		t.Fatalf("observation rows = %d, want 0", n)
	}
	if n := countWhere(t, s, "SELECT count(*) FROM observations_fts WHERE observations_fts MATCH 'uniquedoomedword'"); n != 0 {
		t.Fatalf("fts rows = %d, want 0", n)
	}
	if err := s.DeleteObservation(ctx, rowid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteObservation = %v, want ErrNotFound", err)
	}
}

func TestListProjectsSortedByName(t *testing.T) {
	ctx := context.Background()
	s := openFeedTemp(t)
	for _, p := range []ProjectRow{
		{Key: "/home/dev/src/ledger", Name: "ledger", Dir: "ledger-bbbbbb"},
		{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-aaaaaa"},
	} {
		if err := s.EnsureProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	want := []ProjectRow{
		{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-aaaaaa"},
		{Key: "/home/dev/src/ledger", Name: "ledger", Dir: "ledger-bbbbbb"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListProjects = %+v, want %+v", got, want)
	}
}
