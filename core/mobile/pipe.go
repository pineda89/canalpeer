package mobile

import (
	"io"
	"sync"
)

// pipeCap bounds what the pipe holds: what waits for the player (it grows while
// the viewer pauses) plus what was already played, kept so the viewer can go
// back. ~1 min at a typical 6 Mbit/s channel: the Fire TV has 1.7 GB of RAM
// and 128 MB here filled its swap (measured), lagging the whole system.
const pipeCap = 48 << 20

// pipe carries the TS from the swarm to one HTTP reader. Write never blocks:
// the swarm calls it with the coordinator lock held. Bytes are addressed by
// their absolute offset in the stream; when the pipe goes over capacity the
// oldest are dropped, played or not, and the TS extractor resyncs.
type pipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte // held bytes, oldest first
	start  int64    // offset of chunks[0][0]
	end    int64    // offset after the newest byte
	pos    int64    // next offset the reader gets
	// The player addresses bytes from base: its byte 0 (and its playback time 0)
	// is base. A seek starts a new base at the next reader.
	base   int64
	fresh  bool
	cap    int64
	gen    int // bumped by every new reader and seek; older readers see EOF
	closed bool
}

func newPipe(capBytes int) *pipe {
	p := &pipe{cap: int64(capBytes), fresh: true}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *pipe) Write(b []byte) {
	if len(b) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.chunks = append(p.chunks, append([]byte(nil), b...))
	p.end += int64(len(b))
	for p.end-p.start > p.cap {
		head := p.chunks[0]
		if over := p.end - p.start - p.cap; over < int64(len(head)) {
			p.chunks[0] = head[over:]
			p.start += over
			break
		}
		p.chunks[0] = nil // let the dropped chunk go
		p.chunks = p.chunks[1:]
		p.start += int64(len(head))
	}
	p.pos = max(p.pos, p.start)
	p.cond.Broadcast()
}

// reader replaces any previous reader and starts at the player's byte at,
// counted from base: a reconnecting player resumes exactly where it was, so the
// bytes the old connection had in flight are not lost.
func (p *pipe) reader(at int64) io.Reader {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fresh {
		p.base, p.fresh = p.pos, false
	}
	p.pos = min(max(p.base+at, p.start), p.end)
	p.gen++
	p.cond.Broadcast()
	return &pipeReader{p: p, gen: p.gen}
}

// livePos is where a new viewer joins: what the current reader gets next, or
// the newest byte if nobody has read yet.
func (p *pipe) livePos() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen == 0 {
		return p.end
	}
	return p.pos
}

// readerAt replaces any previous reader and starts at the absolute offset off,
// within what the pipe holds. Unlike reader it leaves base alone: the phone's
// player keeps its own byte origin.
func (p *pipe) readerAt(off int64) io.Reader {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pos = min(max(off, p.start), p.end)
	p.gen++
	p.cond.Broadcast()
	return &pipeReader{p: p, gen: p.gen}
}

// Len is how many bytes wait for the player.
func (p *pipe) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.end - p.pos)
}

// seek moves playback to shown+delta bytes past base (what is on screen plus
// delta), within what the pipe holds and never nearer the newest byte than
// minBehind (unless already nearer). It ends the current reader and returns
// how far behind the newest byte the next reader starts.
func (p *pipe) seek(shown, delta, minBehind int64) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.base + shown
	target := now + delta
	if delta > 0 {
		target = min(target, max(p.end-minBehind, now))
	}
	p.pos = min(max(target, p.start), p.end)
	p.base, p.fresh = p.pos, true
	p.gen++
	p.cond.Broadcast()
	return p.end - p.pos
}

func (p *pipe) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.chunks = nil // the Java side frees the Session only when its GC gets to it
	p.cond.Broadcast()
}

type pipeReader struct {
	p   *pipe
	gen int
}

func (r *pipeReader) Read(b []byte) (int, error) {
	p := r.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.pos == p.end && !p.closed && p.gen == r.gen {
		p.cond.Wait()
	}
	if p.closed || p.gen != r.gen {
		return 0, io.EOF
	}
	// ponytail: linear walk over held chunks (a few thousand at most);
	// index by offset if profiling ever shows it.
	off := p.start
	for _, c := range p.chunks {
		if p.pos < off+int64(len(c)) {
			n := copy(b, c[p.pos-off:])
			p.pos += int64(n)
			return n, nil
		}
		off += int64(len(c))
	}
	return 0, io.EOF // unreachable: pos < end lies inside a chunk
}
