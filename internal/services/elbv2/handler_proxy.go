package elbv2

// handler_proxy.go — the load balancer data plane.
//
// A load balancer that only stores its listeners and target groups hands out a
// DNS name that reaches nothing, which is the shape every "deploy a service and
// open the URL" workflow depends on. This forwards a request arriving on a load
// balancer's DNS name to one of the targets registered behind it.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
)

// proxyTimeout bounds a single forwarded request. ALB's own idle timeout
// defaults to 60s, which is the closest analogue.
const proxyTimeout = 60 * time.Second

// roundRobin is the cursor behind target selection. ALB actually defaults to
// round robin for application load balancers, so this is the real algorithm
// rather than a stand-in.
var roundRobin atomic.Uint64

// HostRouteRewrite claims a request addressed to a load balancer's DNS name.
//
// Kept thin per the hostroute recipe: the handler resolves the load balancer
// from the Host header itself, so nothing about the name needs re-encoding
// into the path. The ALB hostname puts the region *before* the label
// ({name}-{id}.{region}.elb.{base}), so m.ID carries both and is not a useful
// key on its own.
func (s *Service) HostRouteRewrite(r *http.Request, _ middleware.HostRouteMatch) {
	middleware.PrefixPath(r, "/_overcast/elb")
}

// ProxyRequest forwards a request to a target registered behind the load
// balancer whose DNS name it arrived on.
func (h *Handler) ProxyRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	region := h.region(ctx)
	host := hostWithoutPort(r.Host)

	lb, err := h.loadBalancerByDNSName(ctx, region, host)
	if err != nil || lb == nil {
		// AWS answers a name that resolves to a load balancer it does not have
		// with a connection failure; the closest HTTP analogue is 421.
		http.Error(w, "no load balancer for host "+host, http.StatusMisdirectedRequest)
		return
	}

	target, port, err := h.pickTarget(ctx, region, lb, listenerPort(r))
	if err != nil {
		h.log.Warn("elbv2: no target available",
			zap.String("load_balancer", lb.LoadBalancerName), zap.Error(err))
		// ALB returns 503 with no healthy targets, which is the single most
		// recognisable load balancer response there is.
		http.Error(w, "503 Service Temporarily Unavailable", http.StatusServiceUnavailable)
		return
	}

	// Strip the /_overcast/elb prefix the host rewrite added, restoring the path the
	// client actually asked for. RawPath is stripped alongside Path rather than
	// cleared: ALB forwards the path to the target unchanged, so a %2F must not
	// reach it as a separator (#2136).
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/_overcast/elb")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}
	r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/_overcast/elb")

	upstream := &url.URL{Scheme: "http", Host: net.JoinHostPort(target, strconv.Itoa(port))}
	proxyCtx, cancel := context.WithTimeout(ctx, proxyTimeout)
	defer cancel()

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			// The backend is addressed by IP, but an application behind a load
			// balancer builds its own links from the Host it was given — which
			// is the load balancer's name, not the target's address. WordPress
			// is the canonical example.
			pr.Out.Host = r.Host
			pr.SetXForwarded()
		},
		ErrorHandler: func(ew http.ResponseWriter, _ *http.Request, perr error) {
			h.log.Warn("elbv2: target request failed",
				zap.String("load_balancer", lb.LoadBalancerName),
				zap.String("target", upstream.Host), zap.Error(perr))
			http.Error(ew, "502 Bad Gateway", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(proxyCtx))
}

// listenerPort is the port the client believes it connected to. Overcast serves
// every listener on its own port, so the load balancer's listener port is taken
// from the Host header when it carries one and defaults to 80.
func listenerPort(r *http.Request) int {
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		if p, convErr := strconv.Atoi(port); convErr == nil {
			return p
		}
	}
	return 80
}

func hostWithoutPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// loadBalancerByDNSName finds the load balancer a request's Host names.
func (h *Handler) loadBalancerByDNSName(ctx context.Context, region, host string) (*LoadBalancer, error) {
	lbs, err := h.listLBs(ctx, region)
	if err != nil {
		return nil, err
	}
	for _, lb := range lbs {
		if strings.EqualFold(lb.DNSName, host) {
			return lb, nil
		}
	}
	return nil, nil
}

// pickTarget chooses a target to forward to, round robin across those
// registered in the target group the listener forwards to.
//
// Overcast serves every listener on the same address, so a request cannot say
// which listener port it meant beyond what the Host carries; when only one
// listener exists — overwhelmingly the common case — that ambiguity does not
// arise. The port a target is reached on is the target's own registered port,
// falling back to the target group's, exactly as ALB resolves it.
func (h *Handler) pickTarget(ctx context.Context, region string, lb *LoadBalancer, port int) (string, int, error) {
	listeners, err := h.listListenersByLB(ctx, region, lb.LoadBalancerArn)
	if err != nil {
		return "", 0, err
	}
	if len(listeners) == 0 {
		return "", 0, fmt.Errorf("load balancer %s has no listeners", lb.LoadBalancerName)
	}
	listener := listeners[0]
	for _, l := range listeners {
		if l.Port == port {
			listener = l
			break
		}
	}

	for _, tgArn := range listener.forwardTargetGroups() {
		tg, found, tgErr := h.getTG(ctx, region, tgArn)
		if tgErr != nil || !found {
			continue
		}
		targets, tErr := h.listTargets(ctx, region, tgArn)
		if tErr != nil || len(targets) == 0 {
			continue
		}
		t := targets[int(roundRobin.Add(1)-1)%len(targets)]
		targetPort := t.Port
		if targetPort == 0 {
			targetPort = tg.Port
		}
		if targetPort == 0 {
			continue
		}
		return t.Id, targetPort, nil
	}
	return "", 0, fmt.Errorf("no registered targets behind %s", lb.LoadBalancerName)
}
