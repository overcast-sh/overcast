//go:build dev

package athena

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		capabilities.Capability{Service: "athena", Operation: "StartQueryExecution", Category: "Queries", Status: capabilities.StatusSupported, Notes: "Starts a query; immediately succeeds"},
		capabilities.Capability{Service: "athena", Operation: "GetQueryExecution", Category: "Queries", Status: capabilities.StatusSupported, Notes: "Returns query execution details"},
		capabilities.Capability{Service: "athena", Operation: "GetQueryResults", Category: "Queries", Status: capabilities.StatusSupported, Notes: "Returns query results (empty result set)"},
		capabilities.Capability{Service: "athena", Operation: "StopQueryExecution", Category: "Queries", Status: capabilities.StatusSupported, Notes: "Accepts a stop; queries are already SUCCEEDED, so state is unchanged"},
		capabilities.Capability{Service: "athena", Operation: "ListQueryExecutions", Category: "Queries", Status: capabilities.StatusSupported, Notes: "Lists all query execution IDs"},
		capabilities.Capability{Service: "athena", Operation: "CreateWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported, Notes: "Creates a workgroup"},
		capabilities.Capability{Service: "athena", Operation: "GetWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported, Notes: "Returns workgroup details"},
		capabilities.Capability{Service: "athena", Operation: "ListWorkGroups", Category: "WorkGroups", Status: capabilities.StatusSupported, Notes: "Lists all workgroups"},
		capabilities.Capability{Service: "athena", Operation: "DeleteWorkGroup", Category: "WorkGroups", Status: capabilities.StatusSupported, Notes: "Deletes a workgroup"},
		capabilities.Capability{Service: "athena", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Adds/updates tags on a workgroup"},
		capabilities.Capability{Service: "athena", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Removes tag keys from a workgroup"},
		capabilities.Capability{Service: "athena", Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Lists tags on a workgroup"},
	)
}
