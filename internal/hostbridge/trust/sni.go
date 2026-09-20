// sni.go — per-handshake leaf minting, which is what makes host-routed names
// work over TLS.
//
// A TLS wildcard matches exactly one label. A leaf carrying
// "*.localhost.overcast.sh" covers "mybucket.localhost.overcast.sh" and
// nothing deeper, so every host-routed invoke URL Overcast hands out —
// "{id}.execute-api.{region}.{base}", "{id}.lambda-url.{region}.{base}",
// "{id}.appsync-api.{region}.{base}" — falls outside a leaf minted from a
// fixed SAN list. Enumerating the missing names is not a fix: the middle is
// the cross product of every host-route label with every region, on every
// base domain, and a certificate carrying that would still miss the label
// added next.
//
// A local CA does not have to enumerate — it can mint. CertSource holds the
// CA open for the life of the process and answers each ClientHello by SNI:
//
//   - a name the startup leaf already covers gets the startup leaf;
//   - a name at least one label below a domain Overcast advertises gets a
//     leaf minted for its one-label wildcard parent
//     ("*.execute-api.us-east-1.localhost.overcast.sh"), cached, so every API
//     ID in that region and every bucket in that namespace costs one mint;
//   - anything else gets the startup leaf, and the client's own name check
//     rejects it — the answer a server should give for a name it does not
//     serve, and a far more diagnosable one than an internal_error alert.
//
// Minting is about a millisecond of ECDSA and nothing minted here is written
// to disk: the on-disk pair (cert.pem / key.pem, see ServerCertificate) stays
// the startup leaf, so a read-only CA mount is unaffected and the cache dies
// with the process it was built for.
//
// This is how a leaf-per-name CA is normally run (mkcert mints per invocation,
// Caddy's internal issuer mints per name at handshake time); the only thing
// specific to Overcast is which names it is willing to mint for.

package trust

import (
	"crypto/tls"
	"crypto/x509"
	"math"
	"strings"
	"sync"
)

const (
	// maxDynamicLeaves bounds the per-SNI cache. One entry covers a whole
	// wildcard family — one host-route label, in one region, under one base
	// domain — so a real stack occupies a handful of slots. The cap is there
	// so a client dialling an unbounded set of deep names cannot grow the map
	// without limit; eviction is least-recently-used, and an evicted family
	// costs one mint to come back.
	maxDynamicLeaves = 64
	// maxNameLabels rejects absurdly deep SNI values before they reach the
	// signer. The deepest shape Overcast itself advertises is
	// {id}.{label}.{region}.{base} with a four-label base, and a dotted S3
	// bucket adds a couple more.
	maxNameLabels = 12
)

// CertSource chooses the certificate for each TLS handshake: the leaf minted
// at startup for the names that can be enumerated, or one minted on demand
// for a host-routed name under a domain Overcast advertises. See the file
// comment for why the second kind exists.
//
// Safe for concurrent use — GetCertificate runs on every handshake, on every
// listener sharing the source.
type CertSource struct {
	ca *CA
	// static is the startup leaf, minted (or loaded) for the enumerable SAN
	// set and also served to clients that send no SNI at all — an IP-literal
	// dial, where there is no name to mint for.
	static *tls.Certificate
	// bases are the domains, lower-cased, below which a name may be minted:
	// the wildcard DNS domains, the split-horizon hosts and OVERCAST_HOSTNAME.
	// A name outside all of them is not ours to certify.
	bases []string

	mu      sync.Mutex
	dynamic map[string]*cachedLeaf
	// uses is a monotonic counter standing in for a clock in the LRU: it only
	// has to order cache hits against each other, and an integer bump under
	// the lock is cheaper (and more deterministic under test) than a
	// time.Now per handshake.
	uses uint64
}

type cachedLeaf struct {
	cert     *tls.Certificate
	lastUsed uint64
}

// NewCertSource loads (or creates) the CA in dir, prepares the startup leaf
// covering sans — reusing the on-disk one exactly as ServerCertificate does —
// and returns a source that additionally mints leaves on demand for names
// under bases. The returned pool contains the CA, for clients that need to
// dial the resulting server.
func NewCertSource(dir string, sans, bases []string) (*CertSource, *x509.CertPool, error) {
	ca, err := LoadOrCreateCA(dir)
	if err != nil {
		return nil, nil, err
	}
	static, err := ca.cachedServerCertificate(dir, sans)
	if err != nil {
		return nil, nil, err
	}
	s := &CertSource{
		ca:      ca,
		static:  static,
		bases:   normaliseBases(bases),
		dynamic: make(map[string]*cachedLeaf),
	}
	return s, ca.pool(), nil
}

// TLSConfig returns the server configuration this source backs: the startup
// leaf for a handshake that carries no SNI, GetCertificate for every one that
// does.
//
// Both are set on purpose. crypto/tls only consults GetCertificate when the
// ClientHello names a server (or when there is no static certificate at all),
// so Certificates is what answers an IP-literal dial — https://127.0.0.1:4566
// sends no SNI, and the startup leaf carries the loopback IP SANs it needs.
func (s *CertSource) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates:   []tls.Certificate{*s.static},
		GetCertificate: s.GetCertificate,
	}
}

// GetCertificate answers one ClientHello. See the file comment for the three
// outcomes.
func (s *CertSource) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
	if name == "" || s.static.Leaf.VerifyHostname(name) == nil {
		// Asking the leaf itself is the same question the client will ask of
		// it, so the two can never disagree about what "covered" means.
		return s.static, nil
	}
	wildcard, ok := wildcardParent(name, s.bases)
	if !ok {
		return s.static, nil
	}
	return s.leafFor(wildcard)
}

// leafFor returns the cached leaf for a wildcard name, minting one when the
// cache has none or the cached one is near expiry.
//
// The mint happens under the lock. It costs about a millisecond and only the
// first handshake of a family pays it; letting concurrent handshakes race to
// mint would trade that for duplicate keys and a torn cache.
func (s *CertSource) leafFor(wildcard string) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uses++
	if entry, ok := s.dynamic[wildcard]; ok && !expiringSoon(entry.cert.Leaf) {
		entry.lastUsed = s.uses
		return entry.cert, nil
	}
	cert, err := s.ca.issueTLSCertificate([]string{wildcard})
	if err != nil {
		return nil, err
	}
	s.evictLocked()
	s.dynamic[wildcard] = &cachedLeaf{cert: cert, lastUsed: s.uses}
	return cert, nil
}

// evictLocked drops least-recently-used entries until there is room for one
// more. Called with s.mu held.
func (s *CertSource) evictLocked() {
	for len(s.dynamic) >= maxDynamicLeaves {
		oldestKey, oldest := "", uint64(math.MaxUint64)
		for name, entry := range s.dynamic {
			if entry.lastUsed < oldest {
				oldestKey, oldest = name, entry.lastUsed
			}
		}
		delete(s.dynamic, oldestKey)
	}
}

// wildcardParent returns the one-label wildcard that covers name — name with
// its leftmost label replaced by "*" — when name is a plausible DNS name
// sitting at least one label below one of bases. It reports false for
// everything else, which is what keeps the daemon from minting for names it
// does not serve.
//
// Minting the parent rather than the exact name is what bounds the cache: one
// leaf covers every API ID under "execute-api.us-east-1.localhost.overcast.sh"
// instead of one per ID. The wildcard is still single-label, so it certifies
// no more than the name that asked for it and its siblings.
func wildcardParent(name string, bases []string) (string, bool) {
	if !plausibleDNSName(name) {
		return "", false
	}
	under := false
	for _, base := range bases {
		if strings.HasSuffix(name, "."+base) {
			under = true
			break
		}
	}
	if !under {
		return "", false
	}
	_, parent, found := strings.Cut(name, ".")
	if !found || parent == "" {
		return "", false
	}
	return "*." + parent, true
}

// plausibleDNSName reports whether name is a lower-cased hostname worth
// signing: labelled, within the length limits, and made only of characters a
// hostname may carry. It exists to keep anything surprising an SNI extension
// might contain — a wildcard, a path, a NUL — out of a certificate this
// daemon signs.
func plausibleDNSName(name string) bool {
	if name == "" || len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) > maxNameLabels {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return true
}

// normaliseBases lower-cases, trims and de-duplicates the mintable base
// domains, dropping anything empty so a stray separator in
// OVERCAST_SPLIT_HORIZON_HOSTS cannot turn into a base that matches every
// name ending in a dot.
func normaliseBases(bases []string) []string {
	out := make([]string, 0, len(bases))
	seen := make(map[string]bool, len(bases))
	for _, base := range bases {
		base = strings.ToLower(strings.Trim(strings.TrimSpace(base), "."))
		if base == "" || seen[base] {
			continue
		}
		seen[base] = true
		out = append(out, base)
	}
	return out
}
