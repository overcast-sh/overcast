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

// listProtectionsOut is the ListProtectionsResponse shape these tests read:
// the Protection members Overcast populates, plus the NextToken the
// @paginated trait names as the continuation token.
type listProtectionsOut struct {
	Protections []struct {
		Id            string `json:"Id"`
		Name          string `json:"Name"`
		ResourceArn   string `json:"ResourceArn"`
		ProtectionArn string `json:"ProtectionArn"`
	} `json:"Protections"`
	NextToken string `json:"NextToken"`
}

// listProtections calls ListProtections with the given request members and
// decodes a successful response.
func listProtections(t *testing.T, srv *helpers.TestServer, body map[string]any) listProtectionsOut {
	t.Helper()
	resp := shieldCall(t, srv, "ListProtections", body)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var out listProtectionsOut
	helpers.DecodeJSON(t, resp, &out)
	return out
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
	// only member in the pinned model (shield-2016-06-02.json). In
	// particular it does not carry ProtectionArn, even though Protection
	// itself does: the ARN comes back from DescribeProtection and
	// ListProtections, not from the create call.
	helpers.AssertStatus(t, resp, http.StatusOK)
	helpers.AssertRequestID(t, resp)
	var out map[string]any
	helpers.DecodeJSON(t, resp, &out)
	if id, _ := out["ProtectionId"].(string); id == "" {
		t.Error("expected a non-empty ProtectionId")
	}
	if _, present := out["ProtectionArn"]; present {
		t.Error("CreateProtectionResponse carries ProtectionId only; ProtectionArn is a Protection member")
	}
}

// TestCreateProtection_duplicateResourceArn pins AWS's one-protection-per-
// resource rule. CreateProtection models ResourceAlreadyExistsException
// (shield-2016-06-02.json, CreateProtection.errors), a client error with no
// @httpError trait, so it is answered with HTTP 400 like every other Shield
// client error.
func TestCreateProtection_duplicateResourceArn(t *testing.T) {
	// Given: a protection on a resource
	srv := helpers.NewTestServer(t)
	resourceArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc"
	id := createTestProtection(t, srv, "web-alb", resourceArn)

	// When: CreateProtection is called again for the same ResourceArn
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"Name":        "web-alb-again",
		"ResourceArn": resourceArn,
	})
	defer resp.Body.Close()

	// Then: ResourceAlreadyExistsException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ResourceAlreadyExistsException")
	helpers.AssertRequestID(t, resp)

	// And: the original protection is untouched — one record, original name
	out := listProtections(t, srv, map[string]any{})
	if len(out.Protections) != 1 {
		t.Fatalf("expected the duplicate to be refused, got %d protections", len(out.Protections))
	}
	if out.Protections[0].Id != id || out.Protections[0].Name != "web-alb" {
		t.Errorf("original protection changed: got Id %q Name %q, want Id %q Name web-alb",
			out.Protections[0].Id, out.Protections[0].Name, id)
	}
}

// TestCreateProtection_duplicateNameDifferentResource proves the duplicate
// check keys on ResourceArn and nothing else: the model puts no uniqueness
// constraint on Name, and the docs describe a protection as covering one
// resource ("You can add protection to only a single resource with each
// CreateProtection request").
func TestCreateProtection_duplicateNameDifferentResource(t *testing.T) {
	// Given: a protection on one resource
	srv := helpers.NewTestServer(t)
	createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")

	// When: a second protection reuses the name on a different resource
	resp := shieldCall(t, srv, "CreateProtection", map[string]any{
		"Name":        "web-alb",
		"ResourceArn": "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/two/bbb",
	})
	defer resp.Body.Close()

	// Then: it is created
	helpers.AssertStatus(t, resp, http.StatusOK)
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

// TestDescribeProtection_protectionArn pins the ProtectionArn member the
// pinned model gives Protection (shield-2016-06-02.json, Protection
// .ProtectionArn, typed ResourceArn so it must match ^arn:aws). Shield is a
// global service, so the protection ARN carries an empty region —
// arn:aws:shield::<account>:protection/<id>, the same shape TagResource,
// UntagResource and ListTagsForResource already parse.
func TestDescribeProtection_protectionArn(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "web-alb", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc")

	// When: DescribeProtection is called
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{"ProtectionId": id})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	// Then: the Protection object carries the protection's own ARN
	var out struct {
		Protection struct {
			ProtectionArn string `json:"ProtectionArn"`
		} `json:"Protection"`
	}
	helpers.DecodeJSON(t, resp, &out)
	if want := protectionARN(id); out.Protection.ProtectionArn != want {
		t.Errorf("ProtectionArn: got %q, want %q", out.Protection.ProtectionArn, want)
	}
}

// TestDescribeProtection_bothIdentifiers covers the model's "but not both"
// wording on DescribeProtectionRequest's ProtectionId and ResourceArn members.
//
// Fork: AWS does not document which error this produces. DescribeProtection
// models exactly three — InternalErrorException, InvalidParameterException and
// ResourceNotFoundException (shield-2016-06-02.json) — and of those only
// InvalidParameterException ("the parameters passed to the API are invalid")
// fits a request supplying a mutually exclusive pair, so that is what Overcast
// answers. Recorded in docs/dev/compatibility/services/shield.yaml.
func TestDescribeProtection_bothIdentifiers(t *testing.T) {
	// Given: an existing protection
	srv := helpers.NewTestServer(t)
	resourceArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc"
	id := createTestProtection(t, srv, "web-alb", resourceArn)

	// When: DescribeProtection is called with both identifiers, both of
	// which name that same protection
	resp := shieldCall(t, srv, "DescribeProtection", map[string]any{
		"ProtectionId": id,
		"ResourceArn":  resourceArn,
	})
	defer resp.Body.Close()

	// Then: InvalidParameterException rather than a silent preference for
	// one of the two
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidParameterException")
	helpers.AssertRequestID(t, resp)
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

// TestListProtections_returnsProtectionArn pins ProtectionArn on the list
// surface too — Protections is a list of the same Protection shape
// DescribeProtection returns (shield-2016-06-02.json).
func TestListProtections_returnsProtectionArn(t *testing.T) {
	// Given: one protection
	srv := helpers.NewTestServer(t)
	id := createTestProtection(t, srv, "p1", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")

	// When: ListProtections is called
	out := listProtections(t, srv, map[string]any{})

	// Then: the listed Protection carries its own ARN
	if len(out.Protections) != 1 {
		t.Fatalf("expected 1 protection, got %d", len(out.Protections))
	}
	if want := protectionARN(id); out.Protections[0].ProtectionArn != want {
		t.Errorf("ProtectionArn: got %q, want %q", out.Protections[0].ProtectionArn, want)
	}
}

// TestListProtections_maxResultsPaginates pins MaxResults and NextToken, which
// ListProtections declares through @paginated{inputToken: NextToken,
// outputToken: NextToken, items: Protections, pageSize: MaxResults}.
func TestListProtections_maxResultsPaginates(t *testing.T) {
	// Given: two protections
	srv := helpers.NewTestServer(t)
	id1 := createTestProtection(t, srv, "p1", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")
	id2 := createTestProtection(t, srv, "p2", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/two/bbb")

	// When: the list is walked one protection at a time
	first := listProtections(t, srv, map[string]any{"MaxResults": 1})

	// Then: the first page holds one protection and a continuation token
	if len(first.Protections) != 1 {
		t.Fatalf("first page: got %d protections, want 1", len(first.Protections))
	}
	if first.NextToken == "" {
		t.Fatal("first page: expected a NextToken, got none")
	}

	// And: that token fetches the second and final page
	second := listProtections(t, srv, map[string]any{"MaxResults": 1, "NextToken": first.NextToken})
	if len(second.Protections) != 1 {
		t.Fatalf("second page: got %d protections, want 1", len(second.Protections))
	}
	if second.NextToken != "" {
		t.Errorf("second page: expected no NextToken, got %q", second.NextToken)
	}

	// And: the two pages together are the whole set, with no repeats
	got := map[string]bool{first.Protections[0].Id: true, second.Protections[0].Id: true}
	if !got[id1] || !got[id2] {
		t.Errorf("pages covered %v, want both %q and %q", got, id1, id2)
	}
}

// TestListProtections_invalidNextToken pins InvalidPaginationTokenException,
// one of ListProtections' three modeled errors. Silently restarting from the
// first page on a token the service never minted is the divergence
// serviceutil.ErrInvalidPageToken exists to prevent.
func TestListProtections_invalidNextToken(t *testing.T) {
	// Given: a protection exists, so the list is non-empty
	srv := helpers.NewTestServer(t)
	createTestProtection(t, srv, "p1", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")

	// When: ListProtections is called with a token Overcast never minted
	resp := shieldCall(t, srv, "ListProtections", map[string]any{"NextToken": "not-a-real-token"})
	defer resp.Body.Close()

	// Then: InvalidPaginationTokenException, not the whole list again
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "InvalidPaginationTokenException")
	helpers.AssertRequestID(t, resp)
}

// TestListProtections_inclusionFilters pins the three InclusionProtectionFilters
// members — ResourceArns, ProtectionNames and ResourceTypes — and the model's
// rule that a protection must match every filter type supplied ("Shield
// Advanced returns protections that exactly match all of the filter criteria
// that you provide").
//
// ResourceTypes is matched against the ProtectedResourceType derived from each
// protection's ResourceArn: Protection carries no resource-type member of its
// own, so there is nothing stored to compare against.
func TestListProtections_inclusionFilters(t *testing.T) {
	// Given: three protections on three kinds of resource
	srv := helpers.NewTestServer(t)
	albArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/web/abc"
	clbArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/classic-web"
	cfArn := "arn:aws:cloudfront::000000000000:distribution/E1234567890ABC"
	albID := createTestProtection(t, srv, "alb", albArn)
	clbID := createTestProtection(t, srv, "clb", clbArn)
	cfID := createTestProtection(t, srv, "cdn", cfArn)

	tests := []struct {
		name    string
		filters map[string]any
		want    []string
	}{
		{"by ProtectionNames", map[string]any{"ProtectionNames": []string{"cdn"}}, []string{cfID}},
		{"by ResourceArns", map[string]any{"ResourceArns": []string{clbArn}}, []string{clbID}},
		{"by ResourceTypes APPLICATION_LOAD_BALANCER", map[string]any{"ResourceTypes": []string{"APPLICATION_LOAD_BALANCER"}}, []string{albID}},
		{"by ResourceTypes CLASSIC_LOAD_BALANCER", map[string]any{"ResourceTypes": []string{"CLASSIC_LOAD_BALANCER"}}, []string{clbID}},
		{"by ResourceTypes CLOUDFRONT_DISTRIBUTION", map[string]any{"ResourceTypes": []string{"CLOUDFRONT_DISTRIBUTION"}}, []string{cfID}},
		{"every filter type at once", map[string]any{
			"ProtectionNames": []string{"alb"},
			"ResourceArns":    []string{albArn},
			"ResourceTypes":   []string{"APPLICATION_LOAD_BALANCER"},
		}, []string{albID}},
		{"criteria that cannot all match", map[string]any{
			"ProtectionNames": []string{"alb"},
			"ResourceTypes":   []string{"CLOUDFRONT_DISTRIBUTION"},
		}, nil},
		{"a name nothing carries", map[string]any{"ProtectionNames": []string{"absent"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// When: ListProtections is called with those InclusionFilters
			out := listProtections(t, srv, map[string]any{"InclusionFilters": tc.filters})

			// Then: exactly the matching protections come back
			got := make([]string, 0, len(out.Protections))
			for _, p := range out.Protections {
				got = append(got, p.Id)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d protections %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("protection %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestListProtections_filtersApplyBeforePagination pins the ordering the
// @paginated trait implies: MaxResults caps the filtered set, not the raw one,
// so a page is never short because a filter emptied part of it.
func TestListProtections_filtersApplyBeforePagination(t *testing.T) {
	// Given: two protections that match a name filter and one that does not
	srv := helpers.NewTestServer(t)
	createTestProtection(t, srv, "keep", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/one/aaa")
	createTestProtection(t, srv, "drop", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/two/bbb")
	createTestProtection(t, srv, "keep", "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/three/ccc")

	filters := map[string]any{"ProtectionNames": []string{"keep"}}

	// When: the filtered list is walked one at a time
	first := listProtections(t, srv, map[string]any{"InclusionFilters": filters, "MaxResults": 1})

	// Then: a full page of one matching protection, and more to come
	if len(first.Protections) != 1 || first.Protections[0].Name != "keep" {
		t.Fatalf("first page: got %d protections %+v, want 1 named keep", len(first.Protections), first.Protections)
	}
	if first.NextToken == "" {
		t.Fatal("first page: expected a NextToken, got none")
	}

	// And: the second page holds the other match and ends the walk — the
	// unmatched protection never appears
	second := listProtections(t, srv, map[string]any{
		"InclusionFilters": filters,
		"MaxResults":       1,
		"NextToken":        first.NextToken,
	})
	if len(second.Protections) != 1 || second.Protections[0].Name != "keep" {
		t.Fatalf("second page: got %d protections %+v, want 1 named keep", len(second.Protections), second.Protections)
	}
	if second.NextToken != "" {
		t.Errorf("second page: expected no NextToken, got %q", second.NextToken)
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
