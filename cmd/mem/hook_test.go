package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setHome(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("MEM_ENGINE_HOME", home)
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
