*! [iam] A user or role `PermissionsBoundaryType` is now `PermissionsBoundaryPolicy`, the value AWS sends, not the member name `Policy` (#1920).
  migration: code that compared the type to `"Policy"` must compare to `"PermissionsBoundaryPolicy"`, as against AWS; typed SDK enums now parse it.
