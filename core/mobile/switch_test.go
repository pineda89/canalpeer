package mobile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"canalpeer.com/core/ace"
	"canalpeer.com/core/route"
)

// blocked is a harness whose swarms dial peers and never complete a handshake.
func blocked() *harness {
	return &harness{dialed: 30, switchAfter: 50 * time.Millisecond}
}

func (h *harness) counts() (warpCalls, switches, forgets int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.warpCalls, h.switches, h.forgets
}

func (h *harness) swarms() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runners)
}

// via says which fake dialer a swarm was given.
func (h *harness) via(t *testing.T, r *fakeRunner) string {
	t.Helper()
	switch r.cfg.Dialer {
	case h.direct:
		return "direct"
	case h.warp:
		return "warp"
	}
	t.Fatalf("swarm dialer %v is neither fake", r.cfg.Dialer)
	return ""
}

// noMoreSwarms checks that the session keeps the n swarms it has.
func noMoreSwarms(t *testing.T, h *harness, n int) {
	t.Helper()
	time.Sleep(300 * time.Millisecond)
	if got := h.swarms(); got != n {
		t.Fatalf("%d swarms, want %d", got, n)
	}
}

func TestAutoSwitchesWhenNothingHandshakes(t *testing.T) {
	h := blocked()
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if v := h.via(t, h.runner(t, 0)); v != "direct" {
		t.Fatalf("first swarm via %s", v)
	}
	r1 := h.runner(t, 1)
	if v := h.via(t, r1); v != "warp" {
		t.Fatalf("second swarm via %s", v)
	}
	if _, switches, _ := h.counts(); switches != 1 {
		t.Fatalf("switched %d times", switches)
	}
	if st := s.Status(); st.Outbound != "warp" || st.Phase != "searching" {
		t.Fatalf("status %+v", st)
	}
}

func TestAutoDoesNotSwitchDeadChannel(t *testing.T) {
	h := &harness{switchAfter: 50 * time.Millisecond} // nothing dialed: no peers listed
	s := mustStart(t, h.deps(time.Minute, 300*time.Millisecond))
	h.runner(t, 0)
	if st := waitPhase(t, s, "failed"); st.Err != "no_peers" {
		t.Fatalf("err %q", st.Err)
	}
	if warpCalls, _, _ := h.counts(); warpCalls != 0 || h.swarms() != 1 {
		t.Fatalf("warp asked %d times, %d swarms", warpCalls, h.swarms())
	}
}

func TestAutoDoesNotSwitchWhenHandshakes(t *testing.T) {
	h := blocked()
	h.handshook = 2
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	h.runner(t, 0)
	noMoreSwarms(t, h, 1)
	if warpCalls, _, _ := h.counts(); warpCalls != 0 || s.Status().Outbound != "direct" {
		t.Fatalf("warp asked %d times, status %+v", warpCalls, s.Status())
	}
}

func TestSwitchOnReconnectAfterPlaying(t *testing.T) {
	h := blocked()
	s := mustStart(t, h.deps(200*time.Millisecond, time.Minute))
	h.runner(t, 0).cfg.Out([]byte("a")) // playing: this swarm never meets the rule
	waitPhase(t, s, "playing")
	waitPhase(t, s, "reconnecting")
	if v := h.via(t, h.runner(t, 1)); v != "direct" {
		t.Fatalf("reconnect via %s", v)
	}
	if v := h.via(t, h.runner(t, 2)); v != "warp" {
		t.Fatalf("after the rule via %s", v)
	}
	if _, switches, _ := h.counts(); switches != 1 {
		t.Fatalf("switched %d times", switches)
	}
}

func TestSwitchedSessionStaysOnWarp(t *testing.T) {
	h := blocked()
	s := mustStart(t, h.deps(200*time.Millisecond, time.Minute))
	r1 := h.runner(t, 1)
	r1.cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	waitPhase(t, s, "reconnecting")
	if v := h.via(t, h.runner(t, 2)); v != "warp" {
		t.Fatalf("reconnect after the switch via %s", v)
	}
	if _, switches, _ := h.counts(); switches != 1 {
		t.Fatalf("switched %d times", switches)
	}
}

func TestRememberedStartsOnWarpAndFallsBackOnce(t *testing.T) {
	h := blocked()
	h.onWarp, h.remembered = true, true
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if v := h.via(t, h.runner(t, 0)); v != "warp" {
		t.Fatalf("remembered session starts via %s", v)
	}
	if v := h.via(t, h.runner(t, 1)); v != "direct" {
		t.Fatalf("way back via %s", v)
	}
	if _, switches, forgets := h.counts(); forgets != 1 || switches != 0 {
		t.Fatalf("forgot %d times, switched %d", forgets, switches)
	}
	if st := s.Status(); st.Outbound != "direct" {
		t.Fatalf("status %+v", st)
	}
	noMoreSwarms(t, h, 2) // the direct swarm meets the rule too: no going back
}

func TestOnModeFailsWithVpnErr(t *testing.T) {
	h := &harness{onWarp: true, warpErr: fmt.Errorf("%w: no endpoint", route.ErrVPN)}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if st := waitPhase(t, s, "failed"); st.Err != "vpn" {
		t.Fatalf("err %q", st.Err)
	}
	if h.swarms() != 0 {
		t.Fatal("On went direct")
	}
}

func TestAutoWarpUnavailableStaysDirect(t *testing.T) {
	h := blocked()
	h.warpErr = fmt.Errorf("%w: no endpoint", route.ErrVPN)
	s := mustStart(t, h.deps(200*time.Millisecond, time.Minute))
	r0 := h.runner(t, 0)
	noMoreSwarms(t, h, 1) // the direct swarm keeps going
	r0.cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	waitPhase(t, s, "reconnecting")
	if v := h.via(t, h.runner(t, 1)); v != "direct" {
		t.Fatalf("reconnect via %s", v)
	}
	if _, switches, _ := h.counts(); switches != 0 {
		t.Fatalf("switched %d times", switches)
	}
	if st := s.Status(); st.Err != "" || st.Outbound != "direct" {
		t.Fatalf("status %+v", st)
	}
}

func TestResolverNetworkErrorRetriesThroughWarpOnly(t *testing.T) {
	h := &harness{resolveErr: errors.New("dial tcp: blocked")}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if v := h.via(t, h.runner(t, 0)); v != "direct" {
		t.Fatalf("swarm via %s", v)
	}
	h.mu.Lock()
	vias := slices.Clone(h.vias)
	h.mu.Unlock()
	if len(vias) != 1 || vias[0] != h.warp {
		t.Fatalf("AceStream hosts retried with %v", vias)
	}
	if _, switches, _ := h.counts(); switches != 0 || s.Status().Outbound != "direct" {
		t.Fatalf("switched %d times, status %+v", switches, s.Status())
	}
}

func TestResolverNotFoundIsNotRetried(t *testing.T) {
	h := &harness{resolveErr: ace.ErrNotFound}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	waitPhase(t, s, "failed")
	if warpCalls, _, _ := h.counts(); warpCalls != 0 {
		t.Fatalf("warp asked %d times", warpCalls)
	}
}

func TestMovieInUseDisablesLeaks(t *testing.T) {
	info := metainfo.Info{Name: "m.mkv", PieceLength: 256 << 10, Length: 1 << 20, Pieces: make([]byte, 80)}
	mi := metainfo.MetaInfo{AnnounceList: [][]string{{"http://192.0.2.1:6969/announce", "udp://192.0.2.2:6969/announce"}}}
	mi.InfoBytes, _ = bencode.Marshal(info)
	raw, _ := bencode.Marshal(mi)

	h := &harness{onWarp: true}
	d := h.deps(time.Minute, time.Minute)
	d.resolve = func(context.Context, string) (ace.Descriptor, error) { return ace.Descriptor{Torrent: raw}, nil }
	var cfg torrent.ClientConfig
	d.clientConfig = func(c *torrent.ClientConfig) { cfg = *c }
	d.onTorrent = func(t *torrent.Torrent) {
		t.AddPeers([]torrent.PeerInfo{{Addr: net.TCPAddrFromAddrPort(netip.MustParseAddrPort("192.0.2.3:6881"))}})
	}
	s, err := start(&Options{ContentID: testID, CacheDir: t.TempDir()}, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	waitPhase(t, s, "searching")
	if !cfg.NoDHT || !cfg.DisableUTP || cfg.AcceptPeerConnections || !cfg.NoDefaultPortForwarding || !cfg.DisableTCP {
		t.Fatalf("leaky config: dht off %v, utp off %v, accepts %v, no port forwarding %v, no tcp listener %v",
			cfg.NoDHT, cfg.DisableUTP, cfg.AcceptPeerConnections, cfg.NoDefaultPortForwarding, cfg.DisableTCP)
	}
	want := []string{"tcp 192.0.2.1:6969", "udp 192.0.2.2:6969", "tcp 192.0.2.3:6881"}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := h.warp.dialed()
		missing := slices.DeleteFunc(slices.Clone(want), func(w string) bool { return slices.Contains(got, w) })
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not dialed through WARP: %v (dialed %v)", missing, got)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := h.direct.dialed(); len(got) != 0 {
		t.Fatalf("dialed direct: %v", got)
	}
	if st := s.Status(); st.Outbound != "warp" {
		t.Fatalf("status %+v", st)
	}
}

// The bring-up runs beside the direct swarm: if that swarm starts working
// meanwhile, it is kept and nothing is switched.
func TestSlowBringUpKeepsADirectSwarmThatStartsWorking(t *testing.T) {
	h := blocked()
	h.warpDelay = time.Second
	s := mustStart(t, h.deps(400*time.Millisecond, time.Minute))
	r0 := h.runner(t, 0)
	deadline := time.Now().Add(2 * time.Second)
	for warpCalls, _, _ := h.counts(); warpCalls == 0; warpCalls, _, _ = h.counts() {
		if time.Now().After(deadline) {
			t.Fatal("WARP never asked for")
		}
		time.Sleep(5 * time.Millisecond)
	}
	asked := time.Now()
	h.mu.Lock()
	h.handshook = 1
	h.mu.Unlock()
	// Video keeps flowing past WARP's answer: the swarm is still watched when
	// it arrives, so only the re-check keeps it.
	flowing := make(chan struct{})
	defer close(flowing)
	go func() {
		for {
			r0.cfg.Out([]byte("a"))
			select {
			case <-flowing:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	waitPhase(t, s, "playing")
	if d := time.Since(asked); d >= h.warpDelay/2 {
		t.Fatalf("playing only %s after WARP was asked for: the bring-up blocked the watch", d)
	}
	time.Sleep(h.warpDelay + 300*time.Millisecond)
	if _, switches, _ := h.counts(); switches != 0 {
		t.Fatalf("switched %d times away from a working direct swarm", switches)
	}
	if n := h.swarms(); n != 1 {
		t.Fatalf("%d swarms: the working direct swarm was replaced", n)
	}
}

// The way back needs no dials: through WARP the trackers may give nothing.
func TestRememberedFallsBackWithoutDials(t *testing.T) {
	h := &harness{onWarp: true, remembered: true, switchAfter: 50 * time.Millisecond}
	mustStart(t, h.deps(time.Minute, time.Minute))
	if v := h.via(t, h.runner(t, 0)); v != "warp" {
		t.Fatalf("remembered session starts via %s", v)
	}
	if v := h.via(t, h.runner(t, 1)); v != "direct" {
		t.Fatalf("way back via %s", v)
	}
	if _, _, forgets := h.counts(); forgets != 1 {
		t.Fatalf("forgot %d times", forgets)
	}
}

// The way back is for a start that does not work: once it played on WARP, a
// later reconnect stays on WARP.
func TestRememberedSessionThatPlayedStaysOnWarp(t *testing.T) {
	h := blocked()
	h.onWarp, h.remembered = true, true
	h.handshook = 1
	s := mustStart(t, h.deps(200*time.Millisecond, time.Minute))
	h.runner(t, 0).cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	h.mu.Lock()
	h.handshook = 0
	h.mu.Unlock()
	waitPhase(t, s, "reconnecting")
	h.runner(t, 1)
	time.Sleep(400 * time.Millisecond)
	if _, _, forgets := h.counts(); forgets != 0 {
		t.Fatalf("forgot %d times", forgets)
	}
	h.mu.Lock()
	runners := slices.Clone(h.runners)
	h.mu.Unlock()
	for i, r := range runners {
		if v := h.via(t, r); v != "warp" {
			t.Fatalf("swarm %d via %s", i, v)
		}
	}
}

// On gives up on a tunnel that does not come up within warpWait: a network
// that drops WARP's UDP would otherwise hold the player for minutes.
func TestOnModeGivesUpOnSlowBringUp(t *testing.T) {
	h := &harness{onWarp: true, warpDelay: time.Hour}
	d := h.deps(time.Minute, time.Minute)
	d.warpWait = 100 * time.Millisecond
	s := mustStart(t, d)
	if st := waitPhase(t, s, "failed"); st.Err != "vpn" {
		t.Fatalf("err %q", st.Err)
	}
	if h.swarms() != 0 {
		t.Fatal("On went direct")
	}
}

// A bring-up that failed once is tried again while the first swarm still has
// nothing: that swarm never stalls, so no later swarm would ask.
func TestFailedBringUpRetriedWhileNothingWorks(t *testing.T) {
	h := blocked()
	h.warpFails = 1
	d := h.deps(400*time.Millisecond, time.Minute) // ticks every 100 ms
	d.warpRetry = 200 * time.Millisecond
	s := mustStart(t, d)
	if v := h.via(t, h.runner(t, 0)); v != "direct" {
		t.Fatalf("first swarm via %s", v)
	}
	if v := h.via(t, h.runner(t, 1)); v != "warp" {
		t.Fatalf("second swarm via %s", v)
	}
	if warpCalls, switches, _ := h.counts(); warpCalls != 2 || switches != 1 {
		t.Fatalf("warp asked %d times, switched %d", warpCalls, switches)
	}
	if st := s.Status(); st.Outbound != "warp" {
		t.Fatalf("status %+v", st)
	}
}
