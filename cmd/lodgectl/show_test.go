package main

import (
	"bytes"
	"testing"
	"time"
)

// showTable runs a list command's show on raw and returns its stdout. The
// test fails when the command returns an error or writes to stderr.
func showTable(t *testing.T, f func(*out, []byte) error, raw string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := f(&out{stdout: &stdout, stderr: &stderr}, []byte(raw)); err != nil {
		t.Fatalf("show returned %v, want nil", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
	return stdout.String()
}

// wantTable fails when the table a command printed is not want.
func wantTable(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("stdout = %q\nwant %q", got, want)
	}
}

// zone is the machine's time zone, which here reports.
func zone() *time.Location { return time.Now().Location() }

// at formats t the way when does: in the machine's time zone.
func at(t time.Time) string { return t.In(zone()).Format("2006-01-02 15:04") }

// clock formats t the way showSessions formats a session's seen time.
func clock(t time.Time) string { return t.In(zone()).Format("15:04:05") }

func TestShowUsage(t *testing.T) {
	raw := `[{"app":"frostnotes","alias":"local","requests":12,"failures":1,"inputTokens":3400,"outputTokens":950},
	         {"app":"frostmail","alias":"","requests":0,"failures":0,"inputTokens":0,"outputTokens":0}]`
	want := "APP         ALIAS  REQUESTS  FAILURES  INPUT TOKENS  OUTPUT TOKENS\n" +
		"frostnotes  local  12        1         3400          950\n" +
		"frostmail          0         0         0             0\n"
	wantTable(t, showTable(t, showUsage, raw), want)
}

func TestShowApps(t *testing.T) {
	raw := `[{"id":"notes","manifest":{"name":"Notes","version":"1.2.0","content":true,"device":true,"mcp":true},"key":"n","state":"up","reason":"","notes":[""]},
	         {"id":"read","manifest":{"name":"Read","version":"0.1.0"},"key":"","state":"degraded","reason":"protocol mismatch","notes":["key taken by notes",""]}]`
	want := "ID     NAME   VERSION  KEY  STATE     FLAGS               REASON\n" +
		"notes  Notes  1.2.0    n    up        content,device,mcp  \n" +
		"read   Read   0.1.0    -    degraded                      protocol mismatch; key taken by notes\n"
	wantTable(t, showTable(t, showApps, raw), want)
}

func TestShowLogins(t *testing.T) {
	alice := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	raw := `[{"login":"alice@example.com","added":"2026-11-04T09:30:00Z"},
	         {"login":"bob@example.com","added":"0001-01-01T00:00:00Z"}]`
	want := "LOGIN              ADDED\n" +
		"alice@example.com  " + at(alice) + "\n" +
		"bob@example.com    -\n"
	wantTable(t, showTable(t, showLogins, raw), want)
}

func TestShowLoginsEmpty(t *testing.T) {
	wantTable(t, showTable(t, showLogins, `[]`), "LOGIN  ADDED\n")
}

func TestShowNodes(t *testing.T) {
	alice := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	raw := `[{"id":"node1","name":"phone","added":"2026-11-04T09:30:00Z"},
	         {"id":"node2","name":"","added":"0001-01-01T00:00:00Z"}]`
	want := "ID     NAME   ADDED\n" +
		"node1  phone  " + at(alice) + "\n" +
		"node2  -      -\n"
	wantTable(t, showTable(t, showNodes, raw), want)
}

func TestShowTokens(t *testing.T) {
	created := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	lastUsed := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	raw := `[{"name":"porter","app":"porter","scope":"mcp","agent":"frostporter","created":"2026-11-04T09:30:00Z","lastUsed":"2026-11-05T10:00:00Z"},
	         {"name":"ci","app":"notes","scope":"read","created":"0001-01-01T00:00:00Z"}]`
	want := "NAME    APP     SCOPE  AGENT        CREATED           LAST USE          REVOKED\n" +
		"porter  porter  mcp    frostporter  " + at(created) + "  " + at(lastUsed) + "  -\n" +
		"ci      notes   read   -            -                 -                 -\n"
	wantTable(t, showTable(t, showTokens, raw), want)
}

func TestShowSessions(t *testing.T) {
	started := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	seen := time.Date(2026, 11, 4, 11, 15, 5, 0, time.UTC)
	raw := `[{"id":"s1","app":"notes","login":"alice@example.com","nodeName":"phone","started":"2026-11-04T09:30:00Z","seenAt":"2026-11-04T11:15:05Z"},
	         {"id":"s2","app":"read","login":"","nodeName":"","started":"0001-01-01T00:00:00Z","seenAt":"2026-11-04T11:15:05Z"}]`
	want := "ID  APP    LOGIN              NODE   STARTED           SEEN\n" +
		"s1  notes  alice@example.com  phone  " + at(started) + "  " + clock(seen) + "\n" +
		"s2  read                             -                 " + clock(seen) + "\n"
	wantTable(t, showTable(t, showSessions, raw), want)
}

func TestShowModels(t *testing.T) {
	raw := `[{"id":"gpt-x","name":"GPT X"},{"id":"m2"}]`
	want := "MODEL  NAME\n" +
		"gpt-x  GPT X\n" +
		"m2     \n"
	wantTable(t, showTable(t, showModels, raw), want)
}

func TestShowModelsEmpty(t *testing.T) {
	wantTable(t, showTable(t, showModels, `[]`), "MODEL  NAME\n")
}

// A list command passes its decoder's error on: the caller prints it.
func TestShowTablesRejectBadJSON(t *testing.T) {
	for name, f := range map[string]func(*out, []byte) error{
		"usage":    showUsage,
		"apps":     showApps,
		"logins":   showLogins,
		"nodes":    showNodes,
		"tokens":   showTokens,
		"sessions": showSessions,
		"models":   showModels,
	} {
		var stdout, stderr bytes.Buffer
		if err := f(&out{stdout: &stdout, stderr: &stderr}, []byte(`{`)); err == nil {
			t.Errorf("show%s({) returned nil error, want a decode error", name)
		}
		if stdout.Len() != 0 {
			t.Errorf("show%s({) wrote %q, want nothing", name, stdout.String())
		}
	}
}
