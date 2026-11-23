package project

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"github.com/andreabreu76/mem-engine/internal/config"
)

type Project struct {
	Key, Name, Dir string
}

var User = Project{Key: "user", Name: "user", Dir: "user"}

func Resolve(cwd string, cfg config.Config) (Project, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return Project{}, err
	}
	path := evalExisting(abs)

	key, name, grouped := matchGroup(path, cfg.Groups)
	if !grouped {
		key = gitRoot(path)
	}
	for _, a := range cfg.Aliases {
		if filepath.IsAbs(a.Path) && filepath.IsAbs(a.Key) && evalOrClean(a.Path) == key {
			key = a.Key
			break
		}
	}
	if !grouped {
		name = filepath.Base(key)
	}
	return Project{Key: key, Name: name, Dir: DirName(name, key)}, nil
}

func DirName(name, key string) string {
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])[:6]
	slug := Slug(name)
	if slug == "" {
		return hash
	}
	return slug + "-" + hash
}

func Slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return strings.TrimRight(out, "-")
}

func evalExisting(path string) string {
	rest := ""
	for dir := path; ; {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

func evalOrClean(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func matchGroup(path string, groups []config.Group) (string, string, bool) {
	var key, name string
	for _, g := range groups {
		if !filepath.IsAbs(g.Prefix) {
			continue
		}
		prefix := evalOrClean(g.Prefix)
		if !within(path, prefix) || len(prefix) <= len(key) {
			continue
		}
		key, name = prefix, g.Name
	}
	return key, name, key != ""
}

func within(path, prefix string) bool {
	if path == prefix {
		return true
	}
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(path, prefix)
}

func gitRoot(path string) string {
	for dir := path; ; {
		if filepath.Base(dir) == ".git" {
			return filepath.Dir(dir)
		}
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				return dir
			}
			if root, ok := rootFromGitFile(dotGit); ok {
				return root
			}
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		dir = parent
	}
}

func rootFromGitFile(dotGit string) (string, bool) {
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	line, _, _ := strings.Cut(string(data), "\n")
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", false
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(filepath.Dir(dotGit), gitDir)
	}
	if !linksBack(gitDir, dotGit) {
		return "", false
	}
	data, err = os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	common := strings.TrimSpace(string(data))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	common = evalOrClean(common)
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), true
	}
	return common, true
}

func linksBack(gitDir, dotGit string) bool {
	data, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return false
	}
	back := strings.TrimSpace(string(data))
	if !filepath.IsAbs(back) {
		back = filepath.Join(gitDir, back)
	}
	return evalOrClean(back) == evalOrClean(dotGit)
}
