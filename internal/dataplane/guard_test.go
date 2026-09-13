package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/docker"
)

// fakeSurveyor stands in for the daemon. Each entry is a container, its
// networks, and what it answers to on each.
type fakeSurveyor struct {
	containers []fakeContainer
	listErr    error
	inspects   int
}

type fakeContainer struct {
	id       string
	resource string
	service  string
	// networks maps network name → the container's address there.
	networks map[string]string
	// aliases maps network name → the names it answers to there.
	aliases map[string][]string
}

func (f *fakeSurveyor) ListContainers(context.Context, string) ([]docker.ContainerSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]docker.ContainerSummary, 0, len(f.containers))
	for _, c := range f.containers {
		summary := docker.ContainerSummary{ID: c.id, Names: []string{"/" + c.id}}
		summary.Labels = map[string]string{
			docker.LabelService:    c.service,
			docker.LabelResourceID: c.resource,
		}
		out = append(out, summary)
	}
	return out, nil
}

func (f *fakeSurveyor) InspectContainer(_ context.Context, id string) (*docker.ContainerInspect, error) {
	f.inspects++
	for _, c := range f.containers {
		if c.id != id {
			continue
		}
		info := &docker.ContainerInspect{ID: c.id}
		info.NetworkSettings.Networks = make(map[string]docker.ContainerNetwork, len(c.networks))
		for network, addr := range c.networks {
			info.NetworkSettings.Networks[network] = docker.ContainerNetwork{
				IPAddress: addr,
				Aliases:   c.aliases[network],
			}
		}
		return info, nil
	}
	return nil, errors.New("no such container")
}

func guardWith(s surveyor) *Guard {
	g := NewGuard(nil, zap.NewNop())
	g.docker = s
	return g
}

const (
	cacheName = "cache-1.us-east-1.cfg.localhost.overcast.sh"
	callerIP  = "172.20.0.5"
)

// The case this whole phase exists for: the resource is real, the caller is
// real, and they are on different planes. Answering would hand the caller
// Overcast's address and hang it on the engine's port.
func TestGuard_refusesWhenCallerAndTargetSharaNoPlane(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{
		{
			id: "cache", service: "elasticache", resource: "cache-1",
			networks: map[string]string{"overcast-vpc-abc": "10.1.0.4"},
			aliases:  map[string][]string{"overcast-vpc-abc": {cacheName}},
		},
		{
			id: "task", service: "ecs", resource: "app/9f2",
			networks: map[string]string{"overcast-vpc-def": callerIP},
		},
	}})

	if !g.Refuse(context.Background(), cacheName, netip.MustParseAddr(callerIP)) {
		t.Fatal("answered a name the caller has no plane in common with")
	}
}

func TestGuard_answersWhenTheyShareAPlane(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{
		{
			id: "cache", service: "elasticache", resource: "cache-1",
			networks: map[string]string{"overcast": "10.1.0.4"},
			aliases:  map[string][]string{"overcast": {cacheName}},
		},
		{
			id: "task", service: "ecs", resource: "app/9f2",
			networks: map[string]string{"overcast": callerIP},
		},
	}})

	if g.Refuse(context.Background(), cacheName, netip.MustParseAddr(callerIP)) {
		t.Fatal("refused a name reachable on a shared plane")
	}
}

// The reason this guard identifies rather than pattern-matches. A bucket named
// `my.rds` is addressed virtual-hosted as `my.rds.localhost.overcast.sh`, which
// matches every data-plane shape and is not a container at all. Nothing
// advertises it, so nothing may refuse it.
func TestGuard_neverRefusesANameNoContainerAdvertises(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{
		{
			id: "task", service: "ecs", resource: "app/9f2",
			networks: map[string]string{"overcast": callerIP},
		},
	}})

	for _, name := range []string{
		"my.rds.localhost.overcast.sh",           // a bucket that looks like RDS
		"mybucket.s3.localhost.overcast.sh",      // virtual-hosted S3
		"abc.execute-api.us-east-1.localhost",    // API Gateway
		"db.us-east-1.rds.localhost.overcast.sh", // a real shape, no container
	} {
		if g.Refuse(context.Background(), name, netip.MustParseAddr(callerIP)) {
			t.Errorf("refused %q, which no container advertises", name)
		}
	}
}

// ListContainers only returns Overcast-managed containers, so a user's own
// compose service joined to the shared plane is invisible here. Its absence is
// not evidence that it cannot reach the target, and refusing on it would break
// exactly the setup the shared plane exists to support.
func TestGuard_answersWhenTheCallerCannotBeIdentified(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{{
		id: "cache", service: "elasticache", resource: "cache-1",
		networks: map[string]string{"overcast-vpc-abc": "10.1.0.4"},
		aliases:  map[string][]string{"overcast-vpc-abc": {cacheName}},
	}}})

	// The caller holds an address no managed container has.
	if g.Refuse(context.Background(), cacheName, netip.MustParseAddr("192.168.77.9")) {
		t.Fatal("refused a caller that could not be identified")
	}
}

func TestGuard_answersWhenTheDaemonCannotBeListed(t *testing.T) {
	g := guardWith(&fakeSurveyor{listErr: errors.New("daemon gone")})
	if g.Refuse(context.Background(), cacheName, netip.MustParseAddr(callerIP)) {
		t.Fatal("refused when the daemon could not be listed")
	}
}

// Alias comparison is case-insensitive, as DNS name comparison is (RFC 4343).
func TestGuard_matchesAliasesCaseInsensitively(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{
		{
			id: "cache", service: "elasticache", resource: "cache-1",
			networks: map[string]string{"overcast-vpc-abc": "10.1.0.4"},
			aliases:  map[string][]string{"overcast-vpc-abc": {cacheName}},
		},
		{id: "task", networks: map[string]string{"overcast": callerIP}},
	}})

	if !g.Refuse(context.Background(), "CACHE-1.US-EAST-1.CFG.LOCALHOST.OVERCAST.SH", netip.MustParseAddr(callerIP)) {
		t.Fatal("an upper-case query was not matched against the alias")
	}
}

// A client that cannot resolve a name retries hard; without the cache each
// retry would be a fresh sweep of every container.
func TestGuard_cachesSoRetriesDoNotResweepTheDaemon(t *testing.T) {
	fake := &fakeSurveyor{containers: []fakeContainer{
		{
			id: "cache", service: "elasticache", resource: "cache-1",
			networks: map[string]string{"overcast-vpc-abc": "10.1.0.4"},
			aliases:  map[string][]string{"overcast-vpc-abc": {cacheName}},
		},
		{id: "task", networks: map[string]string{"overcast": callerIP}},
	}}
	g := guardWith(fake)
	peer := netip.MustParseAddr(callerIP)

	if !g.Refuse(context.Background(), cacheName, peer) {
		t.Fatal("first call did not refuse")
	}
	after := fake.inspects
	for range 5 {
		g.Refuse(context.Background(), cacheName, peer)
	}
	if fake.inspects != after {
		t.Fatalf("inspects went from %d to %d across cached retries", after, fake.inspects)
	}
}

// A stale refusal would outlive the fix: the answer changes the moment somebody
// attaches the container correctly.
func TestGuard_expiresAVerdict(t *testing.T) {
	g := NewGuard(nil, zap.NewNop())
	now := time.Unix(0, 0)
	g.clock = func() time.Time { return now }

	g.remember("k", true)
	if _, ok := g.cached("k"); !ok {
		t.Fatal("a fresh verdict was not reused")
	}
	now = now.Add(verdictTTL + time.Second)
	if _, ok := g.cached("k"); ok {
		t.Fatal("a verdict older than the TTL was reused")
	}
}

func TestGuard_boundsTheCache(t *testing.T) {
	g := NewGuard(nil, zap.NewNop())
	for i := range maxGuardCache + 10 {
		g.remember(string(rune(i)), true)
	}
	if len(g.cache) > maxGuardCache {
		t.Fatalf("cache holds %d entries, want at most %d", len(g.cache), maxGuardCache)
	}
}

// A metadata-only deployment has no daemon, so nothing can be identified and
// nothing may be refused.
func TestGuard_isInertWithoutADaemon(t *testing.T) {
	g := NewGuard(nil, zap.NewNop())
	if g.Refuse(context.Background(), cacheName, netip.MustParseAddr(callerIP)) {
		t.Fatal("refused with no Docker client")
	}
}

// A refusal is kept for the health advisory, with both sides named, so the
// reader learns which resource to move without opening Overcast's log.
func TestGuard_recordsRefusalsForTheAdvisory(t *testing.T) {
	g := guardWith(&fakeSurveyor{containers: []fakeContainer{
		{
			id: "cache", service: "elasticache", resource: "cache-1",
			networks: map[string]string{"overcast": "10.0.0.4"},
			aliases:  map[string][]string{"overcast": {cacheName}},
		},
		{
			id: "task", service: "ecs", resource: "app/9f2",
			networks: map[string]string{"overcast-vpc-def": callerIP},
		},
	}})
	if g.Recent() != nil {
		t.Fatal("a fresh guard reports refusals")
	}

	g.Refuse(context.Background(), cacheName, netip.MustParseAddr(callerIP))

	got := g.Recent()
	if len(got) != 1 {
		t.Fatalf("Recent() = %+v, want one refusal", got)
	}
	r := got[0]
	if r.Name != cacheName || r.Target != "elasticache cache-1" || r.Caller != "ecs app/9f2" {
		t.Errorf("refusal = %+v", r)
	}
	if len(r.TargetNetworks) != 1 || r.TargetNetworks[0] != "overcast" {
		t.Errorf("TargetNetworks = %v, want [overcast]", r.TargetNetworks)
	}
	if len(r.CallerNetworks) != 1 || r.CallerNetworks[0] != "overcast-vpc-def" {
		t.Errorf("CallerNetworks = %v, want [overcast-vpc-def]", r.CallerNetworks)
	}
}

// A name refused again replaces its earlier entry, and the list is bounded,
// so one broken caller retrying cannot crowd out the others.
func TestGuard_recentIsDedupedByNameAndBounded(t *testing.T) {
	g := guardWith(&fakeSurveyor{})
	for i := 0; i < maxRecentRefusals+5; i++ {
		g.record(Refusal{Name: fmt.Sprintf("name-%d", i)})
	}
	g.record(Refusal{Name: "NAME-7", Caller: "again"})

	got := g.Recent()
	if len(got) != maxRecentRefusals {
		t.Fatalf("len(Recent()) = %d, want %d", len(got), maxRecentRefusals)
	}
	if last := got[len(got)-1]; last.Name != "NAME-7" || last.Caller != "again" {
		t.Errorf("newest = %+v, want the re-refused name, once, at the end", last)
	}
	seen := map[string]int{}
	for _, r := range got {
		seen[strings.ToLower(r.Name)]++
	}
	if seen["name-7"] != 1 {
		t.Errorf("name-7 appears %d times, want once", seen["name-7"])
	}
}

// A guard that was never wired — no daemon yet — has nothing to report and
// must not be dereferenced to find that out.
func TestGuard_nilRecentIsEmpty(t *testing.T) {
	var g *Guard
	if got := g.Recent(); got != nil {
		t.Errorf("Recent() on a nil guard = %v, want nil", got)
	}
}
