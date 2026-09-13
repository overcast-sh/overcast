package router

import (
	"fmt"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/containerendpoint"
	"github.com/overcast-sh/overcast/internal/dataplane"
	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/state"
)

// AdvisorySeverity is the severity of an Advisory: one of the constants below.
// The web UI (see web/src/features/metrics) renders these generically by
// severity — icon/color only — so adding a value here requires a matching UI
// mapping, unlike adding a new *rule*, which requires no UI change at all (see
// computeAdvisories's doc comment). cmd/tsgen renders the typed constants as
// the TypeScript union, which is what makes the UI's mapping type-checked
// against this list.
type AdvisorySeverity = string

const (
	advisorySeverityInfo     AdvisorySeverity = "info"
	advisorySeverityWarning  AdvisorySeverity = "warning"
	advisorySeverityCritical AdvisorySeverity = "critical"
)

// Advisory codes — stable identifiers a caller (or a future test) can key
// off of without parsing Title/Detail text.
const (
	advisoryCodeJournalModeNotWAL         = "journal-mode-not-wal"
	advisoryCodeStoreDegradedMemoryOnly   = "store-degraded-memory-only"
	advisoryCodeStoreUnhealthy            = "store-unhealthy"
	advisoryCodeDataDirSlowFilesystem     = "data-dir-slow-filesystem"
	advisoryCodeReadPressureObserved      = "read-pressure-observed"
	advisoryCodeMemoryMode                = "memory-mode"
	advisoryCodeMemoryModeIgnoresExisting = "memory-mode-ignores-existing-database"
	advisoryCodeNetworkStateMismatch      = "network-state-mismatch"
	advisoryCodeVPCNetworkIsolationStale  = "vpc-network-isolation-stale"
	advisoryCodeVPCNetworkUnbacked        = "vpc-network-unbacked"
	advisoryCodeEgressNotWithheld         = "vpc-egress-not-withheld"
	advisoryCodeDataPlaneNameRefused      = "data-plane-name-refused"
	advisoryCodeLambdaInitVolumeForeign   = "lambda-init-volume-foreign"
	advisoryCodeRuntimeAPIUnreachable     = "lambda-runtime-api-unreachable"
)

// networkingDocsPath points the network-state advisory at the page that
// explains what is verified and what `overcast network reset` does.
const networkingDocsPath = "networking/network-state.md"

// performanceDocsPath is the docs-browser-relative path (see
// web/src/routes/docs.tsx's `path` search param, and internal/bff/bff.go's
// handleDocsPage, both of which resolve paths relative to the embedded docs/
// directory — cmd/overcast/embed_web.go roots docsFS at "docs") for the
// storage tuning guide. Every advisory whose remediation is "move your data
// dir" or "your storage backend needs attention" points here, per the
// project's docs-reorganization guidance that user-facing tuning content
// stays under docs/performance/.
const performanceDocsPath = "performance/storage-tuning.md"

// dataDirDocsPath deep-links the slow-filesystem advisory straight to the
// relevant section. The fragment is the heading's id — GitHub's slug, which
// internal/docsindex.Slug and web/src/lib/slug.ts both produce: lowercase,
// punctuation dropped, each space a hyphen, so the " — " in "Data dir
// placement — avoid host bind mounts on Docker Desktop"
// (docs/performance/storage-tuning.md)
// is the "--". web/src/routes/docs.slug.test.ts pins this literal to that
// heading; the UI splits DocsPath on "#" into path + hash.
const dataDirDocsPath = performanceDocsPath + "#data-dir-placement--avoid-host-bind-mounts-on-docker-desktop"

// storageDocsPath is the docs-browser-relative path (see performanceDocsPath
// for how these resolve) for the storage backends guide — the page that
// compares the four backends by durability and explains what OVERCAST_STATE
// accepts.
const storageDocsPath = "storage.md"

// noSQLiteDocsPath deep-links the no-SQLite memory-mode advisory to the
// section spelling out the whole story: which published artifacts are built
// -tags nosqlite, why auto can only ever land on memory there, and the
// volume-plus-OVERCAST_STATE=wal recipe for durability. The fragment is the
// docs browser's heading slug for "Builds without SQLite" — see
// dataDirDocsPath for how that slug is derived.
const noSQLiteDocsPath = storageDocsPath + "#builds-without-sqlite"

// vpcNetworkDocsPath deep-links the stale-isolation advisory to the section
// of the VPC backing page that explains what a gateway does to a VPC's Docker
// network and what a failed flip leaves behind. The fragment is the docs
// browser's slug for that heading — see dataDirDocsPath.
const vpcNetworkDocsPath = "networking/vpc-backing.md#internet-gateways-and-isolation"

// vpcNetworkUnbackedDocsPath deep-links the unbacked-VPC advisory to the
// section of the same page that says what a refused create leaves behind and
// what frees the address range the daemon refused. The fragment is the docs
// browser's slug for that heading — see dataDirDocsPath.
const vpcNetworkUnbackedDocsPath = "networking/vpc-backing.md#when-a-network-cannot-be-created"

// vpcsDocsPath points the data-plane-name-refused advisory at the page that
// says what a VPC restricts, what "refused" means, and the three ways out.
const vpcsDocsPath = "networking/vpcs.md"

// egressModeDocsPath points the egress advisory at the page that explains what
// each mode can and cannot deliver, and on which hosts. It lands on the modes
// page rather than on `routed`'s own, because `none` and `routed` fail on the
// same host for the same reason — see checkEgressNotWithheld, which is one rule
// for the same reason.
const egressModeDocsPath = "networking/egress.md"

// lambdaInitVolumeDocsPath deep-links the lambda-init-volume-foreign advisory
// to the section of the Lambda execution environments page explaining why a foreign
// init volume is safe to reuse but only its own creating instance can prune
// or remove it. The fragment is the docs browser's slug for that
// heading — see dataDirDocsPath.
const lambdaInitVolumeDocsPath = "services/lambda/execution-environments.md#init-delivery-is-shared-across-instances"

// Advisory is one actionable diagnostic surfaced alongside the storage
// diagnostics in GET /_overcast/debug/metrics (see debugMetricsResponse.Advisories).
// The web UI's Metrics & Health page renders these generically — icon/color
// from Severity, then Title/Detail/optional DocsPath link — so adding a
// future rule to computeAdvisories never requires a web UI change.
type Advisory struct {
	// Severity is one of "info", "warning", "critical".
	Severity AdvisorySeverity `json:"severity"`
	// Code is a stable, machine-readable identifier for this advisory rule
	// (e.g. "journal-mode-not-wal") — safe to key UI behavior or tests off
	// of, unlike Title/Detail, which are free text that may be reworded.
	Code string `json:"code"`
	// Title is a short, human-readable summary (fits in a single UI line).
	Title string `json:"title"`
	// Detail is a longer explanation, including any concrete numbers/values
	// relevant to this specific occurrence (e.g. the live journal mode
	// actually observed, or how many reads were affected).
	Detail string `json:"detail"`
	// DocsPath, when non-empty, is a docs-browser-relative path (see
	// performanceDocsPath) the web UI can open for remediation guidance.
	DocsPath string `json:"docsPath,omitempty"`
}

// advisoryInput bundles exactly the data computeAdvisories needs — all of it
// already computed by the debugMetrics handler for the rest of the payload,
// nothing fetched specially for advisories. Keeping this as a plain struct
// (rather than passing cfg/store directly) keeps every rule function a pure,
// table-testable function of fake data — no real Store or Config required.
type advisoryInput struct {
	// StateBackend is cfg.State, the globally configured storage backend —
	// drives the memory-mode rule. Deliberately the global setting only, not
	// cfg.ServiceStates per-service overrides: a mixed-backend fleet (most of
	// it hybrid, one service forced to memory via
	// OVERCAST_STATE_<SERVICE>=memory) is an intentional, informed choice a
	// per-service override implies, not the "did you mean to do this"
	// signal this rule exists to catch.
	StateBackend config.StateBackend

	// StateSource reports whether StateBackend was chosen explicitly by the
	// user or resolved by the OVERCAST_STATE=auto heuristic — drives which
	// wording variant checkMemoryMode uses when StateBackend is memory. See
	// config.StateSource's doc comment.
	StateSource config.StateSource

	// SQLiteAvailable is config.SQLiteSupported() — false in a -tags nosqlite
	// build (the overcast-slim image and the overcastd binaries). It selects
	// checkMemoryMode's third wording variant, because in such a build every
	// remediation the ordinary auto wording suggests is wrong: mounting a
	// volume changes nothing (config.resolveAutoState short-circuits to memory
	// before weighing any signal) and OVERCAST_STATE=hybrid stops the daemon
	// from starting at all (state.NewHybridStore is stubbed out to return an
	// error — see internal/state/sqlite_hybrid_nosqlite.go).
	SQLiteAvailable bool

	// Stores is one entry per distinct underlying store, exactly as
	// state.DebugMetricsSnapshot returns it — drives every per-store rule
	// (journal-mode-not-wal, store-degraded-memory-only,
	// data-dir-slow-filesystem, read-pressure-observed). A NamespacedStore
	// with mixed backends naturally yields one advisory per affected store,
	// same as the underlying DebugMetrics payload.
	Stores []state.DebugMetrics

	// Health is the aggregated persistent-backend health across every
	// underlying store (state.PersistentHealthSnapshot) — drives
	// store-unhealthy. HasHealth false means no store reports health at all
	// (e.g. every backend is memory-only), matching
	// PersistentHealthSnapshot's own contract exactly.
	Health    state.PersistentHealth
	HasHealth bool

	// ExistingDatabase is config.HasExistingDatabase(cfg.DataDir) — true when
	// an overcast.db or overcast.wal file already sits in the data directory.
	// Drives memory-mode-ignores-existing-database: memory mode is otherwise
	// unremarkable (see checkMemoryMode), but a database already sitting one
	// directory away that this run will neither read nor update is the sharp
	// exception, and the one case worth an extra, stronger advisory for.
	ExistingDatabase bool

	// Networks is the verified state of every Docker network Overcast manages
	// — the two planes and each per-VPC network — exactly as
	// /_overcast/health reports it. Drives network-state-mismatch, and is the
	// first advisory input that is not about storage.
	//
	// Empty means Docker is not wired, not that everything is well. The rule
	// below therefore fires on a mismatch and never on an absence: "no
	// networks reported" is indistinguishable from "no containers on this
	// machine", and an advisory that cannot tell them apart is noise.
	Networks []docker.NetworkStatus
	// VPCNetworkProblems is ec2.Service.NetworkProblems(): the VPCs whose
	// Docker network the EC2 service could not bring to the isolation their
	// internet-gateway state calls for — drives vpc-network-isolation-stale.
	// Nil whenever EC2 is not wired (a service subset) or nothing is wrong.
	VPCNetworkProblems []dataplane.VPCNetworkProblem

	// DNSRefusals is dataplane.Guard.Recent(): the data-plane names the
	// resolver declined to answer because the caller shares no network with
	// the container advertising them — drives data-plane-name-refused. Nil
	// whenever the resolver or its guard is not running, or nothing was
	// refused.
	DNSRefusals []dataplane.Refusal

	// LambdaInitVolumeProblems is lambda.Service.InitVolumeProblems(): init
	// volumes matching this build's content hash that this instance reused
	// but does not own, per docker.LabelInstance — drives
	// lambda-init-volume-foreign. Nil whenever Lambda is not wired, Docker
	// has not been probed yet, or nothing was found.
	LambdaInitVolumeProblems []docker.VolumeOwnershipProblem
	// RuntimeAPI is how the Lambda Runtime API address was established: which
	// candidate won, whether a container was seen to reach it, and what every
	// candidate did. Drives lambda-runtime-api-unreachable. The zero value
	// means Lambda never probed — no Docker, a service subset without Lambda,
	// or startup still in flight — which is an absence rather than a problem
	// and fires nothing.
	RuntimeAPI containerendpoint.Listen

	// VPCEgress is cfg.VPCEgress, the configured egress mode — drives
	// vpc-egress-not-withheld. Only a mode that promises to *withhold* egress
	// can fail to keep the promise, so `open` fires nothing.
	VPCEgress config.VPCEgressMode

	// PlacementEnforced is dataplane.PlacementEnforced(cfg): whether a
	// resource that named a VPC is held to that VPC's network alone. False
	// means it also joins the shared data plane.
	//
	// Read only under `routed`, and that asymmetry is the point. `none`
	// isolates the shared plane too, so a container on it gains nothing there;
	// `routed` leaves it routable — the resources that named no VPC have
	// egress on AWS as well — so a VPC-placed container that also lands on it
	// takes a default route its subnet's route table never granted.
	PlacementEnforced bool

	// ControlPlane is what this run resolved the control plane's isolation to,
	// and why — docker.Status.Decisions, narrowed to cfg.ControlNetwork().
	//
	// The *decision*, not the network record, and that distinction is the whole
	// reason this field exists. A routable control plane is how a withholding
	// mode fails to withhold: every container Overcast starts is on it and
	// takes its default route from it. Reading that out of Networks made the
	// rule depend on a record that ForgetNetwork deletes on a Docker `destroy`,
	// so `overcast network reset overcast_control` switched the advisory off
	// while the shortfall it reports was entirely unchanged.
	//
	// The zero value means no decision was recorded — Docker was never probed,
	// or this build has no supervisor — which fires nothing.
	ControlPlane docker.NetworkDecision
}

// controlPlaneRoutable reports whether the control plane was left routable, so
// that every container Overcast starts takes a route out through it.
//
// Shared by the egress rules rather than each scanning for itself: `none` and
// `routed` both promise to withhold egress, both fail to keep the promise on a
// host that will not take an internal control plane, and a second copy of this
// scan is a second place for the two to disagree about what "routable" means.
func controlPlaneRoutable(in advisoryInput) bool {
	return in.ControlPlane.Network != "" && !in.ControlPlane.Internal
}

// controlPlaneOverriddenByHost reports whether the control plane was left
// routable because *this host* could not take an isolated one, as opposed to
// because the operator pinned it that way.
//
// The two are different facts and only one of them is about the host, which
// matters because the advisory explains itself: telling an operator who set
// OVERCAST_CONTROL_PLANE_INTERNAL=false that "containers on this host reach the
// Runtime API at the host's own address" states a probe result that was never
// obtained — dataplane.ControlPlaneInternal returns on the pin before it
// consults the host at all. Asserting an unchecked fact is the thing this
// release is about not doing.
//
// The marker is dataplane's, written into the reason at the one place that can
// know: the decision itself.
func controlPlaneOverriddenByHost(in advisoryInput) bool {
	return strings.Contains(in.ControlPlane.Reason, ", overridden: ")
}

// computeAdvisories is the single generator function behind the
// GET /_overcast/debug/metrics payload's `advisories` array. It is a pure function of
// advisoryInput — no I/O, no clock reads beyond formatting a timestamp
// that's already in the input — so every rule is independently unit-tested
// with fake input (see advisories_test.go) rather than requiring a real
// store.
//
// Adding a future rule means adding one more check*(...) *Advisory function
// and one more call below. It must never require a web UI change: the
// Metrics & Health page renders whatever this returns generically, keyed
// only on Severity/Title/Detail/DocsPath.
func computeAdvisories(in advisoryInput) []Advisory {
	var advisories []Advisory
	for _, m := range in.Stores {
		if a := checkJournalModeNotWAL(m); a != nil {
			advisories = append(advisories, *a)
		}
		if a := checkStoreDegraded(m); a != nil {
			advisories = append(advisories, *a)
		}
		if a := checkDataDirSlowFilesystem(m); a != nil {
			advisories = append(advisories, *a)
		}
		if a := checkReadPressure(m); a != nil {
			advisories = append(advisories, *a)
		}
	}
	if a := checkStoreUnhealthy(in.Health, in.HasHealth); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkMemoryMode(in.StateBackend, in.StateSource, in.SQLiteAvailable); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkMemoryModeIgnoresExisting(in.StateBackend, in.ExistingDatabase); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkNetworkStateMismatch(in.Networks); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkVPCNetworkUnbacked(in.VPCNetworkProblems); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkVPCNetworkIsolation(in.VPCNetworkProblems); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkDataPlaneNameRefused(in.DNSRefusals); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkLambdaInitVolumeOwnership(in.LambdaInitVolumeProblems); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkRuntimeAPIUnreachable(in.RuntimeAPI); a != nil {
		advisories = append(advisories, *a)
	}
	if a := checkEgressNotWithheld(in); a != nil {
		advisories = append(advisories, *a)
	}
	return advisories
}

// checkEgressNotWithheld is warning-severity: a mode that promises to withhold
// egress is configured, and on this host the containers Overcast starts have a
// route out anyway.
//
// One rule for `none` and `routed` rather than one each. They promise
// different things — `none` that nothing Overcast starts leaves the machine,
// `routed` that a container leaves it exactly where its subnet's route table
// says — but they fail for the same reason on the same hosts, and a second
// copy of that reasoning is a second place for the two to drift. What differs
// is stated once each, below.
//
// **Both modes: a routable control plane.** Containers here reach the Lambda
// Runtime API at the host's own address, and `--internal` would sever it,
// stranding every invocation at INIT. Overcast makes that trade deliberately
// (dataplane.ControlPlaneMustStayRoutableWarning) and says so at startup,
// once. Every container Overcast starts is on that network, so every one of
// them keeps a default route through it. It can also be the operator's own
// doing — the deprecated pin — which is a different fact with a different fix,
// so the two are told apart rather than blamed on the host alike
// (controlPlaneOverriddenByHost).
//
// **`routed` only: unenforced VPC placement.** `routed` leaves the shared data
// plane routable, because the resources that named no VPC have egress on AWS
// too. That is safe only while a VPC-placed container is held to its VPC's own
// network; where Overcast's DNS resolver cannot start, it is not, and the
// container takes a default route from the shared plane whatever its route
// table said. `none` cannot fail this way: it isolates the shared plane as
// well, so landing on it grants nothing.
//
// It is here rather than only in the startup log because of what these modes
// are for. Nobody sets them to change how a stack feels; they set them to
// *prove* something — that there is no hidden external dependency, or that the
// template's own route tables are what decide. A promise like that failing
// quietly, on the most common developer platform there is, is the one case
// where a card on the Metrics and Health page is worth more than a line that
// scrolled past at boot.
func checkEgressNotWithheld(in advisoryInput) *Advisory {
	mode := in.VPCEgress
	if mode != config.VPCEgressNone && mode != config.VPCEgressRouted {
		return nil
	}
	var why, fixes []string
	if controlPlaneRoutable(in) {
		w := "The control plane " + in.ControlPlane.Network + " was left routable, so every container " +
			"Overcast starts takes a route out from it. "
		if controlPlaneOverriddenByHost(in) {
			w += "Containers on this host reach the Lambda Runtime API at the host's own address, which " +
				"an internal network would sever, stranding every invocation at INIT, so Overcast left it " +
				"routable rather than isolating it."
			fixes = append(fixes, "Run Overcast in a container, or against a native Linux Docker daemon, "+
				"for the whole of `"+string(mode)+"`.")
		} else {
			// The operator pinned it. Naming the deprecated variable is the
			// whole of the fix here, and the host has nothing to do with it —
			// the pin is applied before the host is ever asked.
			w += "That was asked for, not imposed: " + in.ControlPlane.Reason + "."
			fixes = append(fixes, "Unset OVERCAST_CONTROL_PLANE_INTERNAL — it is deprecated, and it "+
				"contradicts the egress mode you have set.")
		}
		why = append(why, w)
	}
	if mode == config.VPCEgressRouted && !in.PlacementEnforced {
		why = append(why, "VPC placement is not enforced on this host — Overcast's DNS resolver is not "+
			"listening, so a resource that named a VPC also joins the shared data plane, which `routed` "+
			"leaves routable.")
		fixes = append(fixes, "Run Overcast in a container, or against a native Linux Docker daemon, "+
			"for the whole of `routed`.")
	}
	if len(why) == 0 {
		return nil
	}
	consequence := "Every data plane is isolated as asked, but this stack is NOT hermetic — a function " +
		"can still reach the internet and real AWS endpoints. "
	if mode == config.VPCEgressRouted {
		consequence = "A subnet with no 0.0.0.0/0 route still reaches the internet, so the missing NAT " +
			"gateway this mode exists to catch goes uncaught. "
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeEgressNotWithheld,
		Title:    "OVERCAST_VPC_EGRESS=" + string(mode) + " cannot withhold egress on this host",
		Detail:   strings.Join(why, " ") + " " + consequence + strings.Join(dedupe(fixes), " "),
		DocsPath: egressModeDocsPath,
	}
}

// dedupe drops repeats while keeping order — two shortfalls with the same fix
// should not print it twice.
func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// checkNetworkStateMismatch is warning-severity: a Docker network that is not
// in the state this configuration asks for.
//
// It is here rather than only in the startup log because of where its cost
// lands. Docker's create-network call returns an existing network unchanged, so
// a network created by an older version, a different egress mode, or by hand
// keeps every setting it was born with — and the failure that produces is a
// function that cannot reach the internet, or cannot reach a database, minutes
// later inside application code, with nothing connecting it back to a warning
// that scrolled past at boot. That is #1564 exactly.
//
// One advisory for the whole set rather than one per network: they share a
// single cause and a single fix, and a list of four cards saying the same thing
// is a list nobody reads.
func checkNetworkStateMismatch(networks []docker.NetworkStatus) *Advisory {
	var bad []docker.NetworkStatus
	for _, n := range networks {
		if !n.OK() {
			bad = append(bad, n)
		}
	}
	if len(bad) == 0 {
		return nil
	}

	var b strings.Builder
	for i, n := range bad {
		if i > 0 {
			b.WriteString(" ")
		}
		// The field list when there is one, and the network's own summary when
		// there is not. A network Overcast could not read carries a Drift and
		// no Mismatch, and "ocrev ()." would be this advisory telling an
		// operator nothing at the moment it has something to say.
		if len(n.Mismatch) > 0 {
			fmt.Fprintf(&b, "%s (%s).", n.Name, strings.Join(docker.DiffStrings(n.Mismatch), "; "))
		} else {
			fmt.Fprintf(&b, "%s: %s.", n.Name, n.Drift)
		}
		if len(n.Attached) > 0 {
			fmt.Fprintf(&b, " Attached: %s.", strings.Join(n.Attached, ", "))
		}
		if n.Owner != "" {
			fmt.Fprintf(&b, " Owned by another Overcast instance (%s).", n.Owner)
		}
	}

	names := make([]string, 0, len(bad))
	fix := ""
	for _, n := range bad {
		names = append(names, n.Name)
		if fix == "" && n.Fix != "" {
			fix = n.Fix
		}
	}
	detail := "Overcast left these networks as they are: Docker cannot change these settings on an " +
		"existing network, and each of them either has containers attached, belongs to another " +
		"instance, or could not be read. Until that is resolved, what a container can reach is not " +
		"what this configuration says. " + b.String()
	if fix != "" {
		detail += " Fix: `" + fix + "`"
		// Only the rebuild takes --dry-run, and only the rebuild changes
		// anything. Offering the flag beside a read-only command would send an
		// operator to a usage error, which is the same class of mistake as
		// naming a command that will refuse them (#1584).
		if strings.HasPrefix(fix, "overcast network reset") {
			detail += " (add --dry-run to see what it would do first)"
		}
		detail += "."
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeNetworkStateMismatch,
		Title:    "Docker network is not in its configured state: " + strings.Join(names, ", "),
		Detail:   detail,
		DocsPath: networkingDocsPath,
	}
}

// vpcNetworkAdvisoryMaxListed bounds how many VPCs the advisory spells out.
// Each entry carries a Docker error string, and the Metrics & Health card
// has to stay a card when twenty VPCs are broken at once.
const vpcNetworkAdvisoryMaxListed = 5

// checkVPCNetworkIsolation is a warning: a VPC in this state works — its
// containers run and reach the control plane — but either with the internet
// reachability of the gateway state before the one DescribeInternetGateways
// reports, or with containers left off the recreated network, which is
// exactly the quiet divergence an emulator must not ship silently. The EC2
// service records an entry when a flip it attempted (on
// Attach/DetachInternetGateway, or at startup for a network adopted with the
// wrong flag) left the network or its containers wrong, and clears it when a
// later flip succeeds or the VPC is deleted. A flip that failed before
// changing anything records nothing: that path fails the API call instead,
// and the network still matches the record.
func checkVPCNetworkIsolation(problems []dataplane.VPCNetworkProblem) *Advisory {
	problems = vpcProblemsWhere(problems, false)
	if len(problems) == 0 {
		return nil
	}
	listed := listVPCProblems(problems)
	title := "A VPC's network does not match its internet gateway or route tables"
	if len(problems) > 1 {
		title = fmt.Sprintf("%d VPC networks do not match their internet gateways or route tables", len(problems))
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeVPCNetworkIsolationStale,
		Title:    title,
		Detail: "Containers in these VPCs do not have the internet reachability DescribeInternetGateways " +
			"and DescribeRouteTables imply. Overcast retries the repair at its next restart; until then, " +
			"check the Docker daemon for the reason quoted, then detach and re-attach the gateway, or " +
			"delete and recreate the route. " + listed,
		DocsPath: vpcNetworkDocsPath,
	}
}

// checkVPCNetworkUnbacked is critical-severity: a VPC exists whose Docker
// network the daemon refused to create, so nothing can be placed in it. The
// EC2 service keeps `CreateVpc` answering 200 in that case — a VPC is metadata
// AWS never refuses, and the next reconcile retries the create — which is
// exactly why the failure has to be reported here, at the moment it happens.
// Left to surface on its own, it did so minutes later and somewhere else: an
// RDS instance refused for a VPC "not launchable", an ECS task that could not
// find a network that was never made.
//
// Critical rather than a warning because, unlike a network in the wrong
// isolation state, nothing in the VPC works at all until it is resolved.
func checkVPCNetworkUnbacked(problems []dataplane.VPCNetworkProblem) *Advisory {
	problems = vpcProblemsWhere(problems, true)
	if len(problems) == 0 {
		return nil
	}
	listed := listVPCProblems(problems)
	title := "A VPC has no Docker network — nothing can be placed in it"
	if len(problems) > 1 {
		title = fmt.Sprintf("%d VPCs have no Docker network — nothing can be placed in them", len(problems))
	}
	return &Advisory{
		Severity: advisorySeverityCritical,
		Code:     advisoryCodeVPCNetworkUnbacked,
		Title:    title,
		Detail: "Docker refused to create the network backing these VPCs, so every ECS task, Lambda function " +
			"and database instance placed in them fails. Overcast retries the create at its next restart; " +
			"until then, check the reason quoted. An address pool that overlaps another network is the " +
			"usual one: `docker network ls` shows what holds it, `overcast network status` says whether it is " +
			"Overcast's, and OVERCAST_EC2_VPC_STRATEGY=remapped gives the VPC a subnet of its own. " + listed,
		DocsPath: vpcNetworkUnbackedDocsPath,
	}
}

// dataPlaneRefusalMaxListed bounds how many refusals the advisory spells out.
const dataPlaneRefusalMaxListed = 5

// checkDataPlaneNameRefused fires when the resolver has refused a data-plane
// name: some container asked for an RDS, ElastiCache, MSK or EFS endpoint
// that only a container on another network advertises.
//
// This is the emulator doing what the template asked — a VPC restricts, here
// as on AWS — but from inside the application it looks like a DNS outage,
// and the log line that explains it is in Overcast's output, not the
// application's. The advisory is what turns "Temporary failure in name
// resolution" into "your cache is not in your task's VPC". Every entry
// carries both sides so the reader sees which resource to move.
func checkDataPlaneNameRefused(refusals []dataplane.Refusal) *Advisory {
	if len(refusals) == 0 {
		return nil
	}
	title := "A container asked for an endpoint it cannot reach — the resource is in a different VPC"
	if len(refusals) > 1 {
		title = fmt.Sprintf("%d endpoints were refused to containers in a different VPC", len(refusals))
	}
	var sb strings.Builder
	// Newest first: the one somebody is debugging right now.
	for i, n := len(refusals)-1, 0; i >= 0 && n < dataPlaneRefusalMaxListed; i, n = i-1, n+1 {
		r := refusals[i]
		fmt.Fprintf(&sb, " %s (on %s) asked for %s, advertised by %s (on %s).",
			r.Caller, strings.Join(r.CallerNetworks, ", "), r.Name, r.Target, strings.Join(r.TargetNetworks, ", "))
	}
	if len(refusals) > dataPlaneRefusalMaxListed {
		fmt.Fprintf(&sb, " …and %d more.", len(refusals)-dataPlaneRefusalMaxListed)
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeDataPlaneNameRefused,
		Title:    title,
		Detail: "Overcast's resolver answered REFUSED rather than hand the caller Overcast's own address, " +
			"which would have connected it to the emulator on the engine's port and hung. " +
			dataplane.RefusalRemedy + sb.String(),
		DocsPath: vpcsDocsPath,
	}
}

// vpcProblemsWhere narrows problems to the unbacked ones, or to the rest.
func vpcProblemsWhere(problems []dataplane.VPCNetworkProblem, unbacked bool) []dataplane.VPCNetworkProblem {
	var out []dataplane.VPCNetworkProblem
	for _, p := range problems {
		if p.Unbacked == unbacked {
			out = append(out, p)
		}
	}
	return out
}

// listVPCProblems renders the first vpcNetworkAdvisoryMaxListed problems as
// "vpc: detail" for an advisory's Detail, and counts the rest.
func listVPCProblems(problems []dataplane.VPCNetworkProblem) string {
	shown := min(len(problems), vpcNetworkAdvisoryMaxListed)
	lines := make([]string, 0, shown)
	for _, p := range problems[:shown] {
		lines = append(lines, p.VpcID+": "+p.Detail)
	}
	listed := strings.Join(lines, "; ")
	if rest := len(problems) - len(lines); rest > 0 {
		listed += fmt.Sprintf("; and %d more", rest)
	}
	return listed
}

// lambdaInitVolumeAdvisoryMaxListed bounds how many volumes the advisory
// spells out, mirroring vpcNetworkAdvisoryMaxListed.
const lambdaInitVolumeAdvisoryMaxListed = 5

// checkLambdaInitVolumeOwnership is informational, not a warning: Overcast's
// Lambda init volume is content-addressed (its name carries the init's own
// hash — see internal/services/lambda/init_volume.go), so two instances
// sharing a daemon and a build reusing one another's copy is expected and
// safe, not itself a problem to fix.
//
// What it does mean is that this instance's own cleanup
// (pruneStaleInitVolumes, forgetInitVolume — see docker.LabelInstance) will
// never remove these volumes, because neither may touch a volume it cannot
// prove it created: a volume here only goes away once the instance that
// created it prunes it, or an operator removes it by hand. Surfacing that
// here is the only way an operator learns these volumes exist at all —
// otherwise they would only ever see a debug-level log line.
func checkLambdaInitVolumeOwnership(problems []docker.VolumeOwnershipProblem) *Advisory {
	if len(problems) == 0 {
		return nil
	}
	shown := min(len(problems), lambdaInitVolumeAdvisoryMaxListed)
	lines := make([]string, 0, shown)
	for _, p := range problems[:shown] {
		owner := p.Owner
		if owner == "" {
			owner = "no owner label"
		}
		lines = append(lines, p.Volume+" ("+owner+")")
	}
	listed := strings.Join(lines, "; ")
	if rest := len(problems) - len(lines); rest > 0 {
		listed += fmt.Sprintf("; and %d more", rest)
	}
	title := "A Lambda init volume belongs to another instance"
	if len(problems) > 1 {
		title = fmt.Sprintf("%d Lambda init volumes belong to other instances", len(problems))
	}
	return &Advisory{
		Severity: advisorySeverityInfo,
		Code:     advisoryCodeLambdaInitVolumeForeign,
		Title:    title,
		Detail: "These volumes hold this build's init and are safe for this instance to keep " +
			"mounting read-only, but it did not create them, so its own cleanup will never remove " +
			"them — only the instance that created each one, or `docker volume rm`, will. " + listed,
		DocsPath: lambdaInitVolumeDocsPath,
	}
}

// checkJournalModeNotWAL is critical: this project shipped with WAL mode
// silently disabled for its entire history (a driver DSN-spelling bug — see
// hybrid_journalmode_internal_test.go and state.DebugMetrics.JournalMode's
// doc comment), and rollback-journal mode's exclusive commit lock directly
// causes the read starvation storage-pressure-handling item 0 was written to
// fix. An empty JournalMode means the readback hasn't happened yet (still
// starting up) or the store already degraded (see checkStoreDegraded, which
// covers that case on its own) — neither is this rule's concern, so it only
// fires on an actual, confirmed non-"wal" reading.
func checkJournalModeNotWAL(m state.DebugMetrics) *Advisory {
	if m.JournalMode == "" || m.JournalMode == "wal" {
		return nil
	}
	if m.Mode != "hybrid" && m.Mode != "persistent" {
		return nil
	}
	return &Advisory{
		Severity: advisorySeverityCritical,
		Code:     advisoryCodeJournalModeNotWAL,
		Title:    "SQLite is not running in WAL mode",
		Detail: fmt.Sprintf(
			"The live PRAGMA journal_mode readback for the %q backend is %q, not \"wal\". "+
				"Rollback-journal mode takes a brief but real exclusive lock on every commit, which "+
				"can block concurrent readers under write pressure. Restart the emulator; if this "+
				"persists, the SQLite driver DSN may not be applying the journal_mode pragma.",
			m.Mode, m.JournalMode,
		),
		DocsPath: performanceDocsPath,
	}
}

// checkStoreDegraded is critical: once HybridStore.degradeToMemoryOnly has
// run, every write for the rest of the process's life is memory-only and
// silently lost on restart — the single worst outcome this page can report.
func checkStoreDegraded(m state.DebugMetrics) *Advisory {
	if !m.Degraded {
		return nil
	}
	return &Advisory{
		Severity: advisorySeverityCritical,
		Code:     advisoryCodeStoreDegradedMemoryOnly,
		Title:    "Storage degraded to memory-only",
		Detail: fmt.Sprintf(
			"The %q backend permanently fell back to memory-only for this process's lifetime — "+
				"data is no longer being persisted and will be lost on restart. Restart the emulator "+
				"to retry opening the persistent backend; check the startup log for the underlying error.",
			m.Mode,
		),
	}
}

// checkStoreUnhealthy is a warning, not critical: state.PersistentHealth's
// Healthy flag is a transient, self-clearing signal (markPersistentSuccess
// flips it back on the next successful flush — see hybrid.go), unlike
// checkStoreDegraded's permanent, un-recoverable-without-a-restart
// condition. A store can be Healthy=false for a moment under a burst of
// write pressure and recover on its own.
func checkStoreUnhealthy(health state.PersistentHealth, hasHealth bool) *Advisory {
	if !hasHealth || health.Healthy {
		return nil
	}
	detail := "The persistent backend reported a write/flush failure and has not yet recovered."
	if health.LastError != "" {
		detail += fmt.Sprintf(" Last error: %s.", health.LastError)
	}
	if !health.LastErrorAt.IsZero() {
		detail += fmt.Sprintf(" Occurred at %s.", health.LastErrorAt.UTC().Format(time.RFC3339))
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeStoreUnhealthy,
		Title:    "Storage backend reported an error",
		Detail:   detail,
	}
}

// checkDataDirSlowFilesystem surfaces HybridStore's one-time startup fsync
// probe (runDataDirProbe) — the same condition that already logs a WARN at
// startup, now visible in the metrics payload too instead of requiring
// someone to have been watching the log at the right moment.
func checkDataDirSlowFilesystem(m state.DebugMetrics) *Advisory {
	if m.DataDirProbe == nil || !m.DataDirProbe.Slow {
		return nil
	}
	var detail string
	switch m.DataDirProbe.MountClass {
	case "shared":
		detail = fmt.Sprintf(
			"A startup fsync probe against the data directory took %dms (median of 3). The data "+
				"directory is on a Docker Desktop file-sharing mount (%s), which adds latency to "+
				"every flush and read. Use a named Docker volume for the data directory instead.",
			m.DataDirProbe.FsyncMillis, m.DataDirProbe.FsType,
		)
	case "native":
		detail = fmt.Sprintf(
			"A startup fsync probe against the data directory took %dms (median of 3). The data "+
				"directory is already on a native filesystem (%s), so the mount type is not the "+
				"problem — the underlying disk or Docker VM is under I/O pressure. Check host disk "+
				"load, antivirus scanning of the Docker VM disk image, and VM CPU/memory resources. "+
				"The probe runs once at startup, so a transient boot-time stall can also trigger "+
				"this; restart to re-measure.",
			m.DataDirProbe.FsyncMillis, m.DataDirProbe.FsType,
		)
	default:
		detail = fmt.Sprintf(
			"A startup fsync probe against the data directory took %dms (median of 3). If the data "+
				"directory is a Docker Desktop bind mount on macOS/Windows, use a named Docker "+
				"volume instead. If it is already on a native filesystem or named volume, the disk "+
				"or VM is under I/O pressure — check host disk load, antivirus scanning of the "+
				"Docker VM disk image, and VM resources.",
			m.DataDirProbe.FsyncMillis,
		)
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeDataDirSlowFilesystem,
		Title:    "Data directory filesystem is slow",
		Detail:   detail,
		DocsPath: dataDirDocsPath,
	}
}

// checkReadPressure fires on ReadTimeoutCount, deliberately NOT on
// ReadRetryCount alone — not even as an info-level advisory.
//
// Justification: retries are the hybrid SQLite read-retry path (see
// state.ErrStorePressure's doc comment) working exactly as designed —
// individual retries happen routinely under perfectly ordinary, brief SQLite
// busy/locked contention (e.g. a flush landing at the same moment as a
// point read) and resolve within the retry window essentially always. A
// non-zero ReadRetryCount on a healthy, moderately-loaded emulator is
// unremarkable and would make this section noisy on installations that have
// nothing wrong. A timeout, in contrast, means the ENTIRE retry window
// (hybridSQLiteReadRetryTimeout) was exhausted without success — that read
// was actually throttled and surfaced to its caller as a throttling error
// (see state.ErrStorePressure) instead of the data it asked for. That is the
// first user-visible symptom of real, sustained write pressure, which is
// exactly the class of condition an advisory should exist to catch.
func checkReadPressure(m state.DebugMetrics) *Advisory {
	if m.ReadTimeoutCount <= 0 {
		return nil
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeReadPressureObserved,
		Title:    "Storage reads are under write pressure",
		Detail: fmt.Sprintf(
			"%d read(s) exhausted their retry window under sustained SQLite write pressure and were "+
				"throttled instead of served. This means writes are arriving faster than the persistent "+
				"backend can flush. Consider reducing write volume, or moving the data directory to "+
				"faster storage.",
			m.ReadTimeoutCount,
		),
		DocsPath: performanceDocsPath,
	}
}

// checkMemoryMode is informational only in all three variants below — memory
// mode is never itself an error condition, just something worth surfacing so
// nobody is surprised data didn't survive a restart.
//
// Three wordings:
//
//   - Explicit (StateSource == "explicit"): OVERCAST_STATE=memory is a
//     deliberate, common choice for tests and CI — this exists purely so a
//     developer who forgot they set it (or inherited an env file that sets
//     it) isn't surprised the next time they restart.
//   - Auto without SQLite (StateSource == "auto", sqliteAvailable false): a
//     -tags nosqlite build (the overcast-slim image, the overcastd binaries).
//     This variant exists because every remediation the plain auto wording
//     offers is actively wrong here: config.resolveAutoState short-circuits
//     to memory before it weighs the mountpoint/data-dir/existing-database
//     signals, so mounting a volume or setting OVERCAST_DATA_DIR changes
//     nothing, and OVERCAST_STATE=hybrid doesn't degrade — it stops the
//     daemon from starting ("init state backend: hybrid store: not compiled
//     with SQLite support"). The one durable backend compiled into every
//     build is wal (internal/state/wal.go carries no build tag), so that is
//     what this variant names.
//   - Auto (StateSource == "auto"): the OVERCAST_STATE=auto resolver (see
//     config.resolveAutoState) found no evidence of persistence intent — no
//     mounted volume, no explicit data directory, no existing database — and
//     landed on memory by design (e.g. a fresh `docker run` with no volume
//     mounted). Severity stays info (this is working as designed, most
//     visibly for CI), but the wording is the actionable variant: it tells
//     the reader what would change the outcome, since "auto" means nobody
//     deliberately typed OVERCAST_STATE=memory to get here.
func checkMemoryMode(backend config.StateBackend, source config.StateSource, sqliteAvailable bool) *Advisory {
	if backend != config.StateBackendMemory {
		return nil
	}
	if source == config.StateSourceAuto && !sqliteAvailable {
		return &Advisory{
			Severity: advisorySeverityInfo,
			Code:     advisoryCodeMemoryMode,
			Title:    "Running in memory-only mode (this build has no SQLite)",
			Detail: "This build was compiled without SQLite, so the hybrid and persistent backends " +
				"don't exist in it — OVERCAST_STATE=auto resolves to memory no matter what is " +
				"mounted at the data directory, and state won't survive restarts. Mounting a volume " +
				"or setting OVERCAST_DATA_DIR alone will not change this, and OVERCAST_STATE=hybrid " +
				"will stop the emulator from starting. Set OVERCAST_STATE=wal — the durable backend " +
				"compiled into every build — with a volume mounted at the data directory, or use the " +
				"full overcast image or overcast binary if you need hybrid.",
			DocsPath: noSQLiteDocsPath,
		}
	}
	if source == config.StateSourceAuto {
		return &Advisory{
			Severity: advisorySeverityInfo,
			Code:     advisoryCodeMemoryMode,
			Title:    "Running in memory-only mode (auto-detected)",
			Detail: "OVERCAST_STATE was not set and no volume is mounted at the data directory — " +
				"state won't survive restarts. Mount a volume there, set OVERCAST_DATA_DIR, or set " +
				"OVERCAST_STATE=hybrid to persist.",
		}
	}
	return &Advisory{
		Severity: advisorySeverityInfo,
		Code:     advisoryCodeMemoryMode,
		Title:    "Running in memory-only mode",
		Detail:   "OVERCAST_STATE=memory — state won't survive restarts; expected in this mode.",
	}
}

// checkMemoryModeIgnoresExisting is a warning, not merely informational like
// checkMemoryMode's three variants: memory mode on its own is a deliberate or
// harmless default (fresh install, CI), but memory mode while a database
// already sits in the data directory means there is real, existing data one
// directory away that this run will neither read nor update. That combination
// only arises two ways — OVERCAST_STATE=memory set explicitly over a
// directory that already has one (a leftover env file, a docker-compose
// override nobody remembers writing), or a -tags nosqlite build's auto
// resolution short-circuiting straight to memory regardless of evidence (see
// config.resolveAutoState's SQLiteAvailable gate) — and both are worse than
// the ordinary "auto landed on memory, nothing here yet" case checkMemoryMode
// already covers: a developer who restarts expecting their state back gets an
// empty emulator with no indication the data still exists, just not in this
// run.
//
// hasExistingDatabase is config.HasExistingDatabase(cfg.DataDir), false for
// every fresh install and every ordinary memory-mode CI run — the two cases
// this must never fire on.
func checkMemoryModeIgnoresExisting(backend config.StateBackend, hasExistingDatabase bool) *Advisory {
	if backend != config.StateBackendMemory || !hasExistingDatabase {
		return nil
	}
	return &Advisory{
		Severity: advisorySeverityWarning,
		Code:     advisoryCodeMemoryModeIgnoresExisting,
		Title:    "Memory mode is ignoring an existing database",
		Detail: "This run is memory-only, but an existing Overcast database was found in the data " +
			"directory — it will not be read and will not be updated: writes made now are lost on " +
			"restart, while that database is left exactly as it was. If you expected this run to use " +
			"it, set OVERCAST_STATE=auto (or hybrid/wal) and confirm this build has SQLite support.",
		DocsPath: storageDocsPath,
	}
}

// lambdaTroubleshootingDocsPath deep-links the Runtime API advisory at the
// section that spells out the whole diagnosis. The fragment is the docs
// browser's heading slug — see dataDirDocsPath for how those are derived.
const lambdaTroubleshootingDocsPath = "services/lambda/troubleshooting.md#containers-cannot-reach-the-runtime-api"

// checkRuntimeAPIUnreachable is the one critical-severity rule in this file,
// and it earns it: while it holds, **no Lambda can run at all**. Every
// invocation strands at INIT and the runtime exits 139.
//
// It fires only on a measured verdict. Overcast now establishes the Runtime API
// address by having a container connect to each candidate in turn
// (containerendpoint), so this reports a fact rather than a suspicion — which
// is the whole point of #1572, where the address was chosen on bindability, the
// log claimed it was "container-reachable", and the only signal a user ever got
// was a SIGSEGV pointing at the wrong subsystem.
//
// A probe that could not run at all (no Docker, no busybox, a daemon refusing
// creates) leaves Unreachable false and fires nothing: an unmeasured address is
// not an unreachable one, and a critical card for "we did not check" is the
// kind of noise that gets a whole panel ignored.
func checkRuntimeAPIUnreachable(l containerendpoint.Listen) *Advisory {
	if !l.Unreachable {
		return nil
	}
	return &Advisory{
		Severity: advisorySeverityCritical,
		Code:     advisoryCodeRuntimeAPIUnreachable,
		Title:    "No Lambda can run: containers cannot reach the Runtime API",
		Detail:   containerendpoint.RuntimeAPIUnreachableDetail(l.Attempts),
		DocsPath: lambdaTroubleshootingDocsPath,
	}
}
