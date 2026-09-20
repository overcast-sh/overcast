package shield

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type createProtectionRequest struct {
	Name        string `json:"Name"`
	ResourceArn string `json:"ResourceArn"`
}

type createProtectionResponse struct {
	ProtectionId string `json:"ProtectionId"`
}

type deleteProtectionRequest struct {
	ProtectionId string `json:"ProtectionId"`
}

type describeProtectionRequest struct {
	ProtectionId string `json:"ProtectionId"`
	ResourceArn  string `json:"ResourceArn"`
}

type describeProtectionResponse struct {
	Protection *Protection `json:"Protection"`
}

type listProtectionsRequest struct{}

type listProtectionsResponse struct {
	Protections []*Protection `json:"Protections"`
}

type describeSubscriptionResponse struct {
	Subscription subscriptionWire `json:"Subscription"`
}

type subscriptionWire struct {
	StartTime               float64 `json:"StartTime"`
	TimeCommitmentInSeconds int     `json:"TimeCommitmentInSeconds"`
	AutoRenew               string  `json:"AutoRenew"`
}

// SubscriptionState is a member of GetSubscriptionStateResponse only, not of
// Subscription / DescribeSubscriptionResponse — Overcast does not implement
// GetSubscriptionState.
func (h *Handler) describeSubscriptionTyped(ctx context.Context, req *struct{}) (*describeSubscriptionResponse, *protocol.AWSError) {
	return &describeSubscriptionResponse{
		Subscription: subscriptionWire{
			StartTime:               1000000000.0,
			TimeCommitmentInSeconds: 31536000,
			AutoRenew:               "ENABLED",
		},
	}, nil
}

func (h *Handler) createProtectionTyped(ctx context.Context, req *createProtectionRequest) (*createProtectionResponse, *protocol.AWSError) {
	if req.Name == "" || req.ResourceArn == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Name and ResourceArn are required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	p := &protectionRecord{Protection: Protection{
		ID:          uuid.NewString(),
		Name:        req.Name,
		ResourceArn: req.ResourceArn,
	}}
	if err := h.store.putProtection(ctx, p); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &createProtectionResponse{ProtectionId: p.ID}, nil
}

func (h *Handler) listProtectionsTyped(ctx context.Context, req *listProtectionsRequest) (*listProtectionsResponse, *protocol.AWSError) {
	records, err := h.store.listProtections(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	protections := make([]*Protection, 0, len(records))
	for _, rec := range records {
		protections = append(protections, &rec.Protection)
	}
	return &listProtectionsResponse{Protections: protections}, nil
}

func (h *Handler) deleteProtectionTyped(ctx context.Context, req *deleteProtectionRequest) (*struct{}, *protocol.AWSError) {
	if req.ProtectionId == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "ProtectionId is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if _, found := h.store.getProtection(ctx, req.ProtectionId); !found {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Protection %s not found", req.ProtectionId),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if err := h.store.deleteProtection(ctx, req.ProtectionId); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) describeProtectionTyped(ctx context.Context, req *describeProtectionRequest) (*describeProtectionResponse, *protocol.AWSError) {
	if req.ProtectionId != "" {
		p, found := h.store.getProtection(ctx, req.ProtectionId)
		if !found {
			return nil, &protocol.AWSError{
				Code:       "ResourceNotFoundException",
				Message:    fmt.Sprintf("Protection %s not found", req.ProtectionId),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		return &describeProtectionResponse{Protection: &p.Protection}, nil
	}
	if req.ResourceArn != "" {
		all, err := h.store.listProtections(ctx)
		if err != nil {
			return nil, protocol.ErrInternalError
		}
		for _, p := range all {
			if p.ResourceArn == req.ResourceArn {
				return &describeProtectionResponse{Protection: &p.Protection}, nil
			}
		}
	}
	return nil, &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "Protection not found",
		HTTPStatus: http.StatusBadRequest,
	}
}

type tagResourceRequest struct {
	ResourceARN string                `json:"ResourceARN" cbor:"ResourceARN"`
	Tags        []serviceutil.TagPair `json:"Tags" cbor:"Tags"`
}

type untagResourceRequest struct {
	ResourceARN string   `json:"ResourceARN" cbor:"ResourceARN"`
	TagKeys     []string `json:"TagKeys" cbor:"TagKeys"`
}

type listTagsForResourceRequest struct {
	ResourceARN string `json:"ResourceARN" cbor:"ResourceARN"`
}

type listTagsForResourceResponse struct {
	Tags []serviceutil.TagPair `json:"Tags" cbor:"Tags"`
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid), HTTPStatus: http.StatusBadRequest,
		}
	}
	tags := p.GetTags()
	if tags == nil {
		tags = map[string]string{}
	}
	for _, t := range req.Tags {
		tags[t.Key] = t.Value
	}
	if aerr := serviceutil.ValidateTags(shieldTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	p.SetTags(tags)
	if err := h.store.putProtection(ctx, p); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid), HTTPStatus: http.StatusBadRequest,
		}
	}
	tags := p.GetTags()
	if tags != nil {
		for _, k := range req.TagKeys {
			delete(tags, k)
		}
		p.SetTags(tags)
	}
	if err := h.store.putProtection(ctx, p); err != nil {
		return nil, protocol.ErrInternalError
	}
	return &struct{}{}, nil
}

func (h *Handler) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	if req.ResourceARN == "" {
		return nil, &protocol.AWSError{
			Code: "InvalidParameterException", Message: "ResourceARN is required", HTTPStatus: http.StatusBadRequest,
		}
	}
	pid, aerr := protectionIDFromARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	p, found := h.store.getProtection(ctx, pid)
	if !found {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: fmt.Sprintf("Protection %s not found", pid), HTTPStatus: http.StatusBadRequest,
		}
	}
	return &listTagsForResourceResponse{Tags: serviceutil.TagsToList(p.GetTags())}, nil
}
