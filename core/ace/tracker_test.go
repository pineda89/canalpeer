package ace

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTracker is an in-process BEP15 tracker. behave tweaks each reply.
type fakeTracker struct {
	conn      net.PacketConn
	peers     []netip.AddrPort
	mu        sync.Mutex
	infohash  [20]byte
	announces atomic.Int32
	// dropFirst ignores the first N requests, to exercise retries.
	dropFirst atomic.Int32
	// strayFirst sends a reply with the wrong txn before the real one.
	strayFirst bool
	errorMsg   string
}

func startFakeTracker(t *testing.T, peers ...netip.AddrPort) *fakeTracker {
	return startFakeTrackerWith(t, nil, peers...)
}

// startFakeTrackerWith applies configure before serving starts.
func startFakeTrackerWith(t *testing.T, configure func(*fakeTracker), peers ...netip.AddrPort) *fakeTracker {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTracker{conn: conn, peers: peers}
	if configure != nil {
		configure(ft)
	}
	t.Cleanup(func() { conn.Close() })
	go ft.serve()
	return ft
}

func (f *fakeTracker) url() string { return "udp://" + f.conn.LocalAddr().String() + "/announce" }

func (f *fakeTracker) serve() {
	buf := make([]byte, 2048)
	for {
		n, addr, err := f.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		if f.dropFirst.Load() > 0 {
			f.dropFirst.Add(-1)
			continue
		}
		req := buf[:n]
		action := binary.BigEndian.Uint32(req[8:12])
		txn := binary.BigEndian.Uint32(req[12:16])
		if f.strayFirst {
			stray := binary.BigEndian.AppendUint32(nil, action)
			stray = binary.BigEndian.AppendUint32(stray, txn+1)
			f.conn.WriteTo(append(stray, make([]byte, 12)...), addr)
		}
		if f.errorMsg != "" {
			resp := binary.BigEndian.AppendUint32(nil, actionError)
			resp = binary.BigEndian.AppendUint32(resp, txn)
			f.conn.WriteTo(append(resp, f.errorMsg...), addr)
			continue
		}
		switch action {
		case actionConnect:
			if binary.BigEndian.Uint64(req[0:8]) != bep15ProtocolID {
				continue
			}
			resp := binary.BigEndian.AppendUint32(nil, actionConnect)
			resp = binary.BigEndian.AppendUint32(resp, txn)
			resp = binary.BigEndian.AppendUint64(resp, 0xC0FFEE)
			f.conn.WriteTo(resp, addr)
		case actionAnnounce:
			if binary.BigEndian.Uint64(req[0:8]) != 0xC0FFEE || len(req) < 98 {
				continue
			}
			f.mu.Lock()
			copy(f.infohash[:], req[16:36])
			f.mu.Unlock()
			f.announces.Add(1)
			resp := binary.BigEndian.AppendUint32(nil, actionAnnounce)
			resp = binary.BigEndian.AppendUint32(resp, txn)
			resp = binary.BigEndian.AppendUint32(resp, 60)
			resp = binary.BigEndian.AppendUint32(resp, 0)
			resp = binary.BigEndian.AppendUint32(resp, uint32(len(f.peers)))
			for _, p := range f.peers {
				a := p.Addr().As4()
				resp = append(resp, a[:]...)
				resp = binary.BigEndian.AppendUint16(resp, p.Port())
			}
			f.conn.WriteTo(resp, addr)
		}
	}
}

var testIH = [20]byte{0xc2, 0x69, 0x13, 0x7a}

func TestAnnounceReturnsPeers(t *testing.T) {
	want := []netip.AddrPort{netip.MustParseAddrPort("1.2.3.4:8621"), netip.MustParseAddrPort("5.6.7.8:9000")}
	ft := startFakeTracker(t, want...)
	got, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{'R', '3', '0'}, 8621, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%v", got)
	}
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.infohash != testIH {
		t.Fatalf("tracker saw infohash %x", ft.infohash)
	}
}

func TestAnnounceSkipsUnusablePeers(t *testing.T) {
	ft := startFakeTracker(t, netip.MustParseAddrPort("0.0.0.0:8621"), netip.MustParseAddrPort("1.2.3.4:0"), netip.MustParseAddrPort("9.9.9.9:1"))
	got, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50)
	if err != nil || len(got) != 1 || got[0].String() != "9.9.9.9:1" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnnounceIgnoresStrayReplies(t *testing.T) {
	ft := startFakeTrackerWith(t, func(f *fakeTracker) { f.strayFirst = true }, netip.MustParseAddrPort("1.2.3.4:8621"))
	got, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnnounceRetriesLostDatagrams(t *testing.T) {
	old := TrackerTimeout
	TrackerTimeout = 100 * time.Millisecond
	t.Cleanup(func() { TrackerTimeout = old })
	ft := startFakeTracker(t, netip.MustParseAddrPort("1.2.3.4:8621"))
	ft.dropFirst.Store(2)
	got, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestAnnounceGivesUpAfterRetries(t *testing.T) {
	old := TrackerTimeout
	TrackerTimeout = 50 * time.Millisecond
	t.Cleanup(func() { TrackerTimeout = old })
	ft := startFakeTracker(t)
	ft.dropFirst.Store(1000)
	start := time.Now()
	if _, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("did not give up promptly")
	}
}

func TestAnnounceTrackerError(t *testing.T) {
	ft := startFakeTrackerWith(t, func(f *fakeTracker) { f.errorMsg = "torrent not registered" })
	_, err := Announce(context.Background(), &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50)
	if err == nil || !strings.Contains(err.Error(), "torrent not registered") {
		t.Fatalf("%v", err)
	}
}

func TestAnnounceHonoursCancellation(t *testing.T) {
	ft := startFakeTracker(t)
	ft.dropFirst.Store(1000)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Announce(ctx, &net.Dialer{}, ft.url(), testIH, [20]byte{}, 1, 50); err == nil {
		t.Fatal("expected an error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancellation took %v", time.Since(start))
	}
}

func TestAnnounceRejectsNonUDP(t *testing.T) {
	if _, err := Announce(context.Background(), &net.Dialer{}, "http://t1.example/announce", testIH, [20]byte{}, 1, 50); err == nil {
		t.Fatal("expected an error")
	}
}
