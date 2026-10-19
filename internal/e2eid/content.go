//go:build e2e

package e2eid

import "net/http"

// ContentCORP wraps the development build's content handler so that every
// response carries Cross-Origin-Resource-Policy: cross-origin in place of
// same-site. In production LODGE and CONTENT share a registrable domain
// (<tailnet>.ts.net), so same-site admits lodge's pages; Chromium does not
// count two ports of 127.0.0.1 as one site, and would block every content
// image an app frame loads (net::ERR_BLOCKED_BY_RESPONSE.NotSameSite).
func ContentCORP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(&corpWriter{ResponseWriter: w}, r)
	})
}

type corpWriter struct {
	http.ResponseWriter
	wrote bool
}

func (c *corpWriter) WriteHeader(code int) {
	if !c.wrote {
		c.wrote = true
		c.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *corpWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}

// FlushError sets the header before a flush sends it, for
// http.ResponseController and the reverse proxy's streaming.
func (c *corpWriter) FlushError() error {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(c.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the writer's other methods.
func (c *corpWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }
