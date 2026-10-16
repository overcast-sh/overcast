package lambdaweb

import "github.com/overcast-sh/overcast/internal/protocol/op"

// typedOps is every implemented operation, keyed by its model name. The REST
// adapters in routes.go are the wire entry point; this registry names the same
// codec-agnostic functions so a test can hold the two in step.
func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		"CreateWebFunction":         op.NewTyped[createWebFunctionRequest, createWebFunctionResponse]("CreateWebFunction", s.createWebFunctionTyped),
		"GetWebFunction":            op.NewTyped[functionRequest, functionSummary]("GetWebFunction", s.getWebFunctionTyped),
		"ListWebFunctions":          op.NewTyped[listRequest, listWebFunctionsResponse]("ListWebFunctions", s.listWebFunctionsTyped),
		"DeleteWebFunction":         op.NewTypedAny[functionRequest]("DeleteWebFunction", s.deleteWebFunctionTyped),
		"CreateWebFunctionRevision": op.NewTyped[createRevisionRequest, revisionView]("CreateWebFunctionRevision", s.createWebFunctionRevisionTyped),
		"GetWebFunctionRevision":    op.NewTyped[revisionRequest, revisionView]("GetWebFunctionRevision", s.getWebFunctionRevisionTyped),
		"ListWebFunctionRevisions":  op.NewTyped[listRequest, listRevisionsResponse]("ListWebFunctionRevisions", s.listWebFunctionRevisionsTyped),
		"DeleteWebFunctionRevision": op.NewTypedAny[revisionRequest]("DeleteWebFunctionRevision", s.deleteWebFunctionRevisionTyped),
		"CreateWebFunctionEndpoint": op.NewTyped[createEndpointRequest, endpointView]("CreateWebFunctionEndpoint", s.createWebFunctionEndpointTyped),
		"GetWebFunctionEndpoint":    op.NewTyped[endpointRequest, endpointView]("GetWebFunctionEndpoint", s.getWebFunctionEndpointTyped),
		"ListWebFunctionEndpoints":  op.NewTyped[listRequest, listEndpointsResponse]("ListWebFunctionEndpoints", s.listWebFunctionEndpointsTyped),
		"UpdateWebFunctionEndpoint": op.NewTyped[updateEndpointRequest, endpointView]("UpdateWebFunctionEndpoint", s.updateWebFunctionEndpointTyped),
		"DeleteWebFunctionEndpoint": op.NewTypedAny[endpointRequest]("DeleteWebFunctionEndpoint", s.deleteWebFunctionEndpointTyped),
		"TagResource":               op.NewTypedAny[tagResourceRequest]("TagResource", s.tagResourceTyped),
		"UntagResource":             op.NewTypedAny[untagResourceRequest]("UntagResource", s.untagResourceTyped),
		"ListTags":                  op.NewTyped[listTagsRequest, listTagsResponse]("ListTags", s.listTagsTyped),
		"PutResourcePolicy":         op.NewTyped[putPolicyRequest, policyResponse]("PutResourcePolicy", s.putResourcePolicyTyped),
		"GetResourcePolicy":         op.NewTyped[policyRef, policyResponse]("GetResourcePolicy", s.getResourcePolicyTyped),
		"DeleteResourcePolicy":      op.NewTypedAny[policyRef]("DeleteResourcePolicy", s.deleteResourcePolicyTyped),
		"GetWebAccountSettings":     op.NewTyped[noInput, accountSettingsResponse]("GetWebAccountSettings", s.getWebAccountSettingsTyped),
	}
}
