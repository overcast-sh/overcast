package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// stubQueryRouter answers RouteQuery with a fixed decision, standing in for
// the router's Query dispatch.
type stubQueryRouter struct {
	route   QueryRoute
	isQuery bool
	err     error
}

func (s stubQueryRouter) RouteQuery(http.ResponseWriter, *http.Request) (QueryRoute, bool, error) {
	return s.route, s.isQuery, s.err
}

// serveQueryScoped sends an IAM CreateUser Query call signed for s3, by a
// principal allowed s3:* only, through IAMEnforce with queries as the router's
// Query dispatch. It reports the response and whether the request was served.
func serveQueryScoped(t *testing.T, queries QueryRouter) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	st := state.NewMemoryStore()
	seedIAMUserWithPolicies(t, st, "s3-only", []string{`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`}, nil)
	served := false
	h := IAMEnforce(true, st, zap.NewNop(), queries)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusOK)
	}))
	r, _ := signedFormRequest("s3", "Action=CreateUser&Version=2010-05-08&UserName=u")
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "Credential=AKID", "Credential=s3-only", 1))
	r.Header.Set("X-Amz-Date", "20260811T000000Z")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, served
}

func TestIAMEnforce_queryRouteNamesTheAction(t *testing.T) {
	// Given: the router serves the call as IAM CreateUser
	queries := stubQueryRouter{route: QueryRoute{Service: "iam", Action: "CreateUser"}, isQuery: true}

	// When: it arrives signed for s3 from an s3-only principal
	rec, served := serveQueryScoped(t, queries)

	// Then: it is authorised as iam:CreateUser, and denied in Query XML
	if served || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "<Code>AccessDenied</Code>") {
		t.Fatalf("served=%v status=%d body=%q; want a Query XML AccessDenied", served, rec.Code, rec.Body.String())
	}
}

func TestIAMEnforce_unownedQueryRouteIsNotGated(t *testing.T) {
	// Given: the router serves no operation for the call; it answers 501 itself
	queries := stubQueryRouter{route: QueryRoute{Action: "CreateUser"}, isQuery: true}

	// When: it arrives
	_, served := serveQueryScoped(t, queries)

	// Then: there is nothing to authorise, and the router's answer stands
	if !served {
		t.Fatal("a Query call no service serves was denied; want it passed to the router")
	}
}

func TestIAMEnforce_queryFormTheRouterRefuses(t *testing.T) {
	// Given: the router cannot parse the call's form
	queries := stubQueryRouter{isQuery: true, err: &http.MaxBytesError{Limit: protocol.MaxQueryRequestBody}}

	// When: it arrives
	rec, served := serveQueryScoped(t, queries)

	// Then: it is refused as the router refuses it, not passed on unparsed
	if served || rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("served=%v status=%d; want the router's 413", served, rec.Code)
	}
}

func TestIAMEnforce_requestTheRouterDoesNotServeAsQuery(t *testing.T) {
	// Given: a router that does not dispatch the request as Query
	queries := stubQueryRouter{}

	// When: it arrives signed for s3 from an s3-only principal
	_, served := serveQueryScoped(t, queries)

	// Then: it is classified from its own content, by its s3 scope
	if !served {
		t.Fatal("want the scope-classified s3 request served")
	}
}
