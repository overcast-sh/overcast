package shield

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ListProtections' page size: the API Reference documents a default of 20,
// and MaxResults is modeled @range(min: 0, max: 10000).
const (
	listProtectionsDefaultMaxResults = 20
	listProtectionsMaxMaxResults     = 10000
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

// inclusionProtectionFilters mirrors InclusionProtectionFilters. Each member
// is modeled @length(min: 1, max: 1), so a well-formed request carries at
// most one criterion per filter type.
type inclusionProtectionFilters struct {
	ResourceArns    []string `json:"ResourceArns" cbor:"ResourceArns"`
	ProtectionNames []string `json:"ProtectionNames" cbor:"ProtectionNames"`
	ResourceTypes   []string `json:"ResourceTypes" cbor:"ResourceTypes"`
}

type listProtectionsRequest struct {
	NextToken        string                      `json:"NextToken" cbor:"NextToken"`
	MaxResults       int                         `json:"MaxResults" cbor:"MaxResults"`
	InclusionFilters *inclusionProtectionFilters `json:"InclusionFilters" cbor:"InclusionFilters"`
}

type listProtectionsResponse struct {
	Protections []*Protection `json:"Protections"`
	NextToken   string        `json:"NextToken,omitempty"`
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

// wireProtection returns the Protection wire shape for a stored record,
// deriving ProtectionArn for a record persisted before Overcast stored one so
// an older SQLite database describes exactly as a fresh one does rather than
// returning the member empty.
func (h *Handler) wireProtection(rec *protectionRecord) *Protection {
	p := rec.Protection
	if p.ProtectionArn == "" {
		p.ProtectionArn = protectionARN(h.accountID(), p.ID)
	}
	return &p
}

func (h *Handler) createProtectionTyped(ctx context.Context, req *createProtectionRequest) (*createProtectionResponse, *protocol.AWSError) {
	if req.Name == "" || req.ResourceArn == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Name and ResourceArn are required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	// A resource carries at most one Protection on AWS: a second
	// CreateProtection for a ResourceArn that already has one reports
	// ResourceAlreadyExistsException, one of CreateProtection's modeled
	// errors, rather than creating a second independent record.
	existing, err := h.store.listProtections(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	for _, p := range existing {
		if p.ResourceArn == req.ResourceArn {
			return nil, &protocol.AWSError{
				Code:       "ResourceAlreadyExistsException",
				Message:    fmt.Sprintf("Resource %s is already protected by protection %s", req.ResourceArn, p.ID),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}
	id := uuid.NewString()
	p := &protectionRecord{Protection: Protection{
		ID:            id,
		Name:          req.Name,
		ResourceArn:   req.ResourceArn,
		ProtectionArn: protectionARN(h.accountID(), id),
	}}
	if err := h.store.putProtection(ctx, p); err != nil {
		return nil, protocol.ErrInternalError
	}
	// CreateProtectionResponse carries ProtectionId alone — ProtectionArn is
	// a Protection member, returned by DescribeProtection and ListProtections.
	return &createProtectionResponse{ProtectionId: p.ID}, nil
}

// matchesInclusionFilters reports whether a protection satisfies every filter
// type the caller supplied: "Shield Advanced returns protections that exactly
// match all of the filter criteria that you provide."
//
// Each filter list is modeled @length(min: 1, max: 1), so one criterion per
// type is the only well-formed request. A longer list is treated as a set the
// protection must be a member of rather than refused: ListProtections models
// no validation error at all (only InternalErrorException,
// InvalidPaginationTokenException and ResourceNotFoundException), so inventing
// one here would answer with a code AWS cannot return for this operation.
func matchesInclusionFilters(rec *protectionRecord, f *inclusionProtectionFilters) bool {
	if f == nil {
		return true
	}
	if len(f.ResourceArns) > 0 && !slices.Contains(f.ResourceArns, rec.ResourceArn) {
		return false
	}
	if len(f.ProtectionNames) > 0 && !slices.Contains(f.ProtectionNames, rec.Name) {
		return false
	}
	if len(f.ResourceTypes) > 0 {
		resourceType := resourceTypeFromARN(rec.ResourceArn)
		// An ARN naming no protectable resource type matches no
		// ResourceTypes filter, rather than matching an empty criterion.
		if resourceType == "" || !slices.Contains(f.ResourceTypes, resourceType) {
			return false
		}
	}
	return true
}

func (h *Handler) listProtectionsTyped(ctx context.Context, req *listProtectionsRequest) (*listProtectionsResponse, *protocol.AWSError) {
	records, err := h.store.listProtections(ctx)
	if err != nil {
		return nil, protocol.ErrInternalError
	}
	// Filter before paginating, which is what the @paginated trait implies:
	// MaxResults caps the matching set, so a page is never short because a
	// filter emptied part of it.
	protections := make([]*Protection, 0, len(records))
	for _, rec := range records {
		if !matchesInclusionFilters(rec, req.InclusionFilters) {
			continue
		}
		protections = append(protections, h.wireProtection(rec))
	}
	page, err := serviceutil.Paginate(protections, req.MaxResults, req.NextToken, serviceutil.PaginateOptions{
		DefaultLimit: listProtectionsDefaultMaxResults,
		MaxLimit:     listProtectionsMaxMaxResults,
	})
	if err != nil {
		if errors.Is(err, serviceutil.ErrInvalidPageToken) {
			return nil, &protocol.AWSError{
				Code:       "InvalidPaginationTokenException",
				Message:    "The NextToken specified in the request is invalid. Submit the request using the NextToken value that was returned in the prior response.",
				HTTPStatus: http.StatusBadRequest,
			}
		}
		return nil, protocol.ErrInternalError
	}
	return &listProtectionsResponse{Protections: page.Items, NextToken: page.NextToken}, nil
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
	// "You must provide either the ResourceArn of the protected resource or
	// the ProtectionID of the protection, but not both" — the documentation
	// on both members. AWS does not document the error for supplying both;
	// of DescribeProtection's three modeled errors only
	// InvalidParameterException fits a mutually exclusive pair, so that is
	// what Overcast answers (recorded as a fork in
	// docs/dev/compatibility/services/shield.yaml).
	if req.ProtectionId != "" && req.ResourceArn != "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "You must provide either the ResourceArn of the protected resource or the ProtectionID of the protection, but not both",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if req.ProtectionId != "" {
		p, found := h.store.getProtection(ctx, req.ProtectionId)
		if !found {
			return nil, &protocol.AWSError{
				Code:       "ResourceNotFoundException",
				Message:    fmt.Sprintf("Protection %s not found", req.ProtectionId),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		return &describeProtectionResponse{Protection: h.wireProtection(p)}, nil
	}
	if req.ResourceArn != "" {
		all, err := h.store.listProtections(ctx)
		if err != nil {
			return nil, protocol.ErrInternalError
		}
		for _, p := range all {
			if p.ResourceArn == req.ResourceArn {
				return &describeProtectionResponse{Protection: h.wireProtection(p)}, nil
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
