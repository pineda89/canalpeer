package ace

import (
	"crypto/sha1"
	"math/big"
	"testing"
)

// The key, the SHA-1 of the content and the trailer of one live piece
// (channel 0364449064d322a1bc0f97d568452053810ef8bc, piece 14626602).
const (
	liveKey = "307a300d06092a864886f70d01010105000369003066026100b3c54151fb6e49e63b3a726627dc58face690aeaec60b09eaad391e216afeea2788574c0b8751a08d022478e1cee9f12887c867d1c1b341dc6785a911128f9f8c3d88e4f54e52ff259419db3e547d91a68f1fb461a961b8d994d3ca05651b289020103"
	liveSum = "06507e4223df886e89740fb2d8e150757c722b5e"
	liveSig = "4dbb68ddaf8d63a0e57851a8c36cad03a5ac0d72ce337a10335231c2f6941e4ae08ad2443d72fb3d7ee06854bf8e41e924e578bc358b3e2687d44ebd2c91d4f62295c2606181d3f608338b0b6a7e1ce99ead507b2f5c066fd920ec0b571c7449"
)

func TestPieceSignature(t *testing.T) {
	der := mustHex(liveKey)
	if pieceCheck(der) == nil {
		t.Fatal("a channel's key is not understood")
	}
	if pieceCheck(nil) != nil || pieceCheck(der[:50]) != nil {
		t.Fatal("no key must mean no check")
	}
	n := new(big.Int).SetBytes(der[len(der)-3-PieceSigLen : len(der)-3])
	e := big.NewInt(3)
	var sum [sha1.Size]byte
	copy(sum[:], mustHex(liveSum))
	sig := mustHex(liveSig)
	if !sigMatches(n, e, sum, sig) {
		t.Fatal("a live piece's signature is rejected")
	}
	sum[0] ^= 1
	if sigMatches(n, e, sum, sig) {
		t.Fatal("accepted for other content")
	}
	sum[0] ^= 1
	sig[len(sig)-1] ^= 1
	if sigMatches(n, e, sum, sig) {
		t.Fatal("a changed signature is accepted")
	}
	// The e=3 forgery: the cube root of a block with the digest right after
	// short padding, and garbage after it. Only a parser accepts it.
	block := append([]byte{0, 1, 0xff, 0}, sha1Info...)
	block = append(block, sum[:]...)
	block = append(block, make([]byte, PieceSigLen-len(block))...)
	for i := len(block) - 40; i < len(block); i++ {
		block[i] = 0xff
	}
	root := cubeRoot(new(big.Int).SetBytes(block))
	if sigMatches(n, e, sum, root.FillBytes(make([]byte, PieceSigLen))) {
		t.Fatal("a forged signature is accepted")
	}
}

func cubeRoot(x *big.Int) *big.Int {
	lo, hi := big.NewInt(0), new(big.Int).Set(x)
	for lo.Cmp(hi) < 0 {
		mid := new(big.Int).Add(lo, hi)
		mid.Add(mid, big.NewInt(1)).Rsh(mid, 1)
		if new(big.Int).Exp(mid, big.NewInt(3), nil).Cmp(x) <= 0 {
			lo = mid
		} else {
			hi = mid.Sub(mid, big.NewInt(1))
		}
	}
	return lo
}
