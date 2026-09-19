//go:build dev

package appconfig

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Applications
		capabilities.Capability{Service: "appconfig", Operation: "CreateApplication", Category: "Applications",
			Status: capabilities.StatusSupported, Notes: "Creates an application; an inline `Tags` map is applied"},
		capabilities.Capability{Service: "appconfig", Operation: "GetApplication", Category: "Applications",
			Status: capabilities.StatusSupported, Notes: "Returns application details"},
		capabilities.Capability{Service: "appconfig", Operation: "ListApplications", Category: "Applications",
			Status: capabilities.StatusSupported, Notes: "Lists all applications, paginated by `max_results`/`next_token`"},
		capabilities.Capability{Service: "appconfig", Operation: "UpdateApplication", Category: "Applications",
			Status: capabilities.StatusSupported, Notes: "Updates `Name` and `Description`; an omitted member leaves the stored value alone"},
		capabilities.Capability{Service: "appconfig", Operation: "DeleteApplication", Category: "Applications",
			Status: capabilities.StatusSupported, Notes: "Deletes an application and its tags; environments and profiles beneath it are left in place"},

		// Environments
		capabilities.Capability{Service: "appconfig", Operation: "CreateEnvironment", Category: "Environments",
			Status: capabilities.StatusSupported, Notes: "Creates an environment for an application; `State` is always `READY_FOR_DEPLOYMENT` because deployments are not emulated"},
		capabilities.Capability{Service: "appconfig", Operation: "GetEnvironment", Category: "Environments",
			Status: capabilities.StatusSupported, Notes: "Returns environment details"},
		capabilities.Capability{Service: "appconfig", Operation: "ListEnvironments", Category: "Environments",
			Status: capabilities.StatusSupported, Notes: "Lists environments for an application, paginated by `max_results`/`next_token`"},
		capabilities.Capability{Service: "appconfig", Operation: "DeleteEnvironment", Category: "Environments",
			Status: capabilities.StatusSupported, Notes: "Deletes an environment and its tags; `x-amzn-deletion-protection-check` is ignored"},

		// Configuration Profiles
		capabilities.Capability{Service: "appconfig", Operation: "CreateConfigurationProfile", Category: "Configuration Profiles",
			Status: capabilities.StatusSupported, Notes: "Creates a configuration profile; `Name` and `LocationUri` are required and `Validators` are stored but never run"},
		capabilities.Capability{Service: "appconfig", Operation: "GetConfigurationProfile", Category: "Configuration Profiles",
			Status: capabilities.StatusSupported, Notes: "Returns configuration profile details"},
		capabilities.Capability{Service: "appconfig", Operation: "ListConfigurationProfiles", Category: "Configuration Profiles",
			Status: capabilities.StatusSupported, Notes: "Lists profile summaries, filtered by the `type` query parameter and paginated by `max_results`/`next_token`"},
		capabilities.Capability{Service: "appconfig", Operation: "DeleteConfigurationProfile", Category: "Configuration Profiles",
			Status: capabilities.StatusSupported, Notes: "Deletes a configuration profile and its tags; hosted versions beneath it are left in place"},

		// Hosted Configuration Versions
		capabilities.Capability{Service: "appconfig", Operation: "CreateHostedConfigurationVersion", Category: "Hosted Configuration Versions",
			Status: capabilities.StatusSupported, Notes: "Stores the request payload against a profile; `VersionNumber` auto-increments, a stale `Latest-Version-Number` header is a `ConflictException`, and content over 1 MB is rejected with `PayloadTooLargeException`"},
		capabilities.Capability{Service: "appconfig", Operation: "GetHostedConfigurationVersion", Category: "Hosted Configuration Versions",
			Status: capabilities.StatusSupported, Notes: "Returns the raw content as the response payload with the metadata in headers"},
		capabilities.Capability{Service: "appconfig", Operation: "ListHostedConfigurationVersions", Category: "Hosted Configuration Versions",
			Status: capabilities.StatusSupported, Notes: "Returns version summaries newest first, filtered by the `version_label` query parameter (exact match, or prefix match with a trailing `*`) and paginated by `max_results`/`next_token`"},
		capabilities.Capability{Service: "appconfig", Operation: "DeleteHostedConfigurationVersion", Category: "Hosted Configuration Versions",
			Status: capabilities.StatusSupported, Notes: "Deletes a single version; other versions of the profile are untouched and version numbers are not reused"},

		// Tags
		capabilities.Capability{Service: "appconfig", Operation: "TagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Adds or overwrites tags on applications, environments, and configuration profiles"},
		capabilities.Capability{Service: "appconfig", Operation: "UntagResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Removes tags by the `tagKeys` query parameter"},
		capabilities.Capability{Service: "appconfig", Operation: "ListTagsForResource", Category: "Tags",
			Status: capabilities.StatusSupported, Notes: "Returns tags for applications, environments, and configuration profiles"},
	)
}
