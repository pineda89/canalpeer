package ace

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type SwarmConfig struct {
	Desc       Descriptor
	Dialer     Dialer
	Out        func([]byte) // continuous MPEG-TS; called with the coordinator lock held
	MaxPeers   int          // concurrent peer sessions (dials included)
	ListenPort uint16       // advertised; we never accept connections
	Tuning     Tuning
	Peer       PeerConfig // InfoHash, PeerID and ExtHandshake are filled in
	ExtraPeers []netip.AddrPort
	StartPiece int64 // > 0: continue from this piece instead of BufferDepth behind live
	Reannounce time.Duration
	Log        *slog.Logger
}

// Swarm joins one channel's swarm and turns it into a TS stream.
type Swarm struct {
	cfg   SwarmConfig
	coord *Coordinator

	dialed, handshook, engaged atomic.Int64
	active                     atomic.Int64
	connected                  atomic.Int64 // sessions past the handshake, now
}

const (
	maxTrackers = 32
	// retryPeer is how long a dialled peer waits before it is dialled again:
	// a seeder that dropped must come back while others keep the pool busy.
	retryPeer = 2 * time.Minute
)

func NewSwarm(cfg SwarmConfig) *Swarm {
	if cfg.MaxPeers == 0 {
		// Most advertised peers are stale or behind NAT (~98% measured), so the
		// few seeders are only found by dialling many.
		cfg.MaxPeers = 80
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8621
	}
	if cfg.Reannounce == 0 {
		cfg.Reannounce = 20 * time.Second
	}
	if cfg.Tuning == (Tuning{}) {
		cfg.Tuning = DefaultTuning()
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	// Each tracker costs a goroutine and a socket per announce, and the list
	// comes from the network.
	cfg.Desc.Trackers = cfg.Desc.Trackers[:min(len(cfg.Desc.Trackers), maxTrackers)]
	s := &Swarm{
		cfg:   cfg,
		coord: NewCoordinator(cfg.Out, int(cfg.Desc.PieceLength), int(cfg.Desc.ChunkLength), PieceSigLen, cfg.Tuning),
	}
	s.coord.reasm.check = pieceCheck(cfg.Desc.PubKey)
	return s
}

type SwarmStats struct {
	Dialed, Active, Handshook, Engaged int64
	Connected                          int64 // peers connected past the handshake right now
	Uploaded                           int64 // bytes served to peers
	CoordinatorStats
}

func (s *Swarm) Stats() SwarmStats {
	return SwarmStats{
		Dialed: s.dialed.Load(), Active: s.active.Load(),
		Handshook: s.handshook.Load(), Engaged: s.engaged.Load(), Connected: s.connected.Load(),
		Uploaded:         s.coord.uploaded.Load(),
		CoordinatorStats: s.coord.Stats(),
	}
}

// Run announces, keeps up to MaxPeers sessions going, re-announces
// periodically, and when the pool runs dry re-dials the whole swarm, like the
// engine does, until seeders answer. It returns when ctx ends.
func (s *Swarm) Run(ctx context.Context) error {
	if s.cfg.StartPiece > 0 {
		s.coord.StartAt(s.cfg.StartPiece)
	}
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	pc := s.cfg.Peer
	pc.InfoHash = s.cfg.Desc.InfoHash
	pc.PeerID = NewPeerID()
	pc.ExtHandshake = func(ip net.IP) ([]byte, error) {
		return BuildExtHandshake(int(s.cfg.ListenPort), ip, key)
	}

	var (
		mu     sync.Mutex
		tried  = map[netip.AddrPort]time.Time{} // when each peer was last dialled
		queued = map[netip.AddrPort]bool{}
		queue  []netip.AddrPort
		wg     sync.WaitGroup
		done   = make(chan struct{}, 1)
	)
	defer wg.Wait()
	enqueue := func(peers []netip.AddrPort) int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for p, at := range tried { // also keeps the map from growing for days
			if time.Since(at) > retryPeer {
				delete(tried, p)
			}
		}
		for _, p := range peers {
			if _, was := tried[p]; !was && !queued[p] {
				queued[p] = true
				queue = append(queue, p)
				n++
			}
		}
		return n
	}
	// Each tracker's peers are queued as soon as it answers: a dead tracker
	// spends its retries (15 s) without holding back the live ones.
	announce := func() {
		enqueue(s.cfg.ExtraPeers)
		var twg sync.WaitGroup
		var failed atomic.Int32
		for _, u := range s.cfg.Desc.Trackers {
			twg.Add(1)
			go func() {
				defer twg.Done()
				peers, err := Announce(ctx, s.cfg.Dialer, u, s.cfg.Desc.InfoHash, pc.PeerID, s.cfg.ListenPort, 200)
				if err != nil {
					failed.Add(1)
					s.cfg.Log.Debug("tracker failed", "tracker", u, "err", err)
					return
				}
				n := enqueue(peers)
				s.cfg.Log.Debug("announce", "tracker", u, "peers", len(peers), "new", n)
			}()
		}
		twg.Wait()
		if n := len(s.cfg.Desc.Trackers); n > 0 && int(failed.Load()) == n && ctx.Err() == nil {
			s.cfg.Log.Warn("announce failed on every tracker")
		}
	}
	topUp := func() {
		mu.Lock()
		defer mu.Unlock()
		for len(queue) > 0 && s.active.Load() < int64(s.cfg.MaxPeers) && ctx.Err() == nil {
			p := queue[0]
			queue = queue[1:]
			delete(queued, p)
			tried[p] = time.Now()
			s.active.Add(1)
			s.dialed.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				var counted atomic.Bool
				cfg := pc
				cfg.OnHandshake = func() {
					counted.Store(true)
					s.connected.Add(1)
				}
				st, err := RunPeer(ctx, s.cfg.Dialer, p, cfg, s.coord)
				if counted.Load() {
					s.connected.Add(-1)
				}
				if st.Handshook {
					s.handshook.Add(1)
				}
				if st.Engaged {
					s.engaged.Add(1)
				}
				if err != nil && !errors.Is(err, context.Canceled) && st.Chunks > 0 {
					s.cfg.Log.Debug("peer ended", "peer", p, "chunks", st.Chunks, "err", err)
				}
				s.active.Add(-1)
				select {
				case done <- struct{}{}:
				default:
				}
			}()
		}
	}

	lastAnnounce := time.Now()
	var announcing atomic.Bool
	announcing.Store(true)
	wg.Add(1)
	go func() {
		defer wg.Done()
		announce()
		announcing.Store(false)
	}()
	dryWait := time.Second
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		topUp()
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-done:
		}
		mu.Lock()
		dry := len(queue) == 0 && s.active.Load() == 0
		if dry {
			clear(tried) // pool exhausted: retry the whole swarm, like the engine
		}
		mu.Unlock()
		// While the pool stays dry, re-announce at 1 s, 2 s, 4 s... up to the
		// normal interval, so a dead channel does not hammer the trackers.
		if !dry {
			dryWait = time.Second
		}
		since := time.Since(lastAnnounce)
		if (since >= s.cfg.Reannounce || dry && since >= dryWait) && !announcing.Load() {
			if dry {
				dryWait = min(2*dryWait, s.cfg.Reannounce)
			}
			// In the background: a slow tracker must not stall topping up.
			lastAnnounce = time.Now()
			announcing.Store(true)
			wg.Add(1)
			go func() {
				defer wg.Done()
				announce()
				announcing.Store(false)
			}()
		}
	}
}
