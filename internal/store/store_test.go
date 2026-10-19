package store

import (
	"path/filepath"
	"testing"
)

func TestOpenMigratesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lodge.db")
	for range 2 {
		db, err := Open(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("schema_migrations has %d rows, want 1", n)
		}
		db.Close()
	}
}

func TestOpenRefusesQueryCharacters(t *testing.T) {
	if _, err := Open(t.Context(), filepath.Join(t.TempDir(), "a?b.db")); err == nil {
		t.Error("Open accepted a path with ?")
	}
}

func TestIsConstraint(t *testing.T) {
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "lodge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO logins VALUES ('a', 'x')`); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO logins VALUES ('a', 'y')`)
	if !IsConstraint(err) {
		t.Errorf("a duplicate login: IsConstraint(%v) = false", err)
	}
	if IsConstraint(nil) {
		t.Error("IsConstraint(nil) = true")
	}
}

func TestRefuseNetwork(t *testing.T) {
	for _, local := range []int64{0xef53 /* ext4 */, 0x9123683e /* btrfs */, 0x58465342 /* xfs */, 0x01021994 /* tmpfs */} {
		if err := refuseNetwork(local); err != nil {
			t.Errorf("refuseNetwork(%#x) = %v, want nil", local, err)
		}
	}
	for _, remote := range []int64{0x6969, 0xff534d42, 0xfe534d42, 0x01021997, 0x65735546, -0xacb2be /* cifs as a signed int32 */} {
		if err := refuseNetwork(remote); err == nil {
			t.Errorf("refuseNetwork(%#x) = nil, want an error", remote)
		}
	}
}

func TestCheckLocalTempDir(t *testing.T) {
	if err := checkLocal(t.TempDir()); err != nil {
		t.Skipf("the temp dir is on a network file system here: %v", err)
	}
}
