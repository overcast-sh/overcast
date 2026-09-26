package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/middleware"
)

// ownedQueryService owns a fixed set of Query actions and records the requests
// it serves.
type ownedQueryService struct {
	name    string
	actions map[string]bool
	served  *string
}

func (f ownedQueryService) Name() string                  { return f.name }
func (f ownedQueryService) RegisterRoutes(chi.Router)     {}
func (f ownedQueryService) OwnsAction(action string) bool { return f.actions[action] }
func (f ownedQueryService) DispatchQuery(w http.ResponseWriter, r *http.Request) {
	*f.served = f.name + ":" + r.FormValue("Action")
	w.WriteHeader(http.StatusOK)
}

// recordingTargetService serves one X-Amz-Target prefix.
type recordingTargetService struct{ served *string }

func (f recordingTargetService) TargetPrefix() string { return "AmazonSQS." }
func (f recordingTargetService) Dispatch(w http.ResponseWriter, _ *http.Request) {
	*f.served = "target"
	w.WriteHeader(http.StatusOK)
}

// TestRootDispatch_routeQueryIsWhatIsServed pins the invariant IAM enforcement
// relies on (#2229): RouteQuery names the service and Action the router then
// serves a request as, and says a request is not Query exactly when the router
// does not serve it as Query.
func TestRootDispatch_routeQueryIsWhatIsServed(t *testing.T) {
	form := func(method, target, body string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	withTarget := func(r *http.Request, target string) *http.Request {
		r.Header.Set("X-Amz-Target", target)
		return r
	}
	for _, tc := range []struct {
		name      string
		req       *http.Request
		wantQuery bool
		want      middleware.QueryRoute
		// wantServed is what answers the request; "" is the router itself.
		wantServed string
	}{
		{"POST versioned", form(http.MethodPost, "/", "Action=CreateUser&Version=2010-05-08"), true, middleware.QueryRoute{Service: "iam", Action: "CreateUser"}, "iam:CreateUser"},
		{"POST unversioned", form(http.MethodPost, "/", "Action=CreateUser"), true, middleware.QueryRoute{Service: "iam", Action: "CreateUser"}, "iam:CreateUser"},
		{"POST action after a large parameter", form(http.MethodPost, "/", "Pad="+strings.Repeat("a", 4096)+"&Action=CreateUser"), true, middleware.QueryRoute{Service: "iam", Action: "CreateUser"}, "iam:CreateUser"},
		{"POST body action over the query string's", form(http.MethodPost, "/?Action=ListQueues", "Action=CreateUser"), true, middleware.QueryRoute{Service: "iam", Action: "CreateUser"}, "iam:CreateUser"},
		{"POST under an unclaimed target", withTarget(form(http.MethodPost, "/", "Action=CreateUser"), "Bogus_20990101.Nothing"), true, middleware.QueryRoute{Service: "iam", Action: "CreateUser"}, "iam:CreateUser"},
		{"POST under a served target", withTarget(form(http.MethodPost, "/", "Action=CreateUser"), "AmazonSQS.ListQueues"), false, middleware.QueryRoute{}, "target"},
		{"POST no service owns", form(http.MethodPost, "/", "Action=NoSuchThing"), true, middleware.QueryRoute{Action: "NoSuchThing"}, ""},
		{"GET", httptest.NewRequest(http.MethodGet, "/?Action=ListQueues&Version=2012-11-05", nil), true, middleware.QueryRoute{Service: "sqs", Action: "ListQueues"}, "sqs:ListQueues"},
		{"GET without Action", httptest.NewRequest(http.MethodGet, "/?list-type=2", nil), false, middleware.QueryRoute{}, "next"},
		{"GET with an unparseable query", httptest.NewRequest(http.MethodGet, "/?Action=ListQueues&x=%zz", nil), false, middleware.QueryRoute{}, "next"},
		{"POST off the root", form(http.MethodPost, "/bucket/key", "Action=CreateUser"), false, middleware.QueryRoute{}, "next"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a router whose IAM owns CreateUser and SQS ListQueues
			var served string
			d := &rootDispatch{
				registry: awsapi.NewRegistry(),
				targets:  []TargetDispatcher{recordingTargetService{&served}},
				queries: []queryService{
					ownedQueryService{"sqs", map[string]bool{"ListQueues": true}, &served},
					ownedQueryService{"iam", map[string]bool{"CreateUser": true}, &served},
				},
			}

			// When: IAM enforcement asks how the request is served, then the
			// router serves it
			route, isQuery, err := d.RouteQuery(httptest.NewRecorder(), tc.req)
			serve(d, tc.req, &served)

			// Then: the two agree
			if err != nil || isQuery != tc.wantQuery || route != tc.want {
				t.Errorf("RouteQuery = %+v, %v, %v; want %+v, %v", route, isQuery, err, tc.want, tc.wantQuery)
			}
			if served != tc.wantServed {
				t.Errorf("served by %q, want %q", served, tc.wantServed)
			}
		})
	}
}

// serve sends r through the root router's two Query entry points the way the
// router mounts them: the GET middleware ahead of every route, and POST /.
func serve(d *rootDispatch, r *http.Request, served *string) {
	mux := chi.NewRouter()
	mux.Use(d.queryGetMiddleware)
	mux.Post("/", d.targetDispatch)
	mux.NotFound(func(w http.ResponseWriter, _ *http.Request) { *served = "next" })
	mux.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) { *served = "next" })
	mux.Get("/", func(w http.ResponseWriter, _ *http.Request) { *served = "next" })
	mux.ServeHTTP(httptest.NewRecorder(), r)
}

func TestRootDispatch_routeQueryRefusesWhatTheRouterRefuses(t *testing.T) {
	// Given: a form too large for the router to parse
	body := "Action=CreateUser&Pad=" + strings.Repeat("a", 11<<20)
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	d := &rootDispatch{registry: awsapi.NewRegistry()}

	// When: IAM enforcement asks how it is served
	_, isQuery, err := d.RouteQuery(httptest.NewRecorder(), r)

	// Then: it is Query traffic the router refuses, with the reason why
	if !isQuery || err == nil {
		t.Fatalf("RouteQuery = isQuery %v, err %v; want a Query request and its parse error", isQuery, err)
	}
}
