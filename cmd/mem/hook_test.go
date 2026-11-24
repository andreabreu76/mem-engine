package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/capture"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func setHome(t *testing.T) string {
	t.Helper()
	userProfile := t.TempDir()
	t.Setenv("HOME", userProfile)
	t.Setenv("USERPROFILE", userProfile)
	home := t.TempDir()
	t.Setenv("MEM_ENGINE_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return home
}

func TestHookCommandAlwaysZero(t *testing.T) {
	home := setHome(t)
	cases := [][]string{
		{"hook", "PostToolUse"},
		{"hook", "NoSuchEvent"},
		{"hook"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, strings.NewReader(`{"session_id": "s1", `), &out, &errb); code != 0 {
			t.Errorf("run(%v) = %d, want 0", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("run(%v) stdout = %q", args, out.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(home, "logs", "mem.log"))
	if err != nil {
		t.Fatalf("read mem.log: %v", err)
	}
	if !strings.Contains(string(data), "PostToolUse") {
		t.Errorf("mem.log = %q", data)
	}
}

func TestHookCommandBrokenConfig(t *testing.T) {
	home := setHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("retention_days = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"hook", "UserPromptSubmit"}, strings.NewReader(`{"session_id":"s1","prompt":"hi"}`), &out, &errb); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(home, "mem.db")); err == nil {
		t.Error("captured with a broken config")
	}
	data, err := os.ReadFile(filepath.Join(home, "logs", "mem.log"))
	if err != nil {
		t.Fatalf("read mem.log: %v", err)
	}
	if !strings.Contains(string(data), "config") {
		t.Errorf("mem.log = %q", data)
	}
}

func TestHookSessionStartInjectsHandoff(t *testing.T) {
	setHome(t)
	cwd := t.TempDir()
	hook := func(event, payload string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := run([]string{"hook", event}, strings.NewReader(payload), &out, &errb); code != 0 {
			t.Fatalf("hook %s = %d", event, code)
		}
		return out.String()
	}
	payload := func(fields map[string]string) string {
		t.Helper()
		fields["cwd"] = cwd
		data, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	hook("SessionStart", payload(map[string]string{"session_id": "s1", "source": "startup"}))
	hook("UserPromptSubmit", payload(map[string]string{"session_id": "s1", "prompt": "fix the retry loop in payments"}))
	hook("Stop", payload(map[string]string{"session_id": "s1", "last_assistant_message": "retry now backs off"}))

	out := hook("SessionStart", payload(map[string]string{"session_id": "s2", "source": "startup"}))
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("stdout %q is not JSON: %v", out, err)
	}
	text := got.HookSpecificOutput.AdditionalContext
	if got.HookSpecificOutput.HookEventName != "SessionStart" ||
		!strings.Contains(text, `asked "fix the retry loop in payments"`) ||
		!strings.Contains(text, `stopped at "retry now backs off"`) ||
		!strings.HasPrefix(text, "mem-engine | project "+filepath.Base(cwd)) {
		t.Errorf("stdout = %q", out)
	}
}

func TestSessionStartOutputFallsBackOnError(t *testing.T) {
	setHome(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	op := capture.Op{
		Project: store.ProjectRow{Key: "/home/dev/src/billing-api", Name: "billing-api"},
		Session: store.SessionEvent{SessionID: "s2", Source: "startup"},
	}
	got := sessionStartOutput(log)(context.Background(), st, op, capture.Payload{SessionID: "s2", Prompt: "secret prompt text"})
	want := "mem-engine | project billing-api\nLookup: search -> timeline -> get (memory tools of this project)."
	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "build handoff failed") || strings.Contains(logs.String(), "secret prompt text") {
		t.Errorf("log = %q", logs.String())
	}
}

func TestHookCommandPanicRecordsCaptureError(t *testing.T) {
	home := setHome(t)
	orig := handleHook
	t.Cleanup(func() { handleHook = orig })
	handleHook = func(context.Context, string, io.Reader, io.Writer, capture.Deps) error { panic("boom") }

	var out, errb bytes.Buffer
	if code := run([]string{"hook", "UserPromptSubmit"}, strings.NewReader(`{}`), &out, &errb); code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, last, err := st.CaptureErrorsSince(context.Background(), time.Unix(0, 0))
	if err != nil || n != 1 || last != "hook panicked" {
		t.Errorf("capture errors = %d, %q, %v", n, last, err)
	}
}
