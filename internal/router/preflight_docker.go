package router

// preflight_docker.go — "why did my container just not start".
//
// # The failure this exists for
//
// Docker not running, or its socket not permitted for the current user, is
// docs/plans/deploy-failure-diagnosis.md's W4's first named instance.
// Nothing about it looks like an environment problem from where a developer
// is standing: `RunTask`/`CreateDBInstance`/`CreateCluster` and friends all
// return a normal-looking 200 (container-backed services degrade to
// metadata-only rather than failing the API call — see
// docker.SetBackingHeaders' x-overcast-backing-reason, which already tells a
// single request the truth), and the container simply never shows up. That
// reads exactly like a bug in Overcast.
//
// # Why this doesn't add a second probe
//
// internal/router's own Docker Supervisor already probes every configured
// service's socket exactly once at startup (internal/docker.Supervisor.Probe,
// which itself calls internal/docker.Probe — the retrying, network-ensuring
// bootstrap every Docker-backed service already shares). This file adds no
// new connection attempt: it is a pure diff between the configs asked for and
// the results Probe actually returned, computed once from data the caller
// already has in hand.
//
// # Why one Warn instead of N
//
// Supervisor.Probe already logs a per-service line when a socket doesn't
// answer (at Debug, precisely so the per-socket detail is still there for
// anyone who goes looking without repeating itself at a level every startup
// prints). What a developer needs at a glance is the answer to one question —
// "is anything degraded, and if so what" — not one line per affected service.
// dockerUnavailableWarning answers that once, naming every affected service
// together with the socket it tried and the fix.
//
// # Why it only ever fires on a matched symptom
//
// configs lists exactly what this process asked Probe for; results lists
// exactly what came back connected. On the default, healthy compose/docker-run
// path the two lists are the same length and this returns "" — nothing to
// warn about. It only produces text when at least one configured service's
// socket did not answer.
import (
	"fmt"
	"sort"
	"strings"

	"github.com/overcast-sh/overcast/internal/docker"
)

// dockerUnavailableWarning names every configured Docker-backed service whose
// socket did not answer during Supervisor.Probe, grouped by the socket path
// they tried, plus what that means and how to fix it. Returns "" when every
// configured service probed successfully — the default, healthy path — so a
// caller can log unconditionally on a non-empty result without re-deriving
// the "was anything actually wrong" question itself.
//
// configs is every ServiceConfig Probe was asked to reach; results is exactly
// what Probe returned (one entry per service that connected — see
// docker.Supervisor.Probe's doc comment). The diff between the two names is
// what's missing.
func dockerUnavailableWarning(configs []docker.ServiceConfig, results []docker.ServiceResult) string {
	if len(configs) == 0 {
		return ""
	}

	connected := make(map[string]bool, len(results))
	for _, r := range results {
		connected[r.Name] = true
	}

	type group struct {
		socket string
		names  []string
	}
	bySocket := make(map[string]*group)
	var sockets []string
	for _, cfg := range configs {
		if connected[cfg.Name] {
			continue
		}
		g, ok := bySocket[cfg.Socket]
		if !ok {
			g = &group{socket: cfg.Socket}
			bySocket[cfg.Socket] = g
			sockets = append(sockets, cfg.Socket)
		}
		g.names = append(g.names, cfg.Name)
	}
	if len(sockets) == 0 {
		return ""
	}
	sort.Strings(sockets)

	parts := make([]string, 0, len(sockets))
	for _, socket := range sockets {
		g := bySocket[socket]
		sort.Strings(g.names)
		parts = append(parts, fmt.Sprintf("%s (socket %s)", strings.Join(g.names, ", "), socket))
	}

	return fmt.Sprintf(
		"Docker is not reachable for: %s — these run metadata-only for this process's lifetime: "+
			"container-backed operations (RunTask, CreateDBInstance, CreateCluster, and similar) will "+
			"fail at create time instead of starting a container, the same fact "+
			"x-overcast-backing-reason already reports per request. Overcast probed each socket "+
			"repeatedly over several seconds before giving up. Fix: start the Docker daemon (Docker "+
			"Desktop, colima, Rancher Desktop, a native dockerd, etc.) and restart Overcast; if it is "+
			"already running, check the socket is readable by this user (on Linux, add yourself to the "+
			"docker group, or verify DOCKER_HOST / the OVERCAST_*_DOCKER_SOCKET override for the "+
			"service named above).",
		strings.Join(parts, "; "),
	)
}

// dockerConnected reports whether the probe connected the named service.
func dockerConnected(results []docker.ServiceResult, name string) bool {
	for _, r := range results {
		if r.Name == name {
			return true
		}
	}
	return false
}
