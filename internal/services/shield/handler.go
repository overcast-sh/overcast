package shield

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/config"
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
	cfg     *config.Config
}

func newHandler(cfg *config.Config, st state.Store) *Handler {
	h := &Handler{store: newShieldStore(st), cfg: cfg}
	h.initOps()
	return h
}

// accountID returns the configured AWS account ID, falling back to the
// standard emulator account when no config is attached (unit-constructed
// handlers). Every protection ARN one server hands out names the same
// account, so protectionIDFromARN parses back what DescribeProtection and
// ListProtections handed out.
func (h *Handler) accountID() string {
	if h.cfg != nil && h.cfg.AccountID != "" {
		return h.cfg.AccountID
	}
	return "000000000000"
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

// createProtection, listProtections and describeProtection delegate to the
// typed implementations the JSON 1.0 / RPCv2 CBOR door already uses, so the
// two doors cannot drift on duplicate detection, ProtectionArn, filtering or
// pagination — the JSON 1.1 path is the one every SDK client takes by
// default, and a second copy of that logic is exactly how the two would
// disagree.
func (h *Handler) createProtection(w http.ResponseWriter, r *http.Request) {
	var req createProtectionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.createProtectionTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) listProtections(w http.ResponseWriter, r *http.Request) {
	var req listProtectionsRequest
	// Every ListProtectionsRequest member is optional, so a hand-built
	// caller may send no body at all; only a body that is present has to
	// parse. AWS SDK clients always send at least {}.
	if r.ContentLength != 0 && !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.listProtectionsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
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
	var req describeProtectionRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.describeProtectionTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
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

// protectionARN builds a protection's own ARN. Shield is a global service,
// so the ARN carries no region — arn:aws:shield::<account>:protection/<id>,
// the shape protectionIDFromARN parses back.
func protectionARN(accountID, protectionID string) string {
	return protocol.ARN("", accountID, serviceName, "protection/"+protectionID)
}

// resourceTypeFromARN derives a protected resource's ProtectedResourceType
// from its ARN, which is what ListProtections' ResourceTypes filter matches
// against. Protection carries no resource-type member of its own, so the ARN
// is the only evidence there is. Returns "" for an ARN naming nothing Shield
// can protect.
func resourceTypeFromARN(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	service, resource := parts[2], parts[5]
	switch service {
	case "cloudfront":
		if strings.HasPrefix(resource, "distribution/") {
			return "CLOUDFRONT_DISTRIBUTION"
		}
	case "route53":
		if strings.HasPrefix(resource, "hostedzone/") {
			return "ROUTE_53_HOSTED_ZONE"
		}
	case "ec2":
		if strings.HasPrefix(resource, "eip-allocation/") {
			return "ELASTIC_IP_ALLOCATION"
		}
	case "globalaccelerator":
		if strings.HasPrefix(resource, "accelerator/") {
			return "GLOBAL_ACCELERATOR"
		}
	case "elasticloadbalancing":
		// An Application Load Balancer nests its name under app/
		// (loadbalancer/app/<name>/<id>); a Classic Load Balancer names it
		// directly (loadbalancer/<name>). Both ARN forms are in
		// CreateProtectionRequest.ResourceArn's own documentation.
		if strings.HasPrefix(resource, "loadbalancer/app/") {
			return "APPLICATION_LOAD_BALANCER"
		}
		if strings.HasPrefix(resource, "loadbalancer/") {
			return "CLASSIC_LOAD_BALANCER"
		}
	}
	return ""
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
