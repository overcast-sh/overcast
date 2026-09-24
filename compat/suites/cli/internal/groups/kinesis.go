package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/overcast-sh/overcast-compat-cli/internal/awscli"
	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// Kinesis returns the Kinesis service group.
func Kinesis() ServiceGroup {
	g := &kinesisGroup{}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			// kinesis-records
			"kinesis-records:PutRecord":        g.PutRecord,
			"kinesis-records:PutRecords":       g.PutRecords,
			"kinesis-records:GetShardIterator": g.GetShardIterator,
			"kinesis-records:GetRecords":       g.GetRecords,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"kinesis-records": g.setupRecords,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"kinesis-records": g.teardownStream,
		},
	}
}

type kinesisGroup struct{}

func (g *kinesisGroup) streamName(t *harness.TestContext) string {
	return fmt.Sprintf("%s-kinesis", t.RunID)
}

// currentStreamName returns the stream name stored in context (set by each group's
// setup) falling back to the default streamName.
func (g *kinesisGroup) currentStreamName(t *harness.TestContext) string {
	if n := t.GetString("stream_name"); n != "" {
		return n
	}
	return g.streamName(t)
}

func (g *kinesisGroup) waitStreamActive(t *harness.TestContext) error {
	for i := 0; i < 30; i++ {
		out, err := awscli.RunOutput(t.Endpoint, t.Region,
			"kinesis", "describe-stream-summary",
			"--stream-name", g.currentStreamName(t),
		)
		if err != nil {
			return err
		}
		desc, _ := out["StreamDescriptionSummary"].(map[string]any)
		status, _ := desc["StreamStatus"].(string)
		if status == "ACTIVE" {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("kinesis: stream %s did not become ACTIVE", g.currentStreamName(t))
}

// ─── shared ───────────────────────────────────────────────────────────────────

func (g *kinesisGroup) teardownStream(_ context.Context, t *harness.TestContext) error {
	awscli.Run(t.Endpoint, t.Region, "kinesis", "delete-stream", "--stream-name", g.currentStreamName(t)) //nolint:errcheck
	return nil
}

// ─── kinesis-records ─────────────────────────────────────────────────────────

func (g *kinesisGroup) setupRecords(_ context.Context, t *harness.TestContext) error {
	t.Set("stream_name", fmt.Sprintf("%s-kinesis-r", t.RunID))
	if err := awscli.Run(t.Endpoint, t.Region,
		"kinesis", "create-stream",
		"--stream-name", g.currentStreamName(t),
		"--shard-count", "1",
	); err != nil {
		return err
	}
	return g.waitStreamActive(t)
}

func (g *kinesisGroup) PutRecord(_ context.Context, t *harness.TestContext) error {
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"kinesis", "put-record",
		"--stream-name", g.currentStreamName(t),
		"--data", encodeBase64([]byte("record-1")),
		"--partition-key", t.RunID,
	)
	if err != nil {
		return err
	}
	shardID, _ := out["ShardId"].(string)
	t.Set("shard_id", shardID)
	return nil
}

func (g *kinesisGroup) PutRecords(_ context.Context, t *harness.TestContext) error {
	records := fmt.Sprintf(
		`[{"Data":"%s","PartitionKey":"pk1"},{"Data":"%s","PartitionKey":"pk2"}]`,
		encodeBase64([]byte("r1")),
		encodeBase64([]byte("r2")),
	)
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"kinesis", "put-records",
		"--stream-name", g.currentStreamName(t),
		"--records", records,
	)
	if err != nil {
		return err
	}
	if failed, _ := out["FailedRecordCount"].(float64); failed != 0 {
		return fmt.Errorf("kinesis PutRecords: FailedRecordCount=%v, expected 0", failed)
	}
	return nil
}

func (g *kinesisGroup) GetShardIterator(_ context.Context, t *harness.TestContext) error {
	shardID := t.GetString("shard_id")
	if shardID == "" {
		shardID = "shardId-000000000000"
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"kinesis", "get-shard-iterator",
		"--stream-name", g.currentStreamName(t),
		"--shard-id", shardID,
		"--shard-iterator-type", "TRIM_HORIZON",
	)
	if err != nil {
		return err
	}
	iter, _ := out["ShardIterator"].(string)
	if iter == "" {
		return fmt.Errorf("kinesis GetShardIterator: missing ShardIterator")
	}
	t.Set("shard_iterator", iter)
	return nil
}

func (g *kinesisGroup) GetRecords(_ context.Context, t *harness.TestContext) error {
	iter := t.GetString("shard_iterator")
	if iter == "" {
		return fmt.Errorf("kinesis GetRecords: missing shard_iterator")
	}
	out, err := awscli.RunOutput(t.Endpoint, t.Region,
		"kinesis", "get-records",
		"--shard-iterator", iter,
		"--limit", "10",
	)
	if err != nil {
		return err
	}
	records, _ := out["Records"].([]any)
	if len(records) == 0 {
		return fmt.Errorf("kinesis GetRecords: no records returned")
	}
	return nil
}
