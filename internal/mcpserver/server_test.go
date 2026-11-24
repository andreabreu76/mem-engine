package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

var (
	billing = project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: project.DirName("billing-api", "/home/dev/src/billing-api")}
	ledger  = project.Project{Key: "/home/dev/src/ledger", Name: "ledger", Dir: project.DirName("ledger", "/home/dev/src/ledger")}
	testNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	memID   = regexp.MustCompile(`m-[0-9a-f]{12}`)
)

type fixture struct {
	st   *store.Store
	svc  *memory.Service
	home string
	log  *bytes.Buffer
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	userProfile := t.TempDir()
	t.Setenv("HOME", userProfile)
	t.Setenv("USERPROFILE", userProfile)
	home := t.TempDir()
	t.Setenv("MEM_ENGINE_HOME", home)
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := func() time.Time { return testNow }
	return fixture{st: st, svc: &memory.Service{Store: st, WikiRoot: filepath.Join(home, "wiki"), Now: now}, home: home, log: &bytes.Buffer{}}
}

func (f fixture) connect(t *testing.T, current project.Project) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(f.log, nil))
	srv := New(Deps{Store: f.st, Memory: f.svc, Current: current, Now: func() time.Time { return testNow }, Log: log})
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func (f fixture) save(t *testing.T, p project.Project, title, summary, body string) string {
	t.Helper()
	m, err := f.svc.Save(context.Background(), p, memory.Input{Type: "decision", Title: title, Summary: summary, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.StructuredContent != nil {
		t.Errorf("%s: structured content %v, want text only", name, res.StructuredContent)
	}
	var b strings.Builder
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("%s: content %T, want text", name, c)
		}
		b.WriteString(tc.Text)
	}
	return b.String(), res.IsError
}

func mustCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, cs, name, args)
	if isErr {
		t.Fatalf("%s(%v) returned tool error: %s", name, args, text)
	}
	return text
}

func TestListToolsOrderHints(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, billing)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"search": "Step 1 of 3", "timeline": "Step 2 of 3", "get": "Step 3 of 3",
		"save": "", "update": "", "forget": "",
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(res.Tools), len(want))
	}
	for _, tool := range res.Tools {
		hint, ok := want[tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if !strings.Contains(tool.Description, hint) {
			t.Errorf("%s description %q lacks %q", tool.Name, tool.Description, hint)
		}
		if tool.OutputSchema != nil {
			t.Errorf("%s has an output schema", tool.Name)
		}
		if hint == "" {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		param, _ := props["project"].(map[string]any)
		if desc, _ := param["description"].(string); !strings.Contains(desc, "Omit it unless the user explicitly asks") {
			t.Errorf("%s project description = %q", tool.Name, desc)
		}
	}
}

func TestSearchTimelineGetFlow(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, billing)
	saved := mustCall(t, cs, "save", map[string]any{
		"type": "decision", "title": "Invoices use UTC", "summary": "store invoice dates in UTC",
		"body": "Local time broke month-end totals.", "tags": []string{"time"},
	})
	id := memID.FindString(saved)
	if id == "" {
		t.Fatalf("save response %q has no id", saved)
	}

	found := mustCall(t, cs, "search", map[string]any{"query": "invoice UTC"})
	if want := id + " 2026-11-23 decision Invoices use UTC | store invoice dates in UTC"; !strings.Contains(found, want) {
		t.Errorf("search = %q, want line %q", found, want)
	}

	line := mustCall(t, cs, "timeline", map[string]any{"id": id})
	if !strings.HasPrefix(line, "> "+id+" ") {
		t.Errorf("timeline = %q, want anchor marked with >", line)
	}

	got := mustCall(t, cs, "get", map[string]any{"ids": []string{id}})
	for _, want := range []string{id, "Invoices use UTC", "store invoice dates in UTC", "Local time broke month-end totals.", "time"} {
		if !strings.Contains(got, want) {
			t.Errorf("get = %q, want %q", got, want)
		}
	}
}

func TestTimelineMarksAnchorAmongNeighbors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var ids []int64
	for i := range 5 {
		id, err := f.st.InsertObservation(ctx, store.Observation{
			SessionID: "s1", Project: billing.Key, Kind: "tool", Title: "step",
			Body: "body", CreatedAt: testNow.Add(time.Duration(i-10) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	cs := f.connect(t, billing)
	out := mustCall(t, cs, "timeline", map[string]any{"id": "o-" + itoa(ids[2]), "before": 1, "after": 1})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("timeline = %q, want 3 lines", out)
	}
	for i, prefix := range []string{"  o-" + itoa(ids[1]) + " ", "> o-" + itoa(ids[2]) + " ", "  o-" + itoa(ids[3]) + " "} {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], prefix)
		}
	}
}

func TestSearchDefaultsToCurrentProject(t *testing.T) {
	f := newFixture(t)
	other := f.save(t, ledger, "Ledger rounding", "round half even", "ledger body")
	mine := f.save(t, billing, "Billing rounding", "round half up", "billing body")
	cs := f.connect(t, billing)
	out := mustCall(t, cs, "search", map[string]any{"query": "rounding"})
	if strings.Contains(out, other) {
		t.Errorf("search leaked other project: %q", out)
	}
	if !strings.Contains(out, mine) {
		t.Errorf("search = %q, want %s", out, mine)
	}
}

func TestSearchExplicitProject(t *testing.T) {
	f := newFixture(t)
	other := f.save(t, ledger, "Ledger rounding", "round half even", "ledger body")
	f.save(t, billing, "Billing rounding", "round half up", "billing body")
	cs := f.connect(t, billing)
	for _, name := range []string{"ledger", ledger.Key} {
		out := mustCall(t, cs, "search", map[string]any{"query": "rounding", "project": name})
		if !strings.Contains(out, other) {
			t.Errorf("project %q: search = %q, want %s", name, out, other)
		}
	}
	out, isErr := call(t, cs, "search", map[string]any{"query": "rounding", "project": "nope"})
	if !isErr || !strings.Contains(out, "nope") {
		t.Errorf("unknown project: isError = %v, text = %q", isErr, out)
	}
}

func TestGetOtherProjectWithoutParam(t *testing.T) {
	f := newFixture(t)
	other := f.save(t, ledger, "Ledger rounding", "round half even", "secret ledger body")
	cs := f.connect(t, billing)

	out := mustCall(t, cs, "get", map[string]any{"ids": []string{other}})
	if strings.Contains(out, "secret ledger body") || strings.Contains(out, "Ledger rounding") {
		t.Errorf("get leaked other project: %q", out)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("get = %q, want not found", out)
	}

	tl, isErr := call(t, cs, "timeline", map[string]any{"id": other})
	if !isErr || strings.Contains(tl, "Ledger") || !strings.Contains(tl, "not found") {
		t.Errorf("timeline: isError = %v, text = %q", isErr, tl)
	}

	missing, isErr := call(t, cs, "timeline", map[string]any{"id": "m-000000000000"})
	if !isErr || strings.ReplaceAll(missing, "m-000000000000", other) != tl {
		t.Errorf("missing id answer %q differs from out-of-scope answer %q", missing, tl)
	}

	out = mustCall(t, cs, "get", map[string]any{"ids": []string{other}, "project": "ledger"})
	if !strings.Contains(out, "secret ledger body") {
		t.Errorf("get with project = %q, want body", out)
	}

	for tool, args := range map[string]map[string]any{"forget": {"id": other}, "update": {"id": other, "title": "changed"}} {
		res, isErr := call(t, cs, tool, args)
		if !isErr || !strings.Contains(res, "not found") {
			t.Errorf("%s other project: isError = %v, text = %q", tool, isErr, res)
		}
	}
	if m, err := f.st.GetMemory(context.Background(), other); err != nil || m.Title != "Ledger rounding" {
		t.Errorf("other project memory = %+v, err %v", m, err)
	}
}

func TestSaveUserScope(t *testing.T) {
	f := newFixture(t)
	cs := f.connect(t, billing)
	saved := mustCall(t, cs, "save", map[string]any{
		"type": "preference", "title": "Prefer table tests", "summary": "table driven tests in Go",
		"body": "", "scope": "user",
	})
	id := memID.FindString(saved)
	m, err := f.st.GetMemory(context.Background(), id)
	if err != nil || m.Project != project.User.Key {
		t.Fatalf("memory = %+v, err %v", m, err)
	}
	out := mustCall(t, f.connect(t, ledger), "search", map[string]any{"query": "table tests"})
	if !strings.Contains(out, id) {
		t.Errorf("user memory not visible from another project: %q", out)
	}
	bad, isErr := call(t, cs, "save", map[string]any{"type": "fact", "title": "t", "summary": "s", "body": "", "scope": "team"})
	if !isErr || !strings.Contains(bad, "scope") {
		t.Errorf("invalid scope: isError = %v, text = %q", isErr, bad)
	}
}

func TestUpdateAndForget(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "Old title", "old summary", "old body")
	cs := f.connect(t, billing)

	out := mustCall(t, cs, "update", map[string]any{"id": id, "title": "New title", "tags": []string{"a"}})
	if !strings.Contains(out, id) {
		t.Errorf("update = %q", out)
	}
	m, err := f.st.GetMemory(context.Background(), id)
	if err != nil || m.Title != "New title" || m.Summary != "old summary" || len(m.Tags) != 1 {
		t.Errorf("after update = %+v, err %v", m, err)
	}
	if bad, isErr := call(t, cs, "update", map[string]any{"id": id, "type": "Not A Type"}); !isErr || !strings.Contains(bad, "type") {
		t.Errorf("invalid type: isError = %v, text = %q", isErr, bad)
	}

	mustCall(t, cs, "forget", map[string]any{"id": id})
	if got := mustCall(t, cs, "get", map[string]any{"ids": []string{id}}); !strings.Contains(got, "not found") {
		t.Errorf("get after forget = %q", got)
	}
	if again, isErr := call(t, cs, "forget", map[string]any{"id": id}); !isErr || !strings.Contains(again, "not found") {
		t.Errorf("second forget: isError = %v, text = %q", isErr, again)
	}
}

func TestSearchSinceAndType(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	old, err := f.st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: billing.Key, Kind: "tool", Title: "old deploy", Body: "deploy", CreatedAt: testNow.AddDate(0, 0, -20)})
	if err != nil {
		t.Fatal(err)
	}
	recent, err := f.st.InsertObservation(ctx, store.Observation{SessionID: "s1", Project: billing.Key, Kind: "tool", Title: "new deploy", Body: "deploy", CreatedAt: testNow.AddDate(0, 0, -2)})
	if err != nil {
		t.Fatal(err)
	}
	mem := f.save(t, billing, "Deploy notes", "deploy on fridays", "")
	cs := f.connect(t, billing)

	for _, since := range []string{"7d", "2026-11-16"} {
		out := mustCall(t, cs, "search", map[string]any{"query": "deploy", "since": since})
		if strings.Contains(out, "o-"+itoa(old)+" ") || !strings.Contains(out, "o-"+itoa(recent)+" ") {
			t.Errorf("since %s: %q", since, out)
		}
	}
	out := mustCall(t, cs, "search", map[string]any{"query": "deploy", "type": "decision"})
	if !strings.Contains(out, mem) || strings.Contains(out, "o-") {
		t.Errorf("type filter: %q", out)
	}
	if bad, isErr := call(t, cs, "search", map[string]any{"query": "deploy", "since": "last week"}); !isErr || !strings.Contains(bad, "since") {
		t.Errorf("invalid since: isError = %v, text = %q", isErr, bad)
	}
	if empty := mustCall(t, cs, "search", map[string]any{"query": "nothingmatches"}); !strings.Contains(empty, "no results") {
		t.Errorf("empty search = %q", empty)
	}
}

func TestSearchLineBudget(t *testing.T) {
	f := newFixture(t)
	for i := range 10 {
		f.save(t, billing, "budget "+strings.Repeat("title ", 40)+itoa(int64(i)), strings.Repeat("summary words ", 60), strings.Repeat("body ", 500))
	}
	cs := f.connect(t, billing)
	out := mustCall(t, cs, "search", map[string]any{"query": "budget", "limit": 10})
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("got %d lines: %q", len(lines), out)
	}
	if len(out) > 10*60*4 {
		t.Errorf("10 lines take %d bytes, want <= %d", len(out), 10*60*4)
	}
	for _, l := range lines {
		if !strings.Contains(l, "…") || strings.Contains(l, "body") {
			t.Errorf("line not capped or carries body: %q", l)
		}
	}
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestUpdateOverCorruptFileIsShort(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "Retry policy", "summary", "body")
	m, err := f.st.GetMemory(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.home, "wiki", m.Path)
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(strings.Replace(string(b), "type: decision", "type: Not Valid", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := f.connect(t, billing)
	out, isErr := call(t, cs, "update", map[string]any{"id": id, "title": "New title"})
	if !isErr || out != "update failed, see mem.log" {
		t.Fatalf("update over corrupt file: isError = %v, text = %q", isErr, out)
	}
	if !strings.Contains(f.log.String(), m.Path) {
		t.Fatalf("mem.log lacks the full error: %q", f.log.String())
	}
}

func TestInternalErrorIsShort(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.svc.WikiRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.svc.WikiRoot, billing.Dir), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := f.connect(t, billing)
	out, isErr := call(t, cs, "save", map[string]any{"type": "fact", "title": "t", "summary": "s", "body": ""})
	if !isErr || out != "save failed, see mem.log" {
		t.Errorf("save: isError = %v, text = %q", isErr, out)
	}
	if strings.Contains(out, f.home) || strings.ContainsRune(out, filepath.Separator) {
		t.Errorf("save error leaks a path: %q", out)
	}
	if !strings.Contains(f.log.String(), billing.Dir) {
		t.Errorf("mem.log lacks the full error: %q", f.log.String())
	}

	for args, want := range map[string]string{
		`{"type": "Bad Type", "title": "t", "summary": "s", "body": ""}`: "invalid memory type",
		`{"type": "fact", "title": " ", "summary": "s", "body": ""}`:     "invalid memory: title is required",
		`{"type": "fact", "title": "t", "summary": "", "body": ""}`:      "invalid memory: summary is required",
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(args), &m); err != nil {
			t.Fatal(err)
		}
		if out, isErr := call(t, cs, "save", m); !isErr || !strings.HasPrefix(out, want) {
			t.Errorf("save %s: isError = %v, text = %q, want prefix %q", args, isErr, out, want)
		}
	}
}

func TestGetCapsWholeResponse(t *testing.T) {
	f := newFixture(t)
	var tags []string
	for i := range 60 {
		tags = append(tags, "tag-"+strings.Repeat("x", 20)+itoa(int64(i)))
	}
	var ids []string
	for i := range 20 {
		m, err := f.svc.Save(context.Background(), billing, memory.Input{
			Type: "fact", Title: "meta " + itoa(int64(i)) + " " + strings.Repeat("t", 200),
			Summary: strings.Repeat("summary ", 60), Tags: tags,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	cs := f.connect(t, billing)
	out := mustCall(t, cs, "get", map[string]any{"ids": ids})
	if len(out) > getMaxBytes {
		t.Errorf("get returned %d bytes, want <= %d", len(out), getMaxBytes)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "[output truncated") {
		t.Fatalf("last line = %q, want truncation notice", last)
	}
	shown := 0
	for _, id := range ids {
		header := strings.Contains(out, "\n"+id+" ") || strings.HasPrefix(out, id+" ")
		listed := strings.Contains(last, id)
		if header == listed {
			t.Errorf("%s: shown = %v, listed as not shown = %v", id, header, listed)
		}
		if header {
			shown++
		}
	}
	if shown == 0 || shown == len(ids) {
		t.Errorf("shown %d of %d items", shown, len(ids))
	}
}

func TestGetCutsSingleOversizedItem(t *testing.T) {
	items := []store.Item{
		{Hit: store.Hit{ID: "m-aaaaaaaaaaaa", Type: "fact", Title: "big", At: testNow}, Body: strings.Repeat("é", 15000)},
		{Hit: store.Hit{ID: "m-bbbbbbbbbbbb", Type: "fact", Title: "next", At: testNow}, Body: "small"},
	}
	out := formatItems(items, []string{"m-cccccccccccc"}, getMaxBytes)
	if len(out) > getMaxBytes || !utf8.ValidString(out) {
		t.Fatalf("len = %d, valid UTF-8 = %v", len(out), utf8.ValidString(out))
	}
	if !strings.HasPrefix(out, "m-aaaaaaaaaaaa ") || !strings.HasSuffix(out, "not found: m-cccccccccccc\n[output truncated at 20000 bytes; m-aaaaaaaaaaaa cut short; not shown: m-bbbbbbbbbbbb]\n") {
		t.Errorf("out ends with %q", out[len(out)-200:])
	}
}

func TestToolsPayloadMatchesListTools(t *testing.T) {
	f := newFixture(t)
	res, err := f.connect(t, billing).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(res.Tools); err != nil {
		t.Fatal(err)
	}
	got, err := ToolsPayload(context.Background())
	if err != nil {
		t.Fatalf("ToolsPayload: %v", err)
	}
	if string(got) != strings.TrimSuffix(want.String(), "\n") {
		t.Errorf("ToolsPayload =\n%s\nwant\n%s", got, want.String())
	}
}

func TestGetBoundsLongUnknownIDs(t *testing.T) {
	f := newFixture(t)
	id := f.save(t, billing, "Retry policy", "summary", "body")
	cs := f.connect(t, billing)
	long := "m-" + strings.Repeat("a", 2000)
	var onlyLong, mixed []string
	for i := range maxGetIDs {
		onlyLong = append(onlyLong, long+itoa(int64(i)))
	}
	mixed = append([]string{id}, onlyLong[1:]...)
	for name, ids := range map[string][]string{"only long": onlyLong, "with a valid id": mixed} {
		out, isErr := call(t, cs, "get", map[string]any{"ids": ids})
		if len(out) > getMaxBytes {
			t.Errorf("%s: get returned %d bytes, want <= %d", name, len(out), getMaxBytes)
		}
		if !isErr || len(out) > 120 || strings.Contains(out, long[:40]) {
			t.Errorf("%s: isError = %v, %d bytes, want a short error without the ids", name, isErr, len(out))
		}
	}
	out := mustCall(t, cs, "get", map[string]any{"ids": []string{id, "m-000000000000", "o-999999"}})
	if !strings.HasSuffix(out, "not found: m-000000000000, o-999999\n") {
		t.Errorf("out = %q", out)
	}
}
