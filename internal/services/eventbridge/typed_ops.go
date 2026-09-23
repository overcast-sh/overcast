package eventbridge

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateEventBus": op.NewTyped[createEventBusRequest, createEventBusResponse](
			"CreateEventBus", s.createEventBusTyped,
		),
		"DescribeEventBus": op.NewTyped[describeEventBusRequest, describeEventBusResponse](
			"DescribeEventBus", s.describeEventBusTyped,
		),
		"ListEventBuses": op.NewTyped[listEventBusesRequest, listEventBusesResponse](
			"ListEventBuses", s.listEventBusesTyped,
		),
		"TagResource": op.NewTyped[tagResourceRequest, struct{}](
			"TagResource", s.tagResourceTyped,
		),
		"ListTagsForResource": op.NewTyped[listTagsForResourceRequest, listTagsForResourceResponse](
			"ListTagsForResource", s.listTagsForResourceTyped,
		),
		"UntagResource": op.NewTyped[untagResourceRequest, struct{}](
			"UntagResource", s.untagResourceTyped,
		),
		"DeleteEventBus": op.NewTyped[deleteEventBusRequest, struct{}](
			"DeleteEventBus", s.deleteEventBusTyped,
		),
		"PutRule": op.NewTyped[putRuleRequest, putRuleResponse](
			"PutRule", s.putRuleTyped,
		),
		"DescribeRule": op.NewTyped[describeRuleRequest, ebRule](
			"DescribeRule", s.describeRuleTyped,
		),
		"ListRules": op.NewTyped[listRulesRequest, listRulesResponse](
			"ListRules", s.listRulesTyped,
		),
		"PutTargets": op.NewTyped[putTargetsRequest, targetsMutationResponse](
			"PutTargets", s.putTargetsTyped,
		),
		"ListTargetsByRule": op.NewTyped[listTargetsByRuleRequest, listTargetsByRuleResponse](
			"ListTargetsByRule", s.listTargetsByRuleTyped,
		),
		"RemoveTargets": op.NewTyped[removeTargetsRequest, targetsMutationResponse](
			"RemoveTargets", s.removeTargetsTyped,
		),
		"DisableRule": op.NewTyped[setRuleStateRequest, struct{}](
			"DisableRule", s.disableRuleTyped,
		),
		"EnableRule": op.NewTyped[setRuleStateRequest, struct{}](
			"EnableRule", s.enableRuleTyped,
		),
		"DeleteRule": op.NewTyped[deleteRuleRequest, struct{}](
			"DeleteRule", s.deleteRuleTyped,
		),
		"PutEvents": op.NewTyped[putEventsRequest, putEventsResponse](
			"PutEvents", s.putEventsTyped,
		),
		"TestEventPattern": op.NewTyped[testEventPatternRequest, testEventPatternResponse](
			"TestEventPattern", s.testEventPatternTyped,
		),
		"PutPermission": op.NewTyped[putPermissionRequest, struct{}](
			"PutPermission", s.putPermissionTyped,
		),
		"RemovePermission": op.NewTyped[removePermissionRequest, struct{}](
			"RemovePermission", s.removePermissionTyped,
		),
	}
}

// Operations implements router.ProtocolService.
func (s *Service) Operations() []op.Operation {
	ops := s.typedOp
	out := make([]op.Operation, 0, len(ops))
	for _, operation := range ops {
		out = append(out, operation)
	}
	return out
}

// SupportedProtocols implements router.ProtocolService.
func (s *Service) SupportedProtocols() []codec.Codec {
	return []codec.Codec{codec.JSON10, codec.JSON11, codec.RPCv2CBOR}
}
