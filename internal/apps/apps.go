// Package apps reads the installed apps' manifests and polls their health
// (docs/specs/app-contract.md, Install layout and The manifest). Each app is
// up, degraded, down or disabled, with a reason, for the rail
// (/lodge/apps) and lodgectl. A disabled app's routes answer 503; the other
// apps are unaffected.
package apps

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/frostyard/lodge/kit/manifest"
)

// Contracts are the contract majors this lodge speaks.
var Contracts = []int{manifest.Contract}

// Manifest is an app's app.json, the kit's type.
type Manifest = manifest.Manifest

// ValidID reports whether id may name an app.
func ValidID(id string) bool { return manifest.ValidID(id) }

// Hearth is an app's hearth manifest: what an agent may be granted for it,
// for the consent page (docs/specs/oauth.md, The app's manifest).
type Hearth struct {
	// Capabilities are the app's capabilities, each <app>:<name>.
	Capabilities []string `json:"capabilities"`
	// Selectors are the app's selector names, such as folder.
	Selectors []string `json:"selectors"`
	// Operations are the app's RPCs (ADR-0024).
	Operations []Operation `json:"operations"`
	// Tools are the app's agent tools, from its catalog (ADR-0024).
	Tools []Tool `json:"tools"`
}

// Operation is 1 RPC of an app's hearth manifest: its full name, the
// capability it needs, what it does, where it is exposed, and, for an agent
// operation, its MCP tool's name (ADR-0024).
type Operation struct {
	// Name is the RPC's full name, <package>.<service>.<method>.
	Name string `json:"name"`
	// Permission is the capability the call needs, one of the app's.
	Permission string `json:"permission"`
	// Effect is what the call does: read, write or outward.
	Effect string `json:"effect"`
	// Expose is where the RPC is reachable: ui, agent, or both.
	Expose []string `json:"expose"`
	// Tool is the RPC's MCP tool name, set exactly when Expose holds agent.
	Tool string `json:"tool,omitzero"`
}

// Agent reports whether an agent may reach the operation.
func (o Operation) Agent() bool { return slices.Contains(o.Expose, "agent") }

// UI reports whether the app's web interface may reach the operation.
func (o Operation) UI() bool { return slices.Contains(o.Expose, "ui") }

// Service is the operation's service: its name up to the last dot.
func (o Operation) Service() string {
	i := strings.LastIndexByte(o.Name, '.')
	if i < 0 {
		return ""
	}
	return o.Name[:i]
}

// Method is the operation's method: its name after the last dot.
func (o Operation) Method() string {
	i := strings.LastIndexByte(o.Name, '.')
	if i < 0 {
		return o.Name
	}
	return o.Name[i+1:]
}

// Tool is 1 tool of an app's catalog, in the shape of an MCP tools/list
// entry: its name, its description, and the JSON Schemas of its request and
// its response (ADR-0024).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// InputSchema and OutputSchema are raw JSON: lodge checks their shape
	// and moves them, and never reads a schema.
	InputSchema  jsontext.Value `json:"inputSchema"`
	OutputSchema jsontext.Value `json:"outputSchema,omitzero"`
}

// GatewayTools are the names of the tools lodge's MCP gateway adds to every
// app's tools, so that no app's tool may make one of them (ADR-0024).
var GatewayTools = []string{"request_access"}

// HearthFormat is the hearth manifest format major this lodge reads.
const HearthFormat = 1

// MaxHearth bounds a hearth manifest.
const MaxHearth = 1 << 20

// hearthName is the form of a capability's name and of a selector.
var hearthName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// operationName is the form of an operation's full name.
var operationName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)

// toolName is the form of an operation's tool name and of a catalog tool's.
var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// What an operation's effect and expose may hold.
var (
	operationEffects   = []string{"read", "write", "outward"}
	operationExposures = []string{"ui", "agent"}
)

// The bounds of a tool catalog (ADR-0024).
const (
	// maxTools is how many tools a catalog may hold.
	maxTools = 128
	// maxToolName bounds a tool's name in bytes: the gateway's name is
	// <app id>_<tool>, and an MCP tool name is at most 64 characters.
	maxToolName = 47
	// maxToolDescription bounds a tool's description in bytes.
	maxToolDescription = 4096
)

// PlaceholderIcon marks the new-lodge-app scaffold's icon
// (.agents/skills/new-lodge-app/template/web/public/icon.svg.tmpl). An app
// that still ships it stays enabled, with a note and a warning in the log.
const PlaceholderIcon = "data-lodge-placeholder"

// servable is a path lodge's file rules serve: every segment a plain name.
var servable = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)

// State is an app's state on the rail.
type State string

// The states (docs/specs/lodge-http.md, Shell routes).
const (
	Up       State = "up"
	Degraded State = "degraded"
	Down     State = "down"
	Disabled State = "disabled"
)

// Health polling (docs/specs/app-contract.md, What an app serves).
const (
	PollEvery   = 15 * time.Second
	PollTimeout = 2 * time.Second
)

// MaxBadge caps the unread count lodge keeps from an app's /health answer
// (ADR-0020).
const MaxBadge = 99999

// App is one installed app as lodge sees it.
type App struct {
	ID       string   `json:"id"`
	Manifest Manifest `json:"manifest"`
	// Key is the switcher key in effect: the manifest's, unless another
	// manifest claims it too.
	Key string `json:"key"`
	// Companion is whether the app is the companion: its manifest says so,
	// and no other enabled app's does (ADR-0013).
	Companion bool   `json:"companion"`
	State     State  `json:"state"`
	Reason    string `json:"reason"`
	// Notes say why an enabled app lost its key or the companion.
	Notes []string `json:"notes,omitzero"`
	// Version and Protocol are the last healthy /health answer's.
	Version  string `json:"version,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	// Badge is the count of things the user has not seen that the app's last
	// /health answer gave, capped at MaxBadge; 0 when it has none or is not
	// up (ADR-0020).
	Badge int `json:"badge"`
	// Hearth is the app's hearth manifest, when its manifest names one and
	// lodge reads it (docs/specs/oauth.md, The app's manifest).
	Hearth *Hearth `json:"hearth,omitzero"`
	// uid is the manifest user's, which must serve the app's socket; -1
	// when the user does not exist.
	uid int
}

// Enabled reports whether the app is served.
func (a App) Enabled() bool { return a.State != Disabled }

// Registry is the installed apps.
type Registry struct {
	root string
	log  *slog.Logger
	http *http.Client
	// devUID may serve any app's socket as well as its own user: lodge's
	// own uid under a layout root (development and tests), else -1.
	devUID int

	mu   sync.RWMutex
	apps map[string]App
}

// New returns a registry for the layout under root ("" for /). It reads
// nothing until Load.
func New(root string, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Registry{root: root, log: log, apps: map[string]App{}, devUID: -1}
	if root != "" {
		r.devUID = os.Getuid()
	}
	r.http = &http.Client{Transport: &http.Transport{DialContext: r.DialContext, DisableKeepAlives: true}, Timeout: PollTimeout}
	return r
}

// AppsDir is where manifests and bundles are installed.
func (r *Registry) AppsDir() string { return filepath.Join(r.root, "/usr/share/lodge/apps") }

// AppDir is app id's install directory.
func (r *Registry) AppDir(id string) string { return filepath.Join(r.AppsDir(), id) }

// SocketPath is app id's socket.
func (r *Registry) SocketPath(id string) string {
	return filepath.Join(r.root, "/run/lodge/apps", id+".sock")
}

// DialContext dials the socket of the app named by addr's host
// ("<id>:80"), for an http.Transport.
func (r *Registry) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if !ValidID(host) {
		return nil, fmt.Errorf("dial app %q: not an app ID", host)
	}
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", r.SocketPath(host))
	if err != nil {
		return nil, err
	}
	if err := r.checkPeer(host, c); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// PeerError is a socket served, or owned, by another user than the
// manifest's.
type PeerError struct {
	App, User string
	UID       int
	// File is whether the socket's file, not its peer, is the wrong one's.
	File bool
}

func (e *PeerError) Error() string {
	if e.File {
		return fmt.Sprintf("its socket file is owned by uid %d, not by user %s", e.UID, e.User)
	}
	return fmt.Sprintf("its socket is served by uid %d, not by user %s", e.UID, e.User)
}

// checkPeer requires app id's socket to be the manifest user's: its file
// owned by that user, and its peer (SO_PEERCRED) that user or root. A
// socket that systemd made and passed to the app reports root, which
// called listen(); another app's user can be neither. Under a layout root
// devUID passes both checks.
func (r *Registry) checkPeer(id string, c net.Conn) error {
	a, _ := r.Get(id)
	ok := func(uid int) bool { return (a.uid >= 0 && uid == a.uid) || (r.devUID >= 0 && uid == r.devUID) }
	fi, err := os.Stat(r.SocketPath(id))
	if err != nil {
		return fmt.Errorf("app %s: %w", id, err)
	}
	if st, isStat := fi.Sys().(*syscall.Stat_t); !isStat || !ok(int(st.Uid)) {
		uid := -1
		if isStat {
			uid = int(st.Uid)
		}
		return &PeerError{App: id, User: a.Manifest.User, UID: uid, File: true}
	}
	uc, isUnix := c.(*net.UnixConn)
	if !isUnix {
		return fmt.Errorf("app %s: not a Unix socket", id)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if credErr != nil {
		return fmt.Errorf("app %s: peer credentials: %w", id, credErr)
	}
	if uid := int(cred.Uid); uid != 0 && !ok(uid) {
		return &PeerError{App: id, User: a.Manifest.User, UID: uid}
	}
	return nil
}

// CheckSocketDir refuses the apps' socket directory unless only root can
// write it: an app that could replace another's socket would receive its
// sessions. Under a layout root it may be the developer's, and may be
// missing.
//
// Under a layout root the directory is the developer's sandbox, whose
// umask may well leave it group-writable: lodge only warns there.
func (r *Registry) CheckSocketDir() error {
	err := checkSocketDir(filepath.Join(r.root, "/run/lodge/apps"), r.root == "")
	if err != nil && r.root != "" {
		r.log.Warn("the apps' socket directory would be refused in production", "err", err)
		return nil
	}
	return err
}

// checkSocketDir checks dir; strict also requires it to exist and be
// root's.
func checkSocketDir(dir string, strict bool) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist) && !strict:
		return nil
	case err != nil:
		return fmt.Errorf("the apps' socket directory: %w (systemd-tmpfiles --create makes it)", err)
	case !fi.IsDir():
		return fmt.Errorf("the apps' socket directory %s is not a directory", dir)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("the apps' socket directory %s is writable by others than its owner (%v)", dir, fi.Mode().Perm())
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("the apps' socket directory %s: no owner", dir)
	}
	if strict && st.Uid != 0 {
		return fmt.Errorf("the apps' socket directory %s is owned by uid %d, not root", dir, st.Uid)
	}
	return nil
}

// Load reads every manifest, replacing the registry's apps. An app that
// stays keeps its last health until the next poll.
func (r *Registry) Load() error {
	entries, err := os.ReadDir(r.AppsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read the apps: %w", err)
	}
	loaded := map[string]App{}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !ValidID(id) {
			r.log.Warn("skip an entry in the apps directory", "name", id)
			continue
		}
		loaded[id] = r.loadOne(id)
	}
	disableShared(loaded)

	r.mu.Lock()
	defer r.mu.Unlock()
	for id, a := range loaded {
		if old, ok := r.apps[id]; ok && a.Enabled() && old.Enabled() {
			a.State, a.Reason, a.Version, a.Protocol, a.Badge =
				old.State, old.Reason, old.Version, old.Protocol, old.Badge
			loaded[id] = a
		}
		if !a.Enabled() {
			r.log.Warn("app disabled", "app", id, "reason", a.Reason)
		}
	}
	r.apps = loaded
	return nil
}

// loadOne loads app id with the kit's manifest.Load, then checks what
// lodge adds: a contract major it speaks, and an index and icon it will
// serve (no symbolic link on the way, no hidden segment).
func (r *Registry) loadOne(id string) App {
	a := App{ID: id, State: Disabled, uid: -1}
	dir := r.AppDir(id)
	m, err := manifest.Load(dir)
	if err != nil {
		a.Manifest = loose(dir)
		a.Reason = oneLine(err.Error())
		return a
	}
	a.Manifest = m
	a.uid = -1
	if u, err := user.Lookup(m.User); err == nil {
		if uid, err := strconv.Atoi(u.Uid); err == nil {
			a.uid = uid
		}
	}
	// Its hearth manifest, when it names one: the capabilities and the
	// selectors the consent page shows, and the operations and the tools an
	// agent reaches (ADR-0024).
	web := filepath.Join(dir, "web")
	var hearthErr error
	if m.Hearth != "" {
		if err := loadHearth(dir, m.Hearth, id, &a); err != nil {
			hearthErr = err
		} else if err := loadTools(dir, m.Tools, id, &a); err != nil {
			hearthErr = err
		}
	}
	switch {
	case a.uid < 0 && r.devUID < 0:
		a.Reason = fmt.Sprintf("system user %s does not exist", m.User)
	case !slices.Contains(Contracts, m.Contract):
		a.Reason = fmt.Sprintf("contract %d is not one this lodge speaks (%v)", m.Contract, Contracts)
	case !regularFile(web, "index.html"):
		a.Reason = "web/index.html is not a regular file"
	case !servable.MatchString(m.Icon) || !regularFile(web, m.Icon):
		a.Reason = fmt.Sprintf("its icon web/%s cannot be served", m.Icon)
	case hearthErr != nil:
		a.Reason = hearthErr.Error()
	default:
		a.Key = m.Key
		a.State, a.Reason = Down, "not polled yet"
		if placeholder(web, m.Icon) {
			a.Notes = append(a.Notes, fmt.Sprintf("its icon web/%s is the scaffold's placeholder", m.Icon))
			r.log.Warn("app ships the scaffold's placeholder icon", "app", id, "icon", "web/"+m.Icon)
		}
	}
	return a
}

// loadsHearth reads and checks app id's hearth manifest, name, from dir into
// a.Hearth: its format major, its app, its capabilities and its selectors
// (docs/specs/oauth.md, The app's manifest). A manifest it cannot read or
// use is an error, which becomes the app's reason.
func loadHearth(dir, name, id string, a *App) error {
	p := filepath.Join(dir, name)
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("its hearth manifest %s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("its hearth manifest %s is not a regular file", name)
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("its hearth manifest %s: %w", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxHearth+1))
	if err != nil {
		return fmt.Errorf("its hearth manifest %s: %w", name, err)
	}
	if len(b) > MaxHearth {
		return fmt.Errorf("its hearth manifest %s is larger than 1 MiB", name)
	}
	var m struct {
		Hearth       int         `json:"hearth"`
		App          string      `json:"app"`
		Capabilities []string    `json:"capabilities"`
		Selectors    []string    `json:"selectors"`
		Operations   []Operation `json:"operations"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("its hearth manifest %s: %w", name, err)
	}
	switch {
	case m.Hearth != HearthFormat:
		return fmt.Errorf("hearth manifest format %d is not one this lodge reads (%d)", m.Hearth, HearthFormat)
	case m.App != id:
		return fmt.Errorf("its hearth manifest %s names app %q, not %q", name, m.App, id)
	}
	for _, cap := range m.Capabilities {
		prefix, cn, ok := strings.Cut(cap, ":")
		if !ok || prefix != id || !hearthName.MatchString(cn) {
			return fmt.Errorf("its hearth manifest %s has capability %q, not %s:<name>", name, cap, id)
		}
	}
	for _, sel := range m.Selectors {
		if !hearthName.MatchString(sel) {
			return fmt.Errorf("its hearth manifest %s has selector %q, not a name", name, sel)
		}
	}
	if err := checkOperations(name, m.Operations, m.Capabilities); err != nil {
		return err
	}
	a.Hearth = &Hearth{Capabilities: m.Capabilities, Selectors: m.Selectors, Operations: m.Operations}
	return nil
}

// checkOperations checks a hearth manifest's operations: each name, the
// capability it needs, its effect, its expose, and its tool (ADR-0024). A
// break names the manifest and the operation.
func checkOperations(name string, ops []Operation, capabilities []string) error {
	names, tools := map[string]bool{}, map[string]bool{}
	for _, op := range ops {
		label := "an operation without a name"
		if op.Name != "" {
			label = "operation " + op.Name
		}
		bad := func(format string, args ...any) error {
			return fmt.Errorf("its hearth manifest %s: %s: %s", name, label, fmt.Sprintf(format, args...))
		}
		switch {
		case !operationName.MatchString(op.Name) || len(strings.Split(op.Name, ".")) < 3:
			return bad("its name must be <package>.<service>.<method>, of at least 3 parts")
		case names[op.Name]:
			return bad("its name is another operation's")
		}
		names[op.Name] = true
		if !slices.Contains(capabilities, op.Permission) {
			return bad("its permission %q is not one of the app's capabilities", op.Permission)
		}
		if !slices.Contains(operationEffects, op.Effect) {
			return bad("its effect %q is not read, write or outward", op.Effect)
		}
		switch {
		case len(op.Expose) == 0:
			return bad("its expose is empty: name ui, agent or both")
		case !distinct(op.Expose):
			return bad("its expose names %v with a repeat", op.Expose)
		}
		for _, where := range op.Expose {
			if !slices.Contains(operationExposures, where) {
				return bad("its expose has %q, not ui or agent", where)
			}
		}
		switch {
		case op.Agent() && op.Tool == "":
			return bad("an agent operation needs its tool's name")
		case !op.Agent() && op.Tool != "":
			return bad("its tool %q is set, and it is not exposed to agents", op.Tool)
		case op.Tool != "" && !toolName.MatchString(op.Tool):
			return bad("its tool %q must match ^[a-z][a-z0-9_]*$", op.Tool)
		case tools[op.Tool]:
			return bad("its tool %q is another operation's", op.Tool)
		}
		if op.Tool != "" {
			tools[op.Tool] = true
		}
	}
	return nil
}

// distinct reports whether no value of list repeats.
func distinct(list []string) bool {
	seen := make(map[string]bool, len(list))
	for _, s := range list {
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}

// loadTools reads and checks app id's tool catalog, name, from dir into
// a.Hearth.Tools: 1 MCP tool for each agent operation of the hearth manifest,
// in the shape of an MCP tools/list result (ADR-0024). A catalog it cannot
// read or use is an error, which becomes the app's reason.
func loadTools(dir, name, id string, a *App) error {
	agents := agentOperations(a.Hearth.Operations)
	if name == "" {
		if len(agents) > 0 {
			return errors.New("its hearth manifest has agent operations, and app.json names no tools")
		}
		return nil
	}
	p := filepath.Join(dir, name)
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("its tool catalog %s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("its tool catalog %s is not a regular file", name)
	}
	f, err := os.Open(p)
	if err != nil {
		return fmt.Errorf("its tool catalog %s: %w", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxHearth+1))
	if err != nil {
		return fmt.Errorf("its tool catalog %s: %w", name, err)
	}
	if len(b) > MaxHearth {
		return fmt.Errorf("its tool catalog %s is larger than 1 MiB", name)
	}
	var c struct {
		Tools []struct {
			Name         string         `json:"name"`
			Description  jsontext.Value `json:"description"`
			InputSchema  jsontext.Value `json:"inputSchema"`
			OutputSchema jsontext.Value `json:"outputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return fmt.Errorf("its tool catalog %s: %w", name, err)
	}
	if len(c.Tools) > maxTools {
		return fmt.Errorf("its tool catalog %s has %d tools, more than %d", name, len(c.Tools), maxTools)
	}
	catalog := make([]Tool, 0, len(c.Tools))
	seen, used := map[string]bool{}, map[string]bool{}
	for _, t := range c.Tools {
		label := "a tool without a name"
		if t.Name != "" {
			label = "tool " + t.Name
		}
		bad := func(format string, args ...any) error {
			return fmt.Errorf("its tool catalog %s: %s: %s", name, label, fmt.Sprintf(format, args...))
		}
		switch {
		case !toolName.MatchString(t.Name):
			return bad("its name must match ^[a-z][a-z0-9_]*$")
		case len(t.Name) > maxToolName:
			return bad("its name is longer than %d bytes", maxToolName)
		case seen[t.Name]:
			return bad("its name is another tool's")
		case !isToolOf(t.Name, agents):
			return bad("it is not the tool of an agent operation of its hearth manifest")
		}
		seen[t.Name], used[t.Name] = true, true
		description := ""
		if len(t.Description) > 0 {
			if t.Description.Kind() != '"' {
				return bad("its description is not a string")
			}
			if err := json.Unmarshal(t.Description, &description); err != nil {
				return bad("its description: %v", err)
			}
		}
		if len(description) > maxToolDescription {
			return bad("its description is longer than %d bytes", maxToolDescription)
		}
		if !objectSchema(t.InputSchema) {
			return bad("its inputSchema is not a JSON object whose type is object")
		}
		in, err := compactSchema(t.InputSchema)
		if err != nil {
			return fmt.Errorf("its tool catalog %s: %s: its inputSchema: %w", name, label, err)
		}
		tool := Tool{Name: t.Name, Description: description, InputSchema: in}
		// A null output schema is none, as an absent one is.
		if len(t.OutputSchema) > 0 && t.OutputSchema.Kind() != 'n' {
			if !objectSchema(t.OutputSchema) {
				return bad("its outputSchema is not a JSON object whose type is object")
			}
			if tool.OutputSchema, err = compactSchema(t.OutputSchema); err != nil {
				return fmt.Errorf("its tool catalog %s: %s: its outputSchema: %w", name, label, err)
			}
		}
		// The gateway names each tool <id>_<tool>, and its own tools are
		// lodge's.
		if g := id + "_" + t.Name; slices.Contains(GatewayTools, g) {
			return bad("the gateway's tool %s is lodge's own", g)
		}
		catalog = append(catalog, tool)
	}
	for _, op := range agents {
		if !used[op.Tool] {
			return fmt.Errorf("its tool catalog %s has no tool %q, which its operation %s needs", name, op.Tool, op.Name)
		}
	}
	a.Hearth.Tools = catalog
	return nil
}

// agentOperations are the operations an agent may reach.
func agentOperations(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.Agent() {
			out = append(out, op)
		}
	}
	return out
}

// isToolOf reports whether name is the tool of one of the operations.
func isToolOf(name string, ops []Operation) bool {
	return slices.ContainsFunc(ops, func(op Operation) bool { return op.Tool == name })
}

// objectSchema reports whether v is a JSON object whose type member is the
// string object.
func objectSchema(v jsontext.Value) bool {
	if len(v) == 0 || v.Kind() != '{' {
		return false
	}
	var s struct {
		Type jsontext.Value `json:"type"`
	}
	if err := json.Unmarshal(v, &s); err != nil {
		return false
	}
	var t string
	return len(s.Type) > 0 && json.Unmarshal(s.Type, &t) == nil && t == "object"
}

// compactSchema is v in its canonical compact form: no space outside a string.
func compactSchema(v jsontext.Value) (jsontext.Value, error) {
	out := v.Clone()
	if err := out.Compact(); err != nil {
		return nil, err
	}
	return out, nil
}

// placeholder reports whether the icon at rel under dir is the scaffold's.
// It reads at most 64 KiB; an icon it cannot read is not a placeholder.
func placeholder(dir, rel string) bool {
	f, err := os.Open(filepath.Join(dir, rel))
	if err != nil {
		return false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	return err == nil && bytes.Contains(b, []byte(PlaceholderIcon))
}

// loose reads what it can of a manifest that does not load, so the rail
// can still name the app.
func loose(dir string) Manifest {
	var m Manifest
	if b, err := os.ReadFile(filepath.Join(dir, "app.json")); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	if utf8Len := len([]rune(m.Name)); utf8Len < 1 || utf8Len > 32 {
		m.Name = ""
	}
	return Manifest{Name: m.Name}
}

// oneLine joins a multi-line error into one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// regularFile reports whether rel names a regular file under dir with no
// symbolic link on the way.
func regularFile(dir, rel string) bool {
	p := dir
	for seg := range strings.SplitSeq(rel, "/") {
		p = filepath.Join(p, seg)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// disableShared disables both apps of a shared user, takes a shared key
// from every app that claims it, and makes the companion the one enabled
// app whose manifest sets companion, or none when several do.
func disableShared(apps map[string]App) {
	users := map[string][]string{}
	keys := map[string][]string{}
	defer companion(apps)
	for id, a := range apps {
		if a.Manifest.User != "" && a.Enabled() {
			users[a.Manifest.User] = append(users[a.Manifest.User], id)
		}
		if a.Key != "" {
			keys[a.Key] = append(keys[a.Key], id)
		}
	}
	for user, ids := range users {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			a := apps[id]
			a.State, a.Reason, a.Key = Disabled, fmt.Sprintf("user %q is claimed by more than one app", user), ""
			apps[id] = a
		}
	}
	for key, ids := range keys {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			a := apps[id]
			if a.Key == key {
				a.Key = ""
				a.Notes = append(a.Notes, fmt.Sprintf("key %s is claimed by more than one app", key))
			}
			apps[id] = a
		}
	}
}

// companion sets Companion on the one enabled app whose manifest asks for
// it. When several ask, none is the companion, and each says why.
func companion(apps map[string]App) {
	var ids []string
	for id, a := range apps {
		if a.Enabled() && a.Manifest.Companion {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		a := apps[id]
		if len(ids) == 1 {
			a.Companion = true
		} else {
			a.Notes = append(a.Notes, "companion is claimed by more than one app: "+strings.Join(ids, ", "))
		}
		apps[id] = a
	}
}

// Get returns app id.
func (r *Registry) Get(id string) (App, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.apps[id]
	return a, ok
}

// AppForUID is the enabled app whose manifest's user has uid, for the
// inference socket's caller.
func (r *Registry) AppForUID(uid int) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for id, a := range r.apps {
		if a.Enabled() && a.uid >= 0 && a.uid == uid {
			return id, true
		}
	}
	return "", false
}

// AppEnabled reports whether id is an enabled app.
func (r *Registry) AppEnabled(id string) bool {
	a, ok := r.Get(id)
	return ok && a.Enabled()
}

// List returns every app, ordered by name.
func (r *Registry) List() []App {
	r.mu.RLock()
	out := make([]App, 0, len(r.apps))
	for _, a := range r.apps {
		out = append(out, a)
	}
	r.mu.RUnlock()
	slices.SortFunc(out, func(a, b App) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Manifest.Name), strings.ToLower(b.Manifest.Name)), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Poll asks every enabled app's /health once, concurrently.
func (r *Registry) Poll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, a := range r.List() {
		if !a.Enabled() {
			continue
		}
		wg.Go(func() {
			h := r.health(ctx, a.ID)
			r.mu.Lock()
			defer r.mu.Unlock()
			cur, ok := r.apps[a.ID]
			if !ok || !cur.Enabled() {
				return
			}
			if cur.State != h.State {
				r.log.Info("app state", "app", a.ID, "state", h.State, "reason", h.Reason)
			}
			cur.State, cur.Reason = h.State, h.Reason
			if h.State == Disabled {
				r.log.Warn("app disabled", "app", a.ID, "reason", h.Reason)
			}
			if h.State == Up {
				cur.Version, cur.Protocol, cur.Badge = h.Version, h.Protocol, h.Badge
			} else {
				cur.Badge = 0
			}
			r.apps[a.ID] = cur
		})
	}
	wg.Wait()
}

// Run polls every PollEvery until ctx ends.
func (r *Registry) Run(ctx context.Context) {
	t := time.NewTicker(PollEvery)
	defer t.Stop()
	for {
		r.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type healthAnswer struct {
	OK       bool   `json:"ok"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	Reason   string `json:"reason"`
	// Badge is the raw JSON, so a value lodge cannot use never fails the
	// decode; badge decides what it means (ADR-0020).
	Badge jsontext.Value `json:"badge"`
}

// badge is the unread count in an app's /health answer: a whole number from
// 0, capped at MaxBadge. Any other value, such as a string, a fraction or a
// negative number, is 0, and never changes the app's state (ADR-0020). It
// reads the JSON number's text, so no rounding turns a fraction into a
// count.
func badge(v jsontext.Value) int {
	if v.Kind() != '0' { // not a number
		return 0
	}
	s := string(v)
	if strings.HasPrefix(s, "-") { // negative, and -0 is 0 anyway
		return 0
	}
	mantissa, expText := s, ""
	if i := strings.IndexAny(mantissa, "eE"); i >= 0 {
		mantissa, expText = mantissa[:i], mantissa[i+1:]
	}
	intDigits, fracDigits, _ := strings.Cut(mantissa, ".")
	exp, over := 0, 0
	if expText != "" {
		var err error
		exp, err = strconv.Atoi(expText)
		if err != nil {
			if !errors.Is(err, strconv.ErrRange) {
				return 0
			}
			// An exponent too big for an int: a positive one is far above
			// MaxBadge, and a negative one is a fraction.
			exp, over = 0, 1
			if strings.HasPrefix(expText, "-") {
				over = -1
			}
		}
	}
	// The body is at most 64 KiB, so an exponent beyond a million is far
	// above MaxBadge, or a fraction, and the shift below cannot overflow.
	switch {
	case exp > 1_000_000:
		exp, over = 0, 1
	case exp < -1_000_000:
		exp, over = 0, -1
	}
	// A count of only zeros is 0, whatever its exponent.
	d := strings.TrimLeft(intDigits+fracDigits, "0")
	if d == "" {
		return 0
	}
	// The leading zeros are gone, so the trailing ones are the shifts the
	// integer digits carry, which the exponent and the fraction adjust.
	trailing := len(d) - len(strings.TrimRight(d, "0"))
	d = strings.TrimRight(d, "0")
	shift := exp - len(fracDigits) + trailing
	// An exponent out of range decides alone: a few digits of fraction
	// cannot undo a million places.
	switch {
	case over > 0: // far above MaxBadge
		return MaxBadge
	case over < 0 || shift < 0: // a fraction
		return 0
	case len(d)+shift > 5: // at least 100000
		return MaxBadge
	}
	n := 0
	for _, c := range d {
		n = n*10 + int(c-'0')
	}
	for range shift {
		n *= 10
	}
	return min(n, MaxBadge)
}

// health asks app id's /health, as lodge's poll: no Lodge-Caller.
func (r *Registry) health(ctx context.Context, id string) App {
	ctx, cancel := context.WithTimeout(ctx, PollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+id+"/health", nil)
	if err != nil {
		return App{State: Down, Reason: err.Error()}
	}
	resp, err := r.http.Do(req)
	var pe *PeerError
	if errors.As(err, &pe) {
		return App{State: Disabled, Reason: pe.Error()}
	}
	if err != nil {
		return App{State: Down, Reason: "its socket does not answer"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var h healthAnswer
	if err != nil || json.Unmarshal(body, &h) != nil {
		return App{State: Down, Reason: fmt.Sprintf("/health answered %d without its JSON", resp.StatusCode)}
	}
	switch {
	case h.OK && resp.StatusCode == http.StatusOK:
		return App{State: Up, Version: h.Version, Protocol: h.Protocol, Badge: badge(h.Badge)}
	case !h.OK:
		return App{State: Degraded, Reason: cmp.Or(truncate(h.Reason, 200), "the app says it is not ready")}
	default:
		return App{State: Down, Reason: fmt.Sprintf("/health answered %d", resp.StatusCode)}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
