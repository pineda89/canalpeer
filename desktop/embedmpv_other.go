//go:build !windows

package main

import "errors"

func embeddedMpv() (string, error) {
	return "", errors.New("only Windows carries mpv in the executable")
}
