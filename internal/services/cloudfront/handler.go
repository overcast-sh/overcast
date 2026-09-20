package cloudfront

import (
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Handler implements the CloudFront REST-XML API handlers.
type Handler struct {
	cfg   *config.Config
	store *Store
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	bus   *events.Bus
	cache *cfCache

	// originHosts decides whether an origin's DomainName is one this emulator
	// serves. It is the same classifier the router uses on inbound requests, so
	// origin resolution and request routing cannot disagree about a hostname.
	originHosts *middleware.HostClassifier

	// tlsPolicyWarnOnce guards the one-time warning emitted when a
	// distribution asks for an HTTPS-only viewer protocol that this server
	// cannot serve. Once per process, not per request: a proxied page pulls
	// dozens of assets through the same distribution.
	tlsPolicyWarnOnce sync.Once
}

func newHandler(cfg *config.Config, store *Store, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	var hostname string
	if cfg != nil {
		hostname = cfg.Hostname
	}
	return &Handler{
		cfg:         cfg,
		store:       store,
		log:         log,
		clk:         clk,
		cache:       newCFCache(clk),
		originHosts: middleware.NewHostClassifier(hostname),
	}
}

// accountID returns the configured AWS account ID, falling back to the standard
// emulator account when no config is attached (unit-constructed handlers).
// Distribution ARNs already read config via protocol.DistributionARN; this gives
// the function and realtime-log-config ARNs the same behaviour, so every ARN one
// server hands out names the same account.
func (h *Handler) accountID() string {
	if h.cfg != nil && h.cfg.AccountID != "" {
		return h.cfg.AccountID
	}
	return "000000000000"
}

// ─── CreateDistribution ─────────────────────────────────────────────────────

// CreateDistribution handles POST /2020-05-31/distribution.
func (h *Handler) CreateDistribution(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateDistribution")

	cfg, err := decodeDistributionConfig(r)
	if err != nil {
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

	ctx := r.Context()

	// CallerReference idempotency — same ref + equivalent config → return existing.
	existing, storeErr := h.store.FindByCallerRef(ctx, cfg.CallerReference)
	if storeErr != nil {
		log.LogStateError(r, "find by caller ref", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if existing != nil {
		if distributionConfigsEqual(&existing.DistributionConfig, cfg) {
			// Idempotent — return the existing distribution.
			w.Header().Set("ETag", computeETag(existing.Version))
			w.Header().Set("Location", fmt.Sprintf("/2020-05-31/distribution/%s", existing.ID))
			writeXML(w, r, http.StatusCreated, existing)
			return
		}
		writeError(w, r, errDistributionAlreadyExists(cfg.CallerReference))
		return
	}

	validateAndNormalizeConfig(cfg)
	if aerr := validateOriginRefs(cfg); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	id := generateDistributionID()
	now := h.clk.Now()

	dist := &Distribution{
		ID:                            id,
		ARN:                           protocol.DistributionARN(h.cfg.AccountID, id),
		Status:                        "Deployed",
		DomainName:                    h.distributionDomainName(r, id),
		LastModifiedTime:              now,
		InProgressInvalidationBatches: 0,
		ActiveTrustedSigners:          &ActiveTrustedList{Enabled: false, Quantity: 0},
		ActiveTrustedKeyGroups:        &ActiveTrustedList{Enabled: false, Quantity: 0},
		DistributionConfig:            *cfg,
		Version:                       1,
	}

	if storeErr := h.store.PutDistribution(ctx, dist); storeErr != nil {
		log.LogStateError(r, "put distribution", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("distribution created", zap.String("id", id), zap.String("arn", dist.ARN))
	h.publish(r, events.CloudFrontDistributionCreated, events.ResourcePayload{Name: id, ARN: dist.ARN})

	w.Header().Set("ETag", computeETag(dist.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/distribution/%s", id))
	writeXML(w, r, http.StatusCreated, dist)
}

// ─── GetDistribution ────────────────────────────────────────────────────────

// GetDistribution handles GET /2020-05-31/distribution/{id}.
func (h *Handler) GetDistribution(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dist, aerr := h.requireDistribution(r, id)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	w.Header().Set("ETag", computeETag(dist.Version))
	writeXML(w, r, http.StatusOK, dist)
}

// ─── GetDistributionConfig ──────────────────────────────────────────────────

// GetDistributionConfig handles GET /2020-05-31/distribution/{id}/config.
func (h *Handler) GetDistributionConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dist, aerr := h.requireDistribution(r, id)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	w.Header().Set("ETag", computeETag(dist.Version))
	writeXML(w, r, http.StatusOK, &dist.DistributionConfig)
}

// ─── UpdateDistribution ─────────────────────────────────────────────────────

// UpdateDistribution handles PUT /2020-05-31/distribution/{id}/config.
func (h *Handler) UpdateDistribution(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateDistribution")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	dist, aerr := h.requireDistribution(r, id)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}

	if ifMatch != computeETag(dist.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	cfg, err := decodeDistributionConfig(r)
	if err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.CallerReference != "" && cfg.CallerReference != dist.DistributionConfig.CallerReference {
		writeError(w, r, &protocol.AWSError{
			Code:       "InvalidArgument",
			Message:    "The CallerReference cannot be changed after a distribution is created",
			HTTPStatus: 400,
		})
		return
	}

	validateAndNormalizeConfig(cfg)
	if aerr := validateOriginRefs(cfg); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	dist.DistributionConfig = *cfg
	dist.LastModifiedTime = h.clk.Now()
	dist.Version++

	if storeErr := h.store.PutDistribution(r.Context(), dist); storeErr != nil {
		log.LogStateError(r, "update distribution", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("distribution updated", zap.String("id", id))
	h.publish(r, events.CloudFrontDistributionUpdated, events.ResourcePayload{Name: id, ARN: dist.ARN})

	w.Header().Set("ETag", computeETag(dist.Version))
	writeXML(w, r, http.StatusOK, dist)
}

// ─── DeleteDistribution ─────────────────────────────────────────────────────

// DeleteDistribution handles DELETE /2020-05-31/distribution/{id}.
func (h *Handler) DeleteDistribution(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteDistribution")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	dist, aerr := h.requireDistribution(r, id)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}

	if ifMatch != computeETag(dist.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if dist.DistributionConfig.Enabled {
		writeError(w, r, errDistributionNotDisabled(id))
		return
	}

	ctx := r.Context()
	if storeErr := h.store.DeleteAllInvalidations(ctx, id); storeErr != nil {
		log.LogStateError(r, "delete invalidations", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	if storeErr := h.store.DeleteDistribution(ctx, id); storeErr != nil {
		log.LogStateError(r, "delete distribution", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	// Clear all cached proxy responses for this distribution.
	h.cache.invalidateAll(id)
	log.Info("distribution deleted", zap.String("id", id))
	h.publish(r, events.CloudFrontDistributionDeleted, events.ResourcePayload{Name: id, ARN: dist.ARN})

	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── ListDistributions ──────────────────────────────────────────────────────

// ListDistributions handles GET /2020-05-31/distribution.
func (h *Handler) ListDistributions(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListDistributions")

	all, err := h.store.ListDistributions(r.Context())
	if err != nil {
		log.LogStateError(r, "list distributions", protocol.Wrap(protocol.ErrInternalError, err))
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

	summaries := make([]DistributionSummary, 0, len(page.Items))
	for _, d := range page.Items {
		summaries = append(summaries, distributionToSummary(d))
	}

	result := DistributionList{
		Marker:      marker,
		MaxItems:    maxItems,
		IsTruncated: page.IsTruncated,
		NextMarker:  page.NextToken,
		Quantity:    len(summaries),
		Items:       summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── CreateInvalidation ─────────────────────────────────────────────────────

// CreateInvalidation handles POST /2020-05-31/distribution/{id}/invalidation.
func (h *Handler) CreateInvalidation(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateInvalidation")

	distID := chi.URLParam(r, "id")
	if _, aerr := h.requireDistribution(r, distID); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	var batch InvalidationBatch
	if err := xml.NewDecoder(r.Body).Decode(&batch); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if batch.CallerReference == "" {
		writeError(w, r, errMissingCallerReference())
		return
	}

	// Validate paths: tag paths must start with "#" and contain a valid tag.
	for _, p := range batch.Paths.Items {
		if strings.HasPrefix(p, "#") {
			tag := p[1:]
			if !isValidCacheTag(tag) {
				writeError(w, r, &protocol.AWSError{
					Code:       "InvalidArgument",
					Message:    fmt.Sprintf("Invalid cache tag: %s", tag),
					HTTPStatus: 400,
				})
				return
			}
		}
	}

	id := generateInvalidationID()
	now := h.clk.Now()

	inv := &Invalidation{
		ID:                id,
		Status:            "Completed",
		CreateTime:        now,
		InvalidationBatch: batch,
	}

	if storeErr := h.store.PutInvalidation(r.Context(), distID, inv); storeErr != nil {
		log.LogStateError(r, "put invalidation", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	// Invalidate cached proxy responses for each path in the batch.
	// Tag paths (prefixed with "#") invalidate by cache tag.
	for _, p := range batch.Paths.Items {
		h.cache.invalidate(distID, p)
	}

	log.Info("invalidation created", zap.String("distribution", distID), zap.String("id", id))
	h.publish(r, events.CloudFrontInvalidationCreated, events.ResourcePayload{Name: id, ARN: distID})

	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/distribution/%s/invalidation/%s", distID, id))
	writeXML(w, r, http.StatusCreated, inv)
}

// ─── GetInvalidation ────────────────────────────────────────────────────────

// GetInvalidation handles GET /2020-05-31/distribution/{id}/invalidation/{invalidationId}.
func (h *Handler) GetInvalidation(w http.ResponseWriter, r *http.Request) {
	distID := chi.URLParam(r, "id")
	invID := chi.URLParam(r, "invalidationId")

	if _, aerr := h.requireDistribution(r, distID); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	inv, err := h.store.GetInvalidation(r.Context(), distID, invID)
	if err != nil {
		h.log.WithOperation("GetInvalidation").LogStateError(r, "get invalidation", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if inv == nil {
		writeError(w, r, errNoSuchInvalidation(invID))
		return
	}

	writeXML(w, r, http.StatusOK, inv)
}

// ─── ListInvalidations ──────────────────────────────────────────────────────

// ListInvalidations handles GET /2020-05-31/distribution/{id}/invalidation.
func (h *Handler) ListInvalidations(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListInvalidations")

	distID := chi.URLParam(r, "id")
	if _, aerr := h.requireDistribution(r, distID); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	all, err := h.store.ListInvalidations(r.Context(), distID)
	if err != nil {
		log.LogStateError(r, "list invalidations", protocol.Wrap(protocol.ErrInternalError, err))
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

	summaries := make([]InvalidationSummary, 0, len(page.Items))
	for _, inv := range page.Items {
		summaries = append(summaries, InvalidationSummary{
			ID:         inv.ID,
			CreateTime: inv.CreateTime,
			Status:     inv.Status,
		})
	}

	result := InvalidationList{
		Marker:      marker,
		MaxItems:    maxItems,
		IsTruncated: page.IsTruncated,
		NextMarker:  page.NextToken,
		Quantity:    len(summaries),
		Items:       summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── TagResource ────────────────────────────────────────────────────────────

// cloudfrontTagCfg tunes the shared tag validator to CloudFront's error
// shape (#1052). CloudFront reports its other rejected-input cases on this
// path (a missing ARN, malformed XML) as InvalidArgument, and declares no
// dedicated tag-count exception, so the 50-tag limit is reported the same
// way an invalid key or value is.
var cloudfrontTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidArgument",
	InvalidCode:     "InvalidArgument",
	ExceededMessage: "Invalid tag: too many tags for this resource.",
}

func cloudfrontTagsToMap(tags []Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

// TagResource handles POST /2020-05-31/tagging?Operation=Tag&Resource={arn}.
func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("TagResource")

	arn := r.URL.Query().Get("Resource")
	if arn == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Resource ARN is required.", HTTPStatus: 400,
		})
		return
	}

	var body Tags
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	ctx := r.Context()

	// Merge with existing tags.
	existing, err := h.store.GetTags(ctx, arn)
	if err != nil {
		log.LogStateError(r, "get tags", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if existing == nil {
		existing = &Tags{}
	}

	// Build map of existing tags, then overlay new ones.
	tagMap := make(map[string]string, len(existing.Items))
	for _, t := range existing.Items {
		tagMap[t.Key] = t.Value
	}
	for _, t := range body.Items {
		tagMap[t.Key] = t.Value
	}

	// Validated against the merged set, not just the incoming delta, so the
	// 50-tag limit holds across repeated TagResource calls (#1052).
	if aerr := serviceutil.ValidateTags(cloudfrontTagCfg, tagMap); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	merged := make([]Tag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, Tag{Key: k, Value: v})
	}
	existing.Items = merged

	if storeErr := h.store.PutTags(ctx, arn, existing); storeErr != nil {
		log.LogStateError(r, "put tags", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── UntagResource ──────────────────────────────────────────────────────────

// UntagResource handles POST /2020-05-31/tagging?Operation=Untag&Resource={arn}.
func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UntagResource")

	arn := r.URL.Query().Get("Resource")
	if arn == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Resource ARN is required.", HTTPStatus: 400,
		})
		return
	}

	var keys TagKeys
	if err := xml.NewDecoder(r.Body).Decode(&keys); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	ctx := r.Context()

	existing, err := h.store.GetTags(ctx, arn)
	if err != nil {
		log.LogStateError(r, "get tags", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if existing == nil {
		// Nothing to untag.
		protocol.WriteEmpty(w, r, http.StatusNoContent)
		return
	}

	removeSet := make(map[string]struct{}, len(keys.Items))
	for _, k := range keys.Items {
		removeSet[k] = struct{}{}
	}

	filtered := make([]Tag, 0, len(existing.Items))
	for _, t := range existing.Items {
		if _, remove := removeSet[t.Key]; !remove {
			filtered = append(filtered, t)
		}
	}
	existing.Items = filtered

	if storeErr := h.store.PutTags(ctx, arn, existing); storeErr != nil {
		log.LogStateError(r, "put tags", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── ListTagsForResource ────────────────────────────────────────────────────

// ListTagsForResource handles GET /2020-05-31/tagging?Resource={arn}.
func (h *Handler) ListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := r.URL.Query().Get("Resource")
	if arn == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Resource ARN is required.", HTTPStatus: 400,
		})
		return
	}

	tags, err := h.store.GetTags(r.Context(), arn)
	if err != nil {
		h.log.WithOperation("ListTagsForResource").LogStateError(r, "get tags", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if tags == nil {
		tags = &Tags{}
	}

	writeXML(w, r, http.StatusOK, &Tagging{Tags: *tags})
}

// ─── CreateDistributionWithTags ─────────────────────────────────────────────

// CreateDistributionWithTags handles POST /2020-05-31/distribution?WithTags.
func (h *Handler) CreateDistributionWithTags(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateDistributionWithTags")

	var body DistributionConfigWithTags
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	cfg := &body.DistributionConfig
	if cfg.CallerReference == "" {
		writeError(w, r, errMissingCallerReference())
		return
	}
	// Request-shape validation before the distribution is created — the
	// same ordering createLogGroupTyped uses
	// (internal/services/cloudwatch/logs/typed_logic.go) — so a rejected
	// create leaves no distribution behind with nothing able to fix its
	// tags (#1052).
	if aerr := serviceutil.ValidateTags(cloudfrontTagCfg, cloudfrontTagsToMap(body.Tags.Items)); aerr != nil {
		writeError(w, r, aerr)
		return
	}

	ctx := r.Context()

	// CallerReference idempotency.
	existing, storeErr := h.store.FindByCallerRef(ctx, cfg.CallerReference)
	if storeErr != nil {
		log.LogStateError(r, "find by caller ref", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if existing != nil {
		if distributionConfigsEqual(&existing.DistributionConfig, cfg) {
			w.Header().Set("ETag", computeETag(existing.Version))
			w.Header().Set("Location", fmt.Sprintf("/2020-05-31/distribution/%s", existing.ID))
			writeXML(w, r, http.StatusCreated, existing)
			return
		}
		writeError(w, r, errDistributionAlreadyExists(cfg.CallerReference))
		return
	}

	// Support _custom_id_ tag for deterministic IDs (LocalStack compat).
	id := ""
	for _, tag := range body.Tags.Items {
		if tag.Key == "_custom_id_" && tag.Value != "" {
			id = tag.Value
			break
		}
	}
	if id == "" {
		id = generateDistributionID()
	}

	now := h.clk.Now()

	dist := &Distribution{
		ID:                            id,
		ARN:                           protocol.DistributionARN(h.cfg.AccountID, id),
		Status:                        "Deployed",
		DomainName:                    h.distributionDomainName(r, id),
		LastModifiedTime:              now,
		InProgressInvalidationBatches: 0,
		ActiveTrustedSigners:          &ActiveTrustedList{Enabled: false, Quantity: 0},
		ActiveTrustedKeyGroups:        &ActiveTrustedList{Enabled: false, Quantity: 0},
		DistributionConfig:            *cfg,
		Version:                       1,
	}

	if storeErr := h.store.PutDistribution(ctx, dist); storeErr != nil {
		log.LogStateError(r, "put distribution", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	// Store tags if any.
	if len(body.Tags.Items) > 0 {
		if storeErr := h.store.PutTags(ctx, dist.ARN, &body.Tags); storeErr != nil {
			log.LogStateError(r, "put tags", protocol.Wrap(protocol.ErrInternalError, storeErr))
			// Distribution created but tags failed — still return success.
		}
	}

	log.Info("distribution with tags created", zap.String("id", id), zap.String("arn", dist.ARN))
	h.publish(r, events.CloudFrontDistributionCreated, events.ResourcePayload{Name: id, ARN: dist.ARN})

	w.Header().Set("ETag", computeETag(dist.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/distribution/%s", id))
	writeXML(w, r, http.StatusCreated, dist)
}

// ─── OAC: CreateOriginAccessControl ─────────────────────────────────────────

// CreateOriginAccessControl handles POST /2020-05-31/origin-access-control.
func (h *Handler) CreateOriginAccessControl(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateOriginAccessControl")

	var body OriginAccessControlConfig
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	id := generateOACID()

	oac := &OriginAccessControl{
		ID:                        id,
		OriginAccessControlConfig: body,
		Version:                   1,
	}

	if storeErr := h.store.PutOAC(r.Context(), oac); storeErr != nil {
		log.LogStateError(r, "put oac", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access control created", zap.String("id", id))

	w.Header().Set("ETag", computeETag(oac.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/origin-access-control/%s", id))
	writeXML(w, r, http.StatusCreated, oac)
}

// ─── OAC: GetOriginAccessControl ────────────────────────────────────────────

// GetOriginAccessControl handles GET /2020-05-31/origin-access-control/{id}.
func (h *Handler) GetOriginAccessControl(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	oac, err := h.store.GetOAC(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetOriginAccessControl").LogStateError(r, "get oac", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oac == nil {
		writeError(w, r, errNoSuchOriginAccessControl(id))
		return
	}

	w.Header().Set("ETag", computeETag(oac.Version))
	writeXML(w, r, http.StatusOK, oac)
}

// ─── OAC: UpdateOriginAccessControl ─────────────────────────────────────────

// UpdateOriginAccessControl handles PUT /2020-05-31/origin-access-control/{id}/config.
func (h *Handler) UpdateOriginAccessControl(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateOriginAccessControl")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	oac, err := h.store.GetOAC(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get oac", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oac == nil {
		writeError(w, r, errNoSuchOriginAccessControl(id))
		return
	}

	if ifMatch != computeETag(oac.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var body OriginAccessControlConfig
	if err := xml.NewDecoder(r.Body).Decode(&body); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	oac.OriginAccessControlConfig = body
	oac.Version++

	if storeErr := h.store.PutOAC(r.Context(), oac); storeErr != nil {
		log.LogStateError(r, "put oac", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access control updated", zap.String("id", id))
	w.Header().Set("ETag", computeETag(oac.Version))
	writeXML(w, r, http.StatusOK, oac)
}

// ─── OAC: DeleteOriginAccessControl ─────────────────────────────────────────

// DeleteOriginAccessControl handles DELETE /2020-05-31/origin-access-control/{id}.
func (h *Handler) DeleteOriginAccessControl(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteOriginAccessControl")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	oac, err := h.store.GetOAC(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get oac", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if oac == nil {
		writeError(w, r, errNoSuchOriginAccessControl(id))
		return
	}

	if ifMatch != computeETag(oac.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteOAC(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete oac", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("origin access control deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── OAC: ListOriginAccessControls ──────────────────────────────────────────

// ListOriginAccessControls handles GET /2020-05-31/origin-access-control.
func (h *Handler) ListOriginAccessControls(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListOriginAccessControls")

	all, err := h.store.ListOACs(r.Context())
	if err != nil {
		log.LogStateError(r, "list oacs", protocol.Wrap(protocol.ErrInternalError, err))
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

	summaries := make([]OriginAccessControlSummary, 0, len(page.Items))
	for _, oac := range page.Items {
		summaries = append(summaries, OriginAccessControlSummary{
			ID:                            oac.ID,
			Name:                          oac.OriginAccessControlConfig.Name,
			Description:                   oac.OriginAccessControlConfig.Description,
			SigningProtocol:               oac.OriginAccessControlConfig.SigningProtocol,
			SigningBehavior:               oac.OriginAccessControlConfig.SigningBehavior,
			OriginAccessControlOriginType: oac.OriginAccessControlConfig.OriginAccessControlOriginType,
		})
	}

	result := OriginAccessControlList{
		Marker:      marker,
		MaxItems:    maxItems,
		IsTruncated: page.IsTruncated,
		NextMarker:  page.NextToken,
		Quantity:    len(summaries),
		Items:       summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// requireDistribution loads a distribution or returns an AWSError.
func (h *Handler) requireDistribution(r *http.Request, id string) (*Distribution, *protocol.AWSError) {
	dist, err := h.store.GetDistribution(r.Context(), id)
	if err != nil {
		h.log.WithOperation("requireDistribution").LogStateError(r, "get distribution", protocol.Wrap(protocol.ErrInternalError, err))
		return nil, protocol.ErrInternalError
	}
	if dist == nil {
		return nil, errDistributionNotFound(id)
	}
	return dist, nil
}

// publish sends an event if the bus is wired.
func (h *Handler) publish(r *http.Request, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(r.Context(), events.Event{
			Type:    t,
			Source:  serviceName,
			Payload: payload,
		})
	}
}

// generateDistributionID creates a 14-character distribution ID: 'E' + 13 uppercase alphanumeric.
// Matches observed AWS pattern (e.g. E1PQRS2T3U4V5W).
func generateDistributionID() string {
	return "E" + generateAlphanumericID(13)
}

// generateInvalidationID creates a 14-character invalidation ID: 'I' + 13 uppercase alphanumeric.
func generateInvalidationID() string {
	return "I" + generateAlphanumericID(13)
}

// generateOACID creates an OAC ID: 'E' + 13 uppercase alphanumeric.
func generateOACID() string {
	return "E" + generateAlphanumericID(13)
}

// generateAlphanumericID returns n uppercase alphanumeric characters via crypto/rand.
func generateAlphanumericID(n int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("cloudfront: crypto/rand failed: %v", err))
	}
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}

// computeETag returns a quoted version string for use as an HTTP ETag header value.
func computeETag(version int) string {
	return fmt.Sprintf(`"%d"`, version)
}

// decodeDistributionConfig reads and decodes an XML DistributionConfig from the request body.
func decodeDistributionConfig(r *http.Request) (*DistributionConfig, error) {
	var cfg DistributionConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode DistributionConfig: %w", err)
	}
	return &cfg, nil
}

// distributionToSummary converts a Distribution to a DistributionSummary for list responses.
func distributionToSummary(d *Distribution) DistributionSummary {
	return DistributionSummary{
		ID:                   d.ID,
		ARN:                  d.ARN,
		Status:               d.Status,
		DomainName:           d.DomainName,
		LastModifiedTime:     d.LastModifiedTime,
		Comment:              d.DistributionConfig.Comment,
		Enabled:              d.DistributionConfig.Enabled,
		Origins:              d.DistributionConfig.Origins,
		DefaultCacheBehavior: d.DistributionConfig.DefaultCacheBehavior,
		CacheBehaviors:       d.DistributionConfig.CacheBehaviors,
		Aliases:              d.DistributionConfig.Aliases,
		PriceClass:           d.DistributionConfig.PriceClass,
		ViewerCertificate:    d.DistributionConfig.ViewerCertificate,
		Restrictions:         d.DistributionConfig.Restrictions,
		WebACLId:             d.DistributionConfig.WebACLId,
		HttpVersion:          d.DistributionConfig.HttpVersion,
		IsIPV6Enabled:        d.DistributionConfig.IsIPV6Enabled,
	}
}

// distributionConfigsEqual compares two DistributionConfig values for CallerReference
// idempotency checking. XMLName is zeroed before comparison because XML decoding
// populates it (with namespace) while JSON roundtrip through the store does not.
// Uses reflect.DeepEqual which is acceptable here since this path only triggers
// when a duplicate CallerReference is detected (rare path).
func distributionConfigsEqual(a, b *DistributionConfig) bool {
	ac, bc := *a, *b
	ac.XMLName = xml.Name{}
	bc.XMLName = xml.Name{}
	return reflect.DeepEqual(&ac, &bc)
}

// validateAndNormalizeConfig silently corrects Quantity fields so they match
// the actual length of their Items slices, matching real AWS behaviour.
func validateAndNormalizeConfig(cfg *DistributionConfig) {
	cfg.Origins.Quantity = len(cfg.Origins.Items)
	if cfg.CacheBehaviors != nil {
		cfg.CacheBehaviors.Quantity = len(cfg.CacheBehaviors.Items)
	}
}

// validateOriginRefs checks that Origins is non-empty and that every
// TargetOriginId in DefaultCacheBehavior and CacheBehaviors references a
// declared origin or origin group. Per AWS CloudFront origin failover docs,
// cache behaviors target an OriginGroup Id to enable failover.
func validateOriginRefs(cfg *DistributionConfig) *protocol.AWSError {
	if cfg.Origins.Quantity == 0 || len(cfg.Origins.Items) == 0 {
		return &protocol.AWSError{
			Code:       "InvalidArgument",
			Message:    "Origins: the Origins element is required",
			HTTPStatus: 400,
		}
	}

	originSet := make(map[string]struct{}, len(cfg.Origins.Items))
	for _, o := range cfg.Origins.Items {
		originSet[o.ID] = struct{}{}
	}

	targetSet := make(map[string]struct{}, len(originSet))
	for id := range originSet {
		targetSet[id] = struct{}{}
	}
	if cfg.OriginGroups != nil {
		for _, group := range cfg.OriginGroups.Items {
			targetSet[group.ID] = struct{}{}
		}
	}

	if _, ok := targetSet[cfg.DefaultCacheBehavior.TargetOriginId]; !ok {
		return &protocol.AWSError{
			Code:       "InvalidArgument",
			Message:    "The specified TargetOriginId does not reference a valid origin",
			HTTPStatus: 400,
		}
	}

	if cfg.CacheBehaviors != nil {
		for _, cb := range cfg.CacheBehaviors.Items {
			if _, ok := targetSet[cb.TargetOriginId]; !ok {
				return &protocol.AWSError{
					Code:       "InvalidArgument",
					Message:    "The specified TargetOriginId does not reference a valid origin",
					HTTPStatus: 400,
				}
			}
		}
	}

	return nil
}

// distributionDomainName mints the hostname a client uses to reach this
// distribution. Real AWS returns "{id}.cloudfront.net"; Overcast returns
// "{id}.cloudfront.{host}" on the hostname the caller reached it on, because
// cloudfront.net is a fixed AWS domain Overcast cannot serve without a DNS
// override — returning it verbatim would advertise a name that resolves away
// from the emulator.
//
// The "cloudfront" label is the same one the router dispatches on, so the
// minted name routes back to this distribution. CloudFront is global, so there
// is no region segment.
func (h *Handler) distributionDomainName(r *http.Request, id string) string {
	return serviceutil.HostRoutedHostname(h.cfg, r, middleware.LabelCloudFront, id, "")
}

// distributionIDFromDomainName recovers the distribution ID from a DomainName
// this service minted. The ID is the first label, which holds whether the
// domain is AWS's "{id}.cloudfront.net" or Overcast's
// "{id}.cloudfront.{host}" — so this must not trim a fixed suffix.
func distributionIDFromDomainName(domain string) string {
	if i := strings.IndexByte(domain, '.'); i > 0 {
		return domain[:i]
	}
	return domain
}
