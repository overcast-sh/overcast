package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// CloudWatchLogs returns the CloudWatch Logs service group.
func CloudWatchLogs() ServiceGroup {
	g := &cwlGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// logs-events
			"logs-events:PutLogEvents":       g.PutLogEvents,
			"logs-events:GetLogEvents":       g.GetLogEvents,
			"logs-events:FilterLogEvents":    g.FilterLogEvents,
			"logs-events:DescribeLogStreams": g.DescribeLogStreams,
			"logs-events:DeleteLogStream":    g.DeleteLogStream,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"logs-events": g.setupEvents,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"logs-events": g.teardownEventsGroup,
		},
	}
}

type cwlGroup struct{}

// eventsGroupName returns the log group name used by the logs-events test group.
func (g *cwlGroup) eventsGroupName(t *harness.TestContext) string {
	return fmt.Sprintf("/oc/cwl-ev/%s", t.RunID)
}

func (g *cwlGroup) streamName(t *harness.TestContext) string {
	return fmt.Sprintf("stream-%s", t.RunID)
}

// ─── logs-events ─────────────────────────────────────────────────────────────

func (g *cwlGroup) teardownEventsGroup(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "logs", "delete-log-group", "--log-group-name", g.eventsGroupName(t)) //nolint:errcheck
	return nil
}

func (g *cwlGroup) setupEvents(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"logs", "create-log-group",
		"--log-group-name", g.eventsGroupName(t),
	); err != nil {
		return err
	}
	return awscli.Run(t.Endpoint, t.Region,
		"logs", "create-log-stream",
		"--log-group-name", g.eventsGroupName(t),
		"--log-stream-name", g.streamName(t),
	)
}

func (g *cwlGroup) PutLogEvents(_ context.Context, t *harness.TestContext) error {
	// The timestamp has to be current: CloudWatch Logs discards an event older
	// than 14 days or more than two hours in the future, reporting it in
	// rejectedLogEventsInfo rather than failing the call, so a hard-coded
	// timestamp silently stops storing anything as it ages (#1721).
	events := fmt.Sprintf(`[{"timestamp":%d,"message":"hello from CLI test"}]`, time.Now().UnixMilli())
	if err := awscli.Run(t.Endpoint, t.Region,
		"logs", "put-log-events",
		"--log-group-name", g.eventsGroupName(t),
		"--log-stream-name", g.streamName(t),
		"--log-events", events,
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"logs", "get-log-events",
		"--log-group-name", g.eventsGroupName(t),
		"--log-stream-name", g.streamName(t),
	)
	if err != nil {
		return fmt.Errorf("cwl PutLogEvents: get-log-events failed: %w", err)
	}
	evts, _ := out["events"].([]any)
	if len(evts) == 0 {
		return fmt.Errorf("cwl PutLogEvents: no events found after put")
	}
	return nil
}

func (g *cwlGroup) GetLogEvents(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"logs", "get-log-events",
		"--log-group-name", g.eventsGroupName(t),
		"--log-stream-name", g.streamName(t),
	)
	if err != nil {
		return err
	}
	evts, _ := out["events"].([]any)
	if len(evts) == 0 {
		return fmt.Errorf("cwl GetLogEvents: expected events, got none")
	}
	return nil
}

func (g *cwlGroup) FilterLogEvents(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"logs", "filter-log-events",
		"--log-group-name", g.eventsGroupName(t),
		"--filter-pattern", "hello",
	)
	if err != nil {
		return err
	}
	evts, _ := out["events"].([]any)
	if len(evts) == 0 {
		return fmt.Errorf("cwl FilterLogEvents: expected matching events, got none")
	}
	return nil
}

func (g *cwlGroup) DescribeLogStreams(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"logs", "describe-log-streams",
		"--log-group-name", g.eventsGroupName(t),
	)
	if err != nil {
		return err
	}
	streams, _ := out["logStreams"].([]any)
	if len(streams) == 0 {
		return fmt.Errorf("cwl DescribeLogStreams: expected at least 1 stream")
	}
	return nil
}

func (g *cwlGroup) DeleteLogStream(_ context.Context, t *harness.TestContext) error {
	if err := awscli.Run(t.Endpoint, t.Region,
		"logs", "delete-log-stream",
		"--log-group-name", g.eventsGroupName(t),
		"--log-stream-name", g.streamName(t),
	); err != nil {
		return err
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"logs", "describe-log-streams",
		"--log-group-name", g.eventsGroupName(t),
	)
	if err != nil {
		return fmt.Errorf("cwl DeleteLogStream: describe failed: %w", err)
	}
	streams, _ := out["logStreams"].([]any)
	for _, raw := range streams {
		if m, ok := raw.(map[string]any); ok && m["logStreamName"] == g.streamName(t) {
			return fmt.Errorf("cwl DeleteLogStream: stream still present")
		}
	}
	return nil
}
