// Command lognode starts one tailnet node against a test control server,
// holds it up for -hold, then stops it, so that TestNothingDialsTheLogHost
// can watch what it dials. It is its own binary because tsnet never starts
// its log uploader inside a test binary. With -bare it starts a plain
// tsnet.Server, without lodge's Start, to show that the test sees an
// upload when one happens.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tsnet"

	"github.com/frostyard/lodge/internal/tailnet"
)

func main() {
	control := flag.String("control", "", "the test control server's URL")
	dir := flag.String("dir", "", "the node's state directory")
	bare := flag.Bool("bare", false, "start a plain tsnet.Server instead of lodge's node")
	hold := flag.Duration("hold", 0, "how long to keep the node up")
	flag.Parse()
	netns.SetEnabled(false)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := run(ctx, *control, *dir, *bare, *hold); err != nil {
		fmt.Fprintln(os.Stderr, "lognode:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, control, dir string, bare bool, hold time.Duration) error {
	if bare {
		srv := &tsnet.Server{Dir: dir, Hostname: "bare", ControlURL: control, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
		if _, err := srv.Up(ctx); err != nil {
			return err
		}
		fmt.Fprintln(srv.LogtailWriter(), "lognode: a line for the log host")
		time.Sleep(hold) // the test stops this process once it sees the upload
		return srv.Close()
	}
	n, err := tailnet.Start(ctx, tailnet.Config{Hostname: "lodge", Dir: dir, ControlURL: control})
	if err != nil {
		return err
	}
	time.Sleep(hold) // the window in which an uploader would flush
	return n.Close()
}
