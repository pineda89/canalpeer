package ace

import (
	"bytes"
	"crypto/sha1"
	"crypto/x509"
	"encoding/asn1"
	"math/big"
)

// sha1Info is the DigestInfo prefix PKCS#1 v1.5 puts before a SHA-1 digest.
var sha1Info = []byte{0x30, 0x21, 0x30, 0x09, 0x06, 0x05, 0x2b, 0x0e, 0x03, 0x02, 0x1a, 0x05, 0x00, 0x04, 0x14}

// pieceCheck returns the test a complete piece, signature trailer included,
// must pass: the trailer is the source's RSA signature (PKCS#1 v1.5) of the
// SHA-1 of the rest, made with the channel's key (measured on live pieces,
// 2026-11-26). It returns nil without a usable key, and the channel plays
// unverified.
//
// shortcut: the piece's index and timestamp are not signed, so a peer can
// still pass off a genuine piece of the channel as another one; nothing in
// the protocol tells them apart.
func pieceCheck(der []byte) func(piece []byte) bool {
	var spki struct {
		Algo asn1.RawValue
		Key  asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &spki); err != nil {
		return nil
	}
	// crypto/rsa refuses keys this small (768 bits, e=3), so the public
	// operation is done here.
	pub, err := x509.ParsePKCS1PublicKey(spki.Key.RightAlign())
	if err != nil || pub.N.BitLen() != PieceSigLen*8 {
		return nil
	}
	e := big.NewInt(int64(pub.E))
	return func(piece []byte) bool {
		if len(piece) <= PieceSigLen {
			return false
		}
		sum := sha1.Sum(piece[:len(piece)-PieceSigLen])
		return sigMatches(pub.N, e, sum, piece[len(piece)-PieceSigLen:])
	}
}

// sigMatches builds the whole padded block and compares it, instead of
// parsing what the signature opens to: with e=3 a parser that skips the
// padding accepts forged signatures.
func sigMatches(n, e *big.Int, sum [sha1.Size]byte, sig []byte) bool {
	s := new(big.Int).SetBytes(sig)
	if s.Cmp(n) >= 0 {
		return false
	}
	want := append([]byte{0, 1}, bytes.Repeat([]byte{0xff}, PieceSigLen-3-len(sha1Info)-len(sum))...)
	want = append(append(append(want, 0), sha1Info...), sum[:]...)
	got := new(big.Int).Exp(s, e, n).FillBytes(make([]byte, PieceSigLen))
	return bytes.Equal(got, want)
}
