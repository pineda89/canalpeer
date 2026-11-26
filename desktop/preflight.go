package main

import (
	"errors"
	"os"
)

// preflight checks the bundled player is where the app expects it, before the
// app registers itself for links: opening CanalPeer.exe from inside the zip in
// Explorer runs it from a temporary folder without mpv next to it.
func preflight(mpvBin string) error {
	if _, err := os.Stat(mpvBin); err != nil {
		return errors.New("CanalPeer cannot find its player.\n\nExtract the whole zip to a folder first, then open CanalPeer from there.")
	}
	return nil
}
