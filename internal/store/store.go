package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	defaultBusyTimeout = 2 * time.Second
	busyRetryInterval  = 10 * time.Millisecond
)

var ErrSchemaTooNew = errors.New("database schema is newer than this binary")

type Options struct {
	BusyTimeout time.Duration
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

func Open(path string) (*Store, error) {
	return OpenWith(path, Options{BusyTimeout: defaultBusyTimeout})
}

func OpenWith(path string, o Options) (*Store, error) {
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = defaultBusyTimeout
	}
	db, err := sql.Open("sqlite", dsn(path, o.BusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrateWithRetry(context.Background(), db, o.BusyTimeout); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func migrateWithRetry(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := migrate(ctx, db)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(busyRetryInterval)
	}
}

func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

func dsn(path string, busyTimeout time.Duration) string {
	escaped := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
	return fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate",
		escaped, busyTimeout.Milliseconds())
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Version(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

func (s *Store) CheckFTS5(ctx context.Context) error {
	for _, table := range []string{"observations_fts", "memories_fts"} {
		q := "SELECT count(*) FROM " + table + " WHERE " + table + " MATCH 'probe'"
		var n int
		if err := s.db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			return fmt.Errorf("fts5 check on %s: %w", table, err)
		}
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if current, err := schemaVersion(ctx, db); err == nil && current == SchemaVersion {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer tx.Rollback()

	current, err := schemaVersion(ctx, tx)
	if err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("%w: database at version %d, binary supports %d", ErrSchemaTooNew, current, SchemaVersion)
	}
	if current == SchemaVersion {
		return nil
	}
	for v := current; v < SchemaVersion; v++ {
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("migrate to version %d: %w", v+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM schema_version"); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version (version) VALUES (?)", SchemaVersion); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func schemaVersion(ctx context.Context, tx rowQuerier) (int, error) {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'").Scan(&exists)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if exists == 0 {
		return 0, nil
	}
	var v int
	if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}
