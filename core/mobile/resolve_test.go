package mobile

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"canalpeer.com/core/ace"
)

func TestResolveChain(t *testing.T) {
	want := ace.Descriptor{Name: "Canal", InfoHash: [20]byte{9}, Trackers: []string{"udp://t:1/announce"}, ChunkLength: 16384, PieceLength: 16384, Bitrate: 400000}
	var served, direct int
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		switch r.URL.Path {
		case "/api/descriptor/" + testID:
			json.NewEncoder(w).Encode(want)
		case "/api/descriptor/" + otherID:
			http.Error(w, "content not found", http.StatusNotFound)
		default:
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer site.Close()
	rc := resolveChain{
		cacheDir: t.TempDir(),
		site:     site.URL,
		client:   site.Client(),
		direct: func(_ context.Context, id string) (ace.Descriptor, error) {
			direct++
			return want, nil
		},
	}
	ctx := context.Background()

	// The site answers first; the operators block the direct hosts.
	if d, err := rc.resolve(ctx, testID); err != nil || !reflect.DeepEqual(d, want) || served != 1 || direct != 0 {
		t.Fatalf("site: %+v %v served=%d direct=%d", d, err, served, direct)
	}
	// Then the device's own copy: a content id's descriptor never changes.
	site.Close()
	if d, err := rc.resolve(ctx, testID); err != nil || !reflect.DeepEqual(d, want) || direct != 0 {
		t.Fatalf("cache: %+v %v direct=%d", d, err, direct)
	}
	// Site unreachable and nothing cached: the direct hosts are the last resort.
	if d, err := rc.resolve(ctx, thirdID); err != nil || !reflect.DeepEqual(d, want) || direct != 1 {
		t.Fatalf("direct: %+v %v direct=%d", d, err, direct)
	}
}

func TestResolveChainNotFound(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "content not found", http.StatusNotFound)
	}))
	defer site.Close()
	direct := 0
	rc := resolveChain{site: site.URL, client: site.Client(),
		direct: func(context.Context, string) (ace.Descriptor, error) {
			direct++
			return ace.Descriptor{}, errors.New("blocked")
		}}
	// The site only knows live channels, so the hosts are asked too; their
	// failure keeps the site's verdict.
	if _, err := rc.resolve(context.Background(), otherID); !errors.Is(err, ace.ErrNotFound) || direct != 1 {
		t.Fatalf("got %v, direct=%d", err, direct)
	}
}

func TestResolveMovieAfterSiteNotFound(t *testing.T) {
	movie := ace.Descriptor{Name: "Movie", Torrent: []byte("d4:infod4:name5:Movieee")}
	var served atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		http.Error(w, "content not found", http.StatusNotFound)
	}))
	dir := t.TempDir()
	direct := 0
	rc := resolveChain{cacheDir: dir, site: site.URL, client: site.Client(),
		direct: func(context.Context, string) (ace.Descriptor, error) {
			direct++
			return movie, nil
		}}
	if d, err := rc.resolve(context.Background(), otherID); err != nil || !reflect.DeepEqual(d, movie) {
		t.Fatalf("%+v %v", d, err)
	}
	time.Sleep(100 * time.Millisecond) // a report would be in flight by now
	if served.Load() != 1 {
		t.Fatalf("a movie was reported to the site: %d requests", served.Load())
	}
	// Cached, and out of the folder sync reports from.
	if m, _ := filepath.Glob(filepath.Join(dir, "descriptors", "*.json")); len(m) != 0 {
		t.Fatalf("movie cached among live channels: %v", m)
	}
	site.Close()
	if d, err := rc.resolve(context.Background(), otherID); err != nil || !reflect.DeepEqual(d, movie) || direct != 1 {
		t.Fatalf("cache: %+v %v direct=%d", d, err, direct)
	}
}

const (
	otherID = "0000000000000000000000000000000000000001"
	thirdID = "0000000000000000000000000000000000000002"
)

func gz(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	json.NewEncoder(zw).Encode(v)
	zw.Close()
	return buf.Bytes()
}

func TestResolveFromBuiltInCopy(t *testing.T) {
	want := ace.Descriptor{Name: "Canal", InfoHash: [20]byte{7}, Trackers: []string{"udp://t:1/announce"}, ChunkLength: 16384, PieceLength: 16384, Bitrate: 1}
	rc := resolveChain{
		builtIn: gz(t, batch{Items: []entry{{Seq: 3, CID: testID, Desc: want}}, Next: 3}),
		site:    "http://127.0.0.1:1", client: http.DefaultClient,
		direct: func(context.Context, string) (ace.Descriptor, error) { return ace.Descriptor{}, errors.New("blocked") },
	}
	if d, err := rc.resolve(context.Background(), testID); err != nil || !reflect.DeepEqual(d, want) {
		t.Fatalf("built-in: %+v %v", d, err)
	}
}

func TestSyncKeepsTheSiteCopyAndReports(t *testing.T) {
	one := ace.Descriptor{Name: "one", Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	all := gz(t, batch{Items: []entry{{Seq: 5, CID: otherID, Desc: one}}, Next: 5})
	var asked []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/descriptors":
			asked = append(asked, r.Header.Get("If-None-Match"))
			if r.Header.Get("If-None-Match") == `"5"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"5"`)
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(all)
		case "/api/descriptor/" + thirdID:
			asked = append(asked, "report "+thirdID)
			json.NewEncoder(w).Encode(one)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	dir := t.TempDir()
	rc := resolveChain{cacheDir: dir, site: site.URL, client: site.Client(),
		direct: func(context.Context, string) (ace.Descriptor, error) { return ace.Descriptor{}, errors.New("blocked") }}
	// A channel this device resolved on its own, unknown to the site.
	keep(filepath.Join(dir, "descriptors", thirdID+".json"), one)
	ctx := context.Background()
	if err := rc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rc.sync(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"", "report " + thirdID, `"5"`, "report " + thirdID}
	if !reflect.DeepEqual(asked, want) {
		t.Fatalf("asked %q", asked)
	}
	site.Close()
	if d, err := rc.resolve(ctx, otherID); err != nil || d.Name != "one" {
		t.Fatalf("from the site's copy: %+v %v", d, err)
	}
	// Site down: sync fails and the copy stays.
	if err := rc.sync(ctx); err == nil {
		t.Fatal("sync with the site down succeeded")
	}
	if d, err := rc.resolve(ctx, otherID); err != nil || d.Name != "one" {
		t.Fatalf("after a failed sync: %+v %v", d, err)
	}
}

// hang is a site that never answers, like one dropped by the operators.
func hang(t *testing.T) *httptest.Server {
	stop := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(stop); s.Close() })
	return s
}

var testKey = bytes.Repeat([]byte{7}, 32)

// seal is what the repo's workflow does to the site's archive.
func seal(t *testing.T, archive []byte) []byte {
	block, _ := aes.NewCipher(testKey)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, 12)
	rand.Read(nonce)
	return append([]byte(mirrorMagic+string(nonce)), gcm.Seal(nil, nonce, archive, nil)...)
}

func mirror(t *testing.T, archive []byte) *httptest.Server {
	sealed := seal(t, archive)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(sealed)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestResolveFromMirrorWhileSiteHangs(t *testing.T) {
	want := ace.Descriptor{Name: "Canal", InfoHash: [20]byte{5}, Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	site := hang(t)
	m := mirror(t, gz(t, batch{Items: []entry{{Seq: 9, CID: testID, Desc: want}}, Next: 9}))
	var direct atomic.Int32
	rc := resolveChain{cacheDir: t.TempDir(), site: site.URL, mirrors: []string{m.URL}, mirrorKey: testKey, client: http.DefaultClient,
		direct: func(ctx context.Context, _ string) (ace.Descriptor, error) {
			direct.Add(1)
			<-ctx.Done()
			return ace.Descriptor{}, ctx.Err()
		}}
	start := time.Now()
	d, err := rc.resolve(context.Background(), testID)
	if err != nil || !reflect.DeepEqual(d, want) {
		t.Fatalf("%+v %v", d, err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("waited %v for a hanging site", el)
	}
	if direct.Load() != 0 {
		t.Fatal("asked the direct hosts although a mirror answered at once")
	}
}

func TestResolveAsksDirectHostsAfterHedge(t *testing.T) {
	want := ace.Descriptor{Name: "Nuevo", Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	site := hang(t)
	rc := resolveChain{site: site.URL, client: http.DefaultClient, hedge: 50 * time.Millisecond,
		direct: func(context.Context, string) (ace.Descriptor, error) { return want, nil }}
	start := time.Now()
	if d, err := rc.resolve(context.Background(), testID); err != nil || d.Name != "Nuevo" {
		t.Fatalf("%+v %v", d, err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("waited %v for a hanging site", el)
	}
}

func TestSyncFromMirrorWhenSiteIsDown(t *testing.T) {
	one := ace.Descriptor{Name: "one", Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	two := ace.Descriptor{Name: "two", Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	older := mirror(t, gz(t, batch{Items: []entry{{Seq: 5, CID: otherID, Desc: one}}, Next: 5}))
	newer := mirror(t, gz(t, batch{Items: []entry{{Seq: 5, CID: otherID, Desc: one}, {Seq: 8, CID: thirdID, Desc: two}}, Next: 8}))
	dir := t.TempDir()
	rc := resolveChain{cacheDir: dir, site: "http://127.0.0.1:1", mirrors: []string{newer.URL}, mirrorKey: testKey, client: http.DefaultClient,
		direct: func(context.Context, string) (ace.Descriptor, error) { return ace.Descriptor{}, errors.New("blocked") }}
	rc.sync(context.Background()) // reports fail while the site is down; the copy still lands
	if d, err := rc.resolve(context.Background(), thirdID); err != nil || d.Name != "two" {
		t.Fatalf("from the mirror's copy: %+v %v", d, err)
	}
	// A mirror that lags behind never rolls the device's copy back.
	rc.mirrors = []string{older.URL}
	rc.sync(context.Background())
	if d, err := rc.resolve(context.Background(), thirdID); err != nil || d.Name != "two" {
		t.Fatalf("an older mirror replaced the copy: %+v %v", d, err)
	}
}

func TestMirrorKeyIsAES256(t *testing.T) {
	if len(mirrorKey) != 32 {
		t.Fatalf("mirrorKey is %d bytes", len(mirrorKey))
	}
	// The workflow that seals the mirror carries its own copy of the key.
	wf, err := os.ReadFile("../../.github/workflows/mirror.yml")
	if err != nil || !strings.Contains(string(wf), "MIRROR_KEY: "+hex.EncodeToString(mirrorKey)+"\n") {
		t.Fatalf("the mirror workflow does not seal with mirrorKey (%v)", err)
	}
	if _, err := openMirror(testKey, []byte("plain gzip, not sealed")); err == nil {
		t.Fatal("opened an unsealed copy")
	}
}

func TestResolveByInfohashFromCopies(t *testing.T) {
	inCopy := ace.Descriptor{Name: "M. LaLiga", InfoHash: [20]byte{0xc1, 0x95}, Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	inMirror := ace.Descriptor{Name: "DAZN", InfoHash: [20]byte{0xd4}, Trackers: []string{"udp://t:1/announce"}, PieceLength: 16384, ChunkLength: 16384}
	m := mirror(t, gz(t, batch{Items: []entry{{Seq: 2, CID: otherID, Desc: inMirror}}, Next: 2}))
	var asked atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1) }))
	defer site.Close()
	rc := resolveChain{
		builtIn: gz(t, batch{Items: []entry{{Seq: 1, CID: testID, Desc: inCopy}}, Next: 1}),
		site:    site.URL, mirrors: []string{m.URL}, mirrorKey: testKey, client: http.DefaultClient,
		direct: func(context.Context, string) (ace.Descriptor, error) {
			asked.Add(1)
			return ace.Descriptor{}, errors.New("no")
		},
	}
	ctx := context.Background()
	if d, err := rc.resolve(ctx, "ih-"+hex.EncodeToString(inCopy.InfoHash[:])); err != nil || d.Name != "M. LaLiga" {
		t.Fatalf("from the built-in copy: %+v %v", d, err)
	}
	if d, err := rc.resolve(ctx, "ih-"+hex.EncodeToString(inMirror.InfoHash[:])); err != nil || d.Name != "DAZN" {
		t.Fatalf("from the mirror: %+v %v", d, err)
	}
	// Neither the site nor the AceStream hosts resolve an infohash.
	if _, err := rc.resolve(ctx, "ih-"+strings.Repeat("ab", 20)); !errors.Is(err, ace.ErrNotFound) || asked.Load() != 0 {
		t.Fatalf("unknown infohash: %v, asked=%d", err, asked.Load())
	}
}

// A descriptor without its lengths would divide by zero in the swarm: every
// JSON source treats it as not there.
func TestDescriptorWithoutLengthsIsNotUsed(t *testing.T) {
	id := strings.Repeat("ab", 20)
	bad := ace.Descriptor{Name: "broken", Trackers: []string{"udp://t:1/announce"}}
	if _, ok := findIn(gz(t, batch{Items: []entry{{Seq: 1, CID: id, Desc: bad}}}), id); ok {
		t.Fatal("the archive's copy was used")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(bad)
	}))
	defer site.Close()
	rc := resolveChain{site: site.URL, client: site.Client()}
	if _, err := rc.fromSite(context.Background(), id); err == nil {
		t.Fatal("the site's answer was used")
	}
	dir := t.TempDir()
	keep(filepath.Join(dir, "descriptors", id+".json"), bad)
	rc = resolveChain{cacheDir: dir, site: site.URL, client: site.Client(),
		direct: func(context.Context, string) (ace.Descriptor, error) { return ace.Descriptor{}, errors.New("blocked") }}
	if d, err := rc.resolve(context.Background(), id); err == nil {
		t.Fatalf("the cached file was used: %+v", d)
	}
}
