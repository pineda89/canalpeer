package mobile

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// share serves a session's live stream on the LAN for a DLNA TV.
type share struct {
	mu       sync.Mutex
	url      string
	srv      *http.Server
	lastSent atomic.Int64 // unix nanos the TV last got bytes, 0 = no GET yet
	origin   int64        // the TV's byte 0 as a pipe offset, -1 until its first GET
}

// Share starts serving the live stream at http://ip:port/live.ts and returns
// that URL; while sharing it returns the same URL. The listener is a plain one,
// never through WARP: the TV is on the LAN. The TV takes the stream over from
// the phone's player.
func (s *Session) Share(ip string) (string, error) {
	sh := &s.share
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.srv != nil {
		return sh.url, nil
	}
	if a := net.ParseIP(ip); a == nil || a.IsUnspecified() || a.IsMulticast() {
		return "", fmt.Errorf("share: %q is not a unicast address", ip)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return "", err
	}
	sh.lastSent.Store(0)
	sh.origin = -1
	// The TV's byte 0 is where it first joined, so it does not start with the
	// minute the pipe keeps of what the phone already showed, and its
	// reconnects (bytes=N-) continue from there.
	open := func(at int64) io.Reader {
		sh.mu.Lock()
		if sh.origin < 0 {
			sh.origin = s.pipe.livePos()
		}
		origin := sh.origin
		sh.mu.Unlock()
		return s.pipe.readerAt(origin + at)
	}
	sent := func() { sh.lastSent.Store(time.Now().UnixNano()) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live.ts", func(w http.ResponseWriter, req *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "video/mp2t")
		h.Set("transferMode.dlna.org", "Streaming")
		h.Set("contentFeatures.dlna.org", "DLNA.ORG_OP=00;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000")
		if req.Method == http.MethodHead {
			return // headers only: a reader here would take the pipe from the TV's GET
		}
		sent()
		serveLive(w, req, open, sent)
	})
	sh.srv = &http.Server{Handler: mux}
	go sh.srv.Serve(ln)
	sh.url = "http://" + ln.Addr().String() + "/live.ts"
	return sh.url, nil
}

// StopShare closes the LAN share. Safe to repeat.
func (s *Session) StopShare() {
	sh := &s.share
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.srv != nil {
		sh.srv.Close()
		sh.srv = nil
	}
}

// ShareIdleMs is how long ago the TV last got bytes, -1 before any GET, and 0
// while nothing waits for it: a stalled source is not a TV that stopped reading.
func (s *Session) ShareIdleMs() int64 {
	t := s.share.lastSent.Load()
	if t == 0 {
		return -1
	}
	if s.pipe.Len() == 0 {
		return 0
	}
	return time.Since(time.Unix(0, t)).Milliseconds()
}
