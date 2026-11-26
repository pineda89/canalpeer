package mobile

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const dlnaFeatures = "DLNA.ORG_OP=00;DLNA.ORG_CI=0;DLNA.ORG_FLAGS=01700000000000000000000000000000"

func sharedSession(t *testing.T) (*Session, string) {
	t.Helper()
	s := &Session{pipe: newPipe(1 << 10)}
	url, err := s.Share("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.StopShare)
	return s, url
}

func get(t *testing.T, url, rng string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The LG asks HEAD first, then GETs: the HEAD must not take over the pipe from
// the GET that is already streaming.
func TestShareHeadAnswersHeadersWithoutTakingOverPipe(t *testing.T) {
	s, url := sharedSession(t)
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/live.ts") {
		t.Fatalf("url %q", url)
	}
	streaming := get(t, url, "bytes=0-")
	resp, err := http.Head(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD: status %d, %d body bytes", resp.StatusCode, len(body))
	}
	for k, want := range map[string]string{
		"Content-Type":             "video/mp2t",
		"transferMode.dlna.org":    "Streaming",
		"contentFeatures.dlna.org": dlnaFeatures,
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	s.pipe.Write([]byte("still"))
	if got := readN(t, streaming.Body, 5); got != "still" {
		t.Fatalf("GET after a HEAD got %q", got)
	}
}

func TestShareGetStreamsFromNow(t *testing.T) {
	s, url := sharedSession(t)
	resp := get(t, url, "bytes=0-")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := resp.Header.Get("contentFeatures.dlna.org"); got != dlnaFeatures {
		t.Fatalf("contentFeatures %q", got)
	}
	s.pipe.Write([]byte("live"))
	if got := readN(t, resp.Body, 4); got != "live" {
		t.Fatalf("got %q", got)
	}
}

func TestShareUnknownPathIs404(t *testing.T) {
	_, url := sharedSession(t)
	resp := get(t, strings.TrimSuffix(url, "live.ts")+"stream", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestShareOnceThenStopShareCloses(t *testing.T) {
	s, url := sharedSession(t)
	again, err := s.Share("127.0.0.1")
	if err != nil || again != url {
		t.Fatalf("second Share = %q, %v; want %q", again, err, url)
	}
	s.StopShare()
	c := &http.Client{Timeout: time.Second}
	if resp, err := c.Head(url); err == nil {
		resp.Body.Close()
		t.Fatal("still listening after StopShare")
	}
}

func TestShareIdleMs(t *testing.T) {
	s, url := sharedSession(t)
	if got := s.ShareIdleMs(); got != -1 {
		t.Fatalf("before any GET: %d, want -1", got)
	}
	resp := get(t, url, "")
	s.pipe.Write([]byte("x"))
	readN(t, resp.Body, 1)
	if got := s.ShareIdleMs(); got < 0 {
		t.Fatalf("after a read: %d, want >= 0", got)
	}
}

// A source that stalls (P2P) is not a TV that stopped reading: idleness counts
// only while bytes wait for the TV.
func TestShareIdleCountsOnlyWhileBytesWait(t *testing.T) {
	s := &Session{pipe: newPipe(1 << 10)}
	s.share.lastSent.Store(time.Now().Add(-time.Second).UnixNano())
	if got := s.ShareIdleMs(); got != 0 {
		t.Fatalf("nothing new for the TV: %d, want 0", got)
	}
	s.pipe.Write([]byte("x"))
	if got := s.ShareIdleMs(); got < 1000 {
		t.Fatalf("a byte waits for the TV: %d, want >= 1000", got)
	}
}

// The TV starts at what the phone's player was about to show, not at the
// minute the pipe keeps of what the phone already played.
func TestShareStartsAtPhonePosition(t *testing.T) {
	s, url := sharedSession(t)
	phone := s.pipe.reader(0)
	s.pipe.Write([]byte("old"))
	readN(t, phone, 3)
	s.pipe.Write([]byte("queued"))
	resp := get(t, url, "bytes=0-")
	if got := readN(t, resp.Body, 6); got != "queued" {
		t.Fatalf("TV got %q, want what the phone had not shown yet", got)
	}
}

func TestShareWithoutPhoneReaderStartsAtLiveEnd(t *testing.T) {
	s, url := sharedSession(t)
	s.pipe.Write([]byte("old"))
	resp := get(t, url, "bytes=0-")
	s.pipe.Write([]byte("new"))
	if got := readN(t, resp.Body, 3); got != "new" {
		t.Fatalf("TV got %q, want only bytes written after its GET", got)
	}
}

// A TV reconnecting with bytes=N- continues N bytes past where the share began.
func TestShareRangeCountsFromShareOrigin(t *testing.T) {
	s, url := sharedSession(t)
	s.pipe.Write([]byte("old"))
	first := get(t, url, "bytes=0-")
	s.pipe.Write([]byte("abcdef"))
	readN(t, first.Body, 6)
	resp := get(t, url, "bytes=3-")
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := readN(t, resp.Body, 3); got != "def" {
		t.Fatalf("reconnect got %q", got)
	}
}

func TestShareRejectsNonUnicastIP(t *testing.T) {
	for _, ip := range []string{"", "0.0.0.0", "::", "239.255.255.250", "wifi"} {
		s := &Session{pipe: newPipe(1 << 10)}
		if url, err := s.Share(ip); err == nil {
			s.StopShare()
			t.Errorf("Share(%q) = %q, want an error", ip, url)
		}
	}
}
