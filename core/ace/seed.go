package ace

import (
	"encoding/binary"
	"math"
	"net"
	"sync"
	"time"
)

// seedCap bounds the raw chunks kept to serve other peers, per channel.
const seedCap = 32 << 20

// maxDone bounds the completion log the HAVE loops read.
const maxDone = 1024

// seedPiece is one piece's chunks exactly as received: timestamp and the
// signature trailer included, which the reassembler strips but peers verify.
type seedPiece struct {
	ts     float64
	chunks map[int][]byte
}

// seedStore keeps the most recent pieces received so they can be passed on.
type seedStore struct {
	mu       sync.Mutex
	npp      int
	pieces   map[int64]*seedPiece
	order    []int64 // pieces in arrival order, oldest first: the eviction queue
	bytes    int     // chunk bytes held, at most seedCap
	done     []int64 // completed pieces in completion order, the newest maxDone
	doneBase int     // completions dropped from the front of done
}

func newSeedStore(npp int) *seedStore {
	return &seedStore{npp: npp, pieces: map[int64]*seedPiece{}}
}

// keep stores one received chunk. The data is not copied: it is the frame
// ReadMessage allocated, shared with the reassembler, and nobody writes to it.
func (s *seedStore) keep(p Piece) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.pieces[p.Index]
	if sp == nil {
		sp = &seedPiece{ts: p.Timestamp, chunks: make(map[int][]byte, s.npp)}
		s.pieces[p.Index] = sp
		s.order = append(s.order, p.Index)
	}
	if _, dup := sp.chunks[p.Chunk]; dup {
		return
	}
	sp.chunks[p.Chunk] = p.Data
	s.bytes += len(p.Data)
	if len(sp.chunks) == s.npp {
		s.done = append(s.done, p.Index)
		if len(s.done) > maxDone {
			s.done = s.done[1:]
			s.doneBase++
		}
	}
	for s.bytes > seedCap {
		old := s.order[0]
		s.order = s.order[1:]
		s.dropLocked(old)
	}
}

// drop forgets a piece that failed its signature: it is never passed on. Its
// place in the eviction queue stays, and is skipped when it comes up.
func (s *seedStore) drop(piece int64) {
	s.mu.Lock()
	s.dropLocked(piece)
	s.mu.Unlock()
}

func (s *seedStore) dropLocked(piece int64) {
	if sp := s.pieces[piece]; sp != nil {
		for _, d := range sp.chunks {
			s.bytes -= len(d)
		}
		delete(s.pieces, piece)
	}
}

// serve returns the PIECE payload for one chunk of a complete piece:
// piece u64be | timestamp f64be | chunk u16be | data, as it was received.
func (s *seedStore) serve(piece int64, chunk int) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := s.pieces[piece]
	if sp == nil || len(sp.chunks) != s.npp {
		return nil, false
	}
	d, ok := sp.chunks[chunk]
	if !ok {
		return nil, false
	}
	b := make([]byte, 0, 18+len(d))
	b = binary.BigEndian.AppendUint64(b, uint64(piece))
	b = binary.BigEndian.AppendUint64(b, math.Float64bits(sp.ts))
	b = binary.BigEndian.AppendUint16(b, uint16(chunk))
	return append(b, d...), true
}

// doneSince returns the pieces completed after the first i completions, and
// the count to pass next time.
func (s *seedStore) doneSince(i int) ([]int64, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i = max(i-s.doneBase, 0)
	return append([]int64(nil), s.done[i:]...), s.doneBase + len(s.done)
}

// Keep stores a received chunk to pass on. Chunks no honest peer sends (wrong
// size or index) are not kept.
func (c *Coordinator) Keep(p Piece) {
	if len(p.Data) != c.chunkLen || p.Chunk < 0 || p.Chunk >= c.npp {
		return
	}
	c.seed.keep(p)
}

// Serve returns the PIECE payload for a chunk of a complete piece we hold.
func (c *Coordinator) Serve(piece int64, chunk int) ([]byte, bool) { return c.seed.serve(piece, chunk) }

// DoneSince returns the pieces completed after the first i, and the next i.
func (c *Coordinator) DoneSince(i int) ([]int64, int) { return c.seed.doneSince(i) }

// lockedConn serializes writes: the request loop, the HAVE loop and the read
// loop (answering REQUESTs) share one connection. Each write gets its own
// deadline, so a peer that stops reading is dropped instead of stalling us.
type lockedConn struct {
	net.Conn
	timeout time.Duration
	mu      sync.Mutex
}

func (c *lockedConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(b)
}
