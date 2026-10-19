// Package store opens lodge's SQLite database in its state directory:
// the allowed logins and nodes (internal/allow) and the MCP tokens
// (internal/tokens). It applies the migrations and refuses a database on a
// network file system, where SQLite's locking cannot be trusted.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
	"modernc.org/sqlite" // also registers the "sqlite" driver
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// TimeFormat is how times are stored: UTC, fixed width, lexically sortable.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Open opens or creates the database at path and applies pending
// migrations. path's directory must be on a local file system.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("open database: path %q contains ? or #", path)
	}
	if err := checkLocal(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}

// IsConstraint reports whether err is a uniqueness or primary-key
// violation.
func IsConstraint(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	}
	return false
}

// networkFS are the statfs magic numbers of file systems lodge refuses:
// network and FUSE file systems (virtiofs reports FUSE's).
var networkFS = map[int64]string{
	0x6969:     "nfs",
	0xff534d42: "cifs",
	0xfe534d42: "smb2",
	0x517b:     "smb",
	0x01021997: "9p",
	0x65735546: "fuse",
	0x00c36400: "ceph",
	0x5346414f: "afs",
	0x73757245: "coda",
	0x564c:     "ncp",
	0x7461636f: "ocfs2",
	0x01161970: "gfs2",
}

// checkLocal refuses a directory on a network file system.
func checkLocal(dir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return fmt.Errorf("check the file system: %w", err)
	}
	return refuseNetwork(int64(st.Type)) //nolint:unconvert // Type is int32 on some architectures.
}

func refuseNetwork(fsType int64) error {
	if name, ok := networkFS[fsType&0xffffffff]; ok {
		return fmt.Errorf("lodge's state must not be on a network file system (%s)", name)
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil || !strings.HasSuffix(e.Name(), ".sql") {
			return nil, fmt.Errorf("migration %s: name must be NNNN_name.sql", e.Name())
		}
		body, err := fs.ReadFile(migrations, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, e.Name(), string(body)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return a.version - b.version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration %s: versions must run 1, 2, 3 without gaps", m.name)
		}
	}
	return ms, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL) STRICT`); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if current > len(ms) {
		return fmt.Errorf("migrate: the database is at version %d, newer than this lodge's %d", current, len(ms))
	}
	for _, m := range ms[current:] {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("migrate %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate %s: %w", m.name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migrate %s: %w", m.name, err)
		}
	}
	return nil
}
