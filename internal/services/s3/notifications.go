package s3

// notifications.go — S3 event notification dispatcher.
//
// This subscribes to the event bus for S3 object mutations and routes
// matching events to configured SQS queues, SNS topics, and Lambda functions
// based on per-bucket notification configuration.
//
// Architecture:
//
//	S3 handler → bus.Publish(S3ObjectCreated{...})
//	                  ↓  (goroutine — async)
//	    NotificationDispatcher.handle(ctx, event)
//	      → load bucket's NotificationConfig from store
//	      → match event type + key filter rules
//	      → call MessageEnqueuer for each matching queue config
//	      → call TopicPublisher for each matching topic config
//	      → call FunctionInvoker for each matching lambda config
//	      → call BusPublisher once when EventBridge delivery is enabled

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// NotificationDispatcher reads per-bucket notification configs and routes
// matched events to destination sinks (SQS, SNS, Lambda).
type NotificationDispatcher struct {
	store    *s3Store
	enqueuer events.MessageEnqueuer
	topics   events.TopicPublisher  // nil only when wired without SNS (tests)
	invoker  events.FunctionInvoker // nil only when wired without Lambda (tests)
	// auth answers whether the bucket's own service principal may invoke a
	// Lambda destination. nil only when wired without Lambda (tests).
	auth   events.FunctionInvokeAuthorizer
	bus    events.BusPublisher // nil only when wired without EventBridge (tests)
	logger *zap.Logger
	region string
	// accountID owns every bucket here, and is the aws:SourceAccount value a
	// Lambda destination's resource policy may be conditioned on.
	accountID string
}

// NewNotificationDispatcher creates a dispatcher and subscribes it to the
// given event bus for all S3 event types. The returned cancel function
// removes the subscriptions (useful in tests).
//
// topics is nil only when wired without SNS (tests); topic notification
// configs will be skipped in that case. invoker is nil only when wired without
// Lambda (tests); Lambda notification configs will be skipped then. eventBus
// is nil only when wired without EventBridge, and EventBridge configurations
// are skipped then.
func NewNotificationDispatcher(
	store *s3Store,
	enqueuer events.MessageEnqueuer,
	topics events.TopicPublisher,
	invoker events.FunctionInvoker,
	auth events.FunctionInvokeAuthorizer,
	eventBus events.BusPublisher,
	bus *events.Bus,
	logger *zap.Logger,
	region string,
	accountID string,
) (d *NotificationDispatcher, cancel func()) {
	d = &NotificationDispatcher{
		store:     store,
		enqueuer:  enqueuer,
		topics:    topics,
		invoker:   invoker,
		auth:      auth,
		bus:       eventBus,
		logger:    logger,
		region:    region,
		accountID: accountID,
	}

	c1 := bus.Subscribe(events.S3ObjectCreated, d.handle)
	c2 := bus.Subscribe(events.S3ObjectRemoved, d.handle)

	return d, func() { c1(); c2() }
}

// handle is the bus subscriber callback. It runs in its own goroutine.
func (d *NotificationDispatcher) handle(ctx context.Context, e events.Event) {
	p, ok := e.Payload.(events.S3ObjectPayload)
	if !ok {
		return
	}

	cfg, aerr := d.store.getNotificationConfig(ctx, p.Bucket)
	if aerr != nil {
		d.logger.Warn("s3: notification config load failed",
			zap.String("bucket", p.Bucket),
			zap.String("error", aerr.Message),
		)
		return
	}

	eventType := string(e.Type)

	for _, qc := range cfg.QueueConfigurations {
		if !matchesEvent(qc.Events, eventType) {
			continue
		}
		if !matchesFilter(qc.Filter, p.Key) {
			continue
		}

		body := buildNotificationJSON(p, e.Time, qc.ID, d.region)
		queueName := queueNameFromARN(qc.ARN)
		if queueName == "" {
			d.logger.Warn("s3: invalid queue ARN in notification config",
				zap.String("arn", qc.ARN),
			)
			continue
		}

		if err := d.enqueuer.EnqueueRaw(ctx, queueName, body); err != nil {
			d.logger.Warn("s3: notification delivery to SQS failed",
				zap.String("queue", queueName),
				zap.Error(err),
			)
		}
	}

	// SNS delivery. Real S3 publishes the notification JSON as the SNS Message
	// — the {"Records":[…]} document as a string inside the standard SNS
	// notification envelope, with Subject "Amazon S3 Notification" — so an
	// SNS→SQS subscriber parses the envelope's Message back into the S3 event.
	if d.topics != nil {
		for _, tc := range cfg.TopicConfigurations {
			if !matchesEvent(tc.Events, eventType) {
				continue
			}
			if !matchesFilter(tc.Filter, p.Key) {
				continue
			}

			body := buildNotificationJSON(p, e.Time, tc.ID, d.region)
			if err := d.topics.PublishToTopic(ctx, tc.ARN, snsNotificationSubject, body); err != nil {
				d.logger.Warn("s3: notification delivery to SNS failed",
					zap.String("topic", tc.ARN),
					zap.Error(err),
				)
			}
		}
	}

	// EventBridge delivery. AWS sends *every* object event to the default bus
	// while EventBridgeConfiguration is present: there is no per-event
	// selection and no key filter on this destination, so nothing is matched
	// here beyond the configuration being set.
	if d.bus != nil && cfg.EventBridgeConfiguration != nil {
		if entry, ok := buildEventBridgeEntry(p); ok {
			if err := d.bus.PublishBusEvent(ctx, entry); err != nil {
				d.logger.Warn("s3: notification delivery to EventBridge failed",
					zap.String("bucket", p.Bucket),
					zap.Error(err),
				)
			}
		}
	}

	// Lambda delivery
	if d.invoker != nil {
		for _, lc := range cfg.LambdaConfigurations {
			if !matchesEvent(lc.Events, eventType) {
				continue
			}
			if !matchesFilter(lc.Filter, p.Key) {
				continue
			}

			// Real S3 checks the function's resource-based policy when the
			// configuration is put, but a permission removed afterwards makes
			// delivery fail silently rather than un-configuring the bucket.
			// Overcast does the same: log it and move on.
			if aerr := d.authorizeLambda(ctx, lc.ARN, p.Bucket); aerr != nil {
				d.logger.Warn("s3: notification delivery to Lambda refused by the function's resource policy",
					zap.String("function", lc.ARN),
					zap.String("bucket", p.Bucket),
					zap.String("reason", aerr.Message),
				)
				continue
			}

			body := buildNotificationJSON(p, e.Time, lc.ID, d.region)
			if err := d.invoker.InvokeAsync(ctx, lc.ARN, []byte(body)); err != nil {
				d.logger.Warn("s3: notification delivery to Lambda failed",
					zap.String("function", lc.ARN),
					zap.Error(err),
				)
			}
		}
	}
}

// authorizeLambda asks Lambda whether s3.amazonaws.com may invoke this
// destination, supplying the two condition-key values AWS documents an S3
// notification as contributing: the bucket ARN and the bucket's owning account.
//
// https://docs.aws.amazon.com/lambda/latest/dg/with-s3-tutorial.html
func (d *NotificationDispatcher) authorizeLambda(ctx context.Context, functionARN, bucket string) *protocol.AWSError {
	if d.auth == nil {
		return nil
	}
	return d.auth.AuthorizeServiceInvoke(ctx, functionARN, s3ServicePrincipal, protocol.ARN("", "", "s3", bucket), d.accountID)
}

// s3ServicePrincipal is the principal an S3 bucket notification invokes a
// Lambda function as, and the value `add-permission --principal` is given for
// one.
const s3ServicePrincipal = "s3.amazonaws.com"

// snsNotificationSubject is the Subject real S3 sets on every notification it
// publishes to an SNS topic.
const snsNotificationSubject = "Amazon S3 Notification"

// matchesEvent checks whether eventType matches any of the configured events.
// Supports wildcard matching: "s3:ObjectCreated:*" matches "s3:ObjectCreated:*".
func matchesEvent(configured []string, eventType string) bool {
	for _, e := range configured {
		if e == eventType {
			return true
		}
		// "s3:ObjectCreated:*" should match "s3:ObjectCreated:Put" etc.
		if strings.HasSuffix(e, ":*") {
			prefix := strings.TrimSuffix(e, "*")
			if strings.HasPrefix(eventType, prefix) {
				return true
			}
		}
	}
	return false
}

// matchesFilter checks whether key passes the notification filter rules.
// No filter means all keys match (AWS behaviour).
func matchesFilter(f *NotificationFilter, key string) bool {
	if f == nil {
		return true
	}
	for _, rule := range f.Key.Rules {
		switch strings.ToLower(rule.Name) {
		case "prefix":
			if !strings.HasPrefix(key, rule.Value) {
				return false
			}
		case "suffix":
			if !strings.HasSuffix(key, rule.Value) {
				return false
			}
		}
	}
	return true
}

// queueNameFromARN extracts the queue name from an SQS ARN.
// ARN format: arn:aws:sqs:<region>:<account>:<queue-name>.
func queueNameFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}
	return parts[5]
}

// s3NotificationRecord is the AWS S3 event notification JSON schema (v2.1).
type s3NotificationRecord struct {
	EventVersion string    `json:"eventVersion"`
	EventSource  string    `json:"eventSource"`
	AWSRegion    string    `json:"awsRegion"`
	EventTime    time.Time `json:"eventTime"`
	EventName    string    `json:"eventName"`
	S3           s3Detail  `json:"s3"`
}

type s3Detail struct {
	SchemaVersion   string       `json:"s3SchemaVersion"`
	ConfigurationID string       `json:"configurationId"`
	Bucket          s3BucketInfo `json:"bucket"`
	Object          s3ObjectInfo `json:"object"`
}

type s3BucketInfo struct {
	Name string `json:"name"`
	ARN  string `json:"arn"`
}

type s3ObjectInfo struct {
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	ETag      string `json:"eTag"`
	VersionID string `json:"versionId,omitempty"`
	Sequencer string `json:"sequencer,omitempty"`
}

type s3NotificationEnvelope struct {
	Records []s3NotificationRecord `json:"Records"`
}

func buildNotificationJSON(p events.S3ObjectPayload, eventTime time.Time, configID, region string) string {
	env := s3NotificationEnvelope{
		Records: []s3NotificationRecord{
			{
				EventVersion: "2.1",
				EventSource:  "aws:s3",
				AWSRegion:    region,
				EventTime:    eventTime,
				EventName:    p.EventName,
				S3: s3Detail{
					SchemaVersion:   "1.0",
					ConfigurationID: configID,
					Bucket: s3BucketInfo{
						Name: p.Bucket,
						ARN:  "arn:aws:s3:::" + p.Bucket,
					},
					Object: s3ObjectInfo{
						Key:  p.Key,
						Size: p.Size,
						ETag: p.ETag,
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(env)
	return string(raw)
}

// ---- EventBridge envelope --------------------------------------------------
//
// AWS's S3 EventBridge events (docs.aws.amazon.com/AmazonS3/latest/userguide/
// ev-events.html) carry source "aws.s3", the bucket ARN in resources, and a
// detail-type naming the change: "Object Created" or "Object Deleted".
//
// The detail below is deliberately partial. version, bucket.name, object.key,
// object.size, object.etag, object.version-id, object.sequencer, reason and
// deletion-type are all values Overcast genuinely has — version-id and
// sequencer since object version history landed, which is what gives a
// consumer something real to order events by. request-id, requester and
// source-ip-address stay omitted rather than invented: a fabricated request ID
// would look like a real one to a consumer correlating events.

const (
	eventBridgeSource        = "aws.s3"
	eventBridgeObjectCreated = "Object Created"
	eventBridgeObjectDeleted = "Object Deleted"

	// AWS's two deletion-type values. A delete against a versioning-enabled
	// bucket writes a tombstone and removes nothing, which is the distinction
	// this field exists to carry.
	eventBridgeDeletionPermanent   = "Permanently Deleted"
	eventBridgeDeletionMarkerAdded = "Delete Marker Created"
)

// eventBridgeReasons maps the S3 event names Overcast publishes onto the
// API-operation names AWS puts in detail.reason.
var eventBridgeReasons = map[string]string{
	"ObjectCreated:Put":                     "PutObject",
	"ObjectCreated:Post":                    "POST Object",
	"ObjectCreated:Copy":                    "CopyObject",
	"ObjectCreated:CompleteMultipartUpload": "CompleteMultipartUpload",
	"ObjectRemoved:Delete":                  "DeleteObject",
	"ObjectRemoved:DeleteMarkerCreated":     "DeleteObject",
}

type eventBridgeDetail struct {
	Version      string                  `json:"version"`
	Bucket       eventBridgeBucketDetail `json:"bucket"`
	Object       eventBridgeObjectDetail `json:"object"`
	Reason       string                  `json:"reason,omitempty"`
	DeletionType string                  `json:"deletion-type,omitempty"`
}

type eventBridgeBucketDetail struct {
	Name string `json:"name"`
}

type eventBridgeObjectDetail struct {
	Key       string `json:"key"`
	Size      int64  `json:"size"`
	ETag      string `json:"etag,omitempty"`
	VersionID string `json:"version-id,omitempty"`
	Sequencer string `json:"sequencer,omitempty"`
}

// buildEventBridgeEntry renders one object mutation as an EventBridge entry.
// It reports false for an event name outside the created/removed families,
// so an unmapped event is skipped rather than published with an empty
// detail-type that no rule could sensibly match.
func buildEventBridgeEntry(p events.S3ObjectPayload) (events.BusEntry, bool) {
	var detailType string
	switch {
	case strings.HasPrefix(p.EventName, "ObjectCreated:"):
		detailType = eventBridgeObjectCreated
	case strings.HasPrefix(p.EventName, "ObjectRemoved:"):
		detailType = eventBridgeObjectDeleted
	default:
		return events.BusEntry{}, false
	}

	detail := eventBridgeDetail{
		Version: "0",
		Bucket:  eventBridgeBucketDetail{Name: p.Bucket},
		Object: eventBridgeObjectDetail{
			Key:       p.Key,
			Size:      p.Size,
			ETag:      unquoteETag(p.ETag),
			VersionID: p.VersionID,
			Sequencer: p.Sequencer,
		},
		Reason: eventBridgeReasons[p.EventName],
	}
	if detailType == eventBridgeObjectDeleted {
		detail.DeletionType = eventBridgeDeletionPermanent
		if p.EventName == "ObjectRemoved:DeleteMarkerCreated" {
			detail.DeletionType = eventBridgeDeletionMarkerAdded
		}
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		return events.BusEntry{}, false
	}
	return events.BusEntry{
		Source:     eventBridgeSource,
		DetailType: detailType,
		Detail:     string(raw),
		Resources:  []string{"arn:aws:s3:::" + p.Bucket},
	}, true
}
