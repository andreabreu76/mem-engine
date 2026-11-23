package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andreabreu76/mem-engine/internal/bench"
	"github.com/andreabreu76/mem-engine/internal/config"
	"github.com/andreabreu76/mem-engine/internal/importer"
	"github.com/andreabreu76/mem-engine/internal/memory"
	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
)

type importArgs struct {
	dir, project string
	user, dryRun bool
}

func runImport(args []string, stdout, stderr io.Writer) int {
	a, err := parseImportArgs(args)
	if err == nil && !a.user && a.project == "" {
		a.user, err = isHomeMemoryDir(a.dir)
	}
	if err != nil {
		fmt.Fprintf(stderr, "mem import: %v\n\n%s", err, usage)
		return 2
	}
	rep, target, err := importNative(a)
	if err != nil {
		fmt.Fprintf(stderr, "mem import: %v\n", err)
		return 1
	}
	if a.dryRun {
		fmt.Fprintf(stdout, "dry run: would import %d into %s, skip %d\n", rep.Imported, target.Key, rep.Skipped)
		for _, title := range rep.Titles {
			fmt.Fprintf(stdout, "  %s\n", title)
		}
	} else {
		fmt.Fprintf(stdout, "imported %d, skipped %d\n", rep.Imported, rep.Skipped)
	}
	for _, u := range rep.Unresolved {
		fmt.Fprintf(stdout, "unresolved %s\n", u)
	}
	for _, n := range rep.Notes {
		fmt.Fprintln(stdout, n)
	}
	return 0
}

func parseImportArgs(args []string) (importArgs, error) {
	var a importArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--user" || arg == "-user":
			a.user = true
		case arg == "--dry-run" || arg == "-dry-run":
			a.dryRun = true
		case arg == "--project" || arg == "-project":
			if i+1 >= len(args) {
				return a, errors.New("--project needs a path or name")
			}
			i++
			a.project = args[i]
		case strings.HasPrefix(arg, "--project="):
			a.project = strings.TrimPrefix(arg, "--project=")
			if a.project == "" {
				return a, errors.New("--project needs a path or name")
			}
		case strings.HasPrefix(arg, "-"):
			return a, fmt.Errorf("unknown flag %s", arg)
		case a.dir != "":
			return a, errors.New("unexpected arguments")
		default:
			a.dir = arg
		}
	}
	switch {
	case a.dir == "":
		return a, errors.New("import needs the native memory directory")
	case a.user && a.project != "":
		return a, errors.New("--user and --project are exclusive")
	}
	return a, nil
}

func isHomeMemoryDir(dir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err == nil {
		native, statErr := os.Stat(filepath.Dir(bench.NativeMemoryPath(home, home)))
		given, givenErr := os.Stat(dir)
		if statErr == nil && givenErr == nil && os.SameFile(native, given) {
			return true, nil
		}
	}
	return false, fmt.Errorf("%s is not the native memory dir of your home: pass --project <path-or-name> or --user", dir)
}

func importNative(a importArgs) (importer.Report, project.Project, error) {
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		return importer.Report{}, project.Project{}, err
	}
	var st *store.Store
	_, statErr := os.Stat(cfg.DBPath())
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return importer.Report{}, project.Project{}, statErr
	}
	if !a.dryRun || statErr == nil {
		if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
			return importer.Report{}, project.Project{}, err
		}
		if st, err = store.Open(cfg.DBPath()); err != nil {
			return importer.Report{}, project.Project{}, err
		}
		defer st.Close()
	}
	target, err := importTarget(ctx, a, cfg, st)
	if err != nil {
		return importer.Report{}, project.Project{}, err
	}
	svc := &memory.Service{Store: st, WikiRoot: cfg.WikiDir(), Now: time.Now}
	rep, err := importer.Run(ctx, svc, st, importer.Options{Dir: a.dir, Target: target, DryRun: a.dryRun})
	return rep, target, err
}

func importTarget(ctx context.Context, a importArgs, cfg config.Config, st *store.Store) (project.Project, error) {
	if a.user {
		return project.User, nil
	}
	if info, err := os.Stat(a.project); err == nil && info.IsDir() {
		return project.Resolve(a.project, cfg)
	}
	if st == nil {
		return project.Project{}, fmt.Errorf("unknown project %q: pass an existing path", a.project)
	}
	row, err := st.FindProject(ctx, a.project)
	if errors.Is(err, store.ErrNotFound) {
		return project.Project{}, fmt.Errorf("unknown project %q: pass an existing path", a.project)
	}
	if err != nil {
		return project.Project{}, err
	}
	return project.Project{Key: row.Key, Name: row.Name, Dir: row.Dir}, nil
}
