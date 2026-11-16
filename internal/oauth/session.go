package oauth

import (
	"encoding/json/v2"
	"time"

	"github.com/ory/fosite"
)

// Session is fosite's session: the subject is the principal the request is
// for, and fosite keeps each token's expiry in it.
type Session struct {
	*fosite.DefaultSession
}

// NewSession returns a session with no principal.
func NewSession() *Session {
	return &Session{DefaultSession: &fosite.DefaultSession{}}
}

// Clone returns a copy of the session.
func (s *Session) Clone() fosite.Session {
	if s == nil {
		return nil
	}
	c := NewSession()
	if s.DefaultSession != nil {
		c.Username = s.Username
		c.Subject = s.Subject
		if s.ExpiresAt != nil {
			c.ExpiresAt = make(map[fosite.TokenType]time.Time, len(s.ExpiresAt))
			for k, v := range s.ExpiresAt {
				c.ExpiresAt[k] = v
			}
		}
	}
	return c
}

// sessionJSON is how a session is stored: JSON, with no claim lodge does not
// set.
type sessionJSON struct {
	ExpiresAt map[string]time.Time `json:"expires_at"`
	Username  string               `json:"username,omitzero"`
	Subject   string               `json:"subject,omitzero"`
}

// sessionFrom is the stored form of s.
func sessionFrom(s fosite.Session) sessionJSON {
	var out sessionJSON
	if s == nil {
		return out
	}
	out.Username, out.Subject = s.GetUsername(), s.GetSubject()
	if sess, ok := s.(*Session); ok && sess.DefaultSession != nil {
		if len(sess.ExpiresAt) > 0 {
			out.ExpiresAt = make(map[string]time.Time, len(sess.ExpiresAt))
			for k, v := range sess.ExpiresAt {
				out.ExpiresAt[string(k)] = v
			}
		}
	}
	return out
}

// session is the session the stored form holds.
func (j sessionJSON) session() *Session {
	s := NewSession()
	s.Username, s.Subject = j.Username, j.Subject
	if len(j.ExpiresAt) > 0 {
		s.ExpiresAt = make(map[fosite.TokenType]time.Time, len(j.ExpiresAt))
		for k, v := range j.ExpiresAt {
			s.ExpiresAt[fosite.TokenType(k)] = v
		}
	}
	return s
}

// set copies another session into s.
func (s *Session) set(other *Session) {
	if other == nil || other.DefaultSession == nil {
		return
	}
	if s.DefaultSession == nil {
		s.DefaultSession = &fosite.DefaultSession{}
	}
	if s.ExpiresAt == nil {
		s.ExpiresAt = make(map[fosite.TokenType]time.Time, len(other.ExpiresAt))
	}
	for k, v := range other.ExpiresAt {
		s.ExpiresAt[k] = v
	}
	s.Username, s.Subject = other.Username, other.Subject
}

// jsonMarshal encodes v as lodge's own JSON.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// jsonUnmarshal decodes b, whose members v does not name are ignored.
func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v, json.RejectUnknownMembers(false))
}
