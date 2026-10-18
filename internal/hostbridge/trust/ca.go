// ca.go — the Overcast local Certificate Authority and its leaf minting.
//
// The CA is a plain crypto/x509 ECDSA CA persisted as two PEM files under a
// directory the caller chooses (DirFor puts it under the Overcast data dir).
// It exists so that `OVERCAST_TLS=auto` can serve browser-trusted HTTPS: the
// serve path mints a leaf covering every name Overcast advertises, and
// `overcast trust install` puts the CA certificate — never its key — into the
// system trust store. There is deliberately exactly one CA; the trust-store
// backends in this package and the TLS listeners in cmd/overcast both hang
// off the same key material.

package trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File names for the persisted key material. The rootCA* names deliberately
// match mkcert's, so users who have handled a local CA before recognise what
// they are looking at.
const (
	// CACertFile is the CA certificate (public — safe to share and to
	// install into trust stores).
	CACertFile = "rootCA.pem"
	// caKeyFile is the CA private key. Never leaves the directory.
	caKeyFile = "rootCA-key.pem"
	// leafCertFile / leafKeyFile cache the most recently minted server
	// certificate so restarts don't mint a new one every time.
	leafCertFile = "cert.pem"
	leafKeyFile  = "key.pem"
)

const (
	caCommonName = "overcast local CA"
	// caValidity is deliberately long: the CA is installed into trust stores
	// once, and rotating it invalidates that installation.
	caValidity = 10 * 365 * 24 * time.Hour
	// leafValidity stays under 825 days: Apple platforms reject longer-lived
	// TLS certificates regardless of the issuing CA (mkcert uses the same
	// bound for the same reason).
	leafValidity = 825 * 24 * time.Hour
	// leafRenewalMargin is how close to expiry a cached leaf may get before
	// ServerCertificate mints a replacement.
	leafRenewalMargin = 30 * 24 * time.Hour
)

// now is time.Now, overridable in tests to exercise expiry behaviour. The
// repo-wide clock.Clock rule targets service/handler code; certificate
// validity is host-side infrastructure that must use real wall-clock time.
var now = time.Now

// DirFor returns the CA directory under an Overcast data dir.
func DirFor(dataDir string) string { return filepath.Join(dataDir, "ca") }

// CA is a loaded local certificate authority.
type CA struct {
	// Cert is the CA certificate.
	Cert *x509.Certificate
	// CertPEM is the PEM encoding of Cert, ready to write into trust stores.
	CertPEM []byte

	key *ecdsa.PrivateKey
}

// LoadOrCreateCA loads the CA from dir, creating (and persisting) a new one
// when none exists yet. Idempotent: repeat calls return the same CA.
func LoadOrCreateCA(dir string) (*CA, error) {
	ca, err := loadCA(dir)
	if err == nil {
		return ca, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return createCA(dir)
}

func loadCA(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, fmt.Errorf("trust: read CA certificate: %w", err)
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err != nil {
		return nil, fmt.Errorf("trust: read CA key: %w", err)
	}
	cert, err := parsePEMCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("trust: parse CA certificate: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("trust: CA key file contains no PEM block")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("trust: parse CA key: %w", err)
	}
	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("trust: CA key is %T, want *ecdsa.PrivateKey", keyAny)
	}
	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

func createCA(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("trust: create CA directory: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("trust: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	notBefore := now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   caCommonName,
			Organization: []string{"Overcast development CA"},
		},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("trust: self-sign CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("trust: parse freshly-minted CA certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("trust: marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(dir, CACertFile), certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("trust: write CA certificate: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("trust: write CA key: %w", err)
	}
	return &CA{Cert: cert, CertPEM: certPEM, key: key}, nil
}

// IssueServerCert mints a leaf server certificate covering sans. Entries that
// parse as IP addresses become IP SANs; everything else is a DNS SAN.
func (ca *CA) IssueServerCert(sans []string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("trust: generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	dnsNames, ips := splitSANs(sans)
	if len(dnsNames) == 0 && len(ips) == 0 {
		return nil, nil, errors.New("trust: no usable SANs to mint a certificate for")
	}
	cn := "overcast"
	if len(dnsNames) > 0 {
		cn = dnsNames[0]
	}
	notBefore := now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("trust: sign leaf certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("trust: marshal leaf key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// issueTLSCertificate mints a leaf covering sans and returns it ready to
// serve: a tls.Certificate with Leaf populated, so the TLS stack (and
// CertSource's own coverage checks) never re-parses the DER.
func (ca *CA) issueTLSCertificate(sans []string) (*tls.Certificate, error) {
	certPEM, keyPEM, err := ca.IssueServerCert(sans)
	if err != nil {
		return nil, err
	}
	return tlsCertificateFromPEM(certPEM, keyPEM)
}

// tlsCertificateFromPEM turns a freshly-minted PEM pair into a servable
// tls.Certificate with Leaf populated.
func tlsCertificateFromPEM(certPEM, keyPEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("trust: load freshly-minted leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("trust: parse freshly-minted leaf: %w", err)
	}
	cert.Leaf = leaf
	return &cert, nil
}

// pool returns a certificate pool containing just this CA — what a client
// needs to dial a server holding one of its leaves.
func (ca *CA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// ServerCertificate returns a TLS server certificate covering sans, signed by
// the CA in dir (created on first use), plus a pool containing that CA for
// clients that need to dial the resulting server.
//
// The leaf is cached on disk next to the CA and reused across restarts; a new
// one is minted only when the cached leaf is missing, unreadable, no longer
// chains to the current CA, does not cover every requested SAN, or expires
// within leafRenewalMargin. Re-minting a leaf never touches the CA, so an
// installed trust-store entry stays valid.
//
// Caching the leaf is best-effort, and deliberately so: dir may be a
// READ-ONLY mount. Sharing one CA with an ephemeral daemon means mounting the
// directory the host owns, and mounting it `:ro` is the correct way to do
// that — the container has no business rewriting the machine's trust anchor.
// A leaf is derived data worth about a millisecond of ECDSA, so failing to
// persist it costs a re-mint per restart, not a startup. Creating the CA
// itself stays fatal: that is the one thing here that is not reproducible.
func ServerCertificate(dir string, sans []string) (tls.Certificate, *x509.CertPool, error) {
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	cert, err := ca.cachedServerCertificate(dir, sans)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return *cert, ca.pool(), nil
}

// cachedServerCertificate is ServerCertificate's body once the CA is in hand,
// shared with NewCertSource so both reach the same on-disk leaf rather than
// minting one each.
func (ca *CA) cachedServerCertificate(dir string, sans []string) (*tls.Certificate, error) {
	certPath := filepath.Join(dir, leafCertFile)
	keyPath := filepath.Join(dir, leafKeyFile)
	if cert, ok := loadCachedLeaf(certPath, keyPath, ca, sans); ok {
		return cert, nil
	}

	certPEM, keyPEM, err := ca.IssueServerCert(sans)
	if err != nil {
		return nil, err
	}
	cacheLeaf(certPath, keyPath, certPEM, keyPEM)
	return tlsCertificateFromPEM(certPEM, keyPEM)
}

// cacheLeaf persists a freshly-minted leaf, ignoring failures — see
// ServerCertificate. The key is written first and the certificate second, so a
// half-written pair never presents as a usable cache entry (loadCachedLeaf
// needs both, and reads the certificate first).
func cacheLeaf(certPath, keyPath string, certPEM, keyPEM []byte) {
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return
	}
	_ = os.WriteFile(certPath, certPEM, 0o644)
}

// loadCachedLeaf returns the on-disk leaf when it is still fit for use:
// readable, signed by ca, covering every requested SAN, and not about to
// expire. Any failure simply reports !ok — the caller mints a replacement.
func loadCachedLeaf(certPath, keyPath string, ca *CA, sans []string) (*tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, false
	}
	if err := leaf.CheckSignatureFrom(ca.Cert); err != nil {
		return nil, false
	}
	if expiringSoon(leaf) {
		return nil, false
	}
	if !leafCoversSANs(leaf, sans) {
		return nil, false
	}
	cert.Leaf = leaf
	return &cert, true
}

// expiringSoon reports whether leaf is inside leafRenewalMargin of expiry, the
// point at which a cached leaf — on disk or in a CertSource — is replaced.
func expiringSoon(leaf *x509.Certificate) bool {
	return now().Add(leafRenewalMargin).After(leaf.NotAfter)
}

// leafCoversSANs reports whether leaf carries every entry in sans, matching
// IPs as IP SANs and everything else as literal DNS SANs (a wildcard request
// must appear as that wildcard — coverage-by-wildcard is not enough, since
// the requested set is exactly what the server should present).
func leafCoversSANs(leaf *x509.Certificate, sans []string) bool {
	dnsNames, ips := splitSANs(sans)
	have := make(map[string]bool, len(leaf.DNSNames))
	for _, n := range leaf.DNSNames {
		have[strings.ToLower(n)] = true
	}
	for _, n := range dnsNames {
		if !have[n] {
			return false
		}
	}
	for _, ip := range ips {
		found := false
		for _, h := range leaf.IPAddresses {
			if h.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// splitSANs separates a SAN list into lower-cased, de-duplicated DNS names
// and parsed IP addresses, in a deterministic order.
func splitSANs(sans []string) (dnsNames []string, ips []net.IP) {
	seenDNS := make(map[string]bool, len(sans))
	seenIP := make(map[string]bool, len(sans))
	for _, s := range sans {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			if key := ip.String(); !seenIP[key] {
				seenIP[key] = true
				ips = append(ips, ip)
			}
			continue
		}
		name := strings.ToLower(s)
		if !seenDNS[name] {
			seenDNS[name] = true
			dnsNames = append(dnsNames, name)
		}
	}
	sort.Strings(dnsNames)
	sort.Slice(ips, func(i, j int) bool { return ips[i].String() < ips[j].String() })
	return dnsNames, ips
}

// ParseCertificatePEM parses the first CERTIFICATE block in pemBytes. Used
// by the /_overcast/ca.pem endpoint to refuse serving a corrupt file, and by
// the remote fetch to validate what a daemon handed back.
func ParseCertificatePEM(pemBytes []byte) (*x509.Certificate, error) {
	return parsePEMCertificate(pemBytes)
}

func parsePEMCertificate(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("no CERTIFICATE PEM block found")
	}
	return x509.ParseCertificate(block.Bytes)
}

func randomSerial() (*big.Int, error) {
	// 128-bit random serial, the standard practice for locally-minted certs.
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("trust: generate serial number: %w", err)
	}
	return serial, nil
}
