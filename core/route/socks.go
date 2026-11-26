package route

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

// socksHandshakeTimeout bounds a client's greeting and request, not the
// tunnel bring-up or the dial that follow.
var socksHandshakeTimeout = 10 * time.Second

// ProxyAddr is "127.0.0.1:<port>" of a local SOCKS5 proxy (no auth, CONNECT
// only) that dials through Dialer, so it follows the route like every other
// path: On fails rather than going direct. It is open, started on first use,
// while WARP is wanted (even before the tunnel is up); "" otherwise.
func ProxyAddr() string {
	mu.Lock()
	defer mu.Unlock()
	if !st.wanted(time.Now()) {
		return ""
	}
	if st.proxy == nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return ""
		}
		st.proxy = ln
		go serveSocks(ln)
	}
	return st.proxy.Addr().String()
}

func serveSocks(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go handleSocks(c)
	}
}

// SOCKS5 reply codes (RFC 1928).
const (
	repOK          = 0
	repFailure     = 1
	repHostUnreach = 4
	repCmdUnsupp   = 7
	repAtypUnsupp  = 8
)

func handleSocks(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(socksHandshakeTimeout))

	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil || hdr[0] != 5 {
		return
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	noAuth := false
	for _, m := range methods {
		noAuth = noAuth || m == 0
	}
	if !noAuth {
		c.Write([]byte{5, 0xFF})
		return
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return
	}

	var req [4]byte // ver, cmd, rsv, atyp
	if _, err := io.ReadFull(c, req[:]); err != nil || req[0] != 5 {
		return
	}
	reply := func(code byte) { c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if req[1] != 1 {
		reply(repCmdUnsupp)
		return
	}
	var host string
	switch req[3] {
	case 1, 4:
		ip := make([]byte, 4)
		if req[3] == 4 {
			ip = make([]byte, 16)
		}
		if _, err := io.ReadFull(c, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		name := make([]byte, n[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return
		}
		host = string(name)
	default:
		reply(repAtypUnsupp)
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:]))))

	c.SetDeadline(time.Time{}) // a first dial may wait for the tunnel to come up
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	up, err := Dialer().DialContext(ctx, "tcp", addr)
	if errors.Is(err, ErrVPN) {
		reply(repFailure)
		return
	}
	if err != nil {
		reply(repHostUnreach)
		return
	}
	defer up.Close()
	reply(repOK)

	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done // either side ending ends both; the defers close them
}
