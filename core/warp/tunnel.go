package warp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// ErrNoAnswer wraps Up's error when the endpoint never completed a WireGuard
// handshake: nothing answered (a network that drops UDP looks like this).
var ErrNoAnswer = errors.New("warp: no handshake")

// ErrSetup wraps Up's error when the local device could not be built or
// configured: nothing was sent, so it says nothing about the account.
var ErrSetup = errors.New("warp: local setup")

// Tunnel is a WireGuard device to WARP running in userspace.
type Tunnel struct {
	dev *device.Device
	net *netstack.Net
}

// Up brings a tunnel up to endpoint and runs check through it (6 s at most);
// when check fails the tunnel is closed and check's error returned, wrapped
// in ErrNoAnswer when the endpoint never handshook. Local failures before
// anything is sent wrap ErrSetup.
func Up(ctx context.Context, a Account, endpoint string, check func(ctx context.Context, n *netstack.Net) error) (*Tunnel, error) {
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{a.Address}, []netip.Addr{netip.MustParseAddr("1.1.1.1")}, 1280)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSetup, err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t := &Tunnel{dev: dev, net: tnet}
	cfg := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\npersistent_keepalive_interval=25\n",
		hex.EncodeToString(a.PrivateKey), hex.EncodeToString(a.PeerPublicKey), endpoint)
	if err := dev.IpcSet(cfg); err != nil {
		t.Close()
		return nil, fmt.Errorf("%w: configure %s: %w", ErrSetup, endpoint, err) // IpcSet errors never echo the key
	}
	if err := dev.Up(); err != nil {
		t.Close()
		return nil, fmt.Errorf("%w: %w", ErrSetup, err)
	}
	cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := check(cctx, tnet); err != nil {
		if !t.handshook() {
			err = fmt.Errorf("%w: %w", ErrNoAnswer, err)
		}
		t.Close()
		return nil, err
	}
	return t, nil
}

// handshook reports whether the device ever completed a handshake.
func (t *Tunnel) handshook() bool {
	s, err := t.dev.IpcGet()
	return err == nil && strings.Contains(s, "last_handshake_time_sec=") && !strings.Contains(s, "last_handshake_time_sec=0\n")
}

// Net is the tunnel's network: dial through it.
func (t *Tunnel) Net() *netstack.Net { return t.net }

// Close tears the device down.
func (t *Tunnel) Close() { t.dev.Close() }

// TraceCheck asks Cloudflare's trace page through n; OK only when it says the
// request arrived over WARP.
func TraceCheck(ctx context.Context, n *netstack.Net) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return err
	}
	c := &http.Client{Transport: &http.Transport{DialContext: n.DialContext}}
	defer c.CloseIdleConnections()
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("warp trace: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("warp trace: %w", err)
	}
	if !bytes.Contains(b, []byte("\nwarp=on\n")) && !bytes.HasPrefix(b, []byte("warp=on\n")) {
		return fmt.Errorf("warp trace: not over WARP (HTTP %d)", resp.StatusCode)
	}
	return nil
}
