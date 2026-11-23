package store

import (
	"context"
	"reflect"
	"testing"
	"time"
)

var sessionBase = time.Date(2026, 11, 20, 14, 2, 0, 0, time.UTC)

func seedSession(t *testing.T, s *Store, project, id string, start time.Time, obs ...Observation) {
	t.Helper()
	ctx := context.Background()
	if err := s.TouchSession(ctx, SessionEvent{SessionID: id, Project: project, At: start}); err != nil {
		t.Fatal(err)
	}
	for i, o := range obs {
		o.SessionID, o.Project = id, project
		if o.CreatedAt.IsZero() {
			o.CreatedAt = start.Add(time.Duration(i+1) * time.Second)
		}
		if _, err := s.InsertObservation(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLastSession(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	seedSession(t, s, "/p/a", "old", sessionBase.Add(-48*time.Hour),
		Observation{Kind: "prompt", Title: "old", Body: "old prompt"})
	seedSession(t, s, "/p/a", "s1", sessionBase,
		Observation{Kind: "session_start", Title: "session start (startup)"},
		Observation{Kind: "prompt", Title: "first", Body: "first prompt"},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/a.go"}},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/b.go"}},
		Observation{Kind: "tool", Title: "Bash", ToolName: "Bash"},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/c.go"}},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/a.go"}},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/d.go"}},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/e.go"}},
		Observation{Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{"/p/a/f.go"}},
		Observation{Kind: "stop", Title: "assistant: done", Body: "first answer"},
		Observation{Kind: "prompt", Title: "second", Body: "second prompt"},
		Observation{Kind: "stop", Title: "assistant: done", Body: "second answer"},
	)
	seedSession(t, s, "/p/a", "empty", sessionBase.Add(time.Hour),
		Observation{Kind: "session_start", Title: "session start (startup)"})
	seedSession(t, s, "/p/a", "current", sessionBase.Add(2*time.Hour),
		Observation{Kind: "prompt", Title: "now", Body: "current prompt"})
	seedSession(t, s, "/p/b", "other", sessionBase.Add(3*time.Hour),
		Observation{Kind: "prompt", Title: "other", Body: "other prompt"})

	got, ok, err := s.LastSession(ctx, "/p/a", "current")
	if err != nil || !ok {
		t.Fatalf("LastSession = %v, %v", ok, err)
	}
	want := SessionSummary{
		SessionID:     "s1",
		StartedAt:     sessionBase,
		LastPrompt:    "second prompt",
		LastAssistant: "second answer",
		Files:         []string{"/p/a/f.go", "/p/a/e.go", "/p/a/d.go", "/p/a/a.go", "/p/a/c.go"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LastSession = %+v, want %+v", got, want)
	}
}

func TestLastSessionNone(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	seedSession(t, s, "/p/a", "current", sessionBase,
		Observation{Kind: "prompt", Title: "now", Body: "current prompt"})
	seedSession(t, s, "/p/a", "empty", sessionBase.Add(-time.Hour))
	if _, ok, err := s.LastSession(ctx, "/p/a", "current"); err != nil || ok {
		t.Errorf("LastSession = %v, %v, want not found", ok, err)
	}
}

func TestLatestCompactSummary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	seedSession(t, s, "/p/a", "s1", sessionBase,
		Observation{Kind: "compact", Title: "compact summary", Body: "older summary"},
		Observation{Kind: "compact", Title: "compact summary", Body: "newer summary"})
	seedSession(t, s, "/p/a", "s2", sessionBase,
		Observation{Kind: "prompt", Title: "p", Body: "p"})

	got, ok, err := s.LatestCompactSummary(ctx, "s1")
	if err != nil || !ok || got != "newer summary" {
		t.Errorf("LatestCompactSummary(s1) = %q, %v, %v", got, ok, err)
	}
	if got, ok, err := s.LatestCompactSummary(ctx, "s2"); err != nil || ok || got != "" {
		t.Errorf("LatestCompactSummary(s2) = %q, %v, %v", got, ok, err)
	}
}

func TestRecentMemoriesPinnedFirst(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	mk := func(id, project string, tags []string, updated time.Duration) {
		m := sampleMemory(id, "title "+id)
		m.Project, m.Tags, m.Updated = project, tags, sessionBase.Add(updated)
		if err := s.UpsertMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	mk("m-000000000001", "/p/a", []string{"retry"}, 5*time.Hour)
	mk("m-000000000002", "/p/a", []string{"pinned"}, time.Hour)
	mk("m-000000000003", "user", nil, 4*time.Hour)
	mk("m-000000000004", "/p/b", []string{"pinned"}, 9*time.Hour)
	mk("m-000000000005", "/p/a", []string{"x", "pinned"}, 2*time.Hour)
	mk("m-000000000006", "/p/a", []string{"unpinned", "pinned-later"}, 3*time.Hour)

	got, err := s.RecentMemories(ctx, CurrentScope("/p/a"), 4)
	if err != nil {
		t.Fatalf("RecentMemories: %v", err)
	}
	var ids []string
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	want := []string{"m-000000000005", "m-000000000002", "m-000000000001", "m-000000000003"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if got[0].Title != "title m-000000000005" || !got[0].Updated.Equal(sessionBase.Add(2*time.Hour)) {
		t.Errorf("first memory = %+v", got[0])
	}
	if got, err := s.RecentMemories(ctx, Scope{}, 4); err != nil || len(got) != 0 {
		t.Errorf("empty scope = %v, %v", got, err)
	}
}
