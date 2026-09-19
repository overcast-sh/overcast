package dynamodb

// handler_update.go implements the DynamoDB UpdateItem operation.
// Supports all UpdateExpression clauses (SET, REMOVE, ADD, DELETE) including
// SET functions (if_not_exists, list_append) and arithmetic (+/-).
// ConditionExpression, ExpressionAttributeNames (#alias) and
// ExpressionAttributeValues (:placeholder) are fully resolved.

import (
	"context"
	"net/http"

	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

type updateItemRequest struct {
	TableName                 string               `json:"TableName"`
	Key                       Item                 `json:"Key"`
	UpdateExpression          string               `json:"UpdateExpression,omitempty"`
	ConditionExpression       string               `json:"ConditionExpression,omitempty"`
	ExpressionAttributeNames  map[string]string    `json:"ExpressionAttributeNames,omitempty"`
	ExpressionAttributeValues map[string]attrValue `json:"ExpressionAttributeValues,omitempty"`
	ReturnValues              string               `json:"ReturnValues,omitempty"`
}

type updateItemResponse struct {
	Attributes Item `json:"Attributes,omitempty"`
}

// UpdateItem handles the DynamoDB UpdateItem operation.
// Supports all UpdateExpression clauses. Upserts the item if it does not exist.
// AWS docs: https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateItem.html
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) {
	var req updateItemRequest
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	resp, aerr := h.updateItemTyped(r.Context(), &req)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, resp)
}

// updateItemTypedCore is UpdateItem's business logic. See updateItemTyped
// (metrics_dynamodb.go) for the metrics-recording wrapper.
func (h *Handler) updateItemTypedCore(ctx context.Context, req *updateItemRequest) (*updateItemResponse, *protocol.AWSError) {
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

	// Load existing item (may be nil — upsert semantics).
	existing, aerr := h.store.getItem(ctx, table, req.Key)
	if aerr != nil {
		return nil, aerr
	}

	// Evaluate ConditionExpression against the existing item, if any.
	if req.ConditionExpression != "" {
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

	// Start with the existing item (or a new item containing just the key).
	var item Item
	if existing != nil {
		item = cloneItem(existing)
	} else {
		item = cloneItem(req.Key)
	}

	// Apply UpdateExpression if present.
	if req.UpdateExpression != "" {
		if err := applyUpdateExpression(item, req.UpdateExpression,
			req.ExpressionAttributeNames, req.ExpressionAttributeValues); err != nil {
			return nil, &protocol.AWSError{
				Code:       "ValidationException",
				Message:    err.Error(),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}

	// The item as it would be stored must fit the 400 KB ceiling
	// (item_size.go); item is a clone, so a rejected update leaves the
	// stored item untouched.
	if itemSizeBytes(item) > maxItemSizeBytes {
		return nil, errItemSizeToUpdateExceeded()
	}

	// existing was already fetched unconditionally above (upsert semantics
	// require it regardless of streams/GSIs) — GSI index-row maintenance
	// (dynamodb-gsi-design.md section 3) rides that same read at zero extra
	// cost, unlike PutItem/DeleteItem which had to extend their existing
	// conditional gates for it.
	if aerr := h.store.putItemWithIndexMaintenance(ctx, table, item, existing); aerr != nil {
		return nil, aerr
	}

	if table.streamEnabled() {
		h.publishPutStreamRecord(ctx, table, item, existing)
	}
	h.bus.Publish(ctx, events.Event{
		Type:    events.DynamoDBItemMutated,
		Source:  "dynamodb",
		Payload: events.ResourcePayload{Name: req.TableName},
	})

	switch req.ReturnValues {
	case "ALL_OLD":
		attrs := existing
		if attrs == nil {
			attrs = Item{}
		}
		return &updateItemResponse{Attributes: attrs}, nil
	case "ALL_NEW":
		return &updateItemResponse{Attributes: item}, nil
	case "UPDATED_NEW":
		changed := diffItemKeys(existing, item)
		attrs := make(Item, len(changed))
		for _, k := range changed {
			if v, ok := item[k]; ok {
				attrs[k] = v
			}
		}
		return &updateItemResponse{Attributes: attrs}, nil
	case "UPDATED_OLD":
		changed := diffItemKeys(existing, item)
		attrs := make(Item, len(changed))
		for _, k := range changed {
			if existing != nil {
				if v, ok := existing[k]; ok {
					attrs[k] = v
				}
			}
		}
		return &updateItemResponse{Attributes: attrs}, nil
	default:
		return &updateItemResponse{}, nil
	}
}

// cloneItem returns a shallow copy of an Item map so modifications don't
// alias the stored value.
func cloneItem(src Item) Item {
	dst := make(Item, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// diffItemKeys returns the set of top-level attribute names that differ
// between old and new (including keys present in one but not the other).
func diffItemKeys(old, new Item) []string {
	seen := make(map[string]bool)
	var changed []string
	for k, nv := range new {
		ov, ok := old[k]
		if !ok || !attrValueEqual(ov, nv) {
			if !seen[k] {
				changed = append(changed, k)
				seen[k] = true
			}
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok && !seen[k] {
			changed = append(changed, k)
			seen[k] = true
		}
	}
	return changed
}
