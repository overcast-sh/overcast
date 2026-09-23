package s3_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/overcast-sh/overcast/internal/bff"
	"github.com/overcast-sh/overcast/tests/helpers"
)

// The console's data grid reads every file by Range through the BFF's object
// download route: the first 64 KB of a CSV, the stream its indexer reads from
// an offset onwards, each block between two index offsets, a Parquet footer
// and its column chunks. The unit test in internal/bff pins the proxy hop
// against a stub; this pins the whole path against the real S3 handler, so a
// change on either side that drops Range — and silently makes every read a
// full download — fails here.
func TestConsoleDownloadRoute_servesRangesEndToEnd(t *testing.T) {
	// Given: an object of 1,000 known bytes behind a real emulator
	srv := helpers.NewTestServer(t)
	createBucket(t, srv, "lake")
	content := []byte(strings.Repeat("0123456789", 100))
	putObject(t, srv, "lake", "raw/orders.csv", content, "text/csv")

	// The BFF dials the emulator on localhost at its API port — the same rule
	// a container uses — so the test server's port is that port.
	apiURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiPort, err := strconv.Atoi(apiURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	console := httptest.NewServer(
		bff.NewHandler(fstest.MapFS{}, fstest.MapFS{}, bff.UIConfig{APIPort: apiPort}),
	)
	t.Cleanup(console.Close)
	download := fmt.Sprintf(
		"%s/api/s3/buckets/lake/objects/%s/download?x-overcast-endpoint=%s",
		console.URL, url.PathEscape("raw/orders.csv"), url.QueryEscape(srv.URL),
	)

	tests := []struct {
		name        string
		rangeHeader string
		wantStatus  int
		wantRange   string
		wantBody    string
		refused     bool // an error answer: no ETag, no bytes to compare
	}{
		{
			name:        "a block between two offsets",
			rangeHeader: "bytes=100-199",
			wantStatus:  http.StatusPartialContent,
			wantRange:   "bytes 100-199/1000",
			wantBody:    string(content[100:200]),
		},
		{
			name:        "the indexer resuming from an offset",
			rangeHeader: "bytes=990-",
			wantStatus:  http.StatusPartialContent,
			wantRange:   "bytes 990-999/1000",
			wantBody:    string(content[990:]),
		},
		{
			name:        "a Parquet footer's suffix read",
			rangeHeader: "bytes=992-999",
			wantStatus:  http.StatusPartialContent,
			wantRange:   "bytes 992-999/1000",
			wantBody:    string(content[992:]),
		},
		{
			name:       "no Range, the whole object",
			wantStatus: http.StatusOK,
			wantBody:   string(content),
		},
		{
			// A stale index asking past the end of a shrunk object: the
			// grid reports the status rather than parsing an error page.
			name:        "a range past the end",
			rangeHeader: "bytes=2000-",
			wantStatus:  http.StatusRequestedRangeNotSatisfiable,
			refused:     true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// When: the console reads it with that Range
			req, err := http.NewRequest(http.MethodGet, download, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.rangeHeader != "" {
				req.Header.Set("Range", tc.rangeHeader)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			// Then: exactly those bytes, as a 206 saying which they are, with
			// the ETag the grid keys its caches on
			helpers.AssertStatus(t, resp, tc.wantStatus)
			if got := resp.Header.Get("Content-Range"); got != tc.wantRange {
				t.Errorf("Content-Range: want %q, got %q", tc.wantRange, got)
			}
			if tc.refused {
				return
			}
			if resp.Header.Get("ETag") == "" {
				t.Error("expected an ETag")
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tc.wantBody {
				t.Errorf("body: want %d bytes %q…, got %d bytes %q…",
					len(tc.wantBody), prefix(tc.wantBody), len(body), prefix(string(body)))
			}
		})
	}
}

func prefix(s string) string {
	if len(s) > 20 {
		return s[:20]
	}
	return s
}
