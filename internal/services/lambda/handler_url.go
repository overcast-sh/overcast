package lambda

// handler_url.go — Lambda function URL config CRUD.
//
// https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunctionUrlConfig.html
// (and Get/Update/Delete/List siblings), all under API version 2021-10-31:
//
//	POST   /2021-10-31/functions/{FunctionName}/url?Qualifier=
//	GET    /2021-10-31/functions/{FunctionName}/url?Qualifier=
//	PUT    /2021-10-31/functions/{FunctionName}/url?Qualifier=
//	DELETE /2021-10-31/functions/{FunctionName}/url?Qualifier=
//	GET    /2021-10-31/functions/{FunctionName}/urls
//
// AuthType (NONE / AWS_IAM) is accepted and stored but never enforced —
// Overcast is not a security boundary (AGENTS.md). Cors is accepted, stored,
// and returned faithfully but likewise not enforced against invocations.
// InvokeMode only ever behaves as BUFFERED regardless of the stored value —
// there is no streaming function-URL invocation path in this emulator.
//
// See handler_url_invoke.go for the Host-routed invocation side
// ({urlId}.lambda-url.{region}.{base}).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// corsWire mirrors AWS's Cors shape on the wire (PascalCase field names).
type corsWire struct {
	AllowCredentials *bool    `json:"AllowCredentials,omitempty"`
	AllowHeaders     []string `json:"AllowHeaders,omitempty"`
	AllowMethods     []string `json:"AllowMethods,omitempty"`
	AllowOrigins     []string `json:"AllowOrigins,omitempty"`
	ExposeHeaders    []string `json:"ExposeHeaders,omitempty"`
	MaxAge           *int     `json:"MaxAge,omitempty"`
}

func corsToWire(c *CorsConfig) *corsWire {
	if c == nil {
		return nil
	}
	return &corsWire{
		AllowCredentials: c.AllowCredentials,
		AllowHeaders:     c.AllowHeaders,
		AllowMethods:     c.AllowMethods,
		AllowOrigins:     c.AllowOrigins,
		ExposeHeaders:    c.ExposeHeaders,
		MaxAge:           c.MaxAge,
	}
}

func corsFromWire(c *corsWire) *CorsConfig {
	if c == nil {
		return nil
	}
	return &CorsConfig{
		AllowCredentials: c.AllowCredentials,
		AllowHeaders:     c.AllowHeaders,
		AllowMethods:     c.AllowMethods,
		AllowOrigins:     c.AllowOrigins,
		ExposeHeaders:    c.ExposeHeaders,
		MaxAge:           c.MaxAge,
	}
}

// functionUrlConfigRequest is the shared CreateFunctionUrlConfig /
// UpdateFunctionUrlConfig request body shape.
type functionUrlConfigRequest struct {
	AuthType   string    `json:"AuthType,omitempty"`
	Cors       *corsWire `json:"Cors,omitempty"`
	InvokeMode string    `json:"InvokeMode,omitempty"`
}

// functionUrlConfigResponse is the shared Create/Get/Update response shape.
type functionUrlConfigResponse struct {
	FunctionArn      string    `json:"FunctionArn"`
	FunctionUrl      string    `json:"FunctionUrl"`
	AuthType         string    `json:"AuthType"`
	Cors             *corsWire `json:"Cors,omitempty"`
	InvokeMode       string    `json:"InvokeMode"`
	CreationTime     string    `json:"CreationTime,omitempty"`
	LastModifiedTime string    `json:"LastModifiedTime,omitempty"`
}

func (h *Handler) functionUrlConfigToResponse(r *http.Request, c *FunctionURLConfig) functionUrlConfigResponse {
	return functionUrlConfigResponse{
		FunctionArn:      c.FunctionArn,
		FunctionUrl:      buildFunctionURL(h.cfg, r, c.UrlID, c.Region),
		AuthType:         c.AuthType,
		Cors:             corsToWire(c.Cors),
		InvokeMode:       c.InvokeMode,
		CreationTime:     c.CreationTime,
		LastModifiedTime: c.LastModifiedTime,
	}
}

// newFunctionURLID generates a 32-character lowercase-hex ID, in the same
// style as (though not cryptographically equivalent to) AWS's function URL
// IDs — real AWS IDs are also opaque lowercase-alphanumeric tokens embedded
// in the FunctionUrl subdomain.
func newFunctionURLID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// buildFunctionURL constructs the caller-facing Lambda function URL:
// http://{urlId}.lambda-url.{region}.{host}:{port}/
//
// It delegates to the shared minting helper that every service handing back a
// host-routed URL now uses, so the grammar cannot drift between services or
// away from what the router accepts. The label is the same constant the router
// dispatches on.
func buildFunctionURL(cfg *config.Config, r *http.Request, urlID, region string) string {
	return serviceutil.HostRoutedURL(cfg, r, middleware.LabelLambdaURL, urlID, region, "/")
}

// CreateFunctionUrlConfig handles POST /2021-10-31/functions/{name}/url.
func (h *Handler) CreateFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	qualifier := r.URL.Query().Get("Qualifier")
	ctx := r.Context()

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	var req functionUrlConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "Invalid request body: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	authType := req.AuthType
	if authType == "" {
		authType = "AWS_IAM" // AWS's own default when AuthType is omitted.
	}
	if authType != "NONE" && authType != "AWS_IAM" {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "AuthType must be NONE or AWS_IAM",
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	invokeMode := req.InvokeMode
	if invokeMode == "" {
		invokeMode = "BUFFERED"
	}

	if existing, aerr := h.ls.getFunctionURLConfig(ctx, name, qualifier); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	} else if existing != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceConflictException",
			Message:    "Function URL config already exists for function: " + name,
			HTTPStatus: http.StatusConflict,
		})
		return
	}

	now := h.clk.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
	c := &FunctionURLConfig{
		FunctionName:     name,
		FunctionArn:      fn.ARN,
		Qualifier:        qualifier,
		AuthType:         authType,
		Cors:             corsFromWire(req.Cors),
		InvokeMode:       invokeMode,
		UrlID:            newFunctionURLID(),
		Region:           h.ls.region(ctx),
		CreationTime:     now,
		LastModifiedTime: now,
	}
	if aerr := h.ls.putFunctionURLConfig(ctx, c); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.LambdaFunctionUrlConfigCreated,
			Time:    h.clk.Now(),
			Source:  "lambda",
			Payload: events.LambdaFunctionPayload{Name: fn.Name, ARN: fn.ARN},
		})
	}

	protocol.WriteRESTJSON(w, r, http.StatusCreated, h.functionUrlConfigToResponse(r, c))
}

// GetFunctionUrlConfig handles GET /2021-10-31/functions/{name}/url.
func (h *Handler) GetFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	qualifier := r.URL.Query().Get("Qualifier")

	c, aerr := h.ls.getFunctionURLConfig(r.Context(), name, qualifier)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if c == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function URL config not found for function: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, h.functionUrlConfigToResponse(r, c))
}

// UpdateFunctionUrlConfig handles PUT /2021-10-31/functions/{name}/url.
func (h *Handler) UpdateFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	qualifier := r.URL.Query().Get("Qualifier")
	ctx := r.Context()

	c, aerr := h.ls.getFunctionURLConfig(ctx, name, qualifier)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if c == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function URL config not found for function: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	var req functionUrlConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "InvalidParameterValueException",
			Message:    "Invalid request body: " + err.Error(),
			HTTPStatus: http.StatusBadRequest,
		})
		return
	}
	if req.AuthType != "" {
		if req.AuthType != "NONE" && req.AuthType != "AWS_IAM" {
			protocol.WriteJSONError(w, r, &protocol.AWSError{
				Code:       "InvalidParameterValueException",
				Message:    "AuthType must be NONE or AWS_IAM",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		c.AuthType = req.AuthType
	}
	if req.Cors != nil {
		c.Cors = corsFromWire(req.Cors)
	}
	if req.InvokeMode != "" {
		c.InvokeMode = req.InvokeMode
	}
	c.LastModifiedTime = h.clk.Now().UTC().Format("2006-01-02T15:04:05.000+0000")

	if aerr := h.ls.putFunctionURLConfig(ctx, c); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.LambdaFunctionUrlConfigUpdated,
			Time:    h.clk.Now(),
			Source:  "lambda",
			Payload: events.LambdaFunctionPayload{Name: c.FunctionName, ARN: c.FunctionArn},
		})
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, h.functionUrlConfigToResponse(r, c))
}

// DeleteFunctionUrlConfig handles DELETE /2021-10-31/functions/{name}/url.
func (h *Handler) DeleteFunctionUrlConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	qualifier := r.URL.Query().Get("Qualifier")
	ctx := r.Context()

	c, aerr := h.ls.getFunctionURLConfig(ctx, name, qualifier)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if c == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function URL config not found for function: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	if aerr := h.ls.deleteFunctionURLConfig(ctx, name, qualifier); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	if h.bus != nil {
		h.bus.Publish(ctx, events.Event{
			Type:    events.LambdaFunctionUrlConfigDeleted,
			Time:    h.clk.Now(),
			Source:  "lambda",
			Payload: events.LambdaFunctionPayload{Name: c.FunctionName, ARN: c.FunctionArn},
		})
	}

	log.Debug("delete function url config", zap.String("function", name), zap.String("qualifier", qualifier))
	w.WriteHeader(http.StatusNoContent)
}

// listFunctionUrlConfigsResponse is the ListFunctionUrlConfigs response shape.
type listFunctionUrlConfigsResponse struct {
	FunctionUrlConfigs []functionUrlConfigResponseWithName `json:"FunctionUrlConfigs"`
	NextMarker         string                              `json:"NextMarker,omitempty"`
}

// functionUrlConfigResponseWithName adds FunctionArn-adjacent identifying
// fields AWS includes in the list response entries.
type functionUrlConfigResponseWithName struct {
	functionUrlConfigResponse
}

// ListFunctionUrlConfigs handles GET /2021-10-31/functions/{name}/urls.
func (h *Handler) ListFunctionUrlConfigs(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	fn, aerr := h.ls.getFunction(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, &protocol.AWSError{
			Code:       "ResourceNotFoundException",
			Message:    "Function not found: " + name,
			HTTPStatus: http.StatusNotFound,
		})
		return
	}

	configs, aerr := h.ls.listFunctionURLConfigs(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	resp := listFunctionUrlConfigsResponse{FunctionUrlConfigs: make([]functionUrlConfigResponseWithName, 0, len(configs))}
	for _, c := range configs {
		resp.FunctionUrlConfigs = append(resp.FunctionUrlConfigs, functionUrlConfigResponseWithName{h.functionUrlConfigToResponse(r, c)})
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, resp)
}

// HostRouteRewrite adapts a Host-routed Lambda function URL invocation
// ({urlId}.lambda-url.{region}.{base}/...) to the emulator's internal
// invocation route (see handler_url_invoke.go).
//
// This rewrite doesn't stamp a region onto the request context, but not
// because the region is unneeded: middleware.Region has already read it off
// the Host by the time this runs (regionFromHost parses the same
// hostRouteLabels grammar the router dispatched on). The region matters —
// getFunctionURLConfigByURLID scans every region, but the getFunction call
// right after it in InvokeFunctionURL is region-scoped, so without the hint a
// function URL created outside the default region resolves its config and
// then 404s on the function behind it.
func (s *Service) HostRouteRewrite(r *http.Request, m middleware.HostRouteMatch) {
	middleware.PrefixPath(r, "/_overcast/lambda/url-invoke/"+m.ID)
}
