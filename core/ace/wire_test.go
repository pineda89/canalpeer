package ace

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"testing"
)

func TestHandshakeMatchesPython(t *testing.T) {
	v := loadVectors(t)
	var ih, pid [20]byte
	for i := range ih {
		ih[i] = 1
	}
	copy(pid[:], "R30")
	hs := BuildHandshake(ih, pid)
	if !bytes.Equal(hs, unhex(t, v.Handshake)) {
		t.Fatalf("handshake differs from Python:\n%x", hs)
	}
	if len(hs) != HandshakeLen {
		t.Fatalf("len %d", len(hs))
	}
	h, err := ParseHandshake(hs)
	if err != nil {
		t.Fatal(err)
	}
	if h.Protocol != PSTR || h.InfoHash != ih || h.PeerID != pid || h.Reserved != [8]byte{} {
		t.Fatalf("parsed %+v", h)
	}
}

func TestParseHandshakeTooShort(t *testing.T) {
	for _, b := range [][]byte{nil, {17}, append([]byte{17}, make([]byte, 40)...)} {
		if _, err := ParseHandshake(b); err == nil {
			t.Errorf("%d bytes: expected an error", len(b))
		}
	}
}

func TestFramesMatchPython(t *testing.T) {
	v := loadVectors(t)
	if got := BuildRequest(65520282, 63); !bytes.Equal(got, unhex(t, v.Request)) {
		t.Errorf("request %x", got)
	}
	if got := BuildMessage(MsgInterested, nil); !bytes.Equal(got, unhex(t, v.Interested)) {
		t.Errorf("interested %x", got)
	}
}

func TestReadMessage(t *testing.T) {
	var stream bytes.Buffer
	stream.Write([]byte{0, 0, 0, 0})
	stream.Write(BuildMessage(MsgUnchoke, nil))
	stream.Write(BuildMessage(MsgHave, []byte{0, 0, 0, 0, 3, 231, 194, 154}))
	id, p, err := ReadMessage(&stream, MaxMessageLen)
	if err != nil || id != MsgKeepAlive || p != nil {
		t.Fatalf("keepalive: %d %v %v", id, p, err)
	}
	id, p, err = ReadMessage(&stream, MaxMessageLen)
	if err != nil || id != MsgUnchoke || len(p) != 0 {
		t.Fatalf("unchoke: %d %v %v", id, p, err)
	}
	id, p, err = ReadMessage(&stream, MaxMessageLen)
	if err != nil || id != MsgHave {
		t.Fatalf("have: %d %v", id, err)
	}
	if pi, err := ParseHave(p); err != nil || pi != 65520282 {
		t.Fatalf("have index %d %v", pi, err)
	}
	if _, _, err := ReadMessage(&stream, MaxMessageLen); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
}

func TestReadMessageRejectsHugeLength(t *testing.T) {
	r := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 7})
	if _, _, err := ReadMessage(r, MaxMessageLen); err == nil {
		t.Fatal("expected an error for a 4 GiB frame")
	}
}

func TestReadMessageTruncatedBody(t *testing.T) {
	r := bytes.NewReader([]byte{0, 0, 0, 10, 7, 1, 2})
	if _, _, err := ReadMessage(r, MaxMessageLen); err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v", err)
	}
}

func TestParsePiece(t *testing.T) {
	p := make([]byte, 18, 18+16384)
	binary.BigEndian.PutUint64(p[0:], 65520282)
	binary.BigEndian.PutUint64(p[8:], math.Float64bits(1792691306.917))
	binary.BigEndian.PutUint16(p[16:], 3)
	p = append(p, bytes.Repeat([]byte{0xab}, 16384)...)
	got, err := ParsePiece(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Index != 65520282 || got.Chunk != 3 || got.Timestamp != 1792691306.917 || len(got.Data) != 16384 {
		t.Fatalf("%+v", got)
	}
	if _, err := ParsePiece(p[:17]); err == nil {
		t.Fatal("expected an error for a short payload")
	}
	binary.BigEndian.PutUint64(p[0:], math.MaxUint64)
	if _, err := ParsePiece(p); err == nil {
		t.Fatal("expected an error for an out-of-range index")
	}
}

func TestParseHaveShort(t *testing.T) {
	if _, err := ParseHave([]byte{1, 2, 3, 4}); err == nil {
		t.Fatal("a 4-byte HAVE must be rejected: peers send 8")
	}
}

func tsPackets(n int) []byte {
	b := make([]byte, n*TSPacket)
	for i := 0; i < n; i++ {
		b[i*TSPacket] = TSSync
		b[i*TSPacket+1] = byte(i)
	}
	return b
}

func TestFindTSSync(t *testing.T) {
	ts := tsPackets(20)
	if o := FindTSSync(ts, 8); o != 0 {
		t.Errorf("aligned: %d", o)
	}
	lead := append([]byte{0x47, 1, 2, 3, 4}, ts...)
	if o := FindTSSync(lead, 8); o != 5 {
		t.Errorf("lead-in: %d", o)
	}
	if o := FindTSSync(tsPackets(3), 8); o != -1 {
		t.Errorf("too short: %d", o)
	}
	if o := FindTSSync(make([]byte, 4000), 8); o != -1 {
		t.Errorf("no sync: %d", o)
	}
}
