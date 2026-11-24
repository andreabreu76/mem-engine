package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrAmbiguous = errors.New("ambiguous")
)

type Memory struct {
	ID, Project, Type, Title, Summary, Body string
	Tags                                    []string
	Path, SourceSession                     string
	Created, Updated                        time.Time
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

const insertMemorySQL = `
INSERT INTO memories (id, project, type, title, summary, body, tags, path, source_session, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, nullif(?, ''), ?, ?)`

const upsertMemorySQL = insertMemorySQL + `
ON CONFLICT(id) DO UPDATE SET
  project = excluded.project,
  type = excluded.type,
  title = excluded.title,
  summary = excluded.summary,
  body = excluded.body,
  tags = excluded.tags,
  path = excluded.path,
  source_session = excluded.source_session,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at`

func (s *Store) UpsertMemory(ctx context.Context, m Memory) error {
	if err := writeMemory(ctx, s.db, upsertMemorySQL, m); err != nil {
		return fmt.Errorf("upsert memory %s: %w", m.ID, err)
	}
	return nil
}

func writeMemory(ctx context.Context, db execer, query string, m Memory) error {
	tags := m.Tags
	if tags == nil {
		tags = []string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return fmt.Errorf("encode tags: %w", err)
	}
	_, err = db.ExecContext(ctx, query,
		m.ID, m.Project, m.Type, m.Title, m.Summary, m.Body, string(tagsJSON), m.Path, m.SourceSession,
		m.Created.Unix(), m.Updated.Unix())
	return err
}

func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM memories WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete memory %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete memory %s: %w", id, ErrNotFound)
	}
	return nil
}

const memoryColumns = "id, project, type, title, summary, body, tags, path, source_session, created_at, updated_at"

type scanner interface {
	Scan(dest ...any) error
}

func scanMemory(row scanner) (Memory, error) {
	var (
		m       Memory
		tags    string
		source  sql.NullString
		created int64
		updated int64
	)
	if err := row.Scan(&m.ID, &m.Project, &m.Type, &m.Title, &m.Summary, &m.Body, &tags, &m.Path, &source, &created, &updated); err != nil {
		return Memory{}, err
	}
	if err := json.Unmarshal([]byte(tags), &m.Tags); err != nil {
		return Memory{}, fmt.Errorf("decode tags: %w", err)
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	m.SourceSession = source.String
	m.Created = time.Unix(created, 0).UTC()
	m.Updated = time.Unix(updated, 0).UTC()
	return m, nil
}

func (s *Store) GetMemory(ctx context.Context, id string) (Memory, error) {
	m, err := scanMemory(s.db.QueryRowContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, fmt.Errorf("memory %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Memory{}, fmt.Errorf("get memory %s: %w", id, err)
	}
	return m, nil
}

func (s *Store) ListMemories(ctx context.Context, project string) ([]Memory, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+memoryColumns+" FROM memories WHERE project = ? ORDER BY created_at, id", project)
	if err != nil {
		return nil, fmt.Errorf("list memories %s: %w", project, err)
	}
	defer rows.Close()
	var ms []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("list memories %s: %w", project, err)
		}
		ms = append(ms, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list memories %s: %w", project, err)
	}
	return ms, nil
}

func (s *Store) ReplaceAllMemories(ctx context.Context, ms []Memory) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace memories: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO memories_fts(memories_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("replace memories: rebuild fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM memories"); err != nil {
		return fmt.Errorf("replace memories: %w", err)
	}
	for _, m := range ms {
		if err := writeMemory(ctx, tx, insertMemorySQL, m); err != nil {
			return fmt.Errorf("replace memories: %s: %w", m.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO memories_fts(memories_fts) VALUES('rebuild')"); err != nil {
		return fmt.Errorf("replace memories: rebuild fts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace memories: %w", err)
	}
	return nil
}

func (s *Store) ProjectByKey(ctx context.Context, key string) (ProjectRow, error) {
	var p ProjectRow
	err := s.db.QueryRowContext(ctx, "SELECT key, name, dir FROM projects WHERE key = ?", key).Scan(&p.Key, &p.Name, &p.Dir)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectRow{}, fmt.Errorf("project %s: %w", key, ErrNotFound)
	}
	if err != nil {
		return ProjectRow{}, fmt.Errorf("get project %s: %w", key, err)
	}
	return p, nil
}

func (s *Store) FindProject(ctx context.Context, nameOrKey string) (ProjectRow, error) {
	p, err := s.ProjectByKey(ctx, nameOrKey)
	if !errors.Is(err, ErrNotFound) {
		return p, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT key, name, dir FROM projects WHERE name = ? ORDER BY key", nameOrKey)
	if err != nil {
		return ProjectRow{}, fmt.Errorf("find project %s: %w", nameOrKey, err)
	}
	defer rows.Close()
	var found []ProjectRow
	for rows.Next() {
		var r ProjectRow
		if err := rows.Scan(&r.Key, &r.Name, &r.Dir); err != nil {
			return ProjectRow{}, fmt.Errorf("find project %s: %w", nameOrKey, err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return ProjectRow{}, fmt.Errorf("find project %s: %w", nameOrKey, err)
	}
	switch len(found) {
	case 0:
		return ProjectRow{}, fmt.Errorf("project %s: %w", nameOrKey, ErrNotFound)
	case 1:
		return found[0], nil
	}
	keys := make([]string, len(found))
	for i, r := range found {
		keys[i] = r.Key
	}
	return ProjectRow{}, fmt.Errorf("project name %q is %w, use one of the keys: %s", nameOrKey, ErrAmbiguous, strings.Join(keys, ", "))
}
