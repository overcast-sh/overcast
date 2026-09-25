package kinesis

// pagination.go — the cursor, page-size and error shapes shared by the four
// Kinesis operations AWS documents as paginated: ListStreams, ListShards,
// DescribeStream and ListTagsForStream (#155).

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// nextTokenTTL is how long a minted NextToken stays usable. ListShards'
// reference documents it and names the error:
//
//	"Tokens expire after 300 seconds. When you obtain a value for NextToken
//	in the response to a call to ListShards, you have 300 seconds to use
//	that value. If you specify an expired token in a call to ListShards, you
//	get ExpiredNextTokenException."
//
// https://docs.aws.amazon.com/kinesis/latest/APIReference/API_ListShards.html
const nextTokenTTL = 300 * time.Second

// Page-size rules, one set per operation, each from that operation's AWS API
// Reference page. defaultLimit and maxLimit are what the reference states in
// prose ("The default value is N. If you specify a value greater than N, at
// most N results are returned"); modelMax is the Smithy @range maximum the
// pinned model carries, which is what a request is *rejected* for exceeding
// rather than silently clamped to.
const (
	listStreamsDefaultLimit = 100
	listStreamsMaxLimit     = 100
	listStreamsModelMax     = 10000

	listShardsDefaultLimit = 1000
	listShardsMaxLimit     = 1000
	listShardsModelMax     = 10000

	describeStreamDefaultLimit = 100
	describeStreamMaxLimit     = 100
	describeStreamModelMax     = 10000

	listTagsDefaultLimit = 50
	listTagsMaxLimit     = 50
	listTagsModelMax     = 50
)

// pageCursor is the decoded form of a Kinesis NextToken.
//
// It is a cursor over the item set's own keys rather than
// serviceutil.Paginate's opaque start index, because that is the shape AWS
// gives these operations: every one of them also exposes the same position
// as a named request member the caller may pass instead of a token
// (ExclusiveStartStreamName, ExclusiveStartShardId, ExclusiveStartTagKey),
// so the two spellings have to mean the same thing. It also has to carry
// two facts an index cannot — the stream, because ListShards' token
// "unambiguously identifies the stream" and the request may therefore not
// name one, and the issue time, because the token expires.
//
// serviceutil.Paginate still does the page-size arithmetic and the
// truncation flag (see pageOf); only the token is Kinesis'.
type pageCursor struct {
	// Stream is the stream the listing belongs to, for ListShards' tokens.
	// Empty for account-wide listings (ListStreams).
	Stream string `json:"s,omitempty"`
	// After is the key of the last item on the page that minted this token;
	// the next page starts strictly after it.
	After string `json:"a"`
	// Issued is the Unix time the token was minted, for nextTokenTTL.
	Issued int64 `json:"t"`
	// Filter is the ShardFilter a ListShards listing was made under. A
	// request carrying the token may not repeat it, so the token must.
	Filter *shardFilter `json:"f,omitempty"`
}

// sealPageToken mints the NextToken for a truncated page.
func (h *Handler) sealPageToken(stream, after string) string {
	return h.sealCursor(pageCursor{Stream: stream, After: after})
}

// sealCursor mints the NextToken for cur, stamping its issue time.
func (h *Handler) sealCursor(cur pageCursor) string {
	cur.Issued = h.clk.Now().Unix()
	raw, err := json.Marshal(cur)
	if err != nil {
		// pageCursor is scalars and a struct of scalars; marshalling it
		// cannot fail. Returning no token degrades to "this is the last
		// page" rather than emitting a token no later call could decode.
		return ""
	}
	return base64.URLEncoding.EncodeToString(raw)
}

// openPageToken decodes and age-checks a caller-supplied NextToken.
// A token that does not decode is a bad argument; one that decodes but is
// older than nextTokenTTL is expired, which AWS reports separately so a
// client can tell "retry from the start" from "this was never valid".
func (h *Handler) openPageToken(token string) (pageCursor, *protocol.AWSError) {
	raw, err := base64.URLEncoding.DecodeString(token)
	if err != nil {
		return pageCursor{}, invalidArgument("Invalid NextToken.")
	}
	var cur pageCursor
	if err := json.Unmarshal(raw, &cur); err != nil {
		return pageCursor{}, invalidArgument("Invalid NextToken.")
	}
	if h.clk.Now().Sub(time.Unix(cur.Issued, 0)) > nextTokenTTL {
		return pageCursor{}, expiredNextToken()
	}
	return cur, nil
}

// validateLimit rejects a page size outside the operation's modeled range.
// A nil requested value is the member being absent, which is the default
// rather than an error; an explicit value below 1 or above modelMax is out
// of the Smithy @range the pinned model carries. Values between the
// operation's documented cap and modelMax are legal and clamped by pageOf,
// not rejected — "if you specify a value greater than N, at most N results
// are returned".
func validateLimit(requested *int, member string, modelMax int) *protocol.AWSError {
	if requested == nil {
		return nil
	}
	if *requested < 1 || *requested > modelMax {
		return invalidArgument(member + " must be between 1 and " + strconv.Itoa(modelMax) + ".")
	}
	return nil
}

// pageOf applies an operation's documented page size to items already
// advanced past the caller's cursor, and reports whether more remain.
// serviceutil.Paginate owns the default/cap arithmetic; the continuation
// token is minted by the caller from the last returned item's key (see
// pageCursor), so no token is passed in here and its error is unreachable.
func pageOf[T any](items []T, requested *int, defaultLimit, maxLimit int) ([]T, bool) {
	limit := 0
	if requested != nil {
		limit = *requested
	}
	page, err := serviceutil.Paginate(items, limit, "", serviceutil.PaginateOptions{
		DefaultLimit: defaultLimit,
		MaxLimit:     maxLimit,
	})
	if err != nil {
		// Only reachable with a non-empty continuation token, which this
		// call never passes.
		return items, false
	}
	return page.Items, page.IsTruncated
}

// itemsAfter drops the leading items whose key is at or before after, for an
// item set already ordered by that key. It is the one place the
// exclusive-cursor rule shared by ExclusiveStartStreamName,
// ExclusiveStartShardId, ExclusiveStartTagKey and the NextToken they all
// stand in for is implemented. A cursor naming no item is not an error: the
// listing resumes at the first item that sorts after it.
func itemsAfter[T any](items []T, after string, key func(T) string) []T {
	if after == "" {
		return items
	}
	for i, item := range items {
		if key(item) > after {
			return items[i:]
		}
	}
	return items[:0]
}

// ---- error shapes ------------------------------------------------------------

// invalidArgument is Kinesis' answer for a parameter that is missing,
// malformed, or not usable with the others in the request. Every operation
// this package implements models InvalidArgumentException, and it is already
// what the package reports for a bad ARN and for a rejected tag set (see
// kinesisTagCfg).
func invalidArgument(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgumentException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// errMissingParameter replaces protocol.ErrMissingParameter for Kinesis.
// Its message is the same; only the code changes, because "MissingParameter"
// is not a code any Kinesis operation models, so no SDK's modeled error type
// matches it and a caller catching InvalidArgumentException — the code AWS
// does model on every one of these operations — never sees it.
func errMissingParameter(param string) *protocol.AWSError {
	return invalidArgument("The request must contain the parameter " + param + ".")
}

// expiredNextToken is the documented answer for a token past nextTokenTTL.
func expiredNextToken() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ExpiredNextTokenException",
		Message:    "The pagination token is expired.",
		HTTPStatus: http.StatusBadRequest,
	}
}
