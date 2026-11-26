package stats

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"canalpeer.com/core/ace"
	"canalpeer.com/core/route"
)

type request struct {
	path string
	body map[string]any
}

// server records every request on the returned channel and answers status.
func server(t *testing.T, status int) (*httptest.Server, chan request) {
	t.Helper()
	got := make(chan request, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("body: %v", err)
		}
		got <- request{r.URL.Path, body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func useSite(t *testing.T, url string) {
	t.Helper()
	old := Site
	Site = url
	t.Cleanup(func() { Site = old })
}

func wait(t *testing.T, got chan request) request {
	t.Helper()
	select {
	case r := <-got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no request arrived")
		return request{}
	}
}

// closedSite is a URL on a port nothing listens on: the dial is refused.
func closedSite(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

// fakeWarp counts bring-ups; its dialer reaches target whatever was asked.
type fakeWarp struct {
	ups    atomic.Int32
	target string
}

func (f *fakeWarp) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, f.target)
}

func (f *fakeWarp) tunneler(context.Context, string) (ace.Dialer, func(), error) {
	f.ups.Add(1)
	return f, func() {}, nil
}

func setupRoute(t *testing.T, mode, target string) *fakeWarp {
	t.Helper()
	route.ResetForTest()
	t.Cleanup(route.ResetForTest)
	f := &fakeWarp{target: target}
	route.SetTunnelerForTest(f.tunneler)
	route.Configure(mode, "")
	return f
}

func TestInstallIDCreatedOnceAndReused(t *testing.T) {
	dir := t.TempDir()
	a, err := InstallID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(a) {
		t.Fatalf("id %q", a)
	}
	b, err := InstallID(dir)
	if err != nil || b != a {
		t.Fatalf("second call %q, %v; want %q", b, err, a)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "install-id")); err != nil || string(data) != a {
		t.Fatalf("file %q, %v", data, err)
	}
}

func TestInstallIDReplacesCorruptFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "install-id")
	if err := os.WriteFile(file, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := InstallID(dir)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("id %q, %v", id, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != id {
		t.Fatalf("file %q, %v; want %q", data, err, id)
	}
}

func TestOpenPostsPlatformVersionInstall(t *testing.T) {
	setupRoute(t, "off", "")
	srv, got := server(t, http.StatusNoContent)
	useSite(t, srv.URL)
	dir := t.TempDir()
	Open("android", "1.6.0", dir)
	r := wait(t, got)
	id, _ := InstallID(dir)
	want := map[string]any{"install": id, "platform": "android", "version": "1.6.0"}
	if r.path != "/api/app/open" || len(r.body) != 3 {
		t.Fatalf("got %s %v", r.path, r.body)
	}
	for k, v := range want {
		if r.body[k] != v {
			t.Fatalf("%s = %v, want %v", k, r.body[k], v)
		}
	}
}

func TestPlayPostsOnlyTheID(t *testing.T) {
	setupRoute(t, "off", "")
	srv, got := server(t, http.StatusNoContent)
	useSite(t, srv.URL)
	const id = "f16d68fa5ff64f3894a45062ca28e4e66f142c6a"
	Play(id)
	r := wait(t, got)
	if r.path != "/api/app/play" || len(r.body) != 1 || r.body["id"] != id {
		t.Fatalf("got %s %v", r.path, r.body)
	}
}

func TestWarpRetryAfterTransportError(t *testing.T) {
	srv, got := server(t, http.StatusNoContent)
	f := setupRoute(t, "auto", srv.Listener.Addr().String())
	useSite(t, closedSite(t))
	if err := send(context.Background(), "/api/app/play", map[string]string{"id": "x"}); err != nil {
		t.Fatal(err)
	}
	if r := wait(t, got); r.body["id"] != "x" {
		t.Fatalf("body %v", r.body)
	}
	if n := f.ups.Load(); n != 1 {
		t.Fatalf("tunnel brought up %d times, want 1", n)
	}
}

func TestNoWarpRetryOnHTTPStatus(t *testing.T) {
	srv, got := server(t, http.StatusInternalServerError)
	f := setupRoute(t, "auto", srv.Listener.Addr().String())
	useSite(t, srv.URL)
	if err := send(context.Background(), "/api/app/play", map[string]string{"id": "x"}); err == nil {
		t.Fatal("500 reported as success")
	}
	wait(t, got)
	if n := f.ups.Load(); n != 0 {
		t.Fatalf("tunnel brought up %d times, want 0", n)
	}
}

func TestNoWarpRetryInOff(t *testing.T) {
	srv, _ := server(t, http.StatusNoContent)
	f := setupRoute(t, "off", srv.Listener.Addr().String())
	useSite(t, closedSite(t))
	if err := send(context.Background(), "/api/app/play", map[string]string{"id": "x"}); err == nil {
		t.Fatal("refused dial reported as success")
	}
	if n := f.ups.Load(); n != 0 {
		t.Fatalf("tunnel brought up %d times, want 0", n)
	}
}
