package shield

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Handler holds Shield handler dependencies.
type Handler struct {
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
	store   *shieldStore
}

func newHandler(st state.Store) *Handler {
	h := &Handler{store: newShieldStore(st)}
	h.initOps()
	return h
}

func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		"DescribeSubscription": h.describeSubscription,
		"CreateProtection":     h.createProtection,
		"ListProtections":      h.listProtections,
		"DeleteProtection":     h.deleteProtection,
		"DescribeProtection":   h.describeProtection,
		"TagResource":          h.tagResource,
		"UntagResource":        h.untagResource,
		"ListTagsForResource":  h.listTagsForResource,
	}
	h.typedOp = h.typedOps()
}

func (h *Handler) describeSubscription(w http.ResponseWriter, r *http.Request) {
	// SubscriptionState is a member of GetSubscriptionStateResponse only,
	// not of Subscription / DescribeSubscriptionResponse — Overcast does
	// not implement GetSubscriptionState.
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"Subscription": map[string]any{
			"StartTime":               1000000000.0,
			"TimeCommitmentInSeconds": 31536000,
			"AutoRenew":               "ENABLED",
		},
	})
}

func (h *Handler) createProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"Name"`
		ResourceArn string `json:"ResourceArn"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.ResourceArn == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Name and ResourceArn are required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	p := &protectionRecord{Protection: Protection{
		ID:          uuid.NewString(),
		Name:        req.Name,
		ResourceArn: req.ResourceArn,
	}}
	if err := h.store.putProtection(r.Context(), p); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"ProtectionId": p.ID,
	})
}

func (h *Handler) listProtections(w http.ResponseWriter, r *http.Request) {
	records, err := h.store.listProtections(r.Context())
	if err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protections := make([]*Protection, 0, len(records))
	for _, rec := range records {
		protections = append(protections, &rec.Protection)
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{
		"Protections": protections,
	})
}

func (h *Handler) deleteProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProtectionId string `json:"ProtectionId"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ProtectionId == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "ProtectionId is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if _, found := h.store.getProtection(r.Context(), req.ProtectionId); !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Protection %s not found", req.ProtectionId),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if err := h.store.deleteProtection(r.Context(), req.ProtectionId); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (h *Handler) describeProtection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProtectionId string `json:"ProtectionId"`
		ResourceArn  string `json:"ResourceArn"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ProtectionId != "" {
		p, found := h.store.getProtection(r.Context(), req.ProtectionId)
		if !found {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "ResourceNotFoundException",
				Message:    fmt.Sprintf("Protection %s not found", req.ProtectionId),
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Protection": &p.Protection})
		return
	}
	if req.ResourceArn != "" {
		all, err := h.store.listProtections(r.Context())
		if err != nil {
			protocol.WriteJSONError(w, r, protocol.ErrInternalError)
			return
		}
		for _, p := range all {
			if p.ResourceArn == req.ResourceArn {
				protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Protection": &p.Protection})
				return
			}
		}
	}
	protocol.WriteJSONError(w, r, &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Protection not found",
		HTTPStatus: http.StatusBadRequest,
	})
}

var shieldTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterException",
	InvalidCode:     "InvalidParameterException",
	ExceededMessage: "Too many tags. Maximum allowed: 50.",
}

func (h *Handler) tagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string                `json:"ResourceARN"`
		Tags        []serviceutil.TagPair `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	ctx := r.Context()
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tags := p.GetTags()
	if tags == nil {
		tags = map[string]string{}
	}
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(shieldTagCfg, tags); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	p.SetTags(tags)
	if err := h.store.putProtection(ctx, p); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (h *Handler) untagResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string   `json:"ResourceARN"`
		TagKeys     []string `json:"TagKeys"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	ctx := r.Context()
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tags := p.GetTags()
	if tags != nil {
		for _, k := range req.TagKeys {
			delete(tags, k)
		}
		p.SetTags(tags)
	}
	if err := h.store.putProtection(ctx, p); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInternalError)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{})
}

func (h *Handler) listTagsForResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ResourceARN string `json:"ResourceARN"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.ResourceARN == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	ctx := r.Context()
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	tagList := serviceutil.TagsToList(p.GetTags())
	protocol.WriteJSON(w, r, http.StatusOK, map[string]any{"Tags": tagList})
}

func protectionIDFromARN(arn string) (string, *protocol.AWSError) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 {
		return "", &protocol.AWSError{
			Code: "InvalidParameterException", Message: "Invalid ResourceARN format",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	resource := parts[5]
	if !strings.HasPrefix(resource, "protection/") {
		return "", &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN must be a protection ARN",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return strings.TrimPrefix(resource, "protection/"), nil
}
