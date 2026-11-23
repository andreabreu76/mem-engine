package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

var version = "dev"

var runCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

const usage = `usage: mem <command>

commands:
  bench     compare the native MEMORY.md load with the mem-engine handoff ([path] [--native file])
  doctor    check the binary, data dir, database, spool, capture errors and hooks
  gc        delete observations older than retention_days
  hook      record a Claude Code hook event read from stdin
  mcp       serve the memory tools to Claude Code over stdio (MCP)
  reindex   rebuild the memory index from the wiki
  setup     register the mem hooks and the mem-engine MCP server (--plan, --yes)
  version   print the mem version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "bench":
		return runBench(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "gc":
		return runGC(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdin, stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stderr)
	case "reindex":
		return runReindex(args[1:], stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdin, stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "mem %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "mem: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
