* [cloudformation/wafv2] `AWS::WAFv2::WebACL` dropped `Description` and `Tags`, and `Fn::GetAtt` `Arn`/`Id` never resolved.
  `CreateWebACL`'s response already carried both attributes; the handler just discarded them.
* [cloudformation/autoscaling] `AWS::AutoScaling::AutoScalingGroup` dropped `Cooldown`, `HealthCheckType` and two more, on create and update.
  `HealthCheckGracePeriod` and `TerminationPolicies` were the rest.
* [cloudformation/autoscaling] `AWS::AutoScaling::LaunchConfiguration` dropped `KeyName`, `IamInstanceProfile` and `UserData`.
