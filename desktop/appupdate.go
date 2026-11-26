package main

import "sync"

// appUpdate is what the app knows about updates, shared by the launcher and
// whichever player is open: an offer to show, and a release staged to swap in.
type appUpdate struct {
	mu     sync.Mutex
	offer  *offer
	next   *staged
	player *player
	told   bool // an offer without a download ("Updated to …") went to a player already
}

// set records an offer and hands it to the open player, if any.
func (a *appUpdate) set(o offer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.offer, a.told = &o, false
	if a.player != nil {
		a.told = true
		select {
		case a.player.offers <- o:
		default: // it already has one waiting
		}
	}
}

// attach makes p the open player (nil when none) and gives it the offer so far.
func (a *appUpdate) attach(p *player) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.player = p
	if p != nil && a.offer != nil && !(a.told && a.offer.url == "") {
		a.told = true
		select {
		case p.offers <- *a.offer:
		default:
		}
	}
}

func (a *appUpdate) stage(s *staged) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next = s
}

func (a *appUpdate) staged() *staged {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.next
}

// banner is the launcher's update line, and a download URL when the viewer
// has to fetch it themselves.
func (a *appUpdate) banner() (text, url string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next != nil {
		return "CanalPeer " + a.next.version + " will be installed when you close the app.", ""
	}
	if a.offer != nil && a.offer.url != "" {
		return "CanalPeer " + a.offer.version + " is available", a.offer.url
	}
	if a.offer != nil {
		return a.offer.text, ""
	}
	return "", ""
}
