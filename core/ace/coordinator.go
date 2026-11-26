package ace

import (
	"sync"
	"sync/atomic"
	"time"
)

// Tuning holds the live-window knobs, calibrated by hand on the Python client.
// The real network will move them, so they stay configurable.
type Tuning struct {
	BufferDepth    int           // pieces between the playback front and the live edge (latency vs smoothness)
	RequestAhead   int           // request missing chunks this many pieces ahead of the front
	SkipAfter      int           // skip a stuck piece once this many later pieces are ready
	MaxLag         int           // fallen this far behind live: jump forward to BufferDepth again
	AssignCooldown time.Duration // don't hand the same chunk to another peer within this
}

func DefaultTuning() Tuning {
	return Tuning{
		BufferDepth:    12,
		RequestAhead:   24, // past SkipAfter: the skip needs that many complete with some still missing
		SkipAfter:      16,
		MaxLag:         48,
		AssignCooldown: 1500 * time.Millisecond,
	}
}

// soloist is the one peer a piece is being fetched from again. Without one
// yet (at is zero) nobody's chunks of it are taken.
type soloist struct {
	src string
	at  time.Time // its last chunk, or when it was given the piece
}

// soloFor is how long a soloist may stay silent before another peer gets the
// piece.
const soloFor = 3 * time.Second

type ChunkRef struct {
	Piece int64
	Chunk int
}

// Coordinator tracks the live edge and the received chunks, tells peers which
// chunks to (re-)request, and writes the reassembled window out in order.
//
// Playing a few pieces BEHIND the live edge (BufferDepth) leaves time to fill
// every chunk of a piece before it is due, so pieces complete instead of
// leaving gaps. Peers re-request missing chunks until complete; only a truly
// stuck piece is skipped.
type Coordinator struct {
	out      func([]byte)
	tune     Tuning
	npp      int
	now      func() time.Time
	mu       sync.Mutex
	reasm    *Reassembler
	edge     int64 // highest piece any peer claims
	trusted  int64 // the edge two peers agree on (see NoteEdge)
	peerEdge map[string]int64
	chunkLen int
	synced   bool
	written  int64
	skipped  int64
	played   int64 // the piece after the last one written out
	rejected int64 // pieces that failed their signature
	// solo holds the pieces that failed their signature, each fetched again
	// from one peer alone: a second failure then names the liar.
	solo map[int64]*soloist
	// assigned records when each chunk was last handed to some peer, which is
	// how many peers calling Wanted split the window between them.
	assigned map[ChunkRef]time.Time

	seed     *seedStore   // the recent pieces, raw, to pass on
	uploaded atomic.Int64 // PIECE payload bytes sent to peers
}

// NewCoordinator writes in-order TS to out, starting at the first TS sync. out
// is called with the coordinator's lock held, so it must not block for long.
func NewCoordinator(out func([]byte), pieceLen, chunkLen, sigLen int, t Tuning) *Coordinator {
	return &Coordinator{
		out:      out,
		tune:     t,
		npp:      max(1, pieceLen/chunkLen),
		now:      time.Now,
		reasm:    NewReassembler(pieceLen, chunkLen, sigLen),
		edge:     -1,
		trusted:  -1,
		peerEdge: map[string]int64{},
		chunkLen: chunkLen,
		assigned: map[ChunkRef]time.Time{},
		solo:     map[int64]*soloist{},
		seed:     newSeedStore(max(1, pieceLen/chunkLen)),
	}
}

// StartAt pins playback to a piece, instead of BufferDepth behind the live
// edge: a successor continues where the previous ingestor was. If that turns
// out to be too far behind live, the MaxLag resync still applies.
func (c *Coordinator) StartAt(piece int64) {
	c.mu.Lock()
	c.reasm.SetStart(piece)
	c.mu.Unlock()
}

// NoteEdge records the newest piece a peer advertises (HAVE, ext-handshake
// max_piece). src identifies the peer.
//
// Two edges come out of it:
//   - edge, the highest anyone claims, bounds what is requested. That is safe
//     with a liar among the peers, because each peer is only asked for pieces
//     inside its own advertised window: a lie only misleads requests to the liar.
//   - trusted, the second-highest of the peers' claims, decides where playback
//     starts and where a too-far-behind stream jumps to: the two decisions one
//     lying peer could otherwise poison, freezing the channel on pieces nobody
//     holds. With a single peer there is nobody to confirm it, and it is trusted.
//
// Measured on a live channel: a "second-highest" edge alone stalls the stream
// when one peer leaves and the only one sending HAVEs is the seeder, since the
// others only state max_piece once, at the handshake.
func (c *Coordinator) NoteEdge(src string, piece int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, known := c.peerEdge[src]
	if piece < 0 || known && piece <= prev || !known && len(c.peerEdge) >= maxEdgeSources {
		return
	}
	c.peerEdge[src] = piece
	c.updateEdges()
}

// Forget drops a peer that went away from the edge vote.
func (c *Coordinator) Forget(src string) {
	c.mu.Lock()
	delete(c.peerEdge, src)
	c.mu.Unlock()
}

// maxLoneJump is how far a single unconfirmed peer may move the trusted edge
// at once: about a day of one-second pieces.
const maxLoneJump = 1 << 16

// maxEdgeSources bounds the vote's bookkeeping; MaxPeers is far below it.
const maxEdgeSources = 4096

func (c *Coordinator) updateEdges() {
	first, second := int64(-1), int64(-1)
	for _, v := range c.peerEdge {
		if v > first {
			first, second = v, first
		} else if v > second {
			second = v
		}
	}
	if len(c.peerEdge) == 1 {
		second = first
		// Nobody confirms a lone peer: it may move the edge on (far, after the
		// process slept), but not by more than a day of pieces, which would
		// pin playback for good on pieces nobody holds (and the next swarm
		// with it, through StartPiece).
		// shortcut: a lone liar within maxLoneJump still freezes this swarm;
		// the session replaces a swarm that stalls, and the next one starts
		// from what really played (Stats.Played), not from here.
		if c.trusted >= 0 && first > c.trusted+maxLoneJump {
			second = c.trusted
		}
	}
	// Both only move forward.
	c.trusted = max(c.trusted, second)
	c.edge = max(c.edge, first)
}

// Sink takes one received chunk. It returns false for a chunk no honest peer
// sends (wrong size), and the caller should drop that peer: holding oversized
// chunks of pieces that never complete is a memory exhaustion attack.
func (c *Coordinator) Sink(piece int64, chunk int, data []byte) bool {
	ok, _ := c.sink("", piece, chunk, data)
	return ok
}

// Take is Sink for a chunk from the peer src, plus Keep when the chunk was
// stored: one outside the window is never passed on, so a peer cannot fill
// the seed store with junk. It also returns false when src alone sent a piece
// that failed its signature.
func (c *Coordinator) Take(src string, p Piece) bool {
	ok, stored := c.sink(src, p.Index, p.Chunk, p.Data)
	if stored {
		c.Keep(p)
	}
	return ok
}

func (c *Coordinator) sink(src string, piece int64, chunk int, data []byte) (ok, stored bool) {
	if len(data) != c.chunkLen {
		return false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.edge < 0 || piece > c.edge {
		return true, false // nobody advertised it: unrequested, not stored
	}
	front, started := c.reasm.Front()
	switch {
	case !started:
		// pin playback a buffer's depth behind the live edge
		c.reasm.SetStart(max(0, c.trusted-int64(c.tune.BufferDepth)))
	case c.trusted-front > int64(c.tune.MaxLag):
		// drifted too far behind live: jump forward (stay current, one glitch)
		c.reasm.ResyncTo(max(0, c.trusted-int64(c.tune.BufferDepth)))
		c.skipped++
	}
	if front, _ := c.reasm.Front(); piece > front+2*int64(c.tune.RequestAhead) {
		return true, false // far past anything requested: an honest peer does not send it
	}
	solo := c.solo[piece]
	if solo != nil {
		if solo.at.IsZero() || solo.src != src {
			return true, false // asked of several peers before it failed: late
		}
		solo.at = c.now()
	}
	done, bad := c.reasm.Add(piece, chunk, data)
	if bad {
		c.rejected++
		c.seed.drop(piece)
		for p := range c.solo {
			if p < front {
				delete(c.solo, p)
			}
		}
		c.solo[piece] = &soloist{} // whoever asks next fetches all of it
		// Sent by several peers, any of them may have lied; sent by one, it did.
		return solo == nil, false
	}
	if done != nil {
		delete(c.solo, piece)
	}
	before, _ := c.reasm.Front()
	pieces := c.reasm.Drain(c.tune.SkipAfter)
	after, _ := c.reasm.Front()
	if after-before > int64(len(pieces)) {
		c.skipped++
	}
	if len(pieces) > 0 {
		c.played = after
	}
	for _, p := range pieces {
		if !c.synced {
			o := FindTSSync(p, 8)
			if o < 0 {
				continue // not enough bytes to lock sync yet
			}
			p = p[o:]
			c.synced = true
		}
		c.out(p)
		c.written += int64(len(p))
	}
	return true, true
}

// Wanted returns up to limit (piece, chunk) pairs still missing in the playback
// window, restricted to the pieces this peer holds ([peerMin, peerMax]; -1 means
// unbounded). Only the returned chunks are marked handed out, each to one peer
// per AssignCooldown, so many peers calling this split the window between them
// instead of all grabbing it.
func (c *Coordinator) Wanted(peerMin, peerMax int64, limit int) []ChunkRef {
	return c.WantedBy("", peerMin, peerMax, limit)
}

// WantedBy is Wanted for the peer src. A piece that failed its signature goes
// to one peer at a time, whole.
func (c *Coordinator) WantedBy(src string, peerMin, peerMax int64, limit int) []ChunkRef {
	c.mu.Lock()
	defer c.mu.Unlock()
	front, started := c.reasm.Front()
	if !started {
		if c.trusted < 0 {
			return nil
		}
		front = max(0, c.trusted-int64(c.tune.BufferDepth))
	}
	hi := min(c.edge, front+int64(c.tune.RequestAhead))
	if peerMax >= 0 {
		hi = min(hi, peerMax)
	}
	if peerMin >= 0 {
		front = max(front, peerMin)
	}
	now := c.now()
	var want []ChunkRef
	for pi := front; pi <= hi && len(want) < limit; pi++ {
		if c.reasm.Complete(pi) {
			continue
		}
		if solo := c.solo[pi]; solo != nil && solo.src != src || solo != nil && solo.at.IsZero() {
			if !solo.at.IsZero() && now.Sub(solo.at) < soloFor {
				continue
			}
			// src takes it over, from nothing: what is held came from another.
			c.reasm.Drop(pi)
			c.seed.drop(pi)
			for ci := 0; ci < c.npp; ci++ {
				delete(c.assigned, ChunkRef{pi, ci})
			}
			*solo = soloist{src: src, at: now}
		}
		for ci := 0; ci < c.npp && len(want) < limit; ci++ {
			ref := ChunkRef{pi, ci}
			if c.reasm.Has(pi, ci) {
				continue
			}
			if t, ok := c.assigned[ref]; ok && now.Sub(t) < c.tune.AssignCooldown {
				continue // recently handed to another peer; let it try
			}
			c.assigned[ref] = now
			want = append(want, ref)
		}
	}
	if len(c.assigned) > 100000 { // bound the bookkeeping
		for ref, t := range c.assigned {
			if now.Sub(t) > 5*time.Second {
				delete(c.assigned, ref)
			}
		}
	}
	return want
}

// Stats is a snapshot for status reporting.
type CoordinatorStats struct {
	Edge, Front  int64
	BytesWritten int64
	Skips        int64
	Played       int64 // the piece after the last one written out; 0 before any
	Rejected     int64 // pieces that failed their signature
	Pending      int
}

func (c *Coordinator) Stats() CoordinatorStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	front, _ := c.reasm.Front()
	return CoordinatorStats{Edge: c.edge, Front: front, BytesWritten: c.written, Skips: c.skipped, Played: c.played, Rejected: c.rejected, Pending: c.reasm.Pending()}
}
