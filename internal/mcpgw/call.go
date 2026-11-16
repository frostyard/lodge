package mcpgw

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/oauth"
)

// maxAnswer bounds 1 app's answer to a tool call: 4 MiB.
const maxAnswer = 4 << 20

// Tool errors' first words, which say what a caller can do about them.
const (
	// insufficientScope starts a call outside the principal's grant: the OAuth
	// scope is right, and a new sign-in cannot widen a grant (ADR-0024).
	insufficientScope = "insufficient_scope: "
)

// toolError is a tool error's result: the call failed, and the text says why.
func toolError(line string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: line}}}
}

// toolResult is an app's answer: its JSON as the result's structured content,
// and the same JSON as 1 text.
func toolResult(object map[string]any, text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{StructuredContent: object, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// requestAccess is lodge's own tool: it makes an access request for the
// session's client, and answers with the link its owner opens.
func (g *Gateway) requestAccess(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	caller, ok := CallerFrom(ctx)
	if !ok {
		return toolError("lodge cannot name the caller of this session"), nil
	}
	client := caller.Client
	if req.Extra != nil && req.Extra.TokenInfo != nil && req.Extra.TokenInfo.UserID != "" {
		client = req.Extra.TokenInfo.UserID
	}
	var in struct {
		App   string `json:"app"`
		Items []struct {
			Cap string            `json:"cap"`
			Sel map[string]string `json:"sel"`
		} `json:"items"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return toolError("the arguments are not an access request: " + oneLine(err.Error())), nil
		}
	}
	items := make([]oauth.Item, 0, len(in.Items))
	for _, it := range in.Items {
		items = append(items, oauth.Item{Cap: it.Cap, Sel: it.Sel})
	}
	r, err := g.opts.OAuth.RequestAccess(ctx, client, in.App, items)
	if err != nil {
		g.log.Info("mcp tool call", "principal", caller.Principal, "client", client, "tool", requestAccessName,
			"outcome", "refused")
		return toolError(oneLine(err.Error())), nil
	}
	expires := r.Expires.UTC().Format(time.RFC3339)
	g.log.Info("mcp tool call", "principal", caller.Principal, "client", client, "tool", requestAccessName,
		"outcome", "request "+r.ID)
	return toolResult(
		map[string]any{"request": r.ID, "url": r.URL, "expires": expires},
		fmt.Sprintf("Ask your owner to open %s to approve, narrow or decline this request; it expires at %s.", r.URL, expires),
	), nil
}

// call is 1 tool call of a principal: the app's operation, through 1 Connect
// call on the app's socket (docs/specs/lodge-http.md, The MCP gateway).
func (g *Gateway) call(ctx context.Context, d definition, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	caller, _ := CallerFrom(ctx)
	res, outcome := g.callOnce(ctx, caller, d, req)
	g.log.Info("mcp tool call", "principal", caller.Principal, "client", caller.Client, "tool", d.tool.Name, "outcome", outcome)
	return res, nil
}

// callOnce is g.call's work, with the outcome that its log line names.
func (g *Gateway) callOnce(ctx context.Context, caller Caller, d definition, req *mcp.CallToolRequest) (*mcp.CallToolResult, string) {
	// 1. The app is installed and enabled, with the operation.
	a, ok := g.opts.Apps.Get(d.app)
	switch {
	case !ok:
		return toolError("app " + d.app + " is not installed"), "the app is gone"
	case !a.Enabled():
		return toolError("app " + d.app + " is disabled: " + a.Reason), "the app is disabled"
	case a.Hearth == nil || !slices.ContainsFunc(a.Hearth.Operations, func(op apps.Operation) bool {
		return op.Name == d.op.Name
	}):
		return toolError("app " + d.app + " has no operation " + d.op.Name), "the operation is gone"
	}
	// 2. The grant is read again: a narrowed grant refuses the next call.
	items, err := g.opts.OAuth.Grant(ctx, caller.Principal, d.app)
	if err != nil {
		g.log.Warn("read a grant", "principal", caller.Principal, "app", d.app, "err", err)
		return toolError("lodge cannot read " + caller.Principal + "'s grant for " + d.app), "the grant cannot be read"
	}
	if !slices.ContainsFunc(items, func(it oauth.Item) bool { return it.Cap == d.op.Permission }) {
		return toolError(fmt.Sprintf("%sprincipal %s holds no %s for app %s: call %s to ask for it",
			insufficientScope, caller.Principal, d.op.Permission, d.app, requestAccessName)), "no grant"
	}
	grant, err := GrantHeader(items)
	if err != nil {
		return toolError("lodge cannot read " + caller.Principal + "'s grant for " + d.app), "the grant cannot be read"
	}
	// 3. The call's arguments, which are 1 JSON object.
	body := jsontext.Value("{}")
	if req.Params != nil && len(req.Params.Arguments) > 0 {
		args := jsontext.Value(req.Params.Arguments)
		switch args.Kind() {
		case 'n':
		case '{':
			body = args.Clone()
		default:
			return toolError("the call's arguments are not a JSON object"), "bad arguments"
		}
	}
	return g.send(ctx, d, caller, grant, body)
}

// send makes the Connect call, and reads the app's answer.
func (g *Gateway) send(ctx context.Context, d definition, caller Caller, grant string, body jsontext.Value) (*mcp.CallToolResult, string) {
	ctx, cancel := context.WithTimeout(ctx, g.opts.CallTimeout)
	defer cancel()
	url := "http://" + d.app + "/connect/" + d.op.Service() + "/" + d.op.Method()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return toolError("lodge cannot make the call to app " + d.app), "the call cannot be made"
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Connect-Protocol-Version", "1")
	hreq.Header.Set("Connect-Timeout-Ms", strconv.FormatInt(g.opts.CallTimeout.Milliseconds(), 10))
	hreq.Header.Set("Lodge-Caller", "principal")
	hreq.Header.Set("Lodge-Principal", caller.Principal)
	hreq.Header.Set("Lodge-Grant", grant)
	resp, err := g.client.Do(hreq)
	if err != nil {
		return toolError("app " + d.app + " cannot be reached"), "the app cannot be reached"
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return toolError("app " + d.app + " cannot be reached"), "the app's answer cannot be read"
	}
	if len(b) > maxAnswer {
		return toolError("app " + d.app + " answered more than 4 MiB"), "the answer is too large"
	}
	if resp.StatusCode != http.StatusOK {
		return connectFailure(d.app, resp.StatusCode, b)
	}
	var object map[string]any
	if err := json.Unmarshal(b, &object); err != nil || object == nil {
		return toolError("app " + d.app + " answered without a JSON object"), "the answer is not an object"
	}
	text := jsontext.Value(b)
	if err := text.Compact(); err != nil {
		return toolError("app " + d.app + " answered without a JSON object"), "the answer is not JSON"
	}
	return toolResult(object, string(text)), "ok"
}

// connectFailure is an answer that is not a result: a Connect error, or any
// other status.
func connectFailure(app string, status int, body []byte) (*mcp.CallToolResult, string) {
	var ce struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &ce); err == nil && ce.Code != "" {
		if ce.Code == "permission_denied" {
			return toolError(ce.Message), "the app refused the grant"
		}
		return toolError(ce.Code + ": " + ce.Message), "the app failed"
	}
	return toolError("app " + app + " answered HTTP " + strconv.Itoa(status)), "HTTP " + strconv.Itoa(status)
}

// oneLine replaces control characters, so a line stays one line.
func oneLine(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		out = append(out, r)
	}
	return string(out)
}
