---
title: "Cognito — Amazon Cognito User Pools"
description: "Quick start, the auth flows, challenges and MFA that work, how tokens and JWKS discovery are served, the managed login pages, and what identity pools and CUSTOM_AUTH triggers do not do."
section: "Service Reference"
tags:
  - amazon
  - cognito
  - docs
  - pools
  - services
  - user
---

# Cognito — Amazon Cognito User Pools

User pools that issue real RS256 JWTs, verifiable against a per-pool JWKS
endpoint. Identity pools (`cognito-identity`) are not emulated.

**Status:** ✅ Supported

## Quick start

```bash
export AWS_ENDPOINT_URL=http://localhost:4566

POOL=$(aws cognito-idp create-user-pool --pool-name app --query UserPool.Id --output text)
CLIENT=$(aws cognito-idp create-user-pool-client --user-pool-id "$POOL" \
  --client-name web --explicit-auth-flows ALLOW_USER_PASSWORD_AUTH ALLOW_REFRESH_TOKEN_AUTH \
  --query UserPoolClient.ClientId --output text)

aws cognito-idp admin-create-user --user-pool-id "$POOL" --username ada --message-action SUPPRESS
aws cognito-idp admin-set-user-password --user-pool-id "$POOL" --username ada \
  --password 'Passw0rd!' --permanent

aws cognito-idp initiate-auth --client-id "$CLIENT" --auth-flow USER_PASSWORD_AUTH \
  --auth-parameters USERNAME=ada,PASSWORD='Passw0rd!'
```

Any credentials work; with none configured, run `eval "$(overcast env)"` first
— see [Using AWS SDKs and CLI](../sdk-cli.md#credentials).

## What works

| Area              | Behaviour                                                                                                            |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------- |
| Pools and clients | Full CRUD, plus sign-in policy, alias attributes, account recovery, device configuration and token validity settings     |
| Users             | Admin and self-service creation, attributes, enable/disable, confirmation, password set and reset                       |
| Auth flows        | `USER_PASSWORD_AUTH`, `USER_SRP_AUTH`, `REFRESH_TOKEN_AUTH`, `CUSTOM_AUTH`, and `USER_AUTH` choice-based sign-in         |
| Challenges        | `NEW_PASSWORD_REQUIRED`, `SMS_MFA`, `SOFTWARE_TOKEN_MFA`, `SELECT_MFA_TYPE`, `MFA_SETUP`, `EMAIL_OTP`, `SMS_OTP`, `WEB_AUTHN`, `DEVICE_SRP_AUTH` and its verifier |
| MFA               | SMS and TOTP (RFC 6238, HMAC-SHA1, 30-second window, 6 digits) second factors, plus WebAuthn registration; not email    |
| Devices           | Confirm, list, forget and update status, in both the user and admin forms                                               |
| Groups            | Full CRUD plus membership, with `Precedence` and `RoleArn`                                                              |
| Domains           | Create, describe, update and delete a pool's hosted-UI domain                                                           |
| Lambda triggers   | `PreSignUp`, `PostConfirmation`, `PreTokenGeneration`, `PostAuthentication` and `CustomMessage`                         |
| Messages          | Per-pool verification, invite and SMS templates, with `{username}` and `{####}` placeholders                            |

Every message a pool sends — confirmation codes, invites, password resets —
lands in the console's [Inbox](http://localhost:4567/inbox) rather than a real
mailbox.

## Tokens and discovery

Access and ID tokens are RS256-signed JWTs, signed with an RSA-2048 key
generated lazily per pool and held in emulator state, so it survives a restart
on a persistent backend. Refresh, session and MFA tokens are opaque hex
strings.

```
GET /{poolId}/.well-known/jwks.json
GET /{poolId}/.well-known/openid-configuration
```

An API Gateway authorizer or a JWT library pointed at those paths validates a
token exactly as it would against AWS. Pool IDs keep AWS's
`{region}_{8-char-hex}` shape.

## Feature plans

Real Cognito gates newer authentication features behind a user pool's
`UserPoolTier` (`LITE`, `ESSENTIALS`, or `PLUS`); a pool with no tier set
behaves as `ESSENTIALS`. Overcast enforces the same gate on `LITE`-tier
pools, rejecting with `FeatureUnavailableInTierException`: a `SignInPolicy`
on `CreateUserPool`/`UpdateUserPool`, `ALLOW_USER_AUTH` on
`CreateUserPoolClient`/`UpdateUserPoolClient`, and a `WebAuthnConfiguration`
on `SetUserPoolMfaConfig` — matching AWS's requirement that choice-based
sign-in and passkeys need the Essentials plan or higher.

## Managed login

A browser-usable hosted UI is served under
`/_overcast/cognito/user-pools/{poolId}/`: the OAuth2 endpoints (`authorize`,
`token`, `userInfo`, `revoke`) plus pages for login, signup, confirmation, new
password, MFA and password reset. It is an emulator convenience, not a copy of
AWS's hosted-UI URLs.

## Differences from AWS

| Area                 | On AWS                                          | Overcast                                                            |
| -------------------- | ----------------------------------------------- | ------------------------------------------------------------------- |
| Identity pools       | `cognito-identity` federates to AWS credentials | Not emulated at all                                                 |
| Message delivery     | Email and SMS reach the user                    | Captured in the Inbox                                               |
| Password storage     | Never retrievable                               | Bcrypt at minimum cost, and an emulator route returns the plaintext |
| Hosted-UI URL        | `{domain}.auth.{region}.amazoncognito.com`      | A path under the emulator's own origin                              |
| CUSTOM_AUTH triggers | `DefineAuthChallenge` and friends run           | Not invoked — the flow works, the triggers do not                   |
| SRP proofs           | Cryptographically verified zero-knowledge proof | Accepted once the claim fields are present; any well-formed proof succeeds |

The full list, including which trigger fires on which call, is in
[Limitations](./cognito/limitations.md).

## Gotchas

> [!WARNING]
> TOTP verification accepts the current 30-second window and the one before it.
> A client clock running *ahead* of the emulator fails; running behind is
> tolerated.

Existing users do not have to be recreated by hand.

> [!TIP]
> `overcast import cognito-users` copies users out of a real pool into a local
> one — see [Examples](./cognito/examples.md).

<!-- BEGIN overcast:capabilities -->

## Operations

All 70 listed operations are implemented.
Per-operation status, notes and AWS API links: [Cognito operations](cognito/operations.md).

<!-- END overcast:capabilities -->

## Related

- [Cognito limitations](./cognito/limitations.md)
- [Cognito examples](./cognito/examples.md)
- [SES](./ses.md) — where pool mail lands
- [All service pages](./README.md)
- [Service names and state overrides](../configuration.md#service-names)
- [AWS API reference](https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/Welcome.html)
