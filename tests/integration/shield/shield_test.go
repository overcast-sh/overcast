// Package shield_test contains integration tests for the AWS Shield
// emulator (internal/services/shield), covering the eight operations it
// implements: DescribeSubscription, CreateProtection, DescribeProtection,
// ListProtections, DeleteProtection, TagResource, UntagResource and
// ListTagsForResource.
//
// Tests here pin Overcast's *current* behaviour against the Shield API
// Reference (https://docs.aws.amazon.com/waf/latest/DDOSAPIReference/).
// Where the handler diverges from documented AWS behaviour, the divergence
// is recorded in docs/dev/compatibility/services/shield.yaml rather than
// asserted here as if it were correct — see the doc comments below and on
// assertResourceNotFound.
//
// Run: go test ./tests/integration/shield/...
package shield_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// testAccountID mirrors helpers.defaultTestConfig's AccountID.
const testAccountID = "000000000000"

// shieldCall performs a Shield X-Amz-Target dispatch request in AWS JSON
// 1.1, the wire protocol Shield's SDK clients use by default.
func shieldCall(t *testing.T, srv *helpers.TestServer, operation string, body map[string]any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal %s body: %v", operation, err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSShield_20160616."+operation)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("shieldCall %s: %v", operation, err)
	}
	return resp
}

// protectionARN builds the protection ARN shape protectionIDFromARN parses
// for TagResource/UntagResource/ListTagsForResource:
// arn:aws:shield::<account>:protection/<id>.
func protectionARN(id string) string {
	return "arn:aws:shield::" + testAccountID + ":protection/" + id
}

// createTestProtection creates a protection and returns its ProtectionId.
func createTestProtection(t *testing.T, srv *helpers.TestServer, name, resourceArn string) string {
	t.Helper()
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"Name":        name,
		"ResourceArn": resourceArn,
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		ProtectionId string `json:"ProtectionId"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.ProtectionId == "" {
		t.Fatal("CreateProtection returned no ProtectionId")
	}
	return out.ProtectionId
}

// assertResourceNotFound asserts the shape of a Shield ResourceNotFoundException
// response: HTTP 400, matching the Shield API Reference, which documents HTTP
// 400 for ResourceNotFoundException on every operation that raises it
// (DescribeProtection, DeleteProtection, TagResource, UntagResource,
// ListTagsForResource).
func assertResourceNotFound(t *testing.T, resp *http.Response) {
	t.Helper()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
	helpers.AssertRequestID(t, resp)
}

// ─── DescribeSubscription ───────────────────────────────────────────────────

func TestDescribeSubscription_success(t *testing.T) {
	// Given: a running server with no prior state
	srv := helpers.NewTestServer(t)

	// When: DescribeSubscription is called
	resp := shieldCall(t, srv, "DescribeSubscription", map[string]any{})
	defer resp.Body.Close()

	// Then: a minimal active-subscription shape is returned, matching the
	// capability note ("Returns a minimal active subscription object") —
	// SubscriptionArn, EndTime, Limits, ProactiveEngagementStatus and
	// SubscriptionLimits are documented Subscription members Overcast does
	// not populate (docs/services/shield.md's "Differences from AWS").
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	var out struct {
		Subscription struct {
			StartTime               float64 `json:"StartTime"`
			TimeCommitmentInSeconds int     `json:"TimeCommitmentInSeconds"`
			AutoRenew               string  `json:"AutoRenew"`
		} `json:"Subscription"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.Subscription.AutoRenew != "ENABLED" {
		t.Errorf("AutoRenew: got %q, want ENABLED", out.Subscription.AutoRenew)
	}
	if out.Subscription.TimeCommitmentInSeconds != 31536000 {
		t.Errorf("TimeCommitmentInSeconds: got %d, want 31536000", out.Subscription.TimeCommitmentInSeconds)
	}
	if out.Subscription.StartTime <= 0 {
		t.Errorf("StartTime: got %v, want a positive epoch timestamp", out.Subscription.StartTime)
	}
}

// ─── CreateProtection ────────────────────────────────────────────────────────

func TestCreateProtection_success(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: CreateProtection is called with Name and ResourceArn
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"Name":        "web-alb",
		"ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc",
	})
	defer resp.Body.Close()

	// Then: 200 with a non-empty ProtectionId — CreateProtectionResponse's
	// only documented member
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	var out struct {
		ProtectionId string `json:"ProtectionId"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.ProtectionId == "" {
		t.Error("expected a non-empty ProtectionId")
	}
}

func TestCreateProtection_missingName(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: CreateProtection is called without Name
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc",
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException, a documented CreateProtection error
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
	helpers.AssertRequestID(t, resp)
}

func TestCreateProtection_missingResourceArn(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: CreateProtection is called without ResourceArn
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"Name": "web-alb",
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
	helpers.AssertRequestID(t, resp)
}

// ─── DescribeProtection ──────────────────────────────────────────────────────

func TestDescribeProtection_byProtectionId_success(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	resourceArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc"
	id := createTestProtection(t, srv, "web-alb", resourceArn)

	// When: DescribeProtection is called by ProtectionId
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{"ProtectionId": id})
	defer resp.Body.Close()

	// Then: the Protection is returned with Id, Name and ResourceArn
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Protection struct {
			Id          string `json:"Id"`
			Name        string `json:"Name"`
			ResourceArn string `json:"ResourceArn"`
		} `json:"Protection"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.Protection.Id != id {
		t.Errorf("Id: got %q, want %q", out.Protection.Id, id)
	}
	if out.Protection.Name != "web-alb" {
		t.Errorf("Name: got %q, want web-alb", out.Protection.Name)
	}
	if out.Protection.ResourceArn != resourceArn {
		t.Errorf("ResourceArn: got %q, want %q", out.Protection.ResourceArn, resourceArn)
	}
}

func TestDescribeProtection_byResourceArn_success(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	resourceArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc"
	id := createTestProtection(t, srv, "web-alb", resourceArn)

	// When: DescribeProtection is called by ResourceArn instead of ProtectionId
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{"ResourceArn": resourceArn})
	defer resp.Body.Close()

	// Then: the same Protection is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Protection struct {
			Id string `json:"Id"`
		} `json:"Protection"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if out.Protection.Id != id {
		t.Errorf("Id: got %q, want %q", out.Protection.Id, id)
	}
}

func TestDescribeProtection_unknownProtectionId(t *testing.T) {
	// Given: no protections exist
	srv := helpers.NewTestServer(t)

	// When: DescribeProtection is called with an unknown ProtectionId
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{
		"ProtectionId": "11111111-1111-1111-1111-111111111111",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

func TestDescribeProtection_unknownResourceArn(t *testing.T) {
	// Given: no protections exist
	srv := helpers.NewTestServer(t)

	// When: DescribeProtection is called with a ResourceArn that names no
	// protection
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{
		"ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/nothing/xyz",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

// TestDescribeProtection_protectionArnFieldOmitted documents a confirmed gap:
// real Shield's Protection object always carries a ProtectionArn member
// (https://docs.aws.amazon.com/waf/latest/DDOSAPIReference/API_Protection.html),
// but Overcast's Protection wire type has no such field, so it is absent from
// every response rather than merely empty. If this starts failing, the field
// has been added — update this test and the compat gap record together
// (docs/dev/compatibility/services/shield.yaml).
func TestDescribeProtection_protectionArnFieldOmitted(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: DescribeProtection is called
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{"ProtectionId": id})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the Protection object carries no ProtectionArn member
	var out struct {
		Protection map[string]any `json:"Protection"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if _, present := out.Protection["ProtectionArn"]; present {
		t.Error("ProtectionArn is now present on Protection — this gap appears to be fixed; update the compat record")
	}
}

// ─── ListProtections ─────────────────────────────────────────────────────────

func TestListProtections_empty(t *testing.T) {
	// Given: no protections
	srv := helpers.NewTestServer(t)

	// When: ListProtections is called
	resp := shieldCall(t, srv, "ListProtections", map[string]any{})
	defer resp.Body.Close()

	// Then: an empty list, not an error
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Protections []any `json:"Protections"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Protections) != 0 {
		t.Errorf("expected no protections, got %d", len(out.Protections))
	}
}

func TestListProtections_success(t *testing.T) {
	// Given: two protections
	srv := helpers.NewTestServer(t)
	id1 := createTestProtection(t, srv, "p1", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")
	id2 := createTestProtection(t, srv, "p2", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/two/bbb")

	// When: ListProtections is called
	resp := shieldCall(t, srv, "ListProtections", map[string]any{})
	defer resp.Body.Close()

	// Then: both protections are returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Protections []struct {
			Id string `json:"Id"`
		} `json:"Protections"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Protections) != 2 {
		t.Fatalf("expected 2 protections, got %d", len(out.Protections))
	}
	got := map[string]bool{}
	for _, p := range out.Protections {
		got[p.Id] = true
	}
	if !got[id1] || !got[id2] {
		t.Errorf("expected both %q and %q in the list, got %v", id1, id2, got)
	}
}

// TestListProtections_filtersAndPaginationIgnored documents a confirmed gap:
// ListProtections accepts InclusionFilters, MaxResults and NextToken (they
// decode without error, since Overcast's ListProtections request type has no
// members at all) but implements none of them — every call returns every
// protection and NextToken is never set, regardless of how many exist or
// what is asked for. Tracked in docs/dev/compatibility/services/shield.yaml.
func TestListProtections_filtersAndPaginationIgnored(t *testing.T) {
	// Given: two protections
	srv := helpers.NewTestServer(t)
	createTestProtection(t, srv, "p1", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")
	createTestProtection(t, srv, "p2", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/two/bbb")

	// When: ListProtections is called with a name filter, MaxResults=1, and
	// a garbage NextToken — real Shield would apply the filter, cap the
	// page at 1 result, and reject the NextToken with
	// InvalidPaginationTokenException
	resp := shieldCall(t, srv, "ListProtections", map[string]any{
		"InclusionFilters": map[string]any{"ProtectionNames": []string{"p1"}},
		"MaxResults":       1,
		"NextToken":        "not-a-real-token",
	})
	defer resp.Body.Close()

	// Then: the call still succeeds and returns every protection unfiltered
	// and unpaginated, with no NextToken in the response
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out struct {
		Protections []any   `json:"Protections"`
		NextToken   *string `json:"NextToken"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if len(out.Protections) != 2 {
		t.Errorf("expected both protections despite the filter and MaxResults=1, got %d", len(out.Protections))
	}
	if out.NextToken != nil {
		t.Errorf("expected no NextToken since pagination is not implemented, got %q", *out.NextToken)
	}
}

// ─── DeleteProtection ────────────────────────────────────────────────────────

func TestDeleteProtection_success(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: DeleteProtection is called
	resp := shieldCall(t, srv, "DeleteProtection", map[string]any{"ProtectionId": id})
	defer resp.Body.Close()

	// Then: 200 with an empty body
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)

	// And: the protection is gone
	describeResp := shieldCall(t, srv, "DescribeProtection", map[string]any{"ProtectionId": id})
	defer describeResp.Body.Close()
	assertResourceNotFound(t, describeResp)
}

func TestDeleteProtection_missingProtectionId(t *testing.T) {
	// Given: a running server
	srv := helpers.NewTestServer(t)

	// When: DeleteProtection is called without ProtectionId
	resp := shieldCall(t, srv, "DeleteProtection", map[string]any{})
	defer resp.Body.Close()

	// Then: InvalidParameterException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
}

func TestDeleteProtection_unknownProtectionId(t *testing.T) {
	// Given: no protections exist
	srv := helpers.NewTestServer(t)

	// When: DeleteProtection is called with an unknown ProtectionId
	resp := shieldCall(t, srv, "DeleteProtection", map[string]any{
		"ProtectionId": "22222222-2222-2222-2222-222222222222",
	})
	defer resp.Body.Close()

	// Then: ResourceNotFoundException
	assertResourceNotFound(t, resp)
}

func TestDeleteProtection_secondCallIsNotFound(t *testing.T) {
	// Given: a protection that has already been deleted once
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")
	first := shieldCall(t, srv, "DeleteProtection", map[string]any{"ProtectionId": id})
	first.Body.Close()
	helpers.AssertStatus(t, first, http.StatusOK)

	// When: DeleteProtection is called again for the same ProtectionId
	second := shieldCall(t, srv, "DeleteProtection", map[string]any{"ProtectionId": id})
	defer second.Body.Close()

	// Then: DeleteProtection is not idempotent — the second call reports
	// ResourceNotFoundException rather than a second 200, matching real
	// Shield's documented behaviour for deleting an already-deleted resource
	assertResourceNotFound(t, second)
}
