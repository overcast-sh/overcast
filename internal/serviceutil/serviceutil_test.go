package serviceutil_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- DecodeJSON ------------------------------------------------------------

func TestDecodeJSON_success(t *testing.T) {
	// Given: a valid JSON request body
	body := `{"QueueName":"my-queue"}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	w := httptest.NewRecorder()

	// When: we decode it
	var dst struct {
		QueueName string `json:"QueueName"`
	}
	ok := serviceutil.DecodeJSON(w, req, &dst)

	// Then: decode succeeds and the value is populated
	if !ok {
		t.Error("expected DecodeJSON to return true for valid JSON")
	}
	if dst.QueueName != "my-queue" {
		t.Errorf("expected QueueName my-queue, got %q", dst.QueueName)
	}
}

func TestDecodeJSON_invalidJSON_writesError(t *testing.T) {
	// Given: an invalid JSON body
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString("{not json"))
	w := httptest.NewRecorder()

	// When: we try to decode it
	var dst struct{ Name string }
	ok := serviceutil.DecodeJSON(w, req, &dst)

	// Then: returns false and writes a 400 error response
	if ok {
		t.Error("expected DecodeJSON to return false for invalid JSON")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var errResp struct {
		Type string `json:"__type"`
	}
	json.NewDecoder(w.Body).Decode(&errResp)
	// Real AWS JSON-protocol services reject an unparseable body with
	// SerializationException, not a validation-class error: DynamoDB et al.
	// return __type com.amazon.coral.service#SerializationException, and
	// Smithy's malformed-request protocol tests pin 400 +
	// x-amzn-errortype: SerializationException. ValidationException /
	// InvalidArgument are for semantic validation AFTER a successful parse.
	if errResp.Type != "SerializationException" {
		t.Errorf("expected SerializationException error, got %q", errResp.Type)
	}
}

// ---- RequireString ---------------------------------------------------------

func TestRequireString_present(t *testing.T) {
	// Given: a non-empty string
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	// When + Then: returns true without writing a response
	if !serviceutil.RequireString(w, req, "my-value", "MyParam") {
		t.Error("expected true for non-empty string")
	}
	if w.Code != 200 {
		t.Errorf("expected no response written, got status %d", w.Code)
	}
}

func TestRequireString_empty_writesError(t *testing.T) {
	// Given: an empty string
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()

	// When + Then: returns false and writes MissingParameter error
	if serviceutil.RequireString(w, req, "", "QueueName") {
		t.Error("expected false for empty string")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---- QueryInt --------------------------------------------------------------

func TestQueryInt_present(t *testing.T) {
	// Given: a URL with the parameter set
	req := httptest.NewRequest(http.MethodGet, "/?max-keys=50", nil)

	// When + Then: returns the parsed value
	if got := serviceutil.QueryInt(req, "max-keys", 1000); got != 50 {
		t.Errorf("expected 50, got %d", got)
	}
}

func TestQueryInt_absent_returnsDefault(t *testing.T) {
	// Given: a URL without the parameter
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// When + Then: returns the default
	if got := serviceutil.QueryInt(req, "max-keys", 1000); got != 1000 {
		t.Errorf("expected default 1000, got %d", got)
	}
}

func TestQueryInt_invalid_returnsDefault(t *testing.T) {
	// Given: a URL with a non-numeric parameter value
	req := httptest.NewRequest(http.MethodGet, "/?max-keys=notanumber", nil)

	// When + Then: returns the default (graceful degradation)
	if got := serviceutil.QueryInt(req, "max-keys", 1000); got != 1000 {
		t.Errorf("expected default 1000, got %d", got)
	}
}

// ---- HasQueryParam ---------------------------------------------------------

func TestHasQueryParam_present(t *testing.T) {
	// Given: a URL with the parameter (no value)
	req := httptest.NewRequest(http.MethodGet, "/bucket?location", nil)
	if !serviceutil.HasQueryParam(req, "location") {
		t.Error("expected location param to be present")
	}
}

func TestHasQueryParam_absent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/bucket", nil)
	if serviceutil.HasQueryParam(req, "location") {
		t.Error("expected location param to be absent")
	}
}

// ---- ClientBaseURL ---------------------------------------------------------

func TestClientBaseURL_configuredHostname(t *testing.T) {
	// Given: an explicit external hostname and port are configured.
	cfg := &config.Config{Hostname: "overcast.local", Port: 4566}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/", nil)

	// When: a service asks for its client-facing base URL.
	got := serviceutil.ClientBaseURL(cfg, req)

	// Then: the configured hostname is authoritative, on the *caller's* port —
	// theirs is the only port proven dialable, and the two differ whenever the
	// API port is remapped. Same precedence as CloudFormation's output
	// re-minting and the plan in docs/plans/client-facing-url-minting.md.
	if got != "http://overcast.local:12345" {
		t.Fatalf("expected the configured host on the caller's port, got %q", got)
	}
}

func TestClientBaseURL_configuredHostnameWithDynamicPort(t *testing.T) {
	// Given: tests configured a hostname but the server port is assigned by httptest.
	cfg := &config.Config{Hostname: "overcast.local", Port: 0}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/", nil)

	// When: a service asks for its client-facing base URL.
	got := serviceutil.ClientBaseURL(cfg, req)

	// Then: the configured host is retained and the request port fills the dynamic value.
	if got != "http://overcast.local:12345" {
		t.Fatalf("expected configured hostname with request port, got %q", got)
	}
}

func TestClientBaseURL_requestHostFallback(t *testing.T) {
	// Given: no external hostname is configured.
	cfg := &config.Config{Port: 4566}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:12345/", nil)

	// When: a service asks for its client-facing base URL.
	got := serviceutil.ClientBaseURL(cfg, req)

	// Then: the request host is used so local test/proxy URLs remain executable.
	if got != "http://127.0.0.1:12345" {
		t.Fatalf("expected request base URL, got %q", got)
	}
}

// ---- ClampInt --------------------------------------------------------------

func TestClampInt(t *testing.T) {
	cases := []struct{ v, min, max, want int }{
		{5, 1, 10, 5},   // within range
		{0, 1, 10, 1},   // below min
		{15, 1, 10, 10}, // above max
		{1, 1, 10, 1},   // at min
		{10, 1, 10, 10}, // at max
	}
	for _, tc := range cases {
		if got := serviceutil.ClampInt(tc.v, tc.min, tc.max); got != tc.want {
			t.Errorf("ClampInt(%d, %d, %d) = %d, want %d", tc.v, tc.min, tc.max, got, tc.want)
		}
	}
}

// ---- Pagination ------------------------------------------------------------

func TestPaginate_firstPage(t *testing.T) {
	// Given: 25 items, max 10 per page
	items := make([]int, 25)
	for i := range items {
		items[i] = i
	}

	// When: we request the first page
	page, err := serviceutil.Paginate(items, 10, "", serviceutil.PaginateOptions{})

	// Then: we get the first 10 items and a continuation token
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 10 {
		t.Errorf("expected 10 items, got %d", len(page.Items))
	}
	if !page.IsTruncated {
		t.Error("expected IsTruncated to be true")
	}
	if page.NextToken == "" {
		t.Error("expected NextToken to be set")
	}
}

func TestPaginate_lastPage(t *testing.T) {
	// Given: 15 items, max 10, fetching page 2
	items := make([]int, 15)
	firstPage, err := serviceutil.Paginate(items, 10, "", serviceutil.PaginateOptions{})
	if err != nil {
		t.Fatalf("unexpected error on page 1: %v", err)
	}

	// When: we request the second page using the continuation token
	secondPage, err := serviceutil.Paginate(items, 10, firstPage.NextToken, serviceutil.PaginateOptions{})

	// Then: we get the remaining 5 items with no continuation token
	if err != nil {
		t.Fatalf("unexpected error on page 2: %v", err)
	}
	if len(secondPage.Items) != 5 {
		t.Errorf("expected 5 items on last page, got %d", len(secondPage.Items))
	}
	if secondPage.IsTruncated {
		t.Error("expected IsTruncated to be false on last page")
	}
	if secondPage.NextToken != "" {
		t.Error("expected no NextToken on last page")
	}
}

func TestPaginate_allItemsFitOnOnePage(t *testing.T) {
	// Given: 5 items, max 100
	items := []string{"a", "b", "c", "d", "e"}

	// When + Then: single page with no truncation
	page, err := serviceutil.Paginate(items, 100, "", serviceutil.PaginateOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 5 {
		t.Errorf("expected 5 items, got %d", len(page.Items))
	}
	if page.IsTruncated {
		t.Error("expected IsTruncated false for single page")
	}
}

// TestPaginate_invalidToken_returnsError pins the fix for this codebase's
// most common AWS-fidelity divergence (docs/plans/pagination-plan.md, H1/G3):
// a garbled continuation token must surface as a distinguishable error, not
// silently restart the walk from page 1 (which causes duplicate delivery to
// any client polling with a stale/corrupted token).
func TestPaginate_invalidToken_returnsError(t *testing.T) {
	// Given: an invalid/corrupt continuation token
	items := []int{1, 2, 3}

	// When: paginating with the garbled token
	page, err := serviceutil.Paginate(items, 10, "!!not-valid-base64!!", serviceutil.PaginateOptions{})

	// Then: ErrInvalidPageToken is returned (errors.Is-checkable) and no
	// items are silently handed back as if this were page 1.
	if !errors.Is(err, serviceutil.ErrInvalidPageToken) {
		t.Fatalf("expected ErrInvalidPageToken, got %v", err)
	}
	if len(page.Items) != 0 {
		t.Errorf("expected zero-value page on error, got %d items", len(page.Items))
	}
}

func TestPaginate_invalidToken_outOfRange_returnsError(t *testing.T) {
	// Given: a well-formed token (valid base64/JSON int) whose index is
	// past the end of the current item set — e.g. items were deleted
	// since the token was issued.
	items := []int{1, 2, 3}
	farToken := base64.URLEncoding.EncodeToString([]byte("999"))

	// When: paginating with the out-of-range token
	_, err := serviceutil.Paginate(items, 10, farToken, serviceutil.PaginateOptions{})

	// Then: it errors instead of silently restarting from index 0.
	if !errors.Is(err, serviceutil.ErrInvalidPageToken) {
		t.Fatalf("expected ErrInvalidPageToken for out-of-range index, got %v", err)
	}
}

func TestPaginate_tokenAtExactEnd_returnsEmptyLastPage(t *testing.T) {
	// Given: a token pointing exactly at len(items) — the legitimate
	// "no more results" position, not an error.
	items := []int{1, 2, 3}
	endToken := base64.URLEncoding.EncodeToString([]byte("3"))

	// When: paginating with that token
	page, err := serviceutil.Paginate(items, 10, endToken, serviceutil.PaginateOptions{})

	// Then: an empty, non-truncated page — not an error.
	if err != nil {
		t.Fatalf("unexpected error at exact-end token: %v", err)
	}
	if len(page.Items) != 0 || page.IsTruncated {
		t.Errorf("expected empty non-truncated page, got %#v", page)
	}
}

func TestPaginate_defaultLimit_appliedWhenRequestedLimitZero(t *testing.T) {
	// Given: 30 items and a caller that supplies its own AWS-documented
	// default via options instead of a client-provided limit.
	items := make([]int, 30)

	// When: requestedLimit is 0 (client omitted MaxResults)
	page, err := serviceutil.Paginate(items, 0, "", serviceutil.PaginateOptions{DefaultLimit: 10})

	// Then: the operation's own default is used, not the package fallback.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 10 {
		t.Errorf("expected DefaultLimit=10 to apply, got %d items", len(page.Items))
	}
}

func TestPaginate_maxLimit_capsRequestedLimit(t *testing.T) {
	// Given: 30 items and an operation whose AWS-documented cap is lower
	// than what the client requested (e.g. SSM GetParametersByPath caps at
	// 10 even though nothing stops a client from asking for more).
	items := make([]int, 30)

	// When: the client asks for more than the cap
	page, err := serviceutil.Paginate(items, 25, "", serviceutil.PaginateOptions{DefaultLimit: 10, MaxLimit: 10})

	// Then: the cap wins.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.Items) != 10 {
		t.Errorf("expected MaxLimit=10 to cap the page, got %d items", len(page.Items))
	}
}

// ---- BucketName validation -------------------------------------------------

// TestBucketName_matchesAWSRules is the specification for S3 general purpose
// bucket names, taken rule-by-rule from
// https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
//
// Overcast's validation must match AWS exactly. Rejecting a name AWS accepts
// is the worse failure of the two: it fails a stack locally that deploys fine
// against AWS, which is the divergence this emulator exists to avoid.
func TestBucketName_matchesAWSRules(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
		why   string
	}{
		// ---- Valid ---------------------------------------------------------
		{name: "my-bucket", valid: true},
		{name: "bucket123", valid: true},
		{name: "a-b-c", valid: true},
		{name: "abc", valid: true, why: "minimum length"},
		{name: strings.Repeat("a", 63), valid: true, why: "maximum length"},

		// Periods are legal. AWS discourages them (they break the
		// *.s3.<region>.amazonaws.com wildcard certificate for virtual-hosted
		// style over HTTPS) but explicitly documents them as valid, and lists
		// example.com / www.example.com / my.example.s3.bucket as valid names.
		{name: "example.com", valid: true, why: "AWS-documented valid example"},
		{name: "www.example.com", valid: true, why: "AWS-documented valid example"},
		{name: "my.example.s3.bucket", valid: true, why: "AWS-documented valid example"},

		// Consecutive hyphens are legal — AWS forbids two adjacent PERIODS,
		// not hyphens. Its own reserved suffixes (--ol-s3, --x-s3, --table-s3)
		// contain double hyphens, which could not exist if they were illegal.
		{name: "my--bucket", valid: true, why: "AWS forbids adjacent periods, not hyphens"},

		// ---- Invalid: length and alphabet ----------------------------------
		{name: "ab", why: "too short"},
		{name: strings.Repeat("a", 64), why: "too long"},
		{name: "A-BUCKET", why: "uppercase"},
		{name: "amzn_s3_demo_bucket", why: "underscores — AWS-documented invalid example"},
		{name: "bucket with spaces", why: "spaces"},

		// ---- Invalid: structure --------------------------------------------
		{name: "-bucket", why: "must begin with a letter or number"},
		{name: "bucket-", why: "must end with a letter or number"},
		{name: ".bucket", why: "must begin with a letter or number"},
		{name: "bucket.", why: "must end with a letter or number"},
		{name: "example..com", why: "two adjacent periods — AWS-documented invalid example"},
		{name: "192.168.5.4", why: "IP address format — AWS-documented invalid example"},

		// ---- Invalid: reserved prefixes ------------------------------------
		{name: "xn--bucket", why: "reserved prefix xn--"},
		{name: "sthree-bucket", why: "reserved prefix sthree-"},
		{name: "amzn-s3-demo-bucket", why: "reserved prefix amzn-s3-demo-"},

		// ---- Invalid: reserved suffixes ------------------------------------
		{name: "my-bucket-s3alias", why: "reserved suffix -s3alias"},
		{name: "my-bucket--ol-s3", why: "reserved suffix --ol-s3"},
		{name: "my-bucket.mrap", why: "reserved suffix .mrap"},
		{name: "my-bucket--x-s3", why: "reserved suffix --x-s3"},
		{name: "my-bucket--table-s3", why: "reserved suffix --table-s3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := serviceutil.BucketName(tc.name)
			if tc.valid && err != nil {
				t.Errorf("BucketName(%q) rejected a name AWS accepts (%s): %v", tc.name, tc.why, err.Message)
			}
			if !tc.valid && err == nil {
				t.Errorf("BucketName(%q) accepted a name AWS rejects (%s)", tc.name, tc.why)
			}
		})
	}
}

// TestTableWarehouseBucketName_exemptsOnlyItsOwnSuffix: the warehouse bucket
// S3 Tables creates must carry the "--table-s3" suffix CreateBucket refuses,
// and every other general purpose rule still applies to it.
func TestTableWarehouseBucketName_exemptsOnlyItsOwnSuffix(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
		why   string
	}{
		{name: "63a8e430-6e0b-46f5-k833abtwr6s8tmtsycedn8s4yc3xhuse1b--table-s3", valid: true, why: "AWS-shaped warehouse name"},
		{name: "abc--table-s3", valid: true, why: "short warehouse name"},
		{name: "my-bucket", why: "must carry the warehouse suffix"},
		{name: "my-bucket--x-s3", why: "another reserved suffix"},
		{name: "xn--abc--table-s3", why: "reserved prefix still applies"},
		{name: "ABC--table-s3", why: "uppercase still refused"},
		{name: strings.Repeat("a", 60) + "--table-s3", why: "too long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := serviceutil.TableWarehouseBucketName(tc.name)
			if tc.valid && err != nil {
				t.Errorf("TableWarehouseBucketName(%q) rejected (%s): %v", tc.name, tc.why, err.Message)
			}
			if !tc.valid && err == nil {
				t.Errorf("TableWarehouseBucketName(%q) accepted (%s)", tc.name, tc.why)
			}
		})
	}
}

// ---- ResourceName validation -----------------------------------------------

func TestResourceName_valid(t *testing.T) {
	// Given: a generic AWS-style identifier rule
	rule := serviceutil.NameRule{
		MinLength:      1,
		MaxLength:      8,
		Allowed:        serviceutil.AlphaNumericHyphenUnderscorePeriod,
		ErrorCode:      "ValidationException",
		LengthMessage:  "bad length",
		AllowedMessage: "bad chars",
	}

	// When + Then: values matching the rule pass
	if aerr := serviceutil.ResourceName("a-1_b.c", rule); aerr != nil {
		t.Fatalf("expected name to be valid, got %v", aerr)
	}
}

func TestResourceName_invalidLength(t *testing.T) {
	// Given: a generic AWS-style identifier rule
	rule := serviceutil.NameRule{
		MinLength:     2,
		MaxLength:     4,
		ErrorCode:     "ValidationException",
		LengthMessage: "bad length",
	}

	// When: the name is too short
	aerr := serviceutil.ResourceName("a", rule)

	// Then: the configured AWS error is returned
	if aerr == nil {
		t.Fatal("expected validation error")
	}
	if aerr.Code != "ValidationException" || aerr.Message != "bad length" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected error: %#v", aerr)
	}
}

func TestResourceName_invalidAllowedCharacters(t *testing.T) {
	// Given: a generic AWS-style identifier rule
	rule := serviceutil.NameRule{
		MinLength:      1,
		MaxLength:      8,
		Allowed:        serviceutil.AlphaNumericHyphenUnderscorePeriod,
		ErrorCode:      "ValidationException",
		AllowedMessage: "bad chars",
	}

	// When: the name contains an unsupported rune
	aerr := serviceutil.ResourceName("bad!", rule)

	// Then: the configured AWS error is returned
	if aerr == nil {
		t.Fatal("expected validation error")
	}
	if aerr.Code != "ValidationException" || aerr.Message != "bad chars" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected error: %#v", aerr)
	}
}

func TestResourceName_invalidPattern(t *testing.T) {
	// Given: a rule with the AppSync GraphQL identifier pattern
	rule := serviceutil.NameRule{
		MinLength:      1,
		MaxLength:      8,
		Pattern:        serviceutil.GraphQLIdentifierPattern,
		ErrorCode:      "BadRequestException",
		PatternMessage: "bad pattern",
	}

	// When: the name starts with a digit
	aerr := serviceutil.ResourceName("1bad", rule)

	// Then: the configured AWS error is returned
	if aerr == nil {
		t.Fatal("expected validation error")
	}
	if aerr.Code != "BadRequestException" || aerr.Message != "bad pattern" || aerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("unexpected error: %#v", aerr)
	}
}

// ---- AppSync validation -----------------------------------------------------

func TestAppSyncIdentifierName_valid(t *testing.T) {
	valid := []string{"Todo", "_Private", "field123"}
	for _, name := range valid {
		if aerr := serviceutil.AppSyncIdentifierName(name, "name"); aerr != nil {
			t.Errorf("AppSyncIdentifierName(%q): unexpected error: %v", name, aerr)
		}
	}
}

func TestAppSyncIdentifierName_invalid(t *testing.T) {
	invalid := []string{"", "1Todo", "bad-name", "bad.name"}
	for _, name := range invalid {
		if aerr := serviceutil.AppSyncIdentifierName(name, "name"); aerr == nil {
			t.Errorf("AppSyncIdentifierName(%q): expected error", name)
		}
	}
}

func TestAppSyncNamedHelpers(t *testing.T) {
	if aerr := serviceutil.AppSyncDataSourceName("DataSource_1"); aerr != nil {
		t.Fatalf("expected data source name to be valid: %v", aerr)
	}
	if aerr := serviceutil.AppSyncFunctionName("fn1"); aerr != nil {
		t.Fatalf("expected function name to be valid: %v", aerr)
	}
	if aerr := serviceutil.AppSyncTypeName("Query"); aerr != nil {
		t.Fatalf("expected type name to be valid: %v", aerr)
	}
	if aerr := serviceutil.AppSyncFieldName("listTodos"); aerr != nil {
		t.Fatalf("expected field name to be valid: %v", aerr)
	}
}

// ---- LazyInit --------------------------------------------------------------

func TestLazyInit_runsOnce(t *testing.T) {
	// Given: a LazyInit and a counter
	var li serviceutil.LazyInit
	callCount := 0

	// When: Do is called multiple times concurrently
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			li.Do(func() error {
				callCount++
				return nil
			})
		}()
	}
	wg.Wait()

	// Then: the function ran exactly once
	if callCount != 1 {
		t.Errorf("expected init function to run exactly once, ran %d times", callCount)
	}
	if !li.Done() {
		t.Error("expected Done() to return true after successful init")
	}
}

func TestLazyInit_retriesOnError(t *testing.T) {
	// Given: an init function that fails the first time
	var li serviceutil.LazyInit
	attempts := 0

	// When: first call fails
	err1 := li.Do(func() error {
		attempts++
		return http.ErrServerClosed // any error
	})
	// Then: error is returned
	if err1 == nil {
		t.Error("expected error on first attempt")
	}

	// When: second call succeeds
	err2 := li.Do(func() error {
		attempts++
		return nil
	})

	// Then: no error, and it ran twice (retried after failure)
	if err2 != nil {
		t.Errorf("expected success on second attempt, got: %v", err2)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestLazyInit_reset(t *testing.T) {
	// Given: a successfully initialised LazyInit
	var li serviceutil.LazyInit
	li.Do(func() error { return nil })

	// When: we reset it
	li.Reset()

	// Then: Done returns false and the next Do runs the function again
	if li.Done() {
		t.Error("expected Done() to be false after Reset()")
	}

	ran := false
	li.Do(func() error {
		ran = true
		return nil
	})
	if !ran {
		t.Error("expected init function to run again after Reset()")
	}
}

// ---- HeaderPrefix ----------------------------------------------------------

func TestHeaderPrefix_extractsAndLowercases(t *testing.T) {
	// Given: a request with x-amz-meta-* headers
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	req.Header.Set("X-Amz-Meta-Author", "alice")
	req.Header.Set("X-Amz-Meta-Version", "2.0")
	req.Header.Set("Content-Type", "text/plain") // should be excluded

	// When: we extract the meta headers
	meta := serviceutil.HeaderPrefix(req, "X-Amz-Meta-")

	// Then: only the meta headers are returned, keys lowercased
	if meta["author"] != "alice" {
		t.Errorf("expected author=alice, got %q", meta["author"])
	}
	if meta["version"] != "2.0" {
		t.Errorf("expected version=2.0, got %q", meta["version"])
	}
	if _, ok := meta["content-type"]; ok {
		t.Error("content-type should not be included in meta")
	}
}

// ---- QueueName -------------------------------------------------------------

func TestQueueName_valid(t *testing.T) {
	cases := []string{"my-queue", "my_queue", "MyQueue123", "q"}
	for _, name := range cases {
		if aerr := serviceutil.QueueName(name); aerr != nil {
			t.Errorf("QueueName(%q): unexpected error: %v", name, aerr)
		}
	}
}

func TestQueueName_tooLong(t *testing.T) {
	name := string(make([]byte, 81)) // 81 chars
	for i := range name {
		name = name[:i] + "a" + name[i+1:]
	}
	if aerr := serviceutil.QueueName(name); aerr == nil {
		t.Error("expected error for name >80 chars")
	}
}

func TestQueueName_empty(t *testing.T) {
	if aerr := serviceutil.QueueName(""); aerr == nil {
		t.Error("expected error for empty queue name")
	}
}

func TestQueueName_invalidChars(t *testing.T) {
	if aerr := serviceutil.QueueName("bad!name"); aerr == nil {
		t.Error("expected error for queue name with invalid chars")
	}
}

// ---- TableName -------------------------------------------------------------

func TestTableName_valid(t *testing.T) {
	cases := []string{"Users", "my-table", "my_table.v2"}
	for _, name := range cases {
		if aerr := serviceutil.TableName(name); aerr != nil {
			t.Errorf("TableName(%q): unexpected error: %v", name, aerr)
		}
	}
}

func TestTableName_tooShort(t *testing.T) {
	if aerr := serviceutil.TableName("ab"); aerr == nil {
		t.Error("expected error for table name < 3 chars")
	}
}

func TestTableName_invalidChars(t *testing.T) {
	if aerr := serviceutil.TableName("bad!table"); aerr == nil {
		t.Error("expected error for table name with invalid chars")
	}
}

// ---- ValidateAndRespond ----------------------------------------------------

func TestValidateAndRespond_noError_returnsTrue(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	got := serviceutil.ValidateAndRespond(w, req, nil)
	if !got {
		t.Error("expected true when aerr is nil")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected no response written (200), got %d", w.Code)
	}
}

func TestValidateAndRespond_withError_writeErrorAndReturnsFalse(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	aerr := &protocol.AWSError{
		Code:       "InvalidParameterValue",
		Message:    "bad input",
		HTTPStatus: http.StatusBadRequest,
	}
	got := serviceutil.ValidateAndRespond(w, req, aerr)
	if got {
		t.Error("expected false when aerr is non-nil")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ---- ServiceLogger ---------------------------------------------------------

func TestServiceLogger_allLevels(t *testing.T) {
	// Use a no-op logger — we just verify calling these methods doesn't panic.
	logger := zap.NewNop()
	slog := serviceutil.NewServiceLogger(logger, "test-service")

	slog.Debug("debug message", zap.String("key", "val"))
	slog.Info("info message")
	slog.Warn("warn message")
	slog.Error("error message", zap.Error(errors.New("something broke")))
}

func TestServiceLogger_With(t *testing.T) {
	logger := zap.NewNop()
	slog := serviceutil.NewServiceLogger(logger, "test-service")

	child := slog.With(zap.String("op", "get-object"), zap.String("bucket", "my-bucket"))
	if child == nil {
		t.Error("With returned nil")
	}
	// Should not panic.
	child.Debug("child log")
}

func TestServiceLogger_Logger(t *testing.T) {
	logger := zap.NewNop()
	slog := serviceutil.NewServiceLogger(logger, "svc")

	if got := slog.Logger(); got == nil {
		t.Error("Logger() returned nil")
	}
}

func TestServiceLogger_LogStateError(t *testing.T) {
	logger := zap.NewNop()
	slog := serviceutil.NewServiceLogger(logger, "test-service")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	aerr := &protocol.AWSError{
		Code:       "InternalError",
		Message:    "store failed",
		HTTPStatus: http.StatusInternalServerError,
	}
	// Should not panic.
	slog.LogStateError(req, "get-object", aerr, zap.String("bucket", "test"))
}

// TestRequestBaseURL_canonicalisesHostCase covers the output half of the
// case-insensitivity rule. Routing folds the Host so any case reaches the right
// service; what Overcast MINTS back must also be canonical, because a caller
// stores and compares those strings. RFC 3986 §3.2.2 says producers should emit
// a lowercase registered name, and AWS does.
//
// Without this, the case of whichever request happened to create a resource was
// baked into its URL: a queue created via "MixedCase.localhost:4566" came back
// with a QueueUrl no string-equal to the same queue's URL fetched over the
// lowercase host, even though both address one endpoint.
func TestRequestBaseURL_canonicalisesHostCase(t *testing.T) {
	cases := []struct {
		name, host, forwarded, want string
	}{
		{name: "mixed case host", host: "MixedCase.Localhost.Overcast.SH:4566", want: "http://mixedcase.localhost.overcast.sh:4566"},
		{name: "already lowercase", host: "localhost:4566", want: "http://localhost:4566"},
		{name: "upper via X-Forwarded-Host", host: "localhost:4566", forwarded: "Proxy.Example.TEST", want: "http://proxy.example.test"},
		{name: "IPv4 literal untouched", host: "127.0.0.1:4566", want: "http://127.0.0.1:4566"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://example/", nil)
			r.Host = tc.host
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Host", tc.forwarded)
			}
			if got := serviceutil.RequestBaseURL(r); got != tc.want {
				t.Errorf("RequestBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDomainPrefix_canonicalisesCase covers the same rule for the value handed
// to function code: requestContext.domainPrefix is data a handler can branch on,
// so it must not vary with how the caller happened to type the Host.
func TestDomainPrefix_canonicalisesCase(t *testing.T) {
	for _, tc := range []struct{ host, want string }{
		{"MixedCase.Localhost.Overcast.SH:4566", "mixedcase"},
		{"abc123.execute-api.us-east-1.localhost:4566", "abc123"},
		{"", "localhost"},
	} {
		if got := serviceutil.DomainPrefix(tc.host); got != tc.want {
			t.Errorf("DomainPrefix(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}
