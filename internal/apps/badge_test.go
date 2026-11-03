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
		{"negative zero", "-0", 0},
		{"a fraction", "2.5", 0},
		{"a long fraction below one", "1.0000000000000001", 0},
		{"a long fraction above one", "12345678901234567.5", 0},
		{"a whole number in float form", "3.0", 3},
		{"a whole number and a zero", "99999.0", 99999},
		{"the cap and a zero", "100000.0", 99999},
		{"zero and a zero", "0.0", 0},
		{"a fraction with trailing zeros", "3.50", 0},
		{"an exponent makes it whole", "30e-1", 3},
		{"an exponent makes it a fraction", "35e-1", 0},
		{"an upper case exponent", "1E5", 99999},
		{"a huge negative exponent", "1e-999999999999999999999", 0},
		{"a huge positive exponent", "1e999999999999999999999", 99999},
		{"zero with a huge exponent", "0e999999999999999999999", 0},
		{"null", "null", 0},
		{"a string", `"3"`, 0},
		{"a bool", "true", 0},
		{"an object", `{"n":3}`, 0},
		{"an array", "[3]", 0},
		{"the largest int exponent", "10e9223372036854775807", 99999},
		{"the smallest int exponent", "1.5e-9223372036854775808", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := badge(jsontext.Value(tc.v)); got != tc.want {
				t.Errorf("badge(%s) = %d, want %d", tc.v, got, tc.want)
			}
		})
	}
}
