// Package debuggertest holds what a compute service's tests need to stand up
// a debugger.Manager: a port range the OS just proved free, so a manager
// under test binds without touching the real 9229-9329 range or any fixed
// port. It exists once so the Lambda and ECS suites do not each carry a copy.
package debuggertest

import (
	"net"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/debugger"
)

// FreePortRange is a small port range starting at a port the OS just handed
// out. The probe is closed before returning, so a later bind can still lose a
// race with another process; the manager scans upward from it, which is what
// keeps that from failing a test.
func FreePortRange(t testing.TB) [2]int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return [2]int{port, port + 50}
}

// NewManager builds a loopback manager over FreePortRange, closed with the
// test.
func NewManager(t testing.TB, clk clock.Clock, policy config.DebuggerTimeoutPolicy) *debugger.Manager {
	t.Helper()
	m := debugger.NewManager(clk, zap.NewNop(), "127.0.0.1", FreePortRange(t), policy)
	t.Cleanup(m.Close)
	return m
}
