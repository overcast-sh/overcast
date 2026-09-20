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

// ─── FLE Config: Create ─────────────────────────────────────────────────────

// CreateFieldLevelEncryptionConfig handles POST /2020-05-31/field-level-encryption.
func (h *Handler) CreateFieldLevelEncryptionConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateFieldLevelEncryptionConfig")

	var cfg FLEConfigData
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

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	c := &FLEConfig{
		ID:               id,
		LastModifiedTime: now,
		FLEConfigData:    cfg,
		Version:          1,
	}

	if storeErr := h.store.PutFLEConfig(r.Context(), c); storeErr != nil {
		log.LogStateError(r, "put fle config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption config created", zap.String("id", id))

	resp := fleConfigXML{
		ID:                         c.ID,
		LastModifiedTime:           c.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionConfig: c.FLEConfigData,
	}
	w.Header().Set("ETag", computeETag(c.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/field-level-encryption/%s", id))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── FLE Config: Get ────────────────────────────────────────────────────────

// GetFieldLevelEncryption handles GET /2020-05-31/field-level-encryption/{id}.
func (h *Handler) GetFieldLevelEncryption(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	c, err := h.store.GetFLEConfig(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetFieldLevelEncryption").LogStateError(r, "get fle config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if c == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionConfig(id))
		return
	}

	resp := fleConfigXML{
		ID:                         c.ID,
		LastModifiedTime:           c.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionConfig: c.FLEConfigData,
	}
	w.Header().Set("ETag", computeETag(c.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Config: GetConfig ──────────────────────────────────────────────────

// GetFieldLevelEncryptionConfig handles GET /2020-05-31/field-level-encryption/{id}/config.
func (h *Handler) GetFieldLevelEncryptionConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	c, err := h.store.GetFLEConfig(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetFieldLevelEncryptionConfig").LogStateError(r, "get fle config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if c == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionConfig(id))
		return
	}

	resp := fleConfigDataXML{
		CallerReference: c.FLEConfigData.CallerReference,
		Comment:         c.FLEConfigData.Comment,
	}
	w.Header().Set("ETag", computeETag(c.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Config: Update ─────────────────────────────────────────────────────

// UpdateFieldLevelEncryptionConfig handles PUT /2020-05-31/field-level-encryption/{id}/config.
func (h *Handler) UpdateFieldLevelEncryptionConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateFieldLevelEncryptionConfig")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	c, err := h.store.GetFLEConfig(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get fle config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if c == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionConfig(id))
		return
	}

	if ifMatch != computeETag(c.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg FLEConfigData
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	c.FLEConfigData = cfg
	c.LastModifiedTime = h.clk.Now()
	c.Version++

	if storeErr := h.store.PutFLEConfig(r.Context(), c); storeErr != nil {
		log.LogStateError(r, "put fle config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption config updated", zap.String("id", id))

	resp := fleConfigXML{
		ID:                         c.ID,
		LastModifiedTime:           c.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionConfig: c.FLEConfigData,
	}
	w.Header().Set("ETag", computeETag(c.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Config: Delete ─────────────────────────────────────────────────────

// DeleteFieldLevelEncryption handles DELETE /2020-05-31/field-level-encryption/{id}.
func (h *Handler) DeleteFieldLevelEncryption(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteFieldLevelEncryption")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	c, err := h.store.GetFLEConfig(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get fle config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if c == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionConfig(id))
		return
	}

	if ifMatch != computeETag(c.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteFLEConfig(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete fle config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption config deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── FLE Config: List ───────────────────────────────────────────────────────

// ListFieldLevelEncryptionConfigs handles GET /2020-05-31/field-level-encryption.
func (h *Handler) ListFieldLevelEncryptionConfigs(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListFieldLevelEncryptionConfigs")

	all, err := h.store.ListFLEConfigs(r.Context())
	if err != nil {
		log.LogStateError(r, "list fle configs", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]fleConfigSummary, 0, len(all))
	for _, c := range all {
		summaries = append(summaries, fleConfigSummary{
			ID:               c.ID,
			LastModifiedTime: c.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
			Comment:          c.FLEConfigData.Comment,
		})
	}

	result := fleConfigListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}
	writeXML(w, r, http.StatusOK, &result)
}

// ─── FLE Profile: Create ────────────────────────────────────────────────────

// CreateFieldLevelEncryptionProfile handles POST /2020-05-31/field-level-encryption-profile.
func (h *Handler) CreateFieldLevelEncryptionProfile(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateFieldLevelEncryptionProfile")

	var cfg FLEProfileData
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

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	p := &FLEProfile{
		ID:               id,
		LastModifiedTime: now,
		FLEProfileData:   cfg,
		Version:          1,
	}

	if storeErr := h.store.PutFLEProfile(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put fle profile", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption profile created", zap.String("id", id), zap.String("name", cfg.Name))

	resp := fleProfileXML{
		ID:                                p.ID,
		LastModifiedTime:                  p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionProfileConfig: p.FLEProfileData,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/field-level-encryption-profile/%s", id))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── FLE Profile: Get ───────────────────────────────────────────────────────

// GetFieldLevelEncryptionProfile handles GET /2020-05-31/field-level-encryption-profile/{id}.
func (h *Handler) GetFieldLevelEncryptionProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetFLEProfile(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetFieldLevelEncryptionProfile").LogStateError(r, "get fle profile", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionProfile(id))
		return
	}

	resp := fleProfileXML{
		ID:                                p.ID,
		LastModifiedTime:                  p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionProfileConfig: p.FLEProfileData,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Profile: GetConfig ─────────────────────────────────────────────────

// GetFieldLevelEncryptionProfileConfig handles GET /2020-05-31/field-level-encryption-profile/{id}/config.
func (h *Handler) GetFieldLevelEncryptionProfileConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	p, err := h.store.GetFLEProfile(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetFieldLevelEncryptionProfileConfig").LogStateError(r, "get fle profile", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionProfile(id))
		return
	}

	resp := fleProfileDataXML{
		CallerReference: p.FLEProfileData.CallerReference,
		Name:            p.FLEProfileData.Name,
		Comment:         p.FLEProfileData.Comment,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Profile: Update ────────────────────────────────────────────────────

// UpdateFieldLevelEncryptionProfile handles PUT /2020-05-31/field-level-encryption-profile/{id}/config.
func (h *Handler) UpdateFieldLevelEncryptionProfile(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateFieldLevelEncryptionProfile")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetFLEProfile(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get fle profile", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionProfile(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg FLEProfileData
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	p.FLEProfileData = cfg
	p.LastModifiedTime = h.clk.Now()
	p.Version++

	if storeErr := h.store.PutFLEProfile(r.Context(), p); storeErr != nil {
		log.LogStateError(r, "put fle profile", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption profile updated", zap.String("id", id))

	resp := fleProfileXML{
		ID:                                p.ID,
		LastModifiedTime:                  p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		FieldLevelEncryptionProfileConfig: p.FLEProfileData,
	}
	w.Header().Set("ETag", computeETag(p.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── FLE Profile: Delete ────────────────────────────────────────────────────

// DeleteFieldLevelEncryptionProfile handles DELETE /2020-05-31/field-level-encryption-profile/{id}.
func (h *Handler) DeleteFieldLevelEncryptionProfile(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteFieldLevelEncryptionProfile")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	p, err := h.store.GetFLEProfile(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get fle profile", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if p == nil {
		writeError(w, r, errNoSuchFieldLevelEncryptionProfile(id))
		return
	}

	if ifMatch != computeETag(p.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteFLEProfile(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete fle profile", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("field-level encryption profile deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── FLE Profile: List ──────────────────────────────────────────────────────

// ListFieldLevelEncryptionProfiles handles GET /2020-05-31/field-level-encryption-profile.
func (h *Handler) ListFieldLevelEncryptionProfiles(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListFieldLevelEncryptionProfiles")

	all, err := h.store.ListFLEProfiles(r.Context())
	if err != nil {
		log.LogStateError(r, "list fle profiles", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]fleProfileSummary, 0, len(all))
	for _, p := range all {
		summaries = append(summaries, fleProfileSummary{
			ID:               p.ID,
			LastModifiedTime: p.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
			Name:             p.FLEProfileData.Name,
			Comment:          p.FLEProfileData.Comment,
		})
	}

	result := fleProfileListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}
	writeXML(w, r, http.StatusOK, &result)
}
