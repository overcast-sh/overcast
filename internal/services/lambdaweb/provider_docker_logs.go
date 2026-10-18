package lambdaweb

// provider_docker_logs.go — the application log, container to CloudWatch.
//
// What the bootstrap prints (one JSON line per console call) is read back from
// the daemon by internal/containerlogs, as ECS does for awslogs, and written to
// the log group and stream the environment was given. The `.local` companion
// and the system logs are not written here.

import (
	"context"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/containerlogs"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// logPump is one environment's follower.
type logPump struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// startLogs follows the container's output into CloudWatch Logs in the
// background. It returns nil when there is nowhere to write.
func (p *DockerProvider) startLogs(dc *docker.Client, id string, spec envSpec) *logPump {
	w := p.writer()
	if w == nil || spec.LogGroup == "" || spec.LogStream == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(middleware.ContextWithRegion(context.Background(), spec.Region))
	pump := &logPump{cancel: cancel, done: make(chan struct{})}

	follower := containerlogs.New(containerlogs.Config{
		Client:      dc,
		ContainerID: id,
		Clock:       p.clk,
		Logger:      p.log,
		Sink: &appLogSink{inner: containerlogs.NewCloudWatchBatcher(containerlogs.BatcherConfig{
			Writer:  w,
			Group:   spec.LogGroup,
			Stream:  spec.LogStream,
			Context: ctx,
			Region:  spec.Region,
			Clock:   p.clk,
			Logger:  p.log,
		})},
	})
	go func() {
		defer close(pump.done)
		if err := w.EnsureLogGroup(ctx, spec.LogGroup); err != nil {
			p.log.Warn("lambda-web: could not create log group",
				zap.String("group", spec.LogGroup), zap.Error(err))
		} else if err := w.EnsureLogStream(ctx, spec.LogGroup, spec.LogStream); err != nil {
			p.log.Warn("lambda-web: could not create log stream",
				zap.String("group", spec.LogGroup), zap.String("stream", spec.LogStream), zap.Error(err))
		}
		follower.Follow(ctx)
		// Follow returns when the pump is cancelled, which closes the daemon
		// connection under a read that may have been mid-line: one pass over
		// the daemon's persisted copy picks up what that cost. The follower's
		// cursor means only what is missing is written.
		rctx, rcancel := context.WithTimeout(middleware.ContextWithRegion(context.Background(), spec.Region), 2*time.Second)
		defer rcancel()
		follower.Reconcile(rctx)
	}()
	return pump
}

// stop ends the follower and waits, up to limit, for it to deliver what it
// holds. Safe on a nil pump.
func (lp *logPump) stop(limit time.Duration) {
	if lp == nil {
		return
	}
	lp.cancel()
	select {
	case <-lp.done:
	case <-time.After(limit):
	}
}

// appLogSink gives each event the time the application stamped it with. The
// bootstrap's lines start `{"timestamp":"<RFC 3339>",`; the daemon's own stamp
// on the line is when the line was read, not when it was logged.
type appLogSink struct {
	inner containerlogs.LineSink
}

const timestampPrefix = `{"timestamp":"`

func (s *appLogSink) Line(line containerlogs.Line) bool {
	if rest, ok := strings.CutPrefix(line.Message, timestampPrefix); ok {
		if end := strings.IndexByte(rest, '"'); end > 0 {
			if t, err := time.Parse(time.RFC3339Nano, rest[:end]); err == nil {
				line.Time = t
			}
		}
	}
	return s.inner.Line(line)
}

func (s *appLogSink) Flush() int { return s.inner.Flush() }
