package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"canalpeer.com/core/mobile"
)

// The same copy as the Android app's strings.xml: change both together.
const (
	txtIdle         = "Open an acestream:// link from your browser,\nor press Ctrl+V / ⌘V to paste one."
	txtResolving    = "Finding the channel…"
	txtSearching    = "Looking for peers… (%d connected)"
	txtLoading      = "Loading…"
	txtReconnecting = "Reconnecting… (%d connected)"
	txtPaused       = "Paused · press Space to resume"
	txtNotFound     = "This channel does not exist or is no longer broadcasting."
	txtNoNetwork    = "No internet connection."
	txtNoPeers      = "Nobody is broadcasting this channel right now."
	txtNoSpace      = "Not enough free disk space to play this movie."
	txtBadLink      = "This link is not valid."
	txtPressBack    = "Press Esc to go back, or open another link."
	txtInfo         = "%d connected · %d cuts · %d s buffered · v%s"
	txtLive         = "Live"
	txtBehind       = "−%d s"
	txtVpnFailed    = "WARP could not connect. Try Automatic or Off in Settings."
	txtViaWarp      = " · via WARP"
	txtUpload       = " · ↑ %.1f Mbps"
)

const (
	seekStepMs = 5000
	liveMs     = 13_000 // the core never jumps nearer than ~12 s to the edge
)

// offer is a newer release, announced on screen; "u" opens its download.
type offer struct{ text, url, version string }

// movieSeeks are the seconds each key moves a movie.
var movieSeeks = map[string]int{"back": -5, "forward": 5, "back60": -60, "forward60": 60}

type session interface {
	Status() *mobile.Status
	URL() string
	Jump(deltaMs, shownMs int64) int64
	Stop()
}

type mpvCtl interface {
	command(args ...any) error
	get(prop string) (any, error)
	observe(prop string) error
	events() <-chan mpvEvent
	showText(text string, ms int) error
}

// player ties the core session to mpv. Everything runs on run's goroutine;
// play may be called from anywhere.
type player struct {
	m       mpvCtl
	start   func(id string) (session, error)
	logPath string
	version string
	now     func() int64
	ticks   <-chan time.Time
	links   chan string
	update  func() // the "u" key; nil when there is nothing newer
	offers  chan offer
	offer   *offer // shown once, when playback first starts

	s              session
	st             *mobile.Status
	loaded         bool
	everReady      bool
	buffering      bool
	bufferingSince int64
	paused         bool
	id             string // the channel playing
	cuts           *cutTracker
	lastReopen     int64
	failed         string // text of a failure shown instead of the stream

	vpn         string            // the WARP mode chosen: "auto", "on" or "off"
	setVpn      func(mode string) // hands a new mode to the core
	settingsDir string            // where the mode is saved
	vpnShown    string            // what the controls were last told
}

func newPlayer(m mpvCtl, start func(string) (session, error), logPath string) *player {
	p := &player{m: m, start: start, logPath: logPath, links: make(chan string, 8), offers: make(chan offer, 1),
		now: func() int64 { return time.Now().UnixMilli() }}
	p.cuts = newCutTracker(func() int64 { return p.now() })
	return p
}

// play switches to link: the old channel stops, the new one starts.
func (p *player) play(link string) { p.links <- link }

// run returns when mpv exits or ctx ends; the session is always stopped.
func (p *player) run(ctx context.Context) {
	if p.ticks == nil {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		p.ticks = t.C
	}
	// Only what changes rarely is observed: time-pos changes every frame and
	// would flood the event channel. Everything else is read with get when needed.
	p.m.observe("paused-for-cache")
	p.m.observe("eof-reached") // a movie's end: --keep-open sends no end-file
	defer func() {
		if p.s != nil {
			p.s.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-p.m.events():
			if !ok {
				return
			}
			p.handle(e)
		case l := <-p.links:
			p.switchTo(l)
		case o := <-p.offers:
			p.offer = &o
			if o.url != "" {
				p.update = func() { openInBrowser(o.url) }
			}
		case <-p.ticks:
			// Events and links that arrived first are handled before the tick.
			for drained := false; !drained; {
				select {
				case e, ok := <-p.m.events():
					if !ok {
						return
					}
					p.handle(e)
				case l := <-p.links:
					p.switchTo(l)
				default:
					drained = true
				}
			}
			p.tick()
		}
	}
}

// changeVpn applies the mode chosen in the controls' panel: saved, handed to
// the core, and the channel playing starts again on the new route.
func (p *player) changeVpn(mode string) {
	if mode != "auto" && mode != "on" && mode != "off" || mode == p.vpn {
		return // unchanged: nothing to restart
	}
	p.vpn = mode
	saveVpn(p.settingsDir, mode)
	p.setVpn(mode)
	if p.id != "" { // also a channel that failed to start: the new route may reach it
		if p.s != nil {
			p.s.Stop()
			p.s = nil
		}
		p.switchTo(p.id)
	}
	p.tellVpn()
}

// tellVpn sends the controls the mode and whether WARP carries this channel,
// when that changed since the last time.
func (p *player) tellVpn() {
	inUse := p.s != nil && p.st != nil && p.st.Outbound == "warp"
	msg := fmt.Sprint(p.vpn, " ", inUse)
	if msg == p.vpnShown {
		return
	}
	p.vpnShown = msg
	p.m.command("script-message", "canalpeer-vpn", p.vpn, fmt.Sprint(inUse))
}

func (p *player) switchTo(link string) {
	id, err := mobile.Normalize(link)
	if err != nil {
		p.fail(txtBadLink)
		return
	}
	if p.s != nil && id == p.id && (p.st == nil || p.st.Phase != "failed") {
		return // the channel already playing: clicking it again must not restart it (a failed one, it retries)
	}
	p.m.command("stop")
	p.m.command("set", "pause", "no")
	// A track picked at runtime outlives loadfile, which keeps it across the
	// reloads of one channel; the next channel starts on its own default.
	p.m.command("set", "aid", "auto")
	if p.s != nil {
		p.s.Stop()
		p.s = nil
	}
	p.id, p.st = id, nil // the old channel's status says nothing of this one
	p.failed, p.loaded, p.everReady, p.buffering = "", false, false, false
	p.cuts = newCutTracker(func() int64 { return p.now() })
	s, err := p.start(id)
	if err != nil {
		p.fail(txtNoNetwork)
		return
	}
	p.s = s
	p.m.showText(txtResolving, 1500)
}

func (p *player) fail(text string) {
	p.failed = text + "\n\n" + txtPressBack
	p.m.showText(p.failed, 1500)
}

func (p *player) tick() {
	if p.s == nil {
		p.tellVpn()
		if p.failed != "" {
			p.m.showText(p.failed, 1500)
		} else {
			p.m.showText(txtIdle, 1500)
		}
		return
	}
	p.st = p.s.Status()
	p.tellVpn()
	v, _ := p.m.get("pause")
	p.paused = v == true
	if p.st.Phase == "playing" && !p.loaded {
		p.load()
	}
	if p.loaded && shouldReopen(p.st.Phase, p.st.QueuedMs, p.cuts.openForMs(), p.now()-p.lastReopen) {
		p.lastReopen = p.now()
		p.reopen(0)
	}
	p.render()
}

// load opens the stream in mpv; the player shows nothing until the first frame.
func (p *player) load() {
	p.loaded = true
	p.m.command("loadfile", p.s.URL())
	if !p.buffering {
		p.buffering, p.bufferingSince = true, p.now()
	}
}

// reopen moves the core to what is on screen plus deltaMs and reloads, so
// the player neither skips nor replays. It returns how far behind live it lands.
func (p *player) reopen(deltaMs int64) int64 {
	pos, _ := p.m.get("time-pos")
	shown, _ := pos.(float64)
	behind := p.s.Jump(deltaMs, int64(shown*1000))
	p.load()
	return behind
}

func (p *player) render() {
	st := p.st
	bufferingMs := int64(0)
	if p.buffering {
		bufferingMs = p.now() - p.bufferingSince
	}
	var text string
	switch overlayFor(st.Phase, p.paused, p.buffering, p.everReady, bufferingMs) {
	case OverlayFailed:
		switch st.Err {
		case "not_found":
			text = txtNotFound
		case "no_peers":
			text = txtNoPeers
		case "no_space":
			text = txtNoSpace
		case "vpn":
			text = txtVpnFailed
		default:
			text = txtNoNetwork
		}
		text += "\n\n" + txtPressBack
	case OverlayResolving:
		text = txtResolving
	case OverlaySearching:
		text = fmt.Sprintf(txtSearching, st.Peers) + via(st)
	case OverlayPaused:
		text = txtPaused
	case OverlayLoading:
		text = txtLoading
	case OverlayReconnecting:
		text = fmt.Sprintf(txtReconnecting, st.Peers) + via(st)
	default:
		return
	}
	p.m.showText(text, 1500)
}

func (p *player) ready() {
	if !p.everReady && p.offer != nil {
		p.m.showText(p.offer.text, 6000)
		p.offer = nil
	}
	p.buffering, p.everReady = false, true
	if cut := p.cuts.onReady(); cut != nil {
		p.logCut(cut)
	}
}

func (p *player) handle(e mpvEvent) {
	switch e.Name {
	case "property-change":
		if e.Property == "eof-reached" && e.Data == true && p.loaded && p.st != nil && p.st.Vod {
			p.m.command("quit", mpvBackCode) // the movie ended
			return
		}
		if e.Property == "paused-for-cache" {
			if e.Data == true {
				if !p.buffering {
					p.buffering, p.bufferingSince = true, p.now()
				}
				p.cuts.onBuffering()
			} else if e.Data == false && p.buffering && p.everReady {
				p.ready()
			}
		}
	case "playback-restart": // the first frame after a load is on screen
		if p.buffering {
			p.ready()
		}
	case "end-file":
		// Our own loadfile/stop end the old file with "stop"; eof or error means
		// the stream broke: never leave the viewer on a frozen last frame.
		if p.loaded && e.Reason == "error" && p.st != nil && p.st.Vod {
			// A movie is a file: reload it where it broke, not from the start.
			pos, _ := p.m.get("time-pos")
			at, _ := pos.(float64)
			p.m.command("loadfile", p.s.URL(), "replace", -1, fmt.Sprintf("start=%.3f", at))
			return
		}
		if p.loaded && (e.Reason == "eof" || e.Reason == "error") {
			p.cuts.onError()
			p.m.command("set", "pause", "no")
			p.reopen(0)
		}
	case "client-message":
		if len(e.Args) == 2 && e.Args[0] == "canalpeer" {
			p.key(e.Args[1])
		}
	}
}

func (p *player) key(k string) {
	switch k {
	case "back", "forward", "back60", "forward60":
		if p.s == nil || !p.loaded {
			return
		}
		if p.st != nil && p.st.Vod {
			// A movie is a file: mpv seeks in it with HTTP ranges.
			p.m.command("seek", movieSeeks[k], "relative+exact") // keyframes alone make ±5 s land 10-15 s off
			return
		}
		if k == "back60" || k == "forward60" {
			return // live keeps its ±5 s
		}
		delta := int64(seekStepMs)
		if k == "back" {
			delta = -delta
		}
		p.cuts.onSeek()
		behind := p.reopen(delta)
		if behind <= liveMs {
			p.m.showText(txtLive, 1500)
		} else {
			p.m.showText(fmt.Sprintf(txtBehind, behind/1000), 1500)
		}
	case "pause":
		p.m.command("cycle", "pause")
	case "audio":
		p.m.command("osd-msg", "cycle", "audio") // osd-msg: mpv says which track it is
	case "fullscreen":
		p.m.command("cycle", "fullscreen")
	case "esc":
		// Asked, not remembered: a dropped event must not turn Esc into quit.
		if fs, _ := p.m.get("fullscreen"); fs == true {
			p.m.command("set", "fullscreen", "no")
		} else {
			p.m.command("quit", mpvBackCode)
		}
	case "stop":
		p.m.command("quit", mpvBackCode)
	case "info":
		if p.s == nil {
			return
		}
		st := p.s.Status()
		cache, _ := p.m.get("demuxer-cache-duration")
		secs, _ := cache.(float64)
		p.m.showText(fmt.Sprintf(txtInfo, st.Peers, p.cuts.count(), (st.QueuedMs/1000)+int64(secs), p.version)+
			fmt.Sprintf(txtUpload, float64(st.UpBps)*8/1e6)+via(st), 4000)
	case "paste":
		if v, _ := p.m.get("clipboard/text"); v != nil {
			if s, ok := v.(string); ok {
				p.switchTo(s)
			}
		}
	case "update":
		if p.update != nil {
			p.update()
		}
	case "vpn-state": // the controls just loaded: whatever was sent before is lost
		p.vpnShown = ""
		p.tellVpn()
	default:
		if mode, ok := strings.CutPrefix(k, "vpn "); ok {
			p.changeVpn(mode)
		}
	}
}

// via names the route when it is WARP; direct goes unsaid.
func via(st *mobile.Status) string {
	if st.Outbound == "warp" {
		return txtViaWarp
	}
	return ""
}

func (p *player) logCut(c *Cut) {
	if p.logPath == "" {
		return
	}
	line := map[string]any{"start": c.StartMs, "ms": c.DurationMs, "reason": c.Reason}
	if p.st != nil {
		line["peers"], line["phase"] = p.st.Peers, p.st.Phase
	}
	b, _ := json.Marshal(line)
	f, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}
