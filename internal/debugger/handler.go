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

// Handler serves the two emulator-only endpoints under /_overcast/debugger.
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
	service := Service(chi.URLParam(r, "service"))
	resource := chi.URLParam(r, "resource")
	container := r.URL.Query().Get("container")

	if t, ok := h.manager.Get(TargetID(service, resource, container)); ok {
		writeJSON(w, http.StatusOK, t.Descriptor())
		return
	}
	if container == "" {
		prefix := TargetID(service, resource, "") + "/"
		for _, t := range h.manager.List() {
			if strings.HasPrefix(t.ID(), prefix) {
				writeJSON(w, http.StatusOK, t.Descriptor())
				return
			}
		}
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

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
