package cloudfront_test

// Pagination contract coverage for CloudFront's List* operations — part of
// docs/plans/pagination-plan.md's H2 (shared paginator-contract test
// helper) and G3 (invalid-token error mapping for CloudFront ×4). Seeds
// enough distributions to force multiple pages, walks ListDistributions to
// termination via helpers.PaginationContractTest, and asserts a garbled
// Marker returns CloudFront's documented InvalidArgument error instead of
// silently restarting the walk from page 1.

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// listDistributionsPage is the minimal shape needed to walk ListDistributions'
// pagination — see DistributionList/DistributionSummary in
// internal/services/cloudfront/types.go for the full response shape.
type listDistributionsPage struct {
	XMLName    xml.Name `xml:"DistributionList"`
	NextMarker string   `xml:"NextMarker"`
	Items      []struct {
		ID string `xml:"Id"`
	} `xml:"Items>DistributionSummary"`
}

func TestListDistributions_paginationContract(t *testing.T) {
	// Given: 5 distributions — more than the MaxItems=2 page size below, so
	// the walk must take at least 3 pages.
	srv := helpers.NewTestServer(t)
	const total = 5
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		dist, _ := cfCreateAndParse(t, srv, fmt.Sprintf("pagination-contract-%d", i))
		ids = append(ids, dist.ID)
	}
	// MemoryStore.Scan (internal/state/memory.go) returns entries in
	// ascending key order, and ListDistributions doesn't re-sort — so the
	// paginated walk's order is the ascending order of the (server-
	// generated, opaque) distribution IDs.
	sort.Strings(ids)

	fetch := func(t *testing.T, token string) ([]string, string) {
		t.Helper()
		url := srv.URL + "/2020-05-31/distribution?MaxItems=2"
		if token != "" {
			url += "&Marker=" + token
		}
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("ListDistributions: %v", err)
		}
		defer resp.Body.Close()
		helpers.AssertStatus(t, resp, http.StatusOK)
		b := readBody(t, resp)
		var page listDistributionsPage
		if err := xml.Unmarshal(b, &page); err != nil {
			t.Fatalf("unmarshal DistributionList: %v\nbody: %s", err, b)
		}
		got := make([]string, len(page.Items))
		for i, item := range page.Items {
			got[i] = item.ID
		}
		return got, page.NextMarker
	}

	// When: a request carries a garbled Marker that cannot decode to a
	// valid pagination position.
	probe := func(t *testing.T) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet,
			srv.URL+"/2020-05-31/distribution?MaxItems=2&Marker=not-a-real-token", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("ListDistributions (invalid marker): %v", err)
		}
		defer resp.Body.Close()
		b := readBody(t, resp)
		// CloudFront is rest-xml without noErrorWrapping, so the code sits
		// inside the <ErrorResponse><Error> envelope (#2010).
		var errResp struct {
			Error struct {
				Code string `xml:"Code"`
			} `xml:"Error"`
		}
		if err := xml.Unmarshal(b, &errResp); err != nil {
			t.Fatalf("unmarshal error response: %v\nbody: %s", err, b)
		}
		return resp.StatusCode, errResp.Error.Code
	}

	// Then: exactly-once + order + termination for the valid walk, and the
	// AWS-documented InvalidArgument error for the invalid-token probe (see
	// https://docs.aws.amazon.com/cloudfront/latest/APIReference/API_ListDistributions.html#API_ListDistributions_Errors).
	helpers.PaginationContractTest(t, ids, func(id string) string { return id }, fetch, probe,
		helpers.PaginationContractOptions{
			WantInvalidTokenStatus:    http.StatusBadRequest,
			WantInvalidTokenErrorCode: "InvalidArgument",
		})
}
