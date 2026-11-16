package events

import (
	"encoding/base32"
	"encoding/base64"
	"regexp"
	"strings"
)

// clientIDPrefix starts every OAuth client's id (internal/oauth).
const clientIDPrefix = "lgc_"

// clientNameRE is the form of the broker's name of a client: "o" and 26
// lower-case base32 characters, which is 16 bytes.
var clientNameRE = regexp.MustCompile(`^o[a-z2-7]{26}$`)

// base32 is the encoding of a client's name: standard alphabet, no padding.
var base32Alphabet = base32.StdEncoding.WithPadding(base32.NoPadding)

// ClientName returns the name the broker knows the OAuth client id by, and
// whether id is an OAuth client's: "o" and the lower-case base32 of the 16
// random bytes of its id, which fits the broker's
// ^[a-z0-9_]{1,32}$.
func ClientName(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, clientIDPrefix)
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return "o" + strings.ToLower(base32Alphabet.EncodeToString(raw)), true
}

// ClientID returns the OAuth client id that the broker's name name belongs
// to, and whether name is one of them.
func ClientID(name string) (string, bool) {
	if !clientNameRE.MatchString(name) {
		return "", false
	}
	raw, err := base32Alphabet.DecodeString(strings.ToUpper(name[1:]))
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return clientIDPrefix + base64.RawURLEncoding.EncodeToString(raw), true
}
