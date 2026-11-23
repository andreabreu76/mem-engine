package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/andreabreu76/mem-engine/internal/setup"
)

const claudeTimeout = time.Minute

func runSetup(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plan := fs.Bool("plan", false, "print the changes without applying them")
	yes := fs.Bool("yes", false, "apply without asking for confirmation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "mem setup: unexpected arguments\n\n%s", usage)
		return 2
	}
	path, err := setup.SettingsPath()
	if err != nil {
		fmt.Fprintf(stderr, "mem setup: %v\n", err)
		return 1
	}
	current, exists, err := setup.ReadSettings(path)
	if err != nil {
		fmt.Fprintf(stderr, "mem setup: %v\n", err)
		return 1
	}
	updated, changed, err := setup.MergeHooks(current)
	if err != nil {
		fmt.Fprintf(stderr, "mem setup: %s: %v\n", path, err)
		return 1
	}
	if changed {
		hooks, err := json.MarshalIndent(setup.HooksConfig(), "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "mem setup: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "settings: %s\nhooks to register (existing \"mem hook\" entries are replaced, other keys and hooks are kept):\n%s\n", path, hooks)
	} else {
		fmt.Fprintf(stdout, "hooks already configured in %s\n", path)
	}
	addCmd := "claude " + strings.Join(setup.MCPAddArgs(), " ")
	if *plan {
		fmt.Fprintf(stdout, "MCP server to register (skipped if already registered):\n  %s\n", addCmd)
		return 0
	}
	register := false
	switch _, err := runClaude(setup.MCPGetArgs()); {
	case err == nil:
		fmt.Fprintf(stdout, "MCP server %s already registered\n", setup.MCPServerName)
	case errors.Is(err, exec.ErrNotFound):
		fmt.Fprintf(stderr, "mem setup: claude not found in PATH, register the MCP server later with:\n  %s\n", addCmd)
	default:
		register = true
		fmt.Fprintf(stdout, "MCP server to register:\n  %s\n", addCmd)
	}
	if !changed && !register {
		return 0
	}
	if !*yes && !confirm(stdin, stdout) {
		fmt.Fprintln(stderr, "mem setup: aborted, nothing written")
		return 1
	}
	if changed {
		if err := setup.WriteSettings(path, updated); err != nil {
			fmt.Fprintf(stderr, "mem setup: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "updated %s\n", path)
		if exists {
			fmt.Fprintf(stdout, "backup: %s.bak\n", path)
		}
	}
	if register {
		if out, err := runClaude(setup.MCPAddArgs()); err != nil {
			fmt.Fprintf(stderr, "mem setup: %s: %v\n%s", addCmd, err, out)
			return 1
		}
		fmt.Fprintf(stdout, "registered MCP server %s\n", setup.MCPServerName)
	}
	return 0
}

func runClaude(args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), claudeTimeout)
	defer cancel()
	return runCommand(ctx, "claude", args...)
}

func confirm(stdin io.Reader, stdout io.Writer) bool {
	fmt.Fprint(stdout, "apply? [y/N] ")
	if stdin == nil {
		return false
	}
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
