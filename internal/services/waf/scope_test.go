package waf

import (
	"context"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// The typed path serves RPC v2 CBOR callers, so it needs the same Scope enum
// check the legacy JSON path has; these run against it directly.

func newScopeTestHandler(t *testing.T) *Handler {
	t.Helper()
	return newHandler(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, state.NewMemoryStore(), clock.New())
}

func TestValidateScope(t *testing.T) {
	// Given: values on and off WAFv2's Scope enum
	cases := []struct {
		name    string
		scope   string
		wantErr bool
	}{
		{"regional", "REGIONAL", false},
		{"cloudfront", "CLOUDFRONT", false},
		{"lowercase", "regional", true},
		{"mixed case", "CloudFront", true},
		{"invented", "GLOBAL", true},
		{"trailing space", "REGIONAL ", true},
		// Empty is a missing required parameter, not an enum violation; each
		// operation answers that its own way.
		{"empty", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the value is validated
			aerr := validateScope(tc.scope)

			// Then: only a value off the enum is refused, as WAFInvalidParameterException
			if tc.wantErr != (aerr != nil) {
				t.Fatalf("validateScope(%q) = %v, wantErr = %v", tc.scope, aerr, tc.wantErr)
			}
			if aerr != nil && aerr.Code != "WAFInvalidParameterException" {
				t.Errorf("validateScope(%q).Code = %q, want WAFInvalidParameterException", tc.scope, aerr.Code)
			}
		})
	}
}

func TestTypedWebACLOps_scopeOutsideEnum(t *testing.T) {
	// Given: a handler holding one REGIONAL web ACL
	h := newScopeTestHandler(t)
	ctx := context.Background()
	created, aerr := h.createWebACLTyped(ctx, &createWebACLRequest{Name: "test-acl", Scope: "REGIONAL"})
	if aerr != nil {
		t.Fatalf("createWebACLTyped: %v", aerr)
	}

	// When: each typed operation is called with a Scope outside the enum
	calls := map[string]func() *protocol.AWSError{
		"CreateWebACL": func() *protocol.AWSError {
			_, aerr := h.createWebACLTyped(ctx, &createWebACLRequest{Name: "other", Scope: "GLOBAL"})
			return aerr
		},
		"GetWebACL": func() *protocol.AWSError {
			_, aerr := h.getWebACLTyped(ctx, &getWebACLRequest{ID: created.Summary.Id, Scope: "GLOBAL"})
			return aerr
		},
		"ListWebACLs": func() *protocol.AWSError {
			_, aerr := h.listWebACLsTyped(ctx, &listWebACLsRequest{Scope: "GLOBAL"})
			return aerr
		},
		"DeleteWebACL": func() *protocol.AWSError {
			_, aerr := h.deleteWebACLTyped(ctx, &deleteWebACLRequest{ID: created.Summary.Id, Scope: "GLOBAL"})
			return aerr
		},
	}

	// Then: every one is WAFInvalidParameterException
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			aerr := call()
			if aerr == nil {
				t.Fatalf("%s accepted Scope=GLOBAL", name)
			}
			if aerr.Code != "WAFInvalidParameterException" {
				t.Errorf("%s error code = %q, want WAFInvalidParameterException", name, aerr.Code)
			}
			if aerr.HTTPStatus != http.StatusBadRequest {
				t.Errorf("%s status = %d, want 400", name, aerr.HTTPStatus)
			}
		})
	}
}

func TestTypedWebACLOps_cloudfrontScopeRoundTrips(t *testing.T) {
	// Given: a handler
	h := newScopeTestHandler(t)
	ctx := context.Background()

	// When: a CLOUDFRONT-scope web ACL is created
	created, aerr := h.createWebACLTyped(ctx, &createWebACLRequest{Name: "edge-acl", Scope: "CLOUDFRONT"})
	if aerr != nil {
		t.Fatalf("createWebACLTyped: %v", aerr)
	}

	// Then: it is readable under CLOUDFRONT and invisible under REGIONAL
	if _, aerr := h.getWebACLTyped(ctx, &getWebACLRequest{ID: created.Summary.Id, Scope: "CLOUDFRONT"}); aerr != nil {
		t.Fatalf("getWebACLTyped(CLOUDFRONT): %v", aerr)
	}
	if _, aerr := h.getWebACLTyped(ctx, &getWebACLRequest{ID: created.Summary.Id, Scope: "REGIONAL"}); aerr == nil {
		t.Error("getWebACLTyped(REGIONAL) reached a CLOUDFRONT web ACL")
	} else if aerr.Code != "WAFNonexistentItemException" {
		t.Errorf("getWebACLTyped(REGIONAL).Code = %q, want WAFNonexistentItemException", aerr.Code)
	}
	listed, aerr := h.listWebACLsTyped(ctx, &listWebACLsRequest{Scope: "REGIONAL"})
	if aerr != nil {
		t.Fatalf("listWebACLsTyped(REGIONAL): %v", aerr)
	}
	if len(listed.WebACLs) != 0 {
		t.Errorf("ListWebACLs(REGIONAL) = %d web ACLs, want 0", len(listed.WebACLs))
	}
}
