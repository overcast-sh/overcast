package awsmodel

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestTargetPrefixForService_usesKnownLegacyOverride(t *testing.T) {
	protocols := []string{"AWSJSON11"}
	if got, want := targetPrefixForService("com.amazonaws.cloudtrail#CloudTrail_20131101", protocols), "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101."; got != want {
		t.Errorf("targetPrefixForService() = %q, want %q", got, want)
	}
}

func TestTargetPrefixForService_ignoresNonJSONProtocols(t *testing.T) {
	if got := targetPrefixForService("example.widget#Widget", []string{"RESTJSON"}); got != "" {
		t.Errorf("targetPrefixForService() = %q, want empty", got)
	}
}

func TestModelProtocol_hasStablePrecedence(t *testing.T) {
	// Given: a service that carries more than one recognized protocol trait.
	traits := map[string]json.RawMessage{
		"aws.protocols#restJson1":  {},
		"aws.protocols#awsJson1_1": {},
	}

	// Then: the canonical protocol is selected deterministically.
	if got, want := modelProtocol(traits), "AWSJSON11"; got != want {
		t.Errorf("modelProtocol() = %q, want %q", got, want)
	}
}

func TestModelProtocol_recognizesSmithyRPCV2Protocols(t *testing.T) {
	for trait, want := range map[string]string{
		"smithy.protocols#rpcv2Cbor": "RPCV2CBOR",
		"smithy.protocols#rpcv2Json": "RPCV2JSON",
	} {
		if got := modelProtocol(map[string]json.RawMessage{trait: {}}); got != want {
			t.Errorf("modelProtocol(%q) = %q, want %q", trait, got, want)
		}
	}
}

func TestModelProtocols_preservesAdditiveTraits(t *testing.T) {
	traits := map[string]json.RawMessage{
		"aws.protocols#awsJson1_1":   {},
		"smithy.protocols#rpcv2Cbor": {},
	}

	if got, want := ModelProtocols(traits), []string{"AWSJSON11", "RPCV2CBOR"}; !slices.Equal(got, want) {
		t.Errorf("ModelProtocols() = %v, want %v", got, want)
	}
}

func TestRESTXMLNoErrorWrapping_readsTheRestXmlTrait(t *testing.T) {
	for name, tc := range map[string]struct {
		traits map[string]json.RawMessage
		want   bool
	}{
		"S3's trait":         {map[string]json.RawMessage{"aws.protocols#restXml": json.RawMessage(`{"noErrorWrapping": true}`)}, true},
		"CloudFront's trait": {map[string]json.RawMessage{"aws.protocols#restXml": json.RawMessage(`{}`)}, false},
		"another protocol":   {map[string]json.RawMessage{"aws.protocols#restJson1": json.RawMessage(`{"noErrorWrapping": true}`)}, false},
	} {
		if got := restXMLNoErrorWrapping(tc.traits); got != tc.want {
			t.Errorf("%s: restXMLNoErrorWrapping() = %v, want %v", name, got, tc.want)
		}
	}
}

func TestShapeName_takesSuffixAfterHash(t *testing.T) {
	if got, want := ShapeName("com.amazonaws.example#Widget"), "Widget"; got != want {
		t.Errorf("ShapeName() = %q, want %q", got, want)
	}
}
