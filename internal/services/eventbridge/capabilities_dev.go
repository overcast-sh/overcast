//go:build dev

package eventbridge

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Event buses
		capabilities.Capability{Service: "eventbridge", Operation: "CreateEventBus", Category: "Event buses", Status: capabilities.StatusSupported, Notes: "Creates a custom event bus; stores Description, DeadLetterConfig and KmsKeyIdentifier"},
		capabilities.Capability{Service: "eventbridge", Operation: "DescribeEventBus", Category: "Event buses", Status: capabilities.StatusSupported, Notes: "Returns bus details including Description, DeadLetterConfig, KmsKeyIdentifier and the Policy built from PutPermission grants; synthetic default bus"},
		capabilities.Capability{Service: "eventbridge", Operation: "ListEventBuses", Category: "Event buses", Status: capabilities.StatusSupported, Notes: "Always includes default bus"},
		capabilities.Capability{Service: "eventbridge", Operation: "DeleteEventBus", Category: "Event buses", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "eventbridge", Operation: "PutPermission", Category: "Event buses", Status: capabilities.StatusSupported, Notes: "Stores a resource policy statement on the bus (or replaces it wholesale via the Policy parameter), returned by DescribeEventBus as Policy; stored, not enforced, the same as the rest of Overcast's IAM policies"},
		capabilities.Capability{Service: "eventbridge", Operation: "RemovePermission", Category: "Event buses", Status: capabilities.StatusSupported, Notes: "Removes one statement by StatementId, or the whole policy with RemoveAllPermissions"},
		// Rules
		capabilities.Capability{Service: "eventbridge", Operation: "PutRule", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Creates or updates a rule; refuses a rule with neither EventPattern nor ScheduleExpression, an unknown event bus, and an event pattern that is malformed or uses cidr, wildcard or $or"},
		capabilities.Capability{Service: "eventbridge", Operation: "DescribeRule", Category: "Rules", Status: capabilities.StatusSupported},
		capabilities.Capability{Service: "eventbridge", Operation: "ListRules", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Lists rules for a bus"},
		capabilities.Capability{Service: "eventbridge", Operation: "EnableRule", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Sets rule state to ENABLED"},
		capabilities.Capability{Service: "eventbridge", Operation: "DisableRule", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Sets rule state to DISABLED"},
		capabilities.Capability{Service: "eventbridge", Operation: "DeleteRule", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Refuses a rule that still has targets, as AWS does; Force is the managed-rule escape only and does not bypass that"},
		capabilities.Capability{Service: "eventbridge", Operation: "TestEventPattern", Category: "Rules", Status: capabilities.StatusSupported, Notes: "Evaluates an event against a pattern with the matcher rule delivery uses; malformed patterns and the cidr, wildcard and $or match types are InvalidEventPatternException, mandatory envelope fields are not enforced"},
		// Targets
		capabilities.Capability{Service: "eventbridge", Operation: "PutTargets", Category: "Targets", Status: capabilities.StatusSupported, Notes: "Adds Lambda, SQS, SNS, Step Functions, Kinesis, Firehose, ECS and event-bus targets; rejects other target types at add time"},
		capabilities.Capability{Service: "eventbridge", Operation: "ListTargetsByRule", Category: "Targets", Status: capabilities.StatusSupported, Notes: "Lists targets including input transformers and ECS/Kinesis/SQS target parameters"},
		capabilities.Capability{Service: "eventbridge", Operation: "RemoveTargets", Category: "Targets", Status: capabilities.StatusSupported, Notes: "Removes targets from a rule"},
		// Events
		capabilities.Capability{Service: "eventbridge", Operation: "PutEvents", Category: "Events", Status: capabilities.StatusSupported, Notes: "Delivers matching rules to Lambda, SQS, SNS, Step Functions, Kinesis, Firehose, ECS and event-bus targets, applying InputPath/InputTransformer and RetryPolicy/DLQ; patterns match on exact values plus prefix, suffix, exists, equals-ignore-case, numeric and anything-but"},
		// Tags
		capabilities.Capability{Service: "eventbridge", Operation: "TagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Tag buses and rules"},
		capabilities.Capability{Service: "eventbridge", Operation: "ListTagsForResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "List tags for a resource"},
		capabilities.Capability{Service: "eventbridge", Operation: "UntagResource", Category: "Tags", Status: capabilities.StatusSupported, Notes: "Removes tags from a resource"},
		// Archives
		capabilities.Capability{Service: "eventbridge", Operation: "CreateArchive", Category: "Archives", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "DescribeArchive", Category: "Archives", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "ListArchives", Category: "Archives", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "DeleteArchive", Category: "Archives", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		// Replays
		capabilities.Capability{Service: "eventbridge", Operation: "StartReplay", Category: "Replays", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "DescribeReplay", Category: "Replays", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "ListReplays", Category: "Replays", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		// Connections
		capabilities.Capability{Service: "eventbridge", Operation: "CreateConnection", Category: "Connections", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "DescribeConnection", Category: "Connections", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "ListConnections", Category: "Connections", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
		capabilities.Capability{Service: "eventbridge", Operation: "DeleteConnection", Category: "Connections", Status: capabilities.StatusUnsupported, Notes: "Returns 501", DocOnly: true},
	)
}
