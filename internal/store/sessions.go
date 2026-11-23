package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const lastSessionFiles = 5

type SessionSummary struct {
	SessionID                 string
	StartedAt                 time.Time
	LastPrompt, LastAssistant string
	Files                     []string
}

func (s *Store) LastSession(ctx context.Context, project, excludeSession string) (SessionSummary, bool, error) {
	var (
		sum     SessionSummary
		started int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, started_at FROM sessions
WHERE project = ?1 AND id <> ?2 AND EXISTS (
  SELECT 1 FROM observations o
  WHERE o.session_id = sessions.id AND o.kind NOT IN ('session_start', 'compact_start'))
ORDER BY started_at DESC, id DESC LIMIT 1`, project, excludeSession).Scan(&sum.SessionID, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionSummary{}, false, nil
	}
	if err != nil {
		return SessionSummary{}, false, fmt.Errorf("last session of %s: %w", project, err)
	}
	sum.StartedAt = time.Unix(started, 0).UTC()
	if sum.LastPrompt, _, err = s.latestBody(ctx, sum.SessionID, "prompt"); err != nil {
		return SessionSummary{}, false, err
	}
	if sum.LastAssistant, _, err = s.latestBody(ctx, sum.SessionID, "stop"); err != nil {
		return SessionSummary{}, false, err
	}
	if sum.Files, err = s.recentFiles(ctx, sum.SessionID); err != nil {
		return SessionSummary{}, false, err
	}
	return sum, true, nil
}

func (s *Store) LatestCompactSummary(ctx context.Context, sessionID string) (string, bool, error) {
	return s.latestBody(ctx, sessionID, "compact")
}

func (s *Store) latestBody(ctx context.Context, sessionID, kind string) (string, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `
SELECT body FROM observations WHERE session_id = ? AND kind = ?
ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID, kind).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("latest %s of session %s: %w", kind, sessionID, err)
	}
	return body, true, nil
}

func (s *Store) recentFiles(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT files FROM observations
WHERE session_id = ? AND kind = 'tool' AND files IS NOT NULL AND files <> '[]'
ORDER BY created_at DESC, id DESC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("files of session %s: %w", sessionID, err)
	}
	defer rows.Close()
	var files []string
	seen := map[string]bool{}
	for len(files) < lastSessionFiles && rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("files of session %s: %w", sessionID, err)
		}
		var batch []string
		if err := json.Unmarshal([]byte(raw), &batch); err != nil {
			return nil, fmt.Errorf("files of session %s: decode: %w", sessionID, err)
		}
		for _, f := range batch {
			if f != "" && !seen[f] && len(files) < lastSessionFiles {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("files of session %s: %w", sessionID, err)
	}
	return files, nil
}

func (s *Store) RecentMemories(ctx context.Context, sc Scope, limit int) ([]Memory, error) {
	if len(sc.Projects) == 0 || limit <= 0 {
		return nil, nil
	}
	where, args := scopeClause("project", sc)
	rows, err := s.db.QueryContext(ctx, "SELECT "+memoryColumns+` FROM memories WHERE `+where+`
ORDER BY EXISTS (SELECT 1 FROM json_each(memories.tags) WHERE value = 'pinned') DESC, updated_at DESC, id
LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("recent memories: %w", err)
	}
	defer rows.Close()
	var ms []Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, fmt.Errorf("recent memories: %w", err)
		}
		ms = append(ms, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent memories: %w", err)
	}
	return ms, nil
}
