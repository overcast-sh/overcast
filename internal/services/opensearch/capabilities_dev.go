//go:build dev

package opensearch

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Domains
		capabilities.Capability{Service: "opensearch", Operation: "CreateDomain", Category: "Domains",
			Status: capabilities.StatusSupported, Notes: "creates a domain, immediately active; `DomainName` and `EngineVersion` are checked against their modeled patterns; inline `TagList` applied at creation; a repeat name in the same region is rejected"},
		capabilities.Capability{Service: "opensearch", Operation: "DescribeDomain", Category: "Domains",
			Status: capabilities.StatusSupported, Notes: "returns the stored domain: every required member plus the others an inert domain can populate honestly; `Endpoint` is an AWS-shaped hostname that serves nothing"},
		capabilities.Capability{Service: "opensearch", Operation: "DescribeDomains", Category: "Domains",
			Status: capabilities.StatusSupported, Notes: "batch describe; a name that matches nothing is omitted from the list"},
		capabilities.Capability{Service: "opensearch", Operation: "ListDomainNames", Category: "Domains",
			Status: capabilities.StatusSupported, Notes: "lists the region's domains; the `engineType` filter is honoured, derived from each domain's `EngineVersion`"},
		capabilities.Capability{Service: "opensearch", Operation: "DeleteDomain", Category: "Domains",
			Status: capabilities.StatusSupported, Notes: "deletes a domain and the tags attached to it"},

		// Tags
		capabilities.Capability{Service: "opensearch", Operation: "AddTags", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "adds tags to a resource, addressed by ARN in the body"},
		capabilities.Capability{Service: "opensearch", Operation: "ListTags", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "lists tags for a resource, addressed by the `arn` query parameter"},
		capabilities.Capability{Service: "opensearch", Operation: "RemoveTags", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "removes the named tag keys from a resource"},
	)
}
