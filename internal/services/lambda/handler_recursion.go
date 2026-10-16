package lambda

// handler_recursion.go — recursive-loop detection setting.
//
// Implemented:
//   - PutFunctionRecursionConfig  PUT /2024-08-31/functions/{name}/recursion-config
//   - GetFunctionRecursionConfig  GET /2024-08-31/functions/{name}/recursion-config
//
// Overcast runs no recursive-loop detection, so the setting is stored and
// echoed and changes nothing about execution.

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// AWS's RecursiveLoop enum.
const (
	recursiveLoopAllow     = "Allow"
	recursiveLoopTerminate = "Terminate"
)

// functionRecursionConfig is the body of PutFunctionRecursionConfig (request
// and response) and of the GetFunctionRecursionConfig response.
type functionRecursionConfig struct {
	RecursiveLoop string `json:"RecursiveLoop,omitempty"`
}

// recursiveLoopOf reports a function's setting, defaulting to AWS's Terminate.
func recursiveLoopOf(fn *Function) string {
	if fn.RecursiveLoop == "" {
		return recursiveLoopTerminate
	}
	return fn.RecursiveLoop
}

// PutFunctionRecursionConfig handles PUT /2024-08-31/functions/{name}/recursion-config.
func (h *Handler) PutFunctionRecursionConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("put function recursion config", zap.String("function", name))
	ctx := r.Context()

	fn, aerr := h.ls.getFunction(ctx, name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, lambdaFunctionNotFound(name))
		return
	}

	var req functionRecursionConfig
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		protocol.WriteJSONError(w, r, protocol.ErrInvalidArgument("invalid request body"))
		return
	}
	if req.RecursiveLoop == "" {
		protocol.WriteJSONError(w, r, smithyRequiredConstraint("recursiveLoop"))
		return
	}
	if req.RecursiveLoop != recursiveLoopAllow && req.RecursiveLoop != recursiveLoopTerminate {
		protocol.WriteJSONError(w, r, lambdaInvalidParameter("1 validation error detected: Value '"+req.RecursiveLoop+"' at 'recursiveLoop' failed to satisfy constraint: Member must satisfy enum value set: [Allow, Terminate]"))
		return
	}

	fn, changed, aerr := h.ls.mutateFunction(ctx, name, func(current *Function) (bool, *protocol.AWSError) {
		current.RecursiveLoop = req.RecursiveLoop
		return true, nil
	})
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if !changed || fn == nil {
		protocol.WriteJSONError(w, r, lambdaFunctionNotFound(name))
		return
	}

	protocol.WriteRESTJSON(w, r, http.StatusOK, functionRecursionConfig(req))
}

// GetFunctionRecursionConfig handles GET /2024-08-31/functions/{name}/recursion-config.
func (h *Handler) GetFunctionRecursionConfig(w http.ResponseWriter, r *http.Request) {
	log := h.log.WithRecorder(r.Context())
	name := chi.URLParam(r, "name")
	log.Debug("get function recursion config", zap.String("function", name))

	fn, aerr := h.ls.getFunction(r.Context(), name)
	if aerr != nil {
		protocol.WriteJSONError(w, r, aerr)
		return
	}
	if fn == nil {
		protocol.WriteJSONError(w, r, lambdaFunctionNotFound(name))
		return
	}
	protocol.WriteRESTJSON(w, r, http.StatusOK, functionRecursionConfig{RecursiveLoop: recursiveLoopOf(fn)})
}
