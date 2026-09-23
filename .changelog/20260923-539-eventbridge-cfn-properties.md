* [cloudformation/eventbridge] AWS::Events::Rule and AWS::Events::EventBus now forward Tags.
  EventBus properties with no service member (Description, DeadLetterConfig,
  KmsKeyIdentifier, Policy) are reported as unconsumed instead of dropped silently.
