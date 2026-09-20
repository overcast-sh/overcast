package dynamodb

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/lifecycle"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/op"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// Handler holds DynamoDB handler dependencies.
type Handler struct {
	cfg   *config.Config
	store *dynamoStore
	bus   *events.Bus
	log   *serviceutil.ServiceLogger
	clk   clock.Clock
	ops   map[string]http.HandlerFunc
	rawOp map[string]op.Operation

	// ttlSched drives the asynchronous ENABLING/DISABLING transitions
	// UpdateTimeToLive starts — see handler_ttl.go.
	ttlSched *lifecycle.Scheduler

	// ttlLocks keeps UpdateTimeToLive and settleTTLTransition from
	// interleaving on one table's record (#1868).
	ttlLocks serviceutil.RecordLocks

	// metrics is nil until Service.InitMetrics is called (or when automatic
	// collection is disabled — see config.ServiceMetricsMode). Every call
	// site in metrics_dynamodb.go is nil-safe, matching Lambda's
	// metrics_lambda.go/handler.go pattern.
	metrics metricsRecorder
}

// newHandler constructs a Handler from the raw dependencies.
func newHandler(cfg *config.Config, tables state.Store, items itemBackend, streams streamBackend, bus *events.Bus, log *serviceutil.ServiceLogger, clk clock.Clock, defaultRegion string) *Handler {
	h := &Handler{
		cfg:      cfg,
		store:    newDynamoStore(tables, items, streams, defaultRegion),
		bus:      bus,
		log:      log,
		clk:      clk,
		ttlSched: lifecycle.NewScheduler(clk),
	}
	h.initOps()
	h.rawOp = h.typedOps()
	return h
}

// initOps registers every known DynamoDB operation to its handler.
// Implemented operations point to their handler method; stubs live in handler_stubs.go.
// Adding a new operation: add an entry here, implement in handler.go, delete from handler_stubs.go.
func (h *Handler) initOps() {
	h.ops = map[string]http.HandlerFunc{
		// Table management
		"CreateTable":   h.CreateTable,
		"DescribeTable": h.DescribeTable,
		"ListTables":    h.ListTables,
		"DeleteTable":   h.DeleteTable,
		// TODO(priority:P2): implement full UpdateTable (GSI/LSI, provisioned throughput)
		"UpdateTable": h.UpdateTable,
		// Item operations
		"PutItem":    h.PutItem,
		"GetItem":    h.GetItem,
		"DeleteItem": h.DeleteItem,
		// UpdateItem — handler_update.go
		"UpdateItem":     h.UpdateItem,
		"BatchGetItem":   h.BatchGetItem,
		"BatchWriteItem": h.BatchWriteItem,
		// Query & scan
		"Scan":  h.Scan,
		"Query": h.Query,
		// TTL
		"UpdateTimeToLive":   h.UpdateTimeToLive,
		"DescribeTimeToLive": h.DescribeTimeToLive,
		// Transactions
		"TransactWriteItems": h.TransactWriteItems,
		"TransactGetItems":   h.TransactGetItems,
		// Tags
		"TagResource":        h.TagResource,
		"ListTagsOfResource": h.ListTagsOfResource,
		"UntagResource":      h.UntagResource,
	}
}

// ---- Request / response types ----------------------------------------------

type createTableRequest struct {
	TableName              string                 `json:"TableName"`
	KeySchema              []KeySchemaElement     `json:"KeySchema"`
	AttributeDefinitions   []AttributeDef         `json:"AttributeDefinitions"`
	BillingMode            string                 `json:"BillingMode,omitempty"`
	ProvisionedThroughput  *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	StreamSpecification    *StreamSpecification   `json:"StreamSpecification,omitempty"`
	GlobalSecondaryIndexes []SecondaryIndex       `json:"GlobalSecondaryIndexes,omitempty"`
	LocalSecondaryIndexes  []SecondaryIndex       `json:"LocalSecondaryIndexes,omitempty"`
	Tags                   []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags,omitempty"`
}

type createTableResponse struct {
	TableDescription *TableDescription `json:"TableDescription"`
}

type describeTableRequest struct {
	TableName string `json:"TableName"`
}

type describeTableResponse struct {
	Table *TableDescription `json:"Table"`
}

type deleteTableRequest struct {
	TableName string `json:"TableName"`
}

type putItemRequest struct {
	TableName                 string               `json:"TableName"`
	Item                      Item                 `json:"Item"`
	ConditionExpression       string               `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues,omitempty"`
	ReturnValues              string               `json:"ReturnValues,omitempty"`
}

type putItemResponse struct {
	Attributes Item `json:"Attributes,omitempty"`
}

type getItemRequest struct {
	TableName                string            `json:"TableName"`
	Key                      Item              `json:"Key"`
	ProjectionExpression     string            `json:"ProjectionExpression,omitempty"`
	ExpressionAttributeNames map[string]string `json:"ExpressionAttributeNames,omitempty"`
}

type getItemResponse struct {
	Item Item `json:"Item,omitempty"`
}

type deleteItemRequest struct {
	TableName                 string               `json:"TableName"`
	Key                       Item                 `json:"Key"`
	ReturnValues              string               `json:"ReturnValues,omitempty"`
	ConditionExpression       string               `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues,omitempty"`
}

type deleteItemResponse struct {
	Attributes Item `json:"Attributes,omitempty"`
}

type scanRequest struct {
	TableName                 string               `json:"TableName"`
	IndexName                 string               `json:"IndexName,omitempty"`
	FilterExpression          string               `json:"FilterExpression,omitempty"`
	ProjectionExpression      string               `json:"ProjectionExpression,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues,omitempty"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	Limit                     int                  `json:"Limit,omitempty"`
	ExclusiveStartKey         Item                 `json:"ExclusiveStartKey,omitempty"`
	Segment                   int                  `json:"Segment,omitempty"`
	TotalSegments             int                  `json:"TotalSegments,omitempty"`
	Select                    string               `json:"Select,omitempty"`
	ConsistentRead            bool                 `json:"ConsistentRead,omitempty"`
}

type scanResponse struct {
	Items            []Item `json:"Items"`
	Count            int    `json:"Count"`
	ScannedCount     int    `json:"ScannedCount"`
	LastEvaluatedKey Item   `json:"LastEvaluatedKey,omitempty"`
}

// countOnlyResponse is used when Select="COUNT": Items must be absent from the response.
type countOnlyResponse struct {
	Count            int  `json:"Count"`
	ScannedCount     int  `json:"ScannedCount"`
	LastEvaluatedKey Item `json:"LastEvaluatedKey,omitempty"`
}

type queryRequest struct {
	TableName                 string               `json:"TableName"`
	IndexName                 string               `json:"IndexName,omitempty"`
	KeyConditionExpression    string               `json:"KeyConditionExpression"`
	FilterExpression          string               `json:"FilterExpression,omitempty"`
	ProjectionExpression      string               `json:"ProjectionExpression,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	Limit                     int                  `json:"Limit,omitempty"`
	ExclusiveStartKey         Item                 `json:"ExclusiveStartKey,omitempty"`
	ScanIndexForward          *bool                `json:"ScanIndexForward,omitempty"`
	Select                    string               `json:"Select,omitempty"`
	ConsistentRead            bool                 `json:"ConsistentRead,omitempty"`
}

type queryResponse struct {
	Items            []Item `json:"Items"`
	Count            int    `json:"Count"`
	ScannedCount     int    `json:"ScannedCount"`
	LastEvaluatedKey Item   `json:"LastEvaluatedKey,omitempty"`
}

type listTablesRequest struct {
	ExclusiveStartTableName string `json:"ExclusiveStartTableName,omitempty"`
	Limit                   int    `json:"Limit,omitempty"`
}

type listTablesResponse struct {
	TableNames             []string `json:"TableNames"`
	LastEvaluatedTableName string   `json:"LastEvaluatedTableName,omitempty"`
}

// ---- Handlers --------------------------------------------------------------

// CreateTable handles the DynamoDB CreateTable operation.
func (h *Handler) CreateTable(w http.ResponseWriter, r *http.Request) {
	var req createTableRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.createTableTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) createTableTyped(ctx context.Context, req *createTableRequest) (*createTableResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}
	if aerr := serviceutil.TableName(req.TableName); aerr != nil {
		return nil, aerr
	}

	// Billing mode and throughput are validated before anything is written:
	// AWS rejects an invalid parameter combination without creating a table,
	// and parameter validation precedes the existence check.
	billingMode, aerr := resolveBillingMode(req.BillingMode)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateTableThroughput(billingMode, req.ProvisionedThroughput); aerr != nil {
		return nil, aerr
	}
	for i := range req.GlobalSecondaryIndexes {
		if aerr := validateIndexThroughput(billingMode, &req.GlobalSecondaryIndexes[i]); aerr != nil {
			return nil, aerr
		}
	}
	// AttributeDefinitions must describe exactly the key attributes of the
	// table and its indexes (attribute_definitions.go).
	if aerr := validateAttributeDefinitions(req); aerr != nil {
		return nil, aerr
	}
	// Request-shape validation before the existence check resolves against
	// the store — the same ordering createLogGroupTyped uses
	// (internal/services/cloudwatch/logs/typed_logic.go) — so a rejected
	// create must not depend on whether the name happens to collide (#1052).
	// CreateTable's inline Tags reached ValidateTags nowhere before this: the
	// shared dynamoTagCfg above is already wired for TagResource.
	if len(req.Tags) > 0 {
		tagMap := make(map[string]string, len(req.Tags))
		for _, t := range req.Tags {
			tagMap[t.Key] = t.Value
		}
		if aerr := serviceutil.ValidateTags(dynamoTagCfg, tagMap); aerr != nil {
			return nil, aerr
		}
	}

	exists, aerr := h.store.tableExists(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	if exists {
		return nil, errTableExists(req.TableName)
	}

	region := middleware.RegionFromContext(ctx, h.cfg.Region)
	table := &Table{
		TableName:            req.TableName,
		KeySchema:            req.KeySchema,
		AttributeDefinitions: req.AttributeDefinitions,
		TableStatus:          "ACTIVE",
		BillingMode:          req.BillingMode,
		TableARN:             "arn:aws:dynamodb:" + region + ":" + h.cfg.AccountID + ":table/" + req.TableName,
		TableId:              uuid.New().String(),
		CreationDateTime:     float64(h.clk.Now().UnixMilli()) / 1000.0,
		ItemCount:            0,
	}
	// Only an explicitly requested billing mode is echoed back: AWS reports no
	// BillingModeSummary for a table left on the default PROVISIONED mode.
	if req.BillingMode != "" {
		table.BillingModeSummary = &BillingModeSummary{BillingMode: req.BillingMode}
	}
	if req.ProvisionedThroughput != nil {
		table.ProvisionedThroughput = req.ProvisionedThroughput
	}

	// Populate GSI definitions with ARN and status.
	for i := range req.GlobalSecondaryIndexes {
		gsi := &req.GlobalSecondaryIndexes[i]
		gsi.IndexArn = table.TableARN + "/index/" + gsi.IndexName
		gsi.IndexStatus = "ACTIVE"
	}
	table.GlobalSecondaryIndexes = req.GlobalSecondaryIndexes

	// Populate LSI definitions with ARN.
	for i := range req.LocalSecondaryIndexes {
		lsi := &req.LocalSecondaryIndexes[i]
		lsi.IndexArn = table.TableARN + "/index/" + lsi.IndexName
	}
	table.LocalSecondaryIndexes = req.LocalSecondaryIndexes

	if req.StreamSpecification != nil && (req.StreamSpecification.StreamEnabled || req.StreamSpecification.StreamViewType != "") {
		req.StreamSpecification.StreamEnabled = true
		h.applyStreamSpec(table, req.StreamSpecification, region)
	}

	if len(req.Tags) > 0 {
		table.Tags = make(map[string]string, len(req.Tags))
		for _, t := range req.Tags {
			table.Tags[t.Key] = t.Value
		}
	}

	if aerr := h.store.putTable(ctx, table); aerr != nil {
		return nil, aerr
	}

	log.Info("table created", zap.String("table", req.TableName))
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.DynamoDBTableCreated,
			Time:    h.clk.Now(),
			Source:  "dynamodb",
			Payload: events.ResourcePayload{Name: req.TableName, ARN: table.TableARN},
		})
	}
	return &createTableResponse{TableDescription: table.description()}, nil
}

// ListTables handles the DynamoDB ListTables operation.
func (h *Handler) ListTables(w http.ResponseWriter, r *http.Request) {
	var req listTablesRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.listTablesTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// dynamoListTablesDefaultLimit is both the default and the maximum number of
// table names ListTables returns per page — see "ListTables" in the API
// reference: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ListTables.html
// ("If you don't specify a value for the Limit parameter, then ListTables
// returns up to 100 table names").
const dynamoListTablesDefaultLimit = 100

func (h *Handler) listTablesTyped(ctx context.Context, req *listTablesRequest) (*listTablesResponse, *protocol.AWSError) {
	// ListTables is bounded metadata (tables are created by humans/IaC, not
	// workload traffic — storage-access-plan.md's boundedness rule), so
	// paginating the already-materialized, already-sorted list in the
	// handler is the correct shape; no storage-layer change is needed here
	// (contrast with Scan/Query's A3 item, which pages unbounded item data
	// at the storage layer). store.listTables already returns tables in
	// table-name order because both state.Store implementations return List
	// results in lexicographic key order and table keys are region-prefixed
	// names.
	tables, aerr := h.store.listTables(ctx, "")
	if aerr != nil {
		return nil, aerr
	}

	limit := req.Limit
	if limit <= 0 || limit > dynamoListTablesDefaultLimit {
		limit = dynamoListTablesDefaultLimit
	}

	start := 0
	if req.ExclusiveStartTableName != "" {
		// Position-based, exactly like Scan/Query's cursor fix
		// (pagination-plan.md G2): resume after the first table name that
		// sorts strictly after the given name. AWS documents no validation
		// error for an ExclusiveStartTableName that names a table which no
		// longer exists (or never did) — real DynamoDB resumes from where
		// that name would sort, it does not reject the request or restart
		// from the beginning, so that is the behavior modeled here too.
		start = len(tables)
		for i, t := range tables {
			if t.TableName > req.ExclusiveStartTableName {
				start = i
				break
			}
		}
	}

	page := tables[start:]
	var lastEvaluated string
	if len(page) > limit {
		page = page[:limit]
		lastEvaluated = page[len(page)-1].TableName
	}

	names := make([]string, len(page))
	for i, t := range page {
		names[i] = t.TableName
	}

	return &listTablesResponse{TableNames: names, LastEvaluatedTableName: lastEvaluated}, nil
}

// DescribeTable handles the DynamoDB DescribeTable operation.
func (h *Handler) DescribeTable(w http.ResponseWriter, r *http.Request) {
	var req describeTableRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.describeTableTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) describeTableTyped(ctx context.Context, req *describeTableRequest) (*describeTableResponse, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	// Populate live item count — the stored descriptor always has 0.
	if n, aerr := h.store.countItems(ctx, req.TableName); aerr == nil {
		table.ItemCount = n
	}

	return &describeTableResponse{Table: table.description()}, nil
}

// PutItem handles the DynamoDB PutItem operation.
func (h *Handler) PutItem(w http.ResponseWriter, r *http.Request) {
	var req putItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.putItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// putItemTypedCore is PutItem's business logic. See putItemTyped
// (metrics_dynamodb.go) for the metrics-recording wrapper both dispatch
// paths actually call.
func (h *Handler) putItemTypedCore(ctx context.Context, req *putItemRequest) (*putItemResponse, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateKeySchema(table, req.Item, operandItem); aerr != nil {
		return nil, aerr
	}
	if itemSizeBytes(req.Item) > maxItemSizeBytes {
		return nil, errItemSizeExceeded()
	}

	// Evaluate ConditionExpression against the existing item, if any.
	if req.ConditionExpression != "" {
		existing, aerr := h.store.getItem(ctx, table, req.Item)
		if aerr != nil {
			return nil, aerr
		}
		filter, err := compileFilter(req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		checkItem := existing
		if checkItem == nil {
			checkItem = Item{}
		}
		ok, err := evalFilter(filter, checkItem)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if !ok {
			return nil, &protocol.AWSError{
				Code:       "ConditionalCheckFailedException",
				Message:    "The conditional request failed",
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}

	// For stream OLD_IMAGE capture, ReturnValues=ALL_OLD, or GSI index-row
	// maintenance (dynamodb-gsi-design.md section 3 — a table with GSIs
	// must diff the old item against the new one on every write to decide
	// which index rows change), read the existing item.
	var oldItem Item
	if table.streamEnabled() || req.ReturnValues == "ALL_OLD" || len(table.GlobalSecondaryIndexes) > 0 {
		oldItem, _ = h.store.getItem(ctx, table, req.Item)
	}

	if aerr := h.store.putItemWithIndexMaintenance(ctx, table, req.Item, oldItem); aerr != nil {
		return nil, aerr
	}

	if table.streamEnabled() {
		h.publishPutStreamRecord(ctx, table, req.Item, oldItem)
	}

	h.bus.Publish(ctx, events.Event{
		Type:    events.DynamoDBItemMutated,
		Source:  "dynamodb",
		Payload: events.ResourcePayload{Name: req.TableName},
	})

	if req.ReturnValues == "ALL_OLD" && oldItem != nil {
		return &putItemResponse{Attributes: oldItem}, nil
	}
	return &putItemResponse{}, nil
}

// GetItem handles the DynamoDB GetItem operation.
func (h *Handler) GetItem(w http.ResponseWriter, r *http.Request) {
	var req getItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.getItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// getItemTypedCore is GetItem's business logic. See getItemTyped
// (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) getItemTypedCore(ctx context.Context, req *getItemRequest) (*getItemResponse, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateKeySchema(table, req.Key, operandKey); aerr != nil {
		return nil, aerr
	}
	item, aerr := h.store.getItem(ctx, table, req.Key)
	if aerr != nil {
		return nil, aerr
	}

	// AWS returns 200 with empty Item when not found.
	resp := getItemResponse{}
	if item != nil {
		// Apply ProjectionExpression if provided.
		if req.ProjectionExpression != "" {
			proj, err := compileProjection(req.ProjectionExpression, req.ExpressionAttributeNames)
			if err != nil {
				return nil, &protocol.AWSError{
					Code:       "ValidationException",
					Message:    err.Error(),
					HTTPStatus: http.StatusBadRequest,
				}
			}
			item = applyProjection(item, proj, table)
		}
		resp.Item = item
	}
	return &resp, nil
}

// DeleteItem handles the DynamoDB DeleteItem operation.
func (h *Handler) DeleteItem(w http.ResponseWriter, r *http.Request) {
	var req deleteItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.deleteItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// deleteItemTypedCore is DeleteItem's business logic. See deleteItemTyped
// (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) deleteItemTypedCore(ctx context.Context, req *deleteItemRequest) (*deleteItemResponse, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}
	if aerr := validateKeySchema(table, req.Key, operandKey); aerr != nil {
		return nil, aerr
	}

	// Capture old item (needed for ConditionExpression, ReturnValues,
	// streams, and — per dynamodb-gsi-design.md section 3 — GSI index-row
	// cleanup: every GSI whose key the old item satisfied needs its index
	// row deleted).
	var oldItem Item
	if table.streamEnabled() || req.ConditionExpression != "" || req.ReturnValues == "ALL_OLD" || len(table.GlobalSecondaryIndexes) > 0 {
		oldItem, _ = h.store.getItem(ctx, table, req.Key)
	}

	// Evaluate ConditionExpression if provided.
	if req.ConditionExpression != "" {
		filter, err := compileFilter(req.ConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		checkItem := oldItem
		if checkItem == nil {
			checkItem = Item{}
		}
		ok, err := evalFilter(filter, checkItem)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		if !ok {
			return nil, &protocol.AWSError{
				Code:       "ConditionalCheckFailedException",
				Message:    "The conditional request failed",
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}

	if aerr := h.store.deleteItemWithIndexMaintenance(ctx, table, req.Key, oldItem); aerr != nil {
		return nil, aerr
	}

	if table.streamEnabled() && oldItem != nil {
		h.publishDeleteStreamRecord(ctx, table, req.Key, oldItem)
	}

	h.bus.Publish(ctx, events.Event{
		Type:    events.DynamoDBItemMutated,
		Source:  "dynamodb",
		Payload: events.ResourcePayload{Name: req.TableName},
	})

	if req.ReturnValues == "ALL_OLD" && oldItem != nil {
		return &deleteItemResponse{Attributes: oldItem}, nil
	}
	return &deleteItemResponse{}, nil
}

// Scan handles the DynamoDB Scan operation.
func (h *Handler) Scan(w http.ResponseWriter, r *http.Request) {
	var req scanRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.scanTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// scanTypedCore is Scan's business logic. See scanTyped (metrics_dynamodb.go)
// for the metrics-recording wrapper.
func (h *Handler) scanTypedCore(ctx context.Context, req *scanRequest) (any, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}
	if aerr := validateScanSegments(req.Segment, req.TotalSegments); aerr != nil {
		return nil, aerr
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	// When scanning a GSI, exclude items that lack the index's hash key attribute.
	var scanIdx *SecondaryIndex
	if req.IndexName != "" {
		scanIdx = table.findIndex(req.IndexName)
		if scanIdx == nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "The table does not have the specified index: " + req.IndexName,
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}

	isGSIScan := scanIdx != nil && table.isGSI(req.IndexName)
	if req.ConsistentRead && isGSIScan {
		return nil, errConsistentReadOnGSI()
	}

	limit := effectivePageLimit(req.Limit)

	var items []Item
	var lastKey Item

	switch {
	case scanIdx == nil && req.TotalSegments <= 1:
		// Base-table scan (no GSI, no parallel segments): page directly at the
		// storage layer instead of reading the whole table on every call
		// (storage-access-plan.md A3). scanItemsPage's keyset cursor is
		// position-based by construction, not identity-based — a deleted
		// "last returned item" still resolves to the correct resume point
		// (pagination-plan.md G2), so no separate cursor-search step is needed
		// on this path.
		pageItems, hasMore, aerr := h.store.scanItemsPage(ctx, table, req.ExclusiveStartKey, limit)
		if aerr != nil {
			return nil, aerr
		}
		items = pageItems
		if hasMore {
			lastKey = extractItemKeys(items[len(items)-1], table)
		}

	case isGSIScan && req.TotalSegments <= 1:
		// GSI scan (no parallel segments): page directly against the GSI's
		// ordered index structure (dynamodb-gsi-design.md §4) instead of
		// reading the whole table — the same A3-style keyset-page upgrade
		// the base table already has. Sparse-index exclusion needs no
		// read-time filter here (unlike the fallback branch below): a row
		// is only ever written to the index when the item satisfies the
		// index's key at write time (design §3's sparse-write rule), so
		// every row scanIndexPage returns already belongs. Entries are also
		// already narrowed to the index's own Projection, closing the same
		// projection-fidelity gap queryTyped's GSI branch closes below.
		pageItems, hasMore, aerr := h.store.scanIndexPage(ctx, table, scanIdx, req.ExclusiveStartKey, limit)
		if aerr != nil {
			return nil, aerr
		}
		items = pageItems
		if hasMore {
			lastKey = extractItemKeysWithIndex(items[len(items)-1], table, scanIdx)
		}

	case scanIdx == nil:
		// Parallel base-table scan (TotalSegments > 1): a bounded keyset
		// walk that keeps only its own segment's items, instead of reading
		// and sorting the whole table once per segment
		// (dynamodb-gsi-design.md §5's segmentation follow-up — see
		// scan_segments.go for why segment membership is hashed from the
		// item's own key rather than sliced out of a materialized list).
		pageItems, hasMore, aerr := h.store.scanItemsSegmentPage(ctx, table, req.ExclusiveStartKey, limit, req.Segment, req.TotalSegments)
		if aerr != nil {
			return nil, aerr
		}
		items = pageItems
		if hasMore && len(items) > 0 {
			lastKey = extractItemKeys(items[len(items)-1], table)
		}

	case isGSIScan:
		// Parallel GSI scan: the same segment walk over the GSI's own
		// ordered index structure, so a segmented index scan is
		// projection-faithful and sparse-correct exactly like the
		// unsegmented one (it previously fell through to the base-table
		// fallback below and could return attributes outside the index's
		// projection).
		pageItems, hasMore, aerr := h.store.scanIndexSegmentPage(ctx, table, scanIdx, req.ExclusiveStartKey, limit, req.Segment, req.TotalSegments)
		if aerr != nil {
			return nil, aerr
		}
		items = pageItems
		if hasMore && len(items) > 0 {
			lastKey = extractItemKeysWithIndex(items[len(items)-1], table, scanIdx)
		}

	default:
		// LSI scan, segmented or not: LSIs have no index storage at all
		// (dynamodb-gsi-design.md §5), so this path still reads the whole
		// table and paginates in memory. It still gets G2's position-based
		// cursor fix: ExclusiveStartKey is resolved by where it falls in
		// (hash, sort) order, not by searching for an exact item match.
		allItems, aerr := h.store.scanItems(ctx, req.TableName)
		if aerr != nil {
			return nil, aerr
		}

		if scanIdx != nil {
			// Sparse-index rule: an item is only in the index when every
			// index key attribute exists on it — the hash key AND the sort
			// key, when the index has one (dynamodb-gsi-design.md §3's
			// sparse-write rule, applied here as a read-time filter since
			// this fallback has no index storage to consult). For an LSI
			// the hash key is the table's own and always present, so the
			// sort-key check is the one doing the work.
			hashKey := indexHashKeyName(scanIdx)
			sortKey := indexSortKeyName(scanIdx)
			filtered := make([]Item, 0, len(allItems))
			for _, item := range allItems {
				if _, ok := item[hashKey]; !ok {
					continue
				}
				if sortKey != "" {
					if _, ok := item[sortKey]; !ok {
						continue
					}
				}
				filtered = append(filtered, item)
			}
			allItems = filtered
		}
		if allItems == nil {
			allItems = []Item{}
		}

		// Sort by (hashKey, sortKey) — a full total order, needed for
		// position-based cursor resolution to be well-defined (ties on hash
		// key alone would make "the position after the cursor" ambiguous).
		// Type-aware per AWS's ordering contract: a Number-typed key sorts
		// numerically, not as raw decimal text (dynamodb-gsi-design.md §2).
		hashKeyName := table.hashKeyName()
		sortKeyName := table.sortKeyName()
		hashKeyType := keyAttrType(table, hashKeyName)
		sortKeyType := keyAttrType(table, sortKeyName)
		sort.Slice(allItems, func(i, j int) bool {
			ih := extractKeyValue(allItems[i][hashKeyName])
			jh := extractKeyValue(allItems[j][hashKeyName])
			if c := compareKeyAttr(hashKeyType, ih, jh); c != 0 {
				return c < 0
			}
			iv := extractKeyValue(allItems[i][sortKeyName])
			jv := extractKeyValue(allItems[j][sortKeyName])
			return compareKeyAttr(sortKeyType, iv, jv) < 0
		})

		// Parallel scan: keep only this segment's items. An LSI shares the
		// base table's partition key, so the segment is hashed from that
		// key — the same assignment the index-backed paths above use, so a
		// client sees one segmentation rule whichever index it scans
		// (scan_segments.go).
		if req.TotalSegments > 1 {
			segmented := make([]Item, 0, len(allItems))
			for _, item := range allItems {
				h, _, kerr := resolveStorageKeys(table, item)
				if kerr != nil {
					continue
				}
				if segmentForKey(h, req.TotalSegments) == req.Segment {
					segmented = append(segmented, item)
				}
			}
			allItems = segmented
		}

		// Apply ExclusiveStartKey by position, not identity (pagination-plan.md G2).
		startIdx := resolveCursorPosition(allItems, req.ExclusiveStartKey, table, hashKeyName, sortKeyName, true)
		allItems = allItems[startIdx:]

		// Apply Limit (must be before FilterExpression per DynamoDB semantics: Limit caps the
		// number of items READ, not the number returned after filtering).
		if len(allItems) > limit {
			allItems = allItems[:limit]
			lastKey = extractItemKeysWithIndex(allItems[len(allItems)-1], table, scanIdx)
		}
		items = allItems
	}

	scannedCount := len(items)

	// Apply FilterExpression if provided.
	if req.FilterExpression != "" {
		filter, err := compileFilterExpression(req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		filtered := items[:0]
		for _, item := range items {
			pass, err := evalFilter(filter, item)
			if err != nil {
				return nil, &protocol.AWSError{
					Code:       "ValidationException",
					Message:    err.Error(),
					HTTPStatus: http.StatusBadRequest,
				}
			}
			if pass {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}

	// Select=COUNT: return only counts, no items.
	if req.Select == "COUNT" {
		return &countOnlyResponse{Count: len(items), ScannedCount: scannedCount, LastEvaluatedKey: lastKey}, nil
	}

	// Apply ProjectionExpression if provided.
	if req.ProjectionExpression != "" {
		proj, err := compileProjection(req.ProjectionExpression, req.ExpressionAttributeNames)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		for i, item := range items {
			items[i] = applyProjection(item, proj, table)
		}
	}

	return &scanResponse{Items: items, Count: len(items), ScannedCount: scannedCount, LastEvaluatedKey: lastKey}, nil
}

// DeleteTable handles the DynamoDB DeleteTable operation.
func (h *Handler) DeleteTable(w http.ResponseWriter, r *http.Request) {
	var req deleteTableRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.deleteTableTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) deleteTableTyped(ctx context.Context, req *deleteTableRequest) (*createTableResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	if aerr := h.store.deleteTable(ctx, req.TableName); aerr != nil {
		return nil, aerr
	}

	log.Info("table deleted", zap.String("table", req.TableName))
	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.DynamoDBTableDeleted,
			Time:    h.clk.Now(),
			Source:  "dynamodb",
			Payload: events.ResourcePayload{Name: req.TableName, ARN: table.TableARN},
		})
	}
	// AWS's DeleteTable response echoes the table description with
	// TableStatus DELETING (the table is already gone from our store by the
	// time we respond, but the wire shape reflects the transient deleting
	// state real AWS returns) under the same TableDescription member
	// CreateTable/DescribeTable/UpdateTable use — see API_DeleteTable's
	// ResponseSyntax.
	table.TableStatus = "DELETING"
	return &createTableResponse{TableDescription: table.description()}, nil
}

// Query handles the DynamoDB Query operation.
// Supports hash-key equality, sort-key conditions (=, <, <=, >, >=, BETWEEN,
// begins_with), FilterExpression, ProjectionExpression, and GSI/LSI via IndexName.
func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.queryTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// queryTypedCore is Query's business logic. See queryTyped
// (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) queryTypedCore(ctx context.Context, req *queryRequest) (any, *protocol.AWSError) {
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}
	if req.KeyConditionExpression == "" {
		return nil, protocol.ErrMissingParameter("KeyConditionExpression")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	// Resolve key schema: either from the index or the table.
	var idxHashKeyName, idxSortKeyName string
	var activeIdx *SecondaryIndex
	if req.IndexName != "" {
		activeIdx = table.findIndex(req.IndexName)
		if activeIdx == nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "The table does not have the specified index: " + req.IndexName,
				HTTPStatus: http.StatusBadRequest,
			}
		}
		idxHashKeyName = indexHashKeyName(activeIdx)
		idxSortKeyName = indexSortKeyName(activeIdx)
		if req.ConsistentRead && table.isGSI(req.IndexName) {
			return nil, errConsistentReadOnGSI()
		}
	}

	// Parse the KeyConditionExpression using the full expression parser.
	kc, err := compileKeyCondition(req.KeyConditionExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
	if err != nil {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    err.Error(),
			HTTPStatus: http.StatusBadRequest,
		}
	}

	// Determine which attribute names to use for matching.
	hashAttrName := table.hashKeyName()
	sortAttrName := table.sortKeyName()
	if req.IndexName != "" {
		hashAttrName = idxHashKeyName
		sortAttrName = idxSortKeyName
	}

	// The condition must constrain the key schema in play (key_schema.go);
	// this may swap a sort-key-first expression into canonical order, so it
	// runs before the partition-key value is read out. It is also what lets
	// the branches below match kc.sortCond against the item directly: past
	// this point the condition is known to name sortAttrName, where they used
	// to overwrite the parsed name with it and so answer a condition on a
	// non-key attribute as if it were a sort-key condition (issue #135).
	if aerr := validateKeyConditionSchema(hashAttrName, sortAttrName, kc); aerr != nil {
		return nil, aerr
	}

	hashVal := extractKeyValue(kc.hashVal)

	// A key condition comparing a key against the wrong type is rejected, not
	// answered with an empty page: the stored key encoding is type-dependent
	// (key_schema.go), so a mistyped condition never matches anything and the
	// empty result reads as "no such items" rather than as the fault it is.
	if aerr := validateKeyConditionTypes(table, hashAttrName, sortAttrName, kc); aerr != nil {
		return nil, aerr
	}

	// Collect matching items.
	var matched []Item
	switch {
	case req.IndexName != "" && table.isGSI(req.IndexName):
		// GSI query: partition-scoped read into the GSI's ordered index
		// structure (dynamodb-gsi-design.md §4) — an O(k) read replacing
		// the O(table size) full-scan-and-filter this branch used to do,
		// giving GSI Query the same partition-scoped cost base-table Query
		// already has. Entries are already narrowed to the index's own
		// Projection (write-path maintenance in index_maintenance.go's
		// projectForIndex), which is the projection-fidelity fix from
		// design §2: a KEYS_ONLY/INCLUDE GSI can no longer return
		// base-table attributes it was never projected — the downstream
		// FilterExpression/Select/ProjectionExpression code below is
		// unchanged, it simply now operates on a smaller candidate
		// attribute set.
		candidates, aerr := h.store.scanIndexByHash(ctx, table, activeIdx, hashVal)
		if aerr != nil {
			return nil, aerr
		}
		if kc.sortCond != nil {
			for _, item := range candidates {
				if kc.sortCond.matchItem(item) {
					matched = append(matched, item)
				}
			}
		} else {
			matched = candidates
		}

	case req.IndexName != "" && idxHashKeyName == table.hashKeyName():
		// LSI query: partition-scoped read of the base partition
		// (dynamodb-gsi-design.md §5's routing follow-up). An LSI shares the
		// base table's hash key by definition, so the same O(k)
		// scanItemsByHashKey primitive base-table Query uses already returns
		// exactly the candidate set — no separate index structure needed,
		// and no full-table scan. LSIs are sparse the same way GSIs are: an
		// item without the LSI's sort key attribute is not in the index at
		// all, so it is excluded here even when no sort-key condition was
		// supplied (the pre-routing fallback missed this — its only
		// presence check was the hash key, which an LSI item always has).
		candidates, aerr := h.store.scanItemsByHashKey(ctx, table, hashVal)
		if aerr != nil {
			return nil, aerr
		}
		for _, item := range candidates {
			if idxSortKeyName != "" {
				if _, ok := item[idxSortKeyName]; !ok {
					continue // sparse: not propagated to the LSI
				}
			}
			if kc.sortCond != nil && !kc.sortCond.matchItem(item) {
				continue
			}
			matched = append(matched, item)
		}

	case req.IndexName != "":
		// Defensive-only: an index whose hash key differs from the table's
		// and isn't a GSI. Real AWS rejects such an LSI at CreateTable
		// (LSIs must reuse the table's partition key), so this branch only
		// serves malformed/legacy table records — per the isolation rule it
		// degrades to the old full-scan-and-filter behavior instead of
		// returning wrong partitions from a hash-key mismatch.
		allItems, aerr := h.store.scanItems(ctx, req.TableName)
		if aerr != nil {
			return nil, aerr
		}
		for _, item := range allItems {
			av, ok := item[hashAttrName]
			if !ok || extractKeyValue(av) != hashVal {
				continue
			}
			// Apply sort key condition if present.
			if kc.sortCond != nil && !kc.sortCond.matchItem(item) {
				continue
			}
			matched = append(matched, item)
		}

	case sortAttrName == "":
		// Hash-only table: point lookup.
		keyMap := Item{hashAttrName: kc.hashVal}
		item, aerr := h.store.getItem(ctx, table, keyMap)
		if aerr != nil {
			return nil, aerr
		}
		if item != nil {
			matched = []Item{item}
		} else {
			matched = []Item{}
		}

	default:
		// Hash+sort table: load all items for the hash key, then filter by sort condition.
		candidates, aerr := h.store.scanItemsByHashKey(ctx, table, hashVal)
		if aerr != nil {
			return nil, aerr
		}
		if kc.sortCond != nil {
			for _, item := range candidates {
				if kc.sortCond.matchItem(item) {
					matched = append(matched, item)
				}
			}
		} else {
			matched = candidates
		}
	}

	if matched == nil {
		matched = []Item{}
	}

	// Sort matched items by sort key for stable pagination order. Type-aware
	// per AWS's ordering contract (dynamodb-gsi-design.md §2): effectiveSortKey
	// may be the table's own sort key or an index's sort key, but either way
	// its declared type lives in table.AttributeDefinitions (AWS requires
	// every key attribute — table or index — to be declared there).
	effectiveSortKey := sortAttrName
	if effectiveSortKey != "" {
		effectiveSortKeyType := keyAttrType(table, effectiveSortKey)
		sort.Slice(matched, func(i, j int) bool {
			iv := extractKeyValue(matched[i][effectiveSortKey])
			jv := extractKeyValue(matched[j][effectiveSortKey])
			return compareKeyAttr(effectiveSortKeyType, iv, jv) < 0
		})
	}

	// Apply ScanIndexForward (default true, false = reverse order).
	if req.ScanIndexForward != nil && !*req.ScanIndexForward && effectiveSortKey != "" {
		for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
			matched[i], matched[j] = matched[j], matched[i]
		}
	}

	// Apply ExclusiveStartKey by position, not identity: find the first item
	// that sorts strictly after the cursor in the order just established
	// above (ascending or reversed per ScanIndexForward). Real DynamoDB
	// degrades the same way when the cursor's item no longer exists — a
	// position-based search still lands on the correct resume point, where
	// an exact-match search silently restarts from the beginning and
	// duplicates every item already delivered (pagination-plan.md G2).
	ascending := req.ScanIndexForward == nil || *req.ScanIndexForward
	startIdx := resolveCursorPosition(matched, req.ExclusiveStartKey, table, hashAttrName, effectiveSortKey, ascending)
	matched = matched[startIdx:]

	// Apply Limit (must be before FilterExpression per DynamoDB semantics: Limit caps the
	// number of items READ, not the number returned after filtering).
	limit := effectivePageLimit(req.Limit)
	var lastKey Item
	if len(matched) > limit {
		matched = matched[:limit]
		lastKey = extractItemKeysWithIndex(matched[len(matched)-1], table, activeIdx)
	}

	scannedCount := len(matched)

	// Apply FilterExpression (post-key-condition, per DynamoDB semantics).
	if req.FilterExpression != "" {
		filter, err := compileFilterExpression(req.FilterExpression, req.ExpressionAttributeNames, req.ExpressionAttributeValues)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		out := matched[:0]
		for _, item := range matched {
			pass, err := evalFilter(filter, item)
			if err != nil {
				return nil, &protocol.AWSError{
					Code:       "ValidationException",
					Message:    err.Error(),
					HTTPStatus: http.StatusBadRequest,
				}
			}
			if pass {
				out = append(out, item)
			}
		}
		matched = out
	}

	// Select=COUNT: return only counts, no items.
	if req.Select == "COUNT" {
		return &countOnlyResponse{Count: len(matched), ScannedCount: scannedCount, LastEvaluatedKey: lastKey}, nil
	}

	// Apply ProjectionExpression if provided.
	if req.ProjectionExpression != "" {
		proj, err := compileProjection(req.ProjectionExpression, req.ExpressionAttributeNames)
		if err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
		for i, item := range matched {
			matched[i] = applyProjection(item, proj, table)
		}
	}

	return &queryResponse{Items: matched, Count: len(matched), ScannedCount: scannedCount, LastEvaluatedKey: lastKey}, nil
}

// GSIUpdate describes a single GlobalSecondaryIndex update operation.
type GSIUpdate struct {
	Create *SecondaryIndex `json:"Create,omitempty"`
	Delete *struct {
		IndexName string `json:"IndexName"`
	} `json:"Delete,omitempty"`
	Update *struct {
		IndexName             string                 `json:"IndexName"`
		ProvisionedThroughput *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	} `json:"Update,omitempty"`
}

type updateTableRequest struct {
	TableName                   string                 `json:"TableName"`
	BillingMode                 string                 `json:"BillingMode,omitempty"`
	AttributeDefinitions        []AttributeDef         `json:"AttributeDefinitions,omitempty"`
	StreamSpecification         *StreamSpecification   `json:"StreamSpecification,omitempty"`
	ProvisionedThroughput       *ProvisionedThroughput `json:"ProvisionedThroughput,omitempty"`
	GlobalSecondaryIndexUpdates []GSIUpdate            `json:"GlobalSecondaryIndexUpdates,omitempty"`
}

// UpdateTable handles the DynamoDB UpdateTable operation.
func (h *Handler) UpdateTable(w http.ResponseWriter, r *http.Request) {
	var req updateTableRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.updateTableTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

func (h *Handler) updateTableTyped(ctx context.Context, req *updateTableRequest) (*createTableResponse, *protocol.AWSError) {
	log := h.log.WithRecorder(ctx)
	if req.TableName == "" {
		return nil, protocol.ErrMissingParameter("TableName")
	}

	table, aerr := h.store.getTable(ctx, req.TableName)
	if aerr != nil {
		return nil, aerr
	}

	changed := false

	// ── BillingMode ─────────────────────────────────────────────────────
	if req.BillingMode != "" {
		table.BillingMode = req.BillingMode
		summary := &BillingModeSummary{BillingMode: req.BillingMode}
		if req.BillingMode == billingModePayPerRequest {
			summary.LastUpdateToPayPerRequest = float64(h.clk.Now().UnixMilli()) / 1000.0
		}
		table.BillingModeSummary = summary
		changed = true
	}

	// ── AttributeDefinitions ────────────────────────────────────────────
	if len(req.AttributeDefinitions) > 0 {
		table.AttributeDefinitions = req.AttributeDefinitions
		changed = true
	}

	// ── ProvisionedThroughput ────────────────────────────────────────────
	if req.ProvisionedThroughput != nil {
		table.ProvisionedThroughput = req.ProvisionedThroughput
		changed = true
	}

	// ── GlobalSecondaryIndexUpdates ─────────────────────────────────────
	for _, update := range req.GlobalSecondaryIndexUpdates {
		if update.Create != nil {
			gsi := *update.Create
			gsi.IndexArn = table.TableARN + "/index/" + gsi.IndexName
			gsi.IndexStatus = "ACTIVE"
			table.GlobalSecondaryIndexes = append(table.GlobalSecondaryIndexes, gsi)
			changed = true

			// Backfill (dynamodb-gsi-design.md section 3): the table may
			// already have items when a GSI is added — synchronously scan
			// and populate the new index's rows before returning, so
			// IndexStatus: "ACTIVE" is truthful the instant this response
			// is sent, matching CreateTable's existing immediate-ACTIVE
			// convention rather than introducing a CREATING/backfilling
			// state nothing else in this codebase models.
			if aerr := h.backfillIndex(ctx, table, &gsi); aerr != nil {
				return nil, aerr
			}
		}
		if update.Delete != nil {
			filtered := table.GlobalSecondaryIndexes[:0]
			for _, g := range table.GlobalSecondaryIndexes {
				if g.IndexName != update.Delete.IndexName {
					filtered = append(filtered, g)
				}
			}
			table.GlobalSecondaryIndexes = filtered
			changed = true

			// Clean up the removed GSI's index rows so a later GSI
			// recreated under the same name doesn't inherit stale entries
			// from this one's lifetime (dynamodb-gsi-design.md section 3).
			if aerr := h.store.deleteIndexEntriesForIndex(ctx, table.TableName, update.Delete.IndexName); aerr != nil {
				return nil, aerr
			}
		}
		if update.Update != nil {
			for i := range table.GlobalSecondaryIndexes {
				if table.GlobalSecondaryIndexes[i].IndexName == update.Update.IndexName {
					if update.Update.ProvisionedThroughput != nil {
						table.GlobalSecondaryIndexes[i].ProvisionedThroughput = update.Update.ProvisionedThroughput
					}
					changed = true
					break
				}
			}
		}
	}

	// ── StreamSpecification ─────────────────────────────────────────────
	if req.StreamSpecification != nil {
		if req.StreamSpecification.StreamEnabled || req.StreamSpecification.StreamViewType != "" {
			req.StreamSpecification.StreamEnabled = true
			h.applyStreamSpec(table, req.StreamSpecification, middleware.RegionFromContext(ctx, h.cfg.Region))
		} else {
			table.StreamSpecification = &StreamSpecification{StreamEnabled: false}
		}
		changed = true
	}

	if changed {
		if aerr := h.store.putTable(ctx, table); aerr != nil {
			return nil, aerr
		}
		log.Info("table updated", zap.String("table", req.TableName))
		if h.bus != nil {
			h.bus.Publish(ctx, events.Event{
				Type:    events.DynamoDBStreamUpdated,
				Time:    h.clk.Now(),
				Source:  "dynamodb",
				Payload: events.ResourcePayload{Name: req.TableName, ARN: table.TableARN},
			})
		}
	}

	return &createTableResponse{TableDescription: table.description()}, nil
}

// backfillIndex populates idx's index rows for every item already stored in
// table — dynamodb-gsi-design.md section 3's "UpdateTable adding a GSI to a
// table that already has items" backfill. A brand-new table needs no
// equivalent call: CreateTable's GSIs start with zero items to backfill,
// so every subsequent write populates the index going forward via the
// normal write-path maintenance (diffIndexMutations).
//
// Uses the same indexKeyComponents/projectForIndex helpers as the write
// path and the migration-time backfill (migrations.go's
// backfillIndexEntriesForTable), so all three producers of index rows agree
// on what a row looks like for a given item.
func (h *Handler) backfillIndex(ctx context.Context, table *Table, idx *SecondaryIndex) *protocol.AWSError {
	items, aerr := h.store.scanItems(ctx, table.TableName)
	if aerr != nil {
		return aerr
	}

	var mutations []indexMutation
	for _, item := range items {
		indexHash, indexSort, ok := indexKeyComponents(table, idx, item)
		if !ok {
			continue // sparse — item doesn't satisfy this GSI's key
		}
		baseHash, baseSort, aerr := resolveStorageKeys(table, item)
		if aerr != nil {
			continue // malformed item — isolate, skip (CLAUDE.md isolation rule)
		}
		mutations = append(mutations, indexMutation{
			indexName: idx.IndexName,
			op:        indexMutationUpsert,
			indexHash: indexHash, indexSort: indexSort,
			baseHash: baseHash, baseSort: baseSort,
			item: projectForIndex(table, idx, item),
		})
	}
	if len(mutations) == 0 {
		return nil
	}
	if aerr := h.store.applyIndexMutations(ctx, table.TableName, mutations); aerr != nil {
		return aerr
	}
	return nil
}

// ---- Stream helpers --------------------------------------------------------

// applyStreamSpec sets the stream fields on a table, generating a new stream ARN/label.
func (h *Handler) applyStreamSpec(table *Table, spec *StreamSpecification, region string) {
	now := h.clk.Now()
	label := now.UTC().Format("2006-01-02T15:04:05.000")
	table.StreamSpecification = spec
	table.LatestStreamLabel = label
	table.LatestStreamArn = fmt.Sprintf(
		"arn:aws:dynamodb:%s:%s:table/%s/stream/%s",
		region, h.cfg.AccountID, table.TableName, label,
	)
}

// streamRecordRegion returns the region whose stream a table's change records
// belong to, read back from the table's own ARNs rather than from the request
// context.
//
// A table name alone does not identify a stream — the same name can exist in
// several regions with independent streams (issue #673) — so every stream
// event published on the bus carries this alongside the table name, and
// subscribers (lambda's ESM stream handler, pipes' stream delivery) match on
// both. Taking it from the stored ARN rather than from the context means a
// record published by a background path that pinned a region (the TTL sweeper)
// and one published from a request agree by construction.
func streamRecordRegion(table *Table) string {
	if region := serviceutil.ARNRegion(table.LatestStreamArn); region != "" {
		return region
	}
	return serviceutil.ARNRegion(table.TableARN)
}

// extractKeys builds a key-only Item from a full item using the table's key schema.
//
// Every item reaching here has already passed validateKeySchema
// (key_schema.go) or was read back out of storage, so each key attribute is
// present. The presence test below only guards a malformed stored record; it
// is no longer how a request with a missing key attribute is handled.
func extractKeys(table *Table, item Item) Item {
	keys := make(Item, 2)
	for _, k := range table.KeySchema {
		if v, ok := item[k.AttributeName]; ok {
			keys[k.AttributeName] = v
		}
	}
	return keys
}

// buildStreamImages returns newImage and oldImage based on the table's StreamViewType.
func buildStreamImages(viewType string, newItem, oldItem Item) (newImage, oldImage Item) {
	switch viewType {
	case "NEW_IMAGE":
		newImage = newItem
	case "OLD_IMAGE":
		oldImage = oldItem
	case "NEW_AND_OLD_IMAGES":
		newImage = newItem
		oldImage = oldItem
		// KEYS_ONLY: neither image is included
	}
	return
}

// publishPutStreamRecord publishes an INSERT or MODIFY stream record and events bus event.
func (h *Handler) publishPutStreamRecord(ctx context.Context, table *Table, newItem, oldItem Item) {
	log := h.log.WithRecorder(ctx)
	eventName := "INSERT"
	if oldItem != nil {
		eventName = "MODIFY"
	}

	keys := extractKeys(table, newItem)
	newImage, oldImage := buildStreamImages(table.streamViewType(), newItem, oldItem)

	rec := &StreamRecord{
		EventName: eventName,
		Keys:      keys,
		NewImage:  newImage,
		OldImage:  oldImage,
		CreatedAt: h.clk.Now().UnixMilli(),
	}
	if aerr := h.store.appendStreamRecord(ctx, table.TableName, rec); aerr != nil {
		log.Error("stream: append record", zap.String("table", table.TableName), zap.String("event", eventName))
		return
	}

	if h.bus != nil {
		evtType := events.DynamoDBStreamInsert
		if eventName == "MODIFY" {
			evtType = events.DynamoDBStreamModify
		}
		streamRegion := streamRecordRegion(table)
		seqStr := fmt.Sprintf("%021d", rec.SequenceNumber)
		ddbRecord := map[string]any{
			"ApproximateCreationDateTime": float64(rec.CreatedAt) / 1000.0,
			"Keys":                        keys,
			"NewImage":                    newImage,
			"OldImage":                    oldImage,
			"SequenceNumber":              seqStr,
			"StreamViewType":              table.streamViewType(),
		}
		h.bus.Publish(ctx, events.Event{
			Type:   evtType,
			Time:   h.clk.Now(),
			Source: "dynamodb",
			Payload: events.DynamoDBStreamPayload{
				Table:          table.TableName,
				Region:         streamRegion,
				EventName:      eventName,
				SequenceNumber: rec.SequenceNumber,
				Keys:           keys,
				NewImage:       newImage,
				OldImage:       oldImage,
				CreatedAt:      rec.CreatedAt,
			},
		})
		// Companion observability event: AWS StreamRecord shape so the event console
		// shows exactly what ESM filter patterns are evaluated against.
		h.bus.Publish(ctx, events.Event{
			Type:   events.DynamoDBStreamRecord,
			Time:   h.clk.Now(),
			Source: "dynamodb",
			Payload: events.DynamoDBStreamRecordPayload{
				Table:     table.TableName,
				Region:    streamRegion,
				EventName: eventName,
				Dynamodb:  ddbRecord,
			},
		})
	}
}

// publishDeleteStreamRecord publishes a REMOVE stream record and events bus event.
func (h *Handler) publishDeleteStreamRecord(ctx context.Context, table *Table, _, oldItem Item) {
	log := h.log.WithRecorder(ctx)
	keys := extractKeys(table, oldItem)
	_, oldImage := buildStreamImages(table.streamViewType(), nil, oldItem)

	rec := &StreamRecord{
		EventName: "REMOVE",
		Keys:      keys,
		OldImage:  oldImage,
		CreatedAt: h.clk.Now().UnixMilli(),
	}
	if aerr := h.store.appendStreamRecord(ctx, table.TableName, rec); aerr != nil {
		log.Error("stream: append remove record", zap.String("table", table.TableName))
		return
	}

	if h.bus != nil {
		streamRegion := streamRecordRegion(table)
		seqStr := fmt.Sprintf("%021d", rec.SequenceNumber)
		ddbRecord := map[string]any{
			"ApproximateCreationDateTime": float64(rec.CreatedAt) / 1000.0,
			"Keys":                        keys,
			"OldImage":                    oldImage,
			"SequenceNumber":              seqStr,
			"StreamViewType":              table.streamViewType(),
		}
		h.bus.Publish(ctx, events.Event{
			Type:   events.DynamoDBStreamRemove,
			Time:   h.clk.Now(),
			Source: "dynamodb",
			Payload: events.DynamoDBStreamPayload{
				Table:          table.TableName,
				Region:         streamRegion,
				EventName:      "REMOVE",
				SequenceNumber: rec.SequenceNumber,
				Keys:           keys,
				OldImage:       oldImage,
				CreatedAt:      rec.CreatedAt,
			},
		})
		// Companion observability event: AWS StreamRecord shape.
		h.bus.Publish(ctx, events.Event{
			Type:   events.DynamoDBStreamRecord,
			Time:   h.clk.Now(),
			Source: "dynamodb",
			Payload: events.DynamoDBStreamRecordPayload{
				Table:     table.TableName,
				Region:    streamRegion,
				EventName: "REMOVE",
				Dynamodb:  ddbRecord,
			},
		})
	}
}

// ---- BatchGetItem ----------------------------------------------------------

type batchGetItemRequest struct {
	RequestItems map[string]batchGetTableRequest `json:"RequestItems"`
}

type batchGetTableRequest struct {
	Keys []Item `json:"Keys"`
}

type batchGetItemResponse struct {
	Responses       map[string][]Item               `json:"Responses"`
	UnprocessedKeys map[string]batchGetTableRequest `json:"UnprocessedKeys"`
}

// BatchGetItem handles the DynamoDB BatchGetItem operation.
func (h *Handler) BatchGetItem(w http.ResponseWriter, r *http.Request) {
	var req batchGetItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.batchGetItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// batchGetItemTypedCore is BatchGetItem's business logic. See
// batchGetItemTyped (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) batchGetItemTypedCore(ctx context.Context, req *batchGetItemRequest) (*batchGetItemResponse, *protocol.AWSError) {
	// At most 100 keys across every table in the request. The API reference
	// documents both the limit and this exact message: "If you request more
	// than 100 items, BatchGetItem returns a ValidationException with the
	// message 'Too many items requested for the BatchGetItem call.'"
	var totalKeys int
	for _, tableReq := range req.RequestItems {
		totalKeys += len(tableReq.Keys)
	}
	if totalKeys > 100 {
		return nil, errValidation("Too many items requested for the BatchGetItem call")
	}

	responses := make(map[string][]Item, len(req.RequestItems))

	for tableName, tableReq := range req.RequestItems {
		table, aerr := h.store.getTable(ctx, tableName)
		if aerr != nil {
			return nil, aerr
		}

		items := make([]Item, 0, len(tableReq.Keys))
		for _, key := range tableReq.Keys {
			if aerr := validateKeySchema(table, key, operandKey); aerr != nil {
				return nil, aerr
			}
			item, aerr := h.store.getItem(ctx, table, key)
			if aerr != nil {
				return nil, aerr
			}
			if item != nil {
				items = append(items, item)
			}
		}
		responses[tableName] = items
	}

	return &batchGetItemResponse{
		Responses:       responses,
		UnprocessedKeys: map[string]batchGetTableRequest{},
	}, nil
}

// ---- BatchWriteItem --------------------------------------------------------

type batchWriteItemRequest struct {
	RequestItems map[string][]writeRequest `json:"RequestItems"`
}

type writeRequest struct {
	PutRequest    *putRequest    `json:"PutRequest,omitempty"`
	DeleteRequest *deleteRequest `json:"DeleteRequest,omitempty"`
}

type putRequest struct {
	Item Item `json:"Item"`
}

type deleteRequest struct {
	Key Item `json:"Key"`
}

type batchWriteItemResponse struct {
	UnprocessedItems map[string][]writeRequest `json:"UnprocessedItems"`
}

// BatchWriteItem handles the DynamoDB BatchWriteItem operation.
func (h *Handler) BatchWriteItem(w http.ResponseWriter, r *http.Request) {
	var req batchWriteItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.batchWriteItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// batchWriteItemTypedCore is BatchWriteItem's business logic. See
// batchWriteItemTyped (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) batchWriteItemTypedCore(ctx context.Context, req *batchWriteItemRequest) (*batchWriteItemResponse, *protocol.AWSError) {
	// Count total operations — AWS limit is 25.
	var totalOps int
	for _, ops := range req.RequestItems {
		totalOps += len(ops)
	}
	if totalOps > 25 {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Too many items requested for the BatchWriteItem call",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	// Resolve every table and check every key against its schema before
	// applying anything: a batch is not atomic on AWS, but a key-schema fault
	// is a request-validation fault, answered with a ValidationException for
	// the whole call rather than reported per item — so none of the batch's
	// writes may have landed by the time it is raised. The resolved tables are
	// carried into the apply pass so this costs no extra store reads.
	//
	// The same pass enforces two more of the API reference's whole-batch
	// rejections: "Any individual item in a batch exceeds 400 KB" and "You
	// try to perform multiple operations on the same item in the same
	// BatchWriteItem request" — a put and a delete of one key as much as two
	// puts of it. Duplicates are found per table by primary key, which is the
	// only identity a put and a delete share.
	tables := make(map[string]*Table, len(req.RequestItems))
	for tableName, ops := range req.RequestItems {
		table, aerr := h.store.getTable(ctx, tableName)
		if aerr != nil {
			return nil, aerr
		}
		tables[tableName] = table
		seen := make(map[string]bool, len(ops))
		for _, op := range ops {
			var keyOrItem Item
			switch {
			case op.PutRequest != nil:
				if aerr := validateKeySchema(table, op.PutRequest.Item, operandItem); aerr != nil {
					return nil, aerr
				}
				if itemSizeBytes(op.PutRequest.Item) > maxItemSizeBytes {
					return nil, errItemSizeExceeded()
				}
				keyOrItem = op.PutRequest.Item
			case op.DeleteRequest != nil:
				if aerr := validateKeySchema(table, op.DeleteRequest.Key, operandKey); aerr != nil {
					return nil, aerr
				}
				keyOrItem = op.DeleteRequest.Key
			default:
				continue
			}
			id, aerr := itemIdentity(table, keyOrItem)
			if aerr != nil {
				return nil, aerr
			}
			if seen[id] {
				return nil, errValidation("Provided list of item keys contains duplicates")
			}
			seen[id] = true
		}
	}

	for tableName, ops := range req.RequestItems {
		table := tables[tableName]

		// Same gate PutItem/DeleteItem use: a table with GSIs must read the
		// old item to compute index-row diffs (dynamodb-gsi-design.md
		// section 3), in addition to the existing stream-only condition.
		needsOldItem := table.streamEnabled() || len(table.GlobalSecondaryIndexes) > 0

		for _, op := range ops {
			switch {
			case op.PutRequest != nil:
				var oldItem Item
				if needsOldItem {
					oldItem, _ = h.store.getItem(ctx, table, op.PutRequest.Item)
				}
				if aerr := h.store.putItemWithIndexMaintenance(ctx, table, op.PutRequest.Item, oldItem); aerr != nil {
					return nil, aerr
				}
				if table.streamEnabled() {
					h.publishPutStreamRecord(ctx, table, op.PutRequest.Item, oldItem)
				}

			case op.DeleteRequest != nil:
				var oldItem Item
				if needsOldItem {
					oldItem, _ = h.store.getItem(ctx, table, op.DeleteRequest.Key)
				}
				if aerr := h.store.deleteItemWithIndexMaintenance(ctx, table, op.DeleteRequest.Key, oldItem); aerr != nil {
					return nil, aerr
				}
				if table.streamEnabled() && oldItem != nil {
					h.publishDeleteStreamRecord(ctx, table, op.DeleteRequest.Key, oldItem)
				}
			}
		}

		h.bus.Publish(ctx, events.Event{
			Type:    events.DynamoDBItemMutated,
			Source:  "dynamodb",
			Payload: events.ResourcePayload{Name: tableName},
		})
	}

	return &batchWriteItemResponse{
		UnprocessedItems: map[string][]writeRequest{},
	}, nil
}

// itemIdentity returns a string that is equal for two keys or items naming
// the same item of table and different otherwise — the storage-encoded key
// pair, joined by a byte no encoded component contains. It is what the
// batch and transaction paths use to find two operations on one item; the
// caller has already run validateKeySchema, so the key attributes are
// present and correctly typed.
func itemIdentity(table *Table, keyOrItem Item) (string, *protocol.AWSError) {
	hashKey, sortKey, aerr := resolveStorageKeys(table, keyOrItem)
	if aerr != nil {
		return "", aerr
	}
	return hashKey + "\x00" + sortKey, nil
}

// dynamoDefaultPageLimit is the implicit cap on the number of items a single
// Query or Scan response returns when the caller supplies no Limit (or one
// larger than this cap).
//
// Real DynamoDB bounds a Query/Scan response page to 1 MB of item data,
// evaluated before FilterExpression — see "Query" and "Scan" in the API
// reference: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html
// and https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Scan.html
// ("A single Scan/Query only returns a result set that fits within the 1 MB
// size limit"). Overcast does not track accumulated item byte size on this
// path today (deferred — see docs/plans/pagination-plan.md G2's landing
// notes), so this constant approximates that bound with a fixed item count
// instead: 1000 items, chosen as a conservative stand-in assuming AWS's own
// documented average item size guidance (items are commonly well under 1 KB;
// 1000 items keeps a page far below 1 MB for the vast majority of realistic
// test/dev item shapes) while still being large enough that no existing
// behavioral test (all of which use small, human-sized tables) is truncated
// by it. The purpose of the cap is solely to stop a client-observable
// "unbounded single page" response on very large tables — pinning the exact
// number is not part of AWS's compatibility contract the way the 1 MB byte
// bound is, so this is a heuristic, not a wire-fidelity guarantee.
const dynamoDefaultPageLimit = 1000

// effectivePageLimit returns the Limit to apply to a Query/Scan page: the
// caller's explicit Limit when it's a positive value at or under the
// implicit cap, otherwise the cap itself (dynamoDefaultPageLimit) — see its
// doc comment for why an implicit cap exists at all (pagination-plan.md G2).
func effectivePageLimit(requested int) int {
	if requested <= 0 || requested > dynamoDefaultPageLimit {
		return dynamoDefaultPageLimit
	}
	return requested
}

// errConsistentReadOnGSI is AWS's rejection of a strongly consistent read
// against a global secondary index. The Query API reference is categorical
// about it — a GSI is maintained eventually consistently and has no
// strongly-consistent read mode to ask for — so this is a request-validation
// error, not a capability the emulator could choose to honour
// (docs/plans/dynamodb-gsi-design.md §2). LSIs are the deliberate contrast:
// they live in the base table's own partition and do support
// ConsistentRead=true, so only GSI reads are rejected here.
func errConsistentReadOnGSI() *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    "Consistent reads are not supported on global secondary indexes",
		HTTPStatus: http.StatusBadRequest,
	}
}

// resolveCursorPosition returns the index of the first item in items — which
// must already be sorted by (hashName, sortName) in the given direction —
// that lies strictly after cursor's position. Returns 0 when cursor is nil
// (no ExclusiveStartKey: start from the beginning) and len(items) when every
// item is at or before the cursor's position.
//
// This is a positional search, not an identity lookup: cursor need not match
// any item in items by value. That is exactly the fix pagination-plan.md G2
// requires — the old code searched for an item *equal* to the cursor and
// silently restarted from page 1 when that exact item had been deleted
// between pages. A position-based search degrades the same way real
// DynamoDB does: the page simply resumes from where the deleted item would
// have sorted.
//
// Comparisons are type-aware via table's AttributeDefinitions
// (dynamodb-gsi-design.md §2): a Number-typed hashName/sortName compares
// numerically, matching the order items is expected to already be sorted
// in (String/Binary compare as raw UTF-8 byte order, unchanged).
func resolveCursorPosition(items []Item, cursor Item, table *Table, hashName, sortName string, ascending bool) int {
	if cursor == nil {
		return 0
	}
	hashType := keyAttrType(table, hashName)
	sortType := keyAttrType(table, sortName)

	cursorHash := extractKeyValue(cursor[hashName])
	var cursorSort string
	if sortName != "" {
		cursorSort = extractKeyValue(cursor[sortName])
	}

	for i, item := range items {
		itemHash := extractKeyValue(item[hashName])
		var itemSort string
		if sortName != "" {
			itemSort = extractKeyValue(item[sortName])
		}

		hc := compareKeyAttr(hashType, itemHash, cursorHash)
		sc := compareKeyAttr(sortType, itemSort, cursorSort)
		var after bool
		if ascending {
			after = hc > 0 || (hc == 0 && sc > 0)
		} else {
			after = hc < 0 || (hc == 0 && sc < 0)
		}
		if after {
			return i
		}
	}
	return len(items)
}

// extractItemKeys returns only the table primary-key attributes from the given item.
func extractItemKeys(item Item, table *Table) Item {
	keys := Item{}
	hk := table.hashKeyName()
	if v, ok := item[hk]; ok {
		keys[hk] = v
	}
	sk := table.sortKeyName()
	if sk != "" {
		if v, ok := item[sk]; ok {
			keys[sk] = v
		}
	}
	return keys
}

// extractItemKeysWithIndex returns the primary-key attributes PLUS the index key
// attributes for the given item. AWS requires LastEvaluatedKey for index operations
// to include both the table's primary key and the index key attributes.
func extractItemKeysWithIndex(item Item, table *Table, idx *SecondaryIndex) Item {
	keys := extractItemKeys(item, table)
	if idx == nil {
		return keys
	}
	if hk := indexHashKeyName(idx); hk != "" {
		if v, ok := item[hk]; ok {
			keys[hk] = v
		}
	}
	if sk := indexSortKeyName(idx); sk != "" {
		if v, ok := item[sk]; ok {
			keys[sk] = v
		}
	}
	return keys
}

// ---- Tag request / response types -------------------------------------------

type tagResourceRequest struct {
	ResourceArn string `json:"ResourceArn"`
	Tags        []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}

type listTagsOfResourceRequest struct {
	ResourceArn string `json:"ResourceArn"`
}

type listTagsOfResourceResponse struct {
	Tags []serviceutil.TagPair `json:"Tags"`
}

type untagResourceRequest struct {
	ResourceArn string   `json:"ResourceArn"`
	TagKeys     []string `json:"TagKeys"`
}

// ---- Tag handlers ------------------------------------------------------------

var dynamoTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "LimitExceededException",
	InvalidCode:     "ValidationException",
	ExceededMessage: "Tag count exceeded the maximum of 50 tags per resource.",
}

func tableNameFromARN(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return ""
	}
	resource := parts[5]
	if !strings.HasPrefix(resource, "table/") {
		return ""
	}
	rest := strings.TrimPrefix(resource, "table/")
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

func (h *Handler) TagResource(w http.ResponseWriter, r *http.Request) {
	var req tagResourceRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}

	if aerr := serviceutil.ApplyInlineTags(r.Context(), tableName, incoming, dynamoTagCfg,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
		func(ctx context.Context, t *Table) *protocol.AWSError { return h.store.putTable(ctx, t) },
	); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

func (h *Handler) ListTagsOfResource(w http.ResponseWriter, r *http.Request) {
	var req listTagsOfResourceRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	tags, aerr := serviceutil.ListInlineTags(r.Context(), tableName,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
	)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, &listTagsOfResourceResponse{Tags: serviceutil.TagsToList(tags)})
}

func (h *Handler) UntagResource(w http.ResponseWriter, r *http.Request) {
	var req untagResourceRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}

	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	if aerr := serviceutil.RemoveInlineTags(r.Context(), tableName, req.TagKeys,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
		func(ctx context.Context, t *Table) *protocol.AWSError { return h.store.putTable(ctx, t) },
	); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	protocol.WriteJSON(w, r, http.StatusOK, struct{}{})
}

func (h *Handler) tagResourceTyped(ctx context.Context, req *tagResourceRequest) (*struct{}, *protocol.AWSError) {
	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		}
	}

	incoming := make(map[string]string, len(req.Tags))
	for _, t := range req.Tags {
		incoming[t.Key] = t.Value
	}

	if aerr := serviceutil.ApplyInlineTags(ctx, tableName, incoming, dynamoTagCfg,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
		func(ctx context.Context, t *Table) *protocol.AWSError { return h.store.putTable(ctx, t) },
	); aerr != nil {
		return nil, aerr
	}

	return &struct{}{}, nil
}

func (h *Handler) listTagsOfResourceTyped(ctx context.Context, req *listTagsOfResourceRequest) (*listTagsOfResourceResponse, *protocol.AWSError) {
	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		}
	}

	tags, aerr := serviceutil.ListInlineTags(ctx, tableName,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
	)
	if aerr != nil {
		return nil, aerr
	}

	return &listTagsOfResourceResponse{Tags: serviceutil.TagsToList(tags)}, nil
}

func (h *Handler) untagResourceTyped(ctx context.Context, req *untagResourceRequest) (*struct{}, *protocol.AWSError) {
	tableName := tableNameFromARN(req.ResourceArn)
	if tableName == "" {
		return nil, &protocol.AWSError{
			Code: "ResourceNotFoundException", Message: "Table not found: " + req.ResourceArn,
			HTTPStatus: http.StatusNotFound,
		}
	}

	if aerr := serviceutil.RemoveInlineTags(ctx, tableName, req.TagKeys,
		func(ctx context.Context, name string) (*Table, *protocol.AWSError) {
			return h.store.getTable(ctx, name)
		},
		func(ctx context.Context, t *Table) *protocol.AWSError { return h.store.putTable(ctx, t) },
	); aerr != nil {
		return nil, aerr
	}

	return &struct{}{}, nil
}
