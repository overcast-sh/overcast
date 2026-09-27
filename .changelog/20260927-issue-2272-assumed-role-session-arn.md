* [iam/sts] An enforcement denial names a role session by its `assumed-role/<role>/<session>` ARN, as AWS does, instead of the role ARN.
  A role session's `aws:userid` is now the `AssumedRoleId` STS returned for it; `aws:PrincipalArn` stays the role ARN. Sessions issued before this keep the old naming.
