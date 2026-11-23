package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const (
	gcInterval = 24 * time.Hour
	lastGCKey  = "last_gc"
)

func (s *Store) GC(ctx context.Context, now time.Time, retention time.Duration, force bool) (deleted int64, ran bool, err error) {
	if retention <= 0 {
		return 0, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("gc: begin: %w", err)
	}
	defer tx.Rollback()
	if !force {
		due, err := gcDue(ctx, tx, now)
		if err != nil || !due {
			return 0, false, err
		}
	}
	cutoff := now.Add(-retention).Unix()
	res, err := tx.ExecContext(ctx, "DELETE FROM observations WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, false, fmt.Errorf("gc: delete observations: %w", err)
	}
	deleted, err = res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("gc: delete observations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM capture_errors WHERE created_at < ?", cutoff); err != nil {
		return 0, false, fmt.Errorf("gc: delete capture errors: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		lastGCKey, strconv.FormatInt(now.Unix(), 10)); err != nil {
		return 0, false, fmt.Errorf("gc: record last run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("gc: commit: %w", err)
	}
	return deleted, true, nil
}

func gcDue(ctx context.Context, tx *sql.Tx, now time.Time) (bool, error) {
	var value string
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", lastGCKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("gc: read last run: %w", err)
	}
	last, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return true, nil
	}
	return last > now.Unix() || now.Sub(time.Unix(last, 0)) > gcInterval, nil
}
