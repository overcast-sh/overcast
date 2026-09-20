package cloudfront

import (
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── Continuous Deployment Policy: Create ───────────────────────────────────

// CreateContinuousDeploymentPolicy handles POST /2020-05-31/continuous-deployment-policy.
func (h *Handler) CreateContinuousDeploymentPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateContinuousDeploymentPolicy")

	var cfg CDPConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	p := &ContinuousDeploymentPolicy{
		ID:                               id,
		LastModifiedTime:                 now,
		ContinuousDeploymentPolicyConfig: cfg,
		Version:                          1,
	}

	if storeErr := h.store.PutContinuousDeploymentPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("continuous deployment policy created", zap.String("id", id))

	resp := cdpXML{
		ID:                               p.ID,
		LastModifiedTime:                 p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		ContinuousDeploymentPolicyConfig: p.ContinuousDeploymentPolicyConfig,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/continuous-deployment-policy/%s", id))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── Continuous Deployment Policy: Get ──────────────────────────────────────

// GetContinuousDeploymentPolicy handles GET /2020-05-31/continuous-deployment-policy/{id}.
func (h *Handler) GetContinuousDeploymentPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetContinuousDeploymentPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetContinuousDeploymentPolicy").LogStateError(r, "get continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchContinuousDeploymentPolicy(id))
		return
	}

	resp := cdpXML{
		ID:                               p.ID,
		LastModifiedTime:                 p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		ContinuousDeploymentPolicyConfig: p.ContinuousDeploymentPolicyConfig,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Continuous Deployment Policy: GetConfig ────────────────────────────────

// GetContinuousDeploymentPolicyConfig handles GET /2020-05-31/continuous-deployment-policy/{id}/config.
func (h *Handler) GetContinuousDeploymentPolicyConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetContinuousDeploymentPolicy(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetContinuousDeploymentPolicyConfig").LogStateError(r, "get continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchContinuousDeploymentPolicy(id))
		return
	}

	resp := cdpConfigXML{
		StagingDistributionDnsNames: p.ContinuousDeploymentPolicyConfig.StagingDistributionDnsNames,
		Enabled:                     p.ContinuousDeploymentPolicyConfig.Enabled,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Continuous Deployment Policy: Update ───────────────────────────────────

// UpdateContinuousDeploymentPolicy handles PUT /2020-05-31/continuous-deployment-policy/{id}.
func (h *Handler) UpdateContinuousDeploymentPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateContinuousDeploymentPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetContinuousDeploymentPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchContinuousDeploymentPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg CDPConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	p.ContinuousDeploymentPolicyConfig = cfg
	p.LastModifiedTime = h.clk.Now()
	p.Version++

	if storeErr := h.store.PutContinuousDeploymentPolicy(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("continuous deployment policy updated", zap.String("id", id))

	resp := cdpXML{
		ID:                               p.ID,
		LastModifiedTime:                 p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		ContinuousDeploymentPolicyConfig: p.ContinuousDeploymentPolicyConfig,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Continuous Deployment Policy: Delete ───────────────────────────────────

// DeleteContinuousDeploymentPolicy handles DELETE /2020-05-31/continuous-deployment-policy/{id}.
func (h *Handler) DeleteContinuousDeploymentPolicy(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteContinuousDeploymentPolicy")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetContinuousDeploymentPolicy(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchContinuousDeploymentPolicy(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteContinuousDeploymentPolicy(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete continuous deployment policy", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("continuous deployment policy deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Continuous Deployment Policy: List ─────────────────────────────────────

// ListContinuousDeploymentPolicies handles GET /2020-05-31/continuous-deployment-policy.
func (h *Handler) ListContinuousDeploymentPolicies(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListContinuousDeploymentPolicies")

	all, err := h.store.ListContinuousDeploymentPolicies(r.Context())
	if err != nil {
		log.LogStateError(r, "list continuous deployment policies", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]cdpSummary, 0, len(all))
	for _, p := range all {
		summaries = append(summaries, cdpSummary{
			ContinuousDeploymentPolicy: cdpXML{
				ID:                               p.ID,
				LastModifiedTime:                 p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
				ContinuousDeploymentPolicyConfig: p.ContinuousDeploymentPolicyConfig,
			},
		})
	}

	result := cdpListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}
	writeXML(w, r, http.StatusOK, &result)
}
