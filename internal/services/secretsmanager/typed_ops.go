package secretsmanager

import (
	"context"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (h *Handler) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateSecret": op.NewTyped[createSecretRequest, createSecretResponse](
			"CreateSecret", h.createSecretTyped,
		),
		"GetSecretValue": op.NewTyped[getSecretValueRequest, secretValueResponse](
			"GetSecretValue", h.getSecretValueTyped,
		),
		"DescribeSecret": op.NewTyped[secretIDRequest, describeSecretResponse](
			"DescribeSecret", h.describeSecretTyped,
		),
		"PutSecretValue": op.NewTyped[putSecretValueRequest, putSecretValueResponse](
			"PutSecretValue", h.putSecretValueTyped,
		),
		"UpdateSecret": op.NewTyped[updateSecretRequest, updateSecretResponse](
			"UpdateSecret", h.updateSecretTyped,
		),
		"ListSecrets": op.NewTyped[listSecretsRequest, listSecretsResponse](
			"ListSecrets", h.listSecretsTyped,
		),
		"ListSecretVersionIds": op.NewTyped[secretIDRequest, listSecretVersionIdsResponse](
			"ListSecretVersionIds", h.listSecretVersionIdsTyped,
		),
		"DeleteSecret": op.NewTyped[deleteSecretRequest, deleteSecretResponse](
			"DeleteSecret", h.deleteSecretTyped,
		),
		"TagResource": op.NewTyped[tagResourceRequest, struct{}](
			"TagResource", h.tagResourceTyped,
		),
		"RotateSecret": op.NewTyped[rotateSecretRequest, rotateSecretResponse](
			"RotateSecret", h.rotateSecretTyped,
		),
		"CancelRotateSecret": op.NewTyped[secretIDRequest, cancelRotateSecretResponse](
			"CancelRotateSecret", h.cancelRotateSecretTyped,
		),
		"UntagResource": op.NewTyped[untagResourceRequest, struct{}](
			"UntagResource", h.untagResourceTyped,
		),
		"GetRandomPassword": op.NewTyped[getRandomPasswordRequest, getRandomPasswordResponse](
			"GetRandomPassword", h.getRandomPasswordTyped,
		),
		"BatchGetSecretValue": op.NewTyped[batchGetSecretValueRequest, batchGetSecretValueResponse](
			"BatchGetSecretValue", h.batchGetSecretValueTyped,
		),
		"UpdateSecretVersionStage": op.NewTyped[updateSecretVersionStageRequest, updateSecretVersionStageResponse](
			"UpdateSecretVersionStage", h.updateSecretVersionStageTyped,
		),
		"GetResourcePolicy": op.NewTyped[secretIDRequest, getResourcePolicyResponse](
			"GetResourcePolicy", h.getResourcePolicyTyped,
		),
		"PutResourcePolicy": op.NewTyped[putResourcePolicyRequest, resourcePolicyIdentityResponse](
			"PutResourcePolicy", h.putResourcePolicyTyped,
		),
		"DeleteResourcePolicy": op.NewTyped[secretIDRequest, resourcePolicyIdentityResponse](
			"DeleteResourcePolicy", h.deleteResourcePolicyTyped,
		),
		"ValidateResourcePolicy": op.NewTyped[validateResourcePolicyRequest, validateResourcePolicyResponse](
			"ValidateResourcePolicy", h.validateResourcePolicyTyped,
		),
		"RestoreSecret": op.NewTyped[secretIDRequest, restoreSecretResponse](
			"RestoreSecret", h.restoreSecretTyped,
		),
		"ReplicateSecretToRegions": op.NewTyped[struct{}, struct{}](
			"ReplicateSecretToRegions", unsupportedOperation,
		),
		"RemoveRegionsFromReplication": op.NewTyped[struct{}, struct{}](
			"RemoveRegionsFromReplication", unsupportedOperation,
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

func unsupportedOperation(_ context.Context, _ *struct{}) (*struct{}, *protocol.AWSError) {
	return nil, protocol.ErrNotImplemented
}
