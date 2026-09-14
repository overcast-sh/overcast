package logs

// handler_stubs.go contains every CloudWatch Logs handler that is not yet
// implemented. Each method returns HTTP 501 Not Implemented with
// x-emulator-unsupported: true.
//
// Convention: when an operation is implemented, move its method body out of
// this file and into handler.go. handler.go is the authoritative inventory
// of what actually works.

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// PutSubscriptionFilter creates or updates a subscription filter.
// TODO(priority:P3): implement PutSubscriptionFilter
// AWS docs: https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutSubscriptionFilter.html
func (h *Handler) PutSubscriptionFilter(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}

// StartQuery schedules a CloudWatch Logs Insights query.
// TODO(priority:P3): implement StartQuery
// AWS docs: https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_StartQuery.html
func (h *Handler) StartQuery(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}

// GetQueryResults retrieves results from a CloudWatch Logs Insights query.
// TODO(priority:P3): implement GetQueryResults
// AWS docs: https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetQueryResults.html
func (h *Handler) GetQueryResults(w http.ResponseWriter, r *http.Request) {
	protocol.NotImplementedJSON(w, r)
}
