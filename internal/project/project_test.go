package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/andreabreu76/mem-engine/internal/config"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=dev", "-c", "user.email=dev@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func initRepo(t *testing.T, dir string) {
	t.Helper()
	mkdir(t, dir)
	git(t, dir, "init", "-q")
	git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func resolve(t *testing.T, cwd string, cfg config.Config) Project {
	t.Helper()
	p, err := Resolve(cwd, cfg)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", cwd, err)
	}
	return p
}

func TestResolveGitRoot(t *testing.T) {
	requireGit(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	cwd := filepath.Join(repo, "a", "b")
	mkdir(t, cwd)

	p := resolve(t, cwd, config.Config{})
	if want := realPath(t, repo); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
	if p.Name != "repo" {
		t.Errorf("Name = %q, want repo", p.Name)
	}
	if want := DirName("repo", p.Key); p.Dir != want {
		t.Errorf("Dir = %q, want %q", p.Dir, want)
	}
}

func TestResolveWorktreeSharesKey(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	initRepo(t, repo)
	wt := filepath.Join(root, "elsewhere", "feature")
	git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
	cwd := filepath.Join(wt, "sub")
	mkdir(t, cwd)

	main := resolve(t, repo, config.Config{})
	got := resolve(t, cwd, config.Config{})
	if got.Key != main.Key {
		t.Errorf("worktree Key = %q, want %q", got.Key, main.Key)
	}
	if got.Name != "repo" {
		t.Errorf("worktree Name = %q, want repo", got.Name)
	}
}

func TestResolveBareWorktreeKeepsRepoIdentity(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	initRepo(t, src)
	bare := filepath.Join(root, "billing-api.git")
	git(t, root, "clone", "-q", "--bare", src, bare)
	wt := filepath.Join(root, "wt")
	git(t, bare, "worktree", "add", "-q", wt)

	p := resolve(t, wt, config.Config{})
	if want := realPath(t, bare); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
}

func TestResolveNoGit(t *testing.T) {
	cwd := t.TempDir()
	p := resolve(t, cwd, config.Config{})
	if want := realPath(t, cwd); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
	if want := filepath.Base(cwd); p.Name != want {
		t.Errorf("Name = %q, want %q", p.Name, want)
	}
}

func TestResolveGroupLongestPrefix(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "src", "acme", "infra", "x")
	mkdir(t, cwd)
	cfg := config.Config{Groups: []config.Group{
		{Name: "acme", Prefix: filepath.Join(root, "src", "acme")},
		{Name: "acme-infra", Prefix: filepath.Join(root, "src", "acme", "infra")},
	}}

	p := resolve(t, cwd, cfg)
	if p.Name != "acme-infra" {
		t.Errorf("Name = %q, want acme-infra", p.Name)
	}
	if want := realPath(t, filepath.Join(root, "src", "acme", "infra")); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
}

func TestResolveGroupMatchesOnPathBoundary(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "src", "acme"))
	cwd := filepath.Join(root, "src", "acme-infra")
	mkdir(t, cwd)
	cfg := config.Config{Groups: []config.Group{{Name: "acme", Prefix: filepath.Join(root, "src", "acme")}}}

	p := resolve(t, cwd, cfg)
	if p.Name != "acme-infra" {
		t.Errorf("Name = %q, want acme-infra", p.Name)
	}
	if want := realPath(t, cwd); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
}

func TestResolveIgnoresRelativeGroupAndAlias(t *testing.T) {
	cwd := t.TempDir()
	cfg := config.Config{
		Groups:  []config.Group{{Name: "everything", Prefix: "."}},
		Aliases: []config.Alias{{Path: ".", Key: "/home/dev/src/other"}},
	}
	t.Chdir(cwd)

	p := resolve(t, cwd, cfg)
	if p.Name == "everything" {
		t.Errorf("group with relative prefix matched")
	}
	if want := realPath(t, cwd); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
}

func TestResolveAlias(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	renamed := filepath.Join(root, "billing-api")
	initRepo(t, renamed)
	old := filepath.Join(root, "billing")
	cfg := config.Config{Aliases: []config.Alias{{Path: renamed, Key: old}}}

	p := resolve(t, renamed, cfg)
	if p.Key != old {
		t.Errorf("Key = %q, want %q", p.Key, old)
	}
	if want := DirName(p.Name, old); p.Dir != want {
		t.Errorf("Dir = %q, want %q", p.Dir, want)
	}
}

func TestResolveSymlinkStable(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	initRepo(t, repo)
	mkdir(t, filepath.Join(repo, "a"))
	link := filepath.Join(root, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}

	direct := resolve(t, filepath.Join(repo, "a"), config.Config{})
	viaLink := resolve(t, filepath.Join(link, "a"), config.Config{})
	if viaLink.Key != direct.Key {
		t.Errorf("Key via symlink = %q, want %q", viaLink.Key, direct.Key)
	}
}

func TestResolveMissingCwd(t *testing.T) {
	requireGit(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	p := resolve(t, filepath.Join(repo, "does", "not", "exist"), config.Config{})
	if want := realPath(t, repo); p.Key != want {
		t.Errorf("Key = %q, want %q", p.Key, want)
	}
}

func TestResolveInsideDotGit(t *testing.T) {
	requireGit(t)
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)

	for _, cwd := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".git", "hooks")} {
		p := resolve(t, cwd, config.Config{})
		if want := realPath(t, repo); p.Key != want {
			t.Errorf("Resolve(%q).Key = %q, want %q", cwd, p.Key, want)
		}
		if p.Name != "repo" {
			t.Errorf("Resolve(%q).Name = %q, want repo", cwd, p.Name)
		}
	}
}

func TestDirName(t *testing.T) {
	got := DirName("Billing API", "/home/dev/src/billing-api")
	if !regexp.MustCompile(`^billing-api-[0-9a-f]{6}$`).MatchString(got) {
		t.Errorf("DirName = %q, want billing-api-xxxxxx", got)
	}
	if again := DirName("Billing API", "/home/dev/src/billing-api"); again != got {
		t.Errorf("DirName not deterministic: %q vs %q", got, again)
	}
	if other := DirName("Billing API", "/home/dev/src/billing"); other == got {
		t.Errorf("DirName ignores key: %q", other)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Billing API":     "billing-api",
		"--acme__infra--": "acme-infra",
		"a  b..c":         "a-b-c",
		"":                "",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	long := Slug(strings.Repeat("ab ", 30))
	if len(long) > 40 || strings.HasSuffix(long, "-") {
		t.Errorf("Slug long = %q (len %d)", long, len(long))
	}
}

func TestUser(t *testing.T) {
	if User != (Project{Key: "user", Name: "user", Dir: "user"}) {
		t.Errorf("User = %+v", User)
	}
}
