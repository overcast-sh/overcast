package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	"github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/clients"
	"github.com/overcast-sh/overcast-compat-go-sdk/internal/harness"
)

func Kinesis(c *clients.Clients) ServiceGroup {
	g := &kinesisGroup{c: c}
	return ServiceGroup{
		Impls: map[string]harness.TestFn{
			"kinesis-records:PutRecord":        g.PutRecord,
			"kinesis-records:PutRecords":       g.PutRecords,
			"kinesis-records:GetShardIterator": g.GetShardIterator,
			"kinesis-records:GetRecords":       g.GetRecords,
		},
		Setup: map[string]func(context.Context, *harness.TestContext) error{
			"kinesis-records": g.setupRecords,
		},
		Teardown: map[string]func(context.Context, *harness.TestContext) error{
			"kinesis-records": g.teardownRecords,
		},
	}
}

type kinesisGroup struct{ c *clients.Clients }

func (g *kinesisGroup) cl() *kinesis.Client { return g.c.Kinesis() }

func (g *kinesisGroup) waitStreamActive(ctx context.Context, streamName string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := g.cl().DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{
			StreamName: aws.String(streamName),
		})
		if err != nil {
			return err
		}
		if resp.StreamDescriptionSummary.StreamStatus == types.StreamStatusActive {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("stream %q did not become ACTIVE within timeout", streamName)
}

// ── kinesis-records ───────────────────────────────────────────────────────────

func (g *kinesisGroup) setupRecords(ctx context.Context, t *harness.TestContext) error {
	name := fmt.Sprintf("oc-rec-%s", t.RunID)
	if _, err := g.cl().CreateStream(ctx, &kinesis.CreateStreamInput{
		StreamName: aws.String(name), ShardCount: aws.Int32(1),
	}); err != nil {
		return err
	}
	if err := g.waitStreamActive(ctx, name); err != nil {
		return err
	}
	t.Set("kinesis_rec_stream", name)
	return nil
}

func (g *kinesisGroup) teardownRecords(ctx context.Context, t *harness.TestContext) error {
	if name := t.GetString("kinesis_rec_stream"); name != "" {
		g.cl().DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(name)}) //nolint:errcheck
	}
	return nil
}

func (g *kinesisGroup) PutRecord(ctx context.Context, t *harness.TestContext) error {
	stream := t.GetString("kinesis_rec_stream")
	resp, err := g.cl().PutRecord(ctx, &kinesis.PutRecordInput{
		StreamName:   aws.String(stream),
		Data:         []byte("record-data"),
		PartitionKey: aws.String("pk1"),
	})
	if err != nil {
		return err
	}
	if aws.ToString(resp.ShardId) == "" {
		return fmt.Errorf("PutRecord: missing ShardId in response")
	}
	return nil
}

func (g *kinesisGroup) PutRecords(ctx context.Context, t *harness.TestContext) error {
	stream := t.GetString("kinesis_rec_stream")
	resp, err := g.cl().PutRecords(ctx, &kinesis.PutRecordsInput{
		StreamName: aws.String(stream),
		Records: []types.PutRecordsRequestEntry{
			{Data: []byte("r1"), PartitionKey: aws.String("pk1")},
			{Data: []byte("r2"), PartitionKey: aws.String("pk2")},
		},
	})
	if err != nil {
		return err
	}
	if aws.ToInt32(resp.FailedRecordCount) > 0 {
		return fmt.Errorf("PutRecords: %d records failed", aws.ToInt32(resp.FailedRecordCount))
	}
	return nil
}

func (g *kinesisGroup) GetShardIterator(ctx context.Context, t *harness.TestContext) error {
	stream := t.GetString("kinesis_rec_stream")
	// Get first shard ID
	desc, err := g.cl().DescribeStream(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(stream)})
	if err != nil {
		return err
	}
	if len(desc.StreamDescription.Shards) == 0 {
		return fmt.Errorf("GetShardIterator: no shards")
	}
	shardID := aws.ToString(desc.StreamDescription.Shards[0].ShardId)
	resp, err := g.cl().GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName:        aws.String(stream),
		ShardId:           aws.String(shardID),
		ShardIteratorType: types.ShardIteratorTypeTrimHorizon,
	})
	if err != nil {
		return err
	}
	t.Set("kinesis_shard_iter", aws.ToString(resp.ShardIterator))
	return nil
}

func (g *kinesisGroup) GetRecords(ctx context.Context, t *harness.TestContext) error {
	iter := t.GetString("kinesis_shard_iter")
	if iter == "" {
		return nil
	}
	resp, err := g.cl().GetRecords(ctx, &kinesis.GetRecordsInput{
		ShardIterator: aws.String(iter),
		Limit:         aws.Int32(10),
	})
	if err != nil {
		return err
	}
	if len(resp.Records) == 0 {
		return fmt.Errorf("GetRecords: expected ≥1 record")
	}
	return nil
}
