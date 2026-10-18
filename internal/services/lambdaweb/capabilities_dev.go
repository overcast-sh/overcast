//go:build dev

package lambdaweb

import "github.com/overcast-sh/overcast/internal/capabilities"

const (
	catFunctions = "Web functions"
	catRevisions = "Revisions"
	catEndpoints = "Endpoints"
	catPolicy    = "Resource policy"
	catTags      = "Tags"
	catAccount   = "Account"
)

// The control plane is served in full, including the revision and endpoint
// lifecycles; what is missing is the data plane. A revision's code is checked
// but never started, and an endpoint's domain name is allocated but not served,
// so the operations that create those two carry Partial rather than a ✅
// (docs/services/lambda-web/limitations.md).
func init() {
	capabilities.Default.RegisterForService(serviceName,
		// Web functions
		capabilities.Capability{Operation: "CreateWebFunction", Category: catFunctions,
			Status: capabilities.StatusSupported, Notes: "Optional first revision and endpoint; tags; the create response reports `Pending`, later reads `Active`"},
		capabilities.Capability{Operation: "GetWebFunction", Category: catFunctions,
			Status: capabilities.StatusSupported, Notes: "By name or ARN"},
		capabilities.Capability{Operation: "ListWebFunctions", Category: catFunctions,
			Status: capabilities.StatusSupported, Notes: "`state` filter; `maxResults` and `nextToken` pagination"},
		capabilities.Capability{Operation: "DeleteWebFunction", Category: catFunctions,
			Status: capabilities.StatusSupported, Notes: "Deletes the function's revisions, endpoints, tags and policy with it"},

		// Revisions
		capabilities.Capability{Operation: "CreateWebFunctionRevision", Category: catRevisions,
			Status: capabilities.StatusPartial, Notes: "Validated, pinned to an S3 object version and created `Pending`; a worker checks the package and the revision becomes `Active` or `Failed`. No code runs. `LatestRevision` endpoints are pointed at it once `Active`. Reserved environment variable names are refused. Past 50 revisions the oldest unrouted ones are deleted"},
		capabilities.Capability{Operation: "GetWebFunctionRevision", Category: catRevisions,
			Status: capabilities.StatusSupported, Notes: "By revision ID or ARN; `timeoutSeconds` and `maxConcurrencyPerEnvironment` defaults are returned"},
		capabilities.Capability{Operation: "ListWebFunctionRevisions", Category: catRevisions,
			Status: capabilities.StatusSupported, Notes: "`state` filter; newest first"},
		capabilities.Capability{Operation: "DeleteWebFunctionRevision", Category: catRevisions,
			Status: capabilities.StatusSupported, Notes: "`ConflictException` while an endpoint routes traffic to the revision"},

		// Endpoints
		capabilities.Capability{Operation: "CreateWebFunctionEndpoint", Category: catEndpoints,
			Status: capabilities.StatusPartial, Notes: "`HomeRegion`, `MultiRegion` and `PerRegion`; revision weights, `autoDeploymentMode`, scaling and throttle config; at most 10 per function. The domain name is allocated but nothing serves it"},
		capabilities.Capability{Operation: "GetWebFunctionEndpoint", Category: catEndpoints,
			Status: capabilities.StatusSupported, Notes: "By name or ARN; `regionalEndpoints` has an entry per Region"},
		capabilities.Capability{Operation: "ListWebFunctionEndpoints", Category: catEndpoints,
			Status: capabilities.StatusSupported, Notes: "`authType`, `autoDeploymentMode`, `endpointType`, `state` and `updateStatus` filters"},
		capabilities.Capability{Operation: "UpdateWebFunctionEndpoint", Category: catEndpoints,
			Status: capabilities.StatusPartial, Notes: "Omitted members are kept and an empty `scalingConfig` or `throttleConfig` clears; `updateStatus` goes `InProgress` then `Successful`. Revision weights must name `Active` revisions. No traffic flows to observe it"},
		capabilities.Capability{Operation: "DeleteWebFunctionEndpoint", Category: catEndpoints,
			Status: capabilities.StatusSupported, Notes: "Goes `Deleting` while the data plane drains, then is removed"},

		// Resource policy
		capabilities.Capability{Operation: "PutResourcePolicy", Category: catPolicy,
			Status: capabilities.StatusPartial, Notes: "Public policies are rejected with `ValidationException`; `RevisionId` is checked. The policy is stored, but no endpoint request is authorised against it"},
		capabilities.Capability{Operation: "GetResourcePolicy", Category: catPolicy,
			Status: capabilities.StatusSupported, Notes: "`ResourceNotFoundException` when none is attached"},
		capabilities.Capability{Operation: "DeleteResourcePolicy", Category: catPolicy,
			Status: capabilities.StatusSupported, Notes: "`RevisionId` is checked when given"},

		// Tags
		capabilities.Capability{Operation: "TagResource", Category: catTags,
			Status: capabilities.StatusSupported, Notes: "The web function's ARN only; a tag that exists is overwritten"},
		capabilities.Capability{Operation: "UntagResource", Category: catTags,
			Status: capabilities.StatusSupported},
		capabilities.Capability{Operation: "ListTags", Category: catTags,
			Status: capabilities.StatusSupported},

		// Account
		capabilities.Capability{Operation: "GetWebAccountSettings", Category: catAccount,
			Status: capabilities.StatusSupported, Notes: "AWS's documented default quotas; `functionCount` is the Region's web functions"},
	)
}
