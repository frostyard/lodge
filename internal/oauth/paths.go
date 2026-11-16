package oauth

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// MaxPath bounds one path of a grant's item.
const MaxPath = 512

// ValidPath reports whether p is a path of a grant's item: "/", or
// /-separated segments in Unicode NFC, with no empty, "." or ".." segment and
// no trailing "/" (docs/specs/oauth.md, Principals, clients and grants).
func ValidPath(p string) bool {
	if p == "" || len(p) > MaxPath || norm.NFC.String(p) != p {
		return false
	}
	if p == "/" {
		return true
	}
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for seg := range strings.SplitSeq(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Covers reports whether path g covers path p: p is g, g is "/", or p starts
// with g and "/". An approval may narrow a path to a descendant, never
// widen it to a parent or a sibling.
func Covers(g, p string) bool {
	switch g {
	case p, "/":
		return true
	}
	return strings.HasPrefix(p, g+"/")
}

// Narrow reports whether given is a path an approval of asked may use: a
// valid path that asked covers.
func Narrow(asked, given string) bool {
	return ValidPath(given) && Covers(asked, given)
}
