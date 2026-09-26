package iam

import (
	"context"
	"net/http"
	"strings"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// Permissions boundaries: the managed policy that caps what a user's or role's
// identity policies can grant.
//
// A boundary grants nothing on its own. AWS computes a bounded entity's
// effective permissions as the intersection of its identity policies and its
// boundary, with an explicit deny in either being final — see
// [iampolicy.Evaluate]. Both policy simulation and opt-in request-time
// enforcement read the stored boundary, so what the simulator reports is what
// enforcement would decide.

// permissionsBoundaryTypePolicy is the only value of AWS's
// PermissionsBoundaryAttachmentType enum. The model names that member "Policy"
// but gives it the wire value "PermissionsBoundaryPolicy", and the wire value
// is what AWS sends and every SDK parses (#1920).
const permissionsBoundaryTypePolicy = "PermissionsBoundaryPolicy"

// ─── Request/response types ──────────────────────────────────────────────────

type putUserPermissionsBoundaryReq struct {
	UserName            string `json:"UserName"`
	PermissionsBoundary string `json:"PermissionsBoundary"`
}

type putUserPermissionsBoundaryResp struct {
	XMLName struct{} `xml:"PutUserPermissionsBoundaryResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type deleteUserPermissionsBoundaryReq struct {
	UserName string `json:"UserName"`
}

type deleteUserPermissionsBoundaryResp struct {
	XMLName struct{} `xml:"DeleteUserPermissionsBoundaryResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type putRolePermissionsBoundaryReq struct {
	RoleName            string `json:"RoleName"`
	PermissionsBoundary string `json:"PermissionsBoundary"`
}

type putRolePermissionsBoundaryResp struct {
	XMLName struct{} `xml:"PutRolePermissionsBoundaryResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type deleteRolePermissionsBoundaryReq struct {
	RoleName string `json:"RoleName"`
}

type deleteRolePermissionsBoundaryResp struct {
	XMLName struct{} `xml:"DeleteRolePermissionsBoundaryResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// ─── Typed handlers ──────────────────────────────────────────────────────────

func (h *Handler) putUserPermissionsBoundaryTyped(ctx context.Context, req *putUserPermissionsBoundaryReq) (*putUserPermissionsBoundaryResp, *protocol.AWSError) {
	if strings.TrimSpace(req.UserName) == "" {
		return nil, protocol.ErrMissingParameter("UserName")
	}
	if strings.TrimSpace(req.PermissionsBoundary) == "" {
		return nil, protocol.ErrMissingParameter("PermissionsBoundary")
	}
	user, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkPermissionsBoundaryExists(ctx, req.PermissionsBoundary); aerr != nil {
		return nil, aerr
	}
	user.PermissionsBoundary = req.PermissionsBoundary
	if aerr := h.store.putUser(ctx, user); aerr != nil {
		return nil, aerr
	}
	return &putUserPermissionsBoundaryResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteUserPermissionsBoundaryTyped(ctx context.Context, req *deleteUserPermissionsBoundaryReq) (*deleteUserPermissionsBoundaryResp, *protocol.AWSError) {
	if strings.TrimSpace(req.UserName) == "" {
		return nil, protocol.ErrMissingParameter("UserName")
	}
	user, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	// AWS succeeds whether or not a boundary was attached; only the user has
	// to exist.
	if user.PermissionsBoundary != "" {
		user.PermissionsBoundary = ""
		if aerr := h.store.putUser(ctx, user); aerr != nil {
			return nil, aerr
		}
	}
	return &deleteUserPermissionsBoundaryResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) putRolePermissionsBoundaryTyped(ctx context.Context, req *putRolePermissionsBoundaryReq) (*putRolePermissionsBoundaryResp, *protocol.AWSError) {
	if strings.TrimSpace(req.RoleName) == "" {
		return nil, protocol.ErrMissingParameter("RoleName")
	}
	if strings.TrimSpace(req.PermissionsBoundary) == "" {
		return nil, protocol.ErrMissingParameter("PermissionsBoundary")
	}
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkPermissionsBoundaryExists(ctx, req.PermissionsBoundary); aerr != nil {
		return nil, aerr
	}
	role.PermissionsBoundary = req.PermissionsBoundary
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &putRolePermissionsBoundaryResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteRolePermissionsBoundaryTyped(ctx context.Context, req *deleteRolePermissionsBoundaryReq) (*deleteRolePermissionsBoundaryResp, *protocol.AWSError) {
	if strings.TrimSpace(req.RoleName) == "" {
		return nil, protocol.ErrMissingParameter("RoleName")
	}
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	if role.PermissionsBoundary != "" {
		role.PermissionsBoundary = ""
		if aerr := h.store.putRole(ctx, role); aerr != nil {
			return nil, aerr
		}
	}
	return &deleteRolePermissionsBoundaryResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// ─── Boundary resolution ─────────────────────────────────────────────────────

// checkPermissionsBoundaryExists refuses a boundary ARN that names no managed
// policy, which is what AWS does: a boundary is always an attachable managed
// policy, so one that cannot be resolved is NoSuchEntity rather than something
// to store and discover later.
func (h *Handler) checkPermissionsBoundaryExists(ctx context.Context, arn string) *protocol.AWSError {
	notAttachable := &protocol.AWSError{
		Code:       "NoSuchEntity",
		Message:    "Policy " + arn + " does not exist or is not attachable.",
		HTTPStatus: http.StatusNotFound,
	}
	policy, aerr := h.store.getPolicy(ctx, arn)
	if aerr != nil {
		// Anything but "no such policy" is an infrastructure failure and stays
		// as it is; only the not-found case becomes AWS's boundary wording.
		if aerr.HTTPStatus == http.StatusNotFound {
			return notAttachable
		}
		return aerr
	}
	if strings.TrimSpace(policy.Document) == "" {
		return notAttachable
	}
	return nil
}

// resolvePermissionsBoundary resolves a stored boundary ARN to the compiled
// statements of its managed policy. It returns nil when no boundary is
// attached, which is what tells the evaluator to leave the decision uncapped.
//
// A boundary that is attached but cannot be resolved — the managed policy is
// gone, its record does not decode, or its document is not a valid policy —
// resolves to a boundary carrying no statements, which allows nothing. Reading
// it as absent instead would grant exactly the permissions the boundary exists
// to withhold; failing the whole call would let one corrupt record break an
// otherwise healthy principal, which AGENTS.md § "Malformed persisted state
// must be isolated" rules out.
func (h *Handler) resolvePermissionsBoundary(ctx context.Context, arn string) *iampolicy.Boundary {
	if strings.TrimSpace(arn) == "" {
		return nil
	}
	policy, aerr := h.store.getPolicy(ctx, arn)
	if aerr != nil || strings.TrimSpace(policy.Document) == "" {
		h.log.Warn("permissions boundary policy could not be read; the boundary now allows nothing",
			zap.String("policy_arn", arn))
		return &iampolicy.Boundary{}
	}
	name := policy.PolicyName
	if name == "" {
		name = arn
	}
	boundary, err := iampolicy.NewBoundary(policy.Document,
		iampolicy.SourceRef{ID: name, Type: iampolicy.SourceTypeIAMPolicy})
	if err != nil {
		h.log.Warn("permissions boundary policy could not be parsed; the boundary now allows nothing",
			zap.String("policy_arn", arn), zap.Error(err))
	}
	return boundary
}
