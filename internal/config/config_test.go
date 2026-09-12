package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
)

// TestLoad_defaults verifies that Load() returns sensible defaults when no
// environment variables are set.
//
// OVERCAST_STATE defaults to "auto" (config.StateSourceAuto), which resolves
// to a concrete backend from evidence of persistence intent (see
// resolveAutoState in state_auto.go; exhaustively covered by
// TestResolveAutoState below). To keep *this* test deterministic regardless
// of what happens to exist on the machine running it, OVERCAST_DATA_DIR is
// pinned to a fresh, empty t.TempDir() and marked OVERCAST_DATA_DIR_SOURCE=
// image — simulating the Docker image's own baked-in default data dir, i.e.
// a genuinely fresh, volume-less, unconfigured run — so none of the three
// auto signals fire and the resolved default is deterministically memory.
func TestLoad_defaults(t *testing.T) {
	// Given: no environment variables set, data dir pinned to a fresh temp
	// dir marked as the image's own default (not user-configured).
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

	// When: we load config
	cfg, err := config.Load()

	// Then: defaults are applied correctly
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host: expected 0.0.0.0, got %q", cfg.Host)
	}
	if cfg.Port != 4566 {
		t.Errorf("Port: expected 4566, got %d", cfg.Port)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("Region: expected us-east-1, got %q", cfg.Region)
	}
	if cfg.AccountID != "000000000000" {
		t.Errorf("AccountID: expected 000000000000, got %q", cfg.AccountID)
	}
	if cfg.StateConfigured != "auto" {
		t.Errorf("StateConfigured: expected auto, got %q", cfg.StateConfigured)
	}
	if cfg.StateSource != config.StateSourceAuto {
		t.Errorf("StateSource: expected auto, got %q", cfg.StateSource)
	}
	if cfg.State != config.StateBackendMemory {
		t.Errorf("State: expected memory (no persistence signal), got %q", cfg.State)
	}
	if cfg.StateAutoSignal != "" {
		t.Errorf("StateAutoSignal: expected empty for a memory resolution, got %q", cfg.StateAutoSignal)
	}
	if cfg.StateAutoReason == "" {
		t.Error("StateAutoReason: expected a non-empty explanation")
	}
	if cfg.HybridFlushInterval != 5*time.Second {
		t.Errorf("HybridFlushInterval: expected 5s, got %v", cfg.HybridFlushInterval)
	}
	if cfg.HybridSyncMode != "interval" {
		t.Errorf("HybridSyncMode: expected interval, got %q", cfg.HybridSyncMode)
	}
	if cfg.HybridSyncInterval != 100*time.Millisecond {
		t.Errorf("HybridSyncInterval: expected 100ms, got %v", cfg.HybridSyncInterval)
	}
	if cfg.HybridDirtyEntryThreshold != 10000 {
		t.Errorf("HybridDirtyEntryThreshold: expected 10000, got %d", cfg.HybridDirtyEntryThreshold)
	}
	if cfg.HybridDirtyByteThreshold != 8388608 {
		t.Errorf("HybridDirtyByteThreshold: expected 8388608, got %d", cfg.HybridDirtyByteThreshold)
	}
	if cfg.WALFsyncMode != "interval" {
		t.Errorf("WALFsyncMode: expected interval, got %q", cfg.WALFsyncMode)
	}
	if cfg.WALFsyncInterval != 100*time.Millisecond {
		t.Errorf("WALFsyncInterval: expected 100ms, got %v", cfg.WALFsyncInterval)
	}
	if cfg.WALMaxLogBytes != 67108864 {
		t.Errorf("WALMaxLogBytes: expected 67108864, got %d", cfg.WALMaxLogBytes)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel: expected info, got %q", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Errorf("ShutdownTimeout: expected 5s, got %v", cfg.ShutdownTimeout)
	}
	if cfg.LambdaHotReload {
		t.Error("LambdaHotReload: expected false by default")
	}
	// 0 means "unset": the Lambda runtime derives these from the Docker host
	// (falling back to 4/25/10 when /info is unavailable).
	if cfg.LambdaDockerMaxConcurrentStarts != 0 {
		t.Errorf("LambdaDockerMaxConcurrentStarts: expected 0 (auto), got %d", cfg.LambdaDockerMaxConcurrentStarts)
	}
	if cfg.LambdaMaxInstances != 0 {
		t.Errorf("LambdaMaxInstances: expected 0 (auto), got %d", cfg.LambdaMaxInstances)
	}
	if cfg.LambdaMaxInstancesPerFunction != 0 {
		t.Errorf("LambdaMaxInstancesPerFunction: expected 0 (auto), got %d", cfg.LambdaMaxInstancesPerFunction)
	}
	if cfg.LambdaMaxMemoryMB != 0 {
		t.Errorf("LambdaMaxMemoryMB: expected 0 (auto), got %d", cfg.LambdaMaxMemoryMB)
	}
	if cfg.LambdaSeedRuntimeImages {
		t.Error("LambdaSeedRuntimeImages: expected false by default")
	}
	if !cfg.LambdaProactiveInit {
		t.Error("LambdaProactiveInit: expected true by default (issue #1099)")
	}
	if cfg.LambdaInitTimeout != 10*time.Second {
		t.Errorf("LambdaInitTimeout: expected 10s, got %v", cfg.LambdaInitTimeout)
	}
	if cfg.Debug {
		t.Error("Debug: expected false by default")
	}
	if cfg.TLSEnabled() {
		t.Error("TLS: expected disabled by default")
	}
	if cfg.Hostname != "" {
		t.Errorf("Hostname: expected empty, got %q", cfg.Hostname)
	}
	if cfg.ExternalHostname() != "localhost" {
		t.Errorf("ExternalHostname(): expected localhost, got %q", cfg.ExternalHostname())
	}
	if cfg.ExternalBaseURL() != "http://localhost:4566" {
		t.Errorf("ExternalBaseURL(): expected http://localhost:4566, got %q", cfg.ExternalBaseURL())
	}
	if cfg.MCPRemoteExposure {
		t.Error("MCPRemoteExposure: expected false by default")
	}
	if cfg.MCPAuthToken != "" {
		t.Errorf("MCPAuthToken: expected empty by default, got %q", cfg.MCPAuthToken)
	}
	if cfg.EnforceIAM {
		t.Error("EnforceIAM: expected false by default")
	}
	if cfg.EnforceAPIGatewayThrottle {
		t.Error("EnforceAPIGatewayThrottle: expected false by default")
	}
	if cfg.EnforceAppSyncCognitoAuth {
		t.Error("EnforceAppSyncCognitoAuth: expected false by default")
	}
	if cfg.EnforceLambdaResourcePolicy {
		t.Error("EnforceLambdaResourcePolicy: expected false by default")
	}
	if cfg.ProtocolStrict {
		t.Error("ProtocolStrict: expected false by default (lenient drift posture)")
	}
	if cfg.CFNSyncWait != time.Second {
		t.Errorf("CFNSyncWait: expected 1s, got %v", cfg.CFNSyncWait)
	}
}

func TestLoad_cfnSyncWaitMS(t *testing.T) {
	// Given: OVERCAST_CFN_SYNC_WAIT_MS is set.
	clearEnv(t)
	t.Setenv("OVERCAST_CFN_SYNC_WAIT_MS", "250")

	// When: we load config.
	cfg, err := config.Load()

	// Then: the CloudFormation sync wait budget is parsed as milliseconds.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CFNSyncWait != 250*time.Millisecond {
		t.Fatalf("CFNSyncWait = %v, want 250ms", cfg.CFNSyncWait)
	}
}

func TestLoad_lambdaDockerMaxConcurrentStarts(t *testing.T) {
	// Given: LAMBDA_DOCKER_MAX_CONCURRENT_STARTS is set.
	clearEnv(t)
	t.Setenv("LAMBDA_DOCKER_MAX_CONCURRENT_STARTS", "7")

	// When: we load config.
	cfg, err := config.Load()

	// Then: the Docker startup backpressure limit is parsed.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LambdaDockerMaxConcurrentStarts != 7 {
		t.Fatalf("LambdaDockerMaxConcurrentStarts = %d, want 7", cfg.LambdaDockerMaxConcurrentStarts)
	}
}

func TestLoad_lambdaMaxMemoryMB(t *testing.T) {
	// Given: LAMBDA_MAX_MEMORY_MB is set.
	clearEnv(t)
	t.Setenv("LAMBDA_MAX_MEMORY_MB", "2048")

	// When: we load config.
	cfg, err := config.Load()

	// Then: the aggregate Lambda memory budget is parsed.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LambdaMaxMemoryMB != 2048 {
		t.Fatalf("LambdaMaxMemoryMB = %d, want 2048", cfg.LambdaMaxMemoryMB)
	}
}

func TestLoad_lambdaSeedRuntimeImages(t *testing.T) {
	// Given: LAMBDA_SEED_RUNTIME_IMAGES is enabled.
	clearEnv(t)
	t.Setenv("LAMBDA_SEED_RUNTIME_IMAGES", "true")

	// When: we load config.
	cfg, err := config.Load()

	// Then: broad startup runtime image seeding is enabled explicitly.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LambdaSeedRuntimeImages {
		t.Fatal("LambdaSeedRuntimeImages = false, want true")
	}
}

// TestLoad_lambdaProactiveInitOptOut pins the opt-out direction: the default
// flipped to true in issue #1099, so LAMBDA_PROACTIVE_INIT=false must still
// disable it.
func TestLoad_lambdaProactiveInitOptOut(t *testing.T) {
	// Given: LAMBDA_PROACTIVE_INIT is explicitly disabled.
	clearEnv(t)
	t.Setenv("LAMBDA_PROACTIVE_INIT", "false")

	// When: we load config.
	cfg, err := config.Load()

	// Then: proactive init is off despite the new default.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LambdaProactiveInit {
		t.Fatal("LambdaProactiveInit = true, want false (explicit opt-out)")
	}
}

// TestLoad_lambdaProactiveInitExplicitTrue pins that an explicit "true" is
// equivalent to the default, so the opt-in spelling used before the flip
// keeps working unchanged.
func TestLoad_lambdaProactiveInitExplicitTrue(t *testing.T) {
	// Given: LAMBDA_PROACTIVE_INIT is explicitly enabled.
	clearEnv(t)
	t.Setenv("LAMBDA_PROACTIVE_INIT", "true")

	// When: we load config.
	cfg, err := config.Load()

	// Then: proactive init is on.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LambdaProactiveInit {
		t.Fatal("LambdaProactiveInit = false, want true (explicit opt-in)")
	}
}

func TestLoad_lambdaInitTimeoutSeconds(t *testing.T) {
	// Given: LAMBDA_INIT_TIMEOUT_SECONDS is set.
	clearEnv(t)
	t.Setenv("LAMBDA_INIT_TIMEOUT_SECONDS", "17")

	// When: we load config.
	cfg, err := config.Load()

	// Then: the Lambda init timeout is parsed as a duration.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LambdaInitTimeout != 17*time.Second {
		t.Fatalf("LambdaInitTimeout = %v, want 17s", cfg.LambdaInitTimeout)
	}
}

// TestLoad_ignoresRemovedServicesVar pins that OVERCAST_SERVICES is inert.
//
// It used to select which services ran. Every service is now always wired, so
// a stale value in an old compose file or CI job must load cleanly rather than
// failing validation or half-configuring anything.
func TestLoad_ignoresRemovedServicesVar(t *testing.T) {
	// Given: a leftover OVERCAST_SERVICES, including a name that never existed
	clearEnv(t)
	t.Setenv("OVERCAST_SERVICES", "s3,sqs,unknownservice")

	// When: we load config
	_, err := config.Load()

	// Then: it is ignored entirely
	if err != nil {
		t.Fatalf("Load should ignore OVERCAST_SERVICES, got: %v", err)
	}
}

// TestLoad_invalidPort verifies that non-numeric ports are rejected.
func TestLoad_invalidPort(t *testing.T) {
	// Given: OVERCAST_PORT is not a valid port number
	clearEnv(t)
	t.Setenv("OVERCAST_PORT", "notaport")

	// When: we load config
	_, err := config.Load()

	// Then: an error is returned
	if err == nil {
		t.Error("expected error for invalid port, got nil")
	}
}

// TestLoad_portOutOfRange verifies that ports outside 1-65535 are rejected.
func TestLoad_portOutOfRange(t *testing.T) {
	// Given: OVERCAST_PORT is out of valid range
	cases := []string{"0", "65536", "-1", "99999"}
	for _, p := range cases {
		t.Run("port="+p, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_PORT", p)

			// When + Then
			_, err := config.Load()
			if err == nil {
				t.Errorf("expected error for port %q, got nil", p)
			}
		})
	}
}

// TestLoad_persistentState verifies the persistent (SQLite) backend is selected correctly.
func TestLoad_persistentState(t *testing.T) {
	// Given: OVERCAST_STATE=persistent
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "persistent")
	t.Setenv("OVERCAST_DATA_DIR", "/tmp/test-overcast")

	// When: we load config
	cfg, err := config.Load()

	// Then: persistent backend is selected
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendPersistent {
		t.Errorf("expected persistent backend, got %q", cfg.State)
	}
	if cfg.DataDir != "/tmp/test-overcast" {
		t.Errorf("DataDir: expected /tmp/test-overcast, got %q", cfg.DataDir)
	}
	if cfg.StateSource != config.StateSourceExplicit {
		t.Errorf("StateSource: expected explicit for an explicitly-set OVERCAST_STATE, got %q", cfg.StateSource)
	}
	if cfg.StateConfigured != "persistent" {
		t.Errorf("StateConfigured: expected persistent, got %q", cfg.StateConfigured)
	}
}

// TestLoad_sqliteAlias verifies "sqlite" is accepted as a deprecated alias for "persistent".
func TestLoad_sqliteAlias(t *testing.T) {
	// Given: OVERCAST_STATE=sqlite (deprecated alias)
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "sqlite")

	// When: we load config
	cfg, err := config.Load()

	// Then: it resolves to persistent
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendPersistent {
		t.Errorf("expected sqlite alias to resolve to persistent, got %q", cfg.State)
	}
}

// TestLoad_hybridState verifies the hybrid backend is selected correctly.
func TestLoad_hybridState(t *testing.T) {
	// Given: OVERCAST_STATE=hybrid
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "hybrid")

	// When: we load config
	cfg, err := config.Load()

	// Then: hybrid backend is selected with the default flush interval
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendHybrid {
		t.Errorf("expected hybrid backend, got %q", cfg.State)
	}
	if cfg.HybridFlushInterval != 5*time.Second {
		t.Errorf("HybridFlushInterval: expected 5s default, got %v", cfg.HybridFlushInterval)
	}
}

// TestLoad_hybridFlushInterval verifies the flush interval can be overridden.
func TestLoad_hybridFlushInterval(t *testing.T) {
	// Given: OVERCAST_HYBRID_FLUSH_INTERVAL=10s
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "hybrid")
	t.Setenv("OVERCAST_HYBRID_FLUSH_INTERVAL", "10s")

	// When: we load config
	cfg, err := config.Load()

	// Then: the flush interval is 10 seconds
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HybridFlushInterval != 10*time.Second {
		t.Errorf("HybridFlushInterval: expected 10s, got %v", cfg.HybridFlushInterval)
	}
}

// TestLoad_hybridSyncConfigOverrides verifies the pending-log sync mode and
// size-triggered flush thresholds (1.3/1.4) can be overridden.
func TestLoad_hybridSyncConfigOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HYBRID_SYNC", "always")
	t.Setenv("OVERCAST_HYBRID_SYNC_INTERVAL", "250ms")
	t.Setenv("OVERCAST_HYBRID_DIRTY_ENTRY_THRESHOLD", "42")
	t.Setenv("OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD", "4096")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HybridSyncMode != "always" {
		t.Errorf("HybridSyncMode: expected always, got %q", cfg.HybridSyncMode)
	}
	if cfg.HybridSyncInterval != 250*time.Millisecond {
		t.Errorf("HybridSyncInterval: expected 250ms, got %v", cfg.HybridSyncInterval)
	}
	if cfg.HybridDirtyEntryThreshold != 42 {
		t.Errorf("HybridDirtyEntryThreshold: expected 42, got %d", cfg.HybridDirtyEntryThreshold)
	}
	if cfg.HybridDirtyByteThreshold != 4096 {
		t.Errorf("HybridDirtyByteThreshold: expected 4096, got %d", cfg.HybridDirtyByteThreshold)
	}
}

func TestLoad_invalidHybridSyncConfig(t *testing.T) {
	t.Run("invalid sync mode", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_HYBRID_SYNC", "sometimes")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_HYBRID_SYNC")
		}
	})

	t.Run("invalid interval", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_HYBRID_SYNC_INTERVAL", "0s")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_HYBRID_SYNC_INTERVAL")
		}
	})

	t.Run("invalid dirty byte threshold", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD", "0")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD")
		}
	})
}

// TestLoad_walState verifies the WAL backend is selected correctly.
func TestLoad_walState(t *testing.T) {
	// Given: OVERCAST_STATE=wal
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "wal")

	// When: we load config
	cfg, err := config.Load()

	// Then: wal backend is selected
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendWAL {
		t.Errorf("expected wal backend, got %q", cfg.State)
	}
}

func TestLoad_walConfigOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_WAL_FSYNC", "always")
	t.Setenv("OVERCAST_WAL_FSYNC_INTERVAL", "250ms")
	t.Setenv("OVERCAST_WAL_MAX_LOG_BYTES", "4096")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.WALFsyncMode != "always" {
		t.Errorf("WALFsyncMode: expected always, got %q", cfg.WALFsyncMode)
	}
	if cfg.WALFsyncInterval != 250*time.Millisecond {
		t.Errorf("WALFsyncInterval: expected 250ms, got %v", cfg.WALFsyncInterval)
	}
	if cfg.WALMaxLogBytes != 4096 {
		t.Errorf("WALMaxLogBytes: expected 4096, got %d", cfg.WALMaxLogBytes)
	}
}

func TestLoad_invalidWALConfig(t *testing.T) {
	t.Run("invalid fsync mode", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_WAL_FSYNC", "sometimes")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_WAL_FSYNC")
		}
	})

	t.Run("invalid interval", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_WAL_FSYNC_INTERVAL", "0s")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_WAL_FSYNC_INTERVAL")
		}
	})

	t.Run("invalid max log bytes", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("OVERCAST_WAL_MAX_LOG_BYTES", "0")
		if _, err := config.Load(); err == nil {
			t.Fatal("expected error for invalid OVERCAST_WAL_MAX_LOG_BYTES")
		}
	})
}

// TestLoad_perServiceState verifies per-service storage overrides are parsed.
func TestLoad_perServiceState(t *testing.T) {
	// Given: OVERCAST_STATE=hybrid and per-service overrides
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "hybrid")
	t.Setenv("OVERCAST_STATE_S3", "memory")
	t.Setenv("OVERCAST_STATE_SQS", "persistent")

	// When: we load config
	cfg, err := config.Load()

	// Then: per-service modes are recorded
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServiceStates["s3"] != config.StateBackendMemory {
		t.Errorf("s3: expected memory, got %q", cfg.ServiceStates["s3"])
	}
	if cfg.ServiceStates["sqs"] != config.StateBackendPersistent {
		t.Errorf("sqs: expected persistent, got %q", cfg.ServiceStates["sqs"])
	}
	if _, ok := cfg.ServiceStates["dynamodb"]; ok {
		t.Errorf("dynamodb: expected no override, but found one")
	}
}

// TestLoad_perServiceStateUnknownService verifies that an override naming a
// service that does not exist is rejected rather than ignored.
//
// Load used to probe os.Getenv("OVERCAST_STATE_"+svc) for each known service,
// which cannot see a variable it does not think to construct. So
// OVERCAST_STATE_CLOUDWATCH_LOGS — the plausible guess for a service actually
// named "logs", and what the docs implied until recently — was a silent no-op:
// the user believed CloudWatch Logs was on its own backend while it quietly
// used the global one. Every misspelling and every post-rename name behaved the
// same way.
func TestLoad_perServiceStateUnknownService(t *testing.T) {
	for _, envKey := range []string{
		"OVERCAST_STATE_CLOUDWATCH_LOGS", // real service, wrong name for it
		"OVERCAST_STATE_S4",              // typo
		"OVERCAST_STATE_NOTASERVICE",
	} {
		t.Run(envKey, func(t *testing.T) {
			// Given: an override naming something that is not a service
			clearEnv(t)
			t.Setenv(envKey, "memory")

			// When: we load config
			_, err := config.Load()

			// Then: it fails, naming the offending variable
			if err == nil {
				t.Fatalf("expected %s to be rejected, got nil error", envKey)
			}
			if !strings.Contains(err.Error(), envKey) {
				t.Errorf("error should name %s, got: %v", envKey, err)
			}
		})
	}
}

// TestLoad_perServiceStateEmptyValueIgnored pins that an empty override is
// treated as unset, not as a service name to validate — matching how every
// other variable in Load behaves.
func TestLoad_perServiceStateEmptyValueIgnored(t *testing.T) {
	// Given: a real service override set to the empty string
	clearEnv(t)
	t.Setenv("OVERCAST_STATE_S3", "")

	// When: we load config
	cfg, err := config.Load()

	// Then: it loads, with no override recorded
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.ServiceStates["s3"]; ok {
		t.Error("s3: expected no override for an empty value")
	}
}

// TestLoad_perServiceInvalidState verifies invalid per-service modes are rejected.
func TestLoad_perServiceInvalidState(t *testing.T) {
	// Given: per-service override has an unknown backend
	clearEnv(t)
	t.Setenv("OVERCAST_STATE_S3", "redis")

	// When + Then
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for unknown per-service state backend, got nil")
	}
}

// ---- OVERCAST_STATE=auto (and unset-default) resolution ------------------
//
// These exercise Load() end-to-end for each auto-detection signal
// individually (the pure decision table across all signal combinations is
// covered by TestResolveAutoState_allSignalCombinations in
// state_auto_internal_test.go). Each test pins the environment so the
// resolution is deterministic regardless of the host running the test.

// TestLoad_explicitAutoSameAsUnset verifies that OVERCAST_STATE=auto,
// written out literally, behaves identically to leaving it unset.
func TestLoad_explicitAutoSameAsUnset(t *testing.T) {
	// Given: OVERCAST_STATE=auto (literal), and no persistence signal.
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "auto")
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

	// When
	cfg, err := config.Load()

	// Then: resolves exactly like the unset default (memory, no signal).
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StateConfigured != "auto" {
		t.Errorf("StateConfigured: expected auto, got %q", cfg.StateConfigured)
	}
	if cfg.StateSource != config.StateSourceAuto {
		t.Errorf("StateSource: expected auto, got %q", cfg.StateSource)
	}
	if cfg.State != config.StateBackendMemory {
		t.Errorf("State: expected memory, got %q", cfg.State)
	}
}

// TestLoad_autoResolvesToHybridWhenDataDirExplicit verifies the
// explicit-data-dir signal: a native run where the user set OVERCAST_DATA_DIR
// themselves (no OVERCAST_DATA_DIR_SOURCE=image marker) is treated as
// deliberate persistence intent.
func TestLoad_autoResolvesToHybridWhenDataDirExplicit(t *testing.T) {
	// This assumes a build with SQLite support: in a -tags nosqlite build,
	// auto never resolves to hybrid regardless of evidence (see
	// TestResolveAutoState_sqliteUnavailableOverridesEveryEvidenceSignal in
	// state_auto_internal_test.go, which covers that gate directly).
	if !config.SQLiteSupported() {
		t.Skip("hybrid is unavailable in a -tags nosqlite build; the SQLite gate is covered directly in state_auto_internal_test.go")
	}

	// Given: OVERCAST_STATE unset, OVERCAST_DATA_DIR explicitly set (as a
	// native user would), with no OVERCAST_DATA_DIR_SOURCE marker.
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())

	// When
	cfg, err := config.Load()

	// Then: resolves to hybrid via the explicit-data-dir signal.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendHybrid {
		t.Errorf("State: expected hybrid, got %q", cfg.State)
	}
	if cfg.StateAutoSignal != "explicit-data-dir" {
		t.Errorf("StateAutoSignal: expected explicit-data-dir, got %q", cfg.StateAutoSignal)
	}
}

// TestLoad_autoResolvesToHybridWhenExistingDatabaseFound verifies the
// regression-guard signal: an overcast.db already present in the data
// directory means someone persisted state there before, so auto must not
// strand it in memory mode even with no other signal present.
func TestLoad_autoResolvesToHybridWhenExistingDatabaseFound(t *testing.T) {
	if !config.SQLiteSupported() {
		t.Skip("hybrid is unavailable in a -tags nosqlite build; the SQLite gate is covered directly in state_auto_internal_test.go")
	}

	// Given: a data directory marked as the image's own default (so the
	// explicit-data-dir signal does NOT fire) but containing an existing
	// overcast.db from a prior run.
	clearEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "overcast.db"), []byte{}, 0o644); err != nil {
		t.Fatalf("seed overcast.db: %v", err)
	}
	t.Setenv("OVERCAST_DATA_DIR", dir)
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

	// When
	cfg, err := config.Load()

	// Then: resolves to hybrid via the existing-database signal, not memory.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendHybrid {
		t.Errorf("State: expected hybrid (existing database must not be stranded in memory mode), got %q", cfg.State)
	}
	if cfg.StateAutoSignal != "existing-database" {
		t.Errorf("StateAutoSignal: expected existing-database, got %q", cfg.StateAutoSignal)
	}
}

// TestLoad_explicitStateOverridesAuto verifies that an explicit OVERCAST_STATE
// always wins, even when auto-detection signals are present (e.g. a volume
// mounted at the data dir, or an existing database) — rule 1 from the design:
// "OVERCAST_STATE set by the user wins, exactly as today."
func TestLoad_explicitStateOverridesAuto(t *testing.T) {
	// Given: OVERCAST_STATE=memory explicitly, plus an existing database that
	// would otherwise trigger the auto resolver's hybrid outcome.
	clearEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "overcast.db"), []byte{}, 0o644); err != nil {
		t.Fatalf("seed overcast.db: %v", err)
	}
	t.Setenv("OVERCAST_STATE", "memory")
	t.Setenv("OVERCAST_DATA_DIR", dir)

	// When
	cfg, err := config.Load()

	// Then: the explicit setting wins outright — no auto resolution runs.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.State != config.StateBackendMemory {
		t.Errorf("State: expected memory (explicit override wins), got %q", cfg.State)
	}
	if cfg.StateSource != config.StateSourceExplicit {
		t.Errorf("StateSource: expected explicit, got %q", cfg.StateSource)
	}
	if cfg.StateAutoSignal != "" {
		t.Errorf("StateAutoSignal: expected empty (auto resolution never ran), got %q", cfg.StateAutoSignal)
	}
}

// TestLoad_invalidState verifies that unknown state backends are rejected.
func TestLoad_invalidState(t *testing.T) {
	// Given: OVERCAST_STATE is not a known backend
	clearEnv(t)
	t.Setenv("OVERCAST_STATE", "redis")

	// When + Then
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for unknown state backend, got nil")
	}
}

// TestLoad_tlsBothRequired verifies that TLS requires both cert and key.
func TestLoad_tlsBothRequired(t *testing.T) {
	// Given: only one TLS variable is set
	clearEnv(t)
	t.Setenv("OVERCAST_TLS_CERT", "/path/to/cert.pem")
	// OVERCAST_TLS_KEY is not set

	// When + Then
	_, err := config.Load()
	if err == nil {
		t.Error("expected error when only TLS cert is set (key missing), got nil")
	}
}

// TestLoad_tlsEnabled verifies TLS is enabled when both cert and key are set.
func TestLoad_tlsEnabled(t *testing.T) {
	// Given: both TLS cert and key are set
	clearEnv(t)
	t.Setenv("OVERCAST_TLS_CERT", "/path/to/cert.pem")
	t.Setenv("OVERCAST_TLS_KEY", "/path/to/key.pem")

	// When: we load config
	cfg, err := config.Load()

	// Then: TLS is enabled
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TLSEnabled() {
		t.Error("expected TLSEnabled() to return true")
	}
}

// TestLoad_tlsAutoMode verifies OVERCAST_TLS=auto enables TLS without
// explicit cert/key paths.
func TestLoad_tlsAutoMode(t *testing.T) {
	// Given: auto TLS mode, no cert/key
	clearEnv(t)
	t.Setenv("OVERCAST_TLS", "auto")

	// When: we load config
	cfg, err := config.Load()

	// Then: TLS is on and recognised as the auto flavour
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TLSEnabled() {
		t.Error("expected TLSEnabled() with OVERCAST_TLS=auto")
	}
	if !cfg.TLSAuto() {
		t.Error("expected TLSAuto() with OVERCAST_TLS=auto")
	}
}

// TestLoad_tlsAutoCaseInsensitive verifies "AUTO" is accepted too.
func TestLoad_tlsAutoCaseInsensitive(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_TLS", "AUTO")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TLSAuto() {
		t.Error("expected TLSAuto() with OVERCAST_TLS=AUTO")
	}
}

// TestLoad_tlsRejectsUnknownMode verifies a typo'd OVERCAST_TLS fails loudly
// rather than silently serving plain HTTP.
func TestLoad_tlsRejectsUnknownMode(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_TLS", "yes-please")

	if _, err := config.Load(); err == nil {
		t.Error("expected error for unknown OVERCAST_TLS value, got nil")
	}
}

// TestLoad_tlsAutoConflictsWithExplicitCert verifies that combining
// OVERCAST_TLS=auto with OVERCAST_TLS_CERT/KEY is rejected: the two are
// different answers to the same question and silently preferring one would
// hide a configuration mistake.
func TestLoad_tlsAutoConflictsWithExplicitCert(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_TLS", "auto")
	t.Setenv("OVERCAST_TLS_CERT", "/path/to/cert.pem")
	t.Setenv("OVERCAST_TLS_KEY", "/path/to/key.pem")

	if _, err := config.Load(); err == nil {
		t.Error("expected error when OVERCAST_TLS=auto is combined with explicit cert/key, got nil")
	}
}

// TestLoad_tlsOffValuesAccepted verifies the explicit "off" spellings keep
// TLS disabled rather than erroring.
func TestLoad_tlsOffValuesAccepted(t *testing.T) {
	for _, v := range []string{"", "off", "false", "0"} {
		t.Run("value="+v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_TLS", v)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load with OVERCAST_TLS=%q: %v", v, err)
			}
			if cfg.TLSEnabled() || cfg.TLSAuto() {
				t.Errorf("OVERCAST_TLS=%q unexpectedly enabled TLS", v)
			}
		})
	}
}

// TestTLSAutoSANs verifies the SAN set the auto-minted certificate covers:
// loopback, every built-in wildcard DNS domain (apex, one-level wildcard, and
// the S3 virtual-host level), plus operator-configured names.
func TestTLSAutoSANs(t *testing.T) {
	// Given: a config with a custom hostname and an extra split-horizon host
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "overcast.internal")
	t.Setenv("OVERCAST_SPLIT_HORIZON_HOSTS", "dev.example.test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// When: the SAN set is derived
	sans := cfg.TLSAutoSANs()
	has := func(want string) bool {
		for _, s := range sans {
			if s == want {
				return true
			}
		}
		return false
	}

	// Then: loopback names and addresses are covered
	for _, want := range []string{"localhost", "127.0.0.1", "::1"} {
		if !has(want) {
			t.Errorf("TLSAutoSANs missing %q (got %v)", want, sans)
		}
	}
	// And: each built-in wildcard DNS domain gets apex + wildcard + S3 level
	for _, base := range config.WildcardDNSDomains {
		for _, want := range []string{base, "*." + base, "*.s3." + base} {
			if !has(want) {
				t.Errorf("TLSAutoSANs missing %q (got %v)", want, sans)
			}
		}
	}
	// And: operator-configured names are covered the same way
	for _, want := range []string{
		"overcast.internal", "*.overcast.internal",
		"dev.example.test", "*.dev.example.test",
	} {
		if !has(want) {
			t.Errorf("TLSAutoSANs missing %q (got %v)", want, sans)
		}
	}
}

// TestTLSAutoSANs_ipHostname verifies an IP-literal OVERCAST_HOSTNAME becomes
// a SAN itself without nonsensical wildcard variants.
func TestTLSAutoSANs_ipHostname(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "192.168.1.50")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	sans := cfg.TLSAutoSANs()
	foundIP, foundWildcard := false, false
	for _, s := range sans {
		if s == "192.168.1.50" {
			foundIP = true
		}
		if s == "*.192.168.1.50" {
			foundWildcard = true
		}
	}
	if !foundIP {
		t.Errorf("TLSAutoSANs missing IP hostname (got %v)", sans)
	}
	if foundWildcard {
		t.Errorf("TLSAutoSANs contains a wildcard over an IP literal (got %v)", sans)
	}
}

// TestLoad_debugEnabled verifies debug mode is enabled via env var.
func TestLoad_debugEnabled(t *testing.T) {
	// Given: OVERCAST_DEBUG=true
	clearEnv(t)
	t.Setenv("OVERCAST_DEBUG", "true")

	// When: we load config
	cfg, err := config.Load()

	// Then: debug mode is on
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Debug {
		t.Error("expected Debug to be true")
	}
}

// TestLoad_lambdaHotReloadEnabled verifies Lambda hot-reload mode is enabled via env var.
func TestLoad_lambdaHotReloadEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_LAMBDA_HOT_RELOAD", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LambdaHotReload {
		t.Error("expected LambdaHotReload to be true")
	}
}

// TestLoad_hotReloadInheritance verifies the umbrella/override relationship
// between OVERCAST_HOT_RELOAD and the per-service flags. The interesting case
// is the last one: an explicit "false" must beat an umbrella "true", which is
// what lets a developer run one service's containers from an image while the
// rest hot reload. That behaviour rests entirely on envBool returning its
// fallback only for an absent variable, so it is pinned here.
func TestLoad_hotReloadInheritance(t *testing.T) {
	tests := []struct {
		name                  string
		umbrella, lambda, ecs string
		wantLambda, wantECS   bool
	}{
		{name: "nothing set"},
		{name: "umbrella on", umbrella: "true", wantLambda: true, wantECS: true},
		{name: "lambda only", lambda: "true", wantLambda: true},
		{name: "ecs only", ecs: "true", wantECS: true},
		{name: "umbrella off, ecs on", umbrella: "false", ecs: "true", wantECS: true},
		{name: "umbrella on, lambda opted out", umbrella: "true", lambda: "false", wantECS: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			if tc.umbrella != "" {
				t.Setenv("OVERCAST_HOT_RELOAD", tc.umbrella)
			}
			if tc.lambda != "" {
				t.Setenv("OVERCAST_LAMBDA_HOT_RELOAD", tc.lambda)
			}
			if tc.ecs != "" {
				t.Setenv("OVERCAST_ECS_HOT_RELOAD", tc.ecs)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.LambdaHotReload != tc.wantLambda {
				t.Errorf("LambdaHotReload = %v, want %v", cfg.LambdaHotReload, tc.wantLambda)
			}
			if cfg.ECSHotReload != tc.wantECS {
				t.Errorf("ECSHotReload = %v, want %v", cfg.ECSHotReload, tc.wantECS)
			}
		})
	}
}

// TestLoad_hostBinding verifies custom host binding.
func TestLoad_hostBinding(t *testing.T) {
	// Given: OVERCAST_LISTEN is set to localhost only
	clearEnv(t)
	t.Setenv("OVERCAST_LISTEN", "127.0.0.1")
	t.Setenv("OVERCAST_PORT", "9000")

	// When: we load config
	cfg, err := config.Load()

	// Then: Addr() returns the correct binding
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != "127.0.0.1:9000" {
		t.Errorf("Addr(): expected 127.0.0.1:9000, got %q", cfg.Addr())
	}
	// And: the single address is the whole bind set
	if got := cfg.Addrs(); len(got) != 1 || got[0] != "127.0.0.1:9000" {
		t.Errorf("Addrs(): expected [127.0.0.1:9000], got %v", got)
	}
}

// TestLoad_hostBindingList verifies OVERCAST_LISTEN accepts several addresses.
func TestLoad_hostBindingList(t *testing.T) {
	// Given: OVERCAST_LISTEN names loopback and a host-local bridge address —
	// the shape the compat launcher uses so a sibling container can reach an
	// emulator that is otherwise invisible outside this machine
	clearEnv(t)
	t.Setenv("OVERCAST_LISTEN", " 127.0.0.1 , 172.17.0.1 ,,127.0.0.1 ")
	t.Setenv("OVERCAST_PORT", "9000")

	// When: we load config
	cfg, err := config.Load()

	// Then: both addresses are bound, in order, with blanks and the repeat
	// dropped — a duplicate would fail the second bind with EADDRINUSE
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"127.0.0.1:9000", "172.17.0.1:9000"}
	if got := cfg.Addrs(); !slices.Equal(got, want) {
		t.Errorf("Addrs(): expected %v, got %v", want, got)
	}
	// And: Host stays the first address, so every reader that predates the
	// list — Addr(), the debug endpoint — sees what it always did
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host: expected the first address 127.0.0.1, got %q", cfg.Host)
	}
	if cfg.Addr() != "127.0.0.1:9000" {
		t.Errorf("Addr(): expected 127.0.0.1:9000, got %q", cfg.Addr())
	}
}

// TestLoad_hostBindingRejectsWildcardInList verifies a wildcard cannot be
// combined with a specific address.
func TestLoad_hostBindingRejectsWildcardInList(t *testing.T) {
	// Given: OVERCAST_LISTEN pairs a wildcard with a specific address
	// When: we load config
	// Then: it is refused. A wildcard already covers every address, and on
	// Linux the second bind fails with EADDRINUSE — which would surface as a
	// listen error at startup rather than as the configuration mistake it is.
	for _, host := range []string{"0.0.0.0,127.0.0.1", "127.0.0.1,::", "::,127.0.0.1"} {
		clearEnv(t)
		t.Setenv("OVERCAST_LISTEN", host)

		_, err := config.Load()
		if err == nil {
			t.Errorf("OVERCAST_LISTEN=%q: expected an error, got none", host)
			continue
		}
		if !strings.Contains(err.Error(), "OVERCAST_LISTEN") {
			t.Errorf("OVERCAST_LISTEN=%q: error %q does not name the variable", host, err)
		}
	}
}

// TestLoad_hostBindingBracketsIPv6Literals verifies an IPv6 address comes back
// in a form net.Listen accepts.
func TestLoad_hostBindingBracketsIPv6Literals(t *testing.T) {
	// Given: OVERCAST_LISTEN pairing the two loopbacks
	clearEnv(t)
	t.Setenv("OVERCAST_LISTEN", "127.0.0.1,::1")
	t.Setenv("OVERCAST_PORT", "9000")

	// When: we load config
	cfg, err := config.Load()

	// Then: the v6 address is bracketed. Unbracketed it formats as
	// "::1:9000", which net.Listen reads as a different address entirely —
	// the kind of thing nobody writes until a list makes pairing v4 and v6
	// the obvious thing to do.
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"127.0.0.1:9000", "[::1]:9000"}
	if got := cfg.Addrs(); !slices.Equal(got, want) {
		t.Errorf("Addrs(): expected %v, got %v", want, got)
	}
}

// TestLoad_hostBindingRejectsAnEmptyList verifies a list of nothing is an
// error rather than a silent fall back to the wildcard.
func TestLoad_hostBindingRejectsAnEmptyList(t *testing.T) {
	// Given: OVERCAST_LISTEN set to separators and whitespace only
	// When: we load config
	// Then: it is refused. Unset means "use the default"; set to something
	// that names no address means the value is wrong, and defaulting it would
	// bind every interface — the opposite of what anyone setting this wants.
	for _, host := range []string{",", " , ", ",,"} {
		clearEnv(t)
		t.Setenv("OVERCAST_LISTEN", host)

		if _, err := config.Load(); err == nil {
			t.Errorf("OVERCAST_LISTEN=%q: expected an error, got none", host)
		}
	}
}

// TestLoad_hostBindingDefaultsToWildcard_containerised verifies the
// container default is unchanged (#761): Docker's -p publishing requires
// binding every interface, so this must stay 0.0.0.0 regardless of the
// native default narrowing.
func TestLoad_hostBindingDefaultsToWildcard_containerised(t *testing.T) {
	// Given: no OVERCAST_LISTEN, and the Docker image's own signal present
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

	// When: we load config
	cfg, err := config.Load()

	// Then: a single wildcard bind, as before the list was accepted
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host: expected 0.0.0.0, got %q", cfg.Host)
	}
	if got := cfg.Addrs(); len(got) != 1 {
		t.Errorf("Addrs(): expected one address, got %v", got)
	}
	if cfg.ListenSource != config.ListenSourceAuto {
		t.Errorf("ListenSource: expected auto, got %q", cfg.ListenSource)
	}
	if cfg.ListenAutoSignal != "containerised" {
		t.Errorf("ListenAutoSignal: expected containerised, got %q", cfg.ListenAutoSignal)
	}
	if cfg.ListenAutoReason == "" {
		t.Error("ListenAutoReason: expected a non-empty reason")
	}
}

// TestLoad_hostBindingDefaultsToLoopback_native verifies the #761
// environment-dependent default: a native run (no OVERCAST_DATA_DIR_SOURCE
// marker) narrows to loopback rather than the wildcard.
func TestLoad_hostBindingDefaultsToLoopback_native(t *testing.T) {
	// Given: no OVERCAST_LISTEN, and no container signal
	clearEnv(t)

	// When: we load config
	cfg, err := config.Load()

	// Then: loopback, not the wildcard
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host: expected 127.0.0.1, got %q", cfg.Host)
	}
	if got := cfg.Addrs(); len(got) != 1 {
		t.Errorf("Addrs(): expected one address, got %v", got)
	}
	if cfg.ListenSource != config.ListenSourceAuto {
		t.Errorf("ListenSource: expected auto, got %q", cfg.ListenSource)
	}
	if cfg.ListenAutoSignal != "native" {
		t.Errorf("ListenAutoSignal: expected native, got %q", cfg.ListenAutoSignal)
	}
	if cfg.ListenAutoReason == "" {
		t.Error("ListenAutoReason: expected a non-empty reason")
	}
}

// TestLoad_listenExplicit_winsOverDefault verifies an explicit OVERCAST_LISTEN
// always wins over the environment-dependent default, in both directions:
// naming the wildcard natively, and naming loopback in the image.
func TestLoad_listenExplicit_winsOverDefault(t *testing.T) {
	for _, tc := range []struct {
		name        string
		imageMarker bool
		listenVal   string
	}{
		{name: "wildcard explicit natively", imageMarker: false, listenVal: "0.0.0.0"},
		{name: "loopback explicit in the image", imageMarker: true, listenVal: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			if tc.imageMarker {
				t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")
			}
			t.Setenv("OVERCAST_LISTEN", tc.listenVal)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Host != tc.listenVal {
				t.Errorf("Host: expected %q, got %q", tc.listenVal, cfg.Host)
			}
			if cfg.ListenSource != config.ListenSourceExplicit {
				t.Errorf("ListenSource: expected explicit, got %q", cfg.ListenSource)
			}
			if cfg.ListenAutoReason != "" {
				t.Errorf("ListenAutoReason: expected empty when explicit, got %q", cfg.ListenAutoReason)
			}
			if cfg.ListenAutoSignal != "" {
				t.Errorf("ListenAutoSignal: expected empty when explicit, got %q", cfg.ListenAutoSignal)
			}
		})
	}
}

// TestLoad_listenAlone verifies OVERCAST_LISTEN, the only bind-address
// variable since #870 renamed and removed OVERCAST_HOST, binds correctly.
func TestLoad_listenAlone(t *testing.T) {
	// Given: only OVERCAST_LISTEN is set
	clearEnv(t)
	t.Setenv("OVERCAST_LISTEN", "127.0.0.1")
	t.Setenv("OVERCAST_PORT", "9000")

	// When: we load config
	cfg, err := config.Load()

	// Then: it binds as expected
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr() != "127.0.0.1:9000" {
		t.Errorf("Addr(): expected 127.0.0.1:9000, got %q", cfg.Addr())
	}
}

// TestLoad_overcastHostRemoved_fails verifies that OVERCAST_HOST — the
// pre-#870 name for the bind address — is not silently accepted, ignored, or
// treated as an alias. Overcast is alpha software; the project's policy for a
// removed setting is a loud startup failure naming the replacement, so a
// leftover OVERCAST_HOST from before the rename cannot look like it took
// effect while quietly doing nothing.
func TestLoad_overcastHostRemoved_fails(t *testing.T) {
	// Given: the pre-rename name is set, whether or not OVERCAST_LISTEN also is
	for _, tc := range []struct {
		name      string
		setListen bool
		listenVal string
	}{
		{name: "host alone", setListen: false},
		{name: "host and listen set to the same value", setListen: true, listenVal: "127.0.0.1"},
		{name: "host and listen set to different values", setListen: true, listenVal: "0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_HOST", "127.0.0.1")
			if tc.setListen {
				t.Setenv("OVERCAST_LISTEN", tc.listenVal)
			}

			// When: we load config
			_, err := config.Load()

			// Then: it is refused, naming the replacement variable
			if err == nil {
				t.Fatal("expected an error when OVERCAST_HOST is set, got none")
			}
			for _, want := range []string{"OVERCAST_HOST", "OVERCAST_LISTEN", "removed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// TestAddrsWithoutLoad verifies Addrs() on a hand-built Config.
func TestAddrsWithoutLoad(t *testing.T) {
	// Given: a Config assembled in code rather than by Load — the shape every
	// test helper uses, where Hosts is never populated
	cfg := &config.Config{Host: "127.0.0.1", Port: 4566}

	// When: the bind set is read
	// Then: it falls back to the single Host, so a struct literal keeps working
	if got := cfg.Addrs(); len(got) != 1 || got[0] != "127.0.0.1:4566" {
		t.Errorf("Addrs(): expected [127.0.0.1:4566], got %v", got)
	}
}

// TestLoad_hostname verifies OVERCAST_HOSTNAME is used for external URLs.
func TestLoad_hostname(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "overcast.local")
	t.Setenv("OVERCAST_PORT", "5000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "overcast.local" {
		t.Errorf("Hostname: expected overcast.local, got %q", cfg.Hostname)
	}
	if cfg.ExternalHostname() != "overcast.local" {
		t.Errorf("ExternalHostname(): expected overcast.local, got %q", cfg.ExternalHostname())
	}
	if cfg.ExternalBaseURL() != "http://overcast.local:5000" {
		t.Errorf("ExternalBaseURL(): expected http://overcast.local:5000, got %q", cfg.ExternalBaseURL())
	}
}

// TestLoad_splitHorizonHosts verifies OVERCAST_SPLIT_HORIZON_HOSTS is parsed as
// a comma-separated list, since it is written by hand in compose files.
func TestLoad_splitHorizonHosts(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_SPLIT_HORIZON_HOSTS", "localhost.example.test, aws.internal ,")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"localhost.example.test", "aws.internal"}
	if !slices.Equal(cfg.SplitHorizonHosts, want) {
		t.Errorf("SplitHorizonHosts: expected %v, got %v", want, cfg.SplitHorizonHosts)
	}
}

// TestLoad_splitHorizonHostsUnset verifies the default is empty rather than a
// single blank entry, which would produce a malformed /etc/hosts line.
func TestLoad_splitHorizonHostsUnset(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.SplitHorizonHosts) != 0 {
		t.Errorf("SplitHorizonHosts: expected none, got %v", cfg.SplitHorizonHosts)
	}
}

// TestLoad_hostnameWithTLS verifies ExternalBaseURL uses https when TLS is enabled.
func TestLoad_hostnameWithTLS(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "secure.local")
	t.Setenv("OVERCAST_TLS_CERT", "/tmp/cert.pem")
	t.Setenv("OVERCAST_TLS_KEY", "/tmp/key.pem")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ExternalBaseURL() != "https://secure.local:4566" {
		t.Errorf("ExternalBaseURL(): expected https://secure.local:4566, got %q", cfg.ExternalBaseURL())
	}
}

// TestLoad_localstackHostAlias_aloneIsUsed verifies #1190's core case:
// LOCALSTACK_HOST alone (OVERCAST_HOSTNAME unset) supplies Hostname, and the
// alias source is recorded so the startup log line can name it.
func TestLoad_localstackHostAlias_aloneIsUsed(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOCALSTACK_HOST", "localhost.localstack.cloud")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "localhost.localstack.cloud" {
		t.Errorf("Hostname: expected localhost.localstack.cloud, got %q", cfg.Hostname)
	}
	if cfg.HostnameAliasSource != "LOCALSTACK_HOST" {
		t.Errorf("HostnameAliasSource: expected LOCALSTACK_HOST, got %q", cfg.HostnameAliasSource)
	}
}

// TestLoad_localstackHostAlias_portSuffix verifies LOCALSTACK_HOST's
// documented "hostname[:port]" format (LocalStack's own example is
// "localhost.localstack.cloud:4566"): the hostname part maps to
// OVERCAST_HOSTNAME and a port part matching the configured OVERCAST_PORT is
// accepted (not an error), rather than being rejected merely for being
// present.
func TestLoad_localstackHostAlias_portSuffix(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOCALSTACK_HOST", "localhost.localstack.cloud:4566")
	t.Setenv("OVERCAST_PORT", "4566")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "localhost.localstack.cloud" {
		t.Errorf("Hostname: expected localhost.localstack.cloud (port suffix stripped), got %q", cfg.Hostname)
	}
	if cfg.HostnameAliasSource != "LOCALSTACK_HOST" {
		t.Errorf("HostnameAliasSource: expected LOCALSTACK_HOST, got %q", cfg.HostnameAliasSource)
	}
}

// TestLoad_localstackHostAlias_portConflictFails verifies a LOCALSTACK_HOST
// port suffix that disagrees with OVERCAST_PORT fails startup rather than
// silently overriding OVERCAST_PORT or being silently discarded.
func TestLoad_localstackHostAlias_portConflictFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOCALSTACK_HOST", "localhost.localstack.cloud:4566")
	t.Setenv("OVERCAST_PORT", "9000")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when LOCALSTACK_HOST's port disagrees with OVERCAST_PORT, got none")
	}
	for _, want := range []string{"LOCALSTACK_HOST", "OVERCAST_PORT", "4566", "9000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_localstackHostAlias_bothSameValueIsFine verifies OVERCAST_HOSTNAME
// and LOCALSTACK_HOST both set to the same hostname is accepted (the shape a
// compose file migrated line-by-line from LocalStack, rather than cleaned
// up, naturally produces) — the alias source is still recorded, since
// LOCALSTACK_HOST was recognised and confirmed, not merely ignored.
func TestLoad_localstackHostAlias_bothSameValueIsFine(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "overcast.internal")
	t.Setenv("LOCALSTACK_HOST", "overcast.internal")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "overcast.internal" {
		t.Errorf("Hostname: expected overcast.internal, got %q", cfg.Hostname)
	}
	if cfg.HostnameAliasSource != "LOCALSTACK_HOST" {
		t.Errorf("HostnameAliasSource: expected LOCALSTACK_HOST, got %q", cfg.HostnameAliasSource)
	}
}

// TestLoad_localstackHostAlias_conflictFails verifies OVERCAST_HOSTNAME and
// LOCALSTACK_HOST set to different hostnames fails startup naming both,
// rather than silently preferring one — the same uniform conflict rule
// resolveListen applies to a leftover OVERCAST_HOST.
func TestLoad_localstackHostAlias_conflictFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "overcast.internal")
	t.Setenv("LOCALSTACK_HOST", "localhost.localstack.cloud")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when OVERCAST_HOSTNAME and LOCALSTACK_HOST disagree, got none")
	}
	for _, want := range []string{"OVERCAST_HOSTNAME", "overcast.internal", "LOCALSTACK_HOST", "localhost.localstack.cloud"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_localstackHostAlias_unsetLeavesHostnameAsBefore verifies that
// when LOCALSTACK_HOST is not set at all, behaviour is unchanged: Hostname
// comes from OVERCAST_HOSTNAME alone (or defaults empty), and
// HostnameAliasSource stays empty — no alias was recognised because none was
// present.
func TestLoad_localstackHostAlias_unsetLeavesHostnameAsBefore(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_HOSTNAME", "overcast.internal")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "overcast.internal" {
		t.Errorf("Hostname: expected overcast.internal, got %q", cfg.Hostname)
	}
	if cfg.HostnameAliasSource != "" {
		t.Errorf("HostnameAliasSource: expected empty when LOCALSTACK_HOST is unset, got %q", cfg.HostnameAliasSource)
	}
}

// ---- LocalStack-compatibility alias audit (#1190, second PR) --------------

// TestLoad_edgePortAlias verifies LocalStack's EDGE_PORT is accepted as a
// direct alias for OVERCAST_PORT, and is recorded in LocalStackAliasesUsed.
func TestLoad_edgePortAlias(t *testing.T) {
	clearEnv(t)
	t.Setenv("EDGE_PORT", "9999")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port: expected 9999, got %d", cfg.Port)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_PORT"]; got != "EDGE_PORT" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_PORT]: expected EDGE_PORT, got %q", got)
	}
}

// TestLoad_edgePortAlias_conflictFails verifies EDGE_PORT disagreeing with
// an explicit OVERCAST_PORT fails startup naming both.
func TestLoad_edgePortAlias_conflictFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("EDGE_PORT", "9999")
	t.Setenv("OVERCAST_PORT", "4566")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when EDGE_PORT disagrees with OVERCAST_PORT, got none")
	}
	for _, want := range []string{"EDGE_PORT", "OVERCAST_PORT", "9999", "4566"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_gatewayListenAlias verifies LocalStack's GATEWAY_LISTEN
// ("<ip>:<port>[,<ip>:<port>...]") maps its addresses onto OVERCAST_LISTEN
// and its (agreeing) port onto OVERCAST_PORT, when OVERCAST_LISTEN itself
// was not set explicitly (so the environment-dependent default is not
// mistaken for an explicit conflict).
func TestLoad_gatewayListenAlias(t *testing.T) {
	clearEnv(t)
	t.Setenv("GATEWAY_LISTEN", "127.0.0.1:4566,172.17.0.1:4566")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"127.0.0.1", "172.17.0.1"}
	if !slices.Equal(cfg.Hosts, want) {
		t.Errorf("Hosts: expected %v, got %v", want, cfg.Hosts)
	}
	if cfg.Port != 4566 {
		t.Errorf("Port: expected 4566, got %d", cfg.Port)
	}
	if cfg.ListenSource != config.ListenSourceExplicit {
		t.Errorf("ListenSource: expected explicit (GATEWAY_LISTEN counts as an explicit setting), got %q", cfg.ListenSource)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_LISTEN"]; got != "GATEWAY_LISTEN" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_LISTEN]: expected GATEWAY_LISTEN, got %q", got)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_PORT"]; got != "GATEWAY_LISTEN" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_PORT]: expected GATEWAY_LISTEN, got %q", got)
	}
}

// TestLoad_gatewayListenAlias_mismatchedPortsFail verifies a GATEWAY_LISTEN
// whose entries name different ports is a documented non-match: there is no
// single OVERCAST_PORT to map it to, so it fails loudly rather than picking
// one port and silently dropping the other bind.
func TestLoad_gatewayListenAlias_mismatchedPortsFail(t *testing.T) {
	clearEnv(t)
	t.Setenv("GATEWAY_LISTEN", "127.0.0.1:4566,0.0.0.0:9999")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when GATEWAY_LISTEN entries disagree on port, got none")
	}
	for _, want := range []string{"GATEWAY_LISTEN", "4566", "9999"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_gatewayListenAlias_conflictsWithExplicitListen verifies
// GATEWAY_LISTEN disagreeing with an explicit OVERCAST_LISTEN fails startup
// naming both.
func TestLoad_gatewayListenAlias_conflictsWithExplicitListen(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_LISTEN", "127.0.0.1")
	t.Setenv("GATEWAY_LISTEN", "0.0.0.0:4566")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when GATEWAY_LISTEN disagrees with OVERCAST_LISTEN, got none")
	}
	for _, want := range []string{"OVERCAST_LISTEN", "GATEWAY_LISTEN", "127.0.0.1", "0.0.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_defaultRegionAlias verifies LocalStack's DEFAULT_REGION is
// accepted as a direct alias for OVERCAST_DEFAULT_REGION.
func TestLoad_defaultRegionAlias(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEFAULT_REGION", "eu-west-1")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("Region: expected eu-west-1, got %q", cfg.Region)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_DEFAULT_REGION"]; got != "DEFAULT_REGION" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_DEFAULT_REGION]: expected DEFAULT_REGION, got %q", got)
	}
}

// TestLoad_accountIDAcceptsValidTwelveDigitOverride verifies a real-shaped
// AWS account ID overrides the default cleanly.
func TestLoad_accountIDAcceptsValidTwelveDigitOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ACCOUNT_ID", "123456789012")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AccountID != "123456789012" {
		t.Errorf("AccountID: expected 123456789012, got %q", cfg.AccountID)
	}
}

// TestLoad_accountIDRejectsNonTwelveDigitValues covers #1478: AWS account
// IDs are always exactly 12 ASCII digits, and a non-conforming
// OVERCAST_ACCOUNT_ID silently poisons every ARN and account-regional S3
// bucket name minted from it. Load must refuse to start rather than accept
// the value, the same way OVERCAST_EKS_MODE and friends already do for their
// own invalid values.
func TestLoad_accountIDRejectsNonTwelveDigitValues(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"too short", "12345"},
		{"too long (13 digits)", "1234567890123"},
		{"alphanumeric", "12345678901a"},
		{"contains hyphens", "1234-5678-901"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_ACCOUNT_ID", tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("expected error for OVERCAST_ACCOUNT_ID=%q, got nil", tc.value)
			}
			if got := err.Error(); !containsAll(got, "OVERCAST_ACCOUNT_ID", "12", "digits") {
				t.Fatalf("unexpected error message: %q", got)
			}
		})
	}
}

// TestLoad_accountIDEmptyAfterSetIsTreatedAsUnset verifies an explicitly set
// but empty OVERCAST_ACCOUNT_ID is indistinguishable from unset — the same
// envOr("...", fallback) behaviour every other variable in this file gets
// (see the state-backend loop's own "An empty value means unset" comment) —
// and so falls back to the default rather than failing validation.
func TestLoad_accountIDEmptyAfterSetIsTreatedAsUnset(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ACCOUNT_ID", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AccountID != "000000000000" {
		t.Errorf("AccountID: expected default 000000000000, got %q", cfg.AccountID)
	}
}

// TestLoad_dataDirAlias verifies LocalStack's DATA_DIR is accepted as a
// direct alias for OVERCAST_DATA_DIR, and that it counts as "explicitly
// configured" for OVERCAST_STATE=auto's detection the same way
// OVERCAST_DATA_DIR itself would.
func TestLoad_dataDirAlias(t *testing.T) {
	// This assumes a build with SQLite support: in a -tags nosqlite build,
	// auto never resolves to hybrid regardless of evidence (see
	// TestResolveAutoState_sqliteUnavailableOverridesEveryEvidenceSignal in
	// state_auto_internal_test.go, which covers that gate directly).
	if !config.SQLiteSupported() {
		t.Skip("hybrid is unavailable in a -tags nosqlite build; the SQLite gate is covered directly in state_auto_internal_test.go")
	}

	// No OVERCAST_DATA_DIR_SOURCE=image marker here: that marker means "this
	// is the Docker image's own baked-in default", which suppresses the
	// explicit-data-dir signal precisely because it is not real user intent
	// (see TestLoad_autoResolvesToHybridWhenDataDirExplicit, which this test
	// mirrors for the DATA_DIR alias instead of OVERCAST_DATA_DIR itself).
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != dir {
		t.Errorf("DataDir: expected %q, got %q", dir, cfg.DataDir)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_DATA_DIR"]; got != "DATA_DIR" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_DATA_DIR]: expected DATA_DIR, got %q", got)
	}
	if cfg.State != config.StateBackendHybrid {
		t.Errorf("State: expected hybrid (DATA_DIR should count as an explicit data dir), got %q", cfg.State)
	}
}

// TestLoad_dataDirAlias_overridesImageBakedDefault verifies the image
// carve-out to the alias conflict rule: the Docker image bakes
// OVERCAST_DATA_DIR=/data as its own default, marked by
// OVERCAST_DATA_DIR_SOURCE=image, and `docker run -e DATA_DIR=/persist`
// against that baked ENV must win instead of failing startup as a
// disagreement — the image's default is not user intent (the drop-in
// LocalStack migration promise, #1190). The alias also counts as an
// explicitly configured data directory for OVERCAST_STATE=auto, marker or
// not.
func TestLoad_dataDirAlias_overridesImageBakedDefault(t *testing.T) {
	if !config.SQLiteSupported() {
		t.Skip("hybrid is unavailable in a -tags nosqlite build; the SQLite gate is covered directly in state_auto_internal_test.go")
	}

	// Given: the image's own baked-in data dir (marked as such), and the user
	// pointing DATA_DIR somewhere else — the two disagree.
	clearEnv(t)
	baked := t.TempDir()
	userDir := t.TempDir()
	t.Setenv("OVERCAST_DATA_DIR", baked)
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")
	t.Setenv("DATA_DIR", userDir)

	// When
	cfg, err := config.Load()

	// Then: no conflict — the alias wins over the image-baked default.
	if err != nil {
		t.Fatalf("Load: expected DATA_DIR to override the image-baked OVERCAST_DATA_DIR, got error: %v", err)
	}
	if cfg.DataDir != userDir {
		t.Errorf("DataDir: expected the alias value %q, got %q", userDir, cfg.DataDir)
	}
	if got := cfg.LocalStackAliasesUsed["OVERCAST_DATA_DIR"]; got != "DATA_DIR" {
		t.Errorf("LocalStackAliasesUsed[OVERCAST_DATA_DIR]: expected DATA_DIR, got %q", got)
	}
	if cfg.State != config.StateBackendHybrid {
		t.Errorf("State: expected hybrid (DATA_DIR is user intent even under the image marker), got %q", cfg.State)
	}
	if cfg.StateAutoSignal != "explicit-data-dir" {
		t.Errorf("StateAutoSignal: expected explicit-data-dir, got %q", cfg.StateAutoSignal)
	}
}

// TestLoad_imageBakedDefaults_aliasOnlyConfigurationWins simulates a
// LocalStack migrator's `docker run` against the image environment: the only
// variables the image bakes (OVERCAST_DATA_DIR plus its source marker — see
// the ENV block in Dockerfile, and TestDockerfileBakesNoConfigDefaults,
// which pins that set), with configuration supplied through LocalStack
// aliases alone. Startup must succeed with every alias taking effect;
// reproduced failing before the fix, when the image also baked
// OVERCAST_PORT/OVERCAST_LISTEN/OVERCAST_LOG_LEVEL/OVERCAST_DEFAULT_REGION
// and each disagreeing alias was a startup conflict.
func TestLoad_imageBakedDefaults_aliasOnlyConfigurationWins(t *testing.T) {
	// Given: the image's baked environment, and LocalStack-style settings only.
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")
	t.Setenv("DEFAULT_REGION", "eu-west-1")
	t.Setenv("EDGE_PORT", "4666")
	t.Setenv("DEBUG", "1")

	// When
	cfg, err := config.Load()

	// Then: startup succeeds and every alias took effect.
	if err != nil {
		t.Fatalf("Load: expected alias-only configuration to work against the image environment, got error: %v", err)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("Region: expected eu-west-1 (DEFAULT_REGION), got %q", cfg.Region)
	}
	if cfg.Port != 4666 {
		t.Errorf("Port: expected 4666 (EDGE_PORT), got %d", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: expected debug (DEBUG=1), got %q", cfg.LogLevel)
	}
}

// TestLoad_debugAlias verifies LocalStack's DEBUG=1 maps to
// OVERCAST_LOG_LEVEL=debug, and that DEBUG=0 is a no-op (behaves as unset)
// rather than forcing any particular log level.
func TestLoad_debugAlias(t *testing.T) {
	for _, tc := range []struct {
		name       string
		debugValue string
		wantLevel  string
		wantAlias  string
	}{
		{name: "DEBUG=1", debugValue: "1", wantLevel: "debug", wantAlias: "DEBUG"},
		{name: "DEBUG=0 is a no-op", debugValue: "0", wantLevel: "info", wantAlias: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DEBUG", tc.debugValue)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.LogLevel != tc.wantLevel {
				t.Errorf("LogLevel: expected %q, got %q", tc.wantLevel, cfg.LogLevel)
			}
			if got := cfg.LocalStackAliasesUsed["OVERCAST_LOG_LEVEL"]; got != tc.wantAlias {
				t.Errorf("LocalStackAliasesUsed[OVERCAST_LOG_LEVEL]: expected %q, got %q", tc.wantAlias, got)
			}
		})
	}
}

// TestLoad_debugAlias_conflictFails verifies DEBUG=1 disagreeing with an
// explicit, non-debug OVERCAST_LOG_LEVEL fails startup naming both.
func TestLoad_debugAlias_conflictFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("DEBUG", "1")
	t.Setenv("OVERCAST_LOG_LEVEL", "warn")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when DEBUG=1 disagrees with an explicit OVERCAST_LOG_LEVEL, got none")
	}
	for _, want := range []string{"DEBUG", "OVERCAST_LOG_LEVEL", "warn", "debug"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_persistenceAlias verifies LocalStack's PERSISTENCE=1 maps to
// OVERCAST_STATE=persistent, and that PERSISTENCE=0 is a no-op.
func TestLoad_persistenceAlias(t *testing.T) {
	for _, tc := range []struct {
		name           string
		persistenceVal string
		wantState      config.StateBackend
		wantAlias      string
	}{
		{name: "PERSISTENCE=1", persistenceVal: "1", wantState: config.StateBackendPersistent, wantAlias: "PERSISTENCE"},
		{name: "PERSISTENCE=0 is a no-op", persistenceVal: "0", wantState: config.StateBackendMemory, wantAlias: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("PERSISTENCE", tc.persistenceVal)
			t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
			t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.State != tc.wantState {
				t.Errorf("State: expected %q, got %q", tc.wantState, cfg.State)
			}
			if got := cfg.LocalStackAliasesUsed["OVERCAST_STATE"]; got != tc.wantAlias {
				t.Errorf("LocalStackAliasesUsed[OVERCAST_STATE]: expected %q, got %q", tc.wantAlias, got)
			}
		})
	}
}

// TestLoad_persistenceAlias_conflictFails verifies PERSISTENCE=1 disagreeing
// with an explicit, non-persistent OVERCAST_STATE fails startup naming both.
func TestLoad_persistenceAlias_conflictFails(t *testing.T) {
	clearEnv(t)
	t.Setenv("PERSISTENCE", "1")
	t.Setenv("OVERCAST_STATE", "memory")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when PERSISTENCE=1 disagrees with an explicit OVERCAST_STATE, got none")
	}
	for _, want := range []string{"PERSISTENCE", "OVERCAST_STATE", "memory", "persistent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_hostnameExternalAlias verifies the legacy LocalStack name
// HOSTNAME_EXTERNAL (which LOCALSTACK_HOST replaced) is also accepted as an
// alias for OVERCAST_HOSTNAME, chained after LOCALSTACK_HOST.
func TestLoad_hostnameExternalAlias(t *testing.T) {
	clearEnv(t)
	t.Setenv("HOSTNAME_EXTERNAL", "legacy.internal")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Hostname != "legacy.internal" {
		t.Errorf("Hostname: expected legacy.internal, got %q", cfg.Hostname)
	}
	if cfg.HostnameAliasSource != "HOSTNAME_EXTERNAL" {
		t.Errorf("HostnameAliasSource: expected HOSTNAME_EXTERNAL, got %q", cfg.HostnameAliasSource)
	}
}

// TestLoad_hostnameExternalAlias_conflictsWithLocalstackHost verifies all
// three hostname spellings must agree: HOSTNAME_EXTERNAL disagreeing with
// LOCALSTACK_HOST fails startup naming both, even though OVERCAST_HOSTNAME
// itself was never set.
func TestLoad_hostnameExternalAlias_conflictsWithLocalstackHost(t *testing.T) {
	clearEnv(t)
	t.Setenv("LOCALSTACK_HOST", "localhost.localstack.cloud")
	t.Setenv("HOSTNAME_EXTERNAL", "legacy.internal")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected an error when HOSTNAME_EXTERNAL disagrees with LOCALSTACK_HOST, got none")
	}
	for _, want := range []string{"LOCALSTACK_HOST", "HOSTNAME_EXTERNAL", "localhost.localstack.cloud", "legacy.internal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestLoad_lambdaInitTimeoutAlias verifies LocalStack's
// LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT is accepted as an alias for
// LAMBDA_INIT_TIMEOUT_SECONDS — the same concept (seconds to wait for the
// Lambda runtime environment to start up) under a different name.
func TestLoad_lambdaInitTimeoutAlias(t *testing.T) {
	clearEnv(t)
	t.Setenv("LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT", "17")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LambdaInitTimeout != 17*time.Second {
		t.Errorf("LambdaInitTimeout: expected 17s, got %s", cfg.LambdaInitTimeout)
	}
	if got := cfg.LocalStackAliasesUsed["LAMBDA_INIT_TIMEOUT_SECONDS"]; got != "LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT" {
		t.Errorf("LocalStackAliasesUsed[LAMBDA_INIT_TIMEOUT_SECONDS]: expected LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT, got %q", got)
	}
}

// TestLoad_ignoredLocalStackVars verifies SERVICES, LOCALSTACK_API_KEY, and
// LOCALSTACK_AUTH_TOKEN are recognised-but-inert: present in
// IgnoredLocalStackVars (so a startup log line can say they were seen), but
// never rejected and never given any functional effect.
func TestLoad_ignoredLocalStackVars(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVICES", "s3,sqs,dynamodb")
	t.Setenv("LOCALSTACK_API_KEY", "test-key")
	t.Setenv("LOCALSTACK_AUTH_TOKEN", "test-token")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"SERVICES", "LOCALSTACK_API_KEY", "LOCALSTACK_AUTH_TOKEN"}
	if !slices.Equal(cfg.IgnoredLocalStackVars, want) {
		t.Errorf("IgnoredLocalStackVars: expected %v, got %v", want, cfg.IgnoredLocalStackVars)
	}
}

// TestLoad_ignoredLocalStackVarsUnset verifies the default is an empty list,
// not a false positive when none of the ignored variables are set.
func TestLoad_ignoredLocalStackVarsUnset(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.IgnoredLocalStackVars) != 0 {
		t.Errorf("IgnoredLocalStackVars: expected none, got %v", cfg.IgnoredLocalStackVars)
	}
}

func TestLoad_eksModeDefault(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EKSMode != config.EKSModeMock {
		t.Fatalf("EKSMode: expected %q, got %q", config.EKSModeMock, cfg.EKSMode)
	}
}

func TestLoad_eksModeLive(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_EKS_MODE", "live")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EKSMode != config.EKSModeLive {
		t.Fatalf("EKSMode: expected %q, got %q", config.EKSModeLive, cfg.EKSMode)
	}
}

func TestLoad_eksModeRejectsInvalidValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_EKS_MODE", "sidecar")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid OVERCAST_EKS_MODE, got nil")
	}
	if got := err.Error(); got == "" || !containsAll(got, "OVERCAST_EKS_MODE", "mock", "live") {
		t.Fatalf("unexpected error message: %q", got)
	}
}

// EFS defaults to live because live mode costs nothing without Docker: it
// creates volumes only while a daemon is reachable and otherwise behaves
// exactly like mock. Defaulting to mock meant a file system silently had no
// storage behind it on a machine that could have backed it for free.
func TestLoad_efsModeDefaultsToLive(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EFSMode != config.EFSModeLive {
		t.Fatalf("EFSMode: expected %q, got %q", config.EFSModeLive, cfg.EFSMode)
	}
}

func TestLoad_efsModeLive(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_EFS_MODE", "live")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EFSMode != config.EFSModeLive {
		t.Fatalf("EFSMode: expected %q, got %q", config.EFSModeLive, cfg.EFSMode)
	}
}

// Mock stays reachable as an explicit opt-out for anyone who wants EFS to keep
// its hands off Docker entirely.
func TestLoad_efsModeMock(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_EFS_MODE", "mock")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EFSMode != config.EFSModeMock {
		t.Fatalf("EFSMode: expected %q, got %q", config.EFSModeMock, cfg.EFSMode)
	}
}

// RDS defaults to live for the same reason EFS does: a DB instance nothing
// answers on is not much of a database, and live mode degrades to metadata-only
// by itself when no daemon is reachable.
func TestLoad_rdsModeDefaultsToLive(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RDSMode != config.RDSModeLive {
		t.Fatalf("RDSMode: expected %q, got %q", config.RDSModeLive, cfg.RDSMode)
	}
}

// Mock is the opt-out for an environment that has a daemon but cannot afford
// the tens of seconds a real engine takes to accept its first connection.
func TestLoad_rdsModeMock(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_RDS_MODE", "mock")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RDSMode != config.RDSModeMock {
		t.Fatalf("RDSMode: expected %q, got %q", config.RDSModeMock, cfg.RDSMode)
	}
}

func TestLoad_rdsModeRejectsInvalidValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_RDS_MODE", "metadata")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid OVERCAST_RDS_MODE, got nil")
	}
	if got := err.Error(); got == "" || !containsAll(got, "OVERCAST_RDS_MODE", "mock", "live") {
		t.Fatalf("unexpected error message: %q", got)
	}
}

// Service metrics collection defaults to auto, and MetricsCollectionEnabled
// reports true for it since CloudWatch is always wired (OVERCAST_SERVICES no
// longer gates services) — see config.ServiceMetricsAuto's doc comment.
func TestLoad_serviceMetricsDefaultsToAuto(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServiceMetrics != config.ServiceMetricsAuto {
		t.Fatalf("ServiceMetrics: expected %q, got %q", config.ServiceMetricsAuto, cfg.ServiceMetrics)
	}
	if !cfg.MetricsCollectionEnabled() {
		t.Fatal("expected MetricsCollectionEnabled() to be true under the default auto mode")
	}
}

func TestLoad_serviceMetricsDisabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_SERVICE_METRICS", "disabled")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ServiceMetrics != config.ServiceMetricsDisabled {
		t.Fatalf("ServiceMetrics: expected %q, got %q", config.ServiceMetricsDisabled, cfg.ServiceMetrics)
	}
	if cfg.MetricsCollectionEnabled() {
		t.Fatal("expected MetricsCollectionEnabled() to be false when explicitly disabled")
	}
}

func TestLoad_serviceMetricsRejectsInvalidValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_SERVICE_METRICS", "sometimes")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid OVERCAST_SERVICE_METRICS, got nil")
	}
	if got := err.Error(); got == "" || !containsAll(got, "OVERCAST_SERVICE_METRICS", "auto", "enabled", "disabled") {
		t.Fatalf("unexpected error message: %q", got)
	}
}

// OVERCAST_CONTROL_PLANE_INTERNAL exists so that a pinned Overcast version
// gives the same control-plane isolation on every machine (#1564). The default
// has to stay the host probe, so that unset means auto — anything else would
// change behaviour for everyone who never sets it.
func TestLoad_controlPlaneInternalDefaultsToAuto(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlPlaneInternal != config.ControlPlaneInternalAuto {
		t.Fatalf("ControlPlaneInternal: expected %q, got %q",
			config.ControlPlaneInternalAuto, cfg.ControlPlaneInternal)
	}
}

func TestLoad_controlPlaneInternalAcceptsEachValue(t *testing.T) {
	// Case and surrounding whitespace are tolerated the way every other
	// enum-valued variable tolerates them — a compose file's `"True"` is a
	// pin, not a typo.
	for raw, want := range map[string]config.ControlPlaneInternalMode{
		"auto":   config.ControlPlaneInternalAuto,
		"true":   config.ControlPlaneInternalTrue,
		"false":  config.ControlPlaneInternalFalse,
		" True ": config.ControlPlaneInternalTrue,
		"FALSE":  config.ControlPlaneInternalFalse,
	} {
		t.Run(raw, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_CONTROL_PLANE_INTERNAL", raw)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.ControlPlaneInternal != want {
				t.Fatalf("ControlPlaneInternal: expected %q, got %q", want, cfg.ControlPlaneInternal)
			}
		})
	}
}

// A typo must not fall back to auto. Silently keeping the inferred behaviour
// is precisely the surprise this variable was added to end, and it would be
// worse here — the operator believes they pinned it.
func TestLoad_controlPlaneInternalRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"yes", "1", "internal", "off"} {
		t.Run(raw, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_CONTROL_PLANE_INTERNAL", raw)

			_, err := config.Load()
			if err == nil {
				t.Fatal("expected error for invalid OVERCAST_CONTROL_PLANE_INTERNAL, got nil")
			}
			if got := err.Error(); !containsAll(got, "OVERCAST_CONTROL_PLANE_INTERNAL", "auto", "true", "false") {
				t.Fatalf("unexpected error message: %q", got)
			}
		})
	}
}

// The default is `open`: every container Overcast starts reaches the internet.
// That is what every comparable emulator does and what the common hybrid case
// needs, so it must hold for everyone who never sets the variable.
func TestLoad_vpcEgressDefaultsToOpen(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VPCEgress != config.VPCEgressOpen {
		t.Fatalf("VPCEgress: expected %q, got %q", config.VPCEgressOpen, cfg.VPCEgress)
	}
}

func TestLoad_vpcEgressAcceptsEachImplementedMode(t *testing.T) {
	// Case and surrounding whitespace are tolerated the way every other
	// enum-valued variable tolerates them.
	for raw, want := range map[string]config.VPCEgressMode{
		"open":   config.VPCEgressOpen,
		"none":   config.VPCEgressNone,
		" Open ": config.VPCEgressOpen,
		"NONE":   config.VPCEgressNone,
	} {
		t.Run(raw, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_VPC_EGRESS", raw)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.VPCEgress != want {
				t.Fatalf("VPCEgress: expected %q, got %q", want, cfg.VPCEgress)
			}
		})
	}
}

// `routed` is the third mode (#1571): accepted at load like the other two, with
// the egress-network pool it carves from defaulted beside it.
func TestLoad_vpcEgressRoutedIsAccepted(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_VPC_EGRESS", "routed")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("OVERCAST_VPC_EGRESS=routed: %v", err)
	}
	if cfg.VPCEgress != config.VPCEgressRouted {
		t.Fatalf("VPCEgress = %q, want routed", cfg.VPCEgress)
	}
	if cfg.VPCEgressPool != config.DefaultVPCEgressPool {
		t.Fatalf("VPCEgressPool = %q, want the default %q", cfg.VPCEgressPool, config.DefaultVPCEgressPool)
	}
	if got := cfg.VPCEgressNetwork("vpc-1"); got != "overcast-vpc-vpc-1-egress" {
		t.Fatalf("VPCEgressNetwork = %q", got)
	}
}

// The pool is validated whatever the mode, so a malformed one is not
// discovered on the day `routed` is switched on. It has to hold at least one
// /24, and a range wider than /8 is a typo.
func TestLoad_vpcEgressPoolIsValidated(t *testing.T) {
	for _, tc := range []struct {
		pool string
		ok   bool
	}{
		{"198.18.0.0/16", true},
		{"10.200.0.0/24", true},
		{"172.16.0.0/12", true},
		{"198.18.0.0/25", false},
		{"fd00::/64", false},
		{"0.0.0.0/0", false},
		{"not-a-cidr", false},
	} {
		t.Run(tc.pool, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_VPC_EGRESS_POOL", tc.pool)

			cfg, err := config.Load()
			if tc.ok {
				if err != nil {
					t.Fatalf("expected %q to be accepted: %v", tc.pool, err)
				}
				if cfg.VPCEgressPool != tc.pool {
					t.Fatalf("VPCEgressPool = %q, want %q", cfg.VPCEgressPool, tc.pool)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %q to be rejected", tc.pool)
			}
			if !containsAll(err.Error(), "OVERCAST_VPC_EGRESS_POOL", tc.pool) {
				t.Fatalf("unexpected error message: %q", err)
			}
		})
	}
}

// A typo must not fall back to the default. This setting decides whether a
// function reaches real AWS, and quietly restoring the default answer is the
// class of surprise it exists to end.
func TestLoad_vpcEgressRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"yes", "internal", "off", "true"} {
		t.Run(raw, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_VPC_EGRESS", raw)

			_, err := config.Load()
			if err == nil {
				t.Fatal("expected error for invalid OVERCAST_VPC_EGRESS, got nil")
			}
			if got := err.Error(); !containsAll(got, "OVERCAST_VPC_EGRESS", "open", "routed", "none") {
				t.Fatalf("unexpected error message: %q", got)
			}
		})
	}
}

// The deprecation notice must fire only for the operator who actually set the
// variable. "auto" is both the default and a legitimate explicit value, and
// they are indistinguishable from the value alone — so Load records which it
// was, or every default installation gets warned about a variable it never used.
func TestLoad_controlPlaneInternalRecordsWhetherItWasSet(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlPlaneInternalSet {
		t.Error("ControlPlaneInternalSet is true with the variable unset")
	}

	clearEnv(t)
	t.Setenv("OVERCAST_CONTROL_PLANE_INTERNAL", "auto")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.ControlPlaneInternalSet {
		t.Error("ControlPlaneInternalSet is false with the variable explicitly set to auto")
	}
}

// The network name is the per-instance namespace, and every network Overcast
// creates has to be derivable from it — including the per-VPC ones, which used
// a hard-coded `overcast-vpc-` prefix and so collided across instances. The
// default has to reproduce the historical name exactly, or an existing
// installation stops adopting the networks it already has.
func TestConfig_vpcNetworkNamesFollowTheNetworkPrefix(t *testing.T) {
	clearEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.VPCNetwork("vpc-123"); got != "overcast-vpc-vpc-123" {
		t.Errorf("VPCNetwork = %q, want the historical default name", got)
	}

	clearEnv(t)
	t.Setenv("OVERCAST_NETWORK", "ocalt")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.VPCNetwork("vpc-123"); got != "ocalt-vpc-vpc-123" {
		t.Errorf("VPCNetwork = %q, want it under the configured prefix", got)
	}
	if got := cfg.VPCNetworkPrefix(); got != "ocalt-vpc-" {
		t.Errorf("VPCNetworkPrefix = %q, want %q", got, "ocalt-vpc-")
	}
}

func TestLoad_efsModeRejectsInvalidValues(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_EFS_MODE", "nfs")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid OVERCAST_EFS_MODE, got nil")
	}
	if got := err.Error(); got == "" || !containsAll(got, "OVERCAST_EFS_MODE", "mock", "live") {
		t.Fatalf("unexpected error message: %q", got)
	}
}

func TestLoad_eksDockerDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("LAMBDA_DOCKER_SOCKET", "tcp://dind:2375")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EKSDockerSocket != "tcp://dind:2375" {
		t.Fatalf("EKSDockerSocket: expected tcp://dind:2375, got %q", cfg.EKSDockerSocket)
	}
}

func TestLoad_eksDockerOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("EKS_DOCKER_SOCKET", "tcp://eksdind:2375")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EKSDockerSocket != "tcp://eksdind:2375" {
		t.Fatalf("EKSDockerSocket: expected tcp://eksdind:2375, got %q", cfg.EKSDockerSocket)
	}
}

// Every container Overcast starts shares two networks rather than one per
// service, so there is one knob and the control plane is derived from it —
// there is no way to configure half a pair. See internal/dataplane.
func TestLoad_networkPlanes(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Network != "overcast" {
		t.Fatalf("Network: expected overcast, got %q", cfg.Network)
	}
	if got := cfg.ControlNetwork(); got != "overcast_control" {
		t.Fatalf("ControlNetwork: expected overcast_control, got %q", got)
	}
}

func TestLoad_networkPlanesOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_NETWORK", "acme")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Network != "acme" {
		t.Fatalf("Network: expected acme, got %q", cfg.Network)
	}
	if got := cfg.ControlNetwork(); got != "acme_control" {
		t.Fatalf("ControlNetwork: expected acme_control, got %q", got)
	}
}

// TestLoad_invalidShutdownTimeout verifies malformed durations are rejected.
func TestLoad_invalidShutdownTimeout(t *testing.T) {
	// Given: OVERCAST_SHUTDOWN_TIMEOUT is not a valid duration
	clearEnv(t)
	t.Setenv("OVERCAST_SHUTDOWN_TIMEOUT", "notaduration")

	// When + Then
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid shutdown timeout, got nil")
	}
}

// TestLoad_boolVariants verifies all accepted truthy/falsy values for bool vars.
func TestLoad_boolVariants(t *testing.T) {
	truthy := []string{"true", "1", "yes"}
	falsy := []string{"false", "0", "no"}

	for _, v := range truthy {
		t.Run("debug="+v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_DEBUG", v)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.Debug {
				t.Errorf("expected Debug=true for value %q", v)
			}
		})
	}

	for _, v := range falsy {
		t.Run("debug="+v, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OVERCAST_DEBUG", v)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Debug {
				t.Errorf("expected Debug=false for value %q", v)
			}
		})
	}
}

// TestLoad_ec2VPCStrategyNetnsRejected verifies netns strategy fails fast
// during configuration load with a clear remediation hint.
func TestLoad_ec2VPCStrategyNetnsRejected(t *testing.T) {
	// Given: netns is explicitly selected
	clearEnv(t)
	t.Setenv("OVERCAST_EC2_VPC_STRATEGY", "netns")

	// When: config is loaded
	_, err := config.Load()

	// Then: startup is rejected with guidance
	if err == nil {
		t.Fatal("expected error for OVERCAST_EC2_VPC_STRATEGY=netns, got nil")
	}
	if got := err.Error(); got == "" || !containsAll(got, "OVERCAST_EC2_VPC_STRATEGY", "netns", "shared", "strict", "remapped") {
		t.Fatalf("unexpected error message: %q", got)
	}
}

func TestLoad_mcpRemoteExposureRequiresAuthToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_MCP_REMOTE_EXPOSURE", "true")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error when OVERCAST_MCP_REMOTE_EXPOSURE=true without token")
	}
}

func TestLoad_mcpRemoteExposureWithAuthToken(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_MCP_REMOTE_EXPOSURE", "true")
	t.Setenv("OVERCAST_MCP_AUTH_TOKEN", "test-token")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.MCPRemoteExposure {
		t.Fatal("expected MCPRemoteExposure=true")
	}
	if cfg.MCPAuthToken != "test-token" {
		t.Fatalf("MCPAuthToken = %q, want test-token", cfg.MCPAuthToken)
	}
}

func TestLoad_enforceIAMEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ENFORCE_IAM", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EnforceIAM {
		t.Fatal("expected EnforceIAM=true")
	}
}

func TestLoad_enforceAPIGatewayThrottleEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ENFORCE_APIGATEWAY_THROTTLE", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EnforceAPIGatewayThrottle {
		t.Fatal("expected EnforceAPIGatewayThrottle=true")
	}
}
func TestLoad_enforceAppSyncCognitoAuthEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EnforceAppSyncCognitoAuth {
		t.Fatal("expected EnforceAppSyncCognitoAuth=true")
	}
}

func TestLoad_enforceLambdaResourcePolicyEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.EnforceLambdaResourcePolicy {
		t.Fatal("expected EnforceLambdaResourcePolicy=true")
	}
}

func TestLoad_protocolStrictEnabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_PROTOCOL_STRICT", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.ProtocolStrict {
		t.Fatal("expected ProtocolStrict=true")
	}
}

// TestLoad_dockerHost covers the end-to-end wiring of DOCKER_HOST into the
// Docker endpoint: it supplies the default, LAMBDA_DOCKER_SOCKET still wins,
// and provenance is recorded either way so startup can say which one spoke.
func TestLoad_dockerHost(t *testing.T) {
	t.Run("supplies the endpoint when LAMBDA_DOCKER_SOCKET is unset", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("DOCKER_HOST", "unix:///home/dev/.colima/default/docker.sock")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaDockerSocket != "/home/dev/.colima/default/docker.sock" {
			t.Fatalf("LambdaDockerSocket = %q, want the DOCKER_HOST socket path", cfg.LambdaDockerSocket)
		}
		if cfg.DockerSocketSource != "DOCKER_HOST" {
			t.Fatalf("DockerSocketSource = %q, want DOCKER_HOST", cfg.DockerSocketSource)
		}
	})

	t.Run("LAMBDA_DOCKER_SOCKET wins and claims no DOCKER_HOST provenance", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("DOCKER_HOST", "tcp://elsewhere:2375")
		t.Setenv("LAMBDA_DOCKER_SOCKET", "/var/run/docker.sock")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaDockerSocket != "/var/run/docker.sock" {
			t.Fatalf("LambdaDockerSocket = %q, want the explicit LAMBDA_DOCKER_SOCKET", cfg.LambdaDockerSocket)
		}
		if cfg.DockerSocketSource != "" {
			t.Fatalf("DockerSocketSource = %q, want empty — DOCKER_HOST did not supply it", cfg.DockerSocketSource)
		}
	})

	t.Run("an undialable transport is recorded, not silently swallowed", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("DOCKER_HOST", "ssh://dev@build-host")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaDockerSocket != config.DefaultDockerSocket() {
			t.Fatalf("LambdaDockerSocket = %q, want the platform default", cfg.LambdaDockerSocket)
		}
		if cfg.DockerHostUnsupported != "ssh://dev@build-host" {
			t.Fatalf("DockerHostUnsupported = %q, want the raw value so startup can warn", cfg.DockerHostUnsupported)
		}
	})

	t.Run("unset keeps the platform default", func(t *testing.T) {
		clearEnv(t)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaDockerSocket != config.DefaultDockerSocket() {
			t.Fatalf("LambdaDockerSocket = %q, want the platform default %q",
				cfg.LambdaDockerSocket, config.DefaultDockerSocket())
		}
		if cfg.DockerSocketSource != "" || cfg.DockerHostUnsupported != "" {
			t.Fatalf("provenance = (%q, %q), want both empty",
				cfg.DockerSocketSource, cfg.DockerHostUnsupported)
		}
	})
}

// TestLoad_localStackVolumeUnmounted is the half of the /var/lib/localstack
// adoption that holds on every platform: with nothing mounted anywhere, a
// containerised run keeps the image's own /data and reports no adoption.
// The mount-present cases are covered exhaustively, and platform-independently,
// by TestAdoptLocalStackVolume.
func TestLoad_localStackVolumeUnmounted(t *testing.T) {
	clearEnv(t)
	t.Setenv("OVERCAST_DATA_DIR", t.TempDir())
	t.Setenv("OVERCAST_DATA_DIR_SOURCE", "image")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LocalStackVolumeDataDir != "" {
		t.Fatalf("LocalStackVolumeDataDir = %q, want empty — nothing is mounted at /var/lib/localstack",
			cfg.LocalStackVolumeDataDir)
	}
}

func TestLoad_protocolStrictDisabledByDefault(t *testing.T) {
	clearEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ProtocolStrict {
		t.Fatal("expected ProtocolStrict=false by default")
	}
}

// ---- Helpers ---------------------------------------------------------------

// clearEnv unsets all OVERCAST_* environment variables for test isolation.
// t.Setenv is used for any variables needed in a test — it auto-restores on cleanup.
func clearEnv(t *testing.T) {
	t.Helper()
	awsEmuVars := []string{
		"OVERCAST_HOST", "OVERCAST_LISTEN", "OVERCAST_PORT", "OVERCAST_SERVICES", "OVERCAST_STATE",
		"OVERCAST_HYBRID_FLUSH_INTERVAL", "OVERCAST_HYBRID_SYNC", "OVERCAST_HYBRID_SYNC_INTERVAL",
		"OVERCAST_HYBRID_DIRTY_ENTRY_THRESHOLD", "OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD",
		"OVERCAST_WAL_FSYNC", "OVERCAST_WAL_FSYNC_INTERVAL", "OVERCAST_WAL_MAX_LOG_BYTES",
		"OVERCAST_DATA_DIR", "OVERCAST_DATA_DIR_SOURCE", "OVERCAST_DEFAULT_REGION", "OVERCAST_ACCOUNT_ID",
		"OVERCAST_SIGV4_VALIDATE", "OVERCAST_ENFORCE_IAM", "OVERCAST_ENFORCE_APIGATEWAY_THROTTLE",
		"OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY",
		"OVERCAST_PROTOCOL_STRICT", "OVERCAST_LOG_LEVEL", "OVERCAST_SHUTDOWN_TIMEOUT",
		"OVERCAST_LAMBDA_HOT_RELOAD",
		"OVERCAST_DEBUG", "OVERCAST_TLS", "OVERCAST_TLS_CERT", "OVERCAST_TLS_KEY",
		"OVERCAST_HOSTNAME", "LOCALSTACK_HOST", "HOSTNAME_EXTERNAL", "OVERCAST_SPLIT_HORIZON_HOSTS", "OVERCAST_EKS_MODE", "OVERCAST_EC2_VPC_STRATEGY",
		// LocalStack-compatibility aliases (#1190, second PR).
		"EDGE_PORT", "GATEWAY_LISTEN", "DEFAULT_REGION", "DATA_DIR", "DEBUG", "PERSISTENCE",
		"LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT", "SERVICES", "LOCALSTACK_API_KEY", "LOCALSTACK_AUTH_TOKEN",
		// LocalStack-compatibility aliases and the recognised-but-inert table
		// (drop-in audit). Every one of these has to be isolated: several are
		// plausible things to have exported in a shell that also runs
		// LocalStack, and each would silently change a default asserted below.
		"LS_LOG", "ENFORCE_IAM", "LAMBDA_REMOVE_CONTAINERS", "DNS_ADDRESS", "OVERCAST_DNS",
		"LAMBDA_KEEP_CONTAINERS", "EAGER_SERVICE_LOADING", "ACTIVATE_PRO", "MAIN_CONTAINER_NAME",
		"DISABLE_EVENTS", "SKIP_SSL_CERT_DOWNLOAD", "DISABLE_CORS_CHECKS", "DISABLE_CORS_HEADERS",
		"EXTRA_CORS_ALLOWED_ORIGINS", "EXTRA_CORS_ALLOWED_HEADERS", "SQS_ENDPOINT_STRATEGY",
		"S3_SKIP_SIGNATURE_VALIDATION", "IAM_SOFT_MODE", "LAMBDA_KEEPALIVE_MS",
		"LAMBDA_DOCKER_NETWORK", "MAIN_DOCKER_NETWORK", "LAMBDA_DOCKER_FLAGS", "LAMBDA_RUNTIME_EXECUTOR",
		"SNAPSHOT_SAVE_STRATEGY", "SNAPSHOT_LOAD_STRATEGY", "SNAPSHOT_FLUSH_INTERVAL",
		"ALLOW_NONSTANDARD_REGIONS", "ENABLE_CONFIG_UPDATES",
		"PROVIDER_OVERRIDE_APIGATEWAY", "PROVIDER_OVERRIDE_CLOUDWATCH",
		// The mode defaults these assert are only defaults if the developer
		// running the suite has not exported an opt-out of their own.
		"OVERCAST_EFS_MODE", "OVERCAST_RDS_MODE", "OVERCAST_SERVICE_METRICS",
		"OVERCAST_MCP_REMOTE_EXPOSURE", "OVERCAST_MCP_AUTH_TOKEN",
		"EKS_DOCKER_SOCKET", "OVERCAST_NETWORK", "OVERCAST_CONTROL_PLANE_INTERNAL",
		// The Docker endpoint has two sources to isolate, not one: a
		// developer running Colima or Rancher Desktop exports DOCKER_HOST,
		// and without this the platform-default assertion below would fail on
		// their machine and nowhere else.
		"DOCKER_HOST", "LAMBDA_DOCKER_SOCKET",
		"LAMBDA_RUNTIME_API_PORT", "OVERCAST_SMTP_PORT",
		"OVERCAST_DEBUGGER", "OVERCAST_LAMBDA_DEBUGGER", "OVERCAST_ECS_DEBUGGER",
		"OVERCAST_DEBUGGER_LISTEN", "OVERCAST_DEBUGGER_PORTS", "OVERCAST_DEBUGGER_TIMEOUT",
		"OVERCAST_DEBUGGER_WAIT_TIMEOUT",
	}
	for _, v := range awsEmuVars {
		original := os.Getenv(v)
		os.Unsetenv(v)
		t.Cleanup(func() {
			if original != "" {
				os.Setenv(v, original)
			} else {
				os.Unsetenv(v)
			}
		})
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// TestLoad_lambdaRuntimeAPIPort covers LAMBDA_RUNTIME_API_PORT: the default,
// 0 as a real value (an ephemeral port — no container is ever told the shared
// port), and a port that cannot exist failing at startup instead of surfacing
// later as a bind failure that silently disables the container runtime.
func TestLoad_lambdaRuntimeAPIPort(t *testing.T) {
	t.Run("defaults to 9001", func(t *testing.T) {
		// Given: the variable is unset
		clearEnv(t)

		// When: we load config
		cfg, err := config.Load()

		// Then: the documented default applies
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaRuntimeAPIPort != config.DefaultLambdaRuntimeAPIPort {
			t.Fatalf("LambdaRuntimeAPIPort = %d, want %d", cfg.LambdaRuntimeAPIPort, config.DefaultLambdaRuntimeAPIPort)
		}
	})

	t.Run("0 means an ephemeral port", func(t *testing.T) {
		// Given: LAMBDA_RUNTIME_API_PORT=0
		clearEnv(t)
		t.Setenv("LAMBDA_RUNTIME_API_PORT", "0")

		// When: we load config
		cfg, err := config.Load()

		// Then: 0 is kept, not replaced by the default
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.LambdaRuntimeAPIPort != 0 {
			t.Fatalf("LambdaRuntimeAPIPort = %d, want 0", cfg.LambdaRuntimeAPIPort)
		}
	})

	t.Run("a port that cannot exist fails at startup naming the variable", func(t *testing.T) {
		// Given: a value outside the port range
		clearEnv(t)
		t.Setenv("LAMBDA_RUNTIME_API_PORT", "65536")

		// When: we load config
		_, err := config.Load()

		// Then: startup fails, and the error says which variable to fix
		if err == nil {
			t.Fatal("Load: expected an error for port 65536")
		}
		if !strings.Contains(err.Error(), "LAMBDA_RUNTIME_API_PORT") {
			t.Fatalf("error %q does not name LAMBDA_RUNTIME_API_PORT", err)
		}
	})
}

// TestLoad_smtpPort covers OVERCAST_SMTP_PORT: the default, 0 as a real value
// (an ephemeral port, which Inbox capture follows because the mailer learns
// the bound address), and values that cannot be a port.
func TestLoad_smtpPort(t *testing.T) {
	t.Run("defaults to 1025", func(t *testing.T) {
		// Given: the variable is unset
		clearEnv(t)

		// When: we load config
		cfg, err := config.Load()

		// Then: the documented default applies
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SMTPPort != config.DefaultSMTPPort {
			t.Fatalf("SMTPPort = %d, want %d", cfg.SMTPPort, config.DefaultSMTPPort)
		}
	})

	t.Run("0 means an ephemeral port", func(t *testing.T) {
		// Given: OVERCAST_SMTP_PORT=0
		clearEnv(t)
		t.Setenv("OVERCAST_SMTP_PORT", "0")

		// When: we load config
		cfg, err := config.Load()

		// Then: 0 is accepted rather than rejected as out of range
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.SMTPPort != 0 {
			t.Fatalf("SMTPPort = %d, want 0", cfg.SMTPPort)
		}
	})

	for _, bad := range []string{"70000", "-1", "abc"} {
		t.Run("rejects "+bad+" naming the variable", func(t *testing.T) {
			// Given: a value that is not a port
			clearEnv(t)
			t.Setenv("OVERCAST_SMTP_PORT", bad)

			// When: we load config
			_, err := config.Load()

			// Then: startup fails, and the error says which variable to fix
			if err == nil {
				t.Fatalf("Load: expected an error for OVERCAST_SMTP_PORT=%s", bad)
			}
			if !strings.Contains(err.Error(), "OVERCAST_SMTP_PORT") {
				t.Fatalf("error %q does not name OVERCAST_SMTP_PORT", err)
			}
		})
	}
}
