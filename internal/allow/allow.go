// Package allow keeps the allowed logins and nodes (docs/specs/lodge-http.md,
// Identity, checks d and e). The gateway reads them on every request, so a
// change made through the control socket applies to the next one.
package allow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/frostyard/lodge/internal/store"
)

var (
	// ErrInvalid means a login or node ID breaks its form.
	ErrInvalid = errors.New("invalid")
	// ErrConflict means the login or node is already allowed.
	ErrConflict = errors.New("already allowed")
	// ErrNotFound means the login or node is not allowed.
	ErrNotFound = errors.New("not allowed")
)

// Login is one allowed login.
type Login struct {
	Login string    `json:"login"`
	Added time.Time `json:"added"`
}

// Node is one allowed node.
type Node struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

// Lists are the allow-lists in lodge's database.
type Lists struct {
	db  *sql.DB
	now func() time.Time
}

// New returns the lists in db. now is the clock for added times [time.Now].
func New(db *sql.DB, now func() time.Time) *Lists {
	if now == nil {
		now = time.Now
	}
	return &Lists{db: db, now: now}
}

// ValidLogin reports whether login is 1 to 256 bytes of UTF-8 without
// control characters.
func ValidLogin(login string) bool {
	if login == "" || len(login) > 256 || !utf8.ValidString(login) {
		return false
	}
	for _, r := range login {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

var nodeIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidNodeID reports whether id has a StableNodeID's form.
func ValidNodeID(id string) bool { return nodeIDRE.MatchString(id) }

// LoginAllowed reports whether login is on the list, byte for byte.
func (l *Lists) LoginAllowed(ctx context.Context, login string) (bool, error) {
	return l.exists(ctx, `SELECT 1 FROM logins WHERE login = ?`, login)
}

// NodeAllowed reports whether the node ID is on the list.
func (l *Lists) NodeAllowed(ctx context.Context, id string) (bool, error) {
	return l.exists(ctx, `SELECT 1 FROM nodes WHERE node_id = ?`, id)
}

func (l *Lists) exists(ctx context.Context, q, arg string) (bool, error) {
	var one int
	switch err := l.db.QueryRowContext(ctx, q, arg).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("read the allow-list: %w", err)
	}
	return true, nil
}

// AllowLogin adds login.
func (l *Lists) AllowLogin(ctx context.Context, login string) error {
	if !ValidLogin(login) {
		return fmt.Errorf("login %q: %w", login, ErrInvalid)
	}
	return l.insert(ctx, `INSERT INTO logins (login, added_at) VALUES (?, ?)`, login, l.stamp())
}

// AllowNode adds the node id, recording its current name.
func (l *Lists) AllowNode(ctx context.Context, id, name string) error {
	if !ValidNodeID(id) {
		return fmt.Errorf("node %q: %w", id, ErrInvalid)
	}
	return l.insert(ctx, `INSERT INTO nodes (node_id, name, added_at) VALUES (?, ?, ?)`, id, name, l.stamp())
}

func (l *Lists) insert(ctx context.Context, q string, args ...any) error {
	_, err := l.db.ExecContext(ctx, q, args...)
	if store.IsConstraint(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("write the allow-list: %w", err)
	}
	return nil
}

// DisallowLogin removes login.
func (l *Lists) DisallowLogin(ctx context.Context, login string) error {
	return l.delete(ctx, `DELETE FROM logins WHERE login = ?`, login)
}

// DisallowNode removes the node id.
func (l *Lists) DisallowNode(ctx context.Context, id string) error {
	return l.delete(ctx, `DELETE FROM nodes WHERE node_id = ?`, id)
}

func (l *Lists) delete(ctx context.Context, q, arg string) error {
	res, err := l.db.ExecContext(ctx, q, arg)
	if err != nil {
		return fmt.Errorf("write the allow-list: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("write the allow-list: %w", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Logins returns the allowed logins, sorted.
func (l *Lists) Logins(ctx context.Context) ([]Login, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT login, added_at FROM logins ORDER BY login`)
	if err != nil {
		return nil, fmt.Errorf("read the allow-list: %w", err)
	}
	defer rows.Close()
	out := []Login{}
	for rows.Next() {
		var x Login
		var added string
		if err := rows.Scan(&x.Login, &added); err != nil {
			return nil, err
		}
		x.Added = parseTime(added)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Nodes returns the allowed nodes, sorted by name.
func (l *Lists) Nodes(ctx context.Context) ([]Node, error) {
	rows, err := l.db.QueryContext(ctx, `SELECT node_id, name, added_at FROM nodes ORDER BY name, node_id`)
	if err != nil {
		return nil, fmt.Errorf("read the allow-list: %w", err)
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		var x Node
		var added string
		if err := rows.Scan(&x.ID, &x.Name, &added); err != nil {
			return nil, err
		}
		x.Added = parseTime(added)
		out = append(out, x)
	}
	return out, rows.Err()
}

func (l *Lists) stamp() string { return l.now().UTC().Format(store.TimeFormat) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(store.TimeFormat, s)
	return t
}
