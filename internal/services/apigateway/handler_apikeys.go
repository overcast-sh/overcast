package apigateway

// handler_apikeys.go — Authorizers, API Keys, and Usage Plans for REST API v1,
// and Authorizers for HTTP API v2.
//
// Implemented:
//   CreateAuthorizer, GetAuthorizer, GetAuthorizers, DeleteAuthorizer (REST v1)
//   CreateApiKey, GetApiKey, GetApiKeys, DeleteApiKey
//   CreateUsagePlan, GetUsagePlan, GetUsagePlans, DeleteUsagePlan
//   CreateUsagePlanKey, GetUsagePlanKeys, DeleteUsagePlanKey
//   CreateV2Authorizer, GetV2Authorizer, GetV2Authorizers, DeleteV2Authorizer

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- REST API v1: Authorizers -----------------------------------------------

type createAuthorizerRequest struct {
	Name                         string   `json:"name"`
	Type                         string   `json:"type"` // TOKEN, REQUEST, COGNITO_USER_POOLS
	AuthorizerURI                string   `json:"authorizerUri,omitempty"`
	AuthorizerCredentials        string   `json:"authorizerCredentials,omitempty"`
	IdentitySource               string   `json:"identitySource,omitempty"`
	IdentityValidationExpression string   `json:"identityValidationExpression,omitempty"`
	AuthorizerResultTTLInSeconds int      `json:"authorizerResultTtlInSeconds,omitempty"`
	ProviderARNs                 []string `json:"providerARNs,omitempty"`
}

// CreateAuthorizer handles POST /restapis/{restApiId}/authorizers.
func (h *Handler) CreateAuthorizer(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	apiID := chi.URLParam(r, "restApiId")

	// Verify the REST API exists.
	if _, aerr := h.store.getRestAPI(r.Context(), apiID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	var req createAuthorizerRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		protocol.WriteJSONError(w, r, errBadRequest("Authorizer name is required"))
		return
	}
	if req.Type == "" {
		protocol.WriteJSONError(w, r, errBadRequest("Authorizer type is required"))
		return
	}

	auth := &Authorizer{
		ID:                           generateShortID(),
		Name:                         req.Name,
		Type:                         req.Type,
		AuthorizerURI:                req.AuthorizerURI,
		AuthorizerCredentials:        req.AuthorizerCredentials,
		IdentitySource:               req.IdentitySource,
		IdentityValidationExpression: req.IdentityValidationExpression,
		AuthorizerResultTTLInSeconds: req.AuthorizerResultTTLInSeconds,
		ProviderARNs:                 req.ProviderARNs,
	}

	if aerr := h.store.putAuthorizer(r.Context(), apiID, auth); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	log.Info("authorizer created",
		zap.String("api_id", apiID),
		zap.String("authorizer_id", auth.ID),
		zap.String("name", auth.Name),
	)

	protocol.WriteJSON(w, r, http.StatusCreated, auth)
}

// GetAuthorizer handles GET /restapis/{restApiId}/authorizers/{authorizerId}.
func (h *Handler) GetAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "restApiId")
	authID := chi.URLParam(r, "authorizerId")

	auth, aerr := h.store.getAuthorizer(r.Context(), apiID, authID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, auth)
}

// GetAuthorizers handles GET /restapis/{restApiId}/authorizers.
func (h *Handler) GetAuthorizers(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "restApiId")

	auths, aerr := h.store.listAuthorizers(r.Context(), apiID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"item": auths,
	})
}

// DeleteAuthorizer handles DELETE /restapis/{restApiId}/authorizers/{authorizerId}.
func (h *Handler) DeleteAuthorizer(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "restApiId")
	authID := chi.URLParam(r, "authorizerId")

	// Verify it exists before deleting.
	if _, aerr := h.store.getAuthorizer(r.Context(), apiID, authID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if aerr := h.store.deleteAuthorizer(r.Context(), apiID, authID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ---- REST API v1: API Keys ------------------------------------------------

// apiKeyResponse mirrors APIKey but emits createdDate as epoch seconds
// (float64) for AWS SDK compatibility — the same wire format as RestAPI,
// Stage and Deployment. APIKey.CreatedDate is stored internally as UnixMilli.
type apiKeyResponse struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Value       string            `json:"value"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	CreatedDate float64           `json:"createdDate"`
	Tags        map[string]string `json:"tags,omitempty"`
	StageKeys   []string          `json:"stageKeys,omitempty"`
}

func toAPIKeyResponse(k *APIKey) apiKeyResponse {
	return apiKeyResponse{
		ID:          k.ID,
		Name:        k.Name,
		Value:       k.Value,
		Description: k.Description,
		Enabled:     k.Enabled,
		CreatedDate: float64(k.CreatedDate) / 1000.0,
		Tags:        k.Tags,
		StageKeys:   k.StageKeys,
	}
}

func toAPIKeyResponses(ks []*APIKey) []apiKeyResponse {
	out := make([]apiKeyResponse, len(ks))
	for i, k := range ks {
		out[i] = toAPIKeyResponse(k)
	}
	return out
}

type createAPIKeyRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Enabled     bool              `json:"enabled"`
	Tags        map[string]string `json:"tags,omitempty"`
	StageKeys   []string          `json:"stageKeys,omitempty"`
	Value       string            `json:"value,omitempty"` // optional customer-provided value
}

// CreateApiKey handles POST /apikeys.
func (h *Handler) CreateApiKey(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	var req createAPIKeyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		protocol.WriteJSONError(w, r, errBadRequest("API key name is required"))
		return
	}
	if aerr := serviceutil.ValidateTags(apigatewayTagCfg, req.Tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	value := req.Value
	if value == "" {
		// Generate a random 40-character alphanumeric key value.
		value = generateRandomID(20) // 20 bytes = 40 hex chars
	}

	key := &APIKey{
		ID:          generateShortID(),
		Name:        req.Name,
		Description: req.Description,
		Enabled:     req.Enabled,
		Value:       value,
		CreatedDate: h.clk.Now().UnixMilli(),
		Tags:        req.Tags,
		StageKeys:   req.StageKeys,
	}

	if aerr := h.store.putAPIKey(r.Context(), key); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	log.Info("API key created", zap.String("key_id", key.ID), zap.String("name", key.Name))

	protocol.WriteJSON(w, r, http.StatusCreated, toAPIKeyResponse(key))
}

// GetApiKey handles GET /apikeys/{apiKey}.
func (h *Handler) GetApiKey(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "apiKey")

	key, aerr := h.store.getAPIKey(r.Context(), keyID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, toAPIKeyResponse(key))
}

// GetApiKeys handles GET /apikeys.
func (h *Handler) GetApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, aerr := h.store.listAPIKeys(r.Context())
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"item": toAPIKeyResponses(keys),
	})
}

// DeleteApiKey handles DELETE /apikeys/{apiKey}.
func (h *Handler) DeleteApiKey(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "apiKey")

	// Verify it exists before deleting.
	if _, aerr := h.store.getAPIKey(r.Context(), keyID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if aerr := h.store.deleteAPIKey(r.Context(), keyID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ---- REST API v1: Usage Plans ---------------------------------------------

type createUsagePlanRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	APIStages   []UsagePlanStage  `json:"apiStages,omitempty"`
	Throttle    *ThrottleSettings `json:"throttle,omitempty"`
	Quota       *QuotaSettings    `json:"quota,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

// usagePlanWire is the wire-visible projection of a UsagePlan. The modeled
// UsagePlan shape is [id, name, description, apiStages, throttle, quota,
// productCode, tags] — it has no keyIds member, so KeyIDs (stored on
// UsagePlan to back the plan/key association) is deliberately left out
// here. GetUsagePlanKeys is the modeled channel for reading key
// associations back.
type usagePlanWire struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	APIStages   []UsagePlanStage  `json:"apiStages,omitempty"`
	Throttle    *ThrottleSettings `json:"throttle,omitempty"`
	Quota       *QuotaSettings    `json:"quota,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
}

func newUsagePlanWire(p *UsagePlan) usagePlanWire {
	return usagePlanWire{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		APIStages:   p.APIStages,
		Throttle:    p.Throttle,
		Quota:       p.Quota,
		Tags:        p.Tags,
	}
}

// CreateUsagePlan handles POST /usageplans.
//
// Per the AWS API Gateway REST API reference, `name` is optional on
// CreateUsagePlan — if omitted, AWS auto-generates one. This matches the
// behaviour of CDK's `RestApi.addUsagePlan({ apiStages: [...] })`, which
// does not set a name.
func (h *Handler) CreateUsagePlan(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	var req createUsagePlanRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if aerr := serviceutil.ValidateTags(apigatewayTagCfg, req.Tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	planID := generateShortID()
	name := req.Name
	if name == "" {
		name = "plan-" + planID
	}

	plan := &UsagePlan{
		ID:          planID,
		Name:        name,
		Description: req.Description,
		APIStages:   req.APIStages,
		Throttle:    req.Throttle,
		Quota:       req.Quota,
		Tags:        req.Tags,
	}

	if aerr := h.store.putUsagePlan(r.Context(), plan); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	log.Info("usage plan created", zap.String("plan_id", plan.ID), zap.String("name", plan.Name))

	protocol.WriteJSON(w, r, http.StatusCreated, newUsagePlanWire(plan))
}

// GetUsagePlan handles GET /usageplans/{usagePlanId}.
func (h *Handler) GetUsagePlan(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")

	plan, aerr := h.store.getUsagePlan(r.Context(), planID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, newUsagePlanWire(plan))
}

// GetUsagePlans handles GET /usageplans.
func (h *Handler) GetUsagePlans(w http.ResponseWriter, r *http.Request) {
	plans, aerr := h.store.listUsagePlans(r.Context())
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	items := make([]usagePlanWire, 0, len(plans))
	for _, plan := range plans {
		items = append(items, newUsagePlanWire(plan))
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"item": items,
	})
}

// DeleteUsagePlan handles DELETE /usageplans/{usagePlanId}.
func (h *Handler) DeleteUsagePlan(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")

	// Verify it exists before deleting.
	if _, aerr := h.store.getUsagePlan(r.Context(), planID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if aerr := h.store.deleteUsagePlan(r.Context(), planID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// usageResponse mirrors AWS's Usage shape. Note the wire name of the daily
// log map is `values`, not `items` — the AWS model names the member `items`
// but binds it to `values` on the wire, and SDK clients read the latter.
type usageResponse struct {
	UsagePlanID string               `json:"usagePlanId"`
	StartDate   string               `json:"startDate"`
	EndDate     string               `json:"endDate"`
	Values      map[string][][]int64 `json:"values"`
}

// GetUsage handles GET /usageplans/{usagePlanId}/usage.
//
// Returns the daily [used, remaining] log for every API key on the plan over
// the requested inclusive date range, which is how a caller reads back the
// throttle/quota accounting described in usage.go. Counts are held in memory
// and reset when the emulator restarts.
func (h *Handler) GetUsage(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")

	// AWS marks startDate and endDate required on the operation, so a missing
	// one is a request-shape error and is rejected before the plan lookup.
	q := r.URL.Query()
	startRaw, endRaw := q.Get("startDate"), q.Get("endDate")
	if startRaw == "" || endRaw == "" {
		protocol.WriteJSONError(w, r, errBadRequest("startDate and endDate are required"))
		return
	}
	start, err := time.Parse(usageDateLayout, startRaw)
	if err != nil {
		protocol.WriteJSONError(w, r, errBadRequest("Invalid startDate: expected YYYY-MM-DD"))
		return
	}
	end, err := time.Parse(usageDateLayout, endRaw)
	if err != nil {
		protocol.WriteJSONError(w, r, errBadRequest("Invalid endDate: expected YYYY-MM-DD"))
		return
	}
	if end.Before(start) {
		protocol.WriteJSONError(w, r, errBadRequest("endDate must not be before startDate"))
		return
	}
	// The response carries one entry per day per key, so an unbounded range
	// is an unbounded response. AWS does not document a cap; this one exists
	// to keep a query parameter from sizing the reply, and is far wider than
	// the longest quota period.
	if end.Sub(start) > maxUsageRange {
		protocol.WriteJSONError(w, r, errBadRequest("The date range must not exceed 400 days"))
		return
	}

	plan, aerr := h.store.getUsagePlan(r.Context(), planID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	keyIDs := plan.KeyIDs
	if filter := q.Get("keyId"); filter != "" {
		keyIDs = nil
		for _, id := range plan.KeyIDs {
			if id == filter {
				keyIDs = []string{filter}
				break
			}
		}
	}

	values := make(map[string][][]int64, len(keyIDs))
	for _, keyID := range keyIDs {
		days := h.usage.usageFor(usageKey{planID: plan.ID, keyID: keyID}, plan, start, end)
		rows := make([][]int64, 0, len(days))
		for _, d := range days {
			rows = append(rows, []int64{d[0], d[1]})
		}
		values[keyID] = rows
	}

	protocol.WriteJSON(w, r, http.StatusOK, usageResponse{
		UsagePlanID: plan.ID,
		StartDate:   startRaw,
		EndDate:     endRaw,
		Values:      values,
	})
}

// usagePlanKeyRequest is the request body for associating a key with a usage plan.
type usagePlanKeyRequest struct {
	KeyID   string `json:"keyId"`
	KeyType string `json:"keyType"` // "API_KEY"
}

// CreateUsagePlanKey handles POST /usageplans/{usagePlanId}/keys.
func (h *Handler) CreateUsagePlanKey(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")

	plan, aerr := h.store.getUsagePlan(r.Context(), planID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	var req usagePlanKeyRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.KeyID == "" {
		protocol.WriteJSONError(w, r, errBadRequest("keyId is required"))
		return
	}

	// Verify the API key exists.
	key, aerr := h.store.getAPIKey(r.Context(), req.KeyID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	keyType := req.KeyType
	if keyType == "" {
		keyType = "API_KEY"
	}

	// Check for duplicate.
	for _, existing := range plan.KeyIDs {
		if existing == req.KeyID {
			protocol.WriteJSONError(w, r, errConflict("Usage plan key already exists"))
			return
		}
	}

	// Store the key association on the plan.
	plan.KeyIDs = append(plan.KeyIDs, req.KeyID)
	if aerr := h.store.putUsagePlan(r.Context(), plan); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusCreated, map[string]any{
		"id":    key.ID,
		"name":  key.Name,
		"type":  keyType,
		"value": key.Value,
	})
}

// GetUsagePlanKeys handles GET /usageplans/{usagePlanId}/keys.
func (h *Handler) GetUsagePlanKeys(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")

	plan, aerr := h.store.getUsagePlan(r.Context(), planID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	items := make([]map[string]any, 0, len(plan.KeyIDs))
	for _, keyID := range plan.KeyIDs {
		key, aerr := h.store.getAPIKey(r.Context(), keyID)
		if aerr != nil {
			// Key may have been deleted; skip it.
			continue
		}
		items = append(items, map[string]any{
			"id":    key.ID,
			"name":  key.Name,
			"type":  "API_KEY",
			"value": key.Value,
		})
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"item": items,
	})
}

// DeleteUsagePlanKey handles DELETE /usageplans/{usagePlanId}/keys/{keyId}.
func (h *Handler) DeleteUsagePlanKey(w http.ResponseWriter, r *http.Request) {
	planID := chi.URLParam(r, "usagePlanId")
	keyID := chi.URLParam(r, "keyId")

	plan, aerr := h.store.getUsagePlan(r.Context(), planID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	// Remove the key association from the plan.
	updated := plan.KeyIDs[:0]
	found := false
	for _, existing := range plan.KeyIDs {
		if existing == keyID {
			found = true
			continue
		}
		updated = append(updated, existing)
	}
	if !found {
		protocol.WriteJSONError(w, r, errAPIKeyNotFound(keyID))
		return
	}

	plan.KeyIDs = updated
	if aerr := h.store.putUsagePlan(r.Context(), plan); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// ---- HTTP API v2: Authorizers ---------------------------------------------

type createV2AuthorizerRequest struct {
	Name                           string     `json:"name"`
	AuthorizerType                 string     `json:"authorizerType"` // REQUEST, JWT
	IdentitySource                 string     `json:"identitySource,omitempty"`
	AuthorizerURI                  string     `json:"authorizerUri,omitempty"`
	AuthorizerCredentialsArn       string     `json:"authorizerCredentialsArn,omitempty"`
	AuthorizerResultTTLInSeconds   int        `json:"authorizerResultTtlInSeconds,omitempty"`
	JwtConfiguration               *JwtConfig `json:"jwtConfiguration,omitempty"`
	AuthorizerPayloadFormatVersion string     `json:"authorizerPayloadFormatVersion,omitempty"`
	EnableSimpleResponses          bool       `json:"enableSimpleResponses,omitempty"`
}

// CreateV2Authorizer handles POST /v2/apis/{apiId}/authorizers.
func (h *Handler) CreateV2Authorizer(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	apiID := chi.URLParam(r, "apiId")

	// Verify the API exists.
	if _, aerr := h.store.getV2API(r.Context(), apiID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	var req createV2AuthorizerRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		protocol.WriteJSONError(w, r, errBadRequest("Authorizer name is required"))
		return
	}
	if req.AuthorizerType == "" {
		protocol.WriteJSONError(w, r, errBadRequest("Authorizer type is required"))
		return
	}

	auth := &AuthorizerV2{
		AuthorizerID:                   generateShortID(),
		Name:                           req.Name,
		AuthorizerType:                 req.AuthorizerType,
		IdentitySource:                 req.IdentitySource,
		AuthorizerURI:                  req.AuthorizerURI,
		AuthorizerCredentialsArn:       req.AuthorizerCredentialsArn,
		AuthorizerResultTTLInSeconds:   req.AuthorizerResultTTLInSeconds,
		JwtConfiguration:               req.JwtConfiguration,
		AuthorizerPayloadFormatVersion: req.AuthorizerPayloadFormatVersion,
		EnableSimpleResponses:          req.EnableSimpleResponses,
	}

	if aerr := h.store.putV2Authorizer(r.Context(), apiID, auth); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	log.Info("v2 authorizer created",
		zap.String("api_id", apiID),
		zap.String("authorizer_id", auth.AuthorizerID),
		zap.String("name", auth.Name),
	)

	protocol.WriteJSON(w, r, http.StatusCreated, auth)
}

// GetV2Authorizer handles GET /v2/apis/{apiId}/authorizers/{authorizerId}.
func (h *Handler) GetV2Authorizer(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")
	authID := chi.URLParam(r, "authorizerId")

	auth, aerr := h.store.getV2Authorizer(r.Context(), apiID, authID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, auth)
}

// GetV2Authorizers handles GET /v2/apis/{apiId}/authorizers.
func (h *Handler) GetV2Authorizers(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")

	auths, aerr := h.store.listV2Authorizers(r.Context(), apiID)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"items": auths,
	})
}

// DeleteV2Authorizer handles DELETE /v2/apis/{apiId}/authorizers/{authorizerId}.
func (h *Handler) DeleteV2Authorizer(w http.ResponseWriter, r *http.Request) {
	apiID := chi.URLParam(r, "apiId")
	authID := chi.URLParam(r, "authorizerId")

	// Verify it exists before deleting.
	if _, aerr := h.store.getV2Authorizer(r.Context(), apiID, authID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if aerr := h.store.deleteV2Authorizer(r.Context(), apiID, authID); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
