package awsmodel

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// model is the root of a Smithy JSON AST file: a flat map of every shape it
// declares, keyed by absolute shape ID.
type model struct {
	Shapes map[string]shape `json:"shapes"`
}

// shape is the subset of Smithy shape fields awsmodel reads: enough to walk
// service and resource shapes to their operations, and to read the traits
// that carry protocol, signing, and HTTP binding information.
type shape struct {
	Type                 string                     `json:"type"`
	Version              string                     `json:"version"`
	Operations           []Reference                `json:"operations"`
	CollectionOperations []Reference                `json:"collectionOperations"`
	Resources            []Reference                `json:"resources"`
	Create               *Reference                 `json:"create"`
	Put                  *Reference                 `json:"put"`
	Read                 *Reference                 `json:"read"`
	Update               *Reference                 `json:"update"`
	Delete               *Reference                 `json:"delete"`
	List                 *Reference                 `json:"list"`
	Traits               map[string]json.RawMessage `json:"traits"`
}

type Reference struct {
	Target string `json:"target"`
}

type ServiceTrait struct {
	SDKID string `json:"sdkId"`
}

type sigV4Trait struct {
	Name string `json:"name"`
}

// restXMLTrait is the aws.protocols#restXml trait's one field awsmodel reads.
type restXMLTrait struct {
	NoErrorWrapping bool `json:"noErrorWrapping"`
}

type httpTrait struct {
	Method string `json:"method"`
	URI    string `json:"uri"`
}

// ParseModel parses a single Smithy JSON AST file and returns the operations
// declared by every AWS service shape it contains — both those the service
// references directly and those reached through resource lifecycle bindings.
func ParseModel(path string) ([]Operation, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed model
	if err := json.Unmarshal(contents, &parsed); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var out []Operation
	for shapeID, svc := range parsed.Shapes {
		if svc.Type != "service" {
			continue
		}
		rawTrait, ok := svc.Traits["aws.api#service"]
		if !ok {
			return nil, fmt.Errorf("%s: service %s has no aws.api#service trait", path, shapeID)
		}
		var trait ServiceTrait
		if err := json.Unmarshal(rawTrait, &trait); err != nil {
			return nil, fmt.Errorf("parse service trait in %s: %w", path, err)
		}
		protocols := ModelProtocols(svc.Traits)
		protocol := modelProtocol(svc.Traits)
		targetPrefix := targetPrefixForService(shapeID, protocols)
		signingName := signingNameForService(svc.Traits)
		noErrorWrapping := restXMLNoErrorWrapping(svc.Traits)
		refs, err := serviceOperationReferences(parsed.Shapes, shapeID, svc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, ref := range refs {
			op, ok := parsed.Shapes[ref.Target]
			if !ok || op.Type != "operation" {
				return nil, fmt.Errorf("%s: service %s references missing operation %s", path, shapeID, ref.Target)
			}
			var http httpTrait
			if raw, ok := op.Traits["smithy.api#http"]; ok {
				if err := json.Unmarshal(raw, &http); err != nil {
					return nil, fmt.Errorf("parse HTTP trait for %s: %w", ref.Target, err)
				}
			}
			out = append(out, Operation{
				Service: strings.ToLower(strings.ReplaceAll(trait.SDKID, " ", "-")), ServiceShape: ShapeName(shapeID), SDKID: trait.SDKID,
				APIVersion: svc.Version, Name: ShapeName(ref.Target), Protocol: protocol, Protocols: protocols,
				TargetPrefix: targetPrefix, SigningName: signingName, HTTPMethod: http.Method, URI: http.URI,
				NoErrorWrapping: noErrorWrapping,
			})
		}
	}
	return out, nil
}

func signingNameForService(traits map[string]json.RawMessage) string {
	raw, ok := traits["aws.auth#sigv4"]
	if !ok {
		return ""
	}
	var trait sigV4Trait
	if err := json.Unmarshal(raw, &trait); err != nil {
		return ""
	}
	return trait.Name
}

// restXMLNoErrorWrapping reports whether a service's restXml protocol trait
// sets noErrorWrapping: its errors are a bare <Error> rather than the
// <ErrorResponse><Error> the protocol otherwise wraps them in. S3 is the one
// model that sets it.
func restXMLNoErrorWrapping(traits map[string]json.RawMessage) bool {
	raw, ok := traits["aws.protocols#restXml"]
	if !ok {
		return false
	}
	var trait restXMLTrait
	if err := json.Unmarshal(raw, &trait); err != nil {
		return false
	}
	return trait.NoErrorWrapping
}

// serviceOperationReferences returns every operation a service shape reaches,
// combining the operations it declares directly with those reached by
// walking its resource tree (including nested resources and resource
// lifecycle bindings: create, put, read, update, delete, list).
func serviceOperationReferences(shapes map[string]shape, serviceID string, service shape) ([]Reference, error) {
	refs := append([]Reference(nil), service.Operations...)
	seenOperations := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		seenOperations[ref.Target] = struct{}{}
	}
	visitedResources := make(map[string]struct{})
	var visitResource func(Reference) error
	visitResource = func(ref Reference) error {
		if _, seen := visitedResources[ref.Target]; seen {
			return nil
		}
		visitedResources[ref.Target] = struct{}{}
		resource, ok := shapes[ref.Target]
		if !ok || resource.Type != "resource" {
			return fmt.Errorf("service %s references missing resource %s", serviceID, ref.Target)
		}
		operations := append([]Reference{}, resource.Operations...)
		operations = append(operations, resource.CollectionOperations...)
		for _, lifecycle := range []*Reference{resource.Create, resource.Put, resource.Read, resource.Update, resource.Delete, resource.List} {
			if lifecycle != nil {
				operations = append(operations, *lifecycle)
			}
		}
		for _, operation := range operations {
			if _, seen := seenOperations[operation.Target]; !seen {
				seenOperations[operation.Target] = struct{}{}
				refs = append(refs, operation)
			}
		}
		for _, nested := range resource.Resources {
			if err := visitResource(nested); err != nil {
				return err
			}
		}
		return nil
	}
	for _, resource := range service.Resources {
		if err := visitResource(resource); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func modelProtocol(traits map[string]json.RawMessage) string {
	protocols := ModelProtocols(traits)
	if len(protocols) == 0 {
		return "Unknown"
	}
	return protocols[0]
}

func ModelProtocols(traits map[string]json.RawMessage) []string {
	// Smithy permits protocol extension traits to coexist. Keep this ordered so
	// generated output is reproducible even if a model exposes more than one
	// recognized protocol. The registry phase will retain the complete trait set
	// when protocol negotiation needs it.
	var protocols []string
	for _, candidate := range []struct {
		trait    string
		protocol string
	}{
		{"aws.protocols#awsJson1_1", "AWSJSON11"},
		{"aws.protocols#awsJson1_0", "AWSJSON10"},
		{"aws.protocols#ec2Query", "EC2Query"},
		{"aws.protocols#awsQuery", "AWSQuery"},
		{"aws.protocols#restJson1", "RESTJSON"},
		{"aws.protocols#restXml", "RESTXML"},
		{"smithy.protocols#rpcv2Cbor", "RPCV2CBOR"},
		{"smithy.protocols#rpcv2Json", "RPCV2JSON"},
	} {
		if _, ok := traits[candidate.trait]; ok {
			protocols = append(protocols, candidate.protocol)
		}
	}
	return protocols
}

func hasAWSJSONProtocol(protocols []string) bool {
	return slices.Contains(protocols, "AWSJSON10") || slices.Contains(protocols, "AWSJSON11")
}

// targetPrefixOverrides covers AWS JSON targets whose legacy wire prefix cannot
// be reconstructed from the public Smithy service shape name alone.
var targetPrefixOverrides = map[string]string{
	"com.amazonaws.cloudtrail#CloudTrail_20131101": "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.",
}

func targetPrefixForService(shapeID string, protocols []string) string {
	if !hasAWSJSONProtocol(protocols) {
		return ""
	}
	if prefix, ok := targetPrefixOverrides[shapeID]; ok {
		return prefix
	}
	return ShapeName(shapeID) + "."
}

func ShapeName(id string) string { return id[strings.LastIndex(id, "#")+1:] }
