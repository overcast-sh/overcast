package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/mcp"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/services/acm"
	"github.com/overcast-sh/overcast/internal/services/dynamodb"
	"github.com/overcast-sh/overcast/internal/services/ecr"
	"github.com/overcast-sh/overcast/internal/services/ecs"
	"github.com/overcast-sh/overcast/internal/services/iam"
	"github.com/overcast-sh/overcast/internal/services/kinesis"
	"github.com/overcast-sh/overcast/internal/services/kms"
	"github.com/overcast-sh/overcast/internal/services/lambda"
	"github.com/overcast-sh/overcast/internal/services/secretsmanager"
	"github.com/overcast-sh/overcast/internal/services/sns"
	"github.com/overcast-sh/overcast/internal/services/ssm"
	"github.com/overcast-sh/overcast/internal/services/stepfunctions"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// RuntimeProvider exposes live-instance tools and resources to MCP clients
// connecting directly to a running Overcast instance at /_overcast/mcp.
//
// The runtime MCP is currently local-only (protected by Origin validation in
// the shared server) and does not require authentication. This posture is
// temporary and must be revisited before any remote/production exposure.
type RuntimeProvider struct {
	cfg   *config.Config
	store state.Store

	// router is the emulator's root HTTP handler, wired late via SetRouter —
	// see its doc comment. A tool that needs a service's own validation,
	// defaulting or clock (RequestCertificate's domain checks and
	// DomainValidationOptions, for instance) dispatches through it via
	// invokeJSON rather than re-implementing that logic against the store
	// directly. nil until SetRouter runs, in which case such a tool reports
	// that the runtime router is unavailable rather than silently degrading.
	router http.Handler

	recentEventsMu         sync.RWMutex
	recentEvents           []map[string]any
	recentEventBufferLimit int
	recentEventsSubscribed bool
	resourceListChangedCB  func()
	resourceUpdatedCB      func(uri string)
}

const defaultRuntimeRecentEventBufferLimit = 200

// NewRuntimeProvider creates a RuntimeProvider backed by the given config and
// state store. Both may be nil (in which case affected tools return empty data).
func NewRuntimeProvider(cfg *config.Config, store state.Store) *RuntimeProvider {
	return &RuntimeProvider{
		cfg:                    cfg,
		store:                  store,
		recentEventBufferLimit: defaultRuntimeRecentEventBufferLimit,
	}
}

// SetRouter wires the emulator's root HTTP handler for tools that dispatch a
// real service operation instead of manipulating the store directly. It is
// called once, after the router is fully constructed (internal/router.New),
// mirroring how CloudFormation's provisioner receives its own router late —
// see cloudformation's initRouter — to avoid a circular dependency between
// building the router and the services it mounts. A RuntimeProvider used
// without ever calling SetRouter (as in most unit tests) simply cannot use
// the tools that need it.
func (p *RuntimeProvider) SetRouter(router http.Handler) {
	p.router = router
}

// invokeJSON dispatches an AWS JSON 1.1 X-Amz-Target request into the
// emulator's own router, exactly as a real SDK call would arrive, and returns
// the raw response body. A non-2xx response becomes an error carrying the
// service's own exception text, so a caller seeing it back gets the same
// answer the wire API would.
func (p *RuntimeProvider) invokeJSON(ctx context.Context, target string, body any) ([]byte, error) {
	if p.router == nil {
		return nil, fmt.Errorf("runtime router is unavailable")
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	req.Header.Set("X-Overcast-Region", p.defaultRegion())
	rec := httptest.NewRecorder()
	p.router.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", target, rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	return rec.Body.Bytes(), nil
}

// AttachEventBus subscribes the runtime provider to the shared event bus so
// runtime_get_recent_events can return a bounded in-memory history.
func (p *RuntimeProvider) AttachEventBus(bus *events.Bus) {
	if bus == nil {
		return
	}
	p.recentEventsMu.Lock()
	if p.recentEventsSubscribed {
		p.recentEventsMu.Unlock()
		return
	}
	p.recentEventsSubscribed = true
	p.recentEventsMu.Unlock()

	bus.SubscribeAll(func(_ context.Context, e events.Event) {
		p.recordRecentEvent(e)
	})
}

// SetResourceListChangedEmitter configures a callback used to emit
// notifications/resources/list_changed when runtime service events indicate
// resource inventory changes that happened outside MCP tool calls.
func (p *RuntimeProvider) SetResourceListChangedEmitter(cb func()) {
	p.recentEventsMu.Lock()
	p.resourceListChangedCB = cb
	p.recentEventsMu.Unlock()
}

// SetResourceUpdatedEmitter configures a callback used to emit
// notifications/resources/updated for concrete resource URIs when runtime
// service events provide enough identity information.
func (p *RuntimeProvider) SetResourceUpdatedEmitter(cb func(uri string)) {
	p.recentEventsMu.Lock()
	p.resourceUpdatedCB = cb
	p.recentEventsMu.Unlock()
}

func (p *RuntimeProvider) recordRecentEvent(e events.Event) {
	payload := any(nil)
	if e.Payload != nil {
		rawPayload, err := json.Marshal(e.Payload)
		if err != nil {
			payload = map[string]any{"error": fmt.Sprintf("unable to marshal payload (%T)", e.Payload)}
		} else {
			var decoded any
			if err := json.Unmarshal(rawPayload, &decoded); err != nil {
				payload = map[string]any{"raw": string(rawPayload)}
			} else {
				payload = decoded
			}
		}
	}

	record := map[string]any{
		"type":    string(e.Type),
		"time":    e.Time.UTC().Format(time.RFC3339Nano),
		"source":  e.Source,
		"payload": payload,
	}
	if e.ResourceARN != "" {
		record["resourceArn"] = e.ResourceARN
	}

	p.recentEventsMu.Lock()
	p.recentEvents = append(p.recentEvents, record)
	if limit := p.recentEventBufferLimit; limit > 0 && len(p.recentEvents) > limit {
		p.recentEvents = append([]map[string]any(nil), p.recentEvents[len(p.recentEvents)-limit:]...)
	}
	p.recentEventsMu.Unlock()

	for _, uri := range p.resourceUpdatedURIsFromEvent(e) {
		p.emitResourceUpdated(uri)
	}

	if p.shouldEmitResourceListChangedFromEvent(e) {
		p.emitResourceListChanged()
	}
}

func (p *RuntimeProvider) emitResourceListChanged() {
	p.recentEventsMu.RLock()
	cb := p.resourceListChangedCB
	p.recentEventsMu.RUnlock()
	if cb != nil {
		cb()
	}
}

func (p *RuntimeProvider) emitResourceUpdated(uri string) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return
	}
	p.recentEventsMu.RLock()
	cb := p.resourceUpdatedCB
	p.recentEventsMu.RUnlock()
	if cb != nil {
		cb(uri)
	}
}

func (p *RuntimeProvider) shouldEmitResourceListChangedFromEvent(e events.Event) bool {
	source := strings.ToLower(strings.TrimSpace(e.Source))
	if source == "" {
		return false
	}
	return runtimeEventChangesResourceList(e.Type)
}

func runtimeEventChangesResourceList(eventType events.Type) bool {
	kind := strings.ToLower(strings.TrimSpace(string(eventType)))
	if kind == "" {
		return false
	}
	keywords := []string{
		"created",
		"deleted",
		"removed",
		"registered",
		"deregistered",
	}
	for _, kw := range keywords {
		if strings.Contains(kind, kw) {
			return true
		}
	}
	return false
}

func (p *RuntimeProvider) resourceUpdatedURIsFromEvent(e events.Event) []string {
	source := strings.ToLower(strings.TrimSpace(e.Source))
	if source == "" {
		return nil
	}

	name, arn := runtimeEventResourceIdentity(e.Payload)
	region := runtimeRegionFromARNOrDefault(arn, p.defaultRegion())
	etype := strings.ToLower(strings.TrimSpace(string(e.Type)))

	uris := make([]string, 0, 2)
	switch source {
	case "s3":
		if name != "" {
			uris = append(uris, "oc://s3/buckets/"+url.PathEscape(name))
		}
	case "sqs":
		if name != "" {
			uris = append(uris, "oc://sqs/queues/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "dynamodb":
		if name != "" {
			uris = append(uris, "oc://dynamodb/tables/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "sns":
		if name != "" {
			uris = append(uris, "oc://sns/topics/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "kinesis":
		if name != "" {
			uris = append(uris, "oc://kinesis/streams/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "kms":
		if name != "" {
			uris = append(uris, "oc://kms/keys/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "stepfunctions":
		if name != "" {
			if strings.Contains(etype, "execution") {
				uris = append(uris, "oc://stepfunctions/executions/"+url.PathEscape(region)+"/"+url.PathEscape(name))
			} else {
				uris = append(uris, "oc://stepfunctions/state-machines/"+url.PathEscape(region)+"/"+url.PathEscape(name))
			}
		}
	case "ssm":
		if name != "" {
			uris = append(uris, "oc://ssm/parameters/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "secretsmanager":
		if name != "" {
			uris = append(uris, "oc://secretsmanager/secrets/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "acm":
		if name != "" {
			uris = append(uris, "oc://acm/certificates/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "iam":
		if name != "" {
			switch {
			case strings.Contains(etype, "role"):
				uris = append(uris, "oc://iam/roles/"+url.PathEscape(name))
			case strings.Contains(etype, "policy"):
				uris = append(uris, "oc://iam/policies/"+url.PathEscape(name))
			case strings.Contains(etype, "group"):
				uris = append(uris, "oc://iam/groups/"+url.PathEscape(name))
			default:
				uris = append(uris, "oc://iam/users/"+url.PathEscape(name))
			}
		}
	case "lambda":
		if name != "" {
			uris = append(uris, "oc://lambda/functions/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "ecr":
		if name != "" {
			uris = append(uris, "oc://ecr/repositories/"+url.PathEscape(region)+"/"+url.PathEscape(name))
		}
	case "ecs":
		if name != "" {
			switch {
			case strings.Contains(etype, "cluster"):
				uris = append(uris, "oc://ecs/clusters/"+url.PathEscape(region)+"/"+url.PathEscape(name))
			case strings.Contains(etype, "taskdefinition"):
				uris = append(uris, "oc://ecs/task-definitions")
			}
		}
	}

	return uniqueRuntimeURIs(uris)
}

func runtimeEventResourceIdentity(payload any) (name string, arn string) {
	switch v := payload.(type) {
	case events.ResourcePayload:
		return strings.TrimSpace(v.Name), strings.TrimSpace(v.ARN)
	case *events.ResourcePayload:
		if v == nil {
			return "", ""
		}
		return strings.TrimSpace(v.Name), strings.TrimSpace(v.ARN)
	case events.LambdaFunctionPayload:
		return strings.TrimSpace(v.Name), strings.TrimSpace(v.ARN)
	case *events.LambdaFunctionPayload:
		if v == nil {
			return "", ""
		}
		return strings.TrimSpace(v.Name), strings.TrimSpace(v.ARN)
	case map[string]any:
		nameValue, _ := v["name"].(string)
		arnValue, _ := v["arn"].(string)
		return strings.TrimSpace(nameValue), strings.TrimSpace(arnValue)
	default:
		return "", ""
	}
}

func runtimeRegionFromARNOrDefault(arn string, fallback string) string {
	arn = strings.TrimSpace(arn)
	if arn == "" {
		return fallback
	}
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) >= 4 && strings.TrimSpace(parts[3]) != "" {
		return strings.TrimSpace(parts[3])
	}
	return fallback
}

func uniqueRuntimeURIs(uris []string) []string {
	if len(uris) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(uris))
	out := make([]string, 0, len(uris))
	for _, uri := range uris {
		uri = strings.TrimSpace(uri)
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}
		out = append(out, uri)
	}
	return out
}

func (p *RuntimeProvider) Tools() []mcp.Tool {
	return append([]mcp.Tool{
		{
			Name:        "runtime_instance_info",
			Description: "Return configuration and capability metadata for this running Overcast instance, including region, account ID, port, and enabled services.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"region":           {"type": "string"},
					"account_id":       {"type": "string"},
					"host":             {"type": "string"},
					"port":             {"type": "integer"},
					"hostname":         {"type": "string"},
					"state_backend":    {"type": "string"},
					"log_level":        {"type": "string"},
					"enabled_services": {"type": "array", "items": {"type": "string"}}
				},
				"required": ["region", "account_id", "port", "enabled_services"]
			}`),
		},
		{
			Name:        "runtime_list_services",
			Description: "List services enabled in this Overcast instance with their configured state backend and a count of stored state keys.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"services": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"name":          {"type": "string"},
								"state_backend": {"type": "string"},
								"key_count":     {"type": "integer"}
							}
						}
					}
				}
			}`),
		},
		{
			Name:        "runtime_inventory",
			Description: "Return enabled services and a per-service resource inventory. Uses typed runtime resources when available and falls back to state-key resources for services without typed inventory.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"service": {"type": "string", "description": "Optional service filter."},
					"limit_per_service": {"type": "integer", "description": "Maximum resources per service (default 50, max 200)."},
					"include_collections": {"type": "boolean", "description": "Include aggregate collection URIs (default false)."}
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"region": {"type": "string"},
					"enabled_services": {"type": "array", "items": {"type": "string"}},
					"services": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"service": {"type": "string"},
								"resource_count": {"type": "integer"},
								"truncated": {"type": "boolean"},
								"resources": {
									"type": "array",
									"items": {
										"type": "object",
										"properties": {
											"source": {"type": "string"},
											"uri": {"type": "string"},
											"name": {"type": "string"},
											"description": {"type": "string"},
											"mimeType": {"type": "string"},
											"key": {"type": "string"}
										}
									}
								}
							}
						}
					}
				}
			}`),
		},
		{
			Name:        "runtime_get_health",
			Description: "Return instance health metadata equivalent to the runtime health endpoint payload.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"status":{"type":"string"},
					"timestamp":{"type":"string"},
					"version":{"type":"string"},
					"services":{"type":"array","items":{"type":"string"}}
				},
				"required":["status","timestamp","services"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			Name:        "runtime_get_config",
			Description: "Return instance debug configuration metadata when debug mode is enabled.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"debug_required":{"type":"boolean"},
					"host":{"type":"string"},
					"port":{"type":"integer"},
					"services":{"type":"object"},
					"state":{"type":"string"},
					"serviceStates":{"type":"object"},
					"data_dir":{"type":"string"},
					"region":{"type":"string"},
					"account_id":{"type":"string"},
					"log_level":{"type":"string"},
					"debug":{"type":"boolean"},
					"tls_enabled":{"type":"boolean"}
				},
				"required":["debug_required"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			Name:        "runtime_get_service_state",
			Description: "Return bounded service state view for a namespace or for enabled namespaces.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"namespace":{"type":"string"},
					"key_pattern":{"type":"string"},
					"limit":{"type":"integer"}
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"namespace":{"type":"string"},
					"debug_required":{"type":"boolean"},
					"limit":{"type":"integer"},
					"count":{"type":"integer"},
					"truncated":{"type":"boolean"},
					"entries":{}
				},
				"required":["namespace","limit","count","truncated","entries"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			Name:        "runtime_get_recent_events",
			Description: "Return a bounded newest-first slice of recent internal runtime events captured from the shared event bus.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"source":{"type":"string","description":"Optional exact source filter (case-insensitive), e.g. s3."},
					"type":{"type":"string","description":"Optional exact event type filter (case-insensitive), e.g. s3:BucketCreated."},
					"limit":{"type":"integer","description":"Maximum events to return (default 50, max 200)."}
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"limit":{"type":"integer"},
					"count":{"type":"integer"},
					"truncated":{"type":"boolean"},
					"events":{"type":"array","items":{"type":"object"}}
				},
				"required":["limit","count","truncated","events"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution:   map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			Name:        "runtime_state_scan",
			Description: "Scan key-value pairs stored for a given AWS service namespace in this Overcast instance. Results are bounded to 200 entries.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"service": {"type": "string", "description": "Lowercase service name (e.g. 's3', 'sqs', 'dynamodb')."},
					"prefix":  {"type": "string", "description": "Key prefix filter. Empty string returns all keys."},
					"limit":   {"type": "integer", "description": "Maximum number of entries to return (1–200). Defaults to 50."}
				},
				"required": ["service"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"service":   {"type": "string"},
					"prefix":    {"type": "string"},
					"truncated": {"type": "boolean"},
					"entries": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"key":   {"type": "string"},
								"value": {"type": "string"}
							}
						}
					}
				}
			}`),
		},
		{
			Name:        "runtime_probe_kv_store",
			Description: "Probe bounded key/value state from one namespace or across enabled-service namespaces with filtering and cursor paging.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace":      {"type": "string", "description": "Optional exact namespace (e.g. s3:buckets). If omitted, scans enabled-service namespaces."},
					"key_pattern":    {"type": "string", "description": "Optional case-insensitive substring filter applied to keys."},
					"limit":          {"type": "integer", "description": "Maximum entries to return (default 50, max 500)."},
					"cursor":         {"type": "string", "description": "Opaque cursor token from a previous response's next_cursor."},
					"include_values": {"type": "boolean", "description": "Include full values in entries when true."},
					"preview_bytes":  {"type": "integer", "description": "Maximum preview length per value (default 120, max 2048)."}
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"namespace":     {"type": "string"},
					"limit":         {"type": "integer"},
					"cursor":        {"type": "string"},
					"next_cursor":   {"type": "string"},
					"count":         {"type": "integer"},
					"total_matched": {"type": "integer"},
					"truncated":     {"type": "boolean"},
					"entries": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"namespace":     {"type": "string"},
								"key":           {"type": "string"},
								"value_preview": {"type": "string"},
								"value":         {}
							},
							"required": ["namespace", "key"]
						}
					}
				},
				"required": ["limit", "cursor", "count", "total_matched", "truncated", "entries"]
			}`),
			Annotations: map[string]any{"readOnlyHint": true},
			Execution: map[string]any{
				"readOnlyHint":    true,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_s3_create_bucket",
			Description: "Create an S3 bucket in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"bucket":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","bucket","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_s3_put_bucket_tags",
			Description: "Update/replace S3 bucket tags in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name","tags"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"bucket":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["updated","bucket","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_s3_delete_bucket",
			Description: "Delete an S3 bucket in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sqs_create_queue",
			Description: "Create an SQS queue in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"attributes":{"type":"object","additionalProperties":{"type":"string"}},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"queue":{"type":"object"},
					"uri":{"type":"string"},
					"queue_url":{"type":"string"}
				},
				"required":["created","queue","uri","queue_url"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sqs_set_queue_attributes",
			Description: "Set SQS queue attributes in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"attributes":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name","attributes"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"queue":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["updated","queue","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sqs_delete_queue",
			Description: "Delete an SQS queue in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sqs_purge_queue",
			Description: "Purge all messages from an SQS queue in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"purged":{"type":"boolean"},
					"messages_cleared":{"type":"integer"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["purged","messages_cleared","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_dynamodb_create_table",
			Description: "Create a DynamoDB table in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table_name":{"type":"string"},
					"region":{"type":"string"},
					"key_schema":{"type":"array"},
					"attribute_definitions":{"type":"array"},
					"billing_mode":{"type":"string"},
					"provisioned_throughput":{"type":"object"},
					"stream_specification":{"type":"object"},
					"global_secondary_indexes":{"type":"array"},
					"local_secondary_indexes":{"type":"array"}
				},
				"required":["table_name","key_schema","attribute_definitions"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"table":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","table","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_dynamodb_update_table_ttl",
			Description: "Update a DynamoDB table TTL configuration in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table_name":{"type":"string"},
					"region":{"type":"string"},
					"time_to_live_specification":{
						"type":"object",
						"properties":{
							"Enabled":{"type":"boolean"},
							"AttributeName":{"type":"string"}
						},
						"required":["Enabled"]
					}
				},
				"required":["table_name","time_to_live_specification"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"table":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["updated","table","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sns_create_topic",
			Description: "Create an SNS topic in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"attributes":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"topic":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","topic","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sns_set_topic_attributes",
			Description: "Set SNS topic attributes in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"attributes":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name","attributes"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"topic":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["updated","topic","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_sns_delete_topic",
			Description: "Delete an SNS topic in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kinesis_create_stream",
			Description: "Create a Kinesis stream in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"shard_count":{"type":"integer"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"stream":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","stream","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kinesis_put_record",
			Description: "Put a record into a Kinesis stream in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"stream_name":{"type":"string"},
					"region":{"type":"string"},
					"partition_key":{"type":"string"},
					"data":{"type":"string"}
				},
				"required":["stream_name","partition_key","data"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"stored":{"type":"boolean"},
					"stream_name":{"type":"string"},
					"sequence_number":{"type":"string"},
					"shard_id":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["stored","stream_name","sequence_number","shard_id","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kinesis_delete_stream",
			Description: "Delete a Kinesis stream in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kms_create_key",
			Description: "Create a KMS key in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"region":{"type":"string"},
					"description":{"type":"string"},
					"key_spec":{"type":"string"},
					"key_usage":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				}
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"key":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","key","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kms_disable_key",
			Description: "Disable a KMS key in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"key_id":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["key_id"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"key":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["updated","key","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_kms_schedule_key_deletion",
			Description: "Schedule a KMS key for deletion in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"key_id":{"type":"string"},
					"region":{"type":"string"},
					"pending_window_in_days":{"type":"integer"}
				},
				"required":["key_id"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"scheduled":{"type":"boolean"},
					"key":{"type":"object"},
					"deletion_date":{"type":"string","format":"date-time"},
					"uri":{"type":"string"}
				},
				"required":["deleted","scheduled","key","deletion_date","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_stepfunctions_create_state_machine",
			Description: "Create a Step Functions state machine in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"definition":{"type":"string"},
					"role_arn":{"type":"string"},
					"type":{"type":"string"}
				},
				"required":["name","definition","role_arn"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"state_machine":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","state_machine","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_stepfunctions_start_execution",
			Description: "Start a Step Functions execution in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"input":{"type":"string"},
					"execution_name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"started":{"type":"boolean"},
					"execution":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["started","execution","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_stepfunctions_delete_state_machine",
			Description: "Delete a Step Functions state machine in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_ssm_put_parameter",
			Description: "Create or overwrite an SSM parameter in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"value":{"type":"string"},
					"type":{"type":"string"},
					"description":{"type":"string"},
					"overwrite":{"type":"boolean"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name","value"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"parameter":{"type":"object"},
					"version":{"type":"integer"},
					"uri":{"type":"string"}
				},
				"required":["updated","parameter","version","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_ssm_delete_parameter",
			Description: "Delete an SSM parameter in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_secretsmanager_create_secret",
			Description: "Create a Secrets Manager secret in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"secret_string":{"type":"string"},
					"secret_binary":{"type":"string"},
					"description":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"secret":{"type":"object"},
					"version_id":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["created","secret","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_secretsmanager_put_secret_value",
			Description: "Create a new Secrets Manager secret version in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"},
					"secret_string":{"type":"string"},
					"secret_binary":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"updated":{"type":"boolean"},
					"secret":{"type":"object"},
					"version_id":{"type":"string"},
					"version_stages":{"type":"array","items":{"type":"string"}},
					"uri":{"type":"string"}
				},
				"required":["updated","secret","version_id","version_stages","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_secretsmanager_delete_secret",
			Description: "Delete a Secrets Manager secret in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"region":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"arn":{"type":"string"},
					"deletion_date":{"type":"number"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","arn","deletion_date","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_create_user",
			Description: "Create an IAM user in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"path":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"user":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","user","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_delete_user",
			Description: "Delete an IAM user in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_create_role",
			Description: "Create an IAM role in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"path":{"type":"string"},
					"assume_role_policy_document":{"type":"string"},
					"tags":{"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"role":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","role","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_delete_role",
			Description: "Delete an IAM role in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_create_policy",
			Description: "Create an IAM managed policy in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"path":{"type":"string"},
					"document":{"type":"string"}
				},
				"required":["name","document"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"policy":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","policy","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_delete_policy",
			Description: "Delete an IAM managed policy in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string"}
				},
				"required":["arn"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"arn":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","arn","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_create_group",
			Description: "Create an IAM group in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"path":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"group":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","group","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_delete_group",
			Description: "Delete an IAM group in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_create_instance_profile",
			Description: "Create an IAM instance profile in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"},
					"path":{"type":"string"},
					"roles":{"type":"array","items":{"type":"string"}}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"created":{"type":"boolean"},
					"instance_profile":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["created","instance_profile","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_iam_delete_instance_profile",
			Description: "Delete an IAM instance profile in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"name":{"type":"string"}
				},
				"required":["name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"name":{"type":"string"},
					"uri":{"type":"string"}
				},
				"required":["deleted","name","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_acm_request_certificate",
			Description: "Request a new ACM certificate in this running Overcast instance. The certificate is issued immediately in ISSUED state.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"domain_name":{"type":"string","description":"Primary domain name for the certificate."},
					"subject_alternative_names":{"type":"array","items":{"type":"string"},"description":"Additional domain names."},
					"tags":{"type":"object","additionalProperties":{"type":"string"},"description":"Optional tags as key-value pairs."}
				},
				"required":["domain_name"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"certificate":{"type":"object"},
					"uri":{"type":"string"}
				},
				"required":["certificate","uri"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  false,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_acm_delete_certificate",
			Description: "Delete an ACM certificate from this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string","description":"Certificate ARN to delete."}
				},
				"required":["arn"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"deleted":{"type":"boolean"},
					"arn":{"type":"string"}
				},
				"required":["deleted","arn"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": true,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_acm_add_tags_to_certificate",
			Description: "Add or overwrite tags on an ACM certificate in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string","description":"Certificate ARN."},
					"tags":{"type":"object","additionalProperties":{"type":"string"},"description":"Tags to add or overwrite."}
				},
				"required":["arn","tags"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string"},
					"tags":{"type":"array","items":{"type":"object"}}
				},
				"required":["arn","tags"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_acm_remove_tags_from_certificate",
			Description: "Remove tags from an ACM certificate in this running Overcast instance.",
			InputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string","description":"Certificate ARN."},
					"tag_keys":{"type":"array","items":{"type":"string"},"description":"Tag keys to remove."}
				},
				"required":["arn","tag_keys"]
			}`),
			OutputSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"arn":{"type":"string"},
					"tags":{"type":"array","items":{"type":"object"}}
				},
				"required":["arn","tags"]
			}`),
			Annotations: map[string]any{"readOnlyHint": false},
			Execution: map[string]any{
				"readOnlyHint":    false,
				"destructiveHint": false,
				"idempotentHint":  true,
				"openWorldHint":   false,
			},
		},
		{
			Name:        "runtime_capabilities",
			Title:       "Runtime service capabilities",
			Description: "Return the capabilities of each enabled service as seen by this running Overcast instance. In dev builds includes per-operation status; in prod builds returns the enabled service list only.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"service":{"type":"string","description":"Filter to a single service (optional)"}}}`),
		},
	}, p.dataLakeTools()...)
}

func (p *RuntimeProvider) Handler(name string) (mcp.HandlerFunc, bool) {
	handlers := map[string]mcp.HandlerFunc{
		"runtime_instance_info":                      p.toolInstanceInfo,
		"runtime_list_services":                      p.toolListServices,
		"runtime_inventory":                          p.toolRuntimeInventory,
		"runtime_get_health":                         p.toolGetHealth,
		"runtime_get_config":                         p.toolGetConfig,
		"runtime_get_service_state":                  p.toolGetServiceState,
		"runtime_get_recent_events":                  p.toolGetRecentEvents,
		"runtime_state_scan":                         p.toolStateScan,
		"runtime_probe_kv_store":                     p.toolProbeKVStore,
		"runtime_s3_create_bucket":                   p.toolS3CreateBucket,
		"runtime_s3_put_bucket_tags":                 p.toolS3PutBucketTags,
		"runtime_s3_delete_bucket":                   p.toolS3DeleteBucket,
		"runtime_sqs_create_queue":                   p.toolSQSCreateQueue,
		"runtime_sqs_set_queue_attributes":           p.toolSQSSetQueueAttributes,
		"runtime_sqs_delete_queue":                   p.toolSQSDeleteQueue,
		"runtime_sqs_purge_queue":                    p.toolSQSPurgeQueue,
		"runtime_dynamodb_create_table":              p.toolDynamoDBCreateTable,
		"runtime_dynamodb_update_table_ttl":          p.toolDynamoDBUpdateTableTTL,
		"runtime_sns_create_topic":                   p.toolSNSCreateTopic,
		"runtime_sns_set_topic_attributes":           p.toolSNSSetTopicAttributes,
		"runtime_sns_delete_topic":                   p.toolSNSDeleteTopic,
		"runtime_kinesis_create_stream":              p.toolKinesisCreateStream,
		"runtime_kinesis_put_record":                 p.toolKinesisPutRecord,
		"runtime_kinesis_delete_stream":              p.toolKinesisDeleteStream,
		"runtime_kms_create_key":                     p.toolKMSCreateKey,
		"runtime_kms_disable_key":                    p.toolKMSDisableKey,
		"runtime_kms_schedule_key_deletion":          p.toolKMSScheduleKeyDeletion,
		"runtime_stepfunctions_create_state_machine": p.toolStepFunctionsCreateStateMachine,
		"runtime_stepfunctions_start_execution":      p.toolStepFunctionsStartExecution,
		"runtime_stepfunctions_delete_state_machine": p.toolStepFunctionsDeleteStateMachine,
		"runtime_ssm_put_parameter":                  p.toolSSMPutParameter,
		"runtime_ssm_delete_parameter":               p.toolSSMDeleteParameter,
		"runtime_secretsmanager_create_secret":       p.toolSecretsManagerCreateSecret,
		"runtime_secretsmanager_put_secret_value":    p.toolSecretsManagerPutSecretValue,
		"runtime_secretsmanager_delete_secret":       p.toolSecretsManagerDeleteSecret,
		"runtime_iam_create_user":                    p.toolIAMCreateUser,
		"runtime_iam_delete_user":                    p.toolIAMDeleteUser,
		"runtime_iam_create_role":                    p.toolIAMCreateRole,
		"runtime_iam_delete_role":                    p.toolIAMDeleteRole,
		"runtime_iam_create_policy":                  p.toolIAMCreatePolicy,
		"runtime_iam_delete_policy":                  p.toolIAMDeletePolicy,
		"runtime_iam_create_group":                   p.toolIAMCreateGroup,
		"runtime_iam_delete_group":                   p.toolIAMDeleteGroup,
		"runtime_iam_create_instance_profile":        p.toolIAMCreateInstanceProfile,
		"runtime_iam_delete_instance_profile":        p.toolIAMDeleteInstanceProfile,
		"runtime_acm_request_certificate":            p.toolACMRequestCertificate,
		"runtime_acm_delete_certificate":             p.toolACMDeleteCertificate,
		"runtime_acm_add_tags_to_certificate":        p.toolACMAddTagsToCertificate,
		"runtime_acm_remove_tags_from_certificate":   p.toolACMRemoveTagsFromCertificate,
		"runtime_capabilities":                       p.toolRuntimeCapabilities,
	}
	maps.Copy(handlers, p.dataLakeHandlers())
	fn, ok := handlers[name]
	return fn, ok
}

func (p *RuntimeProvider) ListResources(ctx context.Context) ([]map[string]any, error) {
	resources := make([]map[string]any, 0, 64)
	resources = append(resources, map[string]any{
		"uri":         "oc://runtime/enabled-services",
		"name":        "Enabled Services",
		"description": "Enabled AWS services for this running Overcast instance",
		"mimeType":    "application/json",
	})
	for _, svc := range p.enabledServices() {
		resources = append(resources, map[string]any{
			"uri":         "oc://runtime/services/" + url.PathEscape(svc) + "/resources",
			"name":        svc + " resources",
			"description": "Resource inventory for enabled service " + svc,
			"mimeType":    "application/json",
		})
	}
	resources = append(resources, map[string]any{
		"uri":         "oc://s3/buckets",
		"name":        "S3 Buckets",
		"description": "Collection of S3 buckets in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://sqs/queues",
		"name":        "SQS Queues",
		"description": "Collection of SQS queues in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://dynamodb/tables",
		"name":        "DynamoDB Tables",
		"description": "Collection of DynamoDB tables in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://sns/topics",
		"name":        "SNS Topics",
		"description": "Collection of SNS topics in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://kinesis/streams",
		"name":        "Kinesis Streams",
		"description": "Collection of Kinesis streams in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://kms/keys",
		"name":        "KMS Keys",
		"description": "Collection of KMS keys in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources,
		map[string]any{
			"uri":         "oc://stepfunctions/state-machines",
			"name":        "Step Functions State Machines",
			"description": "Collection of Step Functions state machines in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://stepfunctions/executions",
			"name":        "Step Functions Executions",
			"description": "Collection of Step Functions executions in this running Overcast instance",
			"mimeType":    "application/json",
		},
	)
	resources = append(resources, map[string]any{
		"uri":         "oc://ssm/parameters",
		"name":        "SSM Parameters",
		"description": "Collection of SSM parameters in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://secretsmanager/secrets",
		"name":        "Secrets Manager Secrets",
		"description": "Collection of Secrets Manager secrets in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources, map[string]any{
		"uri":         "oc://acm/certificates",
		"name":        "ACM Certificates",
		"description": "Collection of ACM certificates in this running Overcast instance",
		"mimeType":    "application/json",
	})
	resources = append(resources,
		map[string]any{
			"uri":         "oc://iam/users",
			"name":        "IAM Users",
			"description": "Collection of IAM users in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://iam/roles",
			"name":        "IAM Roles",
			"description": "Collection of IAM roles in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://iam/policies",
			"name":        "IAM Policies",
			"description": "Collection of IAM managed policies in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://iam/groups",
			"name":        "IAM Groups",
			"description": "Collection of IAM groups in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://iam/instance-profiles",
			"name":        "IAM Instance Profiles",
			"description": "Collection of IAM instance profiles in this running Overcast instance",
			"mimeType":    "application/json",
		},
	)
	if p.store == nil {
		return resources, nil
	}
	keys, err := p.store.List(ctx, "s3:buckets", "")
	if err != nil {
		return nil, fmt.Errorf("list s3 buckets: %w", err)
	}
	sort.Strings(keys)
	for _, name := range keys {
		resources = append(resources, map[string]any{
			"uri":         "oc://s3/buckets/" + url.PathEscape(name),
			"name":        name,
			"description": "S3 bucket " + name,
			"mimeType":    "application/json",
		})
	}
	kvs, err := p.store.Scan(ctx, "sqs:queues", "")
	if err != nil {
		return nil, fmt.Errorf("list sqs queues: %w", err)
	}
	for _, kv := range kvs {
		region, queueName := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(queueName) == "" {
			continue
		}
		uri := "oc://sqs/queues/" + url.PathEscape(queueName)
		if strings.TrimSpace(region) != "" {
			uri = "oc://sqs/queues/" + url.PathEscape(region) + "/" + url.PathEscape(queueName)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        queueName,
			"description": "SQS queue " + queueName,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, "dynamodb:tables", "")
	if err != nil {
		return nil, fmt.Errorf("list dynamodb tables: %w", err)
	}
	for _, kv := range kvs {
		region, tableName := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(tableName) == "" {
			continue
		}
		uri := "oc://dynamodb/tables/" + url.PathEscape(tableName)
		if strings.TrimSpace(region) != "" {
			uri = "oc://dynamodb/tables/" + url.PathEscape(region) + "/" + url.PathEscape(tableName)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        tableName,
			"description": "DynamoDB table " + tableName,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, "sns:topics", "")
	if err != nil {
		return nil, fmt.Errorf("list sns topics: %w", err)
	}
	for _, kv := range kvs {
		region, topicName := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(topicName) == "" {
			continue
		}
		uri := "oc://sns/topics/" + url.PathEscape(topicName)
		if strings.TrimSpace(region) != "" {
			uri = "oc://sns/topics/" + url.PathEscape(region) + "/" + url.PathEscape(topicName)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        topicName,
			"description": "SNS topic " + topicName,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, kinesisStreamsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list kinesis streams: %w", err)
	}
	for _, kv := range kvs {
		region, streamName := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(streamName) == "" {
			continue
		}
		uri := "oc://kinesis/streams/" + url.PathEscape(streamName)
		if strings.TrimSpace(region) != "" {
			uri = "oc://kinesis/streams/" + url.PathEscape(region) + "/" + url.PathEscape(streamName)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        streamName,
			"description": "Kinesis stream " + streamName,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, kmsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list kms keys: %w", err)
	}
	for _, kv := range kvs {
		region, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, kmsKeyPrefix) {
			continue
		}
		keyID := strings.TrimPrefix(rest, kmsKeyPrefix)
		if strings.TrimSpace(keyID) == "" {
			continue
		}
		uri := "oc://kms/keys/" + url.PathEscape(keyID)
		if strings.TrimSpace(region) != "" {
			uri = "oc://kms/keys/" + url.PathEscape(region) + "/" + url.PathEscape(keyID)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        keyID,
			"description": "KMS key " + keyID,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, stepFunctionsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list stepfunctions resources: %w", err)
	}
	for _, kv := range kvs {
		region, rest := serviceutil.SplitRegionKey(kv.Key)
		switch {
		case strings.HasPrefix(rest, stepFunctionsStateMachinePrefix):
			name := strings.TrimPrefix(rest, stepFunctionsStateMachinePrefix)
			if strings.TrimSpace(name) == "" {
				continue
			}
			uri := "oc://stepfunctions/state-machines/" + url.PathEscape(name)
			if strings.TrimSpace(region) != "" {
				uri = "oc://stepfunctions/state-machines/" + url.PathEscape(region) + "/" + url.PathEscape(name)
			}
			resources = append(resources, map[string]any{
				"uri":         uri,
				"name":        name,
				"description": "Step Functions state machine " + name,
				"mimeType":    "application/json",
			})
		case strings.HasPrefix(rest, stepFunctionsExecutionPrefix):
			executionARN := strings.TrimPrefix(rest, stepFunctionsExecutionPrefix)
			if strings.TrimSpace(executionARN) == "" {
				continue
			}
			uri := "oc://stepfunctions/executions/" + url.PathEscape(executionARN)
			if strings.TrimSpace(region) != "" {
				uri = "oc://stepfunctions/executions/" + url.PathEscape(region) + "/" + url.PathEscape(executionARN)
			}
			resources = append(resources, map[string]any{
				"uri":         uri,
				"name":        executionARN,
				"description": "Step Functions execution " + executionARN,
				"mimeType":    "application/json",
			})
		}
	}
	kvs, err = p.store.Scan(ctx, ssmStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list ssm parameters: %w", err)
	}
	for _, kv := range kvs {
		region, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, ssmParameterPrefix) {
			continue
		}
		name := strings.TrimPrefix(rest, ssmParameterPrefix)
		if strings.TrimSpace(name) == "" {
			continue
		}
		uri := "oc://ssm/parameters/" + url.PathEscape(name)
		if strings.TrimSpace(region) != "" {
			uri = "oc://ssm/parameters/" + url.PathEscape(region) + "/" + url.PathEscape(name)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        name,
			"description": "SSM parameter " + name,
			"mimeType":    "application/json",
		})
	}
	kvs, err = p.store.Scan(ctx, secretsManagerStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list secretsmanager secrets: %w", err)
	}
	for _, kv := range kvs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(name) == "" {
			continue
		}
		uri := "oc://secretsmanager/secrets/" + url.PathEscape(name)
		if strings.TrimSpace(region) != "" {
			uri = "oc://secretsmanager/secrets/" + url.PathEscape(region) + "/" + url.PathEscape(name)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        name,
			"description": "Secrets Manager secret " + name,
			"mimeType":    "application/json",
		})
	}
	certs, err := p.listACMCertificates(ctx)
	if err != nil {
		return nil, err
	}
	for _, cert := range certs {
		resources = append(resources, map[string]any{
			"uri":         "oc://acm/certificates/" + url.PathEscape(cert.CertificateArn),
			"name":        cert.DomainName,
			"description": "ACM certificate " + cert.CertificateArn,
			"mimeType":    "application/json",
		})
	}
	users, err := p.listIAMUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		resources = append(resources, map[string]any{
			"uri":         "oc://iam/users/" + url.PathEscape(user.UserName),
			"name":        user.UserName,
			"description": "IAM user " + user.UserName,
			"mimeType":    "application/json",
		})
	}

	roles, err := p.listIAMRoles(ctx)
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		resources = append(resources, map[string]any{
			"uri":         "oc://iam/roles/" + url.PathEscape(role.RoleName),
			"name":        role.RoleName,
			"description": "IAM role " + role.RoleName,
			"mimeType":    "application/json",
		})
	}

	policies, err := p.listIAMPolicies(ctx)
	if err != nil {
		return nil, err
	}
	for _, policy := range policies {
		resources = append(resources, map[string]any{
			"uri":         "oc://iam/policies/" + url.PathEscape(policy.Arn),
			"name":        policy.PolicyName,
			"description": "IAM policy " + policy.PolicyName,
			"mimeType":    "application/json",
		})
	}

	groups, err := p.listIAMGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		resources = append(resources, map[string]any{
			"uri":         "oc://iam/groups/" + url.PathEscape(group.GroupName),
			"name":        group.GroupName,
			"description": "IAM group " + group.GroupName,
			"mimeType":    "application/json",
		})
	}

	profiles, err := p.listIAMInstanceProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for _, profile := range profiles {
		resources = append(resources, map[string]any{
			"uri":         "oc://iam/instance-profiles/" + url.PathEscape(profile.InstanceProfileName),
			"name":        profile.InstanceProfileName,
			"description": "IAM instance profile " + profile.InstanceProfileName,
			"mimeType":    "application/json",
		})
	}
	resources = append(resources, map[string]any{
		"uri":         "oc://lambda/functions",
		"name":        "Lambda Functions",
		"description": "Collection of Lambda functions in this running Overcast instance",
		"mimeType":    "application/json",
	})
	kvs, err = p.store.Scan(ctx, lambdaFunctionsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list lambda functions: %w", err)
	}
	for _, kv := range kvs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(name) == "" {
			continue
		}
		uri := "oc://lambda/functions/" + url.PathEscape(name)
		if strings.TrimSpace(region) != "" {
			uri = "oc://lambda/functions/" + url.PathEscape(region) + "/" + url.PathEscape(name)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        name,
			"description": "Lambda function " + name,
			"mimeType":    "application/json",
		})
	}
	resources = append(resources, map[string]any{
		"uri":         "oc://ecr/repositories",
		"name":        "ECR Repositories",
		"description": "Collection of ECR repositories in this running Overcast instance",
		"mimeType":    "application/json",
	})
	kvs, err = p.store.Scan(ctx, ecrReposStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list ecr repositories: %w", err)
	}
	for _, kv := range kvs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(name) == "" {
			continue
		}
		uri := "oc://ecr/repositories/" + url.PathEscape(name)
		if strings.TrimSpace(region) != "" {
			uri = "oc://ecr/repositories/" + url.PathEscape(region) + "/" + url.PathEscape(name)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        name,
			"description": "ECR repository " + name,
			"mimeType":    "application/json",
		})
	}
	resources = append(resources,
		map[string]any{
			"uri":         "oc://ecs/clusters",
			"name":        "ECS Clusters",
			"description": "Collection of ECS clusters in this running Overcast instance",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uri":         "oc://ecs/task-definitions",
			"name":        "ECS Task Definitions",
			"description": "Collection of ECS task definitions in this running Overcast instance",
			"mimeType":    "application/json",
		},
	)
	clusterKVs, err := p.store.Scan(ctx, ecsClustersStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list ecs clusters: %w", err)
	}
	for _, kv := range clusterKVs {
		region, name := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(name) == "" {
			continue
		}
		uri := "oc://ecs/clusters/" + url.PathEscape(name)
		if strings.TrimSpace(region) != "" {
			uri = "oc://ecs/clusters/" + url.PathEscape(region) + "/" + url.PathEscape(name)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        name,
			"description": "ECS cluster " + name,
			"mimeType":    "application/json",
		})
	}
	tdKVs, err := p.store.Scan(ctx, ecsTaskDefsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("list ecs task definitions: %w", err)
	}
	for _, kv := range tdKVs {
		region, key := serviceutil.SplitRegionKey(kv.Key)
		if strings.TrimSpace(key) == "" {
			continue
		}
		uri := "oc://ecs/task-definitions/" + url.PathEscape(key)
		if strings.TrimSpace(region) != "" {
			uri = "oc://ecs/task-definitions/" + url.PathEscape(region) + "/" + url.PathEscape(key)
		}
		resources = append(resources, map[string]any{
			"uri":         uri,
			"name":        key,
			"description": "ECS task definition " + key,
			"mimeType":    "application/json",
		})
	}
	sort.Slice(resources, func(i, j int) bool {
		li, _ := resources[i]["uri"].(string)
		lj, _ := resources[j]["uri"].(string)
		return li < lj
	})
	return resources, nil
}

func (p *RuntimeProvider) ReadResource(ctx context.Context, uri string) ([]map[string]any, error) {
	if uri == "oc://runtime/enabled-services" {
		region := ""
		if p.cfg != nil {
			region = p.cfg.Region
		}
		return resourceJSONContents(uri, map[string]any{
			"region":           region,
			"enabled_services": p.enabledServices(),
		}), nil
	}
	const runtimeServiceResourcesPrefix = "oc://runtime/services/"
	if strings.HasPrefix(uri, runtimeServiceResourcesPrefix) && strings.HasSuffix(uri, "/resources") {
		rest := strings.TrimSuffix(strings.TrimPrefix(uri, runtimeServiceResourcesPrefix), "/resources")
		service, err := url.PathUnescape(strings.Trim(rest, "/"))
		if err != nil || strings.TrimSpace(service) == "" {
			return nil, fmt.Errorf("invalid runtime service resource uri")
		}
		inventory, invErr := p.buildRuntimeInventory(ctx, runtimeInventoryArgs{
			Service:            strings.ToLower(strings.TrimSpace(service)),
			LimitPerService:    200,
			IncludeCollections: false,
		})
		if invErr != nil {
			return nil, invErr
		}
		return resourceJSONContents(uri, inventory), nil
	}

	if p.store == nil {
		return nil, fmt.Errorf("resource not found")
	}
	uri = strings.TrimSpace(uri)
	if uri == "oc://s3/buckets" {
		kvs, err := p.store.Scan(ctx, "s3:buckets", "")
		if err != nil {
			return nil, fmt.Errorf("scan s3 buckets: %w", err)
		}
		buckets := make([]map[string]any, 0, len(kvs))
		for _, kv := range kvs {
			var bucket map[string]any
			if err := json.Unmarshal([]byte(kv.Value), &bucket); err != nil {
				continue
			}
			buckets = append(buckets, bucket)
		}
		sort.Slice(buckets, func(i, j int) bool {
			ni, _ := buckets[i]["name"].(string)
			nj, _ := buckets[j]["name"].(string)
			return ni < nj
		})
		return resourceJSONContents(uri, map[string]any{
			"service": "s3",
			"count":   len(buckets),
			"buckets": buckets,
		}), nil
	}
	const bucketPrefix = "oc://s3/buckets/"
	if strings.HasPrefix(uri, bucketPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(uri, bucketPrefix))
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid bucket resource uri")
		}
		raw, found, err := p.store.Get(ctx, "s3:buckets", name)
		if err != nil {
			return nil, fmt.Errorf("read s3 bucket: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var bucket map[string]any
		if err := json.Unmarshal([]byte(raw), &bucket); err != nil {
			return nil, fmt.Errorf("decode bucket payload: %w", err)
		}
		return resourceJSONContents(uri, bucket), nil
	}
	if uri == "oc://sqs/queues" {
		kvs, err := p.store.Scan(ctx, "sqs:queues", "")
		if err != nil {
			return nil, fmt.Errorf("scan sqs queues: %w", err)
		}
		queues := make([]map[string]any, 0, len(kvs))
		for _, kv := range kvs {
			var queue map[string]any
			if err := json.Unmarshal([]byte(kv.Value), &queue); err != nil {
				continue
			}
			queues = append(queues, queue)
		}
		sort.Slice(queues, func(i, j int) bool {
			ni, _ := queues[i]["name"].(string)
			nj, _ := queues[j]["name"].(string)
			return ni < nj
		})
		return resourceJSONContents(uri, map[string]any{
			"service": "sqs",
			"count":   len(queues),
			"queues":  queues,
		}), nil
	}
	const queuePrefix = "oc://sqs/queues/"
	if strings.HasPrefix(uri, queuePrefix) {
		rest := strings.TrimPrefix(uri, queuePrefix)
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, fmt.Errorf("invalid queue resource uri")
		}
		var region string
		var name string
		if len(parts) == 1 {
			decodedName, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid queue resource uri")
			}
			name = decodedName
			region = p.defaultRegion()
		} else {
			decodedRegion, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedRegion) == "" {
				return nil, fmt.Errorf("invalid queue resource uri")
			}
			decodedName, err := url.PathUnescape(parts[1])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid queue resource uri")
			}
			region = decodedRegion
			name = decodedName
		}
		key := serviceutil.RegionKey(region, name)
		raw, found, err := p.store.Get(ctx, "sqs:queues", key)
		if err != nil {
			return nil, fmt.Errorf("read sqs queue: %w", err)
		}
		if !found && region == p.defaultRegion() {
			// Backward-compatible fallback for non-region-prefixed entries.
			raw, found, err = p.store.Get(ctx, "sqs:queues", name)
			if err != nil {
				return nil, fmt.Errorf("read sqs queue: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var queue map[string]any
		if err := json.Unmarshal([]byte(raw), &queue); err != nil {
			return nil, fmt.Errorf("decode queue payload: %w", err)
		}
		return resourceJSONContents(uri, queue), nil
	}
	if uri == "oc://dynamodb/tables" {
		kvs, err := p.store.Scan(ctx, "dynamodb:tables", "")
		if err != nil {
			return nil, fmt.Errorf("scan dynamodb tables: %w", err)
		}
		tables := make([]dynamodb.Table, 0, len(kvs))
		for _, kv := range kvs {
			var table dynamodb.Table
			if err := json.Unmarshal([]byte(kv.Value), &table); err != nil {
				continue
			}
			tables = append(tables, table)
		}
		sort.Slice(tables, func(i, j int) bool {
			return tables[i].TableName < tables[j].TableName
		})
		return resourceJSONContents(uri, map[string]any{
			"service": "dynamodb",
			"count":   len(tables),
			"tables":  tables,
		}), nil
	}
	const dynamoTablePrefix = "oc://dynamodb/tables/"
	if strings.HasPrefix(uri, dynamoTablePrefix) {
		rest := strings.TrimPrefix(uri, dynamoTablePrefix)
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, fmt.Errorf("invalid dynamodb table resource uri")
		}
		var region string
		var name string
		if len(parts) == 1 {
			decodedName, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid dynamodb table resource uri")
			}
			name = decodedName
			region = p.defaultRegion()
		} else {
			decodedRegion, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedRegion) == "" {
				return nil, fmt.Errorf("invalid dynamodb table resource uri")
			}
			decodedName, err := url.PathUnescape(parts[1])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid dynamodb table resource uri")
			}
			region = decodedRegion
			name = decodedName
		}
		key := serviceutil.RegionKey(region, name)
		raw, found, err := p.store.Get(ctx, "dynamodb:tables", key)
		if err != nil {
			return nil, fmt.Errorf("read dynamodb table: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, "dynamodb:tables", name)
			if err != nil {
				return nil, fmt.Errorf("read dynamodb table: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var table dynamodb.Table
		if err := json.Unmarshal([]byte(raw), &table); err != nil {
			return nil, fmt.Errorf("decode dynamodb table payload: %w", err)
		}
		return resourceJSONContents(uri, table), nil
	}
	if uri == "oc://sns/topics" {
		kvs, err := p.store.Scan(ctx, "sns:topics", "")
		if err != nil {
			return nil, fmt.Errorf("scan sns topics: %w", err)
		}
		topics := make([]sns.Topic, 0, len(kvs))
		for _, kv := range kvs {
			var topic sns.Topic
			if err := json.Unmarshal([]byte(kv.Value), &topic); err != nil {
				continue
			}
			topics = append(topics, topic)
		}
		sort.Slice(topics, func(i, j int) bool {
			return topics[i].Name < topics[j].Name
		})
		return resourceJSONContents(uri, map[string]any{
			"service": "sns",
			"count":   len(topics),
			"topics":  topics,
		}), nil
	}
	const snsTopicPrefix = "oc://sns/topics/"
	if strings.HasPrefix(uri, snsTopicPrefix) {
		rest := strings.TrimPrefix(uri, snsTopicPrefix)
		parts := strings.Split(rest, "/")
		if len(parts) == 0 || len(parts) > 2 {
			return nil, fmt.Errorf("invalid sns topic resource uri")
		}
		var region string
		var name string
		if len(parts) == 1 {
			decodedName, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid sns topic resource uri")
			}
			name = decodedName
			region = p.defaultRegion()
		} else {
			decodedRegion, err := url.PathUnescape(parts[0])
			if err != nil || strings.TrimSpace(decodedRegion) == "" {
				return nil, fmt.Errorf("invalid sns topic resource uri")
			}
			decodedName, err := url.PathUnescape(parts[1])
			if err != nil || strings.TrimSpace(decodedName) == "" {
				return nil, fmt.Errorf("invalid sns topic resource uri")
			}
			region = decodedRegion
			name = decodedName
		}
		key := serviceutil.RegionKey(region, name)
		raw, found, err := p.store.Get(ctx, "sns:topics", key)
		if err != nil {
			return nil, fmt.Errorf("read sns topic: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, "sns:topics", name)
			if err != nil {
				return nil, fmt.Errorf("read sns topic: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var topic sns.Topic
		if err := json.Unmarshal([]byte(raw), &topic); err != nil {
			return nil, fmt.Errorf("decode sns topic payload: %w", err)
		}
		return resourceJSONContents(uri, topic), nil
	}
	if uri == "oc://kinesis/streams" {
		streams, err := p.listKinesisStreams(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "kinesis",
			"count":   len(streams),
			"streams": streams,
		}), nil
	}
	const kinesisStreamURIPrefix = "oc://kinesis/streams/"
	if strings.HasPrefix(uri, kinesisStreamURIPrefix) {
		region, streamName, err := parseRegionalResourceURI(strings.TrimPrefix(uri, kinesisStreamURIPrefix), p.defaultRegion(), "kinesis stream")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, kinesisStreamsStoreNamespace, serviceutil.RegionKey(region, streamName))
		if err != nil {
			return nil, fmt.Errorf("read kinesis stream: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var stream kinesis.Stream
		if err := json.Unmarshal([]byte(raw), &stream); err != nil {
			return nil, fmt.Errorf("decode kinesis stream payload: %w", err)
		}
		return resourceJSONContents(uri, stream), nil
	}
	if uri == "oc://kms/keys" {
		keys, err := p.listKMSKeys(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "kms",
			"count":   len(keys),
			"keys":    keys,
		}), nil
	}
	const kmsKeyURIPrefix = "oc://kms/keys/"
	if strings.HasPrefix(uri, kmsKeyURIPrefix) {
		region, keyID, err := parseRegionalResourceURI(strings.TrimPrefix(uri, kmsKeyURIPrefix), p.defaultRegion(), "kms key")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, kmsStoreNamespace, serviceutil.RegionKey(region, kmsKeyPrefix+keyID))
		if err != nil {
			return nil, fmt.Errorf("read kms key: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var key kms.Key
		if err := json.Unmarshal([]byte(raw), &key); err != nil {
			return nil, fmt.Errorf("decode kms key payload: %w", err)
		}
		return resourceJSONContents(uri, key), nil
	}
	if uri == "oc://stepfunctions/state-machines" {
		stateMachines, err := p.listStepFunctionsStateMachines(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":        "stepfunctions",
			"count":          len(stateMachines),
			"state_machines": stateMachines,
		}), nil
	}
	const stateMachinePrefix = "oc://stepfunctions/state-machines/"
	if strings.HasPrefix(uri, stateMachinePrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, stateMachinePrefix), p.defaultRegion(), "stepfunctions state machine")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, stepFunctionsStoreNamespace, serviceutil.RegionKey(region, stepFunctionsStateMachinePrefix+name))
		if err != nil {
			return nil, fmt.Errorf("read stepfunctions state machine: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var stateMachine stepfunctions.StateMachine
		if err := json.Unmarshal([]byte(raw), &stateMachine); err != nil {
			return nil, fmt.Errorf("decode stepfunctions state machine payload: %w", err)
		}
		return resourceJSONContents(uri, stateMachine), nil
	}
	if uri == "oc://stepfunctions/executions" {
		executions, err := p.listStepFunctionsExecutions(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":    "stepfunctions",
			"count":      len(executions),
			"executions": executions,
		}), nil
	}
	const executionPrefix = "oc://stepfunctions/executions/"
	if strings.HasPrefix(uri, executionPrefix) {
		region, executionARN, err := parseRegionalResourceURI(strings.TrimPrefix(uri, executionPrefix), p.defaultRegion(), "stepfunctions execution")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, stepFunctionsStoreNamespace, serviceutil.RegionKey(region, stepFunctionsExecutionPrefix+executionARN))
		if err != nil {
			return nil, fmt.Errorf("read stepfunctions execution: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var execution stepfunctions.Execution
		if err := json.Unmarshal([]byte(raw), &execution); err != nil {
			return nil, fmt.Errorf("decode stepfunctions execution payload: %w", err)
		}
		return resourceJSONContents(uri, execution), nil
	}
	if uri == "oc://ssm/parameters" {
		parameters, err := p.listSSMParameters(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":    "ssm",
			"count":      len(parameters),
			"parameters": parameters,
		}), nil
	}
	const ssmParameterURIPrefix = "oc://ssm/parameters/"
	if strings.HasPrefix(uri, ssmParameterURIPrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, ssmParameterURIPrefix), p.defaultRegion(), "ssm parameter")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, ssmStoreNamespace, serviceutil.RegionKey(region, ssmParameterPrefix+name))
		if err != nil {
			return nil, fmt.Errorf("read ssm parameter: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var parameter ssm.ParameterRecord
		if err := json.Unmarshal([]byte(raw), &parameter); err != nil {
			return nil, fmt.Errorf("decode ssm parameter payload: %w", err)
		}
		return resourceJSONContents(uri, parameter), nil
	}
	if uri == "oc://secretsmanager/secrets" {
		secrets, err := p.listSecretsManagerSecrets(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "secretsmanager",
			"count":   len(secrets),
			"secrets": secrets,
		}), nil
	}
	const secretsManagerSecretURIPrefix = "oc://secretsmanager/secrets/"
	if strings.HasPrefix(uri, secretsManagerSecretURIPrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, secretsManagerSecretURIPrefix), p.defaultRegion(), "secretsmanager secret")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, secretsManagerStoreNamespace, serviceutil.RegionKey(region, name))
		if err != nil {
			return nil, fmt.Errorf("read secretsmanager secret: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var secret secretsmanager.Secret
		if err := json.Unmarshal([]byte(raw), &secret); err != nil {
			return nil, fmt.Errorf("decode secretsmanager secret payload: %w", err)
		}
		return resourceJSONContents(uri, secret), nil
	}
	if uri == "oc://acm/certificates" {
		certs, err := p.listACMCertificates(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":      "acm",
			"count":        len(certs),
			"certificates": certs,
		}), nil
	}
	const acmCertificateURIPrefix = "oc://acm/certificates/"
	if strings.HasPrefix(uri, acmCertificateURIPrefix) {
		arn, err := url.PathUnescape(strings.TrimPrefix(uri, acmCertificateURIPrefix))
		if err != nil || strings.TrimSpace(arn) == "" {
			return nil, fmt.Errorf("invalid acm certificate resource uri")
		}
		raw, found, err := p.store.Get(ctx, acmCertsStoreNamespace, arn)
		if err != nil {
			return nil, fmt.Errorf("read acm certificate: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var cert acm.Certificate
		if err := json.Unmarshal([]byte(raw), &cert); err != nil {
			return nil, fmt.Errorf("decode acm certificate payload: %w", err)
		}
		tags, tagsErr := p.listACMCertificateTags(ctx, arn)
		if tagsErr != nil {
			return nil, tagsErr
		}
		return resourceJSONContents(uri, map[string]any{
			"certificate": cert,
			"tags":        tags,
		}), nil
	}
	if uri == "oc://iam/users" {
		users, err := p.listIAMUsers(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "iam",
			"count":   len(users),
			"users":   users,
		}), nil
	}
	const iamUserURIPrefix = "oc://iam/users/"
	if strings.HasPrefix(uri, iamUserURIPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(uri, iamUserURIPrefix))
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid iam user resource uri")
		}
		raw, found, err := p.store.Get(ctx, iamUsersStoreNamespace, name)
		if err != nil {
			return nil, fmt.Errorf("read iam user: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var user iam.User
		if err := json.Unmarshal([]byte(raw), &user); err != nil {
			return nil, fmt.Errorf("decode iam user payload: %w", err)
		}
		return resourceJSONContents(uri, user), nil
	}
	if uri == "oc://iam/roles" {
		roles, err := p.listIAMRoles(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "iam",
			"count":   len(roles),
			"roles":   roles,
		}), nil
	}
	const iamRoleURIPrefix = "oc://iam/roles/"
	if strings.HasPrefix(uri, iamRoleURIPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(uri, iamRoleURIPrefix))
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid iam role resource uri")
		}
		raw, found, err := p.store.Get(ctx, iamRolesStoreNamespace, name)
		if err != nil {
			return nil, fmt.Errorf("read iam role: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var role iam.Role
		if err := json.Unmarshal([]byte(raw), &role); err != nil {
			return nil, fmt.Errorf("decode iam role payload: %w", err)
		}
		return resourceJSONContents(uri, role), nil
	}
	if uri == "oc://iam/policies" {
		policies, err := p.listIAMPolicies(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":  "iam",
			"count":    len(policies),
			"policies": policies,
		}), nil
	}
	const iamPolicyURIPrefix = "oc://iam/policies/"
	if strings.HasPrefix(uri, iamPolicyURIPrefix) {
		arn, err := url.PathUnescape(strings.TrimPrefix(uri, iamPolicyURIPrefix))
		if err != nil || strings.TrimSpace(arn) == "" {
			return nil, fmt.Errorf("invalid iam policy resource uri")
		}
		raw, found, err := p.store.Get(ctx, iamPoliciesStoreNamespace, arn)
		if err != nil {
			return nil, fmt.Errorf("read iam policy: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var policy iam.Policy
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return nil, fmt.Errorf("decode iam policy payload: %w", err)
		}
		return resourceJSONContents(uri, policy), nil
	}
	if uri == "oc://iam/groups" {
		groups, err := p.listIAMGroups(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service": "iam",
			"count":   len(groups),
			"groups":  groups,
		}), nil
	}
	const iamGroupURIPrefix = "oc://iam/groups/"
	if strings.HasPrefix(uri, iamGroupURIPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(uri, iamGroupURIPrefix))
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid iam group resource uri")
		}
		raw, found, err := p.store.Get(ctx, iamGroupsStoreNamespace, name)
		if err != nil {
			return nil, fmt.Errorf("read iam group: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var group iam.Group
		if err := json.Unmarshal([]byte(raw), &group); err != nil {
			return nil, fmt.Errorf("decode iam group payload: %w", err)
		}
		return resourceJSONContents(uri, group), nil
	}
	if uri == "oc://iam/instance-profiles" {
		profiles, err := p.listIAMInstanceProfiles(ctx)
		if err != nil {
			return nil, err
		}
		return resourceJSONContents(uri, map[string]any{
			"service":           "iam",
			"count":             len(profiles),
			"instance_profiles": profiles,
		}), nil
	}
	const iamInstanceProfileURIPrefix = "oc://iam/instance-profiles/"
	if strings.HasPrefix(uri, iamInstanceProfileURIPrefix) {
		name, err := url.PathUnescape(strings.TrimPrefix(uri, iamInstanceProfileURIPrefix))
		if err != nil || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("invalid iam instance profile resource uri")
		}
		raw, found, err := p.store.Get(ctx, iamProfilesStoreNamespace, name)
		if err != nil {
			return nil, fmt.Errorf("read iam instance profile: %w", err)
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var profile iam.InstanceProfile
		if err := json.Unmarshal([]byte(raw), &profile); err != nil {
			return nil, fmt.Errorf("decode iam instance profile payload: %w", err)
		}
		return resourceJSONContents(uri, profile), nil
	}
	// ── Lambda ────────────────────────────────────────────────────────────
	if uri == "oc://lambda/functions" {
		kvs, err := p.store.Scan(ctx, lambdaFunctionsStoreNamespace, "")
		if err != nil {
			return nil, fmt.Errorf("scan lambda functions: %w", err)
		}
		functions := make([]lambda.Function, 0, len(kvs))
		for _, kv := range kvs {
			var fn lambda.Function
			if err := json.Unmarshal([]byte(kv.Value), &fn); err != nil {
				continue
			}
			functions = append(functions, fn)
		}
		sort.Slice(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })
		return resourceJSONContents(uri, map[string]any{
			"service":   "lambda",
			"count":     len(functions),
			"functions": functions,
		}), nil
	}
	const lambdaFunctionURIPrefix = "oc://lambda/functions/"
	if strings.HasPrefix(uri, lambdaFunctionURIPrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, lambdaFunctionURIPrefix), p.defaultRegion(), "lambda function")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, lambdaFunctionsStoreNamespace, serviceutil.RegionKey(region, name))
		if err != nil {
			return nil, fmt.Errorf("read lambda function: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, lambdaFunctionsStoreNamespace, name)
			if err != nil {
				return nil, fmt.Errorf("read lambda function: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var fn lambda.Function
		if err := json.Unmarshal([]byte(raw), &fn); err != nil {
			return nil, fmt.Errorf("decode lambda function payload: %w", err)
		}
		return resourceJSONContents(uri, fn), nil
	}
	// ── ECR ───────────────────────────────────────────────────────────────
	if uri == "oc://ecr/repositories" {
		kvs, err := p.store.Scan(ctx, ecrReposStoreNamespace, "")
		if err != nil {
			return nil, fmt.Errorf("scan ecr repositories: %w", err)
		}
		repos := make([]ecr.Repository, 0, len(kvs))
		for _, kv := range kvs {
			var repo ecr.Repository
			if err := json.Unmarshal([]byte(kv.Value), &repo); err != nil {
				continue
			}
			repos = append(repos, repo)
		}
		sort.Slice(repos, func(i, j int) bool { return repos[i].RepositoryName < repos[j].RepositoryName })
		return resourceJSONContents(uri, map[string]any{
			"service":      "ecr",
			"count":        len(repos),
			"repositories": repos,
		}), nil
	}
	const ecrRepoURIPrefix = "oc://ecr/repositories/"
	if strings.HasPrefix(uri, ecrRepoURIPrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, ecrRepoURIPrefix), p.defaultRegion(), "ecr repository")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, ecrReposStoreNamespace, serviceutil.RegionKey(region, name))
		if err != nil {
			return nil, fmt.Errorf("read ecr repository: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, ecrReposStoreNamespace, name)
			if err != nil {
				return nil, fmt.Errorf("read ecr repository: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var repo ecr.Repository
		if err := json.Unmarshal([]byte(raw), &repo); err != nil {
			return nil, fmt.Errorf("decode ecr repository payload: %w", err)
		}
		return resourceJSONContents(uri, repo), nil
	}
	// ── ECS ───────────────────────────────────────────────────────────────
	if uri == "oc://ecs/clusters" {
		kvs, err := p.store.Scan(ctx, ecsClustersStoreNamespace, "")
		if err != nil {
			return nil, fmt.Errorf("scan ecs clusters: %w", err)
		}
		clusters := make([]ecs.Cluster, 0, len(kvs))
		for _, kv := range kvs {
			var c ecs.Cluster
			if err := json.Unmarshal([]byte(kv.Value), &c); err != nil {
				continue
			}
			clusters = append(clusters, c)
		}
		sort.Slice(clusters, func(i, j int) bool { return clusters[i].ClusterName < clusters[j].ClusterName })
		return resourceJSONContents(uri, map[string]any{
			"service":  "ecs",
			"count":    len(clusters),
			"clusters": clusters,
		}), nil
	}
	const ecsClusterURIPrefix = "oc://ecs/clusters/"
	if strings.HasPrefix(uri, ecsClusterURIPrefix) {
		region, name, err := parseRegionalResourceURI(strings.TrimPrefix(uri, ecsClusterURIPrefix), p.defaultRegion(), "ecs cluster")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, ecsClustersStoreNamespace, serviceutil.RegionKey(region, name))
		if err != nil {
			return nil, fmt.Errorf("read ecs cluster: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, ecsClustersStoreNamespace, name)
			if err != nil {
				return nil, fmt.Errorf("read ecs cluster: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var cluster ecs.Cluster
		if err := json.Unmarshal([]byte(raw), &cluster); err != nil {
			return nil, fmt.Errorf("decode ecs cluster payload: %w", err)
		}
		return resourceJSONContents(uri, cluster), nil
	}
	if uri == "oc://ecs/task-definitions" {
		kvs, err := p.store.Scan(ctx, ecsTaskDefsStoreNamespace, "")
		if err != nil {
			return nil, fmt.Errorf("scan ecs task definitions: %w", err)
		}
		taskDefs := make([]ecs.TaskDefinition, 0, len(kvs))
		for _, kv := range kvs {
			var td ecs.TaskDefinition
			if err := json.Unmarshal([]byte(kv.Value), &td); err != nil {
				continue
			}
			taskDefs = append(taskDefs, td)
		}
		sort.Slice(taskDefs, func(i, j int) bool {
			ki := fmt.Sprintf("%s:%d", taskDefs[i].Family, taskDefs[i].Revision)
			kj := fmt.Sprintf("%s:%d", taskDefs[j].Family, taskDefs[j].Revision)
			return ki < kj
		})
		return resourceJSONContents(uri, map[string]any{
			"service":          "ecs",
			"count":            len(taskDefs),
			"task_definitions": taskDefs,
		}), nil
	}
	const ecsTaskDefURIPrefix = "oc://ecs/task-definitions/"
	if strings.HasPrefix(uri, ecsTaskDefURIPrefix) {
		region, key, err := parseRegionalResourceURI(strings.TrimPrefix(uri, ecsTaskDefURIPrefix), p.defaultRegion(), "ecs task definition")
		if err != nil {
			return nil, err
		}
		raw, found, err := p.store.Get(ctx, ecsTaskDefsStoreNamespace, serviceutil.RegionKey(region, key))
		if err != nil {
			return nil, fmt.Errorf("read ecs task definition: %w", err)
		}
		if !found && region == p.defaultRegion() {
			raw, found, err = p.store.Get(ctx, ecsTaskDefsStoreNamespace, key)
			if err != nil {
				return nil, fmt.Errorf("read ecs task definition: %w", err)
			}
		}
		if !found {
			return nil, fmt.Errorf("resource not found")
		}
		var td ecs.TaskDefinition
		if err := json.Unmarshal([]byte(raw), &td); err != nil {
			return nil, fmt.Errorf("decode ecs task definition payload: %w", err)
		}
		return resourceJSONContents(uri, td), nil
	}
	return nil, fmt.Errorf("resource not found")
}

func (p *RuntimeProvider) ListResourceTemplates(_ context.Context) ([]map[string]any, error) {
	templates := make([]map[string]any, 0, 10)
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://runtime/services/{service}/resources",
		"name":        "Runtime Service Resources",
		"description": "Inspect resource inventory for an enabled service",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://s3/buckets/{bucket}",
		"name":        "S3 Bucket",
		"description": "Inspect a specific S3 bucket",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://sqs/queues/{region}/{queue}",
		"name":        "SQS Queue",
		"description": "Inspect a specific SQS queue",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://dynamodb/tables/{region}/{table}",
		"name":        "DynamoDB Table",
		"description": "Inspect a specific DynamoDB table",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://sns/topics/{region}/{topic}",
		"name":        "SNS Topic",
		"description": "Inspect a specific SNS topic",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://kinesis/streams/{region}/{stream}",
		"name":        "Kinesis Stream",
		"description": "Inspect a specific Kinesis stream",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://kms/keys/{region}/{keyId}",
		"name":        "KMS Key",
		"description": "Inspect a specific KMS key",
		"mimeType":    "application/json",
	})
	templates = append(templates,
		map[string]any{
			"uriTemplate": "oc://stepfunctions/state-machines/{region}/{name}",
			"name":        "Step Functions State Machine",
			"description": "Inspect a specific Step Functions state machine",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://stepfunctions/executions/{region}/{executionArn}",
			"name":        "Step Functions Execution",
			"description": "Inspect a specific Step Functions execution",
			"mimeType":    "application/json",
		},
	)
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://ssm/parameters/{region}/{name}",
		"name":        "SSM Parameter",
		"description": "Inspect a specific SSM parameter",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://secretsmanager/secrets/{region}/{name}",
		"name":        "Secrets Manager Secret",
		"description": "Inspect a specific Secrets Manager secret",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://acm/certificates/{certificateArn}",
		"name":        "ACM Certificate",
		"description": "Inspect a specific ACM certificate",
		"mimeType":    "application/json",
	})
	templates = append(templates,
		map[string]any{
			"uriTemplate": "oc://iam/users/{user}",
			"name":        "IAM User",
			"description": "Inspect a specific IAM user",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://iam/roles/{role}",
			"name":        "IAM Role",
			"description": "Inspect a specific IAM role",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://iam/policies/{policyArn}",
			"name":        "IAM Policy",
			"description": "Inspect a specific IAM managed policy",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://iam/groups/{group}",
			"name":        "IAM Group",
			"description": "Inspect a specific IAM group",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://iam/instance-profiles/{instanceProfile}",
			"name":        "IAM Instance Profile",
			"description": "Inspect a specific IAM instance profile",
			"mimeType":    "application/json",
		},
	)
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://lambda/functions/{region}/{name}",
		"name":        "Lambda Function",
		"description": "Inspect a specific Lambda function",
		"mimeType":    "application/json",
	})
	templates = append(templates, map[string]any{
		"uriTemplate": "oc://ecr/repositories/{region}/{name}",
		"name":        "ECR Repository",
		"description": "Inspect a specific ECR repository",
		"mimeType":    "application/json",
	})
	templates = append(templates,
		map[string]any{
			"uriTemplate": "oc://ecs/clusters/{region}/{name}",
			"name":        "ECS Cluster",
			"description": "Inspect a specific ECS cluster",
			"mimeType":    "application/json",
		},
		map[string]any{
			"uriTemplate": "oc://ecs/task-definitions/{region}/{key}",
			"name":        "ECS Task Definition",
			"description": "Inspect a specific ECS task definition (key is family:revision)",
			"mimeType":    "application/json",
		},
	)
	return templates, nil
}

func (p *RuntimeProvider) toolInstanceInfo(_ context.Context, _ json.RawMessage) (any, error) {
	var result map[string]any
	if p.cfg == nil {
		result = map[string]any{
			"region":           "us-east-1",
			"account_id":       "000000000000",
			"host":             "0.0.0.0",
			"port":             4566,
			"hostname":         "localhost",
			"state_backend":    "unknown",
			"log_level":        "info",
			"enabled_services": []string{},
		}
		return mcp.ToolResult{
			Content:           mcp.TextContent("Running Overcast instance metadata is available, but no runtime config was injected into the MCP provider."),
			StructuredContent: result,
		}, nil
	}
	services := config.AllServices()
	sort.Strings(services)
	result = map[string]any{
		"region":           p.cfg.Region,
		"account_id":       p.cfg.AccountID,
		"host":             p.cfg.Host,
		"port":             p.cfg.Port,
		"hostname":         p.cfg.Hostname,
		"state_backend":    string(p.cfg.State),
		"log_level":        p.cfg.LogLevel,
		"enabled_services": services,
	}
	return mcp.ToolResult{
		Content:           mcp.TextContent(fmt.Sprintf("Overcast runtime on %s:%d with %d enabled services.", p.cfg.Host, p.cfg.Port, len(services))),
		StructuredContent: result,
	}, nil
}

func (p *RuntimeProvider) toolListServices(ctx context.Context, _ json.RawMessage) (any, error) {
	if p.cfg == nil {
		return map[string]any{"services": []any{}}, nil
	}
	services := p.enabledServices()

	type serviceEntry struct {
		Name         string `json:"name"`
		StateBackend string `json:"state_backend"`
		KeyCount     int    `json:"key_count"`
	}
	entries := make([]serviceEntry, 0, len(services))
	for _, svc := range services {
		backend := string(p.cfg.State)
		if override, ok := p.cfg.ServiceStates[svc]; ok {
			backend = string(override)
		}
		keyCount := 0
		if p.store != nil {
			if count, err := p.countServiceStateKeys(ctx, svc); err == nil {
				keyCount = count
			}
		}
		entries = append(entries, serviceEntry{
			Name:         svc,
			StateBackend: backend,
			KeyCount:     keyCount,
		})
	}
	return map[string]any{"services": entries}, nil
}

func (p *RuntimeProvider) countServiceStateKeys(ctx context.Context, service string) (int, error) {
	if p.store == nil {
		return 0, nil
	}
	total := 0
	for _, namespace := range runtimeServiceStateNamespaces(service) {
		keys, err := p.store.List(ctx, namespace, "")
		if err != nil {
			return 0, err
		}
		total += len(keys)
	}
	return total, nil
}

func runtimeServiceStateNamespaces(service string) []string {
	switch service {
	case "s3":
		return []string{"s3:buckets"}
	case "sqs":
		return []string{"sqs:queues"}
	case "dynamodb":
		return []string{"dynamodb:tables"}
	case "sns":
		return []string{"sns:topics"}
	case "kinesis":
		return []string{kinesisStreamsStoreNamespace, kinesisRecordsStoreNamespace}
	case "kms":
		return []string{kmsStoreNamespace}
	case "stepfunctions":
		return []string{stepFunctionsStoreNamespace}
	case "ssm":
		return []string{ssmStoreNamespace}
	case "secretsmanager":
		return []string{secretsManagerStoreNamespace}
	case "iam":
		return []string{iamUsersStoreNamespace, iamRolesStoreNamespace, iamPoliciesStoreNamespace, iamGroupsStoreNamespace, iamProfilesStoreNamespace}
	case "acm":
		return []string{acmCertsStoreNamespace, acmTagsStoreNamespace}
	case "lambda":
		return []string{lambdaFunctionsStoreNamespace}
	case "ecr":
		return []string{ecrReposStoreNamespace}
	case "ecs":
		return []string{ecsClustersStoreNamespace, ecsTaskDefsStoreNamespace}
	default:
		return []string{service}
	}
}

func (p *RuntimeProvider) toolRuntimeInventory(ctx context.Context, params json.RawMessage) (any, error) {
	var args runtimeInventoryArgs
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, &jsonErr{code: -32602, msg: "invalid params: " + err.Error()}
		}
	}
	return p.buildRuntimeInventory(ctx, args)
}

type runtimeInventoryArgs struct {
	Service            string `json:"service"`
	LimitPerService    int    `json:"limit_per_service"`
	IncludeCollections bool   `json:"include_collections"`
}

func (p *RuntimeProvider) buildRuntimeInventory(ctx context.Context, args runtimeInventoryArgs) (map[string]any, error) {
	serviceFilter := strings.TrimSpace(strings.ToLower(args.Service))
	limitPerService := args.LimitPerService
	if limitPerService <= 0 {
		limitPerService = 50
	}
	if limitPerService > 200 {
		limitPerService = 200
	}

	enabled := p.enabledServices()
	if serviceFilter != "" {
		svc := serviceFilter
		found := false
		for _, s := range enabled {
			if s == svc {
				found = true
				enabled = []string{svc}
				break
			}
		}
		if !found {
			return nil, &jsonErr{code: -32602, msg: "service not enabled: " + svc}
		}
	}

	typed, err := p.ListResources(ctx)
	if err != nil {
		return nil, err
	}
	typedByService := map[string][]map[string]any{}
	for _, r := range typed {
		uri, _ := r["uri"].(string)
		svc := runtimeServiceFromURI(uri)
		if svc == "" {
			continue
		}
		if !args.IncludeCollections && runtimeURIIsCollection(uri, svc) {
			continue
		}
		typedByService[svc] = append(typedByService[svc], map[string]any{
			"source":      "typed",
			"uri":         uri,
			"name":        r["name"],
			"description": r["description"],
			"mimeType":    r["mimeType"],
		})
	}

	serviceEntries := make([]map[string]any, 0, len(enabled))
	for _, svc := range enabled {
		resources := append([]map[string]any(nil), typedByService[svc]...)

		if len(resources) == 0 && p.store != nil {
			kvs, scanErr := p.store.Scan(ctx, svc, "")
			if scanErr == nil {
				for _, kv := range kvs {
					resources = append(resources, map[string]any{
						"source": "state_key",
						"uri":    "oc://state/" + svc + "/" + url.PathEscape(kv.Key),
						"key":    kv.Key,
					})
				}
			}
		}

		sort.Slice(resources, func(i, j int) bool {
			li, _ := resources[i]["uri"].(string)
			lj, _ := resources[j]["uri"].(string)
			return li < lj
		})

		truncated := false
		resourceCount := len(resources)
		if len(resources) > limitPerService {
			truncated = true
			resources = resources[:limitPerService]
		}

		serviceEntries = append(serviceEntries, map[string]any{
			"service":        svc,
			"resource_count": resourceCount,
			"truncated":      truncated,
			"resources":      resources,
		})
	}

	region := ""
	if p.cfg != nil {
		region = p.cfg.Region
	}
	return map[string]any{
		"region":           region,
		"enabled_services": enabled,
		"services":         serviceEntries,
	}, nil
}

func (p *RuntimeProvider) toolGetHealth(_ context.Context, _ json.RawMessage) (any, error) {
	out := map[string]any{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"services":  p.enabledServices(),
	}
	if p.cfg != nil {
		out["version"] = p.cfg.Version
	}
	return out, nil
}

func (p *RuntimeProvider) toolGetConfig(_ context.Context, _ json.RawMessage) (any, error) {
	if p.cfg == nil {
		return map[string]any{"debug_required": false}, nil
	}
	if !p.cfg.Debug {
		return map[string]any{"debug_required": true}, nil
	}
	svcStates := make(map[string]string, len(p.cfg.ServiceStates))
	for svc, mode := range p.cfg.ServiceStates {
		svcStates[svc] = string(mode)
	}
	return map[string]any{
		"debug_required": false,
		"host":           p.cfg.Host,
		"port":           p.cfg.Port,
		"services":       config.AllServices(),
		"state":          string(p.cfg.State),
		"serviceStates":  svcStates,
		"data_dir":       p.cfg.DataDir,
		"region":         p.cfg.Region,
		"account_id":     p.cfg.AccountID,
		"log_level":      p.cfg.LogLevel,
		"debug":          p.cfg.Debug,
		"tls_enabled":    p.cfg.TLSEnabled(),
	}, nil
}

func (p *RuntimeProvider) toolGetServiceState(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Namespace  string `json:"namespace"`
		KeyPattern string `json:"key_pattern"`
		Limit      int    `json:"limit"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	limit := 50
	if args.Limit > 0 && args.Limit <= 500 {
		limit = args.Limit
	}
	targetNamespace := strings.TrimSpace(args.Namespace)
	pattern := strings.ToLower(strings.TrimSpace(args.KeyPattern))

	if p.cfg != nil && !p.cfg.Debug {
		return map[string]any{
			"namespace":      targetNamespace,
			"debug_required": true,
			"limit":          limit,
			"count":          0,
			"truncated":      false,
			"entries":        map[string]any{},
		}, nil
	}

	if p.store == nil {
		return map[string]any{
			"namespace": targetNamespace,
			"limit":     limit,
			"count":     0,
			"truncated": false,
			"entries":   map[string]any{},
		}, nil
	}

	if targetNamespace != "" {
		keys, err := p.store.List(ctx, targetNamespace, "")
		if err != nil {
			return nil, fmt.Errorf("list namespace %q: %w", targetNamespace, err)
		}
		sort.Strings(keys)
		entries := map[string]any{}
		count := 0
		truncated := false
		for _, key := range keys {
			if pattern != "" && !strings.Contains(strings.ToLower(key), pattern) {
				continue
			}
			if count >= limit {
				truncated = true
				break
			}
			value, _, err := p.store.Get(ctx, targetNamespace, key)
			if err != nil {
				return nil, fmt.Errorf("get namespace %q key %q: %w", targetNamespace, key, err)
			}
			entries[key] = value
			count++
		}
		return map[string]any{
			"namespace": targetNamespace,
			"limit":     limit,
			"count":     count,
			"truncated": truncated,
			"entries":   entries,
		}, nil
	}

	namespaces := make([]string, 0, 16)
	seen := map[string]struct{}{}
	for _, svc := range p.enabledServices() {
		for _, ns := range runtimeServiceStateNamespaces(svc) {
			if _, ok := seen[ns]; ok {
				continue
			}
			seen[ns] = struct{}{}
			namespaces = append(namespaces, ns)
		}
	}
	sort.Strings(namespaces)

	entries := make(map[string]any)
	count := 0
	truncated := false
	for _, ns := range namespaces {
		if pattern != "" && !strings.Contains(strings.ToLower(ns), pattern) {
			continue
		}
		keys, err := p.store.List(ctx, ns, "")
		if err != nil {
			return nil, fmt.Errorf("list namespace %q: %w", ns, err)
		}
		if len(keys) == 0 {
			continue
		}
		if count >= limit {
			truncated = true
			break
		}
		sort.Strings(keys)
		list := make([]any, 0, len(keys))
		for _, key := range keys {
			list = append(list, key)
		}
		entries[ns] = list
		count++
	}

	return map[string]any{
		"namespace": "",
		"limit":     limit,
		"count":     count,
		"truncated": truncated,
		"entries":   entries,
	}, nil
}

func (p *RuntimeProvider) toolGetRecentEvents(_ context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Source string `json:"source"`
		Type   string `json:"type"`
		Limit  int    `json:"limit"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	limit := 50
	if args.Limit > 0 && args.Limit <= defaultRuntimeRecentEventBufferLimit {
		limit = args.Limit
	}
	sourceFilter := strings.ToLower(strings.TrimSpace(args.Source))
	typeFilter := strings.ToLower(strings.TrimSpace(args.Type))

	p.recentEventsMu.RLock()
	snapshot := append([]map[string]any(nil), p.recentEvents...)
	p.recentEventsMu.RUnlock()

	matched := make([]map[string]any, 0, limit)
	totalMatched := 0
	for i := len(snapshot) - 1; i >= 0; i-- {
		ev := snapshot[i]
		src, _ := ev["source"].(string)
		typ, _ := ev["type"].(string)
		if sourceFilter != "" && strings.ToLower(src) != sourceFilter {
			continue
		}
		if typeFilter != "" && strings.ToLower(typ) != typeFilter {
			continue
		}
		totalMatched++
		if len(matched) < limit {
			matched = append(matched, ev)
		}
	}

	return map[string]any{
		"limit":     limit,
		"count":     len(matched),
		"truncated": totalMatched > limit,
		"events":    matched,
	}, nil
}

func (p *RuntimeProvider) toolStateScan(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Service string `json:"service"`
		Prefix  string `json:"prefix"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	args.Service = strings.TrimSpace(strings.ToLower(args.Service))
	if args.Service == "" {
		return nil, fmt.Errorf("service is required")
	}
	if !isKnownService(args.Service) {
		return nil, fmt.Errorf("service %q is not one Overcast implements", args.Service)
	}
	if args.Limit <= 0 {
		args.Limit = 50
	}
	const maxLimit = 200
	if args.Limit > maxLimit {
		args.Limit = maxLimit
	}
	if p.store == nil {
		return map[string]any{
			"service":   args.Service,
			"prefix":    args.Prefix,
			"truncated": false,
			"entries":   []any{},
		}, nil
	}
	kvs, err := p.store.Scan(ctx, args.Service, args.Prefix)
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}
	truncated := len(kvs) > args.Limit
	if truncated {
		kvs = kvs[:args.Limit]
	}
	type entry struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	entries := make([]entry, len(kvs))
	for i, kv := range kvs {
		entries[i] = entry{Key: kv.Key, Value: kv.Value}
	}
	return map[string]any{
		"service":   args.Service,
		"prefix":    args.Prefix,
		"truncated": truncated,
		"entries":   entries,
	}, nil
}

func (p *RuntimeProvider) toolProbeKVStore(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Namespace     string `json:"namespace"`
		KeyPattern    string `json:"key_pattern"`
		Limit         int    `json:"limit"`
		Cursor        string `json:"cursor"`
		IncludeValues bool   `json:"include_values"`
		PreviewBytes  int    `json:"preview_bytes"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	limit := 50
	if args.Limit > 0 && args.Limit <= 500 {
		limit = args.Limit
	}
	previewBytes := 120
	if args.PreviewBytes > 0 && args.PreviewBytes <= 2048 {
		previewBytes = args.PreviewBytes
	}

	targetNamespace := strings.TrimSpace(args.Namespace)
	namespaces := make([]string, 0, 16)
	if targetNamespace != "" {
		namespaces = append(namespaces, targetNamespace)
	} else {
		seen := map[string]struct{}{}
		for _, svc := range p.enabledServices() {
			for _, ns := range runtimeServiceStateNamespaces(svc) {
				if _, ok := seen[ns]; ok {
					continue
				}
				seen[ns] = struct{}{}
				namespaces = append(namespaces, ns)
			}
		}
		sort.Strings(namespaces)
	}

	type kvEntry struct {
		namespace string
		key       string
		value     string
		token     string
	}
	entriesAll := make([]kvEntry, 0, 256)
	if p.store != nil {
		for _, ns := range namespaces {
			kvs, err := p.store.Scan(ctx, ns, "")
			if err != nil {
				return nil, fmt.Errorf("scan namespace %q: %w", ns, err)
			}
			for _, kv := range kvs {
				token := ns + "|" + kv.Key
				entriesAll = append(entriesAll, kvEntry{namespace: ns, key: kv.Key, value: kv.Value, token: token})
			}
		}
	}

	pattern := strings.ToLower(strings.TrimSpace(args.KeyPattern))
	if pattern != "" {
		filtered := make([]kvEntry, 0, len(entriesAll))
		for _, entry := range entriesAll {
			if strings.Contains(strings.ToLower(entry.key), pattern) {
				filtered = append(filtered, entry)
			}
		}
		entriesAll = filtered
	}

	sort.Slice(entriesAll, func(i, j int) bool {
		return entriesAll[i].token < entriesAll[j].token
	})
	totalMatched := len(entriesAll)

	start := 0
	cursor := strings.TrimSpace(args.Cursor)
	if cursor != "" {
		for start < len(entriesAll) && entriesAll[start].token <= cursor {
			start++
		}
	}
	end := start + limit
	if end > len(entriesAll) {
		end = len(entriesAll)
	}

	outEntries := make([]map[string]any, 0, end-start)
	for _, entry := range entriesAll[start:end] {
		outEntry := map[string]any{
			"namespace": entry.namespace,
			"key":       entry.key,
		}
		if args.IncludeValues {
			outEntry["value"] = entry.value
		}
		preview := entry.value
		if len(preview) > previewBytes {
			preview = preview[:previewBytes]
		}
		outEntry["value_preview"] = preview
		outEntries = append(outEntries, outEntry)
	}

	out := map[string]any{
		"namespace":     targetNamespace,
		"limit":         limit,
		"cursor":        cursor,
		"count":         len(outEntries),
		"total_matched": totalMatched,
		"truncated":     end < len(entriesAll),
		"entries":       outEntries,
	}
	if end < len(entriesAll) && end > 0 {
		out["next_cursor"] = entriesAll[end-1].token
	}
	return out, nil
}

func (p *RuntimeProvider) toolS3CreateBucket(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string            `json:"name"`
		Region string            `json:"region"`
		Tags   map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, "s3:buckets", name); err != nil {
		return nil, fmt.Errorf("check existing bucket: %w", err)
	} else if found {
		return nil, fmt.Errorf("bucket %q already exists", name)
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		if p.cfg != nil && strings.TrimSpace(p.cfg.Region) != "" {
			region = p.cfg.Region
		} else {
			region = "us-east-1"
		}
	}
	bucket := map[string]any{
		"name":          name,
		"region":        region,
		"creation_date": time.Now().UTC(),
	}
	if len(args.Tags) > 0 {
		bucket["tags"] = args.Tags
	}
	raw, err := json.Marshal(bucket)
	if err != nil {
		return nil, fmt.Errorf("encode bucket payload: %w", err)
	}
	if err := p.store.Set(ctx, "s3:buckets", name, string(raw)); err != nil {
		return nil, fmt.Errorf("store bucket: %w", err)
	}
	return map[string]any{
		"created": true,
		"bucket":  bucket,
		"uri":     "oc://s3/buckets/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolS3PutBucketTags(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string            `json:"name"`
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	raw, found, err := p.store.Get(ctx, "s3:buckets", name)
	if err != nil {
		return nil, fmt.Errorf("read bucket: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("bucket %q not found", name)
	}
	var bucket map[string]any
	if err := json.Unmarshal([]byte(raw), &bucket); err != nil {
		return nil, fmt.Errorf("decode bucket payload: %w", err)
	}
	bucket["tags"] = args.Tags
	updated, err := json.Marshal(bucket)
	if err != nil {
		return nil, fmt.Errorf("encode updated bucket payload: %w", err)
	}
	if err := p.store.Set(ctx, "s3:buckets", name, string(updated)); err != nil {
		return nil, fmt.Errorf("store updated bucket: %w", err)
	}
	return map[string]any{
		"updated": true,
		"bucket":  bucket,
		"uri":     "oc://s3/buckets/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolS3DeleteBucket(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, "s3:buckets", name); err != nil {
		return nil, fmt.Errorf("read bucket: %w", err)
	} else if !found {
		return nil, fmt.Errorf("bucket %q not found", name)
	}
	if err := p.store.Delete(ctx, "s3:buckets", name); err != nil {
		return nil, fmt.Errorf("delete bucket: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://s3/buckets/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSQSCreateQueue(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string            `json:"name"`
		Region     string            `json:"region"`
		Attributes map[string]string `json:"attributes"`
		Tags       map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, "sqs:queues", key); err != nil {
		return nil, fmt.Errorf("check existing queue: %w", err)
	} else if found {
		return nil, fmt.Errorf("queue %q already exists", name)
	}
	attrs := defaultSQSQueueAttributes()
	for k, v := range args.Attributes {
		attrs[k] = v
	}
	queue := map[string]any{
		"name":              name,
		"url":               fmt.Sprintf("%s/%s/%s", p.externalBaseURL(), p.accountID(), name),
		"arn":               protocol.QueueARN(region, p.accountID(), name),
		"attributes":        attrs,
		"created_timestamp": time.Now().Unix(),
	}
	if len(args.Tags) > 0 {
		queue["tags"] = args.Tags
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		return nil, fmt.Errorf("encode queue payload: %w", err)
	}
	if err := p.store.Set(ctx, "sqs:queues", key, string(raw)); err != nil {
		return nil, fmt.Errorf("store queue: %w", err)
	}
	return map[string]any{
		"created":   true,
		"queue":     queue,
		"uri":       "oc://sqs/queues/" + url.PathEscape(region) + "/" + url.PathEscape(name),
		"queue_url": queue["url"],
	}, nil
}

func (p *RuntimeProvider) toolSQSSetQueueAttributes(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string            `json:"name"`
		Region     string            `json:"region"`
		Attributes map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(args.Attributes) == 0 {
		return nil, fmt.Errorf("attributes are required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	raw, found, err := p.store.Get(ctx, "sqs:queues", key)
	if err != nil {
		return nil, fmt.Errorf("read queue: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("queue %q not found", name)
	}
	var queue map[string]any
	if err := json.Unmarshal([]byte(raw), &queue); err != nil {
		return nil, fmt.Errorf("decode queue payload: %w", err)
	}
	attrs, _ := queue["attributes"].(map[string]any)
	if attrs == nil {
		attrs = make(map[string]any)
	}
	for k, v := range args.Attributes {
		attrs[k] = v
	}
	queue["attributes"] = attrs
	updated, err := json.Marshal(queue)
	if err != nil {
		return nil, fmt.Errorf("encode queue payload: %w", err)
	}
	if err := p.store.Set(ctx, "sqs:queues", key, string(updated)); err != nil {
		return nil, fmt.Errorf("store queue: %w", err)
	}
	return map[string]any{
		"updated": true,
		"queue":   queue,
		"uri":     "oc://sqs/queues/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSQSDeleteQueue(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, "sqs:queues", key); err != nil {
		return nil, fmt.Errorf("read queue: %w", err)
	} else if !found {
		return nil, fmt.Errorf("queue %q not found", name)
	}
	if err := p.store.Delete(ctx, "sqs:queues", key); err != nil {
		return nil, fmt.Errorf("delete queue: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://sqs/queues/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSQSPurgeQueue(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	// Verify queue exists
	if _, found, err := p.store.Get(ctx, "sqs:queues", key); err != nil {
		return nil, fmt.Errorf("read queue: %w", err)
	} else if !found {
		return nil, fmt.Errorf("queue %q not found", name)
	}
	// Scan all messages in this queue
	msgPrefix := serviceutil.RegionKey(region, name+"/")
	msgPairs, err := p.store.Scan(ctx, "sqs:messages", msgPrefix)
	if err != nil {
		return nil, fmt.Errorf("scan messages: %w", err)
	}
	// Delete all messages
	cleared := 0
	for _, pair := range msgPairs {
		if err := p.store.Delete(ctx, "sqs:messages", pair.Key); err != nil {
			return nil, fmt.Errorf("delete message: %w", err)
		}
		cleared++
	}
	return map[string]any{
		"purged":           true,
		"messages_cleared": cleared,
		"name":             name,
		"uri":              "oc://sqs/queues/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolDynamoDBCreateTable(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		TableName              string                          `json:"table_name"`
		Region                 string                          `json:"region"`
		KeySchema              []dynamodb.KeySchemaElement     `json:"key_schema"`
		AttributeDefinitions   []dynamodb.AttributeDef         `json:"attribute_definitions"`
		BillingMode            string                          `json:"billing_mode"`
		ProvisionedThroughput  *dynamodb.ProvisionedThroughput `json:"provisioned_throughput"`
		StreamSpecification    *dynamodb.StreamSpecification   `json:"stream_specification"`
		GlobalSecondaryIndexes []dynamodb.SecondaryIndex       `json:"global_secondary_indexes"`
		LocalSecondaryIndexes  []dynamodb.SecondaryIndex       `json:"local_secondary_indexes"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.TableName)
	if name == "" {
		return nil, fmt.Errorf("table_name is required")
	}
	if len(args.KeySchema) == 0 {
		return nil, fmt.Errorf("key_schema is required")
	}
	if len(args.AttributeDefinitions) == 0 {
		return nil, fmt.Errorf("attribute_definitions are required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, "dynamodb:tables", key); err != nil {
		return nil, fmt.Errorf("check existing table: %w", err)
	} else if found {
		return nil, fmt.Errorf("table %q already exists", name)
	}
	billingMode := strings.TrimSpace(args.BillingMode)
	if billingMode == "" {
		billingMode = "PAY_PER_REQUEST"
	}
	table := &dynamodb.Table{
		TableName:              name,
		KeySchema:              args.KeySchema,
		AttributeDefinitions:   args.AttributeDefinitions,
		TableStatus:            "ACTIVE",
		BillingMode:            billingMode,
		TableARN:               protocol.TableARN(region, p.accountID(), name),
		CreationDateTime:       float64(time.Now().UnixMilli()) / 1000.0,
		ItemCount:              0,
		BillingModeSummary:     &dynamodb.BillingModeSummary{BillingMode: billingMode},
		GlobalSecondaryIndexes: args.GlobalSecondaryIndexes,
		LocalSecondaryIndexes:  args.LocalSecondaryIndexes,
	}
	if billingMode == "PROVISIONED" {
		if args.ProvisionedThroughput != nil {
			table.ProvisionedThroughput = args.ProvisionedThroughput
		} else {
			table.ProvisionedThroughput = defaultDynamoProvisionedThroughput()
		}
	}
	for index := range table.GlobalSecondaryIndexes {
		table.GlobalSecondaryIndexes[index].IndexArn = table.TableARN + "/index/" + table.GlobalSecondaryIndexes[index].IndexName
		table.GlobalSecondaryIndexes[index].IndexStatus = "ACTIVE"
	}
	for index := range table.LocalSecondaryIndexes {
		table.LocalSecondaryIndexes[index].IndexArn = table.TableARN + "/index/" + table.LocalSecondaryIndexes[index].IndexName
	}
	if args.StreamSpecification != nil && (args.StreamSpecification.StreamEnabled || strings.TrimSpace(args.StreamSpecification.StreamViewType) != "") {
		table.StreamSpecification = &dynamodb.StreamSpecification{
			StreamEnabled:  true,
			StreamViewType: args.StreamSpecification.StreamViewType,
		}
		applyDynamoDBStreamSpec(table, region, p.accountID())
	}
	raw, err := json.Marshal(table)
	if err != nil {
		return nil, fmt.Errorf("encode dynamodb table payload: %w", err)
	}
	if err := p.store.Set(ctx, "dynamodb:tables", key, string(raw)); err != nil {
		return nil, fmt.Errorf("store dynamodb table: %w", err)
	}
	return map[string]any{
		"created": true,
		"table":   table,
		"uri":     "oc://dynamodb/tables/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolDynamoDBUpdateTableTTL(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		TableName               string                            `json:"table_name"`
		Region                  string                            `json:"region"`
		TimeToLiveSpecification *dynamodb.TimeToLiveSpecification `json:"time_to_live_specification"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.TableName)
	if name == "" {
		return nil, fmt.Errorf("table_name is required")
	}
	if args.TimeToLiveSpecification == nil {
		return nil, fmt.Errorf("time_to_live_specification is required")
	}
	if args.TimeToLiveSpecification.Enabled && strings.TrimSpace(args.TimeToLiveSpecification.AttributeName) == "" {
		return nil, fmt.Errorf("time_to_live_specification.AttributeName is required when enabling TTL")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	raw, found, err := p.store.Get(ctx, "dynamodb:tables", key)
	if err != nil {
		return nil, fmt.Errorf("read dynamodb table: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("table %q not found", name)
	}
	var table dynamodb.Table
	if err := json.Unmarshal([]byte(raw), &table); err != nil {
		return nil, fmt.Errorf("decode dynamodb table payload: %w", err)
	}
	if args.TimeToLiveSpecification.Enabled && table.TTL != nil && table.TTL.Enabled && table.TTL.AttributeName != args.TimeToLiveSpecification.AttributeName {
		return nil, fmt.Errorf("TimeToLive is already enabled with AttributeName %s", table.TTL.AttributeName)
	}
	table.TTL = args.TimeToLiveSpecification
	updated, err := json.Marshal(&table)
	if err != nil {
		return nil, fmt.Errorf("encode dynamodb table payload: %w", err)
	}
	if err := p.store.Set(ctx, "dynamodb:tables", key, string(updated)); err != nil {
		return nil, fmt.Errorf("store dynamodb table: %w", err)
	}
	return map[string]any{
		"updated": true,
		"table":   &table,
		"uri":     "oc://dynamodb/tables/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSNSCreateTopic(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string            `json:"name"`
		Region     string            `json:"region"`
		Attributes map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if raw, found, err := p.store.Get(ctx, "sns:topics", key); err != nil {
		return nil, fmt.Errorf("check existing topic: %w", err)
	} else if found {
		var topic sns.Topic
		if err := json.Unmarshal([]byte(raw), &topic); err != nil {
			return nil, fmt.Errorf("decode existing topic payload: %w", err)
		}
		return map[string]any{
			"created": false,
			"topic":   topic,
			"uri":     "oc://sns/topics/" + url.PathEscape(region) + "/" + url.PathEscape(name),
		}, nil
	}
	arn := protocol.TopicARN(region, p.accountID(), name)
	attrs := defaultSNSTopicAttributes(name, arn, p.accountID())
	for key, value := range args.Attributes {
		attrs[key] = value
	}
	topic := sns.Topic{
		Name:             name,
		ARN:              arn,
		Attributes:       attrs,
		CreatedTimestamp: time.Now().Unix(),
	}
	raw, err := json.Marshal(topic)
	if err != nil {
		return nil, fmt.Errorf("encode sns topic payload: %w", err)
	}
	if err := p.store.Set(ctx, "sns:topics", key, string(raw)); err != nil {
		return nil, fmt.Errorf("store sns topic: %w", err)
	}
	return map[string]any{
		"created": true,
		"topic":   topic,
		"uri":     "oc://sns/topics/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSNSSetTopicAttributes(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string            `json:"name"`
		Region     string            `json:"region"`
		Attributes map[string]string `json:"attributes"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if len(args.Attributes) == 0 {
		return nil, fmt.Errorf("attributes are required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	raw, found, err := p.store.Get(ctx, "sns:topics", key)
	if err != nil {
		return nil, fmt.Errorf("read sns topic: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("topic %q not found", name)
	}
	var topic sns.Topic
	if err := json.Unmarshal([]byte(raw), &topic); err != nil {
		return nil, fmt.Errorf("decode sns topic payload: %w", err)
	}
	if topic.Attributes == nil {
		topic.Attributes = map[string]string{}
	}
	for key, value := range args.Attributes {
		topic.Attributes[key] = value
	}
	updated, err := json.Marshal(topic)
	if err != nil {
		return nil, fmt.Errorf("encode sns topic payload: %w", err)
	}
	if err := p.store.Set(ctx, "sns:topics", key, string(updated)); err != nil {
		return nil, fmt.Errorf("store sns topic: %w", err)
	}
	return map[string]any{
		"updated": true,
		"topic":   topic,
		"uri":     "oc://sns/topics/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSNSDeleteTopic(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, "sns:topics", key); err != nil {
		return nil, fmt.Errorf("read sns topic: %w", err)
	} else if !found {
		return nil, fmt.Errorf("topic %q not found", name)
	}
	if err := p.store.Delete(ctx, "sns:topics", key); err != nil {
		return nil, fmt.Errorf("delete sns topic: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://sns/topics/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolKinesisCreateStream(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string `json:"name"`
		Region     string `json:"region"`
		ShardCount int    `json:"shard_count"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, kinesisStreamsStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("check existing kinesis stream: %w", err)
	} else if found {
		return nil, fmt.Errorf("kinesis stream %q already exists", name)
	}
	shardCount := args.ShardCount
	if shardCount <= 0 {
		shardCount = 1
	}
	// The same layout CreateStream gives a stream. Giving every shard the
	// whole keyspace, as this used to, made them all overlap: PutRecord
	// routed every record to shard 0 and a split or merge of any other shard
	// produced ranges no AWS stream could have (#2112).
	createdAt := time.Now().UTC()
	stream := kinesis.Stream{
		StreamName:           name,
		StreamARN:            protocol.ARN(region, p.accountID(), "kinesis", "stream/"+name),
		StreamStatus:         "ACTIVE",
		ShardCount:           shardCount,
		Shards:               kinesis.InitialShards(shardCount, createdAt),
		Tags:                 map[string]string{},
		CreatedAt:            createdAt,
		RetentionPeriodHours: 24,
	}
	raw, err := json.Marshal(stream)
	if err != nil {
		return nil, fmt.Errorf("encode kinesis stream payload: %w", err)
	}
	if err := p.store.Set(ctx, kinesisStreamsStoreNamespace, key, string(raw)); err != nil {
		return nil, fmt.Errorf("store kinesis stream: %w", err)
	}
	return map[string]any{
		"created": true,
		"stream":  stream,
		"uri":     "oc://kinesis/streams/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolKinesisPutRecord(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		StreamName   string `json:"stream_name"`
		Region       string `json:"region"`
		PartitionKey string `json:"partition_key"`
		Data         string `json:"data"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	streamName := strings.TrimSpace(args.StreamName)
	if streamName == "" {
		return nil, fmt.Errorf("stream_name is required")
	}
	partitionKey := strings.TrimSpace(args.PartitionKey)
	if partitionKey == "" {
		return nil, fmt.Errorf("partition_key is required")
	}
	if args.Data == "" {
		return nil, fmt.Errorf("data is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	streamRaw, found, err := p.store.Get(ctx, kinesisStreamsStoreNamespace, serviceutil.RegionKey(region, streamName))
	if err != nil {
		return nil, fmt.Errorf("read kinesis stream: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("kinesis stream %q not found", streamName)
	}
	var stream kinesis.Stream
	if err := json.Unmarshal([]byte(streamRaw), &stream); err != nil {
		return nil, fmt.Errorf("decode kinesis stream payload: %w", err)
	}
	shardID := "shardId-000000000000"
	if len(stream.Shards) > 0 && strings.TrimSpace(stream.Shards[0].ShardId) != "" {
		shardID = stream.Shards[0].ShardId
	}
	sequenceNumber := fmt.Sprintf("49%019d", time.Now().UnixNano())
	record := kinesis.Record{
		SequenceNumber:              sequenceNumber,
		ApproximateArrivalTimestamp: time.Now().UTC(),
		Data:                        []byte(args.Data),
		PartitionKey:                partitionKey,
	}
	recordRaw, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode kinesis record payload: %w", err)
	}
	recordKey := serviceutil.RegionKey(region, streamName+"/"+shardID+"/"+sequenceNumber)
	if err := p.store.Set(ctx, kinesisRecordsStoreNamespace, recordKey, string(recordRaw)); err != nil {
		return nil, fmt.Errorf("store kinesis record: %w", err)
	}
	return map[string]any{
		"stored":          true,
		"stream_name":     streamName,
		"sequence_number": sequenceNumber,
		"shard_id":        shardID,
		"uri":             "oc://kinesis/streams/" + url.PathEscape(region) + "/" + url.PathEscape(streamName),
	}, nil
}

func (p *RuntimeProvider) toolKinesisDeleteStream(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	streamKey := serviceutil.RegionKey(region, name)
	if _, found, err := p.store.Get(ctx, kinesisStreamsStoreNamespace, streamKey); err != nil {
		return nil, fmt.Errorf("read kinesis stream: %w", err)
	} else if !found {
		return nil, fmt.Errorf("kinesis stream %q not found", name)
	}
	if err := p.store.Delete(ctx, kinesisStreamsStoreNamespace, streamKey); err != nil {
		return nil, fmt.Errorf("delete kinesis stream: %w", err)
	}
	recordKeys, err := p.store.List(ctx, kinesisRecordsStoreNamespace, serviceutil.RegionKey(region, name+"/"))
	if err == nil {
		for _, key := range recordKeys {
			_ = p.store.Delete(ctx, kinesisRecordsStoreNamespace, key)
		}
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://kinesis/streams/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolKMSCreateKey(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Region      string            `json:"region"`
		Description string            `json:"description"`
		KeySpec     string            `json:"key_spec"`
		KeyUsage    string            `json:"key_usage"`
		Tags        map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	keyID := uuid.NewString()
	keySpec := strings.TrimSpace(args.KeySpec)
	if keySpec == "" {
		keySpec = "SYMMETRIC_DEFAULT"
	}
	keyUsage := strings.TrimSpace(args.KeyUsage)
	if keyUsage == "" {
		keyUsage = "ENCRYPT_DECRYPT"
	}
	key := kms.Key{
		KeyID:       keyID,
		ARN:         fmt.Sprintf("arn:aws:kms:%s:%s:key/%s", region, p.accountID(), keyID),
		Description: strings.TrimSpace(args.Description),
		KeySpec:     keySpec,
		KeyUsage:    keyUsage,
		Enabled:     true,
		KeyState:    "Enabled",
		CreatedAt:   time.Now().UTC(),
	}
	if len(args.Tags) > 0 {
		key.Tags = make([]kms.Tag, 0, len(args.Tags))
		keys := make([]string, 0, len(args.Tags))
		for k := range args.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			key.Tags = append(key.Tags, kms.Tag{TagKey: k, TagValue: args.Tags[k]})
		}
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("encode kms key payload: %w", err)
	}
	if err := p.store.Set(ctx, kmsStoreNamespace, serviceutil.RegionKey(region, kmsKeyPrefix+keyID), string(raw)); err != nil {
		return nil, fmt.Errorf("store kms key: %w", err)
	}
	return map[string]any{
		"created": true,
		"key":     key,
		"uri":     "oc://kms/keys/" + url.PathEscape(region) + "/" + url.PathEscape(keyID),
	}, nil
}

func (p *RuntimeProvider) toolKMSDisableKey(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		KeyID  string `json:"key_id"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	keyID := strings.TrimSpace(args.KeyID)
	if keyID == "" {
		return nil, fmt.Errorf("key_id is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	keyStoreKey := serviceutil.RegionKey(region, kmsKeyPrefix+keyID)
	raw, found, err := p.store.Get(ctx, kmsStoreNamespace, keyStoreKey)
	if err != nil {
		return nil, fmt.Errorf("read kms key: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("kms key %q not found", keyID)
	}
	var key kms.Key
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return nil, fmt.Errorf("decode kms key payload: %w", err)
	}
	key.Enabled = false
	key.KeyState = "Disabled"
	updated, err := json.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("encode kms key payload: %w", err)
	}
	if err := p.store.Set(ctx, kmsStoreNamespace, keyStoreKey, string(updated)); err != nil {
		return nil, fmt.Errorf("store kms key: %w", err)
	}
	return map[string]any{
		"updated": true,
		"key":     key,
		"uri":     "oc://kms/keys/" + url.PathEscape(region) + "/" + url.PathEscape(keyID),
	}, nil
}

func (p *RuntimeProvider) toolKMSScheduleKeyDeletion(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		KeyID               string `json:"key_id"`
		Region              string `json:"region"`
		PendingWindowInDays int    `json:"pending_window_in_days"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	keyID := strings.TrimSpace(args.KeyID)
	if keyID == "" {
		return nil, fmt.Errorf("key_id is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	pendingDays := args.PendingWindowInDays
	if pendingDays <= 0 {
		pendingDays = 30
	}
	keyStoreKey := serviceutil.RegionKey(region, kmsKeyPrefix+keyID)
	raw, found, err := p.store.Get(ctx, kmsStoreNamespace, keyStoreKey)
	if err != nil {
		return nil, fmt.Errorf("read kms key: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("kms key %q not found", keyID)
	}
	var key kms.Key
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return nil, fmt.Errorf("decode kms key payload: %w", err)
	}
	deletionDate := time.Now().UTC().Add(time.Duration(pendingDays) * 24 * time.Hour)
	key.Enabled = false
	key.KeyState = "PendingDeletion"
	key.DeletionDate = &deletionDate
	updated, err := json.Marshal(key)
	if err != nil {
		return nil, fmt.Errorf("encode kms key payload: %w", err)
	}
	if err := p.store.Set(ctx, kmsStoreNamespace, keyStoreKey, string(updated)); err != nil {
		return nil, fmt.Errorf("store kms key: %w", err)
	}
	return map[string]any{
		"deleted":       true,
		"scheduled":     true,
		"key":           key,
		"deletion_date": deletionDate,
		"uri":           "oc://kms/keys/" + url.PathEscape(region) + "/" + url.PathEscape(keyID),
	}, nil
}

func (p *RuntimeProvider) toolStepFunctionsCreateStateMachine(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name       string `json:"name"`
		Region     string `json:"region"`
		Definition string `json:"definition"`
		RoleARN    string `json:"role_arn"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	definition := strings.TrimSpace(args.Definition)
	if definition == "" {
		return nil, fmt.Errorf("definition is required")
	}
	roleARN := strings.TrimSpace(args.RoleARN)
	if roleARN == "" {
		return nil, fmt.Errorf("role_arn is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, stepFunctionsStateMachinePrefix+name)
	if raw, found, err := p.store.Get(ctx, stepFunctionsStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("check existing state machine: %w", err)
	} else if found {
		var existing stepfunctions.StateMachine
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return nil, fmt.Errorf("decode existing state machine payload: %w", err)
		}
		stateMachineType := strings.TrimSpace(args.Type)
		if stateMachineType == "" {
			stateMachineType = "STANDARD"
		}
		if existing.Definition == definition && existing.RoleArn == roleARN && existing.Type == stateMachineType {
			return map[string]any{
				"created":       false,
				"state_machine": existing,
				"uri":           "oc://stepfunctions/state-machines/" + url.PathEscape(region) + "/" + url.PathEscape(name),
			}, nil
		}
		return nil, fmt.Errorf("state machine %q already exists", name)
	}
	stateMachineType := strings.TrimSpace(args.Type)
	if stateMachineType == "" {
		stateMachineType = "STANDARD"
	}
	stateMachine := stepfunctions.StateMachine{
		Name:       name,
		ARN:        protocol.ARN(region, p.accountID(), "states", "stateMachine:"+name),
		Definition: definition,
		RoleArn:    roleARN,
		Type:       stateMachineType,
		Status:     "ACTIVE",
		CreatedAt:  time.Now().UTC(),
	}
	raw, err := json.Marshal(stateMachine)
	if err != nil {
		return nil, fmt.Errorf("encode stepfunctions state machine payload: %w", err)
	}
	if err := p.store.Set(ctx, stepFunctionsStoreNamespace, key, string(raw)); err != nil {
		return nil, fmt.Errorf("store stepfunctions state machine: %w", err)
	}
	return map[string]any{
		"created":       true,
		"state_machine": stateMachine,
		"uri":           "oc://stepfunctions/state-machines/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolStepFunctionsStartExecution(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name          string `json:"name"`
		Region        string `json:"region"`
		Input         string `json:"input"`
		ExecutionName string `json:"execution_name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	raw, found, err := p.store.Get(ctx, stepFunctionsStoreNamespace, serviceutil.RegionKey(region, stepFunctionsStateMachinePrefix+name))
	if err != nil {
		return nil, fmt.Errorf("read stepfunctions state machine: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("state machine %q not found", name)
	}
	var stateMachine stepfunctions.StateMachine
	if err := json.Unmarshal([]byte(raw), &stateMachine); err != nil {
		return nil, fmt.Errorf("decode stepfunctions state machine payload: %w", err)
	}
	executionName := strings.TrimSpace(args.ExecutionName)
	if executionName == "" {
		executionName = fmt.Sprintf("exec-%d", time.Now().UnixNano())
	}
	execution := stepfunctions.Execution{
		ExecutionArn:    protocol.ARN(region, p.accountID(), "states", "execution:"+name+":"+executionName),
		StateMachineArn: stateMachine.ARN,
		Name:            executionName,
		Input:           args.Input,
		Status:          "SUCCEEDED",
		StartDate:       time.Now().UTC(),
	}
	execRaw, err := json.Marshal(execution)
	if err != nil {
		return nil, fmt.Errorf("encode stepfunctions execution payload: %w", err)
	}
	if err := p.store.Set(ctx, stepFunctionsStoreNamespace, serviceutil.RegionKey(region, stepFunctionsExecutionPrefix+execution.ExecutionArn), string(execRaw)); err != nil {
		return nil, fmt.Errorf("store stepfunctions execution: %w", err)
	}
	return map[string]any{
		"started":   true,
		"execution": execution,
		"uri":       "oc://stepfunctions/executions/" + url.PathEscape(region) + "/" + url.PathEscape(execution.ExecutionArn),
	}, nil
}

func (p *RuntimeProvider) toolStepFunctionsDeleteStateMachine(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, stepFunctionsStateMachinePrefix+name)
	if _, found, err := p.store.Get(ctx, stepFunctionsStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("read stepfunctions state machine: %w", err)
	} else if !found {
		return nil, fmt.Errorf("state machine %q not found", name)
	}
	if err := p.store.Delete(ctx, stepFunctionsStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("delete stepfunctions state machine: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://stepfunctions/state-machines/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSSMPutParameter(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name        string            `json:"name"`
		Region      string            `json:"region"`
		Value       string            `json:"value"`
		Type        string            `json:"type"`
		Description string            `json:"description"`
		Overwrite   bool              `json:"overwrite"`
		Tags        map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if args.Value == "" {
		return nil, fmt.Errorf("value is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, ssmParameterPrefix+name)
	raw, found, err := p.store.Get(ctx, ssmStoreNamespace, key)
	if err != nil {
		return nil, fmt.Errorf("read ssm parameter: %w", err)
	}
	var record *ssm.ParameterRecord
	if found {
		if !args.Overwrite {
			return nil, fmt.Errorf("parameter %q already exists", name)
		}
		var existing ssm.ParameterRecord
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return nil, fmt.Errorf("decode ssm parameter payload: %w", err)
		}
		record = &existing
	} else {
		record = &ssm.ParameterRecord{Name: name, Tags: map[string]string{}}
	}
	if strings.TrimSpace(args.Description) != "" {
		record.Description = args.Description
	}
	if record.Tags == nil {
		record.Tags = map[string]string{}
	}
	for key, value := range args.Tags {
		record.Tags[key] = value
	}
	parameterType := strings.TrimSpace(args.Type)
	if parameterType == "" {
		parameterType = "String"
	}
	record.Versions = append(record.Versions, ssm.ParameterVersion{
		Value:     args.Value,
		Type:      parameterType,
		CreatedAt: time.Now().UTC(),
	})
	updated, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode ssm parameter payload: %w", err)
	}
	if err := p.store.Set(ctx, ssmStoreNamespace, key, string(updated)); err != nil {
		return nil, fmt.Errorf("store ssm parameter: %w", err)
	}
	return map[string]any{
		"updated":   true,
		"parameter": record,
		"version":   record.Version(),
		"uri":       "oc://ssm/parameters/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSSMDeleteParameter(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, ssmParameterPrefix+name)
	if _, found, err := p.store.Get(ctx, ssmStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("read ssm parameter: %w", err)
	} else if !found {
		return nil, fmt.Errorf("parameter %q not found", name)
	}
	if err := p.store.Delete(ctx, ssmStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("delete ssm parameter: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://ssm/parameters/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSecretsManagerCreateSecret(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name         string            `json:"name"`
		Region       string            `json:"region"`
		SecretString string            `json:"secret_string"`
		SecretBinary string            `json:"secret_binary"`
		Description  string            `json:"description"`
		Tags         map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	if raw, found, err := p.store.Get(ctx, secretsManagerStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("check existing secretsmanager secret: %w", err)
	} else if found {
		var existing secretsmanager.Secret
		if err := json.Unmarshal([]byte(raw), &existing); err != nil {
			return nil, fmt.Errorf("decode existing secretsmanager secret payload: %w", err)
		}
		return map[string]any{
			"created": false,
			"secret":  existing,
			"uri":     "oc://secretsmanager/secrets/" + url.PathEscape(region) + "/" + url.PathEscape(name),
		}, nil
	}
	now := float64(time.Now().Unix())
	versionID := uuid.NewString()
	secret := secretsmanager.Secret{
		ARN:              protocol.ARN(region, p.accountID(), "secretsmanager", "secret:"+name),
		Name:             name,
		Description:      strings.TrimSpace(args.Description),
		Tags:             secretsManagerTagsFromMap(args.Tags),
		CurrentVersionId: versionID,
		CreatedDate:      now,
		LastChangedDate:  now,
		Versions: []secretsmanager.SecretVersion{{
			VersionId:    versionID,
			SecretString: args.SecretString,
			SecretBinary: args.SecretBinary,
			Stages:       []string{"AWSCURRENT"},
			CreatedDate:  now,
		}},
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("encode secretsmanager secret payload: %w", err)
	}
	if err := p.store.Set(ctx, secretsManagerStoreNamespace, key, string(raw)); err != nil {
		return nil, fmt.Errorf("store secretsmanager secret: %w", err)
	}
	return map[string]any{
		"created":    true,
		"secret":     secret,
		"version_id": versionID,
		"uri":        "oc://secretsmanager/secrets/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSecretsManagerPutSecretValue(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name         string `json:"name"`
		Region       string `json:"region"`
		SecretString string `json:"secret_string"`
		SecretBinary string `json:"secret_binary"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if args.SecretString == "" && args.SecretBinary == "" {
		return nil, fmt.Errorf("secret_string or secret_binary is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	raw, found, err := p.store.Get(ctx, secretsManagerStoreNamespace, key)
	if err != nil {
		return nil, fmt.Errorf("read secretsmanager secret: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	var secret secretsmanager.Secret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return nil, fmt.Errorf("decode secretsmanager secret payload: %w", err)
	}
	for versionIndex := range secret.Versions {
		for stageIndex, stage := range secret.Versions[versionIndex].Stages {
			if stage == "AWSCURRENT" {
				secret.Versions[versionIndex].Stages[stageIndex] = "AWSPREVIOUS"
			}
		}
	}
	now := float64(time.Now().Unix())
	versionID := uuid.NewString()
	secret.Versions = append(secret.Versions, secretsmanager.SecretVersion{
		VersionId:    versionID,
		SecretString: args.SecretString,
		SecretBinary: args.SecretBinary,
		Stages:       []string{"AWSCURRENT"},
		CreatedDate:  now,
	})
	const maxSecretVersions = 3
	if len(secret.Versions) > maxSecretVersions {
		secret.Versions = secret.Versions[len(secret.Versions)-maxSecretVersions:]
	}
	secret.CurrentVersionId = versionID
	secret.LastChangedDate = now
	updated, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("encode secretsmanager secret payload: %w", err)
	}
	if err := p.store.Set(ctx, secretsManagerStoreNamespace, key, string(updated)); err != nil {
		return nil, fmt.Errorf("store secretsmanager secret: %w", err)
	}
	return map[string]any{
		"updated":        true,
		"secret":         secret,
		"version_id":     versionID,
		"version_stages": []string{"AWSCURRENT"},
		"uri":            "oc://secretsmanager/secrets/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolSecretsManagerDeleteSecret(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name   string `json:"name"`
		Region string `json:"region"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	region := strings.TrimSpace(args.Region)
	if region == "" {
		region = p.defaultRegion()
	}
	key := serviceutil.RegionKey(region, name)
	raw, found, err := p.store.Get(ctx, secretsManagerStoreNamespace, key)
	if err != nil {
		return nil, fmt.Errorf("read secretsmanager secret: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("secret %q not found", name)
	}
	var secret secretsmanager.Secret
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return nil, fmt.Errorf("decode secretsmanager secret payload: %w", err)
	}
	if err := p.store.Delete(ctx, secretsManagerStoreNamespace, key); err != nil {
		return nil, fmt.Errorf("delete secretsmanager secret: %w", err)
	}
	return map[string]any{
		"deleted":       true,
		"name":          name,
		"arn":           secret.ARN,
		"deletion_date": float64(time.Now().Unix()),
		"uri":           "oc://secretsmanager/secrets/" + url.PathEscape(region) + "/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMCreateUser(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string            `json:"name"`
		Path string            `json:"path"`
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamUsersStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("check existing iam user: %w", err)
	} else if found {
		return nil, fmt.Errorf("iam user %q already exists", name)
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	user := iam.User{
		UserName:   name,
		UserId:     "AIDA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		Arn:        fmt.Sprintf("arn:aws:iam::%s:user%s%s", p.accountID(), path, name),
		Path:       path,
		CreateDate: time.Now().UTC().Format(time.RFC3339),
		Tags:       args.Tags,
	}
	raw, err := json.Marshal(user)
	if err != nil {
		return nil, fmt.Errorf("encode iam user payload: %w", err)
	}
	if err := p.store.Set(ctx, iamUsersStoreNamespace, name, string(raw)); err != nil {
		return nil, fmt.Errorf("store iam user: %w", err)
	}
	return map[string]any{
		"created": true,
		"user":    user,
		"uri":     "oc://iam/users/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMDeleteUser(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamUsersStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("read iam user: %w", err)
	} else if !found {
		return nil, fmt.Errorf("iam user %q not found", name)
	}
	if err := p.store.Delete(ctx, iamUsersStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("delete iam user: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://iam/users/" + url.PathEscape(name),
	}, nil
}

// defaultAssumeRolePolicy is the trust policy a role gets when the caller
// supplies none: one sts:AssumeRole statement trusting the account root, which
// is the same-account trust policy AWS's own console offers and a document IAM
// accepts. The placeholder this replaced carried an empty Statement list, which
// is not a policy the IAM API will store (#1850) and which grants nothing, so a
// role written through MCP could not be assumed or evaluated.
func defaultAssumeRolePolicy(accountID string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
		`"Principal":{"AWS":"arn:aws:iam::` + accountID + `:root"},` +
		`"Action":"sts:AssumeRole"}]}`
}

// toolIAMCreateRole writes the role straight to the store, as every runtime
// tool here does, so the policy-document check the IAM handler applies
// (internal/services/iam/policy_document.go) has to be applied here too — the
// same iampolicy.ValidateDocument, so the two write paths agree on what a
// well-formed document is.
func (p *RuntimeProvider) toolIAMCreateRole(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name                     string            `json:"name"`
		Path                     string            `json:"path"`
		AssumeRolePolicyDocument string            `json:"assume_role_policy_document"`
		Tags                     map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	assumePolicy := strings.TrimSpace(args.AssumeRolePolicyDocument)
	if assumePolicy == "" {
		assumePolicy = defaultAssumeRolePolicy(p.accountID())
	}
	if err := iampolicy.ValidateDocument(assumePolicy); err != nil {
		return nil, fmt.Errorf("malformed assume_role_policy_document: %w", err)
	}
	if _, found, err := p.store.Get(ctx, iamRolesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("check existing iam role: %w", err)
	} else if found {
		return nil, fmt.Errorf("iam role %q already exists", name)
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	role := iam.Role{
		RoleName:                 name,
		RoleId:                   "AROA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		Arn:                      fmt.Sprintf("arn:aws:iam::%s:role%s%s", p.accountID(), path, name),
		Path:                     path,
		AssumeRolePolicyDocument: assumePolicy,
		CreateDate:               time.Now().UTC().Format(time.RFC3339),
		Tags:                     args.Tags,
	}
	raw, err := json.Marshal(role)
	if err != nil {
		return nil, fmt.Errorf("encode iam role payload: %w", err)
	}
	if err := p.store.Set(ctx, iamRolesStoreNamespace, name, string(raw)); err != nil {
		return nil, fmt.Errorf("store iam role: %w", err)
	}
	return map[string]any{
		"created": true,
		"role":    role,
		"uri":     "oc://iam/roles/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMDeleteRole(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamRolesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("read iam role: %w", err)
	} else if !found {
		return nil, fmt.Errorf("iam role %q not found", name)
	}
	if err := p.store.Delete(ctx, iamRolesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("delete iam role: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://iam/roles/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMCreatePolicy(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name     string `json:"name"`
		Path     string `json:"path"`
		Document string `json:"document"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	document := strings.TrimSpace(args.Document)
	if document == "" {
		return nil, fmt.Errorf("document is required")
	}
	if err := iampolicy.ValidateDocument(document); err != nil {
		return nil, fmt.Errorf("malformed document: %w", err)
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	arn := fmt.Sprintf("arn:aws:iam::%s:policy%s%s", p.accountID(), path, name)
	if _, found, err := p.store.Get(ctx, iamPoliciesStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("check existing iam policy: %w", err)
	} else if found {
		return nil, fmt.Errorf("iam policy %q already exists", arn)
	}
	// AttachmentCount is not set: IAM derives it from the entities that refer
	// to the policy rather than storing it, so a freshly created policy needs
	// nothing here to read as attached to nothing.
	policy := iam.Policy{
		PolicyName: name,
		PolicyId:   "ANPA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		Arn:        arn,
		Path:       path,
		Document:   document,
		CreateDate: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return nil, fmt.Errorf("encode iam policy payload: %w", err)
	}
	if err := p.store.Set(ctx, iamPoliciesStoreNamespace, arn, string(raw)); err != nil {
		return nil, fmt.Errorf("store iam policy: %w", err)
	}
	return map[string]any{
		"created": true,
		"policy":  policy,
		"uri":     "oc://iam/policies/" + url.PathEscape(arn),
	}, nil
}

func (p *RuntimeProvider) toolIAMDeletePolicy(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		ARN string `json:"arn"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	arn := strings.TrimSpace(args.ARN)
	if arn == "" {
		return nil, fmt.Errorf("arn is required")
	}
	if _, found, err := p.store.Get(ctx, iamPoliciesStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("read iam policy: %w", err)
	} else if !found {
		return nil, fmt.Errorf("iam policy %q not found", arn)
	}
	if err := p.store.Delete(ctx, iamPoliciesStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("delete iam policy: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"arn":     arn,
		"uri":     "oc://iam/policies/" + url.PathEscape(arn),
	}, nil
}

func (p *RuntimeProvider) toolIAMCreateGroup(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamGroupsStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("check existing iam group: %w", err)
	} else if found {
		return nil, fmt.Errorf("iam group %q already exists", name)
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	group := iam.Group{
		GroupName:  name,
		GroupId:    "AGPA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		Arn:        fmt.Sprintf("arn:aws:iam::%s:group%s%s", p.accountID(), path, name),
		Path:       path,
		CreateDate: time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(group)
	if err != nil {
		return nil, fmt.Errorf("encode iam group payload: %w", err)
	}
	if err := p.store.Set(ctx, iamGroupsStoreNamespace, name, string(raw)); err != nil {
		return nil, fmt.Errorf("store iam group: %w", err)
	}
	return map[string]any{
		"created": true,
		"group":   group,
		"uri":     "oc://iam/groups/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMDeleteGroup(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamGroupsStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("read iam group: %w", err)
	} else if !found {
		return nil, fmt.Errorf("iam group %q not found", name)
	}
	if err := p.store.Delete(ctx, iamGroupsStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("delete iam group: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://iam/groups/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMCreateInstanceProfile(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name  string   `json:"name"`
		Path  string   `json:"path"`
		Roles []string `json:"roles"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamProfilesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("check existing iam instance profile: %w", err)
	} else if found {
		return nil, fmt.Errorf("iam instance profile %q already exists", name)
	}
	path := strings.TrimSpace(args.Path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	profile := iam.InstanceProfile{
		InstanceProfileName: name,
		InstanceProfileId:   "AIPA" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", ""))[:16],
		Arn:                 fmt.Sprintf("arn:aws:iam::%s:instance-profile%s%s", p.accountID(), path, name),
		Path:                path,
		CreateDate:          time.Now().UTC().Format(time.RFC3339),
		Roles:               append([]string(nil), args.Roles...),
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("encode iam instance profile payload: %w", err)
	}
	if err := p.store.Set(ctx, iamProfilesStoreNamespace, name, string(raw)); err != nil {
		return nil, fmt.Errorf("store iam instance profile: %w", err)
	}
	return map[string]any{
		"created":          true,
		"instance_profile": profile,
		"uri":              "oc://iam/instance-profiles/" + url.PathEscape(name),
	}, nil
}

func (p *RuntimeProvider) toolIAMDeleteInstanceProfile(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, found, err := p.store.Get(ctx, iamProfilesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("read iam instance profile: %w", err)
	} else if !found {
		return nil, fmt.Errorf("iam instance profile %q not found", name)
	}
	if err := p.store.Delete(ctx, iamProfilesStoreNamespace, name); err != nil {
		return nil, fmt.Errorf("delete iam instance profile: %w", err)
	}
	return map[string]any{
		"deleted": true,
		"name":    name,
		"uri":     "oc://iam/instance-profiles/" + url.PathEscape(name),
	}, nil
}

// toolACMRequestCertificate used to build a Certificate by hand and write it
// straight into acm:certs — skipping the domain/SAN validation, the
// DomainValidationOptions RequestCertificate builds, and the injected clock,
// so a malformed domain name was accepted and a caller's DomainValidation
// read came back empty. It now dispatches through the router into ACM's own
// RequestCertificate, exactly as the SDK would, and reads the result back
// with DescribeCertificate — the same round trip the CloudFormation
// provisioner uses for other services (#2030).
func (p *RuntimeProvider) toolACMRequestCertificate(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		DomainName              string            `json:"domain_name"`
		SubjectAlternativeNames []string          `json:"subject_alternative_names"`
		Tags                    map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	domainName := strings.TrimSpace(args.DomainName)
	if domainName == "" {
		return nil, fmt.Errorf("domain_name is required")
	}
	reqBody := map[string]any{"DomainName": domainName}
	if len(args.SubjectAlternativeNames) > 0 {
		reqBody["SubjectAlternativeNames"] = args.SubjectAlternativeNames
	}
	if len(args.Tags) > 0 {
		tags := make([]acm.Tag, 0, len(args.Tags))
		for k, v := range args.Tags {
			tags = append(tags, acm.Tag{Key: k, Value: v})
		}
		sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
		reqBody["Tags"] = tags
	}
	respBody, err := p.invokeJSON(ctx, "CertificateManager.RequestCertificate", reqBody)
	if err != nil {
		return nil, fmt.Errorf("request certificate: %w", err)
	}
	var reqResp struct {
		CertificateArn string `json:"CertificateArn"`
	}
	if err := json.Unmarshal(respBody, &reqResp); err != nil {
		return nil, fmt.Errorf("decode request certificate response: %w", err)
	}
	describeBody, err := p.invokeJSON(ctx, "CertificateManager.DescribeCertificate", map[string]any{
		"CertificateArn": reqResp.CertificateArn,
	})
	if err != nil {
		return nil, fmt.Errorf("describe certificate: %w", err)
	}
	var describeResp struct {
		Certificate acm.Certificate `json:"Certificate"`
	}
	if err := json.Unmarshal(describeBody, &describeResp); err != nil {
		return nil, fmt.Errorf("decode describe certificate response: %w", err)
	}
	return map[string]any{
		"certificate": describeResp.Certificate,
		"uri":         "oc://acm/certificates/" + url.PathEscape(describeResp.Certificate.CertificateArn),
	}, nil
}

func (p *RuntimeProvider) toolACMDeleteCertificate(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		ARN string `json:"arn"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	arn := strings.TrimSpace(args.ARN)
	if arn == "" {
		return nil, fmt.Errorf("arn is required")
	}
	if _, found, err := p.store.Get(ctx, acmCertsStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("read acm certificate: %w", err)
	} else if !found {
		return nil, fmt.Errorf("acm certificate %q not found", arn)
	}
	if err := p.store.Delete(ctx, acmCertsStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("delete acm certificate: %w", err)
	}
	// best-effort: delete tags; ignore not-found
	_ = p.store.Delete(ctx, acmTagsStoreNamespace, arn)
	return map[string]any{
		"deleted": true,
		"arn":     arn,
	}, nil
}

func (p *RuntimeProvider) toolACMAddTagsToCertificate(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		ARN  string            `json:"arn"`
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	arn := strings.TrimSpace(args.ARN)
	if arn == "" {
		return nil, fmt.Errorf("arn is required")
	}
	if len(args.Tags) == 0 {
		return nil, fmt.Errorf("tags is required and must be non-empty")
	}
	if _, found, err := p.store.Get(ctx, acmCertsStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("read acm certificate: %w", err)
	} else if !found {
		return nil, fmt.Errorf("acm certificate %q not found", arn)
	}
	existing, err := p.listACMCertificateTags(ctx, arn)
	if err != nil {
		return nil, err
	}
	// merge: convert to map, overwrite, convert back
	tagMap := make(map[string]string, len(existing)+len(args.Tags))
	for _, t := range existing {
		tagMap[t.Key] = t.Value
	}
	for k, v := range args.Tags {
		tagMap[k] = v
	}
	merged := make([]acm.Tag, 0, len(tagMap))
	for k, v := range tagMap {
		merged = append(merged, acm.Tag{Key: k, Value: v})
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Key < merged[j].Key })
	tagBytes, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}
	if err := p.store.Set(ctx, acmTagsStoreNamespace, arn, string(tagBytes)); err != nil {
		return nil, fmt.Errorf("store tags: %w", err)
	}
	return map[string]any{
		"arn":  arn,
		"tags": merged,
	}, nil
}

func (p *RuntimeProvider) toolACMRemoveTagsFromCertificate(ctx context.Context, params json.RawMessage) (any, error) {
	var args struct {
		ARN     string   `json:"arn"`
		TagKeys []string `json:"tag_keys"`
	}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if p.store == nil {
		return nil, fmt.Errorf("runtime store is unavailable")
	}
	arn := strings.TrimSpace(args.ARN)
	if arn == "" {
		return nil, fmt.Errorf("arn is required")
	}
	if len(args.TagKeys) == 0 {
		return nil, fmt.Errorf("tag_keys is required and must be non-empty")
	}
	if _, found, err := p.store.Get(ctx, acmCertsStoreNamespace, arn); err != nil {
		return nil, fmt.Errorf("read acm certificate: %w", err)
	} else if !found {
		return nil, fmt.Errorf("acm certificate %q not found", arn)
	}
	existing, err := p.listACMCertificateTags(ctx, arn)
	if err != nil {
		return nil, err
	}
	removeSet := make(map[string]struct{}, len(args.TagKeys))
	for _, k := range args.TagKeys {
		removeSet[k] = struct{}{}
	}
	filtered := make([]acm.Tag, 0, len(existing))
	for _, t := range existing {
		if _, remove := removeSet[t.Key]; !remove {
			filtered = append(filtered, t)
		}
	}
	tagBytes, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("marshal tags: %w", err)
	}
	if err := p.store.Set(ctx, acmTagsStoreNamespace, arn, string(tagBytes)); err != nil {
		return nil, fmt.Errorf("store tags: %w", err)
	}
	return map[string]any{
		"arn":  arn,
		"tags": filtered,
	}, nil
}

// isKnownService reports whether service names a service Overcast implements.
// Every service is always running, so this is a spelling check on caller input,
// not an availability check.
func isKnownService(service string) bool {
	return slices.Contains(config.AllServices(), strings.ToLower(strings.TrimSpace(service)))
}

func resourceJSONContents(uri string, payload any) []map[string]any {
	b, err := json.Marshal(payload)
	if err != nil {
		return []map[string]any{{"uri": uri, "mimeType": "text/plain", "text": "{}"}}
	}
	return []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(b)}}
}

func parseRegionalResourceURI(rest, defaultRegion, resourceKind string) (string, string, error) {
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || len(parts) > 2 {
		return "", "", fmt.Errorf("invalid %s resource uri", resourceKind)
	}
	if len(parts) == 1 {
		decodedName, err := url.PathUnescape(parts[0])
		if err != nil || strings.TrimSpace(decodedName) == "" {
			return "", "", fmt.Errorf("invalid %s resource uri", resourceKind)
		}
		return defaultRegion, decodedName, nil
	}
	decodedRegion, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(decodedRegion) == "" {
		return "", "", fmt.Errorf("invalid %s resource uri", resourceKind)
	}
	decodedName, err := url.PathUnescape(parts[1])
	if err != nil || strings.TrimSpace(decodedName) == "" {
		return "", "", fmt.Errorf("invalid %s resource uri", resourceKind)
	}
	return decodedRegion, decodedName, nil
}

func (p *RuntimeProvider) listStepFunctionsStateMachines(ctx context.Context) ([]stepfunctions.StateMachine, error) {
	kvs, err := p.store.Scan(ctx, stepFunctionsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan stepfunctions state machines: %w", err)
	}
	stateMachines := make([]stepfunctions.StateMachine, 0, len(kvs))
	for _, kv := range kvs {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, stepFunctionsStateMachinePrefix) {
			continue
		}
		var stateMachine stepfunctions.StateMachine
		if err := json.Unmarshal([]byte(kv.Value), &stateMachine); err != nil {
			continue
		}
		stateMachines = append(stateMachines, stateMachine)
	}
	sort.Slice(stateMachines, func(i, j int) bool {
		return stateMachines[i].Name < stateMachines[j].Name
	})
	return stateMachines, nil
}

func (p *RuntimeProvider) listStepFunctionsExecutions(ctx context.Context) ([]stepfunctions.Execution, error) {
	kvs, err := p.store.Scan(ctx, stepFunctionsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan stepfunctions executions: %w", err)
	}
	executions := make([]stepfunctions.Execution, 0, len(kvs))
	for _, kv := range kvs {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, stepFunctionsExecutionPrefix) {
			continue
		}
		var execution stepfunctions.Execution
		if err := json.Unmarshal([]byte(kv.Value), &execution); err != nil {
			continue
		}
		executions = append(executions, execution)
	}
	sort.Slice(executions, func(i, j int) bool {
		return executions[i].ExecutionArn < executions[j].ExecutionArn
	})
	return executions, nil
}

func (p *RuntimeProvider) listSSMParameters(ctx context.Context) ([]ssm.ParameterRecord, error) {
	kvs, err := p.store.Scan(ctx, ssmStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan ssm parameters: %w", err)
	}
	parameters := make([]ssm.ParameterRecord, 0, len(kvs))
	for _, kv := range kvs {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, ssmParameterPrefix) {
			continue
		}
		var parameter ssm.ParameterRecord
		if err := json.Unmarshal([]byte(kv.Value), &parameter); err != nil {
			continue
		}
		parameters = append(parameters, parameter)
	}
	sort.Slice(parameters, func(i, j int) bool {
		return parameters[i].Name < parameters[j].Name
	})
	return parameters, nil
}

func (p *RuntimeProvider) toolRuntimeCapabilities(_ context.Context, params json.RawMessage) (any, error) {
	var args struct {
		Service string `json:"service"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, err
		}
	}

	// Build sorted list of services.
	enabled := config.AllServices()
	sort.Strings(enabled)

	// Filter if a specific service was requested.
	if args.Service != "" {
		found := false
		for _, s := range enabled {
			if s == args.Service {
				found = true
				break
			}
		}
		if !found {
			return nil, &jsonErr{code: -32602, msg: "service not enabled: " + args.Service}
		}
		enabled = []string{args.Service}
	}

	results := runtimeCapabilitiesDetail(enabled)
	region := ""
	if p.cfg != nil {
		region = p.cfg.Region
	}
	return map[string]any{
		"region":   region,
		"services": results,
	}, nil
}

func (p *RuntimeProvider) listKinesisStreams(ctx context.Context) ([]kinesis.Stream, error) {
	kvs, err := p.store.Scan(ctx, kinesisStreamsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan kinesis streams: %w", err)
	}
	streams := make([]kinesis.Stream, 0, len(kvs))
	for _, kv := range kvs {
		var stream kinesis.Stream
		if err := json.Unmarshal([]byte(kv.Value), &stream); err != nil {
			continue
		}
		streams = append(streams, stream)
	}
	sort.Slice(streams, func(i, j int) bool {
		return streams[i].StreamName < streams[j].StreamName
	})
	return streams, nil
}

func (p *RuntimeProvider) listSecretsManagerSecrets(ctx context.Context) ([]secretsmanager.Secret, error) {
	kvs, err := p.store.Scan(ctx, secretsManagerStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan secretsmanager secrets: %w", err)
	}
	secrets := make([]secretsmanager.Secret, 0, len(kvs))
	for _, kv := range kvs {
		var secret secretsmanager.Secret
		if err := json.Unmarshal([]byte(kv.Value), &secret); err != nil {
			continue
		}
		secrets = append(secrets, secret)
	}
	sort.Slice(secrets, func(i, j int) bool {
		return secrets[i].Name < secrets[j].Name
	})
	return secrets, nil
}

func (p *RuntimeProvider) listACMCertificates(ctx context.Context) ([]acm.Certificate, error) {
	kvs, err := p.store.Scan(ctx, acmCertsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan acm certificates: %w", err)
	}
	certs := make([]acm.Certificate, 0, len(kvs))
	for _, kv := range kvs {
		var cert acm.Certificate
		if err := json.Unmarshal([]byte(kv.Value), &cert); err != nil {
			continue
		}
		certs = append(certs, cert)
	}
	sort.Slice(certs, func(i, j int) bool {
		return certs[i].CertificateArn < certs[j].CertificateArn
	})
	return certs, nil
}

func (p *RuntimeProvider) listACMCertificateTags(ctx context.Context, certificateARN string) ([]acm.Tag, error) {
	raw, found, err := p.store.Get(ctx, acmTagsStoreNamespace, certificateARN)
	if err != nil {
		return nil, fmt.Errorf("read acm certificate tags: %w", err)
	}
	if !found {
		return []acm.Tag{}, nil
	}
	var tags []acm.Tag
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, fmt.Errorf("decode acm certificate tags payload: %w", err)
	}
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Key < tags[j].Key
	})
	return tags, nil
}

func (p *RuntimeProvider) listIAMUsers(ctx context.Context) ([]iam.User, error) {
	kvs, err := p.store.Scan(ctx, iamUsersStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan iam users: %w", err)
	}
	users := make([]iam.User, 0, len(kvs))
	for _, kv := range kvs {
		var user iam.User
		if err := json.Unmarshal([]byte(kv.Value), &user); err != nil {
			continue
		}
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool {
		return users[i].UserName < users[j].UserName
	})
	return users, nil
}

func (p *RuntimeProvider) listIAMRoles(ctx context.Context) ([]iam.Role, error) {
	kvs, err := p.store.Scan(ctx, iamRolesStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan iam roles: %w", err)
	}
	roles := make([]iam.Role, 0, len(kvs))
	for _, kv := range kvs {
		var role iam.Role
		if err := json.Unmarshal([]byte(kv.Value), &role); err != nil {
			continue
		}
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool {
		return roles[i].RoleName < roles[j].RoleName
	})
	return roles, nil
}

func (p *RuntimeProvider) listIAMPolicies(ctx context.Context) ([]iam.Policy, error) {
	kvs, err := p.store.Scan(ctx, iamPoliciesStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan iam policies: %w", err)
	}
	policies := make([]iam.Policy, 0, len(kvs))
	for _, kv := range kvs {
		var policy iam.Policy
		if err := json.Unmarshal([]byte(kv.Value), &policy); err != nil {
			continue
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool {
		return policies[i].PolicyName < policies[j].PolicyName
	})
	return policies, nil
}

func (p *RuntimeProvider) listIAMGroups(ctx context.Context) ([]iam.Group, error) {
	kvs, err := p.store.Scan(ctx, iamGroupsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan iam groups: %w", err)
	}
	groups := make([]iam.Group, 0, len(kvs))
	for _, kv := range kvs {
		var group iam.Group
		if err := json.Unmarshal([]byte(kv.Value), &group); err != nil {
			continue
		}
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].GroupName < groups[j].GroupName
	})
	return groups, nil
}

func (p *RuntimeProvider) listIAMInstanceProfiles(ctx context.Context) ([]iam.InstanceProfile, error) {
	kvs, err := p.store.Scan(ctx, iamProfilesStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan iam instance profiles: %w", err)
	}
	profiles := make([]iam.InstanceProfile, 0, len(kvs))
	for _, kv := range kvs {
		var profile iam.InstanceProfile
		if err := json.Unmarshal([]byte(kv.Value), &profile); err != nil {
			continue
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].InstanceProfileName < profiles[j].InstanceProfileName
	})
	return profiles, nil
}

func (p *RuntimeProvider) listKMSKeys(ctx context.Context) ([]kms.Key, error) {
	kvs, err := p.store.Scan(ctx, kmsStoreNamespace, "")
	if err != nil {
		return nil, fmt.Errorf("scan kms keys: %w", err)
	}
	keys := make([]kms.Key, 0, len(kvs))
	for _, kv := range kvs {
		_, rest := serviceutil.SplitRegionKey(kv.Key)
		if !strings.HasPrefix(rest, kmsKeyPrefix) {
			continue
		}
		var key kms.Key
		if err := json.Unmarshal([]byte(kv.Value), &key); err != nil {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].KeyID < keys[j].KeyID
	})
	return keys, nil
}

func secretsManagerTagsFromMap(tags map[string]string) []secretsmanager.Tag {
	if len(tags) == 0 {
		return nil
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]secretsmanager.Tag, 0, len(keys))
	for _, key := range keys {
		result = append(result, secretsmanager.Tag{Key: key, Value: tags[key]})
	}
	return result
}

func defaultSQSQueueAttributes() map[string]string {
	return map[string]string{
		"VisibilityTimeout":             "30",
		"MaximumMessageSize":            "262144",
		"MessageRetentionPeriod":        "345600",
		"DelaySeconds":                  "0",
		"ReceiveMessageWaitTimeSeconds": "0",
	}
}

func defaultDynamoProvisionedThroughput() *dynamodb.ProvisionedThroughput {
	return &dynamodb.ProvisionedThroughput{
		ReadCapacityUnits:  5,
		WriteCapacityUnits: 5,
	}
}

func applyDynamoDBStreamSpec(table *dynamodb.Table, region, accountID string) {
	label := time.Now().UTC().Format("2006-01-02T15:04:05.000")
	table.LatestStreamLabel = label
	table.LatestStreamArn = fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s/stream/%s", region, accountID, table.TableName, label)
}

func defaultSNSTopicAttributes(name, arn, accountID string) map[string]string {
	return map[string]string{
		"TopicArn":                arn,
		"SubscriptionsConfirmed":  "0",
		"SubscriptionsPending":    "0",
		"SubscriptionsDeleted":    "0",
		"EffectiveDeliveryPolicy": `{"defaultHealthyRetryPolicy":{"minDelayTarget":20,"maxDelayTarget":20,"numRetries":3,"numMaxDelayRetries":0,"numNoDelayRetries":0,"numMinDelayRetries":0,"backoffFunction":"linear"},"sicklyRetryPolicy":null,"throttlePolicy":null,"guaranteed":false}`,
		"DisplayName":             name,
		"Policy":                  "",
		"DeliveryPolicy":          "",
		"Owner":                   accountID,
	}
}

const (
	stepFunctionsStoreNamespace     = "stepfunctions"
	stepFunctionsStateMachinePrefix = "sm:"
	stepFunctionsExecutionPrefix    = "exec:"
	ssmStoreNamespace               = "ssm"
	ssmParameterPrefix              = "param:"
	kinesisStreamsStoreNamespace    = "kinesis:streams"
	kinesisRecordsStoreNamespace    = "kinesis:records"
	secretsManagerStoreNamespace    = "secretsmanager:secrets"
	kmsStoreNamespace               = "kms"
	kmsKeyPrefix                    = "key:"
	iamUsersStoreNamespace          = "iam:users"
	iamRolesStoreNamespace          = "iam:roles"
	iamPoliciesStoreNamespace       = "iam:policies"
	iamGroupsStoreNamespace         = "iam:groups"
	iamProfilesStoreNamespace       = "iam:profiles"
	acmCertsStoreNamespace          = "acm:certs"
	acmTagsStoreNamespace           = "acm:tags"
	lambdaFunctionsStoreNamespace   = "lambda:functions"
	ecrReposStoreNamespace          = "ecr:repositories"
	ecsClustersStoreNamespace       = "ecs:clusters"
	ecsTaskDefsStoreNamespace       = "ecs:task-definitions"
)

func (p *RuntimeProvider) defaultRegion() string {
	if p.cfg != nil && strings.TrimSpace(p.cfg.Region) != "" {
		return p.cfg.Region
	}
	return "us-east-1"
}

func (p *RuntimeProvider) accountID() string {
	if p.cfg != nil && strings.TrimSpace(p.cfg.AccountID) != "" {
		return p.cfg.AccountID
	}
	return "000000000000"
}

func (p *RuntimeProvider) enabledServices() []string {
	if p.cfg == nil {
		return nil
	}
	services := config.AllServices()
	sort.Strings(services)
	return services
}

func runtimeServiceFromURI(uri string) string {
	if !strings.HasPrefix(uri, "oc://") {
		return ""
	}
	rest := strings.TrimPrefix(uri, "oc://")
	if rest == "" {
		return ""
	}
	parts := strings.SplitN(rest, "/", 2)
	return strings.TrimSpace(parts[0])
}

func runtimeURIIsCollection(uri, service string) bool {
	prefix := "oc://" + service + "/"
	if !strings.HasPrefix(uri, prefix) {
		return false
	}
	rest := strings.TrimPrefix(uri, prefix)
	if rest == "" {
		return true
	}
	return !strings.Contains(rest, "/")
}

func (p *RuntimeProvider) externalBaseURL() string {
	if p.cfg != nil {
		return p.cfg.ExternalBaseURL()
	}
	return "http://localhost:4566"
}
