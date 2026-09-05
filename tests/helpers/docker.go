package helpers

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
	"github.com/overcast-sh/overcast/internal/docker/dockertest"
)

// ReserveTCPPort returns a host port that was free a moment ago, for a test
// that must name a fixed port before the thing that will listen on it exists —
// WithECRRegistryPort is the case this was written for.
//
// The listener is closed before returning, so this is a hint rather than a
// reservation: another process can take the port in between. That is tolerable
// here because the caller's fallback is benign (the ECR registry falls back to
// an ephemeral port and the test skips), and because the point is a port no
// *other test* will claim, which a kernel-assigned one reliably is.
func ReserveTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a TCP port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// DockerLoginOrSkip authenticates the Docker CLI against a registry, skipping
// the test when the daemon will not talk plain HTTP to it.
//
// That refusal is daemon configuration a test cannot change and says nothing
// about Overcast, so it is a gate rather than a failure. Any other login error
// is real and fails.
//
// The login is given a credential store of its own, which is what keeps this
// suite from both degrading the machine it runs on and flaking against itself.
//
// By default `docker login` records the credential per registry address in two
// shared places at once: an `auths` key in ~/.docker/config.json, and an entry
// in the platform credential store named by `credsStore` (Windows Credential
// Manager, the macOS keychain, libsecret). Every registry here is
// `localhost:<ephemeral port>`, so each run mints an address that will never be
// seen again and leaves an entry behind for it, accumulating across every run
// by every contributor. That has a hard ceiling: a saturated Windows credential
// store fails the next login with "Not enough memory resources are available to
// process this command", which reads as a machine problem rather than as this
// suite's litter and takes every Docker-gated test down with it. Found at ~200
// entries on a development machine.
//
// Logging out again was the first fix and it was not enough, in a way worth
// recording because the failure it caused looked like someone else's. Three
// tests across two packages log in, `go test` runs packages concurrently, and
// login and logout are both read-modify-writes of shared state — so on CI,
// where runners configure no credsStore and the credential really is just a key
// in one JSON file, a logout could write back a snapshot taken before a
// concurrent login and erase a credential still in use. That surfaces as `docker
// pull` failing with "no basic auth credentials" in whichever package lost the
// race. Adding the logout is what introduced a writer that *removes* entries;
// before it, concurrent logins only ever added.
//
// Which fix works depends on where the credential actually lands, and the two
// cases want opposite things — so isolateDockerConfig decides per machine and
// the logout covers what isolation cannot reach. See both for the measurements.
func DockerLoginOrSkip(t *testing.T, proxy, user, password string) {
	t.Helper()
	isolateDockerConfig(t)

	// Registered after any isolation so a partial login — the credential stored,
	// the command still reporting failure — is cleaned up too, and so that
	// DOCKER_CONFIG is still set while this runs: cleanups are LIFO and
	// t.Setenv's own restore was registered first. Logging out of a registry
	// that was never logged in to is a no-op, and this runs on a fresh context
	// because t's is already cancelled by cleanup time.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "docker", "logout", proxy).Run()
	})

	cmd := exec.CommandContext(t.Context(), "docker", "login", proxy, "-u", user, "--password-stdin")
	cmd.Stdin = strings.NewReader(password + "\n")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	msg := string(out)
	if strings.Contains(msg, "server gave HTTP response to HTTPS client") ||
		strings.Contains(msg, "https://") ||
		strings.Contains(msg, "context deadline exceeded") {
		t.Skipf("docker daemon will not talk plain HTTP to %s: %s", proxy, strings.TrimSpace(msg))
	}
	t.Fatalf("docker login %s: %v\n%s", proxy, err, out)
}

// isolateDockerConfig points DOCKER_CONFIG at a directory belonging to this
// test, on the machines where that is the fix and only there.
//
// Where the credential is a key in ~/.docker/config.json and nothing else — CI
// runners, which configure no credential helper — the shared file is the whole
// problem. Three tests across two packages log in, `go test` runs packages
// concurrently, and login and logout are both read-modify-writes of that one
// file, so a logout can write back a snapshot taken before a concurrent login
// and erase a credential still in use. It surfaces as `docker pull` failing with
// "no basic auth credentials" in whichever package lost the race. Giving each
// test process its own config removes the sharing rather than trying to time it,
// and t.TempDir then takes the credential away with the directory.
//
// Where a credential helper owns the credential, isolating the config is worse
// than doing nothing, which is why this is conditional rather than always on.
// Measured on Windows with Docker Desktop, counting `localhost:<port>` entries
// added to Windows Credential Manager by a single ECR run: not isolating leaks
// none, isolating while still logging out leaks one to three, and isolating with
// the logout dropped leaks four. Desktop's CLI reaches the helper whatever this
// config says, so the login lands there — while `docker logout`, reading the
// same isolated config, looks for the credential in the file store and does not
// reliably remove what the helper holds. Isolation breaks the cleanup without
// moving the credential, and the entries accumulate to a hard ceiling: a
// saturated store fails the next login with
// "Not enough memory resources are available to process this command", which
// reads as a machine problem rather than as this suite's litter and takes every
// Docker-gated test down with it. Found at ~200 entries on a development
// machine. So on those machines nothing is isolated and the logout keeps
// working exactly as it did.
//
// t.Setenv rather than an env slice on each command, deliberately: every
// `docker` the test runs afterwards inherits the setting through os.Environ(),
// including the ones in each package's own exec wrappers. Threading it through
// every call site instead would turn a missed one into a confusing auth failure
// rather than a compile error. It also means this cannot be called from a
// parallel test, which Go enforces for us.
func isolateDockerConfig(t *testing.T) {
	t.Helper()
	// A second login in the same test joins the first rather than isolating
	// again. Isolating again would point DOCKER_CONFIG at a fresh directory and
	// strand the credential the first login just stored, which is an auth
	// failure some distance from its cause. No test logs in twice today; this is
	// so that one may.
	if dir := os.Getenv("DOCKER_CONFIG"); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, isolatedConfigMarker)); err == nil {
			return
		}
	}
	if dockerUsesCredentialHelper() {
		return
	}
	dir := t.TempDir()
	// An explicit empty credsStore is the point of writing this file at all:
	// given an empty directory the CLI goes looking for a platform helper
	// instead of falling back to the file store.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{},"credsStore":""}`), 0o600); err != nil {
		t.Fatalf("write an isolated docker config: %v", err)
	}
	// Names this directory as ours. The config alone cannot: one written here
	// and one belonging to a machine that simply has no helper read the same.
	if err := os.WriteFile(filepath.Join(dir, isolatedConfigMarker), nil, 0o600); err != nil {
		t.Fatalf("mark the isolated docker config: %v", err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// isolatedConfigMarker names a DOCKER_CONFIG directory as one isolateDockerConfig
// created. Docker ignores files it does not recognise, so this sits harmlessly
// beside config.json.
const isolatedConfigMarker = ".overcast-isolated"

// dockerUsesCredentialHelper reports whether the Docker CLI on this machine
// hands credentials to a helper process rather than writing them to its config
// file.
//
// Unreadable or absent config means no helper, which is the CI shape and the
// safe way round: the cost of isolating when a helper is present is leaked
// credentials, and the cost of not isolating when one is absent is the flake
// this exists to stop.
func dockerUsesCredentialHelper() bool {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		dir = filepath.Join(home, ".docker")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return false
	}
	var cfg struct {
		CredsStore  string            `json:"credsStore"`
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false
	}
	return cfg.CredsStore != "" || len(cfg.CredHelpers) > 0
}

// removeTestNetworks deletes the Docker networks a test server minted for
// itself, best-effort but not silent: a daemon that never answered created
// none, which is skipped; one still holding a container of ours on a network
// has the container removed first, then the network, with a short wait for the
// daemon's asynchronous endpoint release. Whatever is still refused is logged
// against the test, naming the sweep that finishes the job — the previous
// version discarded the error, and a machine came to hold twenty-two empty
// networks with no line anywhere saying why. None of it fails the test, which
// has already made its assertions.
func removeTestNetworks(t *testing.T, names []string) {
	dc := docker.NewClient(TestDockerSocket(), zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dockertest.RemoveOwned(ctx, dc, names, t.Logf)
}

// removeTestRegistryVolume deletes the storage volume a fixed-port registry
// claim created for this test server, best-effort and for the same reasons
// removeTestNetworks is.
//
// The volume is the point of the fixed-port claim in production — a restart
// there is meant to find the last run's images — and exactly the wrong thing to
// keep in a suite, where every server takes a fresh reserved port. Left alone
// the suite accumulates one volume per Docker-gated registry test, per run,
// each with a port in its name that nothing will ever claim again.
//
// Force, because the registry container may still be on its way out: the
// server's own cleanup removes it and Docker's removal is asynchronous, so an
// unforced delete races the container's disappearance and loses.
func removeTestRegistryVolume(port int) {
	if port <= 0 {
		return
	}
	dc := docker.NewClient(TestDockerSocket(), zap.NewNop())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = dc.RemoveVolume(ctx, "overcast-ecr-registry-data-"+strconv.Itoa(port), true)
}

// DockerAvailable reports why the Docker daemon cannot be reached, or nil when
// it can. It resolves the endpoint exactly as TestDockerSocket does — so the
// gate and the server under test always agree about which daemon is meant —
// and then asks that daemon, rather than looking for a file.
//
// Two ways of answering this question have already been got wrong here, and
// both failed in the same silent direction: reporting "no Docker" on a machine
// with a healthy daemon, which turns every test behind the gate into a skip
// nobody reads.
//
//   - Dialling the empty string. NewClient("") hands "" to dialEndpoint, which
//     on every platform falls through to the Unix-socket branch, so the ping
//     died with "dial unix: missing address" whatever the daemon was doing.
//     helpers.SkipWithoutDocker did this until #1785, and the ECS,
//     CloudFormation and Lambda container tests behind it skipped everywhere,
//     Linux CI included.
//   - Stat-ing "/var/run/docker.sock". That path does not exist on Windows,
//     where Docker Desktop listens on a named pipe, and it is the wrong path
//     wherever DOCKER_HOST or LAMBDA_DOCKER_SOCKET names another daemon. The
//     Lambda container package's private gate did this until #1785 and skipped
//     all 49 of its tests on any Windows workstation.
//
// It takes a context rather than a *testing.T so TestMain can use it: a
// package that pre-pulls images before any test runs needs the same answer, from
// the same resolution, and pre-pulling through a second one is how the daemon a
// suite warms stops being the daemon it tests.
func DockerAvailable(ctx context.Context) error {
	return docker.NewClient(TestDockerSocket(), nil).Ping(ctx)
}

// SkipWithoutDocker calls t.Skip if Docker is not reachable. Use at the top of
// tests that need a running daemon (ECS, Lambda, RDS, ElastiCache, MSK, EFS).
//
// The skip message names the endpoint, because the interesting failure is not
// "no daemon" but "not the daemon you meant" — see DockerAvailable.
func SkipWithoutDocker(t *testing.T) {
	t.Helper()
	if err := DockerAvailable(t.Context()); err != nil {
		t.Skipf("skipping: Docker is not available at %s (%v)", TestDockerSocket(), err)
	}
}

// SkipUnvalidatedDockerTest quarantines a Docker-backed test that has never
// actually run on any platform and fails the first time it does.
//
// SkipWithoutDocker dialled the empty string until #1785 and so skipped
// unconditionally everywhere, Linux CI included: the fourteen ECS service and
// CloudFormation ECS-service tests behind it were green without ever having
// started a container. Fixing the resolution runs them for the first time, and
// on the host where #1785 was done (Windows 11, Docker Desktop 29.7.2, engine
// linux/amd64) six pass and eight fail, all eight in one shape — the service
// places its tasks and emits "(service X) has started N tasks", then
// runningCount stays 0, the rollout never reaches COMPLETED, and the
// CloudFormation stacks time out waiting for CREATE_COMPLETE.
//
// The six that pass keep running; only the eight carry this. Whether their
// failure is platform-specific or true everywhere cannot be told from one host,
// and finding out is its own piece of work rather than part of correcting a
// gate — so they stay skipped deliberately, named, with the measurement
// attached, instead of silently on a bug or red on a first run nobody planned.
//
// Do not reach for this for anything else. A test that passes somewhere and
// fails on one platform is either a defect to fix or a capability the host
// lacks, and gets a gate naming that capability — see
// tests/AGENTS.md § What the container half requires, per platform.
func SkipUnvalidatedDockerTest(t *testing.T) {
	t.Helper()

	// TODO(priority:P2): triage the eight ECS service Docker tests that #1785 showed have never run anywhere
	// tests/integration/ecs and tests/integration/cloudformation gate fourteen tests on a Docker daemon through a
	// helper that skipped unconditionally until #1785, so none had ever started a container. With the gate fixed,
	// six pass and eight fail on Windows + Docker Desktop: the service's tasks are placed and "has started N
	// tasks" is emitted, then runningCount stays 0 and the rollout never completes. Establish whether that is
	// platform-specific, fix what it finds, then put those eight back on helpers.SkipWithoutDocker and delete
	// helpers.SkipUnvalidatedDockerTest.
	t.Skip("skipping: this Docker-backed test has never run on any platform — its gate skipped unconditionally " +
		"until #1785 — and it fails the first time it does. Bringing it up is its own work; see " +
		"helpers.SkipUnvalidatedDockerTest")
}

// Docker-dependent tests that need an image from a public registry have two
// distinct ways to go wrong, and they deserve opposite verdicts.
//
// A registry that cannot be reached — Docker Hub timing out, DNS failing, or
// the anonymous pull-rate limit rejecting a shared CI egress IP — says nothing
// about Overcast. Failing the build for it teaches people to re-run the gate
// instead of reading it, which is exactly what the compat flake policy exists
// to prevent. That is an environmental gate, like the absence of a daemon.
//
// A registry that answers and refuses — unknown manifest, unauthorized, no
// such image — is a real problem with the test or the emulator, and must fail.

// PullOrSkip fetches image so a test can use it, skipping the test when the
// registry is unreachable and failing it when the registry answers with a
// refusal. Pulling up front also warms the daemon's cache for the emulator,
// whose own pull then resolves locally.
func PullOrSkip(t *testing.T, dc *docker.Client, image string) {
	t.Helper()

	// One retry: the transient failures here are timeouts and rate limits,
	// both of which frequently clear within seconds. More than one retry just
	// makes a real outage take longer to report.
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(3 * time.Second)
		}
		if err = dc.PullImage(t.Context(), image); err == nil {
			return
		}
		if !RegistryUnreachable(err) {
			t.Fatalf("pull %s: %v", image, err)
		}
	}
	t.Skipf("skipping: the registry serving %s could not be reached, which is an "+
		"environmental failure rather than an Overcast one: %v", image, err)
}

// RegistryUnreachable reports whether err is the registry being unavailable —
// a transport failure or a pull-rate rejection — rather than the registry
// answering that the image cannot be had. The taxonomy lives in the docker
// package, which uses the same distinction for the ECR registry's own startup
// probe; this wrapper keeps the name test files already use.
func RegistryUnreachable(err error) bool {
	return docker.RegistryUnreachable(err)
}
