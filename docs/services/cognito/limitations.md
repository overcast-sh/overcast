---
title: "Cognito limitations"
description: "Which Lambda triggers fire on which call and which do not, what the Smithy RPC v2 path misses, and the emulator-only routes that have no AWS counterpart."
section: "Service Reference"
tags:
  - cognito
  - docs
  - limitations
  - services
---

# Cognito limitations

Divergences from AWS, in full. The summary is on the
[Cognito page](../cognito.md).

## Lambda triggers

Five of `LambdaConfig`'s triggers are invoked. The whole config is stored and
round-tripped either way, so a pool built with `lambdaTriggers` in CDK looks
configured whether or not its trigger runs.

| Trigger              | Fires on                                                                   | On failure                     |
| -------------------- | ---------------------------------------------------------------------------- | -------------------------------- |
| `PreSignUp`          | `SignUp`, `AdminCreateUser`                                                | Fails the call (`UserLambdaValidationException`) |
| `PostConfirmation`   | `ConfirmSignUp`, `AdminConfirmSignUp`, `ConfirmForgotPassword`             | Logged; the call still succeeds |
| `PostAuthentication` | A completed `InitiateAuth` / `AdminInitiateAuth` / `RespondToAuthChallenge` | Fails the call                  |
| `PreTokenGeneration` | Every token issue                                                          | Fails the call                  |
| `CustomMessage`      | Sign-up, admin create, resend code, forgot password                        | Fails the call                  |

| Not invoked                                                        | Why                                          |
| ------------------------------------------------------------------ | ---------------------------------------------- |
| `PreAuthentication`, `UserMigration`                               | Not wired yet                                 |
| `DefineAuthChallenge`, `CreateAuthChallenge`, `VerifyAuthChallengeResponse` | Tracked separately ([#88](https://github.com/overcast-sh/overcast/issues/88), [#94](https://github.com/overcast-sh/overcast/issues/94), [#101](https://github.com/overcast-sh/overcast/issues/101)) |

`PreSignUp`'s `autoConfirmUser` / `autoVerifyEmail` / `autoVerifyPhone`
responses are honoured on `SignUp` — an auto-confirmed user fires
`PostConfirmation` in the same call and gets no confirmation message. On
`AdminCreateUser` they are ignored, matching AWS.

`PreTokenGeneration` implements event version `V1_0` (ID-token claims) only;
`V2_0` and `V3_0` access-token customisation are not implemented.
`ClientMetadata` and `ValidationData` are not captured from the request, so
trigger events fire without them.

## Wire protocols

Both `awsJson1_1` (`X-Amz-Target: AWSCognitoIdentityProviderService.*`) and
Smithy RPC v2 CBOR (`POST /service/cognito/operation/{Operation}`) are served,
but they are separate implementations. The CBOR path gets
`PreTokenGeneration` — token issue is a single choke point — and not
`PreSignUp`, `PostConfirmation`, `PostAuthentication` or `CustomMessage`. AWS
SDK traffic uses the JSON path, which has all five.

## Emulator-only routes

These have no AWS counterpart and exist for local development:

| Route                                                            | Purpose                                       |
| ---------------------------------------------------------------- | ----------------------------------------------- |
| `/_overcast/cognito/user-pools/{poolId}/…`                       | Managed login pages and OAuth2 endpoints       |
| `/_overcast/cognito/user-pools/{poolId}/debug/token`             | Inspect an issued token                        |
| `/_overcast/cognito/user-pools/{poolId}/users/{username}/password` | Read a user's plaintext password             |
| `/_overcast/cognito/user-pools/{poolId}/branding`                | Get and set managed-login branding             |
| `/_overcast/cognito/user-pools/{poolId}/import-users`            | Bulk user import                               |

> [!CAUTION]
> One of these routes hands out plaintext passwords. Do not expose Overcast's
> port to a network you do not control.

## Everything else

- **Identity pools** (`cognito-identity`) are not emulated. Only user pools
  (`cognito-idp`) are.
- **Passwords** are bcrypt-hashed at the library's minimum cost, which is not a
  production hashing configuration.
- **SRP and device SRP proofs** are accepted once the claimed fields
  (`PASSWORD_CLAIM_SIGNATURE`, `PASSWORD_CLAIM_SECRET_BLOCK`, `TIMESTAMP` for
  `PASSWORD_VERIFIER`/`DEVICE_PASSWORD_VERIFIER`) are present; the proof
  itself is not cryptographically verified, so any well-formed claim
  succeeds.
- **WebAuthn** registration and sign-in accept AWS-shaped requests without
  full cryptographic verification: relying-party/domain checks, and
  attestation/assertion signature validation are not performed, so a
  well-formed credential response succeeds. The registration challenge does
  expire after 5 minutes, but the sign-in challenge is never validated
  against the client's response at all.
- **Verification-code delivery** (sign-up, forgot-password, and attribute
  verification codes) has no emulated TTL, throttling, or delivery-failure
  behavior — a code stays valid until consumed or replaced.
- **`AdminGetDevice`/`AdminListDevices`** don't check IAM authorization on the
  requested user — the same project-wide behavior as every operation (see
  [behavioural differences](../../migration/differences.md)) — and the
  `DeviceAttributes` names they return (`device_status`, `device_name`,
  `dev:device_remembered_status`) are emulator choices where AWS's list is
  unconstrained. `AdminListDevices`' `PaginationToken` is an emulator-internal
  integer offset, not AWS's opaque token.
- **`UpdateUserPoolDomain`** accepts an SSL certificate update and does nothing
  with it.
- **`DescribeUserPoolDomain`** returns an empty `DomainDescription` when the
  domain is not found, rather than an error.
- Any operation outside the 70 listed returns `501 Not Implemented`.

## Related

- [Cognito](../cognito.md) — quick start and what works
- [Cognito examples](./examples.md) — worked sign-up and sign-in flows
- [Cognito operations](./operations.md) — per-operation status
