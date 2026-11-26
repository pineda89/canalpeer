package ace

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

// keepPiece stores every chunk of piece idx, each filled with a byte derived
// from (idx, chunk), and returns the chunks as kept.
func keepPiece(s *seedStore, idx int64, npp, chunkLen int, ts float64) [][]byte {
	var chunks [][]byte
	for c := 0; c < npp; c++ {
		d := bytes.Repeat([]byte{byte(idx) + byte(c)}, chunkLen)
		chunks = append(chunks, d)
		s.keep(Piece{Index: idx, Timestamp: ts, Chunk: c, Data: d})
	}
	return chunks
}

func TestServeReturnsRawPayload(t *testing.T) {
	s := newSeedStore(4)
	chunks := keepPiece(s, 7000, 4, 1024, 12.5)
	copy(chunks[3][1024-PieceSigLen:], bytes.Repeat([]byte{0xEE}, PieceSigLen)) // the signature trailer
	for c := range 4 {
		got, ok := s.serve(7000, c)
		if !ok {
			t.Fatalf("chunk %d not served", c)
		}
		want := binary.BigEndian.AppendUint64(nil, 7000)
		want = binary.BigEndian.AppendUint64(want, math.Float64bits(12.5))
		want = binary.BigEndian.AppendUint16(want, uint16(c))
		want = append(want, chunks[c]...)
		if !bytes.Equal(got, want) {
			t.Fatalf("chunk %d payload differs from what was received", c)
		}
		pc, err := ParsePiece(got)
		if err != nil || pc.Index != 7000 || pc.Timestamp != 12.5 || pc.Chunk != c {
			t.Fatalf("chunk %d does not parse back: %+v %v", c, pc, err)
		}
	}
}

func TestServeOnlyCompletePieces(t *testing.T) {
	s := newSeedStore(4)
	for c := range 3 {
		s.keep(Piece{Index: 1, Chunk: c, Data: make([]byte, 1024)})
	}
	for c := range 4 {
		if _, ok := s.serve(1, c); ok {
			t.Fatalf("served chunk %d of an incomplete piece", c)
		}
	}
	if _, ok := s.serve(2, 0); ok {
		t.Fatal("served a piece never seen")
	}
	if done, _ := s.doneSince(0); len(done) != 0 {
		t.Fatalf("incomplete piece reported done: %v", done)
	}
	s.keep(Piece{Index: 1, Chunk: 3, Data: make([]byte, 1024)})
	if _, ok := s.serve(1, 3); !ok {
		t.Fatal("complete piece not served")
	}
	if done, n := s.doneSince(0); len(done) != 1 || done[0] != 1 || n != 1 {
		t.Fatalf("doneSince = %v, %d", done, n)
	}
}

func TestDoneSinceCountsPastTheTrimmedLog(t *testing.T) {
	s := newSeedStore(1)
	for i := range int64(maxDone + 10) {
		s.keep(Piece{Index: i, Data: []byte{1}})
	}
	if done, n := s.doneSince(0); len(done) != maxDone || done[0] != 10 || n != maxDone+10 {
		t.Fatalf("doneSince(0) = %d pieces from %d, next %d", len(done), done[0], n)
	}
	if done, n := s.doneSince(maxDone + 9); len(done) != 1 || done[0] != maxDone+9 || n != maxDone+10 {
		t.Fatalf("doneSince(last) = %v, next %d", done, n)
	}
}

func TestSeedCapDropsOldest(t *testing.T) {
	const npp, chunkLen = 4, 64 << 10 // 256 KiB pieces
	s := newSeedStore(npp)
	const pieces = seedCap/(npp*chunkLen) + 20
	for i := range int64(pieces) {
		keepPiece(s, 100+i, npp, chunkLen, 0)
	}
	if s.bytes > seedCap {
		t.Fatalf("holding %d bytes, cap %d", s.bytes, seedCap)
	}
	if _, ok := s.serve(100, 0); ok {
		t.Fatal("oldest piece still served")
	}
	if _, ok := s.serve(100+pieces-1, npp-1); !ok {
		t.Fatal("newest piece not served")
	}
	if len(s.pieces) != seedCap/(npp*chunkLen) {
		t.Fatalf("holding %d pieces, want %d", len(s.pieces), seedCap/(npp*chunkLen))
	}
}

// startUploadSwarm runs a swarm that dials only peers, until the test ends.
func startUploadSwarm(t *testing.T, peers ...netip.AddrPort) *Swarm {
	t.Helper()
	return startUploadSwarmWith(t, &net.Dialer{}, peers...)
}

func startUploadSwarmWith(t *testing.T, d Dialer, peers ...netip.AddrPort) *Swarm {
	t.Helper()
	sw := NewSwarm(SwarmConfig{
		Desc:       Descriptor{InfoHash: testIH, PieceLength: testPieceLen, ChunkLength: testChunkLen},
		Dialer:     d,
		Out:        func([]byte) {},
		Tuning:     fastTuning(),
		Peer:       PeerConfig{RequestEvery: 20 * time.Millisecond, ChokedPoll: 10 * time.Millisecond},
		ExtraPeers: peers,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sw
}

func TestPeerUnchokesOnInterested(t *testing.T) {
	l := startLeech(t, leechOpts{})
	startUploadSwarm(t, l.addr())
	l.send(t, BuildMessage(MsgInterested, nil))
	l.waitFor(t, "UNCHOKE", func() bool { return len(l.received(MsgUnchoke)) > 0 })
}

func TestPeerSendsHaveOnCompletion(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	seeder := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	l := startLeech(t, leechOpts{})
	sw := startUploadSwarm(t, seeder.addr(), l.addr())
	l.waitFor(t, "HAVE", func() bool { return len(l.received(MsgHave)) > 0 })
	n := int64(binary.BigEndian.Uint64(l.received(MsgHave)[0]))
	if n < src.first || n > src.edge() {
		t.Fatalf("HAVE %d outside the channel's pieces [%d, %d]", n, src.first, src.edge())
	}
	if _, ok := sw.coord.Serve(n, 0); !ok {
		t.Fatalf("HAVE %d for a piece we cannot serve", n)
	}
}

func TestPeerServesRequest(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	seeder := startFakePeer(t, src, fakePeerOpts{infohash: testIH})
	l := startLeech(t, leechOpts{})
	sw := startUploadSwarm(t, seeder.addr(), l.addr())
	l.waitFor(t, "HAVE", func() bool { return len(l.received(MsgHave)) > 0 })
	n := int64(binary.BigEndian.Uint64(l.received(MsgHave)[0]))
	if up := sw.Stats().Uploaded; up != 0 {
		t.Fatalf("Uploaded %d before any request", up)
	}
	l.send(t, BuildMessage(MsgInterested, nil))
	l.send(t, BuildRequest(n, 1))
	l.waitFor(t, "PIECE", func() bool { return len(l.received(MsgPiece)) > 0 })
	got := l.received(MsgPiece)[0]
	pc, err := ParsePiece(got)
	if err != nil || pc.Index != n || pc.Chunk != 1 || !bytes.Equal(pc.Data, src.chunk(n, 1)) {
		t.Fatalf("PIECE is not chunk (%d, 1) as the seeder sent it: %+v %v", n, pc.Index, err)
	}
	if want, _ := sw.coord.Serve(n, 1); !bytes.Equal(got, want) {
		t.Fatal("PIECE payload is not the raw payload received")
	}
	l.waitFor(t, "Uploaded", func() bool { return sw.Stats().Uploaded == int64(len(got)) })
}

func TestPeerIgnoresUnknownRequest(t *testing.T) {
	l := startLeech(t, leechOpts{})
	startUploadSwarm(t, l.addr())
	l.send(t, BuildMessage(MsgInterested, nil))
	l.send(t, BuildRequest(123456, 0))
	l.send(t, BuildMessage(MsgInterested, nil)) // answered only if the connection survived the request
	l.waitFor(t, "second UNCHOKE", func() bool { return len(l.received(MsgUnchoke)) == 2 })
	if n := len(l.received(MsgPiece)); n != 0 {
		t.Fatalf("%d PIECEs for a piece we do not hold", n)
	}
}

func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	src := newLiveSource(testPieceLen, testChunkLen, 600)
	// dropFirst: chunks the seeder ignores are handed again, often to the leech,
	// so our request loop writes to it all along.
	seeder := startFakePeer(t, src, fakePeerOpts{infohash: testIH, dropFirst: true})
	l := startLeech(t, leechOpts{src: src, autoRequest: true})
	startUploadSwarmWith(t, splitDialer{}, seeder.addr(), l.addr())
	l.send(t, BuildMessage(MsgInterested, nil))
	// Our REQUESTs (request loop), HAVEs (HAVE loop) and PIECEs (read loop)
	// all share the connection.
	l.waitFor(t, "mixed traffic", func() bool {
		return len(l.received(MsgPiece)) >= 200 && len(l.received(MsgHave)) >= 20 && len(l.received(MsgRequest)) >= 20
	})
	for _, p := range l.received(MsgPiece) {
		pc, _ := ParsePiece(p)
		if !bytes.Equal(pc.Data, src.chunk(pc.Index, pc.Chunk)) {
			t.Fatalf("PIECE (%d, %d) data corrupted", pc.Index, pc.Chunk)
		}
	}
}

// splitDialer's connections write in small pieces, yielding between them, the
// way a userspace network stack may: a Write is not atomic on its own, so two
// unserialized writers interleave their frames. (A kernel TCPConn serializes
// whole Writes itself and would hide a missing lock.)
type splitDialer struct{}

func (splitDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c, err := (&net.Dialer{}).DialContext(ctx, network, address)
	return splitConn{c}, err
}

type splitConn struct{ net.Conn }

func (c splitConn) Write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		k, err := c.Conn.Write(b[:min(7, len(b))])
		n += k
		if err != nil {
			return n, err
		}
		b = b[k:]
		runtime.Gosched()
	}
	return n, nil
}
