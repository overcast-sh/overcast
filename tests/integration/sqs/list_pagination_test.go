package sqs_test

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// Pagination of ListQueues and ListDeadLetterSourceQueues (#1094).
//
// Per AWS docs (API_ListQueues.html, API_ListDeadLetterSourceQueues.html):
// MaxResults is 1..1000; without it the response holds up to 1,000 results and
// NextToken is null; with it, NextToken is present while more results remain.
// The docs name no error for a bad token or an out-of-range MaxResults; the
// emulator answers InvalidParameterValue, SQS's generic bad-argument code.

// listPage is one decoded page, whichever protocol carried it.
type listPage struct {
	urls      []string
	nextToken string
}

// listCaller issues one page request for an op on one protocol. On a non-200
// the page is empty and the caller owns the response body.
type listCaller func(t *testing.T, srv *helpers.TestServer, op string, params map[string]any) (*http.Response, listPage)

func jsonListCaller(t *testing.T, srv *helpers.TestServer, op string, params map[string]any) (*http.Response, listPage) {
	t.Helper()
	resp := sqsCall(t, srv, op, params)
	if resp.StatusCode != http.StatusOK {
		return resp, listPage{}
	}
	defer resp.Body.Close()
	var out struct {
		NextToken  string   `json:"NextToken"`
		QueueUrls  []string `json:"QueueUrls"`
		QueueUrlsL []string `json:"queueUrls"`
	}
	helpers.DecodeJSON(t, resp, &out)
	return resp, listPage{urls: append(out.QueueUrls, out.QueueUrlsL...), nextToken: out.NextToken}
}

type queryListResult struct {
	NextToken string   `xml:"NextToken"`
	QueueUrls []string `xml:"QueueUrl"`
}

func queryListCaller(t *testing.T, srv *helpers.TestServer, op string, params map[string]any) (*http.Response, listPage) {
	t.Helper()
	form := url.Values{}
	for k, v := range params {
		switch x := v.(type) {
		case string:
			form.Set(k, x)
		case int:
			form.Set(k, strconv.Itoa(x))
		}
	}
	resp := sqsGoldenQueryCall(t, srv, op, form)
	if resp.StatusCode != http.StatusOK {
		return resp, listPage{}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var out struct {
		Queues queryListResult `xml:"ListQueuesResult"`
		DLQ    queryListResult `xml:"ListDeadLetterSourceQueuesResult"`
	}
	if err := xml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v\n%s", op, err, raw)
	}
	r := out.Queues
	if op == "ListDeadLetterSourceQueues" {
		r = out.DLQ
	}
	return resp, listPage{urls: r.QueueUrls, nextToken: r.NextToken}
}

var listProtocols = map[string]listCaller{"JSON": jsonListCaller, "Query": queryListCaller}

func walkPages(t *testing.T, srv *helpers.TestServer, call listCaller, op string, base map[string]any, size int) (pages int, all []string) {
	t.Helper()
	token := ""
	for {
		params := map[string]any{"MaxResults": size}
		for k, v := range base {
			params[k] = v
		}
		if token != "" {
			params["NextToken"] = token
		}
		resp, page := call(t, srv, op, params)
		helpers.AssertStatus(t, resp, http.StatusOK)
		pages++
		if len(page.urls) > size {
			t.Fatalf("page %d holds %d items, MaxResults was %d", pages, len(page.urls), size)
		}
		all = append(all, page.urls...)
		if page.nextToken == "" {
			return pages, all
		}
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
		token = page.nextToken
	}
}

func TestListQueues_maxResultsPaginates(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: five queues
			srv := helpers.NewTestServer(t)
			want := map[string]bool{}
			for _, n := range []string{"pg-a", "pg-b", "pg-c", "pg-d", "pg-e"} {
				want[createQueue(t, srv, n)] = true
			}

			// When: the list is walked two at a time
			pages, all := walkPages(t, srv, call, "ListQueues", nil, 2)

			// Then: three pages, each queue exactly once, in a stable order
			if pages != 3 {
				t.Errorf("pages = %d, want 3", pages)
			}
			if len(all) != 5 {
				t.Fatalf("got %d items, want 5: %v", len(all), all)
			}
			if !sort.StringsAreSorted(all) {
				t.Errorf("items not in a stable sorted order: %v", all)
			}
			for _, u := range all {
				if !want[u] {
					t.Errorf("unexpected or duplicate item %q", u)
				}
				delete(want, u)
			}
		})
	}
}

func TestListQueues_withoutMaxResultsReturnsNoToken(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: two queues
			srv := helpers.NewTestServer(t)
			createQueue(t, srv, "nt-a")
			createQueue(t, srv, "nt-b")

			// When: MaxResults is omitted
			resp, page := call(t, srv, "ListQueues", map[string]any{})

			// Then: everything comes back and there is no token
			helpers.AssertStatus(t, resp, http.StatusOK)
			if len(page.urls) != 2 || page.nextToken != "" {
				t.Errorf("got %d urls, token %q; want 2 urls and no token", len(page.urls), page.nextToken)
			}
		})
	}
}

func TestListQueues_exactFitHasNoToken(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: two queues
			srv := helpers.NewTestServer(t)
			createQueue(t, srv, "fit-a")
			createQueue(t, srv, "fit-b")

			// When: MaxResults equals the number of queues
			_, page := call(t, srv, "ListQueues", map[string]any{"MaxResults": 2})

			// Then: the single page is the last
			if len(page.urls) != 2 || page.nextToken != "" {
				t.Errorf("got %d urls, token %q; want 2 urls and no token", len(page.urls), page.nextToken)
			}
		})
	}
}

func TestListQueues_prefixAndPagination(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: three queues under a prefix and one outside it
			srv := helpers.NewTestServer(t)
			for _, n := range []string{"keep-1", "keep-2", "keep-3", "other-1"} {
				createQueue(t, srv, n)
			}

			// When: the prefixed list is walked one at a time
			pages, all := walkPages(t, srv, call, "ListQueues", map[string]any{"QueueNamePrefix": "keep"}, 1)

			// Then: only the prefixed queues, three pages
			if pages != 3 || len(all) != 3 {
				t.Errorf("pages=%d items=%d, want 3 and 3: %v", pages, len(all), all)
			}
		})
	}
}

func TestListQueues_nextTokenWithoutMaxResultsIsAccepted(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: three queues and a token for the second page at size one
			srv := helpers.NewTestServer(t)
			for _, n := range []string{"tk-a", "tk-b", "tk-c"} {
				createQueue(t, srv, n)
			}
			_, first := call(t, srv, "ListQueues", map[string]any{"MaxResults": 1})
			if first.nextToken == "" {
				t.Fatal("first page carried no NextToken")
			}

			// When: the token is sent without MaxResults
			resp, rest := call(t, srv, "ListQueues", map[string]any{"NextToken": first.nextToken})

			// Then: the remainder is returned and the call is not rejected
			helpers.AssertStatus(t, resp, http.StatusOK)
			if len(rest.urls) != 2 || rest.nextToken != "" {
				t.Errorf("got %d urls, token %q; want the remaining 2 and no token", len(rest.urls), rest.nextToken)
			}
		})
	}
}

func TestListQueues_invalidNextToken(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			createQueue(t, srv, "bad-token")

			resp, _ := call(t, srv, "ListQueues", map[string]any{"MaxResults": 1, "NextToken": "not-a-token"})
			defer resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			assertInvalidParameterValue(t, proto, resp)
		})
	}
}

func TestListQueues_maxResultsOutOfRange(t *testing.T) {
	for proto, call := range listProtocols {
		for _, bad := range []int{0, -1, 1001} {
			t.Run(proto+"/"+strconv.Itoa(bad), func(t *testing.T) {
				srv := helpers.NewTestServer(t)
				createQueue(t, srv, "range-q")

				resp, _ := call(t, srv, "ListQueues", map[string]any{"MaxResults": bad})
				defer resp.Body.Close()

				helpers.AssertStatus(t, resp, http.StatusBadRequest)
				assertInvalidParameterValue(t, proto, resp)
			})
		}
	}
}

func TestListQueues_maxResultsBoundsAccepted(t *testing.T) {
	for proto, call := range listProtocols {
		for _, ok := range []int{1, 1000} {
			t.Run(proto+"/"+strconv.Itoa(ok), func(t *testing.T) {
				srv := helpers.NewTestServer(t)
				createQueue(t, srv, "bound-q")
				resp, page := call(t, srv, "ListQueues", map[string]any{"MaxResults": ok})
				helpers.AssertStatus(t, resp, http.StatusOK)
				if len(page.urls) != 1 {
					t.Errorf("got %d urls, want 1", len(page.urls))
				}
			})
		}
	}
}

// dlqWithSources creates a DLQ and n source queues that point at it.
func dlqWithSources(t *testing.T, srv *helpers.TestServer, n int) string {
	t.Helper()
	dlqURL := createQueue(t, srv, "pg-dlq")
	attrsResp := sqsCall(t, srv, "GetQueueAttributes", map[string]any{
		"QueueUrl": dlqURL, "AttributeNames": []string{"QueueArn"},
	})
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	helpers.DecodeJSON(t, attrsResp, &attrs)
	policy := `{"deadLetterTargetArn":"` + attrs.Attributes["QueueArn"] + `","maxReceiveCount":"3"}`
	for i := 0; i < n; i++ {
		src := createQueue(t, srv, "pg-src-"+strconv.Itoa(i))
		r := sqsCall(t, srv, "SetQueueAttributes", map[string]any{
			"QueueUrl": src, "Attributes": map[string]string{"RedrivePolicy": policy},
		})
		r.Body.Close()
		helpers.AssertStatus(t, r, http.StatusOK)
	}
	return dlqURL
}

func TestListDeadLetterSourceQueues_maxResultsPaginates(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			// Given: a DLQ with three source queues
			srv := helpers.NewTestServer(t)
			dlq := dlqWithSources(t, srv, 3)

			// When: walked two at a time
			pages, all := walkPages(t, srv, call, "ListDeadLetterSourceQueues", map[string]any{"QueueUrl": dlq}, 2)

			// Then: two pages, three distinct sources
			seen := map[string]bool{}
			for _, u := range all {
				seen[u] = true
			}
			if pages != 2 || len(all) != 3 || len(seen) != 3 {
				t.Errorf("pages=%d items=%d distinct=%d, want 2/3/3: %v", pages, len(all), len(seen), all)
			}
		})
	}
}

func TestListDeadLetterSourceQueues_withoutMaxResultsReturnsNoToken(t *testing.T) {
	for proto, call := range listProtocols {
		t.Run(proto, func(t *testing.T) {
			srv := helpers.NewTestServer(t)
			dlq := dlqWithSources(t, srv, 2)
			resp, page := call(t, srv, "ListDeadLetterSourceQueues", map[string]any{"QueueUrl": dlq})
			helpers.AssertStatus(t, resp, http.StatusOK)
			if len(page.urls) != 2 || page.nextToken != "" {
				t.Errorf("got %d urls, token %q; want 2 and no token", len(page.urls), page.nextToken)
			}
		})
	}
}

func TestListDeadLetterSourceQueues_invalidPagingParameters(t *testing.T) {
	cases := map[string]map[string]any{
		"badToken":  {"MaxResults": 1, "NextToken": "not-a-token"},
		"zeroMax":   {"MaxResults": 0},
		"tooBigMax": {"MaxResults": 1001},
		"negMax":    {"MaxResults": -5},
	}
	for proto, call := range listProtocols {
		for name, extra := range cases {
			t.Run(proto+"/"+name, func(t *testing.T) {
				srv := helpers.NewTestServer(t)
				dlq := dlqWithSources(t, srv, 1)
				params := map[string]any{"QueueUrl": dlq}
				for k, v := range extra {
					params[k] = v
				}
				resp, _ := call(t, srv, "ListDeadLetterSourceQueues", params)
				defer resp.Body.Close()
				helpers.AssertStatus(t, resp, http.StatusBadRequest)
				assertInvalidParameterValue(t, proto, resp)
			})
		}
	}
}

func assertInvalidParameterValue(t *testing.T, proto string, resp *http.Response) {
	t.Helper()
	if proto == "JSON" {
		helpers.AssertJSONError(t, resp, "InvalidParameterValue")
		return
	}
	helpers.AssertQueryXMLError(t, resp, "InvalidParameterValue")
}
