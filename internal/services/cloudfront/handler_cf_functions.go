package cloudfront

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/dop251/goja"
	"go.uber.org/zap"
)

// cfFunctionResult is the parsed return value from a CloudFront Function.
type cfFunctionResult struct {
	// isResponse indicates the function returned an HTTP response (short-circuit).
	isResponse bool
	statusCode int
	statusDesc string
	// Modified request fields (when isResponse is false).
	uri    string
	method string
	// Key-value headers from the function return (lowercase name -> value).
	headers map[string]string
}

// The two ways a CloudFront Function fails, as AWS counts and answers them
// (viewing-cloudfront-metrics). Either one ends the request: the origin is not
// called, or its response is not delivered.
var (
	// errFunctionExecution: the function "fails to complete successfully" —
	// it throws, or its code does not compile or defines no handler. The
	// viewer gets a 503 (http-503-service-unavailable).
	errFunctionExecution = errors.New("function execution error")
	// errFunctionValidation: the function ran but returned an invalid event
	// object. The viewer gets a 502 (http-502-bad-gateway, "CloudFront
	// function validation error").
	errFunctionValidation = errors.New("function validation error")
)

// functionErrorStatus is the status a viewer gets for a failed function.
func functionErrorStatus(err error) int {
	if errors.Is(err, errFunctionValidation) {
		return http.StatusBadGateway
	}
	return http.StatusServiceUnavailable
}

// runViewerRequest executes all viewer-request CloudFront Functions for the
// matching cache behavior.
//
// reqPath is percent-encoded, and is what event.request.uri carries: AWS hands
// a function the URI as it arrived. A uri the function returns is brought back
// to that form with escapeURIPath before it is used.
//
// Returns:
//   - result: parsed output (may include an early HTTP response)
//   - err: errFunctionExecution or errFunctionValidation, wrapped, if a
//     function failed; the request must then be refused, not forwarded
func (h *Handler) runViewerRequest(
	r *http.Request,
	distID string,
	domainName string,
	reqPath string,
	fas *FunctionAssociations,
) (*cfFunctionResult, error) {
	log := h.log.WithRecorder(r.Context())
	if fas == nil || len(fas.Items) == 0 {
		return nil, nil
	}
	var result *cfFunctionResult
	for _, fa := range fas.Items {
		if fa.EventType != "viewer-request" {
			continue
		}
		fn, ferr := h.store.GetFunctionByARN(r.Context(), fa.FunctionARN)
		if ferr != nil || fn == nil {
			log.Warn("viewer-request function not found",
				zap.String("arn", fa.FunctionARN))
			continue
		}
		event := buildViewerRequestEvent(r, distID, domainName, reqPath)
		res, execErr := execCFFunction(fn.FunctionCode, event)
		if execErr != nil {
			log.Warn("viewer-request function failed",
				zap.String("arn", fa.FunctionARN),
				zap.Error(execErr))
			return nil, execErr
		}
		result = res
		// If it returned a response, stop processing.
		if res != nil && res.isResponse {
			return result, nil
		}
		// The next function in the chain sees this one's uri.
		if res != nil && res.uri != "" {
			res.uri = escapeURIPath(res.uri)
			reqPath = res.uri
		}
	}
	return result, nil
}

// runViewerResponse executes all viewer-response CloudFront Functions.
// Modifies the headers map in-place.
//
// It returns errFunctionExecution or errFunctionValidation, wrapped, if a
// function failed; the origin's response must then not be delivered.
func (h *Handler) runViewerResponse(
	r *http.Request,
	distID string,
	domainName string,
	reqPath string,
	fas *FunctionAssociations,
	statusCode int,
	headers map[string][]string,
) error {
	log := h.log.WithRecorder(r.Context())
	if fas == nil || len(fas.Items) == 0 {
		return nil
	}
	for _, fa := range fas.Items {
		if fa.EventType != "viewer-response" {
			continue
		}
		fn, ferr := h.store.GetFunctionByARN(r.Context(), fa.FunctionARN)
		if ferr != nil || fn == nil {
			log.Warn("viewer-response function not found",
				zap.String("arn", fa.FunctionARN))
			continue
		}
		event := buildViewerResponseEvent(r, distID, domainName, reqPath, statusCode, headers)
		res, execErr := execCFFunction(fn.FunctionCode, event)
		if execErr != nil {
			log.Warn("viewer-response function failed",
				zap.String("arn", fa.FunctionARN),
				zap.Error(execErr))
			return execErr
		}
		if res != nil {
			// Merge modified headers back.
			for k, v := range res.headers {
				headers[http.CanonicalHeaderKey(k)] = []string{v}
			}
		}
	}
	return nil
}

// buildViewerRequestEvent constructs the CloudFront Functions event object for viewer-request.
func buildViewerRequestEvent(r *http.Request, distID, domainName, uri string) map[string]interface{} {
	hdrs := make(map[string]interface{})
	for k, vals := range r.Header {
		lk := strings.ToLower(k)
		mv := make([]map[string]string, len(vals))
		for i, v := range vals {
			mv[i] = map[string]string{"value": v}
		}
		hdrs[lk] = map[string]interface{}{
			"value":      vals[0],
			"multiValue": mv,
		}
	}

	qs := make(map[string]interface{})
	for k, vals := range r.URL.Query() {
		mv := make([]map[string]string, len(vals))
		for i, v := range vals {
			mv[i] = map[string]string{"value": v}
		}
		qs[k] = map[string]interface{}{
			"value":      vals[0],
			"multiValue": mv,
		}
	}

	clientIP := r.RemoteAddr
	if idx := strings.LastIndex(clientIP, ":"); idx > 0 {
		clientIP = clientIP[:idx]
	}

	return map[string]interface{}{
		"version": "1.0",
		"context": map[string]interface{}{
			"distributionDomainName": domainName,
			"distributionId":         distID,
			"eventType":              "viewer-request",
			"requestId":              distID,
		},
		"viewer": map[string]interface{}{
			"ip": clientIP,
		},
		"request": map[string]interface{}{
			"method":      r.Method,
			"uri":         uri,
			"querystring": qs,
			"headers":     hdrs,
			"cookies":     map[string]interface{}{},
		},
	}
}

// buildViewerResponseEvent constructs the CloudFront Functions event object for viewer-response.
func buildViewerResponseEvent(r *http.Request, distID, domainName, uri string, statusCode int, respHeaders map[string][]string) map[string]interface{} {
	event := buildViewerRequestEvent(r, distID, domainName, uri)
	event["context"].(map[string]interface{})["eventType"] = "viewer-response"

	rHeaders := make(map[string]interface{})
	for k, vals := range respHeaders {
		lk := strings.ToLower(k)
		mv := make([]map[string]string, len(vals))
		for i, v := range vals {
			mv[i] = map[string]string{"value": v}
		}
		rHeaders[lk] = map[string]interface{}{
			"value":      vals[0],
			"multiValue": mv,
		}
	}

	event["response"] = map[string]interface{}{
		"statusCode":        statusCode,
		"statusDescription": http.StatusText(statusCode),
		"headers":           rHeaders,
		"cookies":           map[string]interface{}{},
	}
	return event
}

// execCFFunction runs a CloudFront Function using goja and parses the result.
// Every error it returns wraps errFunctionExecution or errFunctionValidation.
func execCFFunction(b64Code string, event map[string]interface{}) (*cfFunctionResult, error) {
	src, err := base64.StdEncoding.DecodeString(b64Code)
	if err != nil {
		return nil, fmt.Errorf("%w: decode function code: %w", errFunctionExecution, err)
	}

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	if _, err = vm.RunString(string(src)); err != nil {
		return nil, fmt.Errorf("%w: compile function: %w", errFunctionExecution, err)
	}

	handlerFn, ok := goja.AssertFunction(vm.Get("handler"))
	if !ok {
		return nil, fmt.Errorf("%w: function must define a 'handler' function", errFunctionExecution)
	}

	retVal, callErr := handlerFn(goja.Undefined(), vm.ToValue(event))
	if callErr != nil {
		return nil, fmt.Errorf("%w: %w", errFunctionExecution, callErr)
	}

	// A function must return a request or a response object; anything else
	// (undefined included, when it forgets to return) is an invalid event.
	ret := retVal.Export()
	retMap, ok := ret.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: function returned %T, not a request or response object", errFunctionValidation, ret)
	}

	res := &cfFunctionResult{}

	// Detect if it's a response (has statusCode) or a request (has uri/method).
	if sc, hasStatus := retMap["statusCode"]; hasStatus {
		res.isResponse = true
		switch v := sc.(type) {
		case int64:
			res.statusCode = int(v)
		case float64:
			res.statusCode = int(v)
		case int:
			res.statusCode = v
		}
		if sd, ok := retMap["statusDescription"].(string); ok {
			res.statusDesc = sd
		}
		if res.statusDesc == "" {
			res.statusDesc = http.StatusText(res.statusCode)
		}
	} else {
		res.isResponse = false
		// "The new uri value must begin with a forward slash"
		// (functions-event-structure). A request object always carries its
		// uri, so an empty or non-string one is as invalid as a relative one.
		if v, present := retMap["uri"]; present {
			uri, ok := v.(string)
			if !ok || !strings.HasPrefix(uri, "/") {
				return nil, fmt.Errorf("%w: request.uri %#v must begin with \"/\"", errFunctionValidation, v)
			}
			res.uri = uri
		}
		if method, ok := retMap["method"].(string); ok {
			res.method = method
		}
	}

	// Extract modified headers.
	res.headers = make(map[string]string)
	if hdrs, ok := retMap["headers"].(map[string]interface{}); ok {
		for k, v := range hdrs {
			if hmap, ok := v.(map[string]interface{}); ok {
				if val, ok := hmap["value"].(string); ok {
					res.headers[k] = val
				}
			}
		}
	}

	return res, nil
}
