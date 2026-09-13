//go:build dev

package logs

import "github.com/overcast-sh/overcast/internal/capabilities"

func init() {
	capabilities.Default.Register(
		// Log groups
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "CreateLogGroup", Category: "Log groups", Status: capabilities.StatusSupported, Notes: "Validates name; returns error on duplicate; applies create-time `tags` atomically with the group (`kmsKeyId`, `logGroupClass` and `deletionProtectionEnabled` are accepted but ignored)"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DescribeLogGroups", Category: "Log groups", Status: capabilities.StatusSupported, Notes: "Optional `logGroupNamePrefix` filter; `limit` (default and maximum 50) and `nextToken` page the ASCII-sorted result"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DeleteLogGroup", Category: "Log groups", Status: capabilities.StatusSupported, Notes: "Deletes group and all streams/events"},
		// Log streams
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "CreateLogStream", Category: "Log streams", Status: capabilities.StatusSupported, Notes: "Validates group exists; returns error on duplicate"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DescribeLogStreams", Category: "Log streams", Status: capabilities.StatusSupported, Notes: "Optional `logStreamNamePrefix` filter; `limit` (default and maximum 50) and `nextToken` page the result (`orderBy`/`descending` are accepted but ignored)"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DeleteLogStream", Category: "Log streams", Status: capabilities.StatusSupported, Notes: "Deletes stream and all its events"},
		// Log events
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "PutLogEvents", Category: "Log events", Status: capabilities.StatusSupported, Notes: "Accepts a batch of events and sets ingestion time; an event more than 2 hours ahead, older than 14 days or older than the group's retention is discarded and reported in `rejectedLogEventsInfo` behind a 200; a batch that is not in chronological order is refused with `InvalidParameterException`"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "GetLogEvents", Category: "Log events", Status: capabilities.StatusSupported, Notes: "startTime/endTime filtering; startFromHead"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "FilterLogEvents", Category: "Log events", Status: capabilities.StatusSupported, Notes: "Text patterns (AND, quoted, ?OR), JSON patterns (`{ $.field op value }` with `&&`/`||`, EXISTS, IS NULL), space-delimited patterns (`[col, col = val, ...]` with `*` glob, `%regex%`, numeric ops, `&&`/`||`, ellipsis); time range, stream name/prefix; each event carries an `eventId` that resolves through GetLogRecord"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "GetLogRecord", Category: "Log events", Status: capabilities.StatusPartial, Notes: "Resolves an `eventId` returned by FilterLogEvents to that event's `@message`, `@timestamp`, `@ingestionTime`, `@log` and `@logStream`; a Logs Insights `@ptr` cannot be produced here because StartQuery is unimplemented, and a structured message is not split into per-field entries"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "StartLiveTail", Category: "Log events", Status: capabilities.StatusSupported, Notes: "AWS event-stream response opening with initial-response, then sessionStart/sessionUpdate; supports group identifiers, stream names/prefixes, and filter patterns"},
		// Insights
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "StartQuery", Category: "Insights", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "GetQueryResults", Category: "Insights", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Metric filters
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "PutMetricFilter", Category: "Metric filters", Status: capabilities.StatusSupported, Notes: "Creates or replaces a filter (100 per group); every accepted log event, from `PutLogEvents` or a Lambda function's output, that matches the pattern publishes a CloudWatch datapoint with the transformation's namespace, name, `metricValue` (a number or a `$field`/`$.field` reference), `dimensions` and `unit`, at the event's timestamp; `defaultValue` is published once per one-minute period that ingested events but none that matched, decided per batch; `applyOnTransformedLogs` is stored and ignored (no log transformers); nothing is published while service metrics are disabled"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DescribeMetricFilters", Category: "Metric filters", Status: capabilities.StatusSupported, Notes: "Optional `logGroupName`, `filterNamePrefix` (honoured only with `logGroupName`), and `metricName` + `metricNamespace` (required together); `limit` (default and maximum 50) and `nextToken` page the name-sorted result"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DeleteMetricFilter", Category: "Metric filters", Status: capabilities.StatusSupported, Notes: "Deletes one filter; deleting a log group deletes its filters"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "TestMetricFilter", Category: "Metric filters", Status: capabilities.StatusSupported, Notes: "Runs a pattern over 1–50 sample messages; `matches` carries the zero-based `eventNumber`, the message and `extractedValues` — every column of a space-delimited pattern (unnamed ones as `$1`…), the properties a JSON pattern selects (AWS documents no JSON example; this mirrors the space-delimited rule), or `{}` for a text pattern"},
		// Retention
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "PutRetentionPolicy", Category: "Retention", Status: capabilities.StatusSupported, Notes: "Sets retentionInDays on log group; values outside AWS's documented set are rejected with `InvalidParameterException` before any state change"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "DeleteRetentionPolicy", Category: "Retention", Status: capabilities.StatusSupported, Notes: "Clears retention (sets to 0)"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "PutSubscriptionFilter", Category: "Retention", Status: capabilities.StatusUnsupported, Notes: "stub; returns 501"},
		// Tagging
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "TagLogGroup", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Adds tags to a log group; enforces AWS's key/value length, reserved `aws:` prefix and 50-tag limits before mutating"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "UntagLogGroup", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Removes tags from a log group"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "ListTagsLogGroup", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Returns tags for a log group"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "TagResource", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Modern, ARN-addressed sibling of TagLogGroup (#1195); resolves `resourceArn` to a log group and shares its validation and storage"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "UntagResource", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Modern, ARN-addressed sibling of UntagLogGroup (#1195)"},
		capabilities.Capability{Service: "cloudwatch-logs", Operation: "ListTagsForResource", Category: "Tagging", Status: capabilities.StatusSupported, Notes: "Modern, ARN-addressed sibling of ListTagsLogGroup (#1195)"},
	)
}
