package sqs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/overcast-sh/overcast/internal/topology"
)

func TestContributeTopology_queuesDeadLetterEdgesAndStackAlias(t *testing.T) {
	// Given: a queue in a non-default region whose RedrivePolicy (as the AWS
	// CLI writes it, with a string maxReceiveCount) targets a DLQ there.
	svc := newTestSQSService(t)
	ctx := context.Background()
	put := func(region string, q Queue) {
		t.Helper()
		raw, _ := json.Marshal(q)
		if err := svc.store.Set(ctx, nsQueues, region+"/"+q.Name, string(raw)); err != nil {
			t.Fatalf("seed %s: %v", q.Name, err)
		}
	}
	put("eu-west-1", Queue{Name: "orders", ARN: "arn:aws:sqs:eu-west-1:000000000000:orders", Attributes: map[string]string{
		"RedrivePolicy": `{"deadLetterTargetArn":"arn:aws:sqs:eu-west-1:000000000000:orders-dlq","maxReceiveCount":"3"}`,
	}})
	put("eu-west-1", Queue{Name: "orders-dlq", ARN: "arn:aws:sqs:eu-west-1:000000000000:orders-dlq"})

	// When: SQS contributes to the map, alongside a stack that owns the queue.
	g, cfn := &topology.Graph{}, &topology.Graph{}
	if err := svc.ContributeTopology(ctx, g); err != nil {
		t.Fatalf("ContributeTopology: %v", err)
	}
	cfn.SetStack(topology.CFN("eu-west-1", "AWS::SQS::Queue", "arn:aws:sqs:eu-west-1:000000000000:orders"), "shop")
	resp := topology.Build("", g, cfn)

	// Then: both queues are nodes in their stored region with message counts,
	// the stack owns the queue by its physical ID, and the DLQ edge is labelled.
	if len(resp.Nodes) != 2 {
		t.Fatalf("nodes: got %+v", resp.Nodes)
	}
	orders := resp.Nodes[0]
	if orders.ID != "eu-west-1::sqs::orders" || orders.Region != "eu-west-1" {
		t.Errorf("queue node identity: got %+v", orders)
	}
	if orders.ApproximateNumberOfMessages == nil || orders.ApproximateNumberOfMessagesNotVisible == nil {
		t.Errorf("queue node should carry message counts: %+v", orders)
	}
	if orders.StackName == nil || *orders.StackName != "shop" {
		t.Errorf("stack name: got %v, want shop", orders.StackName)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("edges: got %+v", resp.Edges)
	}
	if e := resp.Edges[0]; e.ID != "dlq::eu-west-1::sqs::orders→eu-west-1::sqs::orders-dlq" || e.Label != "DLQ (max 3)" {
		t.Errorf("DLQ edge: got %+v", e)
	}
}
