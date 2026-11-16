package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"time"
)

// errBadToken is a CSRF token that is not the one lodge made.
var errBadToken = errors.New("the CSRF token is not one lodge made")

// errStaleToken is a CSRF token that was good at its time, but is not now.
var errStaleToken = errors.New("the CSRF token is too old")

// csrf is lodge's CSRF token for parts: the HMAC-SHA256, under the page key,
// of the parts with a NUL byte between them, in base64url
// (docs/specs/oauth.md, CSRF tokens).
func (s *Server) csrf(parts ...string) string {
	h := hmac.New(sha256.New, s.pagesKey)
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// requestCSRF is a request's CSRF token, which only its consent page holds.
func (s *Server) requestCSRF(id string) string { return s.csrf("consent", id) }

// pageCSRF is the grants page's CSRF token, for an admin's login and the
// page's time in Unix seconds.
func (s *Server) pageCSRF(admin string, ts int64) string {
	return s.csrf("grants", admin, strconv.FormatInt(ts, 10))
}

// checkRequestCSRF checks a decision's CSRF token in constant time.
func (s *Server) checkRequestCSRF(id, given string) error {
	if !hmac.Equal([]byte(given), []byte(s.requestCSRF(id))) {
		return errBadToken
	}
	return nil
}

// checkPageCSRF checks the grants page's CSRF token, and the time it was
// made: it is good from 1 minute before ts until 1 hour after it.
func (s *Server) checkPageCSRF(admin, given, tsText string) error {
	ts, err := strconv.ParseInt(tsText, 10, 64)
	if err != nil {
		return errBadToken
	}
	now, at := s.now(), time.Unix(ts, 0)
	if now.Before(at.Add(-pageSkew)) || now.After(at.Add(pageLifetime)) {
		return errStaleToken
	}
	if !hmac.Equal([]byte(given), []byte(s.pageCSRF(admin, ts))) {
		return errBadToken
	}
	return nil
}

// pageStamp is the grants page's time, in Unix seconds.
func (s *Server) pageStamp() string { return strconv.FormatInt(s.now().Unix(), 10) }
