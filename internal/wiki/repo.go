package wiki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type Repo struct {
	Dir string
}

const (
	lockFile   = ".mem.lock"
	hooksDir   = "mem-engine-hooks"
	lockRetry  = 50 * time.Millisecond
	memoryExt  = ".md"
	commitUser = "mem-engine"
	commitMail = "mem-engine@localhost"
)

func OpenRepo(wikiRoot, dirName string) (*Repo, error) {
	if dirName == "" || dirName == "." || dirName == ".." || strings.ContainsAny(dirName, `/\`) {
		return nil, fmt.Errorf("wiki: invalid directory name %q", dirName)
	}
	dir, err := filepath.Abs(filepath.Join(wikiRoot, dirName))
	if err != nil {
		return nil, fmt.Errorf("wiki: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("wiki: create %s: %w", dir, err)
	}
	return &Repo{Dir: dir}, nil
}

func (r *Repo) Lock(ctx context.Context) (func(), error) {
	fl := flock.New(filepath.Join(r.Dir, lockFile), flock.SetPermissions(0o600))
	locked, err := fl.TryLockContext(ctx, lockRetry)
	if err != nil {
		return nil, fmt.Errorf("wiki: lock %s: %w", r.Dir, err)
	}
	if !locked {
		return nil, fmt.Errorf("wiki: lock %s: not acquired", r.Dir)
	}
	return func() { _ = fl.Unlock() }, nil
}

func (r *Repo) UniqueName(slug string) string {
	taken := map[string]bool{}
	if entries, err := os.ReadDir(r.Dir); err == nil {
		for _, e := range entries {
			taken[strings.ToLower(e.Name())] = true
		}
	}
	name := slug + memoryExt
	for n := 2; taken[strings.ToLower(name)]; n++ {
		name = slug + "-" + strconv.Itoa(n) + memoryExt
	}
	return name
}

func (r *Repo) Write(name string, data []byte) error {
	if err := checkName(name); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(r.Dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("wiki: write %s: %w", name, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("wiki: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("wiki: write %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("wiki: write %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(r.Dir, name)); err != nil {
		return fmt.Errorf("wiki: write %s: %w", name, err)
	}
	return nil
}

func (r *Repo) Remove(name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(r.Dir, name)); err != nil {
		return fmt.Errorf("wiki: remove %s: %w", name, err)
	}
	return nil
}

func (r *Repo) Commit(msg string, paths ...string) error {
	if len(paths) == 0 {
		return errors.New("wiki: commit without paths")
	}
	if err := r.ensureGit(); err != nil {
		return err
	}
	stage, err := r.stageable(paths)
	if err != nil || len(stage) == 0 {
		return err
	}
	if err := r.git(append([]string{"add", "--all", "--"}, stage...)...); err != nil {
		return err
	}
	changed, err := r.stagedChanges(stage)
	if err != nil || !changed {
		return err
	}
	args := []string{
		"-c", "user.name=" + commitUser,
		"-c", "user.email=" + commitMail,
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "--no-verify", "-m", msg, "--",
	}
	return r.git(append(args, stage...)...)
}

func (r *Repo) stagedChanges(paths []string) (bool, error) {
	_, err := r.run(append([]string{"diff", "--cached", "--quiet", "--"}, paths...)...)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return false, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		return true, nil
	default:
		return false, err
	}
}

func (r *Repo) stageable(paths []string) ([]string, error) {
	out, err := r.run(append([]string{"ls-files", "-z", "--"}, paths...)...)
	if err != nil {
		return nil, err
	}
	tracked := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		tracked[p] = true
	}
	var stage []string
	for _, p := range paths {
		if _, err := os.Lstat(filepath.Join(r.Dir, p)); err == nil || tracked[p] {
			stage = append(stage, p)
		}
	}
	return stage, nil
}

func (r *Repo) List() ([]string, error) {
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, fmt.Errorf("wiki: list %s: %w", r.Dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), memoryExt) && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (r *Repo) ensureGit() error {
	if _, err := os.Stat(filepath.Join(r.Dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := r.git("-c", "init.defaultBranch=main", "init", "--quiet"); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.Dir, ".gitignore"), []byte(lockFile+"\n.tmp-*\n"), 0o600); err != nil {
			return fmt.Errorf("wiki: write .gitignore: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("wiki: %w", err)
	}
	if err := os.MkdirAll(r.hooksPath(), 0o700); err != nil {
		return fmt.Errorf("wiki: create hooks dir: %w", err)
	}
	return nil
}

func (r *Repo) hooksPath() string {
	return filepath.Join(r.Dir, ".git", hooksDir)
}

func (r *Repo) git(args ...string) error {
	_, err := r.run(args...)
	return err
}

func (r *Repo) run(args ...string) ([]byte, error) {
	full := append([]string{"-C", r.Dir, "--literal-pathspecs", "-c", "core.hooksPath=" + r.hooksPath()}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = append(gitEnv(), "GIT_CEILING_DIRECTORIES="+filepath.Dir(r.Dir))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(strings.TrimSpace(stdout.String()) + "\n" + strings.TrimSpace(stderr.String()))
		return nil, fmt.Errorf("wiki: git %s: %w: %s", subcommand(args), err, output)
	}
	return stdout.Bytes(), nil
}

func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_CEILING_DIRECTORIES", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR", "GIT_NAMESPACE":
			continue
		}
		env = append(env, kv)
	}
	return env
}

func checkName(name string) error {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, memoryExt) {
		return fmt.Errorf("wiki: invalid file name %q", name)
	}
	return nil
}
