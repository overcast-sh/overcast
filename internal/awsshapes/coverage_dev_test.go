//go:build dev

package awsshapes

import (
	"sort"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/capabilities"
)

// TestSDKShapeTables_coverEveryImplementedService holds
// models/aws/sdk-shapes-services.txt to its stated scope: every service
// Overcast declares a working operation for is reachable from a Step
// Functions aws-sdk integration, so each needs a table for at least one of the
// modeled identities that resolve to it.
func TestSDKShapeTables_coverEveryImplementedService(t *testing.T) {
	// Given: the Overcast service keys with a supported or partial operation.
	implemented := map[string]bool{}
	for _, c := range capabilities.AllCapabilities {
		if c.Status == capabilities.StatusSupported || c.Status == capabilities.StatusPartial {
			implemented[c.Service] = true
		}
	}

	// When: the generated tables are mapped back to Overcast keys.
	covered := map[string]bool{}
	for _, modelService := range Services() {
		covered[awsapi.ServiceKey(modelService)] = true
	}

	// Then: nothing implemented is missing.
	var missing []string
	for key := range implemented {
		if !covered[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("services with capabilities but no SDK shape table: %v — add their model "+
			"service keys to models/aws/sdk-shapes-services.txt and run make generate-aws-operations", missing)
	}
}
