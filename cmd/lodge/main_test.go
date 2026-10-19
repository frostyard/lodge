package main

import (
	"bytes"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	var b bytes.Buffer
	printVersion(&b)
	if got, want := b.String(), "lodge dev (none)\n"; got != want {
		t.Errorf("printVersion = %q, want %q", got, want)
	}
}
