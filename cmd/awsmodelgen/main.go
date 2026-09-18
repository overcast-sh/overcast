// Command awsmodelgen converts pinned AWS Smithy JSON AST models into compact
// static operation metadata. It is an update-time tool; the emulator never
// reads model files at startup or while serving requests.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsmodel"
)

func main() {
	modelsDir := flag.String("models", "", "directory containing Smithy JSON AST files")
	output := flag.String("output", "internal/awsapi/manifest.gen.go", "generated Go file")
	check := flag.Bool("check", false, "check that -output matches generated content without writing")
	revision := flag.String("source-revision", "", "pinned upstream model revision")
	inventoryOutput := flag.String("inventory-output", "", "optional deterministic JSON inventory output")
	baselineInventory := flag.String("baseline-inventory", "", "optional prior JSON inventory to compare")
	summaryOutput := flag.String("summary-output", "", "optional Markdown change summary output")
	changelogOutput := flag.String("changelog-output", "", "optional changelog fragment output, written only when the diff changes what a caller can observe")
	versionFile := flag.String("version-file", "", "optional provenance VERSION file to update")
	modelDate := flag.String("model-date", "", "upstream commit date, read by -version-file and -changelog-output")
	shapesOut := flag.String("shapes-out", "", "optional directory for the pruned Smithy shape snapshot")
	shapesServices := flag.String("shapes-services", "", "reviewed in-scope service list required with -shapes-out")
	sdkShapesOut := flag.String("sdk-shapes-out", "", "optional package directory for the runtime SDK-integration shape tables")
	sdkShapesServices := flag.String("sdk-shapes-services", "", "reviewed service list required with -sdk-shapes-out")
	flag.Parse()
	if *modelsDir == "" || *revision == "" {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -models and -source-revision are required")
		os.Exit(2)
	}
	if (*baselineInventory == "") != (*summaryOutput == "") {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -baseline-inventory and -summary-output must be used together")
		os.Exit(2)
	}
	// -model-date has two consumers now, so it is no longer paired with
	// -version-file alone: each consumer requires it, and it requires at least
	// one of them, which keeps a date passed to nothing from being ignored.
	if *versionFile != "" && *modelDate == "" {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -version-file requires -model-date")
		os.Exit(2)
	}
	// The fragment is a diff against the previous corpus and dates itself from
	// the upstream commit, so it needs both the baseline and -model-date.
	if *changelogOutput != "" && (*baselineInventory == "" || *modelDate == "") {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -changelog-output requires -baseline-inventory and -model-date")
		os.Exit(2)
	}
	if *modelDate != "" && *versionFile == "" && *changelogOutput == "" {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -model-date needs -version-file or -changelog-output to read it")
		os.Exit(2)
	}
	if (*shapesOut == "") != (*shapesServices == "") {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -shapes-out and -shapes-services must be used together")
		os.Exit(2)
	}
	// The provenance file records one digest per committed artifact. Writing it
	// without regenerating the snapshot would leave shapes-sha256 describing a
	// revision the snapshot no longer came from, which is exactly the drift the
	// digest exists to make impossible.
	if *versionFile != "" && *shapesOut == "" {
		fmt.Fprintf(os.Stderr, "awsmodelgen: -version-file requires -shapes-out so %s cannot go stale\n", ShapesDigestField)
		os.Exit(2)
	}
	// The same holds for the runtime SDK shape tables and their digest.
	if (*sdkShapesOut == "") != (*sdkShapesServices == "") {
		fmt.Fprintln(os.Stderr, "awsmodelgen: -sdk-shapes-out and -sdk-shapes-services must be used together")
		os.Exit(2)
	}
	if *versionFile != "" && *sdkShapesOut == "" {
		fmt.Fprintf(os.Stderr, "awsmodelgen: -version-file requires -sdk-shapes-out so %s cannot go stale\n", SDKShapesDigestField)
		os.Exit(2)
	}
	if err := awsmodel.VerifyRevision(*modelsDir, *revision); err != nil {
		fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
		os.Exit(1)
	}
	operations, err := awsmodel.LoadOperations(*modelsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
		os.Exit(1)
	}
	contents, err := renderManifest(operations, *revision)
	if err != nil {
		fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
		os.Exit(1)
	}
	if err := writeOrCheckManifest(*output, contents, *check); err != nil {
		fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
		os.Exit(1)
	}
	shapesDigest := ""
	if *shapesOut != "" {
		services, err := readShapeServices(*shapesServices)
		if err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
		rendered, err := buildShapeSnapshots(*modelsDir, services)
		if err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
		files, err := shapeSnapshotFiles(rendered)
		if err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
		shapesDigest = ShapesDigest(files)
		if err := writeOrCheckShapes(*shapesOut, files, *check); err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
	}
	sdkShapesDigest := ""
	if *sdkShapesOut != "" {
		services, err := readShapeServices(*sdkShapesServices)
		if err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
		files, err := buildSDKShapes(*modelsDir, services)
		if err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
		sdkShapesDigest = ShapesDigest(files)
		if err := writeOrCheckSDKShapes(*sdkShapesOut, files, *check); err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
	}
	if *inventoryOutput != "" || *baselineInventory != "" {
		inventory := buildModelInventory(*revision, operations)
		if *inventoryOutput != "" {
			if err := writeModelInventory(*inventoryOutput, inventory); err != nil {
				fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
				os.Exit(1)
			}
		}
		if *baselineInventory != "" {
			baseline, err := readModelInventory(*baselineInventory)
			if err != nil {
				fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
				os.Exit(1)
			}
			if err := writeInventorySummary(*summaryOutput, baseline, inventory); err != nil {
				fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
				os.Exit(1)
			}
			if *changelogOutput != "" {
				// Printed either way: the caller waives the changelog gate on
				// "inert", and a waiver nobody can trace back to a check is the
				// reflex the gate exists to prevent.
				written, err := writeChangelogFragment(*changelogOutput, diffInventories(baseline, inventory), *modelDate)
				if err != nil {
					fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
					os.Exit(1)
				}
				if written {
					fmt.Printf("changelog: fragment written to %s\n", *changelogOutput)
				} else {
					fmt.Println("changelog: inert — no operation, protocol-trait, binding or collision change in this refresh")
				}
			}
		}
	}
	if *versionFile != "" {
		if err := updateModelVersion(*versionFile, *revision, *modelDate, ManifestDigest(contents), shapesDigest, sdkShapesDigest); err != nil {
			fmt.Fprintf(os.Stderr, "awsmodelgen: %v\n", err)
			os.Exit(1)
		}
	}
}

func writeOrCheckManifest(path string, contents []byte, check bool) error {
	if !check {
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		return nil
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated manifest %s: %w", path, err)
	}
	if !bytes.Equal(current, contents) {
		return fmt.Errorf("%s is stale; run make generate-aws-operations with the pinned AWS model checkout", path)
	}
	return nil
}

// operation is a local alias for the shared Smithy-model reading type. Both
// the type name and its historical zero-value construction sites
// (operation{Service: ..., ...} literals throughout this file and its tests)
// are kept unchanged; only the reading half moved to internal/awsmodel.
type operation = awsmodel.Operation

func generateManifest(modelsDir, revision string) ([]byte, error) {
	operations, err := awsmodel.LoadOperations(modelsDir)
	if err != nil {
		return nil, err
	}
	return renderManifest(operations, revision)
}

func renderManifest(operations []operation, revision string) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("// Code generated by cmd/awsmodelgen; DO NOT EDIT.\n")
	out.WriteString("package awsapi\n\n")
	fmt.Fprintf(&out, "const SourceRevision = %q\n\n", revision)
	out.WriteString("var manifest = []Operation{\n")
	for _, op := range operations {
		fmt.Fprintf(&out, "\t{Service: %q, ServiceShape: %q, SDKID: %q, APIVersion: %q, Name: %q, Protocol: Protocol%s, Protocols: %s, TargetPrefix: %q, HTTPMethod: %q, URI: %q},\n", op.Service, op.ServiceShape, op.SDKID, op.APIVersion, op.Name, op.Protocol, protocolSetExpression(op.Protocols), op.TargetPrefix, op.HTTPMethod, op.URI)
	}
	out.WriteString("}\n")
	writeRegistryIndexes(&out, operations)
	formatted, err := format.Source(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format generated manifest: %w", err)
	}
	return formatted, nil
}

// writeRegistryIndexes emits the immutable lookup indexes used by the router.
// REST bindings are compiled into a segment trie at generation time so runtime
// matching remains independent of the total modeled operation count.
func writeRegistryIndexes(out *bytes.Buffer, operations []operation) {
	targets := make([]operation, 0, len(operations))
	queries := make([]operation, 0, len(operations))
	rest := make([]operation, 0, len(operations))
	var rpc []rpcIndexInput
	for _, op := range operations {
		if op.TargetPrefix != "" && (op.Protocol == "AWSJSON10" || op.Protocol == "AWSJSON11") {
			targets = append(targets, op)
		}
		if queryOp, ok := queryIndexEntry(op); ok {
			queries = append(queries, queryOp)
		}
		if (op.Protocol == "RESTJSON" || op.Protocol == "RESTXML") && op.HTTPMethod != "" && op.URI != "" && op.Service != "s3" {
			rest = append(rest, op)
		}
		for _, protocol := range []string{"RPCV2CBOR", "RPCV2JSON"} {
			if slices.Contains(op.Protocols, protocol) {
				rpc = append(rpc, rpcIndexInput{operation: op, protocol: protocol})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		left, right := targets[i].TargetPrefix+targets[i].Name, targets[j].TargetPrefix+targets[j].Name
		if left != right {
			return left < right
		}
		return targets[i].Service < targets[j].Service
	})
	sort.Slice(queries, func(i, j int) bool {
		if queries[i].APIVersion != queries[j].APIVersion {
			return queries[i].APIVersion < queries[j].APIVersion
		}
		if queries[i].Name != queries[j].Name {
			return queries[i].Name < queries[j].Name
		}
		return queries[i].Service < queries[j].Service
	})
	writeTargetIndex(out, targets)
	writeQueryIndex(out, queries)
	writeRESTTrie(out, rest)
	writeRPCIndex(out, rpc)
	writeModelServiceIndex(out, operations)
}

// writeModelServiceIndex emits every modeled service identity, sorted, so the
// registry can answer whether a name is a service at all.
//
// Smithy RPC v2 carries the service in a URI label rather than a header or a
// path prefix, and a label naming no service must not be believed. The other
// indexes answer that question incidentally — a target or an (Action, Version)
// pair either resolves or does not — but a service label has nothing attached
// to it to resolve against.
func writeModelServiceIndex(out *bytes.Buffer, operations []operation) {
	seen := make(map[string]bool, len(operations))
	services := make([]string, 0, 512)
	for _, op := range operations {
		if op.Service == "" || seen[op.Service] {
			continue
		}
		seen[op.Service] = true
		services = append(services, op.Service)
	}
	sort.Strings(services)
	out.WriteString("\nvar modelServices = []string{\n")
	for _, service := range services {
		fmt.Fprintf(out, "\t%q,\n", service)
	}
	out.WriteString("}\n")
}

func writeTargetIndex(out *bytes.Buffer, operations []operation) {
	out.WriteString("\nvar targetOperations = []targetOperation{\n")
	var collisions []registryCollision
	for start := 0; start < len(operations); {
		target := operations[start].TargetPrefix + operations[start].Name
		end := start + 1
		for end < len(operations) && operations[end].TargetPrefix+operations[end].Name == target {
			end++
		}
		op, services := operations[start], collisionServices(operations[start:end])
		ambiguous := len(services) > 1
		if ambiguous {
			op.Service = ""
			collisions = append(collisions, registryCollision{Key: target, Services: services})
			fmt.Fprintf(out, "\t{Target: %q, ModelService: %q, Operation: %q, Protocol: Protocol%s, Ambiguous: true},\n", target, op.Service, op.Name, op.Protocol)
		} else {
			fmt.Fprintf(out, "\t{Target: %q, ModelService: %q, Operation: %q, Protocol: Protocol%s},\n", target, op.Service, op.Name, op.Protocol)
		}
		start = end
	}
	out.WriteString("}\n")
	writeCollisionIndex(out, "targetCollisions", collisions)
}

// overcastQueryServices names the modeled services Overcast answers on the AWS
// Query wire that the pinned models do not declare a Query protocol for.
//
// AWS has been migrating services off Query, and it retires the trait from a
// model well before it stops accepting the wire — SQS's model is awsJson1_0
// only, while Overcast still serves form-encoded Action/Version requests for it
// (internal/services/sqs implements router.QueryDispatcher and declares
// codec.QueryXML in SupportedProtocols). The generated index is derived from
// what AWS publishes, so the fact that *Overcast* answers a wire AWS no longer
// describes cannot be derived at all; it has to be declared, and this is the
// one place it is.
//
// The consequence of an omission is not cosmetic. A Query request whose action
// the index cannot name has no other service signal when it is unsigned — no
// X-Amz-Target, no distinguishing path — so detectService falls through to the
// S3 fallback, and the request is logged, traced and IAM-authorised as s3.
//
// The value is the reason, kept next to the entry so a later reader can tell a
// deliberate declaration from a workaround.
var overcastQueryServices = map[string]string{
	"sqs": "AWS migrated the SQS model to awsJson1_0; the Query wire is still served and still what unsigned clients send",
}

// queryIndexEntry reports whether an operation belongs in the Query index, and
// with which protocol.
//
// Protocols rather than Protocol: a Smithy service can carry the Query trait
// additively alongside a newer primary protocol, which is exactly what
// CloudWatch does (awsJson1_0 primary, awsQuery and rpcv2Cbor additive). Keying
// on the primary alone dropped every one of its 50 operations from the index,
// so an unsigned PutMetricAlarm — what the AWS CLI and the web UI send — was
// classified as s3. The RPC index three lines below has always read the full
// set; this one was the outlier.
func queryIndexEntry(op operation) (operation, bool) {
	// The primary is tested as well as the set, so this is strictly additive
	// against the rule it replaces. modelProtocols puts the primary first in
	// the set, so for anything parsed from a model the first test is redundant
	// — but it is what makes "this cannot drop an operation the index already
	// had" true by reading rather than by trusting that invariant, which is
	// worth one clause in a function that regenerates a committed manifest.
	if op.Protocol == "AWSQuery" || op.Protocol == "EC2Query" ||
		slices.Contains(op.Protocols, "AWSQuery") || slices.Contains(op.Protocols, "EC2Query") {
		return op, true
	}
	if _, declared := overcastQueryServices[op.Service]; declared {
		// The entry describes a Query binding, so it carries the Query
		// protocol regardless of what the model calls the service's primary.
		// ClaimQuery derives the error envelope from this field, and a Query
		// request must be answered with the Query XML envelope.
		op.Protocol = "AWSQuery"
		return op, true
	}
	return operation{}, false
}

func writeQueryIndex(out *bytes.Buffer, operations []operation) {
	out.WriteString("\nvar queryOperations = []queryOperation{\n")
	var collisions []registryCollision
	for start := 0; start < len(operations); {
		key := operations[start].APIVersion + "\x00" + operations[start].Name
		end := start + 1
		for end < len(operations) && operations[end].APIVersion+"\x00"+operations[end].Name == key {
			end++
		}
		op, services := operations[start], collisionServices(operations[start:end])
		ambiguous := len(services) > 1
		if ambiguous {
			op.Service = ""
			collisions = append(collisions, registryCollision{Key: key, Services: services})
			fmt.Fprintf(out, "\t{Version: %q, Operation: %q, ModelService: %q, Protocol: Protocol%s, Ambiguous: true},\n", op.APIVersion, op.Name, op.Service, op.Protocol)
		} else {
			fmt.Fprintf(out, "\t{Version: %q, Operation: %q, ModelService: %q, Protocol: Protocol%s},\n", op.APIVersion, op.Name, op.Service, op.Protocol)
		}
		start = end
	}
	out.WriteString("}\n")
	writeCollisionIndex(out, "queryCollisions", collisions)
}

type registryCollision struct {
	Key      string
	Services []string
}

func collisionServices(operations []operation) []string {
	services := make([]string, 0, len(operations))
	for _, op := range operations {
		if len(services) == 0 || services[len(services)-1] != op.Service {
			services = append(services, op.Service)
		}
	}
	return services
}

func writeCollisionIndex(out *bytes.Buffer, name string, collisions []registryCollision) {
	fmt.Fprintf(out, "\nvar %s = []operationCollision{\n", name)
	for _, collision := range collisions {
		fmt.Fprintf(out, "\t{Key: %q, Services: []string{%s}},\n", collision.Key, quotedStrings(collision.Services))
	}
	out.WriteString("}\n")
}

type rpcIndexInput struct {
	operation
	protocol string
}

func writeRPCIndex(out *bytes.Buffer, operations []rpcIndexInput) {
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].protocol != operations[j].protocol {
			return operations[i].protocol < operations[j].protocol
		}
		if operations[i].ServiceShape != operations[j].ServiceShape {
			return operations[i].ServiceShape < operations[j].ServiceShape
		}
		if operations[i].Name != operations[j].Name {
			return operations[i].Name < operations[j].Name
		}
		return operations[i].Service < operations[j].Service
	})

	out.WriteString("\nvar rpcOperations = []rpcOperation{\n")
	var collisions []registryCollision
	for start := 0; start < len(operations); {
		end := start + 1
		for end < len(operations) &&
			operations[end].protocol == operations[start].protocol &&
			operations[end].ServiceShape == operations[start].ServiceShape &&
			operations[end].Name == operations[start].Name {
			end++
		}
		op := operations[start]
		services := rpcCollisionServices(operations[start:end])
		ambiguous := len(services) > 1
		if ambiguous {
			op.Service = ""
			key := rpcProtocolValue(op.protocol) + "\x00" + op.ServiceShape + "\x00" + op.Name
			collisions = append(collisions, registryCollision{Key: key, Services: services})
			fmt.Fprintf(out, "\t{Protocol: Protocol%s, ServiceShape: %q, Operation: %q, ModelService: %q, Ambiguous: true},\n", op.protocol, op.ServiceShape, op.Name, op.Service)
		} else {
			fmt.Fprintf(out, "\t{Protocol: Protocol%s, ServiceShape: %q, Operation: %q, ModelService: %q},\n", op.protocol, op.ServiceShape, op.Name, op.Service)
		}
		start = end
	}
	out.WriteString("}\n")
	writeCollisionIndex(out, "rpcCollisions", collisions)
}

func rpcCollisionServices(operations []rpcIndexInput) []string {
	services := make([]string, 0, len(operations))
	for _, op := range operations {
		if len(services) == 0 || services[len(services)-1] != op.Service {
			services = append(services, op.Service)
		}
	}
	return services
}

func rpcProtocolValue(protocol string) string {
	switch protocol {
	case "RPCV2CBOR":
		return "rpcv2Cbor"
	case "RPCV2JSON":
		return "rpcv2Json"
	default:
		return ""
	}
}

type restTrieBuildNode struct {
	literals   map[string]*restTrieBuildNode
	parameter  *restTrieBuildNode
	greedy     *restTrieBuildNode
	operations []operation
}

type restIndexOperation struct {
	Method         string
	Query          string
	ModelService   string
	SigningName    string
	Operation      string
	Protocol       string
	Ambiguous      bool
	CandidateStart int
	CandidateEnd   int
}

// restIndexCandidate is one service's operation at an ambiguous REST binding.
// The generated table it feeds is what keeps a shared binding answerable
// without attributing it: the binding entry still names no service, and a
// caller that already knows the service resolves that service's own operation
// from here. Only REST bindings need it — a target, Query or RPC key contains
// the operation name, so colliding services there already share it.
type restIndexCandidate struct {
	ModelService string
	Operation    string
}

func writeRESTTrie(out *bytes.Buffer, operations []operation) {
	root := &restTrieBuildNode{}
	for _, op := range operations {
		node := root
		path, _ := splitRESTURI(op.URI)
		if len(path) > 1 {
			path = strings.TrimSuffix(path, "/")
		}
		trimmedURI := strings.TrimPrefix(path, "/")
		var segments []string
		if trimmedURI != "" {
			segments = strings.Split(trimmedURI, "/")
		}
		for _, segment := range segments {
			if isGreedyLabel(segment) {
				if node.greedy == nil {
					node.greedy = &restTrieBuildNode{}
				}
				node = node.greedy
				continue
			}
			if isLabel(segment) {
				if node.parameter == nil {
					node.parameter = &restTrieBuildNode{}
				}
				node = node.parameter
				continue
			}
			if node.literals == nil {
				node.literals = make(map[string]*restTrieBuildNode)
			}
			if node.literals[segment] == nil {
				node.literals[segment] = &restTrieBuildNode{}
			}
			node = node.literals[segment]
		}
		node.operations = append(node.operations, op)
	}

	type flattenedNode struct {
		node  *restTrieBuildNode
		index int
	}
	flattened := []flattenedNode{{node: root, index: 0}}
	for i := 0; i < len(flattened); i++ {
		node := flattened[i].node
		keys := sortedKeys(node.literals)
		for _, key := range keys {
			flattened = append(flattened, flattenedNode{node: node.literals[key], index: len(flattened)})
		}
		if node.parameter != nil {
			flattened = append(flattened, flattenedNode{node: node.parameter, index: len(flattened)})
		}
		if node.greedy != nil {
			flattened = append(flattened, flattenedNode{node: node.greedy, index: len(flattened)})
		}
	}
	indexes := make(map[*restTrieBuildNode]int, len(flattened))
	for _, entry := range flattened {
		indexes[entry.node] = entry.index
	}

	var edges []struct {
		segment string
		node    int
	}
	var indexedOperations []restIndexOperation
	var candidates []restIndexCandidate
	var collisions []registryCollision
	out.WriteString("\nvar restTrieNodes = []restTrieNode{\n")
	for _, entry := range flattened {
		node := entry.node
		literalStart := len(edges)
		for _, key := range sortedKeys(node.literals) {
			edges = append(edges, struct {
				segment string
				node    int
			}{key, indexes[node.literals[key]]})
		}
		literalEnd := len(edges)
		parameter, greedy := -1, -1
		if node.parameter != nil {
			parameter = indexes[node.parameter]
		}
		if node.greedy != nil {
			greedy = indexes[node.greedy]
		}
		operationStart := len(indexedOperations)
		sort.Slice(node.operations, func(i, j int) bool {
			if node.operations[i].HTTPMethod != node.operations[j].HTTPMethod {
				return node.operations[i].HTTPMethod < node.operations[j].HTTPMethod
			}
			_, leftQuery := splitRESTURI(node.operations[i].URI)
			_, rightQuery := splitRESTURI(node.operations[j].URI)
			if leftQuery != rightQuery {
				// A literal query binding is more specific than the same path
				// and method without one, so emit it first.
				return leftQuery > rightQuery
			}
			if node.operations[i].Service != node.operations[j].Service {
				return node.operations[i].Service < node.operations[j].Service
			}
			return node.operations[i].Name < node.operations[j].Name
		})
		for start := 0; start < len(node.operations); {
			end := start + 1
			_, query := splitRESTURI(node.operations[start].URI)
			for end < len(node.operations) && sameRESTBinding(node.operations[start], node.operations[end]) {
				end++
			}
			op, services := node.operations[start], collisionServices(node.operations[start:end])
			ambiguous := len(services) > 1
			candidateStart, candidateEnd := 0, 0
			if ambiguous {
				op.Service = ""
				op.SigningName = ""
				collisions = append(collisions, registryCollision{Key: normalizedRESTBinding(op), Services: services})
				// Retain what blanking the service throws away: which services
				// declare this binding, and what each of them calls the
				// operation. node.operations is already sorted by service then
				// name within the group, so this window is deterministic and
				// exact duplicates are adjacent.
				candidateStart = len(candidates)
				for _, member := range node.operations[start:end] {
					if n := len(candidates); n > candidateStart && candidates[n-1].ModelService == member.Service && candidates[n-1].Operation == member.Name {
						continue
					}
					candidates = append(candidates, restIndexCandidate{ModelService: member.Service, Operation: member.Name})
				}
				candidateEnd = len(candidates)
			}
			indexedOperations = append(indexedOperations, restIndexOperation{Method: op.HTTPMethod, Query: query, ModelService: op.Service, SigningName: op.SigningName, Operation: op.Name, Protocol: protocolConstant(op.Protocol), Ambiguous: ambiguous, CandidateStart: candidateStart, CandidateEnd: candidateEnd})
			start = end
		}
		fmt.Fprintf(out, "\t{LiteralStart: %d, LiteralEnd: %d, Parameter: %d, Greedy: %d, OperationStart: %d, OperationEnd: %d},\n", literalStart, literalEnd, parameter, greedy, operationStart, len(indexedOperations))
	}
	out.WriteString("}\n\nvar restTrieEdges = []restTrieEdge{\n")
	for _, edge := range edges {
		fmt.Fprintf(out, "\t{Segment: %q, Node: %d},\n", edge.segment, edge.node)
	}
	out.WriteString("}\n\nvar restOperations = []restOperation{\n")
	for _, op := range indexedOperations {
		if op.Ambiguous {
			fmt.Fprintf(out, "\t{Method: %q, Query: %q, ModelService: %q, SigningName: %q, Operation: %q, Protocol: %s, Ambiguous: true, CandidateStart: %d, CandidateEnd: %d},\n", op.Method, op.Query, op.ModelService, op.SigningName, op.Operation, op.Protocol, op.CandidateStart, op.CandidateEnd)
		} else {
			fmt.Fprintf(out, "\t{Method: %q, Query: %q, ModelService: %q, SigningName: %q, Operation: %q, Protocol: %s},\n", op.Method, op.Query, op.ModelService, op.SigningName, op.Operation, op.Protocol)
		}
	}
	out.WriteString("}\n\nvar restCandidates = []restCandidate{\n")
	for _, candidate := range candidates {
		fmt.Fprintf(out, "\t{ModelService: %q, Operation: %q},\n", candidate.ModelService, candidate.Operation)
	}
	out.WriteString("}\n")
	writeCollisionIndex(out, "restCollisions", collisions)
}

func splitRESTURI(uri string) (path, query string) {
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		return uri[:i], uri[i+1:]
	}
	return uri, ""
}

func sameRESTBinding(left, right operation) bool {
	_, leftQuery := splitRESTURI(left.URI)
	_, rightQuery := splitRESTURI(right.URI)
	return left.HTTPMethod == right.HTTPMethod && leftQuery == rightQuery
}

func normalizedRESTBinding(op operation) string {
	path, query := splitRESTURI(op.URI)
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		switch {
		case isGreedyLabel(segment):
			segments[i] = "{+}"
		case isLabel(segment):
			segments[i] = "{}"
		}
	}
	key := op.HTTPMethod + " " + strings.Join(segments, "/")
	if query != "" {
		key += "?" + query
	}
	return key
}

func isLabel(segment string) bool {
	return strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}")
}
func isGreedyLabel(segment string) bool { return isLabel(segment) && strings.HasSuffix(segment, "+}") }

func sortedKeys(values map[string]*restTrieBuildNode) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func protocolConstant(protocol string) string { return "Protocol" + protocol }

func quotedStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}

func protocolSetExpression(protocols []string) string {
	if len(protocols) == 0 {
		return "0"
	}
	sets := make([]string, len(protocols))
	for i, protocol := range protocols {
		sets[i] = "Protocols" + protocol
	}
	return strings.Join(sets, " | ")
}
