package dynamodb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

const nsTables = "dynamodb:tables"

// StreamSpecification describes the DynamoDB Streams configuration for a table.
type StreamSpecification struct {
	StreamEnabled  bool   `json:"StreamEnabled"`
	StreamViewType string `json:"StreamViewType,omitempty"`
}

// TimeToLiveSpecification describes the TTL configuration for a table.
type TimeToLiveSpecification struct {
	Enabled       bool   `json:"Enabled"`
	AttributeName string `json:"AttributeName"`
}

// TimeToLiveDescription matches the AWS DescribeTimeToLive response shape.
type TimeToLiveDescription struct {
	TimeToLiveStatus string `json:"TimeToLiveStatus"`
	AttributeName    string `json:"AttributeName,omitempty"`
}

// BillingModeSummary contains the billing mode and last transition time.
type BillingModeSummary struct {
	BillingMode               string  `json:"BillingMode"`
	LastUpdateToPayPerRequest float64 `json:"LastUpdateToPayPerRequestDateTime,omitempty"`
}

// ProvisionedThroughput holds the read/write capacity units for a table or GSI.
type ProvisionedThroughput struct {
	ReadCapacityUnits  int64 `json:"ReadCapacityUnits"`
	WriteCapacityUnits int64 `json:"WriteCapacityUnits"`
}

func (t *Table) GetTags() map[string]string     { return t.Tags }
func (t *Table) SetTags(tags map[string]string) { t.Tags = tags }

// Table represents a DynamoDB table definition as persisted in the store.
// TTL, Tags, and BillingMode are store-only: real AWS never returns any of
// them through TableDescription (CreateTable/DescribeTable/UpdateTable/
// DeleteTable) — TTL is read back through DescribeTimeToLive, Tags through
// ListTagsOfResource, and the billing mode only through the nested
// BillingModeSummary — so responses are built from the description()
// projection below, not from this struct directly.
type Table struct {
	TableName              string                   `json:"TableName"`
	KeySchema              []KeySchemaElement       `json:"KeySchema"`
	AttributeDefinitions   []AttributeDef           `json:"AttributeDefinitions"`
	TableStatus            string                   `json:"TableStatus"`
	BillingMode            string                   `json:"BillingMode,omitempty"`
	BillingModeSummary     *BillingModeSummary      `json:"BillingModeSummary,omitempty"`
	ProvisionedThroughput  *ProvisionedThroughput   `json:"ProvisionedThroughput,omitempty"`
	TableARN               string                   `json:"TableArn"`
	TableId                string                   `json:"TableId,omitempty"`
	CreationDateTime       float64                  `json:"CreationDateTime"`
	ItemCount              int64                    `json:"ItemCount"`
	StreamSpecification    *StreamSpecification     `json:"StreamSpecification,omitempty"`
	LatestStreamArn        string                   `json:"LatestStreamArn,omitempty"`
	LatestStreamLabel      string                   `json:"LatestStreamLabel,omitempty"`
	TTL                    *TimeToLiveSpecification `json:"TTL,omitempty"`
	TTLTransitionAt        int64                    `json:"TTLTransitionAt,omitempty"`
	GlobalSecondaryIndexes []SecondaryIndex         `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes  []SecondaryIndex         `json:"LocalSecondaryIndexes,omitempty"`
	Tags                   map[string]string        `json:"Tags,omitempty"`
}

// TableDescription is the wire shape of the TableDescription/Table member
// that CreateTable, DescribeTable, UpdateTable, and DeleteTable all return
// (see dynamodb-2012-08-10.json#TableDescription, 28 members). It mirrors
// every field Table actually populates on the wire, except TTL, Tags, and
// BillingMode: real AWS never exposes those through TableDescription — TTL
// is DescribeTimeToLive's member, Tags is ListTagsOfResource's, and the
// billing mode appears only nested inside BillingModeSummary — Table keeps
// them only so they round-trip through the store (see Table's doc comment).
type TableDescription struct {
	TableName              string                 `json:"TableName"`
	KeySchema              []KeySchemaElement     `json:"KeySchema"`
	AttributeDefinitions   []AttributeDef         `json:"AttributeDefinitions"`
	TableStatus            string                 `json:"TableStatus"`
	BillingModeSummary     *BillingModeSummary    `json:"BillingModeSummary,omitempty"`
	ProvisionedThroughput  *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	TableARN               string                 `json:"TableArn"`
	TableId                string                 `json:"TableId,omitempty"`
	CreationDateTime       float64                `json:"CreationDateTime"`
	ItemCount              int64                  `json:"ItemCount"`
	StreamSpecification    *StreamSpecification   `json:"StreamSpecification,omitempty"`
	LatestStreamArn        string                 `json:"LatestStreamArn,omitempty"`
	LatestStreamLabel      string                 `json:"LatestStreamLabel,omitempty"`
	GlobalSecondaryIndexes []SecondaryIndex       `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes  []SecondaryIndex       `json:"LocalSecondaryIndexes,omitempty"`
}

// description projects t onto the wire TableDescription shape, dropping the
// store-only TTL, Tags, and BillingMode fields — see TableDescription's doc
// comment.
func (t *Table) description() *TableDescription {
	if t == nil {
		return nil
	}
	return &TableDescription{
		TableName:              t.TableName,
		KeySchema:              t.KeySchema,
		AttributeDefinitions:   t.AttributeDefinitions,
		TableStatus:            t.TableStatus,
		BillingModeSummary:     t.BillingModeSummary,
		ProvisionedThroughput:  t.ProvisionedThroughput,
		TableARN:               t.TableARN,
		TableId:                t.TableId,
		CreationDateTime:       t.CreationDateTime,
		ItemCount:              t.ItemCount,
		StreamSpecification:    t.StreamSpecification,
		LatestStreamArn:        t.LatestStreamArn,
		LatestStreamLabel:      t.LatestStreamLabel,
		GlobalSecondaryIndexes: t.GlobalSecondaryIndexes,
		LocalSecondaryIndexes:  t.LocalSecondaryIndexes,
	}
}

// Projection describes which attributes are projected into a secondary index.
type Projection struct {
	ProjectionType   string   `json:"ProjectionType"` // ALL, KEYS_ONLY, INCLUDE
	NonKeyAttributes []string `json:"NonKeyAttributes,omitempty"`
}

// SecondaryIndex represents a GSI or LSI definition.
type SecondaryIndex struct {
	IndexName             string                 `json:"IndexName"`
	KeySchema             []KeySchemaElement     `json:"KeySchema"`
	Projection            Projection             `json:"Projection"`
	IndexArn              string                 `json:"IndexArn,omitempty"`
	IndexStatus           string                 `json:"IndexStatus,omitempty"`
	IndexSizeBytes        int64                  `json:"IndexSizeBytes"`
	ItemCount             int64                  `json:"ItemCount"`
	ProvisionedThroughput *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
}

// streamEnabled reports whether this table has an active stream.
func (t *Table) streamEnabled() bool {
	return t.StreamSpecification != nil && t.StreamSpecification.StreamEnabled
}

// TimeToLiveStatus values, as modeled by AWS
// (dynamodb-2012-08-10.json#TimeToLiveStatus).
const (
	ttlStatusEnabling  = "ENABLING"
	ttlStatusDisabling = "DISABLING"
	ttlStatusEnabled   = "ENABLED"
	ttlStatusDisabled  = "DISABLED"
)

// ttlTransitionPending reports whether the TTL change recorded in TTL is still
// processing at now. TTLTransitionAt is zero on a settled table — and on every
// record written before transitions were modeled, which is what makes the
// field additive: an old row decodes as settled and keeps the status its
// Enabled flag already implied.
func (t *Table) ttlTransitionPending(now time.Time) bool {
	return t.TTLTransitionAt > 0 && now.UnixNano() < t.TTLTransitionAt
}

// ttlStatus derives the TimeToLiveStatus a client sees, from the target
// configuration in TTL and the transition deadline in TTLTransitionAt.
//
// It is a pure function of the record and the clock rather than a persisted
// status string, so a read is never racing the scheduled callback that settles
// the record (handler_ttl.go's settleTTLTransition): once the deadline has
// passed, every reader agrees on the terminal status whether or not that
// callback has run yet. That is what keeps a test deterministic when it
// advances a mock clock and reads back over HTTP, with no scheduler handle to
// settle against.
func (t *Table) ttlStatus(now time.Time) string {
	enabled := t.TTL != nil && t.TTL.Enabled
	if t.ttlTransitionPending(now) {
		if enabled {
			return ttlStatusEnabling
		}
		return ttlStatusDisabling
	}
	if enabled {
		return ttlStatusEnabled
	}
	return ttlStatusDisabled
}

// ttlActive reports whether the sweeper may expire this table's items. AWS
// only begins expiring items once the status reaches ENABLED, so an enable
// that is still processing — and a disable that is not finished — expires
// nothing.
func (t *Table) ttlActive(now time.Time) bool {
	return t.ttlStatus(now) == ttlStatusEnabled
}

// ttlDescription returns the TTL description for DescribeTimeToLive responses.
// AttributeName is omitted only once the table has settled on DISABLED: a
// DISABLING table still names the attribute it is switching off.
func (t *Table) ttlDescription(now time.Time) *TimeToLiveDescription {
	status := t.ttlStatus(now)
	desc := &TimeToLiveDescription{TimeToLiveStatus: status}
	if status != ttlStatusDisabled && t.TTL != nil {
		desc.AttributeName = t.TTL.AttributeName
	}
	return desc
}

// streamViewType returns the configured view type, or "" when streams are off.
func (t *Table) streamViewType() string {
	if t.StreamSpecification == nil {
		return ""
	}
	return t.StreamSpecification.StreamViewType
}

// KeySchemaElement is a hash or range key definition.
type KeySchemaElement struct {
	AttributeName string `json:"AttributeName"`
	KeyType       string `json:"KeyType"` // "HASH" or "RANGE"
}

// AttributeDef defines an attribute type for key schema elements.
type AttributeDef struct {
	AttributeName string `json:"AttributeName"`
	AttributeType string `json:"AttributeType"` // "S", "N", "B"
}

// hashKeyName returns the partition key name for the table.
func (t *Table) hashKeyName() string {
	for _, k := range t.KeySchema {
		if k.KeyType == "HASH" {
			return k.AttributeName
		}
	}
	return ""
}

// sortKeyName returns the sort key name for the table, or "" if none.
func (t *Table) sortKeyName() string {
	for _, k := range t.KeySchema {
		if k.KeyType == "RANGE" {
			return k.AttributeName
		}
	}
	return ""
}

// findIndex looks up a GSI or LSI by name. Returns nil if not found.
func (t *Table) findIndex(name string) *SecondaryIndex {
	for i := range t.GlobalSecondaryIndexes {
		if t.GlobalSecondaryIndexes[i].IndexName == name {
			return &t.GlobalSecondaryIndexes[i]
		}
	}
	for i := range t.LocalSecondaryIndexes {
		if t.LocalSecondaryIndexes[i].IndexName == name {
			return &t.LocalSecondaryIndexes[i]
		}
	}
	return nil
}

// isGSI reports whether name identifies one of table's
// GlobalSecondaryIndexes, as opposed to a LocalSecondaryIndex or no index at
// all. The read path uses this to route GSI Query/Scan onto the ordered
// index structure (dynamodb-gsi-design.md §4) while LSI Query/Scan keeps the
// existing full-scan fallback unchanged — LSI routing is called out in §5 as
// separable follow-up work, not part of this flip, because LSIs have no
// equivalent index storage (index_maintenance.go's diffIndexMutations only
// ever iterates table.GlobalSecondaryIndexes, never LocalSecondaryIndexes).
func (t *Table) isGSI(name string) bool {
	for i := range t.GlobalSecondaryIndexes {
		if t.GlobalSecondaryIndexes[i].IndexName == name {
			return true
		}
	}
	return false
}

// indexHashKeyName returns the partition key name for a secondary index.
func indexHashKeyName(idx *SecondaryIndex) string {
	for _, k := range idx.KeySchema {
		if k.KeyType == "HASH" {
			return k.AttributeName
		}
	}
	return ""
}

// indexSortKeyName returns the sort key name for a secondary index, or "" if none.
func indexSortKeyName(idx *SecondaryIndex) string {
	for _, k := range idx.KeySchema {
		if k.KeyType == "RANGE" {
			return k.AttributeName
		}
	}
	return ""
}

// DynamoDB attribute value: {"S": "foo"} or {"N": "123"} etc.
type attrValue = map[string]any

// Item is a DynamoDB item represented in DynamoDB JSON format.
type Item = map[string]attrValue

// dynamoStore wraps state.Store (for table metadata), an itemBackend
// (for item data), and a streamBackend (for stream records).
type dynamoStore struct {
	tables        state.Store   // table descriptors
	items         itemBackend   // item data — memItemBackend or sqlItemBackend
	streams       streamBackend // stream records — memStreamBackend or sqlStreamBackend
	defaultRegion string
}

func newDynamoStore(tables state.Store, items itemBackend, streams streamBackend, defaultRegion string) *dynamoStore {
	return &dynamoStore{tables: tables, items: items, streams: streams, defaultRegion: defaultRegion}
}

// region extracts the per-request region from context, falling back to the default.
func (s *dynamoStore) region(ctx context.Context) string {
	return middleware.RegionFromContext(ctx, s.defaultRegion)
}

// tableKey is the single place a DynamoDB table name becomes a storage key.
//
// A DynamoDB table is a regional resource, so every keyspace that hangs off a
// table — the table descriptor in nsTables, item rows, GSI index entries and
// stream records — is keyed by "<region>/<tableName>" rather than by the bare
// name. Two same-named tables in different regions therefore never share a
// key, which is what makes their items, index entries and streams independent
// (issue #673).
//
// This mirrors Kinesis, whose record keys are "<region>/<streamName>/..."
// (internal/services/kinesis/store.go's nsRecords) — the region is folded into
// the composite key at the one place that key is built, rather than threaded
// through every backend method as a separate parameter. That matters here
// because itemBackend has ~20 methods across two implementations: keeping the
// qualification in dynamoStore means the backends stay unaware of regions and
// no call site can forget one.
//
// The separator is "/" for the same reason serviceutil.RegionKey uses it: an
// AWS region never contains one, and neither does a DynamoDB table name
// (AWS restricts them to [a-zA-Z0-9_.-]), so the split is unambiguous.
func (s *dynamoStore) tableKey(ctx context.Context, tableName string) string {
	return serviceutil.RegionKey(s.region(ctx), tableName)
}

// ---- Table helpers ---------------------------------------------------------

func (s *dynamoStore) getTable(ctx context.Context, name string) (*Table, *protocol.AWSError) {
	key := s.tableKey(ctx, name)
	raw, found, err := s.tables.Get(ctx, nsTables, key)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if !found {
		return nil, errTableNotFound(name)
	}
	var t Table
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return &t, nil
}

func (s *dynamoStore) putTable(ctx context.Context, t *Table) *protocol.AWSError {
	raw, err := json.Marshal(t)
	if err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	key := s.tableKey(ctx, t.TableName)
	if err := s.tables.Set(ctx, nsTables, key, string(raw)); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *dynamoStore) tableExists(ctx context.Context, name string) (bool, *protocol.AWSError) {
	key := s.tableKey(ctx, name)
	_, found, err := s.tables.Get(ctx, nsTables, key)
	if err != nil {
		return false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return found, nil
}

// listTables returns the tables in ctx's region whose names start with prefix,
// in key order (which, the region prefix being constant, is table-name order —
// what ListTables' pagination cursor relies on).
//
// One Scan rather than List-then-Get-per-key: state.Store.Scan is documented as
// the read to prefer when both keys and values are wanted, it holds the store
// lock once, and it removes the TOCTOU window the old List+Get pair had to
// special-case. A record whose JSON will not decode is skipped rather than
// failing the whole listing, per AGENTS.md § "Malformed persisted state must be
// isolated".
func (s *dynamoStore) listTables(ctx context.Context, prefix string) ([]*Table, *protocol.AWSError) {
	pairs, err := s.tables.Scan(ctx, nsTables, s.tableKey(ctx, prefix))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	tables := make([]*Table, 0, len(pairs))
	for _, kv := range pairs {
		var t Table
		if err := json.Unmarshal([]byte(kv.Value), &t); err != nil {
			continue // malformed table record — isolate, skip
		}
		tables = append(tables, &t)
	}
	return tables, nil
}

// ---- Item helpers ----------------------------------------------------------

// extractKeyValue extracts the scalar string value from a DynamoDB attribute
// node. e.g. {"S": "foo"} → "foo", {"N": "42"} → "42", {"B": <bytes>} →
// base64 text. Delegates to scalarString (expr.go) so a Binary key resolves
// to the identical string whichever protocol decoded the request — see
// scalarString's doc comment (issue #1999).
func extractKeyValue(attr attrValue) string {
	for _, v := range attr {
		if s, ok := scalarString(v); ok {
			return s
		}
	}
	return ""
}

// resolveKeys extracts the (hashKey, sortKey) pair from a key or item map
// using the table's key schema.  sortKey is "" for hash-only tables.
func resolveKeys(table *Table, keyOrItem Item) (hashKey, sortKey string, aerr *protocol.AWSError) {
	hashName := table.hashKeyName()
	if hashName == "" {
		return "", "", &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Table has no hash key defined.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	hashAttr, ok := keyOrItem[hashName]
	if !ok {
		return "", "", &protocol.AWSError{
			Code:       "ValidationException",
			Message:    fmt.Sprintf("The provided key element does not match the schema: missing hash key %q", hashName),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	hashKey = extractKeyValue(hashAttr)

	sortName := table.sortKeyName()
	if sortName != "" {
		if sortAttr, ok := keyOrItem[sortName]; ok {
			sortKey = extractKeyValue(sortAttr)
		}
	}
	return hashKey, sortKey, nil
}

// resolveStorageKeys extracts and encodes the (hashKey, sortKey) pair for
// storage/indexing purposes: Number-typed key components are transformed via
// encodeOrderableNumber so the backend's composite/ORDER BY key sorts
// numerically (docs/plans/dynamodb-gsi-design.md §2); String/Binary
// components pass through unchanged, since their raw form already sorts
// correctly (UTF-8 byte order). This is the single choke point every
// storage call site (put/get/delete/scanPage cursor resolution) goes
// through so memItemBackend/sqlItemBackend never need to know about table
// schema or key types — they just store/compare whatever strings they're
// given.
//
// resolveKeys itself is left returning raw (unencoded) values — callers that
// need the original decimal text (e.g. debug display) call it directly; only
// storage callers need the encoded form.
func resolveStorageKeys(table *Table, keyOrItem Item) (hashKey, sortKey string, aerr *protocol.AWSError) {
	hashKey, sortKey, aerr = resolveKeys(table, keyOrItem)
	if aerr != nil {
		return "", "", aerr
	}
	hashKey = encodeStorageKeyComponent(keyAttrType(table, table.hashKeyName()), hashKey)
	if sortName := table.sortKeyName(); sortName != "" {
		sortKey = encodeStorageKeyComponent(keyAttrType(table, sortName), sortKey)
	}
	return hashKey, sortKey, nil
}

func (s *dynamoStore) putItem(ctx context.Context, table *Table, item Item) *protocol.AWSError {
	hashKey, sortKey, aerr := resolveStorageKeys(table, item)
	if aerr != nil {
		return aerr
	}
	if err := s.items.put(ctx, s.tableKey(ctx, table.TableName), hashKey, sortKey, item); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// putItemWithIndexMaintenance is putItem plus GSI index-row maintenance
// (dynamodb-gsi-design.md section 3): oldItem is the item's previous value
// (nil if it didn't previously exist), used to diff against item and decide
// which of table's GSI index rows need to change. The base-item write and
// every resulting index-row write happen atomically (one *sql.Tx on the SQL
// backend, one mutex critical section on the memory backend — see
// item_store.go's putWithIndexMutations).
//
// Callers (PutItem, UpdateItem, BatchWriteItem) are responsible for having
// already fetched oldItem when table has any GSIs — see each handler's own
// comment for why that read is free (already needed for streams/
// ReturnValues, or for UpdateItem's upsert semantics) rather than a new
// cost this adds.
func (s *dynamoStore) putItemWithIndexMaintenance(ctx context.Context, table *Table, item, oldItem Item) *protocol.AWSError {
	hashKey, sortKey, aerr := resolveStorageKeys(table, item)
	if aerr != nil {
		return aerr
	}
	mutations := diffIndexMutations(table, oldItem, item)
	if err := s.items.putWithIndexMutations(ctx, s.tableKey(ctx, table.TableName), hashKey, sortKey, item, mutations); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

func (s *dynamoStore) getItem(ctx context.Context, table *Table, key Item) (Item, *protocol.AWSError) {
	hashKey, sortKey, aerr := resolveStorageKeys(table, key)
	if aerr != nil {
		return nil, aerr
	}
	item, _, err := s.items.get(ctx, s.tableKey(ctx, table.TableName), hashKey, sortKey)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return item, nil // nil means not found — handler returns 200 with empty Item
}

func (s *dynamoStore) deleteItem(ctx context.Context, table *Table, key Item) *protocol.AWSError {
	hashKey, sortKey, aerr := resolveStorageKeys(table, key)
	if aerr != nil {
		return aerr
	}
	if err := s.items.remove(ctx, s.tableKey(ctx, table.TableName), hashKey, sortKey); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// deleteItemWithIndexMaintenance is deleteItem plus GSI index-row cleanup:
// oldItem (the item's value before this delete, or nil if it never existed)
// is diffed against a nil "new" state, which — per diffIndexMutations —
// produces exactly one delete mutation per GSI whose key oldItem satisfied,
// and none for GSIs it didn't. Atomic with the base-item delete, same as
// putItemWithIndexMaintenance.
func (s *dynamoStore) deleteItemWithIndexMaintenance(ctx context.Context, table *Table, key, oldItem Item) *protocol.AWSError {
	hashKey, sortKey, aerr := resolveStorageKeys(table, key)
	if aerr != nil {
		return aerr
	}
	mutations := diffIndexMutations(table, oldItem, nil)
	if err := s.items.removeWithIndexMutations(ctx, s.tableKey(ctx, table.TableName), hashKey, sortKey, mutations); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// scanItems returns all items in the table via a single backend call.
func (s *dynamoStore) scanItems(ctx context.Context, tableName string) ([]Item, *protocol.AWSError) {
	items, err := s.items.scanAll(ctx, s.tableKey(ctx, tableName))
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return items, nil
}

// scanItemsPage returns up to limit items from the table via a single
// keyset-paginated backend call, ordered by (hashKey, sortKey) — the plain
// Scan fast path for storage-access-plan.md A3. exclusiveStartKey is the raw
// DynamoDB ExclusiveStartKey item (nil means "start of table"); it is
// resolved to the backend's (hashKey, sortKey) cursor via the table's key
// schema, exactly like putItem/getItem/deleteItem already do.
//
// hasMore reports whether items beyond the returned page exist. The backend
// is asked for one extra item (limit+1) to answer this without a second
// round trip — the same "peek one ahead" trick state.MemoryStore/SQLiteStore
// ScanPage callers use elsewhere in this codebase.
func (s *dynamoStore) scanItemsPage(ctx context.Context, table *Table, exclusiveStartKey Item, limit int) (items []Item, hasMore bool, aerr *protocol.AWSError) {
	hasAfter := false
	var afterHash, afterSort string
	if exclusiveStartKey != nil {
		// Must encode identically to putItem/getItem/deleteItem
		// (resolveStorageKeys) — the cursor is compared against the same
		// encoded storage keys those calls wrote.
		h, sk, aerr := resolveStorageKeys(table, exclusiveStartKey)
		if aerr != nil {
			return nil, false, aerr
		}
		hasAfter, afterHash, afterSort = true, h, sk
	}

	fetched, err := s.items.scanPage(ctx, s.tableKey(ctx, table.TableName), hasAfter, afterHash, afterSort, limit+1)
	if err != nil {
		return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(fetched) > limit {
		return fetched[:limit], true, nil
	}
	return fetched, false, nil
}

// errIndexCursorMissingKeys is the rejection for an ExclusiveStartKey that
// does not carry the index's own key attributes. AWS requires an index
// query's cursor to name both the table's primary key and the index key,
// because the index key alone is not unique — without it there is no
// position in the index to resume from.
func errIndexCursorMissingKeys(idx *SecondaryIndex) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    fmt.Sprintf("The provided starting key is missing required keys for index %q", idx.IndexName),
		HTTPStatus: http.StatusBadRequest,
	}
}

// scanIndexPage returns up to limit entries from a GSI's ordered index
// structure via a single keyset-paginated backend call
// (dynamodb-gsi-design.md §4) — the GSI-Scan analogue of scanItemsPage.
// exclusiveStartKey is the raw LastEvaluatedKey-shaped item (table PK plus
// index key attributes, per extractItemKeysWithIndex); it is resolved to the
// backend's 4-tuple (indexHash, indexSort, baseHash, baseSort) cursor via
// idx's key schema (indexKeyComponents) and table's own key schema
// (resolveStorageKeys), the same encode-then-compare choke point every other
// storage call site goes through.
func (s *dynamoStore) scanIndexPage(ctx context.Context, table *Table, idx *SecondaryIndex, exclusiveStartKey Item, limit int) (items []Item, hasMore bool, aerr *protocol.AWSError) {
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

	fetched, err := s.items.scanIndexPage(ctx, s.tableKey(ctx, table.TableName), idx.IndexName, hasAfter, afterIndexHash, afterIndexSort, afterBaseHash, afterBaseSort, limit+1)
	if err != nil {
		return nil, false, protocol.Wrap(protocol.ErrInternalError, err)
	}
	if len(fetched) > limit {
		return fetched[:limit], true, nil
	}
	return fetched, false, nil
}

// scanIndexByHash returns every entry stored for (table, idx) sharing the
// index's hash-key value hashVal — an O(k) partition read into the GSI's
// ordered index structure (dynamodb-gsi-design.md §4), the GSI-Query
// analogue of scanItemsByHashKey. hashVal is the raw (unencoded) hash-key
// scalar value; it is encoded per idx's hash-key type before use as the
// storage lookup key, mirroring scanItemsByHashKey's own encoding of the
// table's hash key.
func (s *dynamoStore) scanIndexByHash(ctx context.Context, table *Table, idx *SecondaryIndex, hashVal string) ([]Item, *protocol.AWSError) {
	storageHashVal := encodeStorageKeyComponent(keyAttrType(table, indexHashKeyName(idx)), hashVal)
	items, err := s.items.queryIndexByHash(ctx, s.tableKey(ctx, table.TableName), idx.IndexName, storageHashVal)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return items, nil
}

// applyIndexMutations applies GSI index-row mutations with no accompanying
// base-item write — the backfill path (UpdateTable adding a GSI to a table
// that already has items). Routed through dynamoStore rather than called on
// s.items directly so that the region-qualified storage key is minted in
// exactly one place (tableKey) and a backfill can never land its rows in a
// different region's partition from the writes that follow it.
func (s *dynamoStore) applyIndexMutations(ctx context.Context, tableName string, mutations []indexMutation) *protocol.AWSError {
	if err := s.items.applyIndexMutations(ctx, s.tableKey(ctx, tableName), mutations); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// deleteIndexEntriesForIndex removes every index row for one (table, index) —
// called when UpdateTable drops a GSI, so a GSI later recreated under the same
// name does not inherit stale rows. Region-qualified for the same reason as
// applyIndexMutations.
func (s *dynamoStore) deleteIndexEntriesForIndex(ctx context.Context, tableName, indexName string) *protocol.AWSError {
	if err := s.items.deleteAllIndexEntriesForIndex(ctx, s.tableKey(ctx, tableName), indexName); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// scanExpiredTTL returns only items whose TTL attribute is expired (> 0 and <= cutoffUnix).
func (s *dynamoStore) scanExpiredTTL(ctx context.Context, tableName, ttlAttr string, cutoffUnix int64) ([]Item, *protocol.AWSError) {
	items, err := s.items.scanExpiredTTL(ctx, s.tableKey(ctx, tableName), ttlAttr, cutoffUnix)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return items, nil
}

// countItems returns the live item count for a table without loading item values.
func (s *dynamoStore) countItems(ctx context.Context, tableName string) (int64, *protocol.AWSError) {
	n, err := s.items.count(ctx, s.tableKey(ctx, tableName))
	if err != nil {
		return 0, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return n, nil
}

// scanItemsByHashKey returns all items in a partition (hash key equality) via
// a single backend call — O(k) where k is the partition size. hashVal is the
// raw (unencoded) hash-key scalar value; it is encoded per the table's hash
// key type before use as the storage lookup key, mirroring
// resolveStorageKeys, so a Number-typed hash key looks up the same encoded
// rows putItem wrote (docs/plans/dynamodb-gsi-design.md §2).
func (s *dynamoStore) scanItemsByHashKey(ctx context.Context, table *Table, hashVal string) ([]Item, *protocol.AWSError) {
	storageHashVal := encodeStorageKeyComponent(keyAttrType(table, table.hashKeyName()), hashVal)
	items, err := s.items.queryByHash(ctx, s.tableKey(ctx, table.TableName), storageHashVal)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return items, nil
}

// ---- Stream helpers -------------------------------------------------------

// appendStreamRecord adds a stream change record for a table.
func (s *dynamoStore) appendStreamRecord(ctx context.Context, tableName string, r *StreamRecord) *protocol.AWSError {
	if err := s.streams.append(ctx, s.tableKey(ctx, tableName), r); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// getStreamRecordsSince returns stream records with SequenceNumber > afterSeq.
func (s *dynamoStore) getStreamRecordsSince(ctx context.Context, tableName string, afterSeq int64, limit int) ([]*StreamRecord, *protocol.AWSError) {
	recs, err := s.streams.since(ctx, s.tableKey(ctx, tableName), afterSeq, limit)
	if err != nil {
		return nil, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return recs, nil
}

// latestStreamSeq returns the highest sequence number stored for the table.
func (s *dynamoStore) latestStreamSeq(ctx context.Context, tableName string) (int64, *protocol.AWSError) {
	seq, err := s.streams.latest(ctx, s.tableKey(ctx, tableName))
	if err != nil {
		return 0, protocol.Wrap(protocol.ErrInternalError, err)
	}
	return seq, nil
}

// deleteTable removes a table descriptor, all its items, its GSI index
// entries, and its stream records — AWS deletes a table's stream along with
// the table, and a table recreated under the same name gets a new, empty
// stream rather than the deleted one's history.
//
// All four keyspaces are addressed by the same region-qualified key, so
// deleting a table in one region leaves a same-named table in another region
// entirely untouched.
func (s *dynamoStore) deleteTable(ctx context.Context, name string) *protocol.AWSError {
	key := s.tableKey(ctx, name)
	if err := s.items.deleteAll(ctx, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.items.deleteAllIndexEntriesForTable(ctx, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.streams.deleteAll(ctx, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	if err := s.tables.Delete(ctx, nsTables, key); err != nil {
		return protocol.Wrap(protocol.ErrInternalError, err)
	}
	return nil
}

// ---- cross-region scan helpers (for background goroutines) -----------------

// scanAllTables returns all tables across all regions. Each returned KV has a
// region-prefixed key (e.g. "us-east-1/myTable").
func (s *dynamoStore) scanAllTables(ctx context.Context) ([]state.KV, error) {
	return s.tables.Scan(ctx, nsTables, "")
}

// ---- Error sentinels -------------------------------------------------------

func errTableNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Requested resource not found: Table: %s not found", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

func errTableExists(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceInUseException",
		Message:    fmt.Sprintf("Table already exists: %s", name),
		HTTPStatus: http.StatusBadRequest,
	}
}
