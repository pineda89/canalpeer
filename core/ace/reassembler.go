package ace

// Reassembler turns chunks into pieces, strips each piece's signature trailer,
// and releases consecutive pieces in order, as one continuous MPEG-TS stream.
// The first TS sync may sit a few bytes in: the lead-in of a packet that began
// in an earlier, uncaptured piece.
//
// It is not safe for concurrent use; the Coordinator serializes access.
type Reassembler struct {
	sigLen  int
	nChunks int
	check   func(piece []byte) bool // nil: pieces are not verified
	pieces  map[int64]map[int][]byte
	done    map[int64][]byte
	next    int64
	started bool
}

func NewReassembler(pieceLen, chunkLen, sigLen int) *Reassembler {
	return &Reassembler{
		sigLen:  sigLen,
		nChunks: max(1, pieceLen/chunkLen),
		pieces:  map[int64]map[int][]byte{},
		done:    map[int64][]byte{},
	}
}

// SetStart pins the first piece to emit. It is ignored once playback began.
func (r *Reassembler) SetStart(piece int64) {
	if !r.started {
		r.next, r.started = piece, true
	}
}

// Front is the next piece to emit, and whether playback has a position yet.
func (r *Reassembler) Front() (int64, bool) { return r.next, r.started }

// ResyncTo jumps the playback front forward (we fell too far behind live),
// dropping everything older.
func (r *Reassembler) ResyncTo(piece int64) {
	if !r.started || piece <= r.next {
		return
	}
	r.next = piece
	for p := range r.pieces {
		if p < piece {
			delete(r.pieces, p)
		}
	}
	for p := range r.done {
		if p < piece {
			delete(r.done, p)
		}
	}
}

// Has reports whether the chunk is already held (or its piece is complete).
func (r *Reassembler) Has(piece int64, chunk int) bool {
	if _, ok := r.done[piece]; ok {
		return true
	}
	_, ok := r.pieces[piece][chunk]
	return ok
}

// Complete reports whether a piece is fully assembled and waiting to drain.
func (r *Reassembler) Complete(piece int64) bool {
	_, ok := r.done[piece]
	return ok
}

// Add stores one chunk and returns the piece bytes if it completed one. bad
// says it completed one that failed check: the whole piece is dropped, to be
// fetched again.
//
// Pieces below the playback front are dropped: a newly connected peer must
// never rewind the stream by pushing its older window (that caused a
// progressive "going back in time" drift). The front only moves forward.
func (r *Reassembler) Add(piece int64, chunk int, data []byte) (out []byte, bad bool) {
	if chunk < 0 || chunk >= r.nChunks {
		return nil, false
	}
	if _, ok := r.done[piece]; ok {
		return nil, false
	}
	if r.started && piece < r.next {
		return nil, false
	}
	if !r.started {
		r.next, r.started = piece, true
	}
	chunks := r.pieces[piece]
	if chunks == nil {
		chunks = make(map[int][]byte, r.nChunks)
		r.pieces[piece] = chunks
	}
	chunks[chunk] = data
	if len(chunks) < r.nChunks {
		return nil, false
	}
	size := 0
	for _, c := range chunks {
		size += len(c)
	}
	out = make([]byte, 0, size)
	for i := 0; i < r.nChunks; i++ {
		out = append(out, chunks[i]...)
	}
	delete(r.pieces, piece)
	if r.check != nil && !r.check(out) {
		return nil, true
	}
	if r.sigLen > 0 {
		out = out[:max(0, len(out)-r.sigLen)]
	}
	r.done[piece] = out
	return out, false
}

// Drop forgets the chunks held of a piece that is not complete.
func (r *Reassembler) Drop(piece int64) { delete(r.pieces, piece) }

// Drain returns the completed pieces that are next in order.
//
// With skipAfter > 0, when the next piece is missing but at least skipAfter
// later pieces are ready, it jumps over the gap: a live stream must keep
// flowing, so a missing piece becomes a brief glitch instead of a freeze.
func (r *Reassembler) Drain(skipAfter int) [][]byte {
	if !r.started {
		return nil
	}
	var out [][]byte
	for {
		if p, ok := r.done[r.next]; ok {
			out = append(out, p)
			delete(r.done, r.next)
			r.next++
			continue
		}
		if skipAfter > 0 && len(r.done) >= skipAfter {
			lowest, ahead := int64(-1), 0
			for p := range r.done {
				if p > r.next {
					ahead++
					if lowest < 0 || p < lowest {
						lowest = p
					}
				}
			}
			if ahead >= skipAfter {
				for p := range r.pieces {
					if p < lowest {
						delete(r.pieces, p)
					}
				}
				r.next = lowest
				continue
			}
		}
		return out
	}
}

// Pending is the number of partially received pieces.
func (r *Reassembler) Pending() int { return len(r.pieces) }
