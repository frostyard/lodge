// Package tokens issues and checks MCP tokens (docs/specs/lodge-http.md,
// Tokens, logins and nodes; ADR-0006). A token is lgm_ and 43 base64url
// characters. lodge keeps only its SHA-256, with its name, app and scope;
// the secret is returned once, by Issue, and never stored or logged.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/frostyard/lodge/internal/store"
)

// Prefix starts every token.
const Prefix = "lgm_"

var (
	// ErrInvalid means a name or scope breaks its form.
	ErrInvalid = errors.New("invalid")
	// ErrConflict means a live token already has the name.
	ErrConflict = errors.New("a live token has this name")
	// ErrNotFound means no live token has the name.
	ErrNotFound = errors.New("no live token has this name")
	// ErrUnauthorized means a secret names no live token for the app.
	ErrUnauthorized = errors.New("unauthorized")
)

// TouchEvery is how often a token's last use is recorded at most.
const TouchEvery = time.Minute

// Token is what lodge keeps of one token, without the secret.
type Token struct {
	// ID is the token's row: names are reused, rows are not.
	ID       int64     `json:"-"`
	Name     string    `json:"name"`
	App      string    `json:"app"`
	Scope    string    `json:"scope"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
	Revoked  time.Time `json:"revoked,omitzero"`
}

// Store keeps tokens in lodge's database.
type Store struct {
	db  *sql.DB
	now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time // by name: when last use was last recorded
}

// New returns the tokens in db. now is the clock [time.Now].
func New(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now, touched: map[string]time.Time{}}
}

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9@._-]{1,64}$`)
	scopeRE  = regexp.MustCompile(`^[!-~]{1,256}$`)
	secretRE = regexp.MustCompile(`^lgm_[A-Za-z0-9_-]{43}$`)
)

// ValidName reports whether name is 1 to 64 of [A-Za-z0-9@._-].
func ValidName(name string) bool { return nameRE.MatchString(name) }

// ValidScope reports whether scope is 1 to 256 visible ASCII characters.
func ValidScope(scope string) bool { return scopeRE.MatchString(scope) }

// Hash is the SHA-256, hex, lodge keeps of a secret.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Issue creates a token for app with scope, and returns its secret.
func (s *Store) Issue(ctx context.Context, name, app, scope string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("token name %q: %w", name, ErrInvalid)
	}
	if !ValidScope(scope) {
		return "", fmt.Errorf("scope: %w", ErrInvalid)
	}
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails.
	secret := Prefix + base64.RawURLEncoding.EncodeToString(b[:])
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (name, app, scope, hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		name, app, scope, Hash(secret), s.stamp(s.now()))
	if store.IsConstraint(err) {
		return "", ErrConflict
	}
	if err != nil {
		return "", fmt.Errorf("issue a token: %w", err)
	}
	return secret, nil
}

// Check returns the live token whose secret this is, when it was issued
// for app. Every other case is ErrUnauthorized.
func (s *Store) Check(ctx context.Context, secret, app string) (Token, error) {
	if !secretRE.MatchString(secret) {
		return Token{}, ErrUnauthorized
	}
	hash := Hash(secret)
	var t Token
	var gotHash, created string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, app, scope, hash, created_at FROM tokens
		WHERE hash = ? AND revoked_at IS NULL`, hash).Scan(&t.ID, &t.Name, &t.App, &t.Scope, &gotHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrUnauthorized
	}
	if err != nil {
		return Token{}, fmt.Errorf("check a token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(gotHash), []byte(hash)) != 1 || t.App != app {
		return Token{}, ErrUnauthorized
	}
	t.Created = parseTime(created)
	return t, nil
}

// Touch records the token's use, at most once per TouchEvery.
func (s *Store) Touch(ctx context.Context, name string) error {
	now := s.now()
	s.mu.Lock()
	last, ok := s.touched[name]
	if ok && now.Sub(last) < TouchEvery {
		s.mu.Unlock()
		return nil
	}
	s.touched[name] = now
	s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE name = ? AND revoked_at IS NULL`, s.stamp(now), name)
	if err != nil {
		return fmt.Errorf("record a token's use: %w", err)
	}
	return nil
}

// Revoke revokes the live token with name, and returns its row.
func (s *Store) Revoke(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE name = ? AND revoked_at IS NULL RETURNING id`,
		s.stamp(s.now()), name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("revoke a token: %w", err)
	}
	s.mu.Lock()
	delete(s.touched, name)
	s.mu.Unlock()
	return id, nil
}

// List returns every token, live and revoked, oldest first.
func (s *Store) List(ctx context.Context) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, app, scope, created_at, COALESCE(last_used_at, ''), COALESCE(revoked_at, '')
		FROM tokens ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var created, used, revoked string
		if err := rows.Scan(&t.Name, &t.App, &t.Scope, &created, &used, &revoked); err != nil {
			return nil, err
		}
		t.Created, t.LastUsed, t.Revoked = parseTime(created), parseTime(used), parseTime(revoked)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Bearer returns the token in an Authorization header's value, or "".
func Bearer(header string) string {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (s *Store) stamp(t time.Time) string { return t.UTC().Format(store.TimeFormat) }

func parseTime(v string) time.Time {
	t, _ := time.Parse(store.TimeFormat, v)
	return t
}
