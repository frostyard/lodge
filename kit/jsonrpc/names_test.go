package jsonrpc_test

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/frostyard/lodge/kit/jsonrpc"
)

// failingWrites is a connection whose writes fail, while reads wait.
type failingWrites struct{ net.Conn }

func (failingWrites) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The client's errors name the server when told its name, and the kit
// otherwise, as before the option.
func TestClientErrorsNameTheServer(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		closed, malformed, event, w string
	}{
		{"", "jsonrpc: connection closed", "jsonrpc: malformed message from the server: ",
			"jsonrpc: malformed event: ", "write to the server: broken pipe"},
		{"notesd", "notesd: connection closed", "notesd: malformed message: ",
			"notesd: malformed event: ", "write to notesd: broken pipe"},
	} {
		newClient := func(nc net.Conn) *jsonrpc.Client[code] {
			if tc.name == "" {
				return jsonrpc.NewClient[code](nc)
			}
			return jsonrpc.NewClientWith[code](nc, jsonrpc.ClientOptions{Name: tc.name})
		}
		for what, server := range map[string]func(net.Conn){
			tc.closed:    func(nc net.Conn) { _ = nc.Close() },
			tc.malformed: func(nc net.Conn) { _, _ = io.WriteString(nc, "not json\n") },
			tc.event:     func(nc net.Conn) { _, _ = io.WriteString(nc, `{"jsonrpc":"2.0","method":"event","params":"x"}`+"\n") },
		} {
			s, cl := net.Pipe()
			c := newClient(cl)
			go server(s)
			<-c.Done()
			if err := c.Err(); err == nil || !strings.HasPrefix(err.Error(), what) {
				t.Errorf("%q: Err = %v, want %q…", tc.name, err, what)
			}
			_ = s.Close()
		}
		s, cl := net.Pipe()
		c := newClient(failingWrites{cl})
		if err := c.Call(t.Context(), "rpc.ping", nil, nil); err == nil || err.Error() != tc.w {
			t.Errorf("%q: a failed write = %v, want %q", tc.name, err, tc.w)
		}
		_ = s.Close()
		<-c.Done()
	}
}
