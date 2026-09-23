package elbv2_test

// forwarding_test.go — the load balancer data plane. Shares elbCall, createLB,
// createTG and decodeXML with elbv2_test.go.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// backend stands in for whatever is registered behind a target group — a task,
// an instance — and reports the Host it was addressed with, because an app
// behind a load balancer builds its links from that. It echoes the path as it
// arrived on the wire, still encoded.
func backend(t *testing.T, body string) (host string, port int, seenHost *string) {
	t.Helper()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Host
		w.Header().Set("X-Backend", "yes")
		_, _ = io.WriteString(w, body+" "+r.URL.EscapedPath())
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse backend URL: %v", err)
	}
	h, p, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split backend host: %v", err)
	}
	pn, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("backend port: %v", err)
	}
	return h, pn, &got
}

// lbDNSName reads back the DNS name a load balancer was given.
func lbDNSName(t *testing.T, srv *helpers.TestServer, arn string) string {
	t.Helper()
	resp := elbCall(t, srv, "DescribeLoadBalancers", url.Values{"LoadBalancerArns.member.1": {arn}})
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Result struct {
			LoadBalancers struct {
				Member []struct {
					DNSName string `xml:"DNSName"`
				} `xml:"member"`
			} `xml:"LoadBalancers"`
		} `xml:"DescribeLoadBalancersResult"`
	}
	decodeXML(t, resp, &out)
	if len(out.Result.LoadBalancers.Member) == 0 || out.Result.LoadBalancers.Member[0].DNSName == "" {
		t.Fatal("expected a DNSName")
	}
	return out.Result.LoadBalancers.Member[0].DNSName
}

// getViaLoadBalancer issues a request to the emulator addressed to the load
// balancer's DNS name, which is what a client that resolved that name does.
func getViaLoadBalancer(t *testing.T, srv *helpers.TestServer, dnsName, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = dnsName
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request via load balancer: %v", err)
	}
	return resp
}

func TestLoadBalancer_forwardsToRegisteredTarget(t *testing.T) {
	// Given: a load balancer whose listener forwards to a target group with one
	// registered target.
	srv := helpers.NewTestServer(t)
	host, port, seenHost := backend(t, "hello from")

	lbArn := createLB(t, srv, "fwd-alb")
	tgArn := createTG(t, srv, "fwd-tg")

	resp := elbCall(t, srv, "CreateListener", url.Values{
		"LoadBalancerArn":                        {lbArn},
		"Protocol":                               {"HTTP"},
		"Port":                                   {"80"},
		"DefaultActions.member.1.Type":           {"forward"},
		"DefaultActions.member.1.TargetGroupArn": {tgArn},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	reg := elbCall(t, srv, "RegisterTargets", url.Values{
		"TargetGroupArn":        {tgArn},
		"Targets.member.1.Id":   {host},
		"Targets.member.1.Port": {strconv.Itoa(port)},
	})
	helpers.AssertStatus(t, reg, http.StatusOK)
	reg.Body.Close()

	// When: a client that resolved the load balancer's name requests a path.
	dnsName := lbDNSName(t, srv, lbArn)
	got := getViaLoadBalancer(t, srv, dnsName, "/some/path")
	defer got.Body.Close()

	// Then: the target answered, and it saw the load balancer's name as its
	// Host — which is what an application behind one builds its links from.
	helpers.AssertStatus(t, got, http.StatusOK)
	body, _ := io.ReadAll(got.Body)
	if !strings.Contains(string(body), "hello from /some/path") {
		t.Errorf("expected the target's response, got %q", body)
	}
	if got.Header.Get("X-Backend") != "yes" {
		t.Error("expected the target's headers to be passed through")
	}
	if *seenHost != dnsName {
		t.Errorf("target saw Host %q, want the load balancer's name %q", *seenHost, dnsName)
	}
}

func TestLoadBalancer_forwardsEncodedSlashUnchanged(t *testing.T) {
	// Given: a load balancer forwarding to one registered target.
	srv := helpers.NewTestServer(t)
	host, port, _ := backend(t, "hello from")

	lbArn := createLB(t, srv, "enc-alb")
	tgArn := createTG(t, srv, "enc-tg")

	resp := elbCall(t, srv, "CreateListener", url.Values{
		"LoadBalancerArn":                        {lbArn},
		"Protocol":                               {"HTTP"},
		"Port":                                   {"80"},
		"DefaultActions.member.1.Type":           {"forward"},
		"DefaultActions.member.1.TargetGroupArn": {tgArn},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	reg := elbCall(t, srv, "RegisterTargets", url.Values{
		"TargetGroupArn":        {tgArn},
		"Targets.member.1.Id":   {host},
		"Targets.member.1.Port": {strconv.Itoa(port)},
	})
	helpers.AssertStatus(t, reg, http.StatusOK)
	reg.Body.Close()

	// When: the path carries a percent-encoded slash inside one segment.
	got := getViaLoadBalancer(t, srv, lbDNSName(t, srv, lbArn), "/pkgs/@scope%2fpkg")
	defer got.Body.Close()

	// Then: the target receives the path exactly as sent — ALB does not
	// rewrite it, so the %2f must not turn into a separator (#2136).
	helpers.AssertStatus(t, got, http.StatusOK)
	body, _ := io.ReadAll(got.Body)
	if want := "hello from /pkgs/@scope%2fpkg"; string(body) != want {
		t.Errorf("target saw %q, want %q", body, want)
	}
}

func TestLoadBalancer_noRegisteredTargetsReturns503(t *testing.T) {
	// Given: a load balancer with a listener but nothing registered behind it.
	srv := helpers.NewTestServer(t)
	lbArn := createLB(t, srv, "empty-alb")
	tgArn := createTG(t, srv, "empty-tg")

	resp := elbCall(t, srv, "CreateListener", url.Values{
		"LoadBalancerArn":                        {lbArn},
		"Protocol":                               {"HTTP"},
		"Port":                                   {"80"},
		"DefaultActions.member.1.Type":           {"forward"},
		"DefaultActions.member.1.TargetGroupArn": {tgArn},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// When: a client requests it.
	got := getViaLoadBalancer(t, srv, lbDNSName(t, srv, lbArn), "/")
	defer got.Body.Close()
	io.Copy(io.Discard, got.Body) //nolint:errcheck

	// Then: the answer is the one a load balancer with no healthy targets gives.
	helpers.AssertStatus(t, got, http.StatusServiceUnavailable)
}

func TestCreateListener_echoesDefaultActions(t *testing.T) {
	// Given: a load balancer and a target group.
	srv := helpers.NewTestServer(t)
	lbArn := createLB(t, srv, "echo-alb")
	tgArn := createTG(t, srv, "echo-tg")

	// When: a listener is created forwarding to the target group.
	resp := elbCall(t, srv, "CreateListener", url.Values{
		"LoadBalancerArn":                        {lbArn},
		"Protocol":                               {"HTTP"},
		"Port":                                   {"80"},
		"DefaultActions.member.1.Type":           {"forward"},
		"DefaultActions.member.1.TargetGroupArn": {tgArn},
	})
	helpers.AssertStatus(t, resp, http.StatusOK)
	resp.Body.Close()

	// Then: DescribeListeners reports the action, so a client can see where the
	// listener forwards — and the link the data plane needs is really stored.
	desc := elbCall(t, srv, "DescribeListeners", url.Values{"LoadBalancerArn": {lbArn}})
	helpers.AssertStatus(t, desc, http.StatusOK)
	var out struct {
		Result struct {
			Listeners struct {
				Member []struct {
					DefaultActions struct {
						Member []struct {
							Type           string `xml:"Type"`
							TargetGroupArn string `xml:"TargetGroupArn"`
						} `xml:"member"`
					} `xml:"DefaultActions"`
				} `xml:"member"`
			} `xml:"Listeners"`
		} `xml:"DescribeListenersResult"`
	}
	decodeXML(t, desc, &out)
	if len(out.Result.Listeners.Member) == 0 {
		t.Fatal("expected a listener")
	}
	actions := out.Result.Listeners.Member[0].DefaultActions.Member
	if len(actions) != 1 || actions[0].Type != "forward" || actions[0].TargetGroupArn != tgArn {
		t.Errorf("expected a forward action to %s, got %#v", tgArn, actions)
	}
}
