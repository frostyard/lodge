// Package events is lodge's policy for grant-driven event subscriptions: the
// read filters that a principal's grant gives on an app's events, the name
// the broker knows an OAuth client by, and the service that keeps a
// subscription in step with the grant (ADR-0022; docs/specs/lodge-http.md,
// The broker).
package events

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/oauth"
)

// rootPath is a grant item's root path: it covers every path.
const rootPath = "/"

// MaxTopicTokens bounds a requested topic: a folder 5 levels deep is 1 of
// its tokens.
const MaxTopicTokens = 32

// requestToken is the form of 1 token of a requested topic: "*", or a
// literal.
var requestToken = regexp.MustCompile(`^(\*|[A-Za-z0-9_%-]+)$`)

// pattern is 1 read pattern of a subscription: the tokens of a subject, and
// whether it also takes every subject below them.
type pattern struct {
	tokens []string
	deep   bool
}

// Filters returns the broker filters that read the events under topic which
// items read: the effective read prefixes of a subscription, as hearth's
// ReadFilters derives them for 1 item (docs/specs/oauth.md, Events).
//
// topic is a prefix of the subjects a client wants: 1 or more tokens, each
// "*" or a literal, the first a literal app id. A pattern that another
// covers is dropped, and the answer holds no repeat: T for each subject, and
// T.> when the pattern is deep. A grant that reads nothing there is an
// empty, non-nil list.
func Filters(evs []apps.Event, items []oauth.Item, topic string) ([]string, error) {
	request, err := parseRequest(topic)
	if err != nil {
		return nil, err
	}
	pats := make([]pattern, 0, len(evs)*len(items))
	for _, item := range items {
		for _, ev := range evs {
			if ev.ReadPermission != item.Cap {
				continue
			}
			g, ok, err := readPattern(ev, item)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if p, ok := intersect(g, request); ok {
				pats = append(pats, p)
			}
		}
	}
	return render(removeCovered(pats)), nil
}

// parseRequest reads a requested topic, and answers its tokens.
func parseRequest(topic string) ([]string, error) {
	tokens := strings.Split(topic, ".")
	if len(tokens) > MaxTopicTokens {
		return nil, fmt.Errorf("the topic %q has more than %d tokens: %w", topic, MaxTopicTokens, ErrInvalid)
	}
	for _, tok := range tokens {
		if !requestToken.MatchString(tok) {
			return nil, fmt.Errorf("the topic %q has the token %q, which is no literal and no *: %w", topic, tok, ErrInvalid)
		}
	}
	if tokens[0] == "*" || !apps.ValidID(tokens[0]) {
		return nil, fmt.Errorf("the topic %q does not start with an app's id: %w", topic, ErrInvalid)
	}
	return tokens, nil
}

// readPattern answers the pattern that item reads of event, and whether it
// reads anything there: the event's whole topic, or the topic and the item's
// path of the event's selector, and every subject below them.
func readPattern(ev apps.Event, item oauth.Item) (pattern, bool, error) {
	if !coversAnySelector(ev, item) {
		return pattern{}, false, nil
	}
	tokens := strings.Split(ev.Topic, ".")
	if ev.Selector == "" {
		return pattern{tokens: tokens}, true, nil
	}
	path := item.Sel[ev.Selector]
	if path == "" || path == rootPath {
		return pattern{tokens: tokens, deep: true}, true, nil
	}
	if !oauth.ValidPath(path) {
		return pattern{}, false, fmt.Errorf("the %s path %q is not a path: %w", ev.Selector, path, ErrInvalid)
	}
	for seg := range strings.SplitSeq(path[1:], "/") {
		tokens = append(tokens, encodeSegment(seg))
	}
	return pattern{tokens: tokens, deep: true}, true, nil
}

// coversAnySelector reports whether item covers the events of ev: an item
// that names another selector at a path that is not the root covers no event
// of it, because the event carries no value of that selector, which is the
// root.
func coversAnySelector(ev apps.Event, item oauth.Item) bool {
	for name, path := range item.Sel {
		if name == ev.Selector || path == "" || path == rootPath {
			continue
		}
		return false
	}
	return true
}

// upperHex are the upper-case hex digits that encodeSegment writes.
const upperHex = "0123456789ABCDEF"

// encodeSegment returns 1 path segment as 1 topic token: every byte outside
// [A-Za-z0-9-] is percent-encoded with upper-case hex.
func encodeSegment(segment string) string {
	var b strings.Builder
	b.Grow(len(segment))
	for i := range len(segment) {
		c := segment[i]
		if tokenByte(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperHex[c>>4])
		b.WriteByte(upperHex[c&0x0f])
	}
	return b.String()
}

// tokenByte reports whether 1 byte needs no encoding.
func tokenByte(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		return true
	}
	return c == '-'
}

// intersect answers the pattern of the subjects that both g and request take,
// and whether any is left.
func intersect(g pattern, request []string) (pattern, bool) {
	n := min(len(g.tokens), len(request))
	merged := make([]string, 0, max(len(g.tokens), len(request)))
	for i := range n {
		tok, want := request[i], g.tokens[i]
		switch {
		case tok == want:
			merged = append(merged, tok)
		case tok == "*":
			merged = append(merged, want)
		case want == "*":
			merged = append(merged, tok)
		default:
			return pattern{}, false
		}
	}
	if len(request) <= len(g.tokens) {
		// The grant is the narrower of the 2: it keeps its own tokens.
		merged = append(merged, g.tokens[n:]...)
		return pattern{tokens: merged, deep: g.deep}, true
	}
	// The request is wider than the grant: only a deep grant reaches it.
	if !g.deep {
		return pattern{}, false
	}
	merged = append(merged, request[n:]...)
	return pattern{tokens: merged, deep: true}, true
}

// covers reports whether b takes every subject that a takes.
func covers(b, a pattern) bool {
	if len(b.tokens) > len(a.tokens) {
		return false
	}
	for i, tok := range b.tokens {
		if tok != "*" && tok != a.tokens[i] {
			return false
		}
	}
	return b.deep || (len(b.tokens) == len(a.tokens) && !a.deep)
}

// removeCovered drops each pattern that another covers, and keeps 1 of equal
// patterns.
func removeCovered(pats []pattern) []pattern {
	slices.SortFunc(pats, func(a, b pattern) int {
		if c := strings.Compare(strings.Join(a.tokens, "."), strings.Join(b.tokens, ".")); c != 0 {
			return c
		}
		switch {
		case a.deep == b.deep:
			return 0
		case a.deep:
			return 1
		}
		return -1
	})
	kept := make([]pattern, 0, len(pats))
	for i, a := range pats {
		covered := false
		for j, b := range pats {
			if i == j || !covers(b, a) {
				continue
			}
			// Equal patterns cover each other: the first one stays.
			if covers(a, b) && j > i {
				continue
			}
			covered = true
			break
		}
		if !covered {
			kept = append(kept, a)
		}
	}
	return kept
}

// render answers the NATS filters of the patterns: the subject of each, and
// every subject below it when it is deep, sorted and without a repeat.
func render(pats []pattern) []string {
	filters := []string{}
	for _, p := range pats {
		subject := strings.Join(p.tokens, ".")
		filters = append(filters, subject)
		if p.deep {
			filters = append(filters, subject+".>")
		}
	}
	slices.Sort(filters)
	return slices.Compact(filters)
}
