package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

func runProject(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "mem project: missing subcommand (merge or alias)\n\n%s", usage)
		return 2
	}
	switch args[0] {
	case "merge":
		return runProjectMerge(args[1:], stdin, stdout, stderr)
	case "alias":
		return runProjectAlias(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "mem project: unknown subcommand %q\n\n%s", args[0], usage)
		return 2
	}
}

func runProjectMerge(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var names []string
	yes := false
	for _, arg := range args {
		switch {
		case arg == "--yes" || arg == "-yes":
			yes = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "mem project merge: unknown flag %s\n\n%s", arg, usage)
			return 2
		default:
			names = append(names, arg)
		}
	}
	if len(names) != 2 {
		fmt.Fprintf(stderr, "mem project merge: needs <from> and <to>\n\n%s", usage)
		return 2
	}
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "mem project merge: %v\n", err)
		return 1
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		fmt.Fprintf(stderr, "mem project merge: no mem-engine database: %v\n", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		fmt.Fprintf(stderr, "mem project merge: %v\n", err)
		return 1
	}
	defer st.Close()
	from, err := findProject(ctx, st, names[0])
	if err != nil {
		fmt.Fprintf(stderr, "mem project merge: %v\n", err)
		return 1
	}
	to, err := findProject(ctx, st, names[1])
	if err != nil {
		fmt.Fprintf(stderr, "mem project merge: %v\n", err)
		return 1
	}
	return mergeProjects(ctx, st, cfg, from, to, yes, stdin, stdout, stderr)
}

func mergeProjects(ctx context.Context, st *store.Store, cfg config.Config, from, to project.Project, yes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	switch {
	case from.Key == to.Key:
		fmt.Fprintf(stderr, "mem project merge: %s and %s are the same project\n", from.Key, to.Key)
		return 1
	case from.Key == project.User.Key || to.Key == project.User.Key:
		fmt.Fprintln(stderr, "mem project merge: the user scope cannot be merged")
		return 1
	}
	ms, err := st.ListMemories(ctx, from.Key)
	if err != nil {
		fmt.Fprintf(stderr, "mem project merge: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "merge %s into %s: %d memories, the observations and the sessions of %s move to %s, and %s is removed\n",
		from.Key, to.Key, len(ms), from.Key, to.Key, from.Key)
	if !yes && !confirm(stdin, stdout) {
		fmt.Fprintln(stderr, "mem project merge: aborted, nothing changed")
		return 1
	}
	svc := &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: time.Now}
	memories, err := svc.MoveProject(ctx, from, to)
	if err != nil {
		fmt.Fprintf(stdout, "moved 0 observations, %d memories\n", memories)
		fmt.Fprintf(stderr, "mem project merge: %v\nrun the same command again to continue\n", err)
		return 1
	}
	observations, err := st.MergeProject(ctx, from.Key, to.Key)
	if err != nil {
		fmt.Fprintf(stdout, "moved 0 observations, %d memories\n", memories)
		fmt.Fprintf(stderr, "mem project merge: %v\nrun the same command again to continue\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "moved %d observations, %d memories\n", observations, memories)
	return 0
}

func findProject(ctx context.Context, st *store.Store, nameOrKey string) (project.Project, error) {
	row, err := st.FindProject(ctx, nameOrKey)
	if errors.Is(err, store.ErrNotFound) {
		return project.Project{}, fmt.Errorf("unknown project %q", nameOrKey)
	}
	if err != nil {
		return project.Project{}, err
	}
	return project.Project{Key: row.Key, Name: row.Name, Dir: row.Dir}, nil
}

func runProjectAlias(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintf(stderr, "mem project alias: needs <old-key> and <new-path>\n\n%s", usage)
		return 2
	}
	alias := config.Alias{Key: config.ExpandHome(args[0]), Path: config.ExpandHome(args[1])}
	if !filepath.IsAbs(alias.Key) || !filepath.IsAbs(alias.Path) {
		fmt.Fprintf(stderr, "mem project alias: <old-key> and <new-path> must be absolute paths\n\n%s", usage)
		return 2
	}
	alias.Key, alias.Path = filepath.Clean(alias.Key), filepath.Clean(alias.Path)
	home, err := config.HomeDir()
	if err == nil {
		err = appendAlias(home, alias)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem project alias: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "alias added to %s: %s is recorded as %s\n", filepath.Join(home, "config.toml"), alias.Path, alias.Key)
	return 0
}

func appendAlias(home string, alias config.Alias) error {
	cfg, err := config.LoadFrom(home)
	if err != nil {
		return err
	}
	for _, a := range cfg.Aliases {
		if a.Path == alias.Path {
			return fmt.Errorf("%s already has an alias (key %s)", alias.Path, a.Key)
		}
	}
	var block bytes.Buffer
	enc := toml.NewEncoder(&block)
	enc.Indent = ""
	if err := enc.Encode(struct {
		Alias []config.Alias `toml:"alias"`
	}{[]config.Alias{alias}}); err != nil {
		return err
	}
	path := filepath.Join(home, "config.toml")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	prefix := ""
	if len(existing) > 0 {
		prefix = "\n"
		if !bytes.HasSuffix(existing, []byte("\n")) {
			prefix = "\n\n"
		}
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(prefix + block.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
