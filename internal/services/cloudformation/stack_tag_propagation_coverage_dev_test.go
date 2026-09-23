//go:build dev

package cloudformation

// stack_tag_propagation_coverage_dev_test.go — #1310: a structural guard
// against the same drift #1310 itself fixed. CloudTrail::Trail,
// Transfer::Server, Transfer::User, IAM::ManagedPolicy and IAM::InstanceProfile
// all forwarded Tags to their service without ever merging the stack's own
// tags, and nothing caught it — the effective-stack-tag mechanism (#1143) and
// the resource handlers that forward Tags were two lists nobody checked
// stayed in sync.
//
// This test parses every provisioner*.go source file's resource handler
// methods, finds every one whose Create or Update forwards Tags (a literal
// props["Tags"]/oldProps["Tags"] index, or a call to one of the shared
// tag-merging helpers that read Tags from props themselves), and asserts each
// such resource type is a member of either stackTagPropagationResourceTypes
// (provisioner.go) or stackTagPropagationExclusions, so a future resource
// type that starts forwarding Tags without joining one of those two lists
// fails this test instead of silently repeating #1310.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// tagForwardingSignal matches source text indicating a handler method
// forwards the template's Tags property to the service it backs: a direct
// props["Tags"] (or oldProps/prior variant) index, or a call to one of the
// helpers that perform that read on the handler's behalf (iamTags,
// iamEffectiveTags for Create; iamTagMutations/iamPolicyTagMutations for
// Update, which merge and diff internally rather than exposing an index
// expression in the caller).
var tagForwardingSignal = regexp.MustCompile(
	`\b(?:props|oldProps|prior)\["Tags"\]` +
		`|\biamTags\(props\)` +
		`|\biamEffectiveTags\(props,` +
		`|\biamTagMutations\(` +
		`|\biamPolicyTagMutations\(`,
)

// stackTagPropagationExclusions lists every resource type whose Create or
// Update handler forwards Tags but is deliberately not a member of
// stackTagPropagationResourceTypes (provisioner.go), each with the reason
// found during #1310's audit. A type belongs here, not silently nowhere, so
// TestStackTagPropagationCoverage keeps passing without the coverage gap
// going unnoticed the way CloudTrail/Transfer/the two IAM types did before
// #1310.
var stackTagPropagationExclusions = map[string]string{
	"AWS::EC2::VPC":                               "EC2 tags via CreateTags (applyEC2Tags), a materially different, resource-ID-keyed mechanism than the Tags-forwarding create call every propagating type above uses; no stack-tag merge exists yet for any EC2 resource type. Retrofitting it is a separate, larger change spanning all nine EC2 handlers below and is out of #1310's scope",
	"AWS::EC2::Subnet":                            "see AWS::EC2::VPC",
	"AWS::EC2::SecurityGroup":                     "see AWS::EC2::VPC",
	"AWS::EC2::InternetGateway":                   "see AWS::EC2::VPC",
	"AWS::EC2::RouteTable":                        "see AWS::EC2::VPC",
	"AWS::EC2::EIP":                               "see AWS::EC2::VPC",
	"AWS::EC2::NatGateway":                        "see AWS::EC2::VPC",
	"AWS::ApiGateway::RestApi":                    "Create merges stack tags, but Update has no tag reconciliation at all — not even for a resource-level Tags change, let alone a stack-tag-only one. A pre-existing gap broader than stack-tag propagation and out of #1310's scope",
	"AWS::ApiGateway::Stage":                      "see AWS::ApiGateway::RestApi",
	"AWS::ApiGatewayV2::Api":                      "see AWS::ApiGateway::RestApi",
	"AWS::ApiGatewayV2::Stage":                    "see AWS::ApiGateway::RestApi",
	"AWS::ServiceCatalogAppRegistry::Application": "Create forwards tags with no stack-tag merge and Update has no tag reconciliation at all; same shape as the ApiGateway gap above and out of #1310's scope",
	"AWS::AutoScaling::AutoScalingGroup":          "Create forwards tags with no stack-tag merge; Update's own tag reconciliation (if any) is untouched here — out of #1310's scope",
	"AWS::CloudWatch::Alarm":                      "Update reconciles resource-level Tags via syncTags but never merges rCtx.StackTags — no stack-tag propagation exists yet for this type; out of #1310's scope",
	"AWS::Scheduler::ScheduleGroup":               "Create forwards tags with no stack-tag merge; out of #1310's scope",
	"AWS::AppSync::Api":                           "Update reconciles resource-level Tags with no stack-tag merge; out of #1310's scope",
	"AWS::AppSync::ChannelNamespace":              "Update reconciles resource-level Tags with no stack-tag merge; out of #1310's scope",
	"AWS::ElastiCache::CacheCluster":              "Create forwards tags with no stack-tag merge; out of #1310's scope",
	"AWS::ElastiCache::ServerlessCache":           "Create forwards tags with no stack-tag merge; out of #1310's scope",
	"AWS::KMS::Key":                               "Create and Update already reconcile resource-level Tags with no stack-tag merge; out of #1310's scope",
	"AWS::EKS::Cluster":                           "Create merges stack tags (#540), but neither UpdateClusterConfig nor UpdateClusterVersion carries tags and the handler dispatches no Tag/Untag call, so joining the propagation set would mark the cluster changed on a stack-tag-only edit and then apply nothing. Reconciling EKS tags on update is its own change",
	"AWS::EKS::Nodegroup":                         "see AWS::EKS::Cluster — UpdateNodegroupConfig carries no tags either",
	"AWS::MSK::Cluster":                           "Create merges stack tags (#540). Joining the propagation set would be actively destructive here: mskClusterHandler.Update returns errReplacementRequired unconditionally, so a stack-tag-only edit would replace a Docker-backed Kafka cluster. MSK's own UpdateSecurity/UpdateMonitoring are 501 stubs; tag reconciliation waits on a real update path",
	"AWS::CloudFront::Distribution":               "Create provisions with tags via CreateDistributionWithTags (#545), but cloudfrontDistributionHandler.Update returns errReplacementRequired unconditionally — the same shape as AWS::MSK::Cluster above — so joining the propagation set would replace a live, proxying distribution for a stack-tag-only edit. A real UpdateDistribution path that reconciles the whole config, tags included, is a separate, larger change out of #545's scope",
}

func TestStackTagPropagationCoverage(t *testing.T) {
	forwarding, err := createOrUpdateMethodsForwardingTags(t)
	if err != nil {
		t.Fatalf("scan resource handlers for Tags forwarding: %v", err)
	}

	resourceTypeByHandlerType := make(map[string]string, len(resourceHandlers))
	for resourceType, handler := range resourceHandlers {
		resourceTypeByHandlerType[handlerTypeName(handler)] = resourceType
	}

	var uncovered []string
	for handlerType := range forwarding {
		resourceType, registered := resourceTypeByHandlerType[handlerType]
		if !registered {
			// Not every *fooHandler-shaped type is a registered resource
			// handler (e.g. a struct reused as a helper); nothing to check
			// against the propagation set without a resource type to key on.
			continue
		}
		if resourceType == "AWS::CloudFormation::Stack" {
			continue // tracked on its own nested-tag path, not this set
		}
		if stackTagPropagationResourceTypes[resourceType] {
			continue
		}
		if _, excluded := stackTagPropagationExclusions[resourceType]; excluded {
			continue
		}
		uncovered = append(uncovered, resourceType+" ("+handlerType+")")
	}

	if len(uncovered) > 0 {
		sort.Strings(uncovered)
		t.Errorf("forward Tags but are in neither stackTagPropagationResourceTypes nor "+
			"stackTagPropagationExclusions (provisioner.go) — add each to one, with a reason "+
			"if excluded:\n  %s", strings.Join(uncovered, "\n  "))
	}
}

// TestStackTagPropagationSetsDoNotOverlap keeps the two lists' purpose
// distinct: a type either participates or is excluded with a reason, never
// both, and every exclusion carries a non-empty reason.
func TestStackTagPropagationSetsDoNotOverlap(t *testing.T) {
	for resourceType := range stackTagPropagationExclusions {
		if stackTagPropagationResourceTypes[resourceType] {
			t.Errorf("%s is in both stackTagPropagationResourceTypes and stackTagPropagationExclusions", resourceType)
		}
	}
	for resourceType, reason := range stackTagPropagationExclusions {
		if strings.TrimSpace(reason) == "" {
			t.Errorf("%s has an empty stackTagPropagationExclusions reason", resourceType)
		}
	}
}

// createOrUpdateMethodsForwardingTags parses every non-test provisioner*.go
// file in this package's directory and returns the set of handler type names
// (e.g. "cloudtrailTrailHandler") whose Create or Update method's body
// matches tagForwardingSignal.
func createOrUpdateMethodsForwardingTags(t *testing.T) (map[string]bool, error) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	paths, err := filepath.Glob(filepath.Join(dir, "provisioner*.go"))
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	forwarding := make(map[string]bool)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || (fn.Name.Name != "Create" && fn.Name.Name != "Update") {
				continue
			}
			handlerType := receiverTypeName(fn.Recv)
			if handlerType == "" {
				continue
			}
			start := fset.Position(fn.Body.Pos()).Offset
			end := fset.Position(fn.Body.End()).Offset
			if tagForwardingSignal.Match(src[start:end]) {
				forwarding[handlerType] = true
			}
		}
	}
	return forwarding, nil
}

// receiverTypeName returns a method's receiver type name (stripping the
// pointer), or "" for a function with no receiver.
func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

// handlerTypeName returns a resourceHandler value's underlying type name
// (stripping the pointer), matching what receiverTypeName reads from source.
func handlerTypeName(handler resourceHandler) string {
	rt := reflect.TypeOf(handler)
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	return rt.Name()
}
