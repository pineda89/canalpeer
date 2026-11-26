package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"canalpeer.com/core/mobile"
)

type fakeCtl struct {
	mu    sync.Mutex
	cmds  [][]any
	texts []string
	props map[string]any
	ev    chan mpvEvent
}

func newFakeCtl() *fakeCtl {
	return &fakeCtl{props: map[string]any{}, ev: make(chan mpvEvent, 16)}
}

func (f *fakeCtl) command(args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, args)
	return nil
}
func (f *fakeCtl) get(p string) (any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.props[p], nil
}
func (f *fakeCtl) observe(string) error    { return nil }
func (f *fakeCtl) events() <-chan mpvEvent { return f.ev }
func (f *fakeCtl) showText(s string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, s)
	return nil
}

// count returns how many commands start with name (and, if given, arg).
func (f *fakeCtl) count(name string, arg ...any) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.cmds {
		if c[0] == name && (len(arg) == 0 || (len(c) > 1 && c[1] == arg[0])) {
			n++
		}
	}
	return n
}

func (f *fakeCtl) lastText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.texts) == 0 {
		return ""
	}
	return f.texts[len(f.texts)-1]
}

type fakeSession struct {
	mu      sync.Mutex
	st      mobile.Status
	jumps   [][2]int64
	stopped int
}

func (s *fakeSession) Status() *mobile.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	return &st
}
func (s *fakeSession) URL() string { return "http://127.0.0.1:1/stream" }
func (s *fakeSession) Jump(d, shown int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jumps = append(s.jumps, [2]int64{d, shown})
	return 20_000
}
func (s *fakeSession) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped++
}
func (s *fakeSession) set(phase string, queued int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Phase, s.st.QueuedMs = phase, queued
}

type rig struct {
	t     *testing.T
	ctl   *fakeCtl
	sess  *fakeSession
	p     *player
	ticks chan time.Time
	clk   *clock
	done  chan struct{}
	log   string
	dir   string   // settings
	vpns  []string // modes handed to the core
}

const link = "acestream://f16d68fa5ff64f3894a45062ca28e4e66f142c6a"

func newRig(t *testing.T) *rig {
	r := &rig{t: t, ctl: newFakeCtl(), sess: &fakeSession{}, ticks: make(chan time.Time), clk: &clock{}, done: make(chan struct{})}
	r.log = filepath.Join(t.TempDir(), "cuts.jsonl")
	r.p = newPlayer(r.ctl, func(string) (session, error) { return r.sess, nil }, r.log)
	r.p.now = r.clk.now
	r.p.ticks = r.ticks
	r.dir = t.TempDir()
	r.p.settingsDir, r.p.vpn = r.dir, "auto"
	r.p.setVpn = func(m string) { r.vpns = append(r.vpns, m) }
	go func() { r.p.run(context.Background()); close(r.done) }()
	r.p.play(link)
	return r
}

// tick is sent twice: the second can only be taken once the first is handled.
func (r *rig) tick()            { r.ticks <- time.Time{}; r.ticks <- time.Time{} }
func (r *rig) event(e mpvEvent) { r.ctl.ev <- e; r.tick() } // the tick orders the event before the next check
func (r *rig) prop(name string, v any) {
	r.event(mpvEvent{Name: "property-change", Property: name, Data: v})
}
func (r *rig) key(k string) {
	r.event(mpvEvent{Name: "client-message", Args: []string{"canalpeer", k}})
}

// playing brings the rig to a loaded, playing stream.
func (r *rig) playing() {
	r.sess.set("playing", 3000)
	r.tick()
	r.event(mpvEvent{Name: "playback-restart"})
}

func (r *rig) setProp(name string, v any) {
	r.ctl.mu.Lock()
	r.ctl.props[name] = v
	r.ctl.mu.Unlock()
}

func TestLoadsWhenCoreStartsPlaying(t *testing.T) {
	r := newRig(t)
	r.sess.set("searching", 0)
	r.tick()
	if n := r.ctl.count("loadfile"); n != 0 {
		t.Fatalf("loaded while searching: %d", n)
	}
	r.sess.set("playing", 3000)
	r.tick()
	r.tick()
	if n := r.ctl.count("loadfile", r.sess.URL()); n != 1 {
		t.Fatalf("loadfile %d times", n)
	}
}

func TestBackJumpsAndReloads(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.setProp("time-pos", 12.0)
	r.key("back")
	if len(r.sess.jumps) != 1 || r.sess.jumps[0] != [2]int64{-5000, 12000} {
		t.Fatalf("jumps %v", r.sess.jumps)
	}
	if n := r.ctl.count("loadfile"); n != 2 {
		t.Fatalf("loadfile %d", n)
	}
	if txt := r.ctl.lastText(); txt != "−20 s" {
		t.Fatalf("text %q", txt)
	}
}

func TestEscLeavesFullscreenFirst(t *testing.T) {
	r := newRig(t)
	r.setProp("fullscreen", true)
	r.key("esc")
	if r.ctl.count("set", "fullscreen") != 1 || r.ctl.count("quit") != 0 {
		t.Fatalf("cmds %v", r.ctl.cmds)
	}
	r.setProp("fullscreen", false)
	r.key("esc")
	if r.ctl.count("quit", mpvBackCode) != 1 {
		t.Fatalf("no quit back to the launcher: %v", r.ctl.cmds)
	}
}

func TestStopKeyQuitsBack(t *testing.T) {
	r := newRig(t)
	r.key("stop")
	if r.ctl.count("quit", mpvBackCode) != 1 {
		t.Fatalf("stop did not go back: %v", r.ctl.cmds)
	}
}

func readLog(t *testing.T, path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestStallIsLoggedAsCut(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.prop("paused-for-cache", true)
	r.clk.add(3000)
	r.prop("paused-for-cache", false)
	lines := readLog(t, r.log)
	if len(lines) != 1 || !strings.Contains(lines[0], `"reason":"rebuffer"`) || !strings.Contains(lines[0], `"ms":3000`) {
		t.Fatalf("log %q", lines)
	}
}

func TestSeekIsNotACut(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.key("forward")
	r.prop("paused-for-cache", true)
	r.prop("paused-for-cache", false)
	if lines := readLog(t, r.log); len(lines) != 0 {
		t.Fatalf("seek logged as cut: %q", lines)
	}
}

func TestWatchdogReopensWithQueuedData(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.setProp("time-pos", 30.0)
	r.prop("paused-for-cache", true)
	r.clk.add(8000)
	r.tick()
	if len(r.sess.jumps) != 1 || r.sess.jumps[0] != [2]int64{0, 30000} || r.ctl.count("loadfile") != 2 {
		t.Fatalf("jumps %v loads %d", r.sess.jumps, r.ctl.count("loadfile"))
	}
}

func TestPlayerStopsSessionWhenMpvExits(t *testing.T) {
	r := newRig(t)
	r.playing()
	close(r.ctl.ev)
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return")
	}
	if r.sess.stopped != 1 {
		t.Fatalf("stopped %d times", r.sess.stopped)
	}
}

func TestNewLinkReplacesTheSession(t *testing.T) {
	r := newRig(t)
	r.playing()
	first := r.sess
	r.sess = &fakeSession{}
	r.p.play("acestream://02be332ebead0484a5354fa25e97b6833b8b129e")
	r.tick()
	if first.stopped != 1 {
		t.Fatalf("old session stopped %d times", first.stopped)
	}
	r.sess.set("playing", 1000)
	r.tick()
	if n := r.ctl.count("loadfile"); n != 2 {
		t.Fatalf("loadfile %d", n)
	}
}

func TestBadLinkSaysSo(t *testing.T) {
	r := newRig(t)
	r.p.play("acestream://nope")
	r.tick()
	if txt := r.ctl.lastText(); !strings.Contains(txt, "This link is not valid.") {
		t.Fatalf("text %q", txt)
	}
}

// Review: a dropped fullscreen event must not turn Esc into quit.
func TestEscAsksMpvForFullscreen(t *testing.T) {
	r := newRig(t)
	r.setProp("fullscreen", true) // no property-change event arrived
	r.key("esc")
	if r.ctl.count("set", "fullscreen") != 1 || r.ctl.count("quit") != 0 {
		t.Fatalf("cmds %v", r.ctl.cmds)
	}
}

// Review: a stale time-pos must not end a stall before the picture moves again.
func TestStaleTimePosDoesNotEndAStall(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.prop("paused-for-cache", true)
	r.prop("time-pos", 40.0)
	r.clk.add(9000)
	r.tick()
	if len(r.sess.jumps) != 1 {
		t.Fatalf("stall ended by a stale time-pos: jumps %v", r.sess.jumps)
	}
}

// Review: mpv ending the file mid-stream must reopen, not freeze on the last frame.
func TestEndOfFileReopens(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.event(mpvEvent{Name: "end-file", Reason: "eof"})
	r.tick()
	// one "set pause no" when the channel started, one to recover
	if r.ctl.count("set", "pause") != 2 || len(r.sess.jumps) != 1 || r.ctl.count("loadfile") != 2 {
		t.Fatalf("cmds %v jumps %v", r.ctl.cmds, r.sess.jumps)
	}
	if lines := readLog(t, r.log); len(lines) != 0 {
		t.Fatalf("logged before recovery: %q", lines) // the cut is logged when playback restarts
	}
	r.event(mpvEvent{Name: "playback-restart"})
	if lines := readLog(t, r.log); len(lines) != 1 || !strings.Contains(lines[0], `"reason":"error"`) {
		t.Fatalf("log %q", lines)
	}
}

// Our own loadfile ends the previous file with reason "stop": not an error.
func TestOwnReloadIsNotAnError(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.event(mpvEvent{Name: "end-file", Reason: "stop"})
	r.tick()
	if len(r.sess.jumps) != 0 {
		t.Fatalf("reopened on our own stop: %v", r.sess.jumps)
	}
}

// Review: clicking the channel already playing must not restart it.
func TestSameLinkDoesNotRestart(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.p.play(strings.ToUpper(link))
	r.tick()
	if r.sess.stopped != 0 || r.ctl.count("loadfile") != 1 {
		t.Fatalf("restarted: stopped %d loads %d", r.sess.stopped, r.ctl.count("loadfile"))
	}
}

// A failed channel is over: the same link again is the viewer retrying.
func TestSameLinkRetriesAFailedChannel(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.sess.mu.Lock()
	r.sess.st.Phase = "failed"
	r.sess.mu.Unlock()
	r.tick()
	r.p.play(link)
	r.tick()
	if r.sess.stopped != 1 {
		t.Fatalf("stopped %d: the failed session was kept", r.sess.stopped)
	}
}

func (s *fakeSession) movie() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Vod = true
}

func TestMovieSeeksInMpv(t *testing.T) {
	r := newRig(t)
	r.sess.movie()
	r.playing()
	for k, want := range map[string]int{"back": -5, "forward": 5, "back60": -60, "forward60": 60} {
		r.key(k)
		if r.ctl.count("seek", want) != 1 {
			t.Fatalf("%s: cmds %v", k, r.ctl.cmds)
		}
	}
	if len(r.sess.jumps) != 0 || r.ctl.count("loadfile") != 1 {
		t.Fatalf("a movie seeks in mpv, not by reopening: jumps %v", r.sess.jumps)
	}
}

func TestMovieEndGoesBack(t *testing.T) {
	r := newRig(t)
	r.sess.movie()
	r.playing()
	// --keep-open: mpv pauses on the last frame and only says eof-reached.
	r.prop("eof-reached", true)
	if r.ctl.count("quit", mpvBackCode) != 1 || r.ctl.count("loadfile") != 1 {
		t.Fatalf("the end of a movie goes back to the launcher: %v", r.ctl.cmds)
	}
}

func TestMovieErrorResumesWhereItWas(t *testing.T) {
	r := newRig(t)
	r.sess.movie()
	r.playing()
	r.setProp("time-pos", 600.0)
	r.event(mpvEvent{Name: "end-file", Reason: "error"})
	r.ctl.mu.Lock()
	last := r.ctl.cmds[len(r.ctl.cmds)-1]
	r.ctl.mu.Unlock()
	if last[0] != "loadfile" || len(last) != 5 || last[4] != "start=600.000" || len(r.sess.jumps) != 0 {
		t.Fatalf("reload %v, jumps %v", last, r.sess.jumps)
	}
}

func TestNoSpaceSaysSo(t *testing.T) {
	r := newRig(t)
	r.sess.mu.Lock()
	r.sess.st.Phase, r.sess.st.Err = "failed", "no_space"
	r.sess.mu.Unlock()
	r.tick()
	r.tick()
	if !strings.HasPrefix(r.ctl.lastText(), txtNoSpace) {
		t.Fatalf("text %q", r.ctl.lastText())
	}
}

// Every channel or movie starts on its own default audio track.
func TestNewLinkStartsOnDefaultAudio(t *testing.T) {
	r := newRig(t)
	r.tick()
	if r.ctl.count("set", "aid") != 1 {
		t.Fatalf("cmds %v", r.ctl.cmds)
	}
	r.p.play("acestream://0000000000000000000000000000000000000009")
	r.tick()
	if r.ctl.count("set", "aid") != 2 {
		t.Fatalf("second link kept the last channel's track: %v", r.ctl.cmds)
	}
}

func TestAudioKeyCyclesWithMessage(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.key("audio")
	if r.ctl.count("osd-msg") != 1 {
		t.Fatalf("cmds %v", r.ctl.cmds)
	}
}

// The panel's choice is saved, handed to the core, and the same channel
// starts again on the new route.
func TestPlayerVpnMessage(t *testing.T) {
	r := newRig(t)
	r.playing()
	first := r.sess
	r.sess = &fakeSession{}
	r.key("vpn on")
	r.tick()
	if s := loadSettings(r.dir); s.Vpn != "on" {
		t.Fatalf("saved %+v", s)
	}
	if len(r.vpns) != 1 || r.vpns[0] != "on" {
		t.Fatalf("core told %v", r.vpns)
	}
	if first.stopped != 1 {
		t.Fatalf("old session stopped %d times", first.stopped)
	}
	r.sess.set("playing", 1000)
	r.tick()
	if n := r.ctl.count("loadfile"); n != 2 {
		t.Fatalf("loadfile %d: the channel did not start again", n)
	}
	if r.ctl.count("script-message", "canalpeer-vpn") == 0 {
		t.Fatalf("the controls were not told the mode: %v", r.ctl.cmds)
	}
	r.key("vpn on") // unchanged: nothing restarts
	if len(r.vpns) != 1 || r.sess.stopped != 0 {
		t.Fatalf("same mode re-fired: %v, stopped %d", r.vpns, r.sess.stopped)
	}
}

func TestPlayerShowsViaWarp(t *testing.T) {
	r := newRig(t)
	r.sess.mu.Lock()
	r.sess.st.Phase, r.sess.st.Outbound, r.sess.st.Peers = "searching", "warp", 2
	r.sess.mu.Unlock()
	r.tick()
	r.tick()
	if txt := r.ctl.lastText(); !strings.Contains(txt, "via WARP") {
		t.Fatalf("text %q", txt)
	}
	r.sess.mu.Lock()
	r.sess.st.Phase, r.sess.st.Err = "failed", "vpn"
	r.sess.mu.Unlock()
	r.tick()
	if txt := r.ctl.lastText(); !strings.HasPrefix(txt, txtVpnFailed) {
		t.Fatalf("text %q", txt)
	}
}

func TestInfoShowsUploadAndRoute(t *testing.T) {
	r := newRig(t)
	r.playing()
	r.sess.mu.Lock()
	r.sess.st.UpBps, r.sess.st.Outbound = 100_000, "warp"
	r.sess.mu.Unlock()
	r.key("info")
	if txt := r.ctl.lastText(); !strings.Contains(txt, "↑ 0.8 Mbps") || !strings.Contains(txt, "via WARP") {
		t.Fatalf("text %q", txt)
	}
}

// The controls ask for the mode when they load: a message sent before their
// handler existed is lost, and the app only sends changes.
func TestPlayerResendsVpnOnRequest(t *testing.T) {
	r := newRig(t)
	r.tick()
	before := r.ctl.count("script-message", "canalpeer-vpn")
	if before == 0 {
		t.Fatalf("the controls were never told the mode: %v", r.ctl.cmds)
	}
	r.key("vpn-state")
	if n := r.ctl.count("script-message", "canalpeer-vpn"); n != before+1 {
		t.Fatalf("told the mode %d times after the request, want %d", n, before+1)
	}
}
