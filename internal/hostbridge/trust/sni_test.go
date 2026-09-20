package trust

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// startupSANs is the enumerable set the daemon asks for at startup, trimmed to
// one base domain — the shape of config.TLSAutoSANs.
var startupSANs = []string{
	"localhost", "127.0.0.1", "::1",
	"localhost.overcast.sh", "*.localhost.overcast.sh", "*.s3.localhost.overcast.sh",
}

var startupBases = []string{"localhost.overcast.sh", "localhost"}

func newTestCertSource(t *testing.T) (*CertSource, *x509.CertPool) {
	t.Helper()
	src, pool, _ := newLoggingCertSource(t)
	return src, pool
}

// newLoggingCertSource also returns the log the source writes to, for the
// tests that assert a refusal is explained rather than silent.
func newLoggingCertSource(t *testing.T) (*CertSource, *x509.CertPool, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zap.DebugLevel)
	src, pool, err := NewCertSource(DirFor(t.TempDir()), startupSANs, startupBases, zap.New(core))
	if err != nil {
		t.Fatalf("NewCertSource: %v", err)
	}
	return src, pool, logs
}

// familyName is a host-routed name in the i-th wildcard family.
func familyName(i int) string {
	return fmt.Sprintf("id.execute-api.us-east-%d.localhost.overcast.sh", i)
}

// deepName is a name under one of the test bases carrying labels leading
// labels, which is past anything a hostname may legitimately be.
func deepName(labels int) string {
	return strings.Repeat("a.", labels) + "localhost.overcast.sh"
}

// wildcardOf is the cache key a name is stored under.
func wildcardOf(name string) string {
	_, parent, _ := strings.Cut(name, ".")
	return "*." + parent
}

// certFor runs the certificate selection one handshake would.
func certFor(t *testing.T, src *CertSource, serverName string) *tls.Certificate {
	t.Helper()
	cert, err := src.GetCertificate(&tls.ClientHelloInfo{ServerName: serverName})
	if err != nil {
		t.Fatalf("GetCertificate(%q): %v", serverName, err)
	}
	return cert
}

func TestCertSource_mintsForAHostRoutedName(t *testing.T) {
	// Given: a source over the enumerable SAN set, which stops one label
	// short of any host-routed name
	src, pool := newTestCertSource(t)
	const invoke = "myapi123.execute-api.us-east-1.localhost.overcast.sh"
	if err := src.static.Leaf.VerifyHostname(invoke); err == nil {
		t.Fatal("the startup leaf already covers the host-routed name — the test proves nothing")
	}

	// When: a client presents that name as SNI
	cert := certFor(t, src, invoke)

	// Then: it is served a leaf minted for the name's wildcard parent
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("minted certificate has no parsed Leaf")
	}
	want := "*.execute-api.us-east-1.localhost.overcast.sh"
	if !slices.Contains(leaf.DNSNames, want) {
		t.Errorf("minted leaf DNSNames %v missing %q", leaf.DNSNames, want)
	}
	// And: that leaf satisfies the check the client itself will run
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   invoke,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("minted leaf does not verify for %s: %v", invoke, err)
	}
}

func TestCertSource_servesTheStartupLeafForEnumeratedNames(t *testing.T) {
	// Given: a source whose startup leaf covers the apex, its one-label
	// wildcard and the S3 level
	src, _ := newTestCertSource(t)

	// When/Then: none of those mint anything — they are already covered, and
	// a second certificate for them would be pure waste
	for _, name := range []string{
		"",                                  // no SNI at all: an IP-literal dial
		"localhost",                         //
		"localhost.overcast.sh",             // apex
		"console.localhost.overcast.sh",     // one-label wildcard
		"mybucket.s3.localhost.overcast.sh", // the S3 level
	} {
		if got := certFor(t, src, name); got != src.static {
			t.Errorf("SNI %q was served a minted leaf, want the startup leaf", name)
		}
	}
	if len(src.dynamic) != 0 {
		t.Errorf("covered names minted %d leaves, want 0", len(src.dynamic))
	}
}

func TestCertSource_refusesNamesOutsideItsBases(t *testing.T) {
	// Given: a source that advertises localhost.overcast.sh and localhost
	src, _ := newTestCertSource(t)

	// When/Then: nothing else gets signed for. The startup leaf comes back so
	// the client fails on a name mismatch it can read, rather than on an
	// internal_error alert, and nothing is cached.
	for _, name := range []string{
		"api.execute-api.us-east-1.amazonaws.com", // real AWS
		"login.example.com",                       // somebody else entirely
		"localhost.overcast.sh.evil.test",         // our name as a prefix, not a suffix
		"notlocalhost.overcast.sh",                // a suffix that is not a label boundary
		deepName(maxNameLabels),                   // deeper than a hostname is allowed to be
		"*.evil.localhost.overcast.sh",            // a wildcard smuggled in through SNI
		"..localhost.overcast.sh",                 // an empty label
	} {
		if got := certFor(t, src, name); got != src.static {
			t.Errorf("SNI %q was minted for, want the startup leaf", name)
		}
	}
	if len(src.dynamic) != 0 {
		t.Errorf("foreign names minted %d leaves, want 0", len(src.dynamic))
	}
}

// An S3 bucket may be 63 characters with dots inside, so a virtual-hosted
// bucket can be far deeper than any invoke URL. Those are real, addressable
// names and must be minted for, not filtered out as implausible.
func TestCertSource_mintsForADeeplyDottedBucket(t *testing.T) {
	// Given: a 32-label bucket — the most a 63-character name can carry
	labels := make([]string, 32)
	for i := range labels {
		labels[i] = string(rune('a' + i%26))
	}
	bucket := strings.Join(labels, ".")
	name := bucket + ".s3.localhost.overcast.sh"

	// When: it is addressed over TLS
	src, pool := newTestCertSource(t)
	cert := certFor(t, src, name)

	// Then: the leaf served covers it
	if _, err := cert.Leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   name,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Errorf("a %d-label bucket was not minted for: %v", len(labels), err)
	}
}

// A refused name reaches the client as a bare certificate name mismatch, so
// the daemon has to say why on its own side. A name below one of our domains
// that cannot be signed is a WARN — something Overcast advertises has become
// unreachable; anything else is ordinary traffic and stays at DEBUG.
func TestCertSource_explainsARefusal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sni   string
		level zapcore.Level
	}{
		{"unsignable name below one of our domains", "*.evil.localhost.overcast.sh", zapcore.WarnLevel},
		{"a name that is not ours at all", "login.example.com", zapcore.DebugLevel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, _, logs := newLoggingCertSource(t)

			certFor(t, src, tc.sni)

			entries := logs.FilterField(zap.String("server_name", tc.sni)).All()
			if len(entries) != 1 {
				t.Fatalf("logged %d lines naming %s, want 1", len(entries), tc.sni)
			}
			if got := entries[0].Level; got != tc.level {
				t.Errorf("refusal logged at %s, want %s", got, tc.level)
			}
		})
	}
}

func TestCertSource_reusesOneLeafPerWildcardFamily(t *testing.T) {
	// Given: a source that has minted for one API in a region
	src, _ := newTestCertSource(t)
	first := certFor(t, src, "api1.execute-api.us-east-1.localhost.overcast.sh")

	// When: a sibling name in the same family arrives
	second := certFor(t, src, "api2.execute-api.us-east-1.localhost.overcast.sh")

	// Then: it is served the same leaf — the wildcard already covers it
	if first != second {
		t.Error("a sibling name minted a second leaf; the wildcard parent should have covered it")
	}
	// And: a different region is a different family, so it does mint
	other := certFor(t, src, "api1.execute-api.eu-west-1.localhost.overcast.sh")
	if other == first {
		t.Error("a different region was served the us-east-1 leaf")
	}
	if len(src.dynamic) != 2 {
		t.Errorf("cached %d families, want 2", len(src.dynamic))
	}
}

func TestCertSource_boundsTheDynamicCache(t *testing.T) {
	// Given: a client dialling far more distinct families than the cache holds
	src, _ := newTestCertSource(t)

	// When: each one is served
	for i := range maxDynamicLeaves * 2 {
		certFor(t, src, familyName(i))
	}

	// Then: the map stayed bounded — an unbounded name space cannot grow it
	if len(src.dynamic) > maxDynamicLeaves {
		t.Errorf("cache holds %d leaves, want at most %d", len(src.dynamic), maxDynamicLeaves)
	}
	// And: a family evicted along the way is served again by minting again,
	// with a leaf that really covers the name asked for
	evicted := familyName(0)
	if err := certFor(t, src, evicted).Leaf.VerifyHostname(evicted); err != nil {
		t.Errorf("an evicted family was not re-minted for %s: %v", evicted, err)
	}
}

func TestCertSource_evictsLeastRecentlyUsed(t *testing.T) {
	// Given: a full cache whose oldest entry has since been used again
	src, _ := newTestCertSource(t)
	for i := range maxDynamicLeaves {
		certFor(t, src, familyName(i))
	}
	kept := certFor(t, src, familyName(0)) // now the most recently used

	// When: a new family needs a slot
	certFor(t, src, familyName(maxDynamicLeaves))

	// Then: the entry evicted is the least recently used, not the oldest by
	// insertion — the one re-used a moment ago survives, and unchanged
	if entry, ok := src.dynamic[wildcardOf(familyName(0))]; !ok || entry.cert != kept {
		t.Error("the most recently used family was evicted")
	}
	if _, ok := src.dynamic[wildcardOf(familyName(1))]; ok {
		t.Error("the least recently used family survived eviction")
	}
}

// A leaf is replaced in place when it nears expiry. Replacing a key needs no
// room, so the eviction that makes room for a NEW key must not run: it would
// drop an unrelated, still-valid family for nothing.
func TestCertSource_remintingAFullCacheEvictsNothing(t *testing.T) {
	// Given: a full cache
	src, _ := newTestCertSource(t)
	for i := range maxDynamicLeaves {
		certFor(t, src, familyName(i))
	}
	// The family re-minted below is the most recently used, so the entry an
	// eviction would pick is a different, still-valid one.
	survivor := wildcardOf(familyName(0))

	// When: that family is re-minted because it is expiring
	restore := now
	now = func() time.Time { return restore().Add(leafValidity - leafRenewalMargin/2) }
	t.Cleanup(func() { now = restore })
	certFor(t, src, familyName(maxDynamicLeaves-1))

	// Then: the cache is still full — nothing was displaced to make room for a
	// key that was already there
	if len(src.dynamic) != maxDynamicLeaves {
		t.Errorf("cache holds %d leaves after a re-mint, want %d", len(src.dynamic), maxDynamicLeaves)
	}
	if _, ok := src.dynamic[survivor]; !ok {
		t.Errorf("re-minting one family evicted an unrelated one (%s)", survivor)
	}
}

// Minting happens outside the cache lock, so concurrent handshakes for the
// same new family can both sign. What must hold is that they converge: one
// cache entry per family, and every caller served a leaf that covers the name
// it asked for.
func TestCertSource_concurrentHandshakes(t *testing.T) {
	// Given: several names spread over a few families
	src, pool := newTestCertSource(t)
	const families, callers = 4, 8

	// When: they are all served at once
	var wg sync.WaitGroup
	errs := make(chan error, families*callers)
	for f := range families {
		for c := range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				name := fmt.Sprintf("api%d.execute-api.us-east-%d.localhost.overcast.sh", c, f)
				cert, err := src.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
				if err != nil {
					errs <- err
					return
				}
				if _, err := cert.Leaf.Verify(x509.VerifyOptions{
					Roots:     pool,
					DNSName:   name,
					KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				}); err != nil {
					errs <- fmt.Errorf("%s: %w", name, err)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Then: the cache settled on exactly one entry per family
	if len(src.dynamic) != families {
		t.Errorf("cache holds %d entries for %d families", len(src.dynamic), families)
	}
}

func TestCertSource_remintsAnExpiringLeaf(t *testing.T) {
	// Given: a family minted a leaf-lifetime ago
	src, _ := newTestCertSource(t)
	const name = "myapi.execute-api.us-east-1.localhost.overcast.sh"
	stale := certFor(t, src, name)

	// When: the clock reaches the renewal margin
	restore := now
	now = func() time.Time { return restore().Add(leafValidity - leafRenewalMargin/2) }
	t.Cleanup(func() { now = restore })

	// Then: the next handshake replaces it rather than serving an expiring one
	if fresh := certFor(t, src, name); fresh == stale {
		t.Error("an expiring cached leaf was served again")
	}
}

// TestCertSource_handshake drives a real TLS handshake for a host-routed name
// with the client verifying against the CA pool — the whole point of the
// exercise, and the only check that also proves the tls.Config wiring
// (GetCertificate vs Certificates) is right.
func TestCertSource_handshake(t *testing.T) {
	src, pool := newTestCertSource(t)

	for _, tc := range []struct {
		name       string
		serverName string
		wantSAN    string
	}{
		{"host-routed invoke URL", "myapi.execute-api.us-east-1.localhost.overcast.sh", "*.execute-api.us-east-1.localhost.overcast.sh"},
		{"lambda function URL", "abc123.lambda-url.eu-west-2.localhost.overcast.sh", "*.lambda-url.eu-west-2.localhost.overcast.sh"},
		{"dotted bucket, virtual-hosted", "my.dotted.bucket.s3.localhost.overcast.sh", "*.dotted.bucket.s3.localhost.overcast.sh"},
		{"bare hostname subdomain", "myapi.execute-api.us-east-1.localhost", "*.execute-api.us-east-1.localhost"},
		{"enumerated name", "console.localhost.overcast.sh", "*.localhost.overcast.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := handshake(t, src, &tls.Config{ServerName: tc.serverName, RootCAs: pool})
			if err != nil {
				t.Fatalf("handshake for %s: %v", tc.serverName, err)
			}
			if got := state.PeerCertificates[0].DNSNames; !slices.Contains(got, tc.wantSAN) {
				t.Errorf("served leaf DNSNames %v missing %q", got, tc.wantSAN)
			}
		})
	}
}

// TestCertSource_handshakeWithoutSNI covers the IP-literal dial
// (https://127.0.0.1:4566), which names no server: there is nothing to mint
// for, and the startup leaf's loopback IP SANs are what has to answer.
func TestCertSource_handshakeWithoutSNI(t *testing.T) {
	src, pool := newTestCertSource(t)

	state, err := handshake(t, src, &tls.Config{RootCAs: pool, InsecureSkipVerify: true}) //nolint:gosec // no SNI is the case under test; the leaf is checked below.
	if err != nil {
		t.Fatalf("handshake without SNI: %v", err)
	}
	if !state.PeerCertificates[0].Equal(src.static.Leaf) {
		t.Error("a handshake without SNI was served something other than the startup leaf")
	}
}

// handshake completes one TLS handshake over an in-memory pipe against the
// source's own server configuration and returns the client's view of it.
func handshake(t *testing.T, src *CertSource, clientCfg *tls.Config) (tls.ConnectionState, error) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() { clientConn.Close(); serverConn.Close() }) //nolint:errcheck // test cleanup.

	server := tls.Server(serverConn, src.TLSConfig())
	done := make(chan error, 1)
	go func() { done <- server.Handshake() }()

	client := tls.Client(clientConn, clientCfg)
	if err := client.Handshake(); err != nil {
		<-done
		return tls.ConnectionState{}, err
	}
	if err := <-done; err != nil {
		return tls.ConnectionState{}, fmt.Errorf("server side: %w", err)
	}
	return client.ConnectionState(), nil
}

func TestWildcardParent(t *testing.T) {
	bases := []string{"localhost.overcast.sh", "localhost"}
	for _, tc := range []struct {
		name string
		want string
		ok   bool
	}{
		{"myapi.execute-api.us-east-1.localhost.overcast.sh", "*.execute-api.us-east-1.localhost.overcast.sh", true},
		{"bucket.localhost", "*.localhost", true},
		{"a.b.localhost", "*.b.localhost", true},
		{"localhost.overcast.sh", "", false}, // the base itself has no parent below a base
		{"localhost", "", false},
		{"example.com", "", false},
		{"", "", false},
	} {
		got, ok := wildcardParent(tc.name, bases)
		if ok != tc.ok || got != tc.want {
			t.Errorf("wildcardParent(%q) = (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
