package capture

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/redact"
	"github.com/andreabreu76/mem-engine/internal/store"
)

var (
	testNow  = time.Unix(1700000000, 0)
	testProj = project.Project{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"}
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MEM_ENGINE_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func testRedactor(t *testing.T) *redact.Redactor {
	t.Helper()
	r, err := redact.New(nil, false)
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}
	return r
}

func build(t *testing.T, event string, p Payload) (Op, bool, error) {
	t.Helper()
	return Build(event, p, testProj, testConfig(t), testRedactor(t), testNow)
}

func mustBuild(t *testing.T, event string, p Payload) Op {
	t.Helper()
	op, keep, err := build(t, event, p)
	if err != nil {
		t.Fatalf("Build(%s): %v", event, err)
	}
	if !keep {
		t.Fatalf("Build(%s) dropped the event", event)
	}
	return op
}

func fakeToken() string {
	return "ghp_" + strings.Repeat("a", 36)
}

func TestBuildEvents(t *testing.T) {
	cases := []struct {
		event     string
		p         Payload
		kind      string
		title     string
		body      string
		toolName  string
		source    string
		end       bool
		endReason string
	}{
		{
			event:  "SessionStart",
			p:      Payload{Source: "startup"},
			kind:   "session_start",
			title:  "session start (startup)",
			source: "startup",
		},
		{
			event: "UserPromptSubmit",
			p:     Payload{Prompt: "fix the retry loop\nin the billing worker"},
			kind:  "prompt",
			title: "fix the retry loop",
			body:  "fix the retry loop\nin the billing worker",
		},
		{
			event:    "PostToolUse",
			p:        Payload{ToolName: "Bash", ToolInput: json.RawMessage(`{"command": "go test ./...", "description": "run tests"}`), ToolResponse: json.RawMessage(`"ok  \tbilling-api\t0.1s"`)},
			kind:     "tool",
			title:    "Bash: go test ./...",
			body:     "input: {\"command\":\"go test ./...\",\"description\":\"run tests\"}\noutput: ok  \tbilling-api\t0.1s",
			toolName: "Bash",
		},
		{
			event:    "PostToolUseFailure",
			p:        Payload{ToolName: "WebFetch", ToolInput: json.RawMessage(`{"prompt":"summarize","url":"https://example.com"}`), Error: "connection refused"},
			kind:     "tool_error",
			title:    "WebFetch: https://example.com",
			body:     "input: {\"prompt\":\"summarize\",\"url\":\"https://example.com\"}\nerror: connection refused",
			toolName: "WebFetch",
		},
		{
			event: "Stop",
			p:     Payload{LastAssistantMessage: "Done. The retry loop now backs off.\nDetails below."},
			kind:  "stop",
			title: "assistant: Done. The retry loop now backs off.",
			body:  "Done. The retry loop now backs off.\nDetails below.",
		},
		{
			event: "PreCompact",
			p:     Payload{Trigger: "auto"},
			kind:  "compact_start",
			title: "compact (auto)",
		},
		{
			event: "PostCompact",
			p:     Payload{Trigger: "manual", CompactSummary: "worked on billing retries"},
			kind:  "compact",
			title: "compact summary",
			body:  "worked on billing retries",
		},
		{
			event:     "SessionEnd",
			p:         Payload{Reason: "logout"},
			end:       true,
			endReason: "logout",
		},
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			tc.p.SessionID = "s1"
			tc.p.Cwd = "/home/dev/src/billing-api"
			tc.p.HookEventName = tc.event
			op := mustBuild(t, tc.event, tc.p)

			if op.Project.Key != testProj.Key || op.Project.Name != testProj.Name || op.Project.Dir != testProj.Dir {
				t.Errorf("Project = %+v", op.Project)
			}
			s := op.Session
			if s.SessionID != "s1" || s.Project != testProj.Key || s.Cwd != "/home/dev/src/billing-api" || !s.At.Equal(testNow) {
				t.Errorf("Session = %+v", s)
			}
			if s.Source != tc.source || s.End != tc.end || s.EndReason != tc.endReason {
				t.Errorf("Session source/end = %q/%v/%q, want %q/%v/%q", s.Source, s.End, s.EndReason, tc.source, tc.end, tc.endReason)
			}
			if tc.kind == "" {
				if op.Obs != nil {
					t.Fatalf("Obs = %+v, want nil", op.Obs)
				}
				return
			}
			if op.Obs == nil {
				t.Fatal("Obs is nil")
			}
			o := op.Obs
			if o.Kind != tc.kind || o.Title != tc.title || o.Body != tc.body || o.ToolName != tc.toolName {
				t.Errorf("Obs kind/title/body/tool = %q/%q/%q/%q\nwant %q/%q/%q/%q", o.Kind, o.Title, o.Body, o.ToolName, tc.kind, tc.title, tc.body, tc.toolName)
			}
			if o.SessionID != "s1" || o.Project != testProj.Key || !o.CreatedAt.Equal(testNow) {
				t.Errorf("Obs = %+v", o)
			}
		})
	}
}

func TestBuildUnknownEvent(t *testing.T) {
	if _, _, err := build(t, "Notification", Payload{SessionID: "s1"}); err == nil {
		t.Fatal("Build accepted an unknown event")
	}
}

func TestBuildRedactsBeforeTitle(t *testing.T) {
	token := fakeToken()
	op := mustBuild(t, "UserPromptSubmit", Payload{SessionID: "s1", Prompt: "use " + token + " to push"})
	if strings.Contains(op.Obs.Title, token) || strings.Contains(op.Obs.Body, token) {
		t.Fatalf("token leaked: title=%q body=%q", op.Obs.Title, op.Obs.Body)
	}
	if !strings.Contains(op.Obs.Title, "[REDACTED:github]") {
		t.Errorf("title = %q, want redaction marker", op.Obs.Title)
	}
}

func TestBuildRedactsLongPromptBeforeCut(t *testing.T) {
	token := fakeToken()
	prompt := strings.Repeat("x", 100) + " " + token
	op := mustBuild(t, "UserPromptSubmit", Payload{SessionID: "s1", Prompt: prompt})
	if strings.Contains(op.Obs.Title, "ghp_aaaa") {
		t.Fatalf("partial token leaked in title %q", op.Obs.Title)
	}
	if n := utf8.RuneCountInString(op.Obs.Title); n > 120 {
		t.Errorf("title has %d runes, want <= 120", n)
	}
}

func TestBuildPromptTitleFirstLineCapped(t *testing.T) {
	prompt := strings.Repeat("ã", 200) + "\nsecond line"
	op := mustBuild(t, "UserPromptSubmit", Payload{SessionID: "s1", Prompt: prompt})
	if op.Obs.Title != strings.Repeat("ã", 120) {
		t.Errorf("title = %q", op.Obs.Title)
	}
}

func TestBuildStripsPrivateInToolInput(t *testing.T) {
	op := mustBuild(t, "PostToolUse", Payload{
		SessionID:    "s1",
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"echo <private>hidden-value</private> done"}`),
		ToolResponse: json.RawMessage(`"done"`),
	})
	if strings.Contains(op.Obs.Body, "hidden-value") || strings.Contains(op.Obs.Title, "hidden-value") {
		t.Fatalf("private content leaked: title=%q body=%q", op.Obs.Title, op.Obs.Body)
	}
	if !strings.Contains(op.Obs.Body, "echo  done") {
		t.Errorf("body = %q", op.Obs.Body)
	}
}

func escapeJSON(s, chars string) string {
	for _, c := range chars {
		s = strings.ReplaceAll(s, string(c), fmt.Sprintf("%cu%04x", '\\', c))
	}
	return s
}

func TestBuildRedactsEscapedJSON(t *testing.T) {
	token := fakeToken()
	input := escapeJSON(`{"command":"echo <private>hidden-value</private> done","note":"`+token+`"}`, "<>_")
	response := escapeJSON(`{"stdout":"<PRIVATE>hidden-output</PRIVATE> ok","token":"`+token+`"}`, "<>_")
	if strings.Contains(input, "<") || strings.Contains(input, "ghp_") {
		t.Fatalf("setup: input not escaped: %s", input)
	}
	op := mustBuild(t, "PostToolUse", Payload{
		SessionID:    "s1",
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(input),
		ToolResponse: json.RawMessage(response),
	})
	for _, leak := range []string{"hidden-value", "hidden-output", token[len("ghp_"):]} {
		if strings.Contains(op.Obs.Body, leak) || strings.Contains(op.Obs.Title, leak) {
			t.Errorf("%q leaked: title=%q body=%q", leak, op.Obs.Title, op.Obs.Body)
		}
	}
	want := "input: {\"command\":\"echo  done\",\"note\":\"[REDACTED:github]\"}\noutput: {\"stdout\":\" ok\",\"token\":\"[REDACTED:github]\"}"
	if op.Obs.Body != want {
		t.Errorf("body = %q\nwant  %q", op.Obs.Body, want)
	}
}

func TestBuildToolResponseObject(t *testing.T) {
	op := mustBuild(t, "PostToolUse", Payload{
		SessionID:    "s1",
		ToolName:     "Read",
		ToolInput:    json.RawMessage(`{"file_path": "/home/dev/src/billing-api/main.go"}`),
		ToolResponse: json.RawMessage("{\n  \"type\": \"text\",\n  \"file\": {\"numLines\": 3, \"note\": \"a<b\"}\n}"),
	})
	want := "input: {\"file_path\":\"/home/dev/src/billing-api/main.go\"}\noutput: {\"type\":\"text\",\"file\":{\"numLines\":3,\"note\":\"a<b\"}}"
	if op.Obs.Body != want {
		t.Errorf("body = %q\nwant  %q", op.Obs.Body, want)
	}
	if op.Obs.Title != "Read: /home/dev/src/billing-api/main.go" {
		t.Errorf("title = %q", op.Obs.Title)
	}
}

func TestBuildToolWithoutDescriptor(t *testing.T) {
	op := mustBuild(t, "PostToolUse", Payload{SessionID: "s1", ToolName: "TodoWrite", ToolInput: json.RawMessage(`{"todos":[]}`)})
	if op.Obs.Title != "TodoWrite" {
		t.Errorf("title = %q", op.Obs.Title)
	}
}

func TestBuildHugeOutputTruncated(t *testing.T) {
	big := strings.Repeat("y", 5<<20)
	resp, err := json.Marshal(big)
	if err != nil {
		t.Fatal(err)
	}
	op := mustBuild(t, "PostToolUse", Payload{SessionID: "s1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"cat big.log"}`), ToolResponse: resp})
	limit := 4096 + len(fmt.Sprintf("\n[truncated %d bytes]", 5<<20+64))
	if len(op.Obs.Body) > limit {
		t.Fatalf("body has %d bytes, want <= %d", len(op.Obs.Body), limit)
	}
	if !strings.Contains(op.Obs.Body, "[truncated ") {
		t.Errorf("body lacks truncation marker")
	}
}

func TestBuildInvalidUTF8(t *testing.T) {
	op := mustBuild(t, "PostToolUse", Payload{
		SessionID:    "s1",
		ToolName:     "Bash",
		ToolInput:    json.RawMessage("{\"command\":\"cat \xff\"}"),
		ToolResponse: json.RawMessage("{\"out\":\"\xfe\xff\"}"),
	})
	if !utf8.ValidString(op.Obs.Body) || !utf8.ValidString(op.Obs.Title) {
		t.Fatalf("invalid UTF-8: title=%q body=%q", op.Obs.Title, op.Obs.Body)
	}
	op = mustBuild(t, "UserPromptSubmit", Payload{SessionID: "s1", Prompt: "bad \xff byte"})
	if !utf8.ValidString(op.Obs.Body) || !utf8.ValidString(op.Obs.Title) {
		t.Fatalf("invalid UTF-8: title=%q body=%q", op.Obs.Title, op.Obs.Body)
	}
}

func TestBuildSkipsOwnTools(t *testing.T) {
	_, keep, err := build(t, "PostToolUse", Payload{SessionID: "s1", ToolName: "mcp__plugin_mem-engine_mem-engine__search", ToolInput: json.RawMessage(`{"query":"retry"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if keep {
		t.Fatal("own tool was not skipped")
	}
}

func TestBuildEditRecordsFiles(t *testing.T) {
	cases := []struct {
		tool  string
		input string
		files []string
	}{
		{"Edit", `{"file_path":"/home/dev/src/billing-api/a.go","old_string":"x","new_string":"y"}`, []string{"/home/dev/src/billing-api/a.go"}},
		{"Write", `{"file_path":"/home/dev/src/billing-api/b.go","content":"package b"}`, []string{"/home/dev/src/billing-api/b.go"}},
		{"MultiEdit", `{"file_path":"/home/dev/src/billing-api/c.go","edits":[]}`, []string{"/home/dev/src/billing-api/c.go"}},
		{"NotebookEdit", `{"notebook_path":"/home/dev/src/billing-api/n.ipynb","new_source":"x"}`, []string{"/home/dev/src/billing-api/n.ipynb"}},
		{"Read", `{"file_path":"/home/dev/src/billing-api/a.go"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			op := mustBuild(t, "PostToolUse", Payload{SessionID: "s1", ToolName: tc.tool, ToolInput: json.RawMessage(tc.input), ToolResponse: json.RawMessage(`"ok"`)})
			if !slices.Equal(op.Obs.Files, tc.files) {
				t.Errorf("Files = %v, want %v", op.Obs.Files, tc.files)
			}
		})
	}
}

func TestBuildRedactsAllStoredFields(t *testing.T) {
	token := fakeToken()
	op := mustBuild(t, "PostToolUse", Payload{
		SessionID:    "s1",
		Cwd:          "/home/dev/src/" + token,
		ToolName:     "Edit",
		ToolInput:    json.RawMessage(`{"file_path":"/home/dev/src/billing-api/` + token + `.go","old_string":"x","new_string":"y"}`),
		ToolResponse: json.RawMessage(`"ok"`),
	})
	if len(op.Obs.Files) != 1 || strings.Contains(op.Obs.Files[0], token) {
		t.Errorf("Files = %v", op.Obs.Files)
	}
	if strings.Contains(op.Session.Cwd, token) {
		t.Errorf("Session.Cwd = %q", op.Session.Cwd)
	}
	op = mustBuild(t, "PostToolUse", Payload{SessionID: "s1", ToolName: "mcp__" + token, ToolInput: json.RawMessage(`{}`)})
	if strings.Contains(op.Obs.ToolName, token) {
		t.Errorf("ToolName = %q", op.Obs.ToolName)
	}
	op = mustBuild(t, "SessionStart", Payload{SessionID: "s1", Source: token})
	if strings.Contains(op.Session.Source, token) {
		t.Errorf("Session.Source = %q", op.Session.Source)
	}
	op = mustBuild(t, "SessionEnd", Payload{SessionID: "s1", Reason: token})
	if strings.Contains(op.Session.EndReason, token) {
		t.Errorf("Session.EndReason = %q", op.Session.EndReason)
	}
	if data, _ := json.Marshal(op); strings.Contains(string(data), token) {
		t.Errorf("op leaks token: %s", data)
	}
}

func TestOpJSONKeysAreStable(t *testing.T) {
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	op := Op{
		Project: store.ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"},
		Session: store.SessionEvent{SessionID: "s1", Project: "/home/dev/src/billing-api", Cwd: "/home/dev/src/billing-api", Source: "startup", EndReason: "exit", At: at, End: true},
		Obs:     &store.Observation{ID: 1, SessionID: "s1", Project: "/home/dev/src/billing-api", Kind: "tool", Title: "Edit", Body: "b", ToolName: "Edit", Files: []string{"a.go"}, CreatedAt: at},
	}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	want := map[string][]string{
		"project": {"dir", "key", "name"},
		"session": {"at", "cwd", "end", "end_reason", "project", "session_id", "source"},
		"obs":     {"body", "created_at", "files", "id", "kind", "project", "session_id", "title", "tool_name"},
	}
	if len(got) != len(want) {
		t.Errorf("top-level keys = %s", data)
	}
	for section, keys := range want {
		var have []string
		for k := range got[section] {
			have = append(have, k)
		}
		sort.Strings(have)
		if !reflect.DeepEqual(have, keys) {
			t.Errorf("%s keys = %v, want %v", section, have, keys)
		}
	}
	var back Op
	if err := json.Unmarshal(data, &back); err != nil || !reflect.DeepEqual(back, op) {
		t.Errorf("round trip = %+v, %v", back, err)
	}
}
