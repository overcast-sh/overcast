package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

func CloudWatchLogs(c *clients.Clients) ServiceGroup {
	g := &cwlGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"logs-events:DescribeLogStreams": g.DescribeLogStreams,
			"logs-events:PutLogEvents":       g.PutLogEvents,
			"logs-events:GetLogEvents":       g.GetLogEvents,
			"logs-events:FilterLogEvents":    g.FilterLogEvents,
			"logs-events:DeleteLogStream":    g.DeleteLogStream,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"logs-events": g.setupEvents,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"logs-events": g.teardownEvents,
		},
	}
}

type cwlGroup struct{ c *clients.Clients }

func (g *cwlGroup) client() *cloudwatchlogs.Client { return g.c.CloudWatchLogs() }

// ── logs-events ───────────────────────────────────────────────────────────────

func (g *cwlGroup) setupEvents(ctx context.Context, t *harness.TestContext) error {
	group := fmt.Sprintf("/oc/%s/events", t.RunID)
	stream := "event-stream"
	if _, err := g.client().CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(group),
	}); err != nil {
		return err
	}
	if _, err := g.client().CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
	}); err != nil {
		return err
	}
	t.Set("cwl_evt_group", group)
	t.Set("cwl_evt_stream", stream)
	return nil
}

func (g *cwlGroup) teardownEvents(ctx context.Context, t *harness.TestContext) error {
	if name := t.GetString("cwl_evt_group"); name != "" {
		g.client().DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(name)}) //nolint:errcheck
	}
	return nil
}

func (g *cwlGroup) PutLogEvents(ctx context.Context, t *harness.TestContext) error {
	group := t.GetString("cwl_evt_group")
	stream := t.GetString("cwl_evt_stream")
	now := time.Now().UnixMilli()
	resp, err := g.client().PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwltypes.InputLogEvent{
			{Message: aws.String("event-1"), Timestamp: aws.Int64(now)},
			{Message: aws.String("event-2"), Timestamp: aws.Int64(now + 1)},
		},
	})
	if err != nil {
		return err
	}
	if resp.RejectedLogEventsInfo != nil && (aws.ToInt32(resp.RejectedLogEventsInfo.TooOldLogEventEndIndex) > 0 || aws.ToInt32(resp.RejectedLogEventsInfo.TooNewLogEventStartIndex) > 0) {
		return fmt.Errorf("PutLogEvents: some events were rejected")
	}
	return nil
}

func (g *cwlGroup) GetLogEvents(ctx context.Context, t *harness.TestContext) error {
	group := t.GetString("cwl_evt_group")
	stream := t.GetString("cwl_evt_stream")
	resp, err := g.client().GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		StartFromHead: aws.Bool(true),
	})
	if err != nil {
		return err
	}
	if len(resp.Events) == 0 {
		return fmt.Errorf("GetLogEvents: expected ≥1 event")
	}
	return nil
}

func (g *cwlGroup) FilterLogEvents(ctx context.Context, t *harness.TestContext) error {
	group := t.GetString("cwl_evt_group")
	resp, err := g.client().FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:  aws.String(group),
		FilterPattern: aws.String("event"),
	})
	if err != nil {
		return err
	}
	if len(resp.Events) == 0 {
		return fmt.Errorf("FilterLogEvents: expected ≥1 matching event")
	}
	return nil
}

func (g *cwlGroup) DescribeLogStreams(ctx context.Context, t *harness.TestContext) error {
	group := t.GetString("cwl_evt_group")
	resp, err := g.client().DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(group),
	})
	if err != nil {
		return err
	}
	if len(resp.LogStreams) == 0 {
		return fmt.Errorf("DescribeLogStreams: expected ≥1 stream")
	}
	return nil
}

func (g *cwlGroup) DeleteLogStream(ctx context.Context, t *harness.TestContext) error {
	group := t.GetString("cwl_evt_group")
	stream := "delete-stream"
	g.client().CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{ //nolint:errcheck
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	_, err := g.client().DeleteLogStream(ctx, &cloudwatchlogs.DeleteLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	if err != nil {
		return err
	}
	resp, dErr := g.client().DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName:        aws.String(group),
		LogStreamNamePrefix: aws.String(stream),
	})
	if dErr != nil {
		return nil
	}
	for _, ls := range resp.LogStreams {
		if aws.ToString(ls.LogStreamName) == stream {
			return fmt.Errorf("DeleteLogStream: stream %q still present", stream)
		}
	}
	return nil
}
