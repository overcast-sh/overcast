package kms_test

import (
	"net/http"
	"testing"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// Per the AWS API Reference (https://docs.aws.amazon.com/kms/latest/APIReference/API_KeyMetadata.html),
// KeyMetadata — the response element of CreateKey and DescribeKey — carries
// AWSAccountId, "the twelve-digit account ID of the AWS account that owns the
// KMS key", and the deprecated CustomerMasterKeySpec, of which the reference
// says "The KeySpec and CustomerMasterKeySpec fields have the same value"
// (#1906). The assertions decode into a generic map so that a missing member
// reads as missing rather than as a zero value.

const keyMetadataTestAccount = "123456789012"

// keyMetadataOf returns the KeyMetadata object of a JSON 1.1 response body.
func keyMetadataOf(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out map[string]any
	decodeJSON(t, resp, &out)
	meta, ok := out["KeyMetadata"].(map[string]any)
	if !ok {
		t.Fatalf("response has no KeyMetadata object: %v", out)
	}
	return meta
}

// assertAccountAndDeprecatedKeySpec checks the two members #1906 found missing.
func assertAccountAndDeprecatedKeySpec(t *testing.T, op string, meta map[string]any, wantKeySpec string) {
	t.Helper()
	if got, ok := meta["AWSAccountId"]; !ok {
		t.Errorf("%s: KeyMetadata.AWSAccountId missing; metadata = %v", op, meta)
	} else if got != keyMetadataTestAccount {
		t.Errorf("%s: KeyMetadata.AWSAccountId = %v, want %q", op, got, keyMetadataTestAccount)
	}
	if got, ok := meta["CustomerMasterKeySpec"]; !ok {
		t.Errorf("%s: KeyMetadata.CustomerMasterKeySpec missing; metadata = %v", op, meta)
	} else if got != wantKeySpec {
		t.Errorf("%s: KeyMetadata.CustomerMasterKeySpec = %v, want %q (equal to KeySpec)", op, got, wantKeySpec)
	}
	if got := meta["KeySpec"]; got != wantKeySpec {
		t.Errorf("%s: KeyMetadata.KeySpec = %v, want %q", op, got, wantKeySpec)
	}
}

func TestKeyMetadata_accountIDAndCustomerMasterKeySpec(t *testing.T) {
	cases := []struct {
		name     string
		keySpec  string
		keyUsage string
	}{
		{name: "symmetric", keySpec: "SYMMETRIC_DEFAULT", keyUsage: "ENCRYPT_DECRYPT"},
		{name: "rsa signing", keySpec: "RSA_2048", keyUsage: "SIGN_VERIFY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an account other than the default one
			srv := helpers.NewTestServer(t, helpers.WithAccountID(keyMetadataTestAccount))

			// When: a key is created and then described
			created := keyMetadataOf(t, kmsCall(t, srv, "CreateKey", map[string]any{
				"KeySpec":  tc.keySpec,
				"KeyUsage": tc.keyUsage,
			}))
			keyID, _ := created["KeyId"].(string)
			if keyID == "" {
				t.Fatalf("CreateKey returned no KeyId: %v", created)
			}
			described := keyMetadataOf(t, kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID}))

			// Then: both responses name the owning account and repeat KeySpec
			// under its deprecated name
			assertAccountAndDeprecatedKeySpec(t, "CreateKey", created, tc.keySpec)
			assertAccountAndDeprecatedKeySpec(t, "DescribeKey", described, tc.keySpec)
		})
	}
}

func TestRPCv2CBOR_DescribeKey_accountIDAndCustomerMasterKeySpec(t *testing.T) {
	// Given: a key created in an account other than the default one
	srv := helpers.NewTestServer(t, helpers.WithAccountID(keyMetadataTestAccount))
	keyID := createKeyCBOR(t, srv, "cbor-metadata")

	// When: the key is described over Smithy RPC v2 CBOR
	resp := kmsCBORCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()

	// Then: the CBOR KeyMetadata carries the same two members
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata map[string]any `cbor:"KeyMetadata"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode CBOR DescribeKey response: %v", err)
	}
	assertAccountAndDeprecatedKeySpec(t, "DescribeKey (CBOR)", out.KeyMetadata, "SYMMETRIC_DEFAULT")
}
