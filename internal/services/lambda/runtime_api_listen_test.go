package lambda

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
)

// runtime_api_listen_test.go covers binding the Runtime API to the set of
// addresses containers can actually reach it on, rather than to every interface.
// containerendpoint.ResolveListen decides the set; these are the mechanics of
// holding it — one port across several addresses, and one server behind them.

// closeAll releases listeners a test opened.
func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

// ipv6LoopbackOrSkip returns "::1" when this machine can bind it, and skips
// otherwise: a second loopback address is the only second address portable
// across Windows, macOS and Linux, and a container with IPv6 disabled has none.
func ipv6LoopbackOrSkip(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this machine: %v", err)
	}
	_ = ln.Close()
	return "::1"
}

func TestRuntimeAPIServer_answersOnEveryAddressItBound(t *testing.T) {
	// Given: a Runtime API server fronting both loopback addresses.
	hosts := []string{"127.0.0.1", ipv6LoopbackOrSkip(t)}
	lns, err := containerendpoint.ListenOn(hosts, 0, zap.NewNop())
	if err != nil {
		t.Fatalf("containerendpoint.ListenOn() error = %v", err)
	}

	srv, err := NewRuntimeAPIServerFromListeners(lns, "container-addr:9001", defaultLambdaInitTimeout, zap.NewNop(), clock.New())
	if err != nil {
		closeAll(lns)
		t.Fatalf("NewRuntimeAPIServerFromListeners() error = %v", err)
	}
	// Stop is not idempotent (it closes a channel), so the safety net here
	// releases the listeners rather than calling it a second time.
	defer closeAll(lns)

	// When: an unknown path is requested on each of them.
	client := &http.Client{Timeout: 5 * time.Second}
	for _, ln := range lns {
		url := fmt.Sprintf("http://%s/2018-06-01/runtime/nope", ln.Addr().String())
		resp, reqErr := client.Get(url) //nolint:noctx // short-lived probe against a local listener
		if reqErr != nil {
			t.Fatalf("GET %s: %v", url, reqErr)
		}
		// Then: the same mux answers — one server behind every address, so a
		// RIC gets an identical response whichever one it reached.
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want %d", url, resp.StatusCode, http.StatusNotFound)
		}
		_ = resp.Body.Close()
	}

	// And: shutting down closes all of them, not just the first.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stopErr := srv.Stop(ctx); stopErr != nil {
		t.Fatalf("Stop() error = %v", stopErr)
	}
	for _, ln := range lns {
		if _, dialErr := net.DialTimeout("tcp", ln.Addr().String(), time.Second); dialErr == nil {
			t.Errorf("%s still accepting connections after Stop", ln.Addr())
		}
	}
}
