package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev"

const usage = `usage: mem <command>

commands:
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
	case "version":
		fmt.Fprintf(stdout, "mem %s\n", version)
		return 0
	default:
		fmt.Fprintf(stderr, "mem: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}
