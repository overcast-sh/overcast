package dynamodb

// handler_transact.go implements DynamoDB TransactWriteItems and TransactGetItems.
// Both operations are all-or-nothing: if any condition check fails, the entire
// transaction is rolled back (TransactWriteItems) or returns an error (TransactGetItems).

import (
	"context"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ---- TransactWriteItems ----------------------------------------------------

type transactWriteItemsRequest struct {
	TransactItems []transactWriteItem `json:"TransactItems"`
}

type transactWriteItem struct {
	ConditionCheck *transactConditionCheck `json:"ConditionCheck,omitempty"`
	Put            *transactPut            `json:"Put,omitempty"`
	Delete         *transactDelete         `json:"Delete,omitempty"`
	Update         *transactUpdate         `json:"Update,omitempty"`
}

type transactConditionCheck struct {
	TableName                 string            `json:"TableName"`
	Key                       Item              `json:"Key"`
	ConditionExpression       string            `json:"ConditionExpression"`
	ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues Item              `json:"ExpressionAttributeValues,omitempty"`
}

type transactPut struct {
	TableName                 string            `json:"TableName"`
	Item                      Item              `json:"Item"`
	ConditionExpression       string            `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues Item              `json:"ExpressionAttributeValues,omitempty"`
}

type transactDelete struct {
	TableName                 string            `json:"TableName"`
	Key                       Item              `json:"Key"`
	ConditionExpression       string            `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues Item              `json:"ExpressionAttributeValues,omitempty"`
}

type transactUpdate struct {
	TableName                 string               `json:"TableName"`
	Key                       Item                 `json:"Key"`
	UpdateExpression          string               `json:"UpdateExpression"`
	ConditionExpression       string               `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues,omitempty"`
}

// TransactWriteItems handles the DynamoDB TransactWriteItems operation.
// All-or-nothing: validates all conditions first, then applies all mutations.
// AWS docs: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html
func (h *Handler) TransactWriteItems(w http.ResponseWriter, r *http.Request) {
	var req transactWriteItemsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.transactWriteItemsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// transactWriteItemsTypedCore is TransactWriteItems' business logic. See
// transactWriteItemsTyped (metrics_dynamodb.go) for the metrics-recording
// wrapper.
func (h *Handler) transactWriteItemsTypedCore(ctx context.Context, req *transactWriteItemsRequest) (*struct{}, *protocol.AWSError) {
	if len(req.TransactItems) > 100 {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Member must have length less than or equal to 100",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	// Phase 1: Validate all tables exist, load existing items, evaluate conditions.
	type resolvedAction struct {
		table   *Table
		oldItem Item // for stream records (Put/Delete/Update)
	}
	resolved := make([]resolvedAction, len(req.TransactItems))
	cancellationReasons := make([]string, len(req.TransactItems))
	cancelled := false
	// Every item the transaction has touched so far, by table and primary
	// key: "no two actions can target the same item" (API reference), and
	// the fault is answered before anything is applied.
	targeted := make(map[string]bool, len(req.TransactItems))

	for i, txItem := range req.TransactItems {
		var tableName string
		var key Item
		var operand keyOperand
		var condExpr string
		var condNames map[string]string
		var condValues Item

		switch {
		case txItem.ConditionCheck != nil:
			cc := txItem.ConditionCheck
			tableName = cc.TableName
			key = cc.Key
			operand = operandKey
			condExpr = cc.ConditionExpression
			condNames = cc.ExpressionAttributeNames
			condValues = cc.ExpressionAttributeValues
		case txItem.Put != nil:
			p := txItem.Put
			tableName = p.TableName
			key = p.Item // extractKeys will pull out proper keys
			operand = operandItem
			condExpr = p.ConditionExpression
			condNames = p.ExpressionAttributeNames
			condValues = p.ExpressionAttributeValues
		case txItem.Delete != nil:
			d := txItem.Delete
			tableName = d.TableName
			key = d.Key
			operand = operandKey
			condExpr = d.ConditionExpression
			condNames = d.ExpressionAttributeNames
			condValues = d.ExpressionAttributeValues
		case txItem.Update != nil:
			u := txItem.Update
			tableName = u.TableName
			key = u.Key
			operand = operandKey
			condExpr = u.ConditionExpression
			condNames = u.ExpressionAttributeNames
			// Convert ExpressionAttributeValues from map[string]attrValue to Item
			if u.ExpressionAttributeValues != nil {
				condValues = make(Item, len(u.ExpressionAttributeValues))
				for k, v := range u.ExpressionAttributeValues {
					condValues[k] = v
				}
			}
		default:
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "TransactItems member must contain exactly one of Put, Delete, Update, or ConditionCheck",
				HTTPStatus: http.StatusBadRequest,
			}
		}

		table, aerr := h.store.getTable(ctx, tableName)
		if aerr != nil {
			return nil, aerr
		}
		resolved[i].table = table

		// A key-schema fault is a parameter-validation fault, which AWS
		// answers before it attempts the transaction at all — so it surfaces
		// as a plain ValidationException rather than as a
		// TransactionCanceledException carrying a ValidationError cancellation
		// reason. The reasons AWS documents under ValidationError are the ones
		// only knowable while applying the transaction (item size, LSI size,
		// update-expression faults), not a static check of the supplied key.
		// Raising it here, in phase 1, is also what keeps the transaction
		// all-or-nothing: phase 2 has not written anything yet.
		if aerr := validateKeySchema(table, key, operand); aerr != nil {
			return nil, aerr
		}

		// Two operations on one item are the same kind of static fault as a
		// key-schema mismatch, and AWS answers them the same way: a plain
		// ValidationException with this message (moto's
		// MultipleTransactionsException), not a cancellation.
		id, aerr := itemIdentity(table, key)
		if aerr != nil {
			return nil, aerr
		}
		id = tableName + "\x00" + id
		if targeted[id] {
			return nil, errValidation("Transaction request cannot include multiple operations on one item")
		}
		targeted[id] = true

		// An oversized Put is different: the API reference lists "An item
		// size becomes too large (larger than 400 KB)" among the reasons a
		// transaction is *cancelled*, with a ValidationError reason at that
		// position, so it is recorded as one rather than raised outright.
		if txItem.Put != nil && itemSizeBytes(txItem.Put.Item) > maxItemSizeBytes {
			cancellationReasons[i] = "ValidationError"
			cancelled = true
		}

		// Load existing item for condition checks and stream records.
		existing, aerr := h.store.getItem(ctx, table, key)
		if aerr != nil {
			return nil, aerr
		}
		resolved[i].oldItem = existing

		// Evaluate condition expression if present.
		if condExpr != "" {
			filter, err := compileFilter(condExpr, condNames, condValues)
			if err != nil {
				return nil, &protocol.AWSError{
					Code:       "ValidationException",
					Message:    err.Error(),
					HTTPStatus: http.StatusBadRequest,
				}
			}
			checkItem := existing
			if checkItem == nil {
				checkItem = Item{} // empty item for attribute_not_exists etc.
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
				cancellationReasons[i] = "ConditionalCheckFailed"
				cancelled = true
			}
		}
	}

	if cancelled {
		return nil, &protocol.AWSError{
			Code:       "TransactionCanceledException",
			Message:    "Transaction cancelled, please refer cancellation reasons for specific reasons [" + joinReasons(cancellationReasons) + "]",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	// Phase 2: Apply all mutations (conditions already validated).
	mutatedTables := make(map[string]bool)

	for i, txItem := range req.TransactItems {
		table := resolved[i].table
		oldItem := resolved[i].oldItem

		switch {
		case txItem.ConditionCheck != nil:
			// No mutation needed — condition already validated.

		case txItem.Put != nil:
			// oldItem was already fetched unconditionally in phase 1 above
			// (needed for ConditionExpression evaluation regardless of
			// GSIs) — GSI index-row maintenance
			// (dynamodb-gsi-design.md §3) rides that same read at zero
			// extra cost, exactly like PutItem/BatchWriteItem's
			// putItemWithIndexMaintenance calls.
			if aerr := h.store.putItemWithIndexMaintenance(ctx, table, txItem.Put.Item, oldItem); aerr != nil {
				return nil, aerr
			}
			if table.streamEnabled() {
				h.publishPutStreamRecord(ctx, table, txItem.Put.Item, oldItem)
			}
			mutatedTables[table.TableName] = true

		case txItem.Delete != nil:
			// Same zero-extra-read situation as Put above — oldItem is
			// diffed against nil to produce the GSI index-row deletes.
			if aerr := h.store.deleteItemWithIndexMaintenance(ctx, table, txItem.Delete.Key, oldItem); aerr != nil {
				return nil, aerr
			}
			if table.streamEnabled() && oldItem != nil {
				h.publishDeleteStreamRecord(ctx, table, txItem.Delete.Key, oldItem)
			}
			mutatedTables[table.TableName] = true

		case txItem.Update != nil:
			u := txItem.Update
			item := cloneItem(u.Key)
			if oldItem != nil {
				item = cloneItem(oldItem)
			}
			if u.UpdateExpression != "" {
				if err := applyUpdateExpression(item, u.UpdateExpression,
					u.ExpressionAttributeNames, u.ExpressionAttributeValues); err != nil {
					return nil, &protocol.AWSError{
						Code:       "ValidationException",
						Message:    err.Error(),
						HTTPStatus: http.StatusBadRequest,
					}
				}
			}
			// Same rationale as UpdateItem's own handler
			// (handler_update.go): oldItem is already read above for
			// upsert semantics, so GSI index-row maintenance is free here
			// too.
			if aerr := h.store.putItemWithIndexMaintenance(ctx, table, item, oldItem); aerr != nil {
				return nil, aerr
			}
			if table.streamEnabled() {
				h.publishPutStreamRecord(ctx, table, item, oldItem)
			}
			mutatedTables[table.TableName] = true
		}
	}

	// Publish mutation events for all affected tables.
	for tableName := range mutatedTables {
		h.bus.Publish(ctx, events.Event{
			Type:    events.DynamoDBItemMutated,
			Source:  "dynamodb",
			Payload: events.ResourcePayload{Name: tableName},
		})
	}

	return &struct{}{}, nil
}

// joinReasons formats cancellation reasons for the error message.
func joinReasons(reasons []string) string {
	var b strings.Builder
	for i, r := range reasons {
		if i > 0 {
			b.WriteString(", ")
		}
		if r == "" {
			b.WriteString("None")
		} else {
			b.WriteString(r)
		}
	}
	return b.String()
}

// ---- TransactGetItems ------------------------------------------------------

type transactGetItemsRequest struct {
	TransactItems []transactGetItem `json:"TransactItems"`
}

type transactGetItem struct {
	Get *transactGet `json:"Get"`
}

type transactGet struct {
	TableName string `json:"TableName"`
	Key       Item   `json:"Key"`
}

type transactGetItemsResponse struct {
	Responses []transactGetResponse `json:"Responses"`
}

type transactGetResponse struct {
	Item Item `json:"Item"`
}

// TransactGetItems handles the DynamoDB TransactGetItems operation.
// AWS docs: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactGetItems.html
func (h *Handler) TransactGetItems(w http.ResponseWriter, r *http.Request) {
	var req transactGetItemsRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.transactGetItemsTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// transactGetItemsTypedCore is TransactGetItems' business logic. See
// transactGetItemsTyped (metrics_dynamodb.go) for the metrics-recording
// wrapper.
func (h *Handler) transactGetItemsTypedCore(ctx context.Context, req *transactGetItemsRequest) (*transactGetItemsResponse, *protocol.AWSError) {
	if len(req.TransactItems) > 100 {
		return nil, &protocol.AWSError{
			Code:       "ValidationException",
			Message:    "Member must have length less than or equal to 100",
			HTTPStatus: http.StatusBadRequest,
		}
	}

	responses := make([]transactGetResponse, len(req.TransactItems))

	for i, txItem := range req.TransactItems {
		if txItem.Get == nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    "TransactItems member must contain Get",
				HTTPStatus: http.StatusBadRequest,
			}
		}

		table, aerr := h.store.getTable(ctx, txItem.Get.TableName)
		if aerr != nil {
			return nil, aerr
		}
		if aerr := validateKeySchema(table, txItem.Get.Key, operandKey); aerr != nil {
			return nil, aerr
		}

		item, aerr := h.store.getItem(ctx, table, txItem.Get.Key)
		if aerr != nil {
			return nil, aerr
		}

		responses[i] = transactGetResponse{Item: item}
	}

	return &transactGetItemsResponse{
		Responses: responses,
	}, nil
}
