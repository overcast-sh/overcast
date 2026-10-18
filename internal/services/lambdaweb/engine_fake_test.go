package lambdaweb

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// fakeProvider is an environmentProvider whose "environments" are in-process
// HTTP servers, so the engine, proxy and scaling logic run without Docker.
type fakeProvider struct {
	mu      sync.Mutex
	starts  atomic.Int32
	stops   atomic.Int32
	specs   []envSpec
	envs    []*fakeEnv
	delay   time.Duration
	handler func(spec envSpec) http.Handler
	failure func(spec envSpec) *initFailure
}

func newFakeProvider(handler func(spec envSpec) http.Handler) *fakeProvider {
	return &fakeProvider{handler: handler}
}

func (p *fakeProvider) Start(ctx context.Context, spec envSpec) (environment, *initFailure) {
	p.starts.Add(1)
	p.mu.Lock()
	p.specs = append(p.specs, spec)
	delay, failure := p.delay, p.failure
	p.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, &initFailure{Type: errTypeInitTimeout, Detail: ctx.Err().Error()}
		}
	}
	if failure != nil {
		if f := failure(spec); f != nil {
			return nil, f
		}
	}
	srv := httptest.NewServer(p.handler(spec))
	env := &fakeEnv{p: p, srv: srv, done: make(chan struct{}), id: fmt.Sprintf("fake-%d", p.starts.Load())}
	p.mu.Lock()
	p.envs = append(p.envs, env)
	p.mu.Unlock()
	return env, nil
}

func (p *fakeProvider) liveEnvs() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, e := range p.envs {
		if !e.stopped.Load() {
			n++
		}
	}
	return n
}

type fakeEnv struct {
	p       *fakeProvider
	srv     *httptest.Server
	id      string
	done    chan struct{}
	once    sync.Once
	stopped atomic.Bool
	reason  atomic.Pointer[exitReason]
}

func (e *fakeEnv) ID() string                     { return e.id }
func (e *fakeEnv) Addr() string                   { return e.srv.Listener.Addr().String() }
func (e *fakeEnv) Done() <-chan struct{}          { return e.done }
func (e *fakeEnv) Stats(context.Context) envStats { return envStats{MaxMemoryMB: 64} }
func (e *fakeEnv) ExitReason() exitReason {
	if r := e.reason.Load(); r != nil {
		return *r
	}
	return exitReason{}
}

func (e *fakeEnv) Stop(context.Context, time.Duration) error {
	e.finish(exitReason{Requested: true})
	return nil
}

// crash simulates the process dying: connections drop and Done closes.
func (e *fakeEnv) crash(r exitReason) {
	e.srv.CloseClientConnections()
	e.finish(r)
}

func (e *fakeEnv) finish(r exitReason) {
	e.once.Do(func() {
		e.reason.Store(&r)
		e.stopped.Store(true)
		e.p.stops.Add(1)
		go e.srv.Close()
		close(e.done)
	})
}

// dpFixture is a Service wired to a fake provider behind a real HTTP server.
type dpFixture struct {
	t      testing.TB
	svc    *Service
	prov   *fakeProvider
	clk    *clock.Mock
	server *httptest.Server
	client *http.Client
}

type dpOptions struct {
	handler   func(spec envSpec) http.Handler
	maxEnvs   int
	idleTTL   time.Duration
	tweakCfg  func(*config.Config)
	noWire    bool
	authorize invokeAuth
}

func newDPFixture(t testing.TB, o dpOptions) *dpFixture {
	t.Helper()
	if o.handler == nil {
		o.handler = func(envSpec) http.Handler { return http.HandlerFunc(echoHandler) }
	}
	clk := clock.NewMock()
	cfg := &config.Config{Region: "us-east-1", AccountID: "111122223333", LambdaWebMaxEnvironments: max(o.maxEnvs, 8), LambdaWebIdleTTL: o.idleTTL}
	if o.tweakCfg != nil {
		o.tweakCfg(cfg)
	}
	svc := New(cfg, state.NewMemoryStore(), zap.NewNop(), clk)
	prov := newFakeProvider(o.handler)
	if !o.noWire {
		svc.InitProvider(prov)
	}
	if o.authorize != nil {
		svc.InitAuth(o.authorize)
	}
	router := chi.NewRouter()
	svc.RegisterRoutes(router)
	srv := httptest.NewServer(router)
	f := &dpFixture{t: t, svc: svc, prov: prov, clk: clk, server: srv, client: &http.Client{
		Timeout:       20 * time.Second,
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.Stop(ctx)
	})
	return f
}

// deployed is a created web function with one revision and one endpoint.
type deployed struct {
	function string
	domain   string
	id       string
	revision string
	arn      string
}

type deployOpts struct {
	name        string
	endpoint    string
	endpointCfg *endpointConfigIn
	revCfg      func(*revisionConfigIn)
}

func (f *dpFixture) deploy(o deployOpts) deployed {
	f.t.Helper()
	if o.name == "" {
		o.name = "app"
	}
	if o.endpoint == "" {
		o.endpoint = "prod"
	}
	rev := testRevisionConfig("code-bucket", "app.zip")
	if o.revCfg != nil {
		o.revCfg(rev)
	}
	ep := &endpointConfigIn{EndpointName: o.endpoint, EndpointType: endpointHomeRegion, AuthType: authApplicationManaged, AutoDeploymentMode: deployLatestRevision}
	if o.endpointCfg != nil {
		ep = o.endpointCfg
		ep.EndpointName = o.endpoint
	}
	out, aerr := f.svc.createWebFunctionTyped(context.Background(), &createWebFunctionRequest{FunctionName: o.name, RevisionConfig: rev, EndpointConfig: ep})
	if aerr != nil {
		f.t.Fatalf("create: %v", aerr)
	}
	f.svc.invalidateRoutes()
	// Read the endpoint back for its stored domain.
	got, aerr := f.svc.getWebFunctionEndpointTyped(context.Background(), &endpointRequest{FunctionName: o.name, EndpointName: o.endpoint})
	if aerr != nil {
		f.t.Fatalf("get endpoint: %v", aerr)
	}
	return deployed{function: o.name, domain: got.DomainName, id: domainID(got.DomainName), revision: out.Revision.RevisionID, arn: got.EndpointARN}
}

// do sends a request to the endpoint's domain.
func (f *dpFixture) do(d deployed, method, path string, body io.Reader, hdr map[string]string) (*http.Response, string) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+invokeBase+"/"+d.id+"/us-east-1"+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	req.Host = d.domain
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// echoHandler answers with what the environment received.
func echoHandler(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", r.Method, r.URL.RequestURI())
	fmt.Fprintf(&b, "host=%s\n", r.Host)
	for _, k := range []string{"Lambda-Web-Request-Id", "Lambda-Web-Endpoint-Name", "X-Forwarded-For", "X-Forwarded-Host", "Authorization", "X-Real-Ip", "Connection", "Expect", "X-Custom", "Lambda-Web-Spoofed", "X-Method-Override", "X-Amz-Security-Token"} {
		fmt.Fprintf(&b, "%s=%s\n", k, r.Header.Get(k))
	}
	w.Header().Set("X-App", "yes")
	_, _ = io.WriteString(w, b.String())
}

// rawRequest sends bytes over a fresh connection and returns the response text.
func rawRequest(t *testing.T, serverURL, request string) string {
	t.Helper()
	u := strings.TrimPrefix(serverURL, "http://")
	c, err := net.DialTimeout("tcp", u, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(c, request)
	b, _ := io.ReadAll(c)
	return string(b)
}

type configT = config.Config
