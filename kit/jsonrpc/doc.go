// Package jsonrpc is the apps' JSON-RPC 2.0 runtime, one message per line:
// the server's connection loop, the router with caller kinds, the client,
// and the private socket. It is generic over an app's own types, the
// generated ErrorCode (C) and its caller kind (K), so an app's generated
// api package sits on it unchanged
// (https://github.com/frostyard/lodge/blob/main/docs/adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md).
//
// An app's hand-written api runtime names the kit's types:
//
//	type ErrorCode int // the generated constants and String name it
//	type Error = jsonrpc.Error[ErrorCode]
//	type Frame = jsonrpc.Frame[ErrorCode]
//	type Event = jsonrpc.Event
//	type EventEnvelope = jsonrpc.EventEnvelope
//	type Router struct{ *jsonrpc.Router[Caller, ErrorCode] }
//	type Client struct{ *jsonrpc.Client[ErrorCode] }
//
//	func (r *Router) handle(method string, callers []Caller, writes bool, h jsonrpc.HandlerFunc) {
//		r.Handle(method, jsonrpc.Route[Caller]{Callers: callers, Writes: writes, Handler: h})
//	}
//	func decodeParams(raw jsontext.Value, p any, required []string) error {
//		return jsonrpc.DecodeParams[ErrorCode](raw, p, required)
//	}
//
// and its rpcserver serves its router on the kit:
//
//	rpc := jsonrpc.NewServer[api.ErrorCode](jsonrpc.ServerOptions{Hello: "rpc.hello"})
//	ln, err := jsonrpc.Listen(socket, log)  // the private socket: same uid only
//	err = rpc.Serve(ctx, ln, router, asLocal) // asLocal puts the caller in each connection's context
//	rpc.ServeConn(asSession(ctx), nc, router) // lodge's /api (lodgeapp.Server.API)
//
// Its rpc.hello calls ConnFrom(ctx).SetReady(client). Its events.subscribe
// starts delivery with AfterReply and Conn.Go, and sends with
// Conn.Notify(EventMethod, envelope).
package jsonrpc
