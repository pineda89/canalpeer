package mobile

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeStreamsPipe(t *testing.T) {
	p := newPipe(1 << 10)
	url, stop, err := serve(p, new(atomic.Pointer[movie]))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Fatalf("content-type %q", ct)
	}
	p.Write([]byte("hello"))
	if got := readN(t, resp.Body, 5); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

// A player that stops reading (paused, buffer full) must not hang stop.
func TestStopReturnsWhenClientStopsReading(t *testing.T) {
	p := newPipe(64 << 20)
	url, stop, err := serve(p, new(atomic.Pointer[movie]))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	p.Write(make([]byte, 32<<20)) // more than the socket buffers hold
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop hung on a client that stopped reading")
	}
}

func TestServeHonoursRange(t *testing.T) {
	p := newPipe(1 << 10)
	p.Write([]byte("abcdefghij"))
	url, stop, err := serve(p, new(atomic.Pointer[movie]))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, _ := http.Get(url) // opens the stream at byte 0
	resp.Body.Close()
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Range", "bytes=4-")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d, want 206 so the player does not skip the bytes itself", resp.StatusCode)
	}
	if got := readN(t, resp.Body, 3); got != "efg" {
		t.Fatalf("got %q", got)
	}
}
