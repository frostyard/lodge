package control

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
)

// Client calls the control socket, as lodgectl does.
type Client struct {
	mu   sync.Mutex
	conn net.Conn
	rd   *bufio.Reader
	next int
}

// Dial connects to the control socket at path.
func Dial(ctx context.Context, path string) (*Client, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	return &Client{conn: c, rd: bufio.NewReaderSize(c, 64<<10)}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call calls method with params and decodes the result into result,
// which may be nil. A JSON-RPC error is returned as *Error.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	}
	c.next++
	if params == nil {
		params = struct{}{}
	}
	p, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := json.Marshal(request{JSONRPC: "2.0", ID: jsontext.Value(strconv.Itoa(c.next)), Method: method, Params: p})
	if err != nil {
		return err
	}
	if _, err := c.conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	line, err := c.rd.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	var resp struct {
		Result jsontext.Value `json:"result"`
		Error  *Error         `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result == nil {
		return nil
	}
	if len(resp.Result) == 0 {
		return errors.New("control socket: an answer without a result")
	}
	return json.Unmarshal(resp.Result, result)
}
