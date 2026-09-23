package sqs

import (
	"context"
	"fmt"

	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/topology"
)

// ContributeTopology puts every queue, in every region, on the system map
// with its message counts, and draws each redrive policy as an edge to the
// dead-letter queue. It implements topology.Contributor.
func (s *Service) ContributeTopology(ctx context.Context, g *topology.Graph) error {
	queues, err := serviceutil.ScanRegions[Queue](ctx, s.store, nsQueues, s.cfg.Region)
	if err != nil {
		return err
	}
	now := s.handler.clk.Now()
	for _, rq := range queues {
		region, q := rq.Region, rq.Value
		counts, err := s.handler.store.backend.countMessages(ctx, region, q.Name, now)
		if err != nil {
			return err
		}
		visible, inFlight := counts.Visible, counts.NotVisible()
		g.AddNode(topology.Node{
			ID:                                    topology.NodeID(region, serviceName, q.Name),
			Service:                               serviceName,
			Label:                                 q.Name,
			Region:                                region,
			ApproximateNumberOfMessages:           &visible,
			ApproximateNumberOfMessagesNotVisible: &inFlight,
		}, topology.CFN(region, "AWS::SQS::Queue", q.ARN))

		rp, err := parseRedrivePolicy(q.Attributes)
		if err != nil || rp == nil || rp.DeadLetterTargetArn == "" {
			continue
		}
		dlqRegion := serviceutil.ARNRegion(rp.DeadLetterTargetArn)
		if dlqRegion == "" {
			dlqRegion = region
		}
		label := "DLQ"
		if rp.MaxReceiveCount > 0 {
			label = fmt.Sprintf("DLQ (max %d)", rp.MaxReceiveCount)
		}
		g.AddLink(topology.Link{
			Source:   topology.ID(region, serviceName, q.Name),
			Target:   topology.ID(dlqRegion, serviceName, queueNameFromARN(rp.DeadLetterTargetArn)),
			IDPrefix: "dlq",
			Type:     "dlq",
			Label:    label,
		})
	}
	return nil
}
