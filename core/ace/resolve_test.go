package ace

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// The (params, _s) pair captured from the real engine (resolve.py selftest).
func TestSignQueryEngineVector(t *testing.T) {
	params := map[string]string{
		"_aa": "arm64-v8a", "_ap": "d3efefe5-4ce4-345b-adb6-adfa3ba92eab",
		"_av": "302131302", "_n": "3.1.80", "_p": "android",
		"_r": "6764002579227581729", "_v": "3018000",
		"pid": "e572a5178ff72eed7d1d751a18b4b3419699f370",
	}
	if got := SignQuery(params); got != "3c680c326ff85c55e8b7fee768d397d33031bf94" {
		t.Fatalf("_s = %s", got)
	}
}

func ligaDescriptor(t *testing.T) map[string]any {
	return map[string]any{
		"name": "M. Liga de Campeones", "authmethod": "RSA",
		"pubkey": unhex(t, "307a300d06092a864886f70d01010105000369003066026100d01b53b80c14610c"+
			"de16362ccdb1feff6368cb3c189ec2406c01a27018dc63abf0b2c8d87734613e2c"+
			"b9dfbb651c0ee454004836fc94a755353324c369bc39fe7f2a69c33bf731e569aa"+
			"22728ae145e0c0c19fef803a2605206b9f08519be4e5020103"),
		"piece_length": 1048576, "chunk_length": 16384, "bitrate": 895306,
		"provider_key": "e8c6b96636d7d4fa6e3cec6c00b0abd5",
		"provider_sid": "dbf76b67a18ee840bcfcf884fcc0f5429e5839955e3d1a36dd79033a6f3f2194",
		"trackers":     []any{"udp://t1.torrentstream.org:2710/announce"},
		"categories":   []any{"sport"}, "quality": "SD", "permanent": 1,
	}
}

func TestParseDescriptorInfohashVector(t *testing.T) {
	plain, err := Encode(ligaDescriptor(t))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDescriptor(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(d.InfoHash[:]); got != "8c1c3eae077f3a786ed2f0a426197ea93fdf7373" {
		t.Fatalf("infohash %s", got)
	}
	if d.Name != "M. Liga de Campeones" || d.PieceLength != 1048576 || d.ChunkLength != 16384 ||
		d.Bitrate != 895306 || len(d.Trackers) != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestParseDescriptorIgnoresMutableFields(t *testing.T) {
	a := ligaDescriptor(t)
	b := ligaDescriptor(t)
	b["quality"], b["trackers"] = "HD", []any{"udp://other:1/announce"}
	pa, _ := Encode(a)
	pb, _ := Encode(b)
	da, _ := ParseDescriptor(pa)
	db, _ := ParseDescriptor(pb)
	if da.InfoHash != db.InfoHash {
		t.Fatal("mutable metadata changed the infohash")
	}
}

func TestParseDescriptorRejectsUnusable(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"no trackers":   func(d map[string]any) { delete(d, "trackers") },
		"zero chunk":    func(d map[string]any) { d["chunk_length"] = 0 },
		"uneven":        func(d map[string]any) { d["chunk_length"] = 1000 },
		"huge piece":    func(d map[string]any) { d["piece_length"] = 1 << 30 },
		"missing piece": func(d map[string]any) { delete(d, "piece_length") },
	} {
		d := ligaDescriptor(t)
		mutate(d)
		plain, _ := Encode(d)
		if _, err := ParseDescriptor(plain); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := ParseDescriptor([]byte("li1ee")); err == nil {
		t.Error("a list is not a descriptor")
	}
}

// Every real blob must decrypt to the infohash the real engine reported for
// that content id (sports-web /api/peers, 2026-10-22).
func TestRealBlobsMatchEngineInfohash(t *testing.T) {
	raw, err := os.ReadFile("testdata/blobs/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) < 5 {
		t.Fatalf("only %d fixtures", len(expected))
	}
	for cid, want := range expected {
		blob, err := os.ReadFile("testdata/blobs/" + cid + ".bin")
		if err != nil {
			t.Fatal(err)
		}
		plain, err := DecryptTransport(blob)
		if err != nil {
			t.Fatalf("%s: %v", cid, err)
		}
		d, err := ParseDescriptor(plain)
		if err != nil {
			t.Fatalf("%s: %v", cid, err)
		}
		if got := hex.EncodeToString(d.InfoHash[:]); got != want {
			t.Errorf("%s: infohash %s, engine says %s", cid, got, want)
		}
	}
}

func encryptTransport(t *testing.T, plain []byte) []byte {
	t.Helper()
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(transportKey)
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, transportIV).CryptBlocks(out, plain)
	return append([]byte(transportMagic+"\x00\x02"), out...)
}

func TestDecryptTransportErrors(t *testing.T) {
	good := encryptTransport(t, []byte("de"))
	if _, err := DecryptTransport(good); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"bad magic":     append([]byte("NotAceStreamTransp\x00\x02"), good[20:]...),
		"short":         []byte("AceStream"),
		"empty body":    []byte(transportMagic + "\x00\x02"),
		"misaligned":    good[:len(good)-1],
		"wrong padding": append(append([]byte{}, good[:20]...), bytes.Repeat([]byte{7}, 16)...),
	}
	for name, blob := range cases {
		if _, err := DecryptTransport(blob); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNormalizeContentID(t *testing.T) {
	ok := map[string]string{
		"e3816fbc661fbc8b2b656aee9483d0a55a1608b6":              "e3816fbc661fbc8b2b656aee9483d0a55a1608b6",
		"acestream://E3816FBC661FBC8B2B656AEE9483D0A55A1608B6":  "e3816fbc661fbc8b2b656aee9483d0a55a1608b6",
		" acestream://e3816fbc661fbc8b2b656aee9483d0a55a1608b6": "e3816fbc661fbc8b2b656aee9483d0a55a1608b6",
	}
	for in, want := range ok {
		if got, err := NormalizeContentID(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "e3816fbc661fbc8b2b656aee9483d0a55a1608b", "e3816fbc661fbc8b2b656aee9483d0a55a1608b6aa",
		"z3816fbc661fbc8b2b656aee9483d0a55a1608b6", "../../../../../../../../../../etc/passwd/x"} {
		if _, err := NormalizeContentID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func resolverServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestResolveFetchesAndFailsOver(t *testing.T) {
	plain, _ := Encode(ligaDescriptor(t))
	blob := base64.StdEncoding.EncodeToString(encryptTransport(t, plain))
	var hits atomic.Int32
	broken := resolverServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "boom", 500)
	})
	good := resolverServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/gettorrent" || q.Get("pid") != "e572a5178ff72eed7d1d751a18b4b3419699f370" {
			http.Error(w, "bad request", 400)
			return
		}
		params := map[string]string{}
		for k := range q {
			if k != "_s" {
				params[k] = q.Get(k)
			}
		}
		if SignQuery(params) != q.Get("_s") {
			http.Error(w, "bad signature", 403)
			return
		}
		// Real responses wrap the base64 across lines.
		fmt.Fprintf(w, `<response sig="x" r="1"><torrent>%s
%s</torrent></response>`, blob[:40], blob[40:])
	})
	r := NewResolver(&net.Dialer{})
	r.Hosts = []string{broken, good}
	d, err := r.Resolve(context.Background(), "acestream://E572A5178FF72EED7D1D751A18B4B3419699F370")
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(d.InfoHash[:]) != "8c1c3eae077f3a786ed2f0a426197ea93fdf7373" || hits.Load() != 1 {
		t.Fatalf("%x hits=%d", d.InfoHash, hits.Load())
	}
}

func TestResolveNotFoundStopsFailover(t *testing.T) {
	var second atomic.Int32
	empty := resolverServer(t, func(w http.ResponseWriter, r *http.Request) {})
	other := resolverServer(t, func(w http.ResponseWriter, r *http.Request) { second.Add(1) })
	r := NewResolver(&net.Dialer{})
	r.Hosts = []string{empty, other}
	_, err := r.Resolve(context.Background(), strings.Repeat("0", 40))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if second.Load() != 0 {
		t.Fatal("a definitive not-found was retried on another host")
	}
}

func TestResolveRejectsBadIDWithoutNetwork(t *testing.T) {
	r := NewResolver(&net.Dialer{})
	r.Hosts = []string{"127.0.0.1:1"}
	if _, err := r.Resolve(context.Background(), "nope"); err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("%v", err)
	}
}

// Movies: the resolver answers with a plain .torrent instead of a transport blob.
func TestResolvePlainTorrentIsAMovie(t *testing.T) {
	raw, _ := Encode(map[string]any{
		"announce":      "https://tracker.example/announce",
		"announce-list": []any{[]any{"https://tracker.example/announce", "udp://other.example:6969/announce"}},
		"info": map[string]any{
			"name": "Movie", "piece length": int64(4 << 20), "length": int64(10), "pieces": strings.Repeat("x", 20),
		},
	})
	blob := base64.StdEncoding.EncodeToString(raw)
	host := resolverServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<response><torrent>%s</torrent></response>`, blob)
	})
	r := NewResolver(&net.Dialer{})
	r.Hosts = []string{host}
	d, err := r.Resolve(context.Background(), strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.Torrent, raw) || d.Name != "Movie" {
		t.Fatalf("torrent %d bytes, name %q", len(d.Torrent), d.Name)
	}
	if strings.Join(d.Trackers, " ") != "https://tracker.example/announce udp://other.example:6969/announce" {
		t.Fatalf("trackers %q", d.Trackers)
	}
}
