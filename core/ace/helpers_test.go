package ace

import (
	"crypto/ed25519"
	"crypto/sha256"
)

// ValidateTS counts whole TS packets from start and those not starting with 0x47.
func ValidateTS(buf []byte, start int) (packets, bad int) {
	packets = (len(buf) - start) / TSPacket
	for k := 0; k < packets; k++ {
		if buf[start+k*TSPacket] != TSSync {
			bad++
		}
	}
	return packets, bad
}

// VerifyExtHandshake checks a signed ext-handshake dict the way it was signed.
func VerifyExtHandshake(payload []byte) (bool, error) {
	v, err := Decode(payload)
	if err != nil {
		return false, err
	}
	d, _ := v.(map[string]any)
	pub, _ := d["node_id"].([]byte)
	sig, _ := d["signature"].([]byte)
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false, nil
	}
	d["signature"] = make([]byte, ed25519.SignatureSize)
	unsigned, err := Encode(d)
	if err != nil {
		return false, err
	}
	digest := sha256.Sum256(unsigned)
	return ed25519.Verify(pub, digest[:], sig), nil
}
