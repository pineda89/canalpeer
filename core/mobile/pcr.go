package mobile

import "sync/atomic"

// rateMeter measures a live TS stream's real bitrate from its own clock: the
// bytes between two PCRs some seconds apart. Descriptors advertise a bitrate
// that can be far off (5.6 Mbit/s announced, 8.4 sent, measured), and every
// time-to-bytes conversion (resuming where the viewer was, the queue shown)
// went wrong by as much. Delivery bursts and stalls don't affect it.
type rateMeter struct {
	carry   []byte // the tail of a packet split across writes
	off     int64  // stream offset of carry[0]
	pid     int    // the PID whose PCR is followed; -1 until one is seen
	refOff  int64  // offset and PCR (27 MHz) of the reference packet
	refPCR  int64
	lastPCR int64
	rate    atomic.Int64 // bytes per second; 0 until measured
}

const (
	tsPacket   = 188
	pcrHz      = 27_000_000
	minSpanPCR = 5 * pcrHz // measure over at least this much stream time
	maxJumpPCR = 2 * pcrHz // a bigger gap between PCRs is a discontinuity
	pcrWrap    = (1 << 33) * 300
)

func newRateMeter() *rateMeter { return &rateMeter{pid: -1} }

// Bitrate is the measured rate in bytes per second, 0 while unknown.
func (m *rateMeter) Bitrate() int64 { return m.rate.Load() }

// Write feeds the bytes that go to the player, in order. Not safe for
// concurrent writers (the swarm has one).
func (m *rateMeter) Write(b []byte) {
	buf := append(m.carry, b...)
	i := 0
	for i+tsPacket <= len(buf) {
		if buf[i] != 0x47 { // resync after a skipped piece
			i++
			continue
		}
		m.packet(buf[i:i+tsPacket], m.off+int64(i))
		i += tsPacket
	}
	// Keep at most one partial packet; drop the scanned part.
	m.carry = append(m.carry[:0:0], buf[i:]...)
	m.off += int64(i)
}

func (m *rateMeter) packet(p []byte, at int64) {
	pid := int(p[1]&0x1f)<<8 | int(p[2])
	if p[3]&0x20 == 0 || p[4] < 7 || p[5]&0x10 == 0 { // no adaptation field, or no PCR
		return
	}
	if m.pid < 0 {
		m.pid = pid
	}
	if pid != m.pid {
		return
	}
	base := int64(p[6])<<25 | int64(p[7])<<17 | int64(p[8])<<9 | int64(p[9])<<1 | int64(p[10])>>7
	pcr := base*300 + (int64(p[10]&1)<<8 | int64(p[11]))
	if m.lastPCR == 0 || pcr < m.lastPCR && m.lastPCR-pcr < pcrWrap/2 || pcr-m.lastPCR > maxJumpPCR {
		m.refOff, m.refPCR = at, pcr // first PCR, or the clock restarted: start over
	}
	if pcr < m.refPCR { // wrapped
		m.refPCR -= pcrWrap
	}
	m.lastPCR = pcr
	if span := pcr - m.refPCR; span >= minSpanPCR {
		m.rate.Store((at - m.refOff) * pcrHz / span)
	}
}
