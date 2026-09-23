package lambda

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/docker"
)

// newStalledContainerInstance returns a containerInstance whose invocation will
// never be answered — no container is polling the Runtime API — so Invoke can
// only leave through its ctx.Done() branch.
func newStalledContainerInstance(t *testing.T) *containerInstance {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv, err := NewRuntimeAPIServerFromListener(ln, addr, zap.NewNop(), clock.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000", Port: 4566}
	sink := newLogSink(logSinkConfig{
		logger:     zap.NewNop(),
		clk:        clock.New(),
		group:      "/aws/lambda/demo",
		stream:     "stream",
		region:     "us-east-1",
		runtimeAPI: srv,
	})
	t.Cleanup(sink.close)
	return &containerInstance{
		id:          "cafebabe1234deadbeef",
		containerIP: "172.19.0.3",
		functionARN: "arn:aws:lambda:us-east-1:000000000000:function:demo",
		memorySize:  2048,
		// Unroutable endpoint: currentMemoryMB is best-effort and reports 0
		// rather than reaching a real daemon.
		docker:     docker.NewClient("tcp://127.0.0.1:1", zap.NewNop()),
		runtimeAPI: srv,
		logger:     zap.NewNop(),
		clk:        clock.New(),
		exitNotify: newExitNotifier(),
		endpoint:   containerendpoint.New(cfg, "http://172.18.0.1:4566"),
		logSink:    sink,
		logCtx:     sink.ctx,
		healthy:    true,
	}
}

// reportLine returns the REPORT line from whichever request buffer holds it.
// A failure path leaves the buffer in place — the environment is unhealthy and
// about to be closed — so this is what the invocation recorded about itself.
func reportLine(t *testing.T, ci *containerInstance) string {
	t.Helper()
	ci.logSink.mu.Lock()
	defer ci.logSink.mu.Unlock()
	var seen []string
	for _, buf := range ci.logSink.buffers {
		for _, line := range strings.Split(string(buf.buf), "\n") {
			seen = append(seen, line)
			if strings.HasPrefix(line, "REPORT RequestId:") {
				return line
			}
		}
	}
	t.Fatalf("no REPORT line in any request buffer:\n%s", strings.Join(seen, "\n"))
	return ""
}

// TestContainerInstanceInvoke_callerCancellationIsNotAFunctionTimeout pins that
// a caller walking away is reported as a cancellation, not as the function
// overrunning its timeout.
//
// The web console's invoke stream used to be proxied through a client with a
// 30 s cap, so any longer invocation had its request context cancelled. The
// emulator then logged "lambda invoke timed out" and wrote Status: timeout into
// CloudWatch for a function whose configured timeout had not been reached —
// pointing straight at the handler for a fault that was in the proxy.
func TestContainerInstanceInvoke_callerCancellationIsNotAFunctionTimeout(t *testing.T) {
	// Given: an invocation bounded by a generous function timeout.
	ci := newStalledContainerInstance(t)
	caller, cancelCaller := context.WithCancel(context.Background())
	invokeCtx, cancel := context.WithTimeout(caller, 30*time.Second)
	defer cancel()

	// When: the caller disconnects long before that timeout elapses.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancelCaller()
	}()
	result, err := ci.Invoke(invokeCtx, []byte(`{}`), InvokeOptions{})

	// Then: the error says the caller went away, not that the function timed out.
	if result != nil {
		t.Fatalf("expected no result, got %+v", result)
	}
	var timeout *invokeTimeoutError
	if errors.As(err, &timeout) {
		t.Fatalf("caller cancellation reported as a function timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error should name the cancellation, got %q", err)
	}

	// And: the REPORT line records an error, not a timeout.
	report := reportLine(t, ci)
	if !strings.HasSuffix(report, "Status: error") {
		t.Errorf("REPORT should end in Status: error, got %q", report)
	}
}

// TestContainerInstanceInvoke_timeoutReportsTheTimeoutAsItsDuration pins that
// a timed-out invocation's REPORT measures the invocation, not the wait for
// its container's output afterwards. The wait used to be counted: a function
// with a 3 s timeout reported "Duration: 5000 ms  Billed Duration: 5001 ms",
// the timeout plus the whole containerOutputEndMax bound, since the container
// is still running (and so its log stream still open) when the deadline fires.
func TestContainerInstanceInvoke_timeoutReportsTheTimeoutAsItsDuration(t *testing.T) {
	// Given: an invocation bounded by a 1 s function timeout, in a container
	// whose init is still connected — its log stream open, as it is when the
	// handler is merely slow rather than dead.
	ci := newStalledContainerInstance(t)
	ci.logSink.mu.Lock()
	ci.logSink.openStreams = 1
	ci.logSink.mu.Unlock()
	invokeCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// When: the function never answers.
	_, _ = ci.Invoke(invokeCtx, []byte(`{}`), InvokeOptions{})

	// Then: the REPORT's duration is the timeout, not the timeout plus the
	// output wait that followed it.
	report := reportLine(t, ci)
	var duration float64
	for _, field := range strings.Split(report, "\t") {
		if strings.HasPrefix(field, "Duration: ") {
			if _, err := fmt.Sscanf(field, "Duration: %f ms", &duration); err != nil {
				t.Fatalf("unreadable Duration field %q: %v", field, err)
			}
		}
	}
	if duration < 1000 || duration >= 1000+float64(containerOutputEndMax.Milliseconds())/2 {
		t.Errorf("REPORT Duration should be about the 1000 ms timeout, got %.2f ms in %q", duration, report)
	}
}

// TestContainerInstanceInvoke_timeoutReportsAWSShapedError pins that a real
// overrun still reports Status: timeout, and that the payload the caller sees
// is the one AWS returns rather than a Go error string.
func TestContainerInstanceInvoke_timeoutReportsAWSShapedError(t *testing.T) {
	// Given: an invocation bounded by a 1 s function timeout.
	ci := newStalledContainerInstance(t)
	invokeCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// When: the function never answers.
	result, err := ci.Invoke(invokeCtx, []byte(`{}`), InvokeOptions{})

	// Then: the error carries the request ID and the configured timeout.
	if result != nil {
		t.Fatalf("expected no result, got %+v", result)
	}
	var timeout *invokeTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("expected an invokeTimeoutError, got %T: %v", err, err)
	}
	if timeout.RequestID == "" {
		t.Error("timeout error should carry the request ID")
	}

	// And: the REPORT line records a timeout.
	report := reportLine(t, ci)
	if !strings.HasSuffix(report, "Status: timeout") {
		t.Errorf("REPORT should end in Status: timeout, got %q", report)
	}

	// And: the response payload matches AWS's timeout shape — a lone
	// errorMessage naming the request ID and the configured timeout.
	payload := string(invokeFailurePayload(err))
	if !strings.Contains(payload, "Task timed out after 1.00 seconds") {
		t.Errorf("payload should carry AWS's timeout message, got %s", payload)
	}
	if !strings.Contains(payload, timeout.RequestID) {
		t.Errorf("payload should name the request ID, got %s", payload)
	}
	if strings.Contains(payload, "errorType") {
		t.Errorf("AWS's timeout payload has no errorType, got %s", payload)
	}
}
