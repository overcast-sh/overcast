package debugger

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Describer synthesises the entry for a resource no tag ever mentioned, so
// GET /_overcast/debugger/targets/{service}/{resource} always has something
// for the console to render: enabled false, reason "not tagged", and the
// setup block. Each compute service implements it for the resources it owns;
// it is optional, and without one an unknown resource is a 404. ctx is the
// request's: a service reads its store with it, and the region the request
// names travels in it.
type Describer interface {
	DescribeUntagged(ctx context.Context, service Service, resource string) (Descriptor, bool)
}

// Handler serves the emulator-only endpoints under /_overcast/debugger: the
// target list, one target's descriptor, and the console's WebSocket bridge.
// Routes are registered by internal/router; this only provides the handlers.
type Handler struct {
	manager   *Manager
	describer Describer
}

// NewHandler builds the handler. describer may be nil.
func NewHandler(m *Manager, describer Describer) *Handler {
	return &Handler{manager: m, describer: describer}
}

// ListTargets is GET /_overcast/debugger/targets: every registered target,
// ordered by id.
func (h *Handler) ListTargets(w http.ResponseWriter, _ *http.Request) {
	targets := h.manager.List()
	list := TargetList{Targets: make([]Descriptor, 0, len(targets))}
	for _, t := range targets {
		list.Targets = append(list.Targets, t.Descriptor())
	}
	writeJSON(w, http.StatusOK, list)
}

// GetTarget is GET /_overcast/debugger/targets/{service}/{resource}, with an
// optional ?container= for an ECS task's containers. Without one, an ECS
// task answers with its first container by id, so the console has something
// to show before it asks for a specific one.
func (h *Handler) GetTarget(w http.ResponseWriter, r *http.Request) {
	service, resource, container := targetParams(r)
	if t, ok := h.lookup(service, resource, container); ok {
		writeJSON(w, http.StatusOK, t.Descriptor())
		return
	}
	if h.describer != nil {
		if d, ok := h.describer.DescribeUntagged(r.Context(), service, resource); ok {
			if container != "" {
				d.ID = TargetID(service, resource, container)
				d.Container = container
			}
			writeJSON(w, http.StatusOK, d)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errorBody{Error: "no such resource: " + TargetID(service, resource, container)})
}

// Bridge is GET /_overcast/debugger/targets/{service}/{resource}/ws, the
// console's WebSocket session on a registered target (bridge.go). A resource
// with no target is a 404 before the upgrade: there is nothing to attach to,
// and the console only asks once the descriptor said consoleDebug.
func (h *Handler) Bridge(w http.ResponseWriter, r *http.Request) {
	service, resource, container := targetParams(r)
	t, ok := h.lookup(service, resource, container)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such target: " + TargetID(service, resource, container)})
		return
	}
	t.ServeWebSocket(w, r)
}

// targetParams reads the target a request names: the route's service and
// resource, and the optional ?container= an ECS task's containers are told
// apart by.
func targetParams(r *http.Request) (service Service, resource, container string) {
	return Service(chi.URLParam(r, "service")), chi.URLParam(r, "resource"), r.URL.Query().Get("container")
}

// lookup finds the registered target, or — with no container named — the
// first of an ECS task's containers by id.
func (h *Handler) lookup(service Service, resource, container string) (*Target, bool) {
	if t, ok := h.manager.Get(TargetID(service, resource, container)); ok {
		return t, true
	}
	if container != "" {
		return nil, false
	}
	prefix := TargetID(service, resource, "") + "/"
	for _, t := range h.manager.List() {
		if strings.HasPrefix(t.ID(), prefix) {
			return t, true
		}
	}
	return nil, false
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
