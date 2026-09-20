+ [eventbridge] event patterns match on `prefix`, `suffix`, `exists`, `equals-ignore-case`, `numeric` and `anything-but`, not only exact values (#148)
  `anything-but` covers a single value, a list, and a nested `prefix`, `suffix` or `equals-ignore-case` clause
  `cidr`, `wildcard` and `$or` stay unimplemented, and `PutRule` and `TestEventPattern` answer `InvalidEventPatternException` naming the match type rather than storing a rule that never matches
* [eventbridge] `PutRule` now validates the rule trigger, the event bus and the event pattern, each of which used to be accepted unchecked (#148)
  AWS requires at least an `EventPattern` or a `ScheduleExpression`; a rule with neither is a `ValidationException` instead of a rule that can never fire
  a rule naming an event bus that was never created is a `ResourceNotFoundException`, and a pattern whose shape or match type EventBridge does not define is an `InvalidEventPatternException`
* [eventbridge] `DeleteRule` now holds a rule to AWS's ordering: remove its targets before deleting it (#148)
  a rule with targets attached answers `ValidationException`; `Force` is the managed-rule escape AWS documents, not a way around the check
  deleting an `AWS::Events::Rule` through CloudFormation needs no change — the stack detaches the targets it attached, as AWS does
