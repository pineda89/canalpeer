package warp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"
)

const regAnswer = `{"id":"x","config":{"peers":[{"public_key":"bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=","endpoint":{"v4":"162.159.192.7:0","v6":"[2606:4700:d0::a29f:c007]:0","host":"engage.cloudflareclient.com:2408"}}],"interface":{"addresses":{"v4":"172.16.0.2","v6":"2606:4700:110:8a36::1"}}}}`

func TestRegisterParsesAnswer(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		io.WriteString(w, regAnswer)
	}))
	defer srv.Close()

	a, err := Register(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := base64.StdEncoding.DecodeString("bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=")
	if !bytes.Equal(a.PeerPublicKey, peer) {
		t.Errorf("peer key %x", a.PeerPublicKey)
	}
	if a.Address.String() != "172.16.0.2" {
		t.Errorf("address %v", a.Address)
	}
	if a.Host != "engage.cloudflareclient.com:2408" {
		t.Errorf("host %q", a.Host)
	}
	pub, _ := curve25519.X25519(a.PrivateKey, curve25519.Basepoint)
	if body["key"] != base64.StdEncoding.EncodeToString(pub) {
		t.Errorf("sent key %v, want public key of PrivateKey", body["key"])
	}
	for _, k := range []string{"install_id", "fcm_token", "serial_number"} {
		if v, ok := body[k]; ok && v != "" {
			t.Errorf("%s = %v, want empty", k, v)
		}
	}
}

func TestRegisterHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := Register(context.Background(), srv.Client(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want one mentioning 403", err)
	}
}

func TestAccountRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := fakeServer(t)
	a, err := Register(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	a.LastEndpoint = "162.159.192.1:4500"
	if err := SaveAccount(dir, a); err != nil {
		t.Fatal(err)
	}
	got, ok := LoadAccount(dir)
	if !ok || !reflect.DeepEqual(got, a) {
		t.Fatalf("Load = %+v, %v; want %+v", got, ok, a)
	}
	fi, err := os.Stat(filepath.Join(dir, "warp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if _, ok := LoadAccount(filepath.Join(dir, "missing")); ok {
		t.Error("Load of a missing dir reported ok")
	}
}

func fakeServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, regAnswer)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEndpointsOrder(t *testing.T) {
	a := Account{Host: "engage.cloudflareclient.com:2408", LastEndpoint: "162.159.193.1:500"}
	eps := Endpoints(a)
	if eps[0] != a.LastEndpoint {
		t.Errorf("first = %q, want LastEndpoint", eps[0])
	}
	if len(eps) != 12 { // 1 + 2×4 + 4, the last endpoint is also in the fixed list
		t.Errorf("len = %d, want 12: %v", len(eps), eps)
	}
	seen := map[string]bool{}
	for _, e := range eps {
		if seen[e] {
			t.Errorf("duplicate %q", e)
		}
		seen[e] = true
	}
	if !seen["engage.cloudflareclient.com:4500"] {
		t.Errorf("hostname ports missing: %v", eps)
	}
}
