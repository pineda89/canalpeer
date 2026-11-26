package mobile

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// Android 7.0 lacks ISRG Root X1, which signs canalpeer.com and the GitHub mirror.
func TestRootsTrustISRGRootX1(t *testing.T) {
	block, _ := pem.Decode(isrgRootX1)
	if block == nil {
		t.Fatal("no PEM block")
	}
	x1, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x1.Verify(x509.VerifyOptions{Roots: roots()}); err != nil {
		t.Fatal(err)
	}
}
