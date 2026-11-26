package route

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"canalpeer.com/core/ace"
	"canalpeer.com/core/warp"
)

// fakeTunnel stands in for WARP: its dialer records every address, connects
// for real (or through dial when set) and fails while dead is set.
type fakeTunnel struct {
	mu     sync.Mutex
	addrs  []string
	dead   atomic.Bool
	hang   atomic.Bool // black hole: dials wait for their ctx, probes fail
	ups    atomic.Int32
	closes atomic.Int32
	err    error         // returned by the Tunneler when set
	delay  time.Duration // how long a bring-up takes; 20 ms when zero
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
}

func (f *fakeTunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	f.addrs = append(f.addrs, address)
	f.mu.Unlock()
	if f.dead.Load() || (f.hang.Load() && address == probeAddr) {
		return nil, errors.New("tunnel dead")
	}
	if f.hang.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if address == probeAddr {
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	if f.dial != nil {
		return f.dial(ctx, network, address)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

// dialed reports the addresses dialed through the tunnel, the liveness probe
// left out.
func (f *fakeTunnel) dialed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, a := range f.addrs {
		if a != probeAddr {
			out = append(out, a)
		}
	}
	return out
}

func (f *fakeTunnel) tunneler(ctx context.Context, dataDir string) (ace.Dialer, func(), error) {
	f.ups.Add(1)
	d := f.delay
	if d == 0 {
		d = 20 * time.Millisecond // concurrent callers overlap the attempt
	}
	time.Sleep(d)
	if f.err != nil {
		return nil, nil, f.err
	}
	f.dead.Store(false)
	f.hang.Store(false)
	return f, func() { f.closes.Add(1) }, nil
}

func setup(t *testing.T) (*fakeTunnel, string) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	oldOnline := online
	t.Cleanup(func() { online = oldOnline })
	online = func() bool { return true }
	t.Cleanup(func() { setIdleClose(5 * time.Minute) })
	f := &fakeTunnel{}
	SetTunnelerForTest(f.tunneler)
	return f, t.TempDir()
}

func dialThrough(t *testing.T, d ace.Dialer, addr string) error {
	t.Helper()
	c, err := d.DialContext(context.Background(), "tcp", addr)
	if err == nil {
		c.Close()
	}
	return err
}

func listener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestOffNeverBuildsTunnel(t *testing.T) {
	f, dir := setup(t)
	Configure("off", dir)
	addr := listener(t)
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if f.ups.Load() != 0 || len(f.dialed()) != 0 {
		t.Fatalf("tunnel used while off: ups=%d dialed=%v", f.ups.Load(), f.dialed())
	}
	if InUse() || ProxyAddr() != "" {
		t.Fatal("off reports in use or a proxy")
	}
	if Mode() != "off" {
		t.Fatalf("mode %q", Mode())
	}
}

func TestEmptyModeIsAuto(t *testing.T) {
	_, dir := setup(t)
	Configure("", dir)
	if Mode() != "auto" {
		t.Fatalf("mode %q", Mode())
	}
}

func TestOnUsesTunnel(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	addr := listener(t)
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if got := f.dialed(); len(got) != 1 || got[0] != addr {
		t.Fatalf("dialed %v, want [%s]", got, addr)
	}
	if !InUse() {
		t.Fatal("not in use")
	}
}

func TestOnFailureIsErrVPN(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	Configure("on", dir)
	err := dialThrough(t, Current(context.Background()), listener(t))
	if !errors.Is(err, ErrVPN) {
		t.Fatalf("err %v, want ErrVPN", err)
	}
	if InUse() {
		t.Fatal("in use without a tunnel")
	}
}

func TestAutoFailureGoesDirect(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	Configure("auto", dir)
	Switched(time.Now())
	if err := dialThrough(t, Current(context.Background()), listener(t)); err != nil {
		t.Fatalf("auto with a failed tunnel should go direct: %v", err)
	}
}

func TestAutoUsesTunnelOnlyWhenRemembered(t *testing.T) {
	f, dir := setup(t)
	Configure("auto", dir)
	addr := listener(t)
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if len(f.dialed()) != 0 || InUse() {
		t.Fatal("auto without memory used the tunnel")
	}

	now := time.Now()
	Switched(now)
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if len(f.dialed()) != 1 || !InUse() {
		t.Fatalf("remembered switch not in use: dialed %v", f.dialed())
	}

	if Remembered(now.Add(3*time.Hour + time.Minute)) {
		t.Fatal("remembered after 3 h")
	}
	Switched(now.Add(-3*time.Hour - time.Minute)) // a switch made 3h01m ago
	if InUse() {
		t.Fatal("in use after the memory expired")
	}
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if len(f.dialed()) != 1 {
		t.Fatalf("expired memory still dials through the tunnel: %v", f.dialed())
	}
}

func TestMemorySurvivesRestart(t *testing.T) {
	f, dir := setup(t)
	Configure("auto", dir)
	Switched(time.Now())

	ResetForTest()
	SetTunnelerForTest(f.tunneler)
	if Remembered(time.Now()) {
		t.Fatal("remembered before Configure loaded the dir")
	}
	Configure("auto", dir)
	if !Remembered(time.Now()) {
		t.Fatal("memory lost across restart")
	}
	ForgetSwitch()
	if Remembered(time.Now()) {
		t.Fatal("remembered after ForgetSwitch")
	}
	if _, err := os.Stat(filepath.Join(dir, memoryFile)); !os.IsNotExist(err) {
		t.Fatalf("memory file left behind: %v", err)
	}
}

func TestRouteSharesOneTunnel(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir) // a background bring-up joins the callers below
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Warp(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.ups.Load(); n != 1 {
		t.Fatalf("tunneler called %d times, want 1", n)
	}
}

func TestRouteRebuildsDeadTunnel(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d, err := Warp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	addr := listener(t)
	f.dead.Store(true)
	for range rebuildAfter {
		if dialThrough(t, d, addr) == nil {
			t.Fatal("dial through a dead tunnel succeeded")
		}
	}
	if err := dialThrough(t, d, addr); err != nil { // same dialer, new tunnel
		t.Fatal(err)
	}
	if n := f.ups.Load(); n != 2 {
		t.Fatalf("tunneler called %d times, want 2", n)
	}
	if f.closes.Load() != 1 {
		t.Fatal("dead tunnel not closed")
	}
}

func TestOneFailedProbeKeepsTunnel(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d, err := Warp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	addr := listener(t)
	for range 3 {
		if _, err := Warp(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := dialThrough(t, d, addr); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	probed := len(f.addrs) - 3
	f.mu.Unlock()
	if probed != 0 {
		t.Fatalf("probed %d times while dials succeed", probed)
	}
	f.dead.Store(true)
	dialThrough(t, d, addr)
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.dead.Store(false)
	if err := dialThrough(t, d, addr); err != nil {
		t.Fatal(err)
	}
	if f.ups.Load() != 1 || f.closes.Load() != 0 {
		t.Fatalf("one failed probe rebuilt the tunnel: ups=%d closes=%d", f.ups.Load(), f.closes.Load())
	}
}

func TestDialerFollowsRoute(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d := Dialer()
	addr := listener(t)
	if err := dialThrough(t, d, addr); err != nil {
		t.Fatal(err)
	}
	f.dead.Store(true)
	for range rebuildAfter {
		dialThrough(t, d, addr)
	}
	if err := dialThrough(t, d, addr); err != nil {
		t.Fatal(err)
	}
	if f.ups.Load() != 2 {
		t.Fatalf("not through a new tunnel: ups=%d", f.ups.Load())
	}
	n := len(f.dialed())
	Configure("off", dir)
	if err := dialThrough(t, d, addr); err != nil {
		t.Fatal(err)
	}
	if len(f.dialed()) != n {
		t.Fatal("off still dials through the tunnel")
	}
}

func TestWarpRefusesWhenOff(t *testing.T) {
	f, dir := setup(t)
	Configure("off", dir)
	if _, err := Warp(context.Background()); !errors.Is(err, ErrVPN) {
		t.Fatalf("err %v, want ErrVPN", err)
	}
	if f.ups.Load() != 0 {
		t.Fatal("tunnel brought up while off")
	}
}

func TestFailureIsRemembered(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	Configure("on", dir)
	addr := listener(t)
	for range 3 {
		if err := dialThrough(t, Current(context.Background()), addr); !errors.Is(err, ErrVPN) {
			t.Fatalf("err %v, want ErrVPN", err)
		}
	}
	if n := f.ups.Load(); n != 1 {
		t.Fatalf("tunneler called %d times within the failure window, want 1", n)
	}
}

func TestAutoNeverWaitsForTunnel(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	f.delay = 2 * time.Second
	Configure("auto", dir)
	Switched(time.Now())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	resp, err := HTTPClient(300 * time.Millisecond).Get(srv.URL)
	if err != nil {
		t.Fatalf("auto waited for a failing tunnel: %v", err)
	}
	resp.Body.Close()
}

func TestOffClearsEverything(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	Switched(time.Now())
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ProxyAddr() == "" {
		t.Fatal("no proxy while on")
	}
	Configure("off", dir)
	if f.closes.Load() != 1 {
		t.Fatal("tunnel not closed")
	}
	if _, err := os.Stat(filepath.Join(dir, memoryFile)); !os.IsNotExist(err) {
		t.Fatalf("memory file left behind: %v", err)
	}
	if ProxyAddr() != "" || InUse() || Remembered(time.Now()) {
		t.Fatal("off left state behind")
	}
}

func TestSocksForwardsThroughTunnel(t *testing.T) {
	f, dir := setup(t)
	target := make(chan net.Conn, 1)
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		target <- c2
		return c1, nil
	}
	Configure("on", dir)
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	addr := ProxyAddr()
	if addr == "" {
		t.Fatal("no proxy")
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(c, reply); err != nil || reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("method reply %v %v", reply, err)
	}
	host := "example.test"
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, 443)
	c.Write(req)
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil || rep[1] != 0 {
		t.Fatalf("connect reply %v %v", rep, err)
	}
	if got := f.dialed(); len(got) != 1 || got[0] != "example.test:443" {
		t.Fatalf("dialed %v", got)
	}

	tc := <-target
	defer tc.Close()
	tc.SetDeadline(time.Now().Add(5 * time.Second))
	go c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(tc, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("target got %q %v", buf, err)
	}
	go tc.Write([]byte("pong"))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "pong" {
		t.Fatalf("client got %q %v", buf, err)
	}
}

func TestHTTPClientFollowsRoute(t *testing.T) {
	f, dir := setup(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	Configure("off", dir)
	c := HTTPClient(5 * time.Second)
	get := func() {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	get()
	if len(f.dialed()) != 0 {
		t.Fatal("off dialed through the tunnel")
	}
	Configure("on", dir)
	get()
	if got := f.dialed(); len(got) != 1 || got[0] != srv.Listener.Addr().String() {
		t.Fatalf("on dialed %v", got)
	}
}

// socksConnect runs a SOCKS5 CONNECT to host:port through ProxyAddr and
// returns the reply code (-1 when there is no reply).
func socksConnect(t *testing.T, host string, port uint16) (net.Conn, int) {
	t.Helper()
	addr := ProxyAddr()
	if addr == "" {
		t.Fatal("no proxy")
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(c, reply); err != nil || reply[1] != 0 {
		t.Fatalf("method reply %v %v", reply, err)
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = binary.BigEndian.AppendUint16(req, port)
	c.Write(req)
	rep := make([]byte, 10)
	if _, err := io.ReadFull(c, rep); err != nil {
		return c, -1
	}
	return c, int(rep[1])
}

func TestProxyOpenWhileTunnelComesUp(t *testing.T) {
	f, dir := setup(t)
	f.delay = 300 * time.Millisecond
	Configure("on", dir)
	if ProxyAddr() == "" {
		t.Fatal("no proxy while on and the tunnel is coming up")
	}
}

func TestProxyOnFailureRefuses(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	Configure("on", dir)
	if _, code := socksConnect(t, "example.test", 443); code != repFailure {
		t.Fatalf("reply %d, want %d", code, repFailure)
	}
}

func TestSocksHandshakeDeadlineSparesBringUp(t *testing.T) {
	f, dir := setup(t)
	old := socksHandshakeTimeout
	socksHandshakeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { socksHandshakeTimeout = old })
	f.delay = 400 * time.Millisecond
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go func() { io.Copy(io.Discard, c2) }()
		return c1, nil
	}
	Configure("on", dir)
	if _, code := socksConnect(t, "example.test", 443); code != repOK {
		t.Fatalf("reply %d, want OK", code)
	}
}

// dialTimeout dials through d with a short deadline, as every real caller does.
func dialTimeout(d ace.Dialer, addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", addr)
	if err == nil {
		c.Close()
	}
	return err
}

func TestBlackHoledTunnelRebuiltOn(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d := Dialer()
	addr := listener(t)
	if err := dialTimeout(d, addr); err != nil {
		t.Fatal(err)
	}
	f.hang.Store(true)
	for range rebuildAfter {
		if dialTimeout(d, addr) == nil {
			t.Fatal("dial through a black-holed tunnel succeeded")
		}
	}
	if err := dialTimeout(d, addr); err != nil {
		t.Fatalf("not rebuilt: %v", err)
	}
	if n := f.ups.Load(); n != 2 {
		t.Fatalf("tunneler called %d times, want 2", n)
	}
}

func TestBlackHoledTunnelAutoGoesDirect(t *testing.T) {
	f, dir := setup(t)
	Configure("auto", dir)
	Switched(time.Now())
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	addr := listener(t)
	f.hang.Store(true)
	for range rebuildAfter {
		dialTimeout(Current(context.Background()), addr)
	}
	if Current(context.Background()) != Direct() {
		t.Fatal("auto still routes through a black-holed tunnel")
	}
}

// Android configures the route more than once per start: an unchanged
// configuration must not forget a failure, or every channel waits for a new
// bring-up.
func TestReconfigureKeepsFailure(t *testing.T) {
	f, dir := setup(t)
	f.err = errors.New("no endpoint answers")
	Configure("on", dir)
	if _, err := Warp(context.Background()); !errors.Is(err, ErrVPN) {
		t.Fatalf("err %v, want ErrVPN", err)
	}
	Configure("on", dir)
	if _, err := Warp(context.Background()); !errors.Is(err, ErrVPN) {
		t.Fatalf("err %v, want ErrVPN", err)
	}
	if n := f.ups.Load(); n != 1 {
		t.Fatalf("tunneler called %d times, want 1: the same configuration forgot the failure", n)
	}
	Configure("off", dir) // a change of mode does forget it
	Configure("on", dir)
	Warp(context.Background())
	if n := f.ups.Load(); n != 2 {
		t.Fatalf("tunneler called %d times after a change of mode, want 2", n)
	}
}

// fakeWarp replaces registration and tunnel bring-up for warpTunnel, with a
// stored account in dir: every endpoint fails with upErr.
func fakeWarp(t *testing.T, dir string, upErr error) (registers *atomic.Int32) {
	t.Helper()
	ResetForTest()
	t.Cleanup(ResetForTest)
	oldReg, oldUp := register, up
	t.Cleanup(func() { register, up = oldReg, oldUp })
	acct := warp.Account{PrivateKey: make([]byte, 32), PeerPublicKey: make([]byte, 32), Address: netip.MustParseAddr("172.16.0.2")}
	if err := warp.SaveAccount(dir, acct); err != nil {
		t.Fatal(err)
	}
	registers = &atomic.Int32{}
	register = func(context.Context, *http.Client) (warp.Account, error) {
		registers.Add(1)
		return acct, nil
	}
	up = func(context.Context, warp.Account, string) (ace.Dialer, func(), error) { return nil, nil, upErr }
	return registers
}

// Within a day of a new account proving as silent as the stored one, a
// silent network registers nothing.
func TestNoAnswerWithinADayDoesNotRegisterAgain(t *testing.T) {
	dir := t.TempDir()
	registers := fakeWarp(t, dir, fmt.Errorf("%w: check: deadline", warp.ErrNoAnswer))
	a, _ := warp.LoadAccount(dir)
	a.NoReregisterUntil = time.Now().Add(time.Hour)
	if err := warp.SaveAccount(dir, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err := warpTunnel(context.Background(), dir); err == nil {
		t.Fatal("tunnel up")
	}
	if n := registers.Load(); n != 0 {
		t.Fatalf("registered %d times, want 0", n)
	}
}

// Endpoints that answer but do not carry traffic with the stored key: one
// new registration, once per process.
func TestAnswerWithoutTrafficRegistersOnce(t *testing.T) {
	dir := t.TempDir()
	registers := fakeWarp(t, dir, errors.New("warp trace: not over WARP"))
	for range 3 {
		if _, _, err := warpTunnel(context.Background(), dir); err == nil {
			t.Fatal("tunnel up")
		}
	}
	if n := registers.Load(); n != 1 {
		t.Fatalf("registered %d times, want 1", n)
	}
}

// fakeRevoked stores an account whose key every endpoint ignores
// (ErrNoAnswer), and registers a new one (PrivateKey[0] == 1) that fails
// with newErr (nil: it works). regErr fails registration instead.
func fakeRevoked(t *testing.T, dir string, newErr, regErr error) (registers *atomic.Int32, newTries *atomic.Int32) {
	t.Helper()
	fakeWarp(t, dir, nil)
	registers, newTries = &atomic.Int32{}, &atomic.Int32{}
	register = func(context.Context, *http.Client) (warp.Account, error) {
		registers.Add(1)
		if regErr != nil {
			return warp.Account{}, regErr
		}
		k := make([]byte, 32)
		k[0] = 1
		return warp.Account{PrivateKey: k, PeerPublicKey: make([]byte, 32), Address: netip.MustParseAddr("172.16.0.3")}, nil
	}
	up = func(_ context.Context, a warp.Account, _ string) (ace.Dialer, func(), error) {
		if a.PrivateKey[0] != 1 {
			return nil, nil, fmt.Errorf("%w: check: deadline", warp.ErrNoAnswer)
		}
		newTries.Add(1)
		if newErr != nil {
			return nil, nil, newErr
		}
		return &net.Dialer{}, func() {}, nil
	}
	return registers, newTries
}

// A revoked key looks like a silent network: a new account that does carry
// traffic replaces it, on disk too.
func TestSilentStoredKeyReplacedByWorkingAccount(t *testing.T) {
	dir := t.TempDir()
	registers, tries := fakeRevoked(t, dir, nil, nil)
	if _, _, err := warpTunnel(context.Background(), dir); err != nil {
		t.Fatalf("tunnel down: %v", err)
	}
	if n := registers.Load(); n != 1 {
		t.Fatalf("registered %d times, want 1", n)
	}
	if n := tries.Load(); n != 1 {
		t.Fatalf("new account tried on %d endpoints, want 1", n)
	}
	a, ok := warp.LoadAccount(dir)
	if !ok || a.PrivateKey[0] != 1 || a.LastEndpoint == "" {
		t.Fatalf("stored %+v, want the new account with its endpoint", a.Address)
	}
}

// When the new account is silent too, the network is to blame: the old
// account stays, and nobody registers again for a day, in this process or
// the next.
func TestSilentNewAccountKeepsOldAndWaitsADay(t *testing.T) {
	dir := t.TempDir()
	registers, tries := fakeRevoked(t, dir, fmt.Errorf("%w: check: deadline", warp.ErrNoAnswer), nil)
	if _, _, err := warpTunnel(context.Background(), dir); err == nil {
		t.Fatal("tunnel up")
	}
	if n := tries.Load(); n != 2 {
		t.Fatalf("new account tried on %d endpoints, want 2", n)
	}
	a, ok := warp.LoadAccount(dir)
	if !ok || a.PrivateKey[0] == 1 {
		t.Fatal("the silent new account replaced the stored one")
	}
	if d := time.Until(a.NoReregisterUntil); d < 23*time.Hour || d > 25*time.Hour {
		t.Fatalf("NoReregisterUntil in %v, want ~24h", d)
	}
	warpTunnel(context.Background(), dir) // same process
	reregistered.Store(false)             // a new process, same disk
	warpTunnel(context.Background(), dir)
	if n := registers.Load(); n != 1 {
		t.Fatalf("registered %d times, want 1", n)
	}
}

// A failed registration call does not spend the process's one chance.
func TestRegisterErrorKeepsTheChance(t *testing.T) {
	dir := t.TempDir()
	registers, _ := fakeRevoked(t, dir, nil, errors.New("HTTP 503"))
	warpTunnel(context.Background(), dir)
	warpTunnel(context.Background(), dir)
	if n := registers.Load(); n != 2 {
		t.Fatalf("registered %d times, want 2", n)
	}
}

// A device that could not even be built locally says nothing about the key.
func TestLocalSetupFailureDoesNotRegister(t *testing.T) {
	dir := t.TempDir()
	registers := fakeWarp(t, dir, fmt.Errorf("%w: configure: bad", warp.ErrSetup))
	if _, _, err := warpTunnel(context.Background(), dir); err == nil {
		t.Fatal("tunnel up")
	}
	if n := registers.Load(); n != 0 {
		t.Fatalf("registered %d times, want 0", n)
	}
}

// fakeResolve makes lookupIP answer from ips (err when set), counting calls.
func fakeResolve(t *testing.T, err error, ips ...string) {
	t.Helper()
	old := lookupIP
	t.Cleanup(func() { lookupIP = old })
	lookupIP = func(context.Context, string, string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, err
	}
}

// triedEndpoints runs upAny for an account on hostname h; up fails like
// production: ErrSetup for anything wireguard-go cannot parse, else
// ErrNoAnswer.
func triedEndpoints(t *testing.T, h string) ([]string, error) {
	t.Helper()
	fakeWarp(t, t.TempDir(), nil)
	var tried []string
	up = func(_ context.Context, _ warp.Account, e string) (ace.Dialer, func(), error) {
		tried = append(tried, e)
		if _, err := netip.ParseAddrPort(e); err != nil {
			return nil, nil, fmt.Errorf("%w: configure %s", warp.ErrSetup, e)
		}
		return nil, nil, fmt.Errorf("%w: check: deadline", warp.ErrNoAnswer)
	}
	a := warp.Account{Host: net.JoinHostPort(h, "2408")}
	_, _, err := upAny(context.Background(), t.TempDir(), a, warp.Endpoints(a))
	return tried, err
}

// A hostname endpoint is tried at its addresses, on the same port.
func TestHostnameEndpointResolved(t *testing.T) {
	fakeResolve(t, nil, "162.159.199.9")
	tried, _ := triedEndpoints(t, "engage.example")
	if len(tried) != 12 || tried[8] != "162.159.199.9:2408" || tried[11] != "162.159.199.9:4500" {
		t.Fatalf("tried %v, want the 8 fixed then 162.159.199.9 on 4 ports", tried)
	}
}

// Resolving to an address already listed adds no attempt.
func TestHostnameEndpointCollapsesIntoExisting(t *testing.T) {
	fakeResolve(t, nil, "162.159.192.1")
	if tried, _ := triedEndpoints(t, "engage.example"); len(tried) != 8 {
		t.Fatalf("tried %d endpoints, want 8 (no duplicates): %v", len(tried), tried)
	}
}

// An unresolvable hostname is dropped, not tried to fail locally.
func TestUnresolvedHostnameDropped(t *testing.T) {
	fakeResolve(t, errors.New("no such host"))
	tried, err := triedEndpoints(t, "engage.example")
	if len(tried) != 8 || errors.Is(err, warp.ErrSetup) {
		t.Fatalf("tried %v err %v, want the 8 fixed, no ErrSetup", tried, err)
	}
}

// The new account first tries the endpoint the old one last worked on, and
// is saved when that works.
func TestNewAccountSeededWithProvenEndpoint(t *testing.T) {
	dir := t.TempDir()
	fakeRevoked(t, dir, nil, nil)
	old, _ := warp.LoadAccount(dir)
	old.LastEndpoint = "162.159.193.1:4500"
	if err := warp.SaveAccount(dir, old); err != nil {
		t.Fatal(err)
	}
	inner := up
	var first string
	up = func(ctx context.Context, a warp.Account, e string) (ace.Dialer, func(), error) {
		if a.PrivateKey[0] == 1 && first == "" {
			first = e
		}
		return inner(ctx, a, e)
	}
	if _, _, err := warpTunnel(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if first != "162.159.193.1:4500" {
		t.Fatalf("new account first tried %q, want the old proven endpoint", first)
	}
	if a, _ := warp.LoadAccount(dir); a.PrivateKey[0] != 1 || a.LastEndpoint != "162.159.193.1:4500" {
		t.Fatalf("stored account not the new one at its endpoint (LastEndpoint %q)", a.LastEndpoint)
	}
}

// Production's mix: some endpoints fail locally, the rest never answer. That
// is silence: the new account is tried.
func TestSetupAndNoAnswerMixRegisters(t *testing.T) {
	dir := t.TempDir()
	registers := fakeWarp(t, dir, nil)
	up = func(_ context.Context, _ warp.Account, e string) (ace.Dialer, func(), error) {
		if e[:13] == "162.159.193.1" {
			return nil, nil, fmt.Errorf("%w: configure", warp.ErrSetup)
		}
		return nil, nil, fmt.Errorf("%w: check: deadline", warp.ErrNoAnswer)
	}
	warpTunnel(context.Background(), dir)
	if n := registers.Load(); n != 1 {
		t.Fatalf("registered %d times, want 1", n)
	}
}

// idle makes the tunnel look unused for longer than staleAfter.
func idle() {
	mu.Lock()
	st.usedOK = time.Now().Add(-2 * staleAfter)
	mu.Unlock()
}

// A tunnel that died while the app sat in the background is found out before
// the first dial uses it, not after rebuildAfter failed ones.
func TestIdleDeadTunnelRebuiltBeforeUse(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d, err := Warp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idle()
	f.dead.Store(true)
	if err := dialThrough(t, d, listener(t)); err != nil {
		t.Fatalf("first dial after idling: %v", err)
	}
	if f.ups.Load() != 2 || f.closes.Load() != 1 {
		t.Fatalf("ups=%d closes=%d, want 2 and 1", f.ups.Load(), f.closes.Load())
	}
}

func TestIdleLiveTunnelKept(t *testing.T) {
	f, dir := setup(t)
	Configure("on", dir)
	d, err := Warp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idle()
	addr := listener(t)
	for range 2 { // the second dial is not idle any more: no probe
		if err := dialThrough(t, d, addr); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	n := len(f.addrs)
	f.mu.Unlock()
	if n != 3 || f.ups.Load() != 1 || f.closes.Load() != 0 {
		t.Fatalf("dials+probes=%d ups=%d closes=%d, want 3, 1, 0", n, f.ups.Load(), f.closes.Load())
	}
}

// Automatic falls back to direct when the idle tunnel is dead and cannot be
// rebuilt.
func TestIdleDeadTunnelAutoGoesDirect(t *testing.T) {
	f, dir := setup(t)
	Configure("auto", dir)
	Switched(time.Now())
	if _, err := Warp(context.Background()); err != nil {
		t.Fatal(err)
	}
	idle()
	f.dead.Store(true)
	f.err = errors.New("no network")
	addr := listener(t)
	dialThrough(t, Current(context.Background()), addr) // finds the tunnel dead
	if Current(context.Background()) != Direct() {
		t.Fatal("auto still routes through a dead tunnel")
	}
	if err := dialThrough(t, Dialer(), addr); err != nil {
		t.Fatal(err)
	}
}

// failedFor brings the tunnel up once, failing, and reports for how long the
// failure is remembered.
func failedFor(t *testing.T, on bool) time.Duration {
	t.Helper()
	f, dir := setup(t)
	online = func() bool { return on }
	f.err = errors.New("no endpoint answers")
	Configure("on", dir)
	if _, err := Warp(context.Background()); !errors.Is(err, ErrVPN) {
		t.Fatalf("err %v, want ErrVPN", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return time.Until(st.failUntil)
}

// Without any network (just woken up) a failed bring-up says nothing about
// WARP: it is tried again soon, not answered from memory for failFor.
func TestFailureWithoutNetworkIsSoonForgotten(t *testing.T) {
	if d := failedFor(t, false); d <= 0 || d > downFor {
		t.Fatalf("offline failure remembered for %v, want up to %v", d, downFor)
	}
	if d := failedFor(t, true); d <= downFor {
		t.Fatalf("online failure remembered for %v, want about %v", d, failFor)
	}
}

// setIdleClose changes idleClose under mu: connections closing in the
// background read it there.
func setIdleClose(d time.Duration) {
	mu.Lock()
	idleClose = d
	mu.Unlock()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

// A tunnel nothing uses is closed; an open connection keeps it, and the next
// dial brings a new one up.
func TestIdleTunnelClosedAndReopened(t *testing.T) {
	f, dir := setup(t)
	setIdleClose(30 * time.Millisecond)
	Configure("on", dir)
	d, err := Warp(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	addr := listener(t)
	c, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if f.closes.Load() != 0 {
		t.Fatal("closed under an open connection")
	}
	c.Close()
	c.Close() // a second Close counts nothing
	waitFor(t, "the idle close", func() bool { return f.closes.Load() == 1 })
	if !InUse() {
		t.Fatal("an idle close is not the route going direct")
	}
	if err := dialThrough(t, d, addr); err != nil {
		t.Fatal(err)
	}
	if n := f.ups.Load(); n != 2 {
		t.Fatalf("tunneler called %d times, want 2", n)
	}
}

// Automatic with a remembered switch stays on WARP across an idle close, and
// goes direct only when the tunnel cannot be had again.
func TestIdleCloseAutoStaysOnWarp(t *testing.T) {
	f, dir := setup(t)
	setIdleClose(30 * time.Millisecond)
	Configure("auto", dir)
	Switched(time.Now())
	if _, err := Warp(context.Background()); err != nil { // up and unused
		t.Fatal(err)
	}
	waitFor(t, "the idle close", func() bool { return f.closes.Load() == 1 })
	addr := listener(t)
	if err := dialThrough(t, Current(context.Background()), addr); err != nil {
		t.Fatal(err)
	}
	if f.ups.Load() != 2 || len(f.dialed()) != 1 {
		t.Fatalf("ups=%d dialed=%v: the dial after an idle close did not go through WARP", f.ups.Load(), f.dialed())
	}
	waitFor(t, "the second idle close", func() bool { return f.closes.Load() == 2 })
	f.err = errors.New("no endpoint answers")
	dialThrough(t, Current(context.Background()), addr)
	if Current(context.Background()) != Direct() {
		t.Fatal("auto waits for a tunnel that cannot be had")
	}
}
