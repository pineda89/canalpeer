//go:build live

package ace

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// TestLiveChannel pulls a real channel through the real swarm:
//
//	ACEWEB_LIVE_CID=<content id> go test -tags live -run TestLiveChannel -v ./internal/ace/
func TestLiveChannel(t *testing.T) {
	cid := os.Getenv("ACEWEB_LIVE_CID")
	if cid == "" {
		t.Skip("set ACEWEB_LIVE_CID to a live content id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d := &net.Dialer{}
	desc, err := NewResolver(d).Resolve(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved %q infohash %x trackers %v", desc.Name, desc.InfoHash, desc.Trackers)
	var out collector
	start := time.Now()
	var first time.Duration
	sw := NewSwarm(SwarmConfig{Desc: desc, Dialer: d, Out: func(p []byte) {
		if first == 0 {
			first = time.Since(start)
		}
		out.write(p)
	}})
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { sw.Run(runCtx); close(done) }()
	out.waitFor(t, 1<<20, 45*time.Second)
	time.Sleep(10 * time.Second)
	stop()
	<-done
	ts := out.bytes()
	packets, bad := ValidateTS(ts, 0)
	t.Logf("first TS after %v; %d bytes, %d packets, %d bad; stats %+v", first, len(ts), packets, bad, sw.Stats())
	if bad != 0 {
		t.Fatalf("%d corrupt TS packets", bad)
	}
}

// TestLiveUpload plays a real channel for 3 minutes and checks peers took
// pieces from us (AceStream peers first request after ~45-75 s, measured):
//
//	ACEWEB_LIVE_CID=<content id> go test -tags live,disable_libutp -run TestLiveUpload -v -timeout 5m ./ace/
func TestLiveUpload(t *testing.T) {
	cid := os.Getenv("ACEWEB_LIVE_CID")
	if cid == "" {
		t.Skip("set ACEWEB_LIVE_CID to a live content id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	d := &net.Dialer{}
	desc, err := NewResolver(d).Resolve(ctx, cid)
	if err != nil {
		t.Fatal(err)
	}
	var out collector
	sw := NewSwarm(SwarmConfig{Desc: desc, Dialer: d, Out: out.write})
	runCtx, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	sw.Run(runCtx)
	st, got := sw.Stats(), len(out.bytes())
	t.Logf("%q: %d TS bytes; uploaded %d bytes; stats %+v", desc.Name, got, st.Uploaded, st)
	if got == 0 {
		t.Fatal("no video in 3 minutes")
	}
	if st.Uploaded == 0 {
		t.Fatal("nobody took a piece from us")
	}
}
