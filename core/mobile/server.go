package mobile

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// serve exposes the pipe as an endless MPEG-TS at http://127.0.0.1:<port>/stream.
// Each request takes over the pipe, so a player that reconnects after an error
// continues from what is queued.
// Once m holds a movie, /stream serves its file instead, with ranges.
func serve(p *pipe, m *atomic.Pointer[movie]) (url string, stop func(), err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, req *http.Request) {
		if mv := m.Load(); mv != nil {
			mv.serve(w, req)
			return
		}
		serveLive(w, req, p.reader, nil)
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	stop = func() {
		p.Close() // ends a handler blocked in Read
		// A handler blocked in Write (the player stopped reading) only ends when
		// its connection is closed, so bound the graceful path.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(ctx) != nil {
			srv.Close()
		}
	}
	return "http://" + ln.Addr().String() + "/stream", stop, nil
}

// serveLive streams the pipe from open(the request's Range start), which takes
// over the pipe; sent, if set, is called whenever bytes reach the client.
func serveLive(w http.ResponseWriter, req *http.Request, open func(at int64) io.Reader, sent func()) {
	w.Header().Set("Content-Type", "video/mp2t")
	var at int64
	status := http.StatusOK
	// A reconnecting player asks for "bytes=N-": answer from there with 206,
	// or it would read and drop N bytes itself.
	if n, ok := strings.CutPrefix(req.Header.Get("Range"), "bytes="); ok {
		if v, err := strconv.ParseInt(strings.TrimSuffix(n, "-"), 10, 64); err == nil && v > 0 {
			at, status = v, http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-/*", v))
		}
	}
	w.WriteHeader(status)
	rc := http.NewResponseController(w)
	rc.Flush() // headers now: the player must not time out waiting for the first bytes
	r := open(at)
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if err != nil {
			return
		}
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
		if sent != nil {
			sent()
		}
	}
}
