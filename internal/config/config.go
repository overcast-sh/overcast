// Package config loads and validates all runtime configuration from environment
// variables. All other packages receive a *Config value — they never read
// os.Getenv directly. This makes configuration explicit and testable.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// StateBackend identifies which storage implementation to use.
type StateBackend string

const (
	// StateBackendMemory stores all state in-process. Fastest; nothing persists
	// across restarts. Best for unit tests and CI pipelines.
	StateBackendMemory StateBackend = "memory"

	// StateBackendPersistent writes every mutation synchronously to SQLite.
	// Slowest; fully durable. Previously named "sqlite" (still accepted as alias).
	StateBackendPersistent StateBackend = "persistent"

	// StateBackendHybrid serves all reads from an in-memory map and flushes
	// writes to SQLite asynchronously at a configurable interval. Fast reads,
	// durable across restarts, with a small window of potential data loss on
	// unclean exit. This is the default — best for general local development.
	StateBackendHybrid StateBackend = "hybrid"

	// StateBackendWAL uses the append-log WALStore (memory reads, write-ahead
	// durability with replay on startup and periodic compaction).
	StateBackendWAL StateBackend = "wal"
)

// EKSMode identifies how the EKS service behaves.
type EKSMode string

const (
	// EKSModeMock keeps EKS metadata-only and does not start a Kubernetes API server.
	EKSModeMock EKSMode = "mock"

	// EKSModeLive enables the future live k3s-backed control plane path.
	EKSModeLive EKSMode = "live"
)

// EFSMode identifies how the EFS service behaves.
type EFSMode string

const (
	// EFSModeMock keeps EFS metadata-only: file systems have no backing storage.
	// An explicit opt-out — the default is live.
	EFSModeMock EFSMode = "mock"

	// EFSModeLive backs each file system with a named Docker volume so
	// emulated compute (Lambda, ECS) can share real file data. The default,
	// because it costs nothing where it cannot be used: volume operations run
	// only while a Docker daemon is reachable, and without one live mode
	// behaves exactly like mock.
	EFSModeLive EFSMode = "live"
)

// RDSMode identifies how the RDS service behaves.
type RDSMode string

const (
	// RDSModeMock keeps RDS metadata-only: instances move through the status
	// model on a timer and no engine container is ever started, so nothing
	// listens on the reported endpoint. An explicit opt-out — the default is
	// live — for environments that have a Docker daemon but cannot afford the
	// tens of seconds a real engine takes to accept its first connection.
	RDSModeMock RDSMode = "mock"

	// RDSModeLive runs a real engine container per DB instance, which is what
	// makes the endpoint connectable. The default. Live mode needs no Docker to
	// be safe: without a reachable daemon it behaves exactly like mock.
	RDSModeLive RDSMode = "live"
)

// ServiceMetricsMode controls whether emulated services automatically record
// AWS-native CloudWatch service metrics (docs/plans/service-metrics-platform.md).
type ServiceMetricsMode string

const (
	// ServiceMetricsAuto follows whether the CloudWatch service is enabled.
	// OVERCAST_SERVICES no longer gates which services are wired (every
	// service is always wired — see TestLoad_ignoresRemovedServicesVar), so
	// CloudWatch is always available and auto is currently equivalent to
	// enabled. The knob is kept distinct from enabled/disabled so a future
	// per-service gate can change auto's meaning without a new env var.
	ServiceMetricsAuto ServiceMetricsMode = "auto"

	// ServiceMetricsEnabled explicitly turns on automatic collection even in
	// a configuration where auto would not (an internal monitor/debug use
	// case; the plan's `disabled AWS service / enabled collection` cell).
	ServiceMetricsEnabled ServiceMetricsMode = "enabled"

	// ServiceMetricsDisabled explicitly turns off automatic collection. AWS
	// CloudWatch's own PutMetricData/query/alarm behavior is unaffected —
	// only the recorder that automatically observes Lambda/SQS/etc.
	// invocation outcomes is skipped.
	ServiceMetricsDisabled ServiceMetricsMode = "disabled"
)

// DebuggerTimeoutPolicy decides what a function's timeout means while a
// debugger is involved (docs/plans/compute-debugger.md § 3.7). On AWS a
// function paused in a debugger would be killed at its timeout, which makes
// step debugging impossible; the policy is the one deliberate divergence, and
// it only ever applies while a debugger client is connected.
type DebuggerTimeoutPolicy string

const (
	// DebuggerTimeoutAttached stops the invocation clock while at least one
	// debugger client is connected to the target. The default: it works for
	// every protocol, including ones whose pauses Overcast cannot see.
	DebuggerTimeoutAttached DebuggerTimeoutPolicy = "attached"

	// DebuggerTimeoutPaused stops the clock only while the protocol reports
	// the program as paused, so a connected-but-running debugger still sees
	// the real timeout. Protocols without a pause observer fall back to
	// attached.
	DebuggerTimeoutPaused DebuggerTimeoutPolicy = "paused"

	// DebuggerTimeoutStrict enforces the real timeout always, exactly as AWS
	// would — for reproducing timeout behaviour with a debugger attached.
	DebuggerTimeoutStrict DebuggerTimeoutPolicy = "strict"
)

// String returns the policy as it is spelled in OVERCAST_DEBUGGER_TIMEOUT.
func (p DebuggerTimeoutPolicy) String() string { return string(p) }

// ParseDebuggerTimeoutPolicy reads an OVERCAST_DEBUGGER_TIMEOUT value. Empty
// means the default. Anything else that is not one of the three policies is
// an error rather than a fallback: the policy decides whether a paused
// function survives, and a typo that quietly restored the default is exactly
// the surprise it exists to prevent.
func ParseDebuggerTimeoutPolicy(raw string) (DebuggerTimeoutPolicy, error) {
	p := DebuggerTimeoutPolicy(strings.ToLower(strings.TrimSpace(raw)))
	switch p {
	case "":
		return DebuggerTimeoutAttached, nil
	case DebuggerTimeoutAttached, DebuggerTimeoutPaused, DebuggerTimeoutStrict:
		return p, nil
	default:
		return "", fmt.Errorf("config: OVERCAST_DEBUGGER_TIMEOUT %q is invalid (expected attached, paused, or strict)", raw)
	}
}

// VPCEgressMode decides whether the containers Overcast starts can reach
// anything outside this machine — the internet, real AWS endpoints, a
// third-party API.
//
// It is one named mode rather than a per-network flag because it is one
// question. A container sits on two Docker networks at once (its data plane
// and the control plane) and takes its default route from whichever of them is
// routable, so isolating one and not the other settles nothing: egress
// survives on the other network and the flag that was set becomes decoration.
// Egress is a property of the whole topology, so it is set on the whole
// topology.
//
// See docs/networking/egress.md for what each mode grants, and
// docs/plans/container-networking-egress.md for the decision this encodes.
type VPCEgressMode string

const (
	// VPCEgressOpen makes every network Overcast creates routable, so every
	// container it starts has egress. The default.
	//
	// It is the default because it is what every comparable emulator does —
	// LocalStack, Floci, Moto and SAM CLI all give a VPC-attached function full
	// egress, and none of them models VPC networking at all — and because the
	// common local case is a hybrid stack whose functions call real AWS or a
	// third-party API. On AWS that stack works because its private subnets have
	// a NAT route. Overcast reads no route tables (see VPCEgressRouted), so
	// withholding egress here withholds it from stacks AWS would have allowed,
	// with no template change that helps.
	VPCEgressOpen VPCEgressMode = "open"

	// VPCEgressRouted decides egress per subnet from its route table: a
	// `0.0.0.0/0` route to an attached internet gateway or an available NAT
	// gateway grants it, no default route (or a blackhole) withholds it. The
	// AWS-faithful answer (#1571).
	//
	// Every VPC keeps its `--internal` plane, which every container in it
	// joins, so resources in one VPC reach each other whatever their subnets
	// route to. A container whose subnet grants egress also joins the VPC's
	// egress network — a routable bridge named Config.VPCEgressNetwork, carved
	// from VPCEgressPool — and takes its default route from there. The control
	// plane is `--internal` too, as under `none`, since a routable one would
	// hand every container a route out regardless of its route table; on a
	// host where that cannot be delivered (Docker Desktop) the shortfall is
	// warned about and reported, exactly as `none` does.
	VPCEgressRouted VPCEgressMode = "routed"

	// VPCEgressNone makes every network Overcast creates `--internal`, so no
	// container it starts reaches anything outside this machine. For
	// deterministic CI, air-gapped hosts, and proving a stack has no hidden
	// external dependency.
	//
	// Overcast's own API and the Lambda Runtime API keep working: they ride the
	// control plane, and reaching a server on this host is not egress. On a
	// host where an `--internal` control plane would sever that path — Docker
	// Desktop, where containers dial the host's routable address rather than a
	// bridge gateway — the control plane is left routable, and the shortfall is
	// warned about at startup and reported by `/_overcast/health`. Stranding
	// every invocation at INIT is not a hermetic stack; it is a broken one.
	VPCEgressNone VPCEgressMode = "none"
)

// ControlPlaneInternalMode decides whether the control plane Docker network
// (Config.ControlNetwork) is created `--internal` — cut off from everything
// Docker did not put on it.
//
// Superseded by VPCEgressMode. It exists because the answer used to be inferred
// from the runtime environment alone, which made the same pinned Overcast
// version behave differently on two machines with nothing saying why (#1564).
// VPCEgressMode asks the question the operator actually has — may containers
// reach the outside — of the whole topology rather than of one network.
//
// The type stays undeprecated because the setting is still honoured and this is
// what it is spelled in; it is Config.ControlPlaneInternal, the field, that is
// deprecated. Read it through Config.LegacyControlPlaneInternal.
type ControlPlaneInternalMode string

const (
	// ControlPlaneInternalAuto keeps the runtime probe: internal when Overcast
	// is itself containerised or is talking to a native Linux daemon, not
	// internal otherwise. The default, because it is what alpha.37 shipped and
	// it is right for most machines — but it is a property of the *host*, so
	// two machines on one pinned version can legitimately differ. Pin true or
	// false when that matters. See dataplane.ControlPlaneInternal.
	ControlPlaneInternalAuto ControlPlaneInternalMode = "auto"

	// ControlPlaneInternalTrue always creates the control plane `--internal`.
	// This is what makes a VPC with no internet gateway actually withhold the
	// internet: without it the VPC's own `--internal` network is decoration,
	// because every container is also on the control plane and would reach the
	// world through that.
	//
	// It costs a Docker Desktop host every Lambda invocation — containers
	// there dial the host's routable address, which `--internal` severs — so
	// pin it only where containers reach Overcast on-link (Overcast
	// containerised, or a native Linux daemon), which is exactly the set auto
	// detects.
	ControlPlaneInternalTrue ControlPlaneInternalMode = "true"

	// ControlPlaneInternalFalse never creates the control plane `--internal`.
	// The pre-alpha.37 behaviour, and the one to pin when compute in a
	// gateway-less VPC is expected to reach the internet anyway — including
	// for parity with LocalStack, which isolates no network of its own
	// (docs/localstack-compatibility.md).
	ControlPlaneInternalFalse ControlPlaneInternalMode = "false"
)

// DefaultEFSNFSImage is the digest-pinned NFS-Ganesha image used for
// mount-target exports when OVERCAST_EFS_NFS is on. Pinned rather than
// floating so an upstream retag cannot change what a mount target runs.
//
// The image is Kubernetes SIG-Storage's NFS provisioner build; Overcast uses
// it only as a carrier for ganesha.nfsd and its VFS FSAL, replacing the
// entrypoint with its own configuration. It is multi-arch (amd64, arm64,
// ppc64le, s390x) and runs unprivileged.
const DefaultEFSNFSImage = "registry.k8s.io/sig-storage/nfs-provisioner@sha256:c825f3d5e28bde099bd7a3daace28772d412c9157ad47fa752a9ad0baafc118d"

// Default ports of the two auxiliary listeners that bind beside the AWS API.
// Both fall back to an ephemeral port when the default is busy and are pinned
// at any other value — the rule the web console's port already follows — so
// that a second Overcast on one host loses neither Lambda nor Inbox capture.
const (
	// DefaultLambdaRuntimeAPIPort is the default LAMBDA_RUNTIME_API_PORT.
	DefaultLambdaRuntimeAPIPort = 9001
	// DefaultSMTPPort is the default OVERCAST_SMTP_PORT.
	DefaultSMTPPort = 1025
)

// Config holds all runtime configuration for the emulator.
// Zero value is not valid — always construct via Load().
type Config struct {
	// Host is the hostname or IP address to bind to.
	//
	// NOT equivalent to LocalStack's LOCALSTACK_HOST — that variable sets the
	// name LocalStack embeds in URLs it returns, which is what Hostname below
	// does here. Host controls the socket bind address, which LocalStack calls
	// GATEWAY_LISTEN; Overcast's OVERCAST_LISTEN follows that idiom (see #870).
	//
	// Set via OVERCAST_LISTEN. The pre-rename name, OVERCAST_HOST, has been
	// removed rather than kept as an alias — Overcast is alpha software, and a
	// leftover OVERCAST_HOST is a hard startup error naming the replacement
	// (see resolveListen) rather than a silently-honoured fallback.
	// Use "127.0.0.1" to restrict to localhost only.
	//
	// Defaults to "0.0.0.0" when Overcast is containerised (required for
	// Docker's -p publishing to reach it) and "127.0.0.1" natively (#761) — an
	// explicit OVERCAST_LISTEN always wins over either default. See
	// ListenSource/ListenAutoReason/ListenAutoSignal for why a given run
	// resolved the way it did.
	// When several addresses are configured this is the first of them — see
	// Hosts.
	Host string

	// Hosts are every address the API listener binds, in order, with Hosts[0]
	// mirrored into Host. Load populates it; a Config built in code may leave
	// it nil, which Addrs() reads as "just Host".
	//
	// More than one address is for reaching the same emulator from two places
	// that no single address covers. The case that drove it: a throwaway
	// instance wants to be on loopback and nowhere routable, but a test suite
	// running in a sibling container reaches the host over the Docker bridge,
	// where loopback is invisible. Binding "127.0.0.1,172.17.0.1" serves both
	// without ever putting the emulator on a network the machine is attached
	// to. A wildcard cannot be combined with a specific address.
	//
	// The web UI listener binds Host only: the extra addresses exist so
	// container-side clients can reach the API, and the UI is a browser
	// surface on the machine itself.
	Hosts []string

	// ListenSource reports whether Host/Hosts came from an explicit
	// OVERCAST_LISTEN (ListenSourceExplicit) or from the environment-dependent
	// default (ListenSourceAuto) — see resolveListen. Drives the startup log
	// line's wording of *why* a run bound what it bound.
	ListenSource ListenSource

	// ListenAutoReason is a short, human-readable explanation of why the
	// default was chosen. Empty when ListenSource is ListenSourceExplicit.
	ListenAutoReason string

	// ListenAutoSignal is the stable code identifying which default fired:
	// "containerised" or "native". Empty when ListenSource is
	// ListenSourceExplicit. Safe to key tests or logs off of, unlike
	// ListenAutoReason's free text.
	ListenAutoSignal string

	// Port is the TCP port the HTTP server listens on.
	Port int

	// Hostname is the externally-reachable hostname or IP that services embed
	// in URLs returned to clients (e.g. SQS QueueUrl, SNS UnsubscribeURL,
	// RDS Endpoint.Address). When empty, defaults to "localhost".
	// Set OVERCAST_HOSTNAME when Overcast runs in Docker Compose alongside
	// app containers that need to reach it by its service name.
	//
	// LocalStack's LOCALSTACK_HOST is accepted as a compatibility alias
	// (#1190): it is the true analogue of this field (unlike OVERCAST_HOST,
	// the pre-#870 name for the *bind* address, which LOCALSTACK_HOST was
	// never equivalent to). This is a deliberate exception to Overcast's alpha
	// "no aliases" policy — that policy governs Overcast's own past names
	// (like the removed OVERCAST_HOST), not another project's documented
	// variables; aliasing LocalStack's is product value, since Overcast is
	// meant to be a drop-in replacement for it. See resolveHostnameAlias for
	// the parsing and conflict rules, and HostnameAliasSource for how a
	// startup log line reports which alias (if any) supplied the value.
	Hostname string

	// HostnameAliasSource names the LocalStack-compatibility environment
	// variable that supplied or confirmed Hostname (currently only
	// "LOCALSTACK_HOST"), or is empty when Hostname came from OVERCAST_HOSTNAME
	// alone, or from neither being set. Set whenever the alias variable was
	// present and accepted — including when both it and OVERCAST_HOSTNAME
	// were set to the same value — so the startup log line
	// (logHostnameAlias in cmd/overcast/cmd_serve.go) can tell the operator
	// their LocalStack-style setting was recognised. Also set when
	// HOSTNAME_EXTERNAL (the legacy name LOCALSTACK_HOST replaced) supplied
	// or confirmed the value — see resolveStringAlias/hostnameExternalAlias
	// in localstack_aliases.go.
	HostnameAliasSource string

	// LocalStackAliasesUsed records every other LocalStack-compatibility
	// alias (#1190, see internal/config/localstack_aliases.go) that supplied
	// or confirmed an Overcast setting, keyed by the Overcast variable name
	// it aliased ("OVERCAST_PORT", "OVERCAST_LISTEN",
	// "OVERCAST_DEFAULT_REGION", "OVERCAST_DATA_DIR", "OVERCAST_LOG_LEVEL",
	// "OVERCAST_STATE", "LAMBDA_INIT_TIMEOUT_SECONDS"), with the LocalStack
	// variable name(s) that contributed as the value (comma-joined when more
	// than one did, e.g. a port confirmed by both EDGE_PORT and
	// GATEWAY_LISTEN agreeing). Hostname has its own dedicated
	// HostnameAliasSource field above rather than an entry here, since it
	// predates this map (#1190's first PR) and already has its own tested
	// startup log line. Empty/absent when no alias fired for that variable.
	// Drives one startup Info line per entry — see logLocalStackAliases in
	// cmd/overcast/cmd_serve.go.
	LocalStackAliasesUsed map[string]string

	// IgnoredLocalStackVars lists the LocalStack-documented variables (see
	// ignoredLocalStackVars in internal/config/localstack_aliases.go)
	// present in the environment that Overcast recognises but which have no
	// effect (SERVICES, LOCALSTACK_API_KEY, LOCALSTACK_AUTH_TOKEN) — see
	// IgnoredLocalStackReason for why each one is inert. Logged once per
	// entry at startup so an operator sees their setting was seen rather
	// than wonder why nothing changed.
	IgnoredLocalStackVars []string

	// LocalStackVolumeDataDir is set to LocalStackVolumeDir when DataDir was
	// adopted from a LocalStack-shaped volume mount at /var/lib/localstack
	// rather than from the image's own /data — see adoptLocalStackVolume.
	// Empty in every other run. Drives one startup Info line, since a data
	// directory that came from neither the environment nor the documented
	// default is exactly the kind of thing an operator should be told about.
	LocalStackVolumeDataDir string

	// SplitHorizonHosts are extra hostnames that resolve to 127.0.0.1 in public
	// DNS and are remapped to Overcast's address inside containers Overcast
	// starts, so one URL is dialable from both the host and a sibling container.
	// Appended to the built-in set (localhost.overcast.sh,
	// localhost.localstack.cloud, localhost.floci.io) — see
	// internal/containerendpoint.
	SplitHorizonHosts []string

	// TestOnlyServiceSubset restricts which services router.New registers.
	// nil — the normal case, and the only case any production path produces —
	// registers every service.
	//
	// Load never populates this: there is no environment variable for it and
	// no way for a user to set it. It exists because a handful of router tests
	// need a server where nothing but the service under test is registered.
	// S3's bucket/object routes are deliberately broad, so proving that no
	// modeled operation falls through to them, or that an S3 bucket whose name
	// collides with another service's REST literal stays reachable, requires
	// removing the other claimants — there is no way to assert it against a
	// fully-registered router. See tests/integration/router and
	// docs/plans/host-routing-precedence.md.
	TestOnlyServiceSubset map[string]bool

	// State controls the global storage backend used for all services. This
	// is always a concrete backend (memory, persistent, hybrid, or wal) —
	// when OVERCAST_STATE is unset or "auto", it holds the *resolved* value
	// (see resolveAutoState); the raw configured value ("auto" or whatever
	// the user set) is preserved separately in StateConfigured.
	// Individual services may override this via ServiceStates.
	State StateBackend

	// StateConfigured is the raw value of OVERCAST_STATE as configured, or
	// "auto" if the variable was unset (auto is also the default). Unlike
	// State, this is never resolved — it exists so /_overcast/health and startup logs
	// can report what was actually configured, not just what it resolved to.
	StateConfigured string

	// StateSource reports whether State was chosen explicitly (OVERCAST_STATE
	// set to a concrete backend) or resolved by the OVERCAST_STATE=auto
	// heuristic. Drives the actionable variant of the memory-mode advisory
	// (internal/router/advisories.go checkMemoryMode) and the startup log
	// line below.
	StateSource StateSource

	// StateAutoReason is a short, human-readable explanation of why
	// resolveAutoState chose State. Empty when StateSource is "explicit".
	StateAutoReason string

	// StateAutoSignal is the stable code identifying which auto-detection
	// signal fired: "mountpoint", "explicit-data-dir", "existing-database",
	// or "" when none fired (State resolved to memory). Empty when
	// StateSource is "explicit". Safe to key tests off of, unlike
	// StateAutoReason's free text.
	StateAutoSignal string

	// ServiceStates overrides the global State for individual services.
	// Keys are lowercase service names ("s3", "sqs", "dynamodb", etc.).
	// Entries missing from the map inherit the global State.
	ServiceStates map[string]StateBackend

	// HybridFlushInterval controls how often the hybrid store flushes
	// in-memory state to disk. Only meaningful when State or a per-service
	// mode is "hybrid".
	HybridFlushInterval time.Duration

	// HybridSyncMode controls fsync policy for the hybrid store's pending
	// log. Valid values: always, interval, never. Corresponds to
	// state.WALSyncMode (the hybrid pending log reuses the same sync-mode
	// mechanism as the WAL backend).
	HybridSyncMode string

	// HybridSyncInterval controls periodic fsync cadence for the hybrid
	// pending log when HybridSyncMode is "interval".
	HybridSyncInterval time.Duration

	// HybridDirtyEntryThreshold triggers an early, out-of-band hybrid flush
	// once this many unflushed pending-log operations have accumulated,
	// ahead of the next HybridFlushInterval tick. A value <= 0 disables the
	// entry-count trigger (the byte threshold still applies unless it is
	// also disabled).
	HybridDirtyEntryThreshold int

	// HybridDirtyByteThreshold triggers an early, out-of-band hybrid flush
	// once the approximate byte size of unflushed writes exceeds this many
	// bytes. A value <= 0 disables the byte-size trigger.
	HybridDirtyByteThreshold int64

	// HybridMaintenanceInterval controls how often the hybrid store's
	// background loop runs routine SQLite housekeeping (a passive WAL
	// checkpoint plus a conditional incremental vacuum — see
	// docs/plans/storage-plan.md item 3.5). Never runs on the request path.
	HybridMaintenanceInterval time.Duration

	// WALFsyncMode controls fsync policy for the WAL backend.
	// Valid values: always, interval, never.
	WALFsyncMode string

	// WALFsyncInterval controls periodic fsync cadence when WALFsyncMode is
	// set to interval.
	WALFsyncInterval time.Duration

	// WALMaxLogBytes triggers log compaction when the append log grows past
	// this size.
	WALMaxLogBytes int64

	// DataDir is the root directory for the SQLite file and any
	// on-disk state (analogous to LocalStack's DATA_DIR).
	DataDir string

	// CADir is where the local overcast CA lives (OVERCAST_CA_DIR),
	// defaulting to <DataDir>/ca.
	//
	// It is separable from DataDir because the two have opposite lifetimes.
	// State is per-environment and disposable; the CA is a trust anchor, and
	// installing one into a system trust store is a per-MACHINE act that a
	// container recreation must not invalidate. Pointing this at a mount the
	// host owns — a small dedicated volume, or the host's own
	// ~/.overcast/data/ca — is what lets an ephemeral daemon mint leaves from
	// a CA that outlives it, which is how mkcert, Caddy and step-ca all treat
	// the anchor-vs-leaf split. The mount may be read-only: nothing here
	// writes to it once the CA exists.
	CADir string

	// CADirConfigured records that OVERCAST_CA_DIR was set explicitly, rather
	// than CADir being the <DataDir>/ca default. It is the only signal the
	// daemon has that someone deliberately placed the CA — used to tell a
	// containerized daemon whose CA will die with it from one sharing a CA the
	// host owns. A heuristic, not proof: it cannot tell a real mount from a
	// path that merely happens to be set.
	//
	// Read it through CACertDir(), not directly: a Config built by hand rather
	// than by Load has an empty CADir, and joining paths onto "" silently
	// yields a relative path in the process's working directory.
	CADirConfigured bool

	// Region is the default AWS region reported in ARNs and responses.
	Region string

	// AccountID is the fake AWS account ID embedded in ARNs.
	AccountID string

	// EKSMode controls whether the EKS service stays metadata-only (`mock`) or
	// enables the live k3s-backed control plane path (`live`).
	EKSMode EKSMode

	// EFSMode controls whether the EFS service backs each file system with a
	// named Docker volume (`live`, the default) or stays metadata-only
	// (`mock`). Live mode needs no Docker to be safe: it falls back to
	// metadata-only whenever a daemon is unreachable.
	EFSMode EFSMode

	// RDSMode controls whether the RDS service starts a real engine container
	// per DB instance (`live`, the default) or stays metadata-only (`mock`).
	// Mock mode makes an instance reach `available` in a moment rather than in
	// the tens of seconds a real engine takes, at the cost of nothing listening
	// on the endpoint it reports.
	RDSMode RDSMode

	// ServiceMetrics controls whether emulated services automatically record
	// the AWS-native CloudWatch metrics they publish (Lambda Invocations,
	// Errors, Duration, ... — docs/plans/service-metrics-platform.md).
	// Defaults to `auto`, which today means enabled (see ServiceMetricsAuto).
	// This is Overcast's own internal-subsystem knob, not an AWS service
	// name, so it is not, and must not become, a value accepted by the
	// retired OVERCAST_SERVICES variable.
	ServiceMetrics ServiceMetricsMode

	// SigV4Validate enables SigV4 signature verification.
	SigV4Validate bool

	// EnforceIAM enables opt-in IAM authorization enforcement middleware.
	// Default false.
	EnforceIAM bool

	// EnforceAPIGatewayThrottle turns API Gateway usage-plan throttle and
	// quota limits from evaluate-and-report into actual rejection: an
	// over-limit request gets AWS's 429 instead of being served. Usage is
	// always measured and readable through GetUsage regardless — only the
	// rejection is gated, so a stack that works today keeps working.
	// Default false. Corresponds to env var
	// OVERCAST_ENFORCE_APIGATEWAY_THROTTLE.
	EnforceAPIGatewayThrottle bool
	// EnforceAppSyncCognitoAuth turns AppSync's AMAZON_COGNITO_USER_POOLS
	// authorization from a bearer-token presence check into real verification
	// against the local Cognito user pool the API is configured with: RS256
	// signature, issuer, token_use, expiry, and appIdClientRegex. Default
	// false, which keeps the low-friction local-dev posture where any bearer
	// token is accepted and its claims are decoded into $ctx.identity — the
	// behaviour resolver tests that mint unsigned JWTs depend on. Corresponds
	// to env var OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH.
	EnforceAppSyncCognitoAuth bool

	// EnforceLambdaResourcePolicy makes a service-originated Lambda
	// invocation — an S3 notification, an SNS subscription delivery, an API
	// Gateway integration, an EventBridge rule target — check the function's
	// resource-based policy before it is delivered, the way AWS does. Off by
	// default: statements are stored and returned but never consulted, so a
	// stack that works today keeps working. Direct client Invoke calls are
	// never gated either way, because Overcast does not validate credentials.
	// Corresponds to env var OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY.
	EnforceLambdaResourcePolicy bool

	// ProtocolStrict restores strict rejection of "claimed-but-undeclared"
	// wire protocols: when a request's identified protocol isn't one a
	// service's SupportedProtocols() lists, the service returns 415
	// UnsupportedProtocol instead of attempting the decode anyway. Default
	// false (lenient): the emulator logs a "protocol drift" warning and
	// attempts dispatch regardless, since AWS SDKs may switch a service's
	// wire protocol without notice (see docs/plans/level2-codegen.md
	// Track 1.2). Corresponds to env var OVERCAST_PROTOCOL_STRICT.
	ProtocolStrict bool

	// CFNSyncWait is the bounded time CloudFormation waits for fast stack
	// create/update/delete provisioning to reach a terminal state before returning.
	// A zero value disables the wait and restores fully asynchronous behaviour.
	// Corresponds to env var OVERCAST_CFN_SYNC_WAIT_MS. Default 1000ms.
	CFNSyncWait time.Duration

	// StepFunctionsExecutionTimeout is the ceiling on how long one Step
	// Functions execution may run. It is a runaway guard, not a request
	// timeout: StartExecution accepts and returns while the execution is still
	// RUNNING, so this never sits on the wire and is set high enough not to
	// rule out ordinary Wait states. A state machine's own top-level
	// TimeoutSeconds can lower the budget but never raise it. Exceeding it
	// ends the execution TIMED_OUT with AWS's States.Timeout.
	// Corresponds to env var OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT.
	// Default 15m; values below 1s are raised to 1s.
	StepFunctionsExecutionTimeout time.Duration

	// ShutdownTimeout is how long the server waits for in-flight
	// requests to complete before forcibly closing.
	ShutdownTimeout time.Duration

	// LogLevel controls log verbosity: "trace", "debug", "info", "warn", "error"
	// (case-insensitive). "trace" is Overcast-specific — it sits below zap's
	// built-in "debug" and covers periodic machine chatter (health-check and
	// /_overcast/debug/* request logs, flush/checkpoint/maintenance/sweep cycle logs,
	// pool internals); see serviceutil.ParseLevel and CONTRIBUTING.md § Log
	// levels. Default "info".
	LogLevel string

	// Network is the Docker network every container Overcast starts is
	// reachable on by name when it belongs to no VPC — the *default data
	// plane*, Overcast's stand-in for the account's default VPC. A resource
	// that names a VPC joins that VPC's network instead; see
	// docs/dev/container-networking.md for the two-plane model and
	// ControlNetwork below for the other half of it.
	//
	// Corresponds to env var OVERCAST_NETWORK. Defaults to "overcast".
	Network string

	// VPCEgress decides whether the containers Overcast starts reach anything
	// outside this machine: "open" (all of them do), "none" (none of them do),
	// "routed" (per subnet, from the template's route tables).
	//
	// Corresponds to env var OVERCAST_VPC_EGRESS. Defaults to "open". See
	// VPCEgressMode and docs/networking/egress.md.
	VPCEgress VPCEgressMode

	// VPCEgressPool is the IPv4 range the per-VPC egress networks are carved
	// from under VPCEgress=routed, one /24 each. It exists so those networks
	// never draw on Docker's own default address pools, which on a stock
	// daemon stretch to about 31 networks in total and are shared with every
	// other tool on the machine — see docs/networking/routed-egress.md § The
	// address-pool ceiling.
	//
	// Corresponds to env var OVERCAST_VPC_EGRESS_POOL. Defaults to
	// "198.18.0.0/16" — the benchmarking range (RFC 2544), which is never
	// routed on the internet and which neither Docker nor the `remapped` VPC
	// strategy (100.64.0.0/10) draws on. Must be a /24 or wider.
	VPCEgressPool string

	// ControlPlaneInternal pins whether ControlNetwork is created `--internal`,
	// overriding what VPCEgress decided for that one network: "auto" (leave it
	// to VPCEgress), "true" (always internal), "false" (never internal).
	//
	// Corresponds to env var OVERCAST_CONTROL_PLANE_INTERNAL.
	//
	// Deprecated: set OVERCAST_VPC_EGRESS instead — `none` for what `true`
	// meant, `open` for what `false` meant, both applied to every plane rather
	// than this one. Kept working, and still winning where it is pinned, so a
	// configuration written against #1566 keeps its answer; setting it logs a
	// deprecation notice naming the mode that expresses the same intent.
	ControlPlaneInternal ControlPlaneInternalMode

	// ControlPlaneInternalSet records that OVERCAST_CONTROL_PLANE_INTERNAL was
	// present in the environment, as opposed to defaulting to "auto".
	//
	// The two are indistinguishable from the value alone and the deprecation
	// notice must only fire for the operator who actually set it — warning
	// every default installation about a variable it never used is how a
	// deprecation notice gets tuned out.
	ControlPlaneInternalSet bool

	// LambdaDockerSocket is the path to the Docker daemon socket used to
	// manage Lambda container siblings. Defaults to DOCKER_HOST when set,
	// otherwise the platform Docker socket (/var/run/docker.sock on
	// Linux/macOS, npipe:////./pipe/docker_engine on Windows).
	//
	// The per-service socket overrides below exist for unusual setups, but
	// every one of them must address the *same* daemon: containers are attached
	// to shared networks across service boundaries, which one daemon cannot do
	// on another's behalf.
	LambdaDockerSocket string

	// DockerSocketSource is "DOCKER_HOST" when that variable supplied
	// LambdaDockerSocket, and empty when LAMBDA_DOCKER_SOCKET or the platform
	// default did. Internal provenance for the startup log only — see
	// docker_host.go.
	DockerSocketSource string

	// DockerHostUnsupported carries the raw DOCKER_HOST value when it names a
	// transport Overcast has no dialer for (ssh://, https://), so startup can
	// warn that the platform default was used instead rather than leaving the
	// operator to work out why their daemon was never reached.
	DockerHostUnsupported string

	// LambdaRuntimeAPIPort is the port of the shared Lambda Runtime API
	// listener. Corresponds to env var LAMBDA_RUNTIME_API_PORT; defaults to
	// DefaultLambdaRuntimeAPIPort, and 0 binds an ephemeral port. No container
	// is ever told this port — each execution environment dials its own
	// per-container listener — so the default falls back to an ephemeral port
	// when it is busy (a second Overcast on the same host), while any other
	// value is pinned: a failed bind then disables the container runtime and
	// is reported by /_overcast/health.
	LambdaRuntimeAPIPort int

	// LambdaRuntimeAPIHost pins the address Lambda containers are told to dial
	// for the Runtime API. Corresponds to env var LAMBDA_RUNTIME_API_HOST;
	// empty (the default, spelled `auto`) means Overcast establishes it by
	// having a container connect to each candidate in turn — see
	// containerendpoint.ResolveListen.
	//
	// The escape hatch exists because the probe costs one container start and
	// can be wrong in ways this machine cannot see (a daemon that refuses
	// container creates, an air-gapped host with no busybox). Setting it skips
	// both the ordering and the probe. `host.docker.internal` is the value that
	// fixes a Docker Desktop host whose firewall blocks the binary's own
	// address (#1572); an IP literal is right for a remote or DinD daemon that
	// reaches this process on one particular interface.
	LambdaRuntimeAPIHost string

	// LambdaDockerMaxConcurrentStarts bounds concurrent Docker-backed Lambda
	// environment starts. This is local Docker backpressure, not an AWS-facing
	// Lambda concurrency quota. Corresponds to env var
	// LAMBDA_DOCKER_MAX_CONCURRENT_STARTS. 0 (the default) means unset: the
	// Lambda runtime derives a value from the Docker host's CPU count —
	// clamp(NCPU/2, 2, 8), because every start bursts to ~2 CPUs during INIT —
	// falling back to 4 when Docker /info is unavailable.
	LambdaDockerMaxConcurrentStarts int

	// LambdaMaxInstances bounds how many Lambda containers Overcast runs at
	// once across all functions — warm and executing combined. When the limit
	// is reached, a new invocation first reclaims the least-recently-used idle
	// container; if none can be reclaimed it queues until the function's
	// timeout and is then throttled. This protects the host, it is not an
	// emulation of the AWS account concurrency quota. Corresponds to env var
	// LAMBDA_MAX_INSTANCES. 0 (the default) means unset: the Lambda runtime
	// derives a value from the Docker host's memory —
	// clamp(MemTotal*0.65 / 256 MiB, 4, 32) — falling back to 25 when Docker
	// /info is unavailable.
	LambdaMaxInstances int

	// LambdaMaxInstancesPerFunction bounds concurrent containers for a single
	// function, independent of any AWS reserved concurrency the function has.
	// Clamped to LambdaMaxInstances. Corresponds to env var
	// LAMBDA_MAX_INSTANCES_PER_FUNCTION. 0 (the default) means unset: the
	// Lambda runtime derives clamp(maxInstances/2, 2, maxInstances) from the
	// effective global limit, falling back to 10 when Docker /info is
	// unavailable.
	LambdaMaxInstancesPerFunction int

	// LambdaMaxMemoryMB bounds the aggregate memory of live Lambda containers,
	// in MB: Σ MemorySize over warm and executing containers. Each container is
	// hard-capped at its function's MemorySize with swap disabled, so this is a
	// real bound on what Lambda can take from the host, not an estimate. When
	// the budget is exhausted a new invocation reclaims idle containers, then
	// queues, then throttles at the function's timeout — the same ladder as
	// LambdaMaxInstances. Corresponds to env var LAMBDA_MAX_MEMORY_MB. 0 (the
	// default) means unset: the Lambda runtime derives 65% of the Docker host's
	// MemTotal, or leaves the budget unlimited when Docker /info is unavailable.
	LambdaMaxMemoryMB int

	// LambdaMaxWarmInstances bounds how many idle containers one function keeps
	// after a concurrency burst. Surplus instances are destroyed on release
	// rather than waiting out the 15-minute idle sweep. A provisioned
	// concurrency allocation raises this floor for that function. Corresponds
	// to env var LAMBDA_MAX_WARM_INSTANCES. Default 10.
	LambdaMaxWarmInstances int

	// LambdaSeedRuntimeImages controls whether Overcast pre-pulls every known
	// managed Lambda runtime image when the Docker runtime starts. Disabled by
	// default to avoid Docker Desktop/containerd pressure during frequent restarts;
	// runtime images are still pulled lazily on first use.
	// Corresponds to env var LAMBDA_SEED_RUNTIME_IMAGES. Default false.
	LambdaSeedRuntimeImages bool

	// LambdaInitTimeout is the maximum time to wait for a Docker-backed Lambda
	// runtime to finish INIT and poll the Runtime API for its first invocation.
	// This is separate from the function invocation timeout. Corresponds to env
	// var LAMBDA_INIT_TIMEOUT_SECONDS. Default 10s.
	LambdaInitTimeout time.Duration

	// LambdaKeepContainers controls whether Docker containers are removed when
	// a Lambda instance expires (idle timeout) or the function is deleted.
	// Set to true to keep stopped containers for post-mortem inspection.
	// Corresponds to env var LAMBDA_KEEP_CONTAINERS. Default false.
	LambdaKeepContainers bool

	// LambdaTarCacheMB bounds the in-memory cache of pre-built cold-start
	// artifacts (code and layer tars), in megabytes. 0 disables the cache.
	// Corresponds to env var LAMBDA_TAR_CACHE_MB. Default 256.
	LambdaTarCacheMB int

	// LambdaProactiveInit pre-initializes one execution environment after a
	// function's configuration settles, mirroring AWS's documented proactive
	// initialization, so the next request lands warm. Corresponds to env var
	// LAMBDA_PROACTIVE_INIT. Default true (on by default since the flip in
	// issue #1099, after a soak of opt-in releases with no reported
	// regressions); set LAMBDA_PROACTIVE_INIT=false to opt back out.
	LambdaProactiveInit bool

	// HotReload is the umbrella opt-in for bind-mount source reload across every
	// compute service, so a developer turns the inner loop on once rather than
	// learning a variable per service. Per-service fields below override it in
	// either direction. Corresponds to env var OVERCAST_HOT_RELOAD.
	// Default false.
	HotReload bool

	// LambdaHotReload enables bind-mount based source reload for functions that
	// opt in via the overcast:hot-reload-path function tag.
	// Corresponds to env var OVERCAST_LAMBDA_HOT_RELOAD, defaulting to
	// HotReload when that variable is unset.
	LambdaHotReload bool

	// Debugger is the umbrella opt-in for attaching a step debugger to user
	// code running inside emulated compute (docs/plans/compute-debugger.md).
	// It mirrors HotReload: one switch for every compute service, with the
	// per-service fields below overriding it in either direction. The switch
	// is server-side because it opens listening ports on the developer's
	// machine, which a tag alone must not be able to do.
	// Corresponds to env var OVERCAST_DEBUGGER. Default false.
	Debugger bool

	// LambdaDebugger enables the debugger for functions that opt in via the
	// overcast:debug / overcast:debug-port tags.
	// Corresponds to env var OVERCAST_LAMBDA_DEBUGGER, defaulting to Debugger
	// when that variable is unset.
	LambdaDebugger bool

	// ECSDebugger enables the debugger for task definitions that opt in via
	// the same tags, optionally suffixed with a container name.
	// Corresponds to env var OVERCAST_ECS_DEBUGGER, defaulting to Debugger
	// when that variable is unset.
	ECSDebugger bool

	// DebuggerListen is the address the per-target debug ports bind on. It
	// follows Host's containerised-vs-native rule (#761) by defaulting to
	// Host itself: loopback natively, every interface in Docker so that
	// `-p 9229-9329:9229-9329` reaches the ports.
	// Corresponds to env var OVERCAST_DEBUGGER_LISTEN.
	DebuggerListen string

	// DebuggerPorts is the inclusive range auto-allocated debug ports are
	// taken from, lowest free first. A fixed range rather than ephemeral
	// ports because the developer publishes it from Docker and types it into
	// an editor; it has to be predictable.
	// Corresponds to env var OVERCAST_DEBUGGER_PORTS, "lo-hi". Default 9229-9329.
	DebuggerPorts [2]int

	// DebuggerTimeout is what the function timeout means while a debugger is
	// attached — see DebuggerTimeoutPolicy.
	// Corresponds to env var OVERCAST_DEBUGGER_TIMEOUT. Default attached.
	DebuggerTimeout DebuggerTimeoutPolicy

	// DebuggerWaitTimeout bounds how long an invocation of a function tagged
	// overcast:debug-wait=true is held for a debugger client to attach
	// before its event is dispatched. On expiry the invocation proceeds
	// undebugged and a WARN says so. Long by default because the hold is
	// what the tag asks for; the bound exists so a forgotten tag cannot
	// park an invocation for ever.
	// Corresponds to env var OVERCAST_DEBUGGER_WAIT_TIMEOUT. Default 120s.
	DebuggerWaitTimeout time.Duration

	// LambdaFetchRemoteLayers enables downloading layer content from real AWS
	// when a layer ARN is not found locally. Requires valid AWS credentials.
	// Downloaded layers are cached on disk and have /opt/extensions/ stripped
	// (extensions can't run locally without the full Lambda platform).
	// Corresponds to env var LAMBDA_FETCH_REMOTE_LAYERS. Default false.
	LambdaFetchRemoteLayers bool

	// LambdaLayerCacheDir overrides the directory used to look up and cache
	// layer zip files. When empty, defaults to {DataDir}/layers (typically
	// /data/layers in the standard Docker image).
	// Users can pre-download layers and mount this directory to avoid needing
	// AWS credentials at runtime. Files are named {sha256(arn)}.zip.
	// Corresponds to env var LAMBDA_LAYER_CACHE_DIR.
	LambdaLayerCacheDir string

	// LambdaRemoteAWSAccessKeyID is the AWS access key used for fetching
	// remote layers. Read from LAMBDA_REMOTE_AWS_ACCESS_KEY_ID.
	LambdaRemoteAWSAccessKeyID string

	// LambdaRemoteAWSSecretAccessKey is the AWS secret key used for fetching
	// remote layers. Read from LAMBDA_REMOTE_AWS_SECRET_ACCESS_KEY.
	LambdaRemoteAWSSecretAccessKey string

	// LambdaRemoteAWSSessionToken is the optional session token for fetching
	// remote layers. Read from LAMBDA_REMOTE_AWS_SESSION_TOKEN.
	LambdaRemoteAWSSessionToken string

	// ECSDockerSocket is the path to the Docker daemon socket used to manage
	// ECS task containers. Defaults to the same value as LambdaDockerSocket.
	ECSDockerSocket string

	// ECSKeepContainers controls whether Docker containers are removed when
	// an ECS task stops. Set to true for post-mortem inspection. Volumes the
	// task owns are kept with them.
	// Corresponds to env var ECS_KEEP_CONTAINERS. Default false.
	ECSKeepContainers bool

	// ECSHotReload enables bind-mount source reload for task definitions that
	// opt in via an overcast:hot-reload-path tag, redirecting a declared
	// scratch volume at a host path.
	// Corresponds to env var OVERCAST_ECS_HOT_RELOAD, defaulting to HotReload
	// when that variable is unset.
	ECSHotReload bool

	// ECRRegistryPort is the host port the shared ECR registry container asks
	// for. A fixed, well-known port keeps repositoryUri stable across restarts
	// (a repository re-mints it from the registry on every read, so it follows
	// the port rather than fixing it), gives daemon configuration such as
	// insecure-registries something nameable, and matches the port LocalStack
	// serves its registry on — worth real money for drop-in migration. When
	// the port is taken the registry falls back to an ephemeral one rather
	// than staying down. 0 skips straight to ephemeral.
	// Corresponds to env var OVERCAST_ECR_REGISTRY_PORT. Default 4510.
	ECRRegistryPort int

	// ECRRegistryPersist backs the fixed-port registry container with a named
	// Docker volume, so images pushed to the emulated ECR survive a restart.
	// It applies to the fixed-port claim only: an ephemeral registry's address
	// changes every run, and its container name is deliberately random, so
	// there is nothing stable to key a volume on and a per-run one would leak
	// forever.
	//
	// Persisting is the default because the alternative silently costs work.
	// cdk-assets decides whether to build and push a container asset by asking
	// DescribeImages for its content-hash tag, so a registry that starts empty
	// makes every `cdk deploy` after a restart rebuild and re-push assets that
	// never changed. Set this false to go back to a registry whose contents die
	// with its container — worth doing when the volume itself is the problem
	// (a corrupt one, or a CI runner that must start from nothing), which is
	// otherwise a `docker volume rm` away.
	// Corresponds to env var OVERCAST_ECR_REGISTRY_PERSIST. Default true.
	ECRRegistryPersist bool

	// RDSDockerSocket is the path to the Docker daemon socket used to manage
	// RDS database containers. Defaults to the same value as LambdaDockerSocket.
	RDSDockerSocket string

	// RDSPortBase is the starting host port for RDS database containers.
	// Each DB instance gets a sequential port starting from this base.
	// Defaults to 33060.
	RDSPortBase int

	// RDSKeepContainers controls whether Docker containers are removed when
	// an RDS instance is deleted. Set to true for post-mortem inspection.
	// Corresponds to env var RDS_KEEP_CONTAINERS. Default false.
	RDSKeepContainers bool

	// ElastiCacheDockerSocket is the path to the Docker daemon socket used to
	// manage ElastiCache Redis/Valkey containers. Defaults to the same value as
	// LambdaDockerSocket.
	ElastiCacheDockerSocket string

	// ElastiCachePortBase is the starting host port for ElastiCache containers.
	// Each cache cluster gets a sequential port starting from this base.
	// Defaults to 63790.
	ElastiCachePortBase int

	// ElastiCacheKeepContainers controls whether Docker containers are removed
	// when a cache cluster is deleted. Set to true for post-mortem inspection.
	// Corresponds to env var ELASTICACHE_KEEP_CONTAINERS. Default false.
	ElastiCacheKeepContainers bool

	// MSKDockerSocket is the path to the Docker daemon socket for MSK Redpanda containers.
	MSKDockerSocket string

	// MSKPortBase is the starting host port for MSK containers.
	MSKPortBase int

	// MSKKeepContainers controls whether Docker containers are removed on delete.
	MSKKeepContainers bool

	// EKSDockerSocket is the path to the Docker daemon socket used to manage
	// EKS live-mode control-plane containers. Defaults to the same value as
	// LambdaDockerSocket.
	EKSDockerSocket string

	// EFSDockerSocket is the path to the Docker daemon socket used to manage
	// EFS live-mode volumes. Defaults to the same value as LambdaDockerSocket.
	EFSDockerSocket string

	// EFSNFSExport opts into the NFS data plane: in live mode each mount
	// target starts an NFS-Ganesha container exporting its file system's
	// volume. Off by default — the volume mounts Lambda and ECS use need no
	// NFS hop, and an export costs a container plus a published port per
	// mount target. Corresponds to OVERCAST_EFS_NFS.
	EFSNFSExport bool

	// EFSNFSPortBase is the starting host port for NFS-Ganesha export
	// containers. Each mount target publishes container port 2049 on the
	// first free port at or above this.
	EFSNFSPortBase int

	// EFSNFSImage is the NFS-Ganesha image used for mount-target exports,
	// pinned by digest. Override to run a different build.
	EFSNFSImage string

	// EC2VPCNetworkStrategy selects the policy used to map stored VPCs onto
	// Docker networks. Docker bridges share one host address space, so two
	// VPCs with overlapping CIDRs cannot both back real networks. Valid
	// values:
	//
	//   shared   (default) — overlapping VPCs share one Docker network.
	//                        Fastest, isolation leaks between sharers.
	//   strict              — reject overlapping CIDRs at CreateVpc; startup
	//                        tolerates existing overlaps.
	//   remapped            — allocate a shadow CIDR from 100.64.0.0/10
	//                        when the requested range collides.
	//   netns               — per-VPC Linux netns for true overlap. (future;
	//                        rejected at load time)
	//
	// Unrecognized values fall back to "shared" with a startup warning.
	// Corresponds to env var OVERCAST_EC2_VPC_STRATEGY.
	EC2VPCNetworkStrategy string

	// Debug enables the /_overcast/debug/* endpoint namespace.
	// These endpoints expose internal state and should never be enabled
	// in shared or production environments.
	Debug bool

	// DebugTraceBuffer is the number of user-facing request traces retained
	// unconditionally — the floor (OVERCAST_DEBUG_TRACE_BUFFER). Only read when
	// Debug is true.
	DebugTraceBuffer int

	// DebugTraceCeiling bounds how far a burst may grow retention past the
	// floor (OVERCAST_DEBUG_TRACE_CEILING). A CDK deploy pushes thousands of
	// requests through in a couple of minutes; the floor alone keeps the
	// rollback noise and discards the error that caused it.
	DebugTraceCeiling int

	// DebugTraceWindow is how long traces above the floor survive
	// (OVERCAST_DEBUG_TRACE_WINDOW). It is sized for the gap between a deploy
	// failing and somebody opening the trace UI, not for the deploy itself.
	DebugTraceWindow time.Duration

	// DebugTraceBytes bounds the request and response body bytes retained
	// (OVERCAST_DEBUG_TRACE_BYTES, in MiB). A backstop on the burst rather than
	// an override of the floor: the ceiling counts traces, and ten thousand
	// 1 MiB uploads are not ten thousand DescribeStacks polls.
	DebugTraceBytes int64

	// DebugTracePinned caps the traces retained because they went wrong
	// (OVERCAST_DEBUG_TRACE_PINNED). Those are exempt from both the floor and
	// the window: a failure is what someone comes back for.
	DebugTracePinned int

	// TLSCertFile is the path to the TLS certificate file.
	// When set (together with TLSKeyFile), the server uses HTTPS.
	TLSCertFile string

	// TLSKeyFile is the path to the TLS private key file.
	TLSKeyFile string

	// TLSMode selects how TLS is provisioned (OVERCAST_TLS). Empty means
	// plain HTTP unless TLSCertFile/TLSKeyFile are set; TLSModeAuto mints a
	// server certificate from Overcast's local CA at startup and serves both
	// the API and the web UI over HTTPS (which also unlocks browser HTTP/2
	// via ALPN). Mutually exclusive with TLSCertFile/TLSKeyFile.
	TLSMode string

	// SMTPMock enables the built-in SMTP capture server. When true, all
	// outbound SNS email/email-json notifications are delivered to the local
	// capture server and are browseable in the web UI under /mail.
	// Automatically set to false when SMTPHost is configured.
	SMTPMock bool

	// SMTPPort is the TCP port the mock SMTP server listens on, and also the
	// default port the mailer dials when SMTPHost is unset. Corresponds to env
	// var OVERCAST_SMTP_PORT; defaults to DefaultSMTPPort, and 0 binds an
	// ephemeral port — Inbox capture follows it, because the mailer learns the
	// bound address rather than assuming the port. The default falls back to
	// an ephemeral port when it is busy; any other value is pinned, and a
	// failed bind is reported by /_overcast/health.
	SMTPPort int

	// SMTPHost is the hostname of an external SMTP relay. When set, SMTPMock
	// is automatically false and the built-in capture server is not started.
	SMTPHost string

	// SMTPFrom is the envelope From address used when sending SNS email notifications.
	SMTPFrom string

	// SMTPUsername and SMTPPassword are the credentials for SMTP AUTH PLAIN
	// when connecting to an external relay. Leave empty for no authentication.
	SMTPUsername string
	SMTPPassword string

	// SMTPTLS enables implicit TLS (port 465). For STARTTLS (port 587) leave
	// this false — Go's net/smtp client upgrades automatically.
	SMTPTLS bool

	// SMTPInboxMax is the maximum number of messages the capture store retains.
	// When exceeded, the oldest message is evicted.
	SMTPInboxMax int

	// InitEnabled controls whether init hook scripts are executed.
	// Defaults to true.
	InitEnabled bool

	// InitDirs is the list of base directories to scan for init hook scripts.
	// Each directory should contain stage subdirs: boot.d/, start.d/, ready.d/,
	// shutdown.d/. Scripts are executed in the order directories appear, then
	// alphabetically within each directory.
	// Defaults to ["/etc/localstack/init", "/etc/overcast/init"].
	InitDirs []string

	// InitTimeout is the maximum time allowed for each individual init script.
	// Defaults to 30s.
	InitTimeout time.Duration

	// Version is the build version string, injected via -ldflags at build time.
	// Not loaded from environment — set by the caller after Load().
	Version string

	// PublishedPort is the host port the API is actually reachable on, which
	// differs from Port only when Overcast runs in a container that remaps it
	// (`docker run -p 4580:4566`). A container cannot see its own port mapping
	// from the inside, so this is recovered by asking Docker about our own
	// container; it is 0 when there is nothing to recover — a native binary, no
	// Docker socket, or a 1:1 mapping.
	//
	// Consumers use it wherever an address has to make sense *outside* the
	// container: the port the web UI hands the browser, and the origins
	// containerendpoint recognises as Overcast's own when rewriting URLs a
	// host-side deploy minted.
	//
	// Not loaded from environment — set by the caller after Load().
	PublishedPort int

	// DNSEnabled turns on the resolver that serves Overcast's split-horizon
	// hostnames to the containers it starts (OVERCAST_DNS, default true).
	//
	// It exists because /etc/hosts is an exact-match table: it can shadow
	// localhost.overcast.sh but not bucket.s3.localhost.overcast.sh, and the
	// URLs AWS SDKs build are overwhelmingly subdomains. See internal/dns.
	DNSEnabled bool

	// DNSPort is the port the resolver listens on (OVERCAST_DNS_PORT, default
	// 53). Docker's --dns cannot express a port, so anything other than 53 is
	// only useful for tests.
	DNSPort int

	// DNSListening reports that the resolver actually bound, which is what
	// makes it safe to point containers at it: a container told to use a
	// resolver that is not there loses all name resolution. Binding port 53
	// needs privilege outside a container, so this is false more often than
	// DNSEnabled is.
	//
	// Not loaded from environment — set by the caller after Load().
	DNSListening bool

	// MCPRemoteExposure explicitly enables remote/runtime MCP exposure mode.
	// When true, MCPAuthToken must be configured.
	MCPRemoteExposure bool

	// MCPAuthToken is the bearer token required for HTTP access to runtime MCP
	// when MCPRemoteExposure is enabled.
	MCPAuthToken string
}

// Addr returns the "host:port" string for the server to listen on. When
// several addresses are configured this is the first — see Addrs.
//
// JoinHostPort rather than "%s:%d" so an IPv6 literal comes back bracketed and
// dialable ("[::1]:4566"); every other host formats identically to before.
func (c *Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}

// Addrs returns every "host:port" the API listener binds, in configured
// order, with Addr() first. A Config assembled in code rather than by Load
// leaves Hosts nil; that reads as the single Addr(), so a struct literal that
// only sets Host behaves exactly as it did before OVERCAST_HOST took a list.
// CACertDir returns the directory holding the local overcast CA — CADir when
// set, otherwise the historical <DataDir>/ca. The fallback exists because
// Configs are also constructed literally (tests, embedders) rather than only
// through Load, and an empty CADir would otherwise resolve to a relative path
// in the working directory — a CA silently minted somewhere nobody is
// looking.
func (c *Config) CACertDir() string {
	if c.CADir != "" {
		return c.CADir
	}
	return filepath.Join(c.DataDir, "ca")
}

func (c *Config) Addrs() []string {
	if len(c.Hosts) == 0 {
		return []string{c.Addr()}
	}
	addrs := make([]string, 0, len(c.Hosts))
	for _, host := range c.Hosts {
		addrs = append(addrs, net.JoinHostPort(host, strconv.Itoa(c.Port)))
	}
	return addrs
}

// parseHosts splits an OVERCAST_LISTEN value into the addresses to bind.
// Blank entries are dropped and repeats collapsed — binding the same address
// twice on one port fails the second listen with EADDRINUSE, and a trailing
// comma is not worth failing a startup over.
//
// A wildcard alongside a specific address is refused rather than collapsed.
// It reads as a widening ("loopback, and also everything") when it is the
// opposite of what the specific address was asking for, and on Linux the
// second bind fails anyway — as an opaque listen error at startup rather than
// as the configuration mistake it is.
func parseHosts(raw string) ([]string, error) {
	var hosts []string
	for _, host := range strings.Split(raw, ",") {
		host = strings.TrimSpace(host)
		if host == "" || slices.Contains(hosts, host) {
			continue
		}
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("config: OVERCAST_LISTEN %q names no address to bind", raw)
	}
	if len(hosts) > 1 {
		for _, host := range hosts {
			if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
				return nil, fmt.Errorf(
					"config: OVERCAST_LISTEN %q combines the wildcard %q with a specific address; "+
						"the wildcard already covers every address, so drop either it or the rest",
					raw, host)
			}
		}
	}
	return hosts, nil
}

// defaultDebuggerPorts is the auto-allocation range for debug ports. It
// starts at 9229 because that is the port every Node.js editor integration
// assumes, so the first debugged function lands where a default launch
// configuration already points.
const defaultDebuggerPorts = "9229-9329"

// defaultDebuggerWaitTimeout bounds the hold for a debugger client. Two
// minutes is long enough to press F5 or open the Code tab after invoking, and
// short enough that a forgotten overcast:debug-wait tag reads as a slow
// invocation with a WARN rather than a hung one.
const defaultDebuggerWaitTimeout = "120s"

// resolveDebuggerListen validates an OVERCAST_DEBUGGER_LISTEN value: one bind
// address. Unlike OVERCAST_LISTEN it takes no list — each debug port is a
// separate listener already, and a per-port address list would multiply them
// for no case anyone has asked for.
func resolveDebuggerListen(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" || strings.Contains(host, ",") {
		return "", fmt.Errorf("config: OVERCAST_DEBUGGER_LISTEN %q must name exactly one bind address", raw)
	}
	return host, nil
}

// parseDebuggerPorts reads an OVERCAST_DEBUGGER_PORTS range, "lo-hi"
// inclusive, or a single port meaning a range of one. Both ends must be real
// ports and lo may not exceed hi; the reversed spelling is refused rather
// than swapped because it is more often a typo than an intention.
func parseDebuggerPorts(raw string) ([2]int, error) {
	invalid := func() ([2]int, error) {
		return [2]int{}, fmt.Errorf("config: OVERCAST_DEBUGGER_PORTS %q is not a port range (expected lo-hi, each 1-65535, lo <= hi)", raw)
	}
	loStr, hiStr, isRange := strings.Cut(strings.TrimSpace(raw), "-")
	if !isRange {
		hiStr = loStr
	}
	lo, err := strconv.Atoi(strings.TrimSpace(loStr))
	if err != nil || lo < 1 || lo > 65535 {
		return invalid()
	}
	hi, err := strconv.Atoi(strings.TrimSpace(hiStr))
	if err != nil || hi < 1 || hi > 65535 || hi < lo {
		return invalid()
	}
	return [2]int{lo, hi}, nil
}

// ListenSource identifies whether the effective bind address (Config.Host /
// Config.Hosts) was chosen explicitly (OVERCAST_LISTEN set) or resolved by
// the environment-dependent default (#761 — see resolveListenDefault). Mirrors
// StateSource's role for the storage backend.
type ListenSource string

const (
	// ListenSourceExplicit means OVERCAST_LISTEN was set.
	ListenSourceExplicit ListenSource = "explicit"

	// ListenSourceAuto means OVERCAST_LISTEN was unset, and the bind address
	// was resolved by resolveListenDefault.
	ListenSourceAuto ListenSource = "auto"
)

// Bind-address auto-detection signal codes — stable identifiers safe to key
// tests or logs off of, unlike ListenAutoReason's free text.
const (
	listenAutoSignalContainerised = "containerised"
	listenAutoSignalNative        = "native"
)

// resolveListenDefault picks the bind-address default for when
// OVERCAST_LISTEN is unset (#761): "0.0.0.0" when Overcast is containerised —
// Docker's -p publishing requires binding every interface, and the container
// case is a hard constraint, not a preference — "127.0.0.1" natively, since
// nothing about a native `overcast serve` requires listening beyond loopback
// unless OVERCAST_LISTEN says otherwise.
//
// dataDirSource is the literal (undefaulted) value of OVERCAST_DATA_DIR_SOURCE,
// read once by the caller (Load) before Host resolution and reused again for
// detectAutoStateSignals below — isDockerImage is the single source of truth
// for "containerised" both places share, so the two decisions can never
// disagree about what that means.
func resolveListenDefault(dataDirSource string) (addr, signal, reason string) {
	if isDockerImage(dataDirSource) {
		return "0.0.0.0", listenAutoSignalContainerised,
			"containerised (OVERCAST_DATA_DIR_SOURCE=image) — Docker's -p publishing requires binding every interface"
	}
	return "127.0.0.1", listenAutoSignalNative,
		"native — nothing requires listening beyond loopback unless OVERCAST_LISTEN says otherwise"
}

// resolveListen determines the raw (comma-separated, undefaulted) bind
// address list from OVERCAST_LISTEN, and whether it was set explicitly.
//
// OVERCAST_HOST, the pre-rename name (#870), has been removed rather than
// kept as an alias: Overcast is alpha software, and the project's policy for
// a removed setting is a loud startup failure naming the replacement, not a
// silently-honoured fallback — a leftover OVERCAST_HOST from before the
// rename must not be quietly ignored (which is exactly what would happen if
// it were left unrecognised: envOr would just see it as irrelevant noise and
// fall through to the OVERCAST_LISTEN default, changing nothing about the
// bind address while looking, to the operator, like it took effect).
//
// defaultListen is the fallback when OVERCAST_LISTEN is unset — see
// resolveListenDefault. An explicit OVERCAST_LISTEN always wins over it, in
// both directions: it can name the wildcard even when the default would have
// been loopback, or loopback even when the default would have been the
// wildcard.
func resolveListen(defaultListen string) (raw string, explicit bool, err error) {
	if _, hostSet := os.LookupEnv("OVERCAST_HOST"); hostSet {
		return "", false, fmt.Errorf(
			"config: OVERCAST_HOST has been removed; use OVERCAST_LISTEN instead (same value format)")
	}
	if v, ok := os.LookupEnv("OVERCAST_LISTEN"); ok {
		return v, true, nil
	}
	return defaultListen, false, nil
}

// resolveHostnameAlias determines Config.Hostname from OVERCAST_HOSTNAME and
// LocalStack's LOCALSTACK_HOST (#1190). LocalStack's docs describe
// LOCALSTACK_HOST as "the hostname (and optional port) under which the
// LocalStack gateway is available" and say it is used to construct
// resource URLs returned to clients — exactly the role Hostname plays here,
// and the reason #870 called it the true analogue of OVERCAST_HOSTNAME
// (unlike the similarly-named but semantically opposite OVERCAST_HOST,
// which controlled the bind address and has since been removed — see
// resolveListen). See
// https://docs.localstack.cloud/aws/capabilities/networking/accessing-endpoint-url/
// and https://docs.localstack.cloud/aws/capabilities/config/configuration/.
//
// Aliasing it is a deliberate LocalStack-compatibility shim, distinct from
// Overcast's own alpha "no aliases for removed settings" policy: that policy
// governs Overcast's own past names (OVERCAST_HOST's removal), not another
// project's documented variables. Overcast is meant to be a drop-in
// replacement for LocalStack, so honouring its documented configuration is
// product value, not scope creep.
//
// LOCALSTACK_HOST's value format is "hostname[:port]" — LocalStack's own
// example is "localhost.localstack.cloud:4566", bundling the port into the
// same variable Overcast splits into OVERCAST_HOSTNAME and OVERCAST_PORT.
// Only the hostname part maps to Hostname; the port part, when present,
// must agree with resolvedPort (the already-resolved OVERCAST_PORT) or
// startup fails — the port is never silently discarded, and never silently
// overrides the configured port.
//
// Conflict rule, uniform with resolveListen/OVERCAST_HOST and the rest of
// the LocalStack alias table: both variables present and disagreeing fails
// startup naming both. Never silently prefer one. The same value in both is
// fine — that is the expected shape of a compose file migrated line by line
// from LocalStack rather than cleaned up.
func resolveHostnameAlias(resolvedPort int) (hostname string, aliasSource string, err error) {
	overcastHostname := os.Getenv("OVERCAST_HOSTNAME")
	localstackHost, lsSet := os.LookupEnv("LOCALSTACK_HOST")
	if !lsSet || localstackHost == "" {
		return overcastHostname, "", nil
	}

	lsHostname, lsPort, err := splitHostnameAliasPort(localstackHost)
	if err != nil {
		return "", "", fmt.Errorf("config: LOCALSTACK_HOST %q is not a valid hostname[:port]: %w", localstackHost, err)
	}

	if overcastHostname != "" && overcastHostname != lsHostname {
		return "", "", fmt.Errorf(
			"config: OVERCAST_HOSTNAME=%q and LOCALSTACK_HOST=%q disagree (LOCALSTACK_HOST's hostname "+
				"part is %q) — LOCALSTACK_HOST is accepted as a compatibility alias for OVERCAST_HOSTNAME, "+
				"but they must name the same hostname when both are set; set only one, or set both to the "+
				"same value",
			overcastHostname, localstackHost, lsHostname)
	}

	if lsPort != "" {
		port, convErr := strconv.Atoi(lsPort)
		if convErr != nil || port < 1 || port > 65535 {
			return "", "", fmt.Errorf("config: LOCALSTACK_HOST %q names an invalid port %q", localstackHost, lsPort)
		}
		if port != resolvedPort {
			return "", "", fmt.Errorf(
				"config: LOCALSTACK_HOST=%q names port %d, which conflicts with OVERCAST_PORT=%d — "+
					"LOCALSTACK_HOST's port must match OVERCAST_PORT when both are set, or be omitted from "+
					"LOCALSTACK_HOST entirely",
				localstackHost, port, resolvedPort)
		}
	}

	return lsHostname, "LOCALSTACK_HOST", nil
}

// splitHostnameAliasPort splits a LocalStack-style "hostname[:port]" value
// into its hostname and (possibly empty) port parts. A bare IP literal —
// IPv4, or unbracketed IPv6 such as "::1" — is returned whole as the
// hostname with no port, since net.SplitHostPort cannot parse either without
// bracket notation and neither is ambiguous with a port suffix here.
func splitHostnameAliasPort(value string) (hostname, port string, err error) {
	if net.ParseIP(value) != nil {
		return value, "", nil
	}
	if !strings.Contains(value, ":") {
		return value, "", nil
	}
	h, p, splitErr := net.SplitHostPort(value)
	if splitErr != nil {
		return "", "", fmt.Errorf("expected hostname[:port], e.g. localhost.localstack.cloud:4566: %w", splitErr)
	}
	return h, p, nil
}

// WildcardDNSDomains are the public domains whose every subdomain resolves to
// 127.0.0.1, so an SDK-generated hostname reaches Overcast with no hosts-file
// edit. They are "split horizon": inside containers Overcast starts, the same
// names are remapped to its container address via /etc/hosts, so one URL is
// dialable from both sides of the container boundary.
//
// This is the single source of truth for the set.
// internal/middleware reads it for S3 virtual-hosted addressing — adding plain
// "localhost", which is not a public domain and so belongs only there — and
// internal/containerendpoint reads it for the /etc/hosts mapping. They were
// previously two hand-maintained lists and they drifted: containerendpoint
// knew localhost.floci.io while the S3 matcher did not, so a bucket was
// unreachable on a domain Overcast itself advertises.
//
// Extend per-deployment with OVERCAST_SPLIT_HORIZON_HOSTS rather than editing
// this list.
var WildcardDNSDomains = []string{
	"localhost.overcast.sh",
	"localhost.localstack.cloud",
	"localhost.floci.io",
}

// ControlNetwork returns the Docker network that carries the channel between
// Overcast and every container it starts: the Lambda Runtime API, and the
// AWS_ENDPOINT_URL calls function and task code makes back into the emulator.
//
// It is deliberately *not* the data plane. On AWS the Runtime API is inside the
// execution sandbox — reachable whatever VPC a function joins, because it is
// the mechanism by which the function runs at all — and the emulator's own API
// stands in for a VPC endpoint per service. Both must survive VPC placement;
// reaching a database must not. Keeping them on separate networks is what lets
// a VPC restrict the second without severing the first (which would strand
// every invocation at INIT).
//
// Derived from Network rather than separately configurable: one name to set,
// no way to configure half a pair.
func (c *Config) ControlNetwork() string {
	return c.Network + "_control"
}

// VPCNetwork returns the Docker network name backing one VPC.
//
// Derived from Network for the same reason ControlNetwork is, and for one more:
// a name shared between two Overcast instances on one daemon is a collision no
// label can untangle, so one OVERCAST_NETWORK per instance separates them by
// name before ownership is even asked about.
//
// Ownership itself is a separate question with a separate answer, and the two
// network classes answer it differently:
//
//   - **The planes** (Network and ControlNetwork) are scoped by *name*. They
//     carry no docker.LabelInstance: they are created by docker.Probe, before
//     any state store has been read, and two instances sharing an
//     OVERCAST_NETWORK share the plane deliberately.
//   - **Per-VPC networks** are scoped by *label*. Their names come from an
//     emulated resource id rather than from configuration, so two instances can
//     mint the same one; docker.LabelInstance carries the EC2 service's
//     data-directory-scoped identity (serviceutil.InstanceDomain) and decides
//     who may remove them.
//
// The default value reproduces the historical name exactly (`overcast-vpc-…`),
// so an installation that never set OVERCAST_NETWORK keeps adopting the
// networks it already has.
func (c *Config) VPCNetwork(vpcID string) string {
	return c.Network + "-vpc-" + vpcID
}

// VPCNetworkPrefix is the leading fragment every VPCNetwork name shares — what
// to match on when looking for this instance's VPC networks without a VPC id in
// hand. VPCEgressNetwork names share it too.
func (c *Config) VPCNetworkPrefix() string {
	return c.Network + "-vpc-"
}

// VPCEgressNetworkSuffix is what VPCEgressNetwork appends to VPCNetwork.
const VPCEgressNetworkSuffix = "-egress"

// VPCEgressNetwork returns the name of the routable bridge that carries the
// default route for containers in vpcID whose subnet's route table grants
// egress, under OVERCAST_VPC_EGRESS=routed. It is the VPC's second network:
// VPCNetwork is the `--internal` plane every container in the VPC joins, and
// this is the one only the containers with a route out also join.
//
// Named from VPCNetwork so it shares the prefix every tool that looks for
// this instance's VPC networks already matches on, and so the pair is
// adjacent in `docker network ls`.
func (c *Config) VPCEgressNetwork(vpcID string) string {
	return c.VPCNetwork(vpcID) + VPCEgressNetworkSuffix
}

// DefaultVPCEgressPool is the VPCEgressPool default. See that field.
const DefaultVPCEgressPool = "198.18.0.0/16"

// ValidateVPCEgressPool reports whether pool is a range the egress networks
// can be carved from: an IPv4 CIDR no narrower than /24 (one network's worth)
// and no wider than /8.
func ValidateVPCEgressPool(pool string) error {
	ip, ipnet, err := net.ParseCIDR(pool)
	if err != nil {
		return err
	}
	if ip.To4() == nil {
		return fmt.Errorf("must be an IPv4 range")
	}
	ones, _ := ipnet.Mask.Size()
	if ones > 24 {
		return fmt.Errorf("/%d is narrower than one /24 egress network", ones)
	}
	if ones < 8 {
		return fmt.Errorf("/%d is wider than /8", ones)
	}
	return nil
}

// LegacyControlPlaneInternal reports the deprecated
// OVERCAST_CONTROL_PLANE_INTERNAL pin: the mode it was set to, and whether it
// was set at all.
//
// It is the one place the deprecated field is read. Every caller goes through
// here so the deprecation stays visible where it matters — on the public field,
// for anyone reaching for it — without every compatibility read having to
// silence the linter itself.
//
// `set` is separate from the value because "auto" is both the default and a
// legitimate explicit value, and the deprecation notice must fire only for the
// operator who actually set it.
func (c *Config) LegacyControlPlaneInternal() (mode ControlPlaneInternalMode, set bool) {
	//nolint:staticcheck // SA1019: the deprecated pin is still honoured where set (#1566); this is its one reader.
	return c.ControlPlaneInternal, c.ControlPlaneInternalSet
}

// ExternalHostname returns the hostname that should appear in client-facing
// URLs. Returns Hostname if set, otherwise "localhost".
func (c *Config) ExternalHostname() string {
	if c.Hostname != "" {
		return c.Hostname
	}
	return "localhost"
}

// ExternalBaseURL returns the base URL for client-facing links, e.g.
// "http://localhost:4566" or "http://overcast:4566".
func (c *Config) ExternalBaseURL() string {
	scheme := "http"
	if c.TLSEnabled() {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s:%d", scheme, c.ExternalHostname(), c.Port)
}

// TLSModeAuto is the OVERCAST_TLS value that mints a certificate from the
// local Overcast CA at startup.
const TLSModeAuto = "auto"

// TLSEnabled returns true when the server should serve HTTPS — either via an
// explicitly configured cert/key pair or via the auto-minted local CA leaf.
func (c *Config) TLSEnabled() bool {
	return c.TLSAuto() || (c.TLSCertFile != "" && c.TLSKeyFile != "")
}

// MetricsCollectionEnabled reports whether automatic service-metric recording
// (docs/plans/service-metrics-platform.md) should run. `disabled` is the only
// value that turns it off; `auto` and `enabled` both turn it on today because
// CloudWatch is always wired (OVERCAST_SERVICES no longer gates services) —
// see ServiceMetricsAuto's doc comment.
func (c *Config) MetricsCollectionEnabled() bool {
	return c.ServiceMetrics != ServiceMetricsDisabled
}

// TLSAuto returns true when OVERCAST_TLS=auto: the server certificate is
// minted from Overcast's local CA at startup.
func (c *Config) TLSAuto() bool {
	return c.TLSMode == TLSModeAuto
}

// TLSAutoSANs returns every subject alternative name the auto-minted server
// certificate must cover: the loopback names and addresses, each wildcard
// DNS domain (apex, one-level wildcard, and the "*.s3." level used by S3
// virtual-hosted addressing), any extra split-horizon hosts, and the
// configured hostname.
//
// One-level wildcards are a TLS constraint, not a choice: "*.<base>" matches
// exactly one label, so deeper host-routed names with a variable middle
// (e.g. "{id}.execute-api.{region}.<base>") cannot be enumerated here and
// are not covered. The S3 level is included explicitly because
// "{bucket}.s3.<base>" is the common two-label shape.
func (c *Config) TLSAutoSANs() []string {
	sans := []string{"localhost", "127.0.0.1", "::1"}
	seen := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		sans = append(sans, name)
	}
	addDomain := func(base string) {
		base = strings.TrimSpace(base)
		if base == "" {
			return
		}
		if net.ParseIP(base) != nil {
			add(base) // an IP literal gets an IP SAN; wildcards over IPs are meaningless
			return
		}
		add(base)
		add("*." + base)
		add("*.s3." + base)
	}
	for _, base := range WildcardDNSDomains {
		addDomain(base)
	}
	for _, base := range c.SplitHorizonHosts {
		addDomain(base)
	}
	addDomain(c.Hostname)
	return sans
}

// allServices is the canonical list of supported service names.
var allServices = []string{"s3", "sqs", "sns", "ses", "dynamodb", "dynamodbstreams", "lambda", "pipes", "logs", "secretsmanager", "sts", "ssm", "kms", "iam", "cloudformation", "ec2", "rds", "ecs", "ecr", "efs", "eks", "cognito", "stepfunctions", "waf", "shield", "appsync", "apigateway", "cloudfront", "eventbridge", "kinesis", "appregistry", "cloudwatch", "acm", "opensearch", "appconfig", "appconfigdata", "bedrock", "glue", "firehose", "athena", "elasticache", "msk", "scheduler", "route53", "elbv2", "organizations", "autoscaling", "cloudtrail", "backup", "transfer"}

// AllServices returns the canonical list of supported service names in
// declaration order. The result is a copy, so callers cannot mutate package
// state through it.
//
// These are the names OVERCAST_STATE_<SERVICE> is keyed by, and cmd/capgen
// generates the service-name table in docs/configuration.md from this list,
// which is what keeps the documented names from drifting away from the real
// ones when a service is added or renamed.
func AllServices() []string {
	out := make([]string, len(allServices))
	copy(out, allServices)
	return out
}

// ServiceNamespacePrefix returns the canonical mapping from a config service
// name (as used in OVERCAST_STATE_<SERVICE> and allServices) to the
// storage-namespace prefix that service actually writes
// under — the segment before the first ":" in namespaces like "cfn:stacks" or
// "sqs:queues".
//
// Most services use their config name as their namespace prefix unchanged
// (e.g. "sqs" → "sqs"), but a few historical namespace prefixes are shorter
// than the config name:
//
//	cloudformation → cfn
//	apigateway     → apigw
//	eventbridge    → eb
//
// This function is the single source of truth for that mapping. It is used
// by both the per-service storage override routing in cmd/overcast's serve
// command and the /_overcast/debug endpoints in internal/router — state.NamespacedStore
// (internal/state/namespaced.go) routes purely on namespace prefix, not on
// config service name, so any caller that keys a NamespacedStore route map
// (or unwraps one) by config service name instead of this prefix will build
// an override that silently never takes effect.
//
// "dynamodbstreams" is intentionally left identity-mapped here — it must
// NOT be remapped to "dynamodb". cfg.ServiceStates is an unordered map: if
// both OVERCAST_STATE_DYNAMODB and OVERCAST_STATE_DYNAMODBSTREAMS were ever
// set to different modes, remapping dynamodbstreams to "dynamodb" would make
// both resolve to the same route-map key and collide, nondeterministically
// redirecting real DynamoDB item/table storage depending on map iteration
// order. It's also moot: dynamodbstreams.New (internal/services/dynamodbstreams/service.go)
// takes no state.Store parameter at all — it's a store-less facade over the
// dynamodb service, so an override for it can never have any effect
// regardless of prefix. See ServiceOverrideIneffective, which documents this
// (and a few other services) as a known no-op override.
func ServiceNamespacePrefix(service string) string {
	switch service {
	case "cloudformation":
		return "cfn"
	case "apigateway":
		return "apigw"
	case "eventbridge":
		return "eb"
	default:
		return service
	}
}

// ServiceOverrideIneffective reports whether an OVERCAST_STATE_<SERVICE>
// override for service is accepted by config validation but has no
// observable effect, and if so, a short human-readable reason why.
//
// These services fall into two groups, both of which mean the service never
// reads or writes through the store instance that a per-service override
// would substitute:
//
//   - "dynamodbstreams": a store-less facade over the dynamodb service.
//     dynamodbstreams.New(ddb DynamoDBService, logger *zap.Logger) — see
//     internal/services/dynamodbstreams/service.go — takes no state.Store
//     parameter; all persisted data actually lives under the dynamodb
//     service's own store.
//   - "sts": STS session state is written under the IAM namespace, not
//     "sts" — see internal/services/sts/handler.go, which calls
//     `h.st.Set(ctx, "iam:sessions", accessKeyID, ...)`. An OVERCAST_STATE_STS
//     override builds a store that is registered under the "sts" prefix,
//     but NamespacedStore.storeFor never routes "iam:sessions" there.
//   - "bedrock": a stateless stub. bedrock.New's state.Store parameter is
//     the blank identifier (see internal/services/bedrock/service.go) — the
//     service struct holds no store field and never persists anything.
//   - "organizations": a stateless stub. organizations.New accepts a
//     state.Store parameter but never assigns or otherwise uses it (see
//     internal/services/organizations/service.go) — the service struct
//     holds no store field.
//
// Callers that build per-service storage overrides (cmd/overcast's serve
// command) should still build and route the override store for these
// services when configured — it's harmless — but should log a warning so
// the no-op isn't silently misread as "the override took effect".
func ServiceOverrideIneffective(service string) (reason string, ok bool) {
	switch service {
	case "dynamodbstreams":
		return "store-less facade over the dynamodb service; dynamodbstreams.New takes no state.Store and owns no stored state of its own", true
	case "sts":
		return `STS session state is written under the "iam:sessions" namespace, not "sts"; a per-service override for sts is never consulted`, true
	case "bedrock":
		return "stateless stub; bedrock.New's state.Store parameter is discarded and never persists anything", true
	case "organizations":
		return "stateless stub; organizations.New's state.Store parameter is accepted but never used", true
	default:
		return "", false
	}
}

// Load reads configuration from environment variables and returns a validated
// Config. Returns an error if any required value is invalid.
//
// Environment variables (all optional, defaults shown):
//
//	OVERCAST_LISTEN                    127.0.0.1 native, 0.0.0.0 containerised (comma-separated
//	                                           for several; bind address — see resolveListenDefault;
//	                                           OVERCAST_HOST was renamed to this and removed, see #870;
//	                                           LocalStack's GATEWAY_LISTEN is accepted as a compatibility
//	                                           alias for it and OVERCAST_PORT together — see
//	                                           resolveGatewayListenAlias (#1190))
//	OVERCAST_HOSTNAME                  (empty — defaults to localhost in URLs; the actual
//	                                           analogue of LocalStack's LOCALSTACK_HOST and its legacy
//	                                           predecessor HOSTNAME_EXTERNAL, both accepted as
//	                                           compatibility aliases — see resolveHostnameAlias and
//	                                           hostnameExternalAlias (#1190); a leftover value that
//	                                           disagrees with OVERCAST_HOSTNAME or OVERCAST_PORT
//	                                           fails startup naming both)
//	OVERCAST_SPLIT_HORIZON_HOSTS       (empty — extra names remapped to Overcast
//	                                           inside containers, comma-separated)
//	OVERCAST_PORT                      4566    (LocalStack's EDGE_PORT is accepted as a direct
//	                                           compatibility alias — see edgePortAlias (#1190))
//	OVERCAST_STATE                     auto    (auto | memory | persistent | hybrid | wal)
//	                                           auto resolves to hybrid when the data directory
//	                                           is a mounted volume/bind mount, OVERCAST_DATA_DIR
//	                                           was explicitly set, or an existing database is
//	                                           found there — otherwise memory. See
//	                                           resolveAutoState (state_auto.go). LocalStack's
//	                                           PERSISTENCE=1 is accepted as a compatibility alias for
//	                                           the "persistent" backend — see persistenceStateAlias (#1190)
//	OVERCAST_STATE_<SERVICE>           <mode>  (per-service override, e.g. OVERCAST_STATE_S3=memory;
//	                                           does not accept "auto")
//	OVERCAST_DATA_DIR_SOURCE           (empty) internal provenance marker — the Docker image sets this
//	                                           to "image" alongside its baked-in OVERCAST_DATA_DIR=/data,
//	                                           so the auto-state resolver doesn't mistake the image
//	                                           default for user intent, and the DATA_DIR alias overrides
//	                                           the baked value instead of conflicting with it. Not for
//	                                           end users to set.
//	OVERCAST_HYBRID_FLUSH_INTERVAL     5s
//	OVERCAST_HYBRID_SYNC                interval (always | interval | never)
//	OVERCAST_HYBRID_SYNC_INTERVAL       100ms
//	OVERCAST_HYBRID_DIRTY_ENTRY_THRESHOLD 10000
//	OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD  8388608
//	OVERCAST_HYBRID_MAINTENANCE_INTERVAL  5m
//	OVERCAST_WAL_FSYNC                 interval (always | interval | never)
//	OVERCAST_WAL_FSYNC_INTERVAL        100ms
//	OVERCAST_WAL_MAX_LOG_BYTES         67108864
//	OVERCAST_DATA_DIR                  ~/.overcast/data (LocalStack's DATA_DIR is accepted as a direct
//	                                           compatibility alias — see dataDirAlias (#1190))
//	OVERCAST_CA_DIR                    {DataDir}/ca  (may be a read-only mount)
//	OVERCAST_DEFAULT_REGION             us-east-1 (LocalStack's DEFAULT_REGION is accepted as a direct
//	                                           compatibility alias — see defaultRegionAlias (#1190))
//	OVERCAST_ACCOUNT_ID                000000000000 (must be exactly 12 ASCII digits)
//	OVERCAST_EKS_MODE                  mock    (mock | live)
//	OVERCAST_EFS_MODE                  live    (mock | live; live is inert without Docker)
//	OVERCAST_RDS_MODE                  live    (mock | live; mock starts no engine container)
//	OVERCAST_SERVICE_METRICS           auto    (auto | enabled | disabled; internal subsystem, not
//	                                           an AWS service name — see docs/plans/service-metrics-platform.md)
//	OVERCAST_SIGV4_VALIDATE            false
//	OVERCAST_ENFORCE_IAM              false
//	OVERCAST_ENFORCE_APIGATEWAY_THROTTLE false//	OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH false  (true = AppSync verifies AMAZON_COGNITO_USER_POOLS
//	                                           bearer tokens against the local Cognito pool — RS256
//	                                           signature, issuer, token_use, expiry, appIdClientRegex —
//	                                           instead of only requiring one to be present)
//	OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY false (true = a service-originated invoke must be
//	                                           allowed by the function's resource-based policy)
//	OVERCAST_PROTOCOL_STRICT           false   (true = 415 on a protocol a service does not
//	                                           declare, instead of attempting the decode anyway)
//	OVERCAST_CFN_SYNC_WAIT_MS          1000
//	OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT 15m (runaway guard on one execution, not a request
//	                                           timeout; values below 1s are raised to 1s)
//	OVERCAST_LOG_LEVEL                 info    (trace | debug | info | warn | error; LocalStack's DEBUG=1
//	                                           is accepted as a compatibility alias for "debug" — see
//	                                           debugLogLevelAlias (#1190))
//	OVERCAST_SHUTDOWN_TIMEOUT          5s
//	OVERCAST_HOT_RELOAD                false   (umbrella for every compute service)
//	OVERCAST_LAMBDA_HOT_RELOAD         <OVERCAST_HOT_RELOAD>
//	OVERCAST_DEBUGGER                  false   (umbrella for every compute service: attach a step
//	                                           debugger to functions and tasks tagged overcast:debug —
//	                                           see docs/plans/compute-debugger.md)
//	OVERCAST_LAMBDA_DEBUGGER           <OVERCAST_DEBUGGER>
//	OVERCAST_ECS_DEBUGGER              <OVERCAST_DEBUGGER>
//	OVERCAST_DEBUGGER_LISTEN           <OVERCAST_LISTEN's resolved host> (one address the debug
//	                                           ports bind on: loopback natively, every interface when
//	                                           containerised so -p 9229-9329:9229-9329 reaches them)
//	OVERCAST_DEBUGGER_PORTS            9229-9329 (inclusive range auto-allocated debug ports are
//	                                           taken from, lowest free first)
//	OVERCAST_DEBUGGER_TIMEOUT          attached (attached | paused | strict — whether the function
//	                                           timeout clock stops while a debugger client is
//	                                           connected, only while the protocol reports a pause,
//	                                           or never)
//	OVERCAST_DEBUG                     false
//	OVERCAST_DEBUG_TRACE_BUFFER        1000    (traces always retained — the floor; only read
//	                                           when OVERCAST_DEBUG is on)
//	OVERCAST_DEBUG_TRACE_CEILING       10000   (how far a burst may grow retention)
//	OVERCAST_DEBUG_TRACE_WINDOW        1h      (how long overflow above the floor survives)
//	OVERCAST_DEBUG_TRACE_PINNED        1000    (traces kept because they went wrong)
//	OVERCAST_DEBUG_TRACE_BYTES_MB      512     (retained body bytes; backstop on the burst)
//	OVERCAST_TLS                       ""    (auto = mint from the local overcast CA; serves API + web UI over HTTPS/h2)
//	OVERCAST_TLS_CERT                  ""
//	OVERCAST_TLS_KEY                   ""
//	OVERCAST_DNS                       true    (resolver serving the split-horizon names to the
//	                                           containers Overcast starts; failing to bind is not fatal)
//	OVERCAST_DNS_PORT                  53      (Docker's --dns cannot express a port, so anything
//	                                           other than 53 is only useful for tests)
//	OVERCAST_EC2_VPC_STRATEGY          shared  (shared | strict | remapped are all implemented;
//	                                           netns is reserved for future work and rejected here)
//	OVERCAST_ECR_REGISTRY_PORT         4510    (host port the shared registry container asks for;
//	                                           0, or a port already taken, falls back to ephemeral)
//	OVERCAST_ECR_REGISTRY_PERSIST      true    (back the fixed-port registry with a named Docker
//	                                           volume, so pushed images survive a restart)
//	LAMBDA_DOCKER_SOCKET               DOCKER_HOST when set, else the platform default
//	                                           (/var/run/docker.sock on Linux/macOS,
//	                                           npipe:////./pipe/docker_engine on Windows). Every
//	                                           per-service socket override below must address the
//	                                           same daemon.
//	LAMBDA_RUNTIME_API_PORT            9001    (shared Runtime API listener; 0 = ephemeral. The default
//	                                           falls back to an ephemeral port when busy; any other
//	                                           value is pinned — see docs/configuration/two-instances.md)
//	LAMBDA_RUNTIME_API_HOST            auto    (the address containers dial for the Runtime API.
//	                                           auto: established by having a container connect to
//	                                           each candidate. Set a bare address — host.docker.internal,
//	                                           or an IP — to pin it and skip the probe. A scheme, port
//	                                           or path is rejected at startup: the port is
//	                                           LAMBDA_RUNTIME_API_PORT and the two are joined)
//	LAMBDA_DOCKER_MAX_CONCURRENT_STARTS (auto) derived from Docker host CPUs: clamp(NCPU/2, 2, 8);
//	                                           4 when Docker /info is unavailable
//	LAMBDA_MAX_INSTANCES               (auto)  derived from Docker host memory:
//	                                           clamp(MemTotal*0.65 / 256MiB, 4, 32); 25 when /info is unavailable
//	LAMBDA_MAX_INSTANCES_PER_FUNCTION  (auto)  clamp(maxInstances/2, 2, maxInstances); 10 when /info is unavailable
//	LAMBDA_MAX_MEMORY_MB               (auto)  aggregate Σ MemorySize budget for live containers, in MB;
//	                                           derived as MemTotal*0.65; unlimited when /info is unavailable
//	LAMBDA_MAX_WARM_INSTANCES          10
//	LAMBDA_SEED_RUNTIME_IMAGES         false (true = pre-pull every managed runtime image at
//	                                           startup, instead of lazily on first use)
//	LAMBDA_INIT_TIMEOUT_SECONDS       10      (LocalStack's LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT is
//	                                           accepted as a direct compatibility alias — see
//	                                           lambdaInitTimeoutAlias (#1190))
//	LAMBDA_KEEP_CONTAINERS             false (true = keep stopped containers after expiry/delete)
//	LAMBDA_TAR_CACHE_MB                256   (in-memory cache of pre-built cold-start code and
//	                                           layer tars; 0 disables it)
//	LAMBDA_PROACTIVE_INIT              true  (false = disable pre-initializing one execution
//	                                           environment once a function's configuration settles)
//	LAMBDA_FETCH_REMOTE_LAYERS         false (true = download missing layers from real AWS)
//	LAMBDA_LAYER_CACHE_DIR             <OVERCAST_DATA_DIR>/layers (where layer zips are looked up
//	                                           and cached, named {sha256(arn)}.zip)
//	LAMBDA_REMOTE_AWS_ACCESS_KEY_ID    ""    (credentials for LAMBDA_FETCH_REMOTE_LAYERS)
//	LAMBDA_REMOTE_AWS_SECRET_ACCESS_KEY ""
//	LAMBDA_REMOTE_AWS_SESSION_TOKEN    ""    (optional)
//	OVERCAST_NETWORK                   overcast (the data plane every container Overcast starts
//	                                           shares, unless the resource names a VPC, in which
//	                                           case it joins that VPC's network. The control plane
//	                                           is derived from this rather than configured — see
//	                                           Config.ControlNetwork. One variable for every
//	                                           service: the per-service LAMBDA_NETWORK,
//	                                           ECS_NETWORK, RDS_NETWORK, ELASTICACHE_NETWORK,
//	                                           MSK_NETWORK, EKS_NETWORK and EFS_NETWORK are gone
//	                                           and are no longer read.)
//	OVERCAST_VPC_EGRESS                open  (open|routed|none — whether the containers Overcast
//	                                           starts reach anything outside this machine. open:
//	                                           all of them do. none: none of them do (hermetic CI;
//	                                           Overcast's own API and the Lambda Runtime API still
//	                                           work). routed: per subnet, from its route table — a
//	                                           0.0.0.0/0 route to an internet or NAT gateway grants
//	                                           egress, no default route withholds it. The decision
//	                                           and its reason are logged at startup and reported by
//	                                           /_overcast/health.)
//	OVERCAST_VPC_EGRESS_POOL   198.18.0.0/16  (the range the per-VPC egress networks of `routed`
//	                                           are carved from, one /24 each, so they never draw on
//	                                           Docker's ~31-network default address pools. A /24
//	                                           or wider.)
//	OVERCAST_CONTROL_PLANE_INTERNAL    auto  (DEPRECATED — set OVERCAST_VPC_EGRESS instead.
//	                                           auto|true|false, pinning the control plane network's
//	                                           `--internal` flag on top of the mode above. Still
//	                                           honoured; setting it logs a deprecation notice. See
//	                                           #1564.)
//	ECS_DOCKER_SOCKET                  <LAMBDA_DOCKER_SOCKET> (default: same as Lambda)
//	ECS_KEEP_CONTAINERS                false
//	OVERCAST_ECS_HOT_RELOAD            <OVERCAST_HOT_RELOAD>
//	RDS_DOCKER_SOCKET                  <LAMBDA_DOCKER_SOCKET> (default: same as Lambda)
//	RDS_PORT_BASE                      33060
//	RDS_KEEP_CONTAINERS                false
//	ELASTICACHE_DOCKER_SOCKET          <LAMBDA_DOCKER_SOCKET> (default: same as Lambda)
//	ELASTICACHE_PORT_BASE              63790
//	ELASTICACHE_KEEP_CONTAINERS        false
//	MSK_DOCKER_SOCKET                  <LAMBDA_DOCKER_SOCKET>
//	MSK_PORT_BASE                      49092
//	MSK_KEEP_CONTAINERS                false
//	EKS_DOCKER_SOCKET                  <LAMBDA_DOCKER_SOCKET>
//	EFS_DOCKER_SOCKET                  <LAMBDA_DOCKER_SOCKET>
//	OVERCAST_EFS_NFS                   false (true = one NFS-Ganesha export container per mount target, live mode only)
//	EFS_NFS_PORT_BASE                  22049
//	EFS_NFS_IMAGE                      registry.k8s.io/sig-storage/nfs-provisioner@sha256:c825f3d5… (digest-pinned)
//	OVERCAST_SMTP_MOCK                 true  (false when SMTP_HOST is set)
//	OVERCAST_SMTP_PORT                 1025  (0 = ephemeral. The default falls back to an ephemeral
//	                                           port when busy; any other value is pinned)
//	OVERCAST_SMTP_HOST                 ""    (set to use an external relay)
//	OVERCAST_SMTP_FROM                 overcast@localhost
//	OVERCAST_SMTP_USERNAME             ""
//	OVERCAST_SMTP_PASSWORD             ""
//	OVERCAST_SMTP_TLS                  false
//	OVERCAST_SMTP_INBOX_MAX            500
//	OVERCAST_INIT_ENABLED              true  (set false to disable init hooks)
//	OVERCAST_INIT_DIRS                 /etc/localstack/init,/etc/overcast/init
//	OVERCAST_INIT_TIMEOUT              30s   (per-script timeout)
//	OVERCAST_MCP_REMOTE_EXPOSURE       false
//	OVERCAST_MCP_AUTH_TOKEN            "" (required when OVERCAST_MCP_REMOTE_EXPOSURE=true)
//
// LocalStack-documented variables recognised but with no effect (#1190) —
// present but ignored rather than rejected, and logged once at startup (see
// ignoredLocalStackVars, IgnoredLocalStackReason):
//
//	SERVICES                           Overcast runs every service, always
//	LOCALSTACK_API_KEY                 no LocalStack Pro/auth-gated feature set to unlock
//	LOCALSTACK_AUTH_TOKEN              (same as LOCALSTACK_API_KEY)
func Load() (*Config, error) {
	cfg := &Config{}

	// Host — OVERCAST_LISTEN (OVERCAST_HOST removed, #870); the default
	// depends on whether Overcast is containerised (#761), so the
	// OVERCAST_DATA_DIR_SOURCE marker is read here, ahead of the data
	// directory section below, which reuses this same variable.
	cfg.LocalStackAliasesUsed = map[string]string{}
	dataDirSource := os.Getenv("OVERCAST_DATA_DIR_SOURCE")
	defaultListen, listenAutoSignal, listenAutoReason := resolveListenDefault(dataDirSource)
	rawListen, listenExplicit, err := resolveListen(defaultListen)
	if err != nil {
		return nil, err
	}

	// Port — resolved ahead of both the rest of Listen and Hostname below:
	// LOCALSTACK_HOST's optional ":port" suffix (#1190) and GATEWAY_LISTEN's
	// per-entry port are both checked against it, and OVERCAST_PORT itself
	// first passes through LocalStack's EDGE_PORT alias (the plain-port
	// analogue of GATEWAY_LISTEN).
	portRaw, edgePortAliasSource, err := resolveStringAlias(edgePortAlias, os.Getenv("OVERCAST_PORT"), "OVERCAST_PORT")
	if err != nil {
		return nil, err
	}

	// GATEWAY_LISTEN (#1190) may contribute to both Listen and Port at once.
	// Its comparison baseline for Listen is the raw OVERCAST_LISTEN value
	// only when it was set explicitly — the environment-dependent *default*
	// (e.g. native loopback) must never look like an explicit conflict, or
	// GATEWAY_LISTEN could never override it the way an explicit
	// OVERCAST_LISTEN would.
	listenRawForAlias := ""
	if listenExplicit {
		listenRawForAlias = rawListen
	}
	gwListen, gwPort, gatewayListenAliasSource, err := resolveGatewayListenAlias(listenRawForAlias, portRaw)
	if err != nil {
		return nil, err
	}
	portRaw = gwPort
	if gatewayListenAliasSource != "" {
		rawListen = gwListen
		listenExplicit = true
	}

	hosts, err := parseHosts(rawListen)
	if err != nil {
		return nil, err
	}
	cfg.Hosts = hosts
	cfg.Host = hosts[0]
	if listenExplicit {
		cfg.ListenSource = ListenSourceExplicit
	} else {
		cfg.ListenSource = ListenSourceAuto
		cfg.ListenAutoSignal = listenAutoSignal
		cfg.ListenAutoReason = listenAutoReason
	}
	if gatewayListenAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_LISTEN"] = gatewayListenAliasSource
	}

	portStr := orDefault(portRaw, "4566")
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("config: OVERCAST_PORT %q is not a valid port number", portStr)
	}
	cfg.Port = port
	if src := joinNonEmpty(edgePortAliasSource, gatewayListenAliasSource); src != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_PORT"] = src
	}

	// Hostname (external — for client-facing URLs) — OVERCAST_HOSTNAME, with
	// LocalStack's LOCALSTACK_HOST and legacy HOSTNAME_EXTERNAL (the name
	// LOCALSTACK_HOST replaced) accepted as compatibility aliases; see
	// resolveHostnameAlias for LOCALSTACK_HOST's hostname[:port] parsing and
	// conflict rules (#1190). Chaining HOSTNAME_EXTERNAL after
	// resolveHostnameAlias means all three spellings must agree when more
	// than one is set.
	hostname, hostnameAliasSource, err := resolveHostnameAlias(cfg.Port)
	if err != nil {
		return nil, err
	}
	hostnameCurrentLabel := orDefault(hostnameAliasSource, "OVERCAST_HOSTNAME")
	hostname, hostnameExternalAliasSource, err := resolveStringAlias(hostnameExternalAlias, hostname, hostnameCurrentLabel)
	if err != nil {
		return nil, err
	}
	cfg.Hostname = hostname
	cfg.HostnameAliasSource = joinNonEmpty(hostnameAliasSource, hostnameExternalAliasSource)

	// Split-horizon hostnames (extra names remapped to Overcast inside containers)
	for _, host := range strings.Split(os.Getenv("OVERCAST_SPLIT_HORIZON_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			cfg.SplitHorizonHosts = append(cfg.SplitHorizonHosts, host)
		}
	}

	// Data directory — resolved before the state backend below, since
	// OVERCAST_STATE=auto inspects it (mountpoint check, existing-database
	// check). dataDirEnvRaw is read raw (undefaulted) because detecting "was
	// this actually configured" requires knowing whether the env var was
	// empty before envOr fills in the default. dataDirSource was already read
	// above, for the OVERCAST_LISTEN default (#761) — reused here rather than
	// read twice. LocalStack's DATA_DIR (#1190) is accepted as an alias —
	// setting it counts as "explicitly configured" for the auto-state
	// detection below exactly as OVERCAST_DATA_DIR would.
	//
	// The Docker image bakes OVERCAST_DATA_DIR=/data as its own default,
	// marked by OVERCAST_DATA_DIR_SOURCE=image (see the ENV block in
	// Dockerfile). For the alias conflict rule that baked value is a default,
	// not user intent, so DATA_DIR overrides it rather than disagreeing with
	// it — `docker run -e DATA_DIR=/persist` against the image's own ENV must
	// not fail startup (the drop-in migration promise, #1190). Only when the
	// alias supplies nothing does the baked value carry through unchanged.
	dataDirNativeRaw := os.Getenv("OVERCAST_DATA_DIR")
	imageBakedDataDir := ""
	if isDockerImage(dataDirSource) {
		imageBakedDataDir = dataDirNativeRaw
		dataDirNativeRaw = ""
	}
	dataDirEnvRaw, dataDirAliasSource, err := resolveStringAlias(dataDirAlias, dataDirNativeRaw, "OVERCAST_DATA_DIR")
	if err != nil {
		return nil, err
	}
	// A compose file migrated from LocalStack mounts its state volume where
	// LocalStack's own documented compose puts it — /var/lib/localstack — and
	// says nothing about /data. Nothing then fails: the mount is simply not
	// where Overcast looks, OVERCAST_STATE=auto sees no volume at /data, and
	// the run is silently ephemeral. Adopting the mount is the whole fix, and
	// it is deliberately narrow — see adoptLocalStackVolume.
	if adopted := adoptLocalStackVolume(dataDirEnvRaw, imageBakedDataDir); adopted != "" {
		dataDirEnvRaw = adopted
		cfg.LocalStackVolumeDataDir = adopted
	}
	if dataDirEnvRaw == "" {
		dataDirEnvRaw = imageBakedDataDir
	}
	cfg.DataDir = orDefault(dataDirEnvRaw, defaultDataDir())
	if dataDirAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_DATA_DIR"] = dataDirAliasSource
	}
	// Defaults under the data dir, so an unset OVERCAST_CA_DIR keeps the
	// historical <data dir>/ca layout byte for byte.
	cfg.CADirConfigured = strings.TrimSpace(os.Getenv("OVERCAST_CA_DIR")) != ""
	cfg.CADir = envOr("OVERCAST_CA_DIR", filepath.Join(cfg.DataDir, "ca"))

	// State backend — accept "sqlite" as a deprecated alias for "persistent",
	// and "auto" (also the default when unset) as a request to resolve the
	// backend from evidence of persistence intent — see resolveAutoState.
	// LocalStack's PERSISTENCE=1 (#1190) maps to "persistent" — see
	// persistenceStateAlias.
	stateRaw, persistenceAliasSource, err := resolveStringAlias(persistenceStateAlias, os.Getenv("OVERCAST_STATE"), "OVERCAST_STATE")
	if err != nil {
		return nil, err
	}
	if persistenceAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_STATE"] = persistenceAliasSource
	}
	cfg.StateConfigured = strings.ToLower(strings.TrimSpace(stateRaw))
	if cfg.StateConfigured == "" {
		cfg.StateConfigured = "auto"
	}
	if cfg.StateConfigured == "sqlite" {
		cfg.StateConfigured = string(StateBackendPersistent)
	}
	if cfg.StateConfigured == "auto" {
		cfg.StateSource = StateSourceAuto
		sig := detectAutoStateSignals(cfg.DataDir, dataDirEnvRaw, dataDirSource, dataDirAliasSource)
		backend, signal, reason := resolveAutoState(sig)
		cfg.State = backend
		cfg.StateAutoSignal = signal
		cfg.StateAutoReason = reason
	} else {
		backend := StateBackend(cfg.StateConfigured)
		if err := validateStateBackend(backend, "OVERCAST_STATE"); err != nil {
			return nil, err
		}
		cfg.State = backend
		cfg.StateSource = StateSourceExplicit
	}

	// Hybrid flush interval
	flushStr := envOr("OVERCAST_HYBRID_FLUSH_INTERVAL", "5s")
	cfg.HybridFlushInterval, err = time.ParseDuration(flushStr)
	if err != nil {
		return nil, fmt.Errorf("config: OVERCAST_HYBRID_FLUSH_INTERVAL %q is not a valid duration", flushStr)
	}

	// Hybrid pending-log sync mode — mirrors the WAL sync settings below,
	// applied to the hybrid store's own pending log instead.
	cfg.HybridSyncMode = strings.ToLower(strings.TrimSpace(envOr("OVERCAST_HYBRID_SYNC", "interval")))
	switch cfg.HybridSyncMode {
	case "always", "interval", "never":
	default:
		return nil, fmt.Errorf("config: OVERCAST_HYBRID_SYNC must be 'always', 'interval', or 'never', got %q", cfg.HybridSyncMode)
	}
	hybridSyncIntervalStr := envOr("OVERCAST_HYBRID_SYNC_INTERVAL", "100ms")
	cfg.HybridSyncInterval, err = time.ParseDuration(hybridSyncIntervalStr)
	if err != nil || cfg.HybridSyncInterval <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_HYBRID_SYNC_INTERVAL %q is not a valid positive duration", hybridSyncIntervalStr)
	}

	// Hybrid size-triggered flush thresholds.
	cfg.HybridDirtyEntryThreshold = envInt("OVERCAST_HYBRID_DIRTY_ENTRY_THRESHOLD", 10000)
	hybridByteThresholdStr := envOr("OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD", "8388608")
	hybridByteThreshold, err := strconv.ParseInt(hybridByteThresholdStr, 10, 64)
	if err != nil || hybridByteThreshold <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_HYBRID_DIRTY_BYTE_THRESHOLD %q must be a positive integer", hybridByteThresholdStr)
	}
	cfg.HybridDirtyByteThreshold = hybridByteThreshold

	// Hybrid background maintenance loop interval (3.5: passive WAL
	// checkpoint + conditional incremental vacuum).
	maintenanceIntervalStr := envOr("OVERCAST_HYBRID_MAINTENANCE_INTERVAL", "5m")
	cfg.HybridMaintenanceInterval, err = time.ParseDuration(maintenanceIntervalStr)
	if err != nil || cfg.HybridMaintenanceInterval <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_HYBRID_MAINTENANCE_INTERVAL %q is not a valid positive duration", maintenanceIntervalStr)
	}

	// WAL settings
	cfg.WALFsyncMode = strings.ToLower(strings.TrimSpace(envOr("OVERCAST_WAL_FSYNC", "interval")))
	switch cfg.WALFsyncMode {
	case "always", "interval", "never":
	default:
		return nil, fmt.Errorf("config: OVERCAST_WAL_FSYNC must be 'always', 'interval', or 'never', got %q", cfg.WALFsyncMode)
	}
	walSyncIntervalStr := envOr("OVERCAST_WAL_FSYNC_INTERVAL", "100ms")
	cfg.WALFsyncInterval, err = time.ParseDuration(walSyncIntervalStr)
	if err != nil || cfg.WALFsyncInterval <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_WAL_FSYNC_INTERVAL %q is not a valid positive duration", walSyncIntervalStr)
	}
	walMaxLogBytesStr := envOr("OVERCAST_WAL_MAX_LOG_BYTES", "67108864")
	walMaxLogBytes, err := strconv.ParseInt(walMaxLogBytesStr, 10, 64)
	if err != nil || walMaxLogBytes <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_WAL_MAX_LOG_BYTES %q must be a positive integer", walMaxLogBytesStr)
	}
	cfg.WALMaxLogBytes = walMaxLogBytes

	// Per-service state overrides.
	//
	// Scanned out of the environment rather than probed per known service.
	// The probe form — building "OVERCAST_STATE_"+svc for each service and
	// reading that — cannot see a variable it does not think to construct, so
	// OVERCAST_STATE_CLOUDWATCH_LOGS (the plausible guess for a service whose
	// name is "logs"), or any typo, or a name that used to be right before a
	// rename, was silently ignored: no error, no warning, and the service
	// quietly kept using the global backend. Scanning means an override that
	// names nothing real is a startup error instead.
	serviceByEnvSuffix := make(map[string]string, len(allServices))
	for _, svc := range allServices {
		serviceByEnvSuffix[strings.ToUpper(strings.ReplaceAll(svc, "-", "_"))] = svc
	}

	const statePrefix = "OVERCAST_STATE_"
	cfg.ServiceStates = make(map[string]StateBackend)
	for _, entry := range os.Environ() {
		envKey, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(envKey, statePrefix) {
			continue
		}
		// An empty value means "unset", matching every other variable here.
		if value == "" {
			continue
		}
		svc, known := serviceByEnvSuffix[envKey[len(statePrefix):]]
		if !known {
			return nil, fmt.Errorf("config: %s does not name a known service (see the service names table in docs/configuration.md#service-names)", envKey)
		}

		raw := strings.ToLower(value)
		if raw == "sqlite" {
			raw = string(StateBackendPersistent)
		}
		svcBackend := StateBackend(raw)
		if err := validateStateBackend(svcBackend, envKey); err != nil {
			return nil, err
		}
		cfg.ServiceStates[svc] = svcBackend
	}

	// AWS identity defaults
	// LocalStack's DEFAULT_REGION (#1190) is accepted as a direct alias.
	regionRaw, regionAliasSource, err := resolveStringAlias(defaultRegionAlias, os.Getenv("OVERCAST_DEFAULT_REGION"), "OVERCAST_DEFAULT_REGION")
	if err != nil {
		return nil, err
	}
	cfg.Region = orDefault(regionRaw, "us-east-1")
	if regionAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_DEFAULT_REGION"] = regionAliasSource
	}
	// AWS account IDs are always exactly 12 ASCII digits. A non-conforming
	// override silently poisons every ARN and, since #1474, account-regional
	// S3 bucket names (<prefix>-<accountId>-<region>-an) minted from it —
	// those validate fine locally and are invalid on real AWS. Fail startup
	// rather than accept it, the same way the mode flags below do for their
	// own invalid values (#1478).
	cfg.AccountID = envOr("OVERCAST_ACCOUNT_ID", "000000000000")
	if !isTwelveDigitAccountID(cfg.AccountID) {
		return nil, fmt.Errorf(
			"config: OVERCAST_ACCOUNT_ID %q is invalid (expected exactly 12 ASCII digits)",
			cfg.AccountID)
	}

	// EKS mode
	rawEKSMode := strings.ToLower(strings.TrimSpace(envOr("OVERCAST_EKS_MODE", string(EKSModeMock))))
	cfg.EKSMode = EKSMode(rawEKSMode)
	if cfg.EKSMode != EKSModeMock && cfg.EKSMode != EKSModeLive {
		return nil, fmt.Errorf("config: OVERCAST_EKS_MODE %q is invalid (expected mock or live)", rawEKSMode)
	}

	// EFS mode. Live by default, unlike EKS: EKS live mode runs a k3s
	// container per cluster whether or not anything needs one, while EFS live
	// mode only creates a volume for a file system someone asked for, and
	// creates nothing at all when Docker is out of reach.
	rawEFSMode := strings.ToLower(strings.TrimSpace(envOr("OVERCAST_EFS_MODE", string(EFSModeLive))))
	cfg.EFSMode = EFSMode(rawEFSMode)
	if cfg.EFSMode != EFSModeMock && cfg.EFSMode != EFSModeLive {
		return nil, fmt.Errorf("config: OVERCAST_EFS_MODE %q is invalid (expected mock or live)", rawEFSMode)
	}

	// RDS mode. Live by default: a DB instance whose endpoint nothing answers
	// on is not much of a database, and live mode costs nothing where it cannot
	// be used — without a reachable daemon RDS is metadata-only anyway.
	rawRDSMode := strings.ToLower(strings.TrimSpace(envOr("OVERCAST_RDS_MODE", string(RDSModeLive))))
	cfg.RDSMode = RDSMode(rawRDSMode)
	if cfg.RDSMode != RDSModeMock && cfg.RDSMode != RDSModeLive {
		return nil, fmt.Errorf("config: OVERCAST_RDS_MODE %q is invalid (expected mock or live)", rawRDSMode)
	}

	// Container egress. Invalid values fail startup rather than falling back to
	// the default — this decides whether a function reaches real AWS, and a typo
	// that quietly restored the default answer is exactly the class of surprise
	// this setting exists to end.
	rawVPCEgress := strings.ToLower(strings.TrimSpace(
		envOr("OVERCAST_VPC_EGRESS", string(VPCEgressOpen))))
	cfg.VPCEgress = VPCEgressMode(rawVPCEgress)
	switch cfg.VPCEgress {
	case VPCEgressOpen, VPCEgressNone, VPCEgressRouted:
	default:
		return nil, fmt.Errorf(
			"config: OVERCAST_VPC_EGRESS %q is invalid (expected open, routed, or none)",
			rawVPCEgress)
	}

	// The egress-network pool for `routed`. Validated whatever the mode, so a
	// pool written against a `routed` deployment is not found to be malformed
	// only on the day the mode is switched.
	cfg.VPCEgressPool = strings.TrimSpace(envOr("OVERCAST_VPC_EGRESS_POOL", DefaultVPCEgressPool))
	if err := ValidateVPCEgressPool(cfg.VPCEgressPool); err != nil {
		return nil, fmt.Errorf("config: OVERCAST_VPC_EGRESS_POOL %q is invalid: %w", cfg.VPCEgressPool, err)
	}

	// Control-plane isolation (#1564), now an override on top of the mode above
	// rather than the egress control itself. Three values rather than a bool
	// because the third is "do not override". Invalid values fail startup
	// rather than falling back to auto — a typo that silently handed the
	// decision back to the mode is the same surprise in a new place.
	rawControlPlaneInternal, controlPlaneInternalSet := os.LookupEnv("OVERCAST_CONTROL_PLANE_INTERNAL")
	if !controlPlaneInternalSet || strings.TrimSpace(rawControlPlaneInternal) == "" {
		rawControlPlaneInternal = string(ControlPlaneInternalAuto)
		controlPlaneInternalSet = false
	}
	rawControlPlaneInternal = strings.ToLower(strings.TrimSpace(rawControlPlaneInternal))
	cfg.ControlPlaneInternal = ControlPlaneInternalMode(rawControlPlaneInternal)
	cfg.ControlPlaneInternalSet = controlPlaneInternalSet
	switch cfg.ControlPlaneInternal {
	case ControlPlaneInternalAuto, ControlPlaneInternalTrue, ControlPlaneInternalFalse:
	default:
		return nil, fmt.Errorf(
			"config: OVERCAST_CONTROL_PLANE_INTERNAL %q is invalid (expected auto, true, or false)",
			rawControlPlaneInternal)
	}

	// Service metrics collection (docs/plans/service-metrics-platform.md).
	// auto by default; auto and enabled currently behave identically because
	// CloudWatch is always wired (OVERCAST_SERVICES is inert).
	rawServiceMetrics := strings.ToLower(strings.TrimSpace(envOr("OVERCAST_SERVICE_METRICS", string(ServiceMetricsAuto))))
	cfg.ServiceMetrics = ServiceMetricsMode(rawServiceMetrics)
	if cfg.ServiceMetrics != ServiceMetricsAuto && cfg.ServiceMetrics != ServiceMetricsEnabled && cfg.ServiceMetrics != ServiceMetricsDisabled {
		return nil, fmt.Errorf("config: OVERCAST_SERVICE_METRICS %q is invalid (expected auto, enabled, or disabled)", rawServiceMetrics)
	}

	// SigV4 validation
	cfg.SigV4Validate = envBool("OVERCAST_SIGV4_VALIDATE", false)

	// Optional IAM enforcement middleware (default off). LocalStack's
	// ENFORCE_IAM is the same switch under another name — see enforceIAMAlias.
	enforceIAMRaw, enforceIAMAliasSource, err := resolveStringAlias(
		enforceIAMAlias, canonicalBool(os.Getenv("OVERCAST_ENFORCE_IAM")), "OVERCAST_ENFORCE_IAM")
	if err != nil {
		return nil, err
	}
	cfg.EnforceIAM = parseBoolOr(enforceIAMRaw, false)
	if enforceIAMAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_ENFORCE_IAM"] = enforceIAMAliasSource
	}

	// Optional API Gateway usage-plan throttle/quota rejection (default off:
	// limits are measured and reported, but never turned into a 429).
	cfg.EnforceAPIGatewayThrottle = envBool("OVERCAST_ENFORCE_APIGATEWAY_THROTTLE", false)
	// Optional AppSync Cognito JWT verification (default off: a bearer token
	// is required and decoded, but never verified).
	cfg.EnforceAppSyncCognitoAuth = envBool("OVERCAST_ENFORCE_APPSYNC_COGNITO_AUTH", false)

	// Optional Lambda resource-policy enforcement for service-originated
	// invocations (default off: statements are stored but never consulted).
	cfg.EnforceLambdaResourcePolicy = envBool("OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY", false)

	// Protocol drift strictness (default off — lenient "attempt anyway" posture).
	cfg.ProtocolStrict = envBool("OVERCAST_PROTOCOL_STRICT", false)

	// CloudFormation synchronous fast-path wait budget.
	cfg.CFNSyncWait = time.Duration(envInt("OVERCAST_CFN_SYNC_WAIT_MS", 1000)) * time.Millisecond
	if cfg.CFNSyncWait < 0 {
		cfg.CFNSyncWait = 0
	}

	// Step Functions runaway-execution guard. Executions run off the request
	// path, so this bounds the run only.
	sfnTimeoutStr := envOr("OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT", "15m")
	cfg.StepFunctionsExecutionTimeout, err = time.ParseDuration(sfnTimeoutStr)
	if err != nil {
		return nil, fmt.Errorf("config: OVERCAST_STEPFUNCTIONS_EXECUTION_TIMEOUT %q is not a duration: %w", sfnTimeoutStr, err)
	}
	if cfg.StepFunctionsExecutionTimeout < time.Second {
		cfg.StepFunctionsExecutionTimeout = time.Second
	}

	// Logging — LocalStack's DEBUG=1 (#1190) maps to OVERCAST_LOG_LEVEL=debug,
	// and LS_LOG names a level outright. LS_LOG is resolved second because
	// LocalStack documents it as overriding DEBUG; here that means the two
	// disagreeing (DEBUG=1 with LS_LOG=error) fails naming both, rather than
	// one silently winning.
	logLevelRaw, debugAliasSource, err := resolveStringAlias(debugLogLevelAlias, os.Getenv("OVERCAST_LOG_LEVEL"), "OVERCAST_LOG_LEVEL")
	if err != nil {
		return nil, err
	}
	logLevelLabel := "OVERCAST_LOG_LEVEL"
	if debugAliasSource != "" {
		logLevelLabel = debugAliasSource
	}
	logLevelRaw, lsLogAliasSource, err := resolveStringAlias(lsLogLevelAlias, logLevelRaw, logLevelLabel)
	if err != nil {
		return nil, err
	}
	cfg.LogLevel = strings.ToLower(orDefault(logLevelRaw, "info"))
	if source := joinNonEmpty(debugAliasSource, lsLogAliasSource); source != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_LOG_LEVEL"] = source
	}

	// Shutdown timeout
	timeoutStr := envOr("OVERCAST_SHUTDOWN_TIMEOUT", "5s")
	cfg.ShutdownTimeout, err = time.ParseDuration(timeoutStr)
	if err != nil {
		return nil, fmt.Errorf("config: OVERCAST_SHUTDOWN_TIMEOUT %q is not a valid duration", timeoutStr)
	}

	// The default data plane. Every container Overcast starts is reachable by
	// name here unless it belongs to a VPC, in which case it is reachable on
	// that VPC's network instead. ControlNetwork() derives the other plane.
	cfg.Network = envOr("OVERCAST_NETWORK", "overcast")

	// Lambda container runtime. LAMBDA_DOCKER_SOCKET wins; absent it,
	// DOCKER_HOST — the Docker CLI's own convention, which every daemon not
	// living at the platform default tells its users to set — supplies the
	// endpoint. See docker_host.go.
	dockerEndpoint, dockerEndpointSource, dockerHostUnsupported := resolveDockerEndpoint()
	cfg.LambdaDockerSocket = envOr("LAMBDA_DOCKER_SOCKET", dockerEndpoint)
	if os.Getenv("LAMBDA_DOCKER_SOCKET") == "" {
		cfg.DockerSocketSource = dockerEndpointSource
		cfg.DockerHostUnsupported = dockerHostUnsupported
	}
	// 0 is a real value here (an ephemeral port), so only the range is
	// checked: a port that cannot exist would otherwise surface as a bind
	// failure after the Docker probe, with the container runtime disabled.
	cfg.LambdaRuntimeAPIPort = envInt("LAMBDA_RUNTIME_API_PORT", DefaultLambdaRuntimeAPIPort)
	if cfg.LambdaRuntimeAPIPort < 0 || cfg.LambdaRuntimeAPIPort > 65535 {
		return nil, fmt.Errorf("config: LAMBDA_RUNTIME_API_PORT %d is not a valid port number (0-65535; 0 = ephemeral)", cfg.LambdaRuntimeAPIPort)
	}
	// "auto" is the default and means "probe it"; anything else is an address
	// taken as given. Normalised to "" here so every reader tests one thing.
	//
	// Validated at Load like the port above it, and for the same reason: the
	// value is joined to a port and handed to every Lambda container, so a bad
	// one is not caught until a function fails to initialise. "host:port" is
	// the easy mistake — the docs and the startup log both render the result as
	// `host.docker.internal:9001` — and net.JoinHostPort would turn it into
	// `host.docker.internal:9001:9001`, advertised to every container, baked
	// into AWS_ENDPOINT_URL, and reported by /_overcast/health as healthy.
	cfg.LambdaRuntimeAPIHost = strings.TrimSpace(envOr("LAMBDA_RUNTIME_API_HOST", "auto"))
	if strings.EqualFold(cfg.LambdaRuntimeAPIHost, "auto") {
		cfg.LambdaRuntimeAPIHost = ""
	}
	if err := validateRuntimeAPIHost(cfg.LambdaRuntimeAPIHost); err != nil {
		return nil, err
	}
	// For the three derivable limits, 0 is a sentinel meaning "unset — derive
	// from the Docker host when the Lambda runtime initialises" (see
	// internal/services/lambda/host_limits.go). A negative or zero env value is
	// normalised to the sentinel rather than clamped to 1: pinning a limit
	// means setting a positive integer.
	cfg.LambdaDockerMaxConcurrentStarts = envInt("LAMBDA_DOCKER_MAX_CONCURRENT_STARTS", 0)
	if cfg.LambdaDockerMaxConcurrentStarts < 0 {
		cfg.LambdaDockerMaxConcurrentStarts = 0
	}
	cfg.LambdaMaxInstances = envInt("LAMBDA_MAX_INSTANCES", 0)
	if cfg.LambdaMaxInstances < 0 {
		cfg.LambdaMaxInstances = 0
	}
	cfg.LambdaMaxInstancesPerFunction = envInt("LAMBDA_MAX_INSTANCES_PER_FUNCTION", 0)
	if cfg.LambdaMaxInstancesPerFunction < 0 {
		cfg.LambdaMaxInstancesPerFunction = 0
	}
	cfg.LambdaMaxMemoryMB = envInt("LAMBDA_MAX_MEMORY_MB", 0)
	if cfg.LambdaMaxMemoryMB < 0 {
		cfg.LambdaMaxMemoryMB = 0
	}
	cfg.LambdaMaxWarmInstances = envInt("LAMBDA_MAX_WARM_INSTANCES", 10)
	if cfg.LambdaMaxWarmInstances < 1 {
		cfg.LambdaMaxWarmInstances = 1
	}
	cfg.LambdaSeedRuntimeImages = envBool("LAMBDA_SEED_RUNTIME_IMAGES", false)
	// LocalStack's LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT (#1190) is accepted as
	// an alias for LAMBDA_INIT_TIMEOUT_SECONDS — the same concept, different
	// name, both plain integer seconds.
	lambdaInitRaw, lambdaInitAliasSource, err := resolveStringAlias(lambdaInitTimeoutAlias, os.Getenv("LAMBDA_INIT_TIMEOUT_SECONDS"), "LAMBDA_INIT_TIMEOUT_SECONDS")
	if err != nil {
		return nil, err
	}
	cfg.LambdaInitTimeout = time.Duration(parseIntOr(lambdaInitRaw, 10)) * time.Second
	if cfg.LambdaInitTimeout <= 0 {
		cfg.LambdaInitTimeout = 10 * time.Second
	}
	if lambdaInitAliasSource != "" {
		cfg.LocalStackAliasesUsed["LAMBDA_INIT_TIMEOUT_SECONDS"] = lambdaInitAliasSource
	}
	// LocalStack's LAMBDA_REMOVE_CONTAINERS is this switch with the opposite
	// polarity, and the same default — see lambdaRemoveContainersAlias.
	keepContainersRaw, keepContainersAliasSource, err := resolveStringAlias(
		lambdaRemoveContainersAlias, canonicalBool(os.Getenv("LAMBDA_KEEP_CONTAINERS")), "LAMBDA_KEEP_CONTAINERS")
	if err != nil {
		return nil, err
	}
	cfg.LambdaKeepContainers = parseBoolOr(keepContainersRaw, false)
	if keepContainersAliasSource != "" {
		cfg.LocalStackAliasesUsed["LAMBDA_KEEP_CONTAINERS"] = keepContainersAliasSource
	}
	cfg.LambdaTarCacheMB = envInt("LAMBDA_TAR_CACHE_MB", 256)
	if cfg.LambdaTarCacheMB < 0 {
		cfg.LambdaTarCacheMB = 0
	}
	cfg.LambdaProactiveInit = envBool("LAMBDA_PROACTIVE_INIT", true)
	// Per-service hot reload inherits the umbrella when its own variable is
	// unset, and overrides it when set — including opting a single service out
	// of an umbrella true, since envBool returns false for an explicit "false"
	// and the fallback only for an absent variable.
	cfg.HotReload = envBool("OVERCAST_HOT_RELOAD", false)
	cfg.LambdaHotReload = envBool("OVERCAST_LAMBDA_HOT_RELOAD", cfg.HotReload)

	// The debugger follows the same umbrella-then-override shape, and its
	// listen address follows Host so the containerised-vs-native decision
	// (#761) is made exactly once, above, and never repeated here.
	cfg.Debugger = envBool("OVERCAST_DEBUGGER", false)
	cfg.LambdaDebugger = envBool("OVERCAST_LAMBDA_DEBUGGER", cfg.Debugger)
	cfg.ECSDebugger = envBool("OVERCAST_ECS_DEBUGGER", cfg.Debugger)
	cfg.DebuggerListen, err = resolveDebuggerListen(envOr("OVERCAST_DEBUGGER_LISTEN", cfg.Host))
	if err != nil {
		return nil, err
	}
	cfg.DebuggerPorts, err = parseDebuggerPorts(envOr("OVERCAST_DEBUGGER_PORTS", defaultDebuggerPorts))
	if err != nil {
		return nil, err
	}
	cfg.DebuggerTimeout, err = ParseDebuggerTimeoutPolicy(os.Getenv("OVERCAST_DEBUGGER_TIMEOUT"))
	if err != nil {
		return nil, err
	}
	waitTimeoutStr := envOr("OVERCAST_DEBUGGER_WAIT_TIMEOUT", defaultDebuggerWaitTimeout)
	cfg.DebuggerWaitTimeout, err = time.ParseDuration(waitTimeoutStr)
	if err != nil || cfg.DebuggerWaitTimeout <= 0 {
		return nil, fmt.Errorf("config: OVERCAST_DEBUGGER_WAIT_TIMEOUT %q is not a positive duration", waitTimeoutStr)
	}

	cfg.LambdaFetchRemoteLayers = envBool("LAMBDA_FETCH_REMOTE_LAYERS", false)
	cfg.LambdaLayerCacheDir = envOr("LAMBDA_LAYER_CACHE_DIR", "")
	cfg.LambdaRemoteAWSAccessKeyID = envOr("LAMBDA_REMOTE_AWS_ACCESS_KEY_ID", "")
	cfg.LambdaRemoteAWSSecretAccessKey = envOr("LAMBDA_REMOTE_AWS_SECRET_ACCESS_KEY", "")
	cfg.LambdaRemoteAWSSessionToken = envOr("LAMBDA_REMOTE_AWS_SESSION_TOKEN", "")

	// Container-facing resolver for the split-horizon hostnames. On by default:
	// out of the box a Lambda must be able to reach Overcast by every name
	// Overcast advertises, including the subdomain forms /etc/hosts cannot
	// express. Failing to bind is not fatal — see DNSListening.
	// LocalStack's DNS_ADDRESS=0 is the documented way to turn its resolver
	// off, and maps here — see dnsAddressAlias for why other values do not.
	dnsEnabledRaw, dnsAliasSource, err := resolveStringAlias(
		dnsAddressAlias, canonicalBool(os.Getenv("OVERCAST_DNS")), "OVERCAST_DNS")
	if err != nil {
		return nil, err
	}
	cfg.DNSEnabled = parseBoolOr(dnsEnabledRaw, true)
	if dnsAliasSource != "" {
		cfg.LocalStackAliasesUsed["OVERCAST_DNS"] = dnsAliasSource
	}
	cfg.DNSPort = envInt("OVERCAST_DNS_PORT", 53)

	// ECS container runtime — defaults fall back to Lambda socket
	cfg.ECSDockerSocket = envOr("ECS_DOCKER_SOCKET", cfg.LambdaDockerSocket)
	cfg.ECSKeepContainers = envBool("ECS_KEEP_CONTAINERS", false)
	cfg.ECSHotReload = envBool("OVERCAST_ECS_HOT_RELOAD", cfg.HotReload)

	// ECR registry — see the field's comment for why the default is pinned.
	cfg.ECRRegistryPort = envInt("OVERCAST_ECR_REGISTRY_PORT", 4510)
	cfg.ECRRegistryPersist = envBool("OVERCAST_ECR_REGISTRY_PERSIST", true)

	// RDS container runtime — defaults fall back to Lambda socket
	cfg.RDSDockerSocket = envOr("RDS_DOCKER_SOCKET", cfg.LambdaDockerSocket)
	cfg.RDSPortBase = envInt("RDS_PORT_BASE", 33060)
	cfg.RDSKeepContainers = envBool("RDS_KEEP_CONTAINERS", false)

	// ElastiCache container runtime — defaults fall back to Lambda socket
	cfg.ElastiCacheDockerSocket = envOr("ELASTICACHE_DOCKER_SOCKET", cfg.LambdaDockerSocket)
	cfg.ElastiCachePortBase = envInt("ELASTICACHE_PORT_BASE", 63790)
	cfg.ElastiCacheKeepContainers = envBool("ELASTICACHE_KEEP_CONTAINERS", false)

	// MSK container runtime — defaults fall back to Lambda socket
	cfg.MSKDockerSocket = envOr("MSK_DOCKER_SOCKET", cfg.LambdaDockerSocket)
	cfg.MSKPortBase = envInt("MSK_PORT_BASE", 49092)
	cfg.MSKKeepContainers = envBool("MSK_KEEP_CONTAINERS", false)

	// EKS live-mode container runtime — defaults fall back to Lambda socket
	cfg.EKSDockerSocket = envOr("EKS_DOCKER_SOCKET", cfg.LambdaDockerSocket)

	// EFS live-mode volume runtime — defaults fall back to Lambda socket
	cfg.EFSDockerSocket = envOr("EFS_DOCKER_SOCKET", cfg.LambdaDockerSocket)
	cfg.EFSNFSExport = envBool("OVERCAST_EFS_NFS", false)
	cfg.EFSNFSPortBase = envInt("EFS_NFS_PORT_BASE", 22049)
	cfg.EFSNFSImage = envOr("EFS_NFS_IMAGE", DefaultEFSNFSImage)

	// EC2 VPC network strategy — unknown values fall back to "shared" at
	// service construction with a logged warning. "netns" is explicitly
	// rejected here because that strategy is not implemented yet.
	cfg.EC2VPCNetworkStrategy = envOr("OVERCAST_EC2_VPC_STRATEGY", "shared")
	if strings.EqualFold(strings.TrimSpace(cfg.EC2VPCNetworkStrategy), "netns") {
		return nil, fmt.Errorf("config: OVERCAST_EC2_VPC_STRATEGY=netns is not supported yet; use shared, strict, or remapped")
	}

	// Debug endpoints
	cfg.Debug = envBool("OVERCAST_DEBUG", false)
	cfg.DebugTraceBuffer = envInt("OVERCAST_DEBUG_TRACE_BUFFER", 1000)
	cfg.DebugTraceCeiling = envInt("OVERCAST_DEBUG_TRACE_CEILING", 10000)
	cfg.DebugTracePinned = envInt("OVERCAST_DEBUG_TRACE_PINNED", 1000)
	cfg.DebugTraceBytes = int64(envInt("OVERCAST_DEBUG_TRACE_BYTES_MB", 512)) << 20
	traceWindowStr := envOr("OVERCAST_DEBUG_TRACE_WINDOW", "1h")
	cfg.DebugTraceWindow, err = time.ParseDuration(traceWindowStr)
	if err != nil {
		return nil, fmt.Errorf("config: OVERCAST_DEBUG_TRACE_WINDOW %q is not a valid duration", traceWindowStr)
	}

	// TLS
	cfg.TLSCertFile = os.Getenv("OVERCAST_TLS_CERT")
	cfg.TLSKeyFile = os.Getenv("OVERCAST_TLS_KEY")
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return nil, fmt.Errorf("config: OVERCAST_TLS_CERT and OVERCAST_TLS_KEY must both be set or both be empty")
	}
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv("OVERCAST_TLS"))); mode {
	case "", "off", "false", "0":
		cfg.TLSMode = ""
	case TLSModeAuto:
		cfg.TLSMode = TLSModeAuto
	default:
		return nil, fmt.Errorf("config: OVERCAST_TLS must be 'auto' or unset, got %q", mode)
	}
	if cfg.TLSAuto() && (cfg.TLSCertFile != "" || cfg.TLSKeyFile != "") {
		return nil, fmt.Errorf("config: OVERCAST_TLS=auto and OVERCAST_TLS_CERT/OVERCAST_TLS_KEY are mutually exclusive — use one or the other")
	}

	// SMTP
	cfg.SMTPHost = os.Getenv("OVERCAST_SMTP_HOST")
	cfg.SMTPFrom = envOr("OVERCAST_SMTP_FROM", "overcast@localhost")
	cfg.SMTPUsername = os.Getenv("OVERCAST_SMTP_USERNAME")
	cfg.SMTPPassword = os.Getenv("OVERCAST_SMTP_PASSWORD")
	cfg.SMTPTLS = envBool("OVERCAST_SMTP_TLS", false)

	smtpPortStr := envOr("OVERCAST_SMTP_PORT", strconv.Itoa(DefaultSMTPPort))
	smtpPort, err := strconv.Atoi(smtpPortStr)
	if err != nil || smtpPort < 0 || smtpPort > 65535 {
		return nil, fmt.Errorf("config: OVERCAST_SMTP_PORT %q is not a valid port number (0-65535; 0 = ephemeral)", smtpPortStr)
	}
	cfg.SMTPPort = smtpPort

	smtpInboxMaxStr := envOr("OVERCAST_SMTP_INBOX_MAX", "500")
	smtpInboxMax, err := strconv.Atoi(smtpInboxMaxStr)
	if err != nil || smtpInboxMax < 1 {
		return nil, fmt.Errorf("config: OVERCAST_SMTP_INBOX_MAX %q must be a positive integer", smtpInboxMaxStr)
	}
	cfg.SMTPInboxMax = smtpInboxMax

	// Mock mode: default true unless an external host is configured.
	if cfg.SMTPHost != "" {
		cfg.SMTPMock = false
	} else {
		cfg.SMTPMock = envBool("OVERCAST_SMTP_MOCK", true)
	}

	// Init hooks
	cfg.InitEnabled = envBool("OVERCAST_INIT_ENABLED", true)

	initDirsStr := envOr("OVERCAST_INIT_DIRS", "/etc/localstack/init,/etc/overcast/init")
	for _, d := range strings.Split(initDirsStr, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			cfg.InitDirs = append(cfg.InitDirs, d)
		}
	}

	initTimeoutStr := envOr("OVERCAST_INIT_TIMEOUT", "30s")
	cfg.InitTimeout, err = time.ParseDuration(initTimeoutStr)
	if err != nil {
		return nil, fmt.Errorf("config: OVERCAST_INIT_TIMEOUT %q is not a valid duration", initTimeoutStr)
	}

	cfg.MCPRemoteExposure = envBool("OVERCAST_MCP_REMOTE_EXPOSURE", false)
	cfg.MCPAuthToken = strings.TrimSpace(os.Getenv("OVERCAST_MCP_AUTH_TOKEN"))
	if cfg.MCPRemoteExposure && cfg.MCPAuthToken == "" {
		return nil, fmt.Errorf("config: OVERCAST_MCP_AUTH_TOKEN is required when OVERCAST_MCP_REMOTE_EXPOSURE=true")
	}

	// LocalStack-documented variables Overcast recognises but that have no
	// effect (#1190) — see ignoredLocalStackVars and IgnoredLocalStackReason.
	cfg.IgnoredLocalStackVars = detectIgnoredLocalStackVars()

	return cfg, nil
}

// isTwelveDigitAccountID reports whether s is exactly 12 ASCII digits, the
// only shape a real AWS account ID ever takes. A byte-range check rather than
// a regexp or unicode.IsDigit: this is a wire-format constraint, not a
// locale-aware one, so a fullwidth or non-Latin digit must not pass.
func isTwelveDigitAccountID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// envOr returns the value of the named environment variable, or fallback if
// the variable is unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool parses a boolean environment variable. Accepts "true", "1", "yes".
func envBool(key string, fallback bool) bool {
	v := strings.ToLower(os.Getenv(key))
	switch v {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}

// envInt parses an integer environment variable, returning fallback if the
// variable is unset, empty, or not a valid integer.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// defaultDataDir returns the path where persistent state is stored.
//
// Inside a dev container (detected via OVERCAST_DEVCONTAINER or the presence
// of /workspace) the data directory is placed inside the workspace mount so
// that it survives container rebuilds. Outside a dev container it falls back
// to ~/.overcast/data, mirroring LocalStack's DATA_DIR convention.
func defaultDataDir() string {
	// Dev container: /workspace is bind-mounted from the host.
	if _, err := os.Stat("/workspace"); err == nil {
		return "/workspace/.overcast/data"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "overcast", "data")
	}
	return filepath.Join(home, ".overcast", "data")
}

// validateStateBackend returns an error if b is not one of the four recognised
// concrete storage modes. envKey is included in the error message for
// context. Callers peel off "auto" (and the "sqlite" alias) before reaching
// here, so this only ever validates a concrete backend name — for the global
// OVERCAST_STATE var specifically, "auto" is also accepted upstream and is
// mentioned in the error for completeness even though this function itself
// never returns nil for it.
func validateStateBackend(b StateBackend, envKey string) error {
	switch b {
	case StateBackendMemory, StateBackendPersistent, StateBackendHybrid, StateBackendWAL:
		return nil
	default:
		if envKey == "OVERCAST_STATE" {
			return fmt.Errorf("config: %s must be 'auto', 'memory', 'persistent', 'hybrid' or 'wal', got %q", envKey, b)
		}
		return fmt.Errorf("config: %s must be 'memory', 'persistent', 'hybrid' or 'wal', got %q", envKey, b)
	}
}

// validateRuntimeAPIHost checks LAMBDA_RUNTIME_API_HOST names a bare address:
// an IP literal, or a hostname. No scheme, no port, no path, no whitespace.
//
// The rejected forms are all things somebody would plausibly type — the log
// line and the docs show the address with its port attached, a URL is the other
// obvious guess — and every one of them is silently wrong rather than loudly
// wrong once it reaches net.JoinHostPort. An empty value is "auto" and is fine.
func validateRuntimeAPIHost(host string) error {
	if host == "" {
		return nil
	}
	const fix = `set LAMBDA_RUNTIME_API_HOST to a bare address — an IP such as "192.168.1.20", or a name such as "host.docker.internal" — with no scheme, port or path, or to "auto" to have Overcast establish it`
	switch {
	case strings.ContainsAny(host, " \t\r\n"):
		return fmt.Errorf("config: LAMBDA_RUNTIME_API_HOST %q contains whitespace; %s", host, fix)
	case strings.Contains(host, "://"), strings.Contains(host, "/"):
		return fmt.Errorf("config: LAMBDA_RUNTIME_API_HOST %q looks like a URL, not a host; %s", host, fix)
	}
	if ip := net.ParseIP(host); ip != nil {
		// An IPv6 literal is a legitimate address and carries colons of its
		// own, so the port check below must not see it.
		return nil
	}
	if strings.Contains(host, ":") {
		return fmt.Errorf("config: LAMBDA_RUNTIME_API_HOST %q includes a port; the port is LAMBDA_RUNTIME_API_PORT, and this is joined to it — %s", host, fix)
	}
	if !isHostname(host) {
		return fmt.Errorf("config: LAMBDA_RUNTIME_API_HOST %q is not a valid hostname or IP address; %s", host, fix)
	}
	return nil
}

// isHostname reports whether s is a plausible DNS name: dot-separated labels of
// letters, digits and hyphens, none empty, none starting or ending with a
// hyphen. Deliberately not a full RFC 1123 parser — this is here to catch typos
// before they reach a container, not to police a resolver.
func isHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
