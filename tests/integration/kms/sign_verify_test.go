package kms_test

// Sign and Verify fidelity (#2160).
//
// AWS reference:
//   - https://docs.aws.amazon.com/kms/latest/APIReference/API_Verify.html
//     "If the signature verification fails, the Verify operation fails with an
//     KMSInvalidSignatureException exception." (HTTP 400)
//   - https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html
//     InvalidKeyUsageException (HTTP 400): "The KeyUsage value of the KMS key
//     is incompatible with the API operation" or "The ... signing algorithm
//     specified for the operation is incompatible with the type of key
//     material in the KMS key (KeySpec)". MessageType DIGEST: "the length of
//     the Message value must match the length of hashed messages for the
//     specified signing algorithm".
//   - https://docs.aws.amazon.com/kms/latest/developerguide/symm-asymm-choose-key-spec.html
//     RSASSA_PSS_SHA_n uses SHA-n for the digest and MGF1 "along with a
//     n-bit salt"; RSASSA_PKCS1_V1_5_SHA_n is PKCS #1 v1.5 padding with SHA-n.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"net/http"
	"testing"

	cborlib "github.com/fxamacker/cbor/v2"

	"github.com/overcast-sh/overcast/tests/helpers"
)

var signMessage = []byte("the message to sign")

// createSigningKey creates a KMS key with the given spec and usage and returns its id.
func createSigningKey(t *testing.T, srv *helpers.TestServer, spec, usage string) string {
	t.Helper()
	resp := kmsCall(t, srv, "CreateKey", map[string]any{"KeySpec": spec, "KeyUsage": usage})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		KeyMetadata struct {
			KeyId string `json:"KeyId"`
		} `json:"KeyMetadata"`
	}
	decodeJSON(t, resp, &out)
	return out.KeyMetadata.KeyId
}

// signWith signs message under keyID and returns the signature, failing the test on error.
func signWith(t *testing.T, srv *helpers.TestServer, keyID string, message []byte, messageType, alg string) []byte {
	t.Helper()
	resp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId": keyID, "Message": message, "MessageType": messageType, "SigningAlgorithm": alg,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Signature        []byte `json:"Signature"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	decodeJSON(t, resp, &out)
	if out.SigningAlgorithm != alg {
		t.Errorf("SigningAlgorithm = %q, want %q", out.SigningAlgorithm, alg)
	}
	return out.Signature
}

func verifyCall(t *testing.T, srv *helpers.TestServer, keyID string, message []byte, messageType string, sig []byte, alg string) *http.Response {
	t.Helper()
	return kmsCall(t, srv, "Verify", map[string]any{
		"KeyId": keyID, "Message": message, "MessageType": messageType,
		"Signature": sig, "SigningAlgorithm": alg,
	})
}

func publicKeyOf(t *testing.T, srv *helpers.TestServer, keyID string) *rsa.PublicKey {
	t.Helper()
	resp := kmsCall(t, srv, "GetPublicKey", map[string]any{"KeyId": keyID})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		PublicKey []byte `json:"PublicKey"`
	}
	decodeJSON(t, resp, &out)
	pub, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key is %T, want *rsa.PublicKey", pub)
	}
	return rsaPub
}

// verifyLocally checks sig the way a client holding the public key would.
func verifyLocally(pub *rsa.PublicKey, alg string, message, sig []byte) error {
	var hash crypto.Hash
	switch alg[len(alg)-3:] {
	case "256":
		hash = crypto.SHA256
	case "384":
		hash = crypto.SHA384
	default:
		hash = crypto.SHA512
	}
	h := hash.New()
	h.Write(message)
	digest := h.Sum(nil)
	if alg[:len("RSASSA_PSS")] == "RSASSA_PSS" {
		return rsa.VerifyPSS(pub, hash, digest, sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: hash})
	}
	return rsa.VerifyPKCS1v15(pub, hash, digest, sig)
}

func TestVerify_tamperedMessageFailsWithInvalidSignature(t *testing.T) {
	// Given: a signature over one message
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	sig := signWith(t, srv, keyID, []byte("hello"), "RAW", "RSASSA_PKCS1_V1_5_SHA_256")

	// When: it is verified against a different message
	resp := verifyCall(t, srv, keyID, []byte("world"), "RAW", sig, "RSASSA_PKCS1_V1_5_SHA_256")
	defer resp.Body.Close()

	// Then: Verify fails rather than answering SignatureValid:false
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "KMSInvalidSignatureException")
}

func TestVerify_garbageSignatureFailsWithInvalidSignature(t *testing.T) {
	// Given: a signing key
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")

	// When: bytes that are no signature at all are verified
	resp := verifyCall(t, srv, keyID, []byte("original message"), "RAW",
		[]byte("definitely not a valid signature bytes here"), "RSASSA_PKCS1_V1_5_SHA_256")
	defer resp.Body.Close()

	// Then: KMSInvalidSignatureException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "KMSInvalidSignatureException")
}

func TestVerify_rpcV2CBORInvalidSignature(t *testing.T) {
	// Given: a signature over one message
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	sig := signWith(t, srv, keyID, []byte("hello"), "RAW", "RSASSA_PSS_SHA_256")

	// When: the typed (CBOR) path verifies it against another message
	resp := kmsCBORCall(t, srv, "Verify", map[string]any{
		"KeyId": keyID, "Message": []byte("world"), "Signature": sig, "SigningAlgorithm": "RSASSA_PSS_SHA_256",
	})
	defer resp.Body.Close()

	// Then: the same KMSInvalidSignatureException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	var errBody map[string]string
	if err := cborlib.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode CBOR error body: %v", err)
	}
	if errBody["__type"] != "KMSInvalidSignatureException" {
		t.Errorf("__type = %q, want KMSInvalidSignatureException", errBody["__type"])
	}
}

func TestSign_everyRSASigningAlgorithm(t *testing.T) {
	// Given: an RSA_2048 signing key and its public key
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	pub := publicKeyOf(t, srv, keyID)

	for _, alg := range []string{
		"RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512",
		"RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384", "RSASSA_PKCS1_V1_5_SHA_512",
	} {
		t.Run(alg, func(t *testing.T) {
			// When: the message is signed with this algorithm
			sig := signWith(t, srv, keyID, signMessage, "RAW", alg)

			// Then: the signature is what the algorithm label says, checked outside KMS
			if err := verifyLocally(pub, alg, signMessage, sig); err != nil {
				t.Errorf("local %s verification failed: %v", alg, err)
			}

			// And: KMS Verify with the same algorithm accepts it
			resp := verifyCall(t, srv, keyID, signMessage, "RAW", sig, alg)
			helpers.AssertStatus(t, resp, http.StatusOK)
			var out struct {
				SignatureValid   bool   `json:"SignatureValid"`
				SigningAlgorithm string `json:"SigningAlgorithm"`
			}
			decodeJSON(t, resp, &out)
			resp.Body.Close()
			if !out.SignatureValid || out.SigningAlgorithm != alg {
				t.Errorf("Verify = %+v, want SignatureValid true with %s", out, alg)
			}
		})
	}
}

func TestVerify_differentAlgorithmFails(t *testing.T) {
	// Given: a PSS SHA-512 signature
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	sig := signWith(t, srv, keyID, signMessage, "RAW", "RSASSA_PSS_SHA_512")

	// When: it is verified as PKCS #1 v1.5 SHA-256
	resp := verifyCall(t, srv, keyID, signMessage, "RAW", sig, "RSASSA_PKCS1_V1_5_SHA_256")
	defer resp.Body.Close()

	// Then: "If you submit a different algorithm, the signature verification fails."
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "KMSInvalidSignatureException")
}

func TestSign_messageTypeDigest(t *testing.T) {
	// Given: a signing key and the SHA-256 digest of a message
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	digest := sha256.Sum256(signMessage)

	// When: the digest is signed with MessageType DIGEST
	sig := signWith(t, srv, keyID, digest[:], "DIGEST", "RSASSA_PSS_SHA_256")

	// Then: it is a signature over the message itself — "A message and its
	// hash digest are considered to be the same message" — so a RAW Verify of
	// the message accepts it
	resp := verifyCall(t, srv, keyID, signMessage, "RAW", sig, "RSASSA_PSS_SHA_256")
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// And: a DIGEST Verify of the digest accepts it too
	resp = verifyCall(t, srv, keyID, digest[:], "DIGEST", sig, "RSASSA_PSS_SHA_256")
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// And: RAW Verify of the digest hashes it a second time, so it fails
	resp = verifyCall(t, srv, keyID, digest[:], "RAW", sig, "RSASSA_PSS_SHA_256")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "KMSInvalidSignatureException")
}

func TestSign_digestLengthMustMatchAlgorithm(t *testing.T) {
	// Given: a signing key and a 32-byte (SHA-256) digest
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	digest := sha256.Sum256(signMessage)

	// When: it is signed as a digest for a SHA-384 algorithm
	resp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId": keyID, "Message": digest[:], "MessageType": "DIGEST",
		"SigningAlgorithm": "RSASSA_PKCS1_V1_5_SHA_384",
	})
	defer resp.Body.Close()

	// Then: ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestSignVerify_keyUsageNotSignVerify(t *testing.T) {
	// Given: a symmetric key and an RSA encryption key
	srv := helpers.NewTestServer(t)
	keys := map[string]string{
		"SYMMETRIC_DEFAULT": createSigningKey(t, srv, "SYMMETRIC_DEFAULT", "ENCRYPT_DECRYPT"),
		"RSA_2048":          createSigningKey(t, srv, "RSA_2048", "ENCRYPT_DECRYPT"),
	}
	for spec, keyID := range keys {
		for _, op := range []string{"Sign", "Verify"} {
			// When: Sign or Verify is called on it
			resp := kmsCall(t, srv, op, map[string]any{
				"KeyId": keyID, "Message": signMessage, "Signature": []byte("sig"),
				"SigningAlgorithm": "RSASSA_PKCS1_V1_5_SHA_256",
			})

			// Then: InvalidKeyUsageException, not a retryable 500
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s on %s: status %d, want 400", op, spec, resp.StatusCode)
			}
			helpers.AssertJSONError(t, resp, "InvalidKeyUsageException")
			resp.Body.Close()
		}
	}
}

func TestSign_algorithmIncompatibleWithKeySpec(t *testing.T) {
	// Given: an RSA signing key
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")

	// When: it is asked to sign with an ECDSA algorithm
	resp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId": keyID, "Message": signMessage, "SigningAlgorithm": "ECDSA_SHA_256",
	})
	defer resp.Body.Close()

	// Then: InvalidKeyUsageException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidKeyUsageException")
}

func TestSign_signingAlgorithmMissingOrUnknown(t *testing.T) {
	// Given: an RSA signing key
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")

	for name, body := range map[string]map[string]any{
		"missing": {"KeyId": keyID, "Message": signMessage},
		"unknown": {"KeyId": keyID, "Message": signMessage, "SigningAlgorithm": "RSASSA_PSS_SHA_1"},
	} {
		// When: SigningAlgorithm is absent or outside its enum
		resp := kmsCall(t, srv, "Sign", body)

		// Then: ValidationException
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, resp.StatusCode)
		}
		helpers.AssertJSONError(t, resp, "ValidationException")
		resp.Body.Close()
	}
}

func TestSign_disabledKey(t *testing.T) {
	// Given: a disabled signing key
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "RSA_2048", "SIGN_VERIFY")
	dis := kmsCall(t, srv, "DisableKey", map[string]any{"KeyId": keyID})
	helpers.AssertStatus(t, dis, http.StatusOK)
	dis.Body.Close()

	// When: it is asked to sign
	resp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId": keyID, "Message": signMessage, "SigningAlgorithm": "RSASSA_PSS_SHA_256",
	})
	defer resp.Body.Close()

	// Then: DisabledException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "DisabledException")
}

func TestSign_unemulatedSigningKeySpecFailsLoudly(t *testing.T) {
	// Given: an ECC signing key, whose key pair Overcast does not generate
	srv := helpers.NewTestServer(t)
	keyID := createSigningKey(t, srv, "ECC_NIST_P256", "SIGN_VERIFY")

	// When: it is asked to sign with the algorithm AWS supports for it
	resp := kmsCall(t, srv, "Sign", map[string]any{
		"KeyId": keyID, "Message": signMessage, "SigningAlgorithm": "ECDSA_SHA_256",
	})
	defer resp.Body.Close()

	// Then: a non-retryable 400, never a signature of some other kind
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}
