package iam

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

var iamMutateActions = map[string]bool{
	"PutUserPolicy":          true,
	"DeleteUserPolicy":       true,
	"AttachUserPolicy":       true,
	"DetachUserPolicy":       true,
	"PutRolePolicy":          true,
	"DeleteRolePolicy":       true,
	"AttachRolePolicy":       true,
	"DetachRolePolicy":       true,
	"PutGroupPolicy":         true,
	"DeleteGroupPolicy":      true,
	"AttachGroupPolicy":      true,
	"DetachGroupPolicy":      true,
	"CreatePolicy":           true,
	"DeletePolicy":           true,
	"CreatePolicyVersion":    true,
	"CreateUser":             true,
	"DeleteUser":             true,
	"UpdateUser":             true,
	"CreateRole":             true,
	"DeleteRole":             true,
	"CreateGroup":            true,
	"DeleteGroup":            true,
	"AddUserToGroup":         true,
	"RemoveUserFromGroup":    true,
	"CreateAccessKey":        true,
	"DeleteAccessKey":        true,
	"UpdateAssumeRolePolicy": true,
	// A permissions boundary caps what the principal's other policies grant, so
	// attaching, replacing or removing one changes its effective permissions
	// exactly as a policy change does.
	"PutUserPermissionsBoundary":    true,
	"DeleteUserPermissionsBoundary": true,
	"PutRolePermissionsBoundary":    true,
	"DeleteRolePermissionsBoundary": true,
}

const iamXMLNS = "https://iam.amazonaws.com/doc/2010-05-08/"

// Handler holds IAM handler dependencies.
type Handler struct {
	cfg     *config.Config
	log     *serviceutil.ServiceLogger
	clk     clock.Clock
	store   *iamStore
	bus     *events.Bus
	ops     map[string]http.HandlerFunc
	typedOp map[string]op.Operation
}

func newHandler(cfg *config.Config, store *iamStore, log *serviceutil.ServiceLogger, clk clock.Clock) *Handler {
	h := &Handler{cfg: cfg, log: log, clk: clk, store: store}
	h.initOps()
	return h
}

// initOps registers every known IAM operation for dispatch. Each operation
// is implemented exactly once, as a typed handler in typed_logic.go (or
// typed_updates.go / permissions_boundary.go / simulate.go); this map
// exists only to serve the AWS Query GET fallback that DispatchQuery falls
// back to when no codec was identified for the request (see dispatch
// below and delete_conflict_test.go) — every entry reaches its operation
// through typedHandler rather than a separate legacy implementation, so
// that fallback path can never drift from the typed one.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		// Users
		"CreateUser": h.typedHandler("CreateUser"),
		"GetUser":    h.typedHandler("GetUser"),
		"ListUsers":  h.typedHandler("ListUsers"),
		"UpdateUser": h.typedHandler("UpdateUser"),
		"DeleteUser": h.typedHandler("DeleteUser"),
		// Access keys
		"CreateAccessKey": h.typedHandler("CreateAccessKey"),
		"DeleteAccessKey": h.typedHandler("DeleteAccessKey"),
		"ListAccessKeys":  h.typedHandler("ListAccessKeys"),
		// Inline user policies
		"PutUserPolicy":    h.typedHandler("PutUserPolicy"),
		"GetUserPolicy":    h.typedHandler("GetUserPolicy"),
		"DeleteUserPolicy": h.typedHandler("DeleteUserPolicy"),
		// Roles
		"CreateRole": h.typedHandler("CreateRole"),
		"GetRole":    h.typedHandler("GetRole"),
		"ListRoles":  h.typedHandler("ListRoles"),
		"DeleteRole": h.typedHandler("DeleteRole"),
		"UpdateRole": h.typedHandler("UpdateRole"),
		// Inline role policies
		"PutRolePolicy":    h.typedHandler("PutRolePolicy"),
		"GetRolePolicy":    h.typedHandler("GetRolePolicy"),
		"ListRolePolicies": h.typedHandler("ListRolePolicies"),
		"DeleteRolePolicy": h.typedHandler("DeleteRolePolicy"),
		// Managed role policies
		"AttachRolePolicy":         h.typedHandler("AttachRolePolicy"),
		"DetachRolePolicy":         h.typedHandler("DetachRolePolicy"),
		"ListAttachedRolePolicies": h.typedHandler("ListAttachedRolePolicies"),
		// Instance profiles
		"CreateInstanceProfile":         h.typedHandler("CreateInstanceProfile"),
		"DeleteInstanceProfile":         h.typedHandler("DeleteInstanceProfile"),
		"GetInstanceProfile":            h.typedHandler("GetInstanceProfile"),
		"AddRoleToInstanceProfile":      h.typedHandler("AddRoleToInstanceProfile"),
		"RemoveRoleFromInstanceProfile": h.typedHandler("RemoveRoleFromInstanceProfile"),
		// Managed policies
		"CreatePolicy":        h.typedHandler("CreatePolicy"),
		"GetPolicy":           h.typedHandler("GetPolicy"),
		"ListPolicies":        h.typedHandler("ListPolicies"),
		"DeletePolicy":        h.typedHandler("DeletePolicy"),
		"CreatePolicyVersion": h.typedHandler("CreatePolicyVersion"),
		// Groups
		"CreateGroup":         h.typedHandler("CreateGroup"),
		"GetGroup":            h.typedHandler("GetGroup"),
		"DeleteGroup":         h.typedHandler("DeleteGroup"),
		"AddUserToGroup":      h.typedHandler("AddUserToGroup"),
		"RemoveUserFromGroup": h.typedHandler("RemoveUserFromGroup"),
		"ListGroupsForUser":   h.typedHandler("ListGroupsForUser"),
		"ListGroups":          h.typedHandler("ListGroups"),
		// Group inline policies
		"PutGroupPolicy":    h.typedHandler("PutGroupPolicy"),
		"GetGroupPolicy":    h.typedHandler("GetGroupPolicy"),
		"DeleteGroupPolicy": h.typedHandler("DeleteGroupPolicy"),
		"ListGroupPolicies": h.typedHandler("ListGroupPolicies"),
		// Managed group policies
		"AttachGroupPolicy":         h.typedHandler("AttachGroupPolicy"),
		"DetachGroupPolicy":         h.typedHandler("DetachGroupPolicy"),
		"ListAttachedGroupPolicies": h.typedHandler("ListAttachedGroupPolicies"),
		// Managed user policies
		"AttachUserPolicy":         h.typedHandler("AttachUserPolicy"),
		"DetachUserPolicy":         h.typedHandler("DetachUserPolicy"),
		"ListAttachedUserPolicies": h.typedHandler("ListAttachedUserPolicies"),
		// Inline user policy listing
		"ListUserPolicies": h.typedHandler("ListUserPolicies"),
		// Permissions boundaries
		"PutUserPermissionsBoundary":    h.typedHandler("PutUserPermissionsBoundary"),
		"DeleteUserPermissionsBoundary": h.typedHandler("DeleteUserPermissionsBoundary"),
		"PutRolePermissionsBoundary":    h.typedHandler("PutRolePermissionsBoundary"),
		"DeleteRolePermissionsBoundary": h.typedHandler("DeleteRolePermissionsBoundary"),
		// Role tagging
		"TagRole":      h.typedHandler("TagRole"),
		"UntagRole":    h.typedHandler("UntagRole"),
		"ListRoleTags": h.typedHandler("ListRoleTags"),
		// User tagging
		"TagUser":      h.typedHandler("TagUser"),
		"UntagUser":    h.typedHandler("UntagUser"),
		"ListUserTags": h.typedHandler("ListUserTags"),
		// Policy tagging
		"TagPolicy":      h.typedHandler("TagPolicy"),
		"UntagPolicy":    h.typedHandler("UntagPolicy"),
		"ListPolicyTags": h.typedHandler("ListPolicyTags"),
		// Instance-profile tagging
		"TagInstanceProfile":      h.typedHandler("TagInstanceProfile"),
		"UntagInstanceProfile":    h.typedHandler("UntagInstanceProfile"),
		"ListInstanceProfileTags": h.typedHandler("ListInstanceProfileTags"),
		// Service-linked roles
		"CreateServiceLinkedRole": h.typedHandler("CreateServiceLinkedRole"),
		// Instance profile by role
		"ListInstanceProfilesForRole": h.typedHandler("ListInstanceProfilesForRole"),
		// Role mutation
		"UpdateAssumeRolePolicy": h.typedHandler("UpdateAssumeRolePolicy"),
		// List instance profiles
		"ListInstanceProfiles": h.typedHandler("ListInstanceProfiles"),
		// Policy simulation
		"SimulatePrincipalPolicy": h.typedHandler("SimulatePrincipalPolicy"),
		"SimulateCustomPolicy":    h.typedHandler("SimulateCustomPolicy"),
		// Account-wide authorization details
		"GetAccountAuthorizationDetails": h.typedHandler("GetAccountAuthorizationDetails"),
	}
	h.typedOp = h.typedOps()
}

func (h *Handler) ownsAction(action string) bool {
	_, ok := h.ops[action]
	return ok
}

// typedHandler adapts a typed operation to the legacy dispatch table, for
// operations that have no separate legacy implementation. IAM speaks the Query
// protocol and nothing else, so the codec is fixed.
func (h *Handler) typedHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		typed, ok := h.typedOp[action]
		if !ok {
			protocol.WriteQueryXMLError(w, r, &protocol.AWSError{
				Code:       "InvalidAction",
				Message:    "The action " + action + " is not valid for IAM.",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		typed.Invoke(w, r, codec.QueryXML)
	}
}

func (h *Handler) dispatch(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("Action")
	if fn, ok := h.ops[action]; ok {
		fn(w, r)
		if iamMutateActions[action] {
			middleware.InvalidateIAMEnforceCache()
		}
		return
	}
	// InvalidAction needs no 501-for-a-real-operation split here, unlike the
	// JSON dispatchers (#1645): the router hands a Query request to IAM only
	// for an Action ownsAction claims, and answers a modeled IAM action
	// nobody implements with the house-rule 501 itself (router.rootDispatch
	// via operationRegistry.ClaimQuery), so an Action reaching this arm
	// bypassed the router.
	protocol.WriteQueryXMLError(w, r, &protocol.AWSError{
		Code:       "InvalidAction",
		Message:    "The action " + action + " is not valid for IAM.",
		HTTPStatus: http.StatusBadRequest,
	})
}

// ─── XML wire types ───────────────────────────────────────────────────────────

type userXML struct {
	Path                string                          `xml:"Path"`
	UserName            string                          `xml:"UserName"`
	UserId              string                          `xml:"UserId"`
	Arn                 string                          `xml:"Arn"`
	CreateDate          string                          `xml:"CreateDate"`
	PermissionsBoundary *attachedPermissionsBoundaryXML `xml:"PermissionsBoundary,omitempty"`
	Tags                *listMembersXML[tagXML]         `xml:"Tags,omitempty"`
}

// attachedPermissionsBoundaryXML is AWS's AttachedPermissionsBoundary, the
// shape User and Role carry their boundary in. The type member is AWS's
// PermissionsBoundaryAttachmentType enum, whose only value is
// "PermissionsBoundaryPolicy".
type attachedPermissionsBoundaryXML struct {
	PermissionsBoundaryType string `xml:"PermissionsBoundaryType"`
	PermissionsBoundaryArn  string `xml:"PermissionsBoundaryArn"`
}

// toPermissionsBoundaryXML renders a stored boundary ARN, or nil when the
// entity has none — AWS omits the member entirely rather than sending an empty
// one.
func toPermissionsBoundaryXML(arn string) *attachedPermissionsBoundaryXML {
	if strings.TrimSpace(arn) == "" {
		return nil
	}
	return &attachedPermissionsBoundaryXML{
		PermissionsBoundaryType: permissionsBoundaryTypePolicy,
		PermissionsBoundaryArn:  arn,
	}
}

type accessKeyXML struct {
	AccessKeyId     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	Status          string `xml:"Status"`
	UserName        string `xml:"UserName"`
	CreateDate      string `xml:"CreateDate"`
}

type roleXML struct {
	Path                     string                          `xml:"Path"`
	RoleName                 string                          `xml:"RoleName"`
	RoleId                   string                          `xml:"RoleId"`
	Arn                      string                          `xml:"Arn"`
	CreateDate               string                          `xml:"CreateDate"`
	AssumeRolePolicyDocument string                          `xml:"AssumeRolePolicyDocument"`
	Description              string                          `xml:"Description,omitempty"`
	MaxSessionDuration       int                             `xml:"MaxSessionDuration"`
	PermissionsBoundary      *attachedPermissionsBoundaryXML `xml:"PermissionsBoundary,omitempty"`
	Tags                     *listMembersXML[tagXML]         `xml:"Tags,omitempty"`
}

type policyXML struct {
	PolicyName       string `xml:"PolicyName"`
	PolicyId         string `xml:"PolicyId"`
	Arn              string `xml:"Arn"`
	Path             string `xml:"Path"`
	Description      string `xml:"Description,omitempty"`
	DefaultVersionId string `xml:"DefaultVersionId"`
	AttachmentCount  int    `xml:"AttachmentCount"`
	// PermissionsBoundaryUsageCount counts the users and roles the policy
	// bounds. AWS carries it alongside AttachmentCount on every Policy it
	// returns (IAM API Reference, API_Policy.html); the two move independently,
	// which is what lets a caller tell an attachment from a boundary.
	PermissionsBoundaryUsageCount int                     `xml:"PermissionsBoundaryUsageCount"`
	IsAttachable                  bool                    `xml:"IsAttachable"`
	CreateDate                    string                  `xml:"CreateDate"`
	UpdateDate                    string                  `xml:"UpdateDate"`
	Tags                          *listMembersXML[tagXML] `xml:"Tags,omitempty"`
}

type groupXML struct {
	Path       string `xml:"Path"`
	GroupName  string `xml:"GroupName"`
	GroupId    string `xml:"GroupId"`
	Arn        string `xml:"Arn"`
	CreateDate string `xml:"CreateDate"`
}

type instanceProfileXML struct {
	InstanceProfileName string                  `xml:"InstanceProfileName"`
	InstanceProfileId   string                  `xml:"InstanceProfileId"`
	Arn                 string                  `xml:"Arn"`
	Path                string                  `xml:"Path"`
	CreateDate          string                  `xml:"CreateDate"`
	Roles               listMembersXML[roleXML] `xml:"Roles"`
	Tags                *listMembersXML[tagXML] `xml:"Tags,omitempty"`
}

type attachedPolicyXML struct {
	PolicyArn  string `xml:"PolicyArn"`
	PolicyName string `xml:"PolicyName"`
}

type tagXML struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

// listMembersXML is a generic helper for lists that encode elements as <member>.
// AWS IAM uses <member> as the element tag for list items, not the field name.
type listMembersXML[T any] struct {
	Members []T
	Tag     string
}

func (l listMembersXML[T]) MarshalXML(enc *xml.Encoder, start xml.StartElement) error {
	if err := enc.EncodeToken(start); err != nil {
		return err
	}
	tag := l.Tag
	if tag == "" {
		tag = "member"
	}
	for _, m := range l.Members {
		if err := enc.EncodeElement(m, xml.StartElement{Name: xml.Name{Local: tag}}); err != nil {
			return err
		}
	}
	return enc.EncodeToken(start.End())
}

// ─── Conversion helpers ───────────────────────────────────────────────────────

// tagsXML renders a resource's tags as AWS's <Tags> member list, or nil when it
// has none — AWS omits the member rather than sending an empty one.
//
// Only the operations that return a whole resource carry it. AWS's listing
// operations "return a subset of the available attributes for the resource"
// and name Tags among the attributes they leave out (IAM API Reference,
// API_ListRoles.html and the ListUsers / ListPolicies / ListInstanceProfiles
// equivalents), pointing the caller at the matching Get instead. That is why
// each toXWithTags below is a separate call rather than a flag on toX: the
// listing paths must keep the subset shape, and so must a role embedded in an
// instance profile, which AWS's samples show without tags.
//
// A nil pointer is what omits the member: encoding/xml drops a nil pointer
// before it looks for a Marshaler, but a non-nil pointer to an *empty* list
// still renders as <Tags></Tags>, which AWS never sends. Every Tags field is
// therefore populated through this function and nowhere else.
func tagsXML(tags map[string]string) *listMembersXML[tagXML] {
	if len(tags) == 0 {
		return nil
	}
	return &listMembersXML[tagXML]{Members: sortedTagXML(tags), Tag: "member"}
}

func toUserXML(u *User) userXML {
	return userXML{
		Path:                u.Path,
		UserName:            u.UserName,
		UserId:              u.UserId,
		Arn:                 u.Arn,
		CreateDate:          u.CreateDate,
		PermissionsBoundary: toPermissionsBoundaryXML(u.PermissionsBoundary),
	}
}

// toUserXMLWithTags renders a user for GetUser / CreateUser, which return the
// whole resource including its tags.
func toUserXMLWithTags(u *User) userXML {
	x := toUserXML(u)
	x.Tags = tagsXML(u.Tags)
	return x
}

// toUserXMLForList renders a user for ListUsers, whose subset excludes
// PermissionsBoundary as well as Tags (IAM API Reference, API_ListUsers.html).
//
// GetGroup's Users are not trimmed: it carries no subset note and its members
// are the same full User shape GetUser returns.
func toUserXMLForList(u *User) userXML {
	x := toUserXML(u)
	x.PermissionsBoundary = nil
	return x
}

func toAccessKeyXML(ak *AccessKey) accessKeyXML {
	return accessKeyXML{
		AccessKeyId:     ak.AccessKeyId,
		SecretAccessKey: ak.SecretAccessKey,
		Status:          ak.Status,
		UserName:        ak.UserName,
		CreateDate:      ak.CreateDate,
	}
}

func toRoleXML(r *Role) roleXML {
	duration := r.MaxSessionDuration
	if duration == 0 {
		// A record persisted before the field existed carries AWS's default.
		duration = defaultMaxSessionDuration
	}
	return roleXML{
		Path:                     r.Path,
		RoleName:                 r.RoleName,
		RoleId:                   r.RoleId,
		Arn:                      r.Arn,
		CreateDate:               r.CreateDate,
		AssumeRolePolicyDocument: encodePolicyDocument(r.AssumeRolePolicyDocument),
		Description:              r.Description,
		MaxSessionDuration:       duration,
		PermissionsBoundary:      toPermissionsBoundaryXML(r.PermissionsBoundary),
	}
}

// toRoleXMLWithTags renders a role for GetRole / CreateRole /
// CreateServiceLinkedRole, which return the whole resource including its tags.
func toRoleXMLWithTags(r *Role) roleXML {
	x := toRoleXML(r)
	x.Tags = tagsXML(r.Tags)
	return x
}

// toRoleXMLForList renders a role for ListRoles, whose subset excludes
// PermissionsBoundary and RoleLastUsed as well as Tags (IAM API Reference,
// API_ListRoles.html). RoleLastUsed is not modelled at all, so the boundary is
// the only member to drop here.
//
// A role embedded in an instance profile is not trimmed: AWS's own
// GetAccountAuthorizationDetails sample shows those members carrying
// RoleLastUsed, so they are whole Role objects rather than a listing subset.
func toRoleXMLForList(r *Role) roleXML {
	x := toRoleXML(r)
	x.PermissionsBoundary = nil
	return x
}

// toPolicyXML renders a managed policy. usage carries the two counters, which
// are derived from the entities that refer to the policy rather than stored on
// it — see policyUsageFrom.
//
// IsAttachable is always true: AWS sets it false only for policies a caller
// may not attach, and every policy here is a customer managed one.
func toPolicyXML(p *Policy, usage policyUsage) policyXML {
	return policyXML{
		PolicyName:                    p.PolicyName,
		PolicyId:                      p.PolicyId,
		Arn:                           p.Arn,
		Path:                          p.Path,
		Description:                   p.Description,
		DefaultVersionId:              policyDefaultVersionID(p),
		AttachmentCount:               usage.Attachments,
		PermissionsBoundaryUsageCount: usage.BoundaryUses,
		IsAttachable:                  true,
		CreateDate:                    p.CreateDate,
		UpdateDate:                    p.CreateDate,
	}
}

// toPolicyXMLWithTags renders a policy for GetPolicy / CreatePolicy, which
// return the whole resource including its tags.
func toPolicyXMLWithTags(p *Policy, usage policyUsage) policyXML {
	x := toPolicyXML(p, usage)
	x.Tags = tagsXML(p.Tags)
	return x
}

// toPolicyXMLForList renders a policy for ListPolicies, which drops Description
// as well as Tags. ListPolicies' own note names only tags, but the exclusion is
// on the member: "This element is included in the response to the GetPolicy
// operation. It is not included in the response to the ListPolicies operation."
// (IAM API Reference, API_Policy.html, Description).
//
// GetAccountAuthorizationDetails keeps it: its policies are
// ManagedPolicyDetail, which documents Description with no such exclusion.
func toPolicyXMLForList(p *Policy, usage policyUsage) policyXML {
	x := toPolicyXML(p, usage)
	x.Description = ""
	return x
}

func toGroupXML(g *Group) groupXML {
	return groupXML{
		Path:       g.Path,
		GroupName:  g.GroupName,
		GroupId:    g.GroupId,
		Arn:        g.Arn,
		CreateDate: g.CreateDate,
	}
}

func toInstanceProfileXML(p *InstanceProfile, roles []roleXML) instanceProfileXML {
	if roles == nil {
		roles = []roleXML{}
	}
	return instanceProfileXML{
		InstanceProfileName: p.InstanceProfileName,
		InstanceProfileId:   p.InstanceProfileId,
		Arn:                 p.Arn,
		Path:                p.Path,
		CreateDate:          p.CreateDate,
		Roles:               listMembersXML[roleXML]{Members: roles, Tag: "member"},
	}
}

// toInstanceProfileXMLWithTags renders an instance profile for
// GetInstanceProfile / CreateInstanceProfile, which return the whole resource
// including its tags.
func toInstanceProfileXMLWithTags(p *InstanceProfile, roles []roleXML) instanceProfileXML {
	x := toInstanceProfileXML(p, roles)
	x.Tags = tagsXML(p.Tags)
	return x
}

// ─── ID and ARN helpers ───────────────────────────────────────────────────────

// iamID generates a random ID with the given prefix and n random uppercase+digit chars.
func iamID(prefix string, n int) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return prefix + string(b)
}

// randBase64 generates n random bytes encoded as base64.
func randBase64(n int) string {
	b := make([]byte, n)
	rand.Read(b) //nolint:errcheck
	return base64.StdEncoding.EncodeToString(b)
}

// ─── GetAccountAuthorizationDetails ──────────────────────────────────────────

type inlinePolicyXML struct {
	PolicyName     string `xml:"PolicyName"`
	PolicyDocument string `xml:"PolicyDocument"`
}

type userDetailXML struct {
	Path                    string                            `xml:"Path"`
	UserName                string                            `xml:"UserName"`
	UserId                  string                            `xml:"UserId"`
	Arn                     string                            `xml:"Arn"`
	CreateDate              string                            `xml:"CreateDate"`
	UserPolicyList          listMembersXML[inlinePolicyXML]   `xml:"UserPolicyList"`
	AttachedManagedPolicies listMembersXML[attachedPolicyXML] `xml:"AttachedManagedPolicies"`
	PermissionsBoundary     *attachedPermissionsBoundaryXML   `xml:"PermissionsBoundary,omitempty"`
}

type groupDetailXML struct {
	Path                    string                            `xml:"Path"`
	GroupName               string                            `xml:"GroupName"`
	GroupId                 string                            `xml:"GroupId"`
	Arn                     string                            `xml:"Arn"`
	CreateDate              string                            `xml:"CreateDate"`
	GroupPolicyList         listMembersXML[inlinePolicyXML]   `xml:"GroupPolicyList"`
	AttachedManagedPolicies listMembersXML[attachedPolicyXML] `xml:"AttachedManagedPolicies"`
}

type roleDetailXML struct {
	Path                     string                            `xml:"Path"`
	RoleName                 string                            `xml:"RoleName"`
	RoleId                   string                            `xml:"RoleId"`
	Arn                      string                            `xml:"Arn"`
	CreateDate               string                            `xml:"CreateDate"`
	AssumeRolePolicyDocument string                            `xml:"AssumeRolePolicyDocument"`
	RolePolicyList           listMembersXML[inlinePolicyXML]   `xml:"RolePolicyList"`
	AttachedManagedPolicies  listMembersXML[attachedPolicyXML] `xml:"AttachedManagedPolicies"`
	PermissionsBoundary      *attachedPermissionsBoundaryXML   `xml:"PermissionsBoundary,omitempty"`
}

// inlinePolicyListXML packages a name→document map as an XML member list,
// each document URL-encoded as PolicyDetail.PolicyDocument documents it.
func inlinePolicyListXML(m map[string]string) listMembersXML[inlinePolicyXML] {
	items := make([]inlinePolicyXML, 0, len(m))
	for name, doc := range m {
		items = append(items, inlinePolicyXML{PolicyName: name, PolicyDocument: encodePolicyDocument(doc)})
	}
	return listMembersXML[inlinePolicyXML]{Members: items, Tag: "member"}
}

// attachedPolicyListXML packages a slice of AttachedPolicy as an XML member list.
func attachedPolicyListXML(ap []AttachedPolicy) listMembersXML[attachedPolicyXML] {
	items := make([]attachedPolicyXML, 0, len(ap))
	for _, p := range ap {
		items = append(items, attachedPolicyXML(p))
	}
	return listMembersXML[attachedPolicyXML]{Members: items, Tag: "member"}
}

// ensureAttachedPolicy appends policyArn to existing unless already present.
// Returns the (possibly-unchanged) list.
func ensureAttachedPolicy(existing []AttachedPolicy, policyArn string) []AttachedPolicy {
	for _, ap := range existing {
		if ap.PolicyArn == policyArn {
			return existing
		}
	}
	return append(existing, AttachedPolicy{
		PolicyArn:  policyArn,
		PolicyName: policyNameFromARN(policyArn),
	})
}

// removeAttachedPolicy returns existing with any entry matching policyArn removed.
func removeAttachedPolicy(existing []AttachedPolicy, policyArn string) []AttachedPolicy {
	filtered := existing[:0]
	for _, ap := range existing {
		if ap.PolicyArn != policyArn {
			filtered = append(filtered, ap)
		}
	}
	return filtered
}

// policyNameFromARN extracts the policy name from an ARN.
// e.g. "arn:aws:iam::aws:policy/AmazonS3ReadOnlyAccess" -> "AmazonS3ReadOnlyAccess".
func policyNameFromARN(arn string) string {
	for i := len(arn) - 1; i >= 0; i-- {
		if arn[i] == '/' {
			return arn[i+1:]
		}
	}
	return arn
}

// encodePolicyDocument URL-encodes a policy document the way IAM does on the
// way out. The store keeps every document raw; each response member that
// carries one — Role.AssumeRolePolicyDocument (CreateRole, GetRole, ListRoles,
// the roles inside an InstanceProfile), the Get{User,Role,Group}Policy
// documents, and GetAccountAuthorizationDetails' RoleDetail trust policy and
// inline PolicyDetail documents — goes through here (#2180).
//
// AWS documents each of them as "URL-encoded compliant with RFC 3986"
// (IAM API Reference: API_Role.html, API_RoleDetail.html,
// API_PolicyDetail.html, API_GetRolePolicy.html) and tells callers to
// URL-decode it. botocore decodes on the client; the Go, JavaScript, Java,
// Rust and .NET SDKs hand the caller the encoded string.
//
// Go's url.QueryEscape is form encoding, which renders a space as "+" — a
// client decoding per RFC 3986 then gets a literal "+" where the space
// belonged, corrupting any policy document that is not minified JSON.
// Percent-encoding the space keeps the round trip exact.
func encodePolicyDocument(doc string) string {
	return strings.ReplaceAll(url.QueryEscape(doc), "+", "%20")
}
