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

// ─── Key Group: Create ──────────────────────────────────────────────────────

// CreateKeyGroup handles POST /2020-05-31/key-group.
func (h *Handler) CreateKeyGroup(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateKeyGroup")

	var cfg KeyGroupConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if cfg.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Key group name is required.", HTTPStatus: 400,
		})
		return
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	kg := &KeyGroup{
		ID:               id,
		LastModifiedTime: now,
		KeyGroupConfig:   cfg,
		Version:          1,
	}

	if storeErr := h.store.PutKeyGroup(r.Context(), kg); storeErr != nil {
		log.LogStateError(r, "put key group", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("key group created", zap.String("id", id), zap.String("name", cfg.Name))

	resp := keyGroupXML{
		ID:               kg.ID,
		LastModifiedTime: kg.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		KeyGroupConfig:   kg.KeyGroupConfig,
	}
	w.Header().Set("ETag", computeETag(kg.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/key-group/%s", id))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── Key Group: Get ─────────────────────────────────────────────────────────

// GetKeyGroup handles GET /2020-05-31/key-group/{id}.
func (h *Handler) GetKeyGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	kg, err := h.store.GetKeyGroup(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetKeyGroup").LogStateError(r, "get key group", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if kg == nil {
		writeError(w, r, errNoSuchKeyGroup(id))
		return
	}

	resp := keyGroupXML{
		ID:               kg.ID,
		LastModifiedTime: kg.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		KeyGroupConfig:   kg.KeyGroupConfig,
	}
	w.Header().Set("ETag", computeETag(kg.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Key Group: GetConfig ───────────────────────────────────────────────────

// GetKeyGroupConfig handles GET /2020-05-31/key-group/{id}/config.
func (h *Handler) GetKeyGroupConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	kg, err := h.store.GetKeyGroup(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetKeyGroupConfig").LogStateError(r, "get key group", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if kg == nil {
		writeError(w, r, errNoSuchKeyGroup(id))
		return
	}

	resp := keyGroupConfigWrapper{
		Name:    kg.KeyGroupConfig.Name,
		Comment: kg.KeyGroupConfig.Comment,
		Items:   kg.KeyGroupConfig.Items,
	}
	w.Header().Set("ETag", computeETag(kg.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Key Group: Update ──────────────────────────────────────────────────────

// UpdateKeyGroup handles PUT /2020-05-31/key-group/{id}.
func (h *Handler) UpdateKeyGroup(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateKeyGroup")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	kg, err := h.store.GetKeyGroup(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get key group", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if kg == nil {
		writeError(w, r, errNoSuchKeyGroup(id))
		return
	}

	if ifMatch != computeETag(kg.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg KeyGroupConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	kg.KeyGroupConfig = cfg
	kg.LastModifiedTime = h.clk.Now()
	kg.Version++

	if storeErr := h.store.PutKeyGroup(r.Context(), kg); storeErr != nil {
		log.LogStateError(r, "put key group", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("key group updated", zap.String("id", id))
	resp := keyGroupXML{
		ID:               kg.ID,
		LastModifiedTime: kg.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		KeyGroupConfig:   kg.KeyGroupConfig,
	}
	w.Header().Set("ETag", computeETag(kg.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Key Group: Delete ──────────────────────────────────────────────────────

// DeleteKeyGroup handles DELETE /2020-05-31/key-group/{id}.
func (h *Handler) DeleteKeyGroup(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteKeyGroup")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	kg, err := h.store.GetKeyGroup(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get key group", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if kg == nil {
		writeError(w, r, errNoSuchKeyGroup(id))
		return
	}

	if ifMatch != computeETag(kg.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteKeyGroup(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete key group", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("key group deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Key Group: List ────────────────────────────────────────────────────────

// ListKeyGroups handles GET /2020-05-31/key-group.
func (h *Handler) ListKeyGroups(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListKeyGroups")

	all, err := h.store.ListKeyGroups(r.Context())
	if err != nil {
		log.LogStateError(r, "list key groups", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]keyGroupSummary, 0, len(all))
	for _, kg := range all {
		summaries = append(summaries, keyGroupSummary{
			KeyGroup: keyGroupXML{
				ID:               kg.ID,
				LastModifiedTime: kg.LastModifiedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
				KeyGroupConfig:   kg.KeyGroupConfig,
			},
		})
	}

	result := keyGroupListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── Public Key: Create ─────────────────────────────────────────────────────

// CreatePublicKey handles POST /2020-05-31/public-key.
func (h *Handler) CreatePublicKey(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreatePublicKey")

	var cfg PublicKeyConfig
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

	// Check for duplicate caller reference.
	existing, _ := h.store.ListPublicKeys(r.Context())
	for _, pk := range existing {
		if pk.PublicKeyConfig.CallerReference == cfg.CallerReference {
			writeError(w, r, errPublicKeyAlreadyExists(cfg.CallerReference))
			return
		}
	}

	id := generateAlphanumericID(14)
	now := h.clk.Now()

	pk := &PublicKey{
		ID:              id,
		CreatedTime:     now,
		PublicKeyConfig: cfg,
		Version:         1,
	}

	if storeErr := h.store.PutPublicKey(r.Context(), pk); storeErr != nil {
		log.LogStateError(r, "put public key", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("public key created", zap.String("id", id), zap.String("name", cfg.Name))

	resp := publicKeyXML{
		ID:              pk.ID,
		CreatedTime:     pk.CreatedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		PublicKeyConfig: pk.PublicKeyConfig,
	}
	w.Header().Set("ETag", computeETag(pk.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/public-key/%s", id))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── Public Key: Get ────────────────────────────────────────────────────────

// GetPublicKey handles GET /2020-05-31/public-key/{id}.
func (h *Handler) GetPublicKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	pk, err := h.store.GetPublicKey(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetPublicKey").LogStateError(r, "get public key", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if pk == nil {
		writeError(w, r, errNoSuchPublicKey(id))
		return
	}

	resp := publicKeyXML{
		ID:              pk.ID,
		CreatedTime:     pk.CreatedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		PublicKeyConfig: pk.PublicKeyConfig,
	}
	w.Header().Set("ETag", computeETag(pk.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Public Key: GetConfig ──────────────────────────────────────────────────

// GetPublicKeyConfig handles GET /2020-05-31/public-key/{id}/config.
func (h *Handler) GetPublicKeyConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	pk, err := h.store.GetPublicKey(r.Context(), id)
	if err != nil {
		h.log.WithOperation("GetPublicKeyConfig").LogStateError(r, "get public key", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if pk == nil {
		writeError(w, r, errNoSuchPublicKey(id))
		return
	}

	resp := publicKeyConfigWrapper{
		CallerReference: pk.PublicKeyConfig.CallerReference,
		Name:            pk.PublicKeyConfig.Name,
		Comment:         pk.PublicKeyConfig.Comment,
		EncodedKey:      pk.PublicKeyConfig.EncodedKey,
	}
	w.Header().Set("ETag", computeETag(pk.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Public Key: Update ─────────────────────────────────────────────────────

// UpdatePublicKey handles PUT /2020-05-31/public-key/{id}/config.
func (h *Handler) UpdatePublicKey(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdatePublicKey")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	pk, err := h.store.GetPublicKey(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get public key", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if pk == nil {
		writeError(w, r, errNoSuchPublicKey(id))
		return
	}

	if ifMatch != computeETag(pk.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var cfg PublicKeyConfig
	if err := xml.NewDecoder(r.Body).Decode(&cfg); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	pk.PublicKeyConfig = cfg
	pk.CreatedTime = h.clk.Now() // AWS updates the timestamp on update
	pk.Version++

	if storeErr := h.store.PutPublicKey(r.Context(), pk); storeErr != nil {
		log.LogStateError(r, "put public key", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("public key updated", zap.String("id", id))
	resp := publicKeyXML{
		ID:              pk.ID,
		CreatedTime:     pk.CreatedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
		PublicKeyConfig: pk.PublicKeyConfig,
	}
	w.Header().Set("ETag", computeETag(pk.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Public Key: Delete ─────────────────────────────────────────────────────

// DeletePublicKey handles DELETE /2020-05-31/public-key/{id}.
func (h *Handler) DeletePublicKey(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeletePublicKey")

	id := chi.URLParam(r, "id")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	pk, err := h.store.GetPublicKey(r.Context(), id)
	if err != nil {
		log.LogStateError(r, "get public key", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if pk == nil {
		writeError(w, r, errNoSuchPublicKey(id))
		return
	}

	if ifMatch != computeETag(pk.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeletePublicKey(r.Context(), id); storeErr != nil {
		log.LogStateError(r, "delete public key", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("public key deleted", zap.String("id", id))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Public Key: List ───────────────────────────────────────────────────────

// ListPublicKeys handles GET /2020-05-31/public-key.
func (h *Handler) ListPublicKeys(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListPublicKeys")

	all, err := h.store.ListPublicKeys(r.Context())
	if err != nil {
		log.LogStateError(r, "list public keys", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]publicKeySummary, 0, len(all))
	for _, pk := range all {
		summaries = append(summaries, publicKeySummary{
			ID:          pk.ID,
			Name:        pk.PublicKeyConfig.Name,
			CreatedTime: pk.CreatedTime.UTC().Format("2006-01-02T15:04:05.000Z"),
			Comment:     pk.PublicKeyConfig.Comment,
			EncodedKey:  pk.PublicKeyConfig.EncodedKey,
		})
	}

	result := publicKeyListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}
