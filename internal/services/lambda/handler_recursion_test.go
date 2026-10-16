package lambda

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recursionCall(t *testing.T, h *Handler, method, name, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := withFunctionNameParam(httptest.NewRequest(method, "/2024-08-31/functions/"+name+"/recursion-config", strings.NewReader(body)), name)
	rec := httptest.NewRecorder()
	if method == http.MethodPut {
		h.PutFunctionRecursionConfig(rec, req)
	} else {
		h.GetFunctionRecursionConfig(rec, req)
	}
	return rec
}

func TestFunctionRecursionConfig(t *testing.T) {
	h, ls, _ := mutationRaceHandler(t)

	// A function that never set one reads as AWS's default.
	rec := recursionCall(t, h, http.MethodGet, "race-fn", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"RecursiveLoop":"Terminate"`) {
		t.Fatalf("default Get = %d %s, want 200 Terminate", rec.Code, rec.Body.String())
	}

	rec = recursionCall(t, h, http.MethodPut, "race-fn", `{"RecursiveLoop":"Allow"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"RecursiveLoop":"Allow"`) {
		t.Fatalf("Put = %d %s, want 200 Allow", rec.Code, rec.Body.String())
	}
	rec = recursionCall(t, h, http.MethodGet, "race-fn", "")
	if !strings.Contains(rec.Body.String(), `"RecursiveLoop":"Allow"`) {
		t.Fatalf("Get after Put = %s, want Allow", rec.Body.String())
	}
	fn, _ := ls.getFunction(t.Context(), "race-fn")
	if fn == nil || fn.RecursiveLoop != "Allow" {
		t.Fatalf("stored RecursiveLoop = %+v, want Allow", fn)
	}
}

func TestFunctionRecursionConfig_errors(t *testing.T) {
	h, _, _ := mutationRaceHandler(t)
	for name, tc := range map[string]struct {
		method, fn, body, code string
		status                 int
	}{
		"put unknown function": {http.MethodPut, "nope", `{"RecursiveLoop":"Allow"}`, "ResourceNotFoundException", http.StatusNotFound},
		"get unknown function": {http.MethodGet, "nope", "", "ResourceNotFoundException", http.StatusNotFound},
		"missing member":       {http.MethodPut, "race-fn", `{}`, "InvalidParameterValueException", http.StatusBadRequest},
		"bad enum value":       {http.MethodPut, "race-fn", `{"RecursiveLoop":"Maybe"}`, "InvalidParameterValueException", http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			rec := recursionCall(t, h, tc.method, tc.fn, tc.body)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body.String(), tc.status, tc.code)
			}
		})
	}
}
