package containerendpoint

import (
	"net"
	"testing"

	"go.uber.org/zap"
)

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

func TestListenOn_bindsEveryAddressOnOnePort(t *testing.T) {
	// Given: two addresses this machine holds, and a port of 0 — which is what
	// the test server uses so parallel packages do not collide.
	hosts := []string{"127.0.0.1", ipv6LoopbackOrSkip(t)}

	// When: the set is bound.
	lns, err := ListenOn(hosts, 0, zap.NewNop())
	if err != nil {
		t.Fatalf("ListenOn() error = %v", err)
	}
	defer closeAll(lns)

	// Then: every address is listening, and on the *same* port. Each taking its
	// own OS-assigned port would leave containers pointed at whichever one the
	// container address happened to be built from.
	if len(lns) != len(hosts) {
		t.Fatalf("bound %d listeners, want %d", len(lns), len(hosts))
	}
	first := lns[0].Addr().(*net.TCPAddr).Port
	if first == 0 {
		t.Fatal("port not resolved from the first listener")
	}
	for i, ln := range lns {
		addr := ln.Addr().(*net.TCPAddr)
		if addr.Port != first {
			t.Errorf("listener %d on port %d, want %d", i, addr.Port, first)
		}
	}
}

func TestListenOn_dropsASecondaryAddressItCannotBind(t *testing.T) {
	// Given: a bindable primary followed by TEST-NET-1, which is not an address
	// of this machine — standing in for loopback already being held by
	// something else.
	hosts := []string{"127.0.0.1", "192.0.2.1"}

	// When: the set is bound.
	lns, err := ListenOn(hosts, 0, zap.NewNop())
	if err != nil {
		t.Fatalf("ListenOn() error = %v, want the primary to carry the run", err)
	}
	defer closeAll(lns)

	// Then: the primary stands. The extra addresses are conveniences; the one
	// containers dial is bound first precisely so a failure on the others is
	// not a reason to leave the server without its listener.
	if len(lns) != 1 {
		t.Fatalf("bound %d listeners, want 1", len(lns))
	}
	if got := lns[0].Addr().(*net.TCPAddr).IP.String(); got != "127.0.0.1" {
		t.Errorf("bound %q, want 127.0.0.1", got)
	}
}

func TestListenOn_failsWhenTheAddressContainersDialIsTaken(t *testing.T) {
	// Given: something already holding the port on the primary address.
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = held.Close() }()
	port := held.Addr().(*net.TCPAddr).Port

	// When: the same address is asked for.
	lns, err := ListenOn([]string{"127.0.0.1"}, port, zap.NewNop())

	// Then: it is an error, not a silent partial bind. The caller disables the
	// container runtime and says why, rather than starting containers that
	// cannot report a result.
	if err == nil {
		closeAll(lns)
		t.Fatal("ListenOn() error = nil, want the primary bind to fail")
	}
	if len(lns) != 0 {
		closeAll(lns)
		t.Errorf("returned %d listeners alongside an error, want none", len(lns))
	}
}

func TestListenOn_refusesAnEmptySet(t *testing.T) {
	// Given/When: no address at all — a resolver bug rather than a network one.
	lns, err := ListenOn(nil, 0, zap.NewNop())

	// Then: it says so instead of binding something arbitrary.
	if err == nil {
		closeAll(lns)
		t.Fatal("ListenOn(nil) error = nil, want an error")
	}
}
