package kittest_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/frostyard/lodge/kit/kittest"
	"github.com/frostyard/lodge/kit/lodgeapp"
)

// Health refuses an answer whose badge is negative: the contract's count is
// a whole number from 0 (ADR-0020).
func TestHealthRefusesANegativeBadge(t *testing.T) {
	l := kittest.Start(t, "notes", &lodgeapp.Server{
		Health: func(context.Context) lodgeapp.Health {
			return lodgeapp.Health{OK: true, Version: "1.0", Protocol: 1, Badge: -1}
		},
		API: func(context.Context, net.Conn, lodgeapp.Caller) {},
	})
	_, err := l.Health(t.Context())
	if err == nil || !strings.Contains(err.Error(), "badge -1 is negative") {
		t.Errorf("Health = %v, want it refused", err)
	}
}
