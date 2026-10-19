// Command lodge is the tailnet web shell for frostyard's first-party apps
// (docs/design/overview.md). Until the roadmap's Phase 2 lands it only
// prints its version (docs/plans/0001-roadmap.md).
package main

import (
	"fmt"
	"io"
	"os"
)

// Set by the Makefile's -ldflags.
var (
	version = "dev"
	commit  = "none"
)

func main() {
	printVersion(os.Stdout)
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "lodge %s (%s)\n", version, commit)
}
