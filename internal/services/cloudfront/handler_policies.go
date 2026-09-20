package cloudfront

import (
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── Cache Policy: Create ───────────────────────────────────────────────────

// CreateCachePolicy handles POST /2020-05-31/cache-policy.
func (h *Handler) CreateCachePolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateCachePolicy")

	var cfg CachePolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Cache policy name is required.", HTTPStatus: 400,
		})
		return
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	cp := &CachePolicy{
		ID:                id,
		LastModifiedTime:  now,
		CachePolicyConfig: cfg,
		Version:           1,
	}

	if storeErr := h.store.PutCachePolicy(r.Context(), cp); storeErr != nil {
		log.LogStateError(r, "put cache policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("cache policy created", zap.String("id", id), zap.String("name", cfg.Name))

	w.Header().Set("ETag", computeETag(cp.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/cache-policy/%s", id))
	writeXML(w, r, http.StatusCreated, cp)
}

// ─── Cache Policy: Get ──────────────────────────────────────────────────────

// GetCachePolicy handles GET /2020-05-31/cache-policy/{id}.
func (h *Handler) GetCachePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cp, err := h.store.GetCachePolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetCachePolicy").LogStateError(r, "get cache policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if cp == nil {
		writeError(w, r, errNoSuchCachePolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(cp.Version))
	writeXML(w, r, http.StatusOK, cp)
}

// ─── Cache Policy: GetConfig ────────────────────────────────────────────────

// GetCachePolicyConfig handles GET /2020-05-31/cache-policy/{id}/config.
func (h *Handler) GetCachePolicyConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	cp, err := h.store.GetCachePolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetCachePolicyConfig").LogStateError(r, "get cache policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if cp == nil {
		writeError(w, r, errNoSuchCachePolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(cp.Version))
	writeXML(w, r, http.StatusOK, &cp.CachePolicyConfig)
}

// ─── Cache Policy: Update ───────────────────────────────────────────────────

// UpdateCachePolicy handles PUT /2020-05-31/cache-policy/{id}.
func (h *Handler) UpdateCachePolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateCachePolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	cp, err := h.store.GetCachePolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get cache policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if cp == nil {
		writeError(w, r, errNoSuchCachePolicy(id))
		return
	}

	if ifMatch != computeETag(cp.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg CachePolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	cp.CachePolicyConfig = cfg
	cp.LastModifiedTime = h.clk.Now()
	cp.Version++

	if storeErr := h.store.PutCachePolicy(r.Context(), cp); storeErr != nil {
		log.LogStateError(r, "put cache policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("cache policy updated", zap.String("id", id))
	w.Header().Set("ETag", computeETag(cp.Version))
	writeXML(w, r, http.StatusOK, cp)
}

// ─── Cache Policy: Delete ───────────────────────────────────────────────────

// DeleteCachePolicy handles DELETE /2020-05-31/cache-policy/{id}.
func (h *Handler) DeleteCachePolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteCachePolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	cp, err := h.store.GetCachePolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get cache policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if cp == nil {
		writeError(w, r, errNoSuchCachePolicy(id))
		return
	}

	if ifMatch != computeETag(cp.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteCachePolicy(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete cache policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("cache policy deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Cache Policy: List ─────────────────────────────────────────────────────

// ListCachePolicies handles GET /2020-05-31/cache-policy.
func (h *Handler) ListCachePolicies(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListCachePolicies")

	all, err := h.store.ListCachePolicies(r.Context())
	if err != nil {
		log.LogStateError(r, "list cache policies", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]CachePolicySummary, 0, len(all))
	for _, cp := range all {
		summaries = append(summaries, CachePolicySummary{
			Type:        "custom",
			CachePolicy: *cp,
		})
	}

	result := CachePolicyList{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── Origin Request Policy: Create ──────────────────────────────────────────

// CreateOriginRequestPolicy handles POST /2020-05-31/origin-request-policy.
func (h *Handler) CreateOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateOriginRequestPolicy")

	var cfg OriginRequestPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Origin request policy name is required.", HTTPStatus: 400,
		})
		return
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	p := &OriginRequestPolicy{
		ID:                        id,
		LastModifiedTime:          now,
		OriginRequestPolicyConfig: cfg,
		Version:                   1,
	}

	if storeErr := h.store.PutOriginRequestPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put origin request policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin request policy created", zap.String("id", id), zap.String("name", cfg.Name))

	w.Header().Set("ETag", computeETag(p.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/origin-request-policy/%s", id))
	writeXML(w, r, http.StatusCreated, p)
}

// ─── Origin Request Policy: Get ─────────────────────────────────────────────

// GetOriginRequestPolicy handles GET /2020-05-31/origin-request-policy/{id}.
func (h *Handler) GetOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetOriginRequestPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetOriginRequestPolicy").LogStateError(r, "get origin request policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchOriginRequestPolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, p)
}

// ─── Origin Request Policy: GetConfig ───────────────────────────────────────

// GetOriginRequestPolicyConfig handles GET /2020-05-31/origin-request-policy/{id}/config.
func (h *Handler) GetOriginRequestPolicyConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetOriginRequestPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetOriginRequestPolicyConfig").LogStateError(r, "get origin request policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchOriginRequestPolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &p.OriginRequestPolicyConfig)
}

// ─── Origin Request Policy: Update ──────────────────────────────────────────

// UpdateOriginRequestPolicy handles PUT /2020-05-31/origin-request-policy/{id}.
func (h *Handler) UpdateOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateOriginRequestPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetOriginRequestPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get origin request policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchOriginRequestPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg OriginRequestPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	p.OriginRequestPolicyConfig = cfg
	p.LastModifiedTime = h.clk.Now()
	p.Version++

	if storeErr := h.store.PutOriginRequestPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put origin request policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin request policy updated", zap.String("id", id))
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, p)
}

// ─── Origin Request Policy: Delete ──────────────────────────────────────────

// DeleteOriginRequestPolicy handles DELETE /2020-05-31/origin-request-policy/{id}.
func (h *Handler) DeleteOriginRequestPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteOriginRequestPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetOriginRequestPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get origin request policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchOriginRequestPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteOriginRequestPolicy(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete origin request policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin request policy deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Origin Request Policy: List ────────────────────────────────────────────

// ListOriginRequestPolicies handles GET /2020-05-31/origin-request-policy.
func (h *Handler) ListOriginRequestPolicies(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListOriginRequestPolicies")

	all, err := h.store.ListOriginRequestPolicies(r.Context())
	if err != nil {
		log.LogStateError(r, "list origin request policies", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]OriginRequestPolicySummary, 0, len(all))
	for _, p := range all {
		summaries = append(summaries, OriginRequestPolicySummary{
			Type:                "custom",
			OriginRequestPolicy: *p,
		})
	}

	result := OriginRequestPolicyList{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── Response Headers Policy: Create ────────────────────────────────────────

// CreateResponseHeadersPolicy handles POST /2020-05-31/response-headers-policy.
func (h *Handler) CreateResponseHeadersPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateResponseHeadersPolicy")

	var cfg ResponseHeadersPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Response headers policy name is required.", HTTPStatus: 400,
		})
		return
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	p := &ResponseHeadersPolicy{
		ID:                          id,
		LastModifiedTime:            now,
		ResponseHeadersPolicyConfig: cfg,
		Version:                     1,
	}

	if storeErr := h.store.PutResponseHeadersPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put response headers policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("response headers policy created", zap.String("id", id), zap.String("name", cfg.Name))

	w.Header().Set("ETag", computeETag(p.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/response-headers-policy/%s", id))
	writeXML(w, r, http.StatusCreated, p)
}

// ─── Response Headers Policy: Get ───────────────────────────────────────────

// GetResponseHeadersPolicy handles GET /2020-05-31/response-headers-policy/{id}.
func (h *Handler) GetResponseHeadersPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetResponseHeadersPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetResponseHeadersPolicy").LogStateError(r, "get response headers policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchResponseHeadersPolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, p)
}

// ─── Response Headers Policy: GetConfig ─────────────────────────────────────

// GetResponseHeadersPolicyConfig handles GET /2020-05-31/response-headers-policy/{id}/config.
func (h *Handler) GetResponseHeadersPolicyConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetResponseHeadersPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetResponseHeadersPolicyConfig").LogStateError(r, "get response headers policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchResponseHeadersPolicy(id))
		return
	}

	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &p.ResponseHeadersPolicyConfig)
}

// ─── Response Headers Policy: Update ────────────────────────────────────────

// UpdateResponseHeadersPolicy handles PUT /2020-05-31/response-headers-policy/{id}.
func (h *Handler) UpdateResponseHeadersPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateResponseHeadersPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetResponseHeadersPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get response headers policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchResponseHeadersPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg ResponseHeadersPolicyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	p.ResponseHeadersPolicyConfig = cfg
	p.LastModifiedTime = h.clk.Now()
	p.Version++

	if storeErr := h.store.PutResponseHeadersPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put response headers policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("response headers policy updated", zap.String("id", id))
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, p)
}

// ─── Response Headers Policy: Delete ────────────────────────────────────────

// DeleteResponseHeadersPolicy handles DELETE /2020-05-31/response-headers-policy/{id}.
func (h *Handler) DeleteResponseHeadersPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteResponseHeadersPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetResponseHeadersPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get response headers policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchResponseHeadersPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteResponseHeadersPolicy(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete response headers policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("response headers policy deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Response Headers Policy: List ──────────────────────────────────────────

// ListResponseHeadersPolicies handles GET /2020-05-31/response-headers-policy.
func (h *Handler) ListResponseHeadersPolicies(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListResponseHeadersPolicies")

	all, err := h.store.ListResponseHeadersPolicies(r.Context())
	if err != nil {
		log.LogStateError(r, "list response headers policies", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]ResponseHeadersPolicySummary, 0, len(all))
	for _, p := range all {
		summaries = append(summaries, ResponseHeadersPolicySummary{
			Type:                  "custom",
			ResponseHeadersPolicy: *p,
		})
	}

	result := ResponseHeadersPolicyList{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── Legacy OAI: Create ─────────────────────────────────────────────────────

// CreateCloudFrontOriginAccessIdentity handles POST /2020-05-31/origin-access-identity/cloudfront.
func (h *Handler) CreateCloudFrontOriginAccessIdentity(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateCloudFrontOriginAccessIdentity")

	var cfg CloudFrontOriginAccessIdentityConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.CallerReference == "" {
		writeError(w, r, errMissingCallerReference())
		return
	}

	id := "E" + generateAlphanumericID(13)
	// Generate a synthetic S3CanonicalUserId — a 64-char hex string.
	canonicalUserID := generateHexID(64)

	oai := &CloudFrontOriginAccessIdentity{
		ID:                                   id,
		S3CanonicalUserId:                    canonicalUserID,
		CloudFrontOriginAccessIdentityConfig: cfg,
		Version:                              1,
	}

	if storeErr := h.store.PutOAI(r.Context(), oai); storeErr != nil {
		log.LogStateError(r, "put oai", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access identity created", zap.String("id", id))

	w.Header().Set("ETag", computeETag(oai.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/origin-access-identity/cloudfront/%s", id))
	writeXML(w, r, http.StatusCreated, oai)
}

// ─── Legacy OAI: Get ────────────────────────────────────────────────────────

// GetCloudFrontOriginAccessIdentity handles GET /2020-05-31/origin-access-identity/cloudfront/{id}.
func (h *Handler) GetCloudFrontOriginAccessIdentity(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	oai, err := h.store.GetOAI(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetCloudFrontOriginAccessIdentity").LogStateError(r, "get oai", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oai == nil {
		writeError(w, r, errNoSuchCloudFrontOAI(id))
		return
	}

	w.Header().Set("ETag", computeETag(oai.Version))
	writeXML(w, r, http.StatusOK, oai)
}

// ─── Legacy OAI: GetConfig ──────────────────────────────────────────────────

// GetCloudFrontOriginAccessIdentityConfig handles GET /2020-05-31/origin-access-identity/cloudfront/{id}/config.
func (h *Handler) GetCloudFrontOriginAccessIdentityConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	oai, err := h.store.GetOAI(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetCloudFrontOriginAccessIdentityConfig").LogStateError(r, "get oai", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oai == nil {
		writeError(w, r, errNoSuchCloudFrontOAI(id))
		return
	}

	w.Header().Set("ETag", computeETag(oai.Version))
	writeXML(w, r, http.StatusOK, &oai.CloudFrontOriginAccessIdentityConfig)
}

// ─── Legacy OAI: Update ─────────────────────────────────────────────────────

// UpdateCloudFrontOriginAccessIdentity handles PUT /2020-05-31/origin-access-identity/cloudfront/{id}/config.
func (h *Handler) UpdateCloudFrontOriginAccessIdentity(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateCloudFrontOriginAccessIdentity")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	oai, err := h.store.GetOAI(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get oai", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oai == nil {
		writeError(w, r, errNoSuchCloudFrontOAI(id))
		return
	}

	if ifMatch != computeETag(oai.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg CloudFrontOriginAccessIdentityConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	oai.CloudFrontOriginAccessIdentityConfig = cfg
	oai.Version++

	if storeErr := h.store.PutOAI(r.Context(), oai); storeErr != nil {
		log.LogStateError(r, "put oai", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access identity updated", zap.String("id", id))
	w.Header().Set("ETag", computeETag(oai.Version))
	writeXML(w, r, http.StatusOK, oai)
}

// ─── Legacy OAI: Delete ─────────────────────────────────────────────────────

// DeleteCloudFrontOriginAccessIdentity handles DELETE /2020-05-31/origin-access-identity/cloudfront/{id}.
func (h *Handler) DeleteCloudFrontOriginAccessIdentity(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteCloudFrontOriginAccessIdentity")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	oai, err := h.store.GetOAI(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get oai", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oai == nil {
		writeError(w, r, errNoSuchCloudFrontOAI(id))
		return
	}

	if ifMatch != computeETag(oai.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteOAI(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete oai", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access identity deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Legacy OAI: List ───────────────────────────────────────────────────────

// ListCloudFrontOriginAccessIdentities handles GET /2020-05-31/origin-access-identity/cloudfront.
func (h *Handler) ListCloudFrontOriginAccessIdentities(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListCloudFrontOriginAccessIdentities")

	all, err := h.store.ListOAIs(r.Context())
	if err != nil {
		log.LogStateError(r, "list oais", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	marker := r.URL.Query().Get("Marker")
	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)
	page, err := serviceutil.Paginate(all, maxItems, marker, serviceutil.PaginateOptions{DefaultLimit: 100})
	if err != nil {
		writeError(w, r, errInvalidMarker())
		return
	}

	summaries := make([]CloudFrontOriginAccessIdentitySummary, 0, len(page.Items))
	for _, oai := range page.Items {
		summaries = append(summaries, CloudFrontOriginAccessIdentitySummary{
			ID:                oai.ID,
			S3CanonicalUserId: oai.S3CanonicalUserId,
			Comment:           oai.CloudFrontOriginAccessIdentityConfig.Comment,
		})
	}

	result := CloudFrontOriginAccessIdentityList{
		Marker:      marker,
		MaxItems:    maxItems,
		IsTruncated: page.IsTruncated,
		NextMarker:  page.NextToken,
		Quantity:    len(summaries),
		Items:       summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// generateHexID returns n hex characters via crypto/rand.
func generateHexID(n int) string {
	const charset = "0123456789abcdef"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("cloudfront: crypto/rand failed: %v", err))
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}
