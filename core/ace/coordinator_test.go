package ace

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

const npp = 8

type fakeClock struct{ t time.Time }

func (f *fakeClock) now() time.Time          { return f.t }
func (f *fakeClock) advance(d time.Duration) { f.t = f.t.Add(d) }

// testCoordinator uses 8 chunks of 188 bytes per piece and no signature, so
// each piece is exactly 8 TS packets: enough for FindTSSync's 8-packet probe.
func testCoordinator(t *testing.T, tune Tuning) (*Coordinator, *bytes.Buffer, *fakeClock) {
	t.Helper()
	var out bytes.Buffer
	c := NewCoordinator(func(p []byte) { out.Write(p) }, npp*TSPacket, TSPacket, 0, tune)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	c.now = clk.now
	return c, &out, clk
}

// tsChunk is one TS packet tagged with its piece and chunk.
func tsChunk(piece int64, chunk int) []byte {
	b := make([]byte, TSPacket)
	b[0] = TSSync
	b[1], b[2] = byte(piece), byte(chunk)
	return b
}

func sinkPiece(c *Coordinator, piece int64) {
	for ci := 0; ci < npp; ci++ {
		c.Sink(piece, ci, tsChunk(piece, ci))
	}
}

func TestCoordinatorStartsBufferDepthBehindEdge(t *testing.T) {
	tune := DefaultTuning()
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 1000)
	got := c.Wanted(-1, -1, 1000)
	if len(got) == 0 || got[0].Piece != 1000-int64(tune.BufferDepth) {
		t.Fatalf("first wanted %v, want piece %d", got[:1], 1000-tune.BufferDepth)
	}
	sinkPiece(c, 1000-int64(tune.BufferDepth)+3)
	if front, _ := c.reasm.Front(); front != 1000-int64(tune.BufferDepth) {
		t.Fatalf("front %d", front)
	}
}

func TestCoordinatorWantedNothingBeforeEdge(t *testing.T) {
	c, _, _ := testCoordinator(t, DefaultTuning())
	if got := c.Wanted(-1, -1, 100); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestCoordinatorWantedWindowAndPeerRange(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth, tune.RequestAhead = 2, 3
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 100) // front 98, window 98..100 (capped by the edge)
	got := c.Wanted(-1, -1, 1000)
	if len(got) != 3*npp || got[0] != (ChunkRef{98, 0}) || got[len(got)-1] != (ChunkRef{100, npp - 1}) {
		t.Fatalf("window %v", got)
	}
	c2, _, _ := testCoordinator(t, tune)
	c2.NoteEdge("peer", 100)
	got = c2.Wanted(99, 99, 1000)
	if len(got) != npp || got[0].Piece != 99 || got[npp-1].Piece != 99 {
		t.Fatalf("peer range [99,99]: %v", got)
	}
	if got := c2.Wanted(200, 300, 1000); len(got) != 0 {
		t.Fatalf("peer ahead of the window: %v", got)
	}
}

func TestCoordinatorSplitsChunksAcrossPeers(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth, tune.RequestAhead = 2, 3
	c, _, clk := testCoordinator(t, tune)
	c.NoteEdge("peer", 100)
	a := c.Wanted(-1, -1, 5)
	b := c.Wanted(-1, -1, 5)
	if len(a) != 5 || len(b) != 5 {
		t.Fatalf("limit not honoured: %d %d", len(a), len(b))
	}
	seen := map[ChunkRef]bool{}
	for _, r := range append(a, b...) {
		if seen[r] {
			t.Fatalf("chunk %v handed to two peers within the cooldown", r)
		}
		seen[r] = true
	}
	// After the cooldown, chunks that never arrived are handed out again.
	clk.advance(tune.AssignCooldown + time.Millisecond)
	again := c.Wanted(-1, -1, 1000)
	if len(again) != 3*npp {
		t.Fatalf("re-request after cooldown: %d chunks", len(again))
	}
}

func TestCoordinatorDoesNotRequestHeldChunks(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth, tune.RequestAhead = 0, 0
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 50)
	c.Sink(50, 0, tsChunk(50, 0))
	c.Sink(50, 2, tsChunk(50, 2))
	got := c.Wanted(-1, -1, 100)
	if len(got) != npp-2 || got[0] != (ChunkRef{50, 1}) || got[1] != (ChunkRef{50, 3}) {
		t.Fatalf("%v", got)
	}
}

func TestCoordinatorWritesContinuousTS(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth = 0
	c, out, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 10)
	for p := int64(10); p < 30; p++ {
		// Chunks arrive out of order, as they do from several peers.
		for _, ci := range []int{3, 1, 0, 7, 2, 6, 4, 5} {
			c.Sink(p, ci, tsChunk(p, ci))
			c.NoteEdge("peer", p+1)
		}
	}
	b := out.Bytes()
	if len(b) != 20*npp*TSPacket {
		t.Fatalf("wrote %d bytes", len(b))
	}
	for i := 0; i < len(b)/TSPacket; i++ {
		pkt := b[i*TSPacket:]
		if pkt[0] != TSSync || int64(pkt[1]) != 10+int64(i/npp) || int(pkt[2]) != i%npp {
			t.Fatalf("packet %d out of order: piece %d chunk %d", i, pkt[1], pkt[2])
		}
	}
	if s := c.Stats(); s.BytesWritten != int64(len(b)) || s.Skips != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestCoordinatorTrimsLeadInToFirstSync(t *testing.T) {
	var out bytes.Buffer
	// One piece = 3 equal chunks: a 5-byte lead-in, 9 whole packets, 1 pad byte.
	ts := append(append([]byte{1, 2, 3, 4, 5}, tsPackets(9)...), 0)
	third := len(ts) / 3
	c := NewCoordinator(func(p []byte) { out.Write(p) }, len(ts), third, 0, Tuning{BufferDepth: 0})
	c.NoteEdge("peer", 0)
	for i := 0; i < 3; i++ {
		c.Sink(0, i, ts[i*third:(i+1)*third])
	}
	if b := out.Bytes(); len(b) != 9*TSPacket+1 || b[0] != TSSync {
		t.Fatalf("lead-in not trimmed: %d bytes, first %#x", len(b), b[0])
	}
}

func TestCoordinatorResyncsWhenTooFarBehind(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth, tune.MaxLag = 2, 10
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 100)
	sinkPiece(c, 98)
	if front, _ := c.reasm.Front(); front != 99 {
		t.Fatalf("front %d", front)
	}
	c.NoteEdge("peer", 150) // the live edge ran away from us
	c.Sink(149, 0, tsChunk(149, 0))
	if front, _ := c.reasm.Front(); front != 148 {
		t.Fatalf("did not jump to edge-BufferDepth: front %d", front)
	}
	if c.Stats().Skips == 0 {
		t.Fatal("resync not counted")
	}
}

func TestCoordinatorLyingPeerCannotMoveStart(t *testing.T) {
	tune := DefaultTuning()
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("honest-a", 1000)
	c.NoteEdge("liar", 1<<50)
	c.NoteEdge("honest-b", 1001)
	// Playback starts behind what two peers agree on, not behind the lie.
	if got := c.Wanted(-1, -1, 1); len(got) != 1 || got[0].Piece != 1001-int64(tune.BufferDepth) {
		t.Fatalf("%v", got)
	}
	// Chunks of far-future pieces are not stored.
	c.Sink(1<<49, 0, tsChunk(0, 0))
	if c.reasm.Pending() != 0 {
		t.Fatal("kept a chunk far past the request window")
	}
}

// Measured live: the seeder keeps sending HAVEs while the other peer only
// stated max_piece once, at the handshake. The stream must keep flowing.
func TestCoordinatorKeepsFlowingWithOneAdvancingPeer(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth = 2
	c, out, _ := testCoordinator(t, tune)
	c.NoteEdge("stale", 100)
	c.NoteEdge("seeder", 100)
	for p := int64(98); p < 160; p++ {
		c.NoteEdge("seeder", p+1)
		got := c.Wanted(-1, p+1, 1000)
		if len(got) == 0 {
			t.Fatalf("nothing to request at piece %d: stalled", p)
		}
		sinkPiece(c, p)
	}
	if n := out.Len() / (npp * TSPacket); n < 60 {
		t.Fatalf("only %d pieces written", n)
	}
}

func TestCoordinatorRejectsWrongSizeChunks(t *testing.T) {
	c, _, _ := testCoordinator(t, DefaultTuning())
	c.NoteEdge("peer", 10)
	if c.Sink(10, 0, make([]byte, 1<<20)) {
		t.Fatal("an oversized chunk was accepted")
	}
	if c.Sink(10, 0, make([]byte, 10)) {
		t.Fatal("an undersized chunk was accepted")
	}
	if !c.Sink(10, 0, tsChunk(10, 0)) {
		t.Fatal("a proper chunk was refused")
	}
}

func TestCoordinatorForgetsGonePeers(t *testing.T) {
	c, _, _ := testCoordinator(t, DefaultTuning())
	c.NoteEdge("a", 500)
	c.Forget("a")
	c.NoteEdge("b", 400)
	if c.Stats().Edge != 500 {
		t.Fatal("the edge went backwards")
	}
}

func TestCoordinatorConcurrentSinks(t *testing.T) {
	tune := DefaultTuning()
	tune.BufferDepth = 0
	c, out, _ := testCoordinator(t, tune)
	c.NoteEdge("peer", 0)
	var wg sync.WaitGroup
	for ci := 0; ci < npp; ci++ {
		wg.Add(1)
		go func(ci int) {
			defer wg.Done()
			for p := int64(0); p < 200; p++ {
				c.NoteEdge("peer", p)
				c.Sink(p, ci, tsChunk(p, ci))
				c.Wanted(-1, -1, 16)
			}
		}(ci)
	}
	wg.Wait()
	if n, bad := ValidateTS(out.Bytes(), 0); bad != 0 || n == 0 {
		t.Fatalf("packets %d bad %d", n, bad)
	}
}

func TestCoordinatorStartAtContinuesFromAPiece(t *testing.T) {
	tune := DefaultTuning()
	c, _, _ := testCoordinator(t, tune)
	c.StartAt(995)
	c.NoteEdge("peer", 1000)
	if got := c.Wanted(-1, -1, 1); len(got) != 1 || got[0].Piece != 995 {
		t.Fatalf("%v, want to continue from 995 rather than edge-BufferDepth", got)
	}
}

// Two pieces nobody holds any more: the skip needs SkipAfter complete pieces
// past them, so the request window must reach that far.
func TestCoordinatorSkipsTwoStuckPieces(t *testing.T) {
	tune := DefaultTuning()
	c, out, _ := testCoordinator(t, tune)
	c.NoteEdge("a", 1000)
	c.NoteEdge("b", 1000)
	front := 1000 - int64(tune.BufferDepth)
	sinkPiece(c, front) // playback starts
	for n := 0; n < 40; n++ {
		c.NoteEdge("a", 1000+int64(n))
		c.NoteEdge("b", 1000+int64(n))
		for _, ref := range c.Wanted(-1, -1, 10000) {
			if ref.Piece != front+1 && ref.Piece != front+2 { // these two never arrive
				c.Sink(ref.Piece, ref.Chunk, tsChunk(ref.Piece, ref.Chunk))
			}
		}
		if f, _ := c.reasm.Front(); f > front+2 {
			if out.Len() == 0 {
				t.Fatal("nothing written")
			}
			return
		}
	}
	f, _ := c.reasm.Front()
	t.Fatalf("front stuck at %d (started %d) with two missing pieces", f, front)
}

// After every session dropped, the first peer back is the only edge source: a
// lie from it must not pin playback on pieces nobody holds.
func TestCoordinatorLonePeerCannotThrowEdgeFarAhead(t *testing.T) {
	tune := DefaultTuning()
	c, _, _ := testCoordinator(t, tune)
	c.NoteEdge("a", 1000)
	c.NoteEdge("b", 1001)
	c.Forget("a")
	c.Forget("b")
	c.NoteEdge("liar", 1<<50)
	if c.trusted != 1000 {
		t.Fatalf("trusted %d after a lone peer's far claim, want 1000", c.trusted)
	}
	c.Forget("liar")
	c.NoteEdge("honest", 1010) // a lone peer still moves it on
	if c.trusted != 1010 {
		t.Fatalf("trusted %d, want 1010", c.trusted)
	}
}

// A chunk outside the window is not stored, so it is not passed on either.
func TestTakeKeepsOnlyStoredChunks(t *testing.T) {
	c, _, _ := testCoordinator(t, DefaultTuning())
	c.NoteEdge("a", 1000)
	c.NoteEdge("b", 1000)
	for ci := 0; ci < npp; ci++ {
		if !c.Take("p", Piece{Index: 5000, Chunk: ci, Data: tsChunk(5000, ci)}) {
			t.Fatal("a right-sized chunk is not a reason to drop the peer")
		}
		c.Take("p", Piece{Index: 995, Chunk: ci, Data: tsChunk(995, ci)})
	}
	if _, ok := c.Serve(5000, 0); ok {
		t.Fatal("serves a piece past the edge")
	}
	if _, ok := c.Serve(995, 0); !ok {
		t.Fatal("does not serve a piece it played")
	}
}

func TestDescriptorCheck(t *testing.T) {
	ok := Descriptor{Trackers: []string{"udp://t:1"}, PieceLength: 1 << 20, ChunkLength: 1 << 14}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]Descriptor{
		"no chunk length": {Trackers: ok.Trackers, PieceLength: 1 << 20},
		"tiny chunks":     {Trackers: ok.Trackers, PieceLength: 16 << 20, ChunkLength: 1},
		"no trackers":     {PieceLength: 1 << 20, ChunkLength: 1 << 14},
	} {
		if d.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A piece that fails its signature is not played or passed on. It is fetched
// again from one peer alone, and the peer that then sends a bad one is the
// liar: only that one is dropped.
func TestBadPieceIsRefetchedFromOnePeer(t *testing.T) {
	tune := DefaultTuning()
	c, out, _ := testCoordinator(t, tune)
	c.reasm.check = func(p []byte) bool { return !bytes.Contains(p, []byte("LIE")) }
	c.NoteEdge("liar", 1000)
	c.NoteEdge("honest", 1000)
	pi := 1000 - int64(tune.BufferDepth) // where playback starts
	lie := func(ci int) Piece {
		b := tsChunk(pi, ci)
		copy(b[10:], "LIE")
		return Piece{Index: pi, Chunk: ci, Data: b}
	}
	good := func(ci int) Piece { return Piece{Index: pi, Chunk: ci, Data: tsChunk(pi, ci)} }

	if !c.Take("liar", lie(0)) {
		t.Fatal("dropped before the piece was whole")
	}
	for ci := 1; ci < npp; ci++ {
		if !c.Take("honest", good(ci)) {
			t.Fatal("dropped a peer that cannot be told from the liar")
		}
	}
	if _, ok := c.Serve(pi, 1); ok || out.Len() != 0 {
		t.Fatalf("a bad piece was played (%d bytes) or is passed on (%v)", out.Len(), ok)
	}

	if got := c.WantedBy("liar", -1, -1, npp); len(got) != npp || got[0].Piece != pi {
		t.Fatalf("the first to ask gets %v, want all of piece %d", got, pi)
	}
	for _, r := range c.WantedBy("honest", -1, -1, 1000) {
		if r.Piece == pi {
			t.Fatal("the piece is asked of two peers again")
		}
	}
	c.Take("honest", good(0)) // late, from the first round: not mixed in
	for ci := 0; ci < npp; ci++ {
		if ok := c.Take("liar", lie(ci)); ok != (ci < npp-1) {
			t.Fatalf("chunk %d from the liar: keep the peer = %v", ci, ok)
		}
	}

	if got := c.WantedBy("honest", -1, -1, npp); len(got) != npp || got[0].Piece != pi {
		t.Fatalf("after the liar, the next peer gets %v", got)
	}
	for ci := 0; ci < npp; ci++ {
		if !c.Take("honest", good(ci)) {
			t.Fatal("dropped the honest peer")
		}
	}
	st := c.Stats()
	if out.Len() != npp*TSPacket || st.Rejected != 2 || st.Played != pi+1 {
		t.Fatalf("wrote %d bytes, stats %+v", out.Len(), st)
	}
	if _, ok := c.Serve(pi, 0); !ok {
		t.Fatal("the good piece is not passed on")
	}
}
