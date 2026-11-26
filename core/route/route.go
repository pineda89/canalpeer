// Package route decides, for the whole process, whether outbound connections
// go direct or through WARP, and owns the one shared WARP tunnel.
//
// Modes: "on" always uses WARP and never falls back to direct; "off" never
// builds a tunnel; "auto" (the default, also "") uses WARP only while a switch
// is remembered (see Switched).
package route

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"canalpeer.com/core/ace"
	"canalpeer.com/core/warp"
)

// ErrVPN is what dials fail with in mode "on" when the tunnel cannot be had.
var ErrVPN = errors.New("vpn unavailable")

// Tunneler brings a tunnel up and returns its dialer and a func that closes
// it.
type Tunneler func(ctx context.Context, dataDir string) (ace.Dialer, func(), error)

// probeAddr is dialed through the tunnel to tell whether it still carries
// traffic: a TCP handshake, no TLS.
const probeAddr = "1.1.1.1:443"

// rebuildAfter consecutive failed probes close the tunnel so the next dial
// builds a new one; probeOKFor spares probing again after a good one;
// failFor is how long a failed bring-up is answered from memory, downFor when
// nothing direct answers either (no network yet, as right after waking: that
// says nothing about WARP, so the next dial soon tries again); a tunnel
// that carried nothing for staleAfter is probed before it is used again (the
// app in the background for days, the laptop asleep), and one failed probe
// rebuilds it.
const (
	rebuildAfter = 3
	probeOKFor   = 5 * time.Second
	failFor      = 60 * time.Second
	staleAfter   = 2 * time.Minute
	downFor      = 5 * time.Second
)

// idleClose after its last connection closed (or after coming up unused) the
// tunnel is closed, so a process left in the background sends no keepalives;
// the next dial brings it up again. Tests shorten it.
var idleClose = 5 * time.Minute

var direct = &net.Dialer{}

// attempt is one bring-up of the tunnel; d and err are set before done closes.
type attempt struct {
	done chan struct{}
	d    ace.Dialer
	err  error
}

type state struct {
	mode     string
	dataDir  string
	until    time.Time // the remembered switch lasts until then
	tunneler Tunneler

	dialer      ace.Dialer // nil while no tunnel is up
	closeTunnel func()
	inflight    *attempt
	cancelUp    context.CancelFunc
	failErr     error // the last bring-up's failure, answered until failUntil
	failUntil   time.Time
	probing     bool
	probedOK    time.Time
	probeFails  int
	usedOK      time.Time   // wall clock: the tunnel last came up, dialed or answered a probe
	gen         int         // counts tunnels: a connection of an earlier one is not this one's
	open        int         // this tunnel's dials under way and connections not yet closed
	idle        *time.Timer // closes the tunnel idleClose after open fell to 0
	rested      bool        // closed for idleness: still the route, brought up by the next dial
	proxy       net.Listener
}

var (
	mu sync.Mutex
	st = state{mode: "auto", tunneler: warpTunnel}
)

func (s *state) wanted(now time.Time) bool {
	return s.mode == "on" || (s.mode == "auto" && now.Before(s.until))
}

func (s *state) inUse() bool { return s.wanted(time.Now()) && (s.dialer != nil || s.rested) }

// used notes that the tunnel just carried traffic. Round(0) drops the
// monotonic reading, which stands still while the device sleeps.
func (s *state) used() { s.usedOK = time.Now().Round(0) }

func (s *state) stale() bool { return time.Since(s.usedOK) > staleAfter }

func (s *state) dropTunnel() {
	if s.dialer != nil {
		s.closeTunnel()
		s.dialer, s.closeTunnel = nil, nil
	}
	s.probedOK, s.probeFails = time.Time{}, 0
	s.stopIdle()
	s.gen++
	s.open = 0
}

func (s *state) stopIdle() {
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

// armIdle closes the tunnel idleClose from now unless a dial comes first.
// Called with mu held and nothing open.
func (s *state) armIdle() {
	s.stopIdle()
	gen := s.gen
	s.idle = time.AfterFunc(idleClose, func() {
		mu.Lock()
		defer mu.Unlock()
		if st.gen == gen && st.open == 0 && st.dialer != nil {
			st.dropTunnel()
			st.rested = true
		}
	})
}

// stop cancels any bring-up, closes the tunnel and the proxy.
func (s *state) stop() {
	if s.inflight != nil {
		s.cancelUp()
		s.inflight, s.cancelUp = nil, nil
	}
	s.dropTunnel()
	s.rested = false
	if s.proxy != nil {
		s.proxy.Close()
		s.proxy = nil
	}
}

// kick starts a background bring-up unless a tunnel is up, one is coming up,
// the last one failed recently, or the mode is off.
func (s *state) kick() {
	if s.mode != "off" && s.dialer == nil && s.inflight == nil && !time.Now().Before(s.failUntil) {
		s.start()
	}
}

// Configure sets the mode and the directory holding WARP's account and the
// switch memory, and, when either changed, forgets any remembered bring-up
// failure (the same configuration again changes nothing). When WARP is
// wanted the tunnel starts coming up in the background; "off" closes it, the
// proxy, and forgets the switch.
func Configure(mode, dataDir string) {
	if mode != "on" && mode != "off" {
		mode = "auto"
	}
	mu.Lock()
	defer mu.Unlock()
	if mode == st.mode && dataDir == st.dataDir {
		if st.wanted(time.Now()) {
			st.kick()
		}
		return
	}
	st.mode, st.dataDir = mode, dataDir
	st.until = loadMemory(dataDir)
	st.failErr, st.failUntil = nil, time.Time{}
	if mode == "off" {
		st.forget()
		st.stop()
	}
	if st.wanted(time.Now()) {
		st.kick()
	}
}

// Mode is the configured mode: "auto", "on" or "off".
func Mode() string {
	mu.Lock()
	defer mu.Unlock()
	return st.mode
}

// InUse reports whether connections currently go through WARP: it is wanted
// and the tunnel is up, or closed only for idleness.
func InUse() bool {
	mu.Lock()
	defer mu.Unlock()
	return st.inUse()
}

// Direct is the dialer that bypasses WARP.
func Direct() ace.Dialer { return direct }

// Warp brings the shared tunnel up (concurrent callers wait for the same
// bring-up) and returns a dialer through WARP. The dialer follows the shared
// tunnel: when it is rebuilt, later dials use the new one, and they fail with
// ErrVPN when it cannot be had or the mode is off. Errors wrap ErrVPN.
func Warp(ctx context.Context) (ace.Dialer, error) {
	if _, err := tunnel(ctx); err != nil {
		return nil, err
	}
	return tunnelDialer{}, nil
}

// tunnel returns the tunnel's own dialer, bringing it up when there is none.
// Off never brings one up, so a caller racing Configure("off") cannot leave a
// tunnel behind. Auto without a remembered switch may: that is how a session
// switches. A failure within failFor is returned without trying again.
func tunnel(ctx context.Context) (ace.Dialer, error) {
	mu.Lock()
	if st.mode == "off" {
		mu.Unlock()
		return nil, fmt.Errorf("%w: off", ErrVPN)
	}
	if d := st.dialer; d != nil && !st.stale() {
		mu.Unlock()
		return d, nil
	}
	a := st.inflight
	if a == nil {
		if st.dialer == nil && time.Now().Before(st.failUntil) {
			err := st.failErr
			mu.Unlock()
			return nil, fmt.Errorf("%w: %w", ErrVPN, err)
		}
		a = st.start()
	}
	mu.Unlock()
	select {
	case <-a.done:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrVPN, ctx.Err())
	}
	if a.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrVPN, a.err)
	}
	return a.d, nil
}

// start launches a bring-up not tied to any caller's context; stop cancels
// it. A tunnel already up (a stale one) is probed first and kept when it
// answers, closed otherwise. Called with mu held.
func (s *state) start() *attempt {
	a := &attempt{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	s.inflight, s.cancelUp = a, cancel
	up, dir, old := s.tunneler, s.dataDir, s.dialer
	s.stopIdle() // not under the probe of a stale tunnel
	go func() {
		if old != nil {
			ok := alive(old)
			mu.Lock()
			if ok || st.dialer != old { // kept, or replaced meanwhile: nothing to build
				if st.inflight == a {
					st.inflight, st.cancelUp = nil, nil
				}
				if ok && st.dialer == old {
					st.used()
					if st.open == 0 {
						st.armIdle()
					}
					a.d = old
				} else {
					a.err = errors.New("route: stopped")
				}
				mu.Unlock()
				cancel()
				close(a.done)
				return
			}
			st.dropTunnel()
			mu.Unlock()
		}
		d, closeFn, err := up(ctx, dir)
		remember := failFor
		if err != nil && ctx.Err() == nil && !online() {
			remember = downFor
		}
		cancel()
		mu.Lock()
		if st.inflight == a { // not stopped (or reset) meanwhile
			st.inflight, st.cancelUp, st.rested = nil, nil, false
			if err == nil {
				st.dialer, st.closeTunnel = d, closeFn
				st.used()
				st.armIdle()
			} else {
				st.failErr, st.failUntil = err, time.Now().Add(remember)
			}
		} else if err == nil {
			closeFn()
			d, err = nil, errors.New("route: stopped")
		}
		a.d, a.err = d, err
		mu.Unlock()
		close(a.done)
	}()
	return a
}

// tunnelDialer dials through whatever tunnel is up now. A failed dial probes
// the tunnel; rebuildAfter failed probes in a row close it, and the next dial
// brings up a new one. A slow probe alone never cuts live connections.
type tunnelDialer struct{}

func (tunnelDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d, gen, err := acquire(ctx)
	if err != nil {
		return nil, err
	}
	c, err := d.DialContext(ctx, network, address)
	if err != nil {
		release(gen)
		// A black-holed tunnel shows up as dials running out their deadline, so
		// those probe too; only a caller cancelling says nothing about the tunnel.
		if !errors.Is(ctx.Err(), context.Canceled) {
			checkTunnel(d)
		}
		return nil, err
	}
	mu.Lock()
	if st.dialer == d {
		st.used()
	}
	mu.Unlock()
	return &tunnelConn{Conn: c, gen: gen}, nil
}

// acquire is tunnel plus a count of the dial, which keeps the tunnel from
// closing for idleness until release.
func acquire(ctx context.Context) (ace.Dialer, int, error) {
	for {
		d, err := tunnel(ctx)
		if err != nil {
			return nil, 0, err
		}
		mu.Lock()
		if st.dialer == d { // else closed since tunnel returned: ask again
			st.open++
			st.stopIdle()
			gen := st.gen
			mu.Unlock()
			return d, gen, nil
		}
		mu.Unlock()
	}
}

func release(gen int) {
	mu.Lock()
	defer mu.Unlock()
	if st.gen != gen {
		return
	}
	if st.open--; st.open == 0 {
		st.armIdle()
	}
}

// tunnelConn releases its count of the tunnel when closed.
type tunnelConn struct {
	net.Conn
	gen  int
	once sync.Once
}

func (c *tunnelConn) Close() error {
	c.once.Do(func() { release(c.gen) })
	return c.Conn.Close()
}

func checkTunnel(d ace.Dialer) {
	mu.Lock()
	if st.dialer != d || st.probing || time.Since(st.probedOK) < probeOKFor {
		mu.Unlock()
		return
	}
	st.probing = true
	mu.Unlock()
	ok := alive(d)
	mu.Lock()
	defer mu.Unlock()
	st.probing = false
	if st.dialer != d {
		return
	}
	if ok {
		st.probedOK, st.probeFails = time.Now(), 0
		st.used()
		return
	}
	st.probeFails++
	if st.probeFails >= rebuildAfter {
		st.dropTunnel()
	}
}

// online reports whether the network carries anything at all: probeAddr
// answers direct (a bare TCP handshake to Cloudflare, like registration).
// Tests replace it.
var online = func() bool { return alive(direct) }

// alive dials probeAddr through d: a TCP handshake, no TLS.
func alive(d ace.Dialer) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", probeAddr)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Current is the dialer new connections should use now. Off, or Automatic
// without a remembered switch: direct. On: WARP, whose dials fail with ErrVPN
// when the tunnel cannot be had, never direct. Automatic with a remembered
// switch: WARP when the tunnel is up or was only closed for idleness (the
// dial brings it up again); otherwise direct at once while the tunnel comes
// up in the background (a caller never waits for it).
func Current(ctx context.Context) ace.Dialer {
	mu.Lock()
	defer mu.Unlock()
	switch {
	case !st.wanted(time.Now()):
		return direct
	case st.mode == "on" || st.dialer != nil || st.rested:
		return tunnelDialer{}
	}
	st.kick()
	return direct
}

type routeDialer struct{}

func (routeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return Current(ctx).DialContext(ctx, network, address)
}

// Dialer follows the route at every dial (Current at each one): what a
// long-lived consumer must keep instead of a dialer Current returned once.
func Dialer() ace.Dialer { return routeDialer{} }

// HTTPClient dials through Dialer at each new connection. Keep-alives are
// off so a pooled connection never outlives a change of route.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       Dialer().DialContext,
		},
	}
}

// reregistered: the one re-registration a process may make was spent (only
// once Register itself succeeded).
var reregistered atomic.Bool

// silentFor is how long, after a new account was as silent as the stored one,
// no process registers again: the network drops UDP, the key is not to blame.
const silentFor = 24 * time.Hour

// warpTunnel is the production Tunneler: the stored account (registered when
// there is none), every endpoint in order, and, once per process, a new
// registration when the stored account did not carry traffic:
//   - endpoints answered but carried nothing: the new account is saved and
//     tried everywhere;
//   - nothing answered (a revoked key looks like a network dropping UDP): the
//     new account is tried on two endpoints and saved only if it works;
//     otherwise it is discarded and the stored one is stamped so no process
//     registers again for silentFor.
//
// Local setup failures say nothing about the account and register nothing.
func warpTunnel(ctx context.Context, dataDir string) (ace.Dialer, func(), error) {
	// The tunnel does not exist yet: registration goes direct.
	reg := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: direct.DialContext}}
	a, stored := warp.LoadAccount(dataDir)
	if !stored {
		var err error
		if a, err = register(ctx, reg); err != nil {
			return nil, nil, err
		}
		_ = warp.SaveAccount(dataDir, a) // unsaved only means registering again next time
	}
	d, closeFn, err := upAny(ctx, dataDir, a, warp.Endpoints(a))
	if err == nil || !stored || ctx.Err() != nil || reregistered.Load() {
		return d, closeFn, err
	}
	ans, silent := answered(err), silentAll(err)
	if !ans && !(silent && time.Now().After(a.NoReregisterUntil)) {
		return nil, nil, err
	}
	n, rerr := register(ctx, reg)
	if rerr != nil {
		return nil, nil, errors.Join(err, rerr)
	}
	reregistered.Store(true) // one bring-up at a time (start): no race
	if ans {
		_ = warp.SaveAccount(dataDir, n)
		return upAny(ctx, dataDir, n, warp.Endpoints(n))
	}
	// First where the old account last worked: a network that only lets some
	// ports through must not make the new key look silent. upAny saves n (with
	// its LastEndpoint) only when it works.
	seed := n
	seed.LastEndpoint = a.LastEndpoint
	d, closeFn, nerr := upAny(ctx, dataDir, n, warp.Endpoints(seed)[:2])
	if nerr == nil {
		return d, closeFn, nil
	}
	if ctx.Err() == nil {
		a.NoReregisterUntil = time.Now().Add(silentFor)
		_ = warp.SaveAccount(dataDir, a)
	}
	return nil, nil, errors.Join(err, nerr)
}

func upAny(ctx context.Context, dataDir string, a warp.Account, endpoints []string) (ace.Dialer, func(), error) {
	var errs []error
	for _, e := range resolve(ctx, endpoints) {
		d, closeFn, err := up(ctx, a, e)
		if err == nil {
			if a.LastEndpoint != e {
				a.LastEndpoint = e
				_ = warp.SaveAccount(dataDir, a)
			}
			return d, closeFn, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", e, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, nil, errors.Join(errs...)
}

// resolve replaces each hostname endpoint with its IPv4 addresses on the same
// port (wireguard-go takes literal IP:port only), skipping any already
// listed; a hostname that does not resolve is dropped.
func resolve(ctx context.Context, endpoints []string) []string {
	seen := map[string]bool{}
	for _, e := range endpoints {
		seen[e] = true
	}
	addrs := map[string][]netip.Addr{}
	var out []string
	for _, e := range endpoints {
		h, port, err := net.SplitHostPort(e)
		if _, perr := netip.ParseAddr(h); err != nil || perr == nil {
			out = append(out, e)
			continue
		}
		ips, ok := addrs[h]
		if !ok {
			ips, _ = lookupIP(ctx, "ip4", h) // on error: nil, the hostname is dropped
			addrs[h] = ips
		}
		for _, ip := range ips {
			if r := net.JoinHostPort(ip.Unmap().String(), port); !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// endpointErrs splits upAny's error into one error per endpoint.
func endpointErrs(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

// answered reports whether any endpoint in upAny's error handshook: neither
// silent nor a local setup failure.
func answered(err error) bool {
	for _, e := range endpointErrs(err) {
		if !errors.Is(e, warp.ErrNoAnswer) && !errors.Is(e, warp.ErrSetup) {
			return true
		}
	}
	return false
}

// silentAll reports whether every endpoint that was reached never answered:
// at least one ErrNoAnswer, the rest local setup failures (wireguard-go
// cannot take the registration's hostname endpoints, so those always fail
// locally).
func silentAll(err error) bool {
	heard := false
	for _, e := range endpointErrs(err) {
		switch {
		case errors.Is(e, warp.ErrNoAnswer):
			heard = true
		case !errors.Is(e, warp.ErrSetup):
			return false
		}
	}
	return heard
}

// lookupIP resolves hostname endpoints with the system resolver; tests
// replace it.
var lookupIP = net.DefaultResolver.LookupNetIP

// register and up reach Cloudflare; tests replace them.
var (
	register = func(ctx context.Context, c *http.Client) (warp.Account, error) {
		return warp.Register(ctx, c, warp.APIURL)
	}
	up = func(ctx context.Context, a warp.Account, endpoint string) (ace.Dialer, func(), error) {
		t, err := warp.Up(ctx, a, endpoint, warp.TraceCheck)
		if err != nil {
			return nil, nil, err
		}
		return t.Net(), t.Close, nil
	}
)

// SetTunnelerForTest replaces how the tunnel is brought up.
func SetTunnelerForTest(t Tunneler) {
	mu.Lock()
	defer mu.Unlock()
	st.tunneler = t
}

// ResetForTest closes everything and returns the package to a fresh
// process's state.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	st.stop()
	st = state{mode: "auto", tunneler: warpTunnel}
	reregistered.Store(false)
}
