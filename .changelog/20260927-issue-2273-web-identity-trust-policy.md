* [iam/sts] `AssumeRoleWithWebIdentity` is decided by the role's trust policy under `OVERCAST_ENFORCE_IAM`, not refused as unsigned (#2273).
  a `Federated` principal matches the token's issuer, conditions read `<provider>:sub`, `:aud` and `sts:RoleSessionName`, and a refusal is `AccessDenied` (403).
~ [iam] The policy simulator and enforcement evaluate a `Federated` principal instead of reporting it unsupported; it never matches an IAM user or role caller.
*. [sts] `AssumeRoleWithWebIdentity` without a `WebIdentityToken` is refused with `MissingParameter` (400), as the model requires the parameter.
