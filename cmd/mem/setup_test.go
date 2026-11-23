package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andreabreu76/mem-engine/internal/setup"
)

func setUserHome(t *testing.T) string {
	t.Helper()
	setHome(t)
	home := os.Getenv("HOME")
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".claude", "settings.json")
}

func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSetupPlanWritesNothing(t *testing.T) {
	path := setUserHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--plan"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), path) || !strings.Contains(out.String(), `"command": "mem hook SessionStart"`) {
		t.Errorf("stdout = %q", out.String())
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("--plan created %s: %v", filepath.Dir(path), err)
	}

	old := `{"model": "opus"}`
	writeSettings(t, path, old)
	out.Reset()
	if code := run([]string{"setup", "--plan"}, strings.NewReader("y\n"), &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Errorf("settings changed by --plan: %s", data)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("--plan wrote a backup: %v", err)
	}
}

func TestSetupYesWritesBackup(t *testing.T) {
	path := setUserHome(t)
	old := `{"model": "opus"}`
	writeSettings(t, path, old)
	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--yes"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errb.String())
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != old {
		t.Fatalf("backup = %q, err %v", bak, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !setup.HasMemHooks(data) || !strings.Contains(string(data), `"model": "opus"`) {
		t.Errorf("settings = %s", data)
	}

	out.Reset()
	if code := run([]string{"setup", "--yes"}, nil, &out, &errb); code != 0 {
		t.Fatalf("second run code = %d, stderr = %q", code, errb.String())
	}
	if !strings.Contains(out.String(), "already configured") {
		t.Errorf("second run stdout = %q", out.String())
	}
	if again, _ := os.ReadFile(path); !bytes.Equal(again, data) {
		t.Errorf("second run rewrote settings")
	}
	if bak2, _ := os.ReadFile(path + ".bak"); string(bak2) != old {
		t.Errorf("second run overwrote backup: %q", bak2)
	}
}

func TestSetupPromptDeclined(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "", "yess\n"} {
		path := setUserHome(t)
		old := `{"model": "opus"}`
		writeSettings(t, path, old)
		var out, errb bytes.Buffer
		if code := run([]string{"setup"}, strings.NewReader(answer), &out, &errb); code != 1 {
			t.Errorf("answer %q: code = %d, want 1", answer, code)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("answer %q: stdout = %q", answer, out.String())
		}
		if data, _ := os.ReadFile(path); string(data) != old {
			t.Errorf("answer %q: settings changed: %s", answer, data)
		}
		if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
			t.Errorf("answer %q: backup written", answer)
		}
	}
}

func TestSetupPromptAccepted(t *testing.T) {
	for _, answer := range []string{"y\n", "YES\n", " Yes \r\n", "y"} {
		path := setUserHome(t)
		var out, errb bytes.Buffer
		if code := run([]string{"setup"}, strings.NewReader(answer), &out, &errb); code != 0 {
			t.Fatalf("answer %q: code = %d, stderr = %q", answer, code, errb.String())
		}
		data, err := os.ReadFile(path)
		if err != nil || !setup.HasMemHooks(data) {
			t.Errorf("answer %q: settings = %s, err %v", answer, data, err)
		}
		if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
			t.Errorf("answer %q: backup written for a new file", answer)
		}
	}
}

func TestSetupInvalidSettings(t *testing.T) {
	path := setUserHome(t)
	old := `{"model": `
	writeSettings(t, path, old)
	var out, errb bytes.Buffer
	if code := run([]string{"setup", "--yes"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if errb.Len() == 0 {
		t.Error("stderr empty")
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Errorf("settings changed: %s", data)
	}
}

func TestSetupRejectsArgs(t *testing.T) {
	setUserHome(t)
	for _, args := range [][]string{{"setup", "now"}, {"setup", "--bogus"}} {
		var out, errb bytes.Buffer
		if code := run(args, nil, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
	}
}
