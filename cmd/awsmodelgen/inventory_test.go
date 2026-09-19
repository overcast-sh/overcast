package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSummarizeInventoryChanges_reportsRoutingRelevantDrift(t *testing.T) {
	// Given: a pinned model inventory and a newer upstream inventory with
	// service, operation, protocol, binding, collision, and coverage changes.
	baseline := buildModelInventory("old-revision", []operation{
		{Service: "alpha", SDKID: "Alpha", APIVersion: "2026-01-01", Name: "PutThing", Protocol: "AWSJSON10", Protocols: []string{"AWSJSON10"}, TargetPrefix: "Alpha."},
		{Service: "beta", SDKID: "Beta", APIVersion: "2026-01-01", Name: "GetThing", Protocol: "RESTJSON", Protocols: []string{"RESTJSON"}, SigningName: "beta", HTTPMethod: "GET", URI: "/things/{id}"},
		{Service: "gamma", SDKID: "Gamma", APIVersion: "2026-01-01", Name: "GetThing", Protocol: "RESTJSON", Protocols: []string{"RESTJSON"}, SigningName: "gamma", HTTPMethod: "GET", URI: "/things/{name}"},
	})
	current := buildModelInventory("new-revision", []operation{
		{Service: "alpha", SDKID: "Alpha", APIVersion: "2026-01-01", Name: "PutThing", Protocol: "AWSJSON10", Protocols: []string{"AWSJSON10", "RPCV2CBOR"}, TargetPrefix: "Alpha."},
		{Service: "beta", SDKID: "Beta", APIVersion: "2026-01-01", Name: "GetThing", Protocol: "RESTJSON", Protocols: []string{"RESTJSON"}, SigningName: "beta", HTTPMethod: "GET", URI: "/widgets/{id}"},
		{Service: "delta", SDKID: "Delta", APIVersion: "2026-02-02", Name: "ListThings", Protocol: "AWSQuery", Protocols: []string{"AWSQuery"}},
	})
	if baseline.Coverage.ClaimableOperations != 1 || baseline.Coverage.AmbiguousOperations != 2 {
		t.Fatalf("baseline coverage = %+v, want one claimable and two explicitly ambiguous operations", baseline.Coverage)
	}
	if current.Coverage.ClaimableOperations != 3 || current.Coverage.AmbiguousOperations != 0 {
		t.Fatalf("current coverage = %+v, want three claimable operations", current.Coverage)
	}

	// When: the refresh summary compares them.
	got := summarizeInventoryChanges(baseline, current)

	// Then: every routing-relevant dimension is explicit and reviewable.
	for _, want := range []string{
		"`old-revision` -> `new-revision`",
		"Services: **+1 / -1**",
		"`delta`",
		"`gamma`",
		"Operations: **+1 / -1**",
		"`delta/ListThings`",
		"`gamma/GetThing`",
		"Protocol trait changes: **1**",
		"`alpha/PutThing`",
		"HTTP/target binding changes: **1**",
		"`beta/GetThing`",
		"Collision changes: **+0 / -1**",
		"Fallback coverage:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestSummarizeInventoryChanges_isDeterministicAndReportsNoDrift(t *testing.T) {
	inventory := buildModelInventory("same-revision", []operation{
		{Service: "beta", APIVersion: "2026-01-01", Name: "B", Protocol: "AWSQuery", Protocols: []string{"AWSQuery"}},
		{Service: "alpha", APIVersion: "2026-01-01", Name: "A", Protocol: "AWSJSON11", Protocols: []string{"AWSJSON11"}, TargetPrefix: "Alpha."},
	})

	first := summarizeInventoryChanges(inventory, inventory)
	second := summarizeInventoryChanges(inventory, inventory)
	if first != second {
		t.Fatal("summary output is not deterministic")
	}
	if !strings.Contains(first, "No model changes detected.") {
		t.Fatalf("summary did not report no drift:\n%s", first)
	}
}

func TestBuildModelInventory_excludesS3FromFallbackCollisions(t *testing.T) {
	inventory := buildModelInventory("revision", []operation{
		{Service: "s3", Name: "GetObject", Protocol: "RESTXML", Protocols: []string{"RESTXML"}, HTTPMethod: "GET", URI: "/{Bucket}/{Key+}"},
		{Service: "example", Name: "GetItem", Protocol: "RESTXML", Protocols: []string{"RESTXML"}, HTTPMethod: "GET", URI: "/{Collection}/{Item+}"},
	})

	if inventory.Coverage.NonS3Operations != 1 || inventory.Coverage.ClaimableOperations != 1 || inventory.Coverage.AmbiguousOperations != 0 {
		t.Fatalf("fallback coverage = %+v, want the non-S3 binding claimable", inventory.Coverage)
	}
	if len(inventory.Collisions) != 0 {
		t.Fatalf("S3 created a fallback collision: %+v", inventory.Collisions)
	}
}

func TestUpdateModelVersion_preservesProvenanceAndUpdatesPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "VERSION")
	const original = "source=https://github.com/aws/api-models-aws\nrevision=old\nmodel-date=2026-01-01\nmanifest-sha256=old-digest\nshapes-sha256=old-shapes\nsdk-shapes-sha256=old-sdk-shapes\nlicense=Apache-2.0\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := updateModelVersion(path, "new", "2026-07-28", "new-digest", "new-shapes", "new-sdk-shapes"); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(contents)
	for _, want := range []string{
		"source=https://github.com/aws/api-models-aws",
		"revision=new",
		"model-date=2026-07-28",
		"manifest-sha256=new-digest",
		"shapes-sha256=new-shapes",
		"sdk-shapes-sha256=new-sdk-shapes",
		"license=Apache-2.0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("updated VERSION missing %q:\n%s", want, got)
		}
	}
}
