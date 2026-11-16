package main

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	var b bytes.Buffer
	printVersion(&b)
	if got, want := b.String(), "lodge dev (none)\n"; got != want {
		t.Errorf("printVersion = %q, want %q", got, want)
	}
}

// TestAfterAppsLoadTellsTheReaders checks a reload whose broker step fails:
// the registry is loaded already, so the gateway and the event subscriptions
// still read it, and the reload still reports the step's error. A grant that
// an app's new major version takes out of force must not outlive the reload
// in an event consumer's filters.
func TestAfterAppsLoadTellsTheReaders(t *testing.T) {
	failed := errors.New("the broker's streams")
	var ran []string
	err := afterAppsLoad([]func() error{
		func() error { ran = append(ran, "sync"); return failed },
		func() error { ran = append(ran, "next"); return nil },
	}, []func(){
		func() { ran = append(ran, "gateway") },
		func() { ran = append(ran, "events") },
	})
	if !errors.Is(err, failed) {
		t.Errorf("afterAppsLoad = %v, want the step's error", err)
	}
	if got := fmt.Sprint(ran); got != "[sync next gateway events]" {
		t.Errorf("ran %s, want every step, then every reader", got)
	}
	if err := afterAppsLoad(nil, nil); err != nil {
		t.Errorf("afterAppsLoad with nothing to do = %v", err)
	}
}
