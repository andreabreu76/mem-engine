package applog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenWritesJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	log, closer, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log.Info("hello", "event", "Stop")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mem.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"msg":"hello"`) || !strings.Contains(string(data), `"event":"Stop"`) {
		t.Errorf("mem.log = %q", data)
	}
	if runtime.GOOS == "windows" {
		return
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "mem.log"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestOpenRotatesLargeLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.log")
	if err := os.WriteFile(path, make([]byte, maxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	log, closer, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	log.Info("fresh")
	closer.Close()
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("mem.log.1: %v", err)
	}
	if rotated.Size() != maxSize+1 {
		t.Errorf("mem.log.1 size = %d", rotated.Size())
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Size() >= maxSize {
		t.Errorf("mem.log size = %d after rotation", current.Size())
	}
}

func TestOpenKeepsSmallLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, closer, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	closer.Close()
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("small log was rotated")
	}
}
