package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed mpvbin
var mpvFiles embed.FS

// embeddedMpv writes the carried mpv under %LOCALAPPDATA%\CanalPeer\mpv.
func embeddedMpv() (string, error) {
	src, err := fs.Sub(mpvFiles, "mpvbin")
	if err != nil {
		return "", err
	}
	base, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		return "", err
	}
	return ensureMpv(src, filepath.Join(base, "CanalPeer", "mpv"))
}
