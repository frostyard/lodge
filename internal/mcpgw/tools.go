package mcpgw

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/frostyard/lodge/internal/apps"
)

// requestAccessName is lodge's own tool, which every principal has.
const requestAccessName = "request_access"

// requestAccessSchema is request_access's input schema: an app, and 1 to 32
// items of its grant (docs/specs/lodge-http.md, The MCP gateway).
const requestAccessSchema = `{"type":"object","properties":{"app":{"type":"string"},` +
	`"items":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object",` +
	`"properties":{"cap":{"type":"string"},"sel":{"type":"object","additionalProperties":{"type":"string"}}},` +
	`"required":["cap"],"additionalProperties":false}}},"required":["app","items"],"additionalProperties":false}`

// definition is 1 tool of a principal: what the session lists, and what a call
// sends.
type definition struct {
	tool *mcp.Tool
	// hash changes when a byte of the tool's definition changes, or when the
	// app or the operation behind it does, so that a reload's new operation
	// reaches a handler that captured the old one.
	hash string
	// requestAccess marks lodge's own tool.
	requestAccess bool
	// app and op are the app, and its operation, that a call sends.
	app string
	op  apps.Operation
}

// sync computes the principal's tools again and registers what changed. A tool
// whose definition is the same is left alone, so the session gets no
// notification for it (docs/specs/lodge-http.md, The MCP gateway).
func (s *server) sync(ctx context.Context) error {
	// 1 sync at a time: a snapshot that was computed before another's would
	// otherwise be applied after it, and put the older tools back.
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	want, err := s.gw.tools(ctx, s.principal)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var gone []string
	for name := range s.tools {
		if _, ok := want[name]; !ok {
			gone = append(gone, name)
		}
	}
	slices.Sort(gone)
	if len(gone) > 0 {
		s.srv.RemoveTools(gone...)
		for _, name := range gone {
			delete(s.tools, name)
		}
	}
	added := 0
	for name, d := range want {
		if s.tools[name] == d.hash {
			continue
		}
		s.srv.AddTool(d.tool, s.gw.handler(d))
		s.tools[name] = d.hash
		added++
	}
	// Every session of the server gets notifications/tools/list_changed for
	// what changed above; the log line says how many.
	if added+len(gone) > 0 {
		sessions := 0
		for range s.srv.Sessions() {
			sessions++
		}
		s.gw.log.Info("mcp tools changed", "principal", s.principal, "added", added, "removed", len(gone),
			"sessions", sessions)
	}
	return nil
}

// tools are principal's tools: lodge's own tool, then for each enabled app on
// hearth, by ID, each agent operation whose permission 1 item of the
// principal's grant for that app holds.
func (g *Gateway) tools(ctx context.Context, principal string) (map[string]definition, error) {
	hearth := g.hearthApps()
	out := make(map[string]definition, len(hearth))
	own := definition{tool: requestAccessTool(hearth), requestAccess: true}
	own.hash = definitionHash(own)
	out[requestAccessName] = own
	for _, a := range hearth {
		items, err := g.opts.OAuth.Grant(ctx, principal, a.ID)
		if err != nil {
			return nil, fmt.Errorf("read %s's grant for %s: %w", principal, a.ID, err)
		}
		caps := make([]string, 0, len(items))
		for _, it := range items {
			caps = append(caps, it.Cap)
		}
		for _, op := range a.Hearth.Operations {
			if !op.Agent() || !slices.Contains(caps, op.Permission) {
				continue
			}
			t, ok := catalogTool(a.Hearth.Tools, op.Tool)
			if !ok {
				continue
			}
			tool := &mcp.Tool{Name: a.ID + "_" + op.Tool, Description: t.Description,
				InputSchema: jsontext.Value(t.InputSchema), Annotations: annotations(op.Effect)}
			if len(t.OutputSchema) > 0 {
				tool.OutputSchema = jsontext.Value(t.OutputSchema)
			}
			d := definition{tool: tool, app: a.ID, op: op}
			d.hash = definitionHash(d)
			out[tool.Name] = d
		}
	}
	return out, nil
}

// hearthApps are the enabled apps on hearth, by ID.
func (g *Gateway) hearthApps() []apps.App {
	var out []apps.App
	for _, a := range g.opts.Apps.List() {
		if a.Enabled() && a.Hearth != nil {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b apps.App) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// catalogTool is the catalog's tool named name.
func catalogTool(tools []apps.Tool, name string) (apps.Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return apps.Tool{}, false
}

// definitionHash is what a re-registration depends on, as canonical JSON: the
// tool as the session lists it, and the app and the operation that its handler
// sends a call to. A reload that renames a tool's RPC, or gives it another
// permission, changes it even when the tool's own bytes do not.
func definitionHash(d definition) string {
	b, err := canonicalJSON(struct {
		Tool       *mcp.Tool `json:"tool"`
		App        string    `json:"app"`
		Name       string    `json:"name"`
		Permission string    `json:"permission"`
		Effect     string    `json:"effect"`
	}{d.tool, d.app, d.op.Name, d.op.Permission, d.op.Effect})
	if err != nil {
		return ""
	}
	return string(b)
}

// annotations are the hints of 1 operation's effect, all 3 written: the SDK's
// schema reads an absent destructive or open-world hint as true
// (docs/specs/lodge-http.md, The MCP gateway).
func annotations(effect string) *mcp.ToolAnnotations {
	no, yes := false, true
	switch effect {
	case "read":
		return &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &no, OpenWorldHint: &no}
	case "write":
		return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &no, OpenWorldHint: &no}
	}
	// outward, and any effect this lodge does not know, is the widest hint.
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &yes, OpenWorldHint: &yes}
}

// requestAccessTool is lodge's own tool, whose description names each enabled
// app on hearth.
func requestAccessTool(hearth []apps.App) *mcp.Tool {
	var b strings.Builder
	b.WriteString("Ask your owner for more access to an app: lodge makes an access request for your client, " +
		"and your owner approves, narrows or declines it. Nothing changes until your owner approves it. " +
		"The answer holds the link to open.\n\nEach app on hearth, with its capabilities and its selectors:\n")
	for _, a := range hearth {
		fmt.Fprintf(&b, "- %s (%s): capabilities %s; selectors %s\n", a.ID, a.Manifest.Name,
			cmp.Or(strings.Join(a.Hearth.Capabilities, ", "), "none"),
			cmp.Or(strings.Join(a.Hearth.Selectors, ", "), "none"))
	}
	return &mcp.Tool{Name: requestAccessName, Description: b.String(), InputSchema: jsontext.Value(requestAccessSchema)}
}

// handler is the tool handler of d.
func (g *Gateway) handler(d definition) mcp.ToolHandler {
	if d.requestAccess {
		return g.requestAccess
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return g.call(ctx, d, req)
	}
}

// unlistedCall is the server's receiving middleware: a tools/call of a name
// that is <id>_<tool> of an agent operation of an installed app on hearth, and
// that this server does not hold, runs lodge's call. The session's list says
// what the principal may call now; the call reads the grant again, so a
// narrowed grant is refused with insufficient_scope and an app disabled since
// the list was read says why. Any other name goes to the SDK, whose answer for
// a tool it does not hold is its unknown-tool error.
func (s *server) unlistedCall(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "tools/call" {
			call, ok := req.(*mcp.CallToolRequest)
			if ok && call.Params != nil {
				if d, ok := s.gw.unlisted(call.Params.Name, s.registered); ok {
					return s.gw.call(ctx, d, call)
				}
			}
		}
		return next(ctx, method, req)
	}
}

// registered reports whether the server holds tool name.
func (s *server) registered(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tools[name]
	return ok
}

// unlisted is the definition of a tool name that is not registered on this
// server, when name is <id>_<tool> of an agent operation of an installed app on
// hearth, enabled or not. Any other name is not lodge's to answer.
func (g *Gateway) unlisted(name string, registered func(string) bool) (definition, bool) {
	if name == requestAccessName || registered(name) {
		return definition{}, false
	}
	for _, a := range g.opts.Apps.List() {
		tool, ok := strings.CutPrefix(name, a.ID+"_")
		if !ok {
			continue
		}
		if op, ok := operationOf(a, tool); ok {
			return definition{tool: &mcp.Tool{Name: name}, app: a.ID, op: op}, true
		}
	}
	return definition{}, false
}

// operationOf is the app's agent operation whose tool is name.
func operationOf(a apps.App, name string) (apps.Operation, bool) {
	if a.Hearth == nil || name == "" {
		return apps.Operation{}, false
	}
	for _, op := range a.Hearth.Operations {
		if op.Agent() && op.Tool == name {
			return op, true
		}
	}
	return apps.Operation{}, false
}
