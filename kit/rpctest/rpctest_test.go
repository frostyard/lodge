package rpctest_test

import (
	"context"
	"net"
	"testing"

	"github.com/frostyard/lodge/kit/rpctest"
)

type services struct{ Note, Mail string }

func TestOptionsEditTheServices(t *testing.T) {
	s := services{Note: "engine", Mail: "engine"}
	rpctest.Options[services]{}.Edit(&s)
	rpctest.Options[services]{Services: func(s *services) { s.Note = "fake" }}.Edit(&s)
	if s != (services{Note: "fake", Mail: "engine"}) {
		t.Errorf("services = %+v", s)
	}
}

func TestServeDialPipeAndStop(t *testing.T) {
	accepted := make(chan struct{}, 1)
	run := rpctest.Serve(t, func(ctx context.Context, ln net.Listener) error {
		go func() {
			<-ctx.Done()
			_ = ln.Close()
		}()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return nil
			}
			_ = nc.Close()
			accepted <- struct{}{}
		}
	})
	run.Dial(t)
	<-accepted
	served := make(chan string, 1)
	nc := run.Pipe(t, func(ctx context.Context, nc net.Conn) {
		buf := make([]byte, 2)
		_, _ = nc.Read(buf)
		served <- string(buf)
		<-ctx.Done()
	})
	if _, err := nc.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got := <-served; got != "hi" {
		t.Errorf("the piped connection read %q", got)
	}
	run.Stop()
	<-run.Context().Done()
	run.Stop() // twice is fine
}
