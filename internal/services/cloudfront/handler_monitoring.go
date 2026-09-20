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

// ─── Monitoring Subscription: Create ────────────────────────────────────────

// CreateMonitoringSubscription handles POST /2020-05-31/distribution/{id}/monitoring-subscription.
func (h *Handler) CreateMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateMonitoringSubscription")
	distID := chi.URLParam(r, "id")

	// Verify the distribution exists.
	dist, err := h.store.GetDistribution(r.Context(), distID)
	if err != nil {
		log.LogStateError(r, "get distribution", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if dist == nil {
		writeError(w, r, errDistributionNotFound(distID))
		return
	}

	var input monitoringSubscriptionXML
	if err := xml.NewDecoder(r.Body).Decode(&input); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	ms := &MonitoringSubscription{
		RealtimeMetricsSubscriptionConfig: input.RealtimeMetricsSubscriptionConfig,
	}

	if storeErr := h.store.PutMonitoringSubscription(r.Context(), distID, ms); storeErr != nil {
		log.LogStateError(r, "put monitoring subscription", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("monitoring subscription created", zap.String("distId", distID))
	writeXML(w, r, http.StatusOK, &input)
}

// ─── Monitoring Subscription: Get ───────────────────────────────────────────

// GetMonitoringSubscription handles GET /2020-05-31/distribution/{id}/monitoring-subscription.
func (h *Handler) GetMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	distID := chi.URLParam(r, "id")

	ms, err := h.store.GetMonitoringSubscription(r.Context(), distID)
	if err != nil {
		h.log.WithOperation("GetMonitoringSubscription").LogStateError(r, "get monitoring subscription", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if ms == nil {
		writeError(w, r, errNoSuchMonitoringSubscription(distID))
		return
	}

	resp := monitoringSubscriptionXML{
		RealtimeMetricsSubscriptionConfig: ms.RealtimeMetricsSubscriptionConfig,
	}
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Monitoring Subscription: Delete ────────────────────────────────────────

// DeleteMonitoringSubscription handles DELETE /2020-05-31/distribution/{id}/monitoring-subscription.
func (h *Handler) DeleteMonitoringSubscription(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteMonitoringSubscription")
	distID := chi.URLParam(r, "id")

	ms, err := h.store.GetMonitoringSubscription(r.Context(), distID)
	if err != nil {
		log.LogStateError(r, "get monitoring subscription", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if ms == nil {
		writeError(w, r, errNoSuchMonitoringSubscription(distID))
		return
	}

	if storeErr := h.store.DeleteMonitoringSubscription(r.Context(), distID); storeErr != nil {
		log.LogStateError(r, "delete monitoring subscription", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("monitoring subscription deleted", zap.String("distId", distID))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Realtime Log Config: Create ────────────────────────────────────────────

// realtimeLogConfigInput is the XML request body for Create/Update realtime log config.
type realtimeLogConfigInput struct {
	Name         string       `xml:"Name"`
	SamplingRate int64        `xml:"SamplingRate"`
	Fields       fieldList    `xml:"Fields"`
	EndPoints    endPointList `xml:"EndPoints"`
}

// CreateRealtimeLogConfig handles POST /2020-05-31/realtime-log-config.
func (h *Handler) CreateRealtimeLogConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateRealtimeLogConfig")

	var input realtimeLogConfigInput
	if err := xml.NewDecoder(r.Body).Decode(&input); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if input.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Name is required.", HTTPStatus: 400,
		})
		return
	}

	// Check for duplicate name.
	existing, _ := h.store.GetRealtimeLogConfig(r.Context(), input.Name)
	if existing != nil {
		writeError(w, r, errRealtimeLogConfigAlreadyExists(input.Name))
		return
	}

	arn := fmt.Sprintf("arn:aws:cloudfront::%s:realtime-log-config/%s", h.accountID(), input.Name)
	rlc := &RealtimeLogConfig{
		ARN:          arn,
		Name:         input.Name,
		SamplingRate: input.SamplingRate,
		Fields:       input.Fields.Items,
		EndPoints:    make([]EndPoint, len(input.EndPoints.Items)),
		Version:      1,
	}
	copy(rlc.EndPoints, input.EndPoints.Items)

	if storeErr := h.store.PutRealtimeLogConfig(r.Context(), rlc); storeErr != nil {
		log.LogStateError(r, "put realtime log config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("realtime log config created", zap.String("name", input.Name))

	type result struct {
		XMLName xml.Name              `xml:"CreateRealtimeLogConfigResult"`
		RLC     *realtimeLogConfigXML `xml:"RealtimeLogConfig"`
	}
	writeXML(w, r, http.StatusCreated, &result{RLC: rlcToXML(rlc)})
}

// ─── Realtime Log Config: Get ───────────────────────────────────────────────

// realtimeLogConfigGetInput is the XML body sent on GET /get-realtime-log-config.
type realtimeLogConfigGetInput struct {
	XMLName xml.Name `xml:"GetRealtimeLogConfigRequest"`
	Name    string   `xml:"Name,omitempty"`
	ARN     string   `xml:"ARN,omitempty"`
}

// GetRealtimeLogConfig handles GET /2020-05-31/get-realtime-log-config.
func (h *Handler) GetRealtimeLogConfig(w http.ResponseWriter, r *http.Request) {
	var input realtimeLogConfigGetInput
	if err := xml.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	name := input.Name
	if name == "" {
		// Look up by ARN — scan all and find matching.
		all, _ := h.store.ListRealtimeLogConfigs(r.Context())
		for _, rlc := range all {
			if rlc.ARN == input.ARN {
				name = rlc.Name
				break
			}
		}
	}

	if name == "" {
		writeError(w, r, errNoSuchRealtimeLogConfig(""))
		return
	}

	rlc, err := h.store.GetRealtimeLogConfig(r.Context(), name)
	if err != nil {
		h.log.WithOperation("GetRealtimeLogConfig").LogStateError(r, "get realtime log config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if rlc == nil {
		writeError(w, r, errNoSuchRealtimeLogConfig(name))
		return
	}

	type result struct {
		XMLName xml.Name              `xml:"GetRealtimeLogConfigResult"`
		RLC     *realtimeLogConfigXML `xml:"RealtimeLogConfig"`
	}
	writeXML(w, r, http.StatusOK, &result{RLC: rlcToXML(rlc)})
}

// ─── Realtime Log Config: Update ────────────────────────────────────────────

// UpdateRealtimeLogConfig handles PUT /2020-05-31/realtime-log-config.
func (h *Handler) UpdateRealtimeLogConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateRealtimeLogConfig")

	var input realtimeLogConfigInput
	if err := xml.NewDecoder(r.Body).Decode(&input); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if input.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Name is required.", HTTPStatus: 400,
		})
		return
	}

	rlc, err := h.store.GetRealtimeLogConfig(r.Context(), input.Name)
	if err != nil {
		log.LogStateError(r, "get realtime log config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if rlc == nil {
		writeError(w, r, errNoSuchRealtimeLogConfig(input.Name))
		return
	}

	rlc.SamplingRate = input.SamplingRate
	rlc.Fields = input.Fields.Items
	rlc.EndPoints = make([]EndPoint, len(input.EndPoints.Items))
	copy(rlc.EndPoints, input.EndPoints.Items)
	rlc.Version++

	if storeErr := h.store.PutRealtimeLogConfig(r.Context(), rlc); storeErr != nil {
		log.LogStateError(r, "put realtime log config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("realtime log config updated", zap.String("name", input.Name))

	type result struct {
		XMLName xml.Name              `xml:"UpdateRealtimeLogConfigResult"`
		RLC     *realtimeLogConfigXML `xml:"RealtimeLogConfig"`
	}
	writeXML(w, r, http.StatusOK, &result{RLC: rlcToXML(rlc)})
}

// ─── Realtime Log Config: Delete ────────────────────────────────────────────

// realtimeLogConfigDeleteInput is the XML body sent on DELETE /delete-realtime-log-config.
type realtimeLogConfigDeleteInput struct {
	XMLName xml.Name `xml:"DeleteRealtimeLogConfigRequest"`
	Name    string   `xml:"Name,omitempty"`
	ARN     string   `xml:"ARN,omitempty"`
}

// DeleteRealtimeLogConfig handles DELETE /2020-05-31/delete-realtime-log-config.
func (h *Handler) DeleteRealtimeLogConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteRealtimeLogConfig")

	var input realtimeLogConfigDeleteInput
	if err := xml.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	name := input.Name
	if name == "" {
		all, _ := h.store.ListRealtimeLogConfigs(r.Context())
		for _, rlc := range all {
			if rlc.ARN == input.ARN {
				name = rlc.Name
				break
			}
		}
	}

	if name == "" {
		writeError(w, r, errNoSuchRealtimeLogConfig(""))
		return
	}

	rlc, err := h.store.GetRealtimeLogConfig(r.Context(), name)
	if err != nil {
		log.LogStateError(r, "get realtime log config", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if rlc == nil {
		writeError(w, r, errNoSuchRealtimeLogConfig(name))
		return
	}

	if storeErr := h.store.DeleteRealtimeLogConfig(r.Context(), name); storeErr != nil {
		log.LogStateError(r, "delete realtime log config", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("realtime log config deleted", zap.String("name", name))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── Realtime Log Config: List ──────────────────────────────────────────────

// ListRealtimeLogConfigs handles GET /2020-05-31/realtime-log-config.
func (h *Handler) ListRealtimeLogConfigs(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListRealtimeLogConfigs")

	all, err := h.store.ListRealtimeLogConfigs(r.Context())
	if err != nil {
		log.LogStateError(r, "list realtime log configs", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	items := make([]realtimeLogConfigXML, 0, len(all))
	for _, rlc := range all {
		items = append(items, *rlcToXML(rlc))
	}

	result := realtimeLogConfigListXML{
		MaxItems: maxItems,
		Items:    items,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// rlcToXML converts a RealtimeLogConfig to its XML representation.
func rlcToXML(rlc *RealtimeLogConfig) *realtimeLogConfigXML {
	eps := make([]EndPoint, len(rlc.EndPoints))
	copy(eps, rlc.EndPoints)
	return &realtimeLogConfigXML{
		ARN:          rlc.ARN,
		Name:         rlc.Name,
		SamplingRate: rlc.SamplingRate,
		Fields:       fieldList{Items: rlc.Fields},
		EndPoints:    endPointList{Items: eps},
	}
}
