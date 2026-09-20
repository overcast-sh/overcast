package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/hostbridge/trust"
)

// loopbackCert mints a leaf from a throwaway CA — the same call the serve path
// makes in auto mode.
func loopbackCert(t *testing.T) tls.Certificate {
	t.Helper()
	cert, _, err := trust.ServerCertificate(trust.DirFor(t.TempDir()), []string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatalf("mint certificate: %v", err)
	}
	return cert
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// The API and the web UI are two http.Servers over one set of TLS material.
// They must not share the *tls.Config value: http.Server.ServeTLS calls
// http2ConfigureServer, which appends "h2"/"http/1.1" to srv.TLSConfig
// .NextProtos IN PLACE, so a shared pointer is written concurrently by both
// serve goroutines — a data race on the ALPN list each listener advertises.
// Run under -race, this test fails against a shared config and passes against
// the clone cmd_serve.go now hands the UI server.
func TestCloneServerTLSConfigIsolatesTheUIServer(t *testing.T) {
	shared := &tls.Config{Certificates: []tls.Certificate{loopbackCert(t)}}

	apiLn := listenLoopback(t)
	uiLn := listenLoopback(t)

	api := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: shared}
	ui := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: cloneServerTLSConfig(shared)}

	var wg sync.WaitGroup
	wg.Add(2)
	// Launched together, as cmd_serve.go launches them.
	go func() { defer wg.Done(); _ = ui.ServeTLS(uiLn, "", "") }()
	go func() { defer wg.Done(); _ = api.ServeTLS(apiLn, "", "") }()

	// Long enough for both goroutines to get through setupHTTP2_ServeTLS,
	// which is where the write lands.
	time.Sleep(200 * time.Millisecond)
	_ = api.Close()
	_ = ui.Close()
	wg.Wait()

	if api.TLSConfig == ui.TLSConfig {
		t.Error("the API and UI servers hold the same *tls.Config — ServeTLS mutates it")
	}
}

// A host-routed invoke URL has a variable middle
// ("{id}.execute-api.{region}.<base>") that no single-label wildcard in the
// startup SAN set covers; trust.CertSource mints for it at handshake time, but
// only for names below the domains serverTLSConfig hands it. This test is the
// wiring: it asserts the auto-mode config the serve path builds actually
// serves such a name, so passing the SAN list without the base list — which
// builds and starts fine, and fails only for the addressing this exists for —
// cannot go unnoticed.
func TestServerTLSConfig_autoModeServesHostRoutedNames(t *testing.T) {
	// Given: the auto-mode TLS config the serve path builds
	cfg := &config.Config{TLSMode: config.TLSModeAuto, CADir: trust.DirFor(t.TempDir()), Hostname: "localhost"}
	tlsCfg, caPEM, err := serverTLSConfig(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("serverTLSConfig: %v", err)
	}
	if len(caPEM) == 0 {
		t.Error("auto mode returned no CA PEM for the BFF to trust")
	}

	// When: a client asks for a host-routed name under an advertised domain
	const invoke = "myapi123.execute-api.us-east-1.localhost.overcast.sh"
	cert, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: invoke})
	if err != nil {
		t.Fatalf("GetCertificate(%q): %v", invoke, err)
	}

	// Then: the certificate it is served covers that name
	if err := cert.Leaf.VerifyHostname(invoke); err != nil {
		t.Errorf("the certificate served for %s does not cover it: %v", invoke, err)
	}
	// And: a client that sends no SNI at all — an IP-literal dial — still gets
	// the startup leaf from Certificates.
	if len(tlsCfg.Certificates) != 1 {
		t.Errorf("auto mode config carries %d static certificates, want 1 for the no-SNI dial", len(tlsCfg.Certificates))
	}
}

func TestCloneServerTLSConfigPassesNilThrough(t *testing.T) {
	// The TLS-off path relies on a nil config staying nil: cmd_serve.go
	// branches on it to choose Serve over ServeTLS.
	if got := cloneServerTLSConfig(nil); got != nil {
		t.Errorf("cloneServerTLSConfig(nil) = %v, want nil", got)
	}
}
