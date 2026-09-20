// Scope is WAFv2's CLOUDFRONT | REGIONAL enum, not a free string: the two
// values are separate namespaces with differently shaped ARNs, so a value
// outside the enum has no namespace to land in. These tests cover the
// rejection and the round trip on both sides of the split.
package waf_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// cloudfrontWebACLReq is defaultWebACLReq's CLOUDFRONT-scope twin.
var cloudfrontWebACLReq = map[string]any{
	"Name":          "edge-acl",
	"Scope":         "CLOUDFRONT",
	"DefaultAction": map[string]any{"Allow": map[string]any{}},
	"VisibilityConfig": map[string]any{
		"SampledRequestsEnabled":   false,
		"CloudWatchMetricsEnabled": false,
		"MetricName":               "edge-acl",
	},
	"Rules": []any{},
}

// ─── Scope enum validation ────────────────────────────────────────────────────

func TestCreateWebACL_scopeOutsideEnum(t *testing.T) {
	// Given: a server, and Scope values outside WAFv2's Scope enum
	srv := helpers.NewTestServer(t)

	for _, scope := range []string{"GLOBAL", "regional", "CloudFront", "REGIONAL "} {
		t.Run(scope, func(t *testing.T) {
			// When: CreateWebACL is called with that Scope
			body := map[string]any{}
			for k, v := range defaultWebACLReq {
				body[k] = v
			}
			body["Scope"] = scope
			resp := wafCall(t, srv, "CreateWebACL", body)
			defer resp.Body.Close()

			// Then: 400 WAFInvalidParameterException, and nothing is stored
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "WAFInvalidParameterException")
		})
	}

	// Then: no web ACL was created under either real scope
	for _, scope := range []string{"REGIONAL", "CLOUDFRONT"} {
		if got := listWebACLIDs(t, srv, scope); len(got) != 0 {
			t.Errorf("scope %s: expected no web ACLs after rejected creates, got %d", scope, len(got))
		}
	}
}

func TestGetWebACL_scopeOutsideEnum(t *testing.T) {
	// Given: an existing REGIONAL web ACL
	srv := helpers.NewTestServer(t)
	id := createWebACL(t, srv, defaultWebACLReq)

	// When: GetWebACL is called with a Scope outside the enum
	resp := wafCall(t, srv, "GetWebACL", map[string]any{
		"Id": id, "Name": "test-acl", "Scope": "GLOBAL",
	})
	defer resp.Body.Close()

	// Then: 400 WAFInvalidParameterException — not "no such item"
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "WAFInvalidParameterException")
}

func TestListWebACLs_scopeOutsideEnum(t *testing.T) {
	// Given: a server
	srv := helpers.NewTestServer(t)

	// When: ListWebACLs is called with a Scope outside the enum
	resp := wafCall(t, srv, "ListWebACLs", map[string]any{"Scope": "GLOBAL"})
	defer resp.Body.Close()

	// Then: 400 WAFInvalidParameterException — not an empty page
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "WAFInvalidParameterException")
}

func TestDeleteWebACL_scopeOutsideEnum(t *testing.T) {
	// Given: an existing REGIONAL web ACL
	srv := helpers.NewTestServer(t)
	id := createWebACL(t, srv, defaultWebACLReq)

	// When: DeleteWebACL is called with a Scope outside the enum
	resp := wafCall(t, srv, "DeleteWebACL", map[string]any{
		"Id": id, "Name": "test-acl", "Scope": "GLOBAL", "LockToken": "whatever",
	})
	defer resp.Body.Close()

	// Then: 400 WAFInvalidParameterException, and the ACL survives
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "WAFInvalidParameterException")
	if got := listWebACLIDs(t, srv, "REGIONAL"); len(got) != 1 {
		t.Errorf("expected the REGIONAL web ACL to survive, got %d", len(got))
	}
}

// ─── CLOUDFRONT scope ─────────────────────────────────────────────────────────

func TestWebACL_cloudfrontScopeRoundTrips(t *testing.T) {
	// Given: a server
	srv := helpers.NewTestServer(t)

	// When: a CLOUDFRONT-scope web ACL is created
	cr := wafCall(t, srv, "CreateWebACL", cloudfrontWebACLReq)
	defer cr.Body.Close()
	helpers.AssertStatus(t, cr, http.StatusOK)
	var created struct {
		Summary struct {
			Id        string `json:"Id"`
			Name      string `json:"Name"`
			ARN       string `json:"ARN"`
			LockToken string `json:"LockToken"`
		} `json:"Summary"`
	}
	helpers.DecodeJSON(t, cr, &created)

	// Then: the ARN carries AWS's global resource type, not the regional one
	if !strings.Contains(created.Summary.ARN, ":global/webacl/") {
		t.Errorf("CLOUDFRONT ARN = %q, want a :global/webacl/ resource", created.Summary.ARN)
	}

	// And: it is gettable, listable and deletable under CLOUDFRONT
	gr := wafCall(t, srv, "GetWebACL", map[string]any{
		"Id": created.Summary.Id, "Name": created.Summary.Name, "Scope": "CLOUDFRONT",
	})
	defer gr.Body.Close()
	helpers.AssertStatus(t, gr, http.StatusOK)

	if got := listWebACLIDs(t, srv, "CLOUDFRONT"); len(got) != 1 || got[0] != created.Summary.Id {
		t.Errorf("ListWebACLs(CLOUDFRONT) = %v, want [%s]", got, created.Summary.Id)
	}

	dr := wafCall(t, srv, "DeleteWebACL", map[string]any{
		"Id": created.Summary.Id, "Name": created.Summary.Name,
		"Scope": "CLOUDFRONT", "LockToken": created.Summary.LockToken,
	})
	defer dr.Body.Close()
	helpers.AssertStatus(t, dr, http.StatusOK)
	if got := listWebACLIDs(t, srv, "CLOUDFRONT"); len(got) != 0 {
		t.Errorf("ListWebACLs(CLOUDFRONT) after delete = %v, want empty", got)
	}
}

func TestWebACL_scopesAreSeparateNamespaces(t *testing.T) {
	// Given: one web ACL in each scope, sharing a name
	srv := helpers.NewTestServer(t)
	sameName := map[string]any{}
	for k, v := range cloudfrontWebACLReq {
		sameName[k] = v
	}
	sameName["Scope"] = "REGIONAL"
	cloudfrontID := createWebACL(t, srv, cloudfrontWebACLReq)
	regionalID := createWebACL(t, srv, sameName)

	// When/Then: each scope lists only its own
	if got := listWebACLIDs(t, srv, "CLOUDFRONT"); len(got) != 1 || got[0] != cloudfrontID {
		t.Errorf("ListWebACLs(CLOUDFRONT) = %v, want [%s]", got, cloudfrontID)
	}
	if got := listWebACLIDs(t, srv, "REGIONAL"); len(got) != 1 || got[0] != regionalID {
		t.Errorf("ListWebACLs(REGIONAL) = %v, want [%s]", got, regionalID)
	}

	// And: a REGIONAL Get cannot reach the CLOUDFRONT web ACL
	resp := wafCall(t, srv, "GetWebACL", map[string]any{
		"Id": cloudfrontID, "Name": "edge-acl", "Scope": "REGIONAL",
	})
	defer resp.Body.Close()
	helpers.AssertJSONError(t, resp, "WAFNonexistentItemException")
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func createWebACL(t *testing.T, srv *helpers.TestServer, body map[string]any) string {
	t.Helper()
	resp := wafCall(t, srv, "CreateWebACL", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var created struct {
		Summary struct {
			Id string `json:"Id"`
		} `json:"Summary"`
	}
	helpers.DecodeJSON(t, resp, &created)
	return created.Summary.Id
}

func listWebACLIDs(t *testing.T, srv *helpers.TestServer, scope string) []string {
	t.Helper()
	resp := wafCall(t, srv, "ListWebACLs", map[string]any{"Scope": scope})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var listed struct {
		WebACLs []struct {
			Id string `json:"Id"`
		} `json:"WebACLs"`
	}
	helpers.DecodeJSON(t, resp, &listed)
	ids := make([]string, 0, len(listed.WebACLs))
	for _, acl := range listed.WebACLs {
		ids = append(ids, acl.Id)
	}
	return ids
}
