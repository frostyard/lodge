//go:build e2e

package e2eid

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContentCORP(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"explicit status": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
			w.WriteHeader(http.StatusForbidden)
		},
		"implicit status": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
			w.Write([]byte("x"))
		},
		"flushed": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("flush: %v", err)
			}
		},
	} {
		rec := httptest.NewRecorder()
		ContentCORP(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/notes/b", nil))
		if got := rec.Result().Header.Values("Cross-Origin-Resource-Policy"); len(got) != 1 || got[0] != "cross-origin" {
			t.Errorf("%s: CORP = %q, want cross-origin", name, got)
		}
	}
}
