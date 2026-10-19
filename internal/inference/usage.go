package inference

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/frostyard/lodge/internal/store"
)

// Record is one request in the usage log: never its content.
type Record struct {
	At        time.Time
	App       string
	Alias     string
	Provider  string
	Model     string
	Priority  Priority
	Input     int64 // -1 when unknown
	Output    int64 // -1 when unknown
	Wait      time.Duration
	FirstByte time.Duration // -1 when no byte came
	Total     time.Duration
	Status    int
}

// Sum is one app's use of one alias.
type Sum struct {
	App      string `json:"app"`
	Alias    string `json:"alias"`
	Requests int64  `json:"requests"`
	Failures int64  `json:"failures"`
	Input    int64  `json:"inputTokens"`
	Output   int64  `json:"outputTokens"`
}

func nullable(n int64) any {
	if n < 0 {
		return nil
	}
	return n
}

func (s *Server) record(ctx context.Context, r Record) {
	attrs := []any{"app", r.App, "alias", r.Alias, "provider", r.Provider, "model", r.Model, "priority", r.Priority.String(),
		"input_tokens", r.Input, "output_tokens", r.Output, "wait_ms", r.Wait.Milliseconds(),
		"first_byte_ms", r.FirstByte.Milliseconds(), "total_ms", r.Total.Milliseconds(), "status", r.Status}
	s.log.Info("inference", attrs...)
	if r.App == "" || s.db == nil {
		return
	}
	var first any
	if r.FirstByte >= 0 {
		first = r.FirstByte.Milliseconds()
	}
	_, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO usage (at, app, alias, provider, model, priority,
		input_tokens, output_tokens, wait_ms, first_byte_ms, total_ms, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.At.UTC().Format(store.TimeFormat), r.App, r.Alias, r.Provider, r.Model, r.Priority.String(),
		nullable(r.Input), nullable(r.Output), r.Wait.Milliseconds(), first, r.Total.Milliseconds(), r.Status)
	if err != nil {
		s.log.Warn("record inference usage", "err", err)
	}
}

// Usage sums the log per app and alias since t (all of it when zero).
func (s *Server) Usage(ctx context.Context, since time.Time) ([]Sum, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app, alias, COUNT(*), SUM(status >= 400),
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0)
		FROM usage WHERE at >= ? GROUP BY app, alias ORDER BY app, alias`, since.UTC().Format(store.TimeFormat))
	if err != nil {
		return nil, fmt.Errorf("read inference usage: %w", err)
	}
	defer rows.Close()
	out := []Sum{}
	for rows.Next() {
		var u Sum
		if err := rows.Scan(&u.App, &u.Alias, &u.Requests, &u.Failures, &u.Input, &u.Output); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetKey sets provider's key, or removes it when key is empty.
func (s *Server) SetKey(ctx context.Context, provider, key string) error {
	if _, ok := s.state().cfg.Providers[provider]; !ok {
		return ErrNoProvider
	}
	var err error
	if key == "" {
		_, err = s.db.ExecContext(ctx, `DELETE FROM provider_keys WHERE provider = ?`, provider)
	} else {
		_, err = s.db.ExecContext(ctx, `INSERT INTO provider_keys (provider, key, set_at) VALUES (?, ?, ?)
			ON CONFLICT (provider) DO UPDATE SET key = excluded.key, set_at = excluded.set_at`,
			provider, key, s.now().UTC().Format(store.TimeFormat))
	}
	if err != nil {
		return fmt.Errorf("set a provider's key: %w", err)
	}
	s.CheckSoon()
	return nil
}

// ErrNoProvider means the configuration names no such provider.
var ErrNoProvider = errors.New("no such provider")

// key is provider's key, or "".
func (s *Server) key(ctx context.Context, provider string) string {
	if s.db == nil {
		return ""
	}
	var k string
	if err := s.db.QueryRowContext(ctx, `SELECT key FROM provider_keys WHERE provider = ?`, provider).Scan(&k); err != nil &&
		!errors.Is(err, sql.ErrNoRows) {
		s.log.Warn("read a provider's key", "provider", provider, "err", err)
	}
	return k
}

// usageTap reads an answer's usage as it passes, and nothing else of it.
type usageTap struct {
	stream bool
	in     int64
	out    int64
	// body collects a whole answer up to tapLimit; line collects one SSE line.
	body bytes.Buffer
	line bytes.Buffer
	over bool
}

// tapLimit bounds what the tap holds: a whole answer, or one stream line.
const tapLimit = 8 << 20

func newTap(stream bool) *usageTap { return &usageTap{stream: stream, in: -1, out: -1} }

func (t *usageTap) Write(p []byte) {
	if !t.stream {
		if t.body.Len()+len(p) > tapLimit {
			t.over = true
			return
		}
		t.body.Write(p)
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if t.line.Len()+len(p) > tapLimit {
				t.over = true
			} else if !t.over {
				t.line.Write(p)
			}
			return
		}
		if !t.over && t.line.Len()+i <= tapLimit {
			t.line.Write(p[:i])
			t.sseLine(t.line.Bytes())
		}
		t.line.Reset()
		t.over = false
		p = p[i+1:]
	}
}

func (t *usageTap) sseLine(l []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimRight(l, "\r"), []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return
	}
	t.usageFrom(data)
}

// Finish reads a whole answer's usage.
func (t *usageTap) Finish() {
	if !t.stream && !t.over && t.body.Len() > 0 {
		t.usageFrom(t.body.Bytes())
	}
	t.body.Reset()
}

type usageCounts struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
}

// usageFrom takes usage from an answer, a chat completion chunk or a
// responses event, whichever carries one.
func (t *usageTap) usageFrom(b []byte) {
	var v struct {
		Usage    *usageCounts `json:"usage"`
		Response *struct {
			Usage *usageCounts `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	u := v.Usage
	if u == nil && v.Response != nil {
		u = v.Response.Usage
	}
	if u == nil {
		return
	}
	switch {
	case u.PromptTokens != nil:
		t.in = *u.PromptTokens
	case u.InputTokens != nil:
		t.in = *u.InputTokens
	}
	switch {
	case u.CompletionTokens != nil:
		t.out = *u.CompletionTokens
	case u.OutputTokens != nil:
		t.out = *u.OutputTokens
	}
}
