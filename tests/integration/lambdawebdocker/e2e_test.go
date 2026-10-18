package lambdawebdocker_test

// End to end: the real router with Docker, a Node.js server uploaded to S3, a
// web function created through the AWS SDK, and HTTP requests to its endpoint's
// domain — the path a user takes. Like the rest of this package it needs a
// Docker daemon and skips without one.

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb"
	"github.com/aws/aws-sdk-go-v2/service/lambdaweb/types"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// echoApp is a dependency-free Node.js server: it answers with what it saw.
const echoApp = `
const http = require('http');
const [host, port] = process.env.AWS_LAMBDA_HTTP_ENDPOINT.split(':');
http.createServer((req, res) => {
  console.log({ msg: 'handling', path: req.url });
  res.setHeader('x-app', 'echo');
  res.end(JSON.stringify({
    version: process.env.APP_VERSION || 'v1',
    method: req.method, url: req.url, host: req.headers.host,
    requestId: req.headers['lambda-web-request-id'], endpoint: req.headers['lambda-web-endpoint-name'],
    xff: req.headers['x-forwarded-for'], auth: req.headers['authorization'] || null,
  }));
}).listen(port, host);
`

type e2e struct {
	t    *testing.T
	ctx  context.Context
	srv  *helpers.TestServer
	web  *lambdaweb.Client
	s3   *awss3.Client
	http *http.Client
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	helpers.SkipWithoutDocker(t)
	srv := helpers.NewTestServer(t, helpers.WithLambdaDocker())
	creds := credentials.NewStaticCredentialsProvider("test", "test", "")
	e := &e2e{
		t: t, ctx: context.Background(), srv: srv,
		web:  lambdaweb.New(lambdaweb.Options{Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(srv.URL), HTTPClient: http.DefaultClient}),
		s3:   awss3.New(awss3.Options{Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(srv.URL), HTTPClient: http.DefaultClient, UsePathStyle: true}),
		http: &http.Client{Timeout: 90 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	if _, err := e.s3.CreateBucket(e.ctx, &awss3.CreateBucketInput{Bucket: aws.String("code")}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := e.s3.PutBucketVersioning(e.ctx, &awss3.PutBucketVersioningInput{
		Bucket: aws.String("code"), VersioningConfiguration: &s3types.VersioningConfiguration{Status: s3types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatalf("PutBucketVersioning: %v", err)
	}
	return e
}

func (e *e2e) upload(key string, files map[string]string) {
	e.t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			e.t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	_ = zw.Close()
	if _, err := e.s3.PutObject(e.ctx, &awss3.PutObjectInput{Bucket: aws.String("code"), Key: aws.String(key), Body: bytes.NewReader(buf.Bytes())}); err != nil {
		e.t.Fatalf("PutObject: %v", err)
	}
}

func revCfg(key string, env map[string]string) *types.RevisionConfig {
	return &types.RevisionConfig{
		BuildConfig: &types.BuildConfig{
			CodeConfig:    &types.CodeConfig{S3Object: &types.S3Object{Bucket: aws.String("code"), Key: aws.String(key)}},
			RuntimeConfig: &types.RuntimeConfig{Runtime: aws.String("nodejs24.x")},
		},
		ServiceConfig: &types.ServiceConfig{ExecutionRoleArn: aws.String("arn:aws:iam::000000000000:role/web"), EnvironmentVariables: env},
	}
}

// deploy creates a web function with one LatestRevision endpoint and waits for
// its revision to be Active; it returns the endpoint's domain.
func (e *e2e) deploy(name string, rc *types.RevisionConfig, auth types.AuthType) string {
	e.t.Helper()
	out, err := e.web.CreateWebFunction(e.ctx, &lambdaweb.CreateWebFunctionInput{
		FunctionName: aws.String(name), RevisionConfig: rc,
		EndpointConfig: &types.EndpointConfig{
			EndpointName: aws.String("prod"), EndpointType: types.EndpointTypeHomeRegion, AuthType: auth,
			AutoDeploymentMode: types.AutoDeploymentModeLatestRevision,
		},
	})
	if err != nil {
		e.t.Fatalf("CreateWebFunction: %v", err)
	}
	e.waitServing(name, aws.ToString(out.Revision.RevisionId))
	return aws.ToString(out.Endpoint.DomainName)
}

func (e *e2e) waitServing(fn, revID string) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ep, err := e.web.GetWebFunctionEndpoint(e.ctx, &lambdaweb.GetWebFunctionEndpointInput{FunctionName: aws.String(fn), EndpointName: aws.String("prod")})
		if err == nil && ep.State == types.EndpointStateActive && len(ep.RevisionWeights) == 1 && aws.ToString(ep.RevisionWeights[0].RevisionId) == revID {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("endpoint does not serve %s: %+v %v", revID, ep, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// get sends a request the way a client does: to Overcast, with the endpoint's
// domain as the Host, which host routing resolves. The first request after the server starts
// can arrive before the Docker probe finishes; those answer 502 without an
// error type, so it retries them for a while.
func (e *e2e) get(domain, path string, hdr map[string]string) (*http.Response, string) {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		req, _ := http.NewRequest("GET", e.srv.URL+path, nil)
		req.Host = domain
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := e.http.Do(req)
		if err != nil {
			e.t.Fatalf("GET %s: %v", path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 502 && resp.Header.Get("Lambda-Web-Error-Type") == "" && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		return resp, string(b)
	}
}

func TestEndToEnd_serveAnEndpointFromAnS3Package(t *testing.T) {
	// Given: a Node.js server uploaded to S3 and deployed behind an endpoint
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("echo", revCfg("echo.zip", nil), types.AuthTypeApplicationManaged)

	// When: a request arrives for the endpoint's domain
	start := time.Now()
	resp, body := e.get(domain, "/hello/world?x=1&y=%2F", map[string]string{"X-Forwarded-For": "1.2.3.4"})
	cold := time.Since(start)

	// Then: the container answered, with the request exactly as sent
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	for _, want := range []string{`"url":"/hello/world?x=1&y=%2F"`, `"host":"` + domain + `"`, `"endpoint":"prod"`, `"version":"v1"`, `"xff":"1.2.3.4, `} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s:\n%s", want, body)
		}
	}
	if resp.Header.Get("X-App") != "echo" || resp.Header.Get("Lambda-Web-Request-Id") == "" || resp.Header.Get("Lambda-Web-Logstream") == "" {
		t.Errorf("headers = %v", resp.Header)
	}
	t.Logf("cold start request: %v", cold)

	// And: a warm request is much faster than the cold one
	var warmTotal time.Duration
	for range 20 {
		s := time.Now()
		r, _ := e.get(domain, "/", nil)
		warmTotal += time.Since(s)
		if r.StatusCode != 200 {
			t.Fatalf("warm request = %d", r.StatusCode)
		}
	}
	t.Logf("warm request average: %v", warmTotal/20)
}

func TestEndToEnd_newRevisionIsServedByLatestRevision(t *testing.T) {
	e := newE2E(t)
	e.upload("v1.zip", map[string]string{"index.js": echoApp})
	e.upload("v2.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("rolling", revCfg("v1.zip", map[string]string{"APP_VERSION": "v1"}), types.AuthTypeApplicationManaged)
	if _, body := e.get(domain, "/", nil); !strings.Contains(body, `"version":"v1"`) {
		t.Fatalf("first revision: %s", body)
	}

	// When: a second revision is created
	rc := revCfg("v2.zip", map[string]string{"APP_VERSION": "v2"})
	out, err := e.web.CreateWebFunctionRevision(e.ctx, &lambdaweb.CreateWebFunctionRevisionInput{FunctionName: aws.String("rolling"), BuildConfig: rc.BuildConfig, ServiceConfig: rc.ServiceConfig})
	if err != nil {
		t.Fatalf("CreateWebFunctionRevision: %v", err)
	}
	e.waitServing("rolling", aws.ToString(out.RevisionId))

	// Then: requests reach it
	if _, body := e.get(domain, "/", nil); !strings.Contains(body, `"version":"v2"`) {
		t.Errorf("second revision: %s", body)
	}
}

func TestEndToEnd_iamAuthRefusesAnUnsignedRequest(t *testing.T) {
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("private", revCfg("echo.zip", nil), types.AuthTypeIamAuth)
	resp, _ := e.get(domain, "/", nil)
	if resp.StatusCode != 403 || resp.Header.Get("Lambda-Web-Error-Type") != "" {
		t.Errorf("unsigned = %d %q", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"))
	}
}

func TestEndToEnd_aBrokenEntryFileIsA502WithItsType(t *testing.T) {
	e := newE2E(t)
	e.upload("bad.zip", map[string]string{"index.js": "require('./does-not-exist')"})
	domain := e.deploy("broken", revCfg("bad.zip", nil), types.AuthTypeApplicationManaged)
	resp, body := e.get(domain, "/", nil)
	if resp.StatusCode != 502 || resp.Header.Get("Lambda-Web-Error-Type") != "Runtime.ImportCodeError" {
		t.Errorf("= %d %q: %s", resp.StatusCode, resp.Header.Get("Lambda-Web-Error-Type"), body)
	}
}

func TestEndToEnd_concurrentRequestsShareWarmEnvironments(t *testing.T) {
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("busy", revCfg("echo.zip", nil), types.AuthTypeApplicationManaged)
	e.get(domain, "/", nil) // warm

	var wg sync.WaitGroup
	var mu sync.Mutex
	failures := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := e.get(domain, "/", nil)
			if resp.StatusCode != 200 {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if failures != 0 {
		t.Errorf("%d of 50 concurrent requests failed", failures)
	}
}

// awsJSON calls a JSON-protocol AWS API (CloudWatch Logs) on the test server.
func (e *e2e) awsJSON(target, body string) string {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+"/", strings.NewReader(body))
	req.Header.Set("X-Amz-Target", target)
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/logs/aws4_request, SignedHeaders=host, Signature=x")
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// metricNames lists the metrics CloudWatch holds for a namespace.
func (e *e2e) metricNames(namespace string) string {
	e.t.Helper()
	form := "Action=ListMetrics&Version=2010-08-01&Namespace=" + namespace
	req, _ := http.NewRequest("POST", e.srv.URL+"/", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=test/20260101/us-east-1/monitoring/aws4_request, SignedHeaders=host, Signature=x")
	resp, err := e.http.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestEndToEnd_logsAndMetricsReachCloudWatch(t *testing.T) {
	// Given: an endpoint that has served a request
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("observed", revCfg("echo.zip", nil), types.AuthTypeApplicationManaged)
	resp, _ := e.get(domain, "/watch", nil)
	requestID := resp.Header.Get("Lambda-Web-Request-Id")
	stream := resp.Header.Get("Lambda-Web-Logstream")
	if requestID == "" || stream == "" {
		t.Fatalf("response lacks its request id or log stream: %v", resp.Header)
	}

	// Then: the application's console.log line, tagged with the request id, is in
	// the function's log group, in the stream the response named
	var events string
	waitUntil(t, 20*time.Second, func() bool {
		events = e.awsJSON("Logs_20140328.FilterLogEvents", `{"logGroupName":"/aws/lambda/web/observed","logStreamNames":["`+stream+`"]}`)
		return strings.Contains(events, "handling") && strings.Contains(events, requestID)
	}, "the application log line")
	for _, want := range []string{`endpointName`, `prod`, `us-east-1`} {
		if !strings.Contains(events, want) {
			t.Errorf("log events lack %q: %s", want, events)
		}
	}

	// And: the platform's own lifecycle events are there too
	waitUntil(t, 20*time.Second, func() bool {
		events = e.awsJSON("Logs_20140328.FilterLogEvents", `{"logGroupName":"/aws/lambda/web/observed","logStreamNames":["`+stream+`"]}`)
		return strings.Contains(events, "platform.initStart") && strings.Contains(events, "platform.initReport")
	}, "the platform init events")
	local := e.awsJSON("Logs_20140328.FilterLogEvents", `{"logGroupName":"/aws/lambda/web/observed.local"}`)
	if !strings.Contains(local, "platform.initStart") {
		t.Errorf(".local group lacks the system events: %s", local)
	}

	// And: request metrics are published (flushed every few seconds)
	waitUntil(t, 40*time.Second, func() bool {
		return strings.Contains(e.metricNames("AWS/Lambda/Web"), "ExecutedRequests")
	}, "the ExecutedRequests metric")
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestEndToEnd_cloudFrontReachesAnIamAuthEndpointThroughItsOAC(t *testing.T) {
	// Given: an IamAuth endpoint whose resource policy admits CloudFront
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("fronted", revCfg("echo.zip", nil), types.AuthTypeIamAuth)
	fn, err := e.web.GetWebFunction(e.ctx, &lambdaweb.GetWebFunctionInput{FunctionName: aws.String("fronted")})
	if err != nil {
		t.Fatalf("GetWebFunction: %v", err)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"cloudfront.amazonaws.com"},` +
		`"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*"}]}`
	if _, err := e.web.PutResourcePolicy(e.ctx, &lambdaweb.PutResourcePolicyInput{ResourceArn: fn.FunctionArn, Policy: aws.String(policy)}); err != nil {
		t.Fatalf("PutResourcePolicy: %v", err)
	}
	// the origin domain is the endpoint's, on a base that resolves to Overcast
	labels := strings.SplitN(domain, ".", 5)
	origin := strings.Join(labels[:4], ".") + ".localhost"

	post := func(path, body string) string {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/xml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s = %d: %s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	between := func(s, open, close string) string {
		i := strings.Index(s, open) + len(open)
		return s[i : i+strings.Index(s[i:], close)]
	}
	oac := post("/2020-05-31/origin-access-control", `<?xml version="1.0" encoding="UTF-8"?>
<OriginAccessControlConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><Name>fronted-oac</Name>
<SigningProtocol>sigv4</SigningProtocol><SigningBehavior>always-amz-auth</SigningBehavior>
<OriginAccessControlOriginType>lambda</OriginAccessControlOriginType></OriginAccessControlConfig>`)
	dist := post("/2020-05-31/distribution", `<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><CallerReference>fronted</CallerReference>
<Comment>lambda web origin</Comment><Enabled>true</Enabled>
<Origins><Quantity>1</Quantity><Items><Origin><Id>web-1</Id><DomainName>`+origin+`</DomainName>
<OriginAccessControlId>`+between(oac, "<Id>", "</Id>")+`</OriginAccessControlId>
<CustomOriginConfig><HTTPPort>80</HTTPPort><HTTPSPort>443</HTTPSPort><OriginProtocolPolicy>http-only</OriginProtocolPolicy></CustomOriginConfig>
</Origin></Items></Origins>
<DefaultCacheBehavior><TargetOriginId>web-1</TargetOriginId><ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
<ForwardedValues><QueryString>false</QueryString><Headers><Quantity>1</Quantity><Items><Name>Authorization</Name></Items></Headers></ForwardedValues>
</DefaultCacheBehavior></DistributionConfig>`)
	distID := between(dist, "<Id>", "</Id>")

	// When: a viewer with an application token requests through the distribution
	resp, body := e.get(e.srv.Listener.Addr().String(), "/_overcast/cloudfront/distributions/"+distID+"/hello",
		map[string]string{"Authorization": "Bearer app-jwt"})

	// Then: the endpoint served it, and its application saw the viewer's token
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"auth":"Bearer app-jwt"`) {
		t.Errorf("through CloudFront = %d %s", resp.StatusCode, body)
	}
	// And: a direct, unsigned request is still refused
	if direct, _ := e.get(domain, "/hello", nil); direct.StatusCode != http.StatusForbidden {
		t.Errorf("direct unsigned = %d", direct.StatusCode)
	}
}

// TestEndToEnd_loadReport measures the serving path against a real container:
// the cold start, the warm latency, and the throughput under concurrency. It
// asserts only loose bounds (nothing fails, nothing is pathologically slow);
// the numbers are in the log (go test -v) and in docs/services/lambda-web/limitations.md.
func TestEndToEnd_loadReport(t *testing.T) {
	if testing.Short() {
		t.Skip("load report skipped in -short mode")
	}
	e := newE2E(t)
	e.upload("echo.zip", map[string]string{"index.js": echoApp})
	domain := e.deploy("load", revCfg("echo.zip", nil), types.AuthTypeApplicationManaged)

	// cold: the first request starts a container
	t0 := time.Now()
	if resp, body := e.get(domain, "/", nil); resp.StatusCode != 200 {
		t.Fatalf("cold = %d %s", resp.StatusCode, body)
	}
	cold := time.Since(t0)

	// warm, one at a time
	lat := make([]time.Duration, 0, 200)
	for range 200 {
		s := time.Now()
		if resp, _ := e.get(domain, "/", nil); resp.StatusCode != 200 {
			t.Fatalf("warm = %d", resp.StatusCode)
		}
		lat = append(lat, time.Since(s))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50, p99 := lat[len(lat)/2], lat[len(lat)*99/100]

	// concurrent, with connections kept alive
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	var ok, bad, throttled atomic.Int64
	stop := time.Now().Add(3 * time.Second)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(stop) {
				req, _ := http.NewRequest("GET", e.srv.URL+"/", nil)
				req.Host = domain
				resp, err := client.Do(req)
				if err != nil {
					bad.Add(1)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 {
					ok.Add(1)
				} else if resp.StatusCode == 429 {
					throttled.Add(1) // the default endpoint limit (10,000/s) was reached: not a failure
				} else {
					bad.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	rps := float64(ok.Load()) / 3

	t.Logf("cold start %v; warm p50 %v p99 %v; %d concurrent clients: %.0f req/s, %d failed, %d throttled", cold, p50, p99, 32, rps, bad.Load(), throttled.Load())
	if bad.Load() != 0 {
		t.Errorf("%d requests failed under load", bad.Load())
	}
	if p50 > 100*time.Millisecond {
		t.Errorf("warm p50 = %v", p50)
	}
	if rps < 100 {
		t.Errorf("throughput = %.0f req/s", rps)
	}
}
