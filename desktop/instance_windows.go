package main

import (
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
)

// A named pipe, not AF_UNIX in %TEMP%: user names with accents (José, Begoña)
// must not break it. One pipe per Windows user.
func instancePath() string { return `\\.\pipe\canalpeer-` + os.Getenv("USERNAME") }

func listenInstance(path string) (net.Listener, error) { return winio.ListenPipe(path, nil) }

func dialInstance(path string) (net.Conn, error) {
	timeout := time.Second
	return winio.DialPipe(path, &timeout)
}

func removeStaleInstance(string) {} // a pipe dies with its process
