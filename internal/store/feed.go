package store

import (
	"context"
	"fmt"
)

func (s *Store) Feed(ctx context.Context, afterID int64, projects []string, limit int) ([]Hit, error) {
	if limit <= 0 {
		return nil, nil
	}
	where, args := "t.id > ?", []any{afterID}
	if len(projects) > 0 {
		clause, scopeArgs := scopeClause("t.project", Scope{Projects: projects})
		where += " AND " + clause
		args = append(args, scopeArgs...)
	}
	q := "SELECT " + observationCorpus.cols + ", 0 FROM observations t WHERE " + where + " ORDER BY t.id ASC LIMIT ?"
	hits, err := s.queryHits(ctx, kindObservation, q, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("feed after %d: %w", afterID, err)
	}
	return hits, nil
}

func (s *Store) DeleteObservation(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM observations WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete observation %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete observation %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete observation %d: %w", id, ErrNotFound)
	}
	return nil
}

func (s *Store) ListProjects(ctx context.Context) ([]ProjectRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, name, dir FROM projects ORDER BY name, key")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	var ps []ProjectRow
	for rows.Next() {
		var p ProjectRow
		if err := rows.Scan(&p.Key, &p.Name, &p.Dir); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		ps = append(ps, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return ps, nil
}
