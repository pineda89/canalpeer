package warp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

func keyPair(t *testing.T) (priv, pub []byte) {
	t.Helper()
	priv = make([]byte, 32)
	priv[0] = byte(time.Now().UnixNano()) | 1
	for i := 1; i < 32; i++ {
		priv[i] = byte(i * 37)
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

// dialServer is a check that reaches for a host behind the tunnel, which
// makes WireGuard handshake, and fails however that goes.
func dialServer(ctx context.Context, n *netstack.Net) error {
	c, err := n.DialContext(ctx, "tcp", "10.9.9.9:80")
	if err == nil {
		c.Close()
	}
	return fmt.Errorf("check: %v", err)
}

// An endpoint that never answers (a network dropping UDP) is ErrNoAnswer.
func TestUpSilentEndpointIsNoAnswer(t *testing.T) {
	sink, err := net.ListenPacket("udp", "127.0.0.1:0") // reads nothing, answers nothing
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	priv, _ := keyPair(t)
	_, peer := keyPair(t)
	a := Account{PrivateKey: priv, PeerPublicKey: peer, Address: netip.MustParseAddr("172.16.0.2")}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = Up(ctx, a, sink.LocalAddr().String(), dialServer)
	if !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err %v, want ErrNoAnswer", err)
	}
}

// An endpoint that handshakes but whose check fails is not ErrNoAnswer.
func TestUpAnsweringEndpointIsNotNoAnswer(t *testing.T) {
	cpriv, cpub := keyPair(t)
	spriv := make([]byte, 32)
	copy(spriv, cpriv)
	spriv[5] ^= 0xff
	spub, _ := curve25519.X25519(spriv, curve25519.Basepoint)

	stun, _, err := netstack.CreateNetTUN([]netip.Addr{netip.MustParseAddr("10.9.9.9")}, nil, 1280)
	if err != nil {
		t.Fatal(err)
	}
	srv := device.NewDevice(stun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	defer srv.Close()
	free, _ := net.ListenPacket("udp", "127.0.0.1:0")
	port := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()
	if err := srv.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=%d\npublic_key=%s\nallowed_ip=172.16.0.2/32\n",
		hex.EncodeToString(spriv), port, hex.EncodeToString(cpub))); err != nil {
		t.Fatal(err)
	}
	if err := srv.Up(); err != nil {
		t.Fatal(err)
	}
	a := Account{PrivateKey: cpriv, PeerPublicKey: spub, Address: netip.MustParseAddr("172.16.0.2")}
	_, err = Up(context.Background(), a, fmt.Sprintf("127.0.0.1:%d", port), dialServer)
	t.Log(err)
	if err == nil || errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err %v, want the check's error, not ErrNoAnswer", err)
	}
}

// A failure to build the local device (here an endpoint wireguard-go cannot
// parse) is ErrSetup: nothing was sent, so it says nothing about the account.
func TestUpLocalFailureIsSetup(t *testing.T) {
	priv, _ := keyPair(t)
	_, peer := keyPair(t)
	a := Account{PrivateKey: priv, PeerPublicKey: peer, Address: netip.MustParseAddr("172.16.0.2")}
	_, err := Up(context.Background(), a, "engage.cloudflareclient.com:2408", dialServer)
	if !errors.Is(err, ErrSetup) || errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err %v, want ErrSetup", err)
	}
}
