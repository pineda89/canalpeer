package mobile

import (
	"encoding/json"
	"net"
	"net/http"
	"sync"
)

// Receiver is what the TV app does when a phone sends it a channel.
type Receiver interface {
	Play(id string) // a normalized content id
	Stop()
}

var receiver struct {
	mu  sync.Mutex
	srv *http.Server
}

// Listen starts the TV's receiver on all IPv4 interfaces and returns its port,
// for the NSD record: POST /play {"id":"…"} calls r.Play, POST /stop calls
// r.Stop. A plain listener, never through WARP. A new Listen replaces the
// previous one.
func Listen(r Receiver) (int, error) {
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return 0, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /play", func(w http.ResponseWriter, req *http.Request) {
		var body struct{ ID string }
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := Normalize(body.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Play(id)
	})
	mux.HandleFunc("POST /stop", func(http.ResponseWriter, *http.Request) { r.Stop() })
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if receiver.srv != nil {
		receiver.srv.Close()
	}
	receiver.srv = &http.Server{Handler: mux}
	go receiver.srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// StopListening closes the receiver. Safe to repeat.
func StopListening() {
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if receiver.srv != nil {
		receiver.srv.Close()
		receiver.srv = nil
	}
}
