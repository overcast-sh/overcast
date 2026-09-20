package cognito

import (
	"github.com/overcast-sh/overcast/internal/protocol/codec"
	"github.com/overcast-sh/overcast/internal/protocol/op"
)

func (s *Service) typedOps() map[string]op.Operation {
	return map[string]op.Operation{
		// Pool management
		"CreateUserPool":       op.NewTyped[CreateUserPoolReq, CreateUserPoolResp]("CreateUserPool", s.CreateUserPoolTyped),
		"DescribeUserPool":     op.NewTyped[UserPoolIDReq, DescribeUserPoolResp]("DescribeUserPool", s.DescribeUserPoolTyped),
		"DeleteUserPool":       op.NewTyped[UserPoolIDReq, struct{}]("DeleteUserPool", s.DeleteUserPoolTyped),
		"ListUserPools":        op.NewTyped[struct{}, ListUserPoolsResp]("ListUserPools", s.ListUserPoolsTyped),
		"UpdateUserPool":       op.NewTyped[UpdateUserPoolReq, struct{}]("UpdateUserPool", s.UpdateUserPoolTyped),
		"SetUserPoolMfaConfig": op.NewTyped[UserPoolMfaConfigReq, UserPoolMfaConfigResp]("SetUserPoolMfaConfig", s.SetUserPoolMfaConfigTyped),
		"GetUserPoolMfaConfig": op.NewTyped[UserPoolIDReq, UserPoolMfaConfigResp]("GetUserPoolMfaConfig", s.GetUserPoolMfaConfigTyped),
		// Domain management
		"CreateUserPoolDomain":   op.NewTyped[DomainAndPoolReq, struct{}]("CreateUserPoolDomain", s.CreateUserPoolDomainTyped),
		"DescribeUserPoolDomain": op.NewTyped[DescribeUserPoolDomainReq, DescribeUserPoolDomainResp]("DescribeUserPoolDomain", s.DescribeUserPoolDomainTyped),
		"DeleteUserPoolDomain":   op.NewTyped[DomainAndPoolReq, struct{}]("DeleteUserPoolDomain", s.DeleteUserPoolDomainTyped),
		"UpdateUserPoolDomain":   op.NewTyped[DomainAndPoolReq, struct{}]("UpdateUserPoolDomain", s.UpdateUserPoolDomainTyped),
		// Client management
		"CreateUserPoolClient":   op.NewTyped[CreateUserPoolClientReq, CreateUserPoolClientResp]("CreateUserPoolClient", s.CreateUserPoolClientTyped),
		"DescribeUserPoolClient": op.NewTyped[PoolAndClientReq, DescribeUserPoolClientResp]("DescribeUserPoolClient", s.DescribeUserPoolClientTyped),
		"DeleteUserPoolClient":   op.NewTyped[PoolAndClientReq, struct{}]("DeleteUserPoolClient", s.DeleteUserPoolClientTyped),
		"ListUserPoolClients":    op.NewTyped[UserPoolIDReq, ListUserPoolClientsResp]("ListUserPoolClients", s.ListUserPoolClientsTyped),
		"UpdateUserPoolClient":   op.NewTyped[UpdateUserPoolClientReq, UpdateUserPoolClientResp]("UpdateUserPoolClient", s.UpdateUserPoolClientTyped),
		// Admin user management
		"AdminCreateUser":             op.NewTyped[AdminCreateUserReq, AdminCreateUserResp]("AdminCreateUser", s.AdminCreateUserTyped),
		"AdminDeleteUser":             op.NewTyped[PoolAndUserReq, struct{}]("AdminDeleteUser", s.AdminDeleteUserTyped),
		"AdminGetUser":                op.NewTyped[PoolAndUserReq, AdminGetUserResp]("AdminGetUser", s.AdminGetUserTyped),
		"AdminSetUserPassword":        op.NewTyped[AdminSetUserPasswordReq, struct{}]("AdminSetUserPassword", s.AdminSetUserPasswordTyped),
		"AdminConfirmSignUp":          op.NewTyped[PoolAndUserReq, struct{}]("AdminConfirmSignUp", s.AdminConfirmSignUpTyped),
		"AdminUpdateUserAttributes":   op.NewTyped[AdminUpdateUserAttributesReq, struct{}]("AdminUpdateUserAttributes", s.AdminUpdateUserAttributesTyped),
		"AdminDeleteUserAttributes":   op.NewTyped[AdminDeleteUserAttributesReq, struct{}]("AdminDeleteUserAttributes", s.AdminDeleteUserAttributesTyped),
		"AdminDisableUser":            op.NewTyped[PoolAndUserReq, struct{}]("AdminDisableUser", s.AdminDisableUserTyped),
		"AdminEnableUser":             op.NewTyped[PoolAndUserReq, struct{}]("AdminEnableUser", s.AdminEnableUserTyped),
		"AdminInitiateAuth":           op.NewTyped[AdminInitiateAuthReq, InitiateAuthResp]("AdminInitiateAuth", s.AdminInitiateAuthTyped),
		"AdminRespondToAuthChallenge": op.NewTyped[AdminRespondToAuthChallengeReq, RespondToAuthChallengeResp]("AdminRespondToAuthChallenge", s.AdminRespondToAuthChallengeTyped),
		"ListUsers":                   op.NewTyped[ListUsersReq, ListUsersResp]("ListUsers", s.ListUsersTyped),
		// Self-service sign-up
		"SignUp":                 op.NewTyped[SignUpReq, SignUpResp]("SignUp", s.SignUpTyped),
		"ConfirmSignUp":          op.NewTyped[ConfirmSignUpReq, ConfirmSignUpResp]("ConfirmSignUp", s.ConfirmSignUpTyped),
		"ResendConfirmationCode": op.NewTyped[ClientUserSecretReq, struct{}]("ResendConfirmationCode", s.ResendConfirmationCodeTyped),
		// Auth flows
		"InitiateAuth":            op.NewTyped[InitiateAuthReq, InitiateAuthResp]("InitiateAuth", s.InitiateAuthTyped),
		"RespondToAuthChallenge":  op.NewTyped[RespondToAuthChallengeReq, RespondToAuthChallengeResp]("RespondToAuthChallenge", s.RespondToAuthChallengeTyped),
		"ConfirmDevice":           op.NewTyped[ConfirmDeviceReq, ConfirmDeviceResp]("ConfirmDevice", s.ConfirmDeviceTyped),
		"GetDevice":               op.NewTyped[DeviceKeyAccessReq, GetDeviceResp]("GetDevice", s.GetDeviceTyped),
		"ListDevices":             op.NewTyped[ListDevicesReq, ListDevicesResp]("ListDevices", s.ListDevicesTyped),
		"UpdateDeviceStatus":      op.NewTyped[UpdateDeviceStatusReq, struct{}]("UpdateDeviceStatus", s.UpdateDeviceStatusTyped),
		"ForgetDevice":            op.NewTyped[DeviceKeyAccessReq, struct{}]("ForgetDevice", s.ForgetDeviceTyped),
		"AdminGetDevice":          op.NewTyped[AdminDeviceReq, GetDeviceResp]("AdminGetDevice", s.AdminGetDeviceTyped),
		"AdminListDevices":        op.NewTyped[AdminListDevicesReq, ListDevicesResp]("AdminListDevices", s.AdminListDevicesTyped),
		"AdminUpdateDeviceStatus": op.NewTyped[AdminUpdateDeviceStatusReq, struct{}]("AdminUpdateDeviceStatus", s.AdminUpdateDeviceStatusTyped),
		"AdminForgetDevice":       op.NewTyped[AdminDeviceReq, struct{}]("AdminForgetDevice", s.AdminForgetDeviceTyped),
		// Password management
		"ForgotPassword":        op.NewTyped[ClientUserSecretReq, ForgotPasswordResp]("ForgotPassword", s.ForgotPasswordTyped),
		"ConfirmForgotPassword": op.NewTyped[ConfirmForgotPasswordReq, struct{}]("ConfirmForgotPassword", s.ConfirmForgotPasswordTyped),
		"ChangePassword":        op.NewTyped[ChangePasswordReq, struct{}]("ChangePassword", s.ChangePasswordTyped),
		// MFA
		"AssociateSoftwareToken":       op.NewTyped[AssociateSoftwareTokenReq, AssociateSoftwareTokenResp]("AssociateSoftwareToken", s.AssociateSoftwareTokenTyped),
		"VerifySoftwareToken":          op.NewTyped[VerifySoftwareTokenReq, VerifySoftwareTokenResp]("VerifySoftwareToken", s.VerifySoftwareTokenTyped),
		"StartWebAuthnRegistration":    op.NewTyped[AccessTokenReq, StartWebAuthnRegistrationResp]("StartWebAuthnRegistration", s.StartWebAuthnRegistrationTyped),
		"CompleteWebAuthnRegistration": op.NewTyped[CompleteWebAuthnRegistrationReq, struct{}]("CompleteWebAuthnRegistration", s.CompleteWebAuthnRegistrationTyped),
		"SetUserMFAPreference":         op.NewTyped[SetUserMFAPreferenceReq, struct{}]("SetUserMFAPreference", s.SetUserMFAPreferenceTyped),
		"AdminSetUserMFAPreference":    op.NewTyped[AdminSetUserMFAPreferenceReq, struct{}]("AdminSetUserMFAPreference", s.AdminSetUserMFAPreferenceTyped),
		// Group management
		"CreateGroup":              op.NewTyped[CreateGroupReq, CreateGroupResp]("CreateGroup", s.CreateGroupTyped),
		"GetGroup":                 op.NewTyped[PoolAndGroupReq, GetGroupResp]("GetGroup", s.GetGroupTyped),
		"DeleteGroup":              op.NewTyped[PoolAndGroupReq, struct{}]("DeleteGroup", s.DeleteGroupTyped),
		"UpdateGroup":              op.NewTyped[UpdateGroupReq, struct{}]("UpdateGroup", s.UpdateGroupTyped),
		"ListGroups":               op.NewTyped[PoolLimitReq, ListGroupsResp]("ListGroups", s.ListGroupsTyped),
		"AdminAddUserToGroup":      op.NewTyped[PoolAndUserGroupReq, struct{}]("AdminAddUserToGroup", s.AdminAddUserToGroupTyped),
		"AdminRemoveUserFromGroup": op.NewTyped[PoolAndUserGroupReq, struct{}]("AdminRemoveUserFromGroup", s.AdminRemoveUserFromGroupTyped),
		"AdminListGroupsForUser":   op.NewTyped[PoolAndUserLimitReq, ListGroupsResp]("AdminListGroupsForUser", s.AdminListGroupsForUserTyped),
		"ListUsersInGroup":         op.NewTyped[PoolAndGroupLimitReq, ListUsersInGroupResp]("ListUsersInGroup", s.ListUsersInGroupTyped),
		// Token / user info
		"GetUser":                          op.NewTyped[AccessTokenReq, GetUserResp]("GetUser", s.GetUserTyped),
		"UpdateUserAttributes":             op.NewTyped[UpdateUserAttributesReq, UpdateUserAttributesResp]("UpdateUserAttributes", s.UpdateUserAttributesTyped),
		"VerifyUserAttribute":              op.NewTyped[VerifyUserAttributeReq, struct{}]("VerifyUserAttribute", s.VerifyUserAttributeTyped),
		"GetUserAttributeVerificationCode": op.NewTyped[GetUserAttributeVerificationCodeReq, GetUserAttributeVerificationCodeResp]("GetUserAttributeVerificationCode", s.GetUserAttributeVerificationCodeTyped),
		"DeleteUserAttributes":             op.NewTyped[DeleteUserAttributesReq, struct{}]("DeleteUserAttributes", s.DeleteUserAttributesTyped),
		"GlobalSignOut":                    op.NewTyped[AccessTokenReq, struct{}]("GlobalSignOut", s.GlobalSignOutTyped),
		"RevokeToken":                      op.NewTyped[RevokeTokenReq, struct{}]("RevokeToken", s.RevokeTokenTyped),
		// Tags
		"TagResource":         op.NewTyped[TagResourceReq, struct{}]("TagResource", s.TagResourceTyped),
		"UntagResource":       op.NewTyped[UntagResourceReq, struct{}]("UntagResource", s.UntagResourceTyped),
		"ListTagsForResource": op.NewTyped[ListTagsForResourceReq, ListTagsForResourceResp]("ListTagsForResource", s.ListTagsForResourceTyped),
	}
}

// Operations implements router.ProtocolService.
func (s *Service) Operations() []op.Operation {
	ops := s.typedOp
	out := make([]op.Operation, 0, len(ops))
	for _, operation := range ops {
		out = append(out, operation)
	}
	return out
}

// SupportedProtocols implements router.ProtocolService.
func (s *Service) SupportedProtocols() []codec.Codec {
	return []codec.Codec{codec.JSON10, codec.JSON11, codec.RPCv2CBOR}
}
