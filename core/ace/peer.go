package ace

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// NewPeerID returns an AceStream live peer id: "R30" + 17 random bytes.
func NewPeerID() [20]byte {
	var id [20]byte
	copy(id[:], "R30")
	rand.Read(id[3:])
	return id
}

type PeerConfig struct {
	InfoHash       [20]byte
	PeerID         [20]byte
	ExtHandshake   func(peerIP net.IP) ([]byte, error) // EXTENDED payload, signed per peer
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration // the peer is dropped after this long silent
	RequestEvery   time.Duration
	ChokedPoll     time.Duration
	RequestBatch   int
	// OnHandshake, when set, runs once the peer has completed the handshake.
	OnHandshake func()
	// OnMessage, when set, sees every frame the peer sends (diagnostics).
	OnMessage func(peer netip.AddrPort, id int, payload []byte)
}

func (c *PeerConfig) defaults() {
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 6 * time.Second
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.RequestEvery == 0 {
		c.RequestEvery = 400 * time.Millisecond
	}
	if c.ChokedPoll == 0 {
		c.ChokedPoll = 300 * time.Millisecond
	}
	if c.RequestBatch == 0 {
		c.RequestBatch = 128
	}
}

type PeerStats struct {
	Handshook bool  // base handshake completed
	Engaged   bool  // sent at least one post-handshake frame
	Chunks    int64 // PIECE messages received
}

// RunPeer connects to one peer and, until ctx ends or the peer goes away,
// requests the chunks the coordinator hands it (only while unchoked, and only
// for pieces it holds) and feeds received chunks back into the coordinator.
func RunPeer(ctx context.Context, d Dialer, addr netip.AddrPort, cfg PeerConfig, coord *Coordinator) (PeerStats, error) {
	cfg.defaults()
	var stats PeerStats
	dctx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	conn, err := d.DialContext(dctx, "tcp", addr.String())
	cancel()
	if err != nil {
		return stats, err
	}
	defer conn.Close()
	parent := ctx
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	raw := conn // conn is replaced below while this may run
	context.AfterFunc(ctx, func() { raw.Close() })

	conn.SetDeadline(time.Now().Add(cfg.ConnectTimeout))
	if _, err := conn.Write(BuildHandshake(cfg.InfoHash, cfg.PeerID)); err != nil {
		return stats, err
	}
	hsBuf := make([]byte, HandshakeLen)
	if _, err := io.ReadFull(conn, hsBuf); err != nil {
		return stats, fmt.Errorf("handshake: %w", err)
	}
	hs, err := ParseHandshake(hsBuf)
	if err != nil {
		return stats, err
	}
	if hs.Protocol != PSTR {
		return stats, fmt.Errorf("bad protocol string %q", hs.Protocol)
	}
	if hs.InfoHash != cfg.InfoHash {
		return stats, fmt.Errorf("infohash mismatch %x", hs.InfoHash)
	}
	stats.Handshook = true
	if cfg.OnHandshake != nil {
		cfg.OnHandshake()
	}
	ext, err := cfg.ExtHandshake(net.IP(addr.Addr().AsSlice()))
	if err != nil {
		return stats, err
	}
	hello := append(BuildMessage(MsgExtended, ext), BuildMessage(MsgInterested, nil)...)
	if _, err := conn.Write(hello); err != nil {
		return stats, err
	}
	conn.SetDeadline(time.Time{})
	conn = &lockedConn{Conn: conn, timeout: cfg.ReadTimeout}

	p := &peerState{min: -1, max: -1, uploads: make(chan ChunkRef, uploadQueue), unchoke: make(chan struct{}, 1)}
	p.choked.Store(true)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		p.uploadLoop(ctx, conn, coord)
	}()
	go func() {
		defer wg.Done()
		p.requestLoop(ctx, addr.String(), conn, cfg, coord)
	}()
	go func() {
		defer wg.Done()
		haveLoop(ctx, conn, coord)
	}()
	err = p.readLoop(addr, conn, cfg, coord, &stats)
	coord.Forget(addr.String())
	stop()
	wg.Wait()
	if parent.Err() != nil {
		err = nil // we hung up, not the peer
	}
	return stats, err
}

// uploadQueue bounds the REQUESTs waiting to be answered, per peer: past it
// they are dropped (the peer asks again), so a slow uplink never holds back
// reading what that peer sends us.
const uploadQueue = 32

type peerState struct {
	uploads  chan ChunkRef // REQUESTs read, waiting for uploadLoop
	unchoke  chan struct{} // an INTERESTED read, waiting for uploadLoop's UNCHOKE
	choked   atomic.Bool
	mu       sync.Mutex
	min, max int64 // the pieces this peer holds, from its ext-handshake and HAVEs
}

func (p *peerState) window() (int64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.min, p.max
}

func (p *peerState) requestLoop(ctx context.Context, src string, conn net.Conn, cfg PeerConfig, coord *Coordinator) {
	for {
		wait := cfg.RequestEvery
		if p.choked.Load() { // a choked peer ignores REQUESTs
			wait = cfg.ChokedPoll
		} else {
			lo, hi := p.window()
			var batch []byte
			for _, r := range coord.WantedBy(src, lo, hi, cfg.RequestBatch) {
				batch = append(batch, BuildRequest(r.Piece, r.Chunk)...)
			}
			if len(batch) > 0 {
				if _, err := conn.Write(batch); err != nil {
					conn.Close()
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// uploadLoop answers what the read loop queued: UNCHOKE for INTERESTED, a
// PIECE for each REQUEST.
func (p *peerState) uploadLoop(ctx context.Context, conn net.Conn, coord *Coordinator) {
	for {
		var r ChunkRef
		select {
		case <-ctx.Done():
			return
		case <-p.unchoke:
			if _, err := conn.Write(BuildMessage(MsgUnchoke, nil)); err != nil {
				conn.Close()
				return
			}
			continue
		case r = <-p.uploads:
		}
		body, ok := coord.Serve(r.Piece, r.Chunk) // evicted meanwhile: not answered
		if !ok {
			continue
		}
		if _, err := conn.Write(BuildMessage(MsgPiece, body)); err != nil {
			conn.Close()
			return
		}
		coord.uploaded.Add(int64(len(body)))
	}
}

// haveLoop tells the peer about every piece we complete from now on, so it
// can request it from us.
func haveLoop(ctx context.Context, conn net.Conn, coord *Coordinator) {
	_, n := coord.DoneSince(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
		var news []int64
		news, n = coord.DoneSince(n)
		var batch []byte
		for _, pi := range news {
			batch = append(batch, BuildMessage(MsgHave, binary.BigEndian.AppendUint64(nil, uint64(pi)))...)
		}
		if len(batch) > 0 {
			if _, err := conn.Write(batch); err != nil {
				conn.Close()
				return
			}
		}
	}
}

func (p *peerState) readLoop(addr netip.AddrPort, conn net.Conn, cfg PeerConfig, coord *Coordinator, stats *PeerStats) error {
	for {
		conn.SetReadDeadline(time.Now().Add(cfg.ReadTimeout))
		id, payload, err := ReadMessage(conn, MaxMessageLen)
		if err != nil {
			return err
		}
		stats.Engaged = true
		if cfg.OnMessage != nil {
			cfg.OnMessage(addr, id, payload)
		}
		switch id {
		case MsgInterested: // we serve every peer that asks: no choking policy
			select {
			case p.unchoke <- struct{}{}:
			default: // one is pending already
			}
		case MsgRequest: // piece u64be + chunk u16be; one we do not hold is ignored
			if len(payload) < 10 {
				continue
			}
			select { // never wait on our uplink here: a full queue drops it
			case p.uploads <- ChunkRef{Piece: int64(binary.BigEndian.Uint64(payload[0:8])), Chunk: int(binary.BigEndian.Uint16(payload[8:10]))}:
			default:
			}
		case MsgPiece:
			pc, err := ParsePiece(payload)
			if err != nil {
				return err
			}
			stats.Chunks++
			if !coord.Take(addr.String(), pc) {
				return fmt.Errorf("chunk of %d bytes, or piece %d with a bad signature", len(pc.Data), pc.Index)
			}
		case MsgUnchoke:
			p.choked.Store(false)
		case MsgChoke:
			p.choked.Store(true)
		case MsgHave: // the peer advertises a new live piece it holds
			pi, err := ParseHave(payload)
			if err != nil {
				return err
			}
			p.mu.Lock()
			p.max = max(p.max, pi)
			p.mu.Unlock()
			coord.NoteEdge(addr.String(), pi)
		case MsgExtended:
			if len(payload) > 0 && payload[0] == 0 {
				p.learnWindow(addr.String(), payload[1:], coord)
			}
			// ids 11/12/34/35/36 (stats, PEX, source push) are not needed for video.
		}
	}
}

// learnWindow reads mi.min_piece / mi.max_piece from the peer's ext-handshake.
func (p *peerState) learnWindow(src string, dict []byte, coord *Coordinator) {
	v, err := Decode(dict)
	if err != nil {
		return
	}
	d, _ := v.(map[string]any)
	mi, _ := d["mi"].(map[string]any)
	mn, okMin := dictInt(mi, "min_piece")
	mx, okMax := dictInt(mi, "max_piece")
	p.mu.Lock()
	if okMin {
		p.min = mn
	}
	if okMax && mx > p.max {
		p.max = mx
	}
	p.mu.Unlock()
	if okMax {
		coord.NoteEdge(src, mx)
	}
}
