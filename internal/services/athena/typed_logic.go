package athena

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Page-size caps, per operation's MaxResults range in the model. A request
// above the cap is clamped rather than refused, as the other catalog services
// here do.
const (
	queryExecutionsPageSize    = 50 // MaxQueryExecutionsCount
	namedQueriesPageSize       = 50 // MaxNamedQueriesCount
	workGroupsPageSize         = 50 // MaxWorkGroupsCount
	preparedStatementsPageSize = 50 // MaxPreparedStatementsCount
	dataCatalogsPageSize       = 50 // MaxDataCatalogsCount
	databasesPageSize          = 50 // MaxDatabasesCount
	tableMetadataPageSize      = 50 // MaxTableMetadataCount
	engineVersionsPageSize     = 10 // MaxEngineVersionsCount
	// batchGetMax is the most IDs or names a BatchGet* call accepts.
	batchGetMax = 50
)

// Client request token length, from the model's IdempotencyToken shape.
const (
	minTokenLength = 32
	maxTokenLength = 128
)

// now is the clock in epoch seconds with millisecond precision, the AWS JSON
// timestamp encoding.
func (s *Service) now() float64 { return float64(s.clk.Now().UnixMilli()) / 1000.0 }

// lock takes the record lock for key and returns its release.
func (s *Service) lock(key string) func() { return s.locks.Lock(key) }

func paginate[T any](items []T, maxResults int32, token string, limit int) (serviceutil.Page[T], *protocol.AWSError) {
	page, err := serviceutil.Paginate(items, int(maxResults), token,
		serviceutil.PaginateOptions{DefaultLimit: limit, MaxLimit: limit})
	if err != nil { // serviceutil.ErrInvalidPageToken, its only error
		return page, errInvalidRequest("Invalid NextToken.")
	}
	return page, nil
}

// requireBatch validates a BatchGet* list: it is required and holds at most
// batchGetMax entries.
func requireBatch(member string, n int) *protocol.AWSError {
	if n == 0 {
		return errRequired(member)
	}
	if n > batchGetMax {
		return errInvalidRequest("%s must contain at most %d items.", member, batchGetMax)
	}
	return nil
}

// idempotent runs create at most once per client request token.
//
// "If another request is received, the same response is returned and another
// query is not created. An error is returned if a parameter ... has changed."
// request is the call's input with the token cleared; its fingerprint is what
// a retry has to match. A call with no token is not deduplicated: the SDKs
// generate one for every call, so only a hand-built request arrives without.
func (s *Service) idempotent(ctx context.Context, operation, token string, request any, create func() (string, *protocol.AWSError)) (string, *protocol.AWSError) {
	if token == "" {
		return create()
	}
	if n := len(token); n < minTokenLength || n > maxTokenLength {
		return "", errInvalidRequest("ClientRequestToken must be between %d and %d characters long.", minTokenLength, maxTokenLength)
	}
	fingerprint, err := requestFingerprint(request)
	if err != nil {
		return "", errInternal(err)
	}
	defer s.lock("token:" + idempotencyKey(operation, token))()
	prior, err := s.store.getIdempotency(ctx, operation, token)
	if err != nil {
		return "", errInternal(err)
	}
	if prior != nil {
		if prior.Fingerprint != fingerprint {
			return "", errInvalidRequest("Idempotent parameters do not match.")
		}
		return prior.ResourceID, nil
	}
	id, aerr := create()
	if aerr != nil {
		return "", aerr
	}
	if err := s.store.putIdempotency(ctx, operation, token, &idempotencyRecord{ResourceID: id, Fingerprint: fingerprint}); err != nil {
		return "", errInternal(err)
	}
	return id, nil
}

func requestFingerprint(request any) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// requireMembers reports the first of the named string members, in order,
// that the caller left empty.
func requireMembers(members ...[2]string) *protocol.AWSError {
	for _, m := range members {
		if m[1] == "" {
			return errRequired(m[0])
		}
	}
	return nil
}

// batchGet looks each key up with get and splits what it found from what it
// could not return, which miss renders as the operation's Unprocessed*
// entry. A client error on one key is a miss; a server error fails the call.
func batchGet[T, U any](keys []string, get func(key string) (*T, *protocol.AWSError), miss func(key string, aerr *protocol.AWSError) U) ([]T, []U, *protocol.AWSError) {
	found, missed := make([]T, 0, len(keys)), []U{}
	for _, key := range keys {
		v, aerr := get(key)
		if aerr == nil {
			found = append(found, *v)
			continue
		}
		if aerr.HTTPStatus >= http.StatusInternalServerError {
			return nil, nil, aerr
		}
		missed = append(missed, miss(key, aerr))
	}
	return found, missed, nil
}
