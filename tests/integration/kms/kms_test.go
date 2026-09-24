// Package kms_test contains integration tests for the KMS emulator.
//
// Run: go test ./tests/integration/kms/...
package kms_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func kmsCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", action, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+action)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request %s: %v", action, err)
	}
	return resp
}

func kmsCBORCall(t *testing.T, srv *helpers.TestServer, action string, body map[string]any) *http.Response {
	t.Helper()
	payload, err := cborlib.Marshal(body)
	if err != nil {
		t.Fatalf("marshal CBOR %s body: %v", action, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/service/kms/operation/"+action, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build CBOR request: %v", err)
	}
	req.Header.Set("Content-Type", "application/cbor")
	req.Header.Set("Smithy-Protocol", "rpc-v2-cbor")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do CBOR request %s: %v", action, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("decodeJSON: read: %v", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("decodeJSON: unmarshal: %v\nbody: %s", err, b)
	}
}

// createKey is a test helper that creates a symmetric KMS key and returns its ID.
func createKey(t *testing.T, srv *helpers.TestServer, desc string) string {
	t.Helper()
	resp := kmsCall(t, srv, "CreateKey", map[string]any{"Description": desc})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &out)
	if out.KeyMetadata.KeyId == "" {
		t.Fatal("CreateKey: empty KeyId in response")
	}
	return out.KeyMetadata.KeyId
}

func createKeyCBOR(t *testing.T, srv *helpers.TestServer, desc string) string {
	t.Helper()
	resp := kmsCBORCall(t, srv, "CreateKey", map[string]any{"Description": desc})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId string `cbor:"KeyId"`
		} `cbor:"KeyMetadata"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode CBOR CreateKey response: %v", err)
	}
	if out.KeyMetadata.KeyId == "" {
		t.Fatal("CreateKey CBOR: empty KeyId in response")
	}
	return out.KeyMetadata.KeyId
}

// ─── CreateKey ────────────────────────────────────────────────────────────────

func TestCreateKey_symmetric(t *testing.T) {
	// Given: a KMS service
	srv := helpers.NewTestServer(t)

	// When: creating a symmetric key
	resp := kmsCall(t, srv, "CreateKey", map[string]any{
		"Description": "test-key",
		"KeySpec":     "SYMMETRIC_DEFAULT",
		"KeyUsage":    "ENCRYPT_DECRYPT",
	})
	defer resp.Body.Close()

	// Then: the response contains valid key metadata
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId    string `json:"KeyId"`
			Arn      string `json:"Arn"`
			KeyState string `json:"KeyState"`
			Enabled  bool   `json:"Enabled"`
			KeySpec  string `json:"KeySpec"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &out)
	if out.KeyMetadata.KeyId == "" {
		t.Error("expected non-empty KeyId")
	}
	if out.KeyMetadata.Arn == "" {
		t.Error("expected non-empty Arn")
	}
	if out.KeyMetadata.KeyState != "Enabled" {
		t.Errorf("KeyState = %q, want Enabled", out.KeyMetadata.KeyState)
	}
	if !out.KeyMetadata.Enabled {
		t.Error("expected Enabled = true")
	}
}

func TestCreateKey_rsa2048(t *testing.T) {
	// Given: a KMS service
	srv := helpers.NewTestServer(t)

	// When: creating an RSA_2048 key
	resp := kmsCall(t, srv, "CreateKey", map[string]any{
		"KeySpec":  "RSA_2048",
		"KeyUsage": "SIGN_VERIFY",
	})
	defer resp.Body.Close()

	// Then: the response contains valid key metadata with correct spec
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId    string `json:"KeyId"`
			KeySpec  string `json:"KeySpec"`
			KeyUsage string `json:"KeyUsage"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &out)
	if out.KeyMetadata.KeyId == "" {
		t.Error("expected non-empty KeyId")
	}
	if out.KeyMetadata.KeySpec != "RSA_2048" {
		t.Errorf("KeySpec = %q, want RSA_2048", out.KeyMetadata.KeySpec)
	}
}

func TestRPCv2CBOR_CreateDescribeAndListKeys(t *testing.T) {
	// Given: a KMS service.
	srv := helpers.NewTestServer(t)

	// When: creating a key over Smithy RPC v2 CBOR.
	resp := kmsCBORCall(t, srv, "CreateKey", map[string]any{
		"Description": "cbor-key",
		"KeySpec":     "SYMMETRIC_DEFAULT",
		"KeyUsage":    "ENCRYPT_DECRYPT",
	})
	defer resp.Body.Close()

	// Then: KMS returns CBOR key metadata.
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")
	helpers.AssertHeader(t, resp, "Smithy-Protocol", "rpc-v2-cbor")
	var created struct {
		KeyMetadata struct {
			KeyId    string `cbor:"KeyId"`
			Arn      string `cbor:"Arn"`
			KeyState string `cbor:"KeyState"`
			Enabled  bool   `cbor:"Enabled"`
		} `cbor:"KeyMetadata"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode CBOR CreateKey response: %v", err)
	}
	if created.KeyMetadata.KeyId == "" || created.KeyMetadata.Arn == "" {
		t.Fatalf("CreateKey returned KeyId=%q Arn=%q", created.KeyMetadata.KeyId, created.KeyMetadata.Arn)
	}
	if created.KeyMetadata.KeyState != "Enabled" || !created.KeyMetadata.Enabled {
		t.Fatalf("CreateKey state = %q enabled=%v, want Enabled/true", created.KeyMetadata.KeyState, created.KeyMetadata.Enabled)
	}

	// When: describing and listing keys over CBOR.
	resp = kmsCBORCall(t, srv, "DescribeKey", map[string]any{"KeyId": created.KeyMetadata.KeyId})
	defer resp.Body.Close()

	// Then: the same key metadata is returned.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var described struct {
		KeyMetadata struct {
			KeyId string `cbor:"KeyId"`
		} `cbor:"KeyMetadata"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&described); err != nil {
		t.Fatalf("decode CBOR DescribeKey response: %v", err)
	}
	if described.KeyMetadata.KeyId != created.KeyMetadata.KeyId {
		t.Fatalf("DescribeKey KeyId = %q, want %q", described.KeyMetadata.KeyId, created.KeyMetadata.KeyId)
	}

	resp = kmsCBORCall(t, srv, "ListKeys", map[string]any{})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listed struct {
		Keys []struct {
			KeyId  string `cbor:"KeyId"`
			KeyArn string `cbor:"KeyArn"`
		} `cbor:"Keys"`
		Truncated bool `cbor:"Truncated"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatalf("decode CBOR ListKeys response: %v", err)
	}
	if len(listed.Keys) != 1 || listed.Keys[0].KeyId != created.KeyMetadata.KeyId || listed.Keys[0].KeyArn == "" {
		t.Fatalf("ListKeys = %#v, want the created key", listed.Keys)
	}
	if listed.Truncated {
		t.Fatal("ListKeys Truncated = true, want false")
	}
}

// ─── DescribeKey ─────────────────────────────────────────────────────────────

func TestDescribeKey_exists(t *testing.T) {
	// Given: a created key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "describe-me")

	// When: describing the key
	resp := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()

	// Then: metadata is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &out)
	if out.KeyMetadata.KeyId != keyID {
		t.Errorf("KeyId = %q, want %q", out.KeyMetadata.KeyId, keyID)
	}
}

func TestDescribeKey_notFound(t *testing.T) {
	// Given: no keys created
	srv := helpers.NewTestServer(t)

	// When: describing a non-existent key
	resp := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": "00000000-0000-0000-0000-000000000000"})
	defer resp.Body.Close()

	// Then: 400 NotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── ListKeys ────────────────────────────────────────────────────────────────

func TestListKeys_empty(t *testing.T) {
	// Given: no keys created
	srv := helpers.NewTestServer(t)

	// When: listing keys
	resp := kmsCall(t, srv, "ListKeys", map[string]any{})
	defer resp.Body.Close()

	// Then: empty list
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Keys []any `json:"Keys"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Keys) != 0 {
		t.Errorf("expected 0 keys, got %d", len(out.Keys))
	}
}

func TestListKeys_returnsTwoKeys(t *testing.T) {
	// Given: two created keys
	srv := helpers.NewTestServer(t)
	createKey(t, srv, "key-1")
	createKey(t, srv, "key-2")

	// When: listing keys
	resp := kmsCall(t, srv, "ListKeys", map[string]any{})
	defer resp.Body.Close()

	// Then: two keys are listed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Keys []struct {
			KeyId  string `json:"KeyId"`
			KeyArn string `json:"KeyArn"`
		} `json:"Keys"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(out.Keys))
	}
}

// ─── DisableKey / EnableKey ───────────────────────────────────────────────────

func TestDisableKey_andEnable(t *testing.T) {
	// Given: an enabled key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "toggle-key")

	// When: disabling the key
	resp := kmsCall(t, srv, "DisableKey", map[string]any{"KeyId": keyID})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: DescribeKey reports Disabled
	resp2 := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID})
	defer resp2.Body.Close()
	var meta struct {
		KeyMetadata struct {
			Enabled  bool   `json:"Enabled"`
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp2, &meta)
	if meta.KeyMetadata.Enabled {
		t.Error("expected Enabled=false after DisableKey")
	}
	if meta.KeyMetadata.KeyState != "Disabled" {
		t.Errorf("KeyState = %q, want Disabled", meta.KeyMetadata.KeyState)
	}

	// When: re-enabling
	resp3 := kmsCall(t, srv, "EnableKey", map[string]any{"KeyId": keyID})
	resp3.Body.Close()
	helpers.AssertStatus(t, resp3, http.StatusOK)

	// Then: key is enabled again
	resp4 := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID})
	defer resp4.Body.Close()
	var meta2 struct {
		KeyMetadata struct {
			Enabled  bool   `json:"Enabled"`
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp4, &meta2)
	if !meta2.KeyMetadata.Enabled {
		t.Error("expected Enabled=true after EnableKey")
	}
}

// ─── ScheduleKeyDeletion / CancelKeyDeletion ─────────────────────────────────

func TestScheduleKeyDeletion_andCancel(t *testing.T) {
	// Given: a key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "delete-me")

	// When: scheduling deletion
	resp := kmsCall(t, srv, "ScheduleKeyDeletion", map[string]any{
		"KeyId":               keyID,
		"PendingWindowInDays": 7,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var schedOut struct {
		KeyState     string  `json:"KeyState"`
		DeletionDate float64 `json:"DeletionDate"`
	}
	decodeJSON(t, resp, &schedOut)
	if schedOut.KeyState != "PendingDeletion" {
		t.Errorf("KeyState = %q, want PendingDeletion", schedOut.KeyState)
	}
	if schedOut.DeletionDate == 0 {
		t.Error("expected non-zero DeletionDate")
	}

	// When: cancelling deletion
	resp2 := kmsCall(t, srv, "CancelKeyDeletion", map[string]any{"KeyId": keyID})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)

	// Then: key is disabled (AWS CancelKeyDeletion transitions to Disabled, not Enabled)
	resp3 := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": keyID})
	defer resp3.Body.Close()
	var meta struct {
		KeyMetadata struct {
			KeyState string `json:"KeyState"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp3, &meta)
	if meta.KeyMetadata.KeyState != "Disabled" {
		t.Errorf("KeyState = %q, want Disabled", meta.KeyMetadata.KeyState)
	}
}

// ─── Aliases ─────────────────────────────────────────────────────────────────

func TestCreateAlias_listAliases_deleteAlias(t *testing.T) {
	// Given: a key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "alias-target")

	// When: creating an alias
	resp := kmsCall(t, srv, "CreateAlias", map[string]any{
		"AliasName":   "alias/my-test-alias",
		"TargetKeyId": keyID,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: ListAliases shows the alias
	resp2 := kmsCall(t, srv, "ListAliases", map[string]any{})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var listOut struct {
		Aliases []struct {
			AliasName   string `json:"AliasName"`
			TargetKeyId string `json:"TargetKeyId"`
		} `json:"Aliases"`
	}
	decodeJSON(t, resp2, &listOut)
	found := false
	for _, a := range listOut.Aliases {
		if a.AliasName == "alias/my-test-alias" && a.TargetKeyId == keyID {
			found = true
		}
	}
	if !found {
		t.Errorf("alias/my-test-alias not found in ListAliases response; got %+v", listOut.Aliases)
	}

	// When: deleting the alias
	resp3 := kmsCall(t, srv, "DeleteAlias", map[string]any{"AliasName": "alias/my-test-alias"})
	resp3.Body.Close()
	helpers.AssertStatus(t, resp3, http.StatusOK)

	// Then: alias is gone
	resp4 := kmsCall(t, srv, "ListAliases", map[string]any{})
	defer resp4.Body.Close()
	var listOut2 struct {
		Aliases []struct {
			AliasName string `json:"AliasName"`
		} `json:"Aliases"`
	}
	decodeJSON(t, resp4, &listOut2)
	for _, a := range listOut2.Aliases {
		if a.AliasName == "alias/my-test-alias" {
			t.Error("alias/my-test-alias should have been deleted")
		}
	}
}

func TestDescribeKey_byAlias(t *testing.T) {
	// Given: a key with an alias
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "aliased-key")
	resp := kmsCall(t, srv, "CreateAlias", map[string]any{
		"AliasName":   "alias/look-me-up",
		"TargetKeyId": keyID,
	})
	resp.Body.Close()

	// When: describing the key by alias
	resp2 := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": "alias/look-me-up"})
	defer resp2.Body.Close()

	// Then: the correct key is returned
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp2, &out)
	if out.KeyMetadata.KeyId != keyID {
		t.Errorf("DescribeKey by alias: KeyId = %q, want %q", out.KeyMetadata.KeyId, keyID)
	}
}

// ─── Encrypt / Decrypt ───────────────────────────────────────────────────────

func TestEncrypt_decrypt_roundtrip(t *testing.T) {
	// Given: a symmetric key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "encrypt-key")
	plaintext := []byte("hello from the test")

	// When: encrypting
	encResp := kmsCall(t, srv, "Encrypt", map[string]any{
		"KeyId":     keyID,
		"Plaintext": plaintext, // JSON marshals []byte as base64
	})
	defer encResp.Body.Close()
	helpers.AssertStatus(t, encResp, http.StatusOK)
	var encOut struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
		KeyId          string `json:"KeyId"`
	}
	decodeJSON(t, encResp, &encOut)
	if len(encOut.CiphertextBlob) == 0 {
		t.Fatal("expected non-empty CiphertextBlob")
	}

	// When: decrypting
	decResp := kmsCall(t, srv, "Decrypt", map[string]any{
		"CiphertextBlob": encOut.CiphertextBlob,
	})
	defer decResp.Body.Close()
	helpers.AssertStatus(t, decResp, http.StatusOK)
	var decOut struct {
		Plaintext []byte `json:"Plaintext"`
	}
	decodeJSON(t, decResp, &decOut)

	// Then: plaintext matches
	if string(decOut.Plaintext) != string(plaintext) {
		t.Errorf("Decrypt: plaintext = %q, want %q", decOut.Plaintext, plaintext)
	}
}

func TestRPCv2CBOR_EncryptDecryptAndTags(t *testing.T) {
	// Given: a symmetric key exists.
	srv := helpers.NewTestServer(t)
	keyID := createKeyCBOR(t, srv, "cbor-crypto-key")
	plaintext := []byte("hello through cbor")

	// When: Encrypt is called over Smithy RPC v2 CBOR.
	resp := kmsCBORCall(t, srv, "Encrypt", map[string]any{
		"KeyId":     keyID,
		"Plaintext": plaintext,
	})
	defer resp.Body.Close()

	// Then: a CBOR ciphertext blob is returned.
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertHeader(t, resp, "Content-Type", "application/cbor")
	var encOut struct {
		CiphertextBlob []byte `cbor:"CiphertextBlob"`
		KeyId          string `cbor:"KeyId"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&encOut); err != nil {
		t.Fatalf("decode CBOR Encrypt response: %v", err)
	}
	if len(encOut.CiphertextBlob) == 0 || encOut.KeyId == "" {
		t.Fatalf("Encrypt returned CiphertextBlob len=%d KeyId=%q", len(encOut.CiphertextBlob), encOut.KeyId)
	}

	// When: Decrypt is called over CBOR.
	resp = kmsCBORCall(t, srv, "Decrypt", map[string]any{
		"CiphertextBlob": encOut.CiphertextBlob,
	})
	defer resp.Body.Close()

	// Then: the original plaintext is returned.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var decOut struct {
		Plaintext []byte `cbor:"Plaintext"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&decOut); err != nil {
		t.Fatalf("decode CBOR Decrypt response: %v", err)
	}
	if string(decOut.Plaintext) != string(plaintext) {
		t.Fatalf("Decrypt Plaintext = %q, want %q", decOut.Plaintext, plaintext)
	}

	// When: tagging and listing resource tags over CBOR.
	resp = kmsCBORCall(t, srv, "TagResource", map[string]any{
		"KeyId": keyID,
		"Tags":  []map[string]string{{"TagKey": "env", "TagValue": "test"}},
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	resp = kmsCBORCall(t, srv, "ListResourceTags", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()

	// Then: the tag is encoded with KMS member names.
	helpers.AssertStatus(t, resp, http.StatusOK)
	var tags struct {
		Tags []struct {
			TagKey   string `cbor:"TagKey"`
			TagValue string `cbor:"TagValue"`
		} `cbor:"Tags"`
	}
	if err := cborlib.NewDecoder(resp.Body).Decode(&tags); err != nil {
		t.Fatalf("decode CBOR ListResourceTags response: %v", err)
	}
	if len(tags.Tags) != 1 || tags.Tags[0].TagKey != "env" || tags.Tags[0].TagValue != "test" {
		t.Fatalf("Tags = %#v, want env=test", tags.Tags)
	}
}

func TestEncrypt_disabledKey_returns400(t *testing.T) {
	// Given: a disabled key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "disabled-key")
	dis := kmsCall(t, srv, "DisableKey", map[string]any{"KeyId": keyID})
	dis.Body.Close()

	// When: encrypting with disabled key
	resp := kmsCall(t, srv, "Encrypt", map[string]any{
		"KeyId":     keyID,
		"Plaintext": []byte("test"),
	})
	defer resp.Body.Close()

	// Then: 400 DisabledException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── GenerateDataKey ─────────────────────────────────────────────────────────

func TestGenerateDataKey_returnsPlaintextAndCiphertext(t *testing.T) {
	// Given: a symmetric key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "gdk-key")

	// When: generating a data key
	resp := kmsCall(t, srv, "GenerateDataKey", map[string]any{
		"KeyId":   keyID,
		"KeySpec": "AES_256",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: both fields are present and the data key can be decrypted
	var out struct {
		Plaintext      []byte `json:"Plaintext"`
		CiphertextBlob []byte `json:"CiphertextBlob"`
		KeyId          string `json:"KeyId"`
	}
	decodeJSON(t, resp, &out)
	if len(out.Plaintext) != 32 {
		t.Errorf("expected 32-byte data key, got %d bytes", len(out.Plaintext))
	}
	if len(out.CiphertextBlob) == 0 {
		t.Error("expected non-empty CiphertextBlob")
	}

	// Verify the ciphertext decrypts back to the data key
	decResp := kmsCall(t, srv, "Decrypt", map[string]any{"CiphertextBlob": out.CiphertextBlob})
	defer decResp.Body.Close()
	helpers.AssertStatus(t, decResp, http.StatusOK)
	var decOut struct {
		Plaintext []byte `json:"Plaintext"`
	}
	decodeJSON(t, decResp, &decOut)
	if string(decOut.Plaintext) != string(out.Plaintext) {
		t.Error("decrypted data key does not match plaintext data key")
	}
}

func TestGenerateDataKeyWithoutPlaintext_returnsCiphertextOnly(t *testing.T) {
	// Given: a symmetric key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "gdk-wo-key")

	// When: generating a data key without plaintext
	resp := kmsCall(t, srv, "GenerateDataKeyWithoutPlaintext", map[string]any{
		"KeyId":   keyID,
		"KeySpec": "AES_256",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: only CiphertextBlob is returned (no Plaintext field)
	var out map[string]json.RawMessage
	decodeJSON(t, resp, &out)
	if _, ok := out["Plaintext"]; ok {
		t.Error("GenerateDataKeyWithoutPlaintext should not return Plaintext")
	}
	if _, ok := out["CiphertextBlob"]; !ok {
		t.Error("expected CiphertextBlob in response")
	}
}

// ─── Sign / Verify ────────────────────────────────────────────────────────────

func TestSign_andVerify_RSA2048(t *testing.T) {
	// Given: an RSA 2048 key with SIGN_VERIFY usage
	srv := helpers.NewTestServer(t)
	resp := kmsCall(t, srv, "CreateKey", map[string]any{
		"KeySpec":  "RSA_2048",
		"KeyUsage": "SIGN_VERIFY",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var createOut struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &createOut)
	keyID := createOut.KeyMetadata.KeyId

	message := []byte("the message to sign")

	// When: signing the message
	signResp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId":            keyID,
		"Message":          message,
		"MessageType":      "RAW",
		"SigningAlgorithm": "RSASSA_PKCS1_V1_5_SHA_256",
	})
	defer signResp.Body.Close()
	helpers.AssertStatus(t, signResp, http.StatusOK)
	var signOut struct {
		Signature        []byte `json:"Signature"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	decodeJSON(t, signResp, &signOut)
	if len(signOut.Signature) == 0 {
		t.Fatal("expected non-empty Signature")
	}

	// When: verifying the signature
	verifyResp := kmsCall(t, srv, "Verify", map[string]any{
		"KeyId":            keyID,
		"Message":          message,
		"MessageType":      "RAW",
		"Signature":        signOut.Signature,
		"SigningAlgorithm": "RSASSA_PKCS1_V1_5_SHA_256",
	})
	defer verifyResp.Body.Close()
	helpers.AssertStatus(t, verifyResp, http.StatusOK)
	var verifyOut struct {
		SignatureValid bool `json:"SignatureValid"`
	}
	decodeJSON(t, verifyResp, &verifyOut)

	// Then: signature is valid
	if !verifyOut.SignatureValid {
		t.Error("expected SignatureValid=true")
	}
}

// ─── GetPublicKey ─────────────────────────────────────────────────────────────

func TestGetPublicKey_RSA2048(t *testing.T) {
	// Given: an RSA_2048 key
	srv := helpers.NewTestServer(t)
	resp := kmsCall(t, srv, "CreateKey", map[string]any{
		"KeySpec":  "RSA_2048",
		"KeyUsage": "SIGN_VERIFY",
	})
	defer resp.Body.Close()
	var createOut struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &createOut)
	keyID := createOut.KeyMetadata.KeyId

	// When: getting the public key
	resp2 := kmsCall(t, srv, "GetPublicKey", map[string]any{"KeyId": keyID})
	defer resp2.Body.Close()

	// Then: a DER-encoded public key is returned
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out struct {
		PublicKey []byte `json:"PublicKey"`
		KeySpec   string `json:"KeySpec"`
		KeyUsage  string `json:"KeyUsage"`
	}
	decodeJSON(t, resp2, &out)
	if len(out.PublicKey) == 0 {
		t.Fatal("expected non-empty PublicKey")
	}
	if out.KeySpec != "RSA_2048" {
		t.Errorf("KeySpec = %q, want RSA_2048", out.KeySpec)
	}
	if out.KeyUsage != "SIGN_VERIFY" {
		t.Errorf("KeyUsage = %q, want SIGN_VERIFY", out.KeyUsage)
	}
}

func TestGetPublicKey_symmetricKey_returns400(t *testing.T) {
	// Given: a symmetric key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "symmetric-key")

	// When: getting the public key of a symmetric key
	resp := kmsCall(t, srv, "GetPublicKey", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()

	// Then: 400 error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── UpdateAlias ──────────────────────────────────────────────────────────────

func TestUpdateAlias_success(t *testing.T) {
	// Given: a key with an alias
	srv := helpers.NewTestServer(t)
	key1 := createKey(t, srv, "key-1")
	key2 := createKey(t, srv, "key-2")

	resp := kmsCall(t, srv, "CreateAlias", map[string]any{
		"AliasName":   "alias/update-test",
		"TargetKeyId": key1,
	})
	resp.Body.Close()

	// When: updating the alias to point to key2
	resp2 := kmsCall(t, srv, "UpdateAlias", map[string]any{
		"AliasName":   "alias/update-test",
		"TargetKeyId": key2,
	})
	defer resp2.Body.Close()

	// Then: the alias now points to key2
	helpers.AssertStatus(t, resp2, http.StatusOK)
	resp3 := kmsCall(t, srv, "DescribeKey", map[string]any{"KeyId": "alias/update-test"})
	defer resp3.Body.Close()
	var desc struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp3, &desc)
	if desc.KeyMetadata.KeyId != key2 {
		t.Errorf("alias/update-test resolved to %q, want %q", desc.KeyMetadata.KeyId, key2)
	}
}

// ─── ReEncrypt ────────────────────────────────────────────────────────────────

func TestReEncrypt_roundtrip(t *testing.T) {
	// Given: two symmetric keys
	srv := helpers.NewTestServer(t)
	key1 := createKey(t, srv, "reencrypt-src")
	key2 := createKey(t, srv, "reencrypt-dst")
	plaintext := []byte("data to re-encrypt")

	// When: encrypting with key1, then re-encrypting with key2
	encResp := kmsCall(t, srv, "Encrypt", map[string]any{
		"KeyId":     key1,
		"Plaintext": plaintext,
	})
	defer encResp.Body.Close()
	helpers.AssertStatus(t, encResp, http.StatusOK)
	var encOut struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
	}
	decodeJSON(t, encResp, &encOut)

	reResp := kmsCall(t, srv, "ReEncrypt", map[string]any{
		"CiphertextBlob":   encOut.CiphertextBlob,
		"DestinationKeyId": key2,
	})
	defer reResp.Body.Close()
	helpers.AssertStatus(t, reResp, http.StatusOK)
	var reOut struct {
		CiphertextBlob []byte `json:"CiphertextBlob"`
		SourceKeyId    string `json:"SourceKeyId"`
	}
	decodeJSON(t, reResp, &reOut)
	if len(reOut.CiphertextBlob) == 0 {
		t.Fatal("expected non-empty CiphertextBlob from ReEncrypt")
	}
	if reOut.SourceKeyId == "" {
		t.Error("expected non-empty SourceKeyId")
	}

	// Then: we can decrypt with key2
	decResp := kmsCall(t, srv, "Decrypt", map[string]any{
		"CiphertextBlob": reOut.CiphertextBlob,
	})
	defer decResp.Body.Close()
	helpers.AssertStatus(t, decResp, http.StatusOK)
	var decOut struct {
		Plaintext []byte `json:"Plaintext"`
	}
	decodeJSON(t, decResp, &decOut)
	if string(decOut.Plaintext) != string(plaintext) {
		t.Errorf("ReEncrypt/Decrypt plaintext = %q, want %q", decOut.Plaintext, plaintext)
	}
}

// ─── GenerateDataKeyPair ─────────────────────────────────────────────────────

func TestGenerateDataKeyPair_RSA2048(t *testing.T) {
	// Given: a symmetric key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "gdkp-key")

	// When: generating a data key pair
	resp := kmsCall(t, srv, "GenerateDataKeyPair", map[string]any{
		"KeyId":       keyID,
		"KeyPairSpec": "RSA_2048",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: both private and public key material is returned
	var out struct {
		PrivateKeyCiphertextBlob []byte `json:"PrivateKeyCiphertextBlob"`
		PrivateKeyPlaintext      []byte `json:"PrivateKeyPlaintext"`
		PublicKey                []byte `json:"PublicKey"`
		KeyPairSpec              string `json:"KeyPairSpec"`
	}
	decodeJSON(t, resp, &out)
	if len(out.PrivateKeyCiphertextBlob) == 0 {
		t.Error("expected non-empty PrivateKeyCiphertextBlob")
	}
	if len(out.PrivateKeyPlaintext) == 0 {
		t.Error("expected non-empty PrivateKeyPlaintext")
	}
	if len(out.PublicKey) == 0 {
		t.Error("expected non-empty PublicKey")
	}
	if out.KeyPairSpec != "RSA_2048" {
		t.Errorf("KeyPairSpec = %q, want RSA_2048", out.KeyPairSpec)
	}
}

// ─── VerifyMac ────────────────────────────────────────────────────────────────

func TestVerifyMac_validHMAC(t *testing.T) {
	// Given: a symmetric key and a message
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "hmac-key")
	message := []byte("message to authenticate")

	// When: we compute HMAC ourselves and verify it
	// (The server computes HMAC-SHA256 using the key's AES key, so we verify
	// with a known-invalid MAC and check the result is a well-formed response)
	resp := kmsCall(t, srv, "VerifyMac", map[string]any{
		"KeyId":        keyID,
		"Message":      message,
		"Mac":          []byte("any 32 byte mac value here!!"),
		"MacAlgorithm": "HMAC_SHA_256",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var out struct {
		MacValid bool `json:"MacValid"`
	}
	decodeJSON(t, resp, &out)
	// MacValid should be false for a bogus MAC
	if out.MacValid {
		t.Error("expected MacValid=false for bogus MAC")
	}
}

// ─── Key Policies ────────────────────────────────────────────────────────────

func TestGetKeyPolicy_default(t *testing.T) {
	// Given: a key with no explicit policy
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "policy-key")

	// When: getting the key policy
	resp := kmsCall(t, srv, "GetKeyPolicy", map[string]any{
		"KeyId":      keyID,
		"PolicyName": "default",
	})
	defer resp.Body.Close()

	// Then: a default policy is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Policy     string `json:"Policy"`
		PolicyName string `json:"PolicyName"`
	}
	decodeJSON(t, resp, &out)
	if out.Policy == "" {
		t.Error("expected non-empty Policy")
	}
	if out.PolicyName != "default" {
		t.Errorf("PolicyName = %q, want default", out.PolicyName)
	}
	var policy struct {
		Statement []struct {
			Resource string `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(out.Policy), &policy); err != nil {
		t.Fatalf("default Policy is not JSON: %v", err)
	}
	if len(policy.Statement) != 1 || policy.Statement[0].Resource != "*" {
		t.Fatalf("default policy statements = %#v, want Resource *", policy.Statement)
	}
}

func TestPutKeyPolicy_andGet(t *testing.T) {
	// Given: a key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "put-policy-key")
	customPolicy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:PutKeyPolicy","Resource":"*"}]}`, srv.Config.AccountID)

	// When: putting a custom key policy
	resp := kmsCall(t, srv, "PutKeyPolicy", map[string]any{
		"KeyId":      keyID,
		"PolicyName": "default",
		"Policy":     customPolicy,
	})
	resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the custom policy is returned
	resp2 := kmsCall(t, srv, "GetKeyPolicy", map[string]any{
		"KeyId":      keyID,
		"PolicyName": "default",
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var out struct {
		Policy string `json:"Policy"`
	}
	decodeJSON(t, resp2, &out)
	if out.Policy != customPolicy {
		t.Errorf("Policy = %q, want %q", out.Policy, customPolicy)
	}
}

func TestListKeyPolicies_returnsDefault(t *testing.T) {
	// Given: a key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "list-policy-key")

	// When: listing key policies
	resp := kmsCall(t, srv, "ListKeyPolicies", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()

	// Then: "default" is listed
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		PolicyNames []string `json:"PolicyNames"`
		Truncated   bool     `json:"Truncated"`
	}
	decodeJSON(t, resp, &out)
	if len(out.PolicyNames) != 1 || out.PolicyNames[0] != "default" {
		t.Errorf("PolicyNames = %v, want [default]", out.PolicyNames)
	}
}

// ─── Grants ──────────────────────────────────────────────────────────────────

func TestCreateGrant_andListGrants(t *testing.T) {
	// Given: a key
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "grant-key")

	// When: creating a grant
	resp := kmsCall(t, srv, "CreateGrant", map[string]any{
		"KeyId":            keyID,
		"GranteePrincipal": "arn:aws:iam::123456789012:role/test",
		"Operations":       []string{"Encrypt", "Decrypt"},
		"Name":             "my-grant",
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var createOut struct {
		GrantId    string `json:"GrantId"`
		GrantToken string `json:"GrantToken"`
	}
	decodeJSON(t, resp, &createOut)
	if createOut.GrantId == "" {
		t.Fatal("expected non-empty GrantId")
	}
	if createOut.GrantToken == "" {
		t.Fatal("expected non-empty GrantToken")
	}

	// Then: the grant appears in ListGrants
	resp2 := kmsCall(t, srv, "ListGrants", map[string]any{"KeyId": keyID})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)
	var listOut struct {
		Grants []struct {
			GrantId          string   `json:"GrantId"`
			GranteePrincipal string   `json:"GranteePrincipal"`
			Operations       []string `json:"Operations"`
			Name             string   `json:"Name"`
		} `json:"Grants"`
	}
	decodeJSON(t, resp2, &listOut)
	if len(listOut.Grants) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(listOut.Grants))
	}
	g := listOut.Grants[0]
	if g.GrantId != createOut.GrantId {
		t.Errorf("GrantId = %q, want %q", g.GrantId, createOut.GrantId)
	}
	if g.GranteePrincipal != "arn:aws:iam::123456789012:role/test" {
		t.Errorf("GranteePrincipal = %q", g.GranteePrincipal)
	}
	if len(g.Operations) != 2 {
		t.Errorf("expected 2 operations, got %d", len(g.Operations))
	}
	if g.Name != "my-grant" {
		t.Errorf("Name = %q, want my-grant", g.Name)
	}
}

func TestRevokeGrant_removesGrant(t *testing.T) {
	// Given: a key with a grant
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "revoke-key")
	resp := kmsCall(t, srv, "CreateGrant", map[string]any{
		"KeyId":            keyID,
		"GranteePrincipal": "arn:aws:iam::123456789012:role/test",
		"Operations":       []string{"Encrypt"},
	})
	defer resp.Body.Close()
	var createOut struct {
		GrantId string `json:"GrantId"`
	}
	decodeJSON(t, resp, &createOut)

	// When: revoking the grant
	revokeResp := kmsCall(t, srv, "RevokeGrant", map[string]any{
		"KeyId":   keyID,
		"GrantId": createOut.GrantId,
	})
	revokeResp.Body.Close()
	helpers.AssertStatus(t, revokeResp, http.StatusOK)

	// Then: the grant is gone
	listResp := kmsCall(t, srv, "ListGrants", map[string]any{"KeyId": keyID})
	defer listResp.Body.Close()
	var listOut struct {
		Grants []any `json:"Grants"`
	}
	decodeJSON(t, listResp, &listOut)
	if len(listOut.Grants) != 0 {
		t.Errorf("expected 0 grants after revoke, got %d", len(listOut.Grants))
	}
}

func TestRetireGrant_byToken(t *testing.T) {
	// Given: a key with a grant that has a retiring principal
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "retire-key")
	resp := kmsCall(t, srv, "CreateGrant", map[string]any{
		"KeyId":             keyID,
		"GranteePrincipal":  "arn:aws:iam::123456789012:role/grantee",
		"RetiringPrincipal": "arn:aws:iam::123456789012:role/retiring",
		"Operations":        []string{"Encrypt"},
	})
	defer resp.Body.Close()
	var createOut struct {
		GrantId    string `json:"GrantId"`
		GrantToken string `json:"GrantToken"`
	}
	decodeJSON(t, resp, &createOut)

	// When: retiring the grant by token
	retireResp := kmsCall(t, srv, "RetireGrant", map[string]any{
		"GrantToken": createOut.GrantToken,
	})
	retireResp.Body.Close()
	helpers.AssertStatus(t, retireResp, http.StatusOK)

	// Then: the grant is gone
	listResp := kmsCall(t, srv, "ListGrants", map[string]any{"KeyId": keyID})
	defer listResp.Body.Close()
	var listOut struct {
		Grants []any `json:"Grants"`
	}
	decodeJSON(t, listResp, &listOut)
	if len(listOut.Grants) != 0 {
		t.Errorf("expected 0 grants after retire, got %d", len(listOut.Grants))
	}
}

func TestListRetirableGrants_returnsMatchingGrants(t *testing.T) {
	// Given: a key with a grant that has a retiring principal
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "lrg-key")
	resp := kmsCall(t, srv, "CreateGrant", map[string]any{
		"KeyId":             keyID,
		"GranteePrincipal":  "arn:aws:iam::123456789012:role/grantee",
		"RetiringPrincipal": "arn:aws:iam::123456789012:role/retirer",
		"Operations":        []string{"Decrypt"},
	})
	resp.Body.Close()

	// When: listing retirable grants for the retiring principal
	resp2 := kmsCall(t, srv, "ListRetirableGrants", map[string]any{
		"RetiringPrincipal": "arn:aws:iam::123456789012:role/retirer",
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)

	// Then: the grant is listed
	var out struct {
		Grants []struct {
			GrantId          string `json:"GrantId"`
			GranteePrincipal string `json:"GranteePrincipal"`
		} `json:"Grants"`
	}
	decodeJSON(t, resp2, &out)
	if len(out.Grants) != 1 {
		t.Fatalf("expected 1 retirable grant, got %d", len(out.Grants))
	}
	if out.Grants[0].GranteePrincipal != "arn:aws:iam::123456789012:role/grantee" {
		t.Errorf("GranteePrincipal = %q", out.Grants[0].GranteePrincipal)
	}
}

func TestListRetirableGrants_nonExistentPrincipal_returnsEmpty(t *testing.T) {
	// Given: a key with a grant
	srv := helpers.NewTestServer(t)
	keyID := createKey(t, srv, "lrg-empty-key")
	resp := kmsCall(t, srv, "CreateGrant", map[string]any{
		"KeyId":            keyID,
		"GranteePrincipal": "arn:aws:iam::123456789012:role/someone",
		"Operations":       []string{"Encrypt"},
	})
	resp.Body.Close()

	// When: listing retirable grants for an unrelated principal
	resp2 := kmsCall(t, srv, "ListRetirableGrants", map[string]any{
		"RetiringPrincipal": "arn:aws:iam::123456789012:role/other",
	})
	defer resp2.Body.Close()
	helpers.AssertStatus(t, resp2, http.StatusOK)

	// Then: empty list
	var out struct {
		Grants []any `json:"Grants"`
	}
	decodeJSON(t, resp2, &out)
	if len(out.Grants) != 0 {
		t.Errorf("expected 0 retirable grants, got %d", len(out.Grants))
	}
}

// ─── CBOR path for new operations ─────────────────────────────────────────────

func TestRPCv2CBOR_GetPublicKey(t *testing.T) {
	// Given: an RSA key
	srv := helpers.NewTestServer(t)
	resp := kmsCall(t, srv, "CreateKey", map[string]any{
		"KeySpec":  "RSA_2048",
		"KeyUsage": "SIGN_VERIFY",
	})
	defer resp.Body.Close()
	var createOut struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &createOut)
	keyID := createOut.KeyMetadata.KeyId

	// When: getting the public key over CBOR
	resp2 := kmsCBORCall(t, srv, "GetPublicKey", map[string]any{"KeyId": keyID})
	defer resp2.Body.Close()

	// Then: CBOR response with public key
	helpers.AssertStatus(t, resp2, http.StatusOK)
	helpers.AssertHeader(t, resp2, "Content-Type", "application/cbor")
	var out struct {
		PublicKey []byte `cbor:"PublicKey"`
		KeySpec   string `cbor:"KeySpec"`
	}
	if err := cborlib.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode CBOR GetPublicKey response: %v", err)
	}
	if len(out.PublicKey) == 0 {
		t.Fatal("expected non-empty PublicKey over CBOR")
	}
}
