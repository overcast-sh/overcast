//go:build dev

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/awsmodel"
)

// The pruned shape snapshot, read back.
//
// cmd/awsmodelgen writes models/aws/shapes/<service>.json (see its README for
// the layout); this file is the consumer side. It decodes the snapshot into
// awsmodel.Snapshot — the pruner's own declaration, so writer and reader cannot
// drift — and then answers the questions the generator asks of a model: which
// operations exist, which input members are required and of what kind, what a
// response path resolves to, what an error shape's wire code is, and what
// constraints a literal must satisfy.
//
// Nothing here reads the raw corpus. A service the snapshot does not cover is
// refused with the instruction to widen models/aws/shapes-services.txt.

// serviceModel is the snapshot plus the derived facts the generator needs.
type serviceModel struct {
	awsmodel.Snapshot
	// EndpointPrefix and SigningName come from the service shape's traits; the
	// interpreters need them to build a client without a table of their own.
	EndpointPrefix string
	SigningName    string
	// QueryCompatible is aws.protocols#awsQueryCompatible: a JSON-protocol
	// service that was migrated from Query and still answers with the Query
	// error code in x-amzn-query-error. Which of the two codes an SDK
	// surfaces depends on the SDK, so an interpreter has to be told.
	QueryCompatible bool
	// operationNames is every operation reachable from the service, sorted.
	operationNames []string
}

// modelMissingHint is what a user reads when a recipe names a service whose
// shapes are not committed. The generator never falls back to the raw corpus.
const modelMissingHint = "add the service to models/aws/shapes-services.txt and regenerate the snapshot with `make generate-aws-operations` (see cmd/awsmodelgen/README.md)"

// snapshotServiceFor is the shape-snapshot key for an Overcast service key
// that has no recipe to state it — an authored scenario's. A recipe says it in
// its `model` field (secretsmanager's is secrets-manager); an authored scenario
// has no such field, because the scenario file deliberately carries no
// per-snapshot naming (compat/model/README.md § Naming) and its `service` must
// be the hand-written registry group's, which is the Overcast key. So the key
// is resolved here instead, from the one table that already relates the two:
// a snapshot named for the service itself wins (kinesis, cloudwatch-logs), and
// otherwise the one committed snapshot whose model service awsapi.ServiceKey
// maps onto it (cognito-identity-provider for cognito). None leaves the service
// its own name, so loadModel refuses it with the instruction to widen the
// snapshot; more than one is ambiguous and refused here, naming them.
func snapshotServiceFor(shapesDir, service string) (string, error) {
	if _, err := os.Stat(filepath.Join(shapesDir, service+".json")); err == nil {
		return service, nil
	}
	files, err := filepath.Glob(filepath.Join(shapesDir, "*.json"))
	if err != nil {
		return "", err
	}
	var matches []string
	for _, f := range files {
		model := strings.TrimSuffix(filepath.Base(f), ".json")
		if awsapi.ServiceKey(model) == service {
			matches = append(matches, model)
		}
	}
	switch len(matches) {
	case 0:
		return service, nil
	case 1:
		return matches[0], nil
	default:
		sort.Strings(matches)
		return "", fmt.Errorf("service %q is the Overcast key of %d committed shape snapshots (%s); an authored scenario cannot say which it means",
			service, len(matches), strings.Join(matches, ", "))
	}
}

// loadModel reads models/aws/shapes/<modelService>.json.
func loadModel(shapesDir, modelService string) (*serviceModel, error) {
	path := filepath.Join(shapesDir, modelService+".json")
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no shape snapshot for service %q at %s: %s", modelService, path, modelMissingHint)
		}
		return nil, fmt.Errorf("read shape snapshot %s: %w", path, err)
	}
	var snapshot awsmodel.Snapshot
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return nil, fmt.Errorf("parse shape snapshot %s: %w", path, err)
	}
	if snapshot.Service != modelService {
		return nil, fmt.Errorf("shape snapshot %s declares service %q", path, snapshot.Service)
	}
	model := &serviceModel{Snapshot: snapshot}
	service, ok := snapshot.Shapes[snapshot.ServiceShape]
	if !ok || service.Type != "service" {
		return nil, fmt.Errorf("shape snapshot %s has no service shape %q", path, snapshot.ServiceShape)
	}
	if err := model.readServiceTraits(service); err != nil {
		return nil, fmt.Errorf("shape snapshot %s: %w", path, err)
	}
	names, err := model.collectOperations(snapshot.ServiceShape)
	if err != nil {
		return nil, fmt.Errorf("shape snapshot %s: %w", path, err)
	}
	model.operationNames = names
	return model, nil
}

func (m *serviceModel) readServiceTraits(service awsmodel.SnapshotShape) error {
	var trait struct {
		EndpointPrefix string `json:"endpointPrefix"`
		ARNNamespace   string `json:"arnNamespace"`
	}
	if raw, ok := service.Traits["aws.api#service"]; ok {
		if err := json.Unmarshal(raw, &trait); err != nil {
			return fmt.Errorf("parse aws.api#service trait: %w", err)
		}
	}
	m.EndpointPrefix = trait.EndpointPrefix
	if m.EndpointPrefix == "" {
		// The trait's endpointPrefix is optional, and the AWS model for S3
		// Tables omits it, stating only arnNamespace "s3tables". Every
		// consumer of the client block needs a prefix — the cli and python-sdk
		// interpreters derive the aws command and the botocore service name
		// from it — and botocore's own metadata for that service
		// (service-2.json, measured with botocore 1.43.67) gives
		// endpointPrefix "s3tables", the arnNamespace. So an absent prefix
		// reads as the arnNamespace; with neither, it stays empty and the
		// interpreters refuse the file as before.
		m.EndpointPrefix = trait.ARNNamespace
	}
	var sigv4 struct {
		Name string `json:"name"`
	}
	if raw, ok := service.Traits["aws.auth#sigv4"]; ok {
		if err := json.Unmarshal(raw, &sigv4); err != nil {
			return fmt.Errorf("parse aws.auth#sigv4 trait: %w", err)
		}
	}
	m.SigningName = sigv4.Name
	m.QueryCompatible = hasTrait(service.Traits, "aws.protocols#awsQueryCompatible")
	return nil
}

// collectOperations walks the service and its resources, exactly as
// internal/awsmodel does for the manifest, so the two agree on what "the
// service's operations" means.
func (m *serviceModel) collectOperations(serviceShape string) ([]string, error) {
	seen := make(map[string]struct{})
	var visitResource func(name string) error
	visitResource = func(name string) error {
		resource, ok := m.Shapes[name]
		if !ok {
			return fmt.Errorf("service references missing resource %q", name)
		}
		for _, op := range []string{resource.Create, resource.Put, resource.Read, resource.Update, resource.Delete, resource.List} {
			if op != "" {
				seen[op] = struct{}{}
			}
		}
		for _, op := range resource.Operations {
			seen[op] = struct{}{}
		}
		for _, op := range resource.CollectionOperations {
			seen[op] = struct{}{}
		}
		for _, child := range resource.Resources {
			if err := visitResource(child); err != nil {
				return err
			}
		}
		return nil
	}
	service := m.Shapes[serviceShape]
	for _, op := range service.Operations {
		seen[op] = struct{}{}
	}
	for _, resource := range service.Resources {
		if err := visitResource(resource); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		if shape, ok := m.Shapes[name]; !ok || shape.Type != "operation" {
			return nil, fmt.Errorf("service references %q, which is not an operation shape", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Operations returns every modeled operation name, sorted.
func (m *serviceModel) Operations() []string { return m.operationNames }

// HasOperation reports whether the service models the named operation.
func (m *serviceModel) HasOperation(name string) bool {
	i := sort.SearchStrings(m.operationNames, name)
	return i < len(m.operationNames) && m.operationNames[i] == name
}

// Resources returns the Smithy resource shapes reachable from the service,
// sorted. Empty for services whose model does not use resource shapes.
func (m *serviceModel) Resources() []string {
	var names []string
	for name, shape := range m.Shapes {
		if shape.Type == "resource" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// InputShape returns the name of an operation's input structure, or "" for a
// unit input.
func (m *serviceModel) InputShape(op string) string {
	return m.nonUnit(m.Shapes[op].Input)
}

// OutputShape returns the name of an operation's output structure, or "" for a
// unit output.
func (m *serviceModel) OutputShape(op string) string {
	return m.nonUnit(m.Shapes[op].Output)
}

func (m *serviceModel) nonUnit(target string) string {
	if target == "" || target == "smithy.api#Unit" {
		return ""
	}
	return target
}

// Members returns a structure's member names, sorted.
func (m *serviceModel) Members(structure string) []string {
	shape := m.Shapes[structure]
	names := make([]string, 0, len(shape.Members))
	for name := range shape.Members {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RequiredMembers returns the members a caller must send, sorted: every
// @required member, whether or not it also carries @clientOptional.
//
// This read used to exclude a @clientOptional member, on the reading that the
// trait says the service does not really require it. It does not. The trait's
// own definition is about *generators*: it "requires that non-authoritative
// generators like clients treat a structure member as optional regardless of
// if the member is also marked with the required trait", which is a statement
// about a client's nullability, not about what the service validates. AWS
// Batch is where the difference showed up — it marks all 182 of its @required
// input members @clientOptional as well, and no other snapshot in the corpus
// carries one — and the reading is measured rather than argued: the AWS SDK
// for Go v2's own generated validator refuses the call before it reaches the
// wire, `1 validation error(s) found. - missing required field,
// DescribeJobsInput.Jobs.`, so a probe that omitted such a member reported
// `fail` instead of the `unimplemented` a 501 would have given it.
//
// smithy-rs really does read the trait the other way, and that is where its
// meaning belongs: a builder whose required members are all @clientOptional
// gets an infallible build(). That question is rustBuilderIsFallible's, and it
// applies the exclusion itself rather than borrowing it from here.
func (m *serviceModel) RequiredMembers(structure string) []string {
	shape := m.Shapes[structure]
	var names []string
	for name, member := range shape.Members {
		if hasTrait(member.Traits, "smithy.api#required") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// MemberTarget returns the shape a structure member targets.
func (m *serviceModel) MemberTarget(structure, member string) (string, bool) {
	entry, ok := m.Shapes[structure].Members[member]
	if !ok {
		return "", false
	}
	return entry.Target, true
}

// preludeShapeTypes is the Smithy type behind each prelude shape. The pruner
// never emits a prelude target (smithy.api#String, …), so their types are known
// by name rather than looked up.
//
// Kind rounds them: byte, short, integer and long all answer "integer", and
// float, double and bigDecimal all answer "float". A backend that has to choose
// a numeric literal's suffix, or the scalar type a value is converted to, needs
// the distinction back — and ShapeType is where it lives.
var preludeShapeTypes = map[string]string{
	"smithy.api#String":           "string",
	"smithy.api#Blob":             "blob",
	"smithy.api#Boolean":          "boolean",
	"smithy.api#PrimitiveBoolean": "boolean",
	"smithy.api#Byte":             "byte",
	"smithy.api#Short":            "short",
	"smithy.api#Integer":          "integer",
	"smithy.api#PrimitiveInteger": "integer",
	"smithy.api#Long":             "long",
	"smithy.api#PrimitiveLong":    "long",
	"smithy.api#Float":            "float",
	"smithy.api#Double":           "double",
	"smithy.api#BigInteger":       "bigInteger",
	"smithy.api#BigDecimal":       "bigDecimal",
	"smithy.api#Timestamp":        "timestamp",
	"smithy.api#Document":         "document",
	"smithy.api#Unit":             "unit",
}

// ShapeType is the shape's own Smithy type, unrounded: byte, short, integer,
// long, bigInteger, float, double, bigDecimal, string, enum, boolean, list,
// map, structure, union. A `string` shape carrying the legacy @enum trait
// answers "enum", as Kind does, because the two spellings of an enum are the
// same thing to every caller, and a `set` answers "list" for the same reason.
// Use Kind where the rounded classification is what is wanted; use this where
// the exact width matters.
func (m *serviceModel) ShapeType(target string) string {
	if t, ok := preludeShapeTypes[target]; ok {
		return t
	}
	shape, ok := m.Shapes[target]
	if !ok {
		return "unknown"
	}
	switch {
	case shape.Type == "string" && hasTrait(shape.Traits, "smithy.api#enum"):
		return "enum"
	case shape.Type == "set":
		return "list"
	}
	return shape.Type
}

// ElementTarget is the shape a list's members target, or "" for a shape that
// is not a list.
func (m *serviceModel) ElementTarget(target string) string { return m.Shapes[target].Member }

// KeyTarget is the shape a map's keys target, or "" for a shape that is not a
// map.
func (m *serviceModel) KeyTarget(target string) string { return m.Shapes[target].Key }

// ValueTarget is the shape a map's values target, or "" for a shape that is
// not a map.
func (m *serviceModel) ValueTarget(target string) string { return m.Shapes[target].Value }

// Kind classifies a shape as one of: string, enum, integer, float, boolean,
// timestamp, blob, document, list, map, structure, union, unit.
//
// It is ShapeType rounded, and deliberately nothing else: two tables saying
// which prelude shape is which would be two tables to keep in step, and the
// rounding is the only thing this adds.
func (m *serviceModel) Kind(target string) string {
	switch shapeType := m.ShapeType(target); shapeType {
	case "byte", "short", "integer", "long", "bigInteger", "intEnum":
		return "integer"
	case "float", "double", "bigDecimal":
		return "float"
	default:
		return shapeType
	}
}

// EnumValues returns an enum shape's wire values in the order the shape
// snapshot carries them. The order is load-bearing: binding rule 4 takes the
// first value, and "first" has to be the model's answer rather than an
// artefact of sorting — so this deliberately does not sort, and
// EnumValuesSorted is the separate copy a membership search uses.
//
// One limitation is worth stating. A `type: enum` shape carries its members as
// a JSON object, and cmd/awsmodelgen writes object keys through encoding/json,
// which sorts them, so for those shapes the snapshot's own order is already
// alphabetical and the model's declaration order is not recoverable here.
// Recovering it means teaching cmd/awsmodelgen to emit an ordered member list;
// until then the pick is at least deterministic, and Go's map iteration is not
// allowed to make it otherwise, which is why that branch still sorts. A
// `smithy.api#enum` trait is a JSON array and does keep the model's order,
// which is the case this preserves.
func (m *serviceModel) EnumValues(target string) []string {
	shape := m.Shapes[target]
	var values []string
	switch shape.Type {
	case "enum":
		for name, member := range shape.Members {
			var value string
			if raw, ok := member.Traits["smithy.api#enumValue"]; ok {
				if err := json.Unmarshal(raw, &value); err != nil {
					value = name
				}
			} else {
				value = name
			}
			values = append(values, value)
		}
		// Members decode into a map, so nothing but sorting is deterministic.
		sort.Strings(values)
	case "string":
		var entries []struct {
			Value string `json:"value"`
		}
		if raw, ok := shape.Traits["smithy.api#enum"]; ok {
			if err := json.Unmarshal(raw, &entries); err == nil {
				for _, entry := range entries {
					values = append(values, entry.Value)
				}
			}
		}
	}
	return values
}

// EnumValuesSorted is EnumValues sorted, for sort.SearchStrings. It is a copy:
// the caller of EnumValues must keep the model's order.
func (m *serviceModel) EnumValuesSorted(target string) []string {
	values := append([]string(nil), m.EnumValues(target)...)
	sort.Strings(values)
	return values
}

// constraints are the value-level traits a literal is checked against.
type constraints struct {
	LengthMin, LengthMax *int64
	RangeMin, RangeMax   *json.Number
	Pattern              string
}

// Constraints collects the constraint traits declared on a shape.
func (m *serviceModel) Constraints(target string) constraints {
	var out constraints
	shape, ok := m.Shapes[target]
	if !ok {
		return out
	}
	if raw, ok := shape.Traits["smithy.api#length"]; ok {
		var length struct {
			Min, Max *int64
		}
		if err := json.Unmarshal(raw, &length); err == nil {
			out.LengthMin, out.LengthMax = length.Min, length.Max
		}
	}
	if raw, ok := shape.Traits["smithy.api#range"]; ok {
		var rng struct {
			Min, Max *json.Number
		}
		if err := json.Unmarshal(raw, &rng); err == nil {
			out.RangeMin, out.RangeMax = rng.Min, rng.Max
		}
	}
	if raw, ok := shape.Traits["smithy.api#pattern"]; ok {
		_ = json.Unmarshal(raw, &out.Pattern)
	}
	return out
}

// ErrorCode returns the code an SDK surfaces for an error shape: the
// aws.protocols#awsQueryError code when the service declares one (SQS's
// QueueDoesNotExist is AWS.SimpleQueueService.NonExistentQueue on the wire),
// else the shape's own name.
func (m *serviceModel) ErrorCode(shape string) string {
	if raw, ok := m.Shapes[shape].Traits["aws.protocols#awsQueryError"]; ok {
		var trait struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(raw, &trait); err == nil && trait.Code != "" {
			return trait.Code
		}
	}
	return shape
}

// paginationTrait is smithy.api#paginated, as far as the generator reads it:
// which output member carries the next-page token, and which carries the page
// of items. cmd/awsmodelgen already allowlists the trait, so the committed
// snapshots carry it and nothing has to be regenerated to consult it.
// `inputToken` and `pageSize` are deliberately absent: nothing generated
// pages, so decoding them would be two fields written and never read.
type paginationTrait struct {
	OutputToken string `json:"outputToken"`
	Items       string `json:"items"`
}

// Pagination returns an operation's @paginated trait, zero-valued where the
// operation declares none. A service may paginate without declaring it —
// organizations' ListInboundResponsibilityTransfers returns a `NextToken` and
// carries no trait — so callers take the trait as authoritative where it
// exists and fall back to the name convention where it does not.
func (m *serviceModel) Pagination(op string) paginationTrait {
	var trait paginationTrait
	if raw, ok := m.Shapes[op].Traits["smithy.api#paginated"]; ok {
		_ = json.Unmarshal(raw, &trait)
	}
	return trait
}

// IsErrorShape reports whether a shape carries @error.
func (m *serviceModel) IsErrorShape(shape string) bool {
	return hasTrait(m.Shapes[shape].Traits, "smithy.api#error")
}

// OperationErrors returns the error shapes an operation declares, sorted.
func (m *serviceModel) OperationErrors(op string) []string {
	errs := append([]string(nil), m.Shapes[op].Errors...)
	sort.Strings(errs)
	return errs
}

func hasTrait(traits map[string]json.RawMessage, name string) bool {
	_, ok := traits[name]
	return ok
}

// ResolvePath walks a response path from a structure shape and returns the
// shape it lands on. `$` is the structure itself; `.Name` selects a structure
// member or a map value; `[n]` selects a list member. The returned shape name
// may be a prelude target.
func (m *serviceModel) ResolvePath(structure string, path responsePath) (string, error) {
	current := structure
	for _, segment := range path.segments {
		kind := m.Kind(current)
		switch {
		case segment.index >= 0:
			if kind != "list" {
				return "", fmt.Errorf("%s: [%d] applied to %s, which is a %s not a list", path, segment.index, current, kind)
			}
			current = m.Shapes[current].Member
		case kind == "structure":
			target, ok := m.MemberTarget(current, segment.name)
			if !ok {
				return "", fmt.Errorf("%s: %s has no member %q", path, current, segment.name)
			}
			current = target
		case kind == "map":
			current = m.Shapes[current].Value
		default:
			return "", fmt.Errorf("%s: .%s applied to %s, which is a %s not a structure or map", path, segment.name, current, kind)
		}
	}
	return current, nil
}

// patternMatches compiles a Smithy pattern under Go's RE2 syntax and reports
// whether the value matches. Smithy patterns are ECMAScript regular
// expressions; the few that RE2 cannot express (lookaround, backreferences)
// are reported as unverifiable rather than as failures.
func patternMatches(pattern, value string) (matched bool, verifiable bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, false
	}
	return re.MatchString(value), true
}
