// Package mobile is the API the Android app gets through gomobile: start a
// channel, read its status, stop it. Everything else stays unexported.
package mobile

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"

	"canalpeer.com/core/ace"
	"canalpeer.com/core/route"
	"canalpeer.com/core/stats"
)

// Options selects what to play and how this process reaches the internet.
type Options struct {
	ContentID string
	// CacheDir keeps the descriptor of every channel opened, so it plays even
	// when no resolver is reachable; "" keeps nothing.
	CacheDir string
	// DataDir keeps the list of channels played (History). It must outlive
	// the app's cache: never a directory the OS may clean. "" keeps no list.
	DataDir string
	// Vpn is the WARP mode for the whole process: "auto" (also ""), "on" or
	// "off". WARP's account and the switch memory live in DataDir.
	Vpn string
}

// Status is a snapshot for the player screen.
type Status struct {
	Phase     string // "resolving" | "searching" | "playing" | "reconnecting" | "failed"
	Peers     int64  // peers past the handshake right now
	LagPieces int64  // pieces between playback and the live edge
	Err       string // only when failed: "not_found" | "network" | "no_peers" | "no_space" | "vpn"
	QueuedMs  int64  // playback time waiting in the core for the player
	Vod       bool   // a movie: the player seeks by itself, there is no live edge
	UpBps     int64  // live: bytes per second served to other peers, over the last 10 s
	Outbound  string // "direct" | "warp": the route this session's swarm takes
}

type runner interface {
	Run(context.Context) error
	Stats() ace.SwarmStats
}

type deps struct {
	resolve     func(context.Context, string) (ace.Descriptor, error)
	newSwarm    func(ace.SwarmConfig) runner
	stallAfter  time.Duration // no bytes this long while playing: restart the swarm
	giveUpAfter time.Duration // no bytes at all this long: the channel is dead

	cacheDir     string                      // Options.CacheDir: movies keep their pieces in its vod folder
	dataDir      string                      // Options.DataDir: the history of channels played
	clientConfig func(*torrent.ClientConfig) // tests keep movies off the internet
	onTorrent    func(*torrent.Torrent)      // tests add their seeder
	played       func(id string)             // the session first reached "playing"; nil reports nothing

	// The route (package route in production). The switch to WARP: a direct
	// swarm with minDials dialled, no handshake and no new video switchAfter
	// after it started.
	resolveVia  func(context.Context, ace.Dialer, string) (ace.Descriptor, error) // the AceStream hosts alone, through a dialer
	direct      ace.Dialer
	warp        func(context.Context) (ace.Dialer, error) // brings the tunnel up; errors wrap route.ErrVPN
	onWarp      func() bool                               // On, or Automatic with a remembered switch
	remembered  func(time.Time) bool                      // Automatic with a remembered switch
	switched    func(time.Time)
	forget      func()
	switchAfter time.Duration
	minDials    int64
	warpWait    time.Duration // the most a session's start waits for the tunnel
	warpRetry   time.Duration // after a failed bring-up, the switch rule asks again this much later
}

type Session struct {
	d          deps
	pipe       *pipe
	url        string
	stopServer func()
	cancel     context.CancelFunc
	done       chan struct{}
	stopOnce   sync.Once
	lastWrite  atomic.Int64 // unix nanos of the last TS bytes, 0 = none yet
	meter      *rateMeter
	up         upMeter

	id string // normalized content id

	mu       sync.Mutex
	name     string // the channel's name, for the history
	recorded bool   // in the history already: a reconnect is the same viewing
	bitrate  int64  // bytes per second, from the descriptor; see rate()
	phase    string
	err      string
	sw       runner
	movie    atomic.Pointer[movie] // set once a movie's file is known; the server serves it
	vod      bool
	peers    func() int64 // a movie's connected peers
	outbound string       // Status.Outbound

	share share // the LAN share for a DLNA TV
}

// Start begins playing a content id or acestream:// link. It returns at once;
// poll Status and play URL once the phase is "playing".
func Start(o *Options) (*Session, error) {
	// The pipe plus swarm buffers: without a limit the GC lets the heap reach
	// twice what is live.
	debug.SetMemoryLimit(2 * pipeCap)
	route.Configure(o.Vpn, o.DataDir)
	rc := newChain(o.CacheDir)
	go rc.syncInBackground()
	remembered := func(now time.Time) bool { return route.Mode() == "auto" && route.Remembered(now) }
	return start(o, deps{
		resolve:     rc.resolve,
		resolveVia:  rc.viaHosts,
		newSwarm:    func(cfg ace.SwarmConfig) runner { return ace.NewSwarm(cfg) },
		stallAfter:  15 * time.Second,
		giveUpAfter: 3 * time.Minute,
		// Follows the route at every dial: direct whenever this session is.
		direct:      route.Dialer(),
		warp:        route.Warp,
		onWarp:      func() bool { return route.Mode() == "on" || remembered(time.Now()) },
		remembered:  remembered,
		switched:    route.Switched,
		forget:      route.ForgetSwitch,
		switchAfter: 12 * time.Second,
		minDials:    10,
		warpWait:    25 * time.Second,
		warpRetry:   5 * time.Second, // route's downFor: asking sooner is answered from its memory
		played:      stats.Play,
	})
}

// newChain resolves through the route: direct, or WARP while it is in use.
func newChain(cacheDir string) resolveChain {
	client := route.HTTPClient(0) // requests carry their own deadlines
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots()}
	return resolveChain{
		cacheDir:  cacheDir,
		builtIn:   builtInDescriptors,
		site:      siteURL,
		mirrors:   siteMirrors,
		mirrorKey: mirrorKey,
		client:    client,
		direct:    ace.NewResolver(route.Dialer()).Resolve,
	}
}

// Sync refreshes the device's copy of the site's channels in the background;
// the apps call it on every start (Start does too, for links that open the
// player directly).
func Sync(cacheDir string) {
	go newChain(cacheDir).syncInBackground()
}

func (rc resolveChain) syncInBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := rc.sync(ctx); err != nil {
		log.Print("descriptor sync: ", err)
	}
}

func start(o *Options, d deps) (*Session, error) {
	id, err := Normalize(o.ContentID)
	if err != nil {
		return nil, err
	}
	d.cacheDir, d.dataDir = o.CacheDir, o.DataDir
	s := &Session{d: d, id: id, pipe: newPipe(pipeCap), meter: newRateMeter(), done: make(chan struct{}), phase: "resolving", outbound: "direct"}
	s.url, s.stopServer, err = serve(s.pipe, &s.movie)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	go func() {
		defer close(s.done)
		s.run(ctx, id)
	}()
	return s, nil
}

func (s *Session) URL() string { return s.url }

// seekMinBehindMs keeps a jump toward live this far from the newest data: right
// at the edge every late piece is a stall (measured on the Fire TV).
const seekMinBehindMs = 12_000

// Jump moves playback by deltaMs (negative goes back), given how long the
// player has been showing since it was last prepared (ExoPlayer's own
// positions are unreliable right after a reopen). It returns how far behind the
// newest data playback will be; the caller then prepares the player again.
// Jump(0, shown) before any re-prepare resumes at what was on screen.
// Positions are estimated from the bitrate.
func (s *Session) Jump(deltaMs, shownMs int64) int64 {
	bitrate := s.rate()
	if bitrate <= 0 {
		return 0
	}
	ms := func(v int64) int64 { return v * bitrate / 1000 }
	behind := s.pipe.seek(ms(shownMs), ms(deltaMs), ms(seekMinBehindMs))
	return behind * 1000 / bitrate
}

// rate is the stream's bytes per second: measured once the stream has run a
// few seconds, the descriptor's until then.
func (s *Session) rate() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rateLocked()
}

func (s *Session) rateLocked() int64 {
	if r := s.meter.Bitrate(); r > 0 {
		return r
	}
	return s.bitrate
}

func (s *Session) Status() *Status {
	s.mu.Lock()
	st := &Status{Phase: s.phase, Err: s.err, Vod: s.vod, Outbound: s.outbound}
	if r := s.rateLocked(); r > 0 {
		st.QueuedMs = int64(s.pipe.Len()) * 1000 / r
	}
	sw, peers := s.sw, s.peers
	s.mu.Unlock()
	if peers != nil {
		st.Peers = peers()
	}
	// Outside s.mu: Stats takes the coordinator lock, which Out holds.
	if sw != nil {
		ss := sw.Stats()
		st.Peers = ss.Connected
		st.LagPieces = max(ss.Edge-ss.Front, 0)
	}
	st.UpBps = s.up.bps()
	return st
}

// Stop ends the swarm, the server and the player's stream. Safe to repeat.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		s.cancel()
		s.StopShare()
		s.stopServer()
		<-s.done
		s.mu.Lock()
		s.sw = nil // the swarm holds its piece buffers
		s.mu.Unlock()
		// Android keeps the process cached after the screen closes; hand the
		// heap back instead of leaving it to the swap.
		debug.FreeOSMemory()
	})
}

func (s *Session) set(phase, err string) {
	s.mu.Lock()
	s.phase, s.err = phase, err
	first := phase == "playing" && !s.recorded
	if first {
		s.recorded = true
	}
	name := s.name
	s.mu.Unlock()
	if first && s.d.played != nil {
		s.d.played(s.id)
	}
	if first && s.d.dataDir != "" {
		if err := recordHistory(s.d.dataDir, s.id, name, time.Now()); err != nil {
			log.Print("history: ", err)
		}
	}
}

func (s *Session) setOutbound(onWarp bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outbound = "direct"
	if onWarp {
		s.outbound = "warp"
	}
}

func (s *Session) run(ctx context.Context, id string) {
	// The route first: On fails before anything goes direct, and a remembered
	// switch starts on WARP.
	dialer, onWarp, fromMemory := s.d.direct, false, false
	if s.d.onWarp() {
		fromMemory = s.d.remembered(time.Now())
		wctx, wcancel := context.WithTimeout(ctx, s.d.warpWait)
		w, err := s.d.warp(wctx)
		wcancel()
		switch {
		case err == nil:
			dialer, onWarp = w, true
		case ctx.Err() != nil:
			return
		case !fromMemory: // On never goes direct
			log.Print("vpn: ", err)
			s.set("failed", "vpn")
			return
		default: // Automatic carries on direct and says nothing
			fromMemory = false
		}
	}
	s.setOutbound(onWarp)

	desc, err := s.d.resolve(ctx, id)
	if err != nil && !onWarp && !errors.Is(err, ace.ErrNotFound) && ctx.Err() == nil {
		// Only the AceStream hosts are retried through WARP: the swarm stays
		// direct and nothing is remembered, since peers may well work direct.
		if w, werr := s.d.warp(ctx); werr == nil {
			desc, err = s.d.resolveVia(ctx, w, id)
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		code := "network"
		if errors.Is(err, ace.ErrNotFound) {
			code = "not_found"
		}
		s.set("failed", code)
		return
	}
	s.mu.Lock()
	s.name = desc.Name
	s.mu.Unlock()
	if desc.Torrent != nil {
		var warp ace.Dialer
		if onWarp {
			warp = dialer
		}
		s.runMovie(ctx, desc.Torrent, warp)
		return
	}
	s.mu.Lock()
	s.bitrate = desc.Bitrate
	s.mu.Unlock()
	s.set("searching", "")
	began := time.Now()
	var startPiece int64
	// One automatic move per session: the switch to WARP, or the way back from
	// a remembered one. Never back and forth.
	mayMove := true
	for {
		if s.lastWrite.Load() != 0 {
			fromMemory = false // the way back is for a start that does not work
		}
		sctx, scancel := context.WithCancel(ctx)
		sw := s.d.newSwarm(ace.SwarmConfig{
			Dialer:     dialer,
			Desc:       desc,
			Tuning:     tuningFor(desc.Bitrate, desc.PieceLength, coreBufferSeconds),
			StartPiece: startPiece,
			Out: func(b []byte) {
				s.pipe.Write(b)
				s.meter.Write(b)
				s.lastWrite.Store(time.Now().UnixNano())
			},
		})
		s.mu.Lock()
		s.sw = sw
		s.mu.Unlock()
		ran := make(chan struct{})
		go func() {
			sw.Run(sctx)
			close(ran)
		}()
		s.up.newSwarm()
		rule := noRule
		switch {
		case !mayMove:
		case !onWarp:
			rule = switchRule
		case fromMemory:
			rule = backRule
		}
		v, w := s.watch(ctx, began, time.Now(), rule)
		if v == unreachable && !onWarp {
			s.d.switched(time.Now())
			dialer, onWarp, mayMove = w, true, false
		} else if v == unreachable { // started on WARP from memory: direct, once
			s.d.forget()
			dialer, onWarp, fromMemory, mayMove = s.d.direct, false, false, false
		}
		scancel()
		<-ran
		if v == ended {
			return
		}
		s.setOutbound(onWarp)
		// From what played, not from the front: an unconfirmed peer can move
		// the front to pieces nobody holds, and that must not outlive the swarm.
		if p := sw.Stats().Played; p > 0 {
			startPiece = p
		}
	}
}

// What watch saw of a swarm.
type verdict int

const (
	ended       verdict = iota // the session ended or gave up
	stalled                    // no bytes for stallAfter: replace the swarm
	unreachable                // the switch rule held: peers listed, none answering
)

// Which automatic move watch looks out for.
type moveRule int

const (
	noRule     moveRule = iota
	switchRule          // a direct swarm: to WARP
	backRule            // a WARP swarm started from the memory: back to direct
)

type bringUp struct {
	d   ace.Dialer
	err error
}

// watch follows one swarm. Once anything has played it never gives up: a home
// network can drop for minutes mid-match.
//
// From switchAfter after the swarm started with no handshake and no new video,
// it reports the swarm unreachable: for backRule at once; for switchRule once
// minDials were dialled and WARP came up, with the dialer to switch to. The
// bring-up runs beside the swarm, which is still watched meanwhile and kept if
// it started working before WARP was ready.
func (s *Session) watch(ctx context.Context, began, swarmStart time.Time, rule moveRule) (verdict, ace.Dialer) {
	tick := time.NewTicker(min(time.Second, s.d.stallAfter/4))
	defer tick.Stop()
	var warp chan bringUp // set while WARP comes up
	var retryAt time.Time // after a failed bring-up, when to ask again
	nothingYet := func() bool {
		return s.lastWrite.Load() < swarmStart.UnixNano() && s.sw.Stats().Handshook == 0
	}
	for {
		select {
		case <-ctx.Done():
			return ended, nil
		case <-tick.C:
		case r := <-warp:
			warp = nil
			switch {
			case r.err != nil:
				// WARP cannot be had (or the mode is Off): this swarm carries on,
				// and asks again once route stops answering from memory, while
				// it still has nothing.
				log.Print("vpn: switch: ", r.err)
				retryAt = time.Now().Add(s.d.warpRetry)
			case nothingYet():
				return unreachable, r.d
			default:
				rule = noRule
			}
			continue
		}
		s.up.sample(time.Now(), s.sw.Stats().Uploaded)
		last := s.lastWrite.Load()
		if rule != noRule && warp == nil && !time.Now().Before(retryAt) && time.Since(swarmStart) >= s.d.switchAfter && nothingYet() {
			switch {
			case rule == backRule:
				return unreachable, nil
			case s.sw.Stats().Dialed >= s.d.minDials:
				warp = make(chan bringUp, 1)
				go func() {
					d, err := s.d.warp(ctx)
					warp <- bringUp{d, err}
				}()
			}
		}
		if last == 0 {
			if time.Since(began) > s.d.giveUpAfter {
				s.set("failed", "no_peers")
				return ended, nil
			}
			continue
		}
		lastAt := time.Unix(0, last)
		if st := s.sw; st != nil && time.Now().Second()%10 == 0 {
			ss := st.Stats()
			log.Printf("canalpeer phase=%s edge=%d front=%d skips=%d bytes=%d peers=%d pending=%d",
				s.Status().Phase, ss.Edge, ss.Front, ss.Skips, ss.BytesWritten, ss.Connected, ss.Pending)
		}
		if lastAt.After(swarmStart) {
			s.set("playing", "")
		}
		if lastAt.Before(swarmStart) {
			lastAt = swarmStart
		}
		if time.Since(lastAt) > s.d.stallAfter {
			s.set("reconnecting", "")
			return stalled, nil
		}
	}
}

// upMeter turns the swarms' Uploaded counters into a rate over the last
// upWindow. Each restarted swarm counts from 0 again; newSwarm says so.
type upMeter struct {
	mu      sync.Mutex
	last    int64 // the current swarm's counter at the last sample
	total   int64 // bytes uploaded by every swarm of the session
	samples []upSample
}

type upSample struct {
	at    time.Time
	total int64
}

const upWindow = 10 * time.Second

func (m *upMeter) newSwarm() {
	m.mu.Lock()
	m.last = 0
	m.mu.Unlock()
}

func (m *upMeter) sample(at time.Time, uploaded int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total += uploaded - m.last
	m.last = uploaded
	m.samples = append(m.samples, upSample{at, m.total})
	for at.Sub(m.samples[0].at) > upWindow {
		m.samples = m.samples[1:]
	}
}

// bps is the bytes per second between the oldest and newest samples in the
// window, 0 until there are two.
func (m *upMeter) bps() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) < 2 {
		return 0
	}
	first, last := m.samples[0], m.samples[len(m.samples)-1]
	dt := last.at.Sub(first.at)
	if dt <= 0 {
		return 0
	}
	return (last.total - first.total) * int64(time.Second) / int64(dt)
}
