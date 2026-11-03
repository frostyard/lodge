package apps

import (
	"encoding/json/jsontext"
	"testing"
)

// badge keeps a whole number from 0, caps it, and makes anything else 0
// (ADR-0020).
func TestBadge(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    string
		want int
	}{
		{"missing", "", 0},
		{"zero", "0", 0},
		{"three", "3", 3},
		{"the cap", "99999", 99999},
		{"above the cap", "100000", 99999},
		{"a big one in float form", "1e9", 99999},
		{"bigger than an int64", "9223372036854775808", 99999},
		{"negative", "-1", 0},
		{"a fraction", "2.5", 0},
		{"a whole number in float form", "3.0", 3},
		{"null", "null", 0},
		{"a string", `"3"`, 0},
		{"a bool", "true", 0},
		{"an object", `{"n":3}`, 0},
		{"an array", "[3]", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := badge(jsontext.Value(tc.v)); got != tc.want {
				t.Errorf("badge(%s) = %d, want %d", tc.v, got, tc.want)
			}
		})
	}
}
