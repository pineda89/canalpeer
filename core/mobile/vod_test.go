package mobile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"canalpeer.com/core/ace"
)

func localClient(t *testing.T, dir string, seed bool) *torrent.Client {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	localOnly(cfg)
	cfg.DataDir, cfg.Seed = dir, seed
	c, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// localOnly keeps a test client off the internet.
func localOnly(cfg *torrent.ClientConfig) {
	cfg.NoDHT, cfg.DisableTrackers, cfg.DisableIPv6 = true, true, true
	cfg.ListenHost = func(string) string { return "127.0.0.1" }
	cfg.ListenPort = 0
}

func TestMovieSessionServesFile(t *testing.T) {
	src := t.TempDir()
	movie := make([]byte, 3<<20)
	rand.New(rand.NewSource(1)).Read(movie)
	os.WriteFile(filepath.Join(src, "movie.mkv"), movie, 0o600)
	os.WriteFile(filepath.Join(src, "ad.url"), []byte("spam"), 0o600)
	info := metainfo.Info{PieceLength: 256 << 10}
	if err := info.BuildFromFilePath(src); err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{}
	mi.InfoBytes, _ = bencode.Marshal(info)
	raw, _ := bencode.Marshal(mi)

	seeder := localClient(t, filepath.Dir(src), true)
	st, err := seeder.AddTorrent(&mi)
	if err != nil {
		t.Fatal(err)
	}
	st.VerifyData()

	cache := t.TempDir()
	d := (&harness{}).deps(time.Minute, time.Minute)
	d.resolve = func(context.Context, string) (ace.Descriptor, error) {
		return ace.Descriptor{Name: "m", Torrent: raw}, nil
	}
	d.clientConfig = localOnly
	d.onTorrent = func(t *torrent.Torrent) { t.AddClientPeer(seeder) }
	s, err := start(&Options{ContentID: testID, CacheDir: cache}, d)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for s.Status().Phase != "playing" {
		if st := s.Status(); st.Phase == "failed" || time.Now().After(deadline) {
			t.Fatalf("status %+v", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !s.Status().Vod {
		t.Fatal("a movie must say so")
	}

	resp, err := http.Get(s.URL())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Set explicitly: sniffing would read the file's first piece before every range.
	if ct := resp.Header.Get("Content-Type"); ct != "video/x-matroska" {
		t.Fatalf("content type %q", ct)
	}
	if !bytes.Equal(got, movie) {
		t.Fatalf("served %d bytes, want the %d of the largest file", len(got), len(movie))
	}
	req, _ := http.NewRequest("GET", s.URL(), nil)
	req.Header.Set("Range", "bytes=1000000-1000099")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(part, movie[1000000:1000100]) {
		t.Fatalf("range: %d, %d bytes", resp.StatusCode, len(part))
	}

	began := time.Now()
	s.Stop()
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("Stop took %s", d)
	}
	if n, _ := filepath.Glob(filepath.Join(cache, "vod", "*", "*")); len(n) == 0 {
		t.Fatal("pieces must stay on disk for the next time")
	}
}

func TestMovieSessionStopsWhileSearching(t *testing.T) {
	info := metainfo.Info{Name: "m.mkv", PieceLength: 256 << 10, Length: 1 << 20, Pieces: make([]byte, 80)}
	mi := metainfo.MetaInfo{}
	mi.InfoBytes, _ = bencode.Marshal(info)
	raw, _ := bencode.Marshal(mi)
	d := (&harness{}).deps(time.Minute, time.Minute)
	d.resolve = func(context.Context, string) (ace.Descriptor, error) {
		return ace.Descriptor{Torrent: raw}, nil
	}
	d.clientConfig = localOnly
	s, err := start(&Options{ContentID: testID, CacheDir: t.TempDir()}, d)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if st := s.Status(); st.Phase != "searching" {
		t.Fatalf("status %+v", st)
	}
	began := time.Now()
	s.Stop()
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("Stop took %s", d)
	}
}

func TestDialedPacketConnTalksToItsTracker(t *testing.T) {
	srv, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go func() {
		b := make([]byte, 64)
		n, from, _ := srv.ReadFrom(b)
		srv.WriteTo(append([]byte("re:"), b[:n]...), from)
	}()
	p := &dialedPacketConn{d: &net.Dialer{}, ready: make(chan struct{})}
	got := make(chan string, 1)
	go func() { // the tracker client reads before it writes
		b := make([]byte, 64)
		n, from, err := p.ReadFrom(b)
		got <- fmt.Sprint(string(b[:n]), " ", from, " ", err)
	}()
	if _, err := p.WriteTo([]byte("hi"), srv.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if g, want := <-got, "re:hi "+srv.LocalAddr().String()+" <nil>"; g != want {
		t.Fatalf("read %q, want %q", g, want)
	}
	p.Close()

	idle := &dialedPacketConn{d: &net.Dialer{}, ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, _, err := idle.ReadFrom(make([]byte, 1)); done <- err }()
	idle.Close()
	if err := <-done; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed before any write: %v", err)
	}
}
