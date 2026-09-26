package trust

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
)

// TransportTrusting is a copy of the default transport whose root pool is
// the system pool plus the certificates in pem, such as Overcast's own CA.
func TransportTrusting(pem []byte) *http.Transport {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	pool.AppendCertsFromPEM(pem)
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		tr = &http.Transport{}
	}
	tr = tr.Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return tr
}
