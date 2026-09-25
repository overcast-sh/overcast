package kms

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	_ "crypto/sha256" // registers crypto.SHA256 for Hash.New
	_ "crypto/sha512" // registers crypto.SHA384 and crypto.SHA512 for Hash.New
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Sign and Verify.
//
// AWS reference:
//   - https://docs.aws.amazon.com/kms/latest/APIReference/API_Sign.html
//   - https://docs.aws.amazon.com/kms/latest/APIReference/API_Verify.html
//   - https://docs.aws.amazon.com/kms/latest/developerguide/symm-asymm-choose-key-spec.html
//     (which signing algorithms each key spec supports)
//
// Message is 1-4096 bytes, Signature 1-6144, SigningAlgorithm is required and
// MessageType is RAW (default) | DIGEST | EXTERNAL_MU. With RAW, KMS hashes
// the message with the algorithm's hash; with DIGEST it skips that step, and
// "the length of the Message value must match the length of hashed messages
// for the specified signing algorithm". A KMS key whose KeyUsage is not
// SIGN_VERIFY, or a signing algorithm "incompatible with the type of key
// material in the KMS key (KeySpec)", is InvalidKeyUsageException (400).
// Verify never answers SignatureValid:false: "If the signature verification
// fails, the Verify operation fails with an KMSInvalidSignatureException".

const (
	maxSignMessageBytes = 4096
	maxSignatureBytes   = 6144

	messageTypeRaw        = "RAW"
	messageTypeDigest     = "DIGEST"
	messageTypeExternalMu = "EXTERNAL_MU"
)

// signingAlgorithmEnum is SigningAlgorithmSpec's Valid Values, in the order the
// API Reference lists them.
var signingAlgorithmEnum = []string{
	"RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512",
	"RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384", "RSASSA_PKCS1_V1_5_SHA_512",
	"ECDSA_SHA_256", "ECDSA_SHA_384", "ECDSA_SHA_512",
	"SM2DSA", "ML_DSA_SHAKE_256", "ED25519_SHA_512", "ED25519_PH_SHA_512",
}

var rsaSigningAlgorithms = []string{
	"RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512",
	"RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384", "RSASSA_PKCS1_V1_5_SHA_512",
}

// keySpecSigningAlgorithms is the developer guide's "Supported signing
// algorithms" tables: every RSA spec takes all six RSASSA algorithms, each
// ECC curve exactly one ECDSA hash, Ed25519 its two EdDSA variants, SM2 SM2DSA
// and every ML-DSA spec ML_DSA_SHAKE_256. A spec absent here (SYMMETRIC_DEFAULT,
// HMAC_*) has no signing algorithm at all.
var keySpecSigningAlgorithms = map[string][]string{
	"RSA_2048":              rsaSigningAlgorithms,
	"RSA_3072":              rsaSigningAlgorithms,
	"RSA_4096":              rsaSigningAlgorithms,
	"ECC_NIST_P256":         {"ECDSA_SHA_256"},
	"ECC_NIST_P384":         {"ECDSA_SHA_384"},
	"ECC_NIST_P521":         {"ECDSA_SHA_512"},
	"ECC_SECG_P256K1":       {"ECDSA_SHA_256"},
	"ECC_NIST_EDWARDS25519": {"ED25519_SHA_512", "ED25519_PH_SHA_512"},
	"SM2":                   {"SM2DSA"},
	"ML_DSA_44":             {"ML_DSA_SHAKE_256"},
	"ML_DSA_65":             {"ML_DSA_SHAKE_256"},
	"ML_DSA_87":             {"ML_DSA_SHAKE_256"},
}

// rsaScheme is how an RSASSA_* algorithm signs: the hash named by its suffix,
// with PSS or PKCS #1 v1.5 padding. KMS's PSS uses the same hash for MGF1 and a
// salt as long as the hash ("SHA-256 for both the message digest and the MGF1
// mask generation function along with a 256-bit salt").
type rsaScheme struct {
	hash crypto.Hash
	pss  bool
}

var rsaSchemes = map[string]rsaScheme{
	"RSASSA_PSS_SHA_256":        {crypto.SHA256, true},
	"RSASSA_PSS_SHA_384":        {crypto.SHA384, true},
	"RSASSA_PSS_SHA_512":        {crypto.SHA512, true},
	"RSASSA_PKCS1_V1_5_SHA_256": {crypto.SHA256, false},
	"RSASSA_PKCS1_V1_5_SHA_384": {crypto.SHA384, false},
	"RSASSA_PKCS1_V1_5_SHA_512": {crypto.SHA512, false},
}

func (s rsaScheme) pssOptions() *rsa.PSSOptions {
	return &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: s.hash}
}

// signingInput is the part of a Sign or Verify request the two share.
type signingInput struct {
	op               string
	keyID            string
	message          []byte
	messageType      string
	signingAlgorithm string
}

// validateSigningRequest checks the request shape before any key is looked up,
// the same ordering the other KMS operations here use.
func validateSigningRequest(in signingInput) *protocol.AWSError {
	if in.signingAlgorithm == "" {
		return errValidation("1 validation error detected: Value null at 'signingAlgorithm' failed to satisfy constraint: Member must not be null")
	}
	if !slices.Contains(signingAlgorithmEnum, in.signingAlgorithm) {
		return errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'signingAlgorithm' failed to satisfy constraint: "+
				"Member must satisfy enum value set: [%s]", in.signingAlgorithm, strings.Join(signingAlgorithmEnum, ", ")))
	}
	switch in.messageType {
	case "", messageTypeRaw, messageTypeDigest, messageTypeExternalMu:
	default:
		return errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'messageType' failed to satisfy constraint: "+
				"Member must satisfy enum value set: [RAW, DIGEST, EXTERNAL_MU]", in.messageType))
	}
	if len(in.message) < 1 || len(in.message) > maxSignMessageBytes {
		return errValidation(fmt.Sprintf(
			"1 validation error detected: Value at 'message' failed to satisfy constraint: "+
				"Member must have length between 1 and %d", maxSignMessageBytes))
	}
	return nil
}

// resolveSigningKey returns the key and the RSA scheme a validated Sign or
// Verify request names, after the key checks AWS makes: the key exists and is
// enabled, its KeyUsage is SIGN_VERIFY, and its KeySpec supports the
// algorithm.
func (h *Handler) resolveSigningKey(ctx context.Context, in signingInput) (*Key, *rsa.PrivateKey, rsaScheme, *protocol.AWSError) {
	k, aerr := h.resolveEnabledKeyForTyped(ctx, in.keyID)
	if aerr != nil {
		return nil, nil, rsaScheme{}, aerr
	}
	if k.KeyUsage != "SIGN_VERIFY" {
		return nil, nil, rsaScheme{}, errInvalidKeyUsage(fmt.Sprintf(
			"%s key usage is %s which is not valid for %s.", k.ARN, k.KeyUsage, in.op))
	}
	if !slices.Contains(keySpecSigningAlgorithms[k.KeySpec], in.signingAlgorithm) {
		return nil, nil, rsaScheme{}, errInvalidKeyUsage(fmt.Sprintf(
			"Algorithm %s is incompatible with key spec %s.", in.signingAlgorithm, k.KeySpec))
	}
	scheme, isRSA := rsaSchemes[in.signingAlgorithm]
	var priv *rsa.PrivateKey
	if isRSA && k.RSAPrivKey != nil {
		if p, err := parseRSAPrivateKey(k.RSAPrivKey); err == nil {
			priv = p
		}
	}
	if priv == nil {
		// A key spec AWS signs with but Overcast generates no key pair for
		// (generateKeyMaterial makes an RSA pair for RSA_2048 only). Refused
		// with a non-retryable 400 rather than a signature the algorithm
		// label does not describe, or a 500 every SDK retries.
		return nil, nil, rsaScheme{}, errValidation(fmt.Sprintf(
			"%s with key spec %s is not supported by this emulator; only RSA_2048 signing keys are emulated.",
			in.op, k.KeySpec))
	}
	if in.messageType == messageTypeExternalMu {
		return nil, nil, rsaScheme{}, errValidation(fmt.Sprintf(
			"MessageType EXTERNAL_MU is only valid with ML_DSA_SHAKE_256, not %s.", in.signingAlgorithm))
	}
	return k, priv, scheme, nil
}

// signingDigest is what the RSA primitive signs: the message hashed with the
// algorithm's hash for RAW, or the caller's digest as-is for DIGEST, which
// must be exactly as long as that hash.
func signingDigest(in signingInput, hash crypto.Hash) ([]byte, *protocol.AWSError) {
	if in.messageType == messageTypeDigest {
		if len(in.message) != hash.Size() {
			return nil, errValidation(fmt.Sprintf("Digest is invalid length for algorithm %s.", in.signingAlgorithm))
		}
		return in.message, nil
	}
	hh := hash.New()
	hh.Write(in.message)
	return hh.Sum(nil), nil
}

func (h *Handler) signTyped(ctx context.Context, req *signRequest) (*signResponse, *protocol.AWSError) {
	in := signingInput{
		op: "Sign", keyID: req.KeyId, message: req.Message,
		messageType: req.MessageType, signingAlgorithm: req.SigningAlgorithm,
	}
	if aerr := validateSigningRequest(in); aerr != nil {
		return nil, aerr
	}
	k, priv, scheme, aerr := h.resolveSigningKey(ctx, in)
	if aerr != nil {
		return nil, aerr
	}
	digest, aerr := signingDigest(in, scheme.hash)
	if aerr != nil {
		return nil, aerr
	}
	var sig []byte
	var err error
	if scheme.pss {
		sig, err = rsa.SignPSS(rand.Reader, priv, scheme.hash, digest, scheme.pssOptions())
	} else {
		sig, err = rsa.SignPKCS1v15(rand.Reader, priv, scheme.hash, digest)
	}
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	return &signResponse{KeyId: k.ARN, Signature: sig, SigningAlgorithm: req.SigningAlgorithm}, nil
}

func (h *Handler) verifyTyped(ctx context.Context, req *verifyRequest) (*verifyResponse, *protocol.AWSError) {
	in := signingInput{
		op: "Verify", keyID: req.KeyId, message: req.Message,
		messageType: req.MessageType, signingAlgorithm: req.SigningAlgorithm,
	}
	if aerr := validateSigningRequest(in); aerr != nil {
		return nil, aerr
	}
	if len(req.Signature) < 1 || len(req.Signature) > maxSignatureBytes {
		return nil, errValidation(fmt.Sprintf(
			"1 validation error detected: Value at 'signature' failed to satisfy constraint: "+
				"Member must have length between 1 and %d", maxSignatureBytes))
	}
	k, priv, scheme, aerr := h.resolveSigningKey(ctx, in)
	if aerr != nil {
		return nil, aerr
	}
	digest, aerr := signingDigest(in, scheme.hash)
	if aerr != nil {
		return nil, aerr
	}
	var err error
	if scheme.pss {
		err = rsa.VerifyPSS(&priv.PublicKey, scheme.hash, digest, req.Signature, scheme.pssOptions())
	} else {
		err = rsa.VerifyPKCS1v15(&priv.PublicKey, scheme.hash, digest, req.Signature)
	}
	if err != nil {
		return nil, errInvalidSignature()
	}
	return &verifyResponse{KeyId: k.ARN, SignatureValid: true, SigningAlgorithm: req.SigningAlgorithm}, nil
}

// errInvalidKeyUsage is InvalidKeyUsageException (400): the key's KeyUsage is
// wrong for the operation, or the algorithm does not fit its KeySpec.
func errInvalidKeyUsage(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidKeyUsageException", Message: msg, HTTPStatus: http.StatusBadRequest}
}

// errInvalidSignature is KMSInvalidSignatureException (400), Verify's answer
// to a signature that does not verify.
func errInvalidSignature() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "KMSInvalidSignatureException",
		Message:    "The signature verification failed.",
		HTTPStatus: http.StatusBadRequest,
	}
}
