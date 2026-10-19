package lodgeapp

import (
	"net/http"
	"strings"
	"testing"
)

func TestHeaderEncodingRoundTrips(t *testing.T) {
	for _, s := range []string{"", "alice@example.com", "alice@github", "Zoë O'Brien", "a b+c~d", "日本", "100%"} {
		enc := EncodeHeader(s)
		for i := range len(enc) {
			if enc[i] != '%' && !unreserved(enc[i]) {
				t.Errorf("EncodeHeader(%q) = %q leaves %q", s, enc, enc[i])
			}
		}
		dec, err := DecodeHeader(enc)
		if err != nil || dec != s {
			t.Errorf("DecodeHeader(EncodeHeader(%q)) = %q, %v", s, dec, err)
		}
	}
	if got := EncodeHeader("Zoë B"); got != "Zo%C3%AB%20B" {
		t.Errorf("EncodeHeader = %q", got)
	}
}

func TestDecodeHeaderRefusesMalformed(t *testing.T) {
	for _, s := range []string{"a b", "%", "%4", "%zz", "%FF", "a\x00", "%C3"} {
		if d, err := DecodeHeader(s); err == nil {
			t.Errorf("DecodeHeader(%q) = %q, want an error", s, d)
		}
	}
	if d, err := DecodeHeader("%c3%ab"); err != nil || d != "ë" {
		t.Errorf("lowercase hex: %q, %v", d, err)
	}
}

func header(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestParseCaller(t *testing.T) {
	id := []string{HeaderLogin, "alice%40example.com", HeaderNodeID, "nABC123CNTRL", HeaderNodeName, "phone"}
	for _, tc := range []struct {
		name string
		h    http.Header
		want Caller
		err  string
	}{
		{"session", header(append([]string{HeaderCaller, "session", HeaderSession, "0123456789abcdef",
			HeaderDisplayName, "Alice%20A"}, id...)...),
			Caller{Kind: Session, Login: "alice@example.com", DisplayName: "Alice A", NodeID: "nABC123CNTRL",
				NodeName: "phone", Session: "0123456789abcdef"}, ""},
		{"x read", header(append([]string{HeaderCaller, "session"}, id...)...),
			Caller{Kind: Session, Login: "alice@example.com", NodeID: "nABC123CNTRL", NodeName: "phone"}, ""},
		{"content ignores a session", header(append([]string{HeaderCaller, "content", HeaderSession, "0123456789abcdef"}, id...)...),
			Caller{Kind: Content, Login: "alice@example.com", NodeID: "nABC123CNTRL", NodeName: "phone"}, ""},
		{"agent", header(HeaderCaller, "agent", HeaderToken, "laptop.claude", HeaderScope, "vault=1;write", HeaderLogin, "x"),
			Caller{Kind: Agent, Token: "laptop.claude", Scope: "vault=1;write"}, ""},
		{"bare device", header(HeaderCaller, "device"), Caller{Kind: Device}, ""},
		{"named device", header(append([]string{HeaderCaller, "device"}, id...)...),
			Caller{Kind: Device, Login: "alice@example.com", NodeID: "nABC123CNTRL", NodeName: "phone"}, ""},
		{"none", header(), Caller{}, "no Lodge-Caller"},
		{"unknown", header(HeaderCaller, "local"), Caller{}, "unknown"},
		{"case matters", header(HeaderCaller, "Session"), Caller{}, "unknown"},
		{"repeated", header(HeaderCaller, "device", HeaderCaller, "device"), Caller{}, "repeated"},
		{"session without login", header(HeaderCaller, "session", HeaderNodeID, "n", HeaderNodeName, "phone"), Caller{}, "no Lodge-Login"},
		{"empty login", header(append([]string{HeaderCaller, "session"}, append([]string{HeaderLogin, ""}, id[2:]...)...)...), Caller{}, "no Lodge-Login"},
		{"session without node", header(HeaderCaller, "session", HeaderLogin, "a"), Caller{}, "no Lodge-Node-ID"},
		{"bad node name", header(HeaderCaller, "session", HeaderLogin, "a", HeaderNodeID, "n", HeaderNodeName, "Phone"), Caller{}, "malformed Lodge-Node-Name"},
		{"bad session", header(append([]string{HeaderCaller, "session", HeaderSession, "0123456789ABCDEF"}, id...)...), Caller{}, "malformed Lodge-Session"},
		{"unencoded login", header(HeaderCaller, "session", HeaderLogin, "a b", HeaderNodeID, "n", HeaderNodeName, "p"), Caller{}, "malformed Lodge-Login"},
		{"agent without scope", header(HeaderCaller, "agent", HeaderToken, "t"), Caller{}, "no Lodge-Scope"},
		{"bad token", header(HeaderCaller, "agent", HeaderToken, "a/b", HeaderScope, "s"), Caller{}, "malformed Lodge-Token"},
		{"long token", header(HeaderCaller, "agent", HeaderToken, strings.Repeat("t", 65), HeaderScope, "s"), Caller{}, "malformed Lodge-Token"},
		{"scope with a space", header(HeaderCaller, "agent", HeaderToken, "t", HeaderScope, "a b"), Caller{}, "malformed Lodge-Scope"},
		{"long scope", header(HeaderCaller, "agent", HeaderToken, "t", HeaderScope, strings.Repeat("s", 257)), Caller{}, "malformed Lodge-Scope"},
		{"device with a bad name", header(HeaderCaller, "device", HeaderNodeName, "a.b"), Caller{}, "malformed Lodge-Node-Name"},
	} {
		got, err := ParseCaller(tc.h)
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.err)
		case got != tc.want:
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestSetHeadersReplacesForgeries(t *testing.T) {
	h := header("Lodge-Caller", "agent", "lodge-token", "forged", "Lodge-Whatever", "x", "Origin", "kept")
	want := Caller{Kind: Session, Login: "zoë@example.com", DisplayName: "Zoë", NodeID: "n1", NodeName: "laptop",
		Session: "00000000000000ff"}
	want.SetHeaders(h)
	if h.Get("Origin") != "kept" || h.Get("Lodge-Whatever") != "" || h.Get(HeaderToken) != "" {
		t.Errorf("SetHeaders left %v", h)
	}
	if got, err := ParseCaller(h); err != nil || got != want {
		t.Errorf("ParseCaller(SetHeaders) = %+v, %v", got, err)
	}
	Caller{}.SetHeaders(h)
	if len(h) != 1 {
		t.Errorf("the zero Caller left %v", h)
	}
}

func TestCallerName(t *testing.T) {
	for c, want := range map[Caller]string{
		{Kind: Session, NodeName: "phone"}:           "phone",
		{Kind: Agent, Token: "stomp", NodeName: "x"}: "stomp",
		{Kind: Device}:                     "device",
		{Kind: Device, NodeName: "laptop"}: "laptop",
	} {
		if got := c.Name(); got != want {
			t.Errorf("%+v.Name() = %q, want %q", c, got, want)
		}
	}
}
