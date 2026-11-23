package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Observation struct {
	ID        int64
	SessionID string
	Project   string
	Kind      string
	Title     string
	Body      string
	ToolName  string
	Files     []string
	CreatedAt time.Time
}

type SessionEvent struct {
	SessionID string
	Project   string
	Cwd       string
	Source    string
	EndReason string
	At        time.Time
	End       bool
}

type ProjectRow struct {
	Key  string
	Name string
	Dir  string
}

var observationKinds = map[string]bool{
	"prompt":        true,
	"tool":          true,
	"tool_error":    true,
	"stop":          true,
	"compact_start": true,
	"compact":       true,
	"session_start": true,
}

func (s *Store) EnsureProject(ctx context.Context, p ProjectRow) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO projects (key, name, dir, created_at) VALUES (?, ?, ?, ?)",
		p.Key, p.Name, p.Dir, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("ensure project %s: %w", p.Key, err)
	}
	return nil
}

func (s *Store) TouchSession(ctx context.Context, ev SessionEvent) error {
	var endedAt sql.NullInt64
	if ev.End {
		endedAt = sql.NullInt64{Int64: ev.At.Unix(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, project, cwd, source, started_at, ended_at, end_reason)
VALUES (?1, ?2, nullif(?3, ''), nullif(?4, ''), ?5, ?6, CASE WHEN ?7 THEN nullif(?8, '') END)
ON CONFLICT(id) DO UPDATE SET
  cwd = coalesce(nullif(sessions.cwd, ''), excluded.cwd),
  source = coalesce(nullif(sessions.source, ''), excluded.source),
  started_at = min(sessions.started_at, excluded.started_at),
  ended_at = CASE WHEN ?7 THEN excluded.ended_at ELSE sessions.ended_at END,
  end_reason = CASE WHEN ?7 THEN coalesce(excluded.end_reason, sessions.end_reason) ELSE sessions.end_reason END`,
		ev.SessionID, ev.Project, ev.Cwd, ev.Source, ev.At.Unix(), endedAt, ev.End, ev.EndReason)
	if err != nil {
		return fmt.Errorf("touch session %s: %w", ev.SessionID, err)
	}
	return nil
}

func (s *Store) InsertObservation(ctx context.Context, o Observation) (int64, error) {
	if !observationKinds[o.Kind] {
		return 0, fmt.Errorf("insert observation: unknown kind %q", o.Kind)
	}
	files := o.Files
	if files == nil {
		files = []string{}
	}
	filesJSON, err := json.Marshal(files)
	if err != nil {
		return 0, fmt.Errorf("insert observation: encode files: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO observations (session_id, project, kind, title, body, tool_name, files, created_at)
VALUES (?, ?, ?, ?, ?, nullif(?, ''), ?, ?)`,
		o.SessionID, o.Project, o.Kind, o.Title, o.Body, o.ToolName, string(filesJSON), o.CreatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("insert observation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert observation: %w", err)
	}
	return id, nil
}

func (s *Store) RecordCaptureError(ctx context.Context, event, msg string, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO capture_errors (event, message, created_at) VALUES (?, ?, ?)",
		event, msg, at.Unix())
	if err != nil {
		return fmt.Errorf("record capture error: %w", err)
	}
	return nil
}

func (s *Store) CaptureErrorsSince(ctx context.Context, since time.Time) (count int, last string, err error) {
	err = s.db.QueryRowContext(ctx, `
SELECT count(*), coalesce((SELECT message FROM capture_errors WHERE created_at >= ?1 ORDER BY created_at DESC, id DESC LIMIT 1), '')
FROM capture_errors WHERE created_at >= ?1`, since.Unix()).Scan(&count, &last)
	if err != nil {
		return 0, "", fmt.Errorf("read capture errors: %w", err)
	}
	return count, last, nil
}
