package capture

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/applog"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/spool"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func testDeps(t *testing.T) Deps {
	t.Helper()
	cfg := testConfig(t)
	log, closer, err := applog.Open(cfg.LogDir())
	if err != nil {
		t.Fatalf("applog.Open: %v", err)
	}
	t.Cleanup(func() { closer.Close() })
	return Deps{Cfg: cfg, Log: log, Now: func() time.Time { return testNow }}
}

func payloadJSON(t *testing.T, p Payload) string {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func promptPayload(t *testing.T, cwd, prompt string) string {
	t.Helper()
	return payloadJSON(t, Payload{SessionID: "s1", Cwd: cwd, HookEventName: "UserPromptSubmit", Prompt: prompt})
}

func handle(t *testing.T, d Deps, event, stdin string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Handle(context.Background(), event, strings.NewReader(stdin), &out, d)
	return out.String(), err
}

func rawDB(t *testing.T, cfg config.Config) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countRows(t *testing.T, cfg config.Config, table string) int {
	t.Helper()
	var n int
	if err := rawDB(t, cfg).QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func captureErrors(t *testing.T, cfg config.Config) (int, string) {
	t.Helper()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	n, last, err := st.CaptureErrorsSince(context.Background(), time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return n, last
}

func allCaptureErrorMessages(t *testing.T, cfg config.Config) string {
	t.Helper()
	rows, err := rawDB(t, cfg).Query("SELECT event || ' ' || message FROM capture_errors")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		b.WriteString(s + "\n")
	}
	return b.String()
}

func readLog(t *testing.T, cfg config.Config) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.LogDir(), "mem.log"))
	if err != nil {
		t.Fatalf("read mem.log: %v", err)
	}
	return string(data)
}

func readSpool(t *testing.T, cfg config.Config) string {
	t.Helper()
	entries, err := os.ReadDir(cfg.SpoolDir())
	if err != nil {
		t.Fatalf("read spool dir: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(cfg.SpoolDir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

func lockDB(t *testing.T, cfg config.Config) func() {
	t.Helper()
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	st.Close()
	db, err := sql.Open("sqlite", cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("BEGIN IMMEDIATE: %v", err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
		db.Close()
	}
	t.Cleanup(release)
	return release
}

func TestHandleWritesObservation(t *testing.T) {
	d := testDeps(t)
	cwd := t.TempDir()
	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, cwd, "add retries to the billing worker")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var kind, title, project, session string
	err := rawDB(t, d.Cfg).QueryRow("SELECT kind, title, project, session_id FROM observations").Scan(&kind, &title, &project, &session)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if kind != "prompt" || title != "add retries to the billing worker" || session != "s1" || project == "" {
		t.Errorf("observation = %q %q %q %q", kind, title, project, session)
	}
	if n := countRows(t, d.Cfg, "projects"); n != 1 {
		t.Errorf("projects = %d, want 1", n)
	}
	if n := countRows(t, d.Cfg, "sessions"); n != 1 {
		t.Errorf("sessions = %d, want 1", n)
	}
}

func TestHandleSessionEndClosesSession(t *testing.T) {
	d := testDeps(t)
	cwd := t.TempDir()
	if _, err := handle(t, d, "SessionStart", payloadJSON(t, Payload{SessionID: "s1", Cwd: cwd, Source: "startup"})); err != nil {
		t.Fatal(err)
	}
	if _, err := handle(t, d, "SessionEnd", payloadJSON(t, Payload{SessionID: "s1", Cwd: cwd, Reason: "logout"})); err != nil {
		t.Fatal(err)
	}
	var source, reason string
	var ended sql.NullInt64
	if err := rawDB(t, d.Cfg).QueryRow("SELECT source, ended_at, end_reason FROM sessions WHERE id = 's1'").Scan(&source, &ended, &reason); err != nil {
		t.Fatal(err)
	}
	if source != "startup" || !ended.Valid || reason != "logout" {
		t.Errorf("session = %q %v %q", source, ended, reason)
	}
	if n := countRows(t, d.Cfg, "observations"); n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
}

func TestHandleMalformedJSON(t *testing.T) {
	d := testDeps(t)
	_, err := handle(t, d, "PostToolUse", `{"session_id": "s1", "tool_name": `)
	if err == nil {
		t.Error("Handle returned nil for malformed JSON")
	}
	if n, _ := captureErrors(t, d.Cfg); n != 1 {
		t.Errorf("capture errors = %d, want 1", n)
	}
	if log := readLog(t, d.Cfg); !strings.Contains(log, "PostToolUse") {
		t.Errorf("mem.log lacks the event: %q", log)
	}
}

func TestHandleMalformedJSONDoesNotLeak(t *testing.T) {
	d := testDeps(t)
	token := fakeToken()
	inputs := []string{
		`{"session_id": "s1", "prompt": "` + token + `", `,
		`{"session_id": "s1", "duration_ms": "` + token + `"}`,
		token + ` not json`,
	}
	for _, in := range inputs {
		handle(t, d, "UserPromptSubmit", in)
	}
	if n, _ := captureErrors(t, d.Cfg); n != len(inputs) {
		t.Errorf("capture errors = %d, want %d", n, len(inputs))
	}
	if log := readLog(t, d.Cfg); strings.Contains(log, token) || strings.Contains(log, "ghp_") {
		t.Errorf("mem.log leaks the token: %q", log)
	}
	if msgs := allCaptureErrorMessages(t, d.Cfg); strings.Contains(msgs, token) || strings.Contains(msgs, "ghp_") {
		t.Errorf("capture_errors leak the token: %q", msgs)
	}
}

func TestHandleSpoolsWhenBusy(t *testing.T) {
	d := testDeps(t)
	d.StoreOptions = store.Options{BusyTimeout: 50 * time.Millisecond}
	cwd := t.TempDir()
	release := lockDB(t, d.Cfg)

	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, cwd, "first prompt")); err != nil {
		t.Fatalf("Handle while busy: %v", err)
	}
	if n, err := spool.Pending(d.Cfg.SpoolDir()); err != nil || n != 1 {
		t.Fatalf("spool.Pending = %d, %v; want 1", n, err)
	}
	release()
	if n := countRows(t, d.Cfg, "observations"); n != 0 {
		t.Fatalf("observations while busy = %d, want 0", n)
	}

	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, cwd, "second prompt")); err != nil {
		t.Fatalf("Handle after release: %v", err)
	}
	if n := countRows(t, d.Cfg, "observations"); n != 2 {
		t.Errorf("observations = %d, want 2", n)
	}
	if n, err := spool.Pending(d.Cfg.SpoolDir()); err != nil || n != 0 {
		t.Errorf("spool.Pending = %d, %v; want 0", n, err)
	}
	rows, err := rawDB(t, d.Cfg).Query("SELECT title FROM observations ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var titles []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		titles = append(titles, s)
	}
	if strings.Join(titles, ",") != "first prompt,second prompt" {
		t.Errorf("titles = %v", titles)
	}
}

func TestHandleSpoolIsRedacted(t *testing.T) {
	d := testDeps(t)
	d.StoreOptions = store.Options{BusyTimeout: 50 * time.Millisecond}
	lockDB(t, d.Cfg)
	token := fakeToken()

	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, t.TempDir(), "push with "+token+" <private>internal-host</private>")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	content := readSpool(t, d.Cfg)
	if content == "" {
		t.Fatal("spool is empty")
	}
	if strings.Contains(content, token) || strings.Contains(content, "internal-host") {
		t.Errorf("spool leaks content: %q", content)
	}
	if log := readLog(t, d.Cfg); strings.Contains(log, token) {
		t.Errorf("mem.log leaks the token: %q", log)
	}
}

func TestHandleDrainSkipsPoisonLine(t *testing.T) {
	d := testDeps(t)
	cwd := t.TempDir()
	spooled := Op{
		Project: store.ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"},
		Session: store.SessionEvent{SessionID: "s0", Project: "/home/dev/src/billing-api", At: testNow},
		Obs:     &store.Observation{SessionID: "s0", Project: "/home/dev/src/billing-api", Kind: "prompt", Title: "spooled", Body: "spooled", CreatedAt: testNow},
	}
	badKind := spooled
	badKind.Obs = &store.Observation{SessionID: "s0", Project: "/home/dev/src/billing-api", Kind: "bogus", Title: "x", Body: "x", CreatedAt: testNow}
	good, err := json.Marshal(spooled)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := json.Marshal(badKind)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d.Cfg.SpoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	content := "garbage line\n" + string(bad) + "\n" + string(good) + "\n"
	if err := os.WriteFile(filepath.Join(d.Cfg.SpoolDir(), "1-1.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, cwd, "current")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := countRows(t, d.Cfg, "observations"); n != 2 {
		t.Errorf("observations = %d, want 2", n)
	}
	if n, err := spool.Pending(d.Cfg.SpoolDir()); err != nil || n != 0 {
		t.Errorf("spool.Pending = %d, %v; want 0", n, err)
	}
	if n, _ := captureErrors(t, d.Cfg); n != 2 {
		t.Errorf("capture errors = %d, want 2", n)
	}
}

func TestHandleSessionStartOutput(t *testing.T) {
	d := testDeps(t)
	var gotOp Op
	d.SessionStartOutput = func(ctx context.Context, st *store.Store, op Op, p Payload) string {
		gotOp = op
		return "hi"
	}
	out, err := handle(t, d, "SessionStart", payloadJSON(t, Payload{SessionID: "s1", Cwd: t.TempDir(), Source: "startup"}))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", out, err)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" || got.HookSpecificOutput.AdditionalContext != "hi" {
		t.Errorf("stdout = %q", out)
	}
	if gotOp.Session.SessionID != "s1" {
		t.Errorf("op passed to SessionStartOutput = %+v", gotOp)
	}
}

func TestHandleSessionStartEmptyOutput(t *testing.T) {
	d := testDeps(t)
	d.SessionStartOutput = func(context.Context, *store.Store, Op, Payload) string { return "" }
	out, err := handle(t, d, "SessionStart", payloadJSON(t, Payload{SessionID: "s1", Cwd: t.TempDir(), Source: "startup"}))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
}

func TestHandleNoOutputForOtherEvents(t *testing.T) {
	d := testDeps(t)
	called := false
	d.SessionStartOutput = func(context.Context, *store.Store, Op, Payload) string { called = true; return "hi" }
	out, err := handle(t, d, "UserPromptSubmit", promptPayload(t, t.TempDir(), "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out != "" || called {
		t.Errorf("stdout = %q, called = %v", out, called)
	}
}

func TestHandleNilLogger(t *testing.T) {
	d := testDeps(t)
	d.Log = nil
	d.Now = nil
	if _, err := handle(t, d, "UserPromptSubmit", "{"); err == nil {
		t.Error("Handle returned nil for malformed JSON")
	}
	d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, t.TempDir(), "ok")); err != nil {
		t.Fatal(err)
	}
}

func TestHandleSessionEndWithinBudget(t *testing.T) {
	d := testDeps(t)
	lockDB(t, d.Cfg)
	start := time.Now()
	if _, err := handle(t, d, "SessionEnd", payloadJSON(t, Payload{SessionID: "s1", Cwd: t.TempDir(), Reason: "exit"})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("SessionEnd took %v, want <= 1.5s", elapsed)
	}
	if n, err := spool.Pending(d.Cfg.SpoolDir()); err != nil || n != 1 {
		t.Errorf("spool.Pending = %d, %v; want 1", n, err)
	}
}

func TestHandleSpoolRedactsFiles(t *testing.T) {
	d := testDeps(t)
	d.StoreOptions = store.Options{BusyTimeout: 50 * time.Millisecond}
	lockDB(t, d.Cfg)
	token := fakeToken()
	p := Payload{
		SessionID:    "s1",
		Cwd:          t.TempDir(),
		ToolName:     "Write",
		ToolInput:    json.RawMessage(`{"file_path":"/home/dev/src/billing-api/` + token + `.env","content":"x"}`),
		ToolResponse: json.RawMessage(`"ok"`),
	}
	if _, err := handle(t, d, "PostToolUse", payloadJSON(t, p)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	content := readSpool(t, d.Cfg)
	if !strings.Contains(content, "billing-api/") {
		t.Fatalf("spool lacks the op: %q", content)
	}
	if strings.Contains(content, token) {
		t.Errorf("spool leaks the token: %q", content)
	}
}

func TestHandleDrainRedactsSpooledOp(t *testing.T) {
	d := testDeps(t)
	token := fakeToken()
	raw := Op{
		Project: store.ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api", Dir: "billing-api-abc123"},
		Session: store.SessionEvent{SessionID: "s0", Project: "/home/dev/src/billing-api", Cwd: "/home/dev/" + token, At: testNow},
		Obs: &store.Observation{SessionID: "s0", Project: "/home/dev/src/billing-api", Kind: "tool", Title: "Edit: " + token, Body: "input: " + token,
			ToolName: "Edit", Files: []string{"/home/dev/" + token}, CreatedAt: testNow},
	}
	if err := spool.Append(d.Cfg.SpoolDir(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := handle(t, d, "UserPromptSubmit", promptPayload(t, t.TempDir(), "current")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if n := countRows(t, d.Cfg, "observations"); n != 2 {
		t.Fatalf("observations = %d, want 2", n)
	}
	var leaks int
	err := rawDB(t, d.Cfg).QueryRow(`SELECT
  (SELECT count(*) FROM observations WHERE instr(title || body || files, ?1) > 0) +
  (SELECT count(*) FROM sessions WHERE instr(coalesce(cwd, ''), ?1) > 0)`, token).Scan(&leaks)
	if err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Errorf("%d rows leak the token", leaks)
	}
}
