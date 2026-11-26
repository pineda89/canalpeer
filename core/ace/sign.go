package ace

import (
	"crypto/ed25519"
	"crypto/sha256"
	"net"
)

// SignExtHandshake returns the bencoded, Ed25519-signed ext-handshake dict
// (without the leading sub-id byte), the way the engine signs:
//
//  1. bencode the fields plus node_id (our public key) and a 64-byte zero
//     signature, with standard sorted keys;
//  2. sig = Ed25519(sha256(that), key);
//  3. the payload is the same dict carrying the real signature.
//
// The reference client (p2p_client/sign.py, removed; see git history) put node_id and signature first
// instead. A real engine-signed message (testdata/ext_template.bin) only
// verifies under standard sorting, and travels sorted, so this follows the
// engine. node_id is our own key, so the signature is self-valid.
func SignExtHandshake(fields map[string]any, key ed25519.PrivateKey) ([]byte, error) {
	d := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		d[k] = v
	}
	d["node_id"] = []byte(key.Public().(ed25519.PublicKey))
	d["signature"] = make([]byte, ed25519.SignatureSize)
	unsigned, err := Encode(d)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(unsigned)
	d["signature"] = ed25519.Sign(key, digest[:])
	return Encode(d)
}

// extFields is the full field set observed on successful exchanges (peers that
// went on to serve pieces), from the reference client's peer.py.
func extFields(listenPort int, peerIP net.IP) map[string]any {
	mi := map[string]any{
		"distance_from_source": -1, "down_rate": 0, "download_window_end": -1,
		"is_accessible": 0, "live_window_size": 768, "lsp": -1, "mam": -1,
		"max_piece": -1, "min_piece": -1, "peer_type": 0, "ping_from_source": -1,
		"position": -1, "time_from_source": -1, "top_session_up_rate": 0,
		"top_up_rate": 0, "up_rate": 0, "upload_rating": 0,
	}
	f := map[string]any{
		"ace_metadata_version": 1, "asn": 0, "asn_country": "XX",
		"geoip_country": "XX", "lsp": -1, "m": map[string]any{"ut_metadata": 2}, "mi": mi,
		"nt": 1, "p": listenPort, "platform": 3, "pv": 2,
		"stream_statuses": map[string]any{}, "ts": 0, "tt": "bt", "v": 3018000,
	}
	// yourip is the peer's own address: an anti-spoofing check on its side.
	if ip4 := peerIP.To4(); ip4 != nil {
		f["yourip"] = []byte(ip4)
	}
	return f
}

// BuildExtHandshake returns the EXTENDED (id 20) payload: sub-id 0 followed by
// the signed dict.
func BuildExtHandshake(listenPort int, peerIP net.IP, key ed25519.PrivateKey) ([]byte, error) {
	d, err := SignExtHandshake(extFields(listenPort, peerIP), key)
	if err != nil {
		return nil, err
	}
	return append([]byte{0}, d...), nil
}
