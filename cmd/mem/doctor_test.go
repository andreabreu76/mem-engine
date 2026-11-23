package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var doctorLine = regexp.MustCompile(`^(ok  |warn|FAIL)  \S+ *  .+$`)

func TestDoctorCommand(t *testing.T) {
	home := setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"doctor"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, out.String(), errb.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 9 {
		t.Fatalf("got %d lines: %q", len(lines), out.String())
	}
	for _, l := range lines {
		if !doctorLine.MatchString(l) {
			t.Errorf("malformed line %q", l)
		}
	}
	if !strings.Contains(out.String(), "run mem setup") {
		t.Errorf("stdout = %q, want hooks warning", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "mem.db")); !os.IsNotExist(err) {
		t.Errorf("doctor created mem.db: %v", err)
	}
}

func TestDoctorCommandFailExitsOne(t *testing.T) {
	home := setHome(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("retention_days = ["), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"doctor"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !regexp.MustCompile(`(?m)^FAIL  config `).MatchString(out.String()) {
		t.Errorf("stdout = %q, want FAIL config line", out.String())
	}
}

func TestDoctorCommandRejectsArgs(t *testing.T) {
	setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"doctor", "now"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
