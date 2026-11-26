package ace

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testPieceLen = 8 * 4096
	testChunkLen = 4096
)

func testPeerConfig(t *testing.T, ih [20]byte) PeerConfig {
	_, key, _ := ed25519.GenerateKey(nil)
	return PeerConfig{
		InfoHash: ih,
		PeerID:   NewPeerID(),
		ExtHandshake: func(ip net.IP) ([]byte, error) {
			return BuildExtHandshake(8621, ip, key)
		},
		RequestEvery: 20 * time.Millisecond,
		ChokedPoll:   10 * time.Millisecond,
	}
}

func fastTuning() Tuning {
	tu := DefaultTuning()
	tu.AssignCooldown = 100 * time.Millisecond
	return tu
}

func runPeerUntil(t *testing.T, fp *fakePeer, coord *Coordinator, out *collector, want int) PeerStats {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		st  PeerStats
		err error
	}
	res := make(chan result, 1)
	go func() {
		st, err := RunPeer(ctx, &net.Dialer{}, fp.addr(), testPeerConfig(t, testIH), coord)
		res <- result{st, err}
	}()
	out.waitFor(t, want, 10*time.Second)
	cancel()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("RunPeer after cancel: %v", r.err)
		}
		return r.st
	case <-time.After(3 * time.Second):
		t.Fatal("RunPeer did not return after cancel")
	}
	return PeerStats{}
}

func TestPeerSessionReassemblesLiveStream(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 400)
	fp := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	var out collector
	coord := NewCoordinator(out.write, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	st := runPeerUntil(t, fp, coord, &out, 20*testPieceLen)
	src.assertContiguous(t, out.bytes(), 20*testPieceLen)
	if !st.Handshook || !st.Engaged || st.Chunks == 0 {
		t.Fatalf("stats %+v", st)
	}
	if !fp.gotExtHello.Load() {
		t.Fatal("no ext-handshake sent")
	}
	fp.mu.Lock()
	hello := fp.remoteHellos[0]
	fp.mu.Unlock()
	if hello[0] != 0 {
		t.Fatal("ext-handshake sub-id must be 0")
	}
	if ok, err := VerifyExtHandshake(hello[1:]); !ok || err != nil {
		t.Fatalf("our ext-handshake does not verify: %v", err)
	}
	d, _ := Decode(hello[1:])
	if ip, _ := d.(map[string]any)["yourip"].([]byte); net.IP(ip).String() != "127.0.0.1" {
		t.Fatalf("yourip = %v", ip)
	}
}

func TestPeerSessionRerequestsLostChunks(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 400)
	fp := startFakePeer(t, src, fakePeerOpts{infohash: testIH, dropFirst: true})
	var out collector
	tune := fastTuning()
	tune.SkipAfter = 0 // a lost chunk must be fetched again, never skipped over
	coord := NewCoordinator(out.write, testPieceLen, testChunkLen, PieceSigLen, tune)
	runPeerUntil(t, fp, coord, &out, 10*testPieceLen)
	src.assertContiguous(t, out.bytes(), 10*testPieceLen)
}

func TestPeerSessionWaitsForUnchoke(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 400)
	fp := startFakePeer(t, src, fakePeerOpts{infohash: testIH, startChoked: 300 * time.Millisecond})
	var out collector
	coord := NewCoordinator(out.write, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	runPeerUntil(t, fp, coord, &out, 5*testPieceLen)
	if n := fp.whileChoked.Load(); n != 0 {
		t.Fatalf("%d requests sent while choked", n)
	}
	src.assertContiguous(t, out.bytes(), 5*testPieceLen)
}

func TestPeerSessionRejectsWrongInfohash(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 10)
	fp := startFakePeer(t, src, fakePeerOpts{infohash: [20]byte{9, 9, 9}})
	coord := NewCoordinator(func([]byte) {}, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	_, err := RunPeer(context.Background(), &net.Dialer{}, fp.addr(), testPeerConfig(t, testIH), coord)
	if err == nil || !strings.Contains(err.Error(), "infohash mismatch") {
		t.Fatalf("%v", err)
	}
}

func TestPeerSessionRejectsGarbage(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, HandshakeLen)
		c.Read(buf)
		c.Write(append(BuildHandshake(testIH, NewPeerID()), 0xff, 0xff, 0xff, 0xff))
		time.Sleep(time.Second)
	}()
	coord := NewCoordinator(func([]byte) {}, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	_, err := RunPeer(context.Background(), &net.Dialer{}, netip.MustParseAddrPort(ln.Addr().String()), testPeerConfig(t, testIH), coord)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("%v", err)
	}
}

func TestPeerSessionConnectRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := netip.MustParseAddrPort(ln.Addr().String())
	ln.Close()
	coord := NewCoordinator(func([]byte) {}, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	st, err := RunPeer(context.Background(), &net.Dialer{}, addr, testPeerConfig(t, testIH), coord)
	if err == nil || st.Handshook {
		t.Fatalf("%+v %v", st, err)
	}
}

// jammer is a peer that asks us for n chunks of piece and stops reading its
// socket, as a slow uplink looks from our side, while it keeps sending us
// chunks of its own. Once resume is closed it reads again and counts the
// PIECEs we answered with.
type jammer struct {
	ln     net.Listener
	resume chan struct{}
	pieces atomic.Int64 // PIECEs it read from us after resuming
}

func startJammer(t *testing.T, src *liveSource, piece int64, n int) *jammer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	j := &jammer{ln: ln, resume: make(chan struct{})}
	done := make(chan struct{})
	t.Cleanup(func() { ln.Close(); close(done) })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() { <-done; c.Close() }()
		if _, err := io.ReadFull(c, make([]byte, HandshakeLen)); err != nil {
			return
		}
		var reqs []byte
		for range n {
			reqs = append(reqs, BuildRequest(piece, 0)...)
		}
		c.Write(append(append(BuildHandshake(testIH, NewPeerID()), BuildMessage(MsgInterested, nil)...), reqs...))
		go func() { // its own chunks keep coming, unasked
			for ci := 0; ; ci = (ci + 1) % (testPieceLen / testChunkLen) {
				msg := binary.BigEndian.AppendUint64(nil, uint64(src.first))
				msg = binary.BigEndian.AppendUint64(msg, 0)
				msg = binary.BigEndian.AppendUint16(msg, uint16(ci))
				if _, err := c.Write(BuildMessage(MsgPiece, append(msg, src.chunk(src.first, ci)...))); err != nil {
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
		select {
		case <-j.resume:
		case <-done:
			return
		}
		for {
			c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			id, _, err := ReadMessage(c, MaxMessageLen)
			if err != nil {
				return
			}
			if id == MsgPiece {
				j.pieces.Add(1)
			}
		}
	}()
	return j
}

// runAgainst runs RunPeer against addr with a coordinator holding one
// complete piece to serve, and counts the PIECEs it reads.
func runAgainst(t *testing.T, addr netip.AddrPort, src *liveSource, piece int64) *atomic.Int64 {
	t.Helper()
	coord := NewCoordinator(func([]byte) {}, testPieceLen, testChunkLen, PieceSigLen, fastTuning())
	for ci := range testPieceLen / testChunkLen {
		coord.Keep(Piece{Index: piece, Chunk: ci, Data: src.chunk(piece, ci)})
	}
	var got atomic.Int64
	cfg := testPeerConfig(t, testIH)
	cfg.OnMessage = func(_ netip.AddrPort, id int, _ []byte) {
		if id == MsgPiece {
			got.Add(1)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunPeer(ctx, &net.Dialer{}, addr, cfg, coord); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return &got
}

// 5000 answers of a 4 KiB chunk are 20 MB: more than the socket buffers hold.
const jamRequests = 5000

// Uploading to a peer that reads slowly must not stop us reading what it
// sends: on a slow uplink that peer is often the main source of the video.
func TestUploadDoesNotBlockDownloadFromSamePeer(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 10)
	j := startJammer(t, src, src.first, jamRequests)
	got := runAgainst(t, j.addr(), src, src.first)
	deadline := time.Now().Add(5 * time.Second)
	for got.Load() < 1000 {
		if time.Now().After(deadline) {
			t.Fatalf("read only %d PIECEs while our answers to its requests were stuck", got.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Requests past the upload queue are dropped, not answered late.
func TestUploadQueueDropsExcessRequests(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 10)
	j := startJammer(t, src, src.first, jamRequests)
	runAgainst(t, j.addr(), src, src.first)
	time.Sleep(time.Second) // our read loop goes through every request meanwhile
	close(j.resume)
	time.Sleep(2 * time.Second)
	if n := j.pieces.Load(); n == 0 || n > jamRequests/2 {
		t.Fatalf("answered %d of %d requests, want some and well under half", n, jamRequests)
	}
}

func (j *jammer) addr() netip.AddrPort { return netip.MustParseAddrPort(j.ln.Addr().String()) }
