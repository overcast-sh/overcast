//go:build dev

package shield

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Subscription
		capabilities.Capability{Service: "shield", Operation: "DescribeSubscription", Category: "Subscription", Status: capabilities.StatusSupported, Notes: "Returns a minimal active subscription object"},
		// Protections
		capabilities.Capability{Service: "shield", Operation: "CreateProtection", Category: "Protections", Status: capabilities.StatusSupported, Notes: "Creates a protection; refuses a ResourceArn that already has one"},
		capabilities.Capability{Service: "shield", Operation: "DescribeProtection", Category: "Protections", Status: capabilities.StatusSupported, Notes: "Lookup by ProtectionId or ResourceArn, but not both"},
		capabilities.Capability{Service: "shield", Operation: "ListProtections", Category: "Protections", Status: capabilities.StatusSupported, Notes: "Filters by InclusionFilters; paginates with MaxResults/NextToken"},
		capabilities.Capability{Service: "shield", Operation: "DeleteProtection", Category: "Protections", Status: capabilities.StatusSupported, Notes: "Deletes a protection by ID"},
		// Tags
		capabilities.Capability{Service: "shield", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Adds/updates tags on a protection"},
		capabilities.Capability{Service: "shield", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Removes tag keys from a protection"},
		capabilities.Capability{Service: "shield", Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Lists tags on a protection"},
	)
}
