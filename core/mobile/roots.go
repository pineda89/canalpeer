package mobile

import (
	"crypto/x509"
	_ "embed"
)

// Let's Encrypt's root signs canalpeer.com and raw.githubusercontent.com, but
// Android only ships it from 7.1.1: on 7.0 every fetch failed verification.
//
//go:embed isrgrootx1.pem
var isrgRootX1 []byte

// roots is the system's trust store plus ISRG Root X1.
func roots() *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(isrgRootX1)
	return pool
}
