package cloudfront

import (
	"encoding/xml"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// ─── CloudFront Functions: Create ───────────────────────────────────────────

// CreateFunction handles POST /2020-05-31/function.
func (h *Handler) CreateFunction(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("CreateFunction")

	var req functionConfigWrapper
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	if req.Name == "" {
		writeError(w, r, &protocol.AWSError{
			Code: "InvalidArgument", Message: "Function name is required.", HTTPStatus: 400,
		})
		return
	}

	// Check for duplicate name.
	existing, _ := h.store.GetFunction(r.Context(), req.Name)
	if existing != nil {
		writeError(w, r, errFunctionAlreadyExists(req.Name))
		return
	}

	now := h.clk.Now()
	accountID := h.accountID()
	region := "us-east-1"

	fn := &CloudFrontFunction{
		Name:           req.Name,
		Status:         "UNPUBLISHED",
		Stage:          "DEVELOPMENT",
		FunctionConfig: req.FunctionConfig,
		FunctionMetadata: FunctionMetadata{
			FunctionARN:      fmt.Sprintf("arn:aws:cloudfront::%s:function/%s", accountID, req.Name),
			Stage:            "DEVELOPMENT",
			CreatedTime:      now,
			LastModifiedTime: now,
		},
		FunctionCode: req.FunctionCode,
		Version:      1,
	}
	_ = region // reserved for future multi-region support

	if storeErr := h.store.PutFunction(r.Context(), fn); storeErr != nil {
		log.LogStateError(r, "put function", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("function created", zap.String("name", req.Name))

	resp := functionSummaryXML(h.buildFunctionSummary(fn))
	w.Header().Set("ETag", computeETag(fn.Version))
	w.Header().Set("Location", fmt.Sprintf("/2020-05-31/function/%s", req.Name))
	writeXML(w, r, http.StatusCreated, &resp)
}

// ─── CloudFront Functions: Describe ─────────────────────────────────────────

// DescribeFunction handles GET /2020-05-31/function/{name}/describe.
func (h *Handler) DescribeFunction(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		h.log.WithOperation("DescribeFunction").LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	resp := functionDescribeXML{
		Name:             fn.Name,
		Status:           fn.Status,
		FunctionConfig:   fn.FunctionConfig,
		FunctionMetadata: fn.FunctionMetadata,
	}
	w.Header().Set("ETag", computeETag(fn.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── CloudFront Functions: Get ──────────────────────────────────────────────

// GetFunction handles GET /2020-05-31/function/{name}.
// Returns the function code in the response body with metadata in headers.
func (h *Handler) GetFunction(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		h.log.WithOperation("GetFunction").LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	w.Header().Set("ETag", computeETag(fn.Version))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	// The AWS API returns the raw function code (base64-encoded).
	w.Write([]byte(fn.FunctionCode)) //nolint:errcheck
}

// ─── CloudFront Functions: Update ───────────────────────────────────────────

// UpdateFunction handles PUT /2020-05-31/function/{name}.
func (h *Handler) UpdateFunction(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("UpdateFunction")

	name := chi.URLParam(r, "name")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		log.LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	if ifMatch != computeETag(fn.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	var req functionUpdateWrapper
	if err := xml.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Debug("decode error", zap.Error(err))
		writeError(w, r, &protocol.AWSError{
			Code: "MalformedXML", Message: "The XML you provided was not well-formed.", HTTPStatus: 400,
		})
		return
	}

	fn.FunctionConfig = req.FunctionConfig
	fn.FunctionCode = req.FunctionCode
	fn.FunctionMetadata.LastModifiedTime = h.clk.Now()
	fn.Version++

	if storeErr := h.store.PutFunction(r.Context(), fn); storeErr != nil {
		log.LogStateError(r, "put function", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("function updated", zap.String("name", name))

	resp := functionSummaryXML(h.buildFunctionSummary(fn))
	w.Header().Set("ETag", computeETag(fn.Version))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── CloudFront Functions: Delete ───────────────────────────────────────────

// DeleteFunction handles DELETE /2020-05-31/function/{name}.
func (h *Handler) DeleteFunction(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("DeleteFunction")

	name := chi.URLParam(r, "name")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		log.LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	if ifMatch != computeETag(fn.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	if storeErr := h.store.DeleteFunction(r.Context(), name); storeErr != nil {
		log.LogStateError(r, "delete function", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("function deleted", zap.String("name", name))
	protocol.WriteEmpty(w, r, http.StatusNoContent)
}

// ─── CloudFront Functions: List ─────────────────────────────────────────────

// ListFunctions handles GET /2020-05-31/function.
func (h *Handler) ListFunctions(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("ListFunctions")

	all, err := h.store.ListFunctions(r.Context())
	if err != nil {
		log.LogStateError(r, "list functions", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	// Filter by Stage query parameter if provided.
	stage := r.URL.Query().Get("Stage")
	maxItems := serviceutil.QueryInt(r, "MaxItems", 100)

	summaries := make([]functionSummaryXML, 0, len(all))
	for _, fn := range all {
		if stage != "" && fn.Stage != stage {
			continue
		}
		summaries = append(summaries, h.buildFunctionSummary(fn))
	}

	result := functionListXML{
		MaxItems: maxItems,
		Quantity: len(summaries),
		Items:    summaries,
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── CloudFront Functions: Test ─────────────────────────────────────────────

// TestFunction handles POST /2020-05-31/function/{name}/test.
// The emulator does not execute JS code — it returns a mock success result.
func (h *Handler) TestFunction(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		h.log.WithOperation("TestFunction").LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	result := testResultXML{
		FunctionSummary:    h.buildFunctionSummary(fn),
		ComputeUtilization: "12",
		FunctionOutput:     "",
	}

	writeXML(w, r, http.StatusOK, &result)
}

// ─── CloudFront Functions: Publish ──────────────────────────────────────────

// PublishFunction handles POST /2020-05-31/function/{name}/publish.
// Promotes DEVELOPMENT stage function to LIVE.
func (h *Handler) PublishFunction(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithOperation("PublishFunction")

	name := chi.URLParam(r, "name")
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, r, errInvalidIfMatch())
		return
	}

	fn, err := h.store.GetFunction(r.Context(), name)
	if err != nil {
		log.LogStateError(r, "get function", protocol.Wrap(protocol.ErrInternalError, err))
		writeError(w, r, protocol.ErrInternalError)
		return
	}
	if fn == nil {
		writeError(w, r, errNoSuchFunction(name))
		return
	}

	if ifMatch != computeETag(fn.Version) {
		writeError(w, r, errPreconditionFailed())
		return
	}

	fn.Stage = "LIVE"
	fn.Status = "UNASSOCIATED"
	fn.FunctionMetadata.Stage = "LIVE"
	fn.FunctionMetadata.LastModifiedTime = h.clk.Now()

	if storeErr := h.store.PutFunction(r.Context(), fn); storeErr != nil {
		log.LogStateError(r, "put function", protocol.Wrap(protocol.ErrInternalError, storeErr))
		writeError(w, r, protocol.ErrInternalError)
		return
	}

	log.Info("function published", zap.String("name", name))

	resp := functionSummaryXML(h.buildFunctionSummary(fn))
	writeXML(w, r, http.StatusOK, &resp)
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// buildFunctionSummary constructs the XML summary for a function.
func (h *Handler) buildFunctionSummary(fn *CloudFrontFunction) functionSummaryXML {
	return functionSummaryXML{
		Name:             fn.Name,
		Status:           fn.Status,
		FunctionConfig:   fn.FunctionConfig,
		FunctionMetadata: fn.FunctionMetadata,
	}
}
