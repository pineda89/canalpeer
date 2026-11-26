package mobile

import (
	"os"
	"testing"
)

// tsWithPCR is a TS packet on PID 0x100 carrying pcr (27 MHz), or none if pcr < 0.
func tsWithPCR(pcr int64) []byte {
	p := make([]byte, tsPacket)
	p[0], p[1], p[2] = 0x47, 0x01, 0x00
	if pcr < 0 {
		p[3] = 0x10
		return p
	}
	base, ext := pcr/300, pcr%300
	p[3], p[4], p[5] = 0x30, 7, 0x10
	p[6], p[7], p[8], p[9] = byte(base>>25), byte(base>>17), byte(base>>9), byte(base>>1)
	p[10] = byte(base<<7) | byte(ext>>8)
	p[11] = byte(ext)
	return p
}

// stream is secs of TS at bytesPerSec, a PCR every 40 ms, starting at pcr0.
func stream(secs, bytesPerSec, pcr0 int64) []byte {
	var out []byte
	perTick := bytesPerSec / 25 / tsPacket // packets per 40 ms
	for t := int64(0); t < secs*25; t++ {
		out = append(out, tsWithPCR(pcr0+t*pcrHz/25)...)
		for range perTick - 1 {
			out = append(out, tsWithPCR(-1)...)
		}
	}
	return out
}

func feed(m *rateMeter, b []byte) {
	for len(b) > 0 { // odd-sized writes split packets
		n := min(len(b), 16_381)
		m.Write(b[:n])
		b = b[n:]
	}
}

func TestRateMeterMeasuresFromPCR(t *testing.T) {
	m := newRateMeter()
	feed(m, stream(3, 1_000_000, 1_000*pcrHz))
	if r := m.Bitrate(); r != 0 {
		t.Fatalf("measured over less than minSpanPCR: %d", r)
	}
	feed(m, stream(10, 1_000_000, 1_003*pcrHz))
	if r := m.Bitrate(); r < 980_000 || r > 1_020_000 {
		t.Fatalf("rate %d, want ~1,000,000", r)
	}
	// The source restarts its clock and halves its rate: start over.
	feed(m, stream(10, 500_000, 5*pcrHz))
	if r := m.Bitrate(); r < 490_000 || r > 510_000 {
		t.Fatalf("after a PCR restart: rate %d, want ~500,000", r)
	}
}

// CANALPEER_TS=capture.ts checks a real capture against ffprobe's figure.
func TestRateMeterOnCapture(t *testing.T) {
	path := os.Getenv("CANALPEER_TS")
	if path == "" {
		t.Skip("CANALPEER_TS not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := newRateMeter()
	feed(m, b)
	t.Logf("%s: %d B/s (%.2f Mbit/s)", path, m.Bitrate(), float64(m.Bitrate())*8/1e6)
}
