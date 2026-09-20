package cognito

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── shared request types ─────────────────────────────────────────────────────

// UserPoolIDReq is shared by DescribeUserPool and DeleteUserPool.
type UserPoolIDReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
}

type ListUsersReq struct {
	UserPoolID      string   `json:"UserPoolId" cbor:"UserPoolId"`
	AttributesToGet []string `json:"AttributesToGet" cbor:"AttributesToGet"`
	Filter          string   `json:"Filter" cbor:"Filter"`
	Limit           int      `json:"Limit" cbor:"Limit"`
	PaginationToken string   `json:"PaginationToken" cbor:"PaginationToken"`
}

// DomainAndPoolReq is shared by CreateUserPoolDomain, DeleteUserPoolDomain,
// UpdateUserPoolDomain.
type DomainAndPoolReq struct {
	Domain     string `json:"Domain" cbor:"Domain"`
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
}

// PoolAndClientReq is shared by DescribeUserPoolClient, DeleteUserPoolClient.
type PoolAndClientReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	ClientID   string `json:"ClientId" cbor:"ClientId"`
}

// PoolAndGroupReq is shared by GetGroup, DeleteGroup.
type PoolAndGroupReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	GroupName  string `json:"GroupName" cbor:"GroupName"`
}

// PoolAndUserReq is shared by AdminDeleteUser, AdminGetUser, AdminConfirmSignUp,
// AdminDisableUser, AdminEnableUser.
type PoolAndUserReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Username   string `json:"Username" cbor:"Username"`
}

// AccessTokenReq is shared by GetUser, GlobalSignOut, AssociateSoftwareToken.
type AccessTokenReq struct {
	AccessToken string `json:"AccessToken" cbor:"AccessToken"`
}

type CompleteWebAuthnRegistrationReq struct {
	AccessToken string `json:"AccessToken" cbor:"AccessToken"`
	Credential  any    `json:"Credential" cbor:"Credential"`
}

type UserPoolMfaConfigReq struct {
	UserPoolID                    string                     `json:"UserPoolId" cbor:"UserPoolId"`
	MfaConfiguration              string                     `json:"MfaConfiguration" cbor:"MfaConfiguration"`
	SmsMfaConfiguration           *SmsMfaConfig              `json:"SmsMfaConfiguration" cbor:"SmsMfaConfiguration"`
	SoftwareTokenMfaConfiguration *SoftwareTokenMfaConfig    `json:"SoftwareTokenMfaConfiguration" cbor:"SoftwareTokenMfaConfiguration"`
	EmailMfaConfiguration         *EmailMfaConfig            `json:"EmailMfaConfiguration" cbor:"EmailMfaConfiguration"`
	WebAuthnConfiguration         *webAuthnConfigurationWire `json:"WebAuthnConfiguration" cbor:"WebAuthnConfiguration"`
}

// ClientUserSecretReq is shared by ForgotPassword, ResendConfirmationCode.
type ClientUserSecretReq struct {
	ClientID   string `json:"ClientId" cbor:"ClientId"`
	Username   string `json:"Username" cbor:"Username"`
	SecretHash string `json:"SecretHash" cbor:"SecretHash"`
}

// ─── pool request types ───────────────────────────────────────────────────────

type CreateUserPoolReq struct {
	PoolName                    string                           `json:"PoolName" cbor:"PoolName"`
	UserPoolTier                string                           `json:"UserPoolTier" cbor:"UserPoolTier"`
	VerificationMessageTemplate *verificationMessageTemplateWire `json:"VerificationMessageTemplate" cbor:"VerificationMessageTemplate"`
	AdminCreateUserConfig       *adminCreateUserConfigWire       `json:"AdminCreateUserConfig" cbor:"AdminCreateUserConfig"`
	EmailConfiguration          *emailConfigurationWire          `json:"EmailConfiguration" cbor:"EmailConfiguration"`
	UserAttributeUpdateSettings *userAttributeUpdateSettingsWire `json:"UserAttributeUpdateSettings" cbor:"UserAttributeUpdateSettings"`
	AutoVerifiedAttributes      []string                         `json:"AutoVerifiedAttributes" cbor:"AutoVerifiedAttributes"`
	DeviceConfiguration         *DeviceConfiguration             `json:"DeviceConfiguration" cbor:"DeviceConfiguration"`
	UsernameAttributes          []string                         `json:"UsernameAttributes" cbor:"UsernameAttributes"`
	AliasAttributes             []string                         `json:"AliasAttributes" cbor:"AliasAttributes"`
	Policies                    *userPoolPoliciesWire            `json:"Policies" cbor:"Policies"`
	AccountRecoverySetting      *AccountRecoverySetting          `json:"AccountRecoverySetting" cbor:"AccountRecoverySetting"`
	SmsConfiguration            *SmsConfiguration                `json:"SmsConfiguration" cbor:"SmsConfiguration"`
	SmsAuthenticationMessage    string                           `json:"SmsAuthenticationMessage" cbor:"SmsAuthenticationMessage"`
	SmsVerificationMessage      string                           `json:"SmsVerificationMessage" cbor:"SmsVerificationMessage"`
	EmailVerificationMessage    string                           `json:"EmailVerificationMessage" cbor:"EmailVerificationMessage"`
	EmailVerificationSubject    string                           `json:"EmailVerificationSubject" cbor:"EmailVerificationSubject"`
	LambdaConfig                *LambdaConfig                    `json:"LambdaConfig" cbor:"LambdaConfig"`
	UserPoolTags                map[string]string                `json:"UserPoolTags" cbor:"UserPoolTags"`
}

type UpdateUserPoolReq struct {
	UserPoolID                  string                           `json:"UserPoolId" cbor:"UserPoolId"`
	UserPoolTier                string                           `json:"UserPoolTier" cbor:"UserPoolTier"`
	VerificationMessageTemplate *verificationMessageTemplateWire `json:"VerificationMessageTemplate" cbor:"VerificationMessageTemplate"`
	AdminCreateUserConfig       *adminCreateUserConfigWire       `json:"AdminCreateUserConfig" cbor:"AdminCreateUserConfig"`
	EmailConfiguration          *emailConfigurationWire          `json:"EmailConfiguration" cbor:"EmailConfiguration"`
	UserAttributeUpdateSettings *userAttributeUpdateSettingsWire `json:"UserAttributeUpdateSettings" cbor:"UserAttributeUpdateSettings"`
	AutoVerifiedAttributes      []string                         `json:"AutoVerifiedAttributes" cbor:"AutoVerifiedAttributes"`
	DeviceConfiguration         *DeviceConfiguration             `json:"DeviceConfiguration" cbor:"DeviceConfiguration"`
	UsernameAttributes          []string                         `json:"UsernameAttributes" cbor:"UsernameAttributes"`
	AliasAttributes             []string                         `json:"AliasAttributes" cbor:"AliasAttributes"`
	Policies                    *userPoolPoliciesWire            `json:"Policies" cbor:"Policies"`
	AccountRecoverySetting      *AccountRecoverySetting          `json:"AccountRecoverySetting" cbor:"AccountRecoverySetting"`
	SmsConfiguration            *SmsConfiguration                `json:"SmsConfiguration" cbor:"SmsConfiguration"`
	SmsAuthenticationMessage    string                           `json:"SmsAuthenticationMessage" cbor:"SmsAuthenticationMessage"`
	SmsVerificationMessage      string                           `json:"SmsVerificationMessage" cbor:"SmsVerificationMessage"`
	EmailVerificationMessage    string                           `json:"EmailVerificationMessage" cbor:"EmailVerificationMessage"`
	EmailVerificationSubject    string                           `json:"EmailVerificationSubject" cbor:"EmailVerificationSubject"`
	LambdaConfig                *LambdaConfig                    `json:"LambdaConfig" cbor:"LambdaConfig"`
}

type DescribeUserPoolDomainReq struct {
	Domain string `json:"Domain" cbor:"Domain"`
}

// ─── pool client request types ────────────────────────────────────────────────

type CreateUserPoolClientReq struct {
	UserPoolID                      string                  `json:"UserPoolId" cbor:"UserPoolId"`
	ClientName                      string                  `json:"ClientName" cbor:"ClientName"`
	GenerateSecret                  bool                    `json:"GenerateSecret" cbor:"GenerateSecret"`
	AccessTokenValidity             int                     `json:"AccessTokenValidity" cbor:"AccessTokenValidity"`
	IdTokenValidity                 int                     `json:"IdTokenValidity" cbor:"IdTokenValidity"`
	RefreshTokenValidity            int                     `json:"RefreshTokenValidity" cbor:"RefreshTokenValidity"`
	TokenValidityUnits              *TokenValidityUnitsType `json:"TokenValidityUnits" cbor:"TokenValidityUnits"`
	CallbackURLs                    []string                `json:"CallbackURLs" cbor:"CallbackURLs"`
	LogoutURLs                      []string                `json:"LogoutURLs" cbor:"LogoutURLs"`
	AllowedOAuthFlows               []string                `json:"AllowedOAuthFlows" cbor:"AllowedOAuthFlows"`
	AllowedOAuthScopes              []string                `json:"AllowedOAuthScopes" cbor:"AllowedOAuthScopes"`
	AllowedOAuthFlowsUserPoolClient bool                    `json:"AllowedOAuthFlowsUserPoolClient" cbor:"AllowedOAuthFlowsUserPoolClient"`
	ExplicitAuthFlows               []string                `json:"ExplicitAuthFlows" cbor:"ExplicitAuthFlows"`
	SupportedIdentityProviders      []string                `json:"SupportedIdentityProviders" cbor:"SupportedIdentityProviders"`
	PreventUserExistenceErrors      string                  `json:"PreventUserExistenceErrors" cbor:"PreventUserExistenceErrors"`
	ReadAttributes                  []string                `json:"ReadAttributes" cbor:"ReadAttributes"`
	WriteAttributes                 []string                `json:"WriteAttributes" cbor:"WriteAttributes"`
}

type UpdateUserPoolClientReq struct {
	UserPoolID                      string                  `json:"UserPoolId" cbor:"UserPoolId"`
	ClientID                        string                  `json:"ClientId" cbor:"ClientId"`
	AccessTokenValidity             int                     `json:"AccessTokenValidity" cbor:"AccessTokenValidity"`
	IdTokenValidity                 int                     `json:"IdTokenValidity" cbor:"IdTokenValidity"`
	RefreshTokenValidity            int                     `json:"RefreshTokenValidity" cbor:"RefreshTokenValidity"`
	TokenValidityUnits              *TokenValidityUnitsType `json:"TokenValidityUnits" cbor:"TokenValidityUnits"`
	CallbackURLs                    *[]string               `json:"CallbackURLs" cbor:"CallbackURLs"`
	LogoutURLs                      *[]string               `json:"LogoutURLs" cbor:"LogoutURLs"`
	AllowedOAuthFlows               *[]string               `json:"AllowedOAuthFlows" cbor:"AllowedOAuthFlows"`
	AllowedOAuthScopes              *[]string               `json:"AllowedOAuthScopes" cbor:"AllowedOAuthScopes"`
	AllowedOAuthFlowsUserPoolClient *bool                   `json:"AllowedOAuthFlowsUserPoolClient" cbor:"AllowedOAuthFlowsUserPoolClient"`
	ExplicitAuthFlows               *[]string               `json:"ExplicitAuthFlows" cbor:"ExplicitAuthFlows"`
	SupportedIdentityProviders      *[]string               `json:"SupportedIdentityProviders" cbor:"SupportedIdentityProviders"`
	PreventUserExistenceErrors      string                  `json:"PreventUserExistenceErrors" cbor:"PreventUserExistenceErrors"`
	ReadAttributes                  *[]string               `json:"ReadAttributes" cbor:"ReadAttributes"`
	WriteAttributes                 *[]string               `json:"WriteAttributes" cbor:"WriteAttributes"`
}

// ─── admin user request types ─────────────────────────────────────────────────

type AdminCreateUserReq struct {
	UserPoolID         string          `json:"UserPoolId" cbor:"UserPoolId"`
	Username           string          `json:"Username" cbor:"Username"`
	TemporaryPassword  string          `json:"TemporaryPassword" cbor:"TemporaryPassword"`
	UserAttributes     []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
	MessageAction      string          `json:"MessageAction" cbor:"MessageAction"`
	ForceAliasCreation bool            `json:"ForceAliasCreation" cbor:"ForceAliasCreation"`
}

type AdminSetUserPasswordReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Username   string `json:"Username" cbor:"Username"`
	Password   string `json:"Password" cbor:"Password"`
	Permanent  bool   `json:"Permanent" cbor:"Permanent"`
}

type AdminUpdateUserAttributesReq struct {
	UserPoolID     string          `json:"UserPoolId" cbor:"UserPoolId"`
	Username       string          `json:"Username" cbor:"Username"`
	UserAttributes []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
}

type AdminDeleteUserAttributesReq struct {
	UserPoolID         string   `json:"UserPoolId" cbor:"UserPoolId"`
	Username           string   `json:"Username" cbor:"Username"`
	UserAttributeNames []string `json:"UserAttributeNames" cbor:"UserAttributeNames"`
}

type AdminInitiateAuthReq struct {
	UserPoolID     string            `json:"UserPoolId" cbor:"UserPoolId"`
	ClientID       string            `json:"ClientId" cbor:"ClientId"`
	AuthFlow       string            `json:"AuthFlow" cbor:"AuthFlow"`
	AuthParameters map[string]string `json:"AuthParameters" cbor:"AuthParameters"`
	Session        string            `json:"Session" cbor:"Session"`
}

type AdminRespondToAuthChallengeReq struct {
	UserPoolID         string            `json:"UserPoolId" cbor:"UserPoolId"`
	ClientID           string            `json:"ClientId" cbor:"ClientId"`
	ChallengeName      string            `json:"ChallengeName" cbor:"ChallengeName"`
	Session            string            `json:"Session" cbor:"Session"`
	ChallengeResponses map[string]string `json:"ChallengeResponses" cbor:"ChallengeResponses"`
}

// ─── self-service request types ───────────────────────────────────────────────

type SignUpReq struct {
	ClientID       string          `json:"ClientId" cbor:"ClientId"`
	Username       string          `json:"Username" cbor:"Username"`
	Password       string          `json:"Password" cbor:"Password"`
	SecretHash     string          `json:"SecretHash" cbor:"SecretHash"`
	UserAttributes []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
}

type ConfirmSignUpReq struct {
	ClientID           string `json:"ClientId" cbor:"ClientId"`
	Username           string `json:"Username" cbor:"Username"`
	ConfirmationCode   string `json:"ConfirmationCode" cbor:"ConfirmationCode"`
	SecretHash         string `json:"SecretHash" cbor:"SecretHash"`
	ForceAliasCreation bool   `json:"ForceAliasCreation" cbor:"ForceAliasCreation"`
}

// ─── auth flow request types ─────────────────────────────────────────────────

type InitiateAuthReq struct {
	ClientID       string            `json:"ClientId" cbor:"ClientId"`
	AuthFlow       string            `json:"AuthFlow" cbor:"AuthFlow"`
	AuthParameters map[string]string `json:"AuthParameters" cbor:"AuthParameters"`
	Session        string            `json:"Session" cbor:"Session"`
}

type RespondToAuthChallengeReq struct {
	ClientID           string            `json:"ClientId" cbor:"ClientId"`
	ChallengeName      string            `json:"ChallengeName" cbor:"ChallengeName"`
	Session            string            `json:"Session" cbor:"Session"`
	ChallengeResponses map[string]string `json:"ChallengeResponses" cbor:"ChallengeResponses"`
}

// ─── password request types ───────────────────────────────────────────────────

type ConfirmForgotPasswordReq struct {
	ClientID         string `json:"ClientId" cbor:"ClientId"`
	Username         string `json:"Username" cbor:"Username"`
	ConfirmationCode string `json:"ConfirmationCode" cbor:"ConfirmationCode"`
	Password         string `json:"Password" cbor:"Password"`
	SecretHash       string `json:"SecretHash" cbor:"SecretHash"`
}

type ChangePasswordReq struct {
	AccessToken      string `json:"AccessToken" cbor:"AccessToken"`
	PreviousPassword string `json:"PreviousPassword" cbor:"PreviousPassword"`
	ProposedPassword string `json:"ProposedPassword" cbor:"ProposedPassword"`
}

// ─── MFA request types ────────────────────────────────────────────────────────

// AssociateSoftwareTokenReq carries either an AccessToken (a signed-in user
// enrolling a factor) or the Session from an MFA_SETUP challenge (a user
// enrolling one mid-sign-in).
type AssociateSoftwareTokenReq struct {
	AccessToken string `json:"AccessToken" cbor:"AccessToken"`
	Session     string `json:"Session" cbor:"Session"`
}

type VerifySoftwareTokenReq struct {
	AccessToken  string `json:"AccessToken" cbor:"AccessToken"`
	Session      string `json:"Session" cbor:"Session"`
	UserCode     string `json:"UserCode" cbor:"UserCode"`
	FriendlyName string `json:"FriendlyDeviceName" cbor:"FriendlyDeviceName"`
}

type MfaSettings struct {
	Enabled      bool `json:"Enabled" cbor:"Enabled"`
	PreferredMfa bool `json:"PreferredMfa" cbor:"PreferredMfa"`
}

type SetUserMFAPreferenceReq struct {
	AccessToken              string       `json:"AccessToken" cbor:"AccessToken"`
	SoftwareTokenMfaSettings *MfaSettings `json:"SoftwareTokenMfaSettings" cbor:"SoftwareTokenMfaSettings"`
	SMSMfaSettings           *MfaSettings `json:"SMSMfaSettings" cbor:"SMSMfaSettings"`
}

type AdminSetUserMFAPreferenceReq struct {
	UserPoolID               string       `json:"UserPoolId" cbor:"UserPoolId"`
	Username                 string       `json:"Username" cbor:"Username"`
	SoftwareTokenMfaSettings *MfaSettings `json:"SoftwareTokenMfaSettings" cbor:"SoftwareTokenMfaSettings"`
	SMSMfaSettings           *MfaSettings `json:"SMSMfaSettings" cbor:"SMSMfaSettings"`
}

// ─── group request types ──────────────────────────────────────────────────────

type CreateGroupReq struct {
	UserPoolID  string `json:"UserPoolId" cbor:"UserPoolId"`
	GroupName   string `json:"GroupName" cbor:"GroupName"`
	Description string `json:"Description" cbor:"Description"`
	Precedence  int    `json:"Precedence" cbor:"Precedence"`
	RoleARN     string `json:"RoleArn" cbor:"RoleArn"`
}

type UpdateGroupReq struct {
	UserPoolID  string `json:"UserPoolId" cbor:"UserPoolId"`
	GroupName   string `json:"GroupName" cbor:"GroupName"`
	Description string `json:"Description" cbor:"Description"`
	Precedence  int    `json:"Precedence" cbor:"Precedence"`
	RoleARN     string `json:"RoleArn" cbor:"RoleArn"`
}

type PoolAndGroupLimitReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	GroupName  string `json:"GroupName" cbor:"GroupName"`
	Limit      int    `json:"Limit" cbor:"Limit"`
	NextToken  string `json:"NextToken" cbor:"NextToken"`
}

type PoolAndUserGroupReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Username   string `json:"Username" cbor:"Username"`
	GroupName  string `json:"GroupName" cbor:"GroupName"`
}

type PoolAndUserLimitReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Username   string `json:"Username" cbor:"Username"`
	Limit      int    `json:"Limit" cbor:"Limit"`
	NextToken  string `json:"NextToken" cbor:"NextToken"`
}

type PoolLimitReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Limit      int    `json:"Limit" cbor:"Limit"`
	NextToken  string `json:"NextToken" cbor:"NextToken"`
}

// ─── token / user info request types ──────────────────────────────────────────

type UpdateUserAttributesReq struct {
	AccessToken    string          `json:"AccessToken" cbor:"AccessToken"`
	UserAttributes []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
}

type VerifyUserAttributeReq struct {
	AccessToken   string `json:"AccessToken" cbor:"AccessToken"`
	AttributeName string `json:"AttributeName" cbor:"AttributeName"`
	Code          string `json:"Code" cbor:"Code"`
}

type GetUserAttributeVerificationCodeReq struct {
	AccessToken   string `json:"AccessToken" cbor:"AccessToken"`
	AttributeName string `json:"AttributeName" cbor:"AttributeName"`
}

type DeleteUserAttributesReq struct {
	AccessToken        string   `json:"AccessToken" cbor:"AccessToken"`
	UserAttributeNames []string `json:"UserAttributeNames" cbor:"UserAttributeNames"`
}

type DeviceSecretVerifierConfigReq struct {
	PasswordVerifier string `json:"PasswordVerifier" cbor:"PasswordVerifier"`
	Salt             string `json:"Salt" cbor:"Salt"`
}

type ConfirmDeviceReq struct {
	AccessToken                string                         `json:"AccessToken" cbor:"AccessToken"`
	DeviceKey                  string                         `json:"DeviceKey" cbor:"DeviceKey"`
	DeviceName                 string                         `json:"DeviceName" cbor:"DeviceName"`
	DeviceSecretVerifierConfig *DeviceSecretVerifierConfigReq `json:"DeviceSecretVerifierConfig" cbor:"DeviceSecretVerifierConfig"`
}

type DeviceKeyAccessReq struct {
	AccessToken string `json:"AccessToken" cbor:"AccessToken"`
	DeviceKey   string `json:"DeviceKey" cbor:"DeviceKey"`
}

type UpdateDeviceStatusReq struct {
	AccessToken            string `json:"AccessToken" cbor:"AccessToken"`
	DeviceKey              string `json:"DeviceKey" cbor:"DeviceKey"`
	DeviceRememberedStatus string `json:"DeviceRememberedStatus" cbor:"DeviceRememberedStatus"`
}

type AdminDeviceReq struct {
	UserPoolID string `json:"UserPoolId" cbor:"UserPoolId"`
	Username   string `json:"Username" cbor:"Username"`
	DeviceKey  string `json:"DeviceKey" cbor:"DeviceKey"`
}

type AdminUpdateDeviceStatusReq struct {
	UserPoolID             string `json:"UserPoolId" cbor:"UserPoolId"`
	Username               string `json:"Username" cbor:"Username"`
	DeviceKey              string `json:"DeviceKey" cbor:"DeviceKey"`
	DeviceRememberedStatus string `json:"DeviceRememberedStatus" cbor:"DeviceRememberedStatus"`
}

type AdminListDevicesReq struct {
	UserPoolID      string `json:"UserPoolId" cbor:"UserPoolId"`
	Username        string `json:"Username" cbor:"Username"`
	Limit           int    `json:"Limit" cbor:"Limit"`
	PaginationToken string `json:"PaginationToken" cbor:"PaginationToken"`
}

type ListDevicesReq struct {
	AccessToken     string `json:"AccessToken" cbor:"AccessToken"`
	Limit           int    `json:"Limit" cbor:"Limit"`
	PaginationToken string `json:"PaginationToken" cbor:"PaginationToken"`
}

type RevokeTokenReq struct {
	ClientID string `json:"ClientId" cbor:"ClientId"`
	Token    string `json:"Token" cbor:"Token"`
}

// ─── response types ───────────────────────────────────────────────────────────

type CreateUserPoolResp struct {
	UserPool userPoolWire `json:"UserPool" cbor:"UserPool"`
}

type DescribeUserPoolResp struct {
	UserPool userPoolWire `json:"UserPool" cbor:"UserPool"`
}

type UserPoolMfaConfigResp struct {
	MfaConfiguration              string                     `json:"MfaConfiguration,omitempty" cbor:"MfaConfiguration,omitempty"`
	SmsMfaConfiguration           *SmsMfaConfig              `json:"SmsMfaConfiguration,omitempty" cbor:"SmsMfaConfiguration,omitempty"`
	SoftwareTokenMfaConfiguration *SoftwareTokenMfaConfig    `json:"SoftwareTokenMfaConfiguration,omitempty" cbor:"SoftwareTokenMfaConfiguration,omitempty"`
	EmailMfaConfiguration         *EmailMfaConfig            `json:"EmailMfaConfiguration,omitempty" cbor:"EmailMfaConfiguration,omitempty"`
	WebAuthnConfiguration         *webAuthnConfigurationWire `json:"WebAuthnConfiguration,omitempty" cbor:"WebAuthnConfiguration,omitempty"`
}

type StartWebAuthnRegistrationResp struct {
	CredentialCreationOptions map[string]any `json:"CredentialCreationOptions" cbor:"CredentialCreationOptions"`
}

type ListUserPoolsResp struct {
	UserPools []poolDesc `json:"UserPools" cbor:"UserPools"`
}

type poolDesc struct {
	ID               string  `json:"Id" cbor:"Id"`
	Name             string  `json:"Name" cbor:"Name"`
	CreationDate     float64 `json:"CreationDate" cbor:"CreationDate"`
	LastModifiedDate float64 `json:"LastModifiedDate" cbor:"LastModifiedDate"`
}

type DescribeUserPoolDomainResp struct {
	DomainDescription domainDescriptionWire `json:"DomainDescription" cbor:"DomainDescription"`
}

type domainDescriptionWire struct {
	Domain     string `json:"Domain" cbor:"Domain"`
	UserPoolId string `json:"UserPoolId" cbor:"UserPoolId"`
	Status     string `json:"Status" cbor:"Status"`
}

type CreateUserPoolClientResp struct {
	UserPoolClient clientWire `json:"UserPoolClient" cbor:"UserPoolClient"`
}

type DescribeUserPoolClientResp struct {
	UserPoolClient clientWire `json:"UserPoolClient" cbor:"UserPoolClient"`
}

type UpdateUserPoolClientResp struct {
	UserPoolClient clientWire `json:"UserPoolClient" cbor:"UserPoolClient"`
}

type ListUserPoolClientsResp struct {
	UserPoolClients []clientDesc `json:"UserPoolClients" cbor:"UserPoolClients"`
}

type clientDesc struct {
	ClientID   string `json:"ClientId" cbor:"ClientId"`
	ClientName string `json:"ClientName" cbor:"ClientName"`
	UserPoolId string `json:"UserPoolId" cbor:"UserPoolId"`
}

type AdminCreateUserResp struct {
	User userWire `json:"User" cbor:"User"`
}

type AdminGetUserResp struct {
	Username             string          `json:"Username" cbor:"Username"`
	UserAttributes       []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
	UserCreateDate       float64         `json:"UserCreateDate" cbor:"UserCreateDate"`
	UserLastModifiedDate float64         `json:"UserLastModifiedDate" cbor:"UserLastModifiedDate"`
	Enabled              bool            `json:"Enabled" cbor:"Enabled"`
	UserStatus           string          `json:"UserStatus" cbor:"UserStatus"`
	PreferredMfaSetting  string          `json:"PreferredMfaSetting,omitempty" cbor:"PreferredMfaSetting,omitempty"`
	UserMFASettingList   []string        `json:"UserMFASettingList,omitempty" cbor:"UserMFASettingList,omitempty"`
}

type ListUsersResp struct {
	Users           []userWire `json:"Users" cbor:"Users"`
	PaginationToken string     `json:"PaginationToken,omitempty" cbor:"PaginationToken,omitempty"`
}

type SignUpResp struct {
	UserConfirmed       bool                 `json:"UserConfirmed" cbor:"UserConfirmed"`
	CodeDeliveryDetails *codeDeliveryDetails `json:"CodeDeliveryDetails,omitempty" cbor:"CodeDeliveryDetails,omitempty"`
	UserSub             string               `json:"UserSub" cbor:"UserSub"`
}

type ConfirmSignUpResp struct {
	Session string `json:"Session,omitempty" cbor:"Session,omitempty"`
}

type AuthChallengeResponse struct {
	ChallengeName       string            `json:"ChallengeName" cbor:"ChallengeName"`
	Session             string            `json:"Session" cbor:"Session"`
	ChallengeParameters map[string]string `json:"ChallengeParameters" cbor:"ChallengeParameters"`
}

type InitiateAuthResp struct {
	AuthenticationResult *authResultWire   `json:"AuthenticationResult,omitempty" cbor:"AuthenticationResult,omitempty"`
	AvailableChallenges  []string          `json:"AvailableChallenges,omitempty" cbor:"AvailableChallenges,omitempty"`
	ChallengeName        string            `json:"ChallengeName,omitempty" cbor:"ChallengeName,omitempty"`
	Session              string            `json:"Session,omitempty" cbor:"Session,omitempty"`
	ChallengeParameters  map[string]string `json:"ChallengeParameters,omitempty" cbor:"ChallengeParameters,omitempty"`
}

type RespondToAuthChallengeResp struct {
	AuthenticationResult *authResultWire   `json:"AuthenticationResult,omitempty" cbor:"AuthenticationResult,omitempty"`
	ChallengeName        string            `json:"ChallengeName,omitempty" cbor:"ChallengeName,omitempty"`
	ChallengeParameters  map[string]string `json:"ChallengeParameters,omitempty" cbor:"ChallengeParameters,omitempty"`
	Session              string            `json:"Session,omitempty" cbor:"Session,omitempty"`
}

type ForgotPasswordResp struct {
	CodeDeliveryDetails codeDeliveryDetails `json:"CodeDeliveryDetails" cbor:"CodeDeliveryDetails"`
}

type UpdateUserAttributesResp struct {
	CodeDeliveryDetailsList []codeDeliveryDetails `json:"CodeDeliveryDetailsList,omitempty" cbor:"CodeDeliveryDetailsList,omitempty"`
}

type GetUserAttributeVerificationCodeResp struct {
	CodeDeliveryDetails codeDeliveryDetails `json:"CodeDeliveryDetails" cbor:"CodeDeliveryDetails"`
}

type codeDeliveryDetails struct {
	DeliveryMedium string `json:"DeliveryMedium" cbor:"DeliveryMedium"`
	Destination    string `json:"Destination" cbor:"Destination"`
	AttributeName  string `json:"AttributeName" cbor:"AttributeName"`
}

type AssociateSoftwareTokenResp struct {
	SecretCode string `json:"SecretCode" cbor:"SecretCode"`
	Session    string `json:"Session,omitempty" cbor:"Session,omitempty"`
}

type VerifySoftwareTokenResp struct {
	Status  string `json:"Status" cbor:"Status"`
	Session string `json:"Session,omitempty" cbor:"Session,omitempty"`
}

type CreateGroupResp struct {
	Group groupWire `json:"Group" cbor:"Group"`
}

type GetGroupResp struct {
	Group groupWire `json:"Group" cbor:"Group"`
}

type ListGroupsResp struct {
	Groups    []groupWire `json:"Groups" cbor:"Groups"`
	NextToken string      `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type ListUsersInGroupResp struct {
	Users     []userWire `json:"Users" cbor:"Users"`
	NextToken string     `json:"NextToken,omitempty" cbor:"NextToken,omitempty"`
}

type GetUserResp struct {
	Username            string          `json:"Username" cbor:"Username"`
	UserAttributes      []UserAttribute `json:"UserAttributes" cbor:"UserAttributes"`
	PreferredMfaSetting string          `json:"PreferredMfaSetting,omitempty" cbor:"PreferredMfaSetting,omitempty"`
	UserMFASettingList  []string        `json:"UserMFASettingList,omitempty" cbor:"UserMFASettingList,omitempty"`
}

// ─── typed helpers ────────────────────────────────────────────────────────────

func (s *Service) requirePoolTyped(ctx context.Context, poolID string) (*UserPool, *protocol.AWSError) {
	pool, err := s.loadPool(ctx, poolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if pool == nil {
		return nil, errPoolNotFound(poolID)
	}
	return pool, nil
}

func (s *Service) requireClientTyped(ctx context.Context, poolID, clientID string) (*UserPoolClient, *protocol.AWSError) {
	c, err := s.loadClient(ctx, poolID, clientID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if c == nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Client " + clientID + " does not exist.",
			HTTPStatus: 400,
		}
	}
	return c, nil
}

func (s *Service) requireClientByIDTyped(ctx context.Context, clientID string) (*UserPoolClient, *protocol.AWSError) {
	c, err := s.loadClientByID(ctx, clientID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if c == nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("UserPool client %s does not exist.", clientID),
			HTTPStatus: 400,
		}
	}
	return c, nil
}

func (s *Service) requireGroupTyped(ctx context.Context, poolID, groupName string) (*Group, *protocol.AWSError) {
	g, err := s.loadGroup(ctx, poolID, groupName)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if g == nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    fmt.Sprintf("Group %s does not exist.", groupName),
			HTTPStatus: 400,
		}
	}
	return g, nil
}

func (s *Service) requireUserTyped(ctx context.Context, poolID, username string) (*User, *protocol.AWSError) {
	u, err := s.resolveUserInPool(ctx, poolID, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	return u, nil
}

func (s *Service) publishTyped(ctx context.Context, t events.Type, payload any) {
	if s.bus != nil {
		s.bus.Publish(ctx, events.Event{
			Type:    t,
			Source:  serviceName,
			Payload: payload,
		})
	}
}

func (s *Service) checkSecretHashTyped(c *UserPoolClient, username, secretHash string) *protocol.AWSError {
	if c.ClientSecret == "" {
		if secretHash != "" {
			return &protocol.AWSError{
				Code:       "InvalidParameterException",
				Message:    "Client " + c.ClientID + " does not have a client secret.",
				HTTPStatus: 400,
			}
		}
		return nil
	}
	if secretHash == "" {
		return &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "SECRET_HASH is required for client " + c.ClientID + ".",
			HTTPStatus: 400,
		}
	}
	mac := hmac.New(sha256.New, []byte(c.ClientSecret))
	mac.Write([]byte(username + c.ClientID))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(secretHash), []byte(expected)) {
		return errNotAuthorized("Unable to verify secret hash for client " + c.ClientID + ".")
	}
	return nil
}

func (s *Service) validateAccessTokenTyped(ctx context.Context, tokenStr string) (*Token, *protocol.AWSError) {
	claims, err := parseJWTClaims(tokenStr)
	if err != nil {
		return nil, errNotAuthorized("Invalid access token.")
	}
	iss, _ := claims["iss"].(string)
	poolID, err := poolIDFromIssuer(iss)
	if err != nil {
		return nil, errNotAuthorized("Invalid access token issuer.")
	}
	// Read-only: poolID comes from an unverified claim, so a pool with no key
	// on record must reject rather than mint one (#1731).
	priv, _, err := s.getSigningKey(ctx, poolID)
	if errors.Is(err, errNoSigningKey) {
		return nil, errNotAuthorized("Invalid access token issuer.")
	}
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := verifyJWTSignature(tokenStr, &priv.PublicKey); err != nil {
		return nil, errNotAuthorized("Invalid access token signature.")
	}
	if tu, _ := claims["token_use"].(string); tu != "access" {
		return nil, errNotAuthorized("Token is not an access token.")
	}
	exp, _ := claims["exp"].(float64)
	if s.clk.Now().Unix() > int64(exp) {
		return nil, errNotAuthorized("Access token has expired.")
	}
	jti, _ := claims["jti"].(string)
	storedTok, err := s.loadToken(ctx, jti)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if storedTok == nil {
		return nil, errNotAuthorized("Access token has been revoked.")
	}
	username, _ := claims["username"].(string)
	u, err := s.loadUser(ctx, poolID, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errNotAuthorized("User does not exist.")
	}
	if u.GlobalSignOutAt != nil {
		iat, _ := claims["iat"].(float64)
		if int64(iat) < u.GlobalSignOutAt.Unix() {
			return nil, errNotAuthorized("Token revoked by global sign-out.")
		}
	}
	return &Token{
		Value:      jti,
		Type:       "access",
		Username:   username,
		UserPoolID: poolID,
		ExpiresAt:  time.Unix(int64(exp), 0),
	}, nil
}

// issuerURLTyped is issuerURL for the typed (Smithy RPC v2 CBOR) dispatch path,
// which is handed a context rather than an *http.Request. It must produce the
// same string issuerURL does: a caller's wire protocol is not allowed to change
// the issuer their token carries, and it used to — this returned a bare
// "{region}/{poolId}" with no scheme or host, so a CBOR client's "iss" was a
// relative string that no OIDC key discovery and no API Gateway JWT authorizer
// could use. See docs/plans/harness-representativeness-audit.md.
func (s *Service) issuerURLTyped(ctx context.Context, poolID string) string {
	return issuerFor(s.issuerBase(ctx), poolID)
}

// issuerBase is serviceutil.ClientBaseURL for the typed path, which holds a
// context rather than a request. The origin comes from the ClientEndpoint
// middleware, which stamps it per request and deliberately leaves it unset for
// real AWS hostnames so those are never echoed back.
//
// This used to be a hand-kept mirror of ClientBaseURL's precedence, and it
// drifted: it took cfg.Port where the JSON path took the caller's, so a
// caller's wire protocol changed the issuer their token carried — the exact
// defect the typed/JSON unification fixed. Both paths now call the same
// function, so they cannot disagree again. See
// docs/plans/client-facing-url-minting.md.
func (s *Service) issuerBase(ctx context.Context) string {
	return serviceutil.ClientBaseURLFromOrigin(s.cfg, middleware.ClientEndpointFromContext(ctx))
}

// ─── typed auth challenge handlers ────────────────────────────────────────────

func (s *Service) handlePasswordAuthTyped(ctx context.Context, client *UserPoolClient, params map[string]string) (*InitiateAuthResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	poolID := client.UserPoolID
	username := params["USERNAME"]
	password := params["PASSWORD"]
	if username == "" || password == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "USERNAME and PASSWORD are required in AuthParameters.",
			HTTPStatus: 400,
		}
	}
	pool, err := s.loadPool(ctx, poolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	if !u.Enabled {
		s.publishTyped(ctx, events.CognitoSignInFailed, events.ResourcePayload{Name: username})
		return nil, errNotAuthorized("User is disabled.")
	}
	if u.Status == StatusUnconfirmed {
		return nil, &protocol.AWSError{
			Code:       "UserNotConfirmedException",
			Message:    "User is not confirmed.",
			HTTPStatus: 400,
		}
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.publishTyped(ctx, events.CognitoSignInFailed, events.ResourcePayload{Name: username})
		return nil, errNotAuthorized("Incorrect username or password.")
	}
	if u.Status == StatusForceChangePassword {
		session, err := s.issueSession(ctx, poolID, username)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &InitiateAuthResp{
			ChallengeName: "NEW_PASSWORD_REQUIRED",
			Session:       session,
			ChallengeParameters: map[string]string{
				"USER_ID_FOR_SRP": username,
				"userAttributes":  "{}",
			},
		}, nil
	}
	if resp, aerr := s.maybeStartDeviceAuthChallenge(ctx, pool, u, params); aerr != nil || resp != nil {
		return resp, aerr
	}
	if mfaResp, aerr := s.startMfaChallenge(ctx, pool, u); aerr != nil || mfaResp != nil {
		return mfaResp, aerr
	}
	issuer := s.issuerURLTyped(ctx, poolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	s.attachNewDeviceMetadata(ctx, pool, result, params)
	log.Info("user authenticated",
		zap.String("poolId", poolID), zap.String("username", username))
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: username})
	return &InitiateAuthResp{AuthenticationResult: result}, nil
}

func (s *Service) handleRefreshTokenAuthTyped(ctx context.Context, c *UserPoolClient, params map[string]string) (*InitiateAuthResp, *protocol.AWSError) {
	poolID := c.UserPoolID
	refreshValue := params["REFRESH_TOKEN"]
	if refreshValue == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "REFRESH_TOKEN is required in AuthParameters.",
			HTTPStatus: 400,
		}
	}
	t, err := s.loadToken(ctx, refreshValue)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if t == nil || t.Type != "refresh" || t.UserPoolID != poolID {
		return nil, errNotAuthorized("Invalid refresh token.")
	}
	if s.clk.Now().After(t.ExpiresAt) {
		return nil, errNotAuthorized("Refresh token has expired.")
	}
	u, err := s.loadUser(ctx, poolID, t.Username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errNotAuthorized("User does not exist.")
	}
	if u.GlobalSignOutAt != nil && t.CreatedAt.Before(*u.GlobalSignOutAt) {
		return nil, errNotAuthorized("Refresh token revoked by global sign-out.")
	}
	if aerr := s.checkSecretHashTyped(c, u.Username, params["SECRET_HASH"]); aerr != nil {
		return nil, aerr
	}
	issuer := s.issuerURLTyped(ctx, poolID)
	result, aerr := s.issueTokens(ctx, u, c, issuer, t.OriginJTI, "", triggerSourceTokenGenRefreshTokens)
	if aerr != nil {
		return nil, aerr
	}
	result.RefreshToken = ""
	return &InitiateAuthResp{AuthenticationResult: result}, nil
}

func (s *Service) handleUserAuthWithConfirmSessionTyped(ctx context.Context, client *UserPoolClient, params map[string]string, session string) (*InitiateAuthResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	username := params["USERNAME"]
	if username == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "USERNAME is required in AuthParameters.",
			HTTPStatus: 400,
		}
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	if session == "" {
		if !u.Enabled {
			s.publishTyped(ctx, events.CognitoSignInFailed, events.ResourcePayload{Name: username})
			return nil, errNotAuthorized("User is disabled.")
		}
		if u.Status == StatusUnconfirmed {
			return nil, &protocol.AWSError{
				Code:       "UserNotConfirmedException",
				Message:    "User is not confirmed.",
				HTTPStatus: 400,
			}
		}
		availableChallenges := availableUserAuthChallenges(pool, u)
		if len(availableChallenges) == 0 {
			return nil, errNoSupportedFirstAuthFactors()
		}
		session, err := s.issueOpaqueToken(ctx, client.UserPoolID, u.Username, "userauth", 5*time.Minute)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		challengeName := "SELECT_CHALLENGE"
		if preferred := params["PREFERRED_CHALLENGE"]; preferred != "" && containsChallenge(availableChallenges, preferred) {
			challengeName = preferred
		}
		challengeParameters := map[string]string{"USERNAME": u.Username}
		if challengeName == "EMAIL_OTP" || challengeName == "SMS_OTP" {
			var aerr *protocol.AWSError
			challengeParameters, aerr = s.issueUserAuthOTP(pool, u, challengeName)
			if aerr != nil {
				return nil, aerr
			}
			if err := s.saveUser(ctx, u); err != nil {
				return nil, protocol.Wrap(protocol.ErrInternalError, err)
			}
		} else if challengeName == "WEB_AUTHN" {
			var aerr *protocol.AWSError
			challengeParameters, aerr = s.startWebAuthnChallengeParameters(pool, u)
			if aerr != nil {
				return nil, aerr
			}
		}
		return &InitiateAuthResp{
			AvailableChallenges: availableChallenges,
			ChallengeName:       challengeName,
			ChallengeParameters: challengeParameters,
			Session:             session,
		}, nil
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "confirm" || st.UserPoolID != client.UserPoolID || st.Username != u.Username {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	if !u.Enabled {
		s.publishTyped(ctx, events.CognitoSignInFailed, events.ResourcePayload{Name: username})
		return nil, errNotAuthorized("User is disabled.")
	}
	if u.Status != StatusConfirmed {
		return nil, &protocol.AWSError{
			Code:       "UserNotConfirmedException",
			Message:    "User is not confirmed.",
			HTTPStatus: 400,
		}
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	log.Info("user authenticated from confirm signup session",
		zap.String("poolId", client.UserPoolID), zap.String("username", username))
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: username})
	return &InitiateAuthResp{AuthenticationResult: result}, nil
}

func (s *Service) handleChoiceAuthChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if session == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Session is required.", HTTPStatus: 400}
	}
	switch responses["ANSWER"] {
	case "PASSWORD":
		return s.completeChoicePasswordChallengeTyped(ctx, client, session, responses)
	case "PASSWORD_SRP":
		return s.startChoiceSRPChallengeTyped(ctx, client, session, responses)
	case "EMAIL_OTP", "SMS_OTP":
		return s.startChoiceOTPChallengeTyped(ctx, client, session, responses)
	case "WEB_AUTHN":
		if responses["CREDENTIAL"] != "" {
			return s.completeWebAuthnChallengeTyped(ctx, client, session, responses)
		}
		return s.startChoiceWebAuthnChallengeTyped(ctx, client, session, responses)
	default:
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Unsupported challenge answer: " + responses["ANSWER"], HTTPStatus: 400}
	}
}

func (s *Service) handleCustomAuthStartTyped(ctx context.Context, client *UserPoolClient, params map[string]string) (*InitiateAuthResp, *protocol.AWSError) {
	username := params["USERNAME"]
	if username == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "USERNAME is required in AuthParameters.", HTTPStatus: 400}
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	if !u.Enabled {
		return nil, errNotAuthorized("User is disabled.")
	}
	if u.Status == StatusUnconfirmed {
		return nil, &protocol.AWSError{Code: "UserNotConfirmedException", Message: "User is not confirmed.", HTTPStatus: 400}
	}
	switch params["CHALLENGE_NAME"] {
	case "", "CUSTOM_CHALLENGE":
		session, err := s.issueOpaqueToken(ctx, client.UserPoolID, u.Username, "customauth", 5*time.Minute)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &InitiateAuthResp{ChallengeName: "CUSTOM_CHALLENGE", ChallengeParameters: map[string]string{"USERNAME": u.Username}, Session: session}, nil
	case "SRP_A":
		if params["SRP_A"] == "" {
			return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "SRP_A is required in AuthParameters.", HTTPStatus: 400}
		}
		session, err := s.issueOpaqueToken(ctx, client.UserPoolID, u.Username, "custom_srp", 5*time.Minute)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &InitiateAuthResp{ChallengeName: "PASSWORD_VERIFIER", ChallengeParameters: srpChallengeParameters(u.Username), Session: session}, nil
	default:
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Unsupported custom challenge name: " + params["CHALLENGE_NAME"], HTTPStatus: 400}
	}
}

func (s *Service) handleSRPAuthStartTyped(ctx context.Context, client *UserPoolClient, params map[string]string) (*InitiateAuthResp, *protocol.AWSError) {
	username := params["USERNAME"]
	if username == "" || params["SRP_A"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "USERNAME and SRP_A are required in AuthParameters.", HTTPStatus: 400}
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil {
		return nil, errUserNotFound(username)
	}
	if !u.Enabled {
		return nil, errNotAuthorized("User is disabled.")
	}
	session, err := s.issueOpaqueToken(ctx, client.UserPoolID, u.Username, "srp", 5*time.Minute)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &InitiateAuthResp{ChallengeName: "PASSWORD_VERIFIER", ChallengeParameters: srpChallengeParameters(u.Username), Session: session}, nil
}

func (s *Service) startChoiceSRPChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if responses["SRP_A"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "SRP_A is required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "userauth" || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	_ = s.removeToken(ctx, session)
	srpSession, err := s.issueOpaqueToken(ctx, client.UserPoolID, st.Username, "srp", 5*time.Minute)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &RespondToAuthChallengeResp{ChallengeName: "PASSWORD_VERIFIER", ChallengeParameters: srpChallengeParameters(st.Username), Session: srpSession}, nil
}

func (s *Service) startChoiceOTPChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "userauth" || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, responses["USERNAME"])
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil || u.Username != st.Username || !containsChallenge(availableUserAuthChallenges(pool, u), responses["ANSWER"]) {
		return nil, errNotAuthorized("Invalid session.")
	}
	challengeParameters, aerr := s.issueUserAuthOTP(pool, u, responses["ANSWER"])
	if aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &RespondToAuthChallengeResp{ChallengeName: responses["ANSWER"], ChallengeParameters: challengeParameters, Session: session}, nil
}

func (s *Service) startChoiceWebAuthnChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	st, u, pool, aerr := s.requireUserAuthChallengeSession(ctx, client, session, responses["USERNAME"])
	if aerr != nil {
		return nil, aerr
	}
	if st.Username != u.Username || !containsChallenge(availableUserAuthChallenges(pool, u), "WEB_AUTHN") {
		return nil, errNotAuthorized("Invalid session.")
	}
	challengeParameters, aerr := s.startWebAuthnChallengeParameters(pool, u)
	if aerr != nil {
		return nil, aerr
	}
	return &RespondToAuthChallengeResp{ChallengeName: "WEB_AUTHN", ChallengeParameters: challengeParameters, Session: session}, nil
}

func (s *Service) handlePasswordChoiceChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if session == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Session is required.", HTTPStatus: 400}
	}
	return s.completeChoicePasswordChallengeTyped(ctx, client, session, responses)
}

func (s *Service) completeChoicePasswordChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	password := responses["PASSWORD"]
	if password == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "PASSWORD is required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "userauth" || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, responses["USERNAME"])
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil || u.Username != st.Username {
		return nil, errNotAuthorized("Invalid session.")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		s.publishTyped(ctx, events.CognitoSignInFailed, events.ResourcePayload{Name: responses["USERNAME"]})
		return nil, errNotAuthorized("Incorrect username or password.")
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result}, nil
}

func (s *Service) completeOTPChallengeTyped(ctx context.Context, client *UserPoolClient, challengeName, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	codeKey := challengeName + "_CODE"
	code := responses[codeKey]
	if code == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: codeKey + " is required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "userauth" || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, responses["USERNAME"])
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil || u.Username != st.Username {
		return nil, errNotAuthorized("Invalid session.")
	}
	stored, ok := authChallengeCode(u, challengeName)
	if !ok || stored.Code != code {
		return nil, errCodeMismatch()
	}
	if !stored.ExpiresAt.IsZero() && s.clk.Now().After(stored.ExpiresAt) {
		removeAuthChallengeCode(u, challengeName)
		_ = s.saveUser(ctx, u)
		return nil, errExpiredCode()
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	removeAuthChallengeCode(u, challengeName)
	_ = s.removeToken(ctx, session)
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result, ChallengeParameters: map[string]string{}}, nil
}

func (s *Service) completeSRPVerifierChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if responses["PASSWORD_CLAIM_SIGNATURE"] == "" || responses["PASSWORD_CLAIM_SECRET_BLOCK"] == "" || responses["TIMESTAMP"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "PASSWORD_CLAIM_SIGNATURE, PASSWORD_CLAIM_SECRET_BLOCK, and TIMESTAMP are required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || (st.Type != "srp" && st.Type != "custom_srp") || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	u, err := s.loadUser(ctx, client.UserPoolID, st.Username)
	if err != nil || u == nil {
		return nil, errNotAuthorized("Invalid session.")
	}
	if st.Type == "custom_srp" {
		_ = s.removeToken(ctx, session)
		customSession, err := s.issueOpaqueToken(ctx, client.UserPoolID, u.Username, "customauth", 5*time.Minute)
		if err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
		return &RespondToAuthChallengeResp{ChallengeName: "CUSTOM_CHALLENGE", ChallengeParameters: map[string]string{"USERNAME": u.Username}, Session: customSession}, nil
	}
	pool, aerr := s.requirePoolTyped(ctx, client.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	if mfaResp, aerr := s.startMfaChallenge(ctx, pool, u); aerr != nil || mfaResp != nil {
		_ = s.removeToken(ctx, session)
		return asRespondResp(mfaResp), aerr
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result, ChallengeParameters: map[string]string{}}, nil
}

func (s *Service) completeCustomAuthChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if responses["ANSWER"] == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "ANSWER is required in ChallengeResponses.", HTTPStatus: 400}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "customauth" || st.UserPoolID != client.UserPoolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, responses["USERNAME"])
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil || u.Username != st.Username {
		return nil, errNotAuthorized("Invalid session.")
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result, ChallengeParameters: map[string]string{}}, nil
}

func (s *Service) completeWebAuthnChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	credentialID := webAuthnCredentialID(responses["CREDENTIAL"])
	if credentialID == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "CREDENTIAL is required in ChallengeResponses.", HTTPStatus: 400}
	}
	_, u, _, aerr := s.requireUserAuthChallengeSession(ctx, client, session, responses["USERNAME"])
	if aerr != nil {
		return nil, aerr
	}
	if !hasWebAuthnCredential(u, credentialID) {
		return nil, errNotAuthorized("Invalid WebAuthn credential.")
	}
	issuer := s.issuerURLTyped(ctx, client.UserPoolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, session)
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: responses["USERNAME"]})
	return &RespondToAuthChallengeResp{AuthenticationResult: result, ChallengeParameters: map[string]string{}}, nil
}

func (s *Service) requireUserAuthChallengeSession(ctx context.Context, client *UserPoolClient, session, username string) (*Token, *User, *UserPool, *protocol.AWSError) {
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "userauth" || st.UserPoolID != client.UserPoolID {
		return nil, nil, nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, nil, nil, errNotAuthorized("Session has expired.")
	}
	pool, err := s.loadPool(ctx, client.UserPoolID)
	if err != nil || pool == nil {
		return nil, nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u, err := s.resolveUser(ctx, pool, username)
	if err != nil {
		return nil, nil, nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if u == nil || u.Username != st.Username {
		return nil, nil, nil, errNotAuthorized("Invalid session.")
	}
	return st, u, pool, nil
}

func (s *Service) startWebAuthnChallengeParameters(pool *UserPool, u *User) (map[string]string, *protocol.AWSError) {
	challenge := generateToken()
	options, err := webAuthnCredentialRequestOptions(pool, u, challenge)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return map[string]string{"CREDENTIAL_REQUEST_OPTIONS": options}, nil
}

func (s *Service) handleNewPasswordChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	poolID := client.UserPoolID
	if session == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Session is required.",
			HTTPStatus: 400,
		}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "session" || st.UserPoolID != poolID {
		return nil, errNotAuthorized("Invalid session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("Session has expired.")
	}
	newPw := responses["NEW_PASSWORD"]
	if newPw == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "NEW_PASSWORD is required in ChallengeResponses.",
			HTTPStatus: 400,
		}
	}
	u, aerr := s.requireUserTyped(ctx, poolID, st.Username)
	if aerr != nil {
		return nil, aerr
	}
	pool, err := s.loadPool(ctx, poolID)
	if err != nil || pool == nil {
		return nil, errNotAuthorized("User pool not found.")
	}
	if aerr := validatePassword(pool, newPw); aerr != nil {
		return nil, aerr
	}
	hash, err := hashPassword(newPw)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u.PasswordHash = string(hash)
	u.PlaintextPassword = newPw
	u.TempPassword = ""
	u.Status = StatusConfirmed
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	_ = s.removeToken(ctx, session)
	issuer := s.issuerURLTyped(ctx, poolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenNewPassword)
	if aerr != nil {
		return nil, aerr
	}
	log.Info("user completed new-password challenge",
		zap.String("poolId", poolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoUserConfirmed, events.ResourcePayload{Name: u.Username})
	s.publishTyped(ctx, events.CognitoPasswordChanged, events.ResourcePayload{Name: u.Username})
	return &RespondToAuthChallengeResp{AuthenticationResult: result}, nil
}

func (s *Service) handleMFAChallengeTyped(ctx context.Context, client *UserPoolClient, session string, responses map[string]string) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	poolID := client.UserPoolID
	if session == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Session is required.",
			HTTPStatus: 400,
		}
	}
	st, err := s.loadToken(ctx, session)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if st == nil || st.Type != "mfa" || st.UserPoolID != poolID {
		return nil, errNotAuthorized("Invalid MFA session.")
	}
	if s.clk.Now().After(st.ExpiresAt) {
		return nil, errNotAuthorized("MFA session has expired.")
	}
	code := responses["SOFTWARE_TOKEN_MFA_CODE"]
	if code == "" {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "SOFTWARE_TOKEN_MFA_CODE is required in ChallengeResponses.",
			HTTPStatus: 400,
		}
	}
	u, aerr := s.requireUserTyped(ctx, poolID, st.Username)
	if aerr != nil {
		return nil, aerr
	}
	if !verifyTOTP(u.TOTPSecret, code, s.clk.Now()) {
		return nil, errCodeMismatch()
	}
	_ = s.removeToken(ctx, session)
	issuer := s.issuerURLTyped(ctx, poolID)
	result, aerr := s.issueTokens(ctx, u, client, issuer, "", "", triggerSourceTokenGenAuthentication)
	if aerr != nil {
		return nil, aerr
	}
	log.Info("user completed MFA challenge",
		zap.String("poolId", poolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoSignIn, events.ResourcePayload{Name: u.Username})
	return &RespondToAuthChallengeResp{AuthenticationResult: result}, nil
}

// ─── pool handlers ────────────────────────────────────────────────────────────

func (s *Service) CreateUserPoolTyped(ctx context.Context, req *CreateUserPoolReq) (*CreateUserPoolResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if aerr := validateUserPoolPolicies(req.Policies); aerr != nil {
		return nil, aerr
	}
	if aerr := validateUserPoolTier(req.UserPoolTier); aerr != nil {
		return nil, aerr
	}
	if aerr := validateTierForSignInPolicy(req.UserPoolTier, req.Policies); aerr != nil {
		return nil, aerr
	}
	// Validated before the pool is written (#1196) — a rejected create
	// leaves no user pool behind.
	if aerr := serviceutil.ValidateTags(cognitoTagCfg, req.UserPoolTags); aerr != nil {
		return nil, aerr
	}
	region := s.region(ctx)
	poolID := region + "_" + generatePoolSuffix()
	arn := fmt.Sprintf("arn:aws:cognito-idp:%s:%s:userpool/%s", region, s.cfg.AccountID, poolID)
	pool := &UserPool{ID: poolID, Name: req.PoolName, ARN: arn, CreatedAt: s.clk.Now(), UserPoolTier: effectiveTierValue(req.UserPoolTier), UsernameAttributes: req.UsernameAttributes, AliasAttributes: req.AliasAttributes, Tags: req.UserPoolTags}
	if v := req.VerificationMessageTemplate; v != nil {
		opt := v.DefaultEmailOption
		if opt == "" {
			opt = "CONFIRM_WITH_CODE"
		}
		pool.VerificationMessageTemplate = &VerificationMessageTemplate{
			DefaultEmailOption: opt,
			EmailMessage:       v.EmailMessage,
			EmailMessageByLink: v.EmailMessageByLink,
			EmailSubject:       v.EmailSubject,
			EmailSubjectByLink: v.EmailSubjectByLink,
			SmsMessage:         v.SmsMessage,
		}
	}
	if a := req.AdminCreateUserConfig; a != nil {
		validityDays := a.UnusedAccountValidityDays
		if validityDays == 0 {
			validityDays = 7
		}
		cfg := &AdminCreateUserConfig{
			AllowAdminCreateUserOnly:  a.AllowAdminCreateUserOnly,
			UnusedAccountValidityDays: validityDays,
		}
		if t := a.InviteMessageTemplate; t != nil {
			cfg.InviteMessageTemplate = &InviteMessageTemplate{
				EmailMessage: t.EmailMessage,
				EmailSubject: t.EmailSubject,
				SMSMessage:   t.SMSMessage,
			}
		}
		pool.AdminCreateUserConfig = cfg
	}
	if e := req.EmailConfiguration; e != nil {
		pool.EmailConfiguration = &EmailConfiguration{
			EmailSendingAccount: e.EmailSendingAccount,
			SourceArn:           e.SourceArn,
			From:                e.From,
			ReplyToEmailAddress: e.ReplyToEmailAddress,
		}
	}
	if aerr := applyUserAttributeUpdateSettings(pool, req.UserAttributeUpdateSettings); aerr != nil {
		return nil, aerr
	}
	if aerr := applyAutoVerifiedAttributes(pool, req.AutoVerifiedAttributes); aerr != nil {
		return nil, aerr
	}
	if req.DeviceConfiguration != nil {
		pool.DeviceConfiguration = req.DeviceConfiguration
	}
	if req.Policies != nil {
		pool.Policies = mergeUserPoolPolicies(nil, req.Policies)
	}
	pool.AccountRecoverySetting = req.AccountRecoverySetting
	pool.SmsConfiguration = req.SmsConfiguration
	pool.SmsAuthenticationMessage = req.SmsAuthenticationMessage
	pool.SmsVerificationMessage = req.SmsVerificationMessage
	pool.EmailVerificationMessage = req.EmailVerificationMessage
	pool.EmailVerificationSubject = req.EmailVerificationSubject
	pool.LambdaConfig = req.LambdaConfig
	if err := s.savePool(ctx, pool); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user pool created", zap.String("poolId", poolID), zap.String("name", req.PoolName))
	s.publishTyped(ctx, events.CognitoUserPoolCreated, events.ResourcePayload{Name: req.PoolName, ARN: arn})
	return &CreateUserPoolResp{UserPool: toUserPoolWire(pool)}, nil
}

func (s *Service) DescribeUserPoolTyped(ctx context.Context, req *UserPoolIDReq) (*DescribeUserPoolResp, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	users, err := s.scanUsers(ctx, req.UserPoolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	wire := toUserPoolWire(pool)
	wire.EstimatedNumberOfUsers = len(users)
	if d, _ := s.loadDomainForPool(ctx, req.UserPoolID); d != nil {
		wire.Domain = d.Domain
	}
	return &DescribeUserPoolResp{UserPool: wire}, nil
}

func (s *Service) DeleteUserPoolTyped(ctx context.Context, req *UserPoolIDReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	if err := s.store.Delete(ctx, nsPools, s.poolKey(ctx, req.UserPoolID)); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	_ = s.removeSigningKey(ctx, req.UserPoolID)
	log.Info("user pool deleted", zap.String("poolId", req.UserPoolID))
	s.publishTyped(ctx, events.CognitoUserPoolDeleted, events.ResourcePayload{Name: req.UserPoolID})
	return &struct{}{}, nil
}

func (s *Service) SetUserPoolMfaConfigTyped(ctx context.Context, req *UserPoolMfaConfigReq) (*UserPoolMfaConfigResp, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateSetUserPoolMfaConfig(pool, req); aerr != nil {
		return nil, aerr
	}
	if req.WebAuthnConfiguration != nil {
		pool.WebAuthnConfiguration = &WebAuthnConfiguration{
			FactorConfiguration: req.WebAuthnConfiguration.FactorConfiguration,
			RelyingPartyID:      req.WebAuthnConfiguration.RelyingPartyID,
			UserVerification:    req.WebAuthnConfiguration.UserVerification,
		}
	}
	// SetUserPoolMfaConfig replaces the MFA configuration wholesale: a factor
	// the request omits is disabled, and an omitted MfaConfiguration is OFF.
	// See the AWS fork note on validateSetUserPoolMfaConfig.
	pool.MfaConfiguration = effectiveMfaConfiguration(req.MfaConfiguration)
	pool.SmsMfaConfiguration = req.SmsMfaConfiguration.clone()
	pool.SoftwareTokenMfaConfiguration = req.SoftwareTokenMfaConfiguration.clone()
	pool.EmailMfaConfiguration = req.EmailMfaConfiguration.clone()
	if err := s.savePool(ctx, pool); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return mfaConfigResponse(pool), nil
}

func (s *Service) GetUserPoolMfaConfigTyped(ctx context.Context, req *UserPoolIDReq) (*UserPoolMfaConfigResp, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	return mfaConfigResponse(pool), nil
}

func (s *Service) ListUserPoolsTyped(ctx context.Context, req *struct{}) (*ListUserPoolsResp, *protocol.AWSError) {
	pools, err := s.scanPools(ctx)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]poolDesc, 0, len(pools))
	for _, p := range pools {
		epoch := float64(p.CreatedAt.Unix())
		out = append(out, poolDesc{ID: p.ID, Name: p.Name, CreationDate: epoch, LastModifiedDate: epoch})
	}
	return &ListUserPoolsResp{UserPools: out}, nil
}

func (s *Service) UpdateUserPoolTyped(ctx context.Context, req *UpdateUserPoolReq) (*struct{}, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateUserPoolPolicies(req.Policies); aerr != nil {
		return nil, aerr
	}
	if aerr := validateUserPoolTier(req.UserPoolTier); aerr != nil {
		return nil, aerr
	}
	newTier := pool.UserPoolTier
	if req.UserPoolTier != "" {
		newTier = req.UserPoolTier
	}
	if aerr := validateTierForSignInPolicy(newTier, req.Policies); aerr != nil {
		return nil, aerr
	}
	if v := req.VerificationMessageTemplate; v != nil {
		opt := v.DefaultEmailOption
		if opt == "" {
			opt = "CONFIRM_WITH_CODE"
		}
		pool.VerificationMessageTemplate = &VerificationMessageTemplate{
			DefaultEmailOption: opt,
			EmailMessage:       v.EmailMessage,
			EmailMessageByLink: v.EmailMessageByLink,
			EmailSubject:       v.EmailSubject,
			EmailSubjectByLink: v.EmailSubjectByLink,
			SmsMessage:         v.SmsMessage,
		}
	}
	if a := req.AdminCreateUserConfig; a != nil {
		validityDays := a.UnusedAccountValidityDays
		if validityDays == 0 {
			validityDays = 7
		}
		cfg := &AdminCreateUserConfig{
			AllowAdminCreateUserOnly:  a.AllowAdminCreateUserOnly,
			UnusedAccountValidityDays: validityDays,
		}
		if t := a.InviteMessageTemplate; t != nil {
			cfg.InviteMessageTemplate = &InviteMessageTemplate{
				EmailMessage: t.EmailMessage,
				EmailSubject: t.EmailSubject,
				SMSMessage:   t.SMSMessage,
			}
		}
		pool.AdminCreateUserConfig = cfg
	}
	if e := req.EmailConfiguration; e != nil {
		pool.EmailConfiguration = &EmailConfiguration{
			EmailSendingAccount: e.EmailSendingAccount,
			SourceArn:           e.SourceArn,
			From:                e.From,
			ReplyToEmailAddress: e.ReplyToEmailAddress,
		}
	}
	if aerr := applyUserAttributeUpdateSettings(pool, req.UserAttributeUpdateSettings); aerr != nil {
		return nil, aerr
	}
	if aerr := applyAutoVerifiedAttributes(pool, req.AutoVerifiedAttributes); aerr != nil {
		return nil, aerr
	}
	if req.DeviceConfiguration != nil {
		pool.DeviceConfiguration = req.DeviceConfiguration
	}
	if req.UsernameAttributes != nil {
		pool.UsernameAttributes = req.UsernameAttributes
	}
	if req.AliasAttributes != nil {
		pool.AliasAttributes = req.AliasAttributes
	}
	if req.Policies != nil {
		pool.Policies = mergeUserPoolPolicies(pool.Policies, req.Policies)
	}
	if req.UserPoolTier != "" {
		pool.UserPoolTier = req.UserPoolTier
	}
	if req.AccountRecoverySetting != nil {
		pool.AccountRecoverySetting = req.AccountRecoverySetting
	}
	if req.SmsConfiguration != nil {
		pool.SmsConfiguration = req.SmsConfiguration
	}
	if req.SmsAuthenticationMessage != "" {
		pool.SmsAuthenticationMessage = req.SmsAuthenticationMessage
	}
	if req.SmsVerificationMessage != "" {
		pool.SmsVerificationMessage = req.SmsVerificationMessage
	}
	if req.EmailVerificationMessage != "" {
		pool.EmailVerificationMessage = req.EmailVerificationMessage
	}
	if req.EmailVerificationSubject != "" {
		pool.EmailVerificationSubject = req.EmailVerificationSubject
	}
	if req.LambdaConfig != nil {
		pool.LambdaConfig = req.LambdaConfig
	}
	if err := s.savePool(ctx, pool); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &struct{}{}, nil
}

// ─── domain handlers ──────────────────────────────────────────────────────────

func (s *Service) CreateUserPoolDomainTyped(ctx context.Context, req *DomainAndPoolReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	existing, err := s.loadDomain(ctx, req.Domain)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Domain " + req.Domain + " is already associated with a user pool.",
			HTTPStatus: 400,
		}
	}
	d := &UserPoolDomain{
		Domain:     req.Domain,
		UserPoolID: req.UserPoolID,
		CreatedAt:  s.clk.Now(),
	}
	if err := s.saveDomain(ctx, d); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	pool.Domain = req.Domain
	if err := s.savePool(ctx, pool); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user pool domain created",
		zap.String("poolId", req.UserPoolID), zap.String("domain", req.Domain))
	return &struct{}{}, nil
}

func (s *Service) DescribeUserPoolDomainTyped(ctx context.Context, req *DescribeUserPoolDomainReq) (*DescribeUserPoolDomainResp, *protocol.AWSError) {
	d, err := s.loadDomain(ctx, req.Domain)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if d == nil {
		return &DescribeUserPoolDomainResp{DomainDescription: domainDescriptionWire{}}, nil
	}
	return &DescribeUserPoolDomainResp{
		DomainDescription: domainDescriptionWire{
			Domain:     d.Domain,
			UserPoolId: d.UserPoolID,
			Status:     "ACTIVE",
		},
	}, nil
}

func (s *Service) DeleteUserPoolDomainTyped(ctx context.Context, req *DomainAndPoolReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.removeDomain(ctx, req.Domain); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if pool.Domain == req.Domain {
		pool.Domain = ""
		_ = s.savePool(ctx, pool)
	}
	log.Info("user pool domain deleted",
		zap.String("poolId", req.UserPoolID), zap.String("domain", req.Domain))
	return &struct{}{}, nil
}

func (s *Service) UpdateUserPoolDomainTyped(ctx context.Context, req *DomainAndPoolReq) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

// ─── pool client handlers ─────────────────────────────────────────────────────

func (s *Service) CreateUserPoolClientTyped(ctx context.Context, req *CreateUserPoolClientReq) (*CreateUserPoolClientResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateExplicitAuthFlows(req.ExplicitAuthFlows); aerr != nil {
		return nil, aerr
	}
	if aerr := validateExplicitAuthFlowsForTier(pool, req.ExplicitAuthFlows); aerr != nil {
		return nil, aerr
	}
	secret := ""
	if req.GenerateSecret {
		secret = generateClientSecret()
	}
	c := &UserPoolClient{
		ClientID:                        generateClientID(),
		ClientName:                      req.ClientName,
		UserPoolID:                      req.UserPoolID,
		CreatedAt:                       s.clk.Now(),
		ClientSecret:                    secret,
		AccessTokenValidity:             req.AccessTokenValidity,
		IdTokenValidity:                 req.IdTokenValidity,
		RefreshTokenValidity:            req.RefreshTokenValidity,
		TokenValidityUnits:              req.TokenValidityUnits,
		CallbackURLs:                    req.CallbackURLs,
		LogoutURLs:                      req.LogoutURLs,
		AllowedOAuthFlows:               req.AllowedOAuthFlows,
		AllowedOAuthScopes:              req.AllowedOAuthScopes,
		AllowedOAuthFlowsUserPoolClient: req.AllowedOAuthFlowsUserPoolClient,
		ExplicitAuthFlows:               req.ExplicitAuthFlows,
		SupportedIdentityProviders:      req.SupportedIdentityProviders,
		PreventUserExistenceErrors:      req.PreventUserExistenceErrors,
		ReadAttributes:                  req.ReadAttributes,
		WriteAttributes:                 req.WriteAttributes,
	}
	applyClientDefaults(c)
	if err := s.saveClient(ctx, c); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user pool client created",
		zap.String("poolId", req.UserPoolID), zap.String("clientId", c.ClientID))
	s.publishTyped(ctx, events.CognitoClientCreated, events.ResourcePayload{Name: c.ClientName})
	return &CreateUserPoolClientResp{UserPoolClient: toClientWire(c)}, nil
}

func (s *Service) DescribeUserPoolClientTyped(ctx context.Context, req *PoolAndClientReq) (*DescribeUserPoolClientResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	c, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	return &DescribeUserPoolClientResp{UserPoolClient: toClientWire(c)}, nil
}

func (s *Service) DeleteUserPoolClientTyped(ctx context.Context, req *PoolAndClientReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	if err := s.removeClient(ctx, req.UserPoolID, req.ClientID); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user pool client deleted",
		zap.String("poolId", req.UserPoolID), zap.String("clientId", req.ClientID))
	s.publishTyped(ctx, events.CognitoClientDeleted, events.ResourcePayload{Name: req.ClientID})
	return &struct{}{}, nil
}

func (s *Service) ListUserPoolClientsTyped(ctx context.Context, req *UserPoolIDReq) (*ListUserPoolClientsResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	clients, err := s.scanClients(ctx, req.UserPoolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out := make([]clientDesc, 0, len(clients))
	for _, c := range clients {
		out = append(out, clientDesc{ClientID: c.ClientID, ClientName: c.ClientName, UserPoolId: c.UserPoolID})
	}
	return &ListUserPoolClientsResp{UserPoolClients: out}, nil
}

func (s *Service) UpdateUserPoolClientTyped(ctx context.Context, req *UpdateUserPoolClientReq) (*UpdateUserPoolClientResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	c, err := s.loadClient(ctx, req.UserPoolID, req.ClientID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if c == nil {
		return nil, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Client " + req.ClientID + " does not exist.",
			HTTPStatus: 400,
		}
	}
	if req.AccessTokenValidity > 0 {
		c.AccessTokenValidity = req.AccessTokenValidity
	}
	if req.IdTokenValidity > 0 {
		c.IdTokenValidity = req.IdTokenValidity
	}
	if req.RefreshTokenValidity > 0 {
		c.RefreshTokenValidity = req.RefreshTokenValidity
	}
	if req.TokenValidityUnits != nil {
		if c.TokenValidityUnits == nil {
			c.TokenValidityUnits = defaultTokenValidityUnits()
		}
		if req.TokenValidityUnits.AccessToken != "" {
			c.TokenValidityUnits.AccessToken = req.TokenValidityUnits.AccessToken
		}
		if req.TokenValidityUnits.IdToken != "" {
			c.TokenValidityUnits.IdToken = req.TokenValidityUnits.IdToken
		}
		if req.TokenValidityUnits.RefreshToken != "" {
			c.TokenValidityUnits.RefreshToken = req.TokenValidityUnits.RefreshToken
		}
	}
	if req.CallbackURLs != nil {
		c.CallbackURLs = *req.CallbackURLs
	}
	if req.LogoutURLs != nil {
		c.LogoutURLs = *req.LogoutURLs
	}
	if req.AllowedOAuthFlows != nil {
		c.AllowedOAuthFlows = *req.AllowedOAuthFlows
	}
	if req.AllowedOAuthScopes != nil {
		c.AllowedOAuthScopes = *req.AllowedOAuthScopes
	}
	if req.AllowedOAuthFlowsUserPoolClient != nil {
		c.AllowedOAuthFlowsUserPoolClient = *req.AllowedOAuthFlowsUserPoolClient
	}
	if req.ExplicitAuthFlows != nil {
		if aerr := validateExplicitAuthFlows(*req.ExplicitAuthFlows); aerr != nil {
			return nil, aerr
		}
		if aerr := validateExplicitAuthFlowsForTier(pool, *req.ExplicitAuthFlows); aerr != nil {
			return nil, aerr
		}
		c.ExplicitAuthFlows = *req.ExplicitAuthFlows
	}
	if req.SupportedIdentityProviders != nil {
		c.SupportedIdentityProviders = *req.SupportedIdentityProviders
	}
	if req.PreventUserExistenceErrors != "" {
		c.PreventUserExistenceErrors = req.PreventUserExistenceErrors
	}
	if req.ReadAttributes != nil {
		c.ReadAttributes = *req.ReadAttributes
	}
	if req.WriteAttributes != nil {
		c.WriteAttributes = *req.WriteAttributes
	}
	if err := s.saveClient(ctx, c); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user pool client updated",
		zap.String("poolId", req.UserPoolID), zap.String("clientId", req.ClientID))
	return &UpdateUserPoolClientResp{UserPoolClient: toClientWire(c)}, nil
}

// ─── admin user handlers ──────────────────────────────────────────────────────

func (s *Service) AdminCreateUserTyped(ctx context.Context, req *AdminCreateUserReq) (*AdminCreateUserResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	attrs := req.UserAttributes
	if attrs == nil {
		attrs = []UserAttribute{}
	}
	internalUsername := req.Username
	sub := uuid.NewString()
	if len(pool.UsernameAttributes) > 0 {
		// Per AWS docs for username-attribute pools, Cognito populates the
		// generated username with the same UUID value as the user's sub claim.
		internalUsername = sub
		autoSetUsernameAttribute(pool.UsernameAttributes, &attrs, req.Username)
	}
	existing, err := s.resolveUser(ctx, pool, req.Username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return nil, errUsernameExists(req.Username)
	}
	aliasOwner, aliasAttr, err := s.findVerifiedAliasOwner(ctx, pool, attrs)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aliasOwner != nil {
		if !req.ForceAliasCreation {
			return nil, errAliasExists()
		}
		if err := s.migrateVerifiedAlias(ctx, aliasOwner, aliasAttr); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	tempPw := req.TemporaryPassword
	if tempPw == "" {
		tempPw = generateTempPassword(pool)
	} else if aerr := validatePassword(pool, tempPw); aerr != nil {
		return nil, aerr
	}
	hash, err := hashPassword(tempPw)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u := &User{
		Username:          internalUsername,
		Sub:               sub,
		UserPoolID:        req.UserPoolID,
		CreatedAt:         s.clk.Now(),
		Status:            StatusForceChangePassword,
		Enabled:           true,
		PasswordHash:      string(hash),
		PlaintextPassword: tempPw,
		TempPassword:      tempPw,
		Attributes:        attrs,
	}
	u.setAttr("sub", sub)
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	// PreSignUp_AdminCreateUser / CustomMessage_AdminCreateUser are not wired
	// on this Smithy RPC v2 duplicate path — see triggers.go's deferred-scope
	// note; AdminCreateUser (the classic API, handler_users.go) has both.
	if req.MessageAction != "SUPPRESS" {
		if emailAddr := u.email(); emailAddr != "" {
			s.sendTempPasswordEmail(pool, emailAddr, u.Username, tempPw, nil)
		}
		if phone := u.phoneNumber(); phone != "" {
			s.sendTempPasswordSMS(pool, phone, u.Username, tempPw, nil)
		}
	}
	log.Info("admin created user",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoUserCreated, events.ResourcePayload{Name: req.Username})
	return &AdminCreateUserResp{User: toUserWire(u)}, nil
}

func (s *Service) AdminDeleteUserTyped(ctx context.Context, req *PoolAndUserReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.removeUser(ctx, req.UserPoolID, u.Username); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("admin deleted user",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoUserDeleted, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminGetUserTyped(ctx context.Context, req *PoolAndUserReq) (*AdminGetUserResp, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	uw := toUserWire(u)
	return &AdminGetUserResp{
		Username:             uw.Username,
		UserAttributes:       uw.Attributes,
		UserCreateDate:       uw.UserCreateDate,
		UserLastModifiedDate: uw.UserLastModifiedDate,
		Enabled:              uw.Enabled,
		UserStatus:           uw.UserStatus,
		PreferredMfaSetting:  preferredMfaSetting(pool, u),
		UserMFASettingList:   userMfaFactors(pool, u),
	}, nil
}

func (s *Service) AdminSetUserPasswordTyped(ctx context.Context, req *AdminSetUserPasswordReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u.PasswordHash = string(hash)
	u.PlaintextPassword = req.Password
	u.TempPassword = ""
	if req.Permanent && u.Status == StatusForceChangePassword {
		u.Status = StatusConfirmed
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("admin set user password",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username),
		zap.Bool("permanent", req.Permanent))
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminConfirmSignUpTyped(ctx context.Context, req *PoolAndUserReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	u.Status = StatusConfirmed
	u.ConfirmationCode = ""
	if u.email() != "" {
		u.setAttr("email_verified", "true")
	}
	if u.phoneNumber() != "" {
		u.setAttr("phone_number_verified", "true")
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("admin confirmed user signup",
		zap.String("poolId", req.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoUserConfirmed, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminUpdateUserAttributesTyped(ctx context.Context, req *AdminUpdateUserAttributesReq) (*struct{}, *protocol.AWSError) {
	pool, aerr := s.requirePoolTyped(ctx, req.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	immediateAttrs := make([]UserAttribute, 0, len(req.UserAttributes))
	for _, attr := range req.UserAttributes {
		if verifiedAttributeName(attr.Name) != "" && requiresVerificationBeforeUpdate(pool, attr.Name) && !hasVerificationBypass(req.UserAttributes, attr.Name) {
			continue
		}
		immediateAttrs = append(immediateAttrs, attr)
	}
	aliasOwner, _, err := s.findVerifiedAliasOwner(ctx, pool, attributesAfterUpdate(u, immediateAttrs))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aliasOwner != nil && aliasOwner.Username != u.Username {
		return nil, errAliasExists()
	}
	if _, aerr := s.updateAttributesWithVerification(ctx, pool, u, req.UserAttributes, true); aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminDeleteUserAttributesTyped(ctx context.Context, req *AdminDeleteUserAttributesReq) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	for _, name := range req.UserAttributeNames {
		u.removeAttr(name)
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminDisableUserTyped(ctx context.Context, req *PoolAndUserReq) (*struct{}, *protocol.AWSError) {
	return s.setUserEnabledTyped(ctx, req.UserPoolID, req.Username, false)
}

func (s *Service) AdminEnableUserTyped(ctx context.Context, req *PoolAndUserReq) (*struct{}, *protocol.AWSError) {
	return s.setUserEnabledTyped(ctx, req.UserPoolID, req.Username, true)
}

func (s *Service) setUserEnabledTyped(ctx context.Context, poolID, username string, enabled bool) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, poolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, poolID, username)
	if aerr != nil {
		return nil, aerr
	}
	u.Enabled = enabled
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

func (s *Service) ListUsersTyped(ctx context.Context, req *ListUsersReq) (*ListUsersResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	users, err := s.scanUsers(ctx, req.UserPoolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	out, nextToken, aerr := filterAndPageUsers(users, req.Filter, req.AttributesToGet, req.Limit, req.PaginationToken)
	if aerr != nil {
		return nil, aerr
	}
	return &ListUsersResp{Users: out, PaginationToken: nextToken}, nil
}

func (s *Service) AdminInitiateAuthTyped(ctx context.Context, req *AdminInitiateAuthReq) (*InitiateAuthResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	switch req.AuthFlow {
	case "ADMIN_USER_PASSWORD_AUTH", "USER_PASSWORD_AUTH":
		pwClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := s.checkSecretHashTyped(pwClient, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		return s.handlePasswordAuthTyped(ctx, pwClient, req.AuthParameters)
	case "USER_SRP_AUTH":
		pwClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := s.checkSecretHashTyped(pwClient, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkAuthFlowAllowedTyped(pwClient, "ALLOW_USER_SRP_AUTH"); aerr != nil {
			return nil, aerr
		}
		return s.handleSRPAuthStartTyped(ctx, pwClient, req.AuthParameters)
	case "USER_AUTH":
		adminClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := s.checkSecretHashTyped(adminClient, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkAuthFlowAllowedTyped(adminClient, "ALLOW_USER_AUTH"); aerr != nil {
			return nil, aerr
		}
		return s.handleUserAuthWithConfirmSessionTyped(ctx, adminClient, req.AuthParameters, req.Session)
	case "CUSTOM_AUTH":
		adminClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := s.checkSecretHashTyped(adminClient, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkCustomAuthFlowAllowedTyped(adminClient); aerr != nil {
			return nil, aerr
		}
		return s.handleCustomAuthStartTyped(ctx, adminClient, req.AuthParameters)
	case "REFRESH_TOKEN_AUTH", "REFRESH_TOKEN":
		adminClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
		if aerr != nil {
			return nil, aerr
		}
		return s.handleRefreshTokenAuthTyped(ctx, adminClient, req.AuthParameters)
	default:
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Unsupported AuthFlow: " + req.AuthFlow,
			HTTPStatus: 400,
		}
	}
}

func (s *Service) AdminRespondToAuthChallengeTyped(ctx context.Context, req *AdminRespondToAuthChallengeReq) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	adminClient, aerr := s.requireClientTyped(ctx, req.UserPoolID, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(adminClient, req.ChallengeResponses["USERNAME"], req.ChallengeResponses["SECRET_HASH"]); aerr != nil {
		return nil, aerr
	}
	switch req.ChallengeName {
	case "SELECT_CHALLENGE":
		return s.handleChoiceAuthChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "PASSWORD":
		return s.handlePasswordChoiceChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "PASSWORD_VERIFIER":
		return s.completeSRPVerifierChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "WEB_AUTHN":
		return s.completeWebAuthnChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "EMAIL_OTP", "SMS_OTP":
		return s.completeOTPChallengeTyped(ctx, adminClient, req.ChallengeName, req.Session, req.ChallengeResponses)
	case "CUSTOM_CHALLENGE":
		return s.completeCustomAuthChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "DEVICE_SRP_AUTH":
		return s.startDeviceSRPChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "DEVICE_PASSWORD_VERIFIER":
		return s.completeDevicePasswordVerifierTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "NEW_PASSWORD_REQUIRED":
		return s.handleNewPasswordChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "SOFTWARE_TOKEN_MFA":
		return s.handleMFAChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "SMS_MFA":
		return s.completeSmsMfaChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "SELECT_MFA_TYPE":
		return s.handleSelectMfaTypeChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	case "MFA_SETUP":
		return s.completeMfaSetupChallengeTyped(ctx, adminClient, req.Session, req.ChallengeResponses)
	default:
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Unknown ChallengeName: " + req.ChallengeName,
			HTTPStatus: 400,
		}
	}
}

// ─── self-service sign-up handlers ────────────────────────────────────────────

func (s *Service) SignUpTyped(ctx context.Context, req *SignUpReq) (*SignUpResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.Username, req.SecretHash); aerr != nil {
		return nil, aerr
	}
	pool, err := s.loadPool(ctx, c.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !allowSelfRegistration(pool) {
		return nil, &protocol.AWSError{
			Code:       "NotAuthorizedException",
			Message:    "User pool is configured to only allow admin-created users.",
			HTTPStatus: 400,
		}
	}
	if aerr := validatePassword(pool, req.Password); aerr != nil {
		return nil, aerr
	}
	existing, err := s.resolveUser(ctx, pool, req.Username)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return nil, errUsernameExists(req.Username)
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	code := generateCode()
	attrs := req.UserAttributes
	if attrs == nil {
		attrs = []UserAttribute{}
	}
	internalUsername := req.Username
	sub := uuid.NewString()
	if len(pool.UsernameAttributes) > 0 {
		internalUsername = sub
		autoSetUsernameAttribute(pool.UsernameAttributes, &attrs, req.Username)
	}
	u := &User{
		Username:          internalUsername,
		Sub:               sub,
		UserPoolID:        c.UserPoolID,
		CreatedAt:         s.clk.Now(),
		Status:            StatusUnconfirmed,
		Enabled:           true,
		PasswordHash:      string(hash),
		PlaintextPassword: req.Password,
		Attributes:        attrs,
		ConfirmationCode:  code,
	}
	u.setAttr("sub", u.Sub)
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	// PreSignUp_SignUp / CustomMessage_SignUp are not wired on this Smithy
	// RPC v2 duplicate path — see triggers.go's deferred-scope note; SignUp
	// (the classic API, handler_auth.go) has both.
	if emailAddr := u.email(); emailAddr != "" {
		s.sendVerificationEmail(pool, emailAddr, u.Username, code, nil)
	}
	if phone := u.phoneNumber(); phone != "" {
		s.sendVerificationSMS(pool, phone, u.Username, code, nil)
	}
	log.Info("user signed up",
		zap.String("poolId", c.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoUserCreated, events.ResourcePayload{Name: req.Username})
	return &SignUpResp{UserConfirmed: false, CodeDeliveryDetails: signUpCodeDeliveryDetails(pool, u), UserSub: u.Sub}, nil
}

func (s *Service) ConfirmSignUpTyped(ctx context.Context, req *ConfirmSignUpReq) (*ConfirmSignUpResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.Username, req.SecretHash); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, c.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if u.ConfirmationCode == "" {
		return nil, errExpiredCode()
	}
	if u.ConfirmationCode != req.ConfirmationCode {
		return nil, errCodeMismatch()
	}
	pool, err := s.loadPool(ctx, c.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	aliasOwner, aliasAttr, err := s.findVerifiedAliasOwner(ctx, pool, attributesAfterConfirmation(u))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aliasOwner != nil && aliasOwner.Username != u.Username {
		if !req.ForceAliasCreation {
			return nil, errAliasExists()
		}
		if err := s.migrateVerifiedAlias(ctx, aliasOwner, aliasAttr); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	u.Status = StatusConfirmed
	u.ConfirmationCode = ""
	if u.email() != "" {
		u.setAttr("email_verified", "true")
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	session, err := s.issueOpaqueToken(ctx, c.UserPoolID, u.Username, "confirm", 5*time.Minute)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user confirmed signup",
		zap.String("poolId", c.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoUserConfirmed, events.ResourcePayload{Name: req.Username})
	return &ConfirmSignUpResp{Session: session}, nil
}

func (s *Service) ResendConfirmationCodeTyped(ctx context.Context, req *ClientUserSecretReq) (*struct{}, *protocol.AWSError) {
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.Username, req.SecretHash); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, c.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	code := generateCode()
	u.ConfirmationCode = code
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	// CustomMessage_ResendCode is not wired on this Smithy RPC v2 duplicate
	// path — see triggers.go's deferred-scope note; ResendConfirmationCode
	// (the classic API, handler_auth.go) has it.
	if emailAddr := u.email(); emailAddr != "" {
		pool, _ := s.loadPool(ctx, c.UserPoolID)
		if pool != nil {
			s.sendVerificationEmail(pool, emailAddr, u.Username, code, nil)
		}
	}
	if phone := u.phoneNumber(); phone != "" {
		pool, _ := s.loadPool(ctx, c.UserPoolID)
		if pool != nil {
			s.sendVerificationSMS(pool, phone, u.Username, code, nil)
		}
	}
	return &struct{}{}, nil
}

// ─── auth flow handlers ───────────────────────────────────────────────────────

func (s *Service) InitiateAuthTyped(ctx context.Context, req *InitiateAuthReq) (*InitiateAuthResp, *protocol.AWSError) {
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	switch req.AuthFlow {
	case "USER_PASSWORD_AUTH":
		if aerr := s.checkSecretHashTyped(c, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		return s.handlePasswordAuthTyped(ctx, c, req.AuthParameters)
	case "USER_SRP_AUTH":
		if aerr := s.checkSecretHashTyped(c, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkAuthFlowAllowedTyped(c, "ALLOW_USER_SRP_AUTH"); aerr != nil {
			return nil, aerr
		}
		return s.handleSRPAuthStartTyped(ctx, c, req.AuthParameters)
	case "USER_AUTH":
		if aerr := s.checkSecretHashTyped(c, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkAuthFlowAllowedTyped(c, "ALLOW_USER_AUTH"); aerr != nil {
			return nil, aerr
		}
		return s.handleUserAuthWithConfirmSessionTyped(ctx, c, req.AuthParameters, req.Session)
	case "CUSTOM_AUTH":
		if aerr := s.checkSecretHashTyped(c, req.AuthParameters["USERNAME"], req.AuthParameters["SECRET_HASH"]); aerr != nil {
			return nil, aerr
		}
		if aerr := checkCustomAuthFlowAllowedTyped(c); aerr != nil {
			return nil, aerr
		}
		return s.handleCustomAuthStartTyped(ctx, c, req.AuthParameters)
	case "REFRESH_TOKEN_AUTH", "REFRESH_TOKEN":
		return s.handleRefreshTokenAuthTyped(ctx, c, req.AuthParameters)
	default:
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Unsupported AuthFlow: " + req.AuthFlow,
			HTTPStatus: 400,
		}
	}
}

func (s *Service) RespondToAuthChallengeTyped(ctx context.Context, req *RespondToAuthChallengeReq) (*RespondToAuthChallengeResp, *protocol.AWSError) {
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.ChallengeResponses["USERNAME"], req.ChallengeResponses["SECRET_HASH"]); aerr != nil {
		return nil, aerr
	}
	switch req.ChallengeName {
	case "SELECT_CHALLENGE":
		return s.handleChoiceAuthChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "PASSWORD":
		return s.handlePasswordChoiceChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "PASSWORD_VERIFIER":
		return s.completeSRPVerifierChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "WEB_AUTHN":
		return s.completeWebAuthnChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "EMAIL_OTP", "SMS_OTP":
		return s.completeOTPChallengeTyped(ctx, c, req.ChallengeName, req.Session, req.ChallengeResponses)
	case "CUSTOM_CHALLENGE":
		return s.completeCustomAuthChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "DEVICE_SRP_AUTH":
		return s.startDeviceSRPChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "DEVICE_PASSWORD_VERIFIER":
		return s.completeDevicePasswordVerifierTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "NEW_PASSWORD_REQUIRED":
		return s.handleNewPasswordChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "SOFTWARE_TOKEN_MFA":
		return s.handleMFAChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "SMS_MFA":
		return s.completeSmsMfaChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "SELECT_MFA_TYPE":
		return s.handleSelectMfaTypeChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	case "MFA_SETUP":
		return s.completeMfaSetupChallengeTyped(ctx, c, req.Session, req.ChallengeResponses)
	default:
		return nil, &protocol.AWSError{
			Code:       "InvalidParameterException",
			Message:    "Unknown ChallengeName: " + req.ChallengeName,
			HTTPStatus: 400,
		}
	}
}

func checkAuthFlowAllowedTyped(client *UserPoolClient, flow string) *protocol.AWSError {
	for _, allowed := range client.ExplicitAuthFlows {
		if allowed == flow {
			return nil
		}
	}
	return &protocol.AWSError{Code: "UnsupportedOperationException", Message: "Auth flow is not enabled for this client.", HTTPStatus: 400}
}

func checkCustomAuthFlowAllowedTyped(client *UserPoolClient) *protocol.AWSError {
	for _, allowed := range client.ExplicitAuthFlows {
		if allowed == "ALLOW_CUSTOM_AUTH" || allowed == "CUSTOM_AUTH_FLOW_ONLY" {
			return nil
		}
	}
	return &protocol.AWSError{Code: "UnsupportedOperationException", Message: "Auth flow is not enabled for this client.", HTTPStatus: 400}
}

func validateExplicitAuthFlows(flows []string) *protocol.AWSError {
	valid := []string{
		"ADMIN_NO_SRP_AUTH",
		"CUSTOM_AUTH_FLOW_ONLY",
		"USER_PASSWORD_AUTH",
		"ALLOW_ADMIN_USER_PASSWORD_AUTH",
		"ALLOW_CUSTOM_AUTH",
		"ALLOW_USER_PASSWORD_AUTH",
		"ALLOW_USER_SRP_AUTH",
		"ALLOW_REFRESH_TOKEN_AUTH",
		"ALLOW_USER_AUTH",
	}
	hasLegacy := false
	hasAllow := false
	for _, flow := range flows {
		if !slices.Contains(valid, flow) {
			return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid ExplicitAuthFlows value: " + flow, HTTPStatus: 400}
		}
		if len(flow) >= len("ALLOW_") && flow[:len("ALLOW_")] == "ALLOW_" {
			hasAllow = true
		} else {
			hasLegacy = true
		}
	}
	if hasLegacy && hasAllow {
		return &protocol.AWSError{Code: "InvalidParameterException", Message: "Legacy ExplicitAuthFlows values can't be mixed with ALLOW_ values.", HTTPStatus: 400}
	}
	return nil
}

func validateExplicitAuthFlowsForTier(pool *UserPool, flows []string) *protocol.AWSError {
	if effectiveUserPoolTier(pool) != "LITE" {
		return nil
	}
	for _, flow := range flows {
		if flow == "ALLOW_USER_AUTH" {
			return errFeatureUnavailableInTier("ALLOW_USER_AUTH requires the Essentials tier or higher.")
		}
	}
	return nil
}

func validateUserPoolTier(tier string) *protocol.AWSError {
	if tier == "" || tier == "LITE" || tier == "ESSENTIALS" || tier == "PLUS" {
		return nil
	}
	return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid UserPoolTier value: " + tier, HTTPStatus: 400}
}

func effectiveTierValue(tier string) string {
	if tier == "" {
		return "ESSENTIALS"
	}
	return tier
}

func effectiveUserPoolTier(pool *UserPool) string {
	if pool == nil || pool.UserPoolTier == "" {
		return "ESSENTIALS"
	}
	return pool.UserPoolTier
}

func validateTierForSignInPolicy(tier string, policies *userPoolPoliciesWire) *protocol.AWSError {
	if effectiveTierValue(tier) == "LITE" && policies != nil && policies.SignInPolicy != nil {
		return errFeatureUnavailableInTier("SignInPolicy requires the Essentials tier or higher.")
	}
	return nil
}

func validateTierForWebAuthn(tier string) *protocol.AWSError {
	if effectiveTierValue(tier) == "LITE" {
		return errFeatureUnavailableInTier("WebAuthnConfiguration requires the Essentials tier or higher.")
	}
	return nil
}

func errFeatureUnavailableInTier(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "FeatureUnavailableInTierException", Message: message, HTTPStatus: 400}
}

func mergeUserPoolPolicies(existing *UserPoolPolicies, wire *userPoolPoliciesWire) *UserPoolPolicies {
	if existing == nil {
		existing = &UserPoolPolicies{}
	}
	if wire == nil {
		return existing
	}
	if pp := wire.PasswordPolicy; pp != nil {
		existing.PasswordPolicy = &PasswordPolicy{
			MinimumLength:                 pp.MinimumLength,
			RequireUppercase:              pp.RequireUppercase,
			RequireLowercase:              pp.RequireLowercase,
			RequireNumbers:                pp.RequireNumbers,
			RequireSymbols:                pp.RequireSymbols,
			TemporaryPasswordValidityDays: pp.TemporaryPasswordValidityDays,
		}
	}
	if sp := wire.SignInPolicy; sp != nil {
		existing.SignInPolicy = &SignInPolicy{AllowedFirstAuthFactors: sp.AllowedFirstAuthFactors}
	}
	if existing.PasswordPolicy == nil && existing.SignInPolicy == nil {
		return nil
	}
	return existing
}

func validateUserPoolPolicies(wire *userPoolPoliciesWire) *protocol.AWSError {
	if wire == nil || wire.SignInPolicy == nil {
		return nil
	}
	factors := wire.SignInPolicy.AllowedFirstAuthFactors
	if len(factors) == 0 || len(factors) > 4 {
		return &protocol.AWSError{Code: "InvalidParameterException", Message: "AllowedFirstAuthFactors must include between 1 and 4 values.", HTTPStatus: 400}
	}
	valid := []string{"PASSWORD", "EMAIL_OTP", "SMS_OTP", "WEB_AUTHN"}
	seen := map[string]bool{}
	for _, factor := range factors {
		if !slices.Contains(valid, factor) {
			return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid AllowedFirstAuthFactors value: " + factor, HTTPStatus: 400}
		}
		if seen[factor] {
			return &protocol.AWSError{Code: "InvalidParameterException", Message: "Duplicate AllowedFirstAuthFactors value: " + factor, HTTPStatus: 400}
		}
		seen[factor] = true
	}
	if seen["WEB_AUTHN"] && len(factors) == 1 {
		return &protocol.AWSError{Code: "InvalidParameterException", Message: "WEB_AUTHN must be configured with at least one additional first auth factor.", HTTPStatus: 400}
	}
	return nil
}

func validateWebAuthnConfiguration(wire *webAuthnConfigurationWire) *protocol.AWSError {
	if wire == nil {
		return nil
	}
	if wire.UserVerification != "" && wire.UserVerification != "preferred" && wire.UserVerification != "required" {
		return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid UserVerification value: " + wire.UserVerification, HTTPStatus: 400}
	}
	if wire.FactorConfiguration != "" && wire.FactorConfiguration != "SINGLE_FACTOR" && wire.FactorConfiguration != "MULTI_FACTOR_WITH_USER_VERIFICATION" {
		return &protocol.AWSError{Code: "InvalidParameterException", Message: "Invalid FactorConfiguration value: " + wire.FactorConfiguration, HTTPStatus: 400}
	}
	return nil
}

// mfaCodePlaceholder is the substitution token every Cognito message template
// that carries a one-time code must contain.
const mfaCodePlaceholder = "{####}"

// Model-pinned constraints from cognito-identity-provider-2016-04-18:
// SmsVerificationMessageType (@length 6..140, @pattern \{####\}) backs
// SmsMfaConfigType.SmsAuthenticationMessage, and EmailMfaMessageType
// (@length 6..20000, @pattern ...\{####\}...) backs EmailMfaConfigType.Message.
const (
	smsMfaMessageMinLen   = 6
	smsMfaMessageMaxLen   = 140
	emailMfaMessageMinLen = 6
	emailMfaMessageMaxLen = 20000

	smsMfaMessagePattern   = `\{####\}`
	emailMfaMessagePattern = `^[\p{L}\p{M}\p{S}\p{N}\p{P}\s*]*\{####\}[\p{L}\p{M}\p{S}\p{N}\p{P}\s*]*$`
)

// effectiveMfaConfiguration resolves an omitted MfaConfiguration to OFF, which
// is both what a pool that has never been configured reports and what a
// SetUserPoolMfaConfig request that omits the member means.
func effectiveMfaConfiguration(value string) string {
	if value == "" {
		return "OFF"
	}
	return value
}

// mfaFactorEnabled reports whether a SetUserPoolMfaConfig request turns on at
// least one MFA factor. SmsMfaConfigType and EmailMfaConfigType have no Enabled
// member, so their presence is the switch; SoftwareTokenMfaConfigType has one.
// A passkey configuration counts only when it is set to satisfy MFA, per
// "passkeys with user verification can satisfy MFA requirements ... when you
// set FactorConfiguration to MULTI_FACTOR_WITH_USER_VERIFICATION" in the
// Cognito Developer Guide (Adding MFA to a user pool).
func mfaFactorEnabled(req *UserPoolMfaConfigReq) bool {
	if req.SmsMfaConfiguration != nil || req.EmailMfaConfiguration != nil {
		return true
	}
	if c := req.SoftwareTokenMfaConfiguration; c != nil && c.Enabled {
		return true
	}
	if w := req.WebAuthnConfiguration; w != nil && w.FactorConfiguration == "MULTI_FACTOR_WITH_USER_VERIFICATION" {
		return true
	}
	return false
}

// validateSetUserPoolMfaConfig applies the request-level rules real Cognito
// enforces on SetUserPoolMfaConfig.
//
// The two combination rules and their message text come from AWS responses
// reported verbatim against the live API: requesting ON or OPTIONAL with no
// factor enabled is rejected even when the pool already has one stored
// (aws/aws-sdk-js#4186), which is also the evidence that the request replaces
// the stored factor configuration rather than merging with it; and turning MFA
// off while configuring a factor is rejected
// (lgallard/terraform-aws-cognito-user-pool#74).
func validateSetUserPoolMfaConfig(pool *UserPool, req *UserPoolMfaConfigReq) *protocol.AWSError {
	switch req.MfaConfiguration {
	case "", "OFF", "ON", "OPTIONAL":
	default:
		return errCognitoConstraint(req.MfaConfiguration, "mfaConfiguration",
			"Member must satisfy enum value set: [OFF, ON, OPTIONAL]")
	}
	if req.WebAuthnConfiguration != nil {
		if aerr := validateTierForWebAuthn(pool.UserPoolTier); aerr != nil {
			return aerr
		}
		if aerr := validateWebAuthnConfiguration(req.WebAuthnConfiguration); aerr != nil {
			return aerr
		}
	}
	if aerr := validateSmsMfaConfiguration(req.SmsMfaConfiguration); aerr != nil {
		return aerr
	}
	if req.EmailMfaConfiguration != nil {
		if aerr := validateTierForEmailMfa(pool.UserPoolTier); aerr != nil {
			return aerr
		}
		if aerr := validateEmailMfaConfiguration(req.EmailMfaConfiguration); aerr != nil {
			return aerr
		}
	}
	enabled := mfaFactorEnabled(req)
	if effectiveMfaConfiguration(req.MfaConfiguration) == "OFF" {
		if enabled {
			return errInvalidMfaConfiguration("can't turn off MFA and configure an MFA together.")
		}
		return nil
	}
	if !enabled {
		return errInvalidMfaConfiguration("can't disable all MFAs with a required or optional configuration.")
	}
	return nil
}

func validateSmsMfaConfiguration(c *SmsMfaConfig) *protocol.AWSError {
	if c == nil || c.SmsAuthenticationMessage == "" {
		return nil
	}
	return validateMfaMessageTemplate(c.SmsAuthenticationMessage,
		"smsMfaConfiguration.smsAuthenticationMessage",
		smsMfaMessageMinLen, smsMfaMessageMaxLen, smsMfaMessagePattern)
}

func validateEmailMfaConfiguration(c *EmailMfaConfig) *protocol.AWSError {
	if c == nil || c.Message == "" {
		return nil
	}
	return validateMfaMessageTemplate(c.Message, "emailMfaConfiguration.message",
		emailMfaMessageMinLen, emailMfaMessageMaxLen, emailMfaMessagePattern)
}

// validateMfaMessageTemplate applies the modeled @length and @pattern
// constraints to a message template. The pattern check is the {####}
// requirement the pattern exists to express; the allowed character classes are
// not enforced.
func validateMfaMessageTemplate(value, member string, minLen, maxLen int, pattern string) *protocol.AWSError {
	if n := len([]rune(value)); n < minLen || n > maxLen {
		return errCognitoConstraint(value, member, fmt.Sprintf(
			"Member must have length greater than or equal to %d, Member must have length less than or equal to %d",
			minLen, maxLen))
	}
	if !strings.Contains(value, mfaCodePlaceholder) {
		return errCognitoConstraint(value, member,
			"Member must satisfy regular expression pattern: "+pattern)
	}
	return nil
}

func errCognitoConstraint(value, member, constraint string) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "InvalidParameterException",
		Message: fmt.Sprintf("1 validation error detected: Value '%s' at '%s' failed to satisfy constraint: %s",
			value, member, constraint),
		HTTPStatus: 400,
	}
}

func errInvalidMfaConfiguration(reason string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidParameterException",
		Message:    "Invalid MFA configuration given, " + reason,
		HTTPStatus: 400,
	}
}

func validateTierForEmailMfa(tier string) *protocol.AWSError {
	if effectiveTierValue(tier) == "LITE" {
		return errFeatureUnavailableInTier("EmailMfaConfiguration requires the Essentials tier or higher.")
	}
	return nil
}

func mfaConfigResponse(pool *UserPool) *UserPoolMfaConfigResp {
	resp := &UserPoolMfaConfigResp{
		MfaConfiguration:              effectiveMfaConfiguration(pool.MfaConfiguration),
		SmsMfaConfiguration:           pool.SmsMfaConfiguration.clone(),
		SoftwareTokenMfaConfiguration: pool.SoftwareTokenMfaConfiguration.clone(),
		EmailMfaConfiguration:         pool.EmailMfaConfiguration.clone(),
	}
	if w := pool.WebAuthnConfiguration; w != nil {
		resp.WebAuthnConfiguration = &webAuthnConfigurationWire{FactorConfiguration: w.FactorConfiguration, RelyingPartyID: w.RelyingPartyID, UserVerification: w.UserVerification}
	}
	return resp
}

//nolint:unused // Kept for auth-flow validation paths that gate PASSWORD factors.
func passwordFirstFactorAllowed(pool *UserPool) bool {
	if pool == nil || pool.Policies == nil || pool.Policies.SignInPolicy == nil || len(pool.Policies.SignInPolicy.AllowedFirstAuthFactors) == 0 {
		return true
	}
	return slices.Contains(pool.Policies.SignInPolicy.AllowedFirstAuthFactors, "PASSWORD")
}

func errNoSupportedFirstAuthFactors() *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidUserPoolConfigurationException", Message: "No supported first authentication factors are enabled for this user pool.", HTTPStatus: 400}
}

// ─── password handlers ────────────────────────────────────────────────────────

func (s *Service) ForgotPasswordTyped(ctx context.Context, req *ClientUserSecretReq) (*ForgotPasswordResp, *protocol.AWSError) {
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.Username, req.SecretHash); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, c.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	code := generateCode()
	u.PasswordResetCode = code
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	// CustomMessage_ForgotPassword is not wired on this Smithy RPC v2
	// duplicate path — see triggers.go's deferred-scope note; ForgotPassword
	// (the classic API, handler_auth.go) has it.
	if emailAddr := u.email(); emailAddr != "" {
		pool, _ := s.loadPool(ctx, c.UserPoolID)
		if pool != nil {
			s.sendPasswordResetEmail(pool, emailAddr, u.Username, code, nil)
		}
	}
	if phone := u.phoneNumber(); phone != "" {
		pool, _ := s.loadPool(ctx, c.UserPoolID)
		if pool != nil {
			s.sendPasswordResetSMS(pool, phone, u.Username, code, nil)
		}
	}
	return &ForgotPasswordResp{
		CodeDeliveryDetails: codeDeliveryDetails{
			DeliveryMedium: "EMAIL",
			Destination:    maskEmailAddress(u.email()),
			AttributeName:  "email",
		},
	}, nil
}

func (s *Service) ConfirmForgotPasswordTyped(ctx context.Context, req *ConfirmForgotPasswordReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	c, aerr := s.requireClientByIDTyped(ctx, req.ClientID)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.checkSecretHashTyped(c, req.Username, req.SecretHash); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, c.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if u.PasswordResetCode == "" {
		return nil, errExpiredCode()
	}
	if u.PasswordResetCode != req.ConfirmationCode {
		return nil, errCodeMismatch()
	}
	pool, err := s.loadPool(ctx, c.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aerr := validatePassword(pool, req.Password); aerr != nil {
		return nil, aerr
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u.PasswordHash = string(hash)
	u.PlaintextPassword = req.Password
	u.PasswordResetCode = ""
	u.TempPassword = ""
	if u.Status == StatusForceChangePassword {
		u.Status = StatusConfirmed
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("user confirmed password reset",
		zap.String("poolId", c.UserPoolID), zap.String("username", req.Username))
	s.publishTyped(ctx, events.CognitoPasswordChanged, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) ChangePasswordTyped(ctx context.Context, req *ChangePasswordReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.PreviousPassword)); err != nil {
		return nil, errNotAuthorized("Incorrect username or password.")
	}
	pool, err := s.loadPool(ctx, t.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if aerr := validatePassword(pool, req.ProposedPassword); aerr != nil {
		return nil, aerr
	}
	hash, err := hashPassword(req.ProposedPassword)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	u.PasswordHash = string(hash)
	u.PlaintextPassword = req.ProposedPassword
	u.TempPassword = ""
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoPasswordChanged, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

// ─── MFA handlers ─────────────────────────────────────────────────────────────

func (s *Service) AssociateSoftwareTokenTyped(ctx context.Context, req *AssociateSoftwareTokenReq) (*AssociateSoftwareTokenResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	u, session, aerr := s.softwareTokenEnrolment(ctx, req.AccessToken, req.Session)
	if aerr != nil {
		return nil, aerr
	}
	secret := generateTOTPSecret()
	u.TOTPSecret = secret
	u.TOTPVerified = false
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("TOTP secret generated",
		zap.String("poolId", u.UserPoolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &AssociateSoftwareTokenResp{SecretCode: secret, Session: session}, nil
}

func (s *Service) VerifySoftwareTokenTyped(ctx context.Context, req *VerifySoftwareTokenReq) (*VerifySoftwareTokenResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	u, session, aerr := s.softwareTokenEnrolment(ctx, req.AccessToken, req.Session)
	if aerr != nil {
		return nil, aerr
	}
	if u.TOTPSecret == "" {
		return nil, &protocol.AWSError{
			Code:       "SoftwareTokenMFANotFoundException",
			Message:    "Software token TOTP MFA not found.",
			HTTPStatus: 400,
		}
	}
	if !verifyTOTP(u.TOTPSecret, req.UserCode, s.clk.Now()) {
		return nil, &protocol.AWSError{
			Code:       "EnableSoftwareTokenMFAException",
			Message:    "Code mismatch and fail enable Software Token MFA.",
			HTTPStatus: 400,
		}
	}
	u.TOTPVerified = true
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("TOTP verified",
		zap.String("poolId", u.UserPoolID), zap.String("username", u.Username))
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &VerifySoftwareTokenResp{Status: "SUCCESS", Session: session}, nil
}

func (s *Service) StartWebAuthnRegistrationTyped(ctx context.Context, req *AccessTokenReq) (*StartWebAuthnRegistrationResp, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	pool, err := s.loadPool(ctx, t.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !allowedFirstAuthFactor(pool, "WEB_AUTHN") {
		return nil, &protocol.AWSError{Code: "WebAuthnNotEnabledException", Message: "WebAuthn is not enabled for this user pool.", HTTPStatus: 400}
	}
	if !webAuthnEnabled(pool) {
		return nil, &protocol.AWSError{Code: "WebAuthnConfigurationMissingException", Message: "WebAuthn relying party configuration is missing.", HTTPStatus: 400}
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	challenge := generateToken()
	setAuthChallengeCode(u, "WEB_AUTHN_REGISTRATION", challenge, s.clk.Now().Add(webAuthnChallengeTTL))
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &StartWebAuthnRegistrationResp{CredentialCreationOptions: webAuthnCredentialCreationOptions(pool, u, challenge)}, nil
}

func (s *Service) CompleteWebAuthnRegistrationTyped(ctx context.Context, req *CompleteWebAuthnRegistrationReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	pool, err := s.loadPool(ctx, t.UserPoolID)
	if err != nil || pool == nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !allowedFirstAuthFactor(pool, "WEB_AUTHN") || !webAuthnEnabled(pool) {
		return nil, &protocol.AWSError{Code: "WebAuthnNotEnabledException", Message: "WebAuthn is not enabled for this user pool.", HTTPStatus: 400}
	}
	credentialID := webAuthnCredentialID(req.Credential)
	if credentialID == "" {
		return nil, &protocol.AWSError{Code: "InvalidParameterException", Message: "Credential.id is required.", HTTPStatus: 400}
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	challenge, ok := authChallengeCode(u, "WEB_AUTHN_REGISTRATION")
	if !ok || (!challenge.ExpiresAt.IsZero() && s.clk.Now().After(challenge.ExpiresAt)) {
		return nil, &protocol.AWSError{Code: "WebAuthnChallengeNotFoundException", Message: "WebAuthn registration challenge not found.", HTTPStatus: 400}
	}
	if len(u.WebAuthnCredentials) >= 20 && !hasWebAuthnCredential(u, credentialID) {
		return nil, &protocol.AWSError{Code: "LimitExceededException", Message: "User has reached the maximum number of WebAuthn credentials.", HTTPStatus: 400}
	}
	if !hasWebAuthnCredential(u, credentialID) {
		u.WebAuthnCredentials = append(u.WebAuthnCredentials, WebAuthnCredential{ID: credentialID, CreatedAt: s.clk.Now()})
	}
	removeAuthChallengeCode(u, "WEB_AUTHN_REGISTRATION")
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

func (s *Service) SetUserMFAPreferenceTyped(ctx context.Context, req *SetUserMFAPreferenceReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := applyMfaPreferences(u, req.SMSMfaSettings, req.SoftwareTokenMfaSettings); aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminSetUserMFAPreferenceTyped(ctx context.Context, req *AdminSetUserMFAPreferenceReq) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := applyMfaPreferences(u, req.SMSMfaSettings, req.SoftwareTokenMfaSettings); aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

// ─── group handlers ───────────────────────────────────────────────────────────

func (s *Service) CreateGroupTyped(ctx context.Context, req *CreateGroupReq) (*CreateGroupResp, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	existing, err := s.loadGroup(ctx, req.UserPoolID, req.GroupName)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if existing != nil {
		return nil, &protocol.AWSError{
			Code:       "GroupExistsException",
			Message:    "A group with the name already exists.",
			HTTPStatus: 400,
		}
	}
	g := &Group{
		GroupName:   req.GroupName,
		UserPoolID:  req.UserPoolID,
		Description: req.Description,
		Precedence:  req.Precedence,
		RoleARN:     req.RoleARN,
		CreatedAt:   s.clk.Now(),
	}
	if err := s.saveGroup(ctx, g); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("group created",
		zap.String("poolId", req.UserPoolID), zap.String("group", req.GroupName))
	s.publishTyped(ctx, events.CognitoGroupCreated, events.ResourcePayload{Name: req.GroupName})
	return &CreateGroupResp{Group: toGroupWire(g)}, nil
}

func (s *Service) GetGroupTyped(ctx context.Context, req *PoolAndGroupReq) (*GetGroupResp, *protocol.AWSError) {
	g, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	return &GetGroupResp{Group: toGroupWire(g)}, nil
}

func (s *Service) DeleteGroupTyped(ctx context.Context, req *PoolAndGroupReq) (*struct{}, *protocol.AWSError) {
	log := s.log.WithRecorder(ctx)
	if _, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName); aerr != nil {
		return nil, aerr
	}
	if err := s.removeGroup(ctx, req.UserPoolID, req.GroupName); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	log.Info("group deleted",
		zap.String("poolId", req.UserPoolID), zap.String("group", req.GroupName))
	s.publishTyped(ctx, events.CognitoGroupDeleted, events.ResourcePayload{Name: req.GroupName})
	return &struct{}{}, nil
}

func (s *Service) UpdateGroupTyped(ctx context.Context, req *UpdateGroupReq) (*struct{}, *protocol.AWSError) {
	g, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName)
	if aerr != nil {
		return nil, aerr
	}
	g.Description = req.Description
	g.Precedence = req.Precedence
	if req.RoleARN != "" {
		g.RoleARN = req.RoleARN
	}
	if err := s.saveGroup(ctx, g); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoGroupUpdated, events.ResourcePayload{Name: req.GroupName})
	return &struct{}{}, nil
}

func (s *Service) ListGroupsTyped(ctx context.Context, req *PoolLimitReq) (*ListGroupsResp, *protocol.AWSError) {
	if _, aerr := s.requirePoolTyped(ctx, req.UserPoolID); aerr != nil {
		return nil, aerr
	}
	groups, err := s.scanGroups(ctx, req.UserPoolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	wires := make([]groupWire, 0, len(groups))
	for _, g := range groups {
		wires = append(wires, toGroupWire(g))
	}
	page, nextToken, aerr := pageGroupWires(wires, req.Limit, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &ListGroupsResp{Groups: page, NextToken: nextToken}, nil
}

func (s *Service) AdminAddUserToGroupTyped(ctx context.Context, req *PoolAndUserGroupReq) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	if !slices.Contains(u.Groups, req.GroupName) {
		u.Groups = append(u.Groups, req.GroupName)
		if err := s.saveUser(ctx, u); err != nil {
			return nil, protocol.Wrap(protocol.ErrInternalError, err)
		}
	}
	s.publishTyped(ctx, events.CognitoGroupMembershipChanged, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminRemoveUserFromGroupTyped(ctx context.Context, req *PoolAndUserGroupReq) (*struct{}, *protocol.AWSError) {
	if _, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName); aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	u.Groups = slices.DeleteFunc(u.Groups, func(g string) bool { return g == req.GroupName })
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoGroupMembershipChanged, events.ResourcePayload{Name: req.Username})
	return &struct{}{}, nil
}

func (s *Service) AdminListGroupsForUserTyped(ctx context.Context, req *PoolAndUserLimitReq) (*ListGroupsResp, *protocol.AWSError) {
	u, aerr := s.requireUserTyped(ctx, req.UserPoolID, req.Username)
	if aerr != nil {
		return nil, aerr
	}
	wires := make([]groupWire, 0, len(u.Groups))
	for _, name := range u.Groups {
		g, err := s.loadGroup(ctx, req.UserPoolID, name)
		if err != nil || g == nil {
			continue
		}
		wires = append(wires, toGroupWire(g))
	}
	page, nextToken, aerr := pageGroupWires(wires, req.Limit, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &ListGroupsResp{Groups: page, NextToken: nextToken}, nil
}

func (s *Service) ListUsersInGroupTyped(ctx context.Context, req *PoolAndGroupLimitReq) (*ListUsersInGroupResp, *protocol.AWSError) {
	if _, aerr := s.requireGroupTyped(ctx, req.UserPoolID, req.GroupName); aerr != nil {
		return nil, aerr
	}
	all, err := s.scanUsers(ctx, req.UserPoolID)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	wires := make([]userWire, 0)
	for _, u := range all {
		if slices.Contains(u.Groups, req.GroupName) {
			wires = append(wires, toUserWire(u))
		}
	}
	page, nextToken, aerr := pageUserWires(wires, req.Limit, req.NextToken)
	if aerr != nil {
		return nil, aerr
	}
	return &ListUsersInGroupResp{Users: page, NextToken: nextToken}, nil
}

// ─── token / user info handlers ───────────────────────────────────────────────

func (s *Service) GetUserTyped(ctx context.Context, req *AccessTokenReq) (*GetUserResp, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	pool, aerr := s.requirePoolTyped(ctx, t.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	uw := toUserWire(u)
	return &GetUserResp{
		Username:            uw.Username,
		UserAttributes:      uw.Attributes,
		PreferredMfaSetting: preferredMfaSetting(pool, u),
		UserMFASettingList:  userMfaFactors(pool, u),
	}, nil
}

func (s *Service) UpdateUserAttributesTyped(ctx context.Context, req *UpdateUserAttributesReq) (*UpdateUserAttributesResp, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	pool, aerr := s.requirePoolTyped(ctx, t.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	details, aerr := s.updateAttributesWithVerification(ctx, pool, u, req.UserAttributes, false)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	if len(details) > 0 {
		return &UpdateUserAttributesResp{CodeDeliveryDetailsList: details}, nil
	}
	return &UpdateUserAttributesResp{}, nil
}

func (s *Service) VerifyUserAttributeTyped(ctx context.Context, req *VerifyUserAttributeReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	pool, aerr := s.requirePoolTyped(ctx, t.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := s.verifyPendingAttribute(ctx, pool, u, req.AttributeName, req.Code); aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

func (s *Service) GetUserAttributeVerificationCodeTyped(ctx context.Context, req *GetUserAttributeVerificationCodeReq) (*GetUserAttributeVerificationCodeResp, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	pool, aerr := s.requirePoolTyped(ctx, t.UserPoolID)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	details, aerr := s.resendAttributeVerificationCode(pool, u, req.AttributeName)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &GetUserAttributeVerificationCodeResp{CodeDeliveryDetails: details}, nil
}

func (s *Service) DeleteUserAttributesTyped(ctx context.Context, req *DeleteUserAttributesReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	u, aerr := s.requireUserTyped(ctx, t.UserPoolID, t.Username)
	if aerr != nil {
		return nil, aerr
	}
	for _, name := range req.UserAttributeNames {
		u.removeAttr(name)
	}
	if err := s.saveUser(ctx, u); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	s.publishTyped(ctx, events.CognitoUserUpdated, events.ResourcePayload{Name: u.Username})
	return &struct{}{}, nil
}

func (s *Service) GlobalSignOutTyped(ctx context.Context, req *AccessTokenReq) (*struct{}, *protocol.AWSError) {
	t, aerr := s.validateAccessTokenTyped(ctx, req.AccessToken)
	if aerr != nil {
		return nil, aerr
	}
	_ = s.removeToken(ctx, t.Value)
	u, err := s.loadUser(ctx, t.UserPoolID, t.Username)
	if err == nil && u != nil {
		now := s.clk.Now()
		u.GlobalSignOutAt = &now
		_ = s.saveUser(ctx, u)
	}
	s.publishTyped(ctx, events.CognitoSignOut, events.ResourcePayload{Name: t.Username})
	return &struct{}{}, nil
}

func (s *Service) RevokeTokenTyped(ctx context.Context, req *RevokeTokenReq) (*struct{}, *protocol.AWSError) {
	_ = s.removeToken(ctx, req.Token)
	s.publishTyped(ctx, events.CognitoSignOut, events.ResourcePayload{Name: req.Token})
	return &struct{}{}, nil
}

// ─── tag types ─────────────────────────────────────────────────────────────────

type TagResourceReq struct {
	ResourceArn string            `json:"ResourceArn" cbor:"ResourceArn"`
	Tags        map[string]string `json:"Tags" cbor:"Tags"`
}

type UntagResourceReq struct {
	ResourceArn string   `json:"ResourceArn" cbor:"ResourceArn"`
	TagKeys     []string `json:"TagKeys" cbor:"TagKeys"`
}

type ListTagsForResourceReq struct {
	ResourceArn string `json:"ResourceArn" cbor:"ResourceArn"`
}

type ListTagsForResourceResp struct {
	Tags map[string]string `json:"Tags" cbor:"Tags"`
}

func (s *Service) TagResourceTyped(ctx context.Context, req *TagResourceReq) (*struct{}, *protocol.AWSError) {
	poolID := extractPoolIDFromARN(req.ResourceArn)

	if aerr := serviceutil.ApplyInlineTags(ctx, poolID, req.Tags, cognitoTagCfg,
		func(ctx context.Context, id string) (*UserPool, *protocol.AWSError) {
			return s.requirePoolTyped(ctx, id)
		},
		func(ctx context.Context, pool *UserPool) *protocol.AWSError {
			if err := s.savePool(ctx, pool); err != nil {
				return protocol.Wrap(protocol.ErrInternalError, err)
			}
			return nil
		},
	); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) UntagResourceTyped(ctx context.Context, req *UntagResourceReq) (*struct{}, *protocol.AWSError) {
	poolID := extractPoolIDFromARN(req.ResourceArn)

	if aerr := serviceutil.RemoveInlineTags(ctx, poolID, req.TagKeys,
		func(ctx context.Context, id string) (*UserPool, *protocol.AWSError) {
			return s.requirePoolTyped(ctx, id)
		},
		func(ctx context.Context, pool *UserPool) *protocol.AWSError {
			if err := s.savePool(ctx, pool); err != nil {
				return protocol.Wrap(protocol.ErrInternalError, err)
			}
			return nil
		},
	); aerr != nil {
		return nil, aerr
	}
	return &struct{}{}, nil
}

func (s *Service) ListTagsForResourceTyped(ctx context.Context, req *ListTagsForResourceReq) (*ListTagsForResourceResp, *protocol.AWSError) {
	poolID := extractPoolIDFromARN(req.ResourceArn)

	tags, aerr := serviceutil.ListInlineTags(ctx, poolID,
		func(ctx context.Context, id string) (*UserPool, *protocol.AWSError) {
			return s.requirePoolTyped(ctx, id)
		},
	)
	if aerr != nil {
		return nil, aerr
	}
	return &ListTagsForResourceResp{Tags: tags}, nil
}

func extractPoolIDFromARN(arn string) string {
	idx := strings.LastIndex(arn, "userpool/")
	if idx < 0 {
		return ""
	}
	return arn[idx+len("userpool/"):]
}
