package cognito

import "time"

// UserStatus represents the account lifecycle state of a Cognito user.
type UserStatus string

const (
	StatusUnconfirmed         UserStatus = "UNCONFIRMED"
	StatusConfirmed           UserStatus = "CONFIRMED"
	StatusForceChangePassword UserStatus = "FORCE_CHANGE_PASSWORD"
	StatusDisabled            UserStatus = "DISABLED"
)

// UserAttribute is a name/value pair attached to a Cognito user.
type UserAttribute struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

func (p *UserPool) GetTags() map[string]string  { return p.Tags }
func (p *UserPool) SetTags(t map[string]string) { p.Tags = t }

// UserPool is the stored representation of a Cognito User Pool.
type UserPool struct {
	ID        string    `json:"Id"`
	Name      string    `json:"Name"`
	ARN       string    `json:"Arn"`
	CreatedAt time.Time `json:"CreatedAt"`

	// Domain is the prefix used for the managed login / hosted UI endpoints.
	// In real AWS this becomes {domain}.auth.{region}.amazoncognito.com;
	// in the emulator it maps to a path prefix on the same host.
	Domain string `json:"Domain,omitempty"`

	// UserPoolTier is the AWS feature plan. AWS defaults omitted values to ESSENTIALS.
	UserPoolTier string `json:"UserPoolTier,omitempty"`

	// VerificationMessageTemplate controls the email/SMS content sent to users
	// during sign-up confirmation and attribute verification.
	VerificationMessageTemplate *VerificationMessageTemplate `json:"VerificationMessageTemplate,omitempty"`

	// AdminCreateUserConfig controls admin-created user invitation
	// messages and related settings.
	AdminCreateUserConfig *AdminCreateUserConfig `json:"AdminCreateUserConfig,omitempty"`

	// EmailConfiguration controls the email sending method and SES settings.
	EmailConfiguration *EmailConfiguration `json:"EmailConfiguration,omitempty"`

	// ManagedLoginBranding controls the managed login page appearance.
	ManagedLoginBranding *ManagedLoginBranding `json:"ManagedLoginBranding,omitempty"`

	// UserAttributeUpdateSettings controls whether email/phone changes remain
	// pending until the user verifies the new value.
	UserAttributeUpdateSettings *UserAttributeUpdateSettings `json:"UserAttributeUpdateSettings,omitempty"`

	// AutoVerifiedAttributes lists the attributes the pool verifies for itself
	// by sending the user a code. Valid values: "email", "phone_number".
	AutoVerifiedAttributes []string `json:"AutoVerifiedAttributes,omitempty"`

	// MFA and WebAuthn configuration configured through SetUserPoolMfaConfig.
	// SetUserPoolMfaConfig replaces MfaConfiguration and all three factor
	// configurations as a unit: a member the request omits is cleared here, not
	// carried over. WebAuthnConfiguration is the exception and is merged.
	MfaConfiguration              string                  `json:"MfaConfiguration,omitempty"`
	SmsMfaConfiguration           *SmsMfaConfig           `json:"SmsMfaConfiguration,omitempty"`
	SoftwareTokenMfaConfiguration *SoftwareTokenMfaConfig `json:"SoftwareTokenMfaConfiguration,omitempty"`
	EmailMfaConfiguration         *EmailMfaConfig         `json:"EmailMfaConfiguration,omitempty"`
	WebAuthnConfiguration         *WebAuthnConfiguration  `json:"WebAuthnConfiguration,omitempty"`
	DeviceConfiguration           *DeviceConfiguration    `json:"DeviceConfiguration,omitempty"`

	// UsernameAttributes lists the user pool attributes that can be used as
	// the username when signing in. Valid values: "email", "phone_number".
	// When empty, users sign in with their literal username string.
	UsernameAttributes []string `json:"UsernameAttributes,omitempty"`

	// AliasAttributes lists verified attributes that can be used as aliases for
	// a stable username. Valid values: "email", "phone_number", "preferred_username".
	AliasAttributes []string `json:"AliasAttributes,omitempty"`

	// Policies holds pool-level policy settings, including PasswordPolicy.
	Policies *UserPoolPolicies `json:"Policies,omitempty"`

	// Tags are resource tags on the user pool.
	Tags map[string]string `json:"Tags,omitempty"`

	// AccountRecoverySetting configures the priority order of account-recovery
	// mechanisms (verified_email, verified_phone_number, admin_only).
	AccountRecoverySetting *AccountRecoverySetting `json:"AccountRecoverySetting,omitempty"`

	// SmsConfiguration configures the IAM role Cognito assumes to send SMS
	// messages through SNS.
	SmsConfiguration *SmsConfiguration `json:"SmsConfiguration,omitempty"`

	// SmsAuthenticationMessage is the SMS body template for SMS_MFA challenges.
	// Must contain {####}.
	SmsAuthenticationMessage string `json:"SmsAuthenticationMessage,omitempty"`

	// SmsVerificationMessage is the legacy top-level SMS verification message
	// template. Must contain {####}. Superseded by
	// VerificationMessageTemplate.SmsMessage but still accepted and echoed
	// separately by real Cognito.
	SmsVerificationMessage string `json:"SmsVerificationMessage,omitempty"`

	// EmailVerificationMessage is the legacy top-level email verification
	// message template. Must contain {####}. Superseded by
	// VerificationMessageTemplate.EmailMessage but still accepted and echoed
	// separately by real Cognito.
	EmailVerificationMessage string `json:"EmailVerificationMessage,omitempty"`

	// EmailVerificationSubject is the legacy top-level email verification
	// subject line. Superseded by VerificationMessageTemplate.EmailSubject but
	// still accepted and echoed separately by real Cognito.
	EmailVerificationSubject string `json:"EmailVerificationSubject,omitempty"`

	// LambdaConfig lists the Lambda triggers configured for this pool. Stored
	// and echoed for template fidelity; the emulator does not invoke any of
	// these triggers during sign-up, confirmation, or auth flows (see
	// capabilities_dev.go and docs/dev/compatibility/services/cognito.yaml).
	LambdaConfig *LambdaConfig `json:"LambdaConfig,omitempty"`
}

// AccountRecoverySetting configures the priority order of account-recovery
// mechanisms available to users of the pool.
type AccountRecoverySetting struct {
	RecoveryMechanisms []RecoveryOption `json:"RecoveryMechanisms,omitempty"`
}

// RecoveryOption is a single account-recovery mechanism and its priority.
// Valid Name values: "verified_email", "verified_phone_number", "admin_only".
type RecoveryOption struct {
	Name     string `json:"Name"`
	Priority int    `json:"Priority"`
}

// SmsConfiguration configures the IAM role Cognito assumes to send SMS
// messages through SNS.
type SmsConfiguration struct {
	SnsCallerArn string `json:"SnsCallerArn,omitempty"`
	ExternalId   string `json:"ExternalId,omitempty"`
	SnsRegion    string `json:"SnsRegion,omitempty"`
}

// clone returns a deep copy, or nil for a nil receiver.
func (c *SmsConfiguration) clone() *SmsConfiguration {
	if c == nil {
		return nil
	}
	dup := *c
	return &dup
}

// SmsMfaConfig is the AWS SmsMfaConfigType: on AWS, the message template used
// for SMS_MFA challenges and the SNS sending configuration behind it. Its
// presence is what makes SMS an available MFA factor — SmsMfaConfigType has no
// Enabled flag. Overcast stores and echoes it but issues no SMS_MFA challenge,
// so the template is never expanded.
type SmsMfaConfig struct {
	// SmsAuthenticationMessage must contain {####}, the code placeholder.
	SmsAuthenticationMessage string            `json:"SmsAuthenticationMessage,omitempty"`
	SmsConfiguration         *SmsConfiguration `json:"SmsConfiguration,omitempty"`
}

// clone returns a deep copy, or nil for a nil receiver.
func (c *SmsMfaConfig) clone() *SmsMfaConfig {
	if c == nil {
		return nil
	}
	return &SmsMfaConfig{
		SmsAuthenticationMessage: c.SmsAuthenticationMessage,
		SmsConfiguration:         c.SmsConfiguration.clone(),
	}
}

// SoftwareTokenMfaConfig is the AWS SoftwareTokenMfaConfigType: whether TOTP
// authenticator apps are an available MFA factor.
type SoftwareTokenMfaConfig struct {
	Enabled bool `json:"Enabled"`
}

// clone returns a deep copy, or nil for a nil receiver.
func (c *SoftwareTokenMfaConfig) clone() *SoftwareTokenMfaConfig {
	if c == nil {
		return nil
	}
	dup := *c
	return &dup
}

// EmailMfaConfig is the AWS EmailMfaConfigType: on AWS, the subject and body of
// the email message sent for email MFA, which requires the Essentials feature
// plan or higher. Overcast stores and echoes it but issues no email MFA
// challenge, so the template is never expanded.
type EmailMfaConfig struct {
	// Message must contain {####}, the code placeholder.
	Message string `json:"Message,omitempty"`
	Subject string `json:"Subject,omitempty"`
}

// clone returns a deep copy, or nil for a nil receiver.
func (c *EmailMfaConfig) clone() *EmailMfaConfig {
	if c == nil {
		return nil
	}
	dup := *c
	return &dup
}

// LambdaConfig lists the Lambda trigger ARNs configured for a user pool. Every
// field is stored and returned by CreateUserPool/DescribeUserPool/
// UpdateUserPool for CDK/CloudFormation template fidelity, but none of these
// triggers are invoked — see capabilities_dev.go.
type LambdaConfig struct {
	PreSignUp                   string `json:"PreSignUp,omitempty"`
	CustomMessage               string `json:"CustomMessage,omitempty"`
	PostConfirmation            string `json:"PostConfirmation,omitempty"`
	PreAuthentication           string `json:"PreAuthentication,omitempty"`
	PostAuthentication          string `json:"PostAuthentication,omitempty"`
	DefineAuthChallenge         string `json:"DefineAuthChallenge,omitempty"`
	CreateAuthChallenge         string `json:"CreateAuthChallenge,omitempty"`
	VerifyAuthChallengeResponse string `json:"VerifyAuthChallengeResponse,omitempty"`
	PreTokenGeneration          string `json:"PreTokenGeneration,omitempty"`
	UserMigration               string `json:"UserMigration,omitempty"`
	KMSKeyID                    string `json:"KMSKeyID,omitempty"`
}

// UserPoolPolicies holds the password and other policy settings for a user pool.
type UserPoolPolicies struct {
	PasswordPolicy *PasswordPolicy `json:"PasswordPolicy,omitempty"`
	SignInPolicy   *SignInPolicy   `json:"SignInPolicy,omitempty"`
}

type SignInPolicy struct {
	AllowedFirstAuthFactors []string `json:"AllowedFirstAuthFactors,omitempty"`
}

type UserAttributeUpdateSettings struct {
	AttributesRequireVerificationBeforeUpdate []string `json:"AttributesRequireVerificationBeforeUpdate,omitempty"`
}

type WebAuthnConfiguration struct {
	FactorConfiguration string `json:"FactorConfiguration,omitempty"`
	RelyingPartyID      string `json:"RelyingPartyId,omitempty"`
	UserVerification    string `json:"UserVerification,omitempty"`
}

type DeviceConfiguration struct {
	ChallengeRequiredOnNewDevice     bool `json:"ChallengeRequiredOnNewDevice,omitempty"`
	DeviceOnlyRememberedOnUserPrompt bool `json:"DeviceOnlyRememberedOnUserPrompt,omitempty"`
}

// PasswordPolicy enforces password strength requirements on user password flows,
// including AdminCreateUser temporary passwords.
type PasswordPolicy struct {
	// MinimumLength is the minimum password length. Default: 8.
	MinimumLength int `json:"MinimumLength,omitempty"`

	// RequireUppercase requires at least one uppercase letter.
	RequireUppercase bool `json:"RequireUppercase,omitempty"`

	// RequireLowercase requires at least one lowercase letter.
	RequireLowercase bool `json:"RequireLowercase,omitempty"`

	// RequireNumbers requires at least one digit.
	RequireNumbers bool `json:"RequireNumbers,omitempty"`

	// RequireSymbols requires at least one symbol character.
	RequireSymbols bool `json:"RequireSymbols,omitempty"`

	// TemporaryPasswordValidityDays controls how long a temporary password
	// issued by AdminCreateUser remains valid. Default: 7.
	TemporaryPasswordValidityDays int `json:"TemporaryPasswordValidityDays,omitempty"`
}

// VerificationMessageTemplate configures the verification messages sent during
// sign-up. Template variables: {username}, {####} (code), {##Verify Email##} (link).
type VerificationMessageTemplate struct {
	// DefaultEmailOption is "CONFIRM_WITH_CODE" (default) or "CONFIRM_WITH_LINK".
	DefaultEmailOption string `json:"DefaultEmailOption,omitempty"`

	// EmailMessage is the email body template for code-based verification.
	// Must contain {####}. Plain text.
	EmailMessage string `json:"EmailMessage,omitempty"`

	// EmailMessageByLink is the email body template for link-based verification.
	// Must contain {##Verify Email##}.
	EmailMessageByLink string `json:"EmailMessageByLink,omitempty"`

	// EmailSubject is the subject line for code-based verification emails.
	EmailSubject string `json:"EmailSubject,omitempty"`

	// EmailSubjectByLink is the subject line for link-based verification emails.
	EmailSubjectByLink string `json:"EmailSubjectByLink,omitempty"`

	// SmsMessage is the SMS body template. Must contain {####}.
	SmsMessage string `json:"SmsMessage,omitempty"`
}

// AdminCreateUserConfig controls settings for admin-created users.
type AdminCreateUserConfig struct {
	// AllowAdminCreateUserOnly when true prevents self-service sign-up.
	AllowAdminCreateUserOnly bool `json:"AllowAdminCreateUserOnly,omitempty"`

	// UnusedAccountValidityDays is how long a temp password stays valid (default 7).
	UnusedAccountValidityDays int `json:"UnusedAccountValidityDays,omitempty"`

	// InviteMessageTemplate controls the welcome message sent to admin-created users.
	// Template variables: {username}, {####} (temporary password).
	InviteMessageTemplate *InviteMessageTemplate `json:"InviteMessageTemplate,omitempty"`
}

// InviteMessageTemplate configures the invitation email/SMS for admin-created users.
type InviteMessageTemplate struct {
	// EmailMessage is the email body. Must contain {username} and {####}.
	EmailMessage string `json:"EmailMessage,omitempty"`

	// EmailSubject is the email subject line.
	EmailSubject string `json:"EmailSubject,omitempty"`

	// SMSMessage is the SMS body. Must contain {username} and {####}.
	SMSMessage string `json:"SMSMessage,omitempty"`
}

// EmailConfiguration controls the email delivery method.
type EmailConfiguration struct {
	// EmailSendingAccount is "COGNITO_DEFAULT" or "DEVELOPER" (SES).
	EmailSendingAccount string `json:"EmailSendingAccount,omitempty"`

	// SourceArn is the SES verified email ARN (used when EmailSendingAccount=DEVELOPER).
	SourceArn string `json:"SourceArn,omitempty"`

	// From is the sender email address.
	From string `json:"From,omitempty"`

	// ReplyToEmailAddress is the reply-to address.
	ReplyToEmailAddress string `json:"ReplyToEmailAddress,omitempty"`
}

// TokenValidityUnitsType specifies the time unit for each token type.
type TokenValidityUnitsType struct {
	AccessToken  string `json:"AccessToken"`
	IdToken      string `json:"IdToken"`
	RefreshToken string `json:"RefreshToken"`
}

// UserPoolClient is an app client registered to a user pool.
type UserPoolClient struct {
	ClientID   string    `json:"ClientId"`
	ClientName string    `json:"ClientName"`
	UserPoolID string    `json:"UserPoolId"`
	CreatedAt  time.Time `json:"CreatedAt"`
	// ClientSecret is non-empty only when the client was created with GenerateSecret=true.
	// It is used to validate the SECRET_HASH parameter on auth calls.
	ClientSecret string `json:"ClientSecret,omitempty"`

	// Token validity configuration — matches AWS Cognito per-client settings.
	AccessTokenValidity  int                     `json:"AccessTokenValidity"`
	IdTokenValidity      int                     `json:"IdTokenValidity"`
	RefreshTokenValidity int                     `json:"RefreshTokenValidity"`
	TokenValidityUnits   *TokenValidityUnitsType `json:"TokenValidityUnits,omitempty"`

	// OAuth / managed login configuration — matches AWS Cognito app client settings.
	CallbackURLs                    []string `json:"CallbackURLs,omitempty"`
	LogoutURLs                      []string `json:"LogoutURLs,omitempty"`
	AllowedOAuthFlows               []string `json:"AllowedOAuthFlows,omitempty"`
	AllowedOAuthScopes              []string `json:"AllowedOAuthScopes,omitempty"`
	AllowedOAuthFlowsUserPoolClient bool     `json:"AllowedOAuthFlowsUserPoolClient"`
	ExplicitAuthFlows               []string `json:"ExplicitAuthFlows,omitempty"`
	SupportedIdentityProviders      []string `json:"SupportedIdentityProviders,omitempty"`

	// PreventUserExistenceErrors controls whether auth/forgot-password errors
	// reveal that a username does not exist. "ENABLED" or "LEGACY".
	PreventUserExistenceErrors string `json:"PreventUserExistenceErrors,omitempty"`

	// ReadAttributes/WriteAttributes restrict which user attributes this
	// client can read/write via GetUser and Update*Attributes calls.
	ReadAttributes  []string `json:"ReadAttributes,omitempty"`
	WriteAttributes []string `json:"WriteAttributes,omitempty"`
}

// User is the stored representation of a Cognito user within a pool.
type User struct {
	Username                string                   `json:"Username"`
	Sub                     string                   `json:"Sub"`
	UserPoolID              string                   `json:"UserPoolId"`
	CreatedAt               time.Time                `json:"UserCreateDate"`
	ModifiedAt              time.Time                `json:"UserLastModifiedDate"`
	Status                  UserStatus               `json:"UserStatus"`
	Enabled                 bool                     `json:"Enabled"`
	PasswordHash            string                   `json:"PasswordHash,omitempty"`
	TempPassword            string                   `json:"TempPassword,omitempty"`
	Attributes              []UserAttribute          `json:"Attributes"`
	ConfirmationCode        string                   `json:"ConfirmationCode,omitempty"`
	PasswordResetCode       string                   `json:"PasswordResetCode,omitempty"`
	PendingAttributeUpdates []PendingAttributeUpdate `json:"PendingAttributeUpdates,omitempty"`
	AuthChallengeCodes      []AuthChallengeCode      `json:"AuthChallengeCodes,omitempty"`
	WebAuthnCredentials     []WebAuthnCredential     `json:"WebAuthnCredentials,omitempty"`
	Devices                 []UserDevice             `json:"Devices,omitempty"`

	// Groups is the list of group names this user belongs to.
	Groups []string `json:"Groups,omitempty"`

	// PlaintextPassword stores the password in cleartext alongside the bcrypt
	// hash. This is an emulator-only convenience — it lets the web UI display
	// and copy user passwords for testing managed login flows.
	PlaintextPassword string `json:"PlaintextPassword,omitempty"`

	// TOTP / MFA fields. MFAEnabled is the software-token (TOTP) preference and
	// SMSMfaEnabled the SMS one, both set through SetUserMFAPreference;
	// PreferredMfa names the factor to challenge with when several are active
	// and carries a ChallengeNameType value, or "".
	TOTPSecret    string `json:"TOTPSecret,omitempty"`
	TOTPVerified  bool   `json:"TOTPVerified,omitempty"`
	MFAEnabled    bool   `json:"MFAEnabled,omitempty"`
	SMSMfaEnabled bool   `json:"SMSMfaEnabled,omitempty"`
	PreferredMfa  string `json:"PreferredMfa,omitempty"`

	// GlobalSignOutAt is set when GlobalSignOut is called; any token with
	// iat before this time is considered revoked.
	GlobalSignOutAt *time.Time `json:"GlobalSignOutAt,omitempty"`
}

type PendingAttributeUpdate struct {
	Name      string    `json:"Name"`
	Value     string    `json:"Value"`
	Code      string    `json:"Code"`
	ExpiresAt time.Time `json:"ExpiresAt,omitempty"`
}

type AuthChallengeCode struct {
	ChallengeName string    `json:"ChallengeName"`
	Code          string    `json:"Code"`
	ExpiresAt     time.Time `json:"ExpiresAt,omitempty"`
}

type UserDevice struct {
	DeviceKey                   string    `json:"DeviceKey"`
	DeviceGroupKey              string    `json:"DeviceGroupKey,omitempty"`
	DeviceName                  string    `json:"DeviceName,omitempty"`
	PasswordVerifier            string    `json:"PasswordVerifier,omitempty"`
	Salt                        string    `json:"Salt,omitempty"`
	DeviceRememberedStatus      string    `json:"DeviceRememberedStatus,omitempty"`
	DeviceCreateDate            time.Time `json:"DeviceCreateDate,omitempty"`
	DeviceLastModifiedDate      time.Time `json:"DeviceLastModifiedDate,omitempty"`
	DeviceLastAuthenticatedDate time.Time `json:"DeviceLastAuthenticatedDate,omitempty"`
}

type WebAuthnCredential struct {
	ID        string    `json:"Id"`
	CreatedAt time.Time `json:"CreatedAt"`
}

// getAttr returns the value of a named attribute, or "" if absent.
func (u *User) getAttr(name string) string {
	for _, a := range u.Attributes {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

// setAttr updates an existing attribute or appends a new one.
func (u *User) setAttr(name, value string) {
	for i, a := range u.Attributes {
		if a.Name == name {
			u.Attributes[i].Value = value
			return
		}
	}
	u.Attributes = append(u.Attributes, UserAttribute{Name: name, Value: value})
}

// removeAttr deletes a named attribute. Returns true if the attribute existed.
func (u *User) removeAttr(name string) bool {
	for i, a := range u.Attributes {
		if a.Name == name {
			u.Attributes = append(u.Attributes[:i], u.Attributes[i+1:]...)
			return true
		}
	}
	return false
}

// email returns the user's email attribute value, or "".
func (u *User) email() string       { return u.getAttr("email") }
func (u *User) phoneNumber() string { return u.getAttr("phone_number") }

// Token is a persisted token record used for revocation tracking.
// For access/id tokens this is keyed by JTI; for refresh/session tokens by the
// opaque hex value itself.
type Token struct {
	Value      string    `json:"Value"` // JTI for JWTs, hex value for opaque tokens
	Type       string    `json:"Type"`  // "access", "id", "refresh", "session", "mfa"
	Username   string    `json:"Username"`
	UserPoolID string    `json:"UserPoolId"`
	DeviceKey  string    `json:"DeviceKey,omitempty"`
	CreatedAt  time.Time `json:"CreatedAt"`
	ExpiresAt  time.Time `json:"ExpiresAt"`
	OriginJTI  string    `json:"OriginJTI,omitempty"` // access token JTI from the original auth event
}

// ─── wire / SDK-facing types ──────────────────────────────────────────────────

// userPoolWire is the AWS SDK wire format for a UserPool.
type userPoolWire struct {
	ID                          string                           `json:"Id"`
	Name                        string                           `json:"Name"`
	Arn                         string                           `json:"Arn"`
	CreationDate                float64                          `json:"CreationDate"`
	LastModifiedDate            float64                          `json:"LastModifiedDate"`
	EstimatedNumberOfUsers      int                              `json:"EstimatedNumberOfUsers"`
	Domain                      string                           `json:"Domain,omitempty"`
	UserPoolTier                string                           `json:"UserPoolTier,omitempty"`
	UsernameAttributes          []string                         `json:"UsernameAttributes,omitempty"`
	AliasAttributes             []string                         `json:"AliasAttributes,omitempty"`
	Policies                    *userPoolPoliciesWire            `json:"Policies,omitempty"`
	VerificationMessageTemplate *verificationMessageTemplateWire `json:"VerificationMessageTemplate,omitempty"`
	AdminCreateUserConfig       *adminCreateUserConfigWire       `json:"AdminCreateUserConfig,omitempty"`
	EmailConfiguration          *emailConfigurationWire          `json:"EmailConfiguration,omitempty"`
	UserAttributeUpdateSettings *userAttributeUpdateSettingsWire `json:"UserAttributeUpdateSettings,omitempty"`
	AutoVerifiedAttributes      []string                         `json:"AutoVerifiedAttributes,omitempty"`
	DeviceConfiguration         *DeviceConfiguration             `json:"DeviceConfiguration,omitempty"`
	AccountRecoverySetting      *AccountRecoverySetting          `json:"AccountRecoverySetting,omitempty"`
	SmsConfiguration            *SmsConfiguration                `json:"SmsConfiguration,omitempty"`
	SmsAuthenticationMessage    string                           `json:"SmsAuthenticationMessage,omitempty"`
	SmsVerificationMessage      string                           `json:"SmsVerificationMessage,omitempty"`
	EmailVerificationMessage    string                           `json:"EmailVerificationMessage,omitempty"`
	EmailVerificationSubject    string                           `json:"EmailVerificationSubject,omitempty"`
	LambdaConfig                *LambdaConfig                    `json:"LambdaConfig,omitempty"`
}

type webAuthnConfigurationWire struct {
	FactorConfiguration string `json:"FactorConfiguration,omitempty"`
	RelyingPartyID      string `json:"RelyingPartyId,omitempty"`
	UserVerification    string `json:"UserVerification,omitempty"`
}

type userAttributeUpdateSettingsWire struct {
	AttributesRequireVerificationBeforeUpdate []string `json:"AttributesRequireVerificationBeforeUpdate,omitempty"`
}

// userPoolPoliciesWire is the AWS SDK wire format for UserPool.Policies.
type userPoolPoliciesWire struct {
	PasswordPolicy *passwordPolicyWire `json:"PasswordPolicy,omitempty"`
	SignInPolicy   *signInPolicyWire   `json:"SignInPolicy,omitempty"`
}

type signInPolicyWire struct {
	AllowedFirstAuthFactors []string `json:"AllowedFirstAuthFactors,omitempty"`
}

// passwordPolicyWire is the AWS SDK wire format for PasswordPolicy.
type passwordPolicyWire struct {
	MinimumLength                 int  `json:"MinimumLength"`
	RequireUppercase              bool `json:"RequireUppercase"`
	RequireLowercase              bool `json:"RequireLowercase"`
	RequireNumbers                bool `json:"RequireNumbers"`
	RequireSymbols                bool `json:"RequireSymbols"`
	TemporaryPasswordValidityDays int  `json:"TemporaryPasswordValidityDays,omitempty"`
}

// verificationMessageTemplateWire is the SDK wire format.
type verificationMessageTemplateWire struct {
	DefaultEmailOption string `json:"DefaultEmailOption,omitempty"`
	EmailMessage       string `json:"EmailMessage,omitempty"`
	EmailMessageByLink string `json:"EmailMessageByLink,omitempty"`
	EmailSubject       string `json:"EmailSubject,omitempty"`
	EmailSubjectByLink string `json:"EmailSubjectByLink,omitempty"`
	SmsMessage         string `json:"SmsMessage,omitempty"`
}

// adminCreateUserConfigWire is the SDK wire format.
type adminCreateUserConfigWire struct {
	AllowAdminCreateUserOnly  bool                       `json:"AllowAdminCreateUserOnly"`
	UnusedAccountValidityDays int                        `json:"UnusedAccountValidityDays"`
	InviteMessageTemplate     *inviteMessageTemplateWire `json:"InviteMessageTemplate,omitempty"`
}

// inviteMessageTemplateWire is the SDK wire format.
type inviteMessageTemplateWire struct {
	EmailMessage string `json:"EmailMessage,omitempty"`
	EmailSubject string `json:"EmailSubject,omitempty"`
	SMSMessage   string `json:"SMSMessage,omitempty"`
}

// emailConfigurationWire is the SDK wire format.
type emailConfigurationWire struct {
	EmailSendingAccount string `json:"EmailSendingAccount,omitempty"`
	SourceArn           string `json:"SourceArn,omitempty"`
	From                string `json:"From,omitempty"`
	ReplyToEmailAddress string `json:"ReplyToEmailAddress,omitempty"`
}

func toUserPoolWire(p *UserPool) userPoolWire {
	epoch := float64(p.CreatedAt.Unix())
	w := userPoolWire{
		ID:                 p.ID,
		Name:               p.Name,
		Arn:                p.ARN,
		CreationDate:       epoch,
		LastModifiedDate:   epoch,
		Domain:             p.Domain,
		UserPoolTier:       effectiveUserPoolTier(p),
		UsernameAttributes: p.UsernameAttributes,
		AliasAttributes:    p.AliasAttributes,
	}
	if pol := p.Policies; pol != nil {
		w.Policies = &userPoolPoliciesWire{}
		if pp := pol.PasswordPolicy; pp != nil {
			w.Policies.PasswordPolicy = &passwordPolicyWire{
				MinimumLength:                 pp.MinimumLength,
				RequireUppercase:              pp.RequireUppercase,
				RequireLowercase:              pp.RequireLowercase,
				RequireNumbers:                pp.RequireNumbers,
				RequireSymbols:                pp.RequireSymbols,
				TemporaryPasswordValidityDays: pp.TemporaryPasswordValidityDays,
			}
		}
		if sp := pol.SignInPolicy; sp != nil {
			w.Policies.SignInPolicy = &signInPolicyWire{AllowedFirstAuthFactors: sp.AllowedFirstAuthFactors}
		}
		if w.Policies.PasswordPolicy == nil && w.Policies.SignInPolicy == nil {
			w.Policies = nil
		}
	}
	if v := p.VerificationMessageTemplate; v != nil {
		w.VerificationMessageTemplate = &verificationMessageTemplateWire{
			DefaultEmailOption: v.DefaultEmailOption,
			EmailMessage:       v.EmailMessage,
			EmailMessageByLink: v.EmailMessageByLink,
			EmailSubject:       v.EmailSubject,
			EmailSubjectByLink: v.EmailSubjectByLink,
			SmsMessage:         v.SmsMessage,
		}
	}
	if a := p.AdminCreateUserConfig; a != nil {
		ac := &adminCreateUserConfigWire{
			AllowAdminCreateUserOnly:  a.AllowAdminCreateUserOnly,
			UnusedAccountValidityDays: a.UnusedAccountValidityDays,
		}
		if t := a.InviteMessageTemplate; t != nil {
			ac.InviteMessageTemplate = &inviteMessageTemplateWire{
				EmailMessage: t.EmailMessage,
				EmailSubject: t.EmailSubject,
				SMSMessage:   t.SMSMessage,
			}
		}
		w.AdminCreateUserConfig = ac
	}
	if e := p.EmailConfiguration; e != nil {
		w.EmailConfiguration = &emailConfigurationWire{
			EmailSendingAccount: e.EmailSendingAccount,
			SourceArn:           e.SourceArn,
			From:                e.From,
			ReplyToEmailAddress: e.ReplyToEmailAddress,
		}
	}
	if s := p.UserAttributeUpdateSettings; s != nil {
		w.UserAttributeUpdateSettings = &userAttributeUpdateSettingsWire{AttributesRequireVerificationBeforeUpdate: s.AttributesRequireVerificationBeforeUpdate}
	}
	w.AutoVerifiedAttributes = p.AutoVerifiedAttributes
	if d := p.DeviceConfiguration; d != nil {
		w.DeviceConfiguration = d
	}
	w.AccountRecoverySetting = p.AccountRecoverySetting
	w.SmsConfiguration = p.SmsConfiguration
	w.SmsAuthenticationMessage = p.SmsAuthenticationMessage
	w.SmsVerificationMessage = p.SmsVerificationMessage
	w.EmailVerificationMessage = p.EmailVerificationMessage
	w.EmailVerificationSubject = p.EmailVerificationSubject
	w.LambdaConfig = p.LambdaConfig
	return w
}

// userWire is the AWS SDK wire format for a User.
type userWire struct {
	Username             string          `json:"Username"`
	UserCreateDate       float64         `json:"UserCreateDate"`
	UserLastModifiedDate float64         `json:"UserLastModifiedDate"`
	Enabled              bool            `json:"Enabled"`
	UserStatus           string          `json:"UserStatus"`
	Attributes           []UserAttribute `json:"Attributes"`
}

func toUserWire(u *User) userWire {
	attrs := make([]UserAttribute, 0, len(u.Attributes)+1)
	hasSub := false
	for _, a := range u.Attributes {
		if a.Name == "sub" {
			hasSub = true
		}
		attrs = append(attrs, a)
	}
	if !hasSub && u.Sub != "" {
		attrs = append(attrs, UserAttribute{Name: "sub", Value: u.Sub})
	}
	modAt := u.ModifiedAt
	if modAt.IsZero() {
		modAt = u.CreatedAt
	}
	return userWire{
		Username:             u.Username,
		UserCreateDate:       float64(u.CreatedAt.Unix()),
		UserLastModifiedDate: float64(modAt.Unix()),
		Enabled:              u.Enabled,
		UserStatus:           string(u.Status),
		Attributes:           attrs,
	}
}

// clientWire is the AWS SDK wire format for a UserPoolClient.
type clientWire struct {
	ClientID                        string                  `json:"ClientId"`
	ClientName                      string                  `json:"ClientName"`
	UserPoolId                      string                  `json:"UserPoolId"`
	CreationDate                    float64                 `json:"CreationDate"`
	ClientSecret                    string                  `json:"ClientSecret,omitempty"`
	AccessTokenValidity             int                     `json:"AccessTokenValidity"`
	IdTokenValidity                 int                     `json:"IdTokenValidity"`
	RefreshTokenValidity            int                     `json:"RefreshTokenValidity"`
	TokenValidityUnits              *TokenValidityUnitsType `json:"TokenValidityUnits,omitempty"`
	CallbackURLs                    []string                `json:"CallbackURLs,omitempty"`
	LogoutURLs                      []string                `json:"LogoutURLs,omitempty"`
	AllowedOAuthFlows               []string                `json:"AllowedOAuthFlows,omitempty"`
	AllowedOAuthScopes              []string                `json:"AllowedOAuthScopes,omitempty"`
	AllowedOAuthFlowsUserPoolClient bool                    `json:"AllowedOAuthFlowsUserPoolClient"`
	ExplicitAuthFlows               []string                `json:"ExplicitAuthFlows,omitempty"`
	SupportedIdentityProviders      []string                `json:"SupportedIdentityProviders,omitempty"`
	PreventUserExistenceErrors      string                  `json:"PreventUserExistenceErrors,omitempty"`
	ReadAttributes                  []string                `json:"ReadAttributes,omitempty"`
	WriteAttributes                 []string                `json:"WriteAttributes,omitempty"`
}

func toClientWire(c *UserPoolClient) clientWire {
	return clientWire{
		ClientID:                        c.ClientID,
		ClientName:                      c.ClientName,
		UserPoolId:                      c.UserPoolID,
		CreationDate:                    float64(c.CreatedAt.Unix()),
		ClientSecret:                    c.ClientSecret,
		AccessTokenValidity:             c.AccessTokenValidity,
		IdTokenValidity:                 c.IdTokenValidity,
		RefreshTokenValidity:            c.RefreshTokenValidity,
		TokenValidityUnits:              c.TokenValidityUnits,
		CallbackURLs:                    c.CallbackURLs,
		LogoutURLs:                      c.LogoutURLs,
		AllowedOAuthFlows:               c.AllowedOAuthFlows,
		AllowedOAuthScopes:              c.AllowedOAuthScopes,
		AllowedOAuthFlowsUserPoolClient: c.AllowedOAuthFlowsUserPoolClient,
		ExplicitAuthFlows:               c.ExplicitAuthFlows,
		SupportedIdentityProviders:      c.SupportedIdentityProviders,
		PreventUserExistenceErrors:      c.PreventUserExistenceErrors,
		ReadAttributes:                  c.ReadAttributes,
		WriteAttributes:                 c.WriteAttributes,
	}
}

// authResultWire is the AWS SDK wire format for a successful authentication result.
type authResultWire struct {
	AccessToken       string             `json:"AccessToken"`
	IdToken           string             `json:"IdToken"`
	RefreshToken      string             `json:"RefreshToken,omitempty"`
	TokenType         string             `json:"TokenType"`
	ExpiresIn         int                `json:"ExpiresIn"`
	NewDeviceMetadata *NewDeviceMetadata `json:"NewDeviceMetadata,omitempty"`
}

type NewDeviceMetadata struct {
	DeviceKey      string `json:"DeviceKey"`
	DeviceGroupKey string `json:"DeviceGroupKey"`
}

// Group is a user pool group used to organise users and assign IAM roles.
type Group struct {
	GroupName   string    `json:"GroupName"`
	UserPoolID  string    `json:"UserPoolId"`
	Description string    `json:"Description,omitempty"`
	Precedence  int       `json:"Precedence"`
	RoleARN     string    `json:"RoleArn,omitempty"`
	CreatedAt   time.Time `json:"CreationDate"`
}

// groupWire is the AWS SDK wire format for a Group.
type groupWire struct {
	GroupName    string  `json:"GroupName"`
	UserPoolId   string  `json:"UserPoolId"`
	Description  string  `json:"Description,omitempty"`
	Precedence   int     `json:"Precedence"`
	RoleArn      string  `json:"RoleArn,omitempty"`
	CreationDate float64 `json:"CreationDate"`
}

func toGroupWire(g *Group) groupWire {
	return groupWire{
		GroupName:    g.GroupName,
		UserPoolId:   g.UserPoolID,
		Description:  g.Description,
		Precedence:   g.Precedence,
		RoleArn:      g.RoleARN,
		CreationDate: float64(g.CreatedAt.Unix()),
	}
}

// defaultTokenValidityUnits returns the AWS-default token validity units.
func defaultTokenValidityUnits() *TokenValidityUnitsType {
	return &TokenValidityUnitsType{
		AccessToken:  "hours",
		IdToken:      "hours",
		RefreshToken: "days",
	}
}

// applyClientDefaults sets AWS-standard defaults for any unset token validity fields.
func applyClientDefaults(c *UserPoolClient) {
	if c.AccessTokenValidity == 0 {
		c.AccessTokenValidity = 1 // 1 hour
	}
	if c.IdTokenValidity == 0 {
		c.IdTokenValidity = 1 // 1 hour
	}
	if c.RefreshTokenValidity == 0 {
		c.RefreshTokenValidity = 30 // 30 days
	}
	if c.TokenValidityUnits == nil {
		c.TokenValidityUnits = defaultTokenValidityUnits()
	} else {
		if c.TokenValidityUnits.AccessToken == "" {
			c.TokenValidityUnits.AccessToken = "hours"
		}
		if c.TokenValidityUnits.IdToken == "" {
			c.TokenValidityUnits.IdToken = "hours"
		}
		if c.TokenValidityUnits.RefreshToken == "" {
			c.TokenValidityUnits.RefreshToken = "days"
		}
	}
}

// tokenDuration converts a validity value + unit string into a time.Duration.
func tokenDuration(value int, unit string) time.Duration {
	switch unit {
	case "seconds":
		return time.Duration(value) * time.Second
	case "minutes":
		return time.Duration(value) * time.Minute
	case "hours":
		return time.Duration(value) * time.Hour
	case "days":
		return time.Duration(value) * 24 * time.Hour
	default:
		return time.Duration(value) * time.Hour
	}
}

// ─── managed login types ──────────────────────────────────────────────────────

// ManagedLoginBranding controls the visual appearance of the managed login pages.
type ManagedLoginBranding struct {
	LogoURL         string `json:"LogoURL,omitempty"`
	BackgroundColor string `json:"BackgroundColor,omitempty"`
	PrimaryColor    string `json:"PrimaryColor,omitempty"`
	FontFamily      string `json:"FontFamily,omitempty"`
	CustomCSS       string `json:"CustomCSS,omitempty"`
}

// UserPoolDomain associates a domain prefix with a user pool for managed login.
type UserPoolDomain struct {
	Domain     string    `json:"Domain"`
	UserPoolID string    `json:"UserPoolId"`
	CreatedAt  time.Time `json:"CreatedAt"`
}

// AuthCode is a short-lived authorization code issued during the OAuth2
// authorization code flow. Single-use, expires in 5 minutes.
type AuthCode struct {
	Code            string    `json:"Code"`
	ClientID        string    `json:"ClientId"`
	UserPoolID      string    `json:"UserPoolId"`
	Username        string    `json:"Username"`
	RedirectURI     string    `json:"RedirectUri"`
	Scopes          []string  `json:"Scopes"`
	State           string    `json:"State,omitempty"`
	Nonce           string    `json:"Nonce,omitempty"`
	CodeChallenge   string    `json:"CodeChallenge,omitempty"`   // PKCE
	ChallengeMethod string    `json:"ChallengeMethod,omitempty"` // "S256" or "plain"
	CreatedAt       time.Time `json:"CreatedAt"`
	ExpiresAt       time.Time `json:"ExpiresAt"`
}

// LoginSession tracks a logged-in user during the managed login flow.
type LoginSession struct {
	SessionID  string    `json:"SessionId"`
	UserPoolID string    `json:"UserPoolId"`
	Username   string    `json:"Username"`
	CreatedAt  time.Time `json:"CreatedAt"`
	ExpiresAt  time.Time `json:"ExpiresAt"`
}

// importUsersRequest is the request body for POST /_overcast/cognito/user-pools/{poolId}/import-users.
type importUsersRequest struct {
	Users []importUserEntry `json:"users"`
}

// importUserEntry is a single user to import from an external Cognito source.
type importUserEntry struct {
	Username   string          `json:"username"`
	Sub        string          `json:"sub"`
	Enabled    bool            `json:"enabled"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"createdAt"`
	ModifiedAt time.Time       `json:"modifiedAt"`
	Attributes []UserAttribute `json:"attributes"`
	Groups     []string        `json:"groups,omitempty"`
	MFAEnabled bool            `json:"mfaEnabled,omitempty"`
}

// importUsersResponse is returned by the import-users endpoint.
type importUsersResponse struct {
	Imported int               `json:"imported"`
	Skipped  int               `json:"skipped"`
	Errors   []importUserError `json:"errors,omitempty"`
}

// importUserError records a problem with a specific entry during import.
type importUserError struct {
	Index    int    `json:"index"`
	Username string `json:"username"`
	Reason   string `json:"reason"`
}
