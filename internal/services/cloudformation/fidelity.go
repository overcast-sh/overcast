package cloudformation

// fidelity.go — telling a user which resources in their stack are stubs or
// backed by an inert/stub-tier service, from the deploy path they already
// watch. See https://github.com/overcast-sh/overcast/issues/760.
//
// A stack can reach CREATE_COMPLETE while parts of it do nothing. Three
// categories look identical to the user (a green resource) but arise for
// different reasons:
//
//  1. No handler is registered for the type at all — resolveHandler falls
//     through and a synthetic physical ID is invented.
//  2. A handler is registered, but it is the deliberate stubResourceHandler —
//     AWS::CDK::Metadata and the like, which creates nothing.
//  3. A real handler creates a real, describable resource, but the service it
//     belongs to is registered at router.TierInert or router.TierStub — see
//     internal/router/tiers.go — so nothing the resource does has any effect.
//
// classifyResourceFidelity is the single point that turns a resource type and
// the handler resolveHandler found for it into one of those three, or into ""
// for a resource Overcast actually backs. It is consulted once, where
// provisionResource and updateResource record what happened, rather than
// requiring every resource handler to know this exists — the same shape
// resolveHandler itself already has, and the reason a new handler on an inert
// service is classified correctly the day it lands with no table to update.
//
// # Reusing the existing channel, not inventing one
//
// The message this produces becomes a resource's ResourceStatusReason through
// exactly the same path internal/services/cloudformation/limitation.go already
// built for a narrower case (a CloudWatch alarm that cannot be evaluated):
// collected via limitationCollector, folded into resolveContext.EmulationLimitation,
// and copied onto the resource's StatusReason and its CREATE_COMPLETE /
// UPDATE_COMPLETE event. protocol/limitation.go documents why ResourceStatusReason
// carrying an Overcast-authored sentence is a considered exception rather than
// the AWS-shaped-field violation docs/plans/cfn-deploy-diagnostics.md's hard
// rules forbid elsewhere: it makes a narrow claim about what Overcast will not
// do, the same kind of statement x-overcast-emulation-limitation already makes.
//
// A handler that already said something more specific — the CloudWatch alarm
// case — is left alone: classifyResourceFidelity only fills the gap when
// nothing more specific was already collected, so the generic sentence never
// drowns out a better one.

import (
	"fmt"
	"strings"
)

// cfnTypeServiceAliases maps a CloudFormation type's second `::`-delimited
// segment to the router/capabilities service name, for the handful of
// resources where lower-casing the segment does not already produce it (see
// internal/router/tiers.go for the canonical service names).
var cfnTypeServiceAliases = map[string]string{
	"Events":                    "eventbridge",
	"WAFv2":                     "waf",
	"CertificateManager":        "acm",
	"KinesisFirehose":           "firehose",
	"ServiceCatalogAppRegistry": "appregistry",
	"OpenSearchService":         "opensearch",
	"ElasticLoadBalancingV2":    "elbv2",
}

// resourceServiceTiers mirrors internal/router.ServiceTiers for exactly the
// services that back at least one AWS::*/Custom::* resource type in
// resourceHandlers with a real (non-stub) handler at inert or stub tier.
//
// cloudformation cannot import internal/router: router.go registers
// cloudformation as a service, so the import would cycle. This is a narrow,
// deliberately small copy rather than a shared reference —
// fidelity_tier_sync_test.go (an external cloudformation_test package, which
// can import both without a cycle) fails if it ever drifts from
// internal/router.ServiceTiers for a service this package actually resolves a
// real handler onto.
var resourceServiceTiers = map[string]string{
	"iam":         "inert",
	"appsync":     "inert",
	"cloudfront":  "inert",
	"waf":         "stub",
	"shield":      "stub",
	"cloudwatch":  "inert",
	"acm":         "inert",
	"opensearch":  "inert",
	"appconfig":   "inert",
	"glue":        "inert",
	"firehose":    "inert",
	"athena":      "inert",
	"appregistry": "inert",
	"route53":     "inert",
	"elbv2":       "inert",
	"cloudtrail":  "inert",
	"backup":      "inert",
	"transfer":    "inert",
}

// cfnResourceService returns the router/capabilities service name a
// CloudFormation resource type belongs to, e.g. "AWS::WAFv2::WebACL" ->
// "waf". Returns false for a type that is not shaped like "AWS::X::Y..."
// (Custom::* resources, chiefly), which classifyResourceFidelity treats as
// fully backed — a custom resource's fidelity depends on the user's own
// Lambda, not on anything Overcast's tier registry can say.
func cfnResourceService(resType string) (string, bool) {
	parts := strings.SplitN(resType, "::", 3)
	if len(parts) < 2 || parts[0] != "AWS" {
		return "", false
	}
	segment := parts[1]
	if svc, ok := cfnTypeServiceAliases[segment]; ok {
		return svc, true
	}
	return strings.ToLower(segment), true
}

// unrecognizedResourceMessage is category 1: resolveHandler found nothing.
func unrecognizedResourceMessage(resType string) string {
	return fmt.Sprintf(
		"Overcast: %s is not a recognised resource type. Nothing was created — the physical ID is a placeholder.",
		resType)
}

// stubHandlerMessage is category 2: a deliberate stubResourceHandler.
func stubHandlerMessage(resType string) string {
	return fmt.Sprintf(
		"Overcast: %s is accepted as a no-op. Nothing is created or enforced.",
		resType)
}

// inertServiceMessage is category 3: a real handler whose service is
// registered at inert or stub tier. tier is router.TierInert ("inert") or
// router.TierStub ("stub"); any other value returns "" since it describes a
// resource Overcast actually backs.
func inertServiceMessage(service, tier string) string {
	switch tier {
	case "inert":
		return fmt.Sprintf(
			"Overcast: created, but %s is emulated at inert tier — the resource exists with no side effects or enforcement.",
			service)
	case "stub":
		return fmt.Sprintf(
			"Overcast: created, but %s is emulated at stub tier — only a minimal subset is implemented, with no side effects or enforcement.",
			service)
	default:
		return ""
	}
}

// classifyResourceFidelity returns the sentence explaining why resType is a
// stub or backed by an inert/stub-tier service, or "" for a resource Overcast
// actually backs with real service behaviour.
//
// found and handler are exactly what resolveHandler returned. Category 1 is
// found == false; category 2 is a *stubResourceHandler; category 3 is
// anything else whose resolved service is at inert or stub tier.
func classifyResourceFidelity(resType string, handler resourceHandler, found bool) string {
	if !found {
		return unrecognizedResourceMessage(resType)
	}
	if _, ok := handler.(*stubResourceHandler); ok {
		return stubHandlerMessage(resType)
	}
	service, ok := cfnResourceService(resType)
	if !ok {
		return ""
	}
	tier, ok := resourceServiceTiers[service]
	if !ok {
		return ""
	}
	return inertServiceMessage(service, tier)
}

// addFidelityFallback records classifyResourceFidelity's verdict for resType
// into limitations, but only when nothing more specific has already been
// collected for this resource — a handler that reported its own reason (the
// CloudWatch alarm case) says more than the generic sentence would.
func addFidelityFallback(limitations *limitationCollector, resType string, handler resourceHandler, found bool) {
	if limitations.reason() != "" {
		return
	}
	if msg := classifyResourceFidelity(resType, handler, found); msg != "" {
		limitations.add(msg)
	}
}
