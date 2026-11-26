package main

// The player rules, kept identical to the Android app (Overlay.kt, CutTracker.kt):
// a change in one belongs in the other.

type Overlay int

const (
	OverlayNone Overlay = iota
	OverlayResolving
	OverlaySearching
	OverlayLoading
	OverlayPaused
	OverlayReconnecting
	OverlayFailed
)

// overlayFor is what the screen tells the viewer, from the core phase and the player state.
func overlayFor(phase string, paused, buffering, everReady bool, bufferingMs int64) Overlay {
	switch {
	case phase == "failed":
		return OverlayFailed
	case phase == "resolving":
		return OverlayResolving
	case phase == "searching":
		return OverlaySearching
	case paused:
		return OverlayPaused
	case buffering && !everReady:
		return OverlayLoading
	// Only while the picture is stopped, and not for sub-second hiccups.
	case buffering && (phase == "reconnecting" || bufferingMs > 2000):
		return OverlayReconnecting
	}
	return OverlayNone
}

const stuckMs = 8000

// shouldReopen: measured on a Fire TV, a player can sit stalled for a minute while
// data waits in the core. Reopen only when there is data to read, at most every stuckMs.
func shouldReopen(phase string, queuedMs, stuck, sinceLastReopenMs int64) bool {
	return phase == "playing" && queuedMs > 0 && stuck >= stuckMs && sinceLastReopenMs >= stuckMs
}

type Cut struct {
	StartMs, DurationMs int64
	Reason              string
}

// cutTracker counts what the viewer sees as a cut: playback stopping after it had started.
type cutTracker struct {
	now     func() int64
	n       int
	started bool
	openAt  int64 // -1 when playing
	reason  string
	seeking bool
}

func newCutTracker(now func() int64) *cutTracker { return &cutTracker{now: now, openAt: -1} }

// onSeek: the viewer jumped, so the stall that follows is theirs, not a cut.
func (c *cutTracker) onSeek() { c.seeking = true }

func (c *cutTracker) onBuffering() {
	if !c.seeking {
		c.open("rebuffer")
	}
}

func (c *cutTracker) onError() { c.open("error") }

func (c *cutTracker) onReady() *Cut {
	c.started, c.seeking = true, false
	if c.openAt < 0 {
		return nil
	}
	cut := &Cut{c.openAt, c.now() - c.openAt, c.reason}
	c.openAt = -1
	return cut
}

// openForMs is how long the current cut has lasted, 0 when playing.
func (c *cutTracker) openForMs() int64 {
	if c.openAt < 0 {
		return 0
	}
	return c.now() - c.openAt
}

func (c *cutTracker) count() int { return c.n }

func (c *cutTracker) open(why string) {
	if !c.started || c.openAt >= 0 {
		return
	}
	c.openAt, c.reason = c.now(), why
	c.n++
}
