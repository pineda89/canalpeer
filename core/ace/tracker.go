package ace

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"time"
)

// Dialer opens every outbound connection the core makes: resolver HTTP,
// tracker UDP and peer TCP. *net.Dialer satisfies it; the VPN proxy is attached
// by passing another implementation.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

const (
	bep15ProtocolID = 0x41727101980
	actionConnect   = 0
	actionAnnounce  = 1
	actionError     = 3
)

// TrackerTimeout is how long one UDP round trip waits before retrying.
var TrackerTimeout = 5 * time.Second

// Announce performs a BEP15 connect + announce against one udp:// tracker and
// returns the compact IPv4 peer list.
func Announce(ctx context.Context, d Dialer, trackerURL string, infohash, peerID [20]byte, port uint16, numWant int) ([]netip.AddrPort, error) {
	u, err := url.Parse(trackerURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "udp" {
		return nil, fmt.Errorf("not a udp tracker: %s", trackerURL)
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), "2710")
	}
	conn, err := d.DialContext(ctx, "udp", host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()

	txn := randUint32()
	req := binary.BigEndian.AppendUint64(nil, bep15ProtocolID)
	req = binary.BigEndian.AppendUint32(req, actionConnect)
	req = binary.BigEndian.AppendUint32(req, txn)
	resp, err := udpRoundTrip(ctx, conn, req, txn, 3)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", host, err)
	}
	if len(resp) < 16 {
		return nil, fmt.Errorf("connect %s: short response", host)
	}
	connID := binary.BigEndian.Uint64(resp[8:16])

	txn = randUint32()
	req = binary.BigEndian.AppendUint64(nil, connID)
	req = binary.BigEndian.AppendUint32(req, actionAnnounce)
	req = binary.BigEndian.AppendUint32(req, txn)
	req = append(req, infohash[:]...)
	req = append(req, peerID[:]...)
	req = binary.BigEndian.AppendUint64(req, 0) // downloaded
	req = binary.BigEndian.AppendUint64(req, 0) // left
	req = binary.BigEndian.AppendUint64(req, 0) // uploaded
	req = binary.BigEndian.AppendUint32(req, 0) // event: none
	req = binary.BigEndian.AppendUint32(req, 0) // ip: sender's
	req = binary.BigEndian.AppendUint32(req, randUint32())
	req = binary.BigEndian.AppendUint32(req, uint32(int32(numWant)))
	req = binary.BigEndian.AppendUint16(req, port)
	resp, err = udpRoundTrip(ctx, conn, req, txn, 3)
	if err != nil {
		return nil, fmt.Errorf("announce %s: %w", host, err)
	}
	if len(resp) < 20 {
		return nil, fmt.Errorf("announce %s: short response", host)
	}
	// action(4) txn(4) interval(4) leechers(4) seeders(4), then 6 bytes per peer
	body := resp[20:]
	peers := make([]netip.AddrPort, 0, len(body)/6)
	for i := 0; i+6 <= len(body); i += 6 {
		addr := netip.AddrFrom4([4]byte(body[i : i+4]))
		port := binary.BigEndian.Uint16(body[i+4 : i+6])
		if port == 0 || addr.IsUnspecified() {
			continue
		}
		peers = append(peers, netip.AddrPortFrom(addr, port))
	}
	return peers, nil
}

// udpRoundTrip sends req and returns the reply carrying txn, retrying on timeout
// and ignoring stray datagrams.
func udpRoundTrip(ctx context.Context, conn net.Conn, req []byte, txn uint32, retries int) ([]byte, error) {
	buf := make([]byte, 4096)
	for attempt := 0; attempt < retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(TrackerTimeout)
		conn.SetReadDeadline(deadline)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() && time.Now().After(deadline.Add(-time.Millisecond)) {
					break // retry
				}
				return nil, err
			}
			if n < 8 || binary.BigEndian.Uint32(buf[4:8]) != txn {
				continue
			}
			if binary.BigEndian.Uint32(buf[0:4]) == actionError {
				return nil, fmt.Errorf("tracker error: %q", buf[8:n])
			}
			return append([]byte(nil), buf[:n]...), nil
		}
	}
	return nil, errors.New("no response")
}

func randUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
