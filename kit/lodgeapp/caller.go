package lodgeapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Kind is a caller kind: the one value of Lodge-Caller that a path admits
// (app-contract.md, What an app serves on its socket).
type Kind string

// The caller kinds of contract 1.
const (
	// Session is a browser in the shell: /api and /x/.
	Session Kind = "session"
	// Agent is an MCP client with a token: /mcp.
	Agent Kind = "agent"
	// Device is a request the app authenticates itself: /device/.
	Device Kind = "device"
	// Content is a browser on the content node: /content/.
	Content Kind = "content"
)

// The headers lodge sets on every proxied request, after deleting any
// Lodge-* header the client sent (app-contract.md, Caller headers).
const (
	HeaderCaller      = "Lodge-Caller"
	HeaderLogin       = "Lodge-Login"
	HeaderDisplayName = "Lodge-Display-Name"
	HeaderNodeID      = "Lodge-Node-ID"
	HeaderNodeName    = "Lodge-Node-Name"
	HeaderSession     = "Lodge-Session"
	HeaderToken       = "Lodge-Token"
	HeaderScope       = "Lodge-Scope"
)

// Caller is who lodge says sent a request. Which fields are set depends on
// Kind:
//
//   - Session and Content: Login, NodeID and NodeName, and DisplayName when
//     Tailscale has one. Session is set on the /api upgrade only.
//   - Agent: Token and Scope.
//   - Device: NodeID and NodeName when WhoIs named the peer, and Login when
//     the node is untagged. They are for display only: an app MUST NOT
//     authorize a device route on them (Rule 12).
type Caller struct {
	Kind Kind
	// Login is the node owner's tailnet login, decoded:
	// alice@example.com, alice@github.
	Login string
	// DisplayName is the owner's display name, decoded; it may be empty.
	DisplayName string
	// NodeID is the node's Tailscale StableNodeID.
	NodeID string
	// NodeName is the node's short host name, [a-z0-9-].
	NodeName string
	// Session is the browser session's ID, 16 lowercase hex digits, on the
	// /api upgrade.
	Session string
	// Token is the MCP token's name, [A-Za-z0-9@._-]{1,64}.
	Token string
	// Scope is the token's scope, whose grammar is the app's own.
	Scope string
}

// Name is what an app attributes a caller's writes to: a session's or
// content caller's node name, an agent's token name, and for a device the
// node name lodge saw, or "device".
func (c Caller) Name() string {
	switch c.Kind {
	case Agent:
		return c.Token
	case Device:
		if c.NodeName == "" {
			return string(Device)
		}
	}
	return c.NodeName
}

type callerKey struct{}

// WithCaller returns ctx carrying c, as the server gives each request. It is
// for tests of handlers that read CallerFrom.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// CallerFrom returns the caller of a request the server admitted. Every
// request but /health has one.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// ParseCaller reads the Lodge-* headers of a request into a Caller, and
// checks each value's form. It returns an error when Lodge-Caller is
// missing or unknown, a header the kind requires is missing, or a value is
// malformed or repeated.
func ParseCaller(h http.Header) (Caller, error) {
	kind, err := one(h, HeaderCaller)
	if err != nil {
		return Caller{}, err
	}
	c := Caller{Kind: Kind(kind)}
	switch c.Kind {
	case Session, Content:
		if c.Login, err = decoded(h, HeaderLogin, true); err != nil {
			return Caller{}, err
		}
		if c.DisplayName, err = decoded(h, HeaderDisplayName, false); err != nil {
			return Caller{}, err
		}
		if c.NodeID, err = checked(h, HeaderNodeID, true, validNodeID); err != nil {
			return Caller{}, err
		}
		if c.NodeName, err = checked(h, HeaderNodeName, true, validNodeName); err != nil {
			return Caller{}, err
		}
		if c.Kind == Session {
			if c.Session, err = checked(h, HeaderSession, false, validSession); err != nil {
				return Caller{}, err
			}
		}
	case Agent:
		if c.Token, err = checked(h, HeaderToken, true, validToken); err != nil {
			return Caller{}, err
		}
		if c.Scope, err = checked(h, HeaderScope, true, validScope); err != nil {
			return Caller{}, err
		}
	case Device:
		if c.Login, err = decoded(h, HeaderLogin, false); err != nil {
			return Caller{}, err
		}
		if c.NodeID, err = checked(h, HeaderNodeID, false, validNodeID); err != nil {
			return Caller{}, err
		}
		if c.NodeName, err = checked(h, HeaderNodeName, false, validNodeName); err != nil {
			return Caller{}, err
		}
	case "":
		return Caller{}, fmt.Errorf("no %s", HeaderCaller)
	default:
		return Caller{}, fmt.Errorf("unknown %s %q", HeaderCaller, kind)
	}
	return c, nil
}

// SetHeaders writes c into h as lodge does: it deletes every Lodge-* header
// in h, then sets c's non-empty fields, encoding Login and DisplayName.
// lodge's gateway and kittest both use it, so the two sides agree.
func (c Caller) SetHeaders(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "Lodge-") {
			delete(h, k)
		}
	}
	set := func(k, v string) {
		if v != "" {
			h.Set(k, v)
		}
	}
	set(HeaderCaller, string(c.Kind))
	set(HeaderLogin, EncodeHeader(c.Login))
	set(HeaderDisplayName, EncodeHeader(c.DisplayName))
	set(HeaderNodeID, c.NodeID)
	set(HeaderNodeName, c.NodeName)
	set(HeaderSession, c.Session)
	set(HeaderToken, c.Token)
	set(HeaderScope, c.Scope)
}

// one returns a header's single value, "" when it is absent, and an error
// when it is repeated.
func one(h http.Header, k string) (string, error) {
	vs := h.Values(k)
	switch len(vs) {
	case 0:
		return "", nil
	case 1:
		return vs[0], nil
	}
	return "", fmt.Errorf("%s is repeated", k)
}

func checked(h http.Header, k string, required bool, valid func(string) bool) (string, error) {
	v, err := one(h, k)
	switch {
	case err != nil:
		return "", err
	case v == "" && required:
		return "", fmt.Errorf("no %s", k)
	case v != "" && !valid(v):
		return "", fmt.Errorf("malformed %s", k)
	}
	return v, nil
}

func decoded(h http.Header, k string, required bool) (string, error) {
	v, err := checked(h, k, required, func(string) bool { return true })
	if err != nil || v == "" {
		return v, err
	}
	d, err := DecodeHeader(v)
	if err != nil {
		return "", fmt.Errorf("malformed %s: %w", k, err)
	}
	if d == "" && required {
		return "", fmt.Errorf("no %s", k)
	}
	return d, nil
}

// unreserved reports whether b passes through EncodeHeader unescaped:
// A–Z a–z 0–9 - . _ ~ @ +.
func unreserved(b byte) bool {
	switch {
	case 'A' <= b && b <= 'Z', 'a' <= b && b <= 'z', '0' <= b && b <= '9':
		return true
	}
	return strings.IndexByte("-._~@+", b) >= 0
}

// EncodeHeader percent-encodes s (RFC 3986) for Lodge-Login and
// Lodge-Display-Name: every byte outside A–Z a–z 0–9 - . _ ~ @ + becomes
// %XX, with uppercase hex digits.
func EncodeHeader(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if unreserved(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// DecodeHeader reverses EncodeHeader. It refuses a byte that should have
// been encoded, a malformed escape, and a result that is not UTF-8.
func DecodeHeader(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '%' {
			if !unreserved(c) {
				return "", fmt.Errorf("unencoded byte %q", c)
			}
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(s) {
			return "", errors.New("truncated escape")
		}
		hi, ok1 := unhex(s[i+1])
		lo, ok2 := unhex(s[i+2])
		if !ok1 || !ok2 {
			return "", fmt.Errorf("bad escape %q", s[i:i+3])
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	if !utf8.ValidString(b.String()) {
		return "", errors.New("not UTF-8")
	}
	return b.String(), nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func all(s string, max int, ok func(byte) bool) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := range len(s) {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func alnum(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9'
}

// validNodeID admits a StableNodeID: up to 128 visible ASCII characters.
func validNodeID(s string) bool {
	return all(s, 128, func(b byte) bool { return '!' <= b && b <= '~' })
}

// validNodeName admits the first label of a DNS name: [a-z0-9-], up to 63.
func validNodeName(s string) bool {
	return all(s, 63, func(b byte) bool { return 'a' <= b && b <= 'z' || '0' <= b && b <= '9' || b == '-' })
}

// validSession admits 16 lowercase hex digits.
func validSession(s string) bool {
	return len(s) == 16 && all(s, 16, func(b byte) bool { return '0' <= b && b <= '9' || 'a' <= b && b <= 'f' })
}

// validToken admits a token name: [A-Za-z0-9@._-]{1,64}.
func validToken(s string) bool {
	return all(s, 64, func(b byte) bool { return alnum(b) || strings.IndexByte("@._-", b) >= 0 })
}

// validScope admits 1 to 256 visible ASCII characters, ! to ~.
func validScope(s string) bool {
	return all(s, 256, func(b byte) bool { return '!' <= b && b <= '~' })
}
