package mobile

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeReceiver struct {
	mu    sync.Mutex
	plays []string
	stops int
}

func (f *fakeReceiver) Play(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plays = append(f.plays, id)
}

func (f *fakeReceiver) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
}

// calls snapshots what the receiver got; the handler runs on another goroutine.
func (f *fakeReceiver) calls() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.plays...), f.stops
}

func listening(t *testing.T) (*fakeReceiver, string) {
	t.Helper()
	f := &fakeReceiver{}
	port, err := Listen(f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(StopListening)
	return f, fmt.Sprintf("http://127.0.0.1:%d", port)
}

func post(t *testing.T, url, body string) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestReceiverPlayNormalizesID(t *testing.T) {
	f, base := listening(t)
	if code := post(t, base+"/play", `{"id":"acestream://`+strings.ToUpper(testID)+`"}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if plays, _ := f.calls(); len(plays) != 1 || plays[0] != testID {
		t.Fatalf("plays %q, want [%s]", plays, testID)
	}
}

func TestReceiverRejectsBadRequests(t *testing.T) {
	f, base := listening(t)
	for _, body := range []string{`{"id":`, `{"id":"nope"}`, `{}`} {
		if code := post(t, base+"/play", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, code)
		}
	}
	if plays, _ := f.calls(); len(plays) != 0 {
		t.Fatalf("Play called with %q", plays)
	}
}

func TestReceiverStop(t *testing.T) {
	f, base := listening(t)
	if code := post(t, base+"/stop", ""); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if _, stops := f.calls(); stops != 1 {
		t.Fatalf("stops %d", stops)
	}
}

func TestReceiverRejectsOtherMethodsAndPaths(t *testing.T) {
	f, base := listening(t)
	resp, err := http.Get(base + "/play")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /play: status %d", resp.StatusCode)
	}
	if code := post(t, base+"/other", ""); code != http.StatusNotFound {
		t.Fatalf("POST /other: status %d", code)
	}
	if plays, stops := f.calls(); len(plays) != 0 || stops != 0 {
		t.Fatal("receiver called")
	}
}

func TestStopListeningCloses(t *testing.T) {
	_, base := listening(t)
	StopListening()
	c := &http.Client{Timeout: time.Second}
	if resp, err := c.Post(base+"/stop", "", nil); err == nil {
		resp.Body.Close()
		t.Fatal("still listening after StopListening")
	}
}
