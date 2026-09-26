//go:build dev

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsmodel"
)

// TestLoadModel_readsAWSQueryCompatible covers the derivation the scenario
// `client` header depends on: a service that carries
// aws.protocols#awsQueryCompatible also answers with the Query error code in
// the x-amzn-query-error header, and an interpreter is told which of the two
// spellings it may see. testdata/shapes/queryservice.json declares the trait;
// the widgets fixture does not.
func TestLoadModel_readsAWSQueryCompatible(t *testing.T) {
	for _, testCase := range []struct {
		service string
		want    bool
	}{
		{service: "queryservice", want: true},
		{service: "widgets", want: false},
	} {
		t.Run(testCase.service, func(t *testing.T) {
			// Given: a committed-shaped snapshot whose service shape does, or
			// does not, carry the trait.
			// When: the generator loads it.
			model, err := loadModel(filepath.Join("testdata", "shapes"), testCase.service)
			if err != nil {
				t.Fatalf("load %s: %v", testCase.service, err)
			}

			// Then: the derived fact matches what the snapshot declares.
			if model.QueryCompatible != testCase.want {
				t.Fatalf("QueryCompatible for %s = %v, want %v", testCase.service, model.QueryCompatible, testCase.want)
			}
		})
	}
}

// TestReadServiceTraits_endpointPrefixFallsBackToARNNamespace covers the
// one committed snapshot whose aws.api#service trait states no endpointPrefix:
// S3 Tables names only its arnNamespace, and botocore's metadata for it gives
// endpointPrefix "s3tables", the same string. A stated prefix always wins.
func TestReadServiceTraits_endpointPrefixFallsBackToARNNamespace(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		trait string
		want  string
	}{
		{name: "stated", trait: `{"endpointPrefix":"kinesis","arnNamespace":"kinesis","sdkId":"Kinesis"}`, want: "kinesis"},
		{name: "stated differs from arnNamespace", trait: `{"endpointPrefix":"events","arnNamespace":"eventbridge"}`, want: "events"},
		{name: "absent", trait: `{"arnNamespace":"s3tables","sdkId":"S3Tables"}`, want: "s3tables"},
		{name: "neither", trait: `{"sdkId":"Widgets"}`, want: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given: a service shape carrying this aws.api#service trait.
			service := awsmodel.SnapshotShape{Type: "service", Traits: map[string]json.RawMessage{
				"aws.api#service": json.RawMessage(testCase.trait),
			}}

			// When: the generator reads its traits.
			model := &serviceModel{}
			if err := model.readServiceTraits(service); err != nil {
				t.Fatalf("readServiceTraits: %v", err)
			}

			// Then: the endpoint prefix is the stated one, else the arnNamespace.
			if model.EndpointPrefix != testCase.want {
				t.Fatalf("EndpointPrefix = %q, want %q", model.EndpointPrefix, testCase.want)
			}
		})
	}
}

// TestClientInfo_alwaysStatesAWSQueryCompatible holds the reason the field has
// no omitempty: a scenario that simply omitted it would be indistinguishable
// from one written before the field existed, and an interpreter would have to
// guess which error codes to accept.
func TestClientInfo_alwaysStatesAWSQueryCompatible(t *testing.T) {
	// Given: a client header for a service that is not Query-compatible.
	client := clientInfo{SDKID: "Widgets", EndpointPrefix: "widgets", Protocol: "awsJson1_1", APIVersion: "2026-01-01"}

	// When: it is encoded as the scenario file carries it.
	encoded, err := json.Marshal(client)
	if err != nil {
		t.Fatalf("marshal client: %v", err)
	}

	// Then: the field is stated as false rather than dropped.
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal client: %v", err)
	}
	value, stated := decoded["awsQueryCompatible"]
	if !stated {
		t.Fatalf("client header omits awsQueryCompatible: %s", encoded)
	}
	if value != false {
		t.Fatalf("awsQueryCompatible = %v, want false", value)
	}
}

// TestSnapshotServiceFor_resolvesAnOvercastKeyToItsSnapshot covers how an
// authored scenario, which has no recipe `model` field, finds its shapes: its
// `service` is the hand-written registry group's Overcast key, and the snapshot
// is named for the model service that key aliases from. cognito-userpools is
// the first port that needs it (#1116) — the registry says cognito, the
// snapshot is cognito-identity-provider.json.
func TestSnapshotServiceFor_resolvesAnOvercastKeyToItsSnapshot(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		files   []string
		service string
		want    string
		wantErr string
	}{
		{name: "own name", files: []string{"kinesis.json"}, service: "kinesis", want: "kinesis"},
		{name: "aliased model service", files: []string{"cognito-identity-provider.json", "kinesis.json"}, service: "cognito", want: "cognito-identity-provider"},
		{name: "own name wins over an alias", files: []string{"cloudwatch-events.json", "eventbridge.json"}, service: "eventbridge", want: "eventbridge"},
		{name: "nothing committed", files: []string{"kinesis.json"}, service: "cognito", want: "cognito"},
		{name: "two snapshots alias to one key", files: []string{"api-gateway.json", "apigatewayv2.json"}, service: "apigateway", wantErr: "api-gateway, apigatewayv2"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given: a snapshot directory holding exactly these files.
			dir := t.TempDir()
			for _, name := range testCase.files {
				writeFile(t, filepath.Join(dir, name), "{}")
			}

			// When: an authored scenario for the service is planned.
			got, err := snapshotServiceFor(dir, testCase.service)

			// Then: it reads the one snapshot the key names, and refuses to
			// guess between two.
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("snapshotServiceFor(%q) = %q, %v; want an error naming %s", testCase.service, got, err, testCase.wantErr)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("snapshotServiceFor(%q) = %q, %v; want %q", testCase.service, got, err, testCase.want)
			}
		})
	}
}
