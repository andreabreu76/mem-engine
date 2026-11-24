package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"

	"github.com/andreabreu76/mem-engine/internal/bench"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func runBench(args []string, stdout, stderr io.Writer) int {
	path, native, err := parseBenchArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "mem bench: %v\n\n%s", err, usage)
		return 2
	}
	r, err := benchmark(path, native)
	if err != nil {
		fmt.Fprintf(stderr, "mem bench: %v\n", err)
		return 1
	}
	nativeTokens, handoffTokens, openTokens := r.Tokens()
	if r.NativeBytes == 0 {
		fmt.Fprintf(stdout, "native MEMORY.md: not found at %s\n", r.NativePath)
	} else {
		fmt.Fprintf(stdout, "native MEMORY.md: %d bytes loaded (~%d tokens)\n", r.NativeBytes, nativeTokens)
	}
	fmt.Fprintf(stdout, "mem-engine handoff: %d bytes (~%d tokens)\n", r.HandoffBytes, handoffTokens)
	fmt.Fprintf(stdout, "mcp tools + skill: %d bytes (~%d tokens)\n", r.SessionOpenBytes, openTokens)
	if r.NativeBytes == 0 {
		return 0
	}
	memBytes := r.HandoffBytes + r.SessionOpenBytes
	percent := int(math.Round(float64(r.NativeBytes-memBytes) * 100 / float64(r.NativeBytes)))
	fmt.Fprintf(stdout, "saving: ~%d tokens per session (%d%%)\n", nativeTokens-r.MemEngineTokens(), percent)
	return 0
}

func parseBenchArgs(args []string) (path, native string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--native" || a == "-native":
			if i+1 >= len(args) {
				return "", "", errors.New("--native needs a file")
			}
			i++
			native = args[i]
		case strings.HasPrefix(a, "--native="):
			native = strings.TrimPrefix(a, "--native=")
		case strings.HasPrefix(a, "-"):
			return "", "", fmt.Errorf("unknown flag %s", a)
		case path != "":
			return "", "", errors.New("unexpected arguments")
		default:
			path = a
		}
	}
	return path, native, nil
}

func benchmark(path, native string) (bench.Result, error) {
	if path == "" {
		wd, err := os.Getwd()
		if err != nil {
			return bench.Result{}, err
		}
		path = wd
	}
	if _, err := os.Stat(path); err != nil {
		return bench.Result{}, err
	}
	cfg, err := config.Load()
	if err != nil {
		return bench.Result{}, err
	}
	p, err := project.Resolve(path, cfg)
	if err != nil {
		return bench.Result{}, err
	}
	if native == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return bench.Result{}, err
		}
		repo, err := project.Resolve(path, config.Config{})
		if err != nil {
			return bench.Result{}, err
		}
		native = bench.NativeMemoryPath(userHome, repo.Key)
	}
	var st *store.Store
	if _, err := os.Stat(cfg.DBPath()); err == nil {
		if st, err = store.Open(cfg.DBPath()); err != nil {
			return bench.Result{}, err
		}
		defer st.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return bench.Result{}, err
	}
	return bench.Run(context.Background(), st, p, native)
}
