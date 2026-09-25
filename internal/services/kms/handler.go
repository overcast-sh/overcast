package kms

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler holds KMS handler dependencies.
type Handler struct {
	cfg   *config.Config
	store *Store
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	bus   *events.Bus
	ops   map[string]http.HandlerFunc

	typedOp map[string]op.Operation
}

func newHandler(cfg *config.Config, store *Store, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{cfg: cfg, store: store, log: log, clk: clk}
	h.initOps()
	return h
}

// initOps registers every known KMS operation to its handler.
// Adding a new operation: add an entry here, implement in handler.go.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		// Key lifecycle
		"CreateKey":            h.CreateKey,
		"DescribeKey":          h.DescribeKey,
		"ListKeys":             h.ListKeys,
		"DisableKey":           h.DisableKey,
		"EnableKey":            h.EnableKey,
		"UpdateKeyDescription": h.UpdateKeyDescription,
		"ScheduleKeyDeletion":  h.ScheduleKeyDeletion,
		"CancelKeyDeletion":    h.CancelKeyDeletion,
		// Aliases
		"CreateAlias": h.CreateAlias,
		"DeleteAlias": h.DeleteAlias,
		"ListAliases": h.ListAliases,
		// Crypto (symmetric)
		"Encrypt":                         h.Encrypt,
		"Decrypt":                         h.Decrypt,
		"GenerateDataKey":                 h.GenerateDataKey,
		"GenerateDataKeyWithoutPlaintext": h.GenerateDataKeyWithoutPlaintext,
		"GenerateRandom":                  h.GenerateRandom,
		// Crypto (asymmetric)
		"Sign":   h.Sign,
		"Verify": h.Verify,
		// Tags
		"TagResource":      h.TagResource,
		"UntagResource":    h.UntagResource,
		"ListResourceTags": h.ListResourceTags,
		// Additional operations
		"GetPublicKey":        h.GetPublicKey,
		"UpdateAlias":         h.UpdateAlias,
		"ReEncrypt":           h.ReEncrypt,
		"GenerateDataKeyPair": h.GenerateDataKeyPair,
		"VerifyMac":           h.VerifyMac,
		// Key policies
		"GetKeyPolicy":    h.GetKeyPolicy,
		"PutKeyPolicy":    h.PutKeyPolicy,
		"ListKeyPolicies": h.ListKeyPolicies,
		// Grants
		"CreateGrant":         h.CreateGrant,
		"ListGrants":          h.ListGrants,
		"RevokeGrant":         h.RevokeGrant,
		"RetireGrant":         h.RetireGrant,
		"ListRetirableGrants": h.ListRetirableGrants,
	}
	h.typedOp = h.typedOps()
}

// ── Key wire type ─────────────────────────────────────────────────────────────

// keyMetadataWire is the KeyMetadata structure of CreateKey and DescribeKey.
// Per the API Reference (https://docs.aws.amazon.com/kms/latest/APIReference/API_KeyMetadata.html)
// it carries AWSAccountId, "the twelve-digit account ID of the AWS account
// that owns the KMS key", and the deprecated CustomerMasterKeySpec, of which
// "the KeySpec and CustomerMasterKeySpec fields have the same value" (#1906).
type keyMetadataWire struct {
	AWSAccountId          string    `json:"AWSAccountId" cbor:"AWSAccountId"`
	KeyId                 string    `json:"KeyId" cbor:"KeyId"`
	Arn                   string    `json:"Arn" cbor:"Arn"`
	Description           string    `json:"Description" cbor:"Description"`
	CustomerMasterKeySpec string    `json:"CustomerMasterKeySpec" cbor:"CustomerMasterKeySpec"`
	KeySpec               string    `json:"KeySpec" cbor:"KeySpec"`
	KeyUsage              string    `json:"KeyUsage" cbor:"KeyUsage"`
	Enabled               bool      `json:"Enabled" cbor:"Enabled"`
	KeyState              string    `json:"KeyState" cbor:"KeyState"`
	CreationDate          float64   `json:"CreationDate" cbor:"CreationDate"`
	DeletionDate          float64   `json:"DeletionDate,omitempty" cbor:"DeletionDate,omitempty"`
	KeyManager            string    `json:"KeyManager" cbor:"KeyManager"`
	Origin                string    `json:"Origin" cbor:"Origin"`
	MultiRegion           bool      `json:"MultiRegion" cbor:"MultiRegion"`
	XksKeyConfiguration   *struct{} `json:"XksKeyConfiguration,omitempty" cbor:"XksKeyConfiguration,omitempty"`
}

func (h *Handler) toMeta(k *Key) keyMetadataWire {
	m := keyMetadataWire{
		AWSAccountId:          h.keyAccountID(k),
		KeyId:                 k.KeyID,
		Arn:                   k.ARN,
		Description:           k.Description,
		CustomerMasterKeySpec: k.KeySpec,
		KeySpec:               k.KeySpec,
		KeyUsage:              k.KeyUsage,
		Enabled:               k.Enabled,
		KeyState:              k.KeyState,
		CreationDate:          float64(k.CreatedAt.UnixMilli()) / 1000.0,
		KeyManager:            "CUSTOMER",
		Origin:                "AWS_KMS",
		MultiRegion:           false,
	}
	if k.DeletionDate != nil {
		m.DeletionDate = float64(k.DeletionDate.UnixMilli()) / 1000.0
	}
	return m
}

// keyAccountID returns the account that owns k: the account segment of its
// ARN, so AWSAccountId always agrees with the Arn beside it — including for a
// key persisted before the configured account changed. A key whose ARN has no
// account segment falls back to the configured account.
func (h *Handler) keyAccountID(k *Key) string {
	// arn:aws:kms:<region>:<account>:key/<id>
	if parts := strings.SplitN(k.ARN, ":", 6); len(parts) == 6 && parts[4] != "" {
		return parts[4]
	}
	return h.cfg.AccountID
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// publish emits an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{Type: t, Payload: payload})
	}
}

// CreateKey creates a new KMS key with optional crypto material.
func (h *Handler) CreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description                    string  `json:"Description"`
		Tags                           []Tag   `json:"Tags"`
		Origin                         string  `json:"Origin"`
		MultiRegion                    bool    `json:"MultiRegion"`
		KeySpec                        string  `json:"KeySpec"`
		KeyUsage                       string  `json:"KeyUsage"`
		Policy                         *string `json:"Policy"`
		BypassPolicyLockoutSafetyCheck bool    `json:"BypassPolicyLockoutSafetyCheck"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.KeySpec == "" {
		req.KeySpec = "SYMMETRIC_DEFAULT"
	}
	if req.KeyUsage == "" {
		req.KeyUsage = "ENCRYPT_DECRYPT"
	}
	if aerr := validateKeyOrigin(req.Origin); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if req.MultiRegion {
		protocol.WriteJSONError(w, r, errMultiRegionNotSupported())
		return
	}

	keyID := uuid.NewString()
	arn := fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", middleware.RegionFromContext(r.Context(), h.cfg.Region), h.cfg.AccountID, keyID)
	if req.Policy != nil {
		if aerr := h.validateKeyPolicy(r.Context(), *req.Policy, arn, req.BypassPolicyLockoutSafetyCheck); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
	}

	policy := ""
	if req.Policy != nil {
		policy = *req.Policy
	}
	// See createKeyTyped (typed_logic.go) — this JSON1.1 path duplicates it
	// rather than delegating, so the tag validation added for #1052 has to be
	// kept in lockstep here too.
	if aerr := validateKeyTags(req.Tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	k := &Key{
		KeyID:       keyID,
		ARN:         arn,
		Description: req.Description,
		KeySpec:     req.KeySpec,
		KeyUsage:    req.KeyUsage,
		Enabled:     true,
		KeyState:    "Enabled",
		CreatedAt:   h.clk.Now(),
		Policy:      policy,
		Tags:        req.Tags,
	}

	// Generate crypto material
	if err := generateKeyMaterial(k); err != nil {
		log := h.log.WithRecorder(r.Context())
		log.Warn("kms: generate key material", zap.Error(err))
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	ctx := r.Context()
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}

	h.publish(r, events.KMSKeyCreated, events.ResourcePayload{Name: k.KeyID, ARN: k.ARN})

	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"KeyMetadata": h.toMeta(k),
	}, "application/x-amz-json-1.1")
}

// DescribeKey returns key metadata.
func (h *Handler) DescribeKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if k == nil {
		protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"KeyMetadata": h.toMeta(k)}, "application/x-amz-json-1.1")
}

// ListKeys returns all key IDs and ARNs.
func (h *Handler) ListKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := h.store.ScanKeys(ctx)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	entries := make([]map[string]string, 0, len(keys))
	for _, k := range keys {
		if k.KeyState != "PendingDeletion" {
			entries = append(entries, map[string]string{"KeyId": k.KeyID, "KeyArn": k.ARN})
		}
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"Keys": entries, "Truncated": false}, "application/x-amz-json-1.1")
}

// DisableKey disables a key.
func (h *Handler) DisableKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	k.Enabled = false
	k.KeyState = "Disabled"
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	h.publish(r, events.KMSKeyStateChanged, events.ResourcePayload{Name: k.KeyID})
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// EnableKey enables a key.
func (h *Handler) EnableKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	k.Enabled = true
	k.KeyState = "Enabled"
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	h.publish(r, events.KMSKeyStateChanged, events.ResourcePayload{Name: k.KeyID})
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// UpdateKeyDescription updates a key's description.
// AWS docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_UpdateKeyDescription.html
func (h *Handler) UpdateKeyDescription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId       string `json:"KeyId"`
		Description string `json:"Description"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	k.Description = req.Description
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// ScheduleKeyDeletion marks a key as pending deletion.
func (h *Handler) ScheduleKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req scheduleKeyDeletionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.scheduleKeyDeletionTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// CancelKeyDeletion cancels pending deletion.
func (h *Handler) CancelKeyDeletion(w http.ResponseWriter, r *http.Request) {
	var req keyIDRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.cancelKeyDeletionTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// ── Alias handlers ────────────────────────────────────────────────────────────

// CreateAlias creates an alias pointing to a key.
func (h *Handler) CreateAlias(w http.ResponseWriter, r *http.Request) {
	var req createAliasRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if _, aerr := h.createAliasTyped(r.Context(), &req); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// DeleteAlias removes an alias.
func (h *Handler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AliasName string `json:"AliasName"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if err := h.store.DeleteAlias(ctx, req.AliasName); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// ListAliases returns all aliases, optionally filtered by key ID.
func (h *Handler) ListAliases(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	filterKeyID := ""
	if req.KeyId != "" {
		k, err := h.resolveKey(ctx, req.KeyId)
		if err != nil {
			protocol.WriteJSONError(w, r, protocol.ErrInternalError)
			return
		}
		if k != nil {
			filterKeyID = k.KeyID
		}
	}
	aliases, err := h.store.ScanAliases(ctx, filterKeyID)
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	type aliasEntry struct {
		AliasName    string  `json:"AliasName"`
		AliasArn     string  `json:"AliasArn"`
		TargetKeyId  string  `json:"TargetKeyId"`
		CreationDate float64 `json:"CreationDate"`
	}
	entries := make([]aliasEntry, 0, len(aliases))
	for _, a := range aliases {
		entries = append(entries, aliasEntry{
			AliasName:    a.AliasName,
			AliasArn:     a.AliasARN,
			TargetKeyId:  a.TargetKeyID,
			CreationDate: float64(a.CreatedAt.UnixMilli()) / 1000.0,
		})
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{"Aliases": entries, "Truncated": false}, "application/x-amz-json-1.1")
}

// ── Crypto handlers ───────────────────────────────────────────────────────────

// Encrypt encrypts plaintext using a symmetric key.
func (h *Handler) Encrypt(w http.ResponseWriter, r *http.Request) {
	var req encryptRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.encryptTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// Decrypt decrypts a ciphertext blob.
func (h *Handler) Decrypt(w http.ResponseWriter, r *http.Request) {
	var req decryptRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.decryptTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// GenerateDataKey returns a new random data key, both plaintext and encrypted.
func (h *Handler) GenerateDataKey(w http.ResponseWriter, r *http.Request) {
	var req generateDataKeyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	k, dataKey, ciphertext, aerr := h.generateDataKeyParts(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"Plaintext":      dataKey,
		"CiphertextBlob": ciphertext,
		"KeyId":          k.ARN,
	}, "application/x-amz-json-1.1")
}

// GenerateDataKeyWithoutPlaintext returns an encrypted data key only.
func (h *Handler) GenerateDataKeyWithoutPlaintext(w http.ResponseWriter, r *http.Request) {
	var req generateDataKeyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	k, _, ciphertext, aerr := h.generateDataKeyParts(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"CiphertextBlob": ciphertext,
		"KeyId":          k.ARN,
	}, "application/x-amz-json-1.1")
}

// GenerateRandom returns NumberOfBytes cryptographically secure random bytes.
func (h *Handler) GenerateRandom(w http.ResponseWriter, r *http.Request) {
	var req generateRandomRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.generateRandomTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"Plaintext": out.Plaintext,
	}, "application/x-amz-json-1.1")
}

// Sign creates a digital signature; the logic lives in signTyped (signing.go).
func (h *Handler) Sign(w http.ResponseWriter, r *http.Request) {
	var req signRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.signTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// Verify verifies a digital signature; the logic lives in verifyTyped (signing.go).
func (h *Handler) Verify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.verifyTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// ── GetPublicKey ──────────────────────────────────────────────────────────────

// GetPublicKey returns the public key for an asymmetric KMS key.
func (h *Handler) GetPublicKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if k.RSAPrivKey == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "UnsupportedOperationException",
			Message:    "GetPublicKey is only supported for asymmetric keys.",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	privKey, parseErr := parseRSAPrivateKey(k.RSAPrivKey)
	if parseErr != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	pubDER, marshalErr := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if marshalErr != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	resp := map[string]any{
		"KeyId":                 k.ARN,
		"PublicKey":             pubDER,
		"CustomerMasterKeySpec": k.KeySpec,
		"KeySpec":               k.KeySpec,
		"KeyUsage":              k.KeyUsage,
	}
	if k.KeyUsage == "SIGN_VERIFY" {
		resp["SigningAlgorithms"] = []string{
			"RSASSA_PKCS1_V1_5_SHA_256",
			"RSASSA_PKCS1_V1_5_SHA_384",
			"RSASSA_PKCS1_V1_5_SHA_512",
			"RSASSA_PSS_SHA_256",
			"RSASSA_PSS_SHA_384",
			"RSASSA_PSS_SHA_512",
		}
	} else if k.KeyUsage == "ENCRYPT_DECRYPT" {
		resp["EncryptionAlgorithms"] = []string{
			"RSAES_OAEP_SHA_1",
			"RSAES_OAEP_SHA_256",
		}
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, resp, "application/x-amz-json-1.1")
}

// ── UpdateAlias ──────────────────────────────────────────────────────────────

// UpdateAlias updates an alias to point to a different key.
func (h *Handler) UpdateAlias(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AliasName   string `json:"AliasName"`
		TargetKeyId string `json:"TargetKeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.AliasName == "" {
		protocol.WriteJSONError(w, r, protocol.ErrMissingParameter("AliasName"))
		return
	}
	ctx := r.Context()
	a, err := h.store.GetAlias(ctx, req.AliasName)
	if err != nil || a == nil {
		if a == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.AliasName))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	k, err := h.resolveKey(ctx, req.TargetKeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.TargetKeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	a.TargetKeyID = k.KeyID
	if err := h.store.PutAlias(ctx, a); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// ── ReEncrypt ────────────────────────────────────────────────────────────────

// ReEncrypt decrypts ciphertext from one key and re-encrypts with another.
func (h *Handler) ReEncrypt(w http.ResponseWriter, r *http.Request) {
	var req reEncryptRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	out, aerr := h.reEncryptTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, out, "application/x-amz-json-1.1")
}

// ── GenerateDataKeyPair ─────────────────────────────────────────────────────

// GenerateDataKeyPair returns an encrypted private key and plaintext public key.
func (h *Handler) GenerateDataKeyPair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId       string `json:"KeyId"`
		KeyPairSpec string `json:"KeyPairSpec"` // "RSA_2048", "RSA_3072", "RSA_4096"
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.KeyPairSpec == "" {
		req.KeyPairSpec = "RSA_2048"
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !k.Enabled {
		protocol.WriteJSONError(w, r, errDisabled(k.KeyID))
		return
	}
	bits := 2048
	switch req.KeyPairSpec {
	case "RSA_3072":
		bits = 3072
	case "RSA_4096":
		bits = 4096
	}
	priv, genErr := rsa.GenerateKey(rand.Reader, bits)
	if genErr != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	privDER := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privDER})

	pubDER, marshalErr := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if marshalErr != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	privateCiphertext, encErr := aesGCMEncrypt(k.AESKey, privPEM, k.KeyID)
	if encErr != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"KeyId":                    k.ARN,
		"KeyPairSpec":              req.KeyPairSpec,
		"PrivateKeyCiphertextBlob": privateCiphertext,
		"PrivateKeyPlaintext":      privPEM,
		"PublicKey":                pubPEM,
	}, "application/x-amz-json-1.1")
}

// ── VerifyMac ────────────────────────────────────────────────────────────────

// VerifyMac verifies an HMAC computed by the client against the KMS key.
func (h *Handler) VerifyMac(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId        string `json:"KeyId"`
		Message      []byte `json:"Message"`
		Mac          []byte `json:"Mac"`
		MacAlgorithm string `json:"MacAlgorithm"` // "HMAC_SHA_256", "HMAC_SHA_384", "HMAC_SHA_512"
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if !k.Enabled {
		protocol.WriteJSONError(w, r, errDisabled(k.KeyID))
		return
	}
	var hash crypto.Hash
	switch req.MacAlgorithm {
	case "HMAC_SHA_256":
		hash = crypto.SHA256
	case "HMAC_SHA_384":
		hash = crypto.SHA384
	case "HMAC_SHA_512":
		hash = crypto.SHA512
	default:
		hash = crypto.SHA256
	}
	mac := hmac.New(sha256.New, k.AESKey)
	if hash == crypto.SHA384 {
		mac = hmac.New(sha512.New384, k.AESKey)
	} else if hash == crypto.SHA512 {
		mac = hmac.New(sha512.New, k.AESKey)
	}
	mac.Write(req.Message)
	expected := mac.Sum(nil)
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"KeyId":        k.ARN,
		"MacValid":     hmac.Equal(expected, req.Mac),
		"MacAlgorithm": req.MacAlgorithm,
	}, "application/x-amz-json-1.1")
}

// ── Key policy handlers ──────────────────────────────────────────────────────

// GetKeyPolicy returns the key policy for a KMS key.
func (h *Handler) GetKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId      string `json:"KeyId"`
		PolicyName string `json:"PolicyName"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.PolicyName == "" {
		req.PolicyName = "default"
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	policy := k.Policy
	if policy == "" {
		policy = defaultKeyPolicy(h.cfg.AccountID)
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"Policy":     policy,
		"PolicyName": req.PolicyName,
	}, "application/x-amz-json-1.1")
}

// PutKeyPolicy attaches a key policy to a KMS key.
func (h *Handler) PutKeyPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId                          string `json:"KeyId"`
		PolicyName                     string `json:"PolicyName"`
		Policy                         string `json:"Policy"`
		BypassPolicyLockoutSafetyCheck bool   `json:"BypassPolicyLockoutSafetyCheck"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.PolicyName == "" {
		req.PolicyName = "default"
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	if aerr := h.validateKeyPolicy(ctx, req.Policy, k.ARN, req.BypassPolicyLockoutSafetyCheck); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	k.Policy = req.Policy
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// ListKeyPolicies lists the policy names for a KMS key.
func (h *Handler) ListKeyPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		if k == nil {
			protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
			return
		}
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	policyNames := []string{"default"}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"PolicyNames": policyNames,
		"Truncated":   false,
	}, "application/x-amz-json-1.1")
	_ = k // suppress unused warning
}

// defaultKeyPolicy returns a minimal key policy granting full access to the account.
func defaultKeyPolicy(accountID string) string {
	return fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":"Enable IAM User Permissions","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:root"},"Action":"kms:*","Resource":"*"}]}`, accountID)
}

// ── Crypto helpers ────────────────────────────────────────────────────────────

// ciphertextEnvelope is the internal format for ciphertext blobs.
type ciphertextEnvelope struct {
	KeyID string `json:"k"`
	Nonce []byte `json:"n"`
	CT    []byte `json:"c"`
}

// aesGCMEncrypt encrypts plaintext with the given AES key, returning a
// JSON-encoded envelope as the ciphertext blob ([]byte for JSON base64 encoding).
func aesGCMEncrypt(aesKey, plaintext []byte, keyID string) ([]byte, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := ciphertextEnvelope{KeyID: keyID, Nonce: nonce, CT: ct}
	return json.Marshal(env)
}

// parseEnvelope parses a ciphertext blob into (keyID, nonce, ciphertext).
func parseEnvelope(blob []byte) (keyID string, nonce, ct []byte, err error) {
	var env ciphertextEnvelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return "", nil, nil, err
	}
	return env.KeyID, env.Nonce, env.CT, nil
}

// aesGCMDecryptRaw decrypts ciphertext using the given AES key and nonce.
func aesGCMDecryptRaw(aesKey, nonce, ct []byte) ([]byte, error) {
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ct, nil)
}

// generateKeyMaterial fills k.AESKey (symmetric) or k.RSAPrivKey (RSA).
func generateKeyMaterial(k *Key) error {
	switch k.KeySpec {
	case "SYMMETRIC_DEFAULT", "AES_256":
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		k.AESKey = key
	case "RSA_2048":
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		der := x509.MarshalPKCS1PrivateKey(priv)
		block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}
		k.RSAPrivKey = pem.EncodeToMemory(block)
	default:
		// For unrecognised specs, generate a 32-byte AES key as fallback.
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		k.AESKey = key
	}
	return nil
}

// parseRSAPrivateKey decodes a PEM-encoded RSA private key.
func parseRSAPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("kms: failed to decode PEM block")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// resolveKey looks up a key by UUID, ARN (arn:aws:kms:...:key/<id>), or alias (alias/...).
func (h *Handler) resolveKey(ctx context.Context, keyID string) (*Key, error) {
	if keyID == "" {
		return nil, nil
	}
	// ARN containing :key/ segment
	if strings.Contains(keyID, ":key/") {
		parts := strings.SplitN(keyID, ":key/", 2)
		return h.store.GetKey(ctx, parts[1])
	}
	// Alias by name (alias/...) or by ARN (:alias/...)
	if strings.HasPrefix(keyID, "alias/") || strings.Contains(keyID, ":alias/") {
		name := keyID
		if strings.Contains(keyID, ":alias/") {
			parts := strings.SplitN(keyID, ":alias/", 2)
			name = "alias/" + parts[1]
		}
		a, err := h.store.GetAlias(ctx, name)
		if err != nil || a == nil {
			return nil, err
		}
		return h.store.GetKey(ctx, a.TargetKeyID)
	}
	// Plain UUID
	return h.store.GetKey(ctx, keyID)
}

// ─── Tag operations ────────────────────────────────────────────────────────────

// TagResource adds or overwrites tags on a KMS key.
// AWS docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_TagResource.html
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
		Tags  []Tag  `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
		return
	}
	tags := k.GetTags()
	for _, t := range req.Tags {
		tags[t.TagKey] = t.TagValue
	}
	// Validated against the merged set, not just the incoming delta, matching
	// tagResourceTyped's ordering (#1052).
	if aerr := serviceutil.ValidateTags(kmsTagCfg, tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	k.SetTags(tags)
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// UntagResource removes tags from a KMS key by key names.
// AWS docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_UntagResource.html
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId   string   `json:"KeyId"`
		TagKeys []string `json:"TagKeys"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
		return
	}
	tags := k.GetTags()
	for _, key := range req.TagKeys {
		delete(tags, key)
	}
	k.SetTags(tags)
	if err := h.store.PutKey(ctx, k); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{}, "application/x-amz-json-1.1")
}

// ListResourceTags lists tags on a KMS key.
// AWS docs: https://docs.aws.amazon.com/kms/latest/APIReference/API_ListResourceTags.html
func (h *Handler) ListResourceTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		KeyId string `json:"KeyId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	k, err := h.resolveKey(ctx, req.KeyId)
	if err != nil || k == nil {
		protocol.WriteJSONError(w, r, errNotFound(req.KeyId))
		return
	}
	tagMap := k.GetTags()
	tags := make([]Tag, 0, len(tagMap))
	for kk, v := range tagMap {
		tags = append(tags, Tag{TagKey: kk, TagValue: v})
	}
	protocol.WriteAWSJSON(w, r, http.StatusOK, map[string]any{
		"Tags":      tags,
		"Truncated": false,
	}, "application/x-amz-json-1.1")
}

// errNotFound returns a KMS NotFoundException.
func errNotFound(keyID string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "NotFoundException",
		Message:    fmt.Sprintf("Invalid keyId %s", keyID),
		HTTPStatus: http.StatusBadRequest,
	}
}

// errDisabled returns a DisabledException.
func errDisabled(keyID string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "DisabledException",
		Message:    fmt.Sprintf("arn:aws:kms key is disabled: %s", keyID),
		HTTPStatus: http.StatusBadRequest,
	}
}

// validateKeyOrigin rejects an Origin the emulator cannot honour. Overcast
// only ever generates AWS_KMS-origin key material — there is no import flow
// backing EXTERNAL (PENDING_IMPORT + ImportKeyMaterial) or a CloudHSM cluster
// backing AWS_CLOUDHSM — so accepting either and silently falling back to
// AWS_KMS would misrepresent the key's actual origin to every later caller.
func validateKeyOrigin(origin string) *protocol.AWSError {
	if origin == "" || origin == "AWS_KMS" {
		return nil
	}
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    fmt.Sprintf("Invalid parameter: Origin %q is not supported by this emulator; only AWS_KMS is emulated.", origin),
		HTTPStatus: http.StatusBadRequest,
	}
}

// errMultiRegionNotSupported reports that a multi-Region key was requested.
// Replica keys (ReplicateKey) and the shared key-material relationship they
// require are not modeled, so honouring MultiRegion=true would silently drop
// the very property that makes the key multi-Region.
func errMultiRegionNotSupported() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "Invalid parameter: MultiRegion=true is not supported by this emulator.",
		HTTPStatus: http.StatusBadRequest,
	}
}
