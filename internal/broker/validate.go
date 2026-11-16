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

// MaxFilters bounds a subscription's filters.
const MaxFilters = 16

// validateFilters checks a subscription's filters: each inside the app's
// subjects, with no empty token, no space, and '>' only last.
func validateFilters(app string, filters []string) error {
	switch {
	case len(filters) == 0:
		return errors.New("a subscription needs at least one filter")
	case len(filters) > MaxFilters:
		return fmt.Errorf("a subscription takes at most %d filters, not %d", MaxFilters, len(filters))
	}
	for _, f := range filters {
		if err := validateFilter(app, f); err != nil {
			return err
		}
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
