package awsapi

import (
	"strings"
	"testing"
)

func TestRegistryClaimTarget_modeledJSONOperation(t *testing.T) {
	// Given: the immutable registry generated from the pinned model corpus.
	registry := NewRegistry()

	// When: a modeled, currently unimplemented AWS JSON target is looked up.
	claim, ok := registry.ClaimTarget("GameLift.ListBuilds")

	// Then: the registry selects the JSON 501 envelope without scanning models.
	if !ok {
		t.Fatal("ClaimTarget() did not recognize GameLift.ListBuilds")
	}
	if claim.ModelService != "gamelift" || claim.Operation != "ListBuilds" {
		t.Errorf("ClaimTarget() = %+v, want gamelift ListBuilds", claim)
	}
	if claim.ErrorProfile != ErrorProfileJSON {
		t.Errorf("ClaimTarget() error profile = %v, want JSON", claim.ErrorProfile)
	}
}

func TestRegistryClaimQuery_ec2Operation(t *testing.T) {
	// Given: the immutable registry generated from the pinned model corpus.
	registry := NewRegistry()

	// When: a fully qualified EC2 Query operation is looked up.
	claim, ok := registry.ClaimQuery("2016-11-15", "DescribeInstances")

	// Then: it chooses EC2's distinct XML error envelope.
	if !ok {
		t.Fatal("ClaimQuery() did not recognize EC2 DescribeInstances")
	}
	if claim.ErrorProfile != ErrorProfileEC2QueryXML {
		t.Errorf("ClaimQuery() error profile = %v, want EC2 Query XML", claim.ErrorProfile)
	}
}

func TestRegistryClaimQuery_unknownOperation(t *testing.T) {
	// Given: the immutable registry generated from the pinned model corpus.
	registry := NewRegistry()

	// When: an unknown future operation is looked up.
	_, ok := registry.ClaimQuery("2099-01-01", "FutureQueryOperation")

	// Then: it remains unclaimed for the generic Query fallback.
	if ok {
		t.Fatal("ClaimQuery() unexpectedly claimed an unknown operation")
	}
}

func TestRegistryClaimREST_modeledOperation(t *testing.T) {
	// Given: the immutable REST trie generated from the pinned model corpus.
	registry := NewRegistry()

	// When: a modeled Access Analyzer operation is looked up by its HTTP binding.
	claim, ok := registry.ClaimREST("GET", "/analyzer")

	// Then: it selects the REST JSON 501 envelope without S3 participation.
	if !ok {
		t.Fatal("ClaimREST() did not recognize Access Analyzer ListAnalyzers")
	}
	if claim.ModelService != "accessanalyzer" || claim.Operation != "ListAnalyzers" {
		t.Errorf("ClaimREST() = %+v, want accessanalyzer ListAnalyzers", claim)
	}
	if claim.ErrorProfile != ErrorProfileJSON {
		t.Errorf("ClaimREST() error profile = %v, want JSON", claim.ErrorProfile)
	}
}

func TestRegistryClaimREST_greedyLabelWithSuffix(t *testing.T) {
	// Given: a modeled REST URI whose greedy label is followed by a literal.
	registry := NewRegistry()

	// When: its label spans multiple path segments.
	claim, ok := registry.ClaimREST("GET", "/v20180820/mrap/instances/one/two/policy")

	// Then: matching reaches the modeled operation rather than S3.
	if !ok || claim.Operation != "GetMultiRegionAccessPointPolicy" {
		t.Errorf("ClaimREST() = %+v, %v; want GetMultiRegionAccessPointPolicy", claim, ok)
	}
}

func TestRegistryClaimREST_rootBinding(t *testing.T) {
	// Given: a modeled REST operation bound to the HTTP root.
	registry := NewRegistry()

	// When: MediaStore Data's root GET binding is classified.
	claim, ok := registry.ClaimREST("GET", "/")

	// Then: the registry retains the signing name needed to distinguish it from S3.
	if !ok || claim.Operation != "ListItems" || claim.SigningName != "mediastore" {
		t.Errorf("ClaimREST() = %+v, %v; want MediaStore Data ListItems", claim, ok)
	}
}

func TestRegistryClaimREST_rootCatchAll(t *testing.T) {
	registry := NewRegistry()
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		// MediaStore Data's "/{Path+}" matches any path at all
		{"GET", "/tables/a/b/c", true},
		{"PUT", "/tables/a/b/c", true},
		{"HEAD", "/anything", true},
		// A root binding and a specific binding are evidence
		{"GET", "/", false},
		{"GET", "/analyzer", false},
		// A greedy label followed by a literal is too
		{"GET", "/v20180820/mrap/instances/one/two/policy", false},
	} {
		// Given: a request only the named kind of binding matches
		// When: it is classified
		claim, ok := registry.ClaimREST(tc.method, tc.path)

		// Then: only the root greedy binding is marked a catch-all
		if !ok || claim.CatchAll != tc.want {
			t.Errorf("ClaimREST(%s %s) = %+v, %v; want CatchAll %v", tc.method, tc.path, claim, ok, tc.want)
		}
	}
}

func TestSigningNameErrorProfile(t *testing.T) {
	// Given: signing names of a restJson1 service, a restXml one, and no service
	// When: each is looked up
	// Then: each gets its protocol's envelope, and the non-name none
	for name, want := range map[string]ErrorProfile{"s3tables": ErrorProfileJSON, "lambda": ErrorProfileJSON, "route53": ErrorProfileXML, "Route53": ErrorProfileXML} {
		if got, ok := SigningNameErrorProfile(name); !ok || got != want {
			t.Errorf("SigningNameErrorProfile(%q) = %v, %v; want %v", name, got, ok, want)
		}
	}
	if _, ok := SigningNameErrorProfile("not-a-real-aws-service"); ok {
		t.Error("SigningNameErrorProfile answered for a name no model declares")
	}
}

// TestSigningNameErrorProfile_isUnambiguous pins what SigningNameErrorProfile
// relies on: no signing name's REST bindings span both REST protocols, so
// "the first in generated order" is never a choice. S3's family is the
// exception the router never asks about (addressesNonS3 rules it out first).
func TestSigningNameErrorProfile_isUnambiguous(t *testing.T) {
	seen := map[string]ErrorProfile{}
	for _, op := range restOperations {
		name := strings.ToLower(op.SigningName)
		if name == "" || name == "s3" {
			continue
		}
		profile := restErrorProfile(op.Protocol)
		if prior, ok := seen[name]; ok && prior != profile {
			t.Errorf("signing name %q has bindings in both REST protocols (%s %s)", name, op.ModelService, op.Operation)
		}
		seen[name] = profile
	}
}

func TestRegistryClaimREST_literalQueryBinding(t *testing.T) {
	// Given: two modeled operations share a path and method but use distinct
	// literal query bindings.
	registry := NewRegistry()

	// When: the TagResource query discriminator is present.
	claim, ok := registry.ClaimRESTQuery("POST", "/tags", "operation=tag-resource")

	// Then: the query-aware trie selects that binding instead of collapsing it
	// into another operation on POST /tags.
	if !ok || claim.Operation != "TagResource" {
		t.Errorf("ClaimRESTQuery() = %+v, %v; want TagResource", claim, ok)
	}
}

func TestRawQueryContains_valuelessLiteralWithEquals(t *testing.T) {
	// Given: a Smithy binding with a valueless literal query component.
	// When: an HTTP client serializes the component with an empty value.
	// Then: "?acl=" is equivalent to "?acl" for binding selection.
	if !rawQueryContains("acl=", "acl") {
		t.Fatal(`rawQueryContains("acl=", "acl") = false, want true`)
	}
}

func TestRegistryClaimRPC_additiveCBORProtocol(t *testing.T) {
	// Given: GameLift's canonical AWS JSON service model also advertises
	// Smithy RPC v2 CBOR as an additive protocol.
	registry := NewRegistry()

	// When: its Smithy RPC route is classified by service shape and operation.
	claim, ok := registry.ClaimRPC(ProtocolRPCV2CBOR, "GameLift", "ListBuilds")

	// Then: the generated RPC index owns the route independently of the
	// service's canonical protocol.
	if !ok || claim.ModelService != "gamelift" || claim.Operation != "ListBuilds" {
		t.Errorf("ClaimRPC() = %+v, %v; want gamelift ListBuilds", claim, ok)
	}
	if claim.ErrorProfile != ErrorProfileRPCV2CBOR {
		t.Errorf("ClaimRPC() error profile = %v, want RPC v2 CBOR", claim.ErrorProfile)
	}
}

// TestRegistryClaimTarget_collidingAliasedIdentities covers the collision that
// is not one. The models carry EventBridge twice, as "cloudwatch-events" and
// "eventbridge", so awsmodelgen blanks the service on all 51 of its targets —
// but both identities are one service, serviceAliases says so, and there is
// nothing to choose between a service and itself.
//
// Before this resolved, every EventBridge JSON call was labelled with the S3
// fallback in the request log and the debug trace, and IAM-authorised as an
// `s3:` action.
func TestRegistryClaimTarget_collidingAliasedIdentities(t *testing.T) {
	// Given: a modeled target shared by CloudWatch Events and EventBridge.
	registry := NewRegistry()

	// When: its immutable registry entry is claimed.
	claim, ok := registry.ClaimTarget("AWSEvents.CreateEventBus")

	// Then: it names the one service both identities are, and is no longer
	// ambiguous. ModelService stays empty: the generated entry retains no
	// single source identity, because the models genuinely declared two.
	if !ok {
		t.Fatal("ClaimTarget() did not recognize AWSEvents.CreateEventBus")
	}
	if claim.Ambiguous || claim.Service != "eventbridge" {
		t.Errorf("ClaimTarget() = %+v, want an unambiguous eventbridge claim", claim)
	}
}

// TestRegistryClaimTarget_collidingUnresolvedServices is the other half of the
// same rule: a collision between services that really are different, and that
// no Overcast declaration resolves, stays ambiguous. Timestream Query and
// Timestream Write share four targets and Overcast implements neither, so
// there is nothing to prefer and preferring anything would be the invention
// the generator refused to make.
func TestRegistryClaimTarget_collidingUnresolvedServices(t *testing.T) {
	// Given: a modeled target shared by two services that are not aliases.
	registry := NewRegistry()

	// When: its immutable registry entry is claimed.
	claim, ok := registry.ClaimTarget("Timestream_20181101.DescribeEndpoints")

	// Then: it is still unattributed.
	if !ok {
		t.Fatal("ClaimTarget() did not recognize Timestream_20181101.DescribeEndpoints")
	}
	if !claim.Ambiguous || claim.ModelService != "" || claim.Service != "" {
		t.Errorf("ClaimTarget() = %+v, want an unassigned ambiguous claim", claim)
	}
}

// TestRegistryClaimQuery_collidingServiceResolvedByVersionOwner covers the
// collision that is real but one-sided. DocumentDB, Neptune and RDS share the
// 2014-10-31 API version and most of its action names — all 71 collisions in
// the query index are these three — and Overcast implements only RDS, which is
// what queryVersionOwners records and what rds.OwnsVersion routes by.
func TestRegistryClaimQuery_collidingServiceResolvedByVersionOwner(t *testing.T) {
	// Given: a Query key shared by DocumentDB, Neptune, and RDS.
	registry := NewRegistry()

	// When: its immutable registry entry is claimed.
	claim, ok := registry.ClaimQuery("2014-10-31", "CreateDBCluster")

	// Then: the declared owner of the version settles it.
	if !ok {
		t.Fatal("ClaimQuery() did not recognize CreateDBCluster")
	}
	if claim.Ambiguous || claim.Service != "rds" {
		t.Errorf("ClaimQuery() = %+v, want an unambiguous rds claim", claim)
	}
}

// TestResolveCollision_honoursOnlyDeclarationsThatCollided states the guard
// that keeps queryVersionOwners a tie-break rather than an override. A declared
// owner is used only when it is one of the modeled services that actually
// collided, so an entry that goes stale — a version whose family the models
// reshuffle — cannot hand its service an action that service never declared.
func TestResolveCollision_honoursOnlyDeclarationsThatCollided(t *testing.T) {
	collisions := []operationCollision{
		{Key: "shared", Services: []string{"docdb", "neptune"}},
	}
	if got := resolveCollision(collisions, "shared", "rds"); got != "" {
		t.Errorf("resolveCollision() = %q for an owner that did not collide, want \"\"", got)
	}
	if got := resolveCollision(collisions, "shared", "neptune"); got != "neptune" {
		t.Errorf("resolveCollision() = %q, want neptune", got)
	}
	if got := resolveCollision(collisions, "absent", "rds"); got != "" {
		t.Errorf("resolveCollision() = %q for a key that never collided, want \"\"", got)
	}
}

func TestRegistryRESTOperation_resolvesSharedBindingPerService(t *testing.T) {
	// Given: REST bindings that several modeled services declare, each under
	// its own operation name, so no single retained name can serve them all.
	registry := NewRegistry()
	tests := []struct {
		name    string
		service string
		method  string
		path    string
		want    string
	}{
		{"apigateway v2 owns GET /v2/apis", "apigateway", "GET", "/v2/apis", "GetApis"},
		{"appsync names the same binding differently", "appsync", "GET", "/v2/apis", "ListApis"},
		{"a service outside the set gets nothing", "dynamodb", "GET", "/v2/apis", ""},
		{"appregistry owns GET /configuration", "appregistry", "GET", "/configuration", "GetConfiguration"},
		{"appconfigdata names it differently", "appconfigdata", "GET", "/configuration", "GetLatestConfiguration"},
		{"apigateway calls tag listing GetTags", "apigateway", "GET", "/tags/arn%3Aaws%3Aapigateway", "GetTags"},
		{"backup calls it ListTags", "backup", "GET", "/tags/arn%3Aaws%3Abackup", "ListTags"},
		{"eks uses the common name", "eks", "GET", "/tags/arn%3Aaws%3Aeks", "ListTagsForResource"},
		{"sqs declares no tag binding here", "sqs", "GET", "/tags/arn%3Aaws%3Asqs", ""},
		// Two bindings where checking only "does this service model an
		// operation of the retained name" answered confidently and wrongly,
		// because the service does model that name — at a different binding.
		{"grafana updates rather than creates a workspace", "grafana", "PUT", "/workspaces/w1", "UpdateWorkspace"},
		{"mpa untags rather than tags on POST", "mpa", "POST", "/tags/arn", "UntagResource"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When: the binding is resolved against an already-classified service.
			got := registry.RESTOperation(tt.service, tt.method, tt.path, "")

			// Then: it names that service's own operation, or nothing.
			if got != tt.want {
				t.Errorf("RESTOperation(%q, %s %s) = %q, want %q", tt.service, tt.method, tt.path, got, tt.want)
			}
		})
	}
}

func TestRegistryRESTOperation_scopesUnsharedBinding(t *testing.T) {
	// Given: a binding only one modeled service declares.
	registry := NewRegistry()

	// When: it is resolved for its owner and for another service.
	owner := registry.RESTOperation("accessanalyzer", "GET", "/analyzer", "")
	other := registry.RESTOperation("s3", "GET", "/analyzer", "")

	// Then: an unambiguous binding stays scoped to its owner, so a bucket named
	// like another service's path cannot borrow that service's operation.
	if owner != "ListAnalyzers" || other != "" {
		t.Errorf("RESTOperation() = %q for accessanalyzer and %q for s3; want ListAnalyzers and \"\"", owner, other)
	}
}

func TestRegistryRESTOperation_neverNamesAnotherServicesOperation(t *testing.T) {
	// Given: every modeled REST binding in the pinned corpus.
	registry := NewRegistry()
	checked := 0

	// When: each is resolved for the service that declares it, and for a
	// service that does not.
	for _, op := range manifest {
		if (op.Protocol != ProtocolRESTJSON && op.Protocol != ProtocolRESTXML) || op.HTTPMethod == "" || op.URI == "" || op.Service == "s3" {
			continue
		}
		path, query := corpusRESTRequest(op.URI)
		service := overcastService(op.Service)
		got := registry.RESTOperation(service, op.HTTPMethod, path, query)
		checked++

		// Then: the answer is either that service's own operation at this
		// binding, or nothing — never another service's name. A greedy or
		// literal binding elsewhere in the trie may legitimately win the match,
		// so a different operation of the *same* service is allowed.
		if got != "" && !serviceDeclaresRESTOperation(service, op.HTTPMethod, got) {
			t.Errorf("RESTOperation(%q, %s %s) = %q, which %s does not declare", service, op.HTTPMethod, op.URI, got, service)
		}
	}
	if checked == 0 {
		t.Fatal("walked no modeled REST bindings")
	}
}

func serviceDeclaresRESTOperation(service, method, operation string) bool {
	for _, op := range manifest {
		if op.Name == operation && op.HTTPMethod == method && overcastService(op.Service) == service {
			return true
		}
	}
	return false
}

func TestGeneratedRESTCandidates_matchEveryAmbiguousBinding(t *testing.T) {
	// Given: the generated REST index and the candidate table it points into.
	covered := 0

	// When: every binding entry is inspected.
	for i, op := range restOperations {
		if !op.Ambiguous {
			// Then: an unshared binding needs no candidate set, and carrying
			// one would mean the generator emitted a window it never reads.
			if op.CandidateStart != 0 || op.CandidateEnd != 0 {
				t.Errorf("restOperations[%d] is unambiguous but has candidates [%d,%d)", i, op.CandidateStart, op.CandidateEnd)
			}
			continue
		}
		if op.CandidateStart < 0 || op.CandidateEnd > len(restCandidates) || op.CandidateStart >= op.CandidateEnd {
			t.Fatalf("restOperations[%d] has an out-of-range candidate window [%d,%d) over %d candidates", i, op.CandidateStart, op.CandidateEnd, len(restCandidates))
		}
		window := restCandidates[op.CandidateStart:op.CandidateEnd]
		covered += len(window)

		// Then: an ambiguous binding retains every service that declares it —
		// at least two, or it would not be ambiguous — and the name the index
		// still carries is the first candidate's, not an invented one.
		services := map[string]bool{}
		for _, candidate := range window {
			if candidate.ModelService == "" || candidate.Operation == "" {
				t.Errorf("restOperations[%d] has an incomplete candidate %+v", i, candidate)
			}
			services[candidate.ModelService] = true
		}
		if len(services) < 2 {
			t.Errorf("restOperations[%d] is ambiguous but names %d service(s)", i, len(services))
		}
		if window[0].Operation != op.Operation {
			t.Errorf("restOperations[%d] retains %q but its first candidate is %q", i, op.Operation, window[0].Operation)
		}
	}

	// Then: the windows tile the candidate table exactly, so a regeneration
	// cannot leave an orphaned or double-counted row behind.
	if covered != len(restCandidates) {
		t.Errorf("candidate windows cover %d of %d generated candidates", covered, len(restCandidates))
	}
	if len(restCandidates) == 0 {
		t.Fatal("generated REST candidate table is empty")
	}
}

func TestGeneratedRESTCandidates_haveOneOperationPerServiceKey(t *testing.T) {
	// Given: several modeled identities alias onto one Overcast service key
	// (apigatewayv2 and api-gateway both become "apigateway", for example).

	// When: each ambiguous binding's candidates are grouped by that key.
	for i, op := range restOperations {
		if !op.Ambiguous {
			continue
		}
		byKey := map[string]string{}
		for _, candidate := range restCandidates[op.CandidateStart:op.CandidateEnd] {
			key := overcastService(candidate.ModelService)
			previous, seen := byKey[key]

			// Then: no key maps to two different operation names. RESTOperation
			// returns the first match, so a disagreement here would make its
			// answer depend on generated ordering rather than on the models.
			if seen && previous != candidate.Operation {
				t.Errorf("restOperations[%d] (%s) gives service key %q both %q and %q", i, op.Method, key, previous, candidate.Operation)
			}
			byKey[key] = candidate.Operation
		}
	}
}

func TestRegistryRESTOperation_hasNoAllocations(t *testing.T) {
	// Given: the immutable generated registry.
	registry := NewRegistry()

	// When: an unshared and a heavily shared binding are repeatedly resolved.
	unsharedAllocs := testing.AllocsPerRun(1_000, func() {
		_ = registry.RESTOperation("accessanalyzer", "GET", "/analyzer", "")
	})
	sharedAllocs := testing.AllocsPerRun(1_000, func() {
		_ = registry.RESTOperation("backup", "GET", "/tags/arn", "")
	})

	// Then: intersecting a candidate set stays on the request path's budget —
	// it walks a static table window rather than building one.
	if unsharedAllocs != 0 || sharedAllocs != 0 {
		t.Errorf("RESTOperation allocations = unshared %.1f, shared %.1f; want zero", unsharedAllocs, sharedAllocs)
	}
}

func TestGeneratedRESTCollisions_areReported(t *testing.T) {
	// Given: the generated REST collision index.

	// When: its entries are inspected.
	if len(restCollisions) == 0 {
		t.Fatal("generated REST collision index is empty")
	}
	for _, collision := range restCollisions {
		// Then: every collision retains a readable key and all competing services.
		if collision.Key == "" || len(collision.Services) < 2 {
			t.Errorf("invalid REST collision: %+v", collision)
		}
	}
}

func TestGeneratedRPCCollisions_areWellFormed(t *testing.T) {
	for _, collision := range rpcCollisions {
		if collision.Key == "" || len(collision.Services) < 2 {
			t.Errorf("invalid RPC collision: %+v", collision)
		}
	}
}

func TestGeneratedCorpus_everyNonS3OperationHasRouteOwnership(t *testing.T) {
	// Given: every operation in the pinned Smithy corpus and the immutable
	// indexes generated from that same input.
	registry := NewRegistry()
	var uncovered []string

	// When: each operation is reduced to its safe wire discriminator.
	for _, op := range manifest {
		if op.Service == "s3" {
			continue
		}
		owned := false
		switch op.Protocol {
		case ProtocolUnknown:
			// Unknown protocols deliberately remain uncovered so the assertion
			// below detects them.
		case ProtocolAWSJSON10, ProtocolAWSJSON11:
			if op.TargetPrefix != "" {
				_, owned = registry.ClaimTarget(op.TargetPrefix + op.Name)
			}
		case ProtocolAWSQuery, ProtocolEC2Query:
			_, owned = registry.ClaimQuery(op.APIVersion, op.Name)
		case ProtocolRESTJSON, ProtocolRESTXML:
			path, query := corpusRESTRequest(op.URI)
			_, owned = registry.ClaimRESTQuery(op.HTTPMethod, path, query)
		case ProtocolRPCV2CBOR, ProtocolRPCV2JSON:
			// RPC traits, including additive traits, are checked uniformly
			// below rather than only when RPC is canonical.
		}
		if op.Protocols&ProtocolsRPCV2CBOR != 0 {
			_, rpcOwned := registry.ClaimRPC(ProtocolRPCV2CBOR, op.ServiceShape, op.Name)
			owned = owned || rpcOwned
		}
		if op.Protocols&ProtocolsRPCV2JSON != 0 {
			_, rpcOwned := registry.ClaimRPC(ProtocolRPCV2JSON, op.ServiceShape, op.Name)
			owned = owned || rpcOwned
		}
		if !owned && len(uncovered) < 50 {
			uncovered = append(uncovered, op.Service+"/"+op.Name+" ("+string(op.Protocol)+")")
		}
	}

	// Then: no modeled non-S3 operation can rely on S3 as its owner.
	if len(uncovered) > 0 {
		t.Fatalf("generated corpus contains operations without route ownership (first %d):\n%s", len(uncovered), strings.Join(uncovered, "\n"))
	}
}

func corpusRESTRequest(uri string) (path, query string) {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		path, query = uri[:i], uri[i+1:]
	} else {
		path = uri
	}
	var out strings.Builder
	out.Grow(len(path))
	for start := 0; start < len(path); {
		open := strings.IndexByte(path[start:], '{')
		if open < 0 {
			out.WriteString(path[start:])
			break
		}
		open += start
		out.WriteString(path[start:open])
		close := strings.IndexByte(path[open:], '}')
		if close < 0 {
			out.WriteString(path[open:])
			break
		}
		close += open
		if path[close-1] == '+' {
			out.WriteString("value/part")
		} else {
			out.WriteString("value")
		}
		start = close + 1
	}
	return out.String(), query
}

func TestOvercastService_alias(t *testing.T) {
	// Given: normalized Smithy identities whose Overcast keys differ.
	tests := []struct {
		modelService string
		want         string
	}{
		{modelService: "secrets-manager", want: "secretsmanager"},
		{modelService: "sesv2", want: "ses"},
		{modelService: "wafv2", want: "waf"},
		// WAF Classic is unimplemented and must not share the v2-implementing
		// "waf" key despite its overlapping operation names.
		{modelService: "waf", want: "waf-classic"},
		// Identity pools are a separate unimplemented service; the identity
		// must not collapse into the user-pool-backed "cognito" key.
		{modelService: "cognito-identity", want: "cognito-identity"},
	}

	// When: the registry reports each service identity.
	for _, test := range tests {
		got := overcastService(test.modelService)

		// Then: it preserves the established router key explicitly.
		if got != test.want {
			t.Errorf("overcastService(%q) = %q, want %q", test.modelService, got, test.want)
		}
	}
}

func TestHasOperation_usesOvercastServiceAlias(t *testing.T) {
	// Given: a capability uses Overcast's established service key.

	// When: capgen validates it against the modeled operation corpus.
	got := HasOperation("secretsmanager", "ListSecrets")

	// Then: the model's secrets-manager identity resolves through the alias.
	if !got {
		t.Fatal("HasOperation() did not resolve secretsmanager/ListSecrets")
	}
}

func TestHasOperation_resolvesLegacyServiceFamilies(t *testing.T) {
	// Given: Overcast service keys that cover a separately modeled AWS family.
	tests := []struct {
		service   string
		operation string
	}{
		{service: "bedrock", operation: "InvokeModel"},
		{service: "cognito", operation: "ListUsers"},
		// CreateEmailIdentity exists only in the sesv2 model.
		{service: "ses", operation: "CreateEmailIdentity"},
		// AssociateWebACL exists only in the wafv2 model.
		{service: "waf", operation: "AssociateWebACL"},
	}

	// When: build tooling validates their capabilities against the corpus.
	for _, test := range tests {
		if !HasOperation(test.service, test.operation) {
			t.Errorf("HasOperation(%q, %q) = false, want true", test.service, test.operation)
		}
	}
}

func TestHasOperation_rejectsMisattributedServiceFamilies(t *testing.T) {
	// Given: operations modeled only by a related-but-distinct AWS service
	// that Overcast does not implement.
	tests := []struct {
		service   string
		operation string
		reason    string
	}{
		// GetChangeToken is WAF Classic only; the v2-implementing waf key
		// must not validate against the classic model.
		{service: "waf", operation: "GetChangeToken", reason: "WAF Classic operation"},
		// CreateIdentityPool belongs to cognito-identity (identity pools),
		// not the user-pool service behind the cognito key.
		{service: "cognito", operation: "CreateIdentityPool", reason: "Cognito Federated Identities operation"},
	}

	// When: build tooling validates capability declarations against the corpus.
	for _, test := range tests {
		if HasOperation(test.service, test.operation) {
			t.Errorf("HasOperation(%q, %q) = true, want false (%s)", test.service, test.operation, test.reason)
		}
	}
}

func TestOperations_carriesTheModeledBinding(t *testing.T) {
	// Given: the SES v2 operation that #862 served on the wrong HTTP method.

	// When: build tooling asks where AWS binds it.
	got := Operations("ses", "CreateEmailIdentity")

	// Then: the corpus answers with the method and URI, not merely "yes".
	if len(got) != 1 {
		t.Fatalf("Operations() returned %d bindings, want 1: %+v", len(got), got)
	}
	if got[0].HTTPMethod != "POST" || got[0].URI != "/v2/email/identities" {
		t.Errorf("Operations() = %s %s, want POST /v2/email/identities", got[0].HTTPMethod, got[0].URI)
	}
}

func TestOperations_returnsEveryIdentityBehindOneServiceKey(t *testing.T) {
	// Given: an operation name both API Gateway v1 and v2 model, under the
	// single "apigateway" key. A caller that assumed one answer would silently
	// validate a v2 route against v1's binding.

	// When: build tooling asks for its bindings.
	got := Operations("apigateway", "DeleteDomainName")

	// Then: both modeled identities come back so the caller can select.
	if len(got) < 2 {
		t.Fatalf("Operations() returned %d bindings, want both API Gateway identities: %+v", len(got), got)
	}
	identities := map[string]bool{}
	for _, op := range got {
		identities[op.Service] = true
	}
	for _, want := range []string{"api-gateway", "apigatewayv2"} {
		if !identities[want] {
			t.Errorf("Operations() omitted the %s identity; got %v", want, identities)
		}
	}
}

func TestOperations_unmodeledNameHasNoBinding(t *testing.T) {
	// Given: an operation name AWS does not model.

	// When: build tooling asks for its bindings.
	got := Operations("secretsmanager", "NotAnAWSOperation")

	// Then: the corpus reports none rather than inventing one.
	if len(got) != 0 {
		t.Errorf("Operations() = %+v, want no bindings", got)
	}
}

func TestServiceAliases_referenceModeledIdentities(t *testing.T) {
	// Given: the set of modeled service identities in the corpus.
	identities := map[string]bool{}
	for _, op := range manifest {
		identities[op.Service] = true
	}

	// When: each alias names its source identity.
	for _, alias := range serviceAliases {
		// Then: the source exists in the corpus — a model refresh that drops
		// or renames an identity must fail here, not silently orphan the alias.
		if !identities[alias.ModelService] {
			t.Errorf("serviceAliases maps %q, which is not a modeled identity", alias.ModelService)
		}
	}
}

func TestRegistryLookup_hasNoAllocations(t *testing.T) {
	// Given: the immutable generated registry.
	registry := NewRegistry()

	// When: representative target and Query operations are repeatedly classified.
	targetAllocs := testing.AllocsPerRun(1_000, func() {
		_, _ = registry.ClaimTarget("GameLift.ListBuilds")
	})
	queryAllocs := testing.AllocsPerRun(1_000, func() {
		_, _ = registry.ClaimQuery("2016-11-15", "DescribeInstances")
	})
	restAllocs := testing.AllocsPerRun(1_000, func() {
		_, _ = registry.ClaimREST("GET", "/analyzer")
	})
	rpcAllocs := testing.AllocsPerRun(1_000, func() {
		_, _ = registry.ClaimRPC(ProtocolRPCV2CBOR, "GameLift", "ListBuilds")
	})

	// Then: lookup does not allocate model-sized data or request-local state.
	if targetAllocs != 0 || queryAllocs != 0 || restAllocs != 0 || rpcAllocs != 0 {
		t.Errorf("lookup allocations = target %.1f, query %.1f, REST %.1f, RPC %.1f; want zero", targetAllocs, queryAllocs, restAllocs, rpcAllocs)
	}
}

func TestGeneratedIndexes_areStrictlySorted(t *testing.T) {
	// Given: the generated immutable lookup indexes.

	// When: adjacent entries are inspected in generation order.
	for i := 1; i < len(targetOperations); i++ {
		if targetOperations[i-1].Target >= targetOperations[i].Target {
			t.Fatalf("target index is not strictly sorted at %d: %q >= %q", i, targetOperations[i-1].Target, targetOperations[i].Target)
		}
	}
	for i := 1; i < len(queryOperations); i++ {
		previous, current := queryOperations[i-1], queryOperations[i]
		if previous.Version > current.Version || (previous.Version == current.Version && previous.Operation >= current.Operation) {
			t.Fatalf("Query index is not strictly sorted at %d: (%q, %q) >= (%q, %q)", i, previous.Version, previous.Operation, current.Version, current.Operation)
		}
	}
	for i := 1; i < len(rpcOperations); i++ {
		previous, current := rpcOperations[i-1], rpcOperations[i]
		if compareRPCOperation(previous, current) >= 0 {
			t.Fatalf("RPC index is not strictly sorted at %d: %+v >= %+v", i, previous, current)
		}
	}
	for i := 1; i < len(serviceAliases); i++ {
		if serviceAliases[i-1].ModelService >= serviceAliases[i].ModelService {
			t.Fatalf("service alias index is not strictly sorted at %d: %q >= %q", i, serviceAliases[i-1].ModelService, serviceAliases[i].ModelService)
		}
	}

	// Then: Registry's binary searches have their required ordering invariant.
}

func BenchmarkRegistryClaimTarget(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = registry.ClaimTarget("GameLift.ListBuilds")
	}
}

func BenchmarkRegistryClaimQuery(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = registry.ClaimQuery("2016-11-15", "DescribeInstances")
	}
}

func BenchmarkRegistryClaimREST(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = registry.ClaimREST("GET", "/analyzer")
	}
}

// BenchmarkRegistryRESTOperationSharedBinding uses GET /tags/{resourceArn},
// the most heavily shared binding in the corpus, so the reported cost is the
// worst case for intersecting a candidate set rather than a typical one.
func BenchmarkRegistryRESTOperationSharedBinding(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_ = registry.RESTOperation("backup", "GET", "/tags/arn", "")
	}
}

func BenchmarkRegistryRESTOperationUnsharedBinding(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_ = registry.RESTOperation("accessanalyzer", "GET", "/analyzer", "")
	}
}

func BenchmarkRegistryClaimRPC(b *testing.B) {
	registry := NewRegistry()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = registry.ClaimRPC(ProtocolRPCV2CBOR, "GameLift", "ListBuilds")
	}
}
