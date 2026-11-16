package allow

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/frostyard/lodge/internal/store"
)

func newLists(t *testing.T) *Lists {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db, func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) })
}

func TestLogins(t *testing.T) {
	l := newLists(t)
	ctx := t.Context()
	if ok, err := l.LoginAllowed(ctx, "alice@example.com"); err != nil || ok {
		t.Fatalf("an empty list allows alice: %v, %v", ok, err)
	}
	if err := l.AllowLogin(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := l.AllowLogin(ctx, "alice@example.com"); !errors.Is(err, ErrConflict) {
		t.Errorf("allowing twice = %v, want ErrConflict", err)
	}
	if ok, _ := l.LoginAllowed(ctx, "alice@example.com"); !ok {
		t.Error("alice is not allowed after AllowLogin")
	}
	// Byte for byte: case and Unicode forms are different logins.
	for _, other := range []string{"Alice@example.com", "alice@example.com ", "alice@examрle.com"} {
		if ok, _ := l.LoginAllowed(ctx, other); ok {
			t.Errorf("%q is allowed by alice's entry", other)
		}
	}
	got, err := l.Logins(ctx)
	if err != nil || len(got) != 1 || got[0].Login != "alice@example.com" || got[0].Added.IsZero() {
		t.Errorf("Logins = %+v, %v", got, err)
	}
	if err := l.DisallowLogin(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := l.DisallowLogin(ctx, "alice@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("disallowing an absent login = %v, want ErrNotFound", err)
	}
	if ok, _ := l.LoginAllowed(ctx, "alice@example.com"); ok {
		t.Error("alice is allowed after DisallowLogin")
	}
}

func TestLoginForm(t *testing.T) {
	l := newLists(t)
	for _, bad := range []string{"", strings.Repeat("a", 257), "a\nb", "a\x00b", "\xff", "a\u0085b"} {
		if err := l.AllowLogin(t.Context(), bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("AllowLogin(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	for _, good := range []string{"a", strings.Repeat("a", 256), "alice@github", "Zoë@example.com"} {
		if !ValidLogin(good) {
			t.Errorf("ValidLogin(%q) = false", good)
		}
	}
}

func TestNodes(t *testing.T) {
	l := newLists(t)
	ctx := t.Context()
	if err := l.AllowNode(ctx, "nAbC123CNTRL", "phone"); err != nil {
		t.Fatal(err)
	}
	if err := l.AllowNode(ctx, "nAbC123CNTRL", "phone"); !errors.Is(err, ErrConflict) {
		t.Errorf("allowing twice = %v, want ErrConflict", err)
	}
	if err := l.AllowNode(ctx, "bad id", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("a bad ID = %v, want ErrInvalid", err)
	}
	if ok, _ := l.NodeAllowed(ctx, "nAbC123CNTRL"); !ok {
		t.Error("the node is not allowed")
	}
	if ok, _ := l.NodeAllowed(ctx, "nOther"); ok {
		t.Error("another node is allowed")
	}
	got, _ := l.Nodes(ctx)
	if len(got) != 1 || got[0].Name != "phone" {
		t.Errorf("Nodes = %+v", got)
	}
	if err := l.DisallowNode(ctx, "nAbC123CNTRL"); err != nil {
		t.Fatal(err)
	}
	if err := l.DisallowNode(ctx, "nAbC123CNTRL"); !errors.Is(err, ErrNotFound) {
		t.Errorf("disallowing an absent node = %v, want ErrNotFound", err)
	}
}

func TestAdmins(t *testing.T) {
	l := newLists(t)
	ctx := t.Context()
	if ok, err := l.AdminAllowed(ctx, "alice@example.com"); err != nil || ok {
		t.Fatalf("an empty list makes alice an admin: %v, %v", ok, err)
	}
	if err := l.AllowAdmin(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := l.AllowAdmin(ctx, "alice@example.com"); !errors.Is(err, ErrConflict) {
		t.Errorf("allowing twice = %v, want ErrConflict", err)
	}
	if ok, _ := l.AdminAllowed(ctx, "alice@example.com"); !ok {
		t.Error("alice is not an admin after AllowAdmin")
	}
	// Byte for byte, as on the login list.
	for _, other := range []string{"Alice@example.com", "alice@example.com "} {
		if ok, _ := l.AdminAllowed(ctx, other); ok {
			t.Errorf("%q is an admin by alice's entry", other)
		}
	}
	got, err := l.Admins(ctx)
	if err != nil || len(got) != 1 || got[0].Login != "alice@example.com" || got[0].Added.IsZero() {
		t.Errorf("Admins = %+v, %v", got, err)
	}
	if err := l.DisallowAdmin(ctx, "alice@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := l.DisallowAdmin(ctx, "alice@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("disallowing an absent admin = %v, want ErrNotFound", err)
	}
	if ok, _ := l.AdminAllowed(ctx, "alice@example.com"); ok {
		t.Error("alice is an admin after DisallowAdmin")
	}
}

func TestAdminForm(t *testing.T) {
	l := newLists(t)
	for name, login := range map[string]string{
		"empty":     "",
		"control":   "alice\x00@example.com",
		"newline":   "alice@example.com\n",
		"too long":  strings.Repeat("a", 257),
		"bad UTF-8": "\xff\xfe",
	} {
		if err := l.AllowAdmin(t.Context(), login); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: AllowAdmin(%q) = %v, want ErrInvalid", name, login, err)
		}
	}
	if got, err := l.Admins(t.Context()); err != nil || len(got) != 0 {
		t.Errorf("Admins after refused logins = %+v, %v", got, err)
	}
}
