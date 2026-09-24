package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/mcp"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
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
	"github.com/overcast-sh/overcast/internal/state"
)

type runtimeResourceExpectation struct {
	uri      string
	contains []string
}

func seedRuntimeJSON(t *testing.T, ctx context.Context, store state.Store, namespace, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal seed %s/%s: %v", namespace, key, err)
	}
	if err := store.Set(ctx, namespace, key, string(raw)); err != nil {
		t.Fatalf("seed %s/%s: %v", namespace, key, err)
	}
}

func assertRuntimeResourceReads(t *testing.T, ctx context.Context, provider *RuntimeProvider, expectations []runtimeResourceExpectation) {
	t.Helper()
	for _, expectation := range expectations {
		contents, err := provider.ReadResource(ctx, expectation.uri)
		if err != nil {
			t.Fatalf("ReadResource(%q) error = %v", expectation.uri, err)
		}
		if len(contents) == 0 {
			t.Fatalf("ReadResource(%q) returned no contents", expectation.uri)
		}
		text, _ := contents[0]["text"].(string)
		if strings.TrimSpace(text) == "" {
			t.Fatalf("ReadResource(%q) returned empty text payload", expectation.uri)
		}
		for _, needle := range expectation.contains {
			if !strings.Contains(text, needle) {
				t.Fatalf("ReadResource(%q) payload missing %q: %q", expectation.uri, needle, text)
			}
		}
	}
}

func assertAllListedOCResourcesResolve(t *testing.T, ctx context.Context, provider *RuntimeProvider) {
	t.Helper()
	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	expectations := make([]runtimeResourceExpectation, 0, len(resources))
	for _, res := range resources {
		uri, _ := res["uri"].(string)
		if strings.HasPrefix(uri, "oc://") {
			expectations = append(expectations, runtimeResourceExpectation{uri: uri})
		}
	}
	assertRuntimeResourceReads(t, ctx, provider, expectations)
}

// newACMTestRouter builds just enough of the emulator's middleware chain to
// serve ACM's own Dispatch: X-Amz-Target codec detection and the region
// header ACM's typed handlers read through middleware.RegionFromContext. The
// full router (internal/router.New) cannot be imported here — it imports this
// package to build the runtime MCP provider, and that import would cycle.
func newACMTestRouter(t *testing.T, cfg *config.Config, store state.Store, clk clock.Clock) http.Handler {
	t.Helper()
	svc := acm.New(cfg, store, zap.NewNop(), clk)
	r := chi.NewRouter()
	r.Use(middleware.Region)
	r.Use(middleware.Protocol(codec.DefaultIdentifiers()))
	r.Post("/", svc.Dispatch)
	return r
}

func assertNoStateKeyFallback(t *testing.T, services []map[string]any, typedServices map[string]struct{}) {
	t.Helper()
	for _, svcEntry := range services {
		svc, _ := svcEntry["service"].(string)
		if _, typed := typedServices[svc]; !typed {
			continue
		}
		resources, _ := svcEntry["resources"].([]map[string]any)
		for _, res := range resources {
			if src, _ := res["source"].(string); src == "state_key" {
				t.Fatalf("typed service %q should not include state_key fallback resources: %#v", svc, resources)
			}
		}
	}
}

var runtimeMutationToolNames = []string{
	"runtime_s3_create_bucket",
	"runtime_s3_put_bucket_tags",
	"runtime_s3_delete_bucket",
	"runtime_sqs_create_queue",
	"runtime_sqs_set_queue_attributes",
	"runtime_sqs_delete_queue",
	"runtime_sqs_purge_queue",
	"runtime_dynamodb_create_table",
	"runtime_dynamodb_update_table_ttl",
	"runtime_sns_create_topic",
	"runtime_sns_set_topic_attributes",
	"runtime_sns_delete_topic",
	"runtime_kinesis_create_stream",
	"runtime_kinesis_put_record",
	"runtime_kinesis_delete_stream",
	"runtime_kms_create_key",
	"runtime_kms_disable_key",
	"runtime_kms_schedule_key_deletion",
	"runtime_stepfunctions_create_state_machine",
	"runtime_stepfunctions_start_execution",
	"runtime_stepfunctions_delete_state_machine",
	"runtime_ssm_put_parameter",
	"runtime_ssm_delete_parameter",
	"runtime_secretsmanager_create_secret",
	"runtime_secretsmanager_put_secret_value",
	"runtime_secretsmanager_delete_secret",
	"runtime_iam_create_user",
	"runtime_iam_delete_user",
	"runtime_iam_create_role",
	"runtime_iam_delete_role",
	"runtime_iam_create_policy",
	"runtime_iam_delete_policy",
	"runtime_iam_create_group",
	"runtime_iam_delete_group",
	"runtime_iam_create_instance_profile",
	"runtime_iam_delete_instance_profile",
	"runtime_acm_request_certificate",
	"runtime_acm_delete_certificate",
	"runtime_acm_add_tags_to_certificate",
	"runtime_acm_remove_tags_from_certificate",
}

func TestRuntimeProvider_RuntimeMutationToolsAdvertiseExplicitOutputSchemas(t *testing.T) {
	provider := NewRuntimeProvider(nil, nil)
	toolsByName := map[string]mcp.Tool{}
	for _, tool := range provider.Tools() {
		toolsByName[tool.Name] = tool
	}
	for _, name := range runtimeMutationToolNames {
		tool, ok := toolsByName[name]
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if len(tool.OutputSchema) == 0 {
			t.Fatalf("tool %q missing outputSchema", name)
		}
		if string(tool.OutputSchema) == "true" {
			t.Fatalf("tool %q still has fallback outputSchema", name)
		}
	}
}

func TestRuntimeProvider_RuntimeMutationToolsAdvertiseExecutionHints(t *testing.T) {
	provider := NewRuntimeProvider(nil, nil)
	toolsByName := map[string]mcp.Tool{}
	for _, tool := range provider.Tools() {
		toolsByName[tool.Name] = tool
	}
	for _, name := range runtimeMutationToolNames {
		tool, ok := toolsByName[name]
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if tool.Execution == nil {
			t.Fatalf("tool %q missing execution metadata", name)
		}
		for _, key := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
			value, ok := tool.Execution[key]
			if !ok {
				t.Fatalf("tool %q missing execution hint %q", name, key)
			}
			if _, ok := value.(bool); !ok {
				t.Fatalf("tool %q execution hint %q has non-bool type %T", name, key, value)
			}
		}
	}
}

func TestRuntimeProvider_ListServices_CountsKeysAcrossRealServiceNamespaces(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region: "us-east-1",
	}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "s3:buckets", "demo-bucket", map[string]any{"name": "demo-bucket"})
	seedRuntimeJSON(t, ctx, store, iamUsersStoreNamespace, "demo-user", iam.User{UserName: "demo-user", UserId: "AIDA1234567890DEMO", Arn: "arn:aws:iam::000000000000:user/demo-user", CreateDate: "2026-04-21T00:00:00Z", Path: "/"})
	seedRuntimeJSON(t, ctx, store, iamRolesStoreNamespace, "demo-role", iam.Role{RoleName: "demo-role", RoleId: "AROA1234567890DEMO", Arn: "arn:aws:iam::000000000000:role/demo-role", CreateDate: "2026-04-21T00:00:00Z", Path: "/"})

	out, err := provider.toolListServices(ctx, nil)
	if err != nil {
		t.Fatalf("toolListServices() error = %v", err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal toolListServices() result: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal toolListServices() result: %v", err)
	}
	// Every service is always running, so all of them are listed; what the
	// tool reports per service is how many keys it actually owns.
	services, _ := body["services"].([]any)
	if len(services) != len(config.AllServices()) {
		t.Fatalf("services length = %d, want %d (%#v)", len(services), len(config.AllServices()), body["services"])
	}
	counts := map[string]float64{}
	for _, entry := range services {
		serviceEntry, _ := entry.(map[string]any)
		name, _ := serviceEntry["name"].(string)
		keyCount, _ := serviceEntry["key_count"].(float64)
		counts[name] = keyCount
	}
	if counts["s3"] != 1 {
		t.Fatalf("s3 key_count = %v, want 1", counts["s3"])
	}
	if counts["iam"] != 2 {
		t.Fatalf("iam key_count = %v, want 2", counts["iam"])
	}
	// A service nothing was seeded into reports zero rather than dropping out.
	if counts["sqs"] != 0 {
		t.Fatalf("sqs key_count = %v, want 0", counts["sqs"])
	}
}

func TestRuntimeProvider_GetHealthAndConfigTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Host:      "0.0.0.0",
		Port:      4566,
		Region:    "us-east-1",
		AccountID: "000000000000",
		Version:   "dev",
		LogLevel:  "info",
		Debug:     true,
		State:     config.StateBackendMemory,
	}
	provider := NewRuntimeProvider(cfg, store)

	healthOut, err := provider.toolGetHealth(ctx, nil)
	if err != nil {
		t.Fatalf("toolGetHealth() error = %v", err)
	}
	health := healthOut.(map[string]any)
	if health["status"] != "ok" {
		t.Fatalf("status = %#v, want ok", health["status"])
	}
	services, _ := health["services"].([]string)
	if len(services) != len(config.AllServices()) {
		t.Fatalf("services = %#v, want all %d services", health["services"], len(config.AllServices()))
	}

	configOut, err := provider.toolGetConfig(ctx, nil)
	if err != nil {
		t.Fatalf("toolGetConfig() error = %v", err)
	}
	cfgBody := configOut.(map[string]any)
	if cfgBody["debug_required"].(bool) {
		t.Fatalf("debug_required = true, want false")
	}
	if cfgBody["region"] != "us-east-1" {
		t.Fatalf("region = %#v, want us-east-1", cfgBody["region"])
	}
}

func TestRuntimeProvider_GetConfigRequiresDebugWhenDisabled(t *testing.T) {
	provider := NewRuntimeProvider(&config.Config{Debug: false}, state.NewMemoryStore())
	out, err := provider.toolGetConfig(context.Background(), nil)
	if err != nil {
		t.Fatalf("toolGetConfig() error = %v", err)
	}
	body := out.(map[string]any)
	if required, _ := body["debug_required"].(bool); !required {
		t.Fatalf("debug_required = %#v, want true", body["debug_required"])
	}
}

func TestRuntimeProvider_GetServiceStateTool(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Debug: true}
	provider := NewRuntimeProvider(cfg, store)

	if err := store.Set(ctx, "s3:buckets", "demo-a", "{}a"); err != nil {
		t.Fatalf("seed demo-a: %v", err)
	}
	if err := store.Set(ctx, "s3:buckets", "demo-b", "{}b"); err != nil {
		t.Fatalf("seed demo-b: %v", err)
	}

	out, err := provider.toolGetServiceState(ctx, json.RawMessage(`{"namespace":"s3:buckets","key_pattern":"demo","limit":1}`))
	if err != nil {
		t.Fatalf("toolGetServiceState() error = %v", err)
	}
	body := out.(map[string]any)
	if body["count"].(int) != 1 {
		t.Fatalf("count = %#v, want 1", body["count"])
	}
	if !body["truncated"].(bool) {
		t.Fatalf("truncated = %#v, want true", body["truncated"])
	}
	entries := body["entries"].(map[string]any)
	if len(entries) != 1 {
		t.Fatalf("entries len = %d, want 1 (%#v)", len(entries), entries)
	}
}

func TestRuntimeProvider_GetRecentEventsTool(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	provider := NewRuntimeProvider(&config.Config{}, store)

	bus := events.NewBus()
	defer bus.Stop()
	provider.AttachEventBus(bus)

	bus.Publish(ctx, events.Event{
		Type:    events.SQSQueueCreated,
		Source:  "sqs",
		Time:    time.Now().UTC().Add(-time.Second),
		Payload: events.ResourcePayload{Name: "queue-a"},
	})
	bus.Publish(ctx, events.Event{
		Type:    events.S3BucketCreated,
		Source:  "s3",
		Time:    time.Now().UTC(),
		Payload: events.ResourcePayload{Name: "bucket-a"},
	})

	var out any
	var err error
	for i := 0; i < 20; i++ {
		out, err = provider.toolGetRecentEvents(ctx, json.RawMessage(`{"limit":5}`))
		if err != nil {
			t.Fatalf("toolGetRecentEvents() error = %v", err)
		}
		body := out.(map[string]any)
		if body["count"].(int) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := out.(map[string]any)
	if body["count"].(int) != 2 {
		t.Fatalf("count = %v, want 2", body["count"])
	}
	eventsList, _ := body["events"].([]map[string]any)
	if len(eventsList) != 2 {
		t.Fatalf("events len = %d, want 2 (%#v)", len(eventsList), body["events"])
	}
	sources := map[string]bool{}
	for _, event := range eventsList {
		source, _ := event["source"].(string)
		sources[source] = true
	}
	if !sources["s3"] || !sources["sqs"] {
		t.Fatalf("expected s3 and sqs events, got %#v", eventsList)
	}

	filteredOut, err := provider.toolGetRecentEvents(ctx, json.RawMessage(`{"source":"s3","type":"s3:BucketCreated","limit":1}`))
	if err != nil {
		t.Fatalf("toolGetRecentEvents(filtered) error = %v", err)
	}
	filtered := filteredOut.(map[string]any)
	if filtered["count"].(int) != 1 {
		t.Fatalf("filtered count = %v, want 1", filtered["count"])
	}
	if filtered["truncated"].(bool) {
		t.Fatalf("filtered truncated = true, want false")
	}
}

func TestRuntimeProvider_EventBusMutation_EmitsResourceListChanged(t *testing.T) {
	store := state.NewMemoryStore()
	provider := NewRuntimeProvider(&config.Config{Region: "us-east-1"}, store)

	listChangedCount := 0
	updatedURIs := make([]string, 0, 1)
	provider.SetResourceListChangedEmitter(func() {
		listChangedCount++
	})
	provider.SetResourceUpdatedEmitter(func(uri string) {
		updatedURIs = append(updatedURIs, uri)
	})

	provider.recordRecentEvent(events.Event{
		Type:   events.S3BucketCreated,
		Source: "s3",
		Time:   time.Now().UTC(),
		Payload: events.ResourcePayload{
			Name: "bucket-a",
		},
	})

	if listChangedCount != 1 {
		t.Fatalf("resource list changed emits = %d, want 1", listChangedCount)
	}
	if len(updatedURIs) != 1 || updatedURIs[0] != "oc://s3/buckets/bucket-a" {
		t.Fatalf("updated URIs = %#v, want [oc://s3/buckets/bucket-a]", updatedURIs)
	}
}

func TestRuntimeProvider_EventBusNonMutation_DoesNotEmitResourceListChanged(t *testing.T) {
	store := state.NewMemoryStore()
	provider := NewRuntimeProvider(&config.Config{Region: "us-east-1"}, store)

	listChangedCount := 0
	updatedCount := 0
	provider.SetResourceListChangedEmitter(func() {
		listChangedCount++
	})
	provider.SetResourceUpdatedEmitter(func(string) {
		updatedCount++
	})

	provider.recordRecentEvent(events.Event{
		Type:   events.Type("s3:BucketListed"),
		Source: "s3",
		Time:   time.Now().UTC(),
	})

	if listChangedCount != 0 {
		t.Fatalf("resource list changed emits = %d, want 0", listChangedCount)
	}
	if updatedCount != 0 {
		t.Fatalf("resource updated emits = %d, want 0", updatedCount)
	}
}

func TestRuntimeProvider_EventBusUpdate_EmitsResourceUpdatedWithoutListChanged(t *testing.T) {
	store := state.NewMemoryStore()
	provider := NewRuntimeProvider(&config.Config{Region: "us-west-2"}, store)

	listChangedCount := 0
	updatedURIs := make([]string, 0, 1)
	provider.SetResourceListChangedEmitter(func() {
		listChangedCount++
	})
	provider.SetResourceUpdatedEmitter(func(uri string) {
		updatedURIs = append(updatedURIs, uri)
	})

	provider.recordRecentEvent(events.Event{
		Type:   events.LambdaFunctionUpdated,
		Source: "lambda",
		Time:   time.Now().UTC(),
		Payload: events.LambdaFunctionPayload{
			Name: "my-func",
			ARN:  "arn:aws:lambda:us-west-2:000000000000:function:my-func",
		},
	})

	if listChangedCount != 0 {
		t.Fatalf("resource list changed emits = %d, want 0", listChangedCount)
	}
	if len(updatedURIs) != 1 || updatedURIs[0] != "oc://lambda/functions/us-west-2/my-func" {
		t.Fatalf("updated URIs = %#v, want [oc://lambda/functions/us-west-2/my-func]", updatedURIs)
	}
}

func TestRuntimeProvider_ProbeKVStoreSupportsCursorAndLimit(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	if err := store.Set(ctx, "s3:buckets", "alpha", "111"); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	if err := store.Set(ctx, "s3:buckets", "bravo", "222"); err != nil {
		t.Fatalf("seed bravo: %v", err)
	}
	if err := store.Set(ctx, "s3:buckets", "charlie", "333"); err != nil {
		t.Fatalf("seed charlie: %v", err)
	}

	out, err := provider.toolProbeKVStore(ctx, json.RawMessage(`{"namespace":"s3:buckets","limit":2}`))
	if err != nil {
		t.Fatalf("toolProbeKVStore() error = %v", err)
	}
	body, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if count, _ := body["count"].(int); count != 2 {
		t.Fatalf("count = %v, want 2", body["count"])
	}
	if truncated, _ := body["truncated"].(bool); !truncated {
		t.Fatalf("truncated = %v, want true", body["truncated"])
	}
	if next, _ := body["next_cursor"].(string); strings.TrimSpace(next) == "" {
		t.Fatalf("next_cursor missing: %#v", body)
	}
}

func TestRuntimeProvider_ProbeKVStoreIncludesValuesAndPatternFilter(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	if err := store.Set(ctx, ssmStoreNamespace, ssmParameterPrefix+"/app/db/password", "very-long-secret-value-preview-me"); err != nil {
		t.Fatalf("seed ssm value: %v", err)
	}
	if err := store.Set(ctx, ssmStoreNamespace, ssmParameterPrefix+"/app/cache/url", "redis://cache"); err != nil {
		t.Fatalf("seed ssm cache: %v", err)
	}

	out, err := provider.toolProbeKVStore(ctx, json.RawMessage(`{"namespace":"ssm","key_pattern":"password","include_values":true,"preview_bytes":8}`))
	if err != nil {
		t.Fatalf("toolProbeKVStore() error = %v", err)
	}
	body, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	entries, _ := body["entries"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("entries length = %d, want 1 (%#v)", len(entries), body["entries"])
	}
	if _, ok := entries[0]["value"]; !ok {
		t.Fatalf("expected full value in entry: %#v", entries[0])
	}
	if preview, _ := entries[0]["value_preview"].(string); len(preview) != 8 {
		t.Fatalf("preview length = %d, want 8 (%q)", len(preview), preview)
	}
}

func TestRuntimeProvider_ListAndReadS3Resources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "s3:buckets", "demo-bucket", map[string]any{"name": "demo-bucket", "region": "us-east-1"})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) < 2 {
		t.Fatalf("expected collection + bucket resources, got %#v", resources)
	}
	haveCollection := false
	haveBucket := false
	for _, r := range resources {
		if r["uri"] == "oc://s3/buckets" {
			haveCollection = true
		}
		if r["uri"] == "oc://s3/buckets/demo-bucket" {
			haveBucket = true
		}
	}
	if !haveCollection || !haveBucket {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://s3/buckets", contains: []string{"demo-bucket"}},
		{uri: "oc://s3/buckets/demo-bucket", contains: []string{"demo-bucket"}},
	})
}

func TestRuntimeProvider_S3MutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{"name": "unit-bucket", "tags": map[string]string{"team": "platform"}})
	created, err := provider.toolS3CreateBucket(ctx, createParams)
	if err != nil {
		t.Fatalf("toolS3CreateBucket() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}

	putTagsParams, _ := json.Marshal(map[string]any{"name": "unit-bucket", "tags": map[string]string{"env": "ci"}})
	updated, err := provider.toolS3PutBucketTags(ctx, putTagsParams)
	if err != nil {
		t.Fatalf("toolS3PutBucketTags() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	bucket := updatedMap["bucket"].(map[string]any)
	tags := bucket["tags"].(map[string]string)
	if tags["env"] != "ci" {
		t.Fatalf("unexpected tags after update: %#v", tags)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-bucket"})
	deleted, err := provider.toolS3DeleteBucket(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolS3DeleteBucket() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "s3:buckets", "unit-bucket"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected bucket to be deleted")
	}
}

func TestRuntimeProvider_ListAndReadSQSResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	queue := map[string]any{
		"name":              "demo-queue",
		"url":               "http://localhost:4566/000000000000/demo-queue",
		"arn":               "arn:aws:sqs:us-east-1:000000000000:demo-queue",
		"attributes":        map[string]string{"VisibilityTimeout": "30"},
		"created_timestamp": int64(1),
	}
	raw, _ := json.Marshal(queue)
	if err := store.Set(ctx, "sqs:queues", "us-east-1/demo-queue", string(raw)); err != nil {
		t.Fatalf("seed queue: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveQueue := false
	for _, r := range resources {
		if r["uri"] == "oc://sqs/queues" {
			haveCollection = true
		}
		if r["uri"] == "oc://sqs/queues/us-east-1/demo-queue" {
			haveQueue = true
		}
	}
	if !haveCollection || !haveQueue {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://sqs/queues", contains: []string{"demo-queue"}},
		{uri: "oc://sqs/queues/us-east-1/demo-queue", contains: []string{"demo-queue"}},
	})
}

func TestRuntimeProvider_SQSMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
		Hostname:  "localhost",
		Port:      4566,
	}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{"name": "unit-queue", "attributes": map[string]string{"DelaySeconds": "5"}})
	created, err := provider.toolSQSCreateQueue(ctx, createParams)
	if err != nil {
		t.Fatalf("toolSQSCreateQueue() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}

	setParams, _ := json.Marshal(map[string]any{"name": "unit-queue", "attributes": map[string]string{"VisibilityTimeout": "60"}})
	updated, err := provider.toolSQSSetQueueAttributes(ctx, setParams)
	if err != nil {
		t.Fatalf("toolSQSSetQueueAttributes() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	queue := updatedMap["queue"].(map[string]any)
	attrs := queue["attributes"].(map[string]any)
	if attrs["VisibilityTimeout"] != "60" {
		t.Fatalf("unexpected attributes after update: %#v", attrs)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-queue"})
	deleted, err := provider.toolSQSDeleteQueue(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolSQSDeleteQueue() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "sqs:queues", "us-east-1/unit-queue"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected queue to be deleted")
	}
}

func TestRuntimeProvider_SQSPurgeQueue(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
		Hostname:  "localhost",
		Port:      4566,
	}
	provider := NewRuntimeProvider(cfg, store)

	// Create a queue
	createParams, _ := json.Marshal(map[string]any{"name": "test-purge-queue"})
	_, err := provider.toolSQSCreateQueue(ctx, createParams)
	if err != nil {
		t.Fatalf("toolSQSCreateQueue() error = %v", err)
	}

	// Seed some messages in the queue's namespace
	for i := 0; i < 3; i++ {
		msgKey := fmt.Sprintf("us-east-1/test-purge-queue/msg-%d", i)
		msgData := fmt.Sprintf(`{"MessageId":"msg-%d"}`, i)
		if err := store.Set(ctx, "sqs:messages", msgKey, msgData); err != nil {
			t.Fatalf("store.Set() error = %v", err)
		}
	}

	// Verify messages exist
	msgPairs, err := store.Scan(ctx, "sqs:messages", "us-east-1/test-purge-queue/")
	if err != nil {
		t.Fatalf("store.Scan() error = %v", err)
	}
	if len(msgPairs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgPairs))
	}

	// Purge the queue
	purgeParams, _ := json.Marshal(map[string]any{"name": "test-purge-queue"})
	purged, err := provider.toolSQSPurgeQueue(ctx, purgeParams)
	if err != nil {
		t.Fatalf("toolSQSPurgeQueue() error = %v", err)
	}

	purgedMap := purged.(map[string]any)
	if purgedMap["purged"] != true {
		t.Fatalf("unexpected purge result: %#v", purgedMap)
	}
	messagesCleared, ok := purgedMap["messages_cleared"].(int)
	if !ok {
		t.Fatalf("messages_cleared type = %T, want int", purgedMap["messages_cleared"])
	}
	if messagesCleared != 3 {
		t.Fatalf("messages_cleared = %d, want 3", messagesCleared)
	}

	// Verify messages are gone
	msgPairs, err = store.Scan(ctx, "sqs:messages", "us-east-1/test-purge-queue/")
	if err != nil {
		t.Fatalf("store.Scan() error = %v", err)
	}
	if len(msgPairs) != 0 {
		t.Fatalf("expected 0 messages after purge, got %d", len(msgPairs))
	}

	// Verify queue still exists
	queueData, found, err := store.Get(ctx, "sqs:queues", "us-east-1/test-purge-queue")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if !found {
		t.Fatal("expected queue to still exist after purge")
	}
	if len(queueData) == 0 {
		t.Fatal("expected non-empty queue data")
	}
}

func TestRuntimeProvider_ListAndReadDynamoDBResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	table := dynamodb.Table{
		TableName:            "demo-table",
		TableStatus:          "ACTIVE",
		BillingMode:          "PAY_PER_REQUEST",
		TableARN:             "arn:aws:dynamodb:us-east-1:000000000000:table/demo-table",
		CreationDateTime:     1,
		AttributeDefinitions: []dynamodb.AttributeDef{{AttributeName: "pk", AttributeType: "S"}},
		KeySchema:            []dynamodb.KeySchemaElement{{AttributeName: "pk", KeyType: "HASH"}},
	}
	raw, _ := json.Marshal(table)
	if err := store.Set(ctx, "dynamodb:tables", "us-east-1/demo-table", string(raw)); err != nil {
		t.Fatalf("seed table: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveTable := false
	for _, r := range resources {
		if r["uri"] == "oc://dynamodb/tables" {
			haveCollection = true
		}
		if r["uri"] == "oc://dynamodb/tables/us-east-1/demo-table" {
			haveTable = true
		}
	}
	if !haveCollection || !haveTable {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://dynamodb/tables")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "demo-table") {
		t.Fatalf("collection payload missing table: %q", text)
	}

	tableContents, err := provider.ReadResource(ctx, "oc://dynamodb/tables/us-east-1/demo-table")
	if err != nil {
		t.Fatalf("ReadResource(table) error = %v", err)
	}
	tableText, _ := tableContents[0]["text"].(string)
	if !strings.Contains(tableText, "demo-table") {
		t.Fatalf("table payload missing table: %q", tableText)
	}
}

func TestRuntimeProvider_DynamoDBMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
	}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{
		"table_name":            "unit-table",
		"key_schema":            []map[string]any{{"AttributeName": "pk", "KeyType": "HASH"}},
		"attribute_definitions": []map[string]any{{"AttributeName": "pk", "AttributeType": "S"}},
		"stream_specification":  map[string]any{"StreamEnabled": true, "StreamViewType": "NEW_AND_OLD_IMAGES"},
	})
	created, err := provider.toolDynamoDBCreateTable(ctx, createParams)
	if err != nil {
		t.Fatalf("toolDynamoDBCreateTable() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdTable := createdMap["table"].(*dynamodb.Table)
	if createdTable.TableName != "unit-table" {
		t.Fatalf("unexpected table after create: %#v", createdTable)
	}
	if createdTable.LatestStreamArn == "" {
		t.Fatalf("expected stream ARN to be populated: %#v", createdTable)
	}

	ttlParams, _ := json.Marshal(map[string]any{
		"table_name":                 "unit-table",
		"time_to_live_specification": map[string]any{"Enabled": true, "AttributeName": "expires_at"},
	})
	updated, err := provider.toolDynamoDBUpdateTableTTL(ctx, ttlParams)
	if err != nil {
		t.Fatalf("toolDynamoDBUpdateTableTTL() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	if updatedMap["updated"] != true {
		t.Fatalf("unexpected update result: %#v", updatedMap)
	}
	updatedTable := updatedMap["table"].(*dynamodb.Table)
	if updatedTable.TTL == nil || updatedTable.TTL.AttributeName != "expires_at" {
		t.Fatalf("unexpected TTL after update: %#v", updatedTable.TTL)
	}

	raw, found, err := store.Get(ctx, "dynamodb:tables", "us-east-1/unit-table")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if !found {
		t.Fatal("expected table to be stored")
	}
	if !strings.Contains(raw, "expires_at") {
		t.Fatalf("stored payload missing TTL update: %q", raw)
	}
}

func TestRuntimeProvider_ListAndReadSNSResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	topic := sns.Topic{
		Name:             "demo-topic",
		ARN:              "arn:aws:sns:us-east-1:000000000000:demo-topic",
		Attributes:       map[string]string{"DisplayName": "demo-topic"},
		CreatedTimestamp: 1,
	}
	raw, _ := json.Marshal(topic)
	if err := store.Set(ctx, "sns:topics", "us-east-1/demo-topic", string(raw)); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveTopic := false
	for _, r := range resources {
		if r["uri"] == "oc://sns/topics" {
			haveCollection = true
		}
		if r["uri"] == "oc://sns/topics/us-east-1/demo-topic" {
			haveTopic = true
		}
	}
	if !haveCollection || !haveTopic {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://sns/topics")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "demo-topic") {
		t.Fatalf("collection payload missing topic: %q", text)
	}

	topicContents, err := provider.ReadResource(ctx, "oc://sns/topics/us-east-1/demo-topic")
	if err != nil {
		t.Fatalf("ReadResource(topic) error = %v", err)
	}
	topicText, _ := topicContents[0]["text"].(string)
	if !strings.Contains(topicText, "demo-topic") {
		t.Fatalf("topic payload missing topic: %q", topicText)
	}
}

func TestRuntimeProvider_SNSMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
	}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{"name": "unit-topic"})
	created, err := provider.toolSNSCreateTopic(ctx, createParams)
	if err != nil {
		t.Fatalf("toolSNSCreateTopic() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdTopic := createdMap["topic"].(sns.Topic)
	if createdTopic.ARN == "" || createdTopic.Name != "unit-topic" {
		t.Fatalf("unexpected topic after create: %#v", createdTopic)
	}

	setParams, _ := json.Marshal(map[string]any{"name": "unit-topic", "attributes": map[string]string{"DisplayName": "Unit Topic"}})
	updated, err := provider.toolSNSSetTopicAttributes(ctx, setParams)
	if err != nil {
		t.Fatalf("toolSNSSetTopicAttributes() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	updatedTopic := updatedMap["topic"].(sns.Topic)
	if updatedTopic.Attributes["DisplayName"] != "Unit Topic" {
		t.Fatalf("unexpected attributes after update: %#v", updatedTopic.Attributes)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-topic"})
	deleted, err := provider.toolSNSDeleteTopic(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolSNSDeleteTopic() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "sns:topics", "us-east-1/unit-topic"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected topic to be deleted")
	}
}

func TestRuntimeProvider_ListAndReadKMSResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	createdAt := time.Unix(1710000000, 0).UTC()
	key := kms.Key{
		KeyID:       "abcd-1234",
		ARN:         "arn:aws:kms:us-east-1:000000000000:key/abcd-1234",
		Description: "demo key",
		KeySpec:     "SYMMETRIC_DEFAULT",
		KeyUsage:    "ENCRYPT_DECRYPT",
		Enabled:     true,
		KeyState:    "Enabled",
		CreatedAt:   createdAt,
	}
	raw, _ := json.Marshal(key)
	if err := store.Set(ctx, "kms", "us-east-1/key:abcd-1234", string(raw)); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveKey := false
	for _, r := range resources {
		if r["uri"] == "oc://kms/keys" {
			haveCollection = true
		}
		if r["uri"] == "oc://kms/keys/us-east-1/abcd-1234" {
			haveKey = true
		}
	}
	if !haveCollection || !haveKey {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://kms/keys")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "abcd-1234") {
		t.Fatalf("collection payload missing key: %q", text)
	}

	keyContents, err := provider.ReadResource(ctx, "oc://kms/keys/us-east-1/abcd-1234")
	if err != nil {
		t.Fatalf("ReadResource(key) error = %v", err)
	}
	keyText, _ := keyContents[0]["text"].(string)
	if !strings.Contains(keyText, "demo key") {
		t.Fatalf("key payload missing description: %q", keyText)
	}
}

func TestRuntimeProvider_KMSMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{
		"description": "unit key",
		"tags":        map[string]string{"team": "platform"},
	})
	created, err := provider.toolKMSCreateKey(ctx, createParams)
	if err != nil {
		t.Fatalf("toolKMSCreateKey() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdKey := createdMap["key"].(kms.Key)
	if createdKey.KeyID == "" || createdKey.KeyState != "Enabled" {
		t.Fatalf("unexpected key after create: %#v", createdKey)
	}
	if len(createdKey.Tags) != 1 || createdKey.Tags[0].TagKey != "team" || createdKey.Tags[0].TagValue != "platform" {
		t.Fatalf("unexpected key tags after create: %#v", createdKey.Tags)
	}

	disableParams, _ := json.Marshal(map[string]any{"key_id": createdKey.KeyID})
	disabled, err := provider.toolKMSDisableKey(ctx, disableParams)
	if err != nil {
		t.Fatalf("toolKMSDisableKey() error = %v", err)
	}
	disabledMap := disabled.(map[string]any)
	if disabledMap["updated"] != true {
		t.Fatalf("unexpected disable result: %#v", disabledMap)
	}
	disabledKey := disabledMap["key"].(kms.Key)
	if disabledKey.Enabled || disabledKey.KeyState != "Disabled" {
		t.Fatalf("unexpected key after disable: %#v", disabledKey)
	}

	deleteParams, _ := json.Marshal(map[string]any{"key_id": createdKey.KeyID, "pending_window_in_days": 7})
	deleted, err := provider.toolKMSScheduleKeyDeletion(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolKMSScheduleKeyDeletion() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["scheduled"] != true {
		t.Fatalf("unexpected schedule deletion result: %#v", deletedMap)
	}
	deletedKey := deletedMap["key"].(kms.Key)
	if deletedKey.KeyState != "PendingDeletion" || deletedKey.DeletionDate == nil {
		t.Fatalf("unexpected key after schedule deletion: %#v", deletedKey)
	}

	raw, found, err := store.Get(ctx, "kms", "us-east-1/key:"+createdKey.KeyID)
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if !found {
		t.Fatal("expected key to remain stored after scheduling deletion")
	}
	if !strings.Contains(raw, "PendingDeletion") {
		t.Fatalf("stored payload missing pending deletion state: %q", raw)
	}
}

func TestRuntimeProvider_ListAndReadStepFunctionsResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	stateMachine := stepfunctions.StateMachine{
		Name:       "demo-sm",
		ARN:        "arn:aws:states:us-east-1:000000000000:stateMachine:demo-sm",
		Definition: `{"StartAt":"Done","States":{"Done":{"Type":"Succeed"}}}`,
		RoleArn:    "arn:aws:iam::000000000000:role/demo",
		Type:       "STANDARD",
		Status:     "ACTIVE",
	}
	execution := stepfunctions.Execution{
		ExecutionArn:    "arn:aws:states:us-east-1:000000000000:execution:demo-sm:exec-1",
		StateMachineArn: stateMachine.ARN,
		Name:            "exec-1",
		Input:           `{}`,
		Status:          "SUCCEEDED",
	}
	smRaw, _ := json.Marshal(stateMachine)
	if err := store.Set(ctx, "stepfunctions", "us-east-1/sm:demo-sm", string(smRaw)); err != nil {
		t.Fatalf("seed state machine: %v", err)
	}
	execRaw, _ := json.Marshal(execution)
	if err := store.Set(ctx, "stepfunctions", "us-east-1/exec:arn:aws:states:us-east-1:000000000000:execution:demo-sm:exec-1", string(execRaw)); err != nil {
		t.Fatalf("seed execution: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveSMCollection := false
	haveExecutionCollection := false
	haveStateMachine := false
	haveExecution := false
	for _, r := range resources {
		if r["uri"] == "oc://stepfunctions/state-machines" {
			haveSMCollection = true
		}
		if r["uri"] == "oc://stepfunctions/executions" {
			haveExecutionCollection = true
		}
		if r["uri"] == "oc://stepfunctions/state-machines/us-east-1/demo-sm" {
			haveStateMachine = true
		}
		if r["uri"] == "oc://stepfunctions/executions/us-east-1/arn:aws:states:us-east-1:000000000000:execution:demo-sm:exec-1" {
			haveExecution = true
		}
	}
	if !haveSMCollection || !haveExecutionCollection || !haveStateMachine || !haveExecution {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://stepfunctions/state-machines")
	if err != nil {
		t.Fatalf("ReadResource(state machine collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "demo-sm") {
		t.Fatalf("state machine collection payload missing state machine: %q", text)
	}

	executionContents, err := provider.ReadResource(ctx, "oc://stepfunctions/executions")
	if err != nil {
		t.Fatalf("ReadResource(execution collection) error = %v", err)
	}
	execText, _ := executionContents[0]["text"].(string)
	if !strings.Contains(execText, "exec-1") {
		t.Fatalf("execution collection payload missing execution: %q", execText)
	}
}

func TestRuntimeProvider_StepFunctionsMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
	}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{
		"name":       "unit-sm",
		"definition": `{"StartAt":"Done","States":{"Done":{"Type":"Succeed"}}}`,
		"role_arn":   "arn:aws:iam::000000000000:role/demo",
	})
	created, err := provider.toolStepFunctionsCreateStateMachine(ctx, createParams)
	if err != nil {
		t.Fatalf("toolStepFunctionsCreateStateMachine() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdSM := createdMap["state_machine"].(stepfunctions.StateMachine)
	if createdSM.ARN == "" || createdSM.Name != "unit-sm" {
		t.Fatalf("unexpected state machine after create: %#v", createdSM)
	}

	startParams, _ := json.Marshal(map[string]any{"name": "unit-sm", "input": `{\"ok\":true}`, "execution_name": "exec-1"})
	started, err := provider.toolStepFunctionsStartExecution(ctx, startParams)
	if err != nil {
		t.Fatalf("toolStepFunctionsStartExecution() error = %v", err)
	}
	startedMap := started.(map[string]any)
	if startedMap["started"] != true {
		t.Fatalf("unexpected start result: %#v", startedMap)
	}
	execution := startedMap["execution"].(stepfunctions.Execution)
	if execution.ExecutionArn == "" || execution.Name != "exec-1" {
		t.Fatalf("unexpected execution after start: %#v", execution)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-sm"})
	deleted, err := provider.toolStepFunctionsDeleteStateMachine(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolStepFunctionsDeleteStateMachine() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "stepfunctions", "us-east-1/sm:unit-sm"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected state machine to be deleted")
	}

	if _, found, err := store.Get(ctx, "stepfunctions", "us-east-1/exec:arn:aws:states:us-east-1:000000000000:execution:unit-sm:exec-1"); err != nil {
		t.Fatalf("execution store.Get() error = %v", err)
	} else if !found {
		t.Fatal("expected execution to remain stored")
	}
}

func TestRuntimeProvider_ListAndReadSSMResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	parameter := ssm.ParameterRecord{
		Name:        "/demo/param",
		Description: "demo parameter",
		Tags:        map[string]string{"env": "test"},
		Versions: []ssm.ParameterVersion{{
			Value: "value-1",
			Type:  "String",
		}},
	}
	raw, _ := json.Marshal(parameter)
	if err := store.Set(ctx, "ssm", "us-east-1/param:/demo/param", string(raw)); err != nil {
		t.Fatalf("seed parameter: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveParameter := false
	for _, r := range resources {
		if r["uri"] == "oc://ssm/parameters" {
			haveCollection = true
		}
		if r["uri"] == "oc://ssm/parameters/us-east-1/%2Fdemo%2Fparam" {
			haveParameter = true
		}
	}
	if !haveCollection || !haveParameter {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://ssm/parameters")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "/demo/param") {
		t.Fatalf("collection payload missing parameter: %q", text)
	}

	parameterContents, err := provider.ReadResource(ctx, "oc://ssm/parameters/us-east-1/%2Fdemo%2Fparam")
	if err != nil {
		t.Fatalf("ReadResource(parameter) error = %v", err)
	}
	parameterText, _ := parameterContents[0]["text"].(string)
	if !strings.Contains(parameterText, "demo parameter") {
		t.Fatalf("parameter payload missing description: %q", parameterText)
	}
}

func TestRuntimeProvider_RuntimeInventory_ListsEnabledServicesAndResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region: "us-east-1",
	}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "s3:buckets", "demo-bucket", map[string]any{"name": "demo-bucket", "region": "us-east-1"})
	seedRuntimeJSON(t, ctx, store, "iam:roles", "demo", iam.Role{RoleName: "demo", RoleId: "AROA1234567890DEMO", Arn: "arn:aws:iam::000000000000:role/demo", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})

	out, err := provider.toolRuntimeInventory(ctx, json.RawMessage(`{"limit_per_service":50}`))
	if err != nil {
		t.Fatalf("toolRuntimeInventory() error = %v", err)
	}
	body := out.(map[string]any)

	// Every service is always running, so the inventory covers all of them;
	// the per-service entries below are what distinguishes seeded from empty.
	enabled := body["enabled_services"].([]string)
	if len(enabled) != len(config.AllServices()) {
		t.Fatalf("enabled_services = %d entries, want all %d: %#v", len(enabled), len(config.AllServices()), enabled)
	}
	for _, want := range []string{"iam", "s3"} {
		if !slices.Contains(enabled, want) {
			t.Fatalf("enabled_services missing %q: %#v", want, enabled)
		}
	}

	entries := body["services"].([]map[string]any)
	byService := map[string]map[string]any{}
	for _, entry := range entries {
		byService[entry["service"].(string)] = entry
	}
	assertNoStateKeyFallback(t, entries, map[string]struct{}{"s3": {}, "iam": {}})

	s3Entry, ok := byService["s3"]
	if !ok {
		t.Fatalf("missing s3 entry: %#v", entries)
	}
	s3Resources := s3Entry["resources"].([]map[string]any)
	haveS3Bucket := false
	for _, res := range s3Resources {
		uri, _ := res["uri"].(string)
		if strings.HasPrefix(uri, "oc://s3/buckets/demo-bucket") {
			if got, _ := res["description"].(string); got != "S3 bucket demo-bucket" {
				t.Fatalf("typed s3 resource description = %q, want %q", got, "S3 bucket demo-bucket")
			}
			if got, _ := res["mimeType"].(string); got != "application/json" {
				t.Fatalf("typed s3 resource mimeType = %q, want %q", got, "application/json")
			}
			haveS3Bucket = true
			break
		}
	}
	if !haveS3Bucket {
		t.Fatalf("expected typed s3 bucket resource, got %#v", s3Resources)
	}

	iamEntry, ok := byService["iam"]
	if !ok {
		t.Fatalf("missing iam entry: %#v", entries)
	}
	iamResources := iamEntry["resources"].([]map[string]any)
	haveIAMRole := false
	for _, res := range iamResources {
		if res["source"] == "typed" && res["uri"] == "oc://iam/roles/demo" {
			haveIAMRole = true
			break
		}
	}
	if !haveIAMRole {
		t.Fatalf("expected typed iam role resource, got %#v", iamResources)
	}
}

func TestRuntimeProvider_RuntimeInventory_ServiceFilter(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "iam:roles", "only", iam.Role{RoleName: "only", RoleId: "AROA1234567890ONLY", Arn: "arn:aws:iam::000000000000:role/only", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})

	out, err := provider.toolRuntimeInventory(ctx, json.RawMessage(`{"service":"iam"}`))
	if err != nil {
		t.Fatalf("toolRuntimeInventory(service=iam) error = %v", err)
	}
	body := out.(map[string]any)
	enabled := body["enabled_services"].([]string)
	if len(enabled) != 1 || enabled[0] != "iam" {
		t.Fatalf("unexpected enabled_services: %#v", enabled)
	}
	entries := body["services"].([]map[string]any)
	if len(entries) != 1 || entries[0]["service"] != "iam" {
		t.Fatalf("unexpected services payload: %#v", entries)
	}
}

func TestRuntimeProvider_RuntimeInventoryResources_ListAndRead(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "s3:buckets", "demo-bucket", map[string]any{"name": "demo-bucket", "region": "us-east-1"})
	seedRuntimeJSON(t, ctx, store, "iam:roles", "demo", iam.Role{RoleName: "demo", RoleId: "AROA1234567890DEMO", Arn: "arn:aws:iam::000000000000:role/demo", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveEnabled := false
	haveIAMInventory := false
	haveS3Inventory := false
	for _, r := range resources {
		if r["uri"] == "oc://runtime/enabled-services" {
			haveEnabled = true
		}
		if r["uri"] == "oc://runtime/services/iam/resources" {
			haveIAMInventory = true
		}
		if r["uri"] == "oc://runtime/services/s3/resources" {
			haveS3Inventory = true
		}
	}
	if !haveEnabled || !haveIAMInventory || !haveS3Inventory {
		t.Fatalf("missing runtime inventory resources: %#v", resources)
	}

	enabledContents, err := provider.ReadResource(ctx, "oc://runtime/enabled-services")
	if err != nil {
		t.Fatalf("ReadResource(enabled-services) error = %v", err)
	}
	enabledText, _ := enabledContents[0]["text"].(string)
	if !strings.Contains(enabledText, "iam") || !strings.Contains(enabledText, "s3") {
		t.Fatalf("enabled-services payload missing expected services: %q", enabledText)
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://runtime/enabled-services", contains: []string{"iam", "s3"}},
		{uri: "oc://runtime/services/iam/resources", contains: []string{"oc://iam/roles/demo"}},
	})
}

func TestRuntimeProvider_ListAndReadIAMResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "iam:users", "alice", iam.User{UserName: "alice", UserId: "AIDA1234567890ALICE", Arn: "arn:aws:iam::000000000000:user/alice", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})
	seedRuntimeJSON(t, ctx, store, "iam:roles", "demo-role", iam.Role{RoleName: "demo-role", RoleId: "AROA1234567890ROLE", Arn: "arn:aws:iam::000000000000:role/demo-role", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})
	seedRuntimeJSON(t, ctx, store, "iam:policies", "arn:aws:iam::000000000000:policy/demo-policy", iam.Policy{PolicyName: "demo-policy", PolicyId: "ANPA1234567890POLICY", Arn: "arn:aws:iam::000000000000:policy/demo-policy", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})
	seedRuntimeJSON(t, ctx, store, "iam:groups", "demo-group", iam.Group{GroupName: "demo-group", GroupId: "AGPA1234567890GROUP", Arn: "arn:aws:iam::000000000000:group/demo-group", Path: "/", CreateDate: "2026-04-21T00:00:00Z", Members: []string{"alice"}})
	seedRuntimeJSON(t, ctx, store, "iam:profiles", "demo-profile", iam.InstanceProfile{InstanceProfileName: "demo-profile", InstanceProfileId: "AIPA1234567890PROF", Arn: "arn:aws:iam::000000000000:instance-profile/demo-profile", Path: "/", CreateDate: "2026-04-21T00:00:00Z", Roles: []string{"demo-role"}})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	want := map[string]bool{
		"oc://iam/users":           false,
		"oc://iam/users/alice":     false,
		"oc://iam/roles":           false,
		"oc://iam/roles/demo-role": false,
		"oc://iam/policies":        false,
		"oc://iam/policies/arn:aws:iam::000000000000:policy%2Fdemo-policy": false,
		"oc://iam/groups":                         false,
		"oc://iam/groups/demo-group":              false,
		"oc://iam/instance-profiles":              false,
		"oc://iam/instance-profiles/demo-profile": false,
	}
	for _, resource := range resources {
		if uri, _ := resource["uri"].(string); uri != "" {
			if _, ok := want[uri]; ok {
				want[uri] = true
			}
		}
	}
	for uri, found := range want {
		if !found {
			t.Fatalf("missing iam resource %q in %#v", uri, resources)
		}
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://iam/users", contains: []string{"alice"}},
		{uri: "oc://iam/users/alice", contains: []string{"alice"}},
		{uri: "oc://iam/roles", contains: []string{"demo-role"}},
		{uri: "oc://iam/roles/demo-role", contains: []string{"demo-role"}},
		{uri: "oc://iam/policies", contains: []string{"demo-policy"}},
		{uri: "oc://iam/policies/arn:aws:iam::000000000000:policy%2Fdemo-policy", contains: []string{"demo-policy"}},
		{uri: "oc://iam/groups", contains: []string{"demo-group"}},
		{uri: "oc://iam/groups/demo-group", contains: []string{"demo-group"}},
		{uri: "oc://iam/instance-profiles", contains: []string{"demo-profile"}},
		{uri: "oc://iam/instance-profiles/demo-profile", contains: []string{"demo-profile"}},
	})
}

func TestRuntimeProvider_ListAndReadACMResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	certARN := "arn:aws:acm:us-east-1:000000000000:certificate/demo-cert"
	seedRuntimeJSON(t, ctx, store, "acm:certs", certARN, acm.Certificate{
		CertificateArn:          certARN,
		DomainName:              "example.com",
		SubjectAlternativeNames: []string{"www.example.com"},
		Status:                  "ISSUED",
		Type:                    "AMAZON_ISSUED",
		CreatedAt:               1710000000,
		IssuedAt:                1710000001,
	})
	seedRuntimeJSON(t, ctx, store, "acm:tags", certARN, []acm.Tag{{Key: "env", Value: "test"}})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(resources) == 0 {
		t.Fatalf("expected non-empty resources, got %#v", resources)
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://acm/certificates", contains: []string{"example.com"}},
		{uri: "oc://acm/certificates/arn:aws:acm:us-east-1:000000000000:certificate%2Fdemo-cert", contains: []string{"example.com", "env", "test"}},
	})
}

func TestRuntimeProvider_IAMMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	provider := NewRuntimeProvider(cfg, store)

	createUserParams, _ := json.Marshal(map[string]any{"name": "unit-user", "tags": map[string]string{"team": "platform"}})
	createdUser, err := provider.toolIAMCreateUser(ctx, createUserParams)
	if err != nil {
		t.Fatalf("toolIAMCreateUser() error = %v", err)
	}
	createdUserMap := createdUser.(map[string]any)
	if createdUserMap["created"] != true {
		t.Fatalf("unexpected create user result: %#v", createdUserMap)
	}
	createdUserObj := createdUserMap["user"].(iam.User)
	if createdUserObj.UserName != "unit-user" || createdUserObj.Tags["team"] != "platform" {
		t.Fatalf("unexpected created user: %#v", createdUserObj)
	}

	createRoleParams, _ := json.Marshal(map[string]any{"name": "unit-role", "assume_role_policy_document": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`})
	createdRole, err := provider.toolIAMCreateRole(ctx, createRoleParams)
	if err != nil {
		t.Fatalf("toolIAMCreateRole() error = %v", err)
	}
	createdRoleMap := createdRole.(map[string]any)
	if createdRoleMap["created"] != true {
		t.Fatalf("unexpected create role result: %#v", createdRoleMap)
	}
	createdRoleObj := createdRoleMap["role"].(iam.Role)
	if createdRoleObj.RoleName != "unit-role" {
		t.Fatalf("unexpected created role: %#v", createdRoleObj)
	}

	createPolicyParams, _ := json.Marshal(map[string]any{"name": "unit-policy", "document": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`})
	createdPolicy, err := provider.toolIAMCreatePolicy(ctx, createPolicyParams)
	if err != nil {
		t.Fatalf("toolIAMCreatePolicy() error = %v", err)
	}
	createdPolicyMap := createdPolicy.(map[string]any)
	if createdPolicyMap["created"] != true {
		t.Fatalf("unexpected create policy result: %#v", createdPolicyMap)
	}
	createdPolicyObj := createdPolicyMap["policy"].(iam.Policy)
	if createdPolicyObj.PolicyName != "unit-policy" {
		t.Fatalf("unexpected created policy: %#v", createdPolicyObj)
	}

	createGroupParams, _ := json.Marshal(map[string]any{"name": "unit-group"})
	createdGroup, err := provider.toolIAMCreateGroup(ctx, createGroupParams)
	if err != nil {
		t.Fatalf("toolIAMCreateGroup() error = %v", err)
	}
	createdGroupMap := createdGroup.(map[string]any)
	if createdGroupMap["created"] != true {
		t.Fatalf("unexpected create group result: %#v", createdGroupMap)
	}
	createdGroupObj := createdGroupMap["group"].(iam.Group)
	if createdGroupObj.GroupName != "unit-group" {
		t.Fatalf("unexpected created group: %#v", createdGroupObj)
	}

	createProfileParams, _ := json.Marshal(map[string]any{"name": "unit-profile", "roles": []string{"unit-role"}})
	createdProfile, err := provider.toolIAMCreateInstanceProfile(ctx, createProfileParams)
	if err != nil {
		t.Fatalf("toolIAMCreateInstanceProfile() error = %v", err)
	}
	createdProfileMap := createdProfile.(map[string]any)
	if createdProfileMap["created"] != true {
		t.Fatalf("unexpected create instance profile result: %#v", createdProfileMap)
	}
	createdProfileObj := createdProfileMap["instance_profile"].(iam.InstanceProfile)
	if createdProfileObj.InstanceProfileName != "unit-profile" || len(createdProfileObj.Roles) != 1 || createdProfileObj.Roles[0] != "unit-role" {
		t.Fatalf("unexpected created instance profile: %#v", createdProfileObj)
	}

	deleteUserParams, _ := json.Marshal(map[string]any{"name": "unit-user"})
	deletedUser, err := provider.toolIAMDeleteUser(ctx, deleteUserParams)
	if err != nil {
		t.Fatalf("toolIAMDeleteUser() error = %v", err)
	}
	deletedUserMap := deletedUser.(map[string]any)
	if deletedUserMap["deleted"] != true {
		t.Fatalf("unexpected delete user result: %#v", deletedUserMap)
	}

	deleteRoleParams, _ := json.Marshal(map[string]any{"name": "unit-role"})
	deletedRole, err := provider.toolIAMDeleteRole(ctx, deleteRoleParams)
	if err != nil {
		t.Fatalf("toolIAMDeleteRole() error = %v", err)
	}
	deletedRoleMap := deletedRole.(map[string]any)
	if deletedRoleMap["deleted"] != true {
		t.Fatalf("unexpected delete role result: %#v", deletedRoleMap)
	}

	deletePolicyParams, _ := json.Marshal(map[string]any{"arn": createdPolicyObj.Arn})
	deletedPolicy, err := provider.toolIAMDeletePolicy(ctx, deletePolicyParams)
	if err != nil {
		t.Fatalf("toolIAMDeletePolicy() error = %v", err)
	}
	deletedPolicyMap := deletedPolicy.(map[string]any)
	if deletedPolicyMap["deleted"] != true {
		t.Fatalf("unexpected delete policy result: %#v", deletedPolicyMap)
	}

	deleteGroupParams, _ := json.Marshal(map[string]any{"name": "unit-group"})
	deletedGroup, err := provider.toolIAMDeleteGroup(ctx, deleteGroupParams)
	if err != nil {
		t.Fatalf("toolIAMDeleteGroup() error = %v", err)
	}
	deletedGroupMap := deletedGroup.(map[string]any)
	if deletedGroupMap["deleted"] != true {
		t.Fatalf("unexpected delete group result: %#v", deletedGroupMap)
	}

	deleteProfileParams, _ := json.Marshal(map[string]any{"name": "unit-profile"})
	deletedProfile, err := provider.toolIAMDeleteInstanceProfile(ctx, deleteProfileParams)
	if err != nil {
		t.Fatalf("toolIAMDeleteInstanceProfile() error = %v", err)
	}
	deletedProfileMap := deletedProfile.(map[string]any)
	if deletedProfileMap["deleted"] != true {
		t.Fatalf("unexpected delete instance profile result: %#v", deletedProfileMap)
	}

	if _, found, err := store.Get(ctx, "iam:users", "unit-user"); err != nil {
		t.Fatalf("store.Get(user) error = %v", err)
	} else if found {
		t.Fatal("expected user to be deleted")
	}
	if _, found, err := store.Get(ctx, "iam:roles", "unit-role"); err != nil {
		t.Fatalf("store.Get(role) error = %v", err)
	} else if found {
		t.Fatal("expected role to be deleted")
	}
	if _, found, err := store.Get(ctx, "iam:policies", createdPolicyObj.Arn); err != nil {
		t.Fatalf("store.Get(policy) error = %v", err)
	} else if found {
		t.Fatal("expected policy to be deleted")
	}
	if _, found, err := store.Get(ctx, "iam:groups", "unit-group"); err != nil {
		t.Fatalf("store.Get(group) error = %v", err)
	} else if found {
		t.Fatal("expected group to be deleted")
	}
	if _, found, err := store.Get(ctx, "iam:profiles", "unit-profile"); err != nil {
		t.Fatalf("store.Get(instance profile) error = %v", err)
	} else if found {
		t.Fatal("expected instance profile to be deleted")
	}
}
func TestRuntimeProvider_SSMMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	putParams, _ := json.Marshal(map[string]any{
		"name":        "/unit/param",
		"value":       "value-1",
		"description": "unit parameter",
		"tags":        map[string]string{"team": "platform"},
	})
	updated, err := provider.toolSSMPutParameter(ctx, putParams)
	if err != nil {
		t.Fatalf("toolSSMPutParameter() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	if updatedMap["updated"] != true {
		t.Fatalf("unexpected put result: %#v", updatedMap)
	}
	parameter := updatedMap["parameter"].(*ssm.ParameterRecord)
	if parameter.Version() != 1 || parameter.Tags["team"] != "platform" {
		t.Fatalf("unexpected parameter after put: %#v", parameter)
	}

	overwriteParams, _ := json.Marshal(map[string]any{
		"name":      "/unit/param",
		"value":     "value-2",
		"overwrite": true,
		"type":      "SecureString",
	})
	overwritten, err := provider.toolSSMPutParameter(ctx, overwriteParams)
	if err != nil {
		t.Fatalf("toolSSMPutParameter(overwrite) error = %v", err)
	}
	overwrittenMap := overwritten.(map[string]any)
	overwrittenParam := overwrittenMap["parameter"].(*ssm.ParameterRecord)
	if overwrittenParam.Version() != 2 || overwrittenParam.Latest().Type != "SecureString" {
		t.Fatalf("unexpected parameter after overwrite: %#v", overwrittenParam)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "/unit/param"})
	deleted, err := provider.toolSSMDeleteParameter(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolSSMDeleteParameter() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "ssm", "us-east-1/param:/unit/param"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected parameter to be deleted")
	}
}

func TestRuntimeProvider_ListAndReadSecretsManagerResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	secret := secretsmanager.Secret{
		ARN:              "arn:aws:secretsmanager:us-east-1:000000000000:secret:/demo/secret",
		Name:             "/demo/secret",
		Description:      "demo secret",
		CurrentVersionId: "version-1",
		CreatedDate:      1710000000,
		LastChangedDate:  1710000000,
		Versions: []secretsmanager.SecretVersion{{
			VersionId:    "version-1",
			SecretString: "value-1",
			Stages:       []string{"AWSCURRENT"},
			CreatedDate:  1710000000,
		}},
		Tags: []secretsmanager.Tag{{Key: "env", Value: "test"}},
	}
	raw, _ := json.Marshal(secret)
	if err := store.Set(ctx, "secretsmanager:secrets", "us-east-1//demo/secret", string(raw)); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveSecret := false
	for _, r := range resources {
		if r["uri"] == "oc://secretsmanager/secrets" {
			haveCollection = true
		}
		if r["uri"] == "oc://secretsmanager/secrets/us-east-1/%2Fdemo%2Fsecret" {
			haveSecret = true
		}
	}
	if !haveCollection || !haveSecret {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://secretsmanager/secrets")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "/demo/secret") {
		t.Fatalf("collection payload missing secret: %q", text)
	}

	secretContents, err := provider.ReadResource(ctx, "oc://secretsmanager/secrets/us-east-1/%2Fdemo%2Fsecret")
	if err != nil {
		t.Fatalf("ReadResource(secret) error = %v", err)
	}
	secretText, _ := secretContents[0]["text"].(string)
	if !strings.Contains(secretText, "demo secret") {
		t.Fatalf("secret payload missing description: %q", secretText)
	}
}

func TestRuntimeProvider_SecretsManagerMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{
		"name":          "unit-secret",
		"secret_string": "value-1",
		"description":   "unit secret",
		"tags":          map[string]string{"team": "platform"},
	})
	created, err := provider.toolSecretsManagerCreateSecret(ctx, createParams)
	if err != nil {
		t.Fatalf("toolSecretsManagerCreateSecret() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdSecret := createdMap["secret"].(secretsmanager.Secret)
	if createdSecret.CurrentVersionId == "" || len(createdSecret.Tags) != 1 || createdSecret.Tags[0].Key != "team" {
		t.Fatalf("unexpected secret after create: %#v", createdSecret)
	}

	putParams, _ := json.Marshal(map[string]any{"name": "unit-secret", "secret_string": "value-2"})
	updated, err := provider.toolSecretsManagerPutSecretValue(ctx, putParams)
	if err != nil {
		t.Fatalf("toolSecretsManagerPutSecretValue() error = %v", err)
	}
	updatedMap := updated.(map[string]any)
	if updatedMap["updated"] != true {
		t.Fatalf("unexpected put result: %#v", updatedMap)
	}
	updatedSecret := updatedMap["secret"].(secretsmanager.Secret)
	if updatedSecret.CurrentVersionId == createdSecret.CurrentVersionId {
		t.Fatalf("expected a new current version, got %#v", updatedSecret)
	}
	havePrevious := false
	haveCurrent := false
	for _, version := range updatedSecret.Versions {
		if version.VersionId == createdSecret.CurrentVersionId && len(version.Stages) == 1 && version.Stages[0] == "AWSPREVIOUS" {
			havePrevious = true
		}
		if version.VersionId == updatedSecret.CurrentVersionId && len(version.Stages) == 1 && version.Stages[0] == "AWSCURRENT" {
			haveCurrent = true
		}
	}
	if !havePrevious || !haveCurrent {
		t.Fatalf("unexpected version stage transitions: %#v", updatedSecret.Versions)
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-secret"})
	deleted, err := provider.toolSecretsManagerDeleteSecret(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolSecretsManagerDeleteSecret() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "secretsmanager:secrets", "us-east-1/unit-secret"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected secret to be deleted")
	}
}

func TestRuntimeProvider_ListAndReadKinesisResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	stream := kinesis.Stream{
		StreamName:           "demo-stream",
		StreamARN:            "arn:aws:kinesis:us-east-1:000000000000:stream/demo-stream",
		StreamStatus:         "ACTIVE",
		ShardCount:           1,
		Shards:               []kinesis.Shard{},
		RetentionPeriodHours: 24,
	}
	raw, _ := json.Marshal(stream)
	if err := store.Set(ctx, "kinesis:streams", "us-east-1/demo-stream", string(raw)); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	haveCollection := false
	haveStream := false
	for _, r := range resources {
		if r["uri"] == "oc://kinesis/streams" {
			haveCollection = true
		}
		if r["uri"] == "oc://kinesis/streams/us-east-1/demo-stream" {
			haveStream = true
		}
	}
	if !haveCollection || !haveStream {
		t.Fatalf("unexpected resources: %#v", resources)
	}

	contents, err := provider.ReadResource(ctx, "oc://kinesis/streams")
	if err != nil {
		t.Fatalf("ReadResource(collection) error = %v", err)
	}
	text, _ := contents[0]["text"].(string)
	if !strings.Contains(text, "demo-stream") {
		t.Fatalf("collection payload missing stream: %q", text)
	}

	streamContents, err := provider.ReadResource(ctx, "oc://kinesis/streams/us-east-1/demo-stream")
	if err != nil {
		t.Fatalf("ReadResource(stream) error = %v", err)
	}
	streamText, _ := streamContents[0]["text"].(string)
	if !strings.Contains(streamText, "demo-stream") {
		t.Fatalf("stream payload missing name: %q", streamText)
	}
}

func TestRuntimeProvider_KinesisMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	provider := NewRuntimeProvider(cfg, store)

	createParams, _ := json.Marshal(map[string]any{
		"name":        "unit-stream",
		"shard_count": 1,
	})
	created, err := provider.toolKinesisCreateStream(ctx, createParams)
	if err != nil {
		t.Fatalf("toolKinesisCreateStream() error = %v", err)
	}
	createdMap := created.(map[string]any)
	if createdMap["created"] != true {
		t.Fatalf("unexpected create result: %#v", createdMap)
	}
	createdStream := createdMap["stream"].(kinesis.Stream)
	if createdStream.StreamName != "unit-stream" || createdStream.ShardCount != 1 {
		t.Fatalf("unexpected stream after create: %#v", createdStream)
	}

	putParams, _ := json.Marshal(map[string]any{
		"stream_name":   "unit-stream",
		"partition_key": "pk1",
		"data":          "hello world",
	})
	putResult, err := provider.toolKinesisPutRecord(ctx, putParams)
	if err != nil {
		t.Fatalf("toolKinesisPutRecord() error = %v", err)
	}
	putMap := putResult.(map[string]any)
	if putMap["stored"] != true || putMap["stream_name"] != "unit-stream" {
		t.Fatalf("unexpected put result: %#v", putMap)
	}
	if putMap["sequence_number"] == "" {
		t.Fatal("expected a sequence number in put result")
	}

	deleteParams, _ := json.Marshal(map[string]any{"name": "unit-stream"})
	deleted, err := provider.toolKinesisDeleteStream(ctx, deleteParams)
	if err != nil {
		t.Fatalf("toolKinesisDeleteStream() error = %v", err)
	}
	deletedMap := deleted.(map[string]any)
	if deletedMap["deleted"] != true {
		t.Fatalf("unexpected delete result: %#v", deletedMap)
	}

	if _, found, err := store.Get(ctx, "kinesis:streams", "us-east-1/unit-stream"); err != nil {
		t.Fatalf("store.Get() error = %v", err)
	} else if found {
		t.Fatal("expected stream to be deleted")
	}
}

func TestRuntimeProvider_ACMMutationTools(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region:    "us-east-1",
		AccountID: "000000000000",
	}
	provider := NewRuntimeProvider(cfg, store)
	provider.SetRouter(newACMTestRouter(t, cfg, store, clock.NewMock()))

	// request certificate
	reqResult, err := provider.toolACMRequestCertificate(ctx, json.RawMessage(`{
		"domain_name": "example.com",
		"subject_alternative_names": ["www.example.com"],
		"tags": {"env": "test"}
	}`))
	if err != nil {
		t.Fatalf("toolACMRequestCertificate: %v", err)
	}
	reqMap, ok := reqResult.(map[string]any)
	if !ok {
		t.Fatalf("expected map result from request, got %T", reqResult)
	}
	cert, ok := reqMap["certificate"].(acm.Certificate)
	if !ok {
		t.Fatalf("expected acm.Certificate in result")
	}
	if cert.DomainName != "example.com" {
		t.Errorf("DomainName: got %q, want %q", cert.DomainName, "example.com")
	}
	if cert.Status != "ISSUED" {
		t.Errorf("Status: got %q, want %q", cert.Status, "ISSUED")
	}
	if cert.CertificateArn == "" {
		t.Error("CertificateArn must not be empty")
	}
	// Dispatched through the router into ACM's own RequestCertificate, so the
	// certificate carries the DomainValidationOptions the wire API builds —
	// the exact gap the store-writing version of this tool had (#2030).
	if len(cert.DomainValidationOptions) != 2 {
		t.Fatalf("DomainValidationOptions: got %d entries, want 2 (domain + SAN): %#v",
			len(cert.DomainValidationOptions), cert.DomainValidationOptions)
	}
	arn := cert.CertificateArn

	// cert in store
	if _, found, err := store.Get(ctx, "acm:certs", arn); err != nil || !found {
		t.Errorf("cert not found in store after request: err=%v found=%v", err, found)
	}

	// tags in store
	tagsRaw, found, err := store.Get(ctx, "acm:tags", arn)
	if err != nil || !found {
		t.Errorf("tags not found in store after request: err=%v found=%v", err, found)
	} else {
		var storedTags []acm.Tag
		if err := json.Unmarshal([]byte(tagsRaw), &storedTags); err != nil {
			t.Fatalf("unmarshal stored tags: %v", err)
		}
		if len(storedTags) != 1 || storedTags[0].Key != "env" || storedTags[0].Value != "test" {
			t.Errorf("unexpected stored tags: %+v", storedTags)
		}
	}

	// add tags
	addResult, err := provider.toolACMAddTagsToCertificate(ctx, json.RawMessage(`{"arn":"`+arn+`","tags":{"team":"platform","env":"prod"}}`))
	if err != nil {
		t.Fatalf("toolACMAddTagsToCertificate: %v", err)
	}
	addMap := addResult.(map[string]any)
	mergedTags := addMap["tags"].([]acm.Tag)
	tagByKey := make(map[string]string)
	for _, tag := range mergedTags {
		tagByKey[tag.Key] = tag.Value
	}
	if tagByKey["env"] != "prod" {
		t.Errorf("expected env=prod after overwrite, got %q", tagByKey["env"])
	}
	if tagByKey["team"] != "platform" {
		t.Errorf("expected team=platform after add, got %q", tagByKey["team"])
	}

	// remove tags
	rmResult, err := provider.toolACMRemoveTagsFromCertificate(ctx, json.RawMessage(`{"arn":"`+arn+`","tag_keys":["team"]}`))
	if err != nil {
		t.Fatalf("toolACMRemoveTagsFromCertificate: %v", err)
	}
	rmMap := rmResult.(map[string]any)
	remainingTags := rmMap["tags"].([]acm.Tag)
	if len(remainingTags) != 1 || remainingTags[0].Key != "env" {
		t.Errorf("expected only env tag after remove, got %+v", remainingTags)
	}

	// delete certificate
	delResult, err := provider.toolACMDeleteCertificate(ctx, json.RawMessage(`{"arn":"`+arn+`"}`))
	if err != nil {
		t.Fatalf("toolACMDeleteCertificate: %v", err)
	}
	delMap := delResult.(map[string]any)
	if delMap["deleted"] != true {
		t.Error("expected deleted=true")
	}

	// cert gone from store
	if _, found, err := store.Get(ctx, "acm:certs", arn); err != nil || found {
		t.Errorf("cert should be gone after delete: err=%v found=%v", err, found)
	}

	// double-delete returns error
	if _, err := provider.toolACMDeleteCertificate(ctx, json.RawMessage(`{"arn":"`+arn+`"}`)); err == nil {
		t.Error("expected error on double-delete")
	}
}

// TestRuntimeProvider_ACMRequestCertificateRejectsMalformedDomain pins that
// the MCP tool now runs through ACM's own RequestCertificate validation
// rather than accepting anything as a domain name — the bypass #2030
// reported. A malformed domain must leave no certificate behind, the same
// no-partial-effect guarantee RequestCertificate itself gives (see
// requestCertificateTyped's validation-before-creation ordering).
func TestRuntimeProvider_ACMRequestCertificateRejectsMalformedDomain(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	provider := NewRuntimeProvider(cfg, store)
	provider.SetRouter(newACMTestRouter(t, cfg, store, clock.NewMock()))

	_, err := provider.toolACMRequestCertificate(ctx, json.RawMessage(`{"domain_name":"not a domain!"}`))
	if err == nil {
		t.Fatal("expected an error for a malformed domain name, got nil")
	}
	if !strings.Contains(err.Error(), "ValidationException") {
		t.Fatalf("error = %q, want it to name ValidationException", err.Error())
	}

	kvs, err := store.Scan(ctx, "acm:certs", "")
	if err != nil {
		t.Fatalf("scan acm:certs: %v", err)
	}
	if len(kvs) != 0 {
		t.Fatalf("expected no certificate stored after a rejected request, got %d", len(kvs))
	}
}

// TestRuntimeProvider_ACMRequestCertificateRequiresRouter covers the failure
// mode of a RuntimeProvider that never had SetRouter called on it — the
// ordinary case for most unit tests in this file, and a real possibility
// during startup before internal/router.New finishes wiring routes.
func TestRuntimeProvider_ACMRequestCertificateRequiresRouter(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	provider := NewRuntimeProvider(&config.Config{Region: "us-east-1"}, store)

	if _, err := provider.toolACMRequestCertificate(ctx, json.RawMessage(`{"domain_name":"example.com"}`)); err == nil {
		t.Fatal("expected an error when no router has been set")
	}
}

func TestRuntimeProvider_AllListedOCResourcesResolve(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{
		Region: "us-east-1",
	}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "s3:buckets", "demo-bucket", map[string]any{"name": "demo-bucket", "region": "us-east-1"})
	seedRuntimeJSON(t, ctx, store, "sqs:queues", "us-east-1/demo-queue", map[string]any{"name": "demo-queue", "url": "http://localhost:4566/000000000000/demo-queue"})
	seedRuntimeJSON(t, ctx, store, "dynamodb:tables", "us-east-1/demo-table", dynamodb.Table{TableName: "demo-table", TableStatus: "ACTIVE"})
	seedRuntimeJSON(t, ctx, store, "sns:topics", "us-east-1/demo-topic", sns.Topic{Name: "demo-topic", ARN: "arn:aws:sns:us-east-1:000000000000:demo-topic"})
	seedRuntimeJSON(t, ctx, store, "kinesis:streams", "us-east-1/demo-stream", kinesis.Stream{StreamName: "demo-stream", StreamARN: "arn:aws:kinesis:us-east-1:000000000000:stream/demo-stream", StreamStatus: "ACTIVE"})
	seedRuntimeJSON(t, ctx, store, "kms", "us-east-1/key:abcd-1234", kms.Key{KeyID: "abcd-1234", ARN: "arn:aws:kms:us-east-1:000000000000:key/abcd-1234", Description: "demo key", KeyState: "Enabled", Enabled: true})
	seedRuntimeJSON(t, ctx, store, "stepfunctions", "us-east-1/sm:demo-sm", stepfunctions.StateMachine{Name: "demo-sm", ARN: "arn:aws:states:us-east-1:000000000000:stateMachine:demo-sm", Status: "ACTIVE"})
	seedRuntimeJSON(t, ctx, store, "stepfunctions", "us-east-1/exec:arn:aws:states:us-east-1:000000000000:execution:demo-sm:exec-1", stepfunctions.Execution{ExecutionArn: "arn:aws:states:us-east-1:000000000000:execution:demo-sm:exec-1", Name: "exec-1", StateMachineArn: "arn:aws:states:us-east-1:000000000000:stateMachine:demo-sm", Status: "SUCCEEDED"})
	seedRuntimeJSON(t, ctx, store, "ssm", "us-east-1/param:/demo/param", ssm.ParameterRecord{Name: "/demo/param", Versions: []ssm.ParameterVersion{{Value: "value-1", Type: "String"}}})
	seedRuntimeJSON(t, ctx, store, "secretsmanager:secrets", "us-east-1//demo/secret", secretsmanager.Secret{Name: "/demo/secret", ARN: "arn:aws:secretsmanager:us-east-1:000000000000:secret:/demo/secret", CurrentVersionId: "v1", Versions: []secretsmanager.SecretVersion{{VersionId: "v1", Stages: []string{"AWSCURRENT"}}}})
	seedRuntimeJSON(t, ctx, store, "iam:roles", "demo", iam.Role{RoleName: "demo", RoleId: "AROA1234567890DEMO", Arn: "arn:aws:iam::000000000000:role/demo", Path: "/", CreateDate: "2026-04-21T00:00:00Z"})
	seedRuntimeJSON(t, ctx, store, "acm:certs", "arn:aws:acm:us-east-1:000000000000:certificate/demo-cert", acm.Certificate{CertificateArn: "arn:aws:acm:us-east-1:000000000000:certificate/demo-cert", DomainName: "example.com", Status: "ISSUED", Type: "AMAZON_ISSUED", CreatedAt: 1710000000})

	assertAllListedOCResourcesResolve(t, ctx, provider)
}

func TestRuntimeProvider_ListAndReadLambdaResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "lambda:functions", "us-east-1/my-func", lambda.Function{
		Name:    "my-func",
		ARN:     "arn:aws:lambda:us-east-1:000000000000:function:my-func",
		Runtime: "python3.11",
		Handler: "index.handler",
		Role:    "arn:aws:iam::000000000000:role/my-role",
		State:   "Active",
	})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	want := map[string]bool{
		"oc://lambda/functions":                   false,
		"oc://lambda/functions/us-east-1/my-func": false,
	}
	for _, resource := range resources {
		if uri, _ := resource["uri"].(string); uri != "" {
			if _, ok := want[uri]; ok {
				want[uri] = true
			}
		}
	}
	for uri, found := range want {
		if !found {
			t.Fatalf("missing lambda resource %q in ListResources", uri)
		}
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://lambda/functions", contains: []string{"my-func"}},
		{uri: "oc://lambda/functions/us-east-1/my-func", contains: []string{"my-func", "python3.11"}},
	})
}

func TestRuntimeProvider_ListAndReadECRResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "ecr:repositories", "us-east-1/my-repo", ecr.Repository{
		RepositoryArn:      "arn:aws:ecr:us-east-1:000000000000:repository/my-repo",
		RegistryId:         "000000000000",
		RepositoryName:     "my-repo",
		RepositoryUri:      "000000000000.dkr.ecr.us-east-1.amazonaws.com/my-repo",
		ImageTagMutability: "MUTABLE",
	})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	want := map[string]bool{
		"oc://ecr/repositories":                   false,
		"oc://ecr/repositories/us-east-1/my-repo": false,
	}
	for _, resource := range resources {
		if uri, _ := resource["uri"].(string); uri != "" {
			if _, ok := want[uri]; ok {
				want[uri] = true
			}
		}
	}
	for uri, found := range want {
		if !found {
			t.Fatalf("missing ecr resource %q in ListResources", uri)
		}
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://ecr/repositories", contains: []string{"my-repo"}},
		{uri: "oc://ecr/repositories/us-east-1/my-repo", contains: []string{"my-repo", "MUTABLE"}},
	})
}

func TestRuntimeProvider_ListAndReadECSResources(t *testing.T) {
	ctx := context.Background()
	store := state.NewMemoryStore()
	cfg := &config.Config{Region: "us-east-1"}
	provider := NewRuntimeProvider(cfg, store)

	seedRuntimeJSON(t, ctx, store, "ecs:clusters", "us-east-1/my-cluster", ecs.Cluster{
		ClusterName: "my-cluster",
		ClusterArn:  "arn:aws:ecs:us-east-1:000000000000:cluster/my-cluster",
		Status:      "ACTIVE",
	})
	seedRuntimeJSON(t, ctx, store, "ecs:task-definitions", "us-east-1/my-task:1", ecs.TaskDefinition{
		TaskDefinitionArn: "arn:aws:ecs:us-east-1:000000000000:task-definition/my-task:1",
		Family:            "my-task",
		Revision:          1,
		Status:            "ACTIVE",
	})

	resources, err := provider.ListResources(ctx)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	want := map[string]bool{
		"oc://ecs/clusters":                             false,
		"oc://ecs/clusters/us-east-1/my-cluster":        false,
		"oc://ecs/task-definitions":                     false,
		"oc://ecs/task-definitions/us-east-1/my-task:1": false,
	}
	for _, resource := range resources {
		if uri, _ := resource["uri"].(string); uri != "" {
			if _, ok := want[uri]; ok {
				want[uri] = true
			}
		}
	}
	for uri, found := range want {
		if !found {
			t.Fatalf("missing ecs resource %q in ListResources", uri)
		}
	}

	assertRuntimeResourceReads(t, ctx, provider, []runtimeResourceExpectation{
		{uri: "oc://ecs/clusters", contains: []string{"my-cluster"}},
		{uri: "oc://ecs/clusters/us-east-1/my-cluster", contains: []string{"my-cluster", "ACTIVE"}},
		{uri: "oc://ecs/task-definitions", contains: []string{"my-task"}},
		{uri: "oc://ecs/task-definitions/us-east-1/my-task:1", contains: []string{"my-task", "ACTIVE"}},
	})
}

// TestRuntimeProvider_IAMCreateRoleTrustPolicy pins the two things the IAM
// create tools owe the policy grammar (#1850). The provider writes to the store
// rather than through the IAM handler, so nothing else stands between an MCP
// client and a document the IAM API itself would refuse with
// MalformedPolicyDocument.
func TestRuntimeProvider_IAMCreateRoleTrustPolicy(t *testing.T) {
	ctx := context.Background()
	provider := NewRuntimeProvider(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore())

	// An unsupplied trust policy defaults to one a role can actually be
	// assumed with: AWS's own CreateRole examples are a single sts:AssumeRole
	// statement, and an empty Statement list is not a document IAM accepts.
	created, err := provider.toolIAMCreateRole(ctx, mustJSON(t, map[string]any{"name": "defaulted-role"}))
	if err != nil {
		t.Fatalf("toolIAMCreateRole() error = %v", err)
	}
	role := created.(map[string]any)["role"].(iam.Role)
	if err := iampolicy.ValidateDocument(role.AssumeRolePolicyDocument); err != nil {
		t.Errorf("default AssumeRolePolicyDocument is one IAM would refuse: %v (%s)", err, role.AssumeRolePolicyDocument)
	}
	if !strings.Contains(role.AssumeRolePolicyDocument, "sts:AssumeRole") {
		t.Errorf("default AssumeRolePolicyDocument = %s, want an sts:AssumeRole statement", role.AssumeRolePolicyDocument)
	}

	// A supplied document is checked, not stored unread.
	for _, tc := range []struct {
		name     string
		document string
	}{
		{"not json", "{not json"},
		{"empty statement list", `{"Version":"2012-10-17","Statement":[]}`},
		{"lowercase effect", `{"Version":"2012-10-17","Statement":[{"Effect":"allow","Action":"sts:AssumeRole"}]}`},
	} {
		t.Run("create role rejects "+tc.name, func(t *testing.T) {
			_, err := provider.toolIAMCreateRole(ctx, mustJSON(t, map[string]any{
				"name": "role-" + strings.ReplaceAll(tc.name, " ", "-"), "assume_role_policy_document": tc.document,
			}))
			if err == nil {
				t.Fatalf("toolIAMCreateRole() accepted a malformed trust policy %q", tc.document)
			}
		})
		t.Run("create policy rejects "+tc.name, func(t *testing.T) {
			_, err := provider.toolIAMCreatePolicy(ctx, mustJSON(t, map[string]any{
				"name": "policy-" + strings.ReplaceAll(tc.name, " ", "-"), "document": tc.document,
			}))
			if err == nil {
				t.Fatalf("toolIAMCreatePolicy() accepted a malformed document %q", tc.document)
			}
		})
	}

	// A refused document leaves nothing behind.
	if _, found, err := provider.store.Get(ctx, iamRolesStoreNamespace, "role-not-json"); err != nil {
		t.Fatalf("read back refused role: %v", err)
	} else if found {
		t.Error("a refused CreateRole still wrote the role to the store")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}
