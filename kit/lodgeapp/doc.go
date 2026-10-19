// Package lodgeapp serves an app daemon's side of the lodge app contract
// (https://github.com/frostyard/lodge/blob/main/docs/specs/app-contract.md):
// HTTP/1.1 on the app's Unix socket, which admits only lodge's uid, with
// the Lodge-* headers parsed into a Caller and each path admitting one
// caller kind.
//
//	env, err := lodgeapp.LoadEnv() // LODGE_ROOT, LODGE_UID, or user lodge
//	ln, err := env.Listen("notes") // socket activation, or /run/lodge/apps/notes.sock
//	srv := &lodgeapp.Server{
//		Health: func(context.Context) lodgeapp.Health {
//			return lodgeapp.Health{OK: true, Version: version, Protocol: api.Protocol}
//		},
//		API: func(ctx context.Context, nc net.Conn, c lodgeapp.Caller) {
//			rpc.ServeConn(ctx, nc, router, api.CallerSession)
//		},
//		MCP: mcpsession.New(mcpHandler, mcpsession.Options{}),
//	}
//	err = srv.Serve(ctx, ln)
//
// The /api WebSocket reaches the app as a net.Conn that carries one
// JSON-RPC message per line, so an app plugs in the server it already has
// for its private socket. Package kittest plays lodge's side in tests.
package lodgeapp
