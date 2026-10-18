package lambdaweb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

func TestServe_proxiesToAnEnvironmentAndAppliesHeaderRules(t *testing.T) {
	// Given: an ApplicationManaged endpoint backed by an echoing environment
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})

	// When: a request arrives carrying headers Lambda rewrites or removes
	resp, body := f.do(d, "POST", "/orders/42?x=1&y=%2F", strings.NewReader("payload"), map[string]string{
		"X-Real-Ip":            "9.9.9.9",
		"X-Method-Override":    "DELETE",
		"Lambda-Web-Spoofed":   "evil",
		"X-Forwarded-For":      "1.2.3.4",
		"X-Forwarded-Host":     "shop.example.com",
		"X-Custom":             "kept",
		"Authorization":        "AWS4-HMAC-SHA256 Credential=x",
		"X-Amz-Security-Token": "token",
	})

	// Then: the application sees the request path and query unchanged
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body %q", resp.StatusCode, body)
	}
	for _, want := range []string{"POST /orders/42?x=1&y=%2F", "host=" + d.domain, "X-Custom=kept", "X-Forwarded-Host=shop.example.com", "Lambda-Web-Endpoint-Name=prod"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	// And: the documented headers are removed, an AWS signature is consumed, XFF is appended
	for _, gone := range []string{"X-Real-Ip=9.9.9.9", "X-Method-Override=DELETE", "Lambda-Web-Spoofed=evil", "Authorization=AWS4", "X-Amz-Security-Token=token"} {
		if strings.Contains(body, gone) {
			t.Errorf("body still has %q:\n%s", gone, body)
		}
	}
	if !strings.Contains(body, "X-Forwarded-For=1.2.3.4, 127.0.0.1") {
		t.Errorf("X-Forwarded-For not appended:\n%s", body)
	}
	// And: Lambda's response headers are present
	if resp.Header.Get("Lambda-Web-Request-Id") == "" || resp.Header.Get("Lambda-Web-Logstream") == "" || resp.Header.Get("X-App") != "yes" {
		t.Errorf("response headers = %v", resp.Header)
	}
	if resp.Header.Get("Lambda-Web-Error-Type") != "" {
		t.Errorf("unexpected error type %q", resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestServe_nonAWSAuthorizationReachesTheApplication(t *testing.T) {
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	_, body := f.do(d, "GET", "/", nil, map[string]string{"Authorization": "Bearer abc"})
	if !strings.Contains(body, "Authorization=Bearer abc") {
		t.Errorf("body = %s", body)
	}
}

func TestServe_protocolRejections(t *testing.T) {
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})

	// An unsupported method is a 400 before the application sees it.
	resp, _ := f.do(d, "TRACE", "/", nil, nil)
	if resp.StatusCode != 400 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeMalformedRequest {
		t.Errorf("TRACE = %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
	// HTTP/1.0 is a 505.
	raw := rawRequest(t, f.server.URL, "GET "+invokeBase+"/"+d.id+"/us-east-1/ HTTP/1.0\r\nHost: "+d.domain+"\r\n\r\n")
	if !strings.HasPrefix(raw, "HTTP/1.0 505") && !strings.HasPrefix(raw, "HTTP/1.1 505") {
		t.Errorf("HTTP/1.0 response = %q", firstLine(raw))
	}
	if !strings.Contains(strings.ToLower(raw), "lambda-web-error-type: platform.unsupportedhttpversion") {
		t.Errorf("505 lacks its error type: %q", raw)
	}
	// A domain no endpoint has is a 404 InvalidEndpoint.
	req, _ := http.NewRequest("GET", f.server.URL+invokeBase+"/nope.xyz/us-east-1/", nil)
	r, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 || r.Header.Get("Lambda-Web-Error-Type") != errTypeInvalidEndpoint {
		t.Errorf("unknown endpoint = %d %q", r.StatusCode, r.Header.Get("Lambda-Web-Error-Type"))
	}
	// A request head over 128 KiB is a 431.
	resp, _ = f.do(d, "GET", "/", nil, map[string]string{"X-Big": strings.Repeat("a", 130*1024)})
	if resp.StatusCode != 431 && resp.StatusCode != 400 {
		t.Errorf("huge header = %d", resp.StatusCode)
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func TestServe_coldStartSharesOneEnvironmentAcrossConcurrentRequests(t *testing.T) {
	// Given: an environment that takes a moment to start
	f := newDPFixture(t, dpOptions{})
	f.prov.delay = 150 * time.Millisecond
	d := f.deploy(deployOpts{})

	// When: ten requests arrive at once
	var wg sync.WaitGroup
	statuses := make([]int, 10)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := f.do(d, "GET", "/", nil, nil)
			statuses[i] = resp.StatusCode
		}()
	}
	wg.Wait()

	// Then: all succeed on one environment
	for i, s := range statuses {
		if s != 200 {
			t.Errorf("request %d = %d", i, s)
		}
	}
	if n := f.prov.starts.Load(); n != 1 {
		t.Errorf("environments started = %d, want 1", n)
	}
}

func TestServe_scalesOutAtTheConcurrencyLimitAndPacksOtherwise(t *testing.T) {
	// Given: environments that hold each request, with room for 2 at a time
	release := make(chan struct{})
	var inFlight, peak atomic.Int32
	f := newDPFixture(t, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			_, _ = io.WriteString(w, "ok")
		})
	}})
	d := f.deploy(deployOpts{revCfg: func(c *revisionConfigIn) { c.Service.MaxConcurrencyPerEnvironment = intp(2) }})

	// When: five requests are in flight at once
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.do(d, "GET", "/", nil, nil)
		}()
	}
	waitFor(t, func() bool { return inFlight.Load() == 5 }, "all five requests to reach an environment")

	// Then: three environments serve them (2+2+1)
	if n := f.prov.starts.Load(); n != 3 {
		t.Errorf("environments = %d, want 3", n)
	}
	close(release)
	wg.Wait()

	// And: a later sequential request reuses the first environment
	before := f.prov.starts.Load()
	f.do(d, "GET", "/", nil, nil)
	if f.prov.starts.Load() != before {
		t.Errorf("a sequential request started a new environment")
	}
}

func TestServe_maxEnvironmentsThrottles(t *testing.T) {
	// Given: an endpoint capped at two environments of one slot each
	release := make(chan struct{})
	var inFlight atomic.Int32
	f := newDPFixture(t, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inFlight.Add(1)
			<-release
		})
	}})
	d := f.deploy(deployOpts{
		revCfg:      func(c *revisionConfigIn) { c.Service.MaxConcurrencyPerEnvironment = intp(1) },
		endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision, ScalingConfig: &scalingConfig{MaxEnvironments: intp(2)}},
	})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); f.do(d, "GET", "/", nil, nil) }()
	}
	waitFor(t, func() bool { return inFlight.Load() == 2 }, "two requests in flight")

	// When: a third arrives
	resp, _ := f.do(d, "GET", "/", nil, nil)
	close(release)
	wg.Wait()

	// Then: it is throttled, with the error type and Retry-After
	if resp.StatusCode != 429 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeEndpointMaxEnvThrottle || resp.Header.Get("Retry-After") == "" {
		t.Errorf("third request = %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestServe_rateLimit(t *testing.T) {
	// Given: an endpoint limited to 100 requests per second (a burst of 10)
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision, ThrottleConfig: &throttleCfg{RateLimit: intp(100)}}})

	// When: a burst of 40 arrives
	ok, throttled := 0, 0
	for range 40 {
		resp, _ := f.do(d, "GET", "/", nil, nil)
		switch resp.StatusCode {
		case 200:
			ok++
		case 429:
			throttled++
			if resp.Header.Get("Lambda-Web-Error-Type") != errTypeEndpointRequestThrot {
				t.Errorf("429 type = %q", resp.Header.Get("Lambda-Web-Error-Type"))
			}
		}
	}

	// Then: some pass and the rest are throttled
	if ok == 0 || throttled == 0 {
		t.Errorf("ok=%d throttled=%d", ok, throttled)
	}
}

func TestServe_rateLimitZeroDisablesTheEndpoint(t *testing.T) {
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision, ThrottleConfig: &throttleCfg{RateLimit: intp(0)}}})
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 503 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeEndpointDisabled {
		t.Errorf("= %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestServe_requestTimeoutIs504(t *testing.T) {
	// Given: a revision with a 1 s timeout and an application that hangs
	f := newDPFixture(t, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	}})
	d := f.deploy(deployOpts{})
	f.patchRevision(d, func(r *revision) { r.Service.TimeoutSeconds = intp(1) }) // the API's minimum is 3 s

	// When / Then
	start := time.Now()
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 504 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeRequestTimeout {
		t.Errorf("= %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
}

func TestServe_initFailureIs502WithItsType(t *testing.T) {
	// Given: an environment that cannot import its entry file
	f := newDPFixture(t, dpOptions{})
	f.prov.failure = func(envSpec) *initFailure {
		return &initFailure{Type: errTypeImportCode, Detail: "Cannot find module '/var/task/index.js'"}
	}
	d := f.deploy(deployOpts{})

	// When: two requests arrive in quick succession
	first, body := f.do(d, "GET", "/", nil, nil)
	second, _ := f.do(d, "GET", "/", nil, nil)

	// Then: both are 502 with the type, and the second did not start another container
	if first.StatusCode != 502 || first.Header.Get("Lambda-Web-Error-Type") != errTypeImportCode || !strings.Contains(body, "Cannot find module") {
		t.Errorf("first = %d %q %q", first.StatusCode, first.Header.Get("Lambda-Web-Error-Type"), body)
	}
	if second.StatusCode != 502 {
		t.Errorf("second = %d", second.StatusCode)
	}
	if n := f.prov.starts.Load(); n != 1 {
		t.Errorf("starts = %d, want 1 (the failure is remembered briefly)", n)
	}
}

func TestServe_environmentCrashIsReportedByHowItDied(t *testing.T) {
	// Given: an application that gets its environment killed for memory mid-request
	f := newDPFixture(t, dpOptions{})
	f.prov.handler = func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.prov.mu.Lock()
			e := f.prov.envs[0]
			f.prov.mu.Unlock()
			go e.crash(exitReason{OOM: true})
			<-r.Context().Done()
		})
	}
	d := f.deploy(deployOpts{})

	// When / Then: the caller learns it ran out of memory
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 503 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeOutOfMemory {
		t.Errorf("= %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestServe_weightedRoutingSplitsTrafficBetweenRevisions(t *testing.T) {
	// Given: two revisions, 90/10, each answering with its own id
	f := newDPFixture(t, dpOptions{handler: func(spec envSpec) http.Handler {
		id := spec.Revision.ID
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, id) })
	}})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision}})
	second := f.addRevision(d.function)
	if _, aerr := f.svc.updateWebFunctionEndpointTyped(context.Background(), &updateEndpointRequest{
		FunctionName: d.function, EndpointName: "prod", AutoDeploymentMode: deployDisabled,
		RevisionWeights: []revisionWt{{RevisionID: d.revision, Weight: 90}, {RevisionID: second, Weight: 10}},
	}); aerr != nil {
		t.Fatalf("update: %v", aerr)
	}
	f.svc.invalidateRoutes()

	// When: 400 requests are made
	counts := map[string]int{}
	for range 400 {
		_, body := f.do(d, "GET", "/", nil, nil)
		counts[body]++
	}

	// Then: both revisions are served, in roughly the weights
	if counts[d.revision] < 300 || counts[second] < 10 || counts[second] > 100 {
		t.Errorf("counts = %v", counts)
	}
}

func (f *dpFixture) addRevision(function string) string {
	f.t.Helper()
	cfg := testRevisionConfig("code-bucket", "app-v2.zip")
	out, aerr := f.svc.createWebFunctionRevisionTyped(context.Background(), &createRevisionRequest{FunctionName: function, revisionConfigIn: *cfg})
	if aerr != nil {
		f.t.Fatalf("revision: %v", aerr)
	}
	f.svc.invalidateRoutes()
	return out.RevisionID
}

func TestServe_idleEnvironmentsScaleToZero(t *testing.T) {
	// Given: a warm environment and a 1 minute idle TTL
	f := newDPFixture(t, dpOptions{idleTTL: time.Minute})
	d := f.deploy(deployOpts{})
	f.do(d, "GET", "/", nil, nil)
	if f.prov.liveEnvs() != 1 {
		t.Fatalf("live environments = %d", f.prov.liveEnvs())
	}

	// When: the clock passes the TTL and a sweep runs
	f.clk.Add(2 * time.Minute)

	// Then: the environment is stopped, and the next request starts another
	waitFor(t, func() bool { return f.prov.liveEnvs() == 0 }, "the idle environment to be stopped")
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 200 || f.prov.starts.Load() != 2 {
		t.Errorf("after scale-to-zero: status %d, starts %d", resp.StatusCode, f.prov.starts.Load())
	}
}

func TestServe_hostCapReclaimsAnIdleEnvironmentElsewhere(t *testing.T) {
	// Given: a host limited to one environment, held idle by function a
	f := newDPFixture(t, dpOptions{tweakCfg: func(c *configT) { c.LambdaWebMaxEnvironments = 1 }})
	a := f.deploy(deployOpts{name: "a"})
	f.do(a, "GET", "/", nil, nil)
	b := f.deploy(deployOpts{name: "b"})

	// When: function b needs an environment
	resp, _ := f.do(b, "GET", "/", nil, nil)

	// Then: a's idle environment is reclaimed to make room
	if resp.StatusCode != 200 {
		t.Errorf("b = %d", resp.StatusCode)
	}
}

func TestServe_hostCapThrottlesWhenNothingIsIdle(t *testing.T) {
	release := make(chan struct{})
	var held atomic.Int32
	f := newDPFixture(t, dpOptions{
		tweakCfg: func(c *configT) { c.LambdaWebMaxEnvironments = 1 },
		handler: func(envSpec) http.Handler {
			return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { held.Add(1); <-release })
		},
	})
	a := f.deploy(deployOpts{name: "a"})
	b := f.deploy(deployOpts{name: "b"})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); f.do(a, "GET", "/", nil, nil) }()
	waitFor(t, func() bool { return held.Load() == 1 }, "a's request to be held")

	resp, _ := f.do(b, "GET", "/", nil, nil)
	close(release)
	wg.Wait()
	if resp.StatusCode != 429 || resp.Header.Get("Lambda-Web-Error-Type") != errTypeAccountVCPUThrottle {
		t.Errorf("b = %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestServe_iamAuthRequiresASignedRequest(t *testing.T) {
	// Given: an IamAuth endpoint and no authoriser wired (the structural check applies)
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision}})

	// When / Then: unsigned is 403 with no error type; signed passes
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 403 || resp.Header.Get("Lambda-Web-Error-Type") != "" {
		t.Errorf("unsigned = %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
	resp, _ = f.do(d, "GET", "/", nil, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=a/b/c/lambda/aws4_request", "X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD"})
	if resp.StatusCode != 200 {
		t.Errorf("signed = %d", resp.StatusCode)
	}
	resp, _ = f.do(d, "GET", "/", nil, map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=a/b/c/lambda/aws4_request"})
	if resp.StatusCode != 403 {
		t.Errorf("signed without a content hash = %d", resp.StatusCode)
	}
}

func TestServe_authoriserDecides(t *testing.T) {
	f := newDPFixture(t, dpOptions{authorize: denyAll{}})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision}})
	resp, _ := f.do(d, "GET", "/", nil, map[string]string{"Authorization": "AWS4-HMAC-SHA256 x", "X-Amz-Content-Sha256": "y"})
	if resp.StatusCode != 403 {
		t.Errorf("= %d", resp.StatusCode)
	}
	// An ApplicationManaged endpoint never consults the authoriser.
	g := f.deploy(deployOpts{name: "open"})
	resp, _ = f.do(g, "GET", "/", nil, nil)
	if resp.StatusCode != 200 {
		t.Errorf("open endpoint = %d", resp.StatusCode)
	}
}

type denyAll struct{}

func (denyAll) Authorize(context.Context, *http.Request, authContext) *platformError {
	return errForbidden()
}

func TestServe_streamsResponsesIncrementally(t *testing.T) {
	// Given: an application that sends a chunk, waits, then sends another
	proceed := make(chan struct{})
	f := newDPFixture(t, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: one\n\n")
			w.(http.Flusher).Flush()
			<-proceed
			_, _ = io.WriteString(w, "data: two\n\n")
		})
	}})
	d := f.deploy(deployOpts{})

	// When: the client reads the first event before the application finishes
	req, _ := http.NewRequest("GET", f.server.URL+invokeBase+"/"+d.id+"/us-east-1/", nil)
	req.Host = d.domain
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)

	// Then: it arrives while the application is still holding the response open
	if err != nil || !strings.Contains(string(buf[:n]), "data: one") {
		t.Fatalf("first read = %q, %v", buf[:n], err)
	}
	close(proceed)
	rest, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(rest), "data: two") {
		t.Errorf("rest = %q", rest)
	}
}

func TestServe_dataPlaneWithoutAProviderIs503(t *testing.T) {
	f := newDPFixture(t, dpOptions{noWire: true})
	d := f.deploy(deployOpts{})
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 503 {
		t.Errorf("= %d", resp.StatusCode)
	}
}

func TestServe_aDeletedEndpointStopsServingAndItsEnvironmentsStop(t *testing.T) {
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	f.do(d, "GET", "/", nil, nil)
	if _, aerr := f.svc.deleteWebFunctionEndpointTyped(context.Background(), &endpointRequest{FunctionName: d.function, EndpointName: "prod"}); aerr != nil {
		t.Fatalf("delete: %v", aerr)
	}
	f.svc.invalidateRoutes()
	resp, _ := f.do(d, "GET", "/", nil, nil)
	if resp.StatusCode != 404 {
		t.Errorf("after delete = %d", resp.StatusCode)
	}
	waitFor(t, func() bool { return f.prov.liveEnvs() == 0 }, "the deleted endpoint's environment to stop")
}

func intp(v int) *int { return &v }

func waitFor(t testing.TB, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var _ = fmt.Sprintf

// patchRevision edits a stored revision directly, for states the API refuses
// to create (a 1 s timeout), and invalidates the routing table.
func (f *dpFixture) patchRevision(d deployed, edit func(*revision)) {
	f.t.Helper()
	ctx := context.Background()
	rev, found, aerr := f.svc.loadRevision(ctx, "us-east-1", d.function, d.revision)
	if aerr != nil || !found {
		f.t.Fatalf("load revision: %v %v", found, aerr)
	}
	edit(rev)
	if aerr := putRecord(ctx, f.svc, nsRevisions, childKey("us-east-1", d.function, d.revision), rev); aerr != nil {
		f.t.Fatalf("put revision: %v", aerr)
	}
	f.svc.invalidateRoutes()
}

// BenchmarkServe measures the data plane's added cost per request: routing,
// header rewriting and the proxy hop to a loopback environment that answers
// "ok". Run with -benchmem; allocations on this path are the number to watch.
func BenchmarkServe(b *testing.B) {
	f := newDPFixture(b, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	}})
	d := f.deploy(deployOpts{})
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	url := f.server.URL + invokeBase + "/" + d.id + "/us-east-1/"
	// Warm the environment.
	if resp, err := client.Get(url); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequest("GET", url, nil)
			req.Host = d.domain
			resp, err := client.Do(req)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})
}

func TestServe_aFailureMidBodyCutsTheConnectionInsteadOfAppendingAnError(t *testing.T) {
	// Given: the real Recovery in front of an application that dies after it has
	// started streaming its response
	f := newDPFixture(t, dpOptions{wrap: middleware.Recovery(zap.NewNop())})
	var env *fakeEnv
	f.prov.handler = func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			f.prov.mu.Lock()
			env = f.prov.envs[0]
			f.prov.mu.Unlock()
			env.crash(exitReason{Code: 1})
			<-r.Context().Done()
		})
	}
	d := f.deploy(deployOpts{})

	// When: a client reads the response
	req, _ := http.NewRequest("GET", f.server.URL+invokeBase+"/"+d.id+"/us-east-1/", nil)
	req.Host = d.domain
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)

	// Then: the body is cut short with an error, and no AWS error blob follows it
	if readErr == nil {
		t.Errorf("body read cleanly: %q", body)
	}
	if strings.Contains(string(body), "InternalError") || strings.Contains(string(body), "<Error>") {
		t.Errorf("an error was appended to the stream: %q", body)
	}
}

func TestServe_throttleComesBeforeAuthentication(t *testing.T) {
	// Given: an IamAuth endpoint limited to 100 per second whose authoriser
	// counts the requests it is asked to verify
	var verified atomic.Int32
	f := newDPFixture(t, dpOptions{authorize: countingDeny{&verified}})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision, ThrottleConfig: &throttleCfg{RateLimit: intp(100)}}})

	// When: a flood of unsigned requests arrives
	throttled := 0
	for range 60 {
		resp, _ := f.do(d, "GET", "/", nil, nil)
		if resp.StatusCode == 429 {
			throttled++
		}
	}

	// Then: the burst (10) is verified and the rest never reach the authoriser
	// (the mock clock does not advance, so the count is exact; nothing in the
	// fixture's do or deploy moves it)
	if throttled != 50 || verified.Load() != 10 {
		t.Errorf("throttled=%d verified=%d, want 50 and 10", throttled, verified.Load())
	}
}

func TestServe_anEndpointThrottledRequestDoesNotSpendTheAccountLimit(t *testing.T) {
	// Given: an endpoint limited to 100 per second (a burst of 10) and an account
	// limit of 1000 per second (a burst of 100); the mock clock stands still
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision, ThrottleConfig: &throttleCfg{RateLimit: intp(100)}}})
	f.svc.startEngine()
	f.svc.dp.acct.Store(newLimiter(1000))

	// When: fifty requests arrive in one instant
	ok := 0
	for range 50 {
		if resp, _ := f.do(d, "GET", "/", nil, nil); resp.StatusCode == 200 {
			ok++
		}
	}

	// Then: the endpoint let its burst through, and the account was charged for those only
	if ok != 10 {
		t.Fatalf("ok = %d, want 10", ok)
	}
	left := 0
	for f.svc.dp.acct.Load().allow(f.clk.Now().UnixNano()) {
		left++
	}
	if left != 90 {
		t.Errorf("account tokens left = %d, want 90", left)
	}
}

type countingDeny struct{ n *atomic.Int32 }

func (c countingDeny) Authorize(context.Context, *http.Request, authContext) *platformError {
	c.n.Add(1)
	return errForbidden()
}

// sourceSpy records the CloudFront identity the authoriser was given.
type sourceSpy struct {
	mu  sync.Mutex
	arn []string
}

func (s *sourceSpy) Authorize(_ context.Context, _ *http.Request, c authContext) *platformError {
	s.mu.Lock()
	s.arn = append(s.arn, c.SourceARN)
	s.mu.Unlock()
	return nil
}

func TestServe_forgedCloudFrontIdentityIsStrippedAndNeverTrusted(t *testing.T) {
	// Given: an IamAuth endpoint behind an authoriser that records the source ARN
	spy := &sourceSpy{}
	f := newDPFixture(t, dpOptions{authorize: spy, handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "arn=%q token=%q", r.Header.Get(serviceutil.CloudFrontSourceARNHeader), r.Header.Get(serviceutil.InternalTrustHeader))
		})
	}})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision}})

	// When: a client claims to be a CloudFront distribution without the
	// process's secret
	_, body := f.do(d, "GET", "/", nil, map[string]string{
		serviceutil.CloudFrontSourceARNHeader: "arn:aws:cloudfront::111122223333:distribution/EFORGED",
		serviceutil.InternalTrustHeader:       "guess",
	})

	// Then: the application never sees either header, and the authoriser is
	// told there is no distribution
	if body != `arn="" token=""` {
		t.Errorf("application saw %s", body)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.arn) != 1 || spy.arn[0] != "" {
		t.Errorf("authoriser source ARNs = %q, want one empty", spy.arn)
	}
}

func TestServe_genuineCloudFrontIdentityReachesTheAuthoriserButNotTheApplication(t *testing.T) {
	// Given: the same endpoint, and a request stamped the way CloudFront in this
	// process stamps it
	spy := &sourceSpy{}
	f := newDPFixture(t, dpOptions{authorize: spy, handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "arn=%q token=%q", r.Header.Get(serviceutil.CloudFrontSourceARNHeader), r.Header.Get(serviceutil.InternalTrustHeader))
		})
	}})
	d := f.deploy(deployOpts{endpointCfg: &endpointConfigIn{EndpointType: endpointHomeRegion, AuthType: authIam, AutoDeploymentMode: deployLatestRevision}})
	h := http.Header{}
	const arn = "arn:aws:cloudfront::111122223333:distribution/EREAL"
	serviceutil.SetCloudFrontSource(h, arn)

	// When: it arrives
	_, body := f.do(d, "GET", "/", nil, map[string]string{
		serviceutil.CloudFrontSourceARNHeader: h.Get(serviceutil.CloudFrontSourceARNHeader),
		serviceutil.InternalTrustHeader:       h.Get(serviceutil.InternalTrustHeader),
	})

	// Then: the authoriser sees the distribution, and the application still sees nothing
	if body != `arn="" token=""` {
		t.Errorf("application saw %s", body)
	}
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if len(spy.arn) != 1 || spy.arn[0] != arn {
		t.Errorf("authoriser source ARNs = %q, want %q", spy.arn, arn)
	}
}

func TestServe_policyAndTagChangesReachTheRouteTable(t *testing.T) {
	// Given: a function that has served a request, so its route table is built
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	f.do(d, "GET", "/", nil, nil)
	fnARN := f.svc.functionARN("us-east-1", d.function)
	route := func() (*policyRecord, map[string]string) {
		tbl, err := f.svc.routes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ro := tbl.byHost[d.id]
		return ro.policy, tbl.tags[fnARN]
	}
	if p, tags := route(); p != nil || len(tags) != 0 {
		t.Fatalf("before = %v %v", p, tags)
	}

	// When: a policy and a tag are added after traffic has started
	policy := resourcePolicy(`{"Service":"cloudfront.amazonaws.com"}`, "")
	if _, aerr := f.svc.putResourcePolicyTyped(context.Background(), &putPolicyRequest{ResourceARN: fnARN, Policy: policy}); aerr != nil {
		t.Fatalf("put policy: %s", aerr.Message)
	}
	if _, aerr := f.svc.tagResourceTyped(context.Background(), &tagResourceRequest{Resource: fnARN, Tags: map[string]string{"team": "a"}}); aerr != nil {
		t.Fatalf("tag: %s", aerr.Message)
	}

	// Then: the next request is judged by them
	if p, tags := route(); p == nil || tags["team"] != "a" {
		t.Errorf("after add = %v %v", p, tags)
	}

	// And when they are removed again, by the next request too
	if _, aerr := f.svc.untagResourceTyped(context.Background(), &untagResourceRequest{Resource: fnARN, TagKeys: []string{"team"}}); aerr != nil {
		t.Fatalf("untag: %s", aerr.Message)
	}
	if _, aerr := f.svc.deleteResourcePolicyTyped(context.Background(), &policyRef{ResourceARN: fnARN}); aerr != nil {
		t.Fatalf("delete policy: %s", aerr.Message)
	}
	if p, tags := route(); p != nil || len(tags) != 0 {
		t.Errorf("after remove = %v %v", p, tags)
	}
}

func TestServe_aCrashedEnvironmentsContainerIsRemoved(t *testing.T) {
	// Given: a running environment
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	f.do(d, "GET", "/", nil, nil)
	f.prov.mu.Lock()
	env := f.prov.envs[0]
	f.prov.mu.Unlock()

	// When: its process dies on its own
	env.crash(exitReason{Code: 1})

	// Then: the engine still asks the provider to remove it
	eventually(t, "Stop to be called on the crashed environment", func() bool { return env.stopCalls.Load() >= 1 })
}

func TestServe_anApplicationsOwnCORSHeadersReplaceThePreset(t *testing.T) {
	// Given: the emulator's permissive CORS in front, and an application that
	// names its own origin
	f := newDPFixture(t, dpOptions{
		wrap: middleware.CORS,
		handler: func(envSpec) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "https://app.example")
			})
		},
	})
	d := f.deploy(deployOpts{})

	// When: a request goes through the invoke path (which keeps the middleware)
	resp, _ := f.do(d, "GET", "/", nil, nil)

	// Then: the browser sees one origin, the application's
	if got := resp.Header.Values("Access-Control-Allow-Origin"); len(got) != 1 || got[0] != "https://app.example" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestServe_resetRetiresEndpointsTheStoreNoLongerHolds(t *testing.T) {
	// Given: an endpoint that is serving, with a running environment
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	if resp, _ := f.do(d, "GET", "/", nil, nil); resp.StatusCode != 200 {
		t.Fatalf("before reset = %d", resp.StatusCode)
	}

	// When: the store is wiped (POST /_overcast/reset) and the service settles
	ctx := context.Background()
	for _, ns := range []string{nsFunctions, nsRevisions, nsEndpoints, nsPolicies, nsTags} {
		keys, _ := f.svc.store.List(ctx, ns, "")
		for _, k := range keys {
			_ = f.svc.store.Delete(ctx, ns, k)
		}
	}
	if err := f.svc.DebugResetState(ctx); err != nil {
		t.Fatal(err)
	}

	// Then: the deleted endpoint no longer serves, and its environment is stopped
	if resp, _ := f.do(d, "GET", "/", nil, nil); resp.StatusCode != 404 {
		t.Errorf("after reset = %d, want 404", resp.StatusCode)
	}
	eventually(t, "the environment to stop", func() bool { return f.prov.liveEnvs() == 0 })
}

func TestLiveEnvironments_countsOneRegionsEndpoint(t *testing.T) {
	// Given: the same function and endpoint name in two Regions, with
	// different numbers of environments
	f := newDPFixture(t, dpOptions{})
	mk := func(region string, n int32) *endpointRT {
		rt := &endpointRT{region: region, function: "api", endpoint: "prod", pools: map[string]*pool{}}
		rt.envs.Store(n)
		return rt
	}
	f.svc.dp.rtMu.Lock()
	f.svc.dp.rts = map[string]*endpointRT{"a": mk("us-east-1", 2), "b": mk("eu-west-1", 3)}
	f.svc.dp.rtMu.Unlock()

	// Then: each Region's node counts its own, and "" counts both
	if got := f.svc.liveEnvironments("us-east-1", "api", "prod"); got != 2 {
		t.Errorf("us-east-1 = %d, want 2", got)
	}
	if got := f.svc.liveEnvironments("eu-west-1", "api", "prod"); got != 3 {
		t.Errorf("eu-west-1 = %d, want 3", got)
	}
	if got := f.svc.liveEnvironments("", "api", "prod"); got != 5 {
		t.Errorf("all = %d, want 5", got)
	}
}

func TestServe_aRetiredPoolStartsNothing(t *testing.T) {
	// Given: a request that resolved its pool from a table that has since
	// retired the revision
	f := newDPFixture(t, dpOptions{})
	d := f.deploy(deployOpts{})
	f.do(d, "GET", "/", nil, nil)
	tbl, err := f.svc.routes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rr := &tbl.byHost[d.id].revs[0]
	rr.pool.rt.mu.Lock()
	delete(rr.pool.rt.pools, rr.pool.revID) // retirePools did this
	rr.pool.rt.mu.Unlock()
	for _, es := range rr.pool.snapshot() { // and its environments are gone
		es.inflight.Store(es.limit)
	}
	before := f.prov.starts.Load()

	// When: it asks for an environment
	_, perr := f.svc.acquire(rr)

	// Then: it is refused and nothing was started in a pool nothing will drain
	if perr == nil || perr.Type != errTypeRevisionNotActive {
		t.Errorf("acquire = %+v", perr)
	}
	if f.prov.starts.Load() != before {
		t.Error("an environment was started in a retired pool")
	}
}

func TestServe_upgradeHeadersAreRemovedFromTheRequest(t *testing.T) {
	// Given: an application that reports the connection headers it received
	f := newDPFixture(t, dpOptions{handler: func(envSpec) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "upgrade=%q connection=%q", r.Header.Get("Upgrade"), r.Header.Values("Connection"))
		})
	}})
	d := f.deploy(deployOpts{})

	// When: a client asks to upgrade to a WebSocket
	resp, body := f.do(d, "GET", "/socket", nil, map[string]string{"Upgrade": "websocket", "Connection": "keep-alive, Upgrade"})

	// Then: Lambda strips both, as it does for every request: the application
	// answers an ordinary one
	if resp.StatusCode != 200 || strings.Contains(strings.ToLower(body), "websocket") || strings.Contains(strings.ToLower(body), "upgrade=\"upgrade") {
		t.Errorf("= %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, `upgrade=""`) {
		t.Errorf("application saw an Upgrade header: %s", body)
	}
}

func TestServe_anApplicationsCORSHeadersReplaceTheWholePreset(t *testing.T) {
	// Given: an application that sets only a credentials header
	f := newDPFixture(t, dpOptions{
		wrap: middleware.CORS,
		handler: func(envSpec) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			})
		},
	})
	d := f.deploy(deployOpts{})

	// When: a request goes through the invoke path
	resp, _ := f.do(d, "GET", "/", nil, nil)

	// Then: the preset wildcard origin is gone, since it cannot accompany credentials
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
	}
	if resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("the application's header was lost")
	}
}
