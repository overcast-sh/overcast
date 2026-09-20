~! [cloudformation/iam] `Ref` on an `AWS::IAM::InstanceProfile` is the profile name, as AWS documents it; the ARN is `Fn::GetAtt "Arn"`
  migration: a template passing the `Ref` somewhere an ARN was wanted needs `Fn::GetAtt` instead — the reverse of what it needed before
*! [iam] an instance profile holds one role, AWS's quota, and one still holding a role refuses to delete
  `AddRoleToInstanceProfile` answers `LimitExceeded` for a second, different role; `DeleteInstanceProfile` answers `DeleteConflict`
  migration: `RemoveRoleFromInstanceProfile` before replacing a profile's role or deleting the profile, as against real AWS
*! [cloudformation/iam] an `AWS::IAM::Policy` naming no `Groups`, `Roles` or `Users` fails the stack instead of writing its document to nobody
  AWS requires at least one of the three, and the resource does nothing at all without one
  migration: name the principal the policy is for; a stack that reported CREATE_COMPLETE without one was granting nothing
* [cloudformation/iam] `Fn::GetAtt "Arn"` keeps a role's or instance profile's `Path` across a stack update
* [iam] `ListRolePolicies`, `ListUserPolicies` and `ListGroupPolicies` return inline-policy names in a stable order
