package mobile

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"canalpeer.com/core/ace"
)

const testID = "f16d68fa5ff64f3894a45062ca28e4e66f142c6a"

type fakeRunner struct {
	cfg     ace.SwarmConfig
	started chan struct{}
	done    chan struct{}
	h       *harness
}

func (f *fakeRunner) Run(ctx context.Context) error {
	close(f.started)
	<-ctx.Done()
	close(f.done)
	return nil
}

func (f *fakeRunner) Stats() ace.SwarmStats {
	f.h.mu.Lock()
	defer f.h.mu.Unlock()
	return ace.SwarmStats{Connected: 3, Uploaded: f.h.uploaded, Dialed: f.h.dialed, Handshook: f.h.handshook,
		CoordinatorStats: ace.CoordinatorStats{Front: f.h.front, Played: f.h.played, Edge: f.h.front + 10}}
}

// harness fakes the resolver and the swarm, and records how they were used.
type harness struct {
	mu         sync.Mutex
	resolveErr error
	resolves   int
	runners    []*fakeRunner
	front      int64
	played     int64
	uploaded   int64 // every swarm reports this as its Uploaded

	dialed, handshook int64 // every swarm reports these
	direct, warp      *fakeDialer
	warpErr           error // what warp returns instead of h.warp
	warpFails         int   // the first calls fail this many times
	warpDelay         time.Duration
	onWarp            bool
	remembered        bool
	switchAfter       time.Duration // 0: an hour, the rule never fires
	warpCalls         int
	switches          int
	forgets           int
	vias              []ace.Dialer // dialers resolveVia was given
}

// fakeDialer records every dial and connects nowhere.
type fakeDialer struct {
	mu    sync.Mutex
	dials []string // "network address"
}

func (d *fakeDialer) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials = append(d.dials, network+" "+address)
	return nil, errors.New("fake dialer connects nowhere")
}

func (d *fakeDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.dials)
}

func (h *harness) deps(stallAfter, giveUpAfter time.Duration) deps {
	h.direct, h.warp = &fakeDialer{}, &fakeDialer{}
	switchAfter := h.switchAfter
	if switchAfter == 0 {
		switchAfter = time.Hour
	}
	return deps{
		resolve: func(context.Context, string) (ace.Descriptor, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.resolves++
			return ace.Descriptor{Name: "test", PieceLength: 262_144, Bitrate: 1_000_000}, h.resolveErr
		},
		newSwarm: func(cfg ace.SwarmConfig) runner {
			h.mu.Lock()
			defer h.mu.Unlock()
			r := &fakeRunner{cfg: cfg, started: make(chan struct{}), done: make(chan struct{}), h: h}
			h.runners = append(h.runners, r)
			return r
		},
		resolveVia: func(_ context.Context, d ace.Dialer, _ string) (ace.Descriptor, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.vias = append(h.vias, d)
			return ace.Descriptor{Name: "test", PieceLength: 262_144, Bitrate: 1_000_000}, nil
		},
		stallAfter:  stallAfter,
		giveUpAfter: giveUpAfter,
		direct:      h.direct,
		warp: func(ctx context.Context) (ace.Dialer, error) {
			h.mu.Lock()
			h.warpCalls++
			delay := h.warpDelay
			h.mu.Unlock()
			select { // a bring-up takes time
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.warpErr != nil {
				return nil, h.warpErr
			}
			if h.warpCalls <= h.warpFails {
				return nil, errors.New("bring-up failed")
			}
			return h.warp, nil
		},
		onWarp:     func() bool { return h.onWarp },
		remembered: func(time.Time) bool { return h.remembered },
		switched: func(time.Time) {
			h.mu.Lock()
			h.switches++
			h.mu.Unlock()
		},
		forget: func() {
			h.mu.Lock()
			h.forgets++
			h.mu.Unlock()
		},
		switchAfter: switchAfter,
		minDials:    10,
		warpWait:    time.Minute,
		warpRetry:   time.Minute,
	}
}

// runner waits for the i-th swarm to be running.
func (h *harness) runner(t *testing.T, i int) *fakeRunner {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.runners)
		h.mu.Unlock()
		if n > i {
			h.mu.Lock()
			r := h.runners[i]
			h.mu.Unlock()
			<-r.started
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("swarm %d never started", i)
	return nil
}

func waitPhase(t *testing.T, s *Session, phase string) *Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var st *Status
	for time.Now().Before(deadline) {
		if st = s.Status(); st.Phase == phase {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("phase %q never reached, last %+v", phase, st)
	return nil
}

func mustStart(t *testing.T, d deps) *Session {
	t.Helper()
	s, err := start(&Options{ContentID: "acestream://" + testID}, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

func TestSessionPlaysWhenBytesFlow(t *testing.T) {
	h := &harness{}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	r := h.runner(t, 0)
	if r.cfg.Tuning.BufferDepth != 77 {
		t.Errorf("tuning not sized from bitrate: %+v", r.cfg.Tuning)
	}
	resp, err := http.Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r.cfg.Out([]byte("ts"))
	st := waitPhase(t, s, "playing")
	if st.Peers != 3 || st.LagPieces != 10 {
		t.Errorf("status %+v", st)
	}
	if got := readN(t, resp.Body, 2); got != "ts" {
		t.Fatalf("got %q", got)
	}
}

func TestSessionNotFound(t *testing.T) {
	h := &harness{resolveErr: ace.ErrNotFound}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if st := waitPhase(t, s, "failed"); st.Err != "not_found" {
		t.Fatalf("err %q", st.Err)
	}
}

func TestSessionNetworkError(t *testing.T) {
	// No WARP either (Off, or it cannot come up): nothing left to retry.
	h := &harness{resolveErr: errors.New("dial tcp: no route"), warpErr: errors.New("vpn unavailable: off")}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	if st := waitPhase(t, s, "failed"); st.Err != "network" {
		t.Fatalf("err %q", st.Err)
	}
}

func TestSessionGivesUpWithoutBytes(t *testing.T) {
	h := &harness{}
	s := mustStart(t, h.deps(time.Minute, 100*time.Millisecond))
	r := h.runner(t, 0)
	if st := waitPhase(t, s, "failed"); st.Err != "no_peers" {
		t.Fatalf("err %q", st.Err)
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("swarm still running after giving up")
	}
}

// From the last piece played, not the front, which a lying peer can move.
func TestSessionRestartsStalledSwarmFromWhatPlayed(t *testing.T) {
	h := &harness{front: 9999, played: 1234}
	s := mustStart(t, h.deps(100*time.Millisecond, time.Minute))
	r0 := h.runner(t, 0)
	r0.cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	waitPhase(t, s, "reconnecting")
	r1 := h.runner(t, 1)
	<-r0.done
	if r1.cfg.StartPiece != 1234 || r1.cfg.Desc.Name != "test" {
		t.Fatalf("restart config: start %d desc %q", r1.cfg.StartPiece, r1.cfg.Desc.Name)
	}
	r1.cfg.Out([]byte("b"))
	waitPhase(t, s, "playing")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.resolves != 1 {
		t.Fatalf("resolved %d times", h.resolves)
	}
}

func TestSessionNeverGivesUpAfterPlaying(t *testing.T) {
	h := &harness{}
	s := mustStart(t, h.deps(50*time.Millisecond, 100*time.Millisecond))
	h.runner(t, 0).cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st := s.Status(); st.Phase == "failed" {
			t.Fatalf("gave up after playing: %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
	waitPhase(t, s, "reconnecting")
}

func TestStopEndsEverything(t *testing.T) {
	h := &harness{}
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	r := h.runner(t, 0)
	resp, err := http.Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	s.Stop()
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("swarm still running after Stop")
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("body did not end cleanly: %v", err)
	}
	s.Stop()
}

func TestStartRejectsBadID(t *testing.T) {
	h := &harness{}
	if _, err := start(&Options{ContentID: "nope"}, h.deps(time.Minute, time.Minute)); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestStatusReportsQueuedTime(t *testing.T) {
	h := &harness{} // descriptor bitrate is 1,000,000 B/s
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	h.runner(t, 0).cfg.Out(make([]byte, 500_000))
	if st := waitPhase(t, s, "playing"); st.QueuedMs != 500 {
		t.Fatalf("queued %d ms, want 500", st.QueuedMs)
	}
}

func TestSessionSeekReportsDistanceFromLive(t *testing.T) {
	h := &harness{} // 1,000,000 B/s
	s := mustStart(t, h.deps(time.Minute, time.Minute))
	h.runner(t, 0).cfg.Out(make([]byte, 20_000_000)) // 20 s held, nothing read yet
	waitPhase(t, s, "playing")
	if behind := s.Jump(2000, 0); behind != 18_000 {
		t.Fatalf("+2 s from the start: %d ms behind, want 18000", behind)
	}
	if behind := s.Jump(60_000, 0); behind != seekMinBehindMs {
		t.Fatalf("toward live: %d ms behind, want the %d ms margin", behind, seekMinBehindMs)
	}
	// 500 ms shown since that seek reopened the stream, then back 1 s.
	if behind := s.Jump(-1000, 500); behind != seekMinBehindMs+500 {
		t.Fatalf("back 1 s: %d ms behind, want %d", behind, seekMinBehindMs+500)
	}
}

func mustStartIn(t *testing.T, d deps, dataDir string) *Session {
	t.Helper()
	s, err := start(&Options{ContentID: "acestream://" + testID, DataDir: dataDir}, d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// waitHistory polls: the entry is written right after the phase changes.
func waitHistory(t *testing.T, dir string, ids ...string) []historyEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		h := loadHistory(dir)
		got := make([]string, len(h))
		for i, e := range h {
			got[i] = e.ID
		}
		if slices.Equal(got, ids) {
			return h
		}
		if time.Now().After(deadline) {
			t.Fatalf("history %v, want %v", got, ids)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPlayingRecordsHistory(t *testing.T) {
	h, dir := &harness{}, t.TempDir()
	s := mustStartIn(t, h.deps(time.Minute, time.Minute), dir)
	h.runner(t, 0).cfg.Out([]byte("ts"))
	waitPhase(t, s, "playing")
	if e := waitHistory(t, dir, testID); e[0].Name != "test" || e[0].At == 0 {
		t.Fatalf("entry %+v", e[0])
	}
}

func TestFailedLeavesNoHistory(t *testing.T) {
	h, dir := &harness{resolveErr: ace.ErrNotFound}, t.TempDir()
	s := mustStartIn(t, h.deps(time.Minute, time.Minute), dir)
	waitPhase(t, s, "failed")
	if got := History(dir); got != "[]" {
		t.Fatalf("history %s", got)
	}
}

func TestNeverPlayingLeavesNoHistory(t *testing.T) {
	h, dir := &harness{}, t.TempDir()
	s := mustStartIn(t, h.deps(time.Minute, 50*time.Millisecond), dir)
	h.runner(t, 0)
	waitPhase(t, s, "failed")
	if got := History(dir); got != "[]" {
		t.Fatalf("history %s", got)
	}
}

// A stall and a reconnect is the same viewing: the entry is not touched again.
func TestRecordedOncePerSession(t *testing.T) {
	h, dir := &harness{}, t.TempDir()
	s := mustStartIn(t, h.deps(100*time.Millisecond, time.Minute), dir)
	h.runner(t, 0).cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	waitHistory(t, dir, testID)
	mustRecord(t, dir, "other", "", time.Now())
	waitPhase(t, s, "reconnecting")
	h.runner(t, 1).cfg.Out([]byte("b"))
	waitPhase(t, s, "playing")
	time.Sleep(50 * time.Millisecond)
	waitHistory(t, dir, "other", testID)
}

func TestNoDataDirNoHistory(t *testing.T) {
	h := &harness{}
	s := mustStartIn(t, h.deps(time.Minute, time.Minute), "")
	h.runner(t, 0).cfg.Out([]byte("ts"))
	waitPhase(t, s, "playing")
	if got := History(""); got != "[]" {
		t.Fatalf("history %s", got)
	}
}

func TestStatusReportsUploadRate(t *testing.T) {
	h := &harness{}
	s := mustStart(t, h.deps(400*time.Millisecond, time.Minute)) // samples every 100 ms
	h.runner(t, 0)
	deadline := time.Now().Add(3 * time.Second)
	for s.Status().UpBps == 0 {
		if time.Now().After(deadline) {
			t.Fatal("UpBps stayed 0 while the swarm uploaded")
		}
		h.mu.Lock()
		h.uploaded += 1000
		h.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUpMeterRateOverLastTenSeconds(t *testing.T) {
	var m upMeter
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	if m.bps() != 0 {
		t.Fatal("rate before any sample")
	}
	m.sample(at(0), 0)
	m.sample(at(5), 5000)
	if r := m.bps(); r != 1000 {
		t.Fatalf("rate %d, want 1000", r)
	}
	m.sample(at(15), 25000) // the sample at 0 s falls out of the window
	if r := m.bps(); r != 2000 {
		t.Fatalf("rate %d, want 2000", r)
	}
	m.newSwarm() // a restarted swarm counts from 0 again
	m.sample(at(16), 1000)
	if r := m.bps(); r != 1000 {
		t.Fatalf("rate %d after a new swarm, want 1000", r)
	}
	m.sample(at(40), 1000) // nothing uploaded for a while
	if r := m.bps(); r != 0 {
		t.Fatalf("rate %d when idle, want 0", r)
	}
}

// A stall and a reconnect is the same viewing: one play is reported.
func TestPlayedOncePerSession(t *testing.T) {
	h := &harness{}
	d := h.deps(100*time.Millisecond, time.Minute)
	played := make(chan string, 4)
	d.played = func(id string) { played <- id }
	s := mustStart(t, d)
	h.runner(t, 0).cfg.Out([]byte("a"))
	waitPhase(t, s, "playing")
	waitPhase(t, s, "reconnecting")
	h.runner(t, 1).cfg.Out([]byte("b"))
	waitPhase(t, s, "playing")
	s.Stop() // the run loop, which reports plays, has ended
	if got := <-played; got != testID {
		t.Fatalf("played %q, want %q", got, testID)
	}
	if len(played) != 0 {
		t.Fatalf("played %d more times", len(played))
	}
}
