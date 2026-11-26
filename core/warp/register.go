// Package warp registers a Cloudflare WARP device and brings a WireGuard
// tunnel to it up in userspace (wireguard-go on a gVisor netstack): no OS
// VPN, no permissions.
package warp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/curve25519"
)

// APIURL is Cloudflare's client registration endpoint.
const APIURL = "https://api.cloudflareclient.com/v0a2158/reg"

const accountFile = "warp.json"

// Account is one registered WARP device. PrivateKey is secret: never log it.
type Account struct {
	PrivateKey    []byte
	PeerPublicKey []byte
	Address       netip.Addr
	Host          string // "engage.cloudflareclient.com:2408"
	LastEndpoint  string // the endpoint that last brought a tunnel up
	// NoReregisterUntil: a new account was silent too (the network, not the
	// key), so none is registered again before then.
	NoReregisterUntil time.Time `json:",omitzero"`
}

// Register creates a new device with a fresh X25519 key. Nothing identifying
// is sent: generic model and locale, empty install id, token and serial.
func Register(ctx context.Context, c *http.Client, apiURL string) (Account, error) {
	priv := make([]byte, 32)
	if _, err := rand.Read(priv); err != nil {
		return Account{}, err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return Account{}, err
	}
	body, _ := json.Marshal(map[string]string{
		"key": base64.StdEncoding.EncodeToString(pub), "install_id": "", "fcm_token": "",
		"tos": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "model": "PC", "serial_number": "", "locale": "en_US",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("CF-Client-Version", "a-6.10-2158")
	req.Header.Set("User-Agent", "okhttp/3.12.1")
	resp, err := c.Do(req)
	if err != nil {
		return Account{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Account{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Account{}, fmt.Errorf("warp register: HTTP %d: %.200s", resp.StatusCode, b)
	}
	var r struct {
		Config struct {
			Peers []struct {
				PublicKey string `json:"public_key"`
				Endpoint  struct {
					Host string `json:"host"`
				} `json:"endpoint"`
			} `json:"peers"`
			Interface struct {
				Addresses struct {
					V4 string `json:"v4"`
				} `json:"addresses"`
			} `json:"interface"`
		} `json:"config"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return Account{}, fmt.Errorf("warp register: %w", err)
	}
	if len(r.Config.Peers) == 0 {
		return Account{}, errors.New("warp register: no peer in answer")
	}
	p := r.Config.Peers[0]
	peer, err := base64.StdEncoding.DecodeString(p.PublicKey)
	if err != nil || len(peer) != 32 {
		return Account{}, fmt.Errorf("warp register: bad peer key %q", p.PublicKey)
	}
	addr, err := netip.ParseAddr(r.Config.Interface.Addresses.V4)
	if err != nil {
		return Account{}, fmt.Errorf("warp register: %w", err)
	}
	return Account{PrivateKey: priv, PeerPublicKey: peer, Address: addr, Host: p.Endpoint.Host}, nil
}

// LoadAccount reads the account stored in dir; false when there is none or it
// is unreadable.
func LoadAccount(dir string) (Account, bool) {
	b, err := os.ReadFile(filepath.Join(dir, accountFile))
	if err != nil {
		return Account{}, false
	}
	var a Account
	if json.Unmarshal(b, &a) != nil || len(a.PrivateKey) != 32 || len(a.PeerPublicKey) != 32 || !a.Address.IsValid() {
		return Account{}, false
	}
	return a, true
}

// SaveAccount replaces the stored account atomically, readable by the owner
// only.
func SaveAccount(dir string, a Account) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "warp-*.tmp") // 0600
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, accountFile)); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// Endpoints lists where to try the tunnel, in order: the last working one,
// then the fixed addresses, then the registration's hostname, each on every
// WARP port.
func Endpoints(a Account) []string {
	hosts := []string{"162.159.192.1", "162.159.193.1"}
	if h, _, err := net.SplitHostPort(a.Host); err == nil && h != "" {
		hosts = append(hosts, h)
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(e string) {
		if e != "" && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	add(a.LastEndpoint)
	for _, h := range hosts {
		for _, port := range []string{"2408", "500", "1701", "4500"} {
			add(net.JoinHostPort(h, port))
		}
	}
	return out
}
