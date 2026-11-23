package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreabreu76/mem-engine/internal/store"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func startUI(t *testing.T, args ...string) (*exec.Cmd, *bufio.Scanner, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"ui"}, args...)...)
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd, bufio.NewScanner(stdout), stderr
}

func getBody(t *testing.T, target string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

func TestUIServesViewer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs SIGINT")
	}
	setHome(t)
	port := freePort(t)
	cmd, stdout, stderr := startUI(t, "--port", strconv.Itoa(port))
	if !stdout.Scan() {
		t.Fatalf("no URL printed: %v, stderr = %q", stdout.Err(), stderr.String())
	}
	line := stdout.Text()
	m := regexp.MustCompile(`^mem ui: http://127\.0\.0\.1:` + strconv.Itoa(port) + `/\?token=([0-9a-f]{64})$`).FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("stdout line = %q", line)
	}
	token := m[1]
	base := "http://127.0.0.1:" + strconv.Itoa(port)

	resp, body := getBody(t, base+"/?token="+url.QueryEscape(token), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `id="feed"`) {
		t.Fatalf("GET / = %d %q", resp.StatusCode, body)
	}
	resp, body = getBody(t, base+"/", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET / without token = %d %q", resp.StatusCode, body)
	}
	resp, body = getBody(t, base+"/assets/app.js", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || body == "" {
		t.Fatalf("GET /assets/app.js = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	resp, body = getBody(t, base+"/api/health", http.Header{"X-Mem-Token": {token}})
	var health map[string]any
	if resp.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &health) != nil {
		t.Fatalf("GET /api/health = %d %q", resp.StatusCode, body)
	}
	if _, ok := health["spool_pending"]; !ok {
		t.Fatalf("health = %v", health)
	}
	if conn, err := net.DialTimeout("tcp", "[::1]:"+strconv.Itoa(port), time.Second); err == nil {
		conn.Close()
		t.Error("mem ui accepts connections on ::1, want 127.0.0.1 only")
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("exit after SIGINT: %v, stderr = %q", err, stderr.String())
	}
	if stdout.Scan() {
		t.Errorf("extra stdout after shutdown: %q", stdout.Text())
	}
}

func TestUIPortInUse(t *testing.T) {
	setHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	cmd, stdout, stderr := startUI(t, "--port", port)
	if stdout.Scan() {
		t.Errorf("stdout = %q, want nothing", stdout.Text())
	}
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want code 1", err)
	}
	if !strings.Contains(stderr.String(), "port "+port+" is already in use") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestUISchemaTooNew(t *testing.T) {
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
	if code := run([]string{"ui", "--port", strconv.Itoa(freePort(t))}, nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "upgrade mem") {
		t.Errorf("stdout = %q, stderr = %q", out.String(), errb.String())
	}
}

func TestUIRejectsBadArgs(t *testing.T) {
	setHome(t)
	for _, args := range [][]string{{"ui", "now"}, {"ui", "--port", "0"}, {"ui", "--port", "70000"}, {"ui", "--port", "x"}} {
		var out, errb bytes.Buffer
		if code := run(args, nil, &out, &errb); code != 2 {
			t.Errorf("run(%v) = %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("run(%v) stdout = %q", args, out.String())
		}
	}
}
