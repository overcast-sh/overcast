package lambdaweb

// REST-JSON adapters. Each route is the model's @http binding. An adapter only
// lifts the binding's labels, query members and body into the typed request;
// typed_logic.go decides everything.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// apiBase is the API version every lambda-web binding is under.
const apiBase = "/2025-03-07"

func (s *Service) registerRoutes(r chi.Router) {
	const (
		fns   = apiBase + "/web-functions"
		fn    = fns + "/{functionName}"
		eps   = fn + "/endpoints"
		revs  = fn + "/revisions"
		tags  = apiBase + "/tags/{resource}"
		pol   = apiBase + "/resource-policy/{resourceArn}"
		acct  = apiBase + "/web-account-settings"
		ok    = http.StatusOK
		empty = http.StatusNoContent
	)
	r.Put(fns, serve(ok, bodyOnly[createWebFunctionRequest], s.createWebFunctionTyped))
	r.Post(fns, serve(ok, fillList, s.listWebFunctionsTyped))
	r.Get(fn, serve(ok, fillFunction, s.getWebFunctionTyped))
	r.Delete(fn, serveAny(empty, fillFunction, s.deleteWebFunctionTyped))

	r.Put(eps, serve(ok, withBody(func(r *http.Request, q *createEndpointRequest) { q.FunctionName = label(r, "functionName") }), s.createWebFunctionEndpointTyped))
	r.Post(fn+"/list-endpoints", serve(ok, fillList, s.listWebFunctionEndpointsTyped))
	r.Get(eps+"/{endpointName}", serve(ok, fillEndpoint, s.getWebFunctionEndpointTyped))
	r.Patch(eps+"/{endpointName}", serve(ok, withBody(func(r *http.Request, q *updateEndpointRequest) {
		q.FunctionName, q.EndpointName = label(r, "functionName"), label(r, "endpointName")
	}), s.updateWebFunctionEndpointTyped))
	r.Delete(eps+"/{endpointName}", serveAny(empty, fillEndpoint, s.deleteWebFunctionEndpointTyped))

	r.Post(revs, serve(ok, withBody(func(r *http.Request, q *createRevisionRequest) { q.FunctionName = label(r, "functionName") }), s.createWebFunctionRevisionTyped))
	r.Post(fn+"/list-revisions", serve(ok, fillList, s.listWebFunctionRevisionsTyped))
	r.Get(revs+"/{revisionId}", serve(ok, fillRevision, s.getWebFunctionRevisionTyped))
	r.Delete(revs+"/{revisionId}", serveAny(empty, fillRevision, s.deleteWebFunctionRevisionTyped))

	r.Get(tags, serve(ok, func(r *http.Request, q *listTagsRequest) *protocol.AWSError {
		q.Resource = label(r, "resource")
		return nil
	}, s.listTagsTyped))
	r.Post(tags, serveAny(empty, withBody(func(r *http.Request, q *tagResourceRequest) { q.Resource = label(r, "resource") }), s.tagResourceTyped))
	r.Delete(tags, serveAny(empty, func(r *http.Request, q *untagResourceRequest) *protocol.AWSError {
		q.Resource, q.TagKeys = label(r, "resource"), r.URL.Query()["tagKeys"]
		return nil
	}, s.untagResourceTyped))

	r.Put(pol, serve(ok, withBody(func(r *http.Request, q *putPolicyRequest) { q.ResourceARN = label(r, "resourceArn") }), s.putResourcePolicyTyped))
	r.Get(pol, serve(ok, fillPolicyRef, s.getResourcePolicyTyped))
	r.Delete(pol, serveAny(empty, fillPolicyRef, s.deleteResourcePolicyTyped))

	r.Get(acct, serve(ok, func(*http.Request, *noInput) *protocol.AWSError { return nil }, s.getWebAccountSettingsTyped))
}

// ─── Serving ──────────────────────────────────────────────────────────────────

type filler[In any] func(*http.Request, *In) *protocol.AWSError

// serve runs an operation with a modeled output shape.
func serve[In, Out any](status int, fill filler[In], fn func(context.Context, *In) (*Out, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		out, aerr := fn(r.Context(), &req)
		if aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		protocol.WriteJSON(w, r, status, out)
	}
}

// serveAny runs an operation whose output shape is empty (Unit): 204 with no
// body, as the model's @http bindings say.
func serveAny[In any](status int, fill filler[In], fn func(context.Context, *In) (any, *protocol.AWSError)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req In
		if aerr := fill(r, &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		if _, aerr := fn(r.Context(), &req); aerr != nil {
			protocol.WriteJSONError(w, r, aerr)
			return
		}
		protocol.WriteEmpty(w, r, status)
	}
}

// ─── Binding helpers ──────────────────────────────────────────────────────────

// decodeBody reads a JSON body into dst. An absent body leaves dst zero, so
// the operation's own required-member checks answer for it.
func decodeBody(r *http.Request, dst any) *protocol.AWSError {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return validationError("The request body could not be parsed as JSON.")
}

// label reads a path label. SDKs percent-encode a label that holds an ARN into
// one segment, and chi matches on the escaped path, so it is unescaped here.
func label(r *http.Request, name string) string {
	raw := chi.URLParam(r, name)
	if v, err := url.PathUnescape(raw); err == nil {
		return v
	}
	return raw
}

func bodyOnly[In any](r *http.Request, req *In) *protocol.AWSError { return decodeBody(r, req) }

// withBody decodes an operation's body and then runs bind, so a label always
// wins over a body member of the same name.
func withBody[In any](bind func(*http.Request, *In)) filler[In] {
	return func(r *http.Request, req *In) *protocol.AWSError {
		if aerr := decodeBody(r, req); aerr != nil {
			return aerr
		}
		bind(r, req)
		return nil
	}
}

func fillFunction(r *http.Request, req *functionRequest) *protocol.AWSError {
	req.FunctionName = label(r, "functionName")
	return nil
}

var fillList = withBody(func(r *http.Request, req *listRequest) { req.FunctionName = label(r, "functionName") })

func fillEndpoint(r *http.Request, req *endpointRequest) *protocol.AWSError {
	req.FunctionName, req.EndpointName = label(r, "functionName"), label(r, "endpointName")
	return nil
}

func fillRevision(r *http.Request, req *revisionRequest) *protocol.AWSError {
	req.FunctionName, req.RevisionID = label(r, "functionName"), label(r, "revisionId")
	return nil
}

// fillPolicyRef binds the {resourceArn} label and the RevisionId query member
// DeleteResourcePolicy carries.
func fillPolicyRef(r *http.Request, req *policyRef) *protocol.AWSError {
	req.ResourceARN, req.RevisionID = label(r, "resourceArn"), r.URL.Query().Get("RevisionId")
	return nil
}
