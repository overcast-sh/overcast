package config_test

import (
	"slices"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
)

// TestServiceNamespacePrefix pins the three known config-service-name →
// storage-namespace-prefix remaps, plus the identity-mapping default for
// every other service. This mapping is the fix for storage-plan item 3.14:
// state.NamespacedStore routes purely on namespace prefix (the segment
// before ":" in namespaces like "cfn:stacks"), which is shorter than the
// config service name for a few services. Any caller that keys a
// NamespacedStore route map by config service name instead of this function
// builds a per-service storage override that silently never takes effect.
func TestServiceNamespacePrefix(t *testing.T) {
	tests := []struct {
		service string
		want    string
	}{
		{"cloudformation", "cfn"},
		{"apigateway", "apigw"},
		{"eventbridge", "eb"},
		// Identity-mapped default — including services with prefixes that
		// happen to already be short. dynamodbstreams is intentionally left
		// identity-mapped and must never be remapped to "dynamodb": doing so
		// would make an OVERCAST_STATE_DYNAMODB override and an
		// OVERCAST_STATE_DYNAMODBSTREAMS override collide on the same
		// route-map key (cfg.ServiceStates is an unordered map), and it's
		// moot anyway — see TestServiceOverrideIneffective.
		{"s3", "s3"},
		{"sqs", "sqs"},
		{"dynamodb", "dynamodb"},
		{"dynamodbstreams", "dynamodbstreams"},
		{"unknown-service", "unknown-service"},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			// When: we resolve the namespace prefix for the service
			got := config.ServiceNamespacePrefix(tt.service)

			// Then: it matches the documented mapping
			if got != tt.want {
				t.Errorf("ServiceNamespacePrefix(%q) = %q, want %q", tt.service, got, tt.want)
			}
		})
	}
}

// TestServiceNamespacePrefix_allServicesAudit walks every known service name
// (via config.AllServices) and asserts that ServiceNamespacePrefix returns
// either the service name unchanged or one of the three documented short
// prefixes.
//
// This is a tripwire, not just a sanity check: if a new service is ever
// added whose real storage-namespace prefix differs from its config name (as
// happened historically for cloudformation/apigateway/eventbridge) but
// ServiceNamespacePrefix is not updated to map it, the per-service
// OVERCAST_STATE_<SERVICE> override for that service will build a store that
// is never consulted — the override silently no-ops. Any new remap MUST be
// added to config.ServiceNamespacePrefix (and to this test's expected set)
// alongside the service that needs it.
func TestServiceNamespacePrefix_allServicesAudit(t *testing.T) {
	// Given: the canonical service list
	services := config.AllServices()
	if len(services) == 0 {
		t.Fatal("expected at least one service, got none")
	}

	knownRemaps := map[string]string{
		"cloudformation": "cfn",
		"apigateway":     "apigw",
		"eventbridge":    "eb",
		"lambdaweb":      "lambda-web",
	}

	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			// When: we resolve the namespace prefix
			got := config.ServiceNamespacePrefix(svc)

			// Then: it's either the identity mapping or a documented remap
			if want, isRemapped := knownRemaps[svc]; isRemapped {
				if got != want {
					t.Errorf("ServiceNamespacePrefix(%q) = %q, want documented remap %q", svc, got, want)
				}
				return
			}
			if got != svc {
				t.Errorf("ServiceNamespacePrefix(%q) = %q, want identity mapping %q (undocumented remap — add it to ServiceNamespacePrefix's known mappings and this test)", svc, got, svc)
			}
		})
	}

	// And: every documented remap is actually present in the service list —
	// guards against the mapping silently going stale if a service were ever
	// renamed or removed.
	for svc := range knownRemaps {
		if !slices.Contains(services, svc) {
			t.Errorf("expected known remapped service %q to be in the service list", svc)
		}
	}
}

// TestServiceOverrideIneffective pins the services whose OVERCAST_STATE_<SVC>
// override is accepted by config validation but never actually takes effect,
// and asserts every other service reports ok=false with an empty reason.
func TestServiceOverrideIneffective(t *testing.T) {
	tests := []struct {
		service   string
		wantOK    bool
		wantEmpty bool // when !wantOK, reason must be empty
	}{
		// Known-inert: store-less facade or wrong-namespace/stateless stub —
		// see config.ServiceOverrideIneffective's doc comment for the
		// file-level evidence backing each of these.
		{service: "dynamodbstreams", wantOK: true},
		{service: "sts", wantOK: true},
		{service: "bedrock", wantOK: true},
		{service: "organizations", wantOK: true},
		// Effectively routable (or at least not in the known-inert list) —
		// reason must come back empty.
		{service: "s3", wantOK: false, wantEmpty: true},
		{service: "sqs", wantOK: false, wantEmpty: true},
		{service: "cloudformation", wantOK: false, wantEmpty: true},
		{service: "unknown-service", wantOK: false, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.service, func(t *testing.T) {
			// When: we check whether an override for this service is inert
			reason, ok := config.ServiceOverrideIneffective(tt.service)

			// Then: ok matches the expected classification
			if ok != tt.wantOK {
				t.Errorf("ServiceOverrideIneffective(%q) ok = %v, want %v", tt.service, ok, tt.wantOK)
			}
			if tt.wantOK && reason == "" {
				t.Errorf("ServiceOverrideIneffective(%q) returned ok=true but an empty reason", tt.service)
			}
			if tt.wantEmpty && reason != "" {
				t.Errorf("ServiceOverrideIneffective(%q) reason = %q, want empty", tt.service, reason)
			}
		})
	}
}

// colonlessNamespaceServices lists services whose persisted namespace has no
// ":" separator at all (e.g. the literal namespace is just "ssm", not
// "ssm:parameters"). These used to bypass state.NamespacedStore routing
// entirely (storeFor only matched the segment before the first ":"), making
// their OVERCAST_STATE_<SERVICE> overrides silently ineffective — that bug
// is now fixed alongside storage-plan item 3.14: storeFor
// (internal/state/namespaced.go) routes a colonless namespace by its whole
// name, and for all five of these the namespace equals the config service
// name, so their overrides route correctly.
//
// The list is kept (and audited below against the live service list) as
// documentation of which services rely on the colonless whole-name routing
// rule, and so the mutual-exclusivity check keeps these five out of
// config.ServiceOverrideIneffective — they are routable, not inert.
var colonlessNamespaceServices = map[string]bool{
	"ssm":           true,
	"kms":           true,
	"stepfunctions": true,
	"appsync":       true,
	"cloudfront":    true,
}

// TestServiceStateOverride_allServicesAudit classifies every known service
// (via config.AllServices) into exactly one bucket relevant to whether its
// OVERCAST_STATE_<SERVICE> override actually works: routable via the
// prefix-before-colon rule (the implicit default), routable via the
// colonless whole-name rule (colonlessNamespaceServices — see
// state.NamespacedStore.storeFor), or known-inert
// (config.ServiceOverrideIneffective). The test fails if a service is ever
// classified as both colonless-routable and known-inert, since those are
// mutually exclusive explanations of how its override behaves.
func TestServiceStateOverride_allServicesAudit(t *testing.T) {
	// Given: the canonical service list
	services := config.AllServices()

	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			// When: we classify the service
			_, inert := config.ServiceOverrideIneffective(svc)
			colonless := colonlessNamespaceServices[svc]

			// Then: it isn't double-classified as both known-inert and
			// colonless-routable — a service's override either works (via
			// either routing rule) or is inert; it can't be both.
			if inert && colonless {
				t.Errorf("service %q is listed as both known-inert (config.ServiceOverrideIneffective) and colonless-routable (colonlessNamespaceServices) — remove it from one list", svc)
			}
		})
	}

	// And: every entry in colonlessNamespaceServices is an actual known
	// service — guards against the exemption list going stale if a service
	// were ever renamed or removed.
	for svc := range colonlessNamespaceServices {
		if !slices.Contains(services, svc) {
			t.Errorf("colonlessNamespaceServices contains %q, which is not a known service", svc)
		}
	}
}
