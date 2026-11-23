package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const runMainEnv = "MEM_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
	}
	runCommand = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	os.Exit(m.Run())
}

type fakeRunner struct {
	calls []string
	reply func(args []string) ([]byte, error)
}

func (f *fakeRunner) install(t *testing.T) {
	t.Helper()
	prev := runCommand
	runCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
		return f.reply(args)
	}
	t.Cleanup(func() { runCommand = prev })
}

func exitError(msg string) ([]byte, error) {
	return []byte(msg), fmt.Errorf("exit status 1")
}

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"version"}, nil, &out, &errb); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if strings.TrimSpace(out.String()) != "mem dev" {
		t.Fatalf("got %q", out.String())
	}
}

func TestRunUnknown(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"nope"}, nil, &out, &errb); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
