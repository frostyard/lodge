package lodgeapp

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /health carries the app's unread count, and no member when it has
// none (ADR-0020).
func TestHealthBadge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		badge int
		want  string
	}{
		{"no badge", 0, `{"ok":true,"version":"1.0","protocol":1}`},
		{"a badge", 3, `{"ok":true,"version":"1.0","protocol":1,"badge":3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Health: func(context.Context) Health {
				return Health{OK: true, Version: "1.0", Protocol: 1, Badge: tc.badge}
			}}
			m := &mux{s: s, log: slog.New(slog.DiscardHandler)}
			w := httptest.NewRecorder()
			m.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))
			if w.Code != http.StatusOK {
				t.Fatalf("GET /health = %d, want 200", w.Code)
			}
			if got := w.Body.String(); got != tc.want {
				t.Errorf("GET /health = %s, want %s", got, tc.want)
			}
		})
	}
}
