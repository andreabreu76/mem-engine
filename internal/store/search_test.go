package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var searchNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func openSearchTemp(t *testing.T) *Store {
	t.Helper()
	s := openGCTemp(t)
	s.now = func() time.Time { return searchNow }
	return s
}

func addMemory(t *testing.T, s *Store, id, project, title, body, session string, created time.Time) {
	t.Helper()
	m := Memory{
		ID:            id,
		Project:       project,
		Type:          "decision",
		Title:         title,
		Summary:       "summary " + id,
		Body:          body,
		Tags:          []string{"tag-" + id},
		Path:          id + ".md",
		SourceSession: session,
		Created:       created,
		Updated:       created,
	}
	if err := s.UpsertMemory(context.Background(), m); err != nil {
		t.Fatalf("UpsertMemory: %v", err)
	}
}

func addObservation(t *testing.T, s *Store, session, project, title, body string, at time.Time) string {
	t.Helper()
	id, err := s.InsertObservation(context.Background(), Observation{
		SessionID: session,
		Project:   project,
		Kind:      "tool",
		Title:     title,
		Body:      body,
		ToolName:  "Bash",
		Files:     []string{"main.go"},
		CreatedAt: at,
	})
	if err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	return fmt.Sprintf("o-%d", id)
}

func hitIDs(hits []Hit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

func itemIDs(items []Item) []string {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	return ids
}

func addFillers(t *testing.T, s *Store, project string) {
	t.Helper()
	at := searchNow.Add(-time.Hour)
	for i := 0; i < 4; i++ {
		addMemory(t, s, fmt.Sprintf("m-filler%d", i), project, "unrelated note", "nothing to see here", "", at)
		addObservation(t, s, "s-filler", project, "unrelated event", "nothing to see here", at)
	}
}

func TestSearchRanksMemoryAboveObservation(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addFillers(t, s, "/p/a")
	at := searchNow.Add(-time.Hour)
	obsID := addObservation(t, s, "s1", "/p/a", "retry backoff policy", "use exponential retry", at)
	addMemory(t, s, "m-000000000001", "/p/a", "retry backoff policy", "use exponential retry", "", at)

	hits, err := s.Search(ctx, SearchParams{Query: "retry", Scope: CurrentScope("/p/a")})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := hitIDs(hits), []string{"m-000000000001", obsID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if hits[0].Kind != "memory" || hits[0].Type != "decision" || hits[0].Summary != "summary m-000000000001" {
		t.Errorf("memory hit = %+v", hits[0])
	}
	if hits[1].Kind != "observation" || hits[1].Type != "tool" || hits[1].Summary != "use exponential retry" || hits[1].SessionID != "s1" {
		t.Errorf("observation hit = %+v", hits[1])
	}
	if !(hits[0].Score > hits[1].Score) || hits[1].Score <= 0 {
		t.Errorf("scores = %v, %v", hits[0].Score, hits[1].Score)
	}
}

func TestSearchRecencyBreaksTie(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addFillers(t, s, "/p/a")
	addMemory(t, s, "m-old", "/p/a", "cache invalidation", "same text", "", searchNow.Add(-60*day))
	addMemory(t, s, "m-new", "/p/a", "cache invalidation", "same text", "", searchNow.Add(-1*day))

	hits, err := s.Search(ctx, SearchParams{Query: "cache", Scope: CurrentScope("/p/a")})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := hitIDs(hits), []string{"m-new", "m-old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	ratio := hits[0].Score / hits[1].Score
	want := (1.0 / (1 + 1.0/30)) / (1.0 / (1 + 60.0/30))
	if diff := ratio - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score ratio = %v, want %v", ratio, want)
	}
}

func TestSearchScopeIsolation(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	at := searchNow.Add(-time.Hour)
	addMemory(t, s, "m-a", "/p/a", "deploy steps", "deploy body", "", at)
	addMemory(t, s, "m-b", "/p/b", "deploy steps", "deploy body", "", at)
	addMemory(t, s, "m-user", "user", "deploy preferences", "deploy body", "", at)
	oa := addObservation(t, s, "sa", "/p/a", "deploy run", "deploy output", at)
	ob := addObservation(t, s, "sb", "/p/b", "deploy run", "deploy output", at)

	for _, q := range []string{"deploy", ""} {
		hits, err := s.Search(ctx, SearchParams{Query: q, Scope: CurrentScope("/p/a")})
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		got := map[string]bool{}
		for _, h := range hits {
			if h.Project != "/p/a" && h.Project != "user" {
				t.Errorf("Search(%q) returned %s from project %s", q, h.ID, h.Project)
			}
			got[h.ID] = true
		}
		for _, id := range []string{"m-a", "m-user", oa} {
			if !got[id] {
				t.Errorf("Search(%q) missing %s, got %v", q, id, hitIDs(hits))
			}
		}
		if got["m-b"] || got[ob] {
			t.Errorf("Search(%q) leaked /p/b: %v", q, hitIDs(hits))
		}
	}

	hits, err := s.Search(ctx, SearchParams{Query: "deploy", Scope: Scope{}})
	if err != nil || len(hits) != 0 {
		t.Errorf("empty scope = %v, %v", hitIDs(hits), err)
	}
}

func TestSearchDiacritics(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addMemory(t, s, "m-1", "/p/a", "não usar ORM", "decisão de não usar ORM", "", searchNow)
	addObservation(t, s, "s1", "/p/a", "ação", "migração concluída", searchNow)

	for q, want := range map[string]int{"nao": 1, "não": 1, "acao": 1, "migracao": 1} {
		hits, err := s.Search(ctx, SearchParams{Query: q, Scope: CurrentScope("/p/a")})
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(hits) != want {
			t.Errorf("Search(%q) = %v, want %d hit", q, hitIDs(hits), want)
		}
	}
}

func TestFTSQuerySpecialChars(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addMemory(t, s, "m-1", "/p/a", "a b foo bar x near and", "body", "", searchNow)
	addObservation(t, s, "s1", "/p/a", "a b foo bar x", "body", searchNow)

	valid := regexp.MustCompile(`^("[\p{L}\p{N}_]+"( OR "[\p{L}\p{N}_]+")*)?$`)
	inputs := []string{
		`a "b`, `a-b`, `foo:bar`, `x*`, `NEAR(a b)`, `AND`, `""`, `)(`,
		`OR`, `NOT`, `a AND b OR NOT c`, `-`, `:`, `*`, `"`, `^a`, `{a b}:x`, `a + b`,
		"", "   ", `日本語 ação`, `snake_case_name`, strings.Repeat(`x" OR `, 5000),
	}
	for _, in := range inputs {
		if q := FTSQuery(in); !valid.MatchString(q) {
			t.Errorf("FTSQuery(%q) = %q, not a quoted OR list", in, q)
		}
		if _, err := s.Search(ctx, SearchParams{Query: in, Scope: CurrentScope("/p/a")}); err != nil {
			t.Errorf("Search(%q): %v", in, err)
		}
	}
}

func TestFTSQuery(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		`"" )(`:            "",
		"retry backoff":    `"retry" OR "backoff"`,
		`foo:bar NEAR(a)`:  `"foo" OR "bar" OR "NEAR" OR "a"`,
		"ação_x-1":         `"ação_x" OR "1"`,
		"  spaced\ttabs\n": `"spaced" OR "tabs"`,
	}
	for in, want := range cases {
		if got := FTSQuery(in); got != want {
			t.Errorf("FTSQuery(%q) = %q, want %q", in, got, want)
		}
	}
	many := strings.Repeat("w ", 100)
	if n := strings.Count(FTSQuery(many), `"w"`); n != 32 {
		t.Errorf("token cap = %d, want 32", n)
	}
}

func TestSearchEmptyQueryListsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addMemory(t, s, "m-1", "/p/a", "first", "body", "", searchNow.Add(-3*time.Hour))
	o2 := addObservation(t, s, "s1", "/p/a", "second", "body", searchNow.Add(-2*time.Hour))
	addMemory(t, s, "m-3", "user", "third", "body", "", searchNow.Add(-1*time.Hour))

	hits, err := s.Search(ctx, SearchParams{Query: "  ", Scope: CurrentScope("/p/a")})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got, want := hitIDs(hits), []string{"m-3", o2, "m-1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}
}

func TestSearchTypeAndSinceFilters(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addMemory(t, s, "m-old", "/p/a", "deploy", "body", "", searchNow.Add(-10*day))
	addMemory(t, s, "m-new", "/p/a", "deploy", "body", "", searchNow.Add(-1*day))
	obs := addObservation(t, s, "s1", "/p/a", "deploy", "body", searchNow.Add(-1*day))

	for _, q := range []string{"deploy", ""} {
		hits, err := s.Search(ctx, SearchParams{Query: q, Type: "decision", Scope: CurrentScope("/p/a")})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := hitIDs(hits); len(got) != 2 || got[0] != "m-new" || got[1] != "m-old" {
			t.Errorf("Type=decision q=%q ids = %v", q, got)
		}
		hits, err = s.Search(ctx, SearchParams{Query: q, Type: "tool", Scope: CurrentScope("/p/a")})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if got := hitIDs(hits); !reflect.DeepEqual(got, []string{obs}) {
			t.Errorf("Type=tool q=%q ids = %v", q, got)
		}
		hits, err = s.Search(ctx, SearchParams{Query: q, Since: searchNow.Add(-2 * day), Scope: CurrentScope("/p/a")})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		for _, h := range hits {
			if h.ID == "m-old" {
				t.Errorf("Since q=%q returned m-old", q)
			}
		}
		if len(hits) != 2 {
			t.Errorf("Since q=%q ids = %v", q, hitIDs(hits))
		}
	}
}

func TestSearchLimit(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	for i := 0; i < 60; i++ {
		addObservation(t, s, "s1", "/p/a", "deploy", "body", searchNow.Add(-time.Duration(i)*time.Minute))
	}
	for _, q := range []string{"deploy", ""} {
		for limit, want := range map[int]int{0: 10, -3: 10, 5: 5, 100: 50} {
			hits, err := s.Search(ctx, SearchParams{Query: q, Limit: limit, Scope: CurrentScope("/p/a")})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) != want {
				t.Errorf("q=%q limit %d: got %d hits, want %d", q, limit, len(hits), want)
			}
		}
	}
}

func TestTimelineSameSession(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	base := searchNow.Add(-time.Hour)
	var ids []string
	for i := 0; i < 10; i++ {
		ids = append(ids, addObservation(t, s, "s1", "/p/a", fmt.Sprintf("step %d", i), "body", base.Add(time.Duration(i)*time.Minute)))
		addObservation(t, s, "s2", "/p/a", "other session", "body", base.Add(time.Duration(i)*time.Minute))
	}

	hits, err := s.Timeline(ctx, ids[4], 2, 2, CurrentScope("/p/a"))
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if got, want := hitIDs(hits), ids[2:7]; !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}

	hits, err = s.Timeline(ctx, ids[0], 3, 1, CurrentScope("/p/a"))
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if got, want := hitIDs(hits), ids[0:2]; !reflect.DeepEqual(got, want) {
		t.Errorf("edge ids = %v, want %v", got, want)
	}
}

func TestTimelineMemory(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	base := searchNow.Add(-time.Hour)
	var ids []string
	for i := 0; i < 6; i++ {
		ids = append(ids, addObservation(t, s, "s1", "/p/a", "step", "body", base.Add(time.Duration(i)*time.Minute)))
	}
	addMemory(t, s, "m-sess", "/p/a", "saved", "body", "s1", base.Add(150*time.Second))
	addMemory(t, s, "m-alone", "/p/a", "saved", "body", "", base)

	hits, err := s.Timeline(ctx, "m-sess", 2, 2, CurrentScope("/p/a"))
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	want := []string{ids[1], ids[2], "m-sess", ids[3], ids[4]}
	if got := hitIDs(hits); !reflect.DeepEqual(got, want) {
		t.Errorf("ids = %v, want %v", got, want)
	}

	hits, err = s.Timeline(ctx, "m-alone", 2, 2, CurrentScope("/p/a"))
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if got := hitIDs(hits); !reflect.DeepEqual(got, []string{"m-alone"}) {
		t.Errorf("ids = %v", got)
	}
}

func TestTimelineOtherProjectNotFound(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	ob := addObservation(t, s, "sb", "/p/b", "secret", "body", searchNow)
	addMemory(t, s, "m-b", "/p/b", "secret", "body", "sb", searchNow)
	addObservation(t, s, "sb", "/p/b", "secret neighbor", "body", searchNow)

	for _, id := range []string{ob, "m-b", "o-999", "m-missing", "x-1", "o-abc", "o-", ""} {
		hits, err := s.Timeline(ctx, id, 2, 2, CurrentScope("/p/a"))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Timeline(%q) = %v, %v, want ErrNotFound", id, hitIDs(hits), err)
		}
	}
}

func TestGetOtherProjectHidden(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	oa := addObservation(t, s, "sa", "/p/a", "visible obs", "first line\nsecond line", searchNow)
	ob := addObservation(t, s, "sb", "/p/b", "hidden obs", "body", searchNow)
	addMemory(t, s, "m-a", "/p/a", "visible memory", "memory body", "", searchNow)
	addMemory(t, s, "m-b", "/p/b", "hidden memory", "body", "", searchNow)
	addMemory(t, s, "m-user", "user", "user memory", "user body", "", searchNow)

	items, err := s.Get(ctx, []string{ob, "m-user", "bogus", oa, "m-b", "o-abc", "m-a", "o-9999"}, CurrentScope("/p/a"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, want := itemIDs(items), []string{"m-user", oa, "m-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	if items[0].Body != "user body" || items[0].Kind != "memory" || !reflect.DeepEqual(items[0].Tags, []string{"tag-m-user"}) {
		t.Errorf("memory item = %+v", items[0])
	}
	obs := items[1]
	if obs.Body != "first line\nsecond line" || obs.Summary != "first line" || obs.ToolName != "Bash" ||
		!reflect.DeepEqual(obs.Files, []string{"main.go"}) || obs.Kind != "observation" || obs.Type != "tool" || obs.Truncated {
		t.Errorf("observation item = %+v", obs)
	}
}

func TestGetRespectsMaxBytes(t *testing.T) {
	ctx := context.Background()
	s := openSearchTemp(t)
	addMemory(t, s, "m-1", "/p/a", "one", strings.Repeat("á", 10), "", searchNow)
	o2 := addObservation(t, s, "s1", "/p/a", "two", strings.Repeat("é", 10), searchNow)
	addMemory(t, s, "m-3", "/p/a", "three", strings.Repeat("í", 10), "", searchNow)
	addMemory(t, s, "m-empty", "/p/a", "empty", "", "", searchNow)

	items, err := s.Get(ctx, []string{"m-1", o2, "m-3", "m-empty"}, CurrentScope("/p/a"), 31)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, want := itemIDs(items), []string{"m-1", o2, "m-3", "m-empty"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	total := 0
	for _, it := range items {
		total += len(it.Body)
		if !utf8.ValidString(it.Body) {
			t.Errorf("%s body is not valid UTF-8: %q", it.ID, it.Body)
		}
	}
	if total > 31 {
		t.Errorf("total body bytes = %d, want <= 31", total)
	}
	if items[0].Truncated || len(items[0].Body) != 20 {
		t.Errorf("item 0 = %q truncated=%v", items[0].Body, items[0].Truncated)
	}
	if !items[1].Truncated || items[1].Body != strings.Repeat("é", 5) {
		t.Errorf("item 1 = %q truncated=%v", items[1].Body, items[1].Truncated)
	}
	if !items[2].Truncated || items[2].Body != "" {
		t.Errorf("item 2 = %q truncated=%v", items[2].Body, items[2].Truncated)
	}
	if items[3].Truncated {
		t.Errorf("empty item marked truncated")
	}

	addMemory(t, s, "m-big", "/p/a", "big", strings.Repeat("x", 30000), "", searchNow)
	items, err = s.Get(ctx, []string{"m-big"}, CurrentScope("/p/a"), 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(items) != 1 || len(items[0].Body) != 20000 || !items[0].Truncated {
		t.Errorf("default cap: len=%d truncated=%v", len(items[0].Body), items[0].Truncated)
	}
}
