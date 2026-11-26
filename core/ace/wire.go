package ace

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// PSTR opens every AceStream handshake (pstrlen 0x11).
const PSTR = "AceStreamProtocol"

const (
	HandshakeLen = 1 + len(PSTR) + 8 + 20 + 20 // 68
	TSPacket     = 188
	TSSync       = 0x47
	// PieceSigLen is the RSA-768 signature trailer stripped from each assembled
	// piece, after pieceCheck verified it.
	PieceSigLen = 96
	// MaxMessageLen bounds a frame: a chunk is 16 KiB, so 1 MiB is generous and
	// stops a hostile length prefix from forcing a huge allocation.
	MaxMessageLen = 1 << 20
)

// BitTorrent-style message ids as AceStream uses them.
const (
	MsgChoke         = 0
	MsgUnchoke       = 1
	MsgInterested    = 2
	MsgNotInterested = 3
	MsgHave          = 4
	MsgBitfield      = 5
	MsgRequest       = 6
	MsgPiece         = 7
	MsgExtended      = 20
)

// MsgKeepAlive is what ReadMessage returns for a zero-length frame.
const MsgKeepAlive = -1

type Handshake struct {
	Protocol string
	Reserved [8]byte
	InfoHash [20]byte
	PeerID   [20]byte
}

// BuildHandshake returns the 68-byte handshake. Live streams use an all-zero
// reserved field (verified against captured traffic).
func BuildHandshake(infohash, peerID [20]byte) []byte {
	b := make([]byte, 0, HandshakeLen)
	b = append(b, byte(len(PSTR)))
	b = append(b, PSTR...)
	b = append(b, make([]byte, 8)...)
	b = append(b, infohash[:]...)
	return append(b, peerID[:]...)
}

func ParseHandshake(b []byte) (Handshake, error) {
	var h Handshake
	if len(b) < 1 || len(b) < 1+int(b[0])+48 {
		return h, errors.New("handshake too short")
	}
	o := 1 + int(b[0])
	h.Protocol = string(b[1:o])
	copy(h.Reserved[:], b[o:o+8])
	copy(h.InfoHash[:], b[o+8:o+28])
	copy(h.PeerID[:], b[o+28:o+48])
	return h, nil
}

// BuildMessage frames a message as <len:u32be><id><payload>.
func BuildMessage(id byte, payload []byte) []byte {
	b := make([]byte, 5, 5+len(payload))
	binary.BigEndian.PutUint32(b, uint32(1+len(payload)))
	b[4] = id
	return append(b, payload...)
}

// ReadMessage reads one frame. It returns MsgKeepAlive for a zero-length frame,
// and an error for frames longer than max.
func ReadMessage(r io.Reader, max int) (int, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 {
		return MsgKeepAlive, nil, nil
	}
	if uint64(n) > uint64(max) {
		return 0, nil, fmt.Errorf("frame of %d bytes exceeds %d", n, max)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return int(body[0]), body[1:], nil
}

// Piece is one PIECE (id 7) payload:
// [0:8] piece u64be, [8:16] timestamp f64be, [16:18] chunk u16be, [18:] data.
type Piece struct {
	Index     int64
	Timestamp float64
	Chunk     int
	Data      []byte
}

func ParsePiece(p []byte) (Piece, error) {
	if len(p) < 18 {
		return Piece{}, fmt.Errorf("piece payload too short (%d)", len(p))
	}
	idx := binary.BigEndian.Uint64(p[0:8])
	if idx > math.MaxInt64 {
		return Piece{}, errors.New("piece index out of range")
	}
	return Piece{
		Index:     int64(idx),
		Timestamp: math.Float64frombits(binary.BigEndian.Uint64(p[8:16])),
		Chunk:     int(binary.BigEndian.Uint16(p[16:18])),
		Data:      p[18:],
	}, nil
}

// ParseHave reads a HAVE payload. The protocol notes say uint32, but peers send
// 8 bytes (frame length 9): a uint64 piece index. The bytes win.
func ParseHave(p []byte) (int64, error) {
	if len(p) < 8 {
		return 0, fmt.Errorf("have payload too short (%d)", len(p))
	}
	v := binary.BigEndian.Uint64(p[:8])
	if v > math.MaxInt64 {
		return 0, errors.New("have index out of range")
	}
	return int64(v), nil
}

// BuildRequest frames a REQUEST for one chunk: piece u64be + chunk u16be.
func BuildRequest(piece int64, chunk int) []byte {
	var p [10]byte
	binary.BigEndian.PutUint64(p[0:8], uint64(piece))
	binary.BigEndian.PutUint16(p[8:10], uint16(chunk))
	return BuildMessage(MsgRequest, p[:])
}

// FindTSSync returns the lead-in length: the first offset in [0,188) with 0x47 at
// stride 188 for probe packets, or -1.
func FindTSSync(buf []byte, probe int) int {
	limit := min(TSPacket, len(buf)-probe*TSPacket)
	for o := 0; o <= limit; o++ {
		ok := true
		for k := 0; k < probe; k++ {
			if i := o + k*TSPacket; i >= len(buf) || buf[i] != TSSync {
				ok = false
				break
			}
		}
		if ok {
			return o
		}
	}
	return -1
}
