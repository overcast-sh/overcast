package dataplane

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
)

// Guard answers the DNS server's question — should this name be answered with
// Overcast's address, or refused because it names a container the caller cannot
// reach — by reading Docker rather than by matching the name against a pattern.
//
// # Why not match the name
//
// The obvious implementation recognises the shapes Overcast mints
// (`{id}.{region}.rds.{base}`, `{id}.{region}.cfg.{base}`, …) and refuses any
// that it cannot place. It is wrong, and it breaks S3.
//
// Those labels are ordinary words in a bucket name, which is why
// docs/networking/host-routing.md declines to register `rds`, `cache`, `kafka`
// and `es` as
// host-routed labels: a bucket named `my.rds` is addressed virtual-hosted as
// `my.rds.localhost`, matches the shape exactly, and is not a data-plane
// endpoint at all. Refusing it would break the bucket.
//
// So a refusal requires positive identification: some container is advertising
// this exact alias, and the caller is on none of the networks carrying it. Both
// halves are facts read from the daemon. Nothing advertises `my.rds.localhost`,
// so a bucket can never be refused.
//
// # Why this is affordable on the query path
//
// A container resolves through Docker's embedded resolver, which answers from
// network aliases and forwards only what it cannot answer. A data-plane name
// that reaches Overcast's resolver has therefore *already* missed — the
// connection it belongs to is broken either way. The daemon calls here are paid
// by failures, never by working lookups.
// surveyor is the slice of *docker.Client the guard reads. An interface rather
// than the concrete client so the decision logic — which is the whole of the
// risk here — is testable without a daemon.
type surveyor interface {
	ListContainers(ctx context.Context, service string) ([]docker.ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (*docker.ContainerInspect, error)
}

type Guard struct {
	docker surveyor
	log    *zap.Logger

	mu    sync.Mutex
	cache map[string]cachedVerdict
	clock func() time.Time

	// recent is the last few refusals, newest last, for the health advisory.
	// A refusal is logged too, but the log is where somebody looks once they
	// suspect Overcast; the advisory is what tells them to. Bounded by
	// maxRecentRefusals; a name refused again replaces its earlier entry
	// rather than filling the list with one broken caller's retries.
	recent []Refusal
}

// Refusal is one data-plane name the guard declined to answer: a caller on
// one set of networks asked for a name advertised only on another.
type Refusal struct {
	// Name is the hostname queried, as the caller spelled it.
	Name string `json:"name"`
	// Target labels the container advertising Name — its service and resource
	// when Overcast started it — and TargetNetworks the networks it is on.
	Target         string   `json:"target"`
	TargetNetworks []string `json:"targetNetworks"`
	// Caller labels the container that asked, and CallerNetworks the networks
	// it is on. The two network sets are disjoint, which is the whole finding.
	Caller         string   `json:"caller"`
	CallerNetworks []string `json:"callerNetworks"`
	// At is when the refusal was recorded.
	At time.Time `json:"at"`
}

// maxRecentRefusals bounds Recent. One entry per name is plenty to see the
// shape of a misplacement, and a deployment with more than this many distinct
// broken names has a problem the first few already describe.
const maxRecentRefusals = 16

// Recent returns the refusals recorded so far, oldest first. Nil for a guard
// that has refused nothing, so a caller can render "nothing to report"
// without a length check.
func (g *Guard) Recent() []Refusal {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.recent) == 0 {
		return nil
	}
	return append([]Refusal(nil), g.recent...)
}

// record keeps r for Recent, replacing an earlier refusal of the same name.
func (g *Guard) record(r Refusal) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.recent {
		if strings.EqualFold(g.recent[i].Name, r.Name) {
			g.recent = append(g.recent[:i], g.recent[i+1:]...)
			break
		}
	}
	if len(g.recent) >= maxRecentRefusals {
		g.recent = g.recent[1:]
	}
	g.recent = append(g.recent, r)
}

// verdictTTL bounds how long a refusal is reused. A client that cannot resolve
// a name retries hard, and without this one misconfiguration would become a
// stream of daemon calls. It is short because the answer changes the moment
// somebody attaches the container correctly, and a stale refusal would outlive
// the fix.
const verdictTTL = 3 * time.Second

type cachedVerdict struct {
	refuse bool
	at     time.Time
}

// NewGuard returns a Guard reading from dc. A nil client makes it inert, which
// is what a metadata-only deployment wants: with no daemon there are no
// containers, so nothing can be positively identified and nothing is refused.
//
// The nil check is on the concrete type deliberately. A nil *docker.Client
// stored in the surveyor interface would be a non-nil interface holding a nil
// pointer, and every call on it would panic rather than degrade.
func NewGuard(dc *docker.Client, log *zap.Logger) *Guard {
	g := &Guard{
		log:   log,
		cache: make(map[string]cachedVerdict),
		clock: time.Now,
	}
	if dc != nil {
		g.docker = dc
	}
	return g
}

// Refuse implements dns.Guard.
func (g *Guard) Refuse(ctx context.Context, name string, peer netip.Addr) bool {
	if g == nil || g.docker == nil || name == "" || !peer.IsValid() {
		return false
	}

	key := strings.ToLower(name) + "\x00" + peer.String()
	if verdict, ok := g.cached(key); ok {
		return verdict
	}
	refuse := g.evaluate(ctx, name, peer)
	g.remember(key, refuse)
	return refuse
}

func (g *Guard) cached(key string) (bool, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.cache[key]
	if !ok || g.clock().Sub(v.at) > verdictTTL {
		return false, false
	}
	return v.refuse, true
}

func (g *Guard) remember(key string, refuse bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Bounded by clearing rather than by eviction order: the set is small (one
	// entry per broken name/caller pair, all of them short-lived) and a plain
	// map with a size cap keeps this off the critical path.
	if len(g.cache) >= maxGuardCache {
		clear(g.cache)
	}
	g.cache[key] = cachedVerdict{refuse: refuse, at: g.clock()}
}

const maxGuardCache = 512

// evaluate does the daemon work: find who is advertising the name, and whether
// the caller shares a network with any of them.
//
// Both sides must be positively identified. Failing to find the *caller* is not
// evidence that it cannot reach the target — ListContainers only returns
// Overcast-managed containers, so a user's own compose service joined to the
// shared plane is invisible here — and refusing on an absence would break
// exactly the setup the shared plane exists to support.
func (g *Guard) evaluate(ctx context.Context, name string, peer netip.Addr) bool {
	containers, err := g.docker.ListContainers(ctx, "")
	if err != nil {
		return false // cannot identify anything; answer as before
	}

	advertisedOn, callerOn, target, caller := g.survey(ctx, containers, name, peer)

	// An ordinary name this zone owns — a bucket, an API Gateway host — or a
	// resource whose container is gone. Indistinguishable from here.
	if len(advertisedOn) == 0 {
		return false
	}
	// The caller is not a container Overcast started, so its attachments are
	// unknown rather than known-disjoint.
	if len(callerOn) == 0 {
		return false
	}
	for network := range callerOn {
		if advertisedOn[network] {
			return false // they share a plane; the miss is not about placement
		}
	}

	targetNetworks, callerNetworks := keys(advertisedOn), keys(callerOn)
	g.log.Warn("refusing a data-plane name the caller cannot reach — "+
		"the container is on a network this caller is not attached to, so answering "+
		"with Overcast's address would connect it to the emulator on the engine's port and hang. "+
		RefusalRemedy,
		zap.String("name", name),
		zap.String("target", target),
		zap.String("caller", caller),
		zap.Strings("target_networks", targetNetworks),
		zap.Strings("caller_networks", callerNetworks))
	g.record(Refusal{
		Name: name, Target: target, TargetNetworks: targetNetworks,
		Caller: caller, CallerNetworks: callerNetworks, At: g.clock(),
	})
	return true
}

// RefusalRemedy is the one-line fix, said the same way in the log line and in
// the health advisory: the two resources are not in the same VPC, which is
// the placement the template asked for, and would not reach each other on AWS
// either. Spelled in AWS's own terms because the fix that works here is the
// fix that works there.
const RefusalRemedy = "Put both resources in the same VPC — a CacheSubnetGroupName or DBSubnetGroupName " +
	"on the resource, a VpcConfig on the function, awsvpcConfiguration subnets on the task — " +
	"or leave both outside any VPC; a resource that named no subnet group is in the default VPC, " +
	"which is the shared data plane. RDS also honours PubliclyAccessible and ECS assignPublicIp."

// survey inspects each container once and answers both questions from the same
// pass: which networks advertise name, and which the caller is attached to.
// Inspecting per question instead would double the daemon calls for no gain.
func (g *Guard) survey(
	ctx context.Context, containers []docker.ContainerSummary, name string, peer netip.Addr,
) (advertisedOn, callerOn map[string]bool, target, caller string) {
	advertisedOn = make(map[string]bool)
	want := peer.String()

	for i := range containers {
		info, err := g.docker.InspectContainer(ctx, containers[i].ID)
		if err != nil || info == nil {
			continue
		}
		holdsPeer := false
		on := make(map[string]bool, len(info.NetworkSettings.Networks))
		for network, endpoint := range info.NetworkSettings.Networks {
			on[network] = true
			if endpoint.IPAddress == want {
				holdsPeer = true
			}
			for _, alias := range endpoint.Aliases {
				if strings.EqualFold(alias, name) {
					advertisedOn[network] = true
					if target == "" {
						target = containerLabel(&containers[i])
					}
				}
			}
		}
		if holdsPeer && callerOn == nil {
			callerOn, caller = on, containerLabel(&containers[i])
		}
	}
	return advertisedOn, callerOn, target, caller
}

// containerLabel names a container the way somebody reading the log would look
// for it: the resource it belongs to when Overcast started it, else its name.
func containerLabel(c *docker.ContainerSummary) string {
	if rid := c.ResourceID(); rid != "" {
		if svc := c.Service(); svc != "" {
			return svc + " " + rid
		}
		return rid
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
