package iam

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── respMeta ────────────────────────────────────────────────────────────────

type respMeta struct {
	RequestId string `xml:"RequestId"`
}

func metaFromCtx(ctx context.Context) respMeta {
	return respMeta{RequestId: protocol.RequestIDFromContext(ctx)}
}

// ─── Request types ──────────────────────────────────────────────────────────

type createUserReq struct {
	UserName            string     `json:"UserName"`
	Path                string     `json:"Path"`
	PermissionsBoundary string     `json:"PermissionsBoundary"`
	Tags                []tagEntry `json:"Tags"`
}

type getUserReq struct {
	UserName string `json:"UserName"`
}

type listUsersReq struct{}

type deleteUserReq struct {
	UserName string `json:"UserName"`
}

type updateUserReq struct {
	UserName    string `json:"UserName"`
	NewPath     string `json:"NewPath"`
	NewUserName string `json:"NewUserName"`
}

type createAccessKeyReq struct {
	UserName string `json:"UserName"`
}

type deleteAccessKeyReq struct {
	UserName    string `json:"UserName"`
	AccessKeyId string `json:"AccessKeyId"`
}

type listAccessKeysReq struct {
	UserName string `json:"UserName"`
}

type putUserPolicyReq struct {
	UserName       string `json:"UserName"`
	PolicyName     string `json:"PolicyName"`
	PolicyDocument string `json:"PolicyDocument"`
}

type getUserPolicyReq struct {
	UserName   string `json:"UserName"`
	PolicyName string `json:"PolicyName"`
}

type deleteUserPolicyReq struct {
	UserName   string `json:"UserName"`
	PolicyName string `json:"PolicyName"`
}

type createRoleReq struct {
	RoleName                 string     `json:"RoleName"`
	AssumeRolePolicyDocument string     `json:"AssumeRolePolicyDocument"`
	Path                     string     `json:"Path"`
	PermissionsBoundary      string     `json:"PermissionsBoundary"`
	Tags                     []tagEntry `json:"Tags"`
	Description              string     `json:"Description"`
	MaxSessionDuration       int        `json:"MaxSessionDuration"`
}

type getRoleReq struct {
	RoleName string `json:"RoleName"`
}

type listRolesReq struct{}

type deleteRoleReq struct {
	RoleName string `json:"RoleName"`
}

type putRolePolicyReq struct {
	RoleName       string `json:"RoleName"`
	PolicyName     string `json:"PolicyName"`
	PolicyDocument string `json:"PolicyDocument"`
}

type getRolePolicyReq struct {
	RoleName   string `json:"RoleName"`
	PolicyName string `json:"PolicyName"`
}

type listRolePoliciesReq struct {
	RoleName string `json:"RoleName"`
}

type deleteRolePolicyReq struct {
	RoleName   string `json:"RoleName"`
	PolicyName string `json:"PolicyName"`
}

type attachRolePolicyReq struct {
	RoleName  string `json:"RoleName"`
	PolicyArn string `json:"PolicyArn"`
}

type detachRolePolicyReq struct {
	RoleName  string `json:"RoleName"`
	PolicyArn string `json:"PolicyArn"`
}

type listAttachedRolePoliciesReq struct {
	RoleName string `json:"RoleName"`
}

type createInstanceProfileReq struct {
	InstanceProfileName string     `json:"InstanceProfileName"`
	Path                string     `json:"Path"`
	Tags                []tagEntry `json:"Tags"`
}

type deleteInstanceProfileReq struct {
	InstanceProfileName string `json:"InstanceProfileName"`
}

type getInstanceProfileReq struct {
	InstanceProfileName string `json:"InstanceProfileName"`
}

type addRoleToInstanceProfileReq struct {
	InstanceProfileName string `json:"InstanceProfileName"`
	RoleName            string `json:"RoleName"`
}

type removeRoleFromInstanceProfileReq struct {
	InstanceProfileName string `json:"InstanceProfileName"`
	RoleName            string `json:"RoleName"`
}

type createPolicyReq struct {
	PolicyName     string     `json:"PolicyName"`
	PolicyDocument string     `json:"PolicyDocument"`
	Path           string     `json:"Path"`
	Tags           []tagEntry `json:"Tags"`
	Description    string     `json:"Description"`
}

type getPolicyReq struct {
	PolicyArn string `json:"PolicyArn"`
}

type listPoliciesReq struct {
	Scope string `json:"Scope"`
}

type deletePolicyReq struct {
	PolicyArn string `json:"PolicyArn"`
}

type createGroupReq struct {
	GroupName string `json:"GroupName"`
	Path      string `json:"Path"`
}

type getGroupReq struct {
	GroupName string `json:"GroupName"`
	Marker    string `json:"Marker"`
	MaxItems  int    `json:"MaxItems"`
}

type deleteGroupReq struct {
	GroupName string `json:"GroupName"`
}

type addUserToGroupReq struct {
	GroupName string `json:"GroupName"`
	UserName  string `json:"UserName"`
}

type removeUserFromGroupReq struct {
	GroupName string `json:"GroupName"`
	UserName  string `json:"UserName"`
}

type listGroupsForUserReq struct {
	UserName string `json:"UserName"`
}

type listGroupsReq struct{}

type putGroupPolicyReq struct {
	GroupName      string `json:"GroupName"`
	PolicyName     string `json:"PolicyName"`
	PolicyDocument string `json:"PolicyDocument"`
}

type getGroupPolicyReq struct {
	GroupName  string `json:"GroupName"`
	PolicyName string `json:"PolicyName"`
}

type deleteGroupPolicyReq struct {
	GroupName  string `json:"GroupName"`
	PolicyName string `json:"PolicyName"`
}

type listGroupPoliciesReq struct {
	GroupName string `json:"GroupName"`
}

type attachGroupPolicyReq struct {
	GroupName string `json:"GroupName"`
	PolicyArn string `json:"PolicyArn"`
}

type detachGroupPolicyReq struct {
	GroupName string `json:"GroupName"`
	PolicyArn string `json:"PolicyArn"`
}

type listAttachedGroupPoliciesReq struct {
	GroupName string `json:"GroupName"`
}

type attachUserPolicyReq struct {
	UserName  string `json:"UserName"`
	PolicyArn string `json:"PolicyArn"`
}

type detachUserPolicyReq struct {
	UserName  string `json:"UserName"`
	PolicyArn string `json:"PolicyArn"`
}

type listAttachedUserPoliciesReq struct {
	UserName string `json:"UserName"`
}

type listUserPoliciesReq struct {
	UserName string `json:"UserName"`
}

type tagEntry struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type tagRoleReq struct {
	RoleName string     `json:"RoleName"`
	Tags     []tagEntry `json:"Tags"`
}

type untagRoleReq struct {
	RoleName string   `json:"RoleName"`
	TagKeys  []string `json:"TagKeys"`
}

type listRoleTagsReq struct {
	RoleName string `json:"RoleName"`
}

type tagUserReq struct {
	UserName string     `json:"UserName"`
	Tags     []tagEntry `json:"Tags"`
}

type untagUserReq struct {
	UserName string   `json:"UserName"`
	TagKeys  []string `json:"TagKeys"`
}

type listUserTagsReq struct {
	UserName string `json:"UserName"`
}

// IAM gives each taggable entity its own operation triple rather than a shared
// TagResource, and keys each one by that entity's own identifier — a policy by
// ARN, an instance profile by name.

type tagPolicyReq struct {
	PolicyArn string     `json:"PolicyArn"`
	Tags      []tagEntry `json:"Tags"`
}

type untagPolicyReq struct {
	PolicyArn string   `json:"PolicyArn"`
	TagKeys   []string `json:"TagKeys"`
}

type listPolicyTagsReq struct {
	PolicyArn string `json:"PolicyArn"`
}

type tagInstanceProfileReq struct {
	InstanceProfileName string     `json:"InstanceProfileName"`
	Tags                []tagEntry `json:"Tags"`
}

type untagInstanceProfileReq struct {
	InstanceProfileName string   `json:"InstanceProfileName"`
	TagKeys             []string `json:"TagKeys"`
}

type listInstanceProfileTagsReq struct {
	InstanceProfileName string `json:"InstanceProfileName"`
}

type createServiceLinkedRoleReq struct {
	AWSServiceName string `json:"AWSServiceName"`
}

type listInstanceProfilesForRoleReq struct {
	RoleName string `json:"RoleName"`
}

type updateAssumeRolePolicyReq struct {
	RoleName       string `json:"RoleName"`
	PolicyDocument string `json:"PolicyDocument"`
}

type listInstanceProfilesReq struct{}

type simulatePrincipalPolicyReq struct {
	PolicySourceArn                    string            `json:"PolicySourceArn"`
	PolicyInputList                    []string          `json:"PolicyInputList"`
	PermissionsBoundaryPolicyInputList []string          `json:"PermissionsBoundaryPolicyInputList"`
	ActionNames                        []string          `json:"ActionNames"`
	ResourceArns                       []string          `json:"ResourceArns"`
	ResourcePolicy                     string            `json:"ResourcePolicy"`
	ResourceOwner                      string            `json:"ResourceOwner"`
	CallerArn                          string            `json:"CallerArn"`
	ContextEntries                     []contextEntryReq `json:"ContextEntries"`
	Marker                             string            `json:"Marker"`
	MaxItems                           int               `json:"MaxItems"`
}

type getAccountAuthorizationDetailsReq struct{}

// ─── Response types ─────────────────────────────────────────────────────────

// --- Users ---

type createUserResp struct {
	XMLName struct{}         `xml:"CreateUserResponse"`
	Xmlns   string           `xml:"xmlns,attr"`
	Result  createUserResult `xml:"CreateUserResult"`
	Meta    respMeta         `xml:"ResponseMetadata"`
}
type createUserResult struct {
	User userXML `xml:"User"`
}

type getUserResp struct {
	XMLName struct{}      `xml:"GetUserResponse"`
	Xmlns   string        `xml:"xmlns,attr"`
	Result  getUserResult `xml:"GetUserResult"`
	Meta    respMeta      `xml:"ResponseMetadata"`
}
type getUserResult struct {
	User userXML `xml:"User"`
}

type listUsersResp struct {
	XMLName struct{}        `xml:"ListUsersResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  listUsersResult `xml:"ListUsersResult"`
	Meta    respMeta        `xml:"ResponseMetadata"`
}
type listUsersResult struct {
	Users       listMembersXML[userXML] `xml:"Users"`
	IsTruncated bool                    `xml:"IsTruncated"`
}

type deleteUserResp struct {
	XMLName struct{} `xml:"DeleteUserResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type updateUserResp struct {
	XMLName struct{} `xml:"UpdateUserResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Access Keys ---

type createAccessKeyResp struct {
	XMLName struct{}              `xml:"CreateAccessKeyResponse"`
	Xmlns   string                `xml:"xmlns,attr"`
	Result  createAccessKeyResult `xml:"CreateAccessKeyResult"`
	Meta    respMeta              `xml:"ResponseMetadata"`
}
type createAccessKeyResult struct {
	AccessKey accessKeyXML `xml:"AccessKey"`
}

type deleteAccessKeyResp struct {
	XMLName struct{} `xml:"DeleteAccessKeyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listAccessKeysResp struct {
	XMLName struct{}             `xml:"ListAccessKeysResponse"`
	Xmlns   string               `xml:"xmlns,attr"`
	Result  listAccessKeysResult `xml:"ListAccessKeysResult"`
	Meta    respMeta             `xml:"ResponseMetadata"`
}
type listAccessKeysResult struct {
	AccessKeyMetadata []accessKeyXML `xml:"AccessKeyMetadata>member"`
	IsTruncated       bool           `xml:"IsTruncated"`
}

// --- Inline User Policies ---

type putUserPolicyResp struct {
	XMLName struct{} `xml:"PutUserPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type getUserPolicyResp struct {
	XMLName struct{}            `xml:"GetUserPolicyResponse"`
	Xmlns   string              `xml:"xmlns,attr"`
	Result  getUserPolicyResult `xml:"GetUserPolicyResult"`
	Meta    respMeta            `xml:"ResponseMetadata"`
}
type getUserPolicyResult struct {
	UserName       string `xml:"UserName"`
	PolicyName     string `xml:"PolicyName"`
	PolicyDocument string `xml:"PolicyDocument"`
}

type deleteUserPolicyResp struct {
	XMLName struct{} `xml:"DeleteUserPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Roles ---

type createRoleResp struct {
	XMLName struct{}         `xml:"CreateRoleResponse"`
	Xmlns   string           `xml:"xmlns,attr"`
	Result  createRoleResult `xml:"CreateRoleResult"`
	Meta    respMeta         `xml:"ResponseMetadata"`
}
type createRoleResult struct {
	Role roleXML `xml:"Role"`
}

type getRoleResp struct {
	XMLName struct{}      `xml:"GetRoleResponse"`
	Xmlns   string        `xml:"xmlns,attr"`
	Result  getRoleResult `xml:"GetRoleResult"`
	Meta    respMeta      `xml:"ResponseMetadata"`
}
type getRoleResult struct {
	Role roleXML `xml:"Role"`
}

type listRolesResp struct {
	XMLName struct{}        `xml:"ListRolesResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  listRolesResult `xml:"ListRolesResult"`
	Meta    respMeta        `xml:"ResponseMetadata"`
}
type listRolesResult struct {
	Roles       listMembersXML[roleXML] `xml:"Roles"`
	IsTruncated bool                    `xml:"IsTruncated"`
}

type deleteRoleResp struct {
	XMLName struct{} `xml:"DeleteRoleResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Inline Role Policies ---

type putRolePolicyResp struct {
	XMLName struct{} `xml:"PutRolePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type getRolePolicyResp struct {
	XMLName struct{}            `xml:"GetRolePolicyResponse"`
	Xmlns   string              `xml:"xmlns,attr"`
	Result  getRolePolicyResult `xml:"GetRolePolicyResult"`
	Meta    respMeta            `xml:"ResponseMetadata"`
}
type getRolePolicyResult struct {
	RoleName       string `xml:"RoleName"`
	PolicyName     string `xml:"PolicyName"`
	PolicyDocument string `xml:"PolicyDocument"`
}

type listRolePoliciesResp struct {
	XMLName struct{}               `xml:"ListRolePoliciesResponse"`
	Xmlns   string                 `xml:"xmlns,attr"`
	Result  listRolePoliciesResult `xml:"ListRolePoliciesResult"`
	Meta    respMeta               `xml:"ResponseMetadata"`
}
type listRolePoliciesResult struct {
	PolicyNames listMembersXML[string] `xml:"PolicyNames"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

type deleteRolePolicyResp struct {
	XMLName struct{} `xml:"DeleteRolePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Managed Role Policies ---

type attachRolePolicyResp struct {
	XMLName struct{} `xml:"AttachRolePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type detachRolePolicyResp struct {
	XMLName struct{} `xml:"DetachRolePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listAttachedRolePoliciesResp struct {
	XMLName struct{}                       `xml:"ListAttachedRolePoliciesResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  listAttachedRolePoliciesResult `xml:"ListAttachedRolePoliciesResult"`
	Meta    respMeta                       `xml:"ResponseMetadata"`
}
type listAttachedRolePoliciesResult struct {
	AttachedPolicies listMembersXML[attachedPolicyXML] `xml:"AttachedPolicies"`
	IsTruncated      bool                              `xml:"IsTruncated"`
}

// --- Instance Profiles ---

type createInstanceProfileResp struct {
	XMLName struct{}                    `xml:"CreateInstanceProfileResponse"`
	Xmlns   string                      `xml:"xmlns,attr"`
	Result  createInstanceProfileResult `xml:"CreateInstanceProfileResult"`
	Meta    respMeta                    `xml:"ResponseMetadata"`
}
type createInstanceProfileResult struct {
	InstanceProfile instanceProfileXML `xml:"InstanceProfile"`
}

type deleteInstanceProfileResp struct {
	XMLName struct{} `xml:"DeleteInstanceProfileResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type getInstanceProfileResp struct {
	XMLName struct{}                 `xml:"GetInstanceProfileResponse"`
	Xmlns   string                   `xml:"xmlns,attr"`
	Result  getInstanceProfileResult `xml:"GetInstanceProfileResult"`
	Meta    respMeta                 `xml:"ResponseMetadata"`
}
type getInstanceProfileResult struct {
	InstanceProfile instanceProfileXML `xml:"InstanceProfile"`
}

type addRoleToInstanceProfileResp struct {
	XMLName struct{} `xml:"AddRoleToInstanceProfileResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type removeRoleFromInstanceProfileResp struct {
	XMLName struct{} `xml:"RemoveRoleFromInstanceProfileResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Managed Policies ---

type createPolicyResp struct {
	XMLName struct{}           `xml:"CreatePolicyResponse"`
	Xmlns   string             `xml:"xmlns,attr"`
	Result  createPolicyResult `xml:"CreatePolicyResult"`
	Meta    respMeta           `xml:"ResponseMetadata"`
}
type createPolicyResult struct {
	Policy policyXML `xml:"Policy"`
}

type getPolicyResp struct {
	XMLName struct{}        `xml:"GetPolicyResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  getPolicyResult `xml:"GetPolicyResult"`
	Meta    respMeta        `xml:"ResponseMetadata"`
}
type getPolicyResult struct {
	Policy policyXML `xml:"Policy"`
}

type listPoliciesResp struct {
	XMLName struct{}           `xml:"ListPoliciesResponse"`
	Xmlns   string             `xml:"xmlns,attr"`
	Result  listPoliciesResult `xml:"ListPoliciesResult"`
	Meta    respMeta           `xml:"ResponseMetadata"`
}
type listPoliciesResult struct {
	Policies    listMembersXML[policyXML] `xml:"Policies"`
	IsTruncated bool                      `xml:"IsTruncated"`
}

type deletePolicyResp struct {
	XMLName struct{} `xml:"DeletePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- Groups ---

type createGroupResp struct {
	XMLName struct{}          `xml:"CreateGroupResponse"`
	Xmlns   string            `xml:"xmlns,attr"`
	Result  createGroupResult `xml:"CreateGroupResult"`
	Meta    respMeta          `xml:"ResponseMetadata"`
}
type createGroupResult struct {
	Group groupXML `xml:"Group"`
}

type getGroupResp struct {
	XMLName struct{}       `xml:"GetGroupResponse"`
	Xmlns   string         `xml:"xmlns,attr"`
	Result  getGroupResult `xml:"GetGroupResult"`
	Meta    respMeta       `xml:"ResponseMetadata"`
}
type getGroupResult struct {
	Group       groupXML                `xml:"Group"`
	Users       listMembersXML[userXML] `xml:"Users"`
	IsTruncated bool                    `xml:"IsTruncated"`
	// Marker is present only on a truncated response, as on AWS.
	Marker string `xml:"Marker,omitempty"`
}

type deleteGroupResp struct {
	XMLName struct{} `xml:"DeleteGroupResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type addUserToGroupResp struct {
	XMLName struct{} `xml:"AddUserToGroupResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type removeUserFromGroupResp struct {
	XMLName struct{} `xml:"RemoveUserFromGroupResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listGroupsForUserResp struct {
	XMLName struct{}                `xml:"ListGroupsForUserResponse"`
	Xmlns   string                  `xml:"xmlns,attr"`
	Result  listGroupsForUserResult `xml:"ListGroupsForUserResult"`
	Meta    respMeta                `xml:"ResponseMetadata"`
}
type listGroupsForUserResult struct {
	Groups      listMembersXML[groupXML] `xml:"Groups"`
	IsTruncated bool                     `xml:"IsTruncated"`
}

type listGroupsResp struct {
	XMLName struct{}         `xml:"ListGroupsResponse"`
	Xmlns   string           `xml:"xmlns,attr"`
	Result  listGroupsResult `xml:"ListGroupsResult"`
	Meta    respMeta         `xml:"ResponseMetadata"`
}
type listGroupsResult struct {
	Groups      listMembersXML[groupXML] `xml:"Groups"`
	IsTruncated bool                     `xml:"IsTruncated"`
}

// --- Inline Group Policies ---

type putGroupPolicyResp struct {
	XMLName struct{} `xml:"PutGroupPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type getGroupPolicyResp struct {
	XMLName struct{}             `xml:"GetGroupPolicyResponse"`
	Xmlns   string               `xml:"xmlns,attr"`
	Result  getGroupPolicyResult `xml:"GetGroupPolicyResult"`
	Meta    respMeta             `xml:"ResponseMetadata"`
}
type getGroupPolicyResult struct {
	GroupName      string `xml:"GroupName"`
	PolicyName     string `xml:"PolicyName"`
	PolicyDocument string `xml:"PolicyDocument"`
}

type deleteGroupPolicyResp struct {
	XMLName struct{} `xml:"DeleteGroupPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listGroupPoliciesResp struct {
	XMLName struct{}                `xml:"ListGroupPoliciesResponse"`
	Xmlns   string                  `xml:"xmlns,attr"`
	Result  listGroupPoliciesResult `xml:"ListGroupPoliciesResult"`
	Meta    respMeta                `xml:"ResponseMetadata"`
}
type listGroupPoliciesResult struct {
	PolicyNames listMembersXML[string] `xml:"PolicyNames"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- Managed Group Policies ---

type attachGroupPolicyResp struct {
	XMLName struct{} `xml:"AttachGroupPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type detachGroupPolicyResp struct {
	XMLName struct{} `xml:"DetachGroupPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listAttachedGroupPoliciesResp struct {
	XMLName struct{}                        `xml:"ListAttachedGroupPoliciesResponse"`
	Xmlns   string                          `xml:"xmlns,attr"`
	Result  listAttachedGroupPoliciesResult `xml:"ListAttachedGroupPoliciesResult"`
	Meta    respMeta                        `xml:"ResponseMetadata"`
}
type listAttachedGroupPoliciesResult struct {
	AttachedPolicies listMembersXML[attachedPolicyXML] `xml:"AttachedPolicies"`
	IsTruncated      bool                              `xml:"IsTruncated"`
}

// --- Managed User Policies ---

type attachUserPolicyResp struct {
	XMLName struct{} `xml:"AttachUserPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type detachUserPolicyResp struct {
	XMLName struct{} `xml:"DetachUserPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listAttachedUserPoliciesResp struct {
	XMLName struct{}                       `xml:"ListAttachedUserPoliciesResponse"`
	Xmlns   string                         `xml:"xmlns,attr"`
	Result  listAttachedUserPoliciesResult `xml:"ListAttachedUserPoliciesResult"`
	Meta    respMeta                       `xml:"ResponseMetadata"`
}
type listAttachedUserPoliciesResult struct {
	AttachedPolicies listMembersXML[attachedPolicyXML] `xml:"AttachedPolicies"`
	IsTruncated      bool                              `xml:"IsTruncated"`
}

// --- Inline User Policy Listing ---

type listUserPoliciesResp struct {
	XMLName struct{}               `xml:"ListUserPoliciesResponse"`
	Xmlns   string                 `xml:"xmlns,attr"`
	Result  listUserPoliciesResult `xml:"ListUserPoliciesResult"`
	Meta    respMeta               `xml:"ResponseMetadata"`
}
type listUserPoliciesResult struct {
	PolicyNames listMembersXML[string] `xml:"PolicyNames"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- Role Tagging ---

type tagRoleResp struct {
	XMLName struct{} `xml:"TagRoleResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type untagRoleResp struct {
	XMLName struct{} `xml:"UntagRoleResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listRoleTagsResp struct {
	XMLName struct{}           `xml:"ListRoleTagsResponse"`
	Xmlns   string             `xml:"xmlns,attr"`
	Result  listRoleTagsResult `xml:"ListRoleTagsResult"`
	Meta    respMeta           `xml:"ResponseMetadata"`
}
type listRoleTagsResult struct {
	Tags        listMembersXML[tagXML] `xml:"Tags"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- User Tagging ---

type tagUserResp struct {
	XMLName struct{} `xml:"TagUserResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type untagUserResp struct {
	XMLName struct{} `xml:"UntagUserResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listUserTagsResp struct {
	XMLName struct{}           `xml:"ListUserTagsResponse"`
	Xmlns   string             `xml:"xmlns,attr"`
	Result  listUserTagsResult `xml:"ListUserTagsResult"`
	Meta    respMeta           `xml:"ResponseMetadata"`
}
type listUserTagsResult struct {
	Tags        listMembersXML[tagXML] `xml:"Tags"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- Managed Policy Tagging ---

type tagPolicyResp struct {
	XMLName struct{} `xml:"TagPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type untagPolicyResp struct {
	XMLName struct{} `xml:"UntagPolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listPolicyTagsResp struct {
	XMLName struct{}             `xml:"ListPolicyTagsResponse"`
	Xmlns   string               `xml:"xmlns,attr"`
	Result  listPolicyTagsResult `xml:"ListPolicyTagsResult"`
	Meta    respMeta             `xml:"ResponseMetadata"`
}
type listPolicyTagsResult struct {
	Tags        listMembersXML[tagXML] `xml:"Tags"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- Instance Profile Tagging ---

type tagInstanceProfileResp struct {
	XMLName struct{} `xml:"TagInstanceProfileResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type untagInstanceProfileResp struct {
	XMLName struct{} `xml:"UntagInstanceProfileResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

type listInstanceProfileTagsResp struct {
	XMLName struct{}                      `xml:"ListInstanceProfileTagsResponse"`
	Xmlns   string                        `xml:"xmlns,attr"`
	Result  listInstanceProfileTagsResult `xml:"ListInstanceProfileTagsResult"`
	Meta    respMeta                      `xml:"ResponseMetadata"`
}
type listInstanceProfileTagsResult struct {
	Tags        listMembersXML[tagXML] `xml:"Tags"`
	IsTruncated bool                   `xml:"IsTruncated"`
}

// --- Service-Linked Roles ---

type createServiceLinkedRoleResp struct {
	XMLName struct{}                      `xml:"CreateServiceLinkedRoleResponse"`
	Xmlns   string                        `xml:"xmlns,attr"`
	Result  createServiceLinkedRoleResult `xml:"CreateServiceLinkedRoleResult"`
	Meta    respMeta                      `xml:"ResponseMetadata"`
}
type createServiceLinkedRoleResult struct {
	Role roleXML `xml:"Role"`
}

// --- Instance Profiles For Role ---

type listInstanceProfilesForRoleResp struct {
	XMLName struct{}                          `xml:"ListInstanceProfilesForRoleResponse"`
	Xmlns   string                            `xml:"xmlns,attr"`
	Result  listInstanceProfilesForRoleResult `xml:"ListInstanceProfilesForRoleResult"`
	Meta    respMeta                          `xml:"ResponseMetadata"`
}
type listInstanceProfilesForRoleResult struct {
	InstanceProfiles listMembersXML[instanceProfileXML] `xml:"InstanceProfiles"`
	IsTruncated      bool                               `xml:"IsTruncated"`
}

// --- Role Mutation ---

type updateAssumeRolePolicyResp struct {
	XMLName struct{} `xml:"UpdateAssumeRolePolicyResponse"`
	Xmlns   string   `xml:"xmlns,attr"`
	Meta    respMeta `xml:"ResponseMetadata"`
}

// --- List Instance Profiles ---

type listInstanceProfilesResp struct {
	XMLName struct{}                   `xml:"ListInstanceProfilesResponse"`
	Xmlns   string                     `xml:"xmlns,attr"`
	Result  listInstanceProfilesResult `xml:"ListInstanceProfilesResult"`
	Meta    respMeta                   `xml:"ResponseMetadata"`
}
type listInstanceProfilesResult struct {
	InstanceProfiles listMembersXML[instanceProfileXML] `xml:"InstanceProfiles"`
	IsTruncated      bool                               `xml:"IsTruncated"`
}

// --- Policy Simulation (see simulate.go for the result types and logic) ---

type simulatePrincipalPolicyResp struct {
	XMLName struct{}          `xml:"SimulatePrincipalPolicyResponse"`
	Xmlns   string            `xml:"xmlns,attr"`
	Result  simulateResultXML `xml:"SimulatePrincipalPolicyResult"`
	Meta    respMeta          `xml:"ResponseMetadata"`
}

// --- GetAccountAuthorizationDetails ---

type getAccountAuthorizationDetailsResp struct {
	XMLName struct{}                             `xml:"GetAccountAuthorizationDetailsResponse"`
	Xmlns   string                               `xml:"xmlns,attr"`
	Result  getAccountAuthorizationDetailsResult `xml:"GetAccountAuthorizationDetailsResult"`
	Meta    respMeta                             `xml:"ResponseMetadata"`
}
type getAccountAuthorizationDetailsResult struct {
	UserDetailList  listMembersXML[userDetailXML]  `xml:"UserDetailList"`
	GroupDetailList listMembersXML[groupDetailXML] `xml:"GroupDetailList"`
	RoleDetailList  listMembersXML[roleDetailXML]  `xml:"RoleDetailList"`
	Policies        listMembersXML[policyXML]      `xml:"Policies"`
	IsTruncated     bool                           `xml:"IsTruncated"`
}

// ─── Typed handler functions ────────────────────────────────────────────────

// --- Users ---

func (h *Handler) createUserTyped(ctx context.Context, req *createUserReq) (*createUserResp, *protocol.AWSError) {
	name := req.UserName
	path := normPath(req.Path)
	if _, aerr := h.store.getUser(ctx, name); aerr == nil {
		return nil, errEntityAlreadyExists("user", name)
	}
	if req.PermissionsBoundary != "" {
		if aerr := h.checkPermissionsBoundaryExists(ctx, req.PermissionsBoundary); aerr != nil {
			return nil, aerr
		}
	}
	tags := createTags(req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	u := &User{
		UserName:            name,
		UserId:              iamID("AIDA", 17),
		Arn:                 h.store.arnForUser(path, name),
		Path:                path,
		CreateDate:          h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
		PermissionsBoundary: req.PermissionsBoundary,
		Tags:                tags,
	}
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMUserCreated, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: name}})
	}
	return &createUserResp{Xmlns: iamXMLNS, Result: createUserResult{User: toUserXMLWithTags(u)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getUserTyped(ctx context.Context, req *getUserReq) (*getUserResp, *protocol.AWSError) {
	name := req.UserName
	if name == "" {
		name = "caller"
	}
	u, aerr := h.store.getUser(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	return &getUserResp{Xmlns: iamXMLNS, Result: getUserResult{User: toUserXMLWithTags(u)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listUsersTyped(ctx context.Context, _ *listUsersReq) (*listUsersResp, *protocol.AWSError) {
	users, aerr := h.store.listUsers(ctx)
	if aerr != nil {
		return nil, aerr
	}
	xmlUsers := make([]userXML, 0, len(users))
	for i := range users {
		xmlUsers = append(xmlUsers, toUserXMLForList(&users[i]))
	}
	return &listUsersResp{Xmlns: iamXMLNS, Result: listUsersResult{
		Users: listMembersXML[userXML]{Members: xmlUsers, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) updateUserTyped(ctx context.Context, req *updateUserReq) (*updateUserResp, *protocol.AWSError) {
	name := req.UserName
	u, aerr := h.store.getUser(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	if req.NewPath != "" {
		u.Path = req.NewPath
		u.Arn = h.store.arnForUser(req.NewPath, u.UserName)
	}
	if req.NewUserName != "" {
		if aerr := h.store.deleteUser(ctx, name); aerr != nil {
			return nil, aerr
		}
		u.UserName = req.NewUserName
		u.Arn = h.store.arnForUser(u.Path, req.NewUserName)
	}
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &updateUserResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteUserTyped(ctx context.Context, req *deleteUserReq) (*deleteUserResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkUserDeletable(ctx, u); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteUser(ctx, req.UserName); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMUserDeleted, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: req.UserName}})
	}
	return &deleteUserResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- Access Keys ---

func (h *Handler) createAccessKeyTyped(ctx context.Context, req *createAccessKeyReq) (*createAccessKeyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	ak := AccessKey{
		AccessKeyId:     iamID("AKIA", 16),
		SecretAccessKey: randBase64(30),
		Status:          "Active",
		UserName:        req.UserName,
		CreateDate:      h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	u.AccessKeys = append(u.AccessKeys, ak)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &createAccessKeyResp{Xmlns: iamXMLNS, Result: createAccessKeyResult{AccessKey: toAccessKeyXML(&ak)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteAccessKeyTyped(ctx context.Context, req *deleteAccessKeyReq) (*deleteAccessKeyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	filtered := u.AccessKeys[:0]
	for _, ak := range u.AccessKeys {
		if ak.AccessKeyId != req.AccessKeyId {
			filtered = append(filtered, ak)
		}
	}
	u.AccessKeys = filtered
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &deleteAccessKeyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listAccessKeysTyped(ctx context.Context, req *listAccessKeysReq) (*listAccessKeysResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	members := make([]accessKeyXML, len(u.AccessKeys))
	for i := range u.AccessKeys {
		members[i] = toAccessKeyXML(&u.AccessKeys[i])
	}
	return &listAccessKeysResp{Xmlns: iamXMLNS, Result: listAccessKeysResult{
		AccessKeyMetadata: members, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Inline User Policies ---

func (h *Handler) putUserPolicyTyped(ctx context.Context, req *putUserPolicyReq) (*putUserPolicyResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.PolicyDocument); aerr != nil {
		return nil, aerr
	}
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	if u.InlinePolicies == nil {
		u.InlinePolicies = make(map[string]string)
	}
	u.InlinePolicies[req.PolicyName] = req.PolicyDocument
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &putUserPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getUserPolicyTyped(ctx context.Context, req *getUserPolicyReq) (*getUserPolicyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	doc, ok := u.InlinePolicies[req.PolicyName]
	if !ok {
		return nil, errNoSuchEntity("policy", req.PolicyName)
	}
	return &getUserPolicyResp{Xmlns: iamXMLNS, Result: getUserPolicyResult{
		UserName: req.UserName, PolicyName: req.PolicyName, PolicyDocument: encodePolicyDocument(doc),
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteUserPolicyTyped(ctx context.Context, req *deleteUserPolicyReq) (*deleteUserPolicyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	delete(u.InlinePolicies, req.PolicyName)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &deleteUserPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- Roles ---

func (h *Handler) createRoleTyped(ctx context.Context, req *createRoleReq) (*createRoleResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.AssumeRolePolicyDocument); aerr != nil {
		return nil, aerr
	}
	path := normPath(req.Path)
	if _, aerr := h.store.getRole(ctx, req.RoleName); aerr == nil {
		return nil, errEntityAlreadyExists("role", req.RoleName)
	}
	if req.PermissionsBoundary != "" {
		if aerr := h.checkPermissionsBoundaryExists(ctx, req.PermissionsBoundary); aerr != nil {
			return nil, aerr
		}
	}
	duration := req.MaxSessionDuration
	if duration == 0 {
		duration = defaultMaxSessionDuration
	} else if aerr := checkMaxSessionDuration(duration); aerr != nil {
		return nil, aerr
	}
	tags := createTags(req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	role := &Role{
		RoleName:                 req.RoleName,
		RoleId:                   iamID("AROA", 17),
		Arn:                      h.store.arnForRole(path, req.RoleName),
		Path:                     path,
		AssumeRolePolicyDocument: req.AssumeRolePolicyDocument,
		CreateDate:               h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
		PermissionsBoundary:      req.PermissionsBoundary,
		Tags:                     tags,
		Description:              req.Description,
		MaxSessionDuration:       duration,
	}
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMRoleCreated, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: req.RoleName}})
	}
	return &createRoleResp{Xmlns: iamXMLNS, Result: createRoleResult{Role: toRoleXMLWithTags(role)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getRoleTyped(ctx context.Context, req *getRoleReq) (*getRoleResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	return &getRoleResp{Xmlns: iamXMLNS, Result: getRoleResult{Role: toRoleXMLWithTags(role)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listRolesTyped(ctx context.Context, _ *listRolesReq) (*listRolesResp, *protocol.AWSError) {
	roles, aerr := h.store.listRoles(ctx)
	if aerr != nil {
		return nil, aerr
	}
	xmlRoles := make([]roleXML, 0, len(roles))
	for i := range roles {
		xmlRoles = append(xmlRoles, toRoleXMLForList(&roles[i]))
	}
	return &listRolesResp{Xmlns: iamXMLNS, Result: listRolesResult{
		Roles: listMembersXML[roleXML]{Members: xmlRoles, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteRoleTyped(ctx context.Context, req *deleteRoleReq) (*deleteRoleResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkRoleDeletable(ctx, role); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteRole(ctx, req.RoleName); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMRoleDeleted, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: req.RoleName}})
	}
	return &deleteRoleResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// sortedPolicyNames renders an entity's inline-policy names for a List*Policies
// response.
//
// The names are stored in a map, whose iteration order Go randomises on
// purpose, so building the list from a range gave a different order on every
// call. Sorting matches what every other IAM listing does —
// listUsers/listRoles/listPolicies/listGroups all sort by name in store.go —
// and gives a caller that diffs two responses nothing spurious to see.
func sortedPolicyNames(policies map[string]string) []string {
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// --- Inline Role Policies ---

func (h *Handler) putRolePolicyTyped(ctx context.Context, req *putRolePolicyReq) (*putRolePolicyResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.PolicyDocument); aerr != nil {
		return nil, aerr
	}
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	if role.InlinePolicies == nil {
		role.InlinePolicies = make(map[string]string)
	}
	role.InlinePolicies[req.PolicyName] = req.PolicyDocument
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &putRolePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getRolePolicyTyped(ctx context.Context, req *getRolePolicyReq) (*getRolePolicyResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	doc, ok := role.InlinePolicies[req.PolicyName]
	if !ok {
		return nil, errNoSuchEntity("policy", req.PolicyName)
	}
	return &getRolePolicyResp{Xmlns: iamXMLNS, Result: getRolePolicyResult{
		RoleName: req.RoleName, PolicyName: req.PolicyName, PolicyDocument: encodePolicyDocument(doc),
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listRolePoliciesTyped(ctx context.Context, req *listRolePoliciesReq) (*listRolePoliciesResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	return &listRolePoliciesResp{Xmlns: iamXMLNS, Result: listRolePoliciesResult{
		PolicyNames: listMembersXML[string]{Members: sortedPolicyNames(role.InlinePolicies), Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteRolePolicyTyped(ctx context.Context, req *deleteRolePolicyReq) (*deleteRolePolicyResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	delete(role.InlinePolicies, req.PolicyName)
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &deleteRolePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- Managed Role Policies ---

func (h *Handler) attachRolePolicyTyped(ctx context.Context, req *attachRolePolicyReq) (*attachRolePolicyResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	role.AttachedPolicies = ensureAttachedPolicy(role.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &attachRolePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) detachRolePolicyTyped(ctx context.Context, req *detachRolePolicyReq) (*detachRolePolicyResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	role.AttachedPolicies = removeAttachedPolicy(role.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &detachRolePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listAttachedRolePoliciesTyped(ctx context.Context, req *listAttachedRolePoliciesReq) (*listAttachedRolePoliciesResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	xmlPolicies := make([]attachedPolicyXML, 0, len(role.AttachedPolicies))
	for _, ap := range role.AttachedPolicies {
		xmlPolicies = append(xmlPolicies, attachedPolicyXML(ap))
	}
	return &listAttachedRolePoliciesResp{Xmlns: iamXMLNS, Result: listAttachedRolePoliciesResult{
		AttachedPolicies: listMembersXML[attachedPolicyXML]{Members: xmlPolicies, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Instance Profiles ---

func (h *Handler) createInstanceProfileTyped(ctx context.Context, req *createInstanceProfileReq) (*createInstanceProfileResp, *protocol.AWSError) {
	path := normPath(req.Path)
	if _, aerr := h.store.getProfile(ctx, req.InstanceProfileName); aerr == nil {
		return nil, errEntityAlreadyExists("instance profile", req.InstanceProfileName)
	}
	tags := createTags(req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	profile := &InstanceProfile{
		InstanceProfileName: req.InstanceProfileName,
		InstanceProfileId:   iamID("AIPA", 17),
		Arn:                 h.store.arnForProfile(path, req.InstanceProfileName),
		Path:                path,
		CreateDate:          h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Tags:                tags,
	}
	if aerr := h.store.putProfile(ctx, profile); aerr != nil {
		return nil, aerr
	}
	return &createInstanceProfileResp{Xmlns: iamXMLNS, Result: createInstanceProfileResult{
		InstanceProfile: toInstanceProfileXMLWithTags(profile, nil),
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteInstanceProfileTyped(ctx context.Context, req *deleteInstanceProfileReq) (*deleteInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkInstanceProfileDeletable(ctx, profile); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteProfile(ctx, req.InstanceProfileName); aerr != nil {
		return nil, aerr
	}
	return &deleteInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// instanceProfileRoleXML resolves the roles an instance profile names. The
// embedded roles carry no tags: AWS's own GetInstanceProfile,
// ListInstanceProfiles and ListInstanceProfilesForRole samples show them
// without, and nothing in the API Reference says they should have them.
func instanceProfileRoleXML(ctx context.Context, store *iamStore, profile *InstanceProfile) []roleXML {
	var roles []roleXML
	for _, rn := range profile.Roles {
		if role, aerr := store.getRole(ctx, rn); aerr == nil {
			roles = append(roles, toRoleXML(role))
		}
	}
	return roles
}

// toInstanceProfileResp renders an instance profile for the listing
// operations, which return the resource without its tags.
func toInstanceProfileResp(ctx context.Context, store *iamStore, profile *InstanceProfile) instanceProfileXML {
	return toInstanceProfileXML(profile, instanceProfileRoleXML(ctx, store, profile))
}

func (h *Handler) getInstanceProfileTyped(ctx context.Context, req *getInstanceProfileReq) (*getInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	return &getInstanceProfileResp{Xmlns: iamXMLNS, Result: getInstanceProfileResult{
		InstanceProfile: toInstanceProfileXMLWithTags(profile, instanceProfileRoleXML(ctx, h.store, profile)),
	}, Meta: metaFromCtx(ctx)}, nil
}

// maxRolesPerInstanceProfile is AWS's hard quota: "An instance profile can
// contain only one role, and this quota cannot be increased."
// https://docs.aws.amazon.com/IAM/latest/APIReference/API_AddRoleToInstanceProfile.html
const maxRolesPerInstanceProfile = 1

// errInstanceProfileRoleQuota is AWS's refusal of a second role on an instance
// profile: LimitExceeded (409), naming the quota as AWS spells it. The wording
// is AWS's own, reproduced verbatim for the same reason delete_conflict.go's
// messages are — tooling reads it.
// https://github.com/hashicorp/terraform/issues/3851
func errInstanceProfileRoleQuota() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "LimitExceeded",
		Message:    "Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1",
		HTTPStatus: http.StatusConflict,
	}
}

func (h *Handler) addRoleToInstanceProfileTyped(ctx context.Context, req *addRoleToInstanceProfileReq) (*addRoleToInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	if _, aerr := h.store.getRole(ctx, req.RoleName); aerr != nil {
		return nil, aerr
	}
	for _, rn := range profile.Roles {
		if rn == req.RoleName {
			return &addRoleToInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
		}
	}
	if len(profile.Roles) >= maxRolesPerInstanceProfile {
		return nil, errInstanceProfileRoleQuota()
	}
	profile.Roles = append(profile.Roles, req.RoleName)
	if aerr := h.store.putProfile(ctx, profile); aerr != nil {
		return nil, aerr
	}
	return &addRoleToInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) removeRoleFromInstanceProfileTyped(ctx context.Context, req *removeRoleFromInstanceProfileReq) (*removeRoleFromInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	filtered := profile.Roles[:0]
	for _, rn := range profile.Roles {
		if rn != req.RoleName {
			filtered = append(filtered, rn)
		}
	}
	profile.Roles = filtered
	if aerr := h.store.putProfile(ctx, profile); aerr != nil {
		return nil, aerr
	}
	return &removeRoleFromInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- Managed Policies ---

func (h *Handler) createPolicyTyped(ctx context.Context, req *createPolicyReq) (*createPolicyResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.PolicyDocument); aerr != nil {
		return nil, aerr
	}
	path := normPath(req.Path)
	arn := h.store.arnForPolicy(path, req.PolicyName)
	if _, aerr := h.store.getPolicy(ctx, arn); aerr == nil {
		return nil, errEntityAlreadyExists("policy", req.PolicyName)
	}
	tags := createTags(req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, tags); aerr != nil {
		return nil, aerr
	}
	p := &Policy{
		PolicyName:  req.PolicyName,
		PolicyId:    iamID("ANPA", 17),
		Arn:         arn,
		Path:        path,
		Document:    req.PolicyDocument,
		CreateDate:  h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Tags:        tags,
		Description: req.Description,
	}
	if aerr := h.store.putPolicy(ctx, p); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMPolicyCreated, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: req.PolicyName}})
	}
	// A policy that has just been created is attached to nothing.
	return &createPolicyResp{Xmlns: iamXMLNS, Result: createPolicyResult{Policy: toPolicyXMLWithTags(p, policyUsage{})}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getPolicyTyped(ctx context.Context, req *getPolicyReq) (*getPolicyResp, *protocol.AWSError) {
	p, aerr := h.store.getPolicy(ctx, req.PolicyArn)
	if aerr != nil {
		return nil, aerr
	}
	usage, aerr := h.store.policyUsageCounts(ctx)
	if aerr != nil {
		return nil, aerr
	}
	return &getPolicyResp{Xmlns: iamXMLNS, Result: getPolicyResult{Policy: toPolicyXMLWithTags(p, usage[p.Arn])}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listPoliciesTyped(ctx context.Context, _ *listPoliciesReq) (*listPoliciesResp, *protocol.AWSError) {
	policies, aerr := h.store.listPolicies(ctx)
	if aerr != nil {
		return nil, aerr
	}
	usage, aerr := h.store.policyUsageCounts(ctx)
	if aerr != nil {
		return nil, aerr
	}
	xmlPolicies := make([]policyXML, 0, len(policies))
	for i := range policies {
		xmlPolicies = append(xmlPolicies, toPolicyXMLForList(&policies[i], usage[policies[i].Arn]))
	}
	return &listPoliciesResp{Xmlns: iamXMLNS, Result: listPoliciesResult{
		Policies: listMembersXML[policyXML]{Members: xmlPolicies, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deletePolicyTyped(ctx context.Context, req *deletePolicyReq) (*deletePolicyResp, *protocol.AWSError) {
	p, aerr := h.store.getPolicy(ctx, req.PolicyArn)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkPolicyDeletable(ctx, p); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deletePolicy(ctx, req.PolicyArn); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMPolicyDeleted, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: req.PolicyArn}})
	}
	return &deletePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- Groups ---

func (h *Handler) createGroupTyped(ctx context.Context, req *createGroupReq) (*createGroupResp, *protocol.AWSError) {
	path := normPath(req.Path)
	if _, aerr := h.store.getGroup(ctx, req.GroupName); aerr == nil {
		return nil, errEntityAlreadyExists("group", req.GroupName)
	}
	g := &Group{
		GroupName:  req.GroupName,
		GroupId:    iamID("AGPA", 17),
		Arn:        h.store.arnForGroup(path, req.GroupName),
		Path:       path,
		CreateDate: h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &createGroupResp{Xmlns: iamXMLNS, Result: createGroupResult{Group: toGroupXML(g)}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getGroupTyped(ctx context.Context, req *getGroupReq) (*getGroupResp, *protocol.AWSError) {
	page, aerr := h.resolveGroupPage(ctx, req.GroupName, req.Marker, req.MaxItems)
	if aerr != nil {
		return nil, aerr
	}
	return &getGroupResp{Xmlns: iamXMLNS, Result: getGroupResult{
		Group:       toGroupXML(page.group),
		Users:       listMembersXML[userXML]{Members: page.users, Tag: "member"},
		IsTruncated: page.isTruncated,
		Marker:      page.marker,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteGroupTyped(ctx context.Context, req *deleteGroupReq) (*deleteGroupResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := h.checkGroupDeletable(ctx, g); aerr != nil {
		return nil, aerr
	}
	if aerr := h.store.deleteGroup(ctx, req.GroupName); aerr != nil {
		return nil, aerr
	}
	return &deleteGroupResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) addUserToGroupTyped(ctx context.Context, req *addUserToGroupReq) (*addUserToGroupResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	for _, m := range g.Members {
		if m == req.UserName {
			return &addUserToGroupResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
		}
	}
	g.Members = append(g.Members, req.UserName)
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &addUserToGroupResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) removeUserFromGroupTyped(ctx context.Context, req *removeUserFromGroupReq) (*removeUserFromGroupResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	filtered := g.Members[:0]
	for _, m := range g.Members {
		if m != req.UserName {
			filtered = append(filtered, m)
		}
	}
	g.Members = filtered
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &removeUserFromGroupResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listGroupsForUserTyped(ctx context.Context, req *listGroupsForUserReq) (*listGroupsForUserResp, *protocol.AWSError) {
	groups, aerr := h.store.listGroupsForUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	xmlGroups := make([]groupXML, 0, len(groups))
	for i := range groups {
		xmlGroups = append(xmlGroups, toGroupXML(&groups[i]))
	}
	return &listGroupsForUserResp{Xmlns: iamXMLNS, Result: listGroupsForUserResult{
		Groups: listMembersXML[groupXML]{Members: xmlGroups, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listGroupsTyped(ctx context.Context, _ *listGroupsReq) (*listGroupsResp, *protocol.AWSError) {
	groups, aerr := h.store.listGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}
	xmlGroups := make([]groupXML, 0, len(groups))
	for i := range groups {
		xmlGroups = append(xmlGroups, toGroupXML(&groups[i]))
	}
	return &listGroupsResp{Xmlns: iamXMLNS, Result: listGroupsResult{
		Groups: listMembersXML[groupXML]{Members: xmlGroups, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Inline Group Policies ---

func (h *Handler) putGroupPolicyTyped(ctx context.Context, req *putGroupPolicyReq) (*putGroupPolicyResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.PolicyDocument); aerr != nil {
		return nil, aerr
	}
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	if g.InlinePolicies == nil {
		g.InlinePolicies = make(map[string]string)
	}
	g.InlinePolicies[req.PolicyName] = req.PolicyDocument
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &putGroupPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) getGroupPolicyTyped(ctx context.Context, req *getGroupPolicyReq) (*getGroupPolicyResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	doc, ok := g.InlinePolicies[req.PolicyName]
	if !ok {
		return nil, errNoSuchEntity("policy", req.PolicyName)
	}
	return &getGroupPolicyResp{Xmlns: iamXMLNS, Result: getGroupPolicyResult{
		GroupName: req.GroupName, PolicyName: req.PolicyName, PolicyDocument: encodePolicyDocument(doc),
	}, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) deleteGroupPolicyTyped(ctx context.Context, req *deleteGroupPolicyReq) (*deleteGroupPolicyResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	delete(g.InlinePolicies, req.PolicyName)
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &deleteGroupPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listGroupPoliciesTyped(ctx context.Context, req *listGroupPoliciesReq) (*listGroupPoliciesResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	return &listGroupPoliciesResp{Xmlns: iamXMLNS, Result: listGroupPoliciesResult{
		PolicyNames: listMembersXML[string]{Members: sortedPolicyNames(g.InlinePolicies), Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Managed Group Policies ---

func (h *Handler) attachGroupPolicyTyped(ctx context.Context, req *attachGroupPolicyReq) (*attachGroupPolicyResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	g.AttachedPolicies = ensureAttachedPolicy(g.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &attachGroupPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) detachGroupPolicyTyped(ctx context.Context, req *detachGroupPolicyReq) (*detachGroupPolicyResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	g.AttachedPolicies = removeAttachedPolicy(g.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putGroup(ctx, g); aerr != nil {
		return nil, aerr
	}
	return &detachGroupPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listAttachedGroupPoliciesTyped(ctx context.Context, req *listAttachedGroupPoliciesReq) (*listAttachedGroupPoliciesResp, *protocol.AWSError) {
	g, aerr := h.store.getGroup(ctx, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	xmlPolicies := make([]attachedPolicyXML, 0, len(g.AttachedPolicies))
	for _, ap := range g.AttachedPolicies {
		xmlPolicies = append(xmlPolicies, attachedPolicyXML(ap))
	}
	return &listAttachedGroupPoliciesResp{Xmlns: iamXMLNS, Result: listAttachedGroupPoliciesResult{
		AttachedPolicies: listMembersXML[attachedPolicyXML]{Members: xmlPolicies, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Managed User Policies ---

func (h *Handler) attachUserPolicyTyped(ctx context.Context, req *attachUserPolicyReq) (*attachUserPolicyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	u.AttachedPolicies = ensureAttachedPolicy(u.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &attachUserPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) detachUserPolicyTyped(ctx context.Context, req *detachUserPolicyReq) (*detachUserPolicyResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	u.AttachedPolicies = removeAttachedPolicy(u.AttachedPolicies, req.PolicyArn)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &detachUserPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listAttachedUserPoliciesTyped(ctx context.Context, req *listAttachedUserPoliciesReq) (*listAttachedUserPoliciesResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	xmlPolicies := make([]attachedPolicyXML, 0, len(u.AttachedPolicies))
	for _, ap := range u.AttachedPolicies {
		xmlPolicies = append(xmlPolicies, attachedPolicyXML(ap))
	}
	return &listAttachedUserPoliciesResp{Xmlns: iamXMLNS, Result: listAttachedUserPoliciesResult{
		AttachedPolicies: listMembersXML[attachedPolicyXML]{Members: xmlPolicies, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Inline User Policy Listing ---

func (h *Handler) listUserPoliciesTyped(ctx context.Context, req *listUserPoliciesReq) (*listUserPoliciesResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	return &listUserPoliciesResp{Xmlns: iamXMLNS, Result: listUserPoliciesResult{
		PolicyNames: listMembersXML[string]{Members: sortedPolicyNames(u.InlinePolicies), Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Role Tagging ---

func (h *Handler) tagRoleTyped(ctx context.Context, req *tagRoleReq) (*tagRoleResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	merged := mergeTagEntries(role.GetTags(), req.Tags)
	// Validated against the merged set, not just the incoming delta, so the
	// 50-tag limit holds across repeated TagRole calls (#1052).
	if aerr := serviceutil.ValidateTags(iamTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	role.SetTags(merged)
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &tagRoleResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) untagRoleTyped(ctx context.Context, req *untagRoleReq) (*untagRoleResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	tags := role.GetTags()
	for _, k := range req.TagKeys {
		delete(tags, k)
	}
	role.SetTags(tags)
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &untagRoleResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listRoleTagsTyped(ctx context.Context, req *listRoleTagsReq) (*listRoleTagsResp, *protocol.AWSError) {
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	tagMap := role.GetTags()
	tags := make([]tagXML, 0, len(tagMap))
	for k, v := range tagMap {
		tags = append(tags, tagXML{Key: k, Value: v})
	}
	return &listRoleTagsResp{Xmlns: iamXMLNS, Result: listRoleTagsResult{
		Tags: listMembersXML[tagXML]{Members: tags, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- User Tagging ---

func (h *Handler) tagUserTyped(ctx context.Context, req *tagUserReq) (*tagUserResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	merged := mergeTagEntries(u.GetTags(), req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	u.SetTags(merged)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &tagUserResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) untagUserTyped(ctx context.Context, req *untagUserReq) (*untagUserResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	tags := u.GetTags()
	for _, k := range req.TagKeys {
		delete(tags, k)
	}
	u.SetTags(tags)
	if aerr := h.store.putUser(ctx, u); aerr != nil {
		return nil, aerr
	}
	return &untagUserResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listUserTagsTyped(ctx context.Context, req *listUserTagsReq) (*listUserTagsResp, *protocol.AWSError) {
	u, aerr := h.store.getUser(ctx, req.UserName)
	if aerr != nil {
		return nil, aerr
	}
	tagMap := u.GetTags()
	tags := make([]tagXML, 0, len(tagMap))
	for k, v := range tagMap {
		tags = append(tags, tagXML{Key: k, Value: v})
	}
	return &listUserTagsResp{Xmlns: iamXMLNS, Result: listUserTagsResult{
		Tags: listMembersXML[tagXML]{Members: tags, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Managed Policy Tagging ---

func (h *Handler) tagPolicyTyped(ctx context.Context, req *tagPolicyReq) (*tagPolicyResp, *protocol.AWSError) {
	p, aerr := h.store.getPolicy(ctx, req.PolicyArn)
	if aerr != nil {
		return nil, aerr
	}
	merged := mergeTagEntries(p.GetTags(), req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	p.SetTags(merged)
	if aerr := h.store.putPolicy(ctx, p); aerr != nil {
		return nil, aerr
	}
	return &tagPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) untagPolicyTyped(ctx context.Context, req *untagPolicyReq) (*untagPolicyResp, *protocol.AWSError) {
	p, aerr := h.store.getPolicy(ctx, req.PolicyArn)
	if aerr != nil {
		return nil, aerr
	}
	p.SetTags(removeTagKeys(p.GetTags(), req.TagKeys))
	if aerr := h.store.putPolicy(ctx, p); aerr != nil {
		return nil, aerr
	}
	return &untagPolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listPolicyTagsTyped(ctx context.Context, req *listPolicyTagsReq) (*listPolicyTagsResp, *protocol.AWSError) {
	p, aerr := h.store.getPolicy(ctx, req.PolicyArn)
	if aerr != nil {
		return nil, aerr
	}
	return &listPolicyTagsResp{Xmlns: iamXMLNS, Result: listPolicyTagsResult{
		Tags: listMembersXML[tagXML]{Members: sortedTagXML(p.GetTags()), Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Instance Profile Tagging ---

func (h *Handler) tagInstanceProfileTyped(ctx context.Context, req *tagInstanceProfileReq) (*tagInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	merged := mergeTagEntries(profile.GetTags(), req.Tags)
	if aerr := serviceutil.ValidateTags(iamTagCfg, merged); aerr != nil {
		return nil, aerr
	}
	profile.SetTags(merged)
	if aerr := h.store.putProfile(ctx, profile); aerr != nil {
		return nil, aerr
	}
	return &tagInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) untagInstanceProfileTyped(ctx context.Context, req *untagInstanceProfileReq) (*untagInstanceProfileResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	profile.SetTags(removeTagKeys(profile.GetTags(), req.TagKeys))
	if aerr := h.store.putProfile(ctx, profile); aerr != nil {
		return nil, aerr
	}
	return &untagInstanceProfileResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

func (h *Handler) listInstanceProfileTagsTyped(ctx context.Context, req *listInstanceProfileTagsReq) (*listInstanceProfileTagsResp, *protocol.AWSError) {
	profile, aerr := h.store.getProfile(ctx, req.InstanceProfileName)
	if aerr != nil {
		return nil, aerr
	}
	return &listInstanceProfileTagsResp{Xmlns: iamXMLNS, Result: listInstanceProfileTagsResult{
		Tags: listMembersXML[tagXML]{Members: sortedTagXML(profile.GetTags()), Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Tag helpers ---

// iamTagCfg tunes the shared tag validator to IAM's error shape (#1052).
// IAM reports the constraint violations its own Query/XML validation
// already models (a MaxSessionDuration outside range, e.g. typed_updates.go)
// as ValidationError, and declares no dedicated tag-count exception across
// its four taggable resource types, so the 50-tag limit is reported the
// same way an invalid key or value is.
var iamTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "ValidationError",
	InvalidCode:     "ValidationError",
	ExceededMessage: "A resource can have no more than 50 tags.",
}

// mergeTagEntries folds an inline Tags list into an entity's tag map. It
// returns a usable map even when the entity had none, which is what makes it
// safe to call straight from a Create* handler.
func mergeTagEntries(existing map[string]string, incoming []tagEntry) map[string]string {
	if existing == nil {
		existing = make(map[string]string, len(incoming))
	}
	for _, t := range incoming {
		existing[t.Key] = t.Value
	}
	return existing
}

// createTags builds the tag map for a Create* call's inline Tags. It returns
// nil when there are none, so an untagged entity keeps omitting the field.
func createTags(incoming []tagEntry) map[string]string {
	if len(incoming) == 0 {
		return nil
	}
	return mergeTagEntries(nil, incoming)
}

func removeTagKeys(existing map[string]string, keys []string) map[string]string {
	for _, k := range keys {
		delete(existing, k)
	}
	return existing
}

// sortedTagXML renders a tag map as a Key-sorted member list. Go randomizes
// map iteration per process, so without this the Tags list would reorder
// between otherwise identical responses.
func sortedTagXML(tags map[string]string) []tagXML {
	return serviceutil.TagElements(tags, func(k, v string) tagXML {
		return tagXML{Key: k, Value: v}
	})
}

// --- Service-Linked Roles ---

func (h *Handler) createServiceLinkedRoleTyped(ctx context.Context, req *createServiceLinkedRoleReq) (*createServiceLinkedRoleResp, *protocol.AWSError) {
	serviceName := req.AWSServiceName
	suffix := serviceName
	if idx := len(suffix) - len(".amazonaws.com"); idx > 0 && suffix[idx:] == ".amazonaws.com" {
		suffix = suffix[:idx]
	}
	roleName := "AWSServiceRoleFor" + strings.ToUpper(suffix[:1]) + suffix[1:]
	path := "/aws-service-role/" + serviceName + "/"

	if _, aerr := h.store.getRole(ctx, roleName); aerr == nil {
		return nil, errEntityAlreadyExists("role", roleName)
	}
	trustDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + serviceName + `"},"Action":"sts:AssumeRole"}]}`
	role := &Role{
		RoleName:                 roleName,
		RoleId:                   iamID("AROA", 17),
		Arn:                      h.store.arnForRole(path, roleName),
		Path:                     path,
		AssumeRolePolicyDocument: trustDoc,
		CreateDate:               h.clk.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{Type: events.IAMRoleCreated, Time: h.clk.Now(), Source: "iam", Payload: events.ResourcePayload{Name: roleName}})
	}
	return &createServiceLinkedRoleResp{Xmlns: iamXMLNS, Result: createServiceLinkedRoleResult{Role: toRoleXMLWithTags(role)}, Meta: metaFromCtx(ctx)}, nil
}

// --- Instance Profiles For Role ---

func (h *Handler) listInstanceProfilesForRoleTyped(ctx context.Context, req *listInstanceProfilesForRoleReq) (*listInstanceProfilesForRoleResp, *protocol.AWSError) {
	profiles, aerr := h.store.listProfilesForRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	var xmlProfiles []instanceProfileXML
	for i := range profiles {
		xmlProfiles = append(xmlProfiles, toInstanceProfileResp(ctx, h.store, &profiles[i]))
	}
	return &listInstanceProfilesForRoleResp{Xmlns: iamXMLNS, Result: listInstanceProfilesForRoleResult{
		InstanceProfiles: listMembersXML[instanceProfileXML]{Members: xmlProfiles, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- Role Mutation ---

func (h *Handler) updateAssumeRolePolicyTyped(ctx context.Context, req *updateAssumeRolePolicyReq) (*updateAssumeRolePolicyResp, *protocol.AWSError) {
	if aerr := checkPolicyDocument(req.PolicyDocument); aerr != nil {
		return nil, aerr
	}
	role, aerr := h.store.getRole(ctx, req.RoleName)
	if aerr != nil {
		return nil, aerr
	}
	role.AssumeRolePolicyDocument = req.PolicyDocument
	if aerr := h.store.putRole(ctx, role); aerr != nil {
		return nil, aerr
	}
	return &updateAssumeRolePolicyResp{Xmlns: iamXMLNS, Meta: metaFromCtx(ctx)}, nil
}

// --- List Instance Profiles ---

func (h *Handler) listInstanceProfilesTyped(ctx context.Context, _ *listInstanceProfilesReq) (*listInstanceProfilesResp, *protocol.AWSError) {
	profiles, aerr := h.store.listProfiles(ctx)
	if aerr != nil {
		return nil, aerr
	}
	var xmlProfiles []instanceProfileXML
	for i := range profiles {
		xmlProfiles = append(xmlProfiles, toInstanceProfileResp(ctx, h.store, &profiles[i]))
	}
	return &listInstanceProfilesResp{Xmlns: iamXMLNS, Result: listInstanceProfilesResult{
		InstanceProfiles: listMembersXML[instanceProfileXML]{Members: xmlProfiles, Tag: "member"}, IsTruncated: false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// --- GetAccountAuthorizationDetails ---

func (h *Handler) getAccountAuthorizationDetailsTyped(ctx context.Context, _ *getAccountAuthorizationDetailsReq) (*getAccountAuthorizationDetailsResp, *protocol.AWSError) {
	users, aerr := h.store.listUsers(ctx)
	if aerr != nil {
		return nil, aerr
	}
	groups, aerr := h.store.listGroups(ctx)
	if aerr != nil {
		return nil, aerr
	}
	roles, aerr := h.store.listRoles(ctx)
	if aerr != nil {
		return nil, aerr
	}
	policies, aerr := h.store.listPolicies(ctx)
	if aerr != nil {
		return nil, aerr
	}

	userDetails := make([]userDetailXML, 0, len(users))
	for _, u := range users {
		userDetails = append(userDetails, userDetailXML{
			Path:                    u.Path,
			UserName:                u.UserName,
			UserId:                  u.UserId,
			Arn:                     u.Arn,
			CreateDate:              u.CreateDate,
			UserPolicyList:          inlinePolicyListXML(u.InlinePolicies),
			AttachedManagedPolicies: attachedPolicyListXML(u.AttachedPolicies),
			PermissionsBoundary:     toPermissionsBoundaryXML(u.PermissionsBoundary),
		})
	}
	groupDetails := make([]groupDetailXML, 0, len(groups))
	for _, g := range groups {
		groupDetails = append(groupDetails, groupDetailXML{
			Path:                    g.Path,
			GroupName:               g.GroupName,
			GroupId:                 g.GroupId,
			Arn:                     g.Arn,
			CreateDate:              g.CreateDate,
			GroupPolicyList:         inlinePolicyListXML(g.InlinePolicies),
			AttachedManagedPolicies: attachedPolicyListXML(g.AttachedPolicies),
		})
	}
	roleDetails := make([]roleDetailXML, 0, len(roles))
	for _, ro := range roles {
		roleDetails = append(roleDetails, roleDetailXML{
			Path:                     ro.Path,
			RoleName:                 ro.RoleName,
			RoleId:                   ro.RoleId,
			Arn:                      ro.Arn,
			CreateDate:               ro.CreateDate,
			AssumeRolePolicyDocument: ro.AssumeRolePolicyDocument,
			RolePolicyList:           inlinePolicyListXML(ro.InlinePolicies),
			AttachedManagedPolicies:  attachedPolicyListXML(ro.AttachedPolicies),
			PermissionsBoundary:      toPermissionsBoundaryXML(ro.PermissionsBoundary),
		})
	}
	// The entities were loaded above for their own detail lists, so the usage
	// tally comes off those rather than scanning the store a second time.
	usage := policyUsageFrom(users, roles, groups)
	policyDetails := make([]policyXML, 0, len(policies))
	for i := range policies {
		policyDetails = append(policyDetails, toPolicyXML(&policies[i], usage[policies[i].Arn]))
	}

	return &getAccountAuthorizationDetailsResp{Xmlns: iamXMLNS, Result: getAccountAuthorizationDetailsResult{
		UserDetailList:  listMembersXML[userDetailXML]{Members: userDetails, Tag: "member"},
		GroupDetailList: listMembersXML[groupDetailXML]{Members: groupDetails, Tag: "member"},
		RoleDetailList:  listMembersXML[roleDetailXML]{Members: roleDetails, Tag: "member"},
		Policies:        listMembersXML[policyXML]{Members: policyDetails, Tag: "member"},
		IsTruncated:     false,
	}, Meta: metaFromCtx(ctx)}, nil
}

// typedPublish emits an event on the bus if wired, using context instead of *http.Request.
//
//nolint:unused // Kept for typed IAM operations that publish events.
func (h *Handler) typedPublish(ctx context.Context, t events.Type, payload any) {
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type: t, Time: h.clk.Now(), Source: "iam", Payload: payload,
		})
	}
}

var _ = json.Marshal // keep json import
