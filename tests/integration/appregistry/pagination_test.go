// Package appregistry_test contains integration tests for the AppRegistry emulator.
//
// This file pins the deliberate no-pagination behaviour tracked by #60: the
// four List operations (ListApplications, ListAssociatedResources,
// ListAttributeGroups, ListAssociatedAttributeGroups) accept AWS's modeled
// maxResults/nextToken query parameters (confirmed against the AWS API
// Reference — API_app-registry_ListApplications,
// API_app-registry_ListAssociatedResources,
// API_app-registry_ListAttributeGroups and
// API_app-registry_ListAssociatedAttributeGroups all bind
// GET .../?maxResults={{maxResults}}&nextToken={{nextToken}}) but ignore
// them: every call returns the full result set in one page and never
// populates a response nextToken. See internal/services/appregistry/handler.go
// (ListApplications, ListAssociatedResources) and
// internal/services/appregistry/handler_attribute_groups.go
// (ListAttributeGroups, ListAssociatedAttributeGroups), none of which read
// maxResults/nextToken from the request or write nextToken to the response.
package appregistry_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestListApplications_paginationParamsAcceptedAndIgnored(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createApp(t, srv, "page-app-1")
	createApp(t, srv, "page-app-2")
	createApp(t, srv, "page-app-3")

	resp := arDo(t, srv, http.MethodGet, "/applications?maxResults=1&nextToken="+url.QueryEscape("bogus"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Applications []map[string]any `json:"applications"`
		NextToken    *string          `json:"nextToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Applications) != 3 {
		t.Errorf("expected all 3 applications despite maxResults=1, got %d", len(result.Applications))
	}
	if result.NextToken != nil {
		t.Errorf("expected no nextToken, got %v", *result.NextToken)
	}
}

func TestListAssociatedResources_paginationParamsAcceptedAndIgnored(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createApp(t, srv, "page-assoc-app")

	for i := 0; i < 3; i++ {
		arn := "arn:aws:cloudformation:us-east-1:000000000000:stack/page-stack-" + string(rune('a'+i)) + "/abcdef"
		resp := arDo(t, srv, http.MethodPut, "/applications/page-assoc-app/resources/CFN_STACK/"+url.PathEscape(arn), map[string]any{})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	}

	resp := arDo(t, srv, http.MethodGet, "/applications/page-assoc-app/resources?maxResults=1&nextToken="+url.QueryEscape("bogus"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		Resources []map[string]any `json:"resources"`
		NextToken *string          `json:"nextToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.Resources) != 3 {
		t.Errorf("expected all 3 resources despite maxResults=1, got %d", len(result.Resources))
	}
	if result.NextToken != nil {
		t.Errorf("expected no nextToken, got %v", *result.NextToken)
	}
}

func TestListAttributeGroups_paginationParamsAcceptedAndIgnored(t *testing.T) {
	srv := helpers.NewTestServer(t)
	for _, name := range []string{"page-ag-1", "page-ag-2", "page-ag-3"} {
		resp := arDo(t, srv, http.MethodPost, "/attribute-groups", map[string]any{"name": name})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
	}

	resp := arDo(t, srv, http.MethodGet, "/attribute-groups?maxResults=1&nextToken="+url.QueryEscape("bogus"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		AttributeGroups []map[string]any `json:"attributeGroups"`
		NextToken       *string          `json:"nextToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.AttributeGroups) != 3 {
		t.Errorf("expected all 3 attribute groups despite maxResults=1, got %d", len(result.AttributeGroups))
	}
	if result.NextToken != nil {
		t.Errorf("expected no nextToken, got %v", *result.NextToken)
	}
}

func TestListAssociatedAttributeGroups_paginationParamsAcceptedAndIgnored(t *testing.T) {
	srv := helpers.NewTestServer(t)
	createApp(t, srv, "page-ag-app")

	for _, name := range []string{"page-ag-assoc-1", "page-ag-assoc-2", "page-ag-assoc-3"} {
		resp := arDo(t, srv, http.MethodPost, "/attribute-groups", map[string]any{"name": name})
		resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)

		resp2 := arDo(t, srv, http.MethodPut, "/applications/page-ag-app/attribute-groups/"+name, map[string]any{})
		resp2.Body.Close()
		helpers.AssertStatus(t, resp2, http.StatusOK)
	}

	resp := arDo(t, srv, http.MethodGet, "/applications/page-ag-app/attribute-groups?maxResults=1&nextToken="+url.QueryEscape("bogus"), nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	var result struct {
		AttributeGroups []string `json:"attributeGroups"`
		NextToken       *string  `json:"nextToken"`
	}
	helpers.DecodeJSON(t, resp, &result)

	if len(result.AttributeGroups) != 3 {
		t.Errorf("expected all 3 associated attribute groups despite maxResults=1, got %d", len(result.AttributeGroups))
	}
	if result.NextToken != nil {
		t.Errorf("expected no nextToken, got %v", *result.NextToken)
	}
}
