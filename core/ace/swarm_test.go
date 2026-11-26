package ace

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestSwarmEndToEnd(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	a := startFakePeer(t, src, fakePeerOpts{infohash: testIH, dropFirst: true})
	b := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	wrong := startFakePeer(t, src, fakePeerOpts{infohash: [20]byte{1}})
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := netip.MustParseAddrPort(dead.Addr().String())
	dead.Close()
	ft := startFakeTracker(t, a.addr(), b.addr(), wrong.addr(), deadAddr)

	var out collector
	sw := NewSwarm(SwarmConfig{
		Desc: Descriptor{
			InfoHash: testIH, Trackers: []string{ft.url()},
			PieceLength: testPieceLen, ChunkLength: testChunkLen,
		},
		Dialer: &net.Dialer{},
		Out:    out.write,
		Tuning: fastTuning(),
		Peer:   PeerConfig{RequestEvery: 20 * time.Millisecond, ChokedPoll: 10 * time.Millisecond},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sw.Run(ctx) }()
	out.waitFor(t, 40*testPieceLen, 15*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	src.assertContiguous(t, out.bytes(), 40*testPieceLen)
	st := sw.Stats()
	if st.Engaged < 2 || st.Dialed < 4 || st.Active != 0 {
		t.Fatalf("stats %+v", st)
	}
	if a.servedChunks.Load() == 0 || b.servedChunks.Load() == 0 {
		t.Fatalf("load not spread: a=%d b=%d", a.servedChunks.Load(), b.servedChunks.Load())
	}
	if ft.announces.Load() < 1 {
		t.Fatal("never announced")
	}
}

func TestSwarmRedialsWhenPoolRunsDry(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	ft := startFakeTracker(t) // no peers at first
	var out collector
	extra := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	sw := NewSwarm(SwarmConfig{
		Desc:       Descriptor{InfoHash: testIH, Trackers: []string{ft.url()}, PieceLength: testPieceLen, ChunkLength: testChunkLen},
		Dialer:     &net.Dialer{},
		Out:        out.write,
		Tuning:     fastTuning(),
		Peer:       PeerConfig{RequestEvery: 20 * time.Millisecond, ChokedPoll: 10 * time.Millisecond, ReadTimeout: 300 * time.Millisecond},
		ExtraPeers: []netip.AddrPort{extra.addr()},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runSwarm(t, sw)
	out.waitFor(t, 5*testPieceLen, 10*time.Second)
	// The only peer goes away; once the pool is dry the swarm announces again.
	before := ft.announces.Load()
	extra.ln.Close()
	extra.connectionsMu.Lock()
	for _, c := range extra.conns {
		c.Close()
	}
	extra.connectionsMu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for ft.announces.Load() == before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ft.announces.Load() == before {
		t.Fatal("did not re-announce after the pool ran dry")
	}
}

// A tracker that never answers must not hold back the peers another tracker
// already gave: the official engine starts on the first reply (measured
// 2026-10-31: one dead tracker out of seven kept a channel at 0 peers for 17 s).
func TestSwarmDoesNotWaitForSilentTracker(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	p := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	ft := startFakeTracker(t, p.addr())
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	var out collector
	sw := NewSwarm(SwarmConfig{
		Desc: Descriptor{
			InfoHash: testIH, Trackers: []string{"udp://" + silent.LocalAddr().String() + "/announce", ft.url()},
			PieceLength: testPieceLen, ChunkLength: testChunkLen,
		},
		Dialer: &net.Dialer{},
		Out:    out.write,
		Tuning: fastTuning(),
		Peer:   PeerConfig{RequestEvery: 20 * time.Millisecond, ChokedPoll: 10 * time.Millisecond},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	runSwarm(t, sw)
	out.waitFor(t, 5*testPieceLen, TrackerTimeout/2)
}

// runSwarm runs sw until the test ends, and waits for Run to return: its
// announces read TrackerTimeout, which other tests set.
func runSwarm(t *testing.T, sw *Swarm) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}
