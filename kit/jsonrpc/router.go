package jsonrpc

import (
	"context"
	"encoding/json/jsontext"
	"slices"
)

// HandlerFunc decodes params, calls the service and returns the result.
type HandlerFunc func(ctx context.Context, params jsontext.Value) (any, error)

// Dispatcher runs one method: a Router, or an app's type that embeds one.
type Dispatcher interface {
	Dispatch(ctx context.Context, method string, params jsontext.Value) (any, error)
}

// Route is one method: the caller kinds it admits, whether it writes, and
// its handler.
type Route[K comparable] struct {
	Callers []K
	// Writes marks a method that changes state, which RouterOptions.Admit
	// may refuse, such as under an agent's read scope.
	Writes  bool
	Handler HandlerFunc
}

// RouterOptions configures a Router.
type RouterOptions[K comparable] struct {
	// Caller returns a request's caller kind, which a method must list. It
	// is required.
	Caller func(ctx context.Context) K
	// Admit, when set, may refuse a call its method's callers admit, such
	// as a write under a read scope.
	Admit func(ctx context.Context, method string, r Route[K]) bool
}

// Router maps wire method names to handlers, with the caller kinds of type
// K each admits. An app's api package embeds it in its own Router, so the
// generated code registers through the app's unexported handle.
type Router[K comparable, C Code] struct {
	routes map[string]Route[K]
	opts   RouterOptions[K]
}

// NewRouter returns an empty Router.
func NewRouter[K comparable, C Code](opts RouterOptions[K]) *Router[K, C] {
	return &Router[K, C]{routes: map[string]Route[K]{}, opts: opts}
}

// Handle registers method, replacing any route it had.
func (r *Router[K, C]) Handle(method string, rt Route[K]) { r.routes[method] = rt }

// Dispatch runs one method for the caller in ctx. A method that does not
// exist, that does not admit the caller's kind, or that Admit refuses,
// answers methodNotFound alike, so a caller cannot learn what it may not
// call.
func (r *Router[K, C]) Dispatch(ctx context.Context, method string, params jsontext.Value) (any, error) {
	rt, ok := r.routes[method]
	if !ok || r.opts.Caller == nil || !slices.Contains(rt.Callers, r.opts.Caller(ctx)) ||
		r.opts.Admit != nil && !r.opts.Admit(ctx, method, rt) {
		return nil, Errorf(C(CodeMethodNotFound), "method %q does not exist", method)
	}
	return rt.Handler(ctx, params)
}

// Route returns method's route, for tests that check every method's callers.
func (r *Router[K, C]) Route(method string) (Route[K], bool) {
	rt, ok := r.routes[method]
	return rt, ok
}
