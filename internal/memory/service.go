package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
	"github.com/andreabreu76/mem-engine/internal/wiki"
)

type Input struct {
	Type, Title, Summary, Body string
	Tags                       []string
	SourceSession              string
}

type Patch struct {
	Type, Title, Summary, Body *string
	Tags                       *[]string
}

type ReindexReport struct {
	Indexed int
	Skipped []string
}

type Service struct {
	Store    *store.Store
	WikiRoot string
	Now      func() time.Time

	beforeReplace func()
}

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	idPattern     = regexp.MustCompile(`^m-[0-9a-f]{12}$`)
	dirHashSuffix = regexp.MustCompile(`-[0-9a-f]{6}$`)
)

func (s *Service) Save(ctx context.Context, p project.Project, in Input) (store.Memory, error) {
	if err := validate(in.Type, in.Title, in.Summary); err != nil {
		return store.Memory{}, err
	}
	id, err := newID()
	if err != nil {
		return store.Memory{}, err
	}
	if err := s.Store.EnsureProject(ctx, store.ProjectRow{Key: p.Key, Name: p.Name, Dir: p.Dir}); err != nil {
		return store.Memory{}, err
	}
	repo, err := wiki.OpenRepo(s.WikiRoot, p.Dir)
	if err != nil {
		return store.Memory{}, err
	}
	unlock, err := repo.Lock(ctx)
	if err != nil {
		return store.Memory{}, err
	}
	defer unlock()

	slug := project.Slug(in.Title)
	if slug == "" {
		slug = id
	}
	name := repo.UniqueName(slug)
	now := s.now()
	doc := wiki.Doc{
		ID:            id,
		Type:          in.Type,
		Title:         in.Title,
		Summary:       in.Summary,
		Project:       p.Key,
		Tags:          copyTags(in.Tags),
		Created:       now,
		Updated:       now,
		SourceSession: in.SourceSession,
		Body:          in.Body,
	}
	if err := repo.Write(name, wiki.Marshal(doc)); err != nil {
		return store.Memory{}, err
	}
	if err := repo.Commit("mem: save "+id+" "+doc.Title, name); err != nil {
		if rmErr := repo.Remove(name); rmErr != nil {
			return store.Memory{}, errors.Join(err, rmErr)
		}
		return store.Memory{}, err
	}
	m := toMemory(doc, p.Dir+"/"+name)
	if err := s.Store.UpsertMemory(ctx, m); err != nil {
		return store.Memory{}, err
	}
	return m, nil
}

func (s *Service) Update(ctx context.Context, id string, patch Patch) (store.Memory, error) {
	current, err := s.Store.GetMemory(ctx, id)
	if err != nil {
		return store.Memory{}, fmt.Errorf("update: %w", err)
	}
	repo, name, err := s.locate(current.Path)
	if err != nil {
		return store.Memory{}, fmt.Errorf("update %s: %w", id, err)
	}
	unlock, err := repo.Lock(ctx)
	if err != nil {
		return store.Memory{}, err
	}
	defer unlock()

	doc, err := readOwnDoc(repo, name, id)
	if err != nil {
		return store.Memory{}, fmt.Errorf("update %s: %s: %w (run mem reindex)", id, current.Path, err)
	}
	apply(&doc, patch)
	if err := validate(doc.Type, doc.Title, doc.Summary); err != nil {
		return store.Memory{}, err
	}
	doc.Updated = s.now()
	if err := repo.Write(name, wiki.Marshal(doc)); err != nil {
		return store.Memory{}, err
	}
	if err := repo.Commit("mem: update "+id+" "+doc.Title, name); err != nil {
		return store.Memory{}, err
	}
	m := toMemory(doc, current.Path)
	if err := s.Store.UpsertMemory(ctx, m); err != nil {
		return store.Memory{}, err
	}
	return m, nil
}

func (s *Service) Forget(ctx context.Context, id string) error {
	current, err := s.Store.GetMemory(ctx, id)
	if err != nil {
		return fmt.Errorf("forget: %w", err)
	}
	repo, name, err := s.locate(current.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return s.Store.DeleteMemory(ctx, id)
	}
	if err != nil {
		return fmt.Errorf("forget %s: %w", id, err)
	}
	unlock, err := repo.Lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	if _, err := readOwnDoc(repo, name, id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("forget %s: %s: %w (run mem reindex)", id, current.Path, err)
	}
	if err := repo.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := repo.Commit("mem: forget "+id+" "+current.Title, name); err != nil {
		return err
	}
	return s.Store.DeleteMemory(ctx, id)
}

func (s *Service) Reindex(ctx context.Context) (ReindexReport, error) {
	var rep ReindexReport
	entries, err := os.ReadDir(s.WikiRoot)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rep, fmt.Errorf("reindex: %w", err)
	}
	var repos []*wiki.Repo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		repo, err := wiki.OpenRepo(s.WikiRoot, e.Name())
		if err != nil {
			return rep, fmt.Errorf("reindex: %w", err)
		}
		unlock, err := repo.Lock(ctx)
		if err != nil {
			return rep, fmt.Errorf("reindex: %w", err)
		}
		defer unlock()
		repos = append(repos, repo)
	}
	var ms []store.Memory
	seen := map[string]string{}
	projects := map[string]store.ProjectRow{}
	for _, repo := range repos {
		dir := filepath.Base(repo.Dir)
		names, err := repo.List()
		if err != nil {
			return rep, fmt.Errorf("reindex: %w", err)
		}
		for _, name := range names {
			rel := dir + "/" + name
			doc, err := readDoc(filepath.Join(repo.Dir, name))
			if err == nil {
				if other, dup := seen[doc.ID]; dup {
					err = fmt.Errorf("duplicate id %s, already in %s", doc.ID, other)
				}
			}
			if err != nil {
				rep.Skipped = append(rep.Skipped, rel+": "+err.Error())
				continue
			}
			seen[doc.ID] = rel
			ms = append(ms, toMemory(doc, rel))
			if _, ok := projects[doc.Project]; !ok {
				projects[doc.Project] = store.ProjectRow{Key: doc.Project, Name: dirHashSuffix.ReplaceAllString(dir, ""), Dir: dir}
			}
		}
	}
	for _, p := range projects {
		if err := s.Store.EnsureProject(ctx, p); err != nil {
			return rep, fmt.Errorf("reindex: %w", err)
		}
	}
	if s.beforeReplace != nil {
		s.beforeReplace()
	}
	if err := s.Store.ReplaceAllMemories(ctx, ms); err != nil {
		return rep, fmt.Errorf("reindex: %w", err)
	}
	rep.Indexed = len(ms)
	return rep, nil
}

func readDoc(file string) (wiki.Doc, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return wiki.Doc{}, err
	}
	doc, err := wiki.Parse(b)
	if err != nil {
		return wiki.Doc{}, err
	}
	if doc.Project == "" {
		return wiki.Doc{}, errors.New("wiki: frontmatter without project")
	}
	if !idPattern.MatchString(doc.ID) {
		return wiki.Doc{}, fmt.Errorf("wiki: invalid id %q, want m- followed by 12 hex digits", doc.ID)
	}
	if err := validateType(doc.Type); err != nil {
		return wiki.Doc{}, err
	}
	return doc, nil
}

func readOwnDoc(repo *wiki.Repo, name, id string) (wiki.Doc, error) {
	b, err := os.ReadFile(filepath.Join(repo.Dir, name))
	if err != nil {
		return wiki.Doc{}, err
	}
	doc, err := wiki.Parse(b)
	if err != nil {
		return wiki.Doc{}, err
	}
	if doc.ID != id {
		return wiki.Doc{}, fmt.Errorf("file holds memory %s", doc.ID)
	}
	return doc, nil
}

func (s *Service) locate(rel string) (*wiki.Repo, string, error) {
	dir, name, ok := strings.Cut(rel, "/")
	if !ok || !validDir(dir) || !validName(name) {
		return nil, "", fmt.Errorf("memory path %q is not <dir>/<file>.md (run mem reindex)", rel)
	}
	info, err := os.Stat(filepath.Join(s.WikiRoot, dir))
	if err != nil {
		return nil, "", fmt.Errorf("memory path %q: %w (run mem reindex)", rel, err)
	}
	if !info.IsDir() {
		return nil, "", fmt.Errorf("memory path %q: %s is not a directory (run mem reindex)", rel, dir)
	}
	repo, err := wiki.OpenRepo(s.WikiRoot, dir)
	if err != nil {
		return nil, "", err
	}
	return repo, name, nil
}

func (s *Service) now() time.Time {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().UTC().Truncate(time.Second)
}

func validDir(dir string) bool {
	return dir != "" && !strings.HasPrefix(dir, ".") && !strings.ContainsAny(dir, `/\`)
}

func validName(name string) bool {
	return validDir(name) && strings.HasSuffix(name, ".md")
}

func validateType(typ string) error {
	if !typePattern.MatchString(typ) {
		return fmt.Errorf("invalid memory type %q: want lowercase letters, digits and dashes, starting with a letter, up to 32 characters (e.g. fact, decision, gotcha, preference, reference)", typ)
	}
	return nil
}

func validate(typ, title, summary string) error {
	if err := validateType(typ); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("memory title is required")
	}
	if strings.TrimSpace(summary) == "" {
		return errors.New("memory summary is required")
	}
	return nil
}

func apply(doc *wiki.Doc, p Patch) {
	if p.Type != nil {
		doc.Type = *p.Type
	}
	if p.Title != nil {
		doc.Title = *p.Title
	}
	if p.Summary != nil {
		doc.Summary = *p.Summary
	}
	if p.Body != nil {
		doc.Body = *p.Body
	}
	if p.Tags != nil {
		doc.Tags = copyTags(*p.Tags)
	}
}

func copyTags(tags []string) []string {
	return append([]string{}, tags...)
}

func toMemory(d wiki.Doc, rel string) store.Memory {
	return store.Memory{
		ID:            d.ID,
		Project:       d.Project,
		Type:          d.Type,
		Title:         d.Title,
		Summary:       d.Summary,
		Body:          d.Body,
		Tags:          copyTags(d.Tags),
		Path:          rel,
		SourceSession: d.SourceSession,
		Created:       d.Created.UTC().Truncate(time.Second),
		Updated:       d.Updated.UTC().Truncate(time.Second),
	}
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("memory id: %w", err)
	}
	return "m-" + hex.EncodeToString(b[:]), nil
}
