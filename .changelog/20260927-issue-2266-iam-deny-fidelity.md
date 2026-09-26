* [iam/sts] `sts:GetCallerIdentity` and `sts:GetSessionToken` are served under `OVERCAST_ENFORCE_IAM` whatever the caller's policies, as AWS serves them.
  both used to be denied to a principal without an `Allow`; AWS documents that no policy, not even an explicit `Deny`, controls either.
* [iam/sns] SNS answers an enforcement denial with its own `AuthorizationError` (403) instead of `AccessDenied`.
~ [iam] An enforcement denial names the principal, action, resource and deciding policy type, in the message format the IAM User Guide documents.
  `User: <arn> is not authorized to perform: <action> on resource: <arn> because no identity-based policy allows the <action> action`, or `with an explicit deny in ...`.
