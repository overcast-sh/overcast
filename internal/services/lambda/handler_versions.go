package lambda

// handler_versions.go — implemented handlers for Lambda versions and aliases.
//
// Implemented:
//   - PublishVersion              POST   /2015-03-31/functions/{name}/versions
//   - ListVersionsByFunction      GET    /2015-03-31/functions/{name}/versions
//   - CreateAlias                 POST   /2015-03-31/functions/{name}/aliases
//   - GetAlias                    GET    /2015-03-31/functions/{name}/aliases/{aliasName}
//   - ListAliases                 GET    /2015-03-31/functions/{name}/aliases
//   - UpdateAlias                 PUT    /2015-03-31/functions/{name}/aliases/{aliasName}
//   - DeleteAlias                 DELETE /2015-03-31/functions/{name}/aliases/{aliasName}

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ─── wire types ──────────────────────────────────────────────────────────────

// publishVersionRequest mirrors the AWS PublishVersion request body.
// https://docs.aws.amazon.com/lambda/latest/api/API_PublishVersion.html
type publishVersionRequest struct {
	CodeSha256  string          `json:"CodeSha256,omitempty"`
	Description string          `json:"Description,omitempty"`
	RevisionId  string          `json:"RevisionId,omitempty"`
	PublishTo   json.RawMessage `json:"PublishTo"`
}

// unsupportedMembers is PublishVersion's 501 gate. PublishTo refuses for the
// same reason CreateFunction's does — see CreateFunction's gate documentation
// in handler_functions.go — and is the only member here Overcast does not
// implement.
func (req *publishVersionRequest) unsupportedMembers() unsupportedRequestMembers {
	return unsupportedRequestMembers{
		"PublishTo": rawRequestField(req.PublishTo),
	}
}

// listVersionsResponse is the ListVersionsByFunction response envelope. Each
// entry is an ordinary FunctionConfiguration; Version tells $LATEST from the
// published snapshots.
type listVersionsResponse struct {
	Versions   []*functionConfiguration `json:"Versions"`
	NextMarker string                   `json:"NextMarker,omitempty"`
}

// aliasResponse mirrors the AWS AliasConfiguration wire shape.
// https://docs.aws.amazon.com/lambda/latest/api/API_AliasConfiguration.html
type aliasResponse struct {
	AliasArn        string `json:"AliasArn"`
	Name            string `json:"Name"`
	FunctionVersion string `json:"FunctionVersion"`
	Description     string `json:"Description,omitempty"`
	RevisionId      string `json:"RevisionId"`
}

// createAliasRequest mirrors the AWS CreateAlias request body.
type createAliasRequest struct {
	Name            string `json:"Name"`
	FunctionVersion string `json:"FunctionVersion"`
	Description     string `json:"Description,omitempty"`
}

// updateAliasRequest mirrors the AWS UpdateAlias request body.
type updateAliasRequest struct {
	FunctionVersion string `json:"FunctionVersion,omitempty"`
	Description     string `json:"Description,omitempty"`
	RevisionId      string `json:"RevisionId,omitempty"`
}

// listAliasesResponse is the ListAliases response envelope.
type listAliasesResponse struct {
	Aliases    []aliasResponse `json:"Aliases"`
	NextMarker string          `json:"NextMarker,omitempty"`
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// wireSha256 converts an internally stored hex SHA-256 digest to the base64
// form AWS uses in CodeSha256 wire fields. Returns "" for "" or a malformed
// digest, so callers can fall through to computing one.
func wireSha256(hexDigest string) string {
	raw, err := hex.DecodeString(hexDigest)
	if err != nil || len(raw) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// codeSha256 returns the function's CodeSha256 exactly as AWS reports it:
// the **base64**-encoded SHA-256 of the deployment package. The encoding is
// load-bearing, not cosmetic — CDK compares this value against the base64
// hash it computes locally when deciding whether deployed code changed, so a
// hex digest here makes every function look permanently drifted. The stored
// CodeHash keeps its hex form: it doubles as the instance-identity input and
// tar-cache key and never reaches the wire.
// If no zip is stored we hash the ARN + last-modified as a stable stand-in.
func codeSha256(fn *Function) string {
	// The hash setCode stored when the package was written — same digest as
	// hashing CodeZip here, without rehashing the package on every
	// configuration read.
	if fn.CodeHash != "" && fn.CodeSize > 0 {
		if b64 := wireSha256(fn.CodeHash); b64 != "" {
			return b64
		}
	}
	h := sha256.New()
	if len(fn.CodeZip) > 0 {
		h.Write(fn.CodeZip)
	} else {
		// Stable placeholder so repeat calls return the same value.
		h.Write([]byte(fn.ARN + fn.LastModified))
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// versionARN returns the qualified ARN for a specific version number.
func versionARN(baseARN string, version int) string {
	return fmt.Sprintf("%s:%d", baseARN, version)
}

// aliasARN returns the qualified ARN for an alias.
func aliasARN(baseARN, aliasName string) string {
	return fmt.Sprintf("%s:%s", baseARN, aliasName)
}

// versionToResponse converts a FunctionVersion to the wire response shape:
// the snapshot's configuration under the qualified ARN and numeric Version.
func versionToResponse(v *FunctionVersion) *functionConfiguration {
	cfg := functionToConfig(&v.Function)
	cfg.FunctionArn = versionARN(v.ARN, v.Version)
	cfg.Version = strconv.Itoa(v.Version)
	// Use the version-level description when set, else the function's.
	if v.Description != "" {
		cfg.Description = v.Description
	}
	// Override with the version's snapshot CodeSha256 (may differ from $LATEST).
	if v.CodeSha256 != "" {
		cfg.CodeSha256 = v.CodeSha256
	}
	return cfg
}

// newFunctionVersion freezes fn into the immutable snapshot a published
// version is. PublishVersion stores one for an existing function; a
// CreateFunction with Publish=true stores one alongside the function it is
// creating. The version number is not assigned here: the store allocates it
// when the snapshot is committed, so a number is never handed out for a
// snapshot that then fails to land.
func (h *Handler) newFunctionVersion(fn *Function, description string) *FunctionVersion {
	v := &FunctionVersion{
		Function:    *fn,
		Description: description,
		CodeSha256:  codeSha256(fn),
	}
	// The deployment package lives under its own store key, named by
	// CodeHash, and is never embedded in a record — see putFunction. A
	// function read back from the store carries no bytes; one being created
	// still does, and they must not be frozen into the version record.
	v.CodeZip = nil
	// Stamp the publish time and a fresh revision ID for this version.
	v.LastModified = h.clk.Now().UTC().Format(time.RFC3339)
	v.RevisionId = uuid.NewString()
	// A published version is finished by definition — nothing can update it —
	// so it reports Successful even when $LATEST was created without ever
	// carrying the field.
	v.LastUpdateStatus = lastUpdateSuccessful
	v.LastUpdateStatusReason = ""
	v.LastUpdateStatusReasonCode = ""
	return v
}

// versionSnapshotFields zeroes the parts of a Function that must not gate
// PublishVersion's "nothing changed" comparison, leaving only what AWS
// actually snapshots into a version:
//
//   - fields a mutation stamps regardless of what changed: LastModified,
//     RevisionId, CreationID;
//   - fields lifecycle machinery advances on its own: State*, LastUpdateStatus*;
//   - the deployment package's bytes and their derived CodeHash/CodeSize/
//     CodeGeneration — code identity is compared separately via CodeSha256
//     (see functionChangedSinceVersion), because LastModified-fallback hashing
//     for a function with no zip on record would otherwise make every image
//     function's code compare unequal to itself;
//   - Tags and ReservedConcurrency, which are associated with the function's
//     unqualified ARN, not with a published snapshot — TagResource and
//     PutFunctionConcurrency neither one bumps RevisionId or LastModified in
//     this codebase, which is the existing signal that AWS does not treat
//     them as function "configuration" either.
func versionSnapshotFields(fn *Function) Function {
	snap := *fn
	snap.LastModified = ""
	snap.RevisionId = ""
	snap.CreationID = ""
	snap.State = ""
	snap.StateReason = ""
	snap.StateReasonCode = ""
	snap.LastUpdateStatus = ""
	snap.LastUpdateStatusReason = ""
	snap.LastUpdateStatusReasonCode = ""
	snap.CodeZip = nil
	snap.CodeSize = 0
	snap.CodeHash = ""
	snap.CodeGeneration = ""
	snap.Tags = nil
	snap.ReservedConcurrency = nil
	return snap
}

// functionChangedSinceVersion reports whether fn's code or configuration
// differs from the snapshot v froze at publish time — the test PublishVersion
// applies before allocating a new version number: "AWS Lambda doesn't publish
// a version if the function's configuration and code haven't changed since
// the last version" (API_PublishVersion.html).
func functionChangedSinceVersion(fn *Function, v *FunctionVersion) bool {
	if codeSha256(fn) != v.CodeSha256 {
		return true
	}
	return !reflect.DeepEqual(versionSnapshotFields(fn), versionSnapshotFields(&v.Function))
}

// aliasToResponse converts a FunctionAlias to the wire response shape.
func aliasToResponse(a *FunctionAlias) aliasResponse {
	return aliasResponse{
		AliasArn:        a.AliasARN,
		Name:            a.Name,
		FunctionVersion: a.FunctionVersion,
		Description:     a.Description,
		RevisionId:      a.RevisionId,
	}
}

// ─── Versions ─────────────────────────────────────────────────────────────────

// PublishVersion handles POST /2015-03-31/functions/{name}/versions.
// https://docs.aws.amazon.com/lambda/latest/api/API_PublishVersion.html
func (h *Handler) PublishVersion(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("publish version", zap.String("function", name))
	ctx := r.Context()

	var req publishVersionRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "InvalidParameterValueException",
				Message:    "Invalid request body: " + err.Error(),
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
	}
	// unsupportedMembers lists what stays 501 here, and why — PublishTo's one
	// value asks for a $LATEST.PUBLISHED qualifier Lambda Managed Instances
	// resolves unqualified invokes to instead of $LATEST; Managed Instances
	// are not emulated (see CreateFunction's gate), so this is refused before
	// any version is allocated, exactly as CreateFunction and
	// UpdateFunctionCode already refuse the same member.
	if req.unsupportedMembers().requested() {
		protocol.NotImplementedJSON(w, r)
		return
	}

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	// A version is an immutable snapshot of code and configuration, so AWS
	// refuses to take one while an update is still landing. The only such
	// window in Overcast is the image pull an UpdateFunctionCode starts — see
	// the update-lifecycle block in handler_functions.go.
	if functionUpdateInProgress(fn) {
		protocol.WriteJSONError(w, r, lambdaUpdateInProgressConflict(fn.ARN))
		return
	}

	// RevisionId and CodeSha256 are preconditions, checked before deciding
	// whether this call is a no-op publish. AWS's own text: RevisionId "Only
	// update the function if the revision ID matches the ID that's specified"
	// — PreconditionFailedException's own documentation names this exact
	// wording for a mismatch (API_PublishVersion.html). CodeSha256 "Only
	// publish a version if the hash value matches the value that's specified"
	// is not tied to a specific error by AWS's docs; this reports it as
	// InvalidParameterValueException, the family every other CodeSha256/hash
	// mismatch in this codebase uses. Unlike RevisionId's, this half is an
	// inferred behaviour rather than one captured against real AWS.
	if req.RevisionId != "" && req.RevisionId != fn.RevisionId {
		protocol.WriteJSONError(w, r, policyRevisionMismatch())
		return
	}
	if req.CodeSha256 != "" {
		if current := codeSha256(fn); req.CodeSha256 != current {
			protocol.WriteJSONError(w, r, lambdaInvalidParameter(
				"CodeSha256 hash value does not match. New hash value: "+current+", Provided hash value: "+req.CodeSha256))
			return
		}
	}

	v := h.newFunctionVersion(fn, req.Description)
	result, published, aerr := h.ls.publishVersionOrReuse(ctx, fn, v)
	if aerr != nil {
		log.Error("publish version", zap.String("function", name), zap.Error(aerr.Unwrap()))
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if !published {
		log.Debug("publish version: no change since last version, reusing it",
			zap.String("function", name), zap.Int("version", result.Version))
	}

	protocol.WriteRESTJSON(w, r, http.StatusCreated, versionToResponse(result))
}

// ListVersionsByFunction handles GET /2015-03-31/functions/{name}/versions.
// https://docs.aws.amazon.com/lambda/latest/api/API_ListVersionsByFunction.html
func (h *Handler) ListVersionsByFunction(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("list versions", zap.String("function", name))
	ctx := r.Context()

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	versions, aerr := h.ls.listVersions(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// AWS always includes $LATEST as the first entry.
	out := make([]*functionConfiguration, 0, len(versions)+1)
	out = append(out, functionToConfig(fn))
	for _, v := range versions {
		out = append(out, versionToResponse(v))
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, listVersionsResponse{Versions: out})
}

// ─── Aliases ──────────────────────────────────────────────────────────────────

// CreateAlias handles POST /2015-03-31/functions/{name}/aliases.
// https://docs.aws.amazon.com/lambda/latest/api/API_CreateAlias.html
func (h *Handler) CreateAlias(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("create alias", zap.String("function", name))
	ctx := r.Context()

	var req createAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "Invalid request body: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if req.Name == "" || req.FunctionVersion == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "Name and FunctionVersion are required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	// Reject duplicate alias names.
	existing, aerr := h.ls.getAlias(ctx, name, req.Name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if existing != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceConflictException",
			Message:    fmt.Sprintf("Alias already exists: %s for function %s", req.Name, name),
			HTTPStatus: http.StatusConflict,
		})
		return
	}

	a := &FunctionAlias{
		FunctionName:    name,
		Name:            req.Name,
		FunctionVersion: req.FunctionVersion,
		Description:     req.Description,
		AliasARN:        aliasARN(fn.ARN, req.Name),
		RevisionId:      uuid.NewString(),
	}
	if aerr := h.ls.putAlias(ctx, a); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteRESTJSON(w, r, http.StatusCreated, aliasToResponse(a))
}

// GetAlias handles GET /2015-03-31/functions/{name}/aliases/{aliasName}.
// https://docs.aws.amazon.com/lambda/latest/api/API_GetAlias.html
func (h *Handler) GetAlias(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	aliasName := chi.URLParam(r, "aliasName")
	log.Debug("get alias", zap.String("function", name), zap.String("alias", aliasName))
	ctx := r.Context()

	a, aerr := h.ls.getAlias(ctx, name, aliasName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if a == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Function not found: %s:%s", name, aliasName),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, aliasToResponse(a))
}

// UpdateAlias handles PUT /2015-03-31/functions/{name}/aliases/{aliasName}.
// https://docs.aws.amazon.com/lambda/latest/api/API_UpdateAlias.html
func (h *Handler) UpdateAlias(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	aliasName := chi.URLParam(r, "aliasName")
	log.Debug("update alias", zap.String("function", name), zap.String("alias", aliasName))
	ctx := r.Context()

	var req updateAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "Invalid request body: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}

	a, aerr := h.ls.getAlias(ctx, name, aliasName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if a == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Function not found: %s:%s", name, aliasName),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	if req.FunctionVersion != "" {
		a.FunctionVersion = req.FunctionVersion
	}
	if req.Description != "" {
		a.Description = req.Description
	}
	a.RevisionId = uuid.NewString()

	if aerr := h.ls.putAlias(ctx, a); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, aliasToResponse(a))
}

// ListAliases handles GET /2015-03-31/functions/{name}/aliases.
// https://docs.aws.amazon.com/lambda/latest/api/API_ListAliases.html
func (h *Handler) ListAliases(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("list aliases", zap.String("function", name))
	ctx := r.Context()

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	aliases, aerr := h.ls.listAliases(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	out := make([]aliasResponse, 0, len(aliases))
	for _, a := range aliases {
		out = append(out, aliasToResponse(a))
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, listAliasesResponse{Aliases: out})
}

// DeleteAlias handles DELETE /2015-03-31/functions/{name}/aliases/{aliasName}.
// https://docs.aws.amazon.com/lambda/latest/api/API_DeleteAlias.html
func (h *Handler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	aliasName := chi.URLParam(r, "aliasName")
	log.Debug("delete alias", zap.String("function", name), zap.String("alias", aliasName))
	ctx := r.Context()

	a, aerr := h.ls.getAlias(ctx, name, aliasName)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if a == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Function not found: %s:%s", name, aliasName),
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	if aerr := h.ls.deleteAlias(ctx, name, aliasName); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
