package awsapi

import (
	"net/http"
	"sort"
	"strings"
)

// ErrorProfile identifies the AWS error envelope an unsupported operation
// expects. It is derived from the modeled wire protocol, with the router
// retaining responsibility for writing the response.
type ErrorProfile uint8

const (
	ErrorProfileJSON ErrorProfile = iota + 1
	ErrorProfileQueryXML
	ErrorProfileEC2QueryXML
	ErrorProfileXML
	ErrorProfileRPCV2CBOR
	ErrorProfileRPCV2JSON
)

// Claim is immutable routing metadata for one modeled AWS operation.
// Service uses Overcast's service key where it differs from the modeled SDK
// identity; ModelService retains that source identity for diagnostics.
type Claim struct {
	Service      string
	ModelService string
	SigningName  string
	Operation    string
	Protocol     Protocol
	ErrorProfile ErrorProfile
	Ambiguous    bool
	// CatchAll marks a REST binding whose whole URI is one root greedy label,
	// "/{Label+}". It matches every path for its method, so a match says
	// nothing about which service the caller addressed. MediaStore Data's
	// object operations are the only such bindings outside S3 (#2264).
	CatchAll bool
}

// Registry classifies modeled, non-S3 operations that no service handler has
// claimed. Its generated indexes are sorted static data, so a lookup performs
// no model parsing, I/O, startup allocation, or linear manifest scan.
type Registry struct{}

// NewRegistry returns the immutable generated operation registry.
func NewRegistry() *Registry { return &Registry{} }

// ClaimTarget classifies a full X-Amz-Target value such as
// "DynamoDB_20120810.ListTables".
func (r *Registry) ClaimTarget(target string) (Claim, bool) {
	i := sort.Search(len(targetOperations), func(i int) bool {
		return targetOperations[i].Target >= target
	})
	if i == len(targetOperations) || targetOperations[i].Target != target {
		return Claim{}, false
	}
	op := targetOperations[i]
	service, ambiguous := overcastService(op.ModelService), op.Ambiguous
	if ambiguous {
		// No declared owner: a target prefix names its service directly, so
		// the only ambiguity worth resolving here is modeled identities that
		// are one service. See ambiguity.go.
		if resolved := resolveCollision(targetCollisions, target, ""); resolved != "" {
			service, ambiguous = resolved, false
		}
	}
	return Claim{
		Service:      service,
		ModelService: op.ModelService,
		Operation:    op.Operation,
		Protocol:     op.Protocol,
		ErrorProfile: ErrorProfileJSON,
		Ambiguous:    ambiguous,
	}, true
}

// ClaimQuery classifies a fully specified AWS Query operation. Version is
// required because action names are shared across services.
func (r *Registry) ClaimQuery(version, action string) (Claim, bool) {
	if version == "" || action == "" {
		return Claim{}, false
	}
	i := sort.Search(len(queryOperations), func(i int) bool {
		op := queryOperations[i]
		return op.Version > version || (op.Version == version && op.Operation >= action)
	})
	if i == len(queryOperations) {
		return Claim{}, false
	}
	op := queryOperations[i]
	if op.Version != version || op.Operation != action {
		return Claim{}, false
	}
	profile := ErrorProfileQueryXML
	if op.Protocol == ProtocolEC2Query {
		profile = ErrorProfileEC2QueryXML
	}
	service, ambiguous := overcastService(op.ModelService), op.Ambiguous
	if ambiguous {
		// The API version is the declared owner: it is what each Query service's
		// OwnsVersion is built from, so this resolves the claim the same way the
		// router already routes it. See ambiguity.go.
		if resolved := resolveCollision(queryCollisions, version+"\x00"+action, queryVersionOwners[version]); resolved != "" {
			service, ambiguous = resolved, false
		}
	}
	return Claim{
		Service:      service,
		ModelService: op.ModelService,
		Operation:    op.Operation,
		Protocol:     op.Protocol,
		ErrorProfile: profile,
		Ambiguous:    ambiguous,
	}, true
}

// ClaimRPC classifies a Smithy RPC v2 operation by the explicit service and
// operation names carried in /service/{service}/operation/{operation}. The
// index includes additive protocol traits, not only each model's canonical
// protocol.
func (r *Registry) ClaimRPC(protocol Protocol, serviceShape, operation string) (Claim, bool) {
	if (protocol != ProtocolRPCV2CBOR && protocol != ProtocolRPCV2JSON) || serviceShape == "" || operation == "" {
		return Claim{}, false
	}
	serviceShape = ServiceShapeName(serviceShape)
	i := sort.Search(len(rpcOperations), func(i int) bool {
		return compareRPCKey(rpcOperations[i], protocol, serviceShape, operation) >= 0
	})
	if i == len(rpcOperations) || compareRPCKey(rpcOperations[i], protocol, serviceShape, operation) != 0 {
		return Claim{}, false
	}
	op := rpcOperations[i]
	profile := ErrorProfileRPCV2CBOR
	if protocol == ProtocolRPCV2JSON {
		profile = ErrorProfileRPCV2JSON
	}
	return Claim{
		Service:      overcastService(op.ModelService),
		ModelService: op.ModelService,
		Operation:    op.Operation,
		Protocol:     op.Protocol,
		ErrorProfile: profile,
		Ambiguous:    op.Ambiguous,
	}, true
}

// ServiceShapeName reduces a Smithy service identifier to the bare shape name
// the generated indexes are keyed by. The Smithy RPC v2 protocol specifies the
// `{service}` URI label as the service's shape name, but the absolute shape ID
// ("com.amazonaws.example#Example_20200101") and the namespace-qualified form
// are both spellings a caller can reasonably produce, and both name the same
// service. Exported because the router has to normalise the same label the same
// way when it resolves the shape to an implementation — two normalisations that
// could drift apart is how a request gets a claim but no dispatcher.
func ServiceShapeName(serviceShape string) string {
	if separator := strings.LastIndexAny(serviceShape, ".#"); separator >= 0 {
		return serviceShape[separator+1:]
	}
	return serviceShape
}

func compareRPCKey(op rpcOperation, protocol Protocol, serviceShape, operation string) int {
	if op.Protocol != protocol {
		return strings.Compare(string(op.Protocol), string(protocol))
	}
	if op.ServiceShape != serviceShape {
		return strings.Compare(op.ServiceShape, serviceShape)
	}
	return strings.Compare(op.Operation, operation)
}

func compareRPCOperation(left, right rpcOperation) int {
	return compareRPCKey(left, right.Protocol, right.ServiceShape, right.Operation)
}

// ClaimREST classifies a modeled REST JSON or REST XML HTTP binding. Literal
// edges take precedence over labels, matching Smithy's URI binding semantics.
// It walks generated static tables only: no model parsing, map construction,
// or whole-manifest scan occurs on the request path.
func (r *Registry) ClaimREST(method, path string) (Claim, bool) {
	return r.ClaimRESTQuery(method, path, "")
}

// ClaimRESTQuery classifies a modeled REST binding including any literal
// query components declared by Smithy (for example ?operation=tag-resource).
// Query matching is allocation-free and accepts additional client parameters.
func (r *Registry) ClaimRESTQuery(method, path, rawQuery string) (Claim, bool) {
	if path == "" || path[0] != '/' {
		return Claim{}, false
	}

	operationIndex, ok := restTrieMatch(0, method, path, rawQuery, 1)
	if !ok {
		return Claim{}, false
	}

	op := restOperations[operationIndex]
	return Claim{
		Service:      overcastService(op.ModelService),
		ModelService: op.ModelService,
		SigningName:  op.SigningName,
		Operation:    op.Operation,
		Protocol:     op.Protocol,
		ErrorProfile: restErrorProfile(op.Protocol),
		Ambiguous:    op.Ambiguous,
		CatchAll:     isRootCatchAll(operationIndex),
	}, true
}

// isRootCatchAll reports whether a matched REST operation is bound at the
// root node's greedy edge with nothing after it: its URI is "/{Label+}".
// A greedy label followed by a literal ends at a deeper node, so it keeps
// the evidence its literal carries.
func isRootCatchAll(operationIndex int) bool {
	greedy := restTrieNodes[0].Greedy
	if greedy < 0 {
		return false
	}
	node := restTrieNodes[greedy]
	return operationIndex >= node.OperationStart && operationIndex < node.OperationEnd
}

// SigningNameErrorProfile returns the error envelope of the REST protocol a
// SigV4 signing name's modeled bindings speak, for answering a caller whose
// request matched none of them. It reports false for a name no REST binding
// declares; see IsSigningName for which names those are. Where a signing
// name's bindings span both REST protocols, the first in generated order wins,
// which TestSigningNameErrorProfile_isUnambiguous shows never happens today.
func SigningNameErrorProfile(signingName string) (ErrorProfile, bool) {
	profile, ok := signingNameProfiles[strings.ToLower(signingName)]
	return profile, ok
}

// signingNameProfiles is built once from the generated REST operations, as
// signingNames is.
var signingNameProfiles = func() map[string]ErrorProfile {
	profiles := make(map[string]ErrorProfile, 128)
	for _, op := range restOperations {
		if op.SigningName == "" {
			continue
		}
		name := strings.ToLower(op.SigningName)
		if _, seen := profiles[name]; seen {
			continue
		}
		profiles[name] = restErrorProfile(op.Protocol)
	}
	return profiles
}()

func restErrorProfile(p Protocol) ErrorProfile {
	if p == ProtocolRESTXML {
		return ErrorProfileXML
	}
	return ErrorProfileJSON
}

// RESTOperation returns the operation the pinned models bind to this request
// for one already-classified service, or "" when that service binds no
// operation to this method and URI shape.
//
// It answers the question ClaimRESTQuery deliberately cannot. A Claim
// describes the binding — enough to choose a 501 envelope — and where several
// services share the binding it names no owner, because picking one would
// invent an attribution the models do not support. A caller that has already
// established the service (from the SigV4 credential scope, a path prefix, or
// a matched route) does not need an owner picked for it: intersecting the
// generated candidate set with that service is exact. Every service outside
// the set gets "", so no request can borrow another service's operation name.
//
// service is an Overcast service key, as ServiceKey reports it. Where several
// modeled identities alias to one key, the lowest modeled identity in the
// binding's generated order wins; TestGeneratedRESTCandidates_haveOneOperationPerServiceKey
// asserts that no such pair disagrees about the operation name.
//
// Like ClaimREST it walks static tables only and allocates nothing, so it
// needs no request-path cache. Intersecting a candidate set is a scan, but the
// sets are small: 285 shared bindings hold 1,062 candidates between them, and
// three tag bindings account for a third of those. BenchmarkRegistryRESTOperation*
// (Go 1.24 in Docker on a Ryzen 5900X, -benchtime=200000x, best of three)
// measures 88ns for an unshared binding and 601ns for GET /tags/{resourceArn},
// the 122-candidate worst case, both at zero allocations.
func (r *Registry) RESTOperation(service, method, path, rawQuery string) string {
	if service == "" || path == "" || path[0] != '/' {
		return ""
	}
	operationIndex, ok := restTrieMatch(0, method, path, rawQuery, 1)
	if !ok {
		return ""
	}
	op := restOperations[operationIndex]
	if !op.Ambiguous {
		if overcastService(op.ModelService) != service {
			return ""
		}
		return op.Operation
	}
	for _, candidate := range restCandidates[op.CandidateStart:op.CandidateEnd] {
		if overcastService(candidate.ModelService) == service {
			return candidate.Operation
		}
	}
	return ""
}

// restTrieMatch walks one path without allocating a strings.Split result. A
// Smithy greedy label can be followed by literal segments, so it tries each
// possible number of consumed segments only after literal and simple-label
// branches fail. That preserves URI precedence while supporting bindings such
// as /mrap/instances/{Name+}/policy.
func restTrieMatch(node int, method, path, rawQuery string, start int) (int, bool) {
	if start == len(path) {
		current := restTrieNodes[node]
		for i := current.OperationStart; i < current.OperationEnd; i++ {
			if restOperationMatches(restOperations[i], method, rawQuery) {
				return i, true
			}
		}
		return 0, false
	}
	end := strings.IndexByte(path[start:], '/')
	if end < 0 {
		end = len(path)
	} else {
		end += start
	}
	nextStart := end
	if nextStart < len(path) {
		nextStart++
	}
	current := restTrieNodes[node]
	if literal := restLiteralNode(current, path[start:end]); literal >= 0 {
		if matched, ok := restTrieMatch(literal, method, path, rawQuery, nextStart); ok {
			return matched, true
		}
	}
	if current.Parameter >= 0 {
		if matched, ok := restTrieMatch(current.Parameter, method, path, rawQuery, nextStart); ok {
			return matched, true
		}
	}
	if current.Greedy < 0 {
		return 0, false
	}
	for greedyStart := nextStart; ; {
		if matched, ok := restTrieMatch(current.Greedy, method, path, rawQuery, greedyStart); ok {
			return matched, true
		}
		if greedyStart == len(path) {
			break
		}
		separator := strings.IndexByte(path[greedyStart:], '/')
		if separator < 0 {
			greedyStart = len(path)
		} else {
			greedyStart += separator + 1
		}
	}
	return 0, false
}

func restOperationMatches(op restOperation, method, rawQuery string) bool {
	if op.Method != method {
		return false
	}
	for required := op.Query; required != ""; {
		end := strings.IndexByte(required, '&')
		part := required
		if end >= 0 {
			part = required[:end]
			required = required[end+1:]
		} else {
			required = ""
		}
		if !rawQueryContains(rawQuery, part) {
			return false
		}
	}
	return true
}

func rawQueryContains(rawQuery, required string) bool {
	for rawQuery != "" {
		end := strings.IndexByte(rawQuery, '&')
		part := rawQuery
		if end >= 0 {
			part = rawQuery[:end]
			rawQuery = rawQuery[end+1:]
		} else {
			rawQuery = ""
		}
		// Net/http and SDK serializers may render a valueless Smithy query
		// literal as either "?acl" or "?acl=". Preserve exact matching for
		// literals that declare a value.
		if part == required || (!strings.Contains(required, "=") && part == required+"=") {
			return true
		}
	}
	return false
}

func restLiteralNode(node restTrieNode, segment string) int {
	i := sort.Search(node.LiteralEnd-node.LiteralStart, func(i int) bool {
		return restTrieEdges[node.LiteralStart+i].Segment >= segment
	})
	if i == node.LiteralEnd-node.LiteralStart {
		return -1
	}
	edge := restTrieEdges[node.LiteralStart+i]
	if edge.Segment != segment {
		return -1
	}
	return edge.Node
}

// Claim classifies the model-backed request forms that can be distinguished
// without decoding an AWS JSON body. REST bindings are exposed separately via
// ClaimREST because their late fallback must preserve S3's legitimate paths.
// For an AWS
// Query request it calls ParseForm, which consumes the request body; callers
// must therefore invoke it only where Query form parsing is already intended.
func (r *Registry) Claim(req *http.Request) (Claim, bool) {
	if target := strings.TrimSpace(req.Header.Get("X-Amz-Target")); target != "" {
		return r.ClaimTarget(target)
	}
	if !strings.Contains(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		return Claim{}, false
	}
	if err := req.ParseForm(); err != nil {
		return Claim{}, false
	}
	return r.ClaimQuery(req.FormValue("Version"), req.FormValue("Action"))
}

// overcastService translates the small set of modeled SDK identities whose
// normalized names differ from the router's established service keys. Keep
// this explicit: a Smithy service name is not automatically an Overcast key.
func overcastService(modelService string) string {
	i := sort.Search(len(serviceAliases), func(i int) bool {
		return serviceAliases[i].ModelService >= modelService
	})
	if i < len(serviceAliases) && serviceAliases[i].ModelService == modelService {
		return serviceAliases[i].OvercastService
	}
	return modelService
}
