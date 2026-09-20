package waf

import (
	"context"
	"encoding/json"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Real WAFv2 sends Tags as a LIST of {Key,Value} structs, never a map.
type createWebACLRequest struct {
	Name             string                `json:"Name"`
	Scope            string                `json:"Scope"`
	Description      string                `json:"Description"`
	DefaultAction    map[string]any        `json:"DefaultAction"`
	VisibilityConfig map[string]any        `json:"VisibilityConfig"`
	Rules            []any                 `json:"Rules"`
	Tags             []serviceutil.TagPair `json:"Tags"`
}

type createWebACLResponse struct {
	Summary webACLSummaryWire `json:"Summary"`
}

type webACLSummaryWire struct {
	Id          string            `json:"Id"`
	Name        string            `json:"Name"`
	Description string            `json:"Description"`
	LockToken   string            `json:"LockToken"`
	ARN         string            `json:"ARN"`
	Tags        map[string]string `json:"Tags,omitempty"`
}

type getWebACLRequest struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Scope string `json:"Scope"`
}

type getWebACLResponse struct {
	WebACL    webACLWire `json:"WebACL"`
	LockToken string     `json:"LockToken"`
}

type webACLWire struct {
	Id               string         `json:"Id"`
	Name             string         `json:"Name"`
	ARN              string         `json:"ARN"`
	Description      string         `json:"Description"`
	DefaultAction    map[string]any `json:"DefaultAction"`
	VisibilityConfig map[string]any `json:"VisibilityConfig"`
	Rules            []any          `json:"Rules"`
}

type listWebACLsRequest struct {
	Scope string `json:"Scope"`
}

type listWebACLsResponse struct {
	WebACLs []webACLSummaryWire `json:"WebACLs"`
}

type deleteWebACLRequest struct {
	ID        string `json:"Id"`
	Name      string `json:"Name"`
	Scope     string `json:"Scope"`
	LockToken string `json:"LockToken"`
}

// Real WAFv2 sends Tags as a LIST of {Key,Value} structs, never a map.
type tagResourceRequest struct {
	ResourceARN string                `json:"ResourceARN"`
	Tags        []serviceutil.TagPair `json:"Tags"`
}
type tagResourceResponse struct{}

type untagResourceRequest struct {
	ResourceARN string   `json:"ResourceARN"`
	TagKeys     []string `json:"TagKeys"`
}
type untagResourceResponse struct{}

type listTagsForResourceRequest struct {
	ResourceARN string `json:"ResourceARN"`
}

// tagInfoForResource mirrors WAFv2's TagInfoForResource: TagList is a LIST
// of Tag structs, never a map.
type tagInfoForResource struct {
	ResourceARN string                `json:"ResourceARN"`
	TagList     []serviceutil.TagPair `json:"TagList"`
}

type listTagsForResourceResponse struct {
	TagInfoForResource tagInfoForResource `json:"TagInfoForResource"`
}

func (h *Handler) createWebACLTyped(ctx context.Context, req *createWebACLRequest) (*createWebACLResponse, *protocol.AWSError) {
	if req.Name == "" {
		return nil, protocol.ErrMissingParameter("Name")
	}
	if req.Scope == "" {
		return nil, protocol.ErrMissingParameter("Scope")
	}
	if aerr := validateScope(req.Scope); aerr != nil {
		return nil, aerr
	}
	tags := serviceutil.TagsFromList(req.Tags)
	if aerr := serviceutil.ValidateTags(wafTagCfg, tags); aerr != nil {
		return nil, aerr
	}

	id := generateID()
	token := generateID()
	acl := &WebACL{
		ID:               id,
		Name:             req.Name,
		Scope:            req.Scope,
		ARN:              h.wafARN(ctx, req.Scope, id),
		LockToken:        token,
		Description:      req.Description,
		DefaultAction:    req.DefaultAction,
		VisibilityConfig: req.VisibilityConfig,
		Rules:            req.Rules,
		Tags:             tags,
		CreatedAt:        h.clk.Now(),
	}

	raw, _ := json.Marshal(acl)
	if err := h.store.Set(ctx, nsWebACLs, h.storeKey(req.Scope, id), string(raw)); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publish(ctx, events.WAFWebACLCreated, acl)

	return &createWebACLResponse{
		Summary: webACLSummaryWire{
			Id:          acl.ID,
			Name:        acl.Name,
			Description: acl.Description,
			LockToken:   acl.LockToken,
			ARN:         acl.ARN,
		},
	}, nil
}

func (h *Handler) getWebACLTyped(ctx context.Context, req *getWebACLRequest) (*getWebACLResponse, *protocol.AWSError) {
	if aerr := validateScope(req.Scope); aerr != nil {
		return nil, aerr
	}
	acl, aerr := h.getACL(ctx, req.Scope, req.ID)
	if aerr != nil {
		return nil, aerr
	}

	return &getWebACLResponse{
		WebACL: webACLWire{
			Id:               acl.ID,
			Name:             acl.Name,
			ARN:              acl.ARN,
			Description:      acl.Description,
			DefaultAction:    acl.DefaultAction,
			VisibilityConfig: acl.VisibilityConfig,
			Rules:            acl.Rules,
		},
		LockToken: acl.LockToken,
	}, nil
}

func (h *Handler) listWebACLsTyped(ctx context.Context, req *listWebACLsRequest) (*listWebACLsResponse, *protocol.AWSError) {
	if aerr := validateScope(req.Scope); aerr != nil {
		return nil, aerr
	}
	prefix := serviceutil.RegionKey(h.cfg.Region, req.Scope+"/")
	pairs, err := h.store.Scan(ctx, nsWebACLs, prefix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}

	summaries := make([]webACLSummaryWire, 0, len(pairs))
	for _, kv := range pairs {
		var acl WebACL
		if json.Unmarshal([]byte(kv.Value), &acl) != nil {
			continue
		}
		summaries = append(summaries, webACLSummaryWire{
			Id:          acl.ID,
			Name:        acl.Name,
			Description: acl.Description,
			LockToken:   acl.LockToken,
			ARN:         acl.ARN,
		})
	}

	return &listWebACLsResponse{WebACLs: summaries}, nil
}

func (h *Handler) deleteWebACLTyped(ctx context.Context, req *deleteWebACLRequest) (*struct{}, *protocol.AWSError) {
	if aerr := validateScope(req.Scope); aerr != nil {
		return nil, aerr
	}
	acl, aerr := h.getACL(ctx, req.Scope, req.ID)
	if aerr != nil {
		return nil, aerr
	}

	if err := h.store.Delete(ctx, nsWebACLs, h.storeKey(req.Scope, req.ID)); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	h.publish(ctx, events.WAFWebACLDeleted, acl)

	return &struct{}{}, nil
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*tagResourceResponse, *protocol.AWSError) {
	scope, id, aerr := h.parseWAFARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	getter := func(ctx context.Context, _ string) (*WebACL, *protocol.AWSError) {
		return h.getACL(ctx, scope, id)
	}
	if aerr := serviceutil.ApplyInlineTags(ctx, scope+"/"+id, serviceutil.TagsFromList(req.Tags), wafTagCfg, getter, h.putACL); aerr != nil {
		return nil, aerr
	}
	return &tagResourceResponse{}, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*untagResourceResponse, *protocol.AWSError) {
	scope, id, aerr := h.parseWAFARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	getter := func(ctx context.Context, _ string) (*WebACL, *protocol.AWSError) {
		return h.getACL(ctx, scope, id)
	}
	if aerr := serviceutil.RemoveInlineTags(ctx, scope+"/"+id, req.TagKeys, getter, h.putACL); aerr != nil {
		return nil, aerr
	}
	return &untagResourceResponse{}, nil
}

func (h *Handler) listTagsForResourceTyped(ctx context.Context, req *listTagsForResourceRequest) (*listTagsForResourceResponse, *protocol.AWSError) {
	scope, id, aerr := h.parseWAFARN(req.ResourceARN)
	if aerr != nil {
		return nil, aerr
	}
	acl, aerr := h.getACL(ctx, scope, id)
	if aerr != nil {
		return nil, aerr
	}
	return &listTagsForResourceResponse{
		TagInfoForResource: tagInfoForResource{
			ResourceARN: req.ResourceARN,
			TagList:     serviceutil.TagsToList(acl.Tags),
		},
	}, nil
}
