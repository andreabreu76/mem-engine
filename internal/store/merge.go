package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const userScope = "user"

func (s *Store) MergeProject(ctx context.Context, from, to string) (moved int64, err error) {
	switch {
	case from == to:
		return 0, fmt.Errorf("merge %s: cannot merge a project into itself", from)
	case from == userScope || to == userScope:
		return 0, errors.New("merge: the user scope cannot be merged")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("merge %s into %s: %w", from, to, err)
	}
	defer tx.Rollback()
	for _, key := range []string{from, to} {
		var found string
		err := tx.QueryRowContext(ctx, "SELECT key FROM projects WHERE key = ?", key).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("merge %s into %s: project %s: %w", from, to, key, err)
		}
	}
	res, err := tx.ExecContext(ctx, "UPDATE observations SET project = ? WHERE project = ?", to, from)
	if err != nil {
		return 0, fmt.Errorf("merge %s into %s: observations: %w", from, to, err)
	}
	if moved, err = res.RowsAffected(); err != nil {
		return 0, fmt.Errorf("merge %s into %s: observations: %w", from, to, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET project = ? WHERE project = ?", to, from); err != nil {
		return 0, fmt.Errorf("merge %s into %s: sessions: %w", from, to, err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM projects WHERE key = ?1 AND NOT EXISTS (SELECT 1 FROM memories WHERE project = ?1)", from); err != nil {
		return 0, fmt.Errorf("merge %s into %s: project row: %w", from, to, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("merge %s into %s: %w", from, to, err)
	}
	return moved, nil
}
