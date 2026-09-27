* [iam/sts] `AssumeRoleWithWebIdentity` is decided by the role's trust policy under `OVERCAST_ENFORCE_IAM`, instead of being refused as unsigned.
  a `Federated` principal matches the token's issuer, conditions read `<provider>:sub` and `:aud`, and a refusal is AWS's `AccessDenied` (403).
