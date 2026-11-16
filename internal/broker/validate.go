package broker

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// nameRE is a client's name, and a subscription's: no '-', so that
// "<client>-<sub>" is one consumer's name without ambiguity.
var nameRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// validateName checks a client's or a subscription's name.
func validateName(what, name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%s %q must match %s", what, name, nameRE)
	}
	return nil
}

// MaxFilters bounds a subscription's filters, its own resync subject
// included: a grant of 5 folders and 3 events gives 30 of them.
const MaxFilters = 64

// ErrTooManyFilters is a subscription whose filters are more than MaxFilters:
// the grant behind it is too wide for 1 consumer.
var ErrTooManyFilters = errors.New("too many filters for a subscription")

// validateFilters checks a subscription's read filters: at least 1, each
// inside the app's subjects, with no empty token, no space, and '>' only
// last.
func validateFilters(app string, filters []string) error {
	if len(filters) == 0 {
		return ErrNoFilters
	}
	for _, f := range filters {
		if err := validateFilter(app, f); err != nil {
			return err
		}
	}
	return nil
}

// validateCount checks that a subscription's filters, its resync subject
// included, are at most MaxFilters.
func validateCount(filters []string) error {
	if len(filters) > MaxFilters {
		return fmt.Errorf("%w: %d of them, and a subscription takes at most %d", ErrTooManyFilters,
			len(filters), MaxFilters)
	}
	return nil
}

// topicTokenRE is a token of a requested topic: "*", or a literal that a
// subject may hold.
var topicTokenRE = regexp.MustCompile(`^(\*|[A-Za-z0-9_%-]+)$`)

// validateTopic checks a subscription's requested topic: 1 or more tokens,
// each "*" or a literal, the first the app's id.
func validateTopic(app, topic string) error {
	tokens := strings.Split(topic, ".")
	for _, tok := range tokens {
		if !topicTokenRE.MatchString(tok) {
			return fmt.Errorf("the topic %q has the token %q, which is no literal and no *", topic, tok)
		}
	}
	if tokens[0] != app {
		return fmt.Errorf("the topic %q does not start with %s, the app's id", topic, app)
	}
	return nil
}

// validateFilter checks one filter.
func validateFilter(app, f string) error {
	if f != app+".>" && !strings.HasPrefix(f, app+".") {
		return fmt.Errorf("filter %q is not inside %s.>", f, app)
	}
	tokens := strings.Split(f, ".")
	for i, tok := range tokens {
		switch {
		case tok == "":
			return fmt.Errorf("filter %q has an empty token", f)
		case strings.ContainsAny(tok, " \t\r\n"):
			return fmt.Errorf("filter %q has a space", f)
		case strings.Contains(tok, ">") && (tok != ">" || i != len(tokens)-1):
			return fmt.Errorf("filter %q has > before its last token", f)
		}
	}
	return nil
}
