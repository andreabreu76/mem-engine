package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/store"
)

func TestMCPStdioSmoke(t *testing.T) {
	home := setHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "mcp")
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "CLAUDE_PROJECT_DIR="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(msg string) {
		t.Helper()
		if _, err := io.WriteString(stdin, msg+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	read := func() map[string]any {
		t.Helper()
		if !lines.Scan() {
			t.Fatalf("no response: %v, stderr = %q", lines.Err(), stderr.String())
		}
		var msg map[string]any
		if err := json.Unmarshal(lines.Bytes(), &msg); err != nil {
			t.Fatalf("stdout line %q is not JSON: %v", lines.Text(), err)
		}
		return msg
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`)
	init := read()
	info, _ := init["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != "mem-engine" {
		t.Fatalf("initialize = %v", init)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := read()
	tools, _ := list["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	sort.Strings(names)
	if got := strings.Join(names, " "); got != "forget get save search timeline update" {
		t.Errorf("tools = %q", got)
	}

	stdin.Close()
	if lines.Scan() {
		t.Errorf("unexpected stdout after tools/list: %q", lines.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("mem mcp exit: %v, stderr = %q", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "mem.db")); err != nil {
		t.Errorf("mem.db: %v", err)
	}
}

func TestMCPSchemaTooNew(t *testing.T) {
	home := setHome(t)
	st, err := store.Open(filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	db, err := sql.Open("sqlite", filepath.Join(home, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"mcp"}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "upgrade mem") {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}

func TestMCPRejectsArgs(t *testing.T) {
	setHome(t)
	var out, errb bytes.Buffer
	if code := run([]string{"mcp", "now"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
