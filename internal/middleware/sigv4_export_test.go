package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/state"
)

func signedLambdaWebRequest(t *testing.T, now time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/orders/1?x=1", nil)
	req.Host = "abc.s1.lambda-web.us-east-1.localhost:4566"
	signHeaderRequest(t, req, now, "us-east-1", "lambda", []string{"host", "x-amz-content-sha256", "x-amz-date"})
	PrefixPath(req, "/_overcast/lambdaweb/invoke/abc.s1/us-east-1")
	return req
}

func TestValidateSigV4_canonicalPathOverride(t *testing.T) {
	// Given: a request signed for /orders/1, then rewritten under the invoke
	// prefix as HostAddressing does
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	clk := clock.NewMock()
	clk.Set(now)
	req := signedLambdaWebRequest(t, now)

	// When: it is validated against the rewritten path and against the original
	_, rewritten := ValidateSigV4(req, clk, nil, "")
	signed, original := ValidateSigV4(req, clk, nil, "/orders/1")

	// Then: only the original path verifies
	if rewritten == nil {
		t.Fatal("the rewritten path verified")
	}
	if !signed || original != nil {
		t.Fatalf("original path: signed=%v err=%v", signed, original)
	}
}

func TestSigV4_leavesLambdaWebInvokeRequestsToTheDataPlane(t *testing.T) {
	// Given: validation on and a lambda-web request whose path was rewritten
	// after signing, so the middleware's own check would fail it
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	clk := clock.NewMock()
	clk.Set(now)
	var accessKey string
	h := SigV4(true, nil, zap.NewNop(), clk)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accessKey = SigV4AccessKeyFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()

	// When: the request is handled
	h.ServeHTTP(rec, signedLambdaWebRequest(t, now))

	// Then: it passes through with its access key stamped
	if rec.Code != http.StatusNoContent || accessKey != "test" {
		t.Fatalf("status %d, access key %q", rec.Code, accessKey)
	}
}

func TestIAMPrincipalResolver_resolvesUsersAndCachesUnknownKeys(t *testing.T) {
	// Given: a user with an inline policy
	st := state.NewMemoryStore()
	rec, _ := json.Marshal(map[string]any{
		"UserName": "alice", "Arn": "arn:aws:iam::111122223333:user/alice",
		"AccessKeys":     []map[string]string{{"AccessKeyId": "AKIAALICE", "SecretAccessKey": "s"}},
		"InlinePolicies": map[string]string{"p": `{"Statement":[{"Effect":"Allow","Action":"lambda:*","Resource":"*"}]}`},
	})
	if err := st.Set(context.Background(), iamUsersNamespace, "alice", string(rec)); err != nil {
		t.Fatal(err)
	}
	r := NewIAMPrincipalResolver(st)

	// When: a known and an unknown key are resolved
	known := r.Resolve(context.Background(), "AKIAALICE")
	unknown := r.Resolve(context.Background(), "AKIANOBODY")

	// Then: the user's identity and compiled statements come back; the other key is unknown
	if !known.Known || known.ARN != "arn:aws:iam::111122223333:user/alice" || known.Account != "111122223333" ||
		len(known.Statements) != 1 || known.Statements[0].Effect != "Allow" || known.Err != nil {
		t.Fatalf("known = %+v", known)
	}
	if unknown.Known || len(unknown.Statements) != 0 {
		t.Fatalf("unknown = %+v", unknown)
	}
}

func TestIAMPrincipalResolver_invalidatedByIAMMutation(t *testing.T) {
	// Given: a resolved user whose policy then changes
	st := state.NewMemoryStore()
	put := func(action string) {
		rec, _ := json.Marshal(map[string]any{
			"UserName": "bob", "Arn": "arn:aws:iam::111122223333:user/bob",
			"AccessKeys":     []map[string]string{{"AccessKeyId": "AKIABOB", "SecretAccessKey": "s"}},
			"InlinePolicies": map[string]string{"p": `{"Statement":[{"Effect":"Allow","Action":"` + action + `","Resource":"*"}]}`},
		})
		_ = st.Set(context.Background(), iamUsersNamespace, "bob", string(rec))
	}
	put("s3:*")
	r := NewIAMPrincipalResolver(st)
	before := r.Resolve(context.Background(), "AKIABOB")

	// When: the IAM service invalidates the enforcement caches
	put("lambda:*")
	InvalidateIAMEnforceCache()
	after := r.Resolve(context.Background(), "AKIABOB")

	// Then: the new policy is read
	if got := func(s []iampolicy.Statement) string { return s[0].Action[0].String() }; got(before.Statements) != "s3:*" || got(after.Statements) != "lambda:*" {
		t.Fatalf("before %q after %q", got(before.Statements), got(after.Statements))
	}
}

func TestIAMPrincipalResolver_nilStoreResolvesNothing(t *testing.T) {
	// Given / When: a resolver without a store, and a nil resolver
	a := NewIAMPrincipalResolver(nil).Resolve(context.Background(), "AKIA")
	b := (*IAMPrincipalResolver)(nil).Resolve(context.Background(), "AKIA")

	// Then: neither knows the key
	if a.Known || b.Known {
		t.Fatal("a store-less resolver knew a key")
	}
}
