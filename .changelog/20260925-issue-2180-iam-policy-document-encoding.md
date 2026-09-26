*! [iam] Every returned policy document is now URL-encoded per RFC 3986, as AWS does, including trust policies and account details (#2180).
  `Role.AssumeRolePolicyDocument` (CreateRole, GetRole, ListRoles, instance profile roles) and GetAccountAuthorizationDetails' documents were raw JSON.
  migration: a caller that parsed the returned string directly as JSON must URL-decode it first, as on AWS; botocore and the AWS CLI already do.
