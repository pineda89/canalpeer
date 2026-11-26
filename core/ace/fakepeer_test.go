package ace

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// liveSource is a synthetic live channel: one continuous TS stream cut into
// pieces of pieceLen-96 content bytes, each followed by a 96-byte "signature",
// exactly like AceStream pieces. Pieces are not aligned to TS packets.
type liveSource struct {
	pieceLen, chunkLen int
	first              int64 // oldest piece any peer holds
	edge0              int64 // live edge when the test starts
	every              time.Duration
	started            time.Time
	stream             []byte
}

func newLiveSource(pieceLen, chunkLen, pieces int) *liveSource {
	content := pieceLen - PieceSigLen
	n := (pieces*content)/TSPacket + 1
	stream := make([]byte, n*TSPacket)
	for i := 0; i < n; i++ {
		p := stream[i*TSPacket:]
		p[0] = TSSync
		binary.BigEndian.PutUint32(p[4:], uint32(i))
		for j := 8; j < TSPacket; j++ {
			p[j] = byte(i*7 + j)
		}
	}
	return &liveSource{
		pieceLen: pieceLen, chunkLen: chunkLen,
		first: 5000, edge0: 5040, every: 20 * time.Millisecond,
		started: time.Now(), stream: stream,
	}
}

func (s *liveSource) edge() int64 {
	e := s.edge0 + int64(time.Since(s.started)/s.every)
	if last := s.first + int64(len(s.stream)/(s.pieceLen-PieceSigLen)) - 1; e > last {
		e = last
	}
	return e
}

func (s *liveSource) chunk(piece int64, chunk int) []byte {
	content := s.pieceLen - PieceSigLen
	off := int(piece-s.first) * content
	full := make([]byte, 0, s.pieceLen)
	full = append(full, s.stream[off:off+content]...)
	full = append(full, bytes.Repeat([]byte{0xEE}, PieceSigLen)...)
	return full[chunk*s.chunkLen : (chunk+1)*s.chunkLen]
}

// assertContiguous checks out is an exact run of the source stream starting on
// a TS packet boundary: nothing rewound, duplicated, reordered or corrupted.
func (s *liveSource) assertContiguous(t *testing.T, out []byte, minBytes int) {
	t.Helper()
	if len(out) < minBytes {
		t.Fatalf("only %d TS bytes, want >= %d", len(out), minBytes)
	}
	if out[0] != TSSync {
		t.Fatal("output does not start on a TS packet")
	}
	at := int(binary.BigEndian.Uint32(out[4:])) * TSPacket
	if at+len(out) > len(s.stream) || !bytes.Equal(out, s.stream[at:at+len(out)]) {
		for i := 0; i < len(out) && at+i < len(s.stream); i++ {
			if out[i] != s.stream[at+i] {
				t.Fatalf("output diverges from the source at byte %d (of %d)", i, len(out))
			}
		}
		t.Fatal("output runs past the source")
	}
}

type fakePeerOpts struct {
	infohash    [20]byte
	startChoked time.Duration // stay choked this long after the handshake
	dropFirst   bool          // ignore the first request for some chunks
}

type fakePeer struct {
	ln            net.Listener
	src           *liveSource
	opts          fakePeerOpts
	requests      atomic.Int64
	choked        atomic.Bool
	whileChoked   atomic.Int64
	gotExtHello   atomic.Bool
	mu            sync.Mutex
	seenOnce      map[[2]int64]bool
	remoteHellos  [][]byte
	servedChunks  atomic.Int64
	connectionsMu sync.Mutex
	conns         []net.Conn
}

func startFakePeer(t *testing.T, src *liveSource, opts fakePeerOpts) *fakePeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakePeer{ln: ln, src: src, opts: opts, seenOnce: map[[2]int64]bool{}}
	t.Cleanup(func() {
		ln.Close()
		fp.connectionsMu.Lock()
		for _, c := range fp.conns {
			c.Close()
		}
		fp.connectionsMu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fp.connectionsMu.Lock()
			fp.conns = append(fp.conns, c)
			fp.connectionsMu.Unlock()
			go fp.serve(c)
		}
	}()
	return fp
}

func (f *fakePeer) addr() netip.AddrPort { return netip.MustParseAddrPort(f.ln.Addr().String()) }

func (f *fakePeer) serve(c net.Conn) {
	defer c.Close()
	var wmu sync.Mutex
	send := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := c.Write(b)
		return err
	}
	hs := make([]byte, HandshakeLen)
	if _, err := io.ReadFull(c, hs); err != nil {
		return
	}
	send(BuildHandshake(f.opts.infohash, NewPeerID()))
	ext, _ := Encode(map[string]any{"mi": map[string]any{"min_piece": f.src.first, "max_piece": f.src.edge()}})
	send(BuildMessage(MsgExtended, append([]byte{0}, ext...)))
	f.choked.Store(true)
	go func() {
		time.Sleep(f.opts.startChoked)
		f.choked.Store(false)
		send(BuildMessage(MsgUnchoke, nil))
		last := int64(-1)
		for {
			if e := f.src.edge(); e != last {
				last = e
				var have [8]byte
				binary.BigEndian.PutUint64(have[:], uint64(e))
				if send(BuildMessage(MsgHave, have[:])) != nil {
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	for {
		id, payload, err := ReadMessage(c, MaxMessageLen)
		if err != nil {
			return
		}
		switch id {
		case MsgExtended:
			f.gotExtHello.Store(true)
			f.mu.Lock()
			f.remoteHellos = append(f.remoteHellos, payload)
			f.mu.Unlock()
		case MsgRequest:
			f.requests.Add(1)
			if f.choked.Load() {
				f.whileChoked.Add(1)
				continue
			}
			pi := int64(binary.BigEndian.Uint64(payload[0:8]))
			ci := int(binary.BigEndian.Uint16(payload[8:10]))
			if pi < f.src.first || pi > f.src.edge() || ci >= f.src.pieceLen/f.src.chunkLen {
				continue
			}
			if f.opts.dropFirst && (pi+int64(ci))%5 == 0 {
				f.mu.Lock()
				first := !f.seenOnce[[2]int64{pi, int64(ci)}]
				f.seenOnce[[2]int64{pi, int64(ci)}] = true
				f.mu.Unlock()
				if first {
					continue // lost: the client must ask again
				}
			}
			msg := binary.BigEndian.AppendUint64(nil, uint64(pi))
			msg = binary.BigEndian.AppendUint64(msg, math.Float64bits(float64(time.Now().UnixNano())/1e9))
			msg = binary.BigEndian.AppendUint16(msg, uint16(ci))
			msg = append(msg, f.src.chunk(pi, ci)...)
			if send(BuildMessage(MsgPiece, msg)) != nil {
				return
			}
			f.servedChunks.Add(1)
		}
	}
}

// collector gathers the TS a coordinator writes.
type collector struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *collector) write(p []byte) { c.mu.Lock(); c.buf.Write(p); c.mu.Unlock() }

func (c *collector) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf.Bytes()...)
}

func (c *collector) waitFor(t *testing.T, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.buf.Len()
		c.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d TS bytes", n)
}

type leechOpts struct {
	src         *liveSource // set: unchoke us and advertise its live window
	autoRequest bool        // request every chunk of each piece we announce
}

// leech is a fake peer that downloads from us. It records every frame we send
// and stops at the first one that is not a well-formed message (or at the
// connection closing), which err reports.
type leech struct {
	ln     net.Listener
	opts   leechOpts
	wmu    sync.Mutex
	mu     sync.Mutex
	conn   net.Conn
	frames []leechFrame
	bad    error
}

type leechFrame struct {
	id      int
	payload []byte
}

func startLeech(t *testing.T, opts leechOpts) *leech {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &leech{ln: ln, opts: opts}
	t.Cleanup(func() {
		ln.Close()
		l.mu.Lock()
		if l.conn != nil {
			l.conn.Close()
		}
		l.mu.Unlock()
	})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		hs := make([]byte, HandshakeLen)
		if _, err := io.ReadFull(c, hs); err != nil {
			c.Close()
			return
		}
		c.Write(BuildHandshake(testIH, NewPeerID()))
		if s := opts.src; s != nil {
			ext, _ := Encode(map[string]any{"mi": map[string]any{"min_piece": s.first, "max_piece": s.edge()}})
			c.Write(append(BuildMessage(MsgExtended, append([]byte{0}, ext...)), BuildMessage(MsgUnchoke, nil)...))
		}
		l.mu.Lock()
		l.conn = c
		l.mu.Unlock()
		if s := opts.src; s != nil {
			go func() { // keep the window current, like a seeder
				for last := int64(-1); ; time.Sleep(5 * time.Millisecond) {
					if e := s.edge(); e != last {
						last = e
						if l.write(BuildMessage(MsgHave, binary.BigEndian.AppendUint64(nil, uint64(e)))) != nil {
							return
						}
					}
				}
			}()
		}
		l.read(c)
	}()
	return l
}

func (l *leech) addr() netip.AddrPort { return netip.MustParseAddrPort(l.ln.Addr().String()) }

func (l *leech) read(c net.Conn) {
	for {
		id, p, err := ReadMessage(c, MaxMessageLen)
		if err == nil && !wellFormed(id, p) {
			err = fmt.Errorf("malformed frame id %d, %d bytes", id, len(p))
		}
		l.mu.Lock()
		if err != nil {
			l.bad = err
		} else {
			l.frames = append(l.frames, leechFrame{id, p})
		}
		l.mu.Unlock()
		if err != nil {
			return
		}
		if id == MsgHave && l.opts.autoRequest {
			n, _ := ParseHave(p)
			for ci := range testPieceLen / testChunkLen {
				l.write(BuildRequest(n, ci))
			}
		}
	}
}

func wellFormed(id int, p []byte) bool {
	switch id {
	case MsgKeepAlive, MsgChoke, MsgUnchoke, MsgInterested, MsgNotInterested:
		return len(p) == 0
	case MsgHave:
		return len(p) == 8
	case MsgRequest:
		return len(p) == 10
	case MsgPiece:
		return len(p) == 18+testChunkLen
	case MsgExtended:
		return len(p) > 0
	}
	return false
}

func (l *leech) write(b []byte) error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	c := l.conn
	l.mu.Unlock()
	_, err := c.Write(b)
	return err
}

// send writes a frame once the swarm has connected.
func (l *leech) send(t *testing.T, b []byte) {
	t.Helper()
	l.waitFor(t, "connection", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.conn != nil
	})
	if err := l.write(b); err != nil {
		t.Fatal(err)
	}
}

func (l *leech) err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bad
}

// received returns the payloads of the frames with this id, in order.
func (l *leech) received(id int) [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out [][]byte
	for _, f := range l.frames {
		if f.id == id {
			out = append(out, f.payload)
		}
	}
	return out
}

// waitFor polls cond, failing on timeout or as soon as the stream breaks.
func (l *leech) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if err := l.err(); err != nil {
			t.Fatalf("waiting for %s: %v", what, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s (pieces %d haves %d requests %d)", what, len(l.received(MsgPiece)), len(l.received(MsgHave)), len(l.received(MsgRequest)))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
