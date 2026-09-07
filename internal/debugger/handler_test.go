package debugger

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// fakeDescriber knows one untagged function.
type fakeDescriber struct{}

func (fakeDescriber) DescribeUntagged(service Service, resource string) (Descriptor, bool) {
	if service == ServiceLambda && resource == "plain" {
		return UntaggedDescriptor(service, resource, "", "arn:plain"), true
	}
	return Descriptor{}, false
}

func newTestRouter(t *testing.T, m *Manager, d Describer) http.Handler {
	t.Helper()
	h := NewHandler(m, d)
	r := chi.NewRouter()
	r.Get("/_overcast/debugger/targets", h.ListTargets)
	r.Get("/_overcast/debugger/targets/{service}/{resource}", h.GetTarget)
	return r
}

func get(t *testing.T, router http.Handler, path string, into any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	if into != nil {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), into), rec.Body.String())
	}
	return rec
}

func TestHandler_listTargets(t *testing.T) {
	// Given: two targets, one inert
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	boundTarget(t, m, "lambda/fn", inspector{})
	_, err := m.Ensure("ecs/task-1/app", Spec{Service: ServiceECS, Container: "app", Tagged: true}, Resolution{Protocol: passthrough{}})
	require.NoError(t, err)
	router := newTestRouter(t, m, nil)

	// When: the list is requested
	var list TargetList
	rec := get(t, router, "/_overcast/debugger/targets", &list)

	// Then: both are returned, ordered by id, with their descriptors
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, list.Targets, 2)
	assert.Equal(t, "ecs/task-1/app", list.Targets[0].ID)
	assert.False(t, list.Targets[0].Enabled)
	assert.Equal(t, "lambda/fn", list.Targets[1].ID)
	assert.Equal(t, "unbound", list.Targets[1].State)
}

func TestHandler_listTargetsEmpty(t *testing.T) {
	// Given: no targets
	router := newTestRouter(t, newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached), nil)

	// When: the list is requested
	rec := get(t, router, "/_overcast/debugger/targets", nil)

	// Then: it is an empty array, not null
	assert.JSONEq(t, `{"targets":[]}`, rec.Body.String())
}

func TestHandler_getTarget(t *testing.T) {
	// Given: a Lambda target and two ECS containers of one task
	m := newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached)
	boundTarget(t, m, "lambda/fn", inspector{})
	for _, c := range []string{"web", "app"} {
		_, err := m.Ensure("ecs/task-1/"+c, Spec{Service: ServiceECS, Container: c, Tagged: true, FlagOn: true}, Resolution{Protocol: passthrough{}})
		require.NoError(t, err)
	}
	router := newTestRouter(t, m, fakeDescriber{})

	t.Run("lambda by name", func(t *testing.T) {
		// When: the function is requested
		var d Descriptor
		rec := get(t, router, "/_overcast/debugger/targets/lambda/fn", &d)

		// Then: its descriptor is returned
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "lambda/fn", d.ID)
		assert.Equal(t, "inspector", d.Protocol)
	})

	t.Run("ecs task without a container picks the first by id", func(t *testing.T) {
		// When: the task is requested with no container
		var d Descriptor
		get(t, router, "/_overcast/debugger/targets/ecs/task-1", &d)

		// Then: the first container by id answers
		assert.Equal(t, "ecs/task-1/app", d.ID)
	})

	t.Run("ecs task with a container", func(t *testing.T) {
		// When: a specific container is requested
		var d Descriptor
		get(t, router, "/_overcast/debugger/targets/ecs/task-1?container=web", &d)

		// Then: that container answers
		assert.Equal(t, "ecs/task-1/web", d.ID)
	})

	t.Run("untagged resource is synthesised", func(t *testing.T) {
		// When: a function no tag mentions is requested
		var d Descriptor
		rec := get(t, router, "/_overcast/debugger/targets/lambda/plain", &d)

		// Then: the describer's entry is returned, off, with setup filled
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.False(t, d.Enabled)
		assert.Equal(t, ReasonNotTagged, d.Reason)
		assert.Contains(t, d.Setup.TagCLI, "arn:plain")
	})

	t.Run("unknown resource is a 404", func(t *testing.T) {
		// When: nothing knows the resource
		var body map[string]string
		rec := get(t, router, "/_overcast/debugger/targets/lambda/ghost", &body)

		// Then: 404 with a JSON error
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, body["error"], "lambda/ghost")
	})
}

func TestHandler_getTargetWithoutDescriber(t *testing.T) {
	// Given: no describer
	router := newTestRouter(t, newTestManager(t, clock.NewMock(), config.DebuggerTimeoutAttached), nil)

	// When: an unknown resource is requested
	rec := get(t, router, "/_overcast/debugger/targets/lambda/fn", nil)

	// Then: it is a 404 rather than a panic
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
