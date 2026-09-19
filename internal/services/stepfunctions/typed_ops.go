package stepfunctions

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (h *Handler) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateStateMachine": op.NewTyped[createStateMachineRequest, createStateMachineResponse](
			"CreateStateMachine", h.createStateMachineTyped,
		),
		"DescribeStateMachine": op.NewTyped[describeStateMachineRequest, describeStateMachineResponse](
			"DescribeStateMachine", h.describeStateMachineTyped,
		),
		"ListStateMachines": op.NewTyped[listStateMachinesRequest, listStateMachinesResponse](
			"ListStateMachines", h.listStateMachinesTyped,
		),
		"StartExecution": op.NewTyped[startExecutionRequest, startExecutionResponse](
			"StartExecution", h.startExecutionTyped,
		),
		"StartSyncExecution": op.NewTyped[startSyncExecutionRequest, startSyncExecutionResponse](
			"StartSyncExecution", h.startSyncExecutionTyped,
		),
		"DescribeExecution": op.NewTyped[describeExecutionRequest, describeExecutionResponse](
			"DescribeExecution", h.describeExecutionTyped,
		),
		"GetExecutionHistory": op.NewTyped[getExecutionHistoryRequest, getExecutionHistoryResponse](
			"GetExecutionHistory", h.getExecutionHistoryTyped,
		),
		"ListExecutions": op.NewTyped[listExecutionsRequest, listExecutionsResponse](
			"ListExecutions", h.listExecutionsTyped,
		),
		"StopExecution": op.NewTyped[stopExecutionRequest, stopExecutionResponse](
			"StopExecution", h.stopExecutionTyped,
		),
		"DescribeStateMachineForExecution": op.NewTyped[describeStateMachineForExecutionRequest, describeStateMachineForExecutionResponse](
			"DescribeStateMachineForExecution", h.describeStateMachineForExecutionTyped,
		),
		"DeleteStateMachine": op.NewTyped[deleteStateMachineRequest, struct{}](
			"DeleteStateMachine", h.deleteStateMachineTyped,
		),
		"UpdateStateMachine": op.NewTyped[updateStateMachineRequest, updateStateMachineResponse](
			"UpdateStateMachine", h.updateStateMachineTyped,
		),
		"TagResource": op.NewTyped[tagResourceRequest, struct{}](
			"TagResource", h.tagResourceTyped,
		),
		"UntagResource": op.NewTyped[untagResourceRequest, struct{}](
			"UntagResource", h.untagResourceTyped,
		),
		"ListTagsForResource": op.NewTyped[listTagsForResourceRequest, listTagsForResourceTypedResponse](
			"ListTagsForResource", h.listTagsForResourceTyped,
		),
		"CreateActivity": op.NewTyped[createActivityRequest, createActivityResponse](
			"CreateActivity", h.createActivityTyped,
		),
		"DescribeActivity": op.NewTyped[activityArnRequest, describeActivityResponse](
			"DescribeActivity", h.describeActivityTyped,
		),
		"DeleteActivity": op.NewTyped[activityArnRequest, struct{}](
			"DeleteActivity", h.deleteActivityTyped,
		),
		"ListActivities": op.NewTyped[listActivitiesRequest, listActivitiesResponse](
			"ListActivities", h.listActivitiesTyped,
		),
		"GetActivityTask": op.NewTyped[getActivityTaskRequest, getActivityTaskResponse](
			"GetActivityTask", h.getActivityTaskTyped,
		),
		"SendTaskSuccess": op.NewTyped[sendTaskSuccessRequest, struct{}](
			"SendTaskSuccess", h.sendTaskSuccessTyped,
		),
		"SendTaskFailure": op.NewTyped[sendTaskFailureRequest, struct{}](
			"SendTaskFailure", h.sendTaskFailureTyped,
		),
		"SendTaskHeartbeat": op.NewTyped[sendTaskHeartbeatRequest, struct{}](
			"SendTaskHeartbeat", h.sendTaskHeartbeatTyped,
		),
		"ListMapRuns": op.NewTyped[listMapRunsRequest, listMapRunsResponse](
			"ListMapRuns", h.listMapRunsTyped,
		),
		"DescribeMapRun": op.NewTyped[mapRunArnRequest, describeMapRunResponse](
			"DescribeMapRun", h.describeMapRunTyped,
		),
		"UpdateMapRun": op.NewTyped[updateMapRunRequest, struct{}](
			"UpdateMapRun", h.updateMapRunTyped,
		),
		"TestState": op.NewTyped[testStateRequest, testStateResponse](
			"TestState", h.testStateTyped,
		),
		"RedriveExecution": op.NewTyped[redriveExecutionRequest, redriveExecutionResponse](
			"RedriveExecution", h.redriveExecutionTyped,
		),
		"ValidateStateMachineDefinition": op.NewTyped[validateStateMachineDefinitionRequest, validateStateMachineDefinitionResponse](
			"ValidateStateMachineDefinition", h.validateStateMachineDefinitionTyped,
		),
		// Versions (versions.go)
		"PublishStateMachineVersion": op.NewTyped[publishStateMachineVersionRequest, publishStateMachineVersionResponse](
			"PublishStateMachineVersion", h.publishStateMachineVersionTyped,
		),
		"ListStateMachineVersions": op.NewTyped[listStateMachineVersionsRequest, listStateMachineVersionsResponse](
			"ListStateMachineVersions", h.listStateMachineVersionsTyped,
		),
		"DeleteStateMachineVersion": op.NewTyped[deleteStateMachineVersionRequest, struct{}](
			"DeleteStateMachineVersion", h.deleteStateMachineVersionTyped,
		),
		// Aliases (aliases.go)
		"CreateStateMachineAlias": op.NewTyped[createStateMachineAliasRequest, createStateMachineAliasResponse](
			"CreateStateMachineAlias", h.createStateMachineAliasTyped,
		),
		"DescribeStateMachineAlias": op.NewTyped[describeStateMachineAliasRequest, describeStateMachineAliasResponse](
			"DescribeStateMachineAlias", h.describeStateMachineAliasTyped,
		),
		"UpdateStateMachineAlias": op.NewTyped[updateStateMachineAliasRequest, updateStateMachineAliasResponse](
			"UpdateStateMachineAlias", h.updateStateMachineAliasTyped,
		),
		"DeleteStateMachineAlias": op.NewTyped[deleteStateMachineAliasRequest, struct{}](
			"DeleteStateMachineAlias", h.deleteStateMachineAliasTyped,
		),
		"ListStateMachineAliases": op.NewTyped[listStateMachineAliasesRequest, listStateMachineAliasesResponse](
			"ListStateMachineAliases", h.listStateMachineAliasesTyped,
		),
	}
}

// Operations implements router.ProtocolService.
func (s *Service) Operations() []op.Operation {
	ops := s.handler.typedOp
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
