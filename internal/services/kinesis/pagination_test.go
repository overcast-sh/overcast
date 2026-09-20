package kinesis

// pagination_test.go covers the cursor and page-size helpers directly.
//
// Token expiry lives here rather than in tests/integration/kinesis because
// the only way to age a token through the HTTP surface is to advance the
// test server's mock clock past nextTokenTTL, and a mock clock replays every
// interval it passes over — 300 virtual seconds across every service the
// test server starts cost ~3.5 s of wall time per test (see tests/AGENTS.md
// § "Wind the clock before the tickers exist"). Nothing here starts a
// router, so the same behaviour is proven in microseconds. The wire mapping
// the integration tests would have added on top is already pinned by
// TestListStreams_malformedNextTokenIsInvalidArgument, which travels the
// identical WriteJSONError path.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// paginationTestHandler builds a Handler on a memory store and a mock clock.
func paginationTestHandler(t *testing.T) (*Handler, *clock.Mock) {
	t.Helper()
	cfg := &config.Config{AccountID: "000000000000", Region: "us-east-1"}
	clk := clock.NewMock()
	h := newHandler(cfg, newKinesisStore(state.NewMemoryStore(), cfg.Region),
		serviceutil.NewServiceLogger(zap.NewNop(), serviceName), clk)
	return h, clk
}

func TestPageToken_roundTripsStreamAndCursor(t *testing.T) {
	// Given: a token minted for a stream at a cursor
	h, _ := paginationTestHandler(t)
	token := h.sealPageToken("orders", "shardId-000000000003")

	// When: it is read back
	cur, aerr := h.openPageToken(token)

	// Then: both halves survive
	if aerr != nil {
		t.Fatalf("openPageToken: %v", aerr)
	}
	if cur.Stream != "orders" || cur.After != "shardId-000000000003" {
		t.Fatalf("cursor = %+v, want stream orders after shardId-000000000003", cur)
	}
}

// "Tokens expire after 300 seconds ... If you specify an expired token in a
// call to ListShards, you get ExpiredNextTokenException."
// https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html
func TestPageToken_expiresAfterTheDocumentedLifetime(t *testing.T) {
	h, clk := paginationTestHandler(t)
	token := h.sealPageToken("orders", "shardId-000000000003")

	// Still valid on the last second of its life.
	clk.Add(nextTokenTTL)
	if _, aerr := h.openPageToken(token); aerr != nil {
		t.Fatalf("token rejected at exactly %s: %v", nextTokenTTL, aerr)
	}

	// One second later it is not.
	clk.Add(time.Second)
	_, aerr := h.openPageToken(token)
	if aerr == nil {
		t.Fatal("expected an expired token to be rejected")
	}
	if aerr.Code != "ExpiredNextTokenException" {
		t.Errorf("code = %q, want ExpiredNextTokenException", aerr.Code)
	}
	if aerr.HTTPStatus != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", aerr.HTTPStatus)
	}
}

func TestPageToken_malformedIsInvalidArgument(t *testing.T) {
	h, _ := paginationTestHandler(t)

	for _, token := range []string{"not-base64!!", "bm90LWpzb24="} {
		_, aerr := h.openPageToken(token)
		if aerr == nil {
			t.Fatalf("token %q was accepted", token)
		}
		if aerr.Code != "InvalidArgumentException" {
			t.Errorf("token %q: code = %q, want InvalidArgumentException", token, aerr.Code)
		}
		if aerr.HTTPStatus != http.StatusBadRequest {
			t.Errorf("token %q: status = %d, want 400", token, aerr.HTTPStatus)
		}
	}
}

// An expired token must be refused by the operation, not just by the codec.
func TestListShards_expiredNextTokenIsRejected(t *testing.T) {
	ctx := context.Background()
	h, clk := paginationTestHandler(t)
	if _, aerr := h.createStreamTyped(ctx, &createStreamRequest{StreamName: "expiry", ShardCount: 3}); aerr != nil {
		t.Fatalf("createStream: %v", aerr)
	}
	one := 1
	first, aerr := h.listShardsTyped(ctx, &listShardsRequest{StreamName: "expiry", MaxResults: &one})
	if aerr != nil {
		t.Fatalf("listShards: %v", aerr)
	}
	if first.NextToken == "" {
		t.Fatal("expected a NextToken from the first page")
	}

	clk.Add(nextTokenTTL + time.Second)

	_, aerr = h.listShardsTyped(ctx, &listShardsRequest{NextToken: first.NextToken})
	if aerr == nil {
		t.Fatal("expected the expired token to be rejected")
	}
	if aerr.Code != "ExpiredNextTokenException" {
		t.Errorf("code = %q, want ExpiredNextTokenException", aerr.Code)
	}
}

func TestValidateLimit_rangeIsTheModelRange(t *testing.T) {
	requested := func(n int) *int { return &n }

	for name, tc := range map[string]struct {
		limit  *int
		reject bool
	}{
		"absent":        {nil, false},
		"minimum":       {requested(1), false},
		"above the cap": {requested(500), false}, // clamped by pageOf, not rejected
		"model maximum": {requested(10000), false},
		"zero":          {requested(0), true},
		"negative":      {requested(-1), true},
		"over the max":  {requested(10001), true},
	} {
		t.Run(name, func(t *testing.T) {
			aerr := validateLimit(tc.limit, "Limit", listStreamsModelMax)
			if tc.reject && aerr == nil {
				t.Fatal("expected the limit to be rejected")
			}
			if !tc.reject && aerr != nil {
				t.Fatalf("limit rejected: %v", aerr)
			}
			if aerr != nil && aerr.Code != "InvalidArgumentException" {
				t.Errorf("code = %q, want InvalidArgumentException", aerr.Code)
			}
		})
	}
}

func TestItemsAfter_isExclusiveAndToleratesAMissingKey(t *testing.T) {
	items := []string{"a", "c", "e"}
	key := func(s string) string { return s }

	for name, tc := range map[string]struct {
		after string
		want  []string
	}{
		"no cursor":          {"", []string{"a", "c", "e"}},
		"exact match":        {"c", []string{"e"}},
		"between two items":  {"b", []string{"c", "e"}},
		"past the last item": {"z", nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := itemsAfter(items, tc.after, key)
			if len(got) != len(tc.want) {
				t.Fatalf("itemsAfter(%q) = %v, want %v", tc.after, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("itemsAfter(%q) = %v, want %v", tc.after, got, tc.want)
				}
			}
		})
	}
}
