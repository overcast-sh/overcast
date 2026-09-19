package dynamodb

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Parallel-scan segmentation over the ordered storage structure
// (docs/plans/dynamodb-gsi-design.md §5's deferred follow-up).
//
// AWS defines a Scan segment as a fixed portion of the key space: real
// DynamoDB divides the partition-key hash space between segments, so an
// item's segment follows from that item's own key and nothing else. Two
// consequences a client depends on: segments are disjoint and together
// cover the table, and an item does not migrate between segments while a
// scan is in flight just because other items were written.
//
// The pre-existing implementation read the whole table, sorted it, and gave
// each segment a positional slice of the sorted list. That satisfies
// disjoint-and-complete only for a table frozen for the duration of the
// scan: segment boundaries were derived from the item *count*, so a single
// insert re-sliced the list and moved existing items across boundaries —
// the classic duplicate-or-skip failure for a worker-per-segment client. It
// also cost O(table) reads plus an O(n log n) sort per segment, so a
// TotalSegments=N scan did N times the work of a serial one.
//
// Segmenting by hash of the key fixes both. Membership is a pure function
// of the item's own partition key (the index's partition key, for an index
// scan), so it is stable under concurrent writes by construction, and it
// keeps all items sharing a partition key in one segment exactly as real
// DynamoDB's hash-space division does. The read becomes a bounded keyset
// walk over the structure the storage layer already keeps in key order —
// the same scanPage/scanIndexPage primitives storage-access-plan.md A3 and
// the GSI read-path flip use — so no full materialization and no sort: a
// page stops as soon as it has enough items for the segment, rather than
// after touching every item in the table.
//
// A dedicated ordered structure keyed by segment would let a segment seek
// straight to its own contiguous range, but it would need its own storage
// and migration; the walk here reuses what exists and is bounded by page
// size rather than table size, which is the cost §5 flagged.

// segmentWalkChunk sizes each backend page during a segment walk. With
// segments assigned by hash, roughly one row in totalSegments belongs to
// the caller's segment, so limit*totalSegments rows is the expected number
// to touch for a full page — bounded at both ends so a tiny Limit still
// amortizes the per-call overhead and a large one cannot pull an unbounded
// slab into memory.
func segmentWalkChunk(limit, totalSegments int) int {
	const (
		minChunk = 64
		maxChunk = 1024
	)
	n := limit * totalSegments
	if n < minChunk {
		return minChunk
	}
	if n > maxChunk {
		return maxChunk
	}
	return n
}

// segmentForKey maps a storage-encoded partition key to one of
// totalSegments segments. FNV-1a over the encoded key: deterministic across
// processes and restarts (no map iteration or address-derived input), and
// spread well enough that segments stay comparably sized for the sequential
// and prefixed key patterns emulator workloads actually use.
//
// Callers pass the *storage-encoded* key (encodeStorageKeyComponent's
// output) so that a Number-typed key hashes by the same bytes the ordered
// structure is keyed by — one representation, one segment, whichever call
// site asks.
//
// The result is always in [0, totalSegments); validateScanSegments has
// already rejected a request whose Segment falls outside that range, so a
// segment that matches nothing here is genuinely empty.
func segmentForKey(encodedHashKey string, totalSegments int) int {
	if totalSegments <= 1 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(encodedHashKey)) // hash.Hash.Write never returns an error
	return int(h.Sum64() % uint64(totalSegments))
}

// validateScanSegments rejects a parallel-scan request whose Segment is not
// below its TotalSegments (issue #1707, rule 9). The API reference states
// the constraint — "The value for Segment must be greater than or equal to
// 0, and less than the value provided for TotalSegments" — and the message
// is the one moto's scan raises for it. Segment IDs are zero-based, so
// Segment == TotalSegments names a segment that does not exist and used to
// be answered with an empty page (see segmentForKey).
//
// Only the relationship is checked. A TotalSegments of zero is what an
// omitted parameter decodes to and means a sequential scan; the lower and
// upper bounds of each parameter on its own, and the rule that the two must
// be supplied together, are separate constraints not modeled here.
func validateScanSegments(segment, totalSegments int) *protocol.AWSError {
	if totalSegments >= 1 && segment >= totalSegments {
		return errValidation(fmt.Sprintf(
			"The Segment parameter is zero-based and must be less than parameter TotalSegments: Segment: %d is not less than TotalSegments: %d",
			segment, totalSegments))
	}
	return nil
}

// scanItemsSegmentPage returns up to limit items from the table that belong
// to segment (of totalSegments), starting after exclusiveStartKey — the
// parallel-scan analogue of scanItemsPage.
//
// hasMore reports whether any row remains beyond the last one examined, not
// whether that row belongs to this segment: answering the stronger question
// would mean walking the whole tail to prove a segment is finished. The
// weaker answer costs a client at most one extra request returning an empty
// page, which is the same thing a FilterExpression that matches nothing on
// the final page already produces in real DynamoDB.
func (s *dynamoStore) scanItemsSegmentPage(ctx context.Context, table *Table, exclusiveStartKey Item, limit, segment, totalSegments int) (items []Item, hasMore bool, aerr *protocol.AWSError) {
	hasAfter := false
	var afterHash, afterSort string
	if exclusiveStartKey != nil {
		h, sk, aerr := resolveStorageKeys(table, exclusiveStartKey)
		if aerr != nil {
			return nil, false, aerr
		}
		hasAfter, afterHash, afterSort = true, h, sk
	}

	chunk := segmentWalkChunk(limit, totalSegments)
	items = []Item{}
	for {
		fetched, err := s.items.scanPage(ctx, s.tableKey(ctx, table.TableName), hasAfter, afterHash, afterSort, chunk)
		if err != nil {
			return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
		}
		advanced := false
		for _, item := range fetched {
			h, sk, kerr := resolveStorageKeys(table, item)
			if kerr != nil {
				continue // malformed stored item: isolate it, keep the scan working
			}
			if len(items) >= limit {
				return items, true, nil
			}
			hasAfter, afterHash, afterSort = true, h, sk
			advanced = true
			if segmentForKey(h, totalSegments) != segment {
				continue
			}
			items = append(items, item)
		}
		if len(fetched) < chunk || !advanced {
			// Exhausted, or a whole chunk of items none of which yields a
			// cursor to seek past — without the second condition that would
			// re-fetch the same chunk forever. A stored item that cannot
			// produce its own storage key should be impossible (writes
			// validate keys), so this bounds the damage of a corrupt record
			// to a short page rather than a hung request.
			return items, false, nil
		}
	}
}

// scanIndexSegmentPage is scanItemsSegmentPage against a GSI's own ordered
// index structure: same walk, same segment semantics, but keyed by the
// index's partition key so a parallel index scan divides the *index's* key
// space the way real DynamoDB does. Entries come from the index, so they
// carry only the index's projected attributes and only items the
// sparse-write rule put there — the fidelity the non-parallel GSI scan path
// already had.
func (s *dynamoStore) scanIndexSegmentPage(ctx context.Context, table *Table, idx *SecondaryIndex, exclusiveStartKey Item, limit, segment, totalSegments int) (items []Item, hasMore bool, aerr *protocol.AWSError) {
	hasAfter := false
	var afterIndexHash, afterIndexSort, afterBaseHash, afterBaseSort string
	if exclusiveStartKey != nil {
		ih, is, ok := indexKeyComponents(table, idx, exclusiveStartKey)
		if !ok {
			return nil, false, errIndexCursorMissingKeys(idx)
		}
		bh, bs, aerr := resolveStorageKeys(table, exclusiveStartKey)
		if aerr != nil {
			return nil, false, aerr
		}
		hasAfter, afterIndexHash, afterIndexSort, afterBaseHash, afterBaseSort = true, ih, is, bh, bs
	}

	chunk := segmentWalkChunk(limit, totalSegments)
	items = []Item{}
	for {
		fetched, err := s.items.scanIndexPage(ctx, s.tableKey(ctx, table.TableName), idx.IndexName, hasAfter, afterIndexHash, afterIndexSort, afterBaseHash, afterBaseSort, chunk)
		if err != nil {
			return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
		}
		advanced := false
		for _, entry := range fetched {
			ih, is, ok := indexKeyComponents(table, idx, entry)
			if !ok {
				continue // malformed index row: isolate it, keep the scan working
			}
			bh, bs, kerr := resolveStorageKeys(table, entry)
			if kerr != nil {
				continue
			}
			if len(items) >= limit {
				return items, true, nil
			}
			hasAfter = true
			afterIndexHash, afterIndexSort, afterBaseHash, afterBaseSort = ih, is, bh, bs
			advanced = true
			if segmentForKey(ih, totalSegments) != segment {
				continue
			}
			items = append(items, entry)
		}
		if len(fetched) < chunk || !advanced {
			return items, false, nil // exhausted, or no cursor to advance past — see scanItemsSegmentPage
		}
	}
}
