package main

import (
	"sync/atomic"
	"testing"
)

// Ported case by case from the Android app's OverlayTest and CutTrackerTest.

func TestOverlayReconnectingIsHiddenWhileVideoPlays(t *testing.T) {
	if got := overlayFor("reconnecting", false, false, true, 0); got != OverlayNone {
		t.Errorf("not buffering: %v", got)
	}
	if got := overlayFor("reconnecting", false, true, true, 100); got != OverlayReconnecting {
		t.Errorf("buffering: %v", got)
	}
}

func TestOverlaySubSecondHiccupIsNotAnnounced(t *testing.T) {
	if got := overlayFor("playing", false, true, true, 500); got != OverlayNone {
		t.Errorf("500 ms: %v", got)
	}
	if got := overlayFor("playing", false, true, true, 2500); got != OverlayReconnecting {
		t.Errorf("2500 ms: %v", got)
	}
}

func TestOverlayStartupAndPause(t *testing.T) {
	if got := overlayFor("playing", false, true, false, 9000); got != OverlayLoading {
		t.Errorf("startup: %v", got)
	}
	if got := overlayFor("playing", true, false, true, 0); got != OverlayPaused {
		t.Errorf("paused: %v", got)
	}
	if got := overlayFor("failed", false, false, false, 0); got != OverlayFailed {
		t.Errorf("failed: %v", got)
	}
}

func TestReopenOnlyWhenDataIsWaiting(t *testing.T) {
	cases := []struct {
		phase                    string
		queued, stuck, sinceLast int64
		want                     bool
	}{
		{"playing", 3000, 8000, 60_000, true},
		{"playing", 0, 20_000, 60_000, false},
		{"reconnecting", 3000, 20_000, 60_000, false},
		{"playing", 3000, 7000, 60_000, false},
		{"playing", 3000, 20_000, 2000, false},
	}
	for _, c := range cases {
		if got := shouldReopen(c.phase, c.queued, c.stuck, c.sinceLast); got != c.want {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

// clock is a fake time source; atomic because players read it from their own goroutine.
type clock struct{ v atomic.Int64 }

func (c *clock) now() int64   { return c.v.Load() }
func (c *clock) add(ms int64) { c.v.Add(ms) }
func (c *clock) set(ms int64) { c.v.Store(ms) }

func TestBufferingBeforeFirstReadyIsNotACut(t *testing.T) {
	c := &clock{}
	tr := newCutTracker(c.now)
	tr.onBuffering()
	if cut := tr.onReady(); cut != nil || tr.count() != 0 {
		t.Fatalf("cut %v count %d", cut, tr.count())
	}
}

func TestRebufferIsOneCutWithDuration(t *testing.T) {
	c := &clock{}
	tr := newCutTracker(c.now)
	tr.onReady()
	c.set(1000)
	tr.onBuffering()
	c.set(2000)
	tr.onBuffering()
	c.set(4000)
	cut := tr.onReady()
	if cut == nil || *cut != (Cut{1000, 3000, "rebuffer"}) || tr.count() != 1 {
		t.Fatalf("cut %+v count %d", cut, tr.count())
	}
}

func TestErrorAfterReadyCountsOnce(t *testing.T) {
	tr := newCutTracker((&clock{}).now)
	tr.onReady()
	tr.onError()
	tr.onBuffering()
	if cut := tr.onReady(); cut == nil || cut.Reason != "error" || tr.count() != 1 {
		t.Fatalf("cut %+v count %d", cut, tr.count())
	}
}

func TestReadyWithoutCutReturnsNil(t *testing.T) {
	tr := newCutTracker((&clock{}).now)
	tr.onReady()
	if cut := tr.onReady(); cut != nil {
		t.Fatalf("cut %+v", cut)
	}
}

func TestOpenForMeasuresOnlyAnOpenCut(t *testing.T) {
	c := &clock{}
	tr := newCutTracker(c.now)
	tr.onReady()
	if tr.openForMs() != 0 {
		t.Fatal("open while playing")
	}
	c.set(1000)
	tr.onBuffering()
	c.set(9500)
	if got := tr.openForMs(); got != 8500 {
		t.Fatalf("open %d", got)
	}
	tr.onReady()
	if tr.openForMs() != 0 {
		t.Fatal("still open")
	}
}

func TestASeekIsNotACut(t *testing.T) {
	tr := newCutTracker((&clock{}).now)
	tr.onReady()
	tr.onSeek()
	tr.onBuffering()
	if cut := tr.onReady(); cut != nil || tr.count() != 0 {
		t.Fatalf("seek counted: %+v", cut)
	}
	tr.onBuffering() // the next stall is real again
	tr.onReady()
	if tr.count() != 1 {
		t.Fatalf("count %d", tr.count())
	}
}
