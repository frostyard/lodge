package oauth

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ory/fosite"
)

//go:embed consent.html grants.html
var pageFiles embed.FS

// pages are the consent and grants pages: no script, and every field the
// spec lists (docs/specs/oauth.md, The consent page and The grants page).
var pages = template.Must(template.ParseFS(pageFiles, "consent.html", "grants.html"))

// PagePolicy is the consent and grants pages' Content-Security-Policy: no
// script, inline styles only, and the form goes to lodge, and, when it is
// not empty, to formOrigin, a sign-in's redirect origin.
func PagePolicy(formOrigin string) string {
	action := "'self'"
	if formOrigin != "" {
		action += " " + formOrigin
	}
	return "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; form-action " + action +
		"; frame-ancestors 'none'; base-uri 'none'"
}

// pageWhen writes a time for people.
func pageWhen(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format("2006-01-02 15:04 MST")
}

// ServeConsent answers a consent or grants route the gateway's checks let
// through, for the admin it named: the consent page and its decision, the
// grants page, and a revocation.
func (s *Server) ServeConsent(w http.ResponseWriter, r *http.Request, route, id, admin string) {
	head := r.Method == http.MethodGet || r.Method == http.MethodHead
	switch {
	case route == RouteAuthorize && head:
		s.authorize(w, r)
	case route == RouteConsent && head:
		s.consentPage(w, r, id)
	case route == RouteConsent:
		s.decideConsent(w, r, id, admin)
	case route == RouteGrants && head:
		s.grantsPage(w, r, admin)
	case route == RouteGrantRevoke:
		s.revokeTarget(w, r, admin)
	default:
		writeOneLine(w, http.StatusNotFound, "not found")
	}
}

// scopeChoice is a scope of a sign-in, with its checkbox.
type scopeChoice struct {
	Name string
	// About says what the scope lets a client use.
	About string
}

// selectorChoice is one selector of an item, with its path.
type selectorChoice struct {
	Name string
	Path string
}

// itemChoice is one item of an access request, with its checkbox.
type itemChoice struct {
	Index      int
	Cap        string
	Everything bool
	Selectors  []selectorChoice
}

// consentData is what the consent page shows.
type consentData struct {
	ID        string
	CSRF      string
	Hash      string
	Kind      Kind
	Client    string
	ClientID  string
	Origin    string
	Principal string
	// Bound is whether the client is bound to its principal already, which
	// makes the field read-only.
	Bound bool
	// IsAccess is whether the request asks for an app's items.
	IsAccess bool
	App      string
	AppID    string
	Scopes   []scopeChoice
	Items    []itemChoice
	Created  string
	Expires  string
}

// itemView is an item as the pages show it.
func itemViews(items []Item) []itemChoice {
	out := make([]itemChoice, 0, len(items))
	for i, it := range items {
		v := itemChoice{Index: i, Cap: it.Cap, Everything: len(it.Sel) == 0}
		names := make([]string, 0, len(it.Sel))
		for name := range it.Sel {
			names = append(names, name)
		}
		sortStrings(names)
		for _, name := range names {
			v.Selectors = append(v.Selectors, selectorChoice{Name: name, Path: it.Sel[name]})
		}
		out = append(out, v)
	}
	return out
}

// sortStrings sorts names, in place.
func sortStrings(names []string) { slices.Sort(names) }

// consentPage shows a pending request.
func (s *Server) consentPage(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	req, err := s.request(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeOneLine(w, http.StatusNotFound, "no request "+id)
		return
	case err != nil:
		s.log.Error("read a request", "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the request")
		return
	case !req.Decided.IsZero():
		writeOneLine(w, http.StatusConflict, "this request was decided: "+req.Decision)
		return
	case req.expires(s.now()):
		writeOneLine(w, http.StatusGone, "this request expired "+pageWhen(req.Expires))
		return
	}
	data := consentData{ID: req.ID, CSRF: s.requestCSRF(req.ID), Hash: req.Hash(), Kind: req.Kind,
		ClientID: req.Client, Principal: req.Principal, Created: pageWhen(req.Created), Expires: pageWhen(req.Expires)}
	if c, err := s.store.clientRow(ctx, req.Client); err == nil {
		data.Client = c.Name
		data.Bound = c.Bound()
	}
	switch req.Kind {
	case KindAccess:
		a, ok := s.apps.Get(req.App)
		if !ok {
			writeOneLine(w, http.StatusNotFound, "no app "+req.App)
			return
		}
		data.AppID = a.ID
		data.App = a.Manifest.Name
		data.Items = itemViews(req.Items)
		data.IsAccess = true
	default:
		// A sign-in's form goes to the client's redirect URI, so its policy
		// admits that origin.
		origin, err := redirectOrigin(req.Query.Get("redirect_uri"))
		if err != nil {
			writeOneLine(w, http.StatusInternalServerError, "the request's redirect URI cannot be read")
			return
		}
		data.Origin = origin
		w.Header().Set("Content-Security-Policy", PagePolicy(origin))
		for _, sc := range req.Scopes {
			data.Scopes = append(data.Scopes, scopeChoice{Name: sc, About: scopeAbout(sc)})
		}
	}
	s.render(w, "consent.html", data)
}

// scopeAbout says what a scope lets a client use.
func scopeAbout(scope string) string {
	switch scope {
	case ScopeMCP:
		return "call lodge's MCP gateway"
	case ScopeEvents:
		return "subscribe to the broker's events"
	}
	return scope
}

// render writes a page.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render a page", "page", name, "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot show the page")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// decideConsent decides a request: the only route of lodge's browser pages
// that writes (docs/specs/oauth.md, The consent page).
func (s *Server) decideConsent(w http.ResponseWriter, r *http.Request, id, admin string) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	if err := r.ParseForm(); err != nil {
		writeOneLine(w, http.StatusBadRequest, "the form cannot be read")
		return
	}
	req, err := s.request(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeOneLine(w, http.StatusNotFound, "no request "+id)
		return
	case err != nil:
		s.log.Error("read a request", "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the request")
		return
	}
	// The request's own token: a decision without it changes nothing.
	if err := s.checkRequestCSRF(id, r.PostForm.Get("csrf")); err != nil {
		writeOneLine(w, http.StatusForbidden, "the CSRF token is not this request's")
		return
	}
	// The hash of the request the page showed.
	if r.PostForm.Get("hash") != req.Hash() {
		writeOneLine(w, http.StatusConflict, "the page shows another request: reload it")
		return
	}
	switch {
	case !req.Decided.IsZero():
		writeOneLine(w, http.StatusConflict, "this request was decided: "+req.Decision)
		return
	case req.expires(s.now()):
		writeOneLine(w, http.StatusGone, "this request expired "+pageWhen(req.Expires))
		return
	}
	decision := r.PostForm.Get("decision")
	switch decision {
	case DecisionApprove:
		s.approve(w, r, req, admin)
	case DecisionDecline:
		s.decline(w, r, req, admin)
	default:
		writeOneLine(w, http.StatusBadRequest, "a decision is approve or decline")
	}
}

// approve decides a request, and does what it says: a sign-in answers the
// client with a code, and an access request adds its items to the grant.
func (s *Server) approve(w http.ResponseWriter, r *http.Request, req Request, admin string) {
	ctx := r.Context()
	var err error
	switch req.Kind {
	case KindSignin:
		var (
			principal string
			scopes    []string
		)
		principal, scopes, err = s.signinChoice(r, req)
		if err != nil {
			writeOneLine(w, http.StatusBadRequest, err.Error())
			return
		}
		// The authorization request is rebuilt and checked before anything
		// is decided: a request lodge cannot answer breaks a rule, answers
		// 400, and stays pending.
		ar, err := s.authorizeRequest(r, req.Query)
		if err != nil {
			s.log.Warn("rebuild a sign-in's request", "request", req.ID, "err", err)
			writeOneLine(w, http.StatusBadRequest, "the request cannot be answered any more: "+oneLine(err.Error()))
			return
		}
		if ok, derr := s.decide(ctx, req.ID, DecisionApprove); !ok {
			s.notDecided(w, derr, req.ID)
			return
		}
		s.log.Info("sign-in approved", "request", req.ID, "client", req.Client, "principal", principal, "scopes", joinScopes(scopes), "admin", admin)
		if err := s.approveSignin(ctx, req, principal, scopes); err != nil {
			s.log.Error("approve a sign-in", "request", req.ID, "err", err)
			writeOneLine(w, http.StatusInternalServerError, "lodge cannot bind the client")
			return
		}
		s.answerSignin(ctx, w, ar, req, principal, scopes)
	case KindAccess:
		items, err := s.accessChoice(r, req)
		if err != nil {
			writeOneLine(w, http.StatusBadRequest, err.Error())
			return
		}
		if ok, derr := s.decide(ctx, req.ID, DecisionApprove); !ok {
			s.notDecided(w, derr, req.ID)
			return
		}
		s.log.Info("access approved", "request", req.ID, "client", req.Client, "principal", req.Principal, "app", req.App, "items", len(items), "admin", admin)
		if err := s.addGrant(ctx, req.Principal, req.App, items); err != nil {
			s.log.Error("add a grant", "request", req.ID, "err", err)
			writeOneLine(w, http.StatusInternalServerError, "lodge cannot write the grant")
			return
		}
		w.Header().Set("Location", "/oauth/grants")
		w.WriteHeader(http.StatusSeeOther)
	default:
		writeOneLine(w, http.StatusBadRequest, "no request kind "+string(req.Kind))
	}
}

// signinChoice is a sign-in's principal and scopes, from the form.
func (s *Server) signinChoice(r *http.Request, req Request) (string, []string, error) {
	ctx := r.Context()
	principal := strings.TrimSpace(r.PostForm.Get("principal"))
	bound, err := s.boundPrincipal(ctx, req.Client)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", nil, fmt.Errorf("lodge cannot read the client")
	}
	// A bound client keeps its principal.
	if bound != "" {
		principal = bound
	}
	if !ValidPrincipal(principal) {
		return "", nil, fmt.Errorf("a principal is a to z, 0 to 9 and dashes, starting with a letter")
	}
	chosen := []string{}
	for _, sc := range r.PostForm["scope"] {
		if !has(req.Scopes, sc) {
			return "", nil, fmt.Errorf("scope %s was not asked for", sc)
		}
		if !has(chosen, sc) {
			chosen = append(chosen, sc)
		}
	}
	if len(chosen) == 0 {
		return "", nil, fmt.Errorf("approve 1 or more scopes")
	}
	return principal, chosen, nil
}

// answerSignin answers the client with fosite's authorization response: a
// redirect to its redirect URI with the code and its state.
func (s *Server) answerSignin(ctx context.Context, w http.ResponseWriter, ar fosite.AuthorizeRequester, req Request, principal string, scopes []string) {
	for _, sc := range scopes {
		ar.GrantScope(sc)
	}
	sess := NewSession()
	sess.Subject = principal
	resp, err := s.provider.NewAuthorizeResponse(ctx, ar, sess)
	if err != nil {
		s.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	s.log.Info("oauth code issued", "request", req.ID, "client", req.Client, "principal", principal)
	s.provider.WriteAuthorizeResponse(ctx, w, ar, resp)
}

// accessChoice is an access request's approved items, from the form: 1 or
// more checked items, each path valid and covered by the path asked for.
func (s *Server) accessChoice(r *http.Request, req Request) ([]Item, error) {
	a, ok := s.apps.Get(req.App)
	if !ok {
		return nil, fmt.Errorf("no app %s", req.App)
	}
	checked := []int{}
	for _, v := range r.PostForm["item"] {
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 || i >= len(req.Items) {
			return nil, fmt.Errorf("item %q is not one of the request's", v)
		}
		if !slices.Contains(checked, i) {
			checked = append(checked, i)
		}
	}
	if len(checked) == 0 {
		return nil, fmt.Errorf("approve 1 or more items")
	}
	out := make([]Item, 0, len(checked))
	for _, i := range checked {
		asked := req.Items[i]
		approved := Item{Cap: asked.Cap}
		if len(asked.Sel) > 0 {
			approved.Sel = make(map[string]string, len(asked.Sel))
			names := make([]string, 0, len(asked.Sel))
			for name := range asked.Sel {
				names = append(names, name)
			}
			sortStrings(names)
			for _, name := range names {
				field := "path." + strconv.Itoa(i) + "." + name
				given := strings.TrimSpace(r.PostForm.Get(field))
				if !ValidPath(given) {
					return nil, fmt.Errorf("%s is not a path: /, or /-separated segments", field)
				}
				// An admin may narrow a path, never widen it.
				if !Covers(asked.Sel[name], given) {
					return nil, fmt.Errorf("%s %s is not inside %s", field, given, asked.Sel[name])
				}
				approved.Sel[name] = given
			}
		}
		if err := validItem(a, approved); err != nil {
			return nil, err
		}
		out = append(out, approved)
	}
	return out, nil
}

// decline decides a request as declined: a sign-in goes back to its client
// with error=access_denied, and an access request goes to the grants page.
func (s *Server) decline(w http.ResponseWriter, r *http.Request, req Request, admin string) {
	ctx := r.Context()
	// A sign-in's request is rebuilt before the decision, as an approval's
	// is: one lodge cannot answer stays pending, and answers 400.
	var ar fosite.AuthorizeRequester
	if req.Kind == KindSignin {
		var err error
		if ar, err = s.authorizeRequest(r, req.Query); err != nil {
			s.log.Warn("rebuild a sign-in's request", "request", req.ID, "err", err)
			writeOneLine(w, http.StatusBadRequest, "the request cannot be answered any more: "+oneLine(err.Error()))
			return
		}
	}
	if ok, err := s.decide(ctx, req.ID, DecisionDecline); !ok {
		s.notDecided(w, err, req.ID)
		return
	}
	s.log.Info("request declined", "request", req.ID, "client", req.Client, "kind", string(req.Kind), "admin", admin)
	if req.Kind == KindAccess {
		w.Header().Set("Location", "/oauth/grants")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	s.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrAccessDenied)
}

// notDecided answers a decision another one won, or one that came too late.
func (s *Server) notDecided(w http.ResponseWriter, err error, id string) {
	if err != nil {
		s.log.Error("decide a request", "request", id, "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot decide the request")
		return
	}
	writeOneLine(w, http.StatusConflict, "this request was decided already")
}

// clientData is a client as the grants page shows it.
type clientData struct {
	ID, Name, Scope, Registered, LastUsed string
}

// grantData is a grant as the grants page shows it.
type grantData struct {
	App, AppName string
	Items        []itemChoice
}

// principalData is a principal as the grants page shows it.
type principalData struct {
	Name, Created, LastUsed string
	Grants                  []grantData
	Clients                 []clientData
}

// requestData is a pending request as the grants page shows it.
type requestData struct {
	ID, URL, Kind, Client, ClientName, Principal, App, Created, Expires string
}

// grantsData is what the grants page shows.
type grantsData struct {
	CSRF       string
	TS         string
	Principals []principalData
	Requests   []requestData
}

// grantsPage lists the principals, their clients and grants, and the
// pending requests.
func (s *Server) grantsPage(w http.ResponseWriter, r *http.Request, admin string) {
	ctx := r.Context()
	list, err := s.store.principals(ctx)
	if err != nil {
		s.log.Error("read the principals", "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the principals")
		return
	}
	ts := s.pageStamp()
	data := grantsData{CSRF: s.pageCSRF(admin, s.now().Unix()), TS: ts}
	for _, p := range list {
		pd := principalData{Name: p.Name, Created: pageWhen(p.Created)}
		clients, err := s.store.clients(ctx, p.Name)
		if err != nil {
			s.log.Error("read a principal's clients", "principal", p.Name, "err", err)
			writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the clients")
			return
		}
		for _, c := range clients {
			pd.Clients = append(pd.Clients, clientData{ID: c.ID, Name: c.Name, Scope: c.Scope,
				Registered: pageWhen(c.Registered), LastUsed: pageWhen(c.LastUsed)})
		}
		pd.LastUsed = pageWhen(principalLastUse(clients))
		granted, err := s.store.grants(ctx, p.Name)
		if err != nil {
			s.log.Error("read a principal's grants", "principal", p.Name, "err", err)
			writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the grants")
			return
		}
		apps := make([]string, 0, len(granted))
		for app := range granted {
			apps = append(apps, app)
		}
		sortStrings(apps)
		for _, app := range apps {
			g := grantData{App: app, AppName: app, Items: itemViews(granted[app])}
			if a, ok := s.apps.Get(app); ok {
				g.AppName = a.Manifest.Name
			}
			pd.Grants = append(pd.Grants, g)
		}
		data.Principals = append(data.Principals, pd)
	}
	pending, err := s.pendingRequests(ctx)
	if err != nil {
		s.log.Error("read the pending requests", "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot read the requests")
		return
	}
	for _, req := range pending {
		rd := requestData{ID: req.ID, URL: req.URL, Kind: string(req.Kind), Principal: req.Principal,
			App: req.App, Created: pageWhen(req.Created), Expires: pageWhen(req.Expires)}
		rd.Client, rd.ClientName = req.Client, req.Client
		if c, err := s.store.clientRow(ctx, req.Client); err == nil {
			rd.ClientName = c.Name
		}
		data.Requests = append(data.Requests, rd)
	}
	s.render(w, "grants.html", data)
}

// revokeTarget revokes a client or a principal, from the grants page.
func (s *Server) revokeTarget(w http.ResponseWriter, r *http.Request, admin string) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	if err := r.ParseForm(); err != nil {
		writeOneLine(w, http.StatusBadRequest, "the form cannot be read")
		return
	}
	if err := s.checkPageCSRF(admin, r.PostForm.Get("csrf"), r.PostForm.Get("ts")); err != nil {
		writeOneLine(w, http.StatusForbidden, err.Error())
		return
	}
	var err error
	client, principal := r.PostForm.Get("client"), r.PostForm.Get("principal")
	switch {
	case client != "" && principal != "":
		writeOneLine(w, http.StatusBadRequest, "a revocation names a client or a principal, not both")
		return
	case client != "":
		err = s.RevokeClient(ctx, client)
	case principal != "":
		err = s.RevokePrincipal(ctx, principal)
	default:
		writeOneLine(w, http.StatusBadRequest, "a revocation names a client or a principal")
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		writeOneLine(w, http.StatusNotFound, "no live client or principal of that name")
		return
	case err != nil:
		s.log.Error("revoke", "err", err)
		writeOneLine(w, http.StatusInternalServerError, "lodge cannot revoke it")
		return
	}
	if client != "" {
		s.log.Info("oauth client revoked by an admin", "client", client, "admin", admin)
	} else {
		s.log.Info("oauth principal revoked by an admin", "principal", principal, "admin", admin)
	}
	w.Header().Set("Location", "/oauth/grants")
	w.WriteHeader(http.StatusSeeOther)
}
