package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andreabreu76/mem-engine/internal/project"
	"github.com/andreabreu76/mem-engine/internal/store"
	"github.com/andreabreu76/mem-engine/internal/wiki"
)

func (s *Service) MoveProject(ctx context.Context, from, to project.Project) (moved int, err error) {
	switch {
	case from.Key == to.Key:
		return 0, fmt.Errorf("move %s: cannot move a project into itself", from.Key)
	case from.Key == project.User.Key || to.Key == project.User.Key:
		return 0, errors.New("move: the user scope cannot be moved")
	}
	unlockRoot, err := wiki.LockRoot(ctx, s.WikiRoot, true)
	if err != nil {
		return 0, err
	}
	defer unlockRoot()
	src, err := wiki.OpenRepo(s.WikiRoot, from.Dir)
	if err != nil {
		return 0, err
	}
	dst, err := wiki.OpenRepo(s.WikiRoot, to.Dir)
	if err != nil {
		return 0, err
	}
	unlock, err := lockAll(ctx, src, dst)
	if err != nil {
		return 0, err
	}
	defer unlock()

	if err := s.Store.EnsureProject(ctx, store.ProjectRow{Key: to.Key, Name: to.Name, Dir: to.Dir}); err != nil {
		return 0, err
	}
	ms, err := s.Store.ListMemories(ctx, from.Key)
	if err != nil {
		return 0, err
	}
	inSrc, err := idsIn(src)
	if err != nil {
		return 0, err
	}
	present, err := idsIn(dst)
	if err != nil {
		return 0, err
	}
	var ids []string
	names := map[string]string{}
	for _, m := range ms {
		dir, name, ok := strings.Cut(m.Path, "/")
		if !ok || dir != from.Dir || !validName(name) {
			return 0, fmt.Errorf("move %s into %s: memory %s: path %q is not in %s (run mem reindex)", from.Key, to.Key, m.ID, m.Path, from.Dir)
		}
		ids = append(ids, m.ID)
		names[m.ID] = name
	}
	for id, name := range inSrc {
		if _, listed := names[id]; listed {
			names[id] = name
			continue
		}
		if err := s.checkUnlisted(ctx, id, to); err != nil {
			return 0, fmt.Errorf("move %s into %s: %s/%s: %w", from.Key, to.Key, from.Dir, name, err)
		}
		ids = append(ids, id)
		names[id] = name
	}
	sort.Strings(ids[len(ms):])
	for _, id := range ids {
		if err := s.moveOne(ctx, id, names[id], from, to, src, dst, present); err != nil {
			return moved, fmt.Errorf("move %s into %s: moved %d of %d: %w", from.Key, to.Key, moved, len(ids), err)
		}
		moved++
	}
	left, err := src.List()
	if err != nil {
		return moved, err
	}
	if len(left) > 0 {
		return moved, fmt.Errorf("move %s into %s: %s still holds files that are not memories: %s (move or delete them, then run again)",
			from.Key, to.Key, from.Dir, strings.Join(left, ", "))
	}
	return moved, nil
}

func (s *Service) checkUnlisted(ctx context.Context, id string, to project.Project) error {
	m, err := s.Store.GetMemory(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.Project != to.Key {
		return fmt.Errorf("memory %s is indexed under project %s (run mem reindex)", id, m.Project)
	}
	return nil
}

func (s *Service) moveOne(ctx context.Context, id, name string, from, to project.Project, src, dst *wiki.Repo, present map[string]string) error {
	var (
		doc wiki.Doc
		err error
	)
	dstName, inDst := present[id]
	if inDst {
		doc, err = readOwnDoc(dst, dstName, id)
	} else {
		doc, err = readOwnDoc(src, name, id)
	}
	if err != nil {
		return fmt.Errorf("memory %s: %w (run mem reindex)", id, err)
	}
	doc.Project = to.Key
	if !inDst {
		dstName = dst.UniqueName(strings.TrimSuffix(name, filepath.Ext(name)))
	}
	if err := dst.Write(dstName, wiki.Marshal(doc)); err != nil {
		return err
	}
	if err := dst.Commit("mem: move "+id+" "+doc.Title+" from "+from.Key, dstName); err != nil {
		if !inDst {
			if rmErr := dst.Remove(dstName); rmErr != nil {
				return errors.Join(err, rmErr)
			}
		}
		return err
	}
	present[id] = dstName
	if err := src.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := src.Commit("mem: move "+id+" "+doc.Title+" to "+to.Key, name); err != nil {
		return err
	}
	return s.Store.UpsertMemory(ctx, toMemory(doc, to.Dir+"/"+dstName))
}

func idsIn(repo *wiki.Repo) (map[string]string, error) {
	names, err := repo.List()
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, name := range names {
		if doc, err := readDoc(filepath.Join(repo.Dir, name)); err == nil {
			ids[doc.ID] = name
		}
	}
	return ids, nil
}

func lockAll(ctx context.Context, repos ...*wiki.Repo) (func(), error) {
	sorted := append([]*wiki.Repo{}, repos...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dir < sorted[j].Dir })
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for i, r := range sorted {
		if i > 0 && r.Dir == sorted[i-1].Dir {
			continue
		}
		unlock, err := r.Lock(ctx)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}
