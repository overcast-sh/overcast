import { awsClients } from "../aws-clients"
import { endpointStore } from "../endpoint-store"
import {
  ListUserPoolsCommand,
  DescribeUserPoolCommand,
  CreateUserPoolCommand,
  DeleteUserPoolCommand,
  UpdateUserPoolCommand,
  CreateUserPoolDomainCommand,
  DeleteUserPoolDomainCommand,
  ListUsersCommand,
  AdminCreateUserCommand,
  AdminDeleteUserCommand,
  AdminDisableUserCommand,
  AdminEnableUserCommand,
  AdminSetUserPasswordCommand,
  AdminGetUserCommand,
  AdminUpdateUserAttributesCommand,
  AdminDeleteUserAttributesCommand,
  ListGroupsCommand,
  CreateGroupCommand,
  DeleteGroupCommand,
  AdminAddUserToGroupCommand,
  AdminRemoveUserFromGroupCommand,
  ListUsersInGroupCommand,
  ListUserPoolClientsCommand,
  CreateUserPoolClientCommand,
  DeleteUserPoolClientCommand,
  DescribeUserPoolClientCommand,
  UpdateUserPoolClientCommand,
  InitiateAuthCommand,
} from "@aws-sdk/client-cognito-identity-provider"
import type {
  TimeUnitsType,
  OAuthFlowType,
  UsernameAttributeType,
  EmailSendingAccountType,
} from "@aws-sdk/client-cognito-identity-provider"

export interface CognitoPool {
  id: string
  name: string
  creationDate?: string
  lastModifiedDate?: string
}

export interface PasswordPolicy {
  minimumLength: number
  requireUppercase: boolean
  requireLowercase: boolean
  requireNumbers: boolean
  requireSymbols: boolean
  temporaryPasswordValidityDays: number
}

export interface AdminCreateUserConfig {
  allowAdminCreateUserOnly: boolean
  unusedAccountValidityDays: number
}

export interface EmailConfig {
  emailSendingAccount: string
  sourceArn: string
  from: string
  replyToEmailAddress: string
}

export interface VerificationMessageTemplate {
  defaultEmailOption: string
  emailMessage: string
  emailSubject: string
  smsMessage: string
}

export interface CognitoPoolDetail {
  id: string
  name: string
  arn: string
  creationDate?: string
  lastModifiedDate?: string
  estimatedNumberOfUsers: number
  domain?: string
  usernameAttributes?: string[]
  policies?: {
    passwordPolicy?: PasswordPolicy
  }
  adminCreateUserConfig?: AdminCreateUserConfig
  emailConfiguration?: EmailConfig
  verificationMessageTemplate?: VerificationMessageTemplate
}

export interface CognitoUser {
  username: string
  userStatus: string
  enabled: boolean
  userCreateDate?: string
  userLastModifiedDate?: string
  attributes: Record<string, string>
}

export interface CognitoGroup {
  name: string
  description?: string
  creationDate?: string
  lastModifiedDate?: string
  userCount?: number
}

export interface TokenValidityUnits {
  accessToken: string
  idToken: string
  refreshToken: string
}

export interface ManagedLoginBranding {
  LogoURL: string
  BackgroundColor: string
  PrimaryColor: string
  FontFamily: string
  CustomCSS: string
}

export interface CognitoClient {
  clientId: string
  clientName: string
  creationDate?: string
  lastModifiedDate?: string
  clientSecret?: string
  accessTokenValidity?: number
  idTokenValidity?: number
  refreshTokenValidity?: number
  tokenValidityUnits?: TokenValidityUnits
  callbackUrls?: string[]
  logoutUrls?: string[]
  allowedOAuthFlows?: string[]
  allowedOAuthScopes?: string[]
  allowedOAuthFlowsUserPoolClient?: boolean
  supportedIdentityProviders?: string[]
}

export const cognito = {
  // ─── Pools ────────────────────────────────────────────────────────────

  listPools: async (): Promise<CognitoPool[]> => {
    const res = await awsClients.cognito().send(new ListUserPoolsCommand({ MaxResults: 60 }))
    return (res.UserPools ?? []).map((p) => ({
      id: p.Id ?? "",
      name: p.Name ?? "",
      creationDate: p.CreationDate?.toISOString(),
      lastModifiedDate: p.LastModifiedDate?.toISOString(),
    }))
  },

  describePool: async (poolId: string): Promise<CognitoPoolDetail | null> => {
    const res = await awsClients.cognito().send(new DescribeUserPoolCommand({ UserPoolId: poolId }))
    const p = res.UserPool
    if (!p) return null
    return {
      id: p.Id ?? "",
      name: p.Name ?? "",
      arn: p.Arn ?? "",
      creationDate: p.CreationDate?.toISOString(),
      lastModifiedDate: p.LastModifiedDate?.toISOString(),
      estimatedNumberOfUsers: p.EstimatedNumberOfUsers ?? 0,
      domain: p.Domain ?? undefined,
      usernameAttributes:
        p.UsernameAttributes && p.UsernameAttributes.length > 0 ? p.UsernameAttributes : undefined,
      policies: p.Policies?.PasswordPolicy
        ? {
            passwordPolicy: {
              minimumLength: p.Policies.PasswordPolicy.MinimumLength ?? 8,
              requireUppercase: p.Policies.PasswordPolicy.RequireUppercase ?? false,
              requireLowercase: p.Policies.PasswordPolicy.RequireLowercase ?? false,
              requireNumbers: p.Policies.PasswordPolicy.RequireNumbers ?? false,
              requireSymbols: p.Policies.PasswordPolicy.RequireSymbols ?? false,
              temporaryPasswordValidityDays:
                p.Policies.PasswordPolicy.TemporaryPasswordValidityDays ?? 7,
            },
          }
        : undefined,
      adminCreateUserConfig: p.AdminCreateUserConfig
        ? {
            allowAdminCreateUserOnly: p.AdminCreateUserConfig.AllowAdminCreateUserOnly ?? false,
            unusedAccountValidityDays: p.AdminCreateUserConfig.UnusedAccountValidityDays ?? 7,
          }
        : undefined,
      emailConfiguration: p.EmailConfiguration
        ? {
            emailSendingAccount: p.EmailConfiguration.EmailSendingAccount ?? "",
            sourceArn: p.EmailConfiguration.SourceArn ?? "",
            from: p.EmailConfiguration.From ?? "",
            replyToEmailAddress: p.EmailConfiguration.ReplyToEmailAddress ?? "",
          }
        : undefined,
      verificationMessageTemplate: p.VerificationMessageTemplate
        ? {
            defaultEmailOption:
              p.VerificationMessageTemplate.DefaultEmailOption ?? "CONFIRM_WITH_CODE",
            emailMessage: p.VerificationMessageTemplate.EmailMessage ?? "",
            emailSubject: p.VerificationMessageTemplate.EmailSubject ?? "",
            smsMessage: p.VerificationMessageTemplate.SmsMessage ?? "",
          }
        : undefined,
    }
  },

  createPool: async (opts: {
    name: string
    usernameAttributes?: string[]
    adminOnly?: boolean
    passwordPolicy?: PasswordPolicy
  }) => {
    await awsClients.cognito().send(
      new CreateUserPoolCommand({
        PoolName: opts.name,
        UsernameAttributes:
          opts.usernameAttributes && opts.usernameAttributes.length > 0
            ? (opts.usernameAttributes as UsernameAttributeType[])
            : undefined,
        AdminCreateUserConfig: opts.adminOnly ? { AllowAdminCreateUserOnly: true } : undefined,
        Policies: opts.passwordPolicy
          ? {
              PasswordPolicy: {
                MinimumLength: opts.passwordPolicy.minimumLength,
                RequireUppercase: opts.passwordPolicy.requireUppercase,
                RequireLowercase: opts.passwordPolicy.requireLowercase,
                RequireNumbers: opts.passwordPolicy.requireNumbers,
                RequireSymbols: opts.passwordPolicy.requireSymbols,
                TemporaryPasswordValidityDays: opts.passwordPolicy.temporaryPasswordValidityDays,
              },
            }
          : undefined,
      }),
    )
  },

  deletePool: async (poolId: string) => {
    await awsClients.cognito().send(new DeleteUserPoolCommand({ UserPoolId: poolId }))
  },

  createDomain: async (poolId: string, domain: string): Promise<void> => {
    await awsClients
      .cognito()
      .send(new CreateUserPoolDomainCommand({ UserPoolId: poolId, Domain: domain }))
  },

  deleteDomain: async (poolId: string, domain: string): Promise<void> => {
    await awsClients
      .cognito()
      .send(new DeleteUserPoolDomainCommand({ UserPoolId: poolId, Domain: domain }))
  },

  updatePoolPasswordPolicy: async (poolId: string, policy: PasswordPolicy): Promise<void> => {
    await awsClients.cognito().send(
      new UpdateUserPoolCommand({
        UserPoolId: poolId,
        Policies: {
          PasswordPolicy: {
            MinimumLength: policy.minimumLength,
            RequireUppercase: policy.requireUppercase,
            RequireLowercase: policy.requireLowercase,
            RequireNumbers: policy.requireNumbers,
            RequireSymbols: policy.requireSymbols,
            TemporaryPasswordValidityDays: policy.temporaryPasswordValidityDays,
          },
        },
      }),
    )
  },

  updatePoolSelfRegistration: async (
    poolId: string,
    config: AdminCreateUserConfig,
  ): Promise<void> => {
    await awsClients.cognito().send(
      new UpdateUserPoolCommand({
        UserPoolId: poolId,
        AdminCreateUserConfig: {
          AllowAdminCreateUserOnly: config.allowAdminCreateUserOnly,
          UnusedAccountValidityDays: config.unusedAccountValidityDays,
        },
      }),
    )
  },

  updatePoolEmailConfig: async (poolId: string, config: EmailConfig): Promise<void> => {
    await awsClients.cognito().send(
      new UpdateUserPoolCommand({
        UserPoolId: poolId,
        EmailConfiguration: {
          EmailSendingAccount: config.emailSendingAccount as EmailSendingAccountType | undefined,
          SourceArn: config.sourceArn || undefined,
          From: config.from || undefined,
          ReplyToEmailAddress: config.replyToEmailAddress || undefined,
        },
      }),
    )
  },

  updatePoolVerificationMessages: async (
    poolId: string,
    template: VerificationMessageTemplate,
  ): Promise<void> => {
    await awsClients.cognito().send(
      new UpdateUserPoolCommand({
        UserPoolId: poolId,
        VerificationMessageTemplate: {
          DefaultEmailOption: (template.defaultEmailOption || "CONFIRM_WITH_CODE") as
            "CONFIRM_WITH_CODE" | "CONFIRM_WITH_LINK",
          EmailMessage: template.emailMessage || undefined,
          EmailSubject: template.emailSubject || undefined,
          SmsMessage: template.smsMessage || undefined,
        },
      }),
    )
  },

  getPlaintextPassword: async (poolId: string, username: string): Promise<string | null> => {
    const { baseUrl } = endpointStore.get()
    const res = await fetch(
      `${baseUrl}/_overcast/cognito/user-pools/${poolId}/users/${encodeURIComponent(username)}/password`,
    )
    if (!res.ok) return null
    const json = (await res.json()) as { password?: string }
    return json.password ?? null
  },

  // ─── Managed Login Branding ───────────────────────────────────────────

  getBranding: async (poolId: string): Promise<ManagedLoginBranding> => {
    const { baseUrl } = endpointStore.get()
    const res = await fetch(`${baseUrl}/_overcast/cognito/user-pools/${poolId}/branding`)
    if (!res.ok) throw new Error(`Failed to fetch branding: ${res.status}`)
    return (await res.json()) as ManagedLoginBranding
  },

  setBranding: async (
    poolId: string,
    branding: ManagedLoginBranding,
  ): Promise<ManagedLoginBranding> => {
    const { baseUrl } = endpointStore.get()
    const res = await fetch(`${baseUrl}/_overcast/cognito/user-pools/${poolId}/branding`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(branding),
    })
    if (!res.ok) throw new Error(`Failed to save branding: ${res.status}`)
    return (await res.json()) as ManagedLoginBranding
  },

  // ─── Users ────────────────────────────────────────────────────────────

  listUsers: async (poolId: string): Promise<CognitoUser[]> => {
    const res = await awsClients.cognito().send(new ListUsersCommand({ UserPoolId: poolId }))
    return (res.Users ?? []).map((u) => ({
      username: u.Username ?? "",
      userStatus: u.UserStatus ?? "",
      enabled: u.Enabled ?? true,
      userCreateDate: u.UserCreateDate?.toISOString(),
      userLastModifiedDate: u.UserLastModifiedDate?.toISOString(),
      attributes: Object.fromEntries(
        (u.Attributes ?? []).map((a) => [a.Name ?? "", a.Value ?? ""]),
      ),
    }))
  },

  createUser: async (
    poolId: string,
    username: string,
    opts?: {
      email?: string
      phoneNumber?: string
      temporaryPassword?: string
      messageAction?: "SUPPRESS" | "RESEND"
    },
  ) => {
    const attrs: { Name: string; Value: string }[] = []
    if (opts?.email) {
      attrs.push({ Name: "email", Value: opts.email })
    }
    if (opts?.phoneNumber) {
      attrs.push({ Name: "phone_number", Value: opts.phoneNumber })
    }
    await awsClients.cognito().send(
      new AdminCreateUserCommand({
        UserPoolId: poolId,
        Username: username,
        TemporaryPassword: opts?.temporaryPassword || undefined,
        UserAttributes: attrs.length > 0 ? attrs : undefined,
        MessageAction: opts?.messageAction,
      }),
    )
  },

  deleteUser: async (poolId: string, username: string) => {
    await awsClients
      .cognito()
      .send(new AdminDeleteUserCommand({ UserPoolId: poolId, Username: username }))
  },

  disableUser: async (poolId: string, username: string) => {
    await awsClients
      .cognito()
      .send(new AdminDisableUserCommand({ UserPoolId: poolId, Username: username }))
  },

  enableUser: async (poolId: string, username: string) => {
    await awsClients
      .cognito()
      .send(new AdminEnableUserCommand({ UserPoolId: poolId, Username: username }))
  },

  setUserPassword: async (
    poolId: string,
    username: string,
    password: string,
    permanent: boolean,
  ) => {
    await awsClients.cognito().send(
      new AdminSetUserPasswordCommand({
        UserPoolId: poolId,
        Username: username,
        Password: password,
        Permanent: permanent,
      }),
    )
  },

  getUser: async (poolId: string, username: string): Promise<CognitoUser> => {
    const res = await awsClients
      .cognito()
      .send(new AdminGetUserCommand({ UserPoolId: poolId, Username: username }))
    return {
      username: res.Username ?? "",
      userStatus: res.UserStatus ?? "",
      enabled: res.Enabled ?? true,
      userCreateDate: res.UserCreateDate?.toISOString(),
      userLastModifiedDate: res.UserLastModifiedDate?.toISOString(),
      attributes: Object.fromEntries(
        (res.UserAttributes ?? []).map((a) => [a.Name ?? "", a.Value ?? ""]),
      ),
    }
  },

  updateUserAttributes: async (
    poolId: string,
    username: string,
    attributes: { name: string; value: string }[],
  ) => {
    await awsClients.cognito().send(
      new AdminUpdateUserAttributesCommand({
        UserPoolId: poolId,
        Username: username,
        UserAttributes: attributes.map((a) => ({ Name: a.name, Value: a.value })),
      }),
    )
  },

  deleteUserAttributes: async (poolId: string, username: string, attributeNames: string[]) => {
    await awsClients.cognito().send(
      new AdminDeleteUserAttributesCommand({
        UserPoolId: poolId,
        Username: username,
        UserAttributeNames: attributeNames,
      }),
    )
  },

  // ─── Groups ───────────────────────────────────────────────────────────

  listGroups: async (poolId: string): Promise<CognitoGroup[]> => {
    const res = await awsClients.cognito().send(new ListGroupsCommand({ UserPoolId: poolId }))
    return (res.Groups ?? []).map((g) => ({
      name: g.GroupName ?? "",
      description: g.Description,
      creationDate: g.CreationDate?.toISOString(),
      lastModifiedDate: g.LastModifiedDate?.toISOString(),
    }))
  },

  createGroup: async (poolId: string, name: string, description?: string) => {
    await awsClients
      .cognito()
      .send(
        new CreateGroupCommand({ UserPoolId: poolId, GroupName: name, Description: description }),
      )
  },

  deleteGroup: async (poolId: string, groupName: string) => {
    await awsClients
      .cognito()
      .send(new DeleteGroupCommand({ UserPoolId: poolId, GroupName: groupName }))
  },

  listUsersInGroup: async (poolId: string, groupName: string): Promise<CognitoUser[]> => {
    const res = await awsClients
      .cognito()
      .send(new ListUsersInGroupCommand({ UserPoolId: poolId, GroupName: groupName }))
    return (res.Users ?? []).map((u) => ({
      username: u.Username ?? "",
      userStatus: u.UserStatus ?? "",
      enabled: u.Enabled ?? true,
      userCreateDate: u.UserCreateDate?.toISOString(),
      userLastModifiedDate: u.UserLastModifiedDate?.toISOString(),
      attributes: Object.fromEntries(
        (u.Attributes ?? []).map((a) => [a.Name ?? "", a.Value ?? ""]),
      ),
    }))
  },

  addUserToGroup: async (poolId: string, username: string, groupName: string) => {
    await awsClients.cognito().send(
      new AdminAddUserToGroupCommand({
        UserPoolId: poolId,
        Username: username,
        GroupName: groupName,
      }),
    )
  },

  removeUserFromGroup: async (poolId: string, username: string, groupName: string) => {
    await awsClients.cognito().send(
      new AdminRemoveUserFromGroupCommand({
        UserPoolId: poolId,
        Username: username,
        GroupName: groupName,
      }),
    )
  },

  // ─── App Clients ──────────────────────────────────────────────────────

  listClients: async (poolId: string): Promise<CognitoClient[]> => {
    const res = await awsClients
      .cognito()
      .send(new ListUserPoolClientsCommand({ UserPoolId: poolId, MaxResults: 60 }))
    return (res.UserPoolClients ?? []).map((c) => ({
      clientId: c.ClientId ?? "",
      clientName: c.ClientName ?? "",
    }))
  },

  createClient: async (
    poolId: string,
    name: string,
    generateSecret: boolean,
  ): Promise<CognitoClient> => {
    const res = await awsClients.cognito().send(
      new CreateUserPoolClientCommand({
        UserPoolId: poolId,
        ClientName: name,
        GenerateSecret: generateSecret,
      }),
    )
    const c = res.UserPoolClient
    return {
      clientId: c?.ClientId ?? "",
      clientName: c?.ClientName ?? "",
      clientSecret: c?.ClientSecret,
      creationDate: c?.CreationDate?.toISOString(),
      lastModifiedDate: c?.LastModifiedDate?.toISOString(),
      accessTokenValidity: c?.AccessTokenValidity,
      idTokenValidity: c?.IdTokenValidity,
      refreshTokenValidity: c?.RefreshTokenValidity,
      tokenValidityUnits: c?.TokenValidityUnits
        ? {
            accessToken: c.TokenValidityUnits.AccessToken ?? "hours",
            idToken: c.TokenValidityUnits.IdToken ?? "hours",
            refreshToken: c.TokenValidityUnits.RefreshToken ?? "days",
          }
        : undefined,
      callbackUrls: c?.CallbackURLs ?? [],
      logoutUrls: c?.LogoutURLs ?? [],
      allowedOAuthFlows: c?.AllowedOAuthFlows ?? [],
      allowedOAuthScopes: c?.AllowedOAuthScopes ?? [],
      allowedOAuthFlowsUserPoolClient: c?.AllowedOAuthFlowsUserPoolClient ?? false,
      supportedIdentityProviders: c?.SupportedIdentityProviders ?? [],
    }
  },

  deleteClient: async (poolId: string, clientId: string) => {
    await awsClients
      .cognito()
      .send(new DeleteUserPoolClientCommand({ UserPoolId: poolId, ClientId: clientId }))
  },

  describeClient: async (poolId: string, clientId: string): Promise<CognitoClient | null> => {
    const res = await awsClients
      .cognito()
      .send(new DescribeUserPoolClientCommand({ UserPoolId: poolId, ClientId: clientId }))
    const c = res.UserPoolClient
    if (!c) return null
    return {
      clientId: c.ClientId ?? "",
      clientName: c.ClientName ?? "",
      clientSecret: c.ClientSecret,
      creationDate: c.CreationDate?.toISOString(),
      lastModifiedDate: c.LastModifiedDate?.toISOString(),
      accessTokenValidity: c.AccessTokenValidity,
      idTokenValidity: c.IdTokenValidity,
      refreshTokenValidity: c.RefreshTokenValidity,
      tokenValidityUnits: c.TokenValidityUnits
        ? {
            accessToken: c.TokenValidityUnits.AccessToken ?? "hours",
            idToken: c.TokenValidityUnits.IdToken ?? "hours",
            refreshToken: c.TokenValidityUnits.RefreshToken ?? "days",
          }
        : undefined,
      callbackUrls: c.CallbackURLs ?? [],
      logoutUrls: c.LogoutURLs ?? [],
      allowedOAuthFlows: c.AllowedOAuthFlows ?? [],
      allowedOAuthScopes: c.AllowedOAuthScopes ?? [],
      allowedOAuthFlowsUserPoolClient: c.AllowedOAuthFlowsUserPoolClient ?? false,
      supportedIdentityProviders: c.SupportedIdentityProviders ?? [],
    }
  },

  updateClient: async (
    poolId: string,
    clientId: string,
    updates: {
      accessTokenValidity?: number
      idTokenValidity?: number
      refreshTokenValidity?: number
      tokenValidityUnits?: {
        accessToken?: string
        idToken?: string
        refreshToken?: string
      }
      callbackUrls?: string[]
      logoutUrls?: string[]
      allowedOAuthFlows?: string[]
      allowedOAuthScopes?: string[]
      allowedOAuthFlowsUserPoolClient?: boolean
      supportedIdentityProviders?: string[]
    },
  ): Promise<CognitoClient> => {
    const res = await awsClients.cognito().send(
      new UpdateUserPoolClientCommand({
        UserPoolId: poolId,
        ClientId: clientId,
        AccessTokenValidity: updates.accessTokenValidity,
        IdTokenValidity: updates.idTokenValidity,
        RefreshTokenValidity: updates.refreshTokenValidity,
        TokenValidityUnits: updates.tokenValidityUnits
          ? {
              AccessToken: updates.tokenValidityUnits.accessToken as TimeUnitsType,
              IdToken: updates.tokenValidityUnits.idToken as TimeUnitsType,
              RefreshToken: updates.tokenValidityUnits.refreshToken as TimeUnitsType,
            }
          : undefined,
        CallbackURLs: updates.callbackUrls,
        LogoutURLs: updates.logoutUrls,
        AllowedOAuthFlows: updates.allowedOAuthFlows as OAuthFlowType[] | undefined,
        AllowedOAuthScopes: updates.allowedOAuthScopes,
        AllowedOAuthFlowsUserPoolClient: updates.allowedOAuthFlowsUserPoolClient,
        SupportedIdentityProviders: updates.supportedIdentityProviders,
      }),
    )
    const c = res.UserPoolClient
    return {
      clientId: c?.ClientId ?? "",
      clientName: c?.ClientName ?? "",
      clientSecret: c?.ClientSecret,
      creationDate: c?.CreationDate?.toISOString(),
      lastModifiedDate: c?.LastModifiedDate?.toISOString(),
      accessTokenValidity: c?.AccessTokenValidity,
      idTokenValidity: c?.IdTokenValidity,
      refreshTokenValidity: c?.RefreshTokenValidity,
      tokenValidityUnits: c?.TokenValidityUnits
        ? {
            accessToken: c.TokenValidityUnits.AccessToken ?? "hours",
            idToken: c.TokenValidityUnits.IdToken ?? "hours",
            refreshToken: c.TokenValidityUnits.RefreshToken ?? "days",
          }
        : undefined,
      callbackUrls: c?.CallbackURLs ?? [],
      logoutUrls: c?.LogoutURLs ?? [],
      allowedOAuthFlows: c?.AllowedOAuthFlows ?? [],
      allowedOAuthScopes: c?.AllowedOAuthScopes ?? [],
      allowedOAuthFlowsUserPoolClient: c?.AllowedOAuthFlowsUserPoolClient ?? false,
      supportedIdentityProviders: c?.SupportedIdentityProviders ?? [],
    }
  },

  // ─── Auth flows ───────────────────────────────────────────────────────

  initiateAuth: async (opts: {
    clientId: string
    authFlow: "USER_PASSWORD_AUTH"
    username: string
    password: string
  }): Promise<{ idToken?: string; accessToken?: string; refreshToken?: string }> => {
    const res = await awsClients.cognito().send(
      new InitiateAuthCommand({
        ClientId: opts.clientId,
        AuthFlow: opts.authFlow,
        AuthParameters: {
          USERNAME: opts.username,
          PASSWORD: opts.password,
        },
      }),
    )
    return {
      idToken: res.AuthenticationResult?.IdToken,
      accessToken: res.AuthenticationResult?.AccessToken,
      refreshToken: res.AuthenticationResult?.RefreshToken,
    }
  },
}
