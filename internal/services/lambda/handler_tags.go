package lambda

// handler_tags.go — Lambda TagResource / UntagResource / ListTags.
//
//	POST   /2017-03-31/tags/{Resource}
//	GET    /2017-03-31/tags/{Resource}
//	DELETE /2017-03-31/tags/{Resource}?tagKeys=…
//
// The path label is TaggableResource: an ARN, never a bare function name.
// Functions and event source mappings are supported; the remaining taggable
// Lambda resources are declared unsupported rather than silently mistagged.

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

var lambdaTagCfg = serviceutil.TagValidationConfig{
	ExceededCode:    "InvalidParameterValueException",
	InvalidCode:     "InvalidParameterValueException",
	ExceededMessage: "Tag count exceeded the maximum of 50 tags per resource.",
}

// taggableResourceConstraint is the TaggableResource pattern from the pinned
// Lambda model (models/aws/VERSION), reported verbatim in validation errors.
const taggableResourceConstraint = `arn:(aws[a-zA-Z-]*):lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:(function:[a-zA-Z0-9-_]+(:(\$LATEST|[a-zA-Z0-9-_]+))?|code-signing-config:csc-[a-z0-9]{17}|event-source-mapping:[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|(capacity-provider|network-connector):[a-zA-Z0-9-_]{1,64})`

var taggableResourcePattern = lazyRegexp(`^` + taggableResourceConstraint + `$`)

// maxTaggableResourceLength is TaggableResource's modeled length ceiling.
const maxTaggableResourceLength = 10000

// tagResource handles POST /2017-03-31/tags/{Resource}.
func (h *Handler) tagResource(w http.ResponseWriter, r *http.Request) {
	store, aerr, unsupported := h.resolveTagResource(r)
	if unsupported {
		protocol.NotImplementedJSON(w, r)
		return
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	var req struct {
		Tags map[string]string `json:"Tags"`
	}
	if !serviceutil.DecodeJSON(w, r, &req) {
		return
	}
	if req.Tags == nil {
		protocol.WriteJSONError(w, r, smithyRequiredMember("tags"))
		return
	}

	if _, aerr := serviceutil.ApplyStoreTags(r.Context(), store, store.key, req.Tags, lambdaTagCfg); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	h.syncDebugTargetForTagWrite(r.Context(), store)
	w.WriteHeader(http.StatusNoContent)
}

// untagResource handles DELETE /2017-03-31/tags/{Resource}, whose keys arrive
// as repeated tagKeys query parameters.
func (h *Handler) untagResource(w http.ResponseWriter, r *http.Request) {
	store, aerr, unsupported := h.resolveTagResource(r)
	if unsupported {
		protocol.NotImplementedJSON(w, r)
		return
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	tagKeys := r.URL.Query()["tagKeys"]
	if len(tagKeys) == 0 {
		protocol.WriteJSONError(w, r, smithyRequiredMember("tagKeys"))
		return
	}

	if _, aerr := serviceutil.RemoveStoreTags(r.Context(), store, store.key, tagKeys); aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	h.syncDebugTargetForTagWrite(r.Context(), store)
	w.WriteHeader(http.StatusNoContent)
}

// syncDebugTargetForTagWrite re-reads a function after its tags changed and
// keeps its debug target in step (syncDebugTarget): the overcast:debug* tags
// are what the console's toggles write, and the Debug tab reads the result
// back from the target list. Tags on an event source mapping change nothing.
func (h *Handler) syncDebugTargetForTagWrite(ctx context.Context, store resourceTagStore) {
	if _, isFunction := store.TagStore.(functionTagStore); !isFunction {
		return
	}
	fn, aerr := h.ls.getFunction(ctx, store.key)
	if aerr != nil || fn == nil {
		return
	}
	h.syncDebugTarget(ctx, fn)
}

// listTags handles GET /2017-03-31/tags/{Resource}.
func (h *Handler) listTags(w http.ResponseWriter, r *http.Request) {
	store, aerr, unsupported := h.resolveTagResource(r)
	if unsupported {
		protocol.NotImplementedJSON(w, r)
		return
	}
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}

	tags, aerr := store.Load(r.Context(), store.key)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	protocol.WriteJSON(w, r, http.StatusOK, struct {
		Tags map[string]string `json:"Tags"`
	}{Tags: tags})
}

// resourceTagStore is a serviceutil.TagStore bound to one resolved resource,
// carrying the store key its Load/Save were resolved for so the handlers never
// re-derive it from the ARN.
type resourceTagStore struct {
	serviceutil.TagStore
	key string
}

// resolveTagResource validates the {Resource} path label against
// TaggableResource, confirms the resource exists in the caller's region and
// account, and returns the tag store that owns it. The bool is true when the
// ARN names a Lambda resource type Overcast does not carry yet, which is an
// honest 501 rather than a wrong 404.
func (h *Handler) resolveTagResource(r *http.Request) (resourceTagStore, *protocol.AWSError, bool) {
	raw := chi.URLParam(r, "ResourceARN")
	arn, err := url.PathUnescape(raw)
	if err != nil {
		return resourceTagStore{}, smithyPatternConstraint("resource", raw, taggableResourceConstraint), false
	}
	if arn == "" {
		return resourceTagStore{}, smithyStringMinimumLengthConstraint("resource", arn, 1), false
	}
	if len(arn) > maxTaggableResourceLength {
		return resourceTagStore{}, smithyStringLengthConstraint("resource", arn, maxTaggableResourceLength), false
	}
	if !taggableResourcePattern().MatchString(arn) {
		return resourceTagStore{}, smithyPatternConstraint("resource", arn, taggableResourceConstraint), false
	}

	// arn:<partition>:lambda:<region>:<account>:<type>:<id…>
	parts := strings.SplitN(arn, ":", 7)
	region, account, resourceType, id := parts[3], parts[4], parts[5], parts[6]
	if region != middleware.RegionFromContext(r.Context(), h.cfg.Region) || account != h.cfg.AccountID {
		return resourceTagStore{}, lambdaTagResourceNotFound(arn), false
	}

	ctx := r.Context()
	switch resourceType {
	case "function":
		if strings.Contains(id, ":") {
			// AWS ListTags: "Lambda does not support adding tags to function
			// aliases or versions." The qualifier satisfies TaggableResource's
			// pattern, so this is a service refusal — and it must never fall
			// through to tagging the unqualified function.
			return resourceTagStore{}, lambdaInvalidParameter("Tags on a version or alias are not supported."), false
		}
		fn, aerr := h.ls.getFunction(ctx, id)
		if aerr != nil {
			return resourceTagStore{}, aerr, false
		}
		if fn == nil {
			return resourceTagStore{}, lambdaTagResourceNotFound(arn), false
		}
		return resourceTagStore{TagStore: functionTagStore{ls: h.ls}, key: id}, nil, false
	case "event-source-mapping":
		mapping, aerr := h.esm.getESM(ctx, id)
		if aerr != nil {
			return resourceTagStore{}, aerr, false
		}
		if mapping == nil {
			return resourceTagStore{}, lambdaTagResourceNotFound(arn), false
		}
		return resourceTagStore{TagStore: esmTagStore{ls: h.ls}, key: id}, nil, false
	default:
		// code-signing-config, capacity-provider and network-connector are
		// taggable on AWS. Overcast does not store their tags yet.
		return resourceTagStore{}, nil, true
	}
}

func lambdaTagResourceNotFound(arn string) *protocol.AWSError {
	if strings.Contains(arn, ":function:") {
		return lambdaFunctionNotFound(arn)
	}
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    "The resource you requested does not exist: " + arn,
		HTTPStatus: http.StatusNotFound,
	}
}

// functionTagStore stores tags inline on the function record — GetFunction
// returns them, so they cannot live in a side namespace.
type functionTagStore struct{ ls *lambdaStore }

func (s functionTagStore) Load(ctx context.Context, name string) (map[string]string, *protocol.AWSError) {
	fn, aerr := s.ls.getFunction(ctx, name)
	if aerr != nil {
		return nil, aerr
	}
	if fn == nil {
		return nil, lambdaFunctionNotFound(name)
	}
	if fn.Tags == nil {
		return map[string]string{}, nil
	}
	return fn.Tags, nil
}

// Save rewrites the tag map under the store's lock so a concurrent
// configuration update cannot be rolled back by a tag write, and vice versa.
func (s functionTagStore) Save(ctx context.Context, name string, tags map[string]string) *protocol.AWSError {
	_, changed, aerr := s.ls.mutateFunction(ctx, name, func(current *Function) (bool, *protocol.AWSError) {
		current.Tags = tags
		return true, nil
	})
	if aerr != nil {
		return aerr
	}
	if !changed {
		return lambdaFunctionNotFound(name)
	}
	return nil
}

// Delete clears the function's tags. Deleting the function removes the record
// they ride on, so this exists for the interface rather than for a caller: a
// function's tags cannot outlive it.
func (s functionTagStore) Delete(ctx context.Context, name string) *protocol.AWSError {
	return s.Save(ctx, name, nil)
}

// esmTagStore keeps event source mapping tags in their own namespace:
// EventSourceMappingConfiguration has no Tags member, and the mapping record is
// marshalled to the wire as-is.
type esmTagStore struct{ ls *lambdaStore }

func (s esmTagStore) Load(ctx context.Context, uuid string) (map[string]string, *protocol.AWSError) {
	return s.ls.esmTags(ctx, uuid)
}

func (s esmTagStore) Save(ctx context.Context, uuid string, tags map[string]string) *protocol.AWSError {
	return s.ls.putESMTags(ctx, uuid, tags)
}

// Delete removes a mapping's tags. deleteESM calls it before dropping the
// mapping record, so the tags cannot be left addressable by an ARN that no
// longer resolves.
func (s esmTagStore) Delete(ctx context.Context, uuid string) *protocol.AWSError {
	return s.ls.esmTagNS().Delete(ctx, s.ls.esmTagKey(ctx, uuid))
}
