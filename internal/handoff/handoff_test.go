package handoff

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/store"
)

const wantLookup = "Lookup: search -> timeline -> get (memory tools of this project)."

var (
	testProject = store.ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"}
	testStart   = time.Date(2026, 11, 20, 14, 2, 0, 0, time.UTC)
)

func setEnv(t *testing.T) {
	t.Helper()
	userProfile := t.TempDir()
	t.Setenv("HOME", userProfile)
	t.Setenv("USERPROFILE", userProfile)
	t.Setenv("MEM_ENGINE_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
}

func memories(n int, title string) []store.Memory {
	ms := make([]store.Memory, n)
	for i := range ms {
		ms[i] = store.Memory{ID: fmt.Sprintf("m-%012x", i+1), Title: title}
	}
	return ms
}

func TestBuildLayout(t *testing.T) {
	setEnv(t)
	got := Build(Input{
		Project: testProject,
		Source:  "startup",
		Last: &store.SessionSummary{
			SessionID:     "s1",
			StartedAt:     testStart.In(time.FixedZone("x", -3*3600)),
			LastPrompt:    "fix the\n\n  retry loop",
			LastAssistant: "Done, retry\tnow backs off",
			Files:         []string{"/home/dev/src/billing-api/a.go", "/home/dev/src/billing-api/internal/b.go", "/tmp/notes.md"},
		},
		Memories: []store.Memory{
			{ID: "m-1a2b3c", Title: "Retry policy for payments"},
			{ID: "m-4d5e6f", Title: "Ledger\nnaming"},
		},
	})
	want := "mem-engine | project billing-api\n" +
		`Last session 2026-11-20 14:02: asked "fix the retry loop"; edited a.go, internal/b.go, notes.md; stopped at "Done, retry now backs off"` + "\n" +
		"Memories: m-1a2b3c Retry policy for payments; m-4d5e6f Ledger naming\n" +
		wantLookup
	if got != want {
		t.Errorf("Build =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildFullUnderBudget(t *testing.T) {
	setEnv(t)
	long := strings.Repeat("retry backoff ", 100)
	files := make([]string, 5)
	for i := range files {
		files[i] = "/home/dev/src/billing-api/internal/" + strings.Repeat("deep/", 20) + fmt.Sprintf("file%d.go", i)
	}
	got := Build(Input{
		Project:  testProject,
		Source:   "resume",
		Last:     &store.SessionSummary{SessionID: "s1", StartedAt: testStart, LastPrompt: long, LastAssistant: long, Files: files},
		Memories: memories(5, strings.Repeat("t", 200)),
	})
	if len(got) > MaxChars || len(got)/4 > 300 {
		t.Errorf("len = %d, tokens = %d", len(got), len(got)/4)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[0] != "mem-engine | project billing-api" || lines[3] != wantLookup {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.HasPrefix(lines[1], `Last session 2026-11-20 14:02: asked "retry backoff`) || !strings.Contains(lines[1], "…") {
		t.Errorf("session line = %q", lines[1])
	}
	if strings.Count(lines[2], "m-0000000000") != 5 {
		t.Errorf("memories line = %q", lines[2])
	}
}

func TestBuildCutsOnRuneBoundary(t *testing.T) {
	setEnv(t)
	wide := strings.Repeat("𝄞é", 400)
	got := Build(Input{
		Project:  store.ProjectRow{Key: "/p", Name: wide},
		Last:     &store.SessionSummary{StartedAt: testStart, LastPrompt: wide, LastAssistant: wide, Files: []string{wide, wide, wide}},
		Memories: memories(5, wide),
	})
	if len(got) > MaxChars || !utf8.ValidString(got) {
		t.Errorf("len = %d, valid = %v", len(got), utf8.ValidString(got))
	}
	if !strings.HasSuffix(got, "\n"+wantLookup) {
		t.Errorf("lookup line lost: %q", got[len(got)-80:])
	}
}

func TestBuildEmptyProject(t *testing.T) {
	setEnv(t)
	got := Build(Input{Project: testProject, Source: "startup"})
	want := "mem-engine | project billing-api\n" + wantLookup
	if got != want {
		t.Errorf("Build = %q, want %q", got, want)
	}
}

func TestBuildCompactUsesSummary(t *testing.T) {
	setEnv(t)
	last := &store.SessionSummary{SessionID: "s1", StartedAt: testStart, LastPrompt: "old prompt"}
	got := Build(Input{Project: testProject, Source: "compact", Last: last, Compact: "Working on\n\nretry   policy"})
	want := "mem-engine | project billing-api\nCompact summary: Working on retry policy\n" + wantLookup
	if got != want {
		t.Errorf("Build = %q, want %q", got, want)
	}
	long := Build(Input{Project: testProject, Source: "compact", Compact: strings.Repeat("summary ", 500)})
	if len(long) > MaxChars || !strings.Contains(long, "…") || !strings.HasSuffix(long, wantLookup) {
		t.Errorf("long compact = %q", long)
	}
	fallback := Build(Input{Project: testProject, Source: "compact", Last: last, Compact: " \n "})
	if !strings.Contains(fallback, `Last session 2026-11-20 14:02: asked "old prompt"`) || strings.Contains(fallback, "Compact") {
		t.Errorf("fallback = %q", fallback)
	}
	resume := Build(Input{Project: testProject, Source: "resume", Last: last, Compact: "ignored"})
	if strings.Contains(resume, "Compact") {
		t.Errorf("non-compact source used summary: %q", resume)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	setEnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seed(t *testing.T, st *store.Store, project, session string, at time.Time, kind, body string) {
	t.Helper()
	ctx := context.Background()
	if err := st.EnsureProject(ctx, store.ProjectRow{Key: project, Name: filepath.Base(project), Dir: "d"}); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchSession(ctx, store.SessionEvent{SessionID: session, Project: project, At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertObservation(ctx, store.Observation{SessionID: session, Project: project, Kind: kind, Title: kind, Body: body, CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectExcludesCurrentSession(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seed(t, st, testProject.Key, "s1", testStart, "prompt", "previous prompt")
	seed(t, st, testProject.Key, "s2", testStart.Add(time.Hour), "prompt", "current prompt")
	seed(t, st, "/home/dev/src/other", "s3", testStart.Add(2*time.Hour), "prompt", "other prompt")
	for _, m := range []store.Memory{
		{ID: "m-000000000001", Project: testProject.Key, Title: "mine", Tags: []string{}, Updated: testStart},
		{ID: "m-000000000002", Project: "user", Title: "user wide", Tags: []string{}, Updated: testStart.Add(time.Hour)},
		{ID: "m-000000000003", Project: "/home/dev/src/other", Title: "foreign", Tags: []string{}, Updated: testStart.Add(2 * time.Hour)},
	} {
		if err := st.UpsertMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	in, err := Collect(ctx, st, testProject, "s2", "startup")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Last == nil || in.Last.SessionID != "s1" || in.Last.LastPrompt != "previous prompt" {
		t.Fatalf("Last = %+v", in.Last)
	}
	if in.Project != testProject || in.Source != "startup" || in.Compact != "" {
		t.Errorf("Input = %+v", in)
	}
	if len(in.Memories) != 2 || in.Memories[0].ID != "m-000000000002" || in.Memories[1].ID != "m-000000000001" {
		t.Errorf("Memories = %+v", in.Memories)
	}
	text := Build(in)
	if !strings.Contains(text, "previous prompt") || strings.Contains(text, "current prompt") || strings.Contains(text, "foreign") {
		t.Errorf("Build = %q", text)
	}
}

func TestCollectCompactUsesCurrentSession(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seed(t, st, testProject.Key, "s1", testStart, "compact", "summary of previous session")
	seed(t, st, testProject.Key, "s2", testStart.Add(time.Hour), "compact", "summary of current session")

	in, err := Collect(ctx, st, testProject, "s2", "compact")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Compact != "summary of current session" {
		t.Errorf("Compact = %q", in.Compact)
	}
	if !strings.Contains(Build(in), "Compact summary: summary of current session") {
		t.Errorf("Build = %q", Build(in))
	}

	seed(t, st, testProject.Key, "s4", testStart.Add(2*time.Hour), "prompt", "fresh prompt")
	in, err = Collect(ctx, st, testProject, "s4", "compact")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Compact != "" || in.Last == nil || in.Last.SessionID != "s2" {
		t.Errorf("fallback Input = %+v", in)
	}
}

func TestCollectFiltersSessionContentByProject(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	other := store.ProjectRow{Key: "/home/dev/src/acme-web", Name: "acme-web", Dir: "acme-web-def456"}
	seed(t, st, testProject.Key, "s1", testStart, "session_start", "")
	seed(t, st, testProject.Key, "s1", testStart.Add(time.Minute), "prompt", "fix retry in billing")
	seed(t, st, other.Key, "s1", testStart.Add(2*time.Minute), "prompt", "deploy acme secret plan")
	if _, err := st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: other.Key, Kind: "tool", Title: "Edit", ToolName: "Edit", Files: []string{other.Key + "/checkout.go"}, CreatedAt: testStart.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	seed(t, st, other.Key, "s1", testStart.Add(4*time.Minute), "stop", "acme answer")
	seed(t, st, other.Key, "s1", testStart.Add(5*time.Minute), "compact", "acme compact summary")
	seed(t, st, testProject.Key, "s2", testStart.Add(time.Hour), "prompt", "current prompt")
	seed(t, st, other.Key, "s3", testStart.Add(2*time.Hour), "prompt", "acme current")

	in, err := Collect(ctx, st, testProject, "s2", "startup")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Last == nil || in.Last.SessionID != "s1" || in.Last.LastPrompt != "fix retry in billing" || in.Last.LastAssistant != "" || len(in.Last.Files) != 0 {
		t.Fatalf("Last of billing-api = %+v", in.Last)
	}
	text := Build(in)
	if strings.Contains(text, "acme") || strings.Contains(text, "checkout.go") {
		t.Errorf("billing-api handoff leaks acme-web: %q", text)
	}

	in, err = Collect(ctx, st, other, "s3", "startup")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Last == nil || in.Last.SessionID != "s1" || in.Last.LastPrompt != "deploy acme secret plan" || in.Last.LastAssistant != "acme answer" || len(in.Last.Files) != 1 {
		t.Fatalf("Last of acme-web = %+v", in.Last)
	}
	if text := Build(in); strings.Contains(text, "billing") || !strings.Contains(text, "checkout.go") {
		t.Errorf("acme-web handoff = %q", text)
	}

	in, err = Collect(ctx, st, testProject, "s1", "compact")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Compact != "" {
		t.Errorf("billing-api compact = %q", in.Compact)
	}
	in, err = Collect(ctx, st, other, "s1", "compact")
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if in.Compact != "acme compact summary" {
		t.Errorf("acme-web compact = %q", in.Compact)
	}
}
