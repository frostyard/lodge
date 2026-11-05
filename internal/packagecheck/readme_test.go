package packagecheck

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// versionQuote is a quoted version line in prose: "prints `lodge 1.2.3 (…)`".
var versionQuote = regexp.MustCompile("prints?\\s+`(lodge|lodgectl) ")

// sentenceEnd ends the prose before a quote: a full stop, a colon, or a
// blank line.
var sentenceEnd = regexp.MustCompile(`[.:]\s|\n\s*\n`)

// TestReadmeVersionQuotesNameTheirCommand checks packaging/README.md's
// quoted version lines: each sentence that says a version line is printed
// names the command that prints it (`lodge --version` or `lodgectl
// version`), so the README never puts lodge's line in lodgectl's mouth.
func TestReadmeVersionQuotesNameTheirCommand(t *testing.T) {
	b, err := os.ReadFile("../../packaging/README.md")
	if err != nil {
		t.Fatal(err)
	}
	checked := checkVersionQuotes(t, string(b))
	if checked == 0 {
		t.Fatal("packaging/README.md quotes no version line: the check checked nothing")
	}
	t.Logf("checked %d version quotes", checked)
}

// TestVersionQuoteCheckFails feeds the check prose it must refuse.
func TestVersionQuoteCheckFails(t *testing.T) {
	for _, text := range []string{
		"The .deb's files are the tag's, and its binaries print `lodge X.Y.Z (<commit>)`.",
		"Run `lodgectl version`. Then `lodge X` prints `lodge 1 (c)`.",
		"`lodgectl version` prints `lodge 1.2.3 (c)`.",
	} {
		if checkOne(text) {
			t.Errorf("accepted %q", text)
		}
	}
	if !checkOne("`/opt/lodge/bin/lodge --version` prints\n`lodge <version> (<commit>)`.") {
		t.Error("refused a correct quote")
	}
}

func checkVersionQuotes(t *testing.T, text string) int {
	t.Helper()
	n := 0
	for _, m := range versionQuote.FindAllStringSubmatchIndex(text, -1) {
		n++
		if !quoteOK(text, m) {
			t.Errorf("packaging/README.md: %q quotes `%s …` without naming the command that prints it",
				sentenceBefore(text, m[0]), text[m[2]:m[3]])
		}
	}
	return n
}

// checkOne reports whether every quote in text passes; it needs one quote.
func checkOne(text string) bool {
	ms := versionQuote.FindAllStringSubmatchIndex(text, -1)
	if len(ms) == 0 {
		return false
	}
	for _, m := range ms {
		if !quoteOK(text, m) {
			return false
		}
	}
	return true
}

func quoteOK(text string, m []int) bool {
	name := text[m[2]:m[3]]
	cmd := map[string]string{"lodge": name + " --version`", "lodgectl": name + " version`"}[name]
	return strings.Contains(sentenceBefore(text, m[0]), cmd)
}

// sentenceBefore is the text from the last sentence end before i up to i.
func sentenceBefore(text string, i int) string {
	head := text[:i]
	start := 0
	for _, e := range sentenceEnd.FindAllStringIndex(head, -1) {
		start = e[1]
	}
	return head[start:]
}
