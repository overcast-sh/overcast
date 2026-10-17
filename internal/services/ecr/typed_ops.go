package ecr

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateRepository": op.NewTyped[createRepositoryRequest, createRepositoryResponse](
			"CreateRepository", s.createRepositoryTyped,
		),
		"DescribeRepositories": op.NewTyped[describeRepositoriesRequest, describeRepositoriesResponse](
			"DescribeRepositories", s.describeRepositoriesTyped,
		),
		"DeleteRepository": op.NewTyped[deleteRepositoryRequest, deleteRepositoryResponse](
			"DeleteRepository", s.deleteRepositoryTyped,
		),
		"GetAuthorizationToken": op.NewTyped[struct{}, getAuthorizationTokenResponse](
			"GetAuthorizationToken", s.getAuthorizationTokenTyped,
		),
		"DescribeRegistry": op.NewTyped[struct{}, describeRegistryResponse](
			"DescribeRegistry", s.describeRegistryTyped,
		),
		"ListImages": op.NewTyped[listImagesRequest, listImagesResponse](
			"ListImages", s.listImagesTyped,
		),
		"DescribeImages": op.NewTyped[describeImagesRequest, describeImagesResponse](
			"DescribeImages", s.describeImagesTyped,
		),
		"PutImage": op.NewTyped[putImageRequest, putImageResponse](
			"PutImage", s.putImageTyped,
		),
		"BatchGetImage": op.NewTyped[imageIDSetRequest, batchGetImageResponse](
			"BatchGetImage", s.batchGetImageTyped,
		),
		"DescribeImageScanFindings": op.NewTyped[describeImageScanFindingsRequest, describeImageScanFindingsResponse](
			"DescribeImageScanFindings", s.describeImageScanFindingsTyped,
		),
		"BatchDeleteImage": op.NewTyped[imageIDSetRequest, batchDeleteImageResponse](
			"BatchDeleteImage", s.batchDeleteImageTyped,
		),
		"PutImageTagMutability": op.NewTyped[putImageTagMutabilityRequest, putImageTagMutabilityResponse](
			"PutImageTagMutability", s.putImageTagMutabilityTyped,
		),
		"PutImageScanningConfiguration": op.NewTyped[putImageScanningConfigurationRequest, putImageScanningConfigurationResponse](
			"PutImageScanningConfiguration", s.putImageScanningConfigurationTyped,
		),
		"SetRepositoryPolicy": op.NewTyped[setRepositoryPolicyRequest, setRepositoryPolicyResponse](
			"SetRepositoryPolicy", s.setRepositoryPolicyTyped,
		),
		"GetRepositoryPolicy": op.NewTyped[repoRefRequest, getRepositoryPolicyResponse](
			"GetRepositoryPolicy", s.getRepositoryPolicyTyped,
		),
		"DeleteRepositoryPolicy": op.NewTyped[repoRefRequest, deleteRepositoryPolicyResponse](
			"DeleteRepositoryPolicy", s.deleteRepositoryPolicyTyped,
		),
		"PutLifecyclePolicy": op.NewTyped[putLifecyclePolicyRequest, putLifecyclePolicyResponse](
			"PutLifecyclePolicy", s.putLifecyclePolicyTyped,
		),
		"GetLifecyclePolicy": op.NewTyped[repoRefRequest, getLifecyclePolicyResponse](
			"GetLifecyclePolicy", s.getLifecyclePolicyTyped,
		),
		"DeleteLifecyclePolicy": op.NewTyped[repoRefRequest, deleteLifecyclePolicyResponse](
			"DeleteLifecyclePolicy", s.deleteLifecyclePolicyTyped,
		),
		"TagResource": op.NewTyped[tagResourceRequest, tagResourceResponse](
			"TagResource", s.tagResourceTyped,
		),
		"UntagResource": op.NewTyped[untagResourceRequest, untagResourceResponse](
			"UntagResource", s.untagResourceTyped,
		),
		"ListTagsForResource": op.NewTyped[listTagsForResourceRequest, listTagsForResourceResponse](
			"ListTagsForResource", s.listTagsForResourceTyped,
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
