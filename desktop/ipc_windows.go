package main

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialIPC(path string) (net.Conn, error) {
	timeout := time.Second
	return winio.DialPipe(path, &timeout)
}
