//go:build !windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"time"
)

// instancePath is per user: /tmp is shared on Linux, $TMPDIR is not on macOS.
func instancePath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "canalpeer.sock")
}

func listenInstance(path string) (net.Listener, error) { return net.Listen("unix", path) }

func dialInstance(path string) (net.Conn, error) { return net.DialTimeout("unix", path, time.Second) }

func removeStaleInstance(path string) { os.Remove(path) }
