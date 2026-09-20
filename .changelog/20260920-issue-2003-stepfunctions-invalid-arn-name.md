*. [stepfunctions] malformed state machine input is refused as AWS refuses it, rather than reported as a state machine that does not exist (#2003).
  `CreateStateMachine` answers `InvalidName` for a name outside AWS's charset — whitespace, brackets, wildcards, reserved specials, control characters, over 80 characters
  it answers `InvalidArn` for a `roleArn` that is not an IAM role ARN, as do the six operations taking a `stateMachineArn` when given something that is not a state machine ARN
  a well-formed ARN naming a state machine that does not exist still answers `StateMachineDoesNotExist`
