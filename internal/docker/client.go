// Package docker provides a thin Docker Engine API client.
//
// Supported endpoints:
//   - Unix socket: "/var/run/docker.sock" (Linux / macOS default)
//   - Named pipe: "npipe:////./pipe/docker_engine" (Windows default)
//   - TCP: "tcp://host:port" (DinD sidecars, all platforms)
//
// This avoids pulling in the massive github.com/docker/docker SDK with its
// transitive dependencies (otel, protobuf, etc.). We only need a handful of
// API calls: create/start/stop/remove container, pull image, create/inspect
// network. The Docker Engine API is stable REST over a Unix socket or TCP.
//
// Reference: https://docs.docker.com/engine/api/v1.45/
package docker

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Client is a lightweight Docker Engine API client.
type Client struct {
	httpClient *http.Client
	host       string // base URL for API requests
	logger     *zap.Logger
	sem        chan struct{} // bounds concurrent mutating Docker operations

	// apiVersion is the daemon's highest supported API version, read once
	// from GET /version and held for the life of the client — see
	// APIVersionAtLeast. Empty until the first call that needs it.
	apiVersionMu sync.Mutex
	apiVersion   string
}

// apiVersionPinned is the API version every request path here carries. It is
// the floor: a daemon older than this (Docker 26) is not supported, and a
// request that needs something newer negotiates it — see APIVersionAtLeast —
// rather than raising the floor for every call.
//
// The other request paths in this file still spell "/v1.45/" literally. They
// are not wrong, and rewriting all 37 of them to interpolate this constant is
// a change to every Docker call in the codebase for no behavioural gain — so
// it is deliberately not done here, where the subject is one connect. A
// mechanical sweep is welcome on its own.
const apiVersionPinned = "1.45"

// apiVersionGatewayPriority is the first Docker API version whose network
// connect accepts EndpointSettings.GwPriority (Docker 28.0). Sent under an
// older path the field is dropped on the daemon side, so a connect that sets
// it goes out under this version when the daemon speaks it.
const apiVersionGatewayPriority = "1.48"

// maxConcurrentOps limits how many container create/start/stop/remove
// operations run concurrently.  Under high load (4 compat suites), the
// Docker daemon becomes overwhelmed by hundreds of simultaneous requests.
// This semaphore provides natural backpressure — each operation waits for
// a slot, keeping the daemon responsive.
const maxConcurrentOps = 8

// maxDockerConns keeps short Docker API calls responsive while long-lived
// connections are open — ECS's awslogs followers hold one per followed task
// container. (Lambda's did too, one per warm container, until the in-container
// init replaced daemon read-back for function logs — its log channel runs over
// Overcast's own Runtime API listener and never touches this transport.)
const maxDockerConns = 64

// NewClient creates a Docker client for the given endpoint.
//
// The endpoint can be:
//   - A Unix socket path: "/var/run/docker.sock" (Linux / macOS)
//   - A Windows named pipe: "npipe:////./pipe/docker_engine" (Windows)
//   - A TCP address: "tcp://host:port" (for DinD sidecars, all platforms)
//
// Use the package-level defaultDockerSocket constant for the platform default.
func NewClient(endpoint string, logger *zap.Logger) *Client {
	// Defaulted rather than assumed: the API-version negotiation below is the
	// first code here to dereference this, so a caller that passed nil — every
	// one of them was fine until now — would newly panic on a network connect.
	if logger == nil {
		logger = zap.NewNop()
	}
	transport, host := Transport(endpoint)
	return &Client{
		httpClient: &http.Client{Transport: transport},
		host:       host,
		logger:     logger,
		sem:        make(chan struct{}, maxConcurrentOps),
	}
}

// Transport returns the http.Transport this package dials endpoint with, and
// the base URL its Engine API paths hang off — "http://docker" for a Unix
// socket or a Windows named pipe, "http://host:port" for tcp://. NewClient is
// built from exactly this, so the two can never drift.
//
// It is exported for the one caller that needs an Engine API endpoint Client
// does not implement: the image build, which nothing in Overcast uses and only
// tests/integration/lambdadocker drives. Reaching for the transport is the
// supported way to do that; hand-rolling an http.Client is not.
//
// The reason it is not, concretely. That package built its own client with
// `net.Dialer.DialContext(ctx, "unix", socket)` and passed it the endpoint
// Overcast had resolved — correct on Linux and macOS, and on Windows a request
// to dial a named pipe as though it were a filesystem socket:
//
//	dial unix npipe:////./pipe/docker_engine: connect: An invalid argument was supplied
//
// Six tests failed that way once the package's Docker gate started letting them
// run on Windows (#1785, from #1776). Only dialEndpoint knows that npipe://
// means winio.DialPipeContext, and only one place should have to.
//
// The returned transport is fresh on every call and belongs to the caller,
// which may adjust it — a long-running endpoint that streams progress before
// finishing has different timeout needs from the short calls Client makes.
func Transport(endpoint string) (*http.Transport, string) {
	dialFn, host := dialEndpoint(endpoint)
	transport := &http.Transport{
		MaxConnsPerHost:       maxDockerConns,
		MaxIdleConns:          maxDockerConns,
		MaxIdleConnsPerHost:   maxDockerConns,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if dialFn != nil {
		transport.DialContext = dialFn
	}
	return transport, host
}

// acquireOp blocks until a concurrent-operation slot is available.  Call
// before mutating Docker state (create/start/stop/remove).  The caller
// MUST call releaseOp when done.  Uses the context for cancellation.
func (d *Client) acquireOp(ctx context.Context) error {
	if d.sem == nil {
		return nil
	}
	select {
	case d.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *Client) releaseOp() {
	if d.sem == nil {
		return
	}
	<-d.sem
}

// ─── Container types ───────────────────────────────────────────────────────

// ContainerConfig describes the container's runtime configuration.
type ContainerConfig struct {
	Image        string              `json:"Image"`
	Env          []string            `json:"Env,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
}

// Standard labels applied by Overcast services to Docker resources (containers
// and networks). The Docker watcher filters on LabelManaged so it only sees
// our resources.
const (
	// LabelManaged marks a resource as Overcast-managed.
	LabelManaged = "overcast.managed"
	// LabelService identifies which Overcast service owns the resource
	// (e.g. "lambda", "ecs", "rds", "ec2").
	LabelService = "overcast.service"
	// LabelResourceID identifies the logical resource that owns the
	// Docker resource (e.g. function name, ECS task ID, VPC ID).
	LabelResourceID = "overcast.resource-id"
	// LabelInstance identifies the Overcast instance that created the
	// resource, so a sweep can tell its own litter from a resource another
	// instance on the same daemon is still using.
	//
	// A sweep that decides a resource is abandoned by failing to find its
	// owner in the sweeping instance's own records is wrong the moment two
	// Overcasts share a daemon: they keep separate records, so each sees the
	// other's live resources as abandoned. Where the daemon can answer the
	// question instead — a volume no container references is dangling — prefer
	// that (see ListUnusedVolumes). Where it cannot, because being
	// unreferenced is the resource's normal resting state, this label is how
	// a sweep stays inside its own scope.
	//
	// The value is the identity of the *data directory* whose store owns the
	// resource, not of the process and not of the store's contents: instances
	// sharing a data directory share records, so they share a sweep domain,
	// and a store that is wiped or memory-backed still resolves to the same
	// identity, so the resources of the run before it are still its own to
	// reclaim. See serviceutil.InstanceIdentity and serviceutil.DataDirAnchor.
	//
	// Absence is not permission. A resource without this label predates the
	// label or belongs to something else; either way its owner cannot be
	// established, so it must not be swept.
	LabelInstance = "overcast.instance"

	// LabelLambdaInitVersion and LabelLambdaInitArch mark the volume holding
	// the in-container Lambda init. They live here rather than in the lambda
	// package so that every label key Overcast stamps on a Docker resource can
	// be read in one place — which is what makes it possible to tell, from
	// this file alone, that no other service's sweep can see this volume.
	//
	// The version is the first 12 hex characters of the SHA-256 of the init
	// binary itself, so an Overcast built with a different init addresses a
	// different volume and can never run against a stale one.
	LabelLambdaInitVersion = "overcast.lambda.init.version"
	// LabelLambdaInitArch is the GOARCH the init in the volume was built for.
	LabelLambdaInitArch = "overcast.lambda.init.arch"
)

// ServiceCore is the LabelService value for resources that belong to Overcast
// itself rather than to any one emulated service — the two planes
// (`overcast` and `overcast_control`).
//
// They are labelled at all so that `docker network ls` and `overcast network
// reset` can find them: an unlabelled network is one nothing can verify,
// account for, or safely rebuild, which is the state that made #1564 invisible.
// The value is deliberately not any service's name, so a service's own
// reconcile can never see a plane among the networks it is asked to sweep.
const ServiceCore = "core"

// ManagedLabels returns the standard Overcast labels for a Docker resource.
// All services should use this instead of constructing the map inline.
func ManagedLabels(service, resourceID string) map[string]string {
	return map[string]string{
		LabelManaged:    "true",
		LabelService:    service,
		LabelResourceID: resourceID,
	}
}

// HostConfig describes the host-side container configuration.
type HostConfig struct {
	Binds        []string                 `json:"Binds,omitempty"`
	NetworkMode  string                   `json:"NetworkMode,omitempty"`
	Memory       int64                    `json:"Memory,omitempty"`     // bytes
	MemorySwap   int64                    `json:"MemorySwap,omitempty"` // bytes (-1 = unlimited)
	NanoCPUs     int64                    `json:"NanoCPUs,omitempty"`   // 1e9 = 1 CPU
	AutoRemove   bool                     `json:"AutoRemove,omitempty"`
	PortBindings map[string][]PortBinding `json:"PortBindings,omitempty"`
	Privileged   bool                     `json:"Privileged,omitempty"` // required by k3s
	Tmpfs        map[string]string        `json:"Tmpfs,omitempty"`      // tmpfs mounts (path → options)
	// CapAdd grants individual Linux capabilities on top of Docker's default
	// set, named without the "CAP_" prefix (e.g. "DAC_READ_SEARCH"). Prefer it
	// to Privileged: one capability is auditable, --privileged is not.
	CapAdd []string `json:"CapAdd,omitempty"`
	// ExtraHosts are "hostname:target" entries written into the container's
	// /etc/hosts, where target is an IP or Docker's "host-gateway". /etc/hosts
	// wins over DNS in glibc and musl, so an entry here shadows a public record
	// for the same name inside this container only.
	ExtraHosts []string `json:"ExtraHosts,omitempty"`
	// Dns sets the container's resolvers. Docker keeps its own embedded
	// resolver (127.0.0.11) in front and uses these as its upstream, so
	// container-name service discovery is unaffected — but names these servers
	// claim are answered by them, including wildcard subdomains that ExtraHosts
	// cannot express. See internal/dns.
	Dns []string `json:"Dns,omitempty"`
	// Mounts is the structured alternative to Binds. Required for named-volume
	// mounts that need VolumeOptions (e.g. Subpath); plain binds can stay in
	// Binds — Docker merges both.
	Mounts []Mount `json:"Mounts,omitempty"`
}

// Mount is one entry of HostConfig.Mounts (Engine API mounts specification).
type Mount struct {
	// Type is "volume", "bind", or "tmpfs"; Overcast uses "volume" and "bind".
	Type string `json:"Type"`
	// Source is the volume name for Type "volume", or an absolute path on the
	// daemon's host for Type "bind". A bind whose source does not exist is
	// rejected by the daemon rather than created — which is what makes a
	// mistyped host path a visible failure instead of an empty directory
	// silently shadowing the container's own.
	Source   string `json:"Source"`
	Target   string `json:"Target"` // absolute path inside the container
	ReadOnly bool   `json:"ReadOnly,omitempty"`
	// VolumeOptions apply when Type is "volume".
	VolumeOptions *MountVolumeOptions `json:"VolumeOptions,omitempty"`
}

// MountVolumeOptions holds volume-mount-specific options.
type MountVolumeOptions struct {
	// Subpath mounts only the named subdirectory of the volume (Engine API
	// v1.45+). The subdirectory must already exist in the volume — the daemon
	// rejects the mount otherwise.
	Subpath string `json:"Subpath,omitempty"`
}

// PortBinding represents a host-to-container port mapping.
type PortBinding struct {
	HostIP   string `json:"HostIp,omitempty"`
	HostPort string `json:"HostPort,omitempty"`
}

// NetworkingConfig specifies the container's networking configuration.
type NetworkingConfig struct {
	EndpointsConfig map[string]*EndpointSettings `json:"EndpointsConfig,omitempty"`
}

// EndpointSettings describes a container's attachment to a Docker network.
type EndpointSettings struct {
	// Empty settings are enough to attach to a network. Aliases are advertised by
	// Docker's embedded DNS to containers on the same user-defined network.
	Aliases []string `json:"Aliases,omitempty"`
	// IPAMConfig pins the address the container gets on this network. Left nil,
	// Docker's own IPAM picks one.
	IPAMConfig *EndpointIPAMConfig `json:"IPAMConfig,omitempty"`

	// GwPriority ranks this network as the source of the container's default
	// route. A container on two routable networks takes its default route from
	// the one with the highest priority; at equal priority Docker picks by
	// network name, lexicographically, which is how a route out gets decided
	// by an accident of naming. Zero is Docker's default.
	//
	// Honoured by Docker 28.0+ (API 1.48): ConnectNetworkWithConfig sends a
	// non-zero value under that version when the daemon supports it and drops
	// it, with a log line, when it does not — see APIVersionAtLeast.
	GwPriority int `json:"GwPriority,omitempty"`
}

// EndpointIPAMConfig requests a specific address on a network. Docker rejects
// the connect outright when the address is outside the network's subnet or
// already taken, so callers that cannot guarantee either must be prepared to
// retry without it.
type EndpointIPAMConfig struct {
	IPv4Address string `json:"IPv4Address,omitempty"`
}

// EndpointAliases returns unique, non-IP hostnames suitable for Docker DNS aliases.
func EndpointAliases(addresses ...string) []string {
	out := make([]string, 0, len(addresses))
	seen := make(map[string]struct{}, len(addresses))
	for _, address := range addresses {
		if address == "" || net.ParseIP(address) != nil || address == "127.0.0.1" || address == "localhost" {
			continue
		}
		if _, ok := seen[address]; ok {
			continue
		}
		seen[address] = struct{}{}
		out = append(out, address)
	}
	return out
}

// CreateContainerRequest combines all container creation parameters.
type CreateContainerRequest struct {
	*ContainerConfig
	HostConfig       *HostConfig       `json:"HostConfig,omitempty"`
	NetworkingConfig *NetworkingConfig `json:"NetworkingConfig,omitempty"`
	Platform         string            `json:"-"`
}

// CreateContainerResponse is the response from container creation.
type CreateContainerResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings,omitempty"`
}

// ImageInspect holds the metadata returned by Docker image inspect: the
// platform, and the parts of the image's own configuration a caller needs when
// it takes the entrypoint over.
type ImageInspect struct {
	Architecture string      `json:"Architecture"`
	OS           string      `json:"Os"`
	Config       ImageConfig `json:"Config"`
}

// ImageConfig is the image's baked-in run configuration. Only the fields a
// caller that replaces the entrypoint has to reproduce are modelled: Lambda's
// in-container init runs as the container's entrypoint and launches the image's
// original ENTRYPOINT+CMD as its child, so it has to know what that command
// was — the daemon can no longer merge it in, because the entrypoint the
// daemon is given is the init.
type ImageConfig struct {
	Entrypoint []string `json:"Entrypoint"`
	Cmd        []string `json:"Cmd"`
	WorkingDir string   `json:"WorkingDir"`
}

// ContainerInspect holds container state and networking details.
type ContainerInspect struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"` // e.g. "/overcast-rds-mydb"
	Labels map[string]string `json:"Labels"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"` // "created", "running", "exited", etc.
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`     // runtime error, e.g. "OCI runtime create failed: ..."
		OOMKilled  bool   `json:"OOMKilled"` // true if the kernel OOM-killer terminated the container
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	HostConfig struct {
		Binds []string `json:"Binds"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		// Networks is keyed by network *name*; ContainerNetwork.NetworkID is
		// the only way to match an entry against a network known by ID.
		Networks map[string]ContainerNetwork `json:"Networks"`
		// Ports maps "containerPort/proto" → list of host bindings.
		// e.g. "3306/tcp" → [{"HostIp":"0.0.0.0","HostPort":"33060"}]
		Ports map[string][]PortBinding `json:"Ports"`
	} `json:"NetworkSettings"`
}

// ExitReason derives the concise reason container-backed services expose for
// an exited container. Keeping the precedence here makes live Docker events
// and reconciliation after a missed event describe the same exit identically.
func (c *ContainerInspect) ExitReason() string {
	if c == nil {
		return ""
	}
	switch {
	case c.State.OOMKilled:
		return "oom"
	case c.State.Error != "":
		return c.State.Error
	case c.State.ExitCode != 0:
		return fmt.Sprintf("exit %d", c.State.ExitCode)
	default:
		return ""
	}
}

// ExitTime returns Docker's recorded container finish time. A missing,
// malformed, or zero timestamp stays unknown so callers can fall back to
// their service clock without inventing historical precision.
func (c *ContainerInspect) ExitTime() time.Time {
	if c == nil || c.State.FinishedAt == "" {
		return time.Time{}
	}
	finished, err := time.Parse(time.RFC3339Nano, c.State.FinishedAt)
	if err != nil || finished.IsZero() {
		return time.Time{}
	}
	return finished
}

// ContainerNetwork is one entry of a container's NetworkSettings.Networks.
type ContainerNetwork struct {
	NetworkID string `json:"NetworkID"`
	IPAddress string `json:"IPAddress"`
	// Gateway is the network's gateway address — on a bridge network, the
	// address at which a container reaches services bound on the daemon host.
	Gateway string `json:"Gateway"`
	// Aliases are the DNS names this container answers to on this network,
	// which is how a resource's endpoint hostname is advertised. Docker returns
	// them per endpoint, so the same container can carry different names on
	// different networks.
	Aliases []string `json:"Aliases"`
}

// HasOvercastLabels reports whether the container was created by Overcast with
// the given service name and resource ID. Use this before reusing a container
// found by name to avoid accidentally attaching to a user-created container
// that happens to share the same name.
//
// It says nothing about *which* Overcast: resource IDs are user-chosen names,
// and two instances sharing a daemon can each have created a container for the
// same one. Pair it with Instance where that matters.
func (c *ContainerInspect) HasOvercastLabels(service, resourceID string) bool {
	labels := c.labels()
	return labels[LabelManaged] == "true" &&
		labels[LabelService] == service &&
		labels[LabelResourceID] == resourceID
}

// Instance returns the overcast.instance label value (empty string if not
// set). See LabelInstance.
func (c *ContainerInspect) Instance() string { return c.labels()[LabelInstance] }

// Managed reports whether the container carries overcast.managed=true — one
// some Overcast instance created, as opposed to a developer's own container
// (or the container the emulator itself runs in) that happens to share a
// network with it.
func (c *ContainerInspect) Managed() bool { return c.labels()[LabelManaged] == "true" }

// labels returns the container's labels, preferring the modern inspect shape.
func (c *ContainerInspect) labels() map[string]string {
	if len(c.Config.Labels) > 0 {
		return c.Config.Labels
	}
	return c.Labels // fallback for older inspect responses
}

// NetworkInspect holds Docker network details.
//
// The field set is everything a NetworkSpec compares a live network against,
// which is why it is this wide: verification that checks only the fields
// somebody happened to think of reports "matches" for a network that does not.
// See VerifyNetwork.
type NetworkInspect struct {
	ID       string            `json:"Id"`
	Name     string            `json:"Name"`
	Created  time.Time         `json:"Created"`
	Internal bool              `json:"Internal"`
	Labels   map[string]string `json:"Labels"`
	IPAM     NetworkIPAM       `json:"IPAM"`

	// Driver is the network driver — "bridge" for everything Overcast creates.
	// A network of the right name under the wrong driver behaves nothing like
	// the one that was asked for, and after Internal it is the most
	// consequential field here.
	Driver string `json:"Driver"`

	// Scope is "local" for everything on a single daemon. "swarm" or "global"
	// means the network is not this daemon's alone to recreate, and a repair
	// must not be attempted on it.
	Scope string `json:"Scope"`

	// EnableIPv6 changes which addresses containers get, and which of them
	// Overcast's own resolver can answer with.
	EnableIPv6 bool `json:"EnableIPv6"`

	// Options are the driver options. `com.docker.network.bridge.enable_icc`
	// and `...enable_ip_masquerade` are the two that decide whether containers
	// on the bridge may talk to each other and whether their egress is
	// source-NATed — both settings a network can silently disagree on while
	// looking correct in every other field.
	Options map[string]string `json:"Options"`

	// Containers is the endpoints currently attached, keyed by container ID.
	// Emptiness decides whether a mismatched network can be repaired in place:
	// one with nothing on it is removed and recreated to apply a setting Docker
	// will not change in place, and one with something on it cannot be. The
	// names are reported to the operator so a warning can say what has to be
	// stopped rather than leaving them to find out.
	Containers map[string]NetworkEndpoint `json:"Containers"`
}

// Instance returns the overcast.instance label value — the Overcast instance
// that created this network — or "" when it carries none. See LabelInstance:
// absence is not permission.
func (n *NetworkInspect) Instance() string { return n.Labels[LabelInstance] }

// Subnet returns the first IPAM subnet, or "" when IPAM was left to Docker.
func (n *NetworkInspect) Subnet() string {
	if len(n.IPAM.Config) == 0 {
		return ""
	}
	return n.IPAM.Config[0].Subnet
}

// Gateway returns the first IPAM gateway, or "" when IPAM was left to Docker.
func (n *NetworkInspect) Gateway() string {
	if len(n.IPAM.Config) == 0 {
		return ""
	}
	return n.IPAM.Config[0].Gateway
}

// AttachedNames lists the containers currently on the network, sorted, for a
// message that has to say what is in the way. Falls back to the container ID
// for an endpoint the daemon reported without a name.
func (n *NetworkInspect) AttachedNames() []string {
	names := make([]string, 0, len(n.Containers))
	for id, ep := range n.Containers {
		if ep.Name != "" {
			names = append(names, ep.Name)
			continue
		}
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

// NetworkEndpoint is one container's attachment to a network, as reported by
// network inspect.
type NetworkEndpoint struct {
	Name        string `json:"Name"`
	EndpointID  string `json:"EndpointID"`
	IPv4Address string `json:"IPv4Address"`
}

// NetworkIPAM describes IP address management for a Docker network.
type NetworkIPAM struct {
	Config []NetworkIPAMConfig `json:"Config"`
}

// NetworkIPAMConfig describes one IPAM pool.
type NetworkIPAMConfig struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

// NetworkSummary is a lightweight network representation used by ListNetworks.
type NetworkSummary struct {
	ID      string            `json:"Id"`
	Name    string            `json:"Name"`
	Created time.Time         `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	IPAM    NetworkIPAM       `json:"IPAM"`

	// Internal is reported by the list endpoint as well as by inspect, which
	// saves an inspect per network when all a caller wants is the isolation.
	Internal bool `json:"Internal"`

	// Driver is the network driver, as on NetworkInspect.
	Driver string `json:"Driver"`
}

// Subnet returns the first IPAM subnet for the network, or empty if unset.
func (n *NetworkSummary) Subnet() string {
	if len(n.IPAM.Config) == 0 {
		return ""
	}
	return n.IPAM.Config[0].Subnet
}

// Service returns the overcast.service label value (empty string if not set).
func (n *NetworkSummary) Service() string { return n.Labels[LabelService] }

// ResourceID returns the overcast.resource-id label value (empty string if not set).
func (n *NetworkSummary) ResourceID() string { return n.Labels[LabelResourceID] }

// Instance returns the overcast.instance label value, or "" when the network
// does not carry one. See LabelInstance.
func (n *NetworkSummary) Instance() string { return n.Labels[LabelInstance] }

// VPCRole returns the network's role within its VPC (LabelVPCRole), reading an
// absent label as VPCRolePlane.
func (n *NetworkSummary) VPCRole() string { return vpcRole(n.Labels) }

// VPCRole is NetworkSummary.VPCRole for an inspected network.
func (n *NetworkInspect) VPCRole() string { return vpcRole(n.Labels) }

func vpcRole(labels map[string]string) string {
	if role := labels[LabelVPCRole]; role != "" {
		return role
	}
	return VPCRolePlane
}

// ContainerSummary is the lightweight container representation returned by
// GET /containers/json (list endpoint), as opposed to the full ContainerInspect
// returned by GET /containers/{id}/json.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"` // e.g. ["/overcast-rds-mydb"]
	Image  string            `json:"Image"`
	State  string            `json:"State"`  // "running", "exited", "created", etc.
	Status string            `json:"Status"` // human-readable, e.g. "Up 2 hours"
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		HostPort      int    `json:"PublicPort"`
		ContainerPort int    `json:"PrivatePort"`
		Type          string `json:"Type"`
	} `json:"Ports"`
}

// Service returns the overcast.service label value (empty string if not set).
func (c *ContainerSummary) Service() string { return c.Labels[LabelService] }

// ResourceID returns the overcast.resource-id label value (empty string if not set).
func (c *ContainerSummary) ResourceID() string { return c.Labels[LabelResourceID] }

// Instance returns the overcast.instance label value (empty string if not
// set). See LabelInstance.
func (c *ContainerSummary) Instance() string { return c.Labels[LabelInstance] }

// FirstName returns the primary container name without the leading slash.
func (c *ContainerSummary) FirstName() string {
	if len(c.Names) == 0 {
		return ""
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// ContainersByService partitions a daemon-wide snapshot by the service label.
// When services is non-empty, unrelated containers are not retained in the
// index. The returned slices contain values so callers can retain the index
// without depending on the lifetime or backing array of the input snapshot.
func ContainersByService(containers []ContainerSummary, services ...string) map[string][]ContainerSummary {
	var wanted map[string]struct{}
	if len(services) > 0 {
		wanted = stringSet(services)
	}
	byService := make(map[string][]ContainerSummary, len(wanted))
	for i := range containers {
		if service := containers[i].Service(); service != "" {
			if len(wanted) > 0 {
				if _, ok := wanted[service]; !ok {
					continue
				}
			}
			byService[service] = append(byService[service], containers[i])
		}
	}
	return byService
}

// ContainersByResource indexes a service-scoped snapshot by resource label.
// Multiple containers may legitimately share a resource ID when independent
// Overcast instances use the same daemon, so every candidate is retained for
// InstanceDomain to resolve.
func ContainersByResource(containers []ContainerSummary) map[string][]*ContainerSummary {
	byResource := make(map[string][]*ContainerSummary, len(containers))
	for i := range containers {
		if resourceID := containers[i].ResourceID(); resourceID != "" {
			byResource[resourceID] = append(byResource[resourceID], &containers[i])
		}
	}
	return byResource
}

// NetworksByService partitions a daemon-wide network snapshot by service.
// When services is non-empty, unrelated networks are not retained.
func NetworksByService(networks []NetworkSummary, services ...string) map[string][]NetworkSummary {
	var wanted map[string]struct{}
	if len(services) > 0 {
		wanted = stringSet(services)
	}
	byService := make(map[string][]NetworkSummary, len(wanted))
	for i := range networks {
		if service := networks[i].Service(); service != "" {
			if len(wanted) > 0 {
				if _, ok := wanted[service]; !ok {
					continue
				}
			}
			byService[service] = append(byService[service], networks[i])
		}
	}
	return byService
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

// ─── API helpers ───────────────────────────────────────────────────────────

func (d *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	return d.doRequestWithHeaders(ctx, method, path, body, nil)
}

// doRequestWithHeaders is doRequest with additional request headers, for the
// few endpoints that carry one (X-Registry-Auth on an authenticated pull).
func (d *Client) doRequestWithHeaders(ctx context.Context, method, path string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, d.host+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return d.httpClient.Do(req)
}

func (d *Client) doJSON(ctx context.Context, method, path string, reqBody interface{}, respBody interface{}) error {
	var body io.Reader
	if reqBody != nil {
		data, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = strings.NewReader(string(data))
	}
	resp, err := d.doRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker %s %s: %d: %s", method, path, resp.StatusCode, string(errBody))
	}

	if respBody != nil {
		return json.NewDecoder(resp.Body).Decode(respBody)
	}
	return nil
}

// ─── Container operations ──────────────────────────────────────────────────

// Ping checks Docker daemon connectivity.
func (d *Client) Ping(ctx context.Context) error {
	resp, err := d.doRequest(ctx, http.MethodGet, "/_ping", nil)
	if err != nil {
		return fmt.Errorf("docker ping: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker ping: status %d", resp.StatusCode)
	}
	return nil
}

// SystemInfo is the subset of GET /info Overcast reads: the resources of the
// machine the daemon runs containers on. That machine is not necessarily the
// one the Overcast process runs on — with Docker Desktop it is the desktop VM,
// with a DinD sidecar or a tcp:// endpoint it is another host entirely — so
// sizing decisions about containers must come from here, never from
// runtime.NumCPU() or the process's own view of memory.
type SystemInfo struct {
	// NCPU is the number of logical CPUs available to the daemon.
	NCPU int `json:"NCPU"`
	// MemTotal is the total memory available to the daemon, in bytes.
	MemTotal int64 `json:"MemTotal"`
	// ID is the daemon's own identity — stable across its restarts, and
	// different for every daemon this process might be pointed at. It is what
	// a cached fact *about a daemon* has to be keyed on: the same binary
	// against Docker Desktop and then against a native daemon is two different
	// questions, and a cache keyed on the host would answer the second with
	// the first. See containerendpoint's remembered Runtime API address.
	ID string `json:"ID"`
}

// Info returns the daemon's system information (GET /info).
func (d *Client) Info(ctx context.Context) (*SystemInfo, error) {
	var info SystemInfo
	if err := d.doJSON(ctx, http.MethodGet, "/v1.45/info", nil, &info); err != nil {
		return nil, fmt.Errorf("docker info: %w", err)
	}
	return &info, nil
}

// CreateContainer creates a container (does not start it).
func (d *Client) CreateContainer(ctx context.Context, name string, req *CreateContainerRequest) (string, error) {
	if err := d.acquireOp(ctx); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	defer d.releaseOp()

	path := "/v1.45/containers/create"
	query := url.Values{}
	if name != "" {
		query.Set("name", name)
	}
	if req != nil && req.Platform != "" {
		query.Set("platform", req.Platform)
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var resp CreateContainerResponse
	if err := d.doJSON(ctx, http.MethodPost, path, req, &resp); err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

// StartContainer starts a previously created container.
func (d *Client) StartContainer(ctx context.Context, id string) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	defer d.releaseOp()

	resp, err := d.doRequest(ctx, http.MethodPost, "/v1.45/containers/"+id+"/start", nil)
	if err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified {
		// The body names the actual failure — a port already allocated, an OCI
		// runtime error — and callers branch on it (see IsPortUnavailable), so
		// a bare status code is not enough.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("start container %s: status %d: %s", id, resp.StatusCode, string(body))
	}
	return nil
}

// StopContainer stops a running container with a timeout.
func (d *Client) StopContainer(ctx context.Context, id string, timeoutSec int) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	defer d.releaseOp()

	path := fmt.Sprintf("/v1.45/containers/%s/stop?t=%d", id, timeoutSec)
	resp, err := d.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	resp.Body.Close()
	// 204 = stopped, 304 = already stopped, 404 = not found (already removed)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotModified && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("stop container %s: status %d", id, resp.StatusCode)
	}
	return nil
}

// ExecResult reports what a command run inside a container did.
type ExecResult struct {
	// ExitCode is the command's exit status. Zero means it succeeded.
	ExitCode int
	// Output is stdout and stderr interleaved, as a terminal would show them,
	// bounded by maxExecOutputBytes. It is what a caller quotes when the
	// command failed, so it is the command's own explanation and not a
	// paraphrase of it.
	Output string
}

// maxExecOutputBytes bounds what Exec keeps. Exec is for short administrative
// commands whose output is a diagnostic, not a data stream; anything that
// produces more than this is not a command Exec should be running.
const maxExecOutputBytes = 64 * 1024

// Exec runs a command inside a running container and waits for it to finish.
// It returns the command's own exit status and output — a non-zero ExitCode is
// not an error here, because "the command ran and refused" is an answer the
// caller has to be able to tell apart from "the command never ran".
//
// cmd is passed to the container verbatim, with no shell in between: quoting
// and word splitting never happen, so an argument containing spaces, quotes or
// a `$` reaches the process as one argument exactly as written. env adds to the
// container's own environment for this command only.
//
// The exec is created with a TTY, which is what `docker exec -t` does. It costs
// the ability to tell stdout from stderr — neither of which a caller of this
// method distinguishes — and buys a plain byte stream instead of Docker's
// 8-byte-framed multiplexed one.
func (d *Client) Exec(ctx context.Context, id string, cmd, env []string) (ExecResult, error) {
	if err := d.acquireOp(ctx); err != nil {
		return ExecResult{}, fmt.Errorf("exec in container %s: %w", id, err)
	}
	defer d.releaseOp()

	var created struct {
		ID string `json:"Id"`
	}
	createReq := struct {
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
		Tty          bool     `json:"Tty"`
		Cmd          []string `json:"Cmd"`
		Env          []string `json:"Env,omitempty"`
	}{AttachStdout: true, AttachStderr: true, Tty: true, Cmd: cmd, Env: env}
	if err := d.doJSON(ctx, http.MethodPost, "/v1.45/containers/"+id+"/exec", createReq, &created); err != nil {
		return ExecResult{}, fmt.Errorf("exec in container %s: create: %w", id, err)
	}
	if created.ID == "" {
		return ExecResult{}, fmt.Errorf("exec in container %s: daemon returned no exec ID", id)
	}

	// Detach=false streams the command's output on this connection and closes
	// it when the command exits, so reading to EOF is how we wait for it.
	startBody := strings.NewReader(`{"Detach":false,"Tty":true}`)
	resp, err := d.doRequest(ctx, http.MethodPost, "/v1.45/exec/"+created.ID+"/start", startBody)
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec in container %s: start: %w", id, err)
	}
	output, readErr := io.ReadAll(io.LimitReader(resp.Body, maxExecOutputBytes))
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return ExecResult{}, fmt.Errorf("exec in container %s: start: status %d: %s",
			id, resp.StatusCode, strings.TrimSpace(string(output)))
	}
	if readErr != nil {
		return ExecResult{}, fmt.Errorf("exec in container %s: read output: %w", id, readErr)
	}

	var inspect struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/v1.45/exec/"+created.ID+"/json", nil, &inspect); err != nil {
		return ExecResult{}, fmt.Errorf("exec in container %s: inspect: %w", id, err)
	}
	if inspect.Running {
		// The stream closed while the daemon still calls the command running,
		// so the exit status we would report is not one yet. Saying so beats
		// reporting the zero value as success.
		return ExecResult{}, fmt.Errorf("exec in container %s: command still running after its output stream closed", id)
	}

	return ExecResult{ExitCode: inspect.ExitCode, Output: string(output)}, nil
}

// CopyFileFromContainer returns the raw bytes of a file path from inside a
// container using Docker's archive endpoint.
func (d *Client) CopyFileFromContainer(ctx context.Context, id, path string) ([]byte, error) {
	endpoint := "/v1.45/containers/" + id + "/archive?path=" + url.QueryEscape(path)
	resp, err := d.doRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("copy file from container %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("copy file from container %s: status %d: %s", id, resp.StatusCode, string(errBody))
	}

	tr := tar.NewReader(resp.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read container archive: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read container file %s: %w", path, err)
		}
		return data, nil
	}

	return nil, fmt.Errorf("file %s not found in container archive", path)
}

// RemoveContainer removes a container. force=true kills it first if running.
func (d *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	defer d.releaseOp()

	path := fmt.Sprintf("/v1.45/containers/%s?force=%t", id, force)
	resp, err := d.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("remove container %s: %w", id, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("remove container %s: status %d", id, resp.StatusCode)
	}
	return nil
}

// RemoveContainerForce removes a container using a background context with a
// deadline, ensuring cleanup always succeeds even when the request context is
// cancelled. Use this for teardown/cleanup paths only.
func (d *Client) RemoveContainerForce(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.RemoveContainer(ctx, id, true)
}

// InspectContainer returns container details.
func (d *Client) InspectContainer(ctx context.Context, id string) (*ContainerInspect, error) {
	var info ContainerInspect
	if err := d.doJSON(ctx, http.MethodGet, "/v1.45/containers/"+id+"/json", nil, &info); err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", id, err)
	}
	return &info, nil
}

// UpdateContainerResources updates resource limits on a running container.
// Only the non-zero fields in the request are applied; zero values are ignored
// by the Docker daemon. Mirrors POST /containers/{id}/update.
func (d *Client) UpdateContainerResources(ctx context.Context, id string, update *UpdateResourcesRequest) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("update container %s resources: %w", id, err)
	}
	defer d.releaseOp()

	path := "/v1.45/containers/" + id + "/update"
	if err := d.doJSON(ctx, http.MethodPost, path, update, nil); err != nil {
		return fmt.Errorf("update container %s resources: %w", id, err)
	}
	return nil
}

// UpdateResourcesRequest contains the resource fields that can be changed on a
// running container via the Docker Engine API POST /containers/{id}/update.
type UpdateResourcesRequest struct {
	NanoCPUs   int64 `json:"NanoCPUs,omitempty"`   // 1e9 = 1 CPU
	Memory     int64 `json:"Memory,omitempty"`     // bytes
	MemorySwap int64 `json:"MemorySwap,omitempty"` // bytes (-1 = unlimited)
}

// GetContainerByName looks up a container by its name (without the leading "/").
// Returns (nil, nil) if no container with that name exists.
func (d *Client) GetContainerByName(ctx context.Context, name string) (*ContainerInspect, error) {
	// Docker accept inspect by name directly.
	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/containers/"+url.PathEscape(name)+"/json", nil)
	if err != nil {
		return nil, fmt.Errorf("inspect container by name %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("inspect container by name %s: %d: %s", name, resp.StatusCode, string(errBody))
	}
	var info ContainerInspect
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("inspect container by name %s: decode: %w", name, err)
	}
	return &info, nil
}

// IsConflict reports whether an error is a Docker 409 Conflict response
// (e.g. container name already in use).
func IsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), ": 409:")
}

// IsPortUnavailable reports whether a container create/start failure means the
// requested host port could not be bound — someone else holds it. The two
// phrasings are the daemon's own: "port is already allocated" when another
// container holds it, "address already in use" when a host process does.
func IsPortUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "port is already allocated") ||
		strings.Contains(msg, "address already in use")
}

// IsNotFound reports whether an error is a Docker 404 Not Found response.
//
// Helpers in this package report a status two ways: doJSON and the by-name
// inspect build "…: 404: {body}", while every helper that drives doRequest
// itself builds "…: status 404". Matching only the first form meant this
// answered "no" for a 404 from StartContainer, StopContainer, ContainerLogs and
// the rest — so a start against a container Docker had already removed looked
// like an ordinary failure rather than the recoverable "it is gone, rebuild it"
// that it is.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, ": 404:") || strings.Contains(msg, ": status 404")
}

// ListContainers returns all containers (running and stopped) that carry
// overcast.managed=true and optionally overcast.service=<service>.
// Pass an empty service string to list across all services.
func (d *Client) ListContainers(ctx context.Context, service string) ([]ContainerSummary, error) {
	// Docker filter JSON: all=true gives stopped containers too.
	filterMap := map[string][]string{
		"label": {LabelManaged + "=true"},
	}
	if service != "" {
		filterMap["label"] = append(filterMap["label"], LabelService+"="+service)
	}
	filterJSON, err := json.Marshal(filterMap)
	if err != nil {
		return nil, fmt.Errorf("list containers: marshal filters: %w", err)
	}
	path := "/v1.45/containers/json?all=true&filters=" + url.QueryEscape(string(filterJSON))
	var containers []ContainerSummary
	if err := d.doJSON(ctx, http.MethodGet, path, nil, &containers); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return containers, nil
}

// ContainerLogs fetches container stdout+stderr logs (non-streaming).
func (d *Client) ContainerLogs(ctx context.Context, id string, tail string) ([]byte, error) {
	path := fmt.Sprintf("/v1.45/containers/%s/logs?stdout=true&stderr=true&tail=%s",
		id, url.QueryEscape(tail))
	resp, err := d.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("container logs %s: %w", id, err)
	}
	defer resp.Body.Close()
	// A daemon error body is not a log stream. Callers de-frame this payload,
	// and a stripper fed `{"message":"No such container: …"}` reads its first
	// 8 bytes as a frame header and emits the rest as if it were output — so
	// the status check is what keeps an error legible.
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("container logs %s: status %d: %s", id, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64*1024))
}

// ContainerLogsSince fetches the full stdout+stderr log payload for a container
// starting from a given Unix timestamp (seconds). Used for reconciliation —
// after a streaming follower fails or on container teardown — to backfill any
// log frames that the streaming connection may have missed. Output includes
// per-line RFC3339Nano timestamps (timestamps=true) so the caller can
// deduplicate against events already delivered.
//
// The response is a multiplexed Docker log stream identical in shape to
// ContainerLogsStream's body; wrap it in a DemuxReader to extract payload bytes.
func (d *Client) ContainerLogsSince(ctx context.Context, id string, since time.Time) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1.45/containers/%s/logs?stdout=true&stderr=true&timestamps=true&tail=all", id)
	if !since.IsZero() {
		path += fmt.Sprintf("&since=%d.%09d", since.Unix(), since.Nanosecond())
	}
	resp, err := d.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("container logs since %s: %w", id, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("container logs since %s: status %d", id, resp.StatusCode)
	}
	return resp.Body, nil
}

// ContainerLogsStream opens a streaming connection to the container log endpoint
// with follow=true. The caller is responsible for closing the returned ReadCloser.
// When ctx is cancelled the underlying HTTP connection is closed automatically,
// which causes reads on the stream to return an error, making the reader goroutine
// exit cleanly without an explicit close call.
//
// The since parameter (Unix seconds with nanosecond fraction) lets a caller
// resume after a stream failure without re-receiving lines that were already
// delivered. Pass time.Time{} for "from start of container".
func (d *Client) ContainerLogsStream(ctx context.Context, id string, since time.Time) (io.ReadCloser, error) {
	path := fmt.Sprintf("/v1.45/containers/%s/logs?stdout=true&stderr=true&follow=true&timestamps=true", id)
	if !since.IsZero() {
		path += fmt.Sprintf("&since=%d.%09d", since.Unix(), since.Nanosecond())
	}
	resp, err := d.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("container logs stream %s: %w", id, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("container logs stream %s: status %d", id, resp.StatusCode)
	}
	return resp.Body, nil
}

// WaitContainer blocks until a container exits. Returns the exit code.
func (d *Client) WaitContainer(ctx context.Context, id string) (int, error) {
	resp, err := d.doRequest(ctx, http.MethodPost, "/v1.45/containers/"+id+"/wait", nil)
	if err != nil {
		return -1, fmt.Errorf("wait container %s: %w", id, err)
	}
	defer resp.Body.Close()
	var result struct {
		StatusCode int `json:"StatusCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return -1, fmt.Errorf("wait container %s: decode: %w", id, err)
	}
	return result.StatusCode, nil
}

// WaitContainerRemoved blocks until the daemon confirms a container has been
// fully removed, rather than a caller having to poll and guess how long that
// might still take. A stop/remove call only asks the daemon to remove a
// container — for one with HostConfig.AutoRemove set, it can also be racing
// Docker's own exit-triggered auto-remove (moby's daemon.autoRemove, run on
// the daemon's internal event goroutine, independent of any client request).
// Whichever side the daemon accepted, the container can keep listing in an
// all=true listing with state "removing" — still unmounting layers, still
// holding its name — long after a remove call has already returned. The
// /wait?condition=removed endpoint is the daemon's own signal for the actual
// completion of that work.
//
// Returns nil if the container is already gone by the time this is called —
// the condition is trivially satisfied, and the daemon answers 404 rather
// than blocking on an id it no longer has any record of.
func (d *Client) WaitContainerRemoved(ctx context.Context, id string) error {
	path := "/v1.45/containers/" + id + "/wait?condition=removed"
	var result struct {
		Error *struct {
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := d.doJSON(ctx, http.MethodPost, path, nil, &result); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("wait container removed %s: %w", id, err)
	}
	if result.Error != nil && result.Error.Message != "" {
		return fmt.Errorf("wait container removed %s: %s", id, result.Error.Message)
	}
	return nil
}

// ContainerStats is a single point-in-time resource sample for a container.
// CPU counters are cumulative; a rate needs two samples
// (see the docker CLI formula: Δcpu_total / Δsystem_cpu × online_cpus × 100).
type ContainerStats struct {
	// MemoryUsageBytes is cgroup usage minus inactive file cache — the same
	// number `docker stats` shows, and closer to what a process actually
	// occupies than the raw cgroup counter, which grows with page cache.
	MemoryUsageBytes int64
	// CPUTotalUsage is cumulative container CPU time in nanoseconds.
	CPUTotalUsage uint64
	// SystemCPUUsage is cumulative host CPU time in nanoseconds.
	SystemCPUUsage uint64
	// OnlineCPUs is the number of CPUs available to the container.
	OnlineCPUs int
}

// ContainerStatsOneShot returns one stats sample for a container.
//
// one-shot=true matters: with stream=false alone the daemon waits an extra
// collection cycle (~1–2 s) to pre-fill the CPU delta fields, which both put
// seconds on any synchronous caller and overran short caller timeouts on slow
// (Docker-in-Docker) hosts — surfacing as memory "0" everywhere it was used.
// One-shot returns the current sample immediately; the precpu fields it leaves
// zeroed are ones this client never read anyway.
func (d *Client) ContainerStatsOneShot(ctx context.Context, id string) (ContainerStats, error) {
	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/containers/"+id+"/stats?stream=false&one-shot=true", nil)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("container stats %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ContainerStats{}, fmt.Errorf("container stats %s: status %d", id, resp.StatusCode)
	}
	var stats struct {
		MemoryStats struct {
			Usage int64 `json:"usage"`
			Stats struct {
				// cgroup v2 name; v1 reports total_inactive_file instead.
				InactiveFile      int64 `json:"inactive_file"`
				TotalInactiveFile int64 `json:"total_inactive_file"`
			} `json:"stats"`
		} `json:"memory_stats"`
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
				// cgroup v1 fallback for OnlineCPUs.
				PercpuUsage []uint64 `json:"percpu_usage"`
			} `json:"cpu_usage"`
			SystemCPUUsage uint64 `json:"system_cpu_usage"`
			OnlineCPUs     int    `json:"online_cpus"`
		} `json:"cpu_stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return ContainerStats{}, fmt.Errorf("container stats %s: decode: %w", id, err)
	}
	mem := stats.MemoryStats.Usage
	inactive := stats.MemoryStats.Stats.InactiveFile
	if inactive == 0 {
		inactive = stats.MemoryStats.Stats.TotalInactiveFile
	}
	if inactive > 0 && inactive <= mem {
		mem -= inactive
	}
	cpus := stats.CPUStats.OnlineCPUs
	if cpus == 0 {
		cpus = len(stats.CPUStats.CPUUsage.PercpuUsage)
	}
	return ContainerStats{
		MemoryUsageBytes: mem,
		CPUTotalUsage:    stats.CPUStats.CPUUsage.TotalUsage,
		SystemCPUUsage:   stats.CPUStats.SystemCPUUsage,
		OnlineCPUs:       cpus,
	}, nil
}

// ContainerMemoryUsage returns the current memory usage (in bytes) of a container.
func (d *Client) ContainerMemoryUsage(ctx context.Context, id string) (usageBytes int64, err error) {
	stats, err := d.ContainerStatsOneShot(ctx, id)
	if err != nil {
		return 0, err
	}
	return stats.MemoryUsageBytes, nil
}

// ─── Image operations ──────────────────────────────────────────────────────

// splitImageRef separates an image reference into the name and the tag-or-
// digest that `POST /images/create` expects as separate query parameters.
//
// Docker's API takes the version in `tag`, not as part of `fromImage`:
// passing "repo@sha256:…" whole means the daemon looks for a repository by
// that literal name. With the classic image store the pull then reports
// success while storing nothing under the digest, and the next container
// create fails with "No such image" — a pull that lies. (Docker Desktop's
// containerd store is forgiving, which is why this only shows up on some
// daemons.) Digest-pinned images are the case that breaks; plain tags happen
// to work either way, and are split here too so one code path serves both.
//
// A registry host may carry a port ("localhost:5000/repo"), so only a colon
// after the last slash is a tag separator.
func splitImageRef(image string) (name, tag string) {
	if at := strings.LastIndexByte(image, '@'); at >= 0 {
		return image[:at], image[at+1:]
	}
	slash := strings.LastIndexByte(image, '/')
	if colon := strings.LastIndexByte(image, ':'); colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}

// PullOptions carries the optional parameters of a pull: the Docker platform
// to fetch, and the credentials for a registry that requires them.
type PullOptions struct {
	// Platform is a Docker platform such as linux/amd64. Empty pulls the
	// daemon's default.
	Platform string
	// Auth authenticates against the registry serving the image. Nil pulls
	// anonymously, which is what a public registry expects.
	Auth *RegistryAuth
}

// PullImage pulls an image anonymously. This blocks until the pull is complete.
func (d *Client) PullImage(ctx context.Context, image string) error {
	return d.PullImageWithOptions(ctx, image, PullOptions{})
}

// PullImageForPlatform pulls an image for a specific Docker platform such as
// linux/amd64.
func (d *Client) PullImageForPlatform(ctx context.Context, image, platform string) error {
	return d.PullImageWithOptions(ctx, image, PullOptions{Platform: platform})
}

// PullImageWithOptions pulls an image, optionally for a specific platform and
// against a registry that requires credentials. Docker Engine expects platform
// in the images/create query string, not in a JSON body, and credentials in the
// X-Registry-Auth header.
func (d *Client) PullImageWithOptions(ctx context.Context, image string, opts PullOptions) error {
	name, tag := splitImageRef(image)
	query := url.Values{}
	query.Set("fromImage", name)
	if tag != "" {
		query.Set("tag", tag)
	}
	if opts.Platform != "" {
		query.Set("platform", opts.Platform)
	}
	authHeader, err := opts.Auth.Header()
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	var headers map[string]string
	if authHeader != "" {
		headers = map[string]string{"X-Registry-Auth": authHeader}
	}
	path := "/v1.45/images/create?" + query.Encode()
	resp, err := d.doRequestWithHeaders(ctx, http.MethodPost, path, nil, headers)
	if err != nil {
		return fmt.Errorf("pull image %s: %w", image, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("pull image %s: status %d: %s", image, resp.StatusCode, string(body))
	}

	// The pull response is a stream of JSON progress objects. We must consume
	// the entire body for the pull to complete. Check the last line for errors.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("pull image %s: read response: %w", image, err)
	}

	// Check for error in the last JSON object.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 0 {
		var lastLine struct {
			Error string `json:"error"`
		}
		if json.Unmarshal([]byte(lines[len(lines)-1]), &lastLine) == nil && lastLine.Error != "" {
			return fmt.Errorf("pull image %s: %s", image, lastLine.Error)
		}
	}

	return nil
}

// PruneDanglingImages removes all dangling (untagged) images. Equivalent to
// `docker image prune -f`.
//
// Do NOT call this after a pull. "Dangling" means untagged, and an image
// pulled by digest ("repo@sha256:…") is untagged by definition, so a prune
// deletes the image the pull just fetched — the pull reports success and the
// container create that follows fails with "No such image". This used to run
// after every pull and made EFS's digest-pinned NFS export image unusable on
// any daemon with the classic image store (Docker Desktop's containerd store
// does not report digest-referenced images as dangling, so it only ever
// failed in CI).
//
// The blast radius is wider than that one case: the filter is daemon-wide, so
// it also deletes the *user's* untagged images, which Overcast does not own,
// and it can race any service that has pulled an image but not yet created
// its container. Reclaiming disk is not worth either. Call it explicitly, if
// ever, and never on a path that is about to use an image.
func (d *Client) PruneDanglingImages(ctx context.Context) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("prune images: %w", err)
	}
	defer d.releaseOp()

	filterJSON, err := json.Marshal(map[string][]string{
		"dangling": {"true"},
	})
	if err != nil {
		return fmt.Errorf("prune images: marshal filters: %w", err)
	}
	path := "/v1.45/images/prune?filters=" + url.QueryEscape(string(filterJSON))
	resp, err := d.doRequest(ctx, http.MethodPost, path, nil)
	if err != nil {
		return fmt.Errorf("prune images: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("prune images: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// ImageExists checks if an image exists locally.
func (d *Client) ImageExists(ctx context.Context, image string) (bool, error) {
	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// ImageMatchesPlatform reports whether the local image tag exists and matches
// the requested Docker platform. Empty platform preserves ImageExists behavior.
func (d *Client) ImageMatchesPlatform(ctx context.Context, image, platform string) (bool, error) {
	if platform == "" {
		return d.ImageExists(ctx, image)
	}
	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("inspect image %s: status %d: %s", image, resp.StatusCode, string(body))
	}
	var inspect ImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspect); err != nil {
		return false, fmt.Errorf("inspect image %s: decode: %w", image, err)
	}
	osName, arch, ok := strings.Cut(platform, "/")
	if !ok {
		return false, nil
	}
	return inspect.OS == osName && inspect.Architecture == arch, nil
}

// InspectImage returns the daemon's view of a local image: its platform and the
// run configuration baked into it. The image must already be present — this
// never pulls.
//
// It is one round trip and the caller is expected to cache it: the only caller
// on a hot path is Lambda's cold start, which needs an image function's
// original ENTRYPOINT+CMD once per image, not once per container.
func (d *Client) InspectImage(ctx context.Context, image string) (*ImageInspect, error) {
	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/images/"+url.PathEscape(image)+"/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("inspect image %s: no such image", image)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("inspect image %s: status %d: %s", image, resp.StatusCode, string(body))
	}
	var inspect ImageInspect
	if err := json.NewDecoder(resp.Body).Decode(&inspect); err != nil {
		return nil, fmt.Errorf("inspect image %s: decode: %w", image, err)
	}
	return &inspect, nil
}

// ─── Volume operations ─────────────────────────────────────────────────────

// VolumeSummary is one entry from GET /volumes.
type VolumeSummary struct {
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// Service returns the owning service name from the managed labels.
func (v *VolumeSummary) Service() string { return v.Labels[LabelService] }

// ResourceID returns the owning resource ID from the managed labels.
func (v *VolumeSummary) ResourceID() string { return v.Labels[LabelResourceID] }

// Instance returns the owning Overcast instance's identity from the managed
// labels, or "" when the volume does not carry one. See LabelInstance.
func (v *VolumeSummary) Instance() string { return v.Labels[LabelInstance] }

// VolumeOwnershipProblem is a Docker volume a service reused — its content
// matched what was wanted, addressed by name (e.g. Lambda's init volume; see
// internal/services/lambda/init_volume.go) — that turned out to be labelled
// for a different Overcast instance, or not labelled with an owner at all.
// Neutral home (mirrors NetworkProblem-shaped types living outside the
// service package that raises them, e.g. dataplane.VPCNetworkProblem): a
// router that surfaces these as an advisory reads this type instead of
// depending on the raising service's own package.
//
// That is not itself a sign of trouble — reuse across instances is often
// exactly what a content-addressed, read-only-mounted resource is for. What
// it does mean is that the reusing instance's own cleanup will never remove
// the volume, because it may only touch one it can prove it created (see
// LabelInstance): it only goes away once its own creating instance removes
// it, or an operator does by hand.
type VolumeOwnershipProblem struct {
	// Volume is the volume's name.
	Volume string
	// Owner is the value of LabelInstance on the volume, or "" when it
	// carries no owner label at all — seeded before this label existed, or
	// by a daemon-native volume operation Overcast never labelled.
	Owner string
}

type createVolumeRequest struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver,omitempty"`
	DriverOpts map[string]string `json:"DriverOpts,omitempty"`
	Labels     map[string]string `json:"Labels,omitempty"`
}

// VolumeOptions are the non-name properties of a volume. The zero value asks
// for the daemon's defaults, which is what every caller wanted before ECS
// needed to honour a task definition's dockerVolumeConfiguration.
type VolumeOptions struct {
	// Driver selects the volume plugin. Empty means Docker's "local".
	Driver string
	// DriverOpts are passed to the driver verbatim. The local driver reads
	// type/o/device here, which is how a named volume can be backed by a bind.
	DriverOpts map[string]string
	Labels     map[string]string
}

// CreateVolume creates a named Docker volume. Creating an existing name is a
// no-op on the daemon side (Docker returns the existing volume), which makes
// this safe to call from reconciliation paths.
func (d *Client) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	return d.CreateVolumeWithOptions(ctx, name, VolumeOptions{Labels: labels})
}

// CreateVolumeWithOptions is CreateVolume with a driver and driver options.
//
// Note the daemon's idempotency has a limit: creating an existing name returns
// the existing volume *as it is*, so options are applied on first creation
// only. A caller that needs different options must remove the volume first.
func (d *Client) CreateVolumeWithOptions(ctx context.Context, name string, opts VolumeOptions) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("create volume %s: %w", name, err)
	}
	defer d.releaseOp()

	req := createVolumeRequest{
		Name:       name,
		Driver:     opts.Driver,
		DriverOpts: opts.DriverOpts,
		Labels:     opts.Labels,
	}
	if err := d.doJSON(ctx, http.MethodPost, "/v1.45/volumes/create", req, nil); err != nil {
		return fmt.Errorf("create volume %s: %w", name, err)
	}
	return nil
}

// VolumeExists reports whether a volume of this name exists on the daemon.
//
// ListVolumes cannot answer this: it filters on the managed label, so a volume
// a user created themselves is invisible to it — and that volume is precisely
// what a dockerVolumeConfiguration with autoprovision=false expects to find.
func (d *Client) VolumeExists(ctx context.Context, name string) (bool, error) {
	if err := d.acquireOp(ctx); err != nil {
		return false, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	defer d.releaseOp()

	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/volumes/"+url.PathEscape(name), nil)
	if err != nil {
		return false, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("inspect volume %s: status %d", name, resp.StatusCode)
	}
}

// InspectVolume returns the daemon's record of a named volume, reporting
// whether it exists at all.
//
// It answers the question VolumeExists cannot: not just "is there a volume of
// this name" but "is it *ours*". A volume Docker auto-created for a container
// that named one carries no labels, and a caller that assumes such a volume
// holds what it seeded there would hand out an empty one forever.
func (d *Client) InspectVolume(ctx context.Context, name string) (*VolumeSummary, bool, error) {
	if err := d.acquireOp(ctx); err != nil {
		return nil, false, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	defer d.releaseOp()

	resp, err := d.doRequest(ctx, http.MethodGet, "/v1.45/volumes/"+url.PathEscape(name), nil)
	if err != nil {
		return nil, false, fmt.Errorf("inspect volume %s: %w", name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("inspect volume %s: status %d", name, resp.StatusCode)
	}
	var vol VolumeSummary
	if err := json.NewDecoder(resp.Body).Decode(&vol); err != nil {
		return nil, false, fmt.Errorf("inspect volume %s: decode: %w", name, err)
	}
	return &vol, true, nil
}

// RemoveVolume removes a named Docker volume. A missing volume is not an
// error, mirroring RemoveContainer's cleanup-friendly semantics.
func (d *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	defer d.releaseOp()

	path := fmt.Sprintf("/v1.45/volumes/%s?force=%t", url.PathEscape(name), force)
	resp, err := d.doRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("remove volume %s: status %d", name, resp.StatusCode)
	}
	return nil
}

// ListVolumes returns managed volumes, optionally filtered to one service.
func (d *Client) ListVolumes(ctx context.Context, service string) ([]VolumeSummary, error) {
	return d.listVolumes(ctx, service, false)
}

// ListUnusedVolumes is ListVolumes restricted to volumes no container
// references — Docker's "dangling" filter.
//
// This is the only sound way to ask whether a volume is still wanted. The
// alternative, asking an emulator's own records whether the owning task still
// exists, is wrong the moment a second Overcast shares the daemon: each has its
// own records, so each sees the other's live volumes as abandoned. The daemon
// holds the one view of container references that every instance agrees on.
func (d *Client) ListUnusedVolumes(ctx context.Context, service string) ([]VolumeSummary, error) {
	return d.listVolumes(ctx, service, true)
}

func (d *Client) listVolumes(ctx context.Context, service string, unusedOnly bool) ([]VolumeSummary, error) {
	filterMap := map[string][]string{
		"label": {LabelManaged + "=true"},
	}
	if service != "" {
		filterMap["label"] = append(filterMap["label"], LabelService+"="+service)
	}
	if unusedOnly {
		filterMap["dangling"] = []string{"true"}
	}
	filterJSON, err := json.Marshal(filterMap)
	if err != nil {
		return nil, fmt.Errorf("list volumes: marshal filters: %w", err)
	}
	path := "/v1.45/volumes?filters=" + url.QueryEscape(string(filterJSON))
	var out struct {
		Volumes []VolumeSummary `json:"Volumes"`
	}
	if err := d.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	return out.Volumes, nil
}

// ─── Network operations ────────────────────────────────────────────────────

// DefaultNetworkDriver is the driver every network Overcast creates uses.
// Named rather than repeated so the create path and the verification that
// compares a live network against what was asked for cannot drift apart.
const DefaultNetworkDriver = "bridge"

type createNetworkRequest struct {
	Name           string            `json:"Name"`
	Driver         string            `json:"Driver"`
	CheckDuplicate bool              `json:"CheckDuplicate"`
	Internal       bool              `json:"Internal,omitempty"`
	EnableIPv6     bool              `json:"EnableIPv6,omitempty"`
	Labels         map[string]string `json:"Labels,omitempty"`
	Options        map[string]string `json:"Options,omitempty"`
	IPAM           *NetworkIPAM      `json:"IPAM,omitempty"`
}

// CreateNetwork creates a Docker network. Returns the network ID.
// Ignores "already exists" errors.
func (d *Client) CreateNetwork(ctx context.Context, name string) (string, error) {
	return d.CreateNetworkWithOptions(ctx, CreateNetworkOptions{Name: name})
}

// CreateNetworkOptions configures a Docker network.
//
// Every field here is also a field NetworkSpec verifies a pre-existing network
// against, and that pairing is deliberate: a setting Overcast can ask for but
// cannot check is a setting that drifts silently.
type CreateNetworkOptions struct {
	Name     string
	Driver   string            // empty = "bridge"
	Labels   map[string]string // nil = no labels
	Subnet   string            // CIDR, e.g. "10.0.0.0/16"; empty = Docker default
	Gateway  string            // empty = Docker picks; ignored without Subnet
	Internal bool              // true = no outbound internet access
	IPv6     bool              // true = allocate IPv6 addresses too

	// Options are driver options passed through verbatim, e.g.
	// "com.docker.network.bridge.enable_icc". Left nil, Docker's defaults apply.
	Options map[string]string
}

// CreateNetworkWithOptions creates a Docker network with full control over
// labels, CIDR, and internal mode. Returns the network ID.
// Ignores "already exists" errors.
func (d *Client) CreateNetworkWithOptions(ctx context.Context, opts CreateNetworkOptions) (string, error) {
	if err := d.acquireOp(ctx); err != nil {
		return "", fmt.Errorf("create network %s: %w", opts.Name, err)
	}
	defer d.releaseOp()

	driver := opts.Driver
	if driver == "" {
		driver = DefaultNetworkDriver
	}
	req := createNetworkRequest{
		Name:           opts.Name,
		Driver:         driver,
		CheckDuplicate: true,
		Internal:       opts.Internal,
		EnableIPv6:     opts.IPv6,
		Labels:         opts.Labels,
		Options:        opts.Options,
	}
	if opts.Subnet != "" {
		req.IPAM = &NetworkIPAM{
			Config: []NetworkIPAMConfig{{Subnet: opts.Subnet, Gateway: opts.Gateway}},
		}
	}
	var resp struct {
		ID      string `json:"Id"`
		Warning string `json:"Warning"`
	}
	err := d.doJSON(ctx, http.MethodPost, "/v1.45/networks/create", &req, &resp)
	if err != nil {
		// Check if it's a "name already in use" error — that's fine.
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "already in use") {
			// Look up the existing network.
			existing, lookupErr := d.InspectNetwork(ctx, opts.Name)
			if lookupErr != nil {
				return "", lookupErr
			}
			return existing.ID, nil
		}
		return "", fmt.Errorf("create network %s: %w", opts.Name, err)
	}
	return resp.ID, nil
}

// InspectNetwork returns network details.
func (d *Client) InspectNetwork(ctx context.Context, nameOrID string) (*NetworkInspect, error) {
	var info NetworkInspect
	if err := d.doJSON(ctx, http.MethodGet, "/v1.45/networks/"+url.PathEscape(nameOrID), nil, &info); err != nil {
		return nil, fmt.Errorf("inspect network %s: %w", nameOrID, err)
	}
	return &info, nil
}

// ConnectNetwork attaches a container to a network.
func (d *Client) ConnectNetwork(ctx context.Context, networkID, containerID string) error {
	return d.ConnectNetworkWithAliases(ctx, networkID, containerID, nil)
}

// ConnectNetworkWithAliases attaches a container to a network with optional DNS aliases.
func (d *Client) ConnectNetworkWithAliases(ctx context.Context, networkID, containerID string, aliases []string) error {
	var cfg *EndpointSettings
	if len(aliases) > 0 {
		cfg = &EndpointSettings{Aliases: aliases}
	}
	return d.ConnectNetworkWithConfig(ctx, networkID, containerID, cfg)
}

// ConnectNetworkWithConfig attaches a container to a network with an explicit
// endpoint configuration — aliases, a pinned address, or both.
//
// A container already attached to the network is treated as success, the same
// way CreateNetwork treats an existing network. Docker rejects the second
// connect with "endpoint with name ... already exists", and every caller here
// reaches this from a path that may legitimately run twice: startup reconcile,
// container reuse after a restart, and Overcast joining a plane it may already
// be on. Without this they would all have to special-case the daemon's wording.
func (d *Client) ConnectNetworkWithConfig(ctx context.Context, networkID, containerID string, cfg *EndpointSettings) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("connect network %s: %w", networkID, err)
	}
	defer d.releaseOp()

	// The pinned version everywhere, except for a connect that ranks the
	// network as a default-route source: that field only exists from 1.48, and
	// a daemon that speaks it is asked under it. One that does not gets the
	// same connect without the ranking, and Docker's name-order tie-break
	// decides the route — said once here, since nothing else will say it.
	version := apiVersionPinned
	if cfg != nil && cfg.GwPriority != 0 {
		if d.APIVersionAtLeast(ctx, apiVersionGatewayPriority) {
			version = apiVersionGatewayPriority
		} else {
			d.logger.Info("this Docker daemon predates gateway priorities (API "+apiVersionGatewayPriority+
				", Docker 28.0); the container's default route is chosen by network name instead",
				zap.String("network", networkID), zap.String("container", containerID))
			c := *cfg
			c.GwPriority = 0
			cfg = &c
		}
	}

	body := struct {
		Container      string            `json:"Container"`
		EndpointConfig *EndpointSettings `json:"EndpointConfig,omitempty"`
	}{Container: containerID, EndpointConfig: cfg}
	err := d.doJSON(ctx, http.MethodPost, "/v"+version+"/networks/"+networkID+"/connect", &body, nil)
	if err != nil && isAlreadyConnected(err) {
		return nil
	}
	return err
}

// APIVersionAtLeast reports whether the daemon supports API version want
// ("1.48"). The daemon's version is read once, from GET /version, and kept; a
// daemon that cannot be asked is taken to support nothing beyond the pinned
// floor, so a caller that needs a newer feature degrades rather than sending a
// request the daemon would refuse with "client version is too new".
func (d *Client) APIVersionAtLeast(ctx context.Context, want string) bool {
	return compareAPIVersions(d.daemonAPIVersion(ctx), want) >= 0
}

func (d *Client) daemonAPIVersion(ctx context.Context) string {
	d.apiVersionMu.Lock()
	defer d.apiVersionMu.Unlock()
	if d.apiVersion != "" {
		return d.apiVersion
	}
	var v struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := d.doJSON(ctx, http.MethodGet, "/version", nil, &v); err != nil || v.APIVersion == "" {
		d.logger.Debug("could not read the Docker API version; assuming the pinned floor",
			zap.String("assumed", apiVersionPinned), zap.Error(err))
		return apiVersionPinned // not cached: the next call may reach the daemon
	}
	d.apiVersion = v.APIVersion
	return d.apiVersion
}

// compareAPIVersions orders two "major.minor" Docker API versions: negative
// when a is older than b, zero when equal, positive when newer. A version that
// does not parse is treated as the oldest possible.
func compareAPIVersions(a, b string) int {
	pa, pb := parseAPIVersion(a), parseAPIVersion(b)
	for i := range 2 {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseAPIVersion(v string) [2]int {
	var out [2]int
	major, minor, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
	if n, err := strconv.Atoi(major); err == nil {
		out[0] = n
	}
	if n, err := strconv.Atoi(minor); err == nil {
		out[1] = n
	}
	return out
}

// isAlreadyConnected reports whether err is Docker refusing a connect because
// the container is on that network already.
func isAlreadyConnected(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "already exists in network") ||
		strings.Contains(msg, "is already attached to network")
}

// DisconnectNetwork detaches a container from a network.
func (d *Client) DisconnectNetwork(ctx context.Context, networkID, containerID string) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("disconnect network %s: %w", networkID, err)
	}
	defer d.releaseOp()

	body := struct {
		Container string `json:"Container"`
		Force     bool   `json:"Force"`
	}{Container: containerID, Force: true}
	return d.doJSON(ctx, http.MethodPost, "/v1.45/networks/"+networkID+"/disconnect", &body, nil)
}

// RemoveNetwork removes a Docker network by name or ID.
func (d *Client) RemoveNetwork(ctx context.Context, nameOrID string) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("remove network %s: %w", nameOrID, err)
	}
	defer d.releaseOp()

	resp, err := d.doRequest(ctx, http.MethodDelete, "/v1.45/networks/"+url.PathEscape(nameOrID), nil)
	if err != nil {
		return fmt.Errorf("remove network %s: %w", nameOrID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		// The body carries the daemon's reason — "has active endpoints" is the
		// one a cleanup needs to tell from every other refusal.
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("remove network %s: status %d: %s", nameOrID, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// ListNetworks returns all Overcast-managed networks, optionally filtered by service.
func (d *Client) ListNetworks(ctx context.Context, service string) ([]NetworkSummary, error) {
	filterMap := map[string][]string{
		"label": {LabelManaged + "=true"},
	}
	if service != "" {
		filterMap["label"] = append(filterMap["label"], LabelService+"="+service)
	}
	filterJSON, err := json.Marshal(filterMap)
	if err != nil {
		return nil, fmt.Errorf("list networks: marshal filters: %w", err)
	}
	path := "/v1.45/networks?filters=" + url.QueryEscape(string(filterJSON))
	var networks []NetworkSummary
	if err := d.doJSON(ctx, http.MethodGet, path, nil, &networks); err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	return networks, nil
}

// ListNetworksNamed returns every network whose name contains fragment,
// managed or not. The daemon's name filter is a substring match (a regular
// expression, in fact), so callers wanting an exact set must check the names
// they get back — this cannot express "starts with".
//
// Unlike ListNetworks it does not require the managed label: the planes
// docker.Probe creates carry none, and neither do the per-test twins the
// suites mint, which is what dockertest.Sweep is looking for.
func (d *Client) ListNetworksNamed(ctx context.Context, fragment string) ([]NetworkSummary, error) {
	filterJSON, err := json.Marshal(map[string][]string{"name": {fragment}})
	if err != nil {
		return nil, fmt.Errorf("list networks named %s: marshal filters: %w", fragment, err)
	}
	path := "/v1.45/networks?filters=" + url.QueryEscape(string(filterJSON))
	var networks []NetworkSummary
	if err := d.doJSON(ctx, http.MethodGet, path, nil, &networks); err != nil {
		return nil, fmt.Errorf("list networks named %s: %w", fragment, err)
	}
	return networks, nil
}

// CopyToContainer copies a tar archive into a container at the given path.
// This uses the Docker "Put Archive" API endpoint.
func (d *Client) CopyToContainer(ctx context.Context, id, destPath string, tarData io.Reader) error {
	if err := d.acquireOp(ctx); err != nil {
		return fmt.Errorf("copy to container %s: %w", id, err)
	}
	defer d.releaseOp()

	path := fmt.Sprintf("/v1.45/containers/%s/archive?path=%s", id, url.QueryEscape(destPath))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.host+path, tarData)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("copy to container %s: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("copy to container %s: status %d: %s", id, resp.StatusCode, string(body))
	}
	return nil
}

// ─── Helpers ───────────────────────────────────────────────────────────────

// Available checks if the Docker daemon is reachable.
func (d *Client) Available(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.Ping(ctx) == nil
}
