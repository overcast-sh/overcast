package containerendpoint

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"go.uber.org/zap"
)

// bind.go holds a Listen's bind set: the container-facing servers — the
// Lambda Runtime API, the Athena engine's gateway — each listen on every
// address of ResolveListen's BindHosts, on one port.

// ListenOn binds hosts on port and returns the listeners in the same order.
//
// The first host is the one containers dial, so its bind is the one that has to
// succeed and the one that settles the port: a port of 0 (which the test server
// uses to avoid collisions between parallel packages) is resolved by the first
// listener and the rest join it there, rather than each taking a different
// OS-assigned port and leaving containers pointed at one of them.
//
// A later host that cannot be bound is dropped with a warning instead of
// failing the lot. Those addresses are conveniences — loopback for a developer
// or a test on this machine — and something else holding the port on one of
// them is no reason to leave the server without its container-facing listener.
func ListenOn(hosts []string, port int, logger *zap.Logger) ([]net.Listener, error) {
	if len(hosts) == 0 {
		return nil, errors.New("listen: no address to bind")
	}

	primary := net.JoinHostPort(hosts[0], strconv.Itoa(port))
	first, err := net.Listen("tcp", primary)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", primary, err)
	}
	lns := []net.Listener{first}

	bound, ok := first.Addr().(*net.TCPAddr)
	if !ok {
		_ = first.Close()
		return nil, fmt.Errorf("listen %s: not a TCP address", primary)
	}

	for _, host := range hosts[1:] {
		addr := net.JoinHostPort(host, strconv.Itoa(bound.Port))
		ln, lnErr := net.Listen("tcp", addr)
		if lnErr != nil {
			logger.Warn("secondary listen failed — address unavailable",
				zap.String("addr", addr), zap.Error(lnErr))
			continue
		}
		lns = append(lns, ln)
	}
	return lns, nil
}
