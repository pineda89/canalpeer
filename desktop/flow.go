package main

import (
	"strings"

	"canalpeer.com/core/mobile"
)

// What a player run ended with.
type ending int

const (
	endBack ending = iota // Stop, Esc, the window's close button: show the launcher
	endQuit               // a real quit (Cmd+Q, mpv's own quit): the app ends
)

// mpvBackCode is the exit code mpv quits with when the viewer goes back.
const mpvBackCode = 5

// launchFunc shows the launcher until the viewer submits a valid link (returned)
// or closes the window (quit). A link arriving on links meanwhile is returned as is.
type launchFunc func(links <-chan string) (link string, quit bool)

// playFunc runs one mpv for link, switching channel on every link from links,
// until mpv ends.
type playFunc func(link string, links <-chan string) ending

// runFlow alternates launcher and player: a link (first, or one submitted)
// plays; going back shows the launcher; closing the launcher or quitting ends.
// Without a launcher (nil: no web view here) the player window is all there is.
func runFlow(first string, links <-chan string, launch launchFunc, play playFunc) {
	if launch == nil {
		play(first, links)
		return
	}
	link := first
	for {
		if link == "" {
			var quit bool
			if link, quit = launch(links); quit {
				return
			}
			if link == "" { // the launcher window could not be made: player window alone
				play("", links)
				return
			}
		}
		if play(link, links) == endQuit {
			return
		}
		link = ""
	}
}

// validate turns what the viewer typed into a link to play, or the error to show.
func validate(text string) (link, errText string) {
	id, err := mobile.Normalize(strings.TrimSpace(text))
	if err != nil || id == "" {
		return "", "That is not a content ID or acestream:// link."
	}
	return id, "" // as is: an infohash id ("ih-...") is not one behind "acestream://"
}
