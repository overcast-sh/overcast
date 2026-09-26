package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// queryService is a service that answers AWS Query requests.
type queryService interface {
	Service
	QueryDispatcher
}

// rootDispatch is the router's dispatch of the requests AWS clients send to a
// service's root: POST / by X-Amz-Target or AWS Query Action, and GET / by
// Query Action. It is the one place that decides which service serves a Query
// request and as which operation; IAM enforcement reads that decision through
// RouteQuery, so the operation it authorises is the one served (#2229).
//
// The slices are filled in by the service registration loop after the
// middleware that reads them is built, and read at request time.
type rootDispatch struct {
	registry *awsapi.Registry
	targets  []TargetDispatcher
	queries  []queryService
}

// queryResolution is how the router serves an AWS Query request: the service that
// owns its Action, nil when no enabled service does, and the Version and
// Action it was resolved by.
type queryResolution struct {
	owner   queryService
	version string
	action  string
}

// targetDispatch answers POST /: by X-Amz-Target when a service or the models
// claim the target, then as AWS Query, and otherwise with an error in the
// envelope the caller's content type expects.
func (d *rootDispatch) targetDispatch(w http.ResponseWriter, r *http.Request) {
	if h := d.targetHandler(r); h != nil {
		h(w, r)
		return
	}
	if route, isQuery, err := d.route(w, r); isQuery {
		d.serveQuery(w, r, route, err)
		return
	}
	// No match. JSON-target requests expect a JSON error; sending one to an
	// XML-expecting SDK causes a parse error ("char '{' is not expected").
	if r.Body != nil {
		io.Copy(io.Discard, r.Body) //nolint:errcheck
		r.Body.Close()              //nolint:errcheck
	}
	body, _ := json.Marshal(struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}{Type: "UnknownOperationException", Message: "Unknown target: " + r.Header.Get("X-Amz-Target")})
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusBadRequest)
	w.Write(body) //nolint:errcheck
}

// queryGetMiddleware intercepts GET / requests carrying an AWS Query Action
// parameter (e.g. SNS UnsubscribeURL) and lets S3's GET / handle everything
// else. It is middleware because chi requires middleware to be registered
// before any route, and S3 owns the GET / route.
func (d *rootDispatch) queryGetMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if route, isQuery, err := d.route(w, r); isQuery {
				d.serveQuery(w, r, route, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RouteQuery satisfies middleware.QueryRouter with the resolution the router
// serves the request by.
func (d *rootDispatch) RouteQuery(w http.ResponseWriter, r *http.Request) (middleware.QueryRoute, bool, error) {
	route, isQuery, err := d.route(w, r)
	served := middleware.QueryRoute{Action: route.action}
	if route.owner != nil {
		served.Service = route.owner.Name()
	}
	return served, isQuery, err
}

// targetHandler is the handler POST / answers r's X-Amz-Target with, or nil
// when nothing claims the target.
func (d *rootDispatch) targetHandler(r *http.Request) http.HandlerFunc {
	target := r.Header.Get("X-Amz-Target")
	for _, td := range d.targets {
		if strings.HasPrefix(target, td.TargetPrefix()) {
			return td.Dispatch
		}
	}
	if target == "" {
		return nil
	}
	if claim, ok := d.registry.ClaimTarget(target); ok {
		return func(w http.ResponseWriter, r *http.Request) { writeNotImplemented(w, r, claim) }
	}
	return nil
}

// route resolves r as AWS Query traffic. isQuery is false when the router
// does not dispatch r as Query, and err is the failure the router refuses a
// Query form with. IAM enforcement resolves a request before the router serves
// it, so a success must resolve the same way twice, and it does: the form is
// cached on r. A failure is not repeatable — net/http leaves the partial form
// set — so a caller that gets err must refuse the request, as IAMEnforce and
// serveQuery do, rather than pass it on.
func (d *rootDispatch) route(w http.ResponseWriter, r *http.Request) (route queryResolution, isQuery bool, err error) {
	if !d.addressesQuery(r) {
		return queryResolution{}, false, nil
	}
	if err := parseQueryForm(w, r); err != nil {
		return queryResolution{}, true, err
	}
	version, action := r.FormValue("Version"), r.FormValue("Action")
	return queryResolution{owner: d.owner(version, action), version: version, action: action}, true, nil
}

// addressesQuery reports whether the router dispatches r as AWS Query: a
// form-encoded POST / that no X-Amz-Target claims, or a GET / whose query
// string parses and carries an Action. A GET's query string is checked here,
// before any parse, because a failed parse is not repeatable — it leaves
// r.Form set, and a second resolution would then call the request Query.
func (d *rootDispatch) addressesQuery(r *http.Request) bool {
	if r.URL.Path != "/" {
		return false
	}
	switch r.Method {
	case http.MethodGet:
		query, err := url.ParseQuery(r.URL.RawQuery)
		return err == nil && query.Get("Action") != ""
	case http.MethodPost:
		return d.targetHandler(r) == nil &&
			strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded")
	}
	return false
}

// parseQueryForm is the one parse that decides the operation for every Query
// service. It is cached on r.Form, so resolving a request twice reads the body
// once. A body too large to parse is refused with the error that says so, not
// the 501 that falling past every dispatch branch would give it: that names a
// feature gap when the problem is the body. A GET's form is its query string,
// which addressesQuery has already parsed.
func parseQueryForm(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		return r.ParseForm()
	}
	return protocol.ParseQueryForm(w, r)
}

// owner is the service that serves a Query (Version, Action) pair: the one
// that owns it, else — for a pair the models do not claim — the first service
// that declares no ownership at all.
func (d *rootDispatch) owner(version, action string) queryService {
	if qs, ok := queryOwner(d.registry, d.queries, version, action); ok {
		return qs
	}
	if _, claimed := d.registry.ClaimQuery(version, action); claimed {
		return nil
	}
	for _, qs := range d.queries {
		_, isActionOwner := qs.(QueryActionOwner)
		_, isVersionOwner := qs.(QueryVersionOwner)
		if !isActionOwner && !isVersionOwner {
			return qs
		}
	}
	return nil
}

// serveQuery answers a resolved Query request.
//
// The refusal of an unparseable form is written in the generic Query envelope
// even for a service that uses EC2's: the Action naming the service was never
// read.
func (d *rootDispatch) serveQuery(w http.ResponseWriter, r *http.Request, route queryResolution, err error) {
	if err != nil {
		protocol.WriteQueryXMLError(w, r, protocol.QueryFormParseError(err))
		return
	}
	if route.owner != nil {
		route.owner.DispatchQuery(w, r)
		return
	}
	if claim, ok := d.registry.ClaimQuery(route.version, route.action); ok {
		writeNotImplemented(w, r, claim)
		return
	}
	// A root request carrying Action is AWS Query traffic, not S3. Query XML
	// keeps an unimplemented AWS command from becoming S3's ListBuckets.
	protocol.NotImplementedQueryXML(w, r)
}

// queryOwner returns the service that explicitly owns an AWS Query request.
// API version wins over action name because actions can be shared by services.
//
// The second pass matches on the action name alone, and AWS reuses names freely
// across services. Elastic Load Balancing Classic (2012-06-01) and ELBv2
// (2015-12-01) share the whole vocabulary a load balancer needs —
// DescribeTags, DescribeLoadBalancers, CreateLoadBalancer,
// DescribeLoadBalancerAttributes — and Overcast implements only v2, so every
// Classic call fell into the action pass and was answered by ELBv2's handler:
// a 200 in the 2015-12-01 namespace, or a 400 naming an ELBv2 member the
// Classic caller never sent (#1884). Neither is an answer to the question that
// was asked, and both hide the 501 an unimplemented service owes it.
//
// So the models decide first. queryVersionService resolves (Version, Action) to
// exactly one service, which is the same fact each QueryVersionOwner states
// about itself, read from the one place both can share.
//
// The guard only ever withdraws a claim, never grants one. Where the models can
// attribute nothing — no Version at all, a version they do not carry, or a pair
// several modeled services declare — it names no service and the action pass
// decides exactly as it did before.
func queryOwner(operationRegistry *awsapi.Registry, services []queryService, version, action string) (queryService, bool) {
	for _, qs := range services {
		if owner, ok := qs.(QueryVersionOwner); ok && owner.OwnsVersion(version) {
			return qs, true
		}
	}
	modeled := queryVersionService(operationRegistry, version, action)
	for _, qs := range services {
		owner, ok := qs.(QueryActionOwner)
		if !ok || !owner.OwnsAction(action) {
			continue
		}
		if modeled != "" && qs.Name() != modeled {
			continue
		}
		return qs, true
	}
	return nil, false
}

// queryVersionService names the Overcast service the pinned models attribute an
// AWS Query (Version, Action) pair to, or "" when they cannot attribute it —
// which Claim.Service already spells as the empty string for a pair that
// several modeled services declare.
func queryVersionService(operationRegistry *awsapi.Registry, version, action string) string {
	claim, ok := operationRegistry.ClaimQuery(version, action)
	if !ok {
		return ""
	}
	return claim.Service
}
