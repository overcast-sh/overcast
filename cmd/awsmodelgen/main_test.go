package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOrCheckManifest_checksWithoutOverwriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.gen.go")
	if err := os.WriteFile(path, []byte("current\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeOrCheckManifest(path, []byte("current\n"), true); err != nil {
		t.Fatalf("check current manifest: %v", err)
	}
	if err := writeOrCheckManifest(path, []byte("new\n"), true); err == nil {
		t.Fatal("stale manifest check unexpectedly passed")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "current\n" {
		t.Fatalf("check mode overwrote manifest: %q", contents)
	}

	if err := writeOrCheckManifest(path, []byte("new\n"), false); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestGenerateManifest_extractsServiceOperationsAndProtocols(t *testing.T) {
	// Given: a minimal official-style Smithy JSON AST with JSON and REST services.
	modelsDir := t.TempDir()
	writeModel(t, filepath.Join(modelsDir, "queue.json"), `{
  "smithy":"2.0",
  "shapes":{
    "example.queue#Queue_20260101":{"type":"service","version":"2026-01-01","operations":[{"target":"example.queue#CreateQueue"}],"traits":{"aws.api#service":{"sdkId":"Queue","endpointPrefix":"queue"},"aws.protocols#awsJson1_0":{},"smithy.protocols#rpcv2Cbor":{}}},
    "example.queue#CreateQueue":{"type":"operation"}
  }
}`)
	writeModel(t, filepath.Join(modelsDir, "widget.json"), `{
  "smithy":"2.0",
  "shapes":{
    "example.widget#Widget":{"type":"service","version":"2026-02-02","operations":[{"target":"example.widget#GetWidget"}],"traits":{"aws.api#service":{"sdkId":"Widget","endpointPrefix":"widget"},"aws.protocols#restJson1":{}}},
    "example.widget#GetWidget":{"type":"operation","traits":{"smithy.api#http":{"method":"GET","uri":"/widgets/{id}"}}}
  }
}`)
	writeModel(t, filepath.Join(modelsDir, "resource.json"), `{
  "smithy":"2.0",
  "shapes":{
    "example.resource#ResourceService":{"type":"service","version":"2026-03-03","resources":[{"target":"example.resource#Widget"}],"traits":{"aws.api#service":{"sdkId":"Resource Service","endpointPrefix":"resource"},"aws.protocols#restJson1":{}}},
    "example.resource#Widget":{"type":"resource","operations":[{"target":"example.resource#GetWidget"}]},
    "example.resource#GetWidget":{"type":"operation","traits":{"smithy.api#http":{"method":"GET","uri":"/widgets/{id}"}}}
  }
}`)
	writeModel(t, filepath.Join(modelsDir, "query.json"), `{
  "smithy":"2.0",
  "shapes":{
    "example.query#Query":{"type":"service","version":"2026-04-04","operations":[{"target":"example.query#DescribeWidgets"}],"traits":{"aws.api#service":{"sdkId":"Query","endpointPrefix":"query"},"aws.protocols#ec2Query":{}}},
    "example.query#DescribeWidgets":{"type":"operation"}
  }
}`)

	// When: the generator reads the model directory.
	got, err := generateManifest(modelsDir, "test-revision")
	if err != nil {
		t.Fatal(err)
	}

	// Then: it emits deterministic operation ownership metadata without model I/O at runtime.
	for _, want := range []string{
		`SourceRevision = "test-revision"`,
		`{Service: "queue", ServiceShape: "Queue_20260101", SDKID: "Queue", APIVersion: "2026-01-01", Name: "CreateQueue", Protocol: ProtocolAWSJSON10, Protocols: ProtocolsAWSJSON10 | ProtocolsRPCV2CBOR, TargetPrefix: "Queue_20260101.", HTTPMethod: "", URI: ""}`,
		`{Service: "widget", ServiceShape: "Widget", SDKID: "Widget", APIVersion: "2026-02-02", Name: "GetWidget", Protocol: ProtocolRESTJSON, Protocols: ProtocolsRESTJSON, TargetPrefix: "", HTTPMethod: "GET", URI: "/widgets/{id}"}`,
		`{Service: "resource-service", ServiceShape: "ResourceService", SDKID: "Resource Service", APIVersion: "2026-03-03", Name: "GetWidget", Protocol: ProtocolRESTJSON, Protocols: ProtocolsRESTJSON, TargetPrefix: "", HTTPMethod: "GET", URI: "/widgets/{id}"}`,
		`{Target: "Queue_20260101.CreateQueue", ModelService: "queue", Operation: "CreateQueue", Protocol: ProtocolAWSJSON10}`,
		`{Version: "2026-04-04", Operation: "DescribeWidgets", ModelService: "query", Protocol: ProtocolEC2Query}`,
		`{Method: "GET", Query: "", ModelService: "", SigningName: "", Operation: "GetWidget", Protocol: ProtocolRESTJSON, Ambiguous: true, CandidateStart: 0, CandidateEnd: 2}`,
		`{ModelService: "resource-service", Operation: "GetWidget"}`,
		`{ModelService: "widget", Operation: "GetWidget"}`,
		`{Protocol: ProtocolRPCV2CBOR, ServiceShape: "Queue_20260101", Operation: "CreateQueue", ModelService: "queue"}`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("generated manifest missing %q:\n%s", want, got)
		}
	}
}

func TestWriteRegistryIndexes_marksCollidingServicesAmbiguous(t *testing.T) {
	// Given: two modeled services with the same AWS JSON target and Query key.
	operations := []operation{
		{Service: "alpha", APIVersion: "2026-01-01", Name: "CreateThing", Protocol: "AWSJSON10", TargetPrefix: "Example."},
		{Service: "beta", APIVersion: "2026-01-01", Name: "CreateThing", Protocol: "AWSJSON10", TargetPrefix: "Example."},
		{Service: "alpha", APIVersion: "2026-02-02", Name: "DescribeThing", Protocol: "AWSQuery"},
		{Service: "beta", APIVersion: "2026-02-02", Name: "DescribeThing", Protocol: "AWSQuery"},
		{Service: "alpha", Name: "GetThing", Protocol: "RESTJSON", HTTPMethod: "GET", URI: "/things/{id}"},
		{Service: "beta", Name: "GetThing", Protocol: "RESTJSON", HTTPMethod: "GET", URI: "/things/{thingId}"},
		{Service: "alpha", ServiceShape: "Example", Name: "CreateThing", Protocol: "AWSJSON10", Protocols: []string{"AWSJSON10", "RPCV2CBOR"}},
		{Service: "beta", ServiceShape: "Example", Name: "CreateThing", Protocol: "AWSJSON10", Protocols: []string{"AWSJSON10", "RPCV2CBOR"}},
	}
	var output bytes.Buffer

	// When: the generator writes its immutable lookup indexes.
	writeRegistryIndexes(&output, operations)

	// Then: it retains a collision report and avoids assigning either service
	// identity to the shared wire operation.
	for _, want := range []string{
		`{Target: "Example.CreateThing", ModelService: "", Operation: "CreateThing", Protocol: ProtocolAWSJSON10, Ambiguous: true}`,
		`{Key: "Example.CreateThing", Services: []string{"alpha", "beta"}}`,
		`{Version: "2026-02-02", Operation: "DescribeThing", ModelService: "", Protocol: ProtocolAWSQuery, Ambiguous: true}`,
		`{Key: "2026-02-02\x00DescribeThing", Services: []string{"alpha", "beta"}}`,
		`{Method: "GET", Query: "", ModelService: "", SigningName: "", Operation: "GetThing", Protocol: ProtocolRESTJSON, Ambiguous: true, CandidateStart: 0, CandidateEnd: 2}`,
		// The blanked binding stays unattributed, but every service that
		// declares it is retained with the name that service gives it, so a
		// caller that already knows its service resolves an exact operation.
		"var restCandidates = []restCandidate{\n\t{ModelService: \"alpha\", Operation: \"GetThing\"},\n\t{ModelService: \"beta\", Operation: \"GetThing\"},\n}",
		`var restCollisions = []operationCollision{`,
		`{Key: "GET /things/{}", Services: []string{"alpha", "beta"}}`,
		`{Protocol: ProtocolRPCV2CBOR, ServiceShape: "Example", Operation: "CreateThing", ModelService: "", Ambiguous: true}`,
		`var rpcCollisions = []operationCollision{`,
		`{Key: "rpcv2Cbor\x00Example\x00CreateThing", Services: []string{"alpha", "beta"}}`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("generated registry index missing %q:\n%s", want, output.String())
		}
	}
}

func TestWriteRegistryIndexes_listsNoErrorWrappingServices(t *testing.T) {
	// Given: one rest-xml service that sets noErrorWrapping and one that does not.
	operations := []operation{
		{Service: "wrapped", Name: "GetThing", Protocol: "RESTXML", HTTPMethod: "GET", URI: "/things/{id}"},
		{Service: "bare", Name: "GetObject", Protocol: "RESTXML", HTTPMethod: "GET", URI: "/{Bucket}/{Key+}", NoErrorWrapping: true},
		{Service: "bare", Name: "PutObject", Protocol: "RESTXML", HTTPMethod: "PUT", URI: "/{Bucket}/{Key+}", NoErrorWrapping: true},
	}
	var output bytes.Buffer

	// When: the generator writes its immutable lookup indexes.
	writeRegistryIndexes(&output, operations)

	// Then: only the service that sets the trait is listed, once.
	if want := "var noErrorWrappingServices = []string{\n\t\"bare\",\n}"; !strings.Contains(output.String(), want) {
		t.Errorf("generated registry index missing %q:\n%s", want, output.String())
	}
}

func TestGenerateManifest_rejectsEmptyModels(t *testing.T) {
	if _, err := generateManifest(t.TempDir(), "test-revision"); err == nil {
		t.Fatal("generateManifest() unexpectedly succeeded")
	}
}

func writeModel(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
