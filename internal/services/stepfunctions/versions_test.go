package stepfunctions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

func TestParseSMARN(t *testing.T) {
	const base = "arn:aws:states:us-east-1:000000000000:stateMachine:sm"
	cases := []struct {
		name          string
		arn           string
		wantOK        bool
		wantQualifier string
		wantVersion   int
	}{
		{name: "unqualified", arn: base, wantOK: true},
		{name: "version", arn: base + ":12", wantOK: true, wantQualifier: "12", wantVersion: 12},
		{name: "alias", arn: base + ":PROD", wantOK: true, wantQualifier: "PROD"},
		{name: "version zero", arn: base + ":0"},
		{name: "empty qualifier", arn: base + ":"},
		{name: "too many parts", arn: base + ":1:2"},
		{name: "execution arn", arn: "arn:aws:states:us-east-1:000000000000:execution:sm:run"},
		{name: "bare name", arn: "sm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the ARN is parsed
			got, ok := parseSMARN(tc.arn)

			// Then: validity, qualifier and version match
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got.base != base || got.name != "sm" || got.qualifier != tc.wantQualifier || got.version != tc.wantVersion {
				t.Errorf("parsed = %+v", got)
			}
		})
	}
}

func TestPickRoute_followsWeights(t *testing.T) {
	routes := []aliasRouteRecord{
		{StateMachineVersionArn: "v1", Weight: 90},
		{StateMachineVersionArn: "v2", Weight: 10},
	}
	cases := []struct {
		roll int
		want string
	}{
		{roll: 0, want: "v1"},
		{roll: 89, want: "v1"},
		{roll: 90, want: "v2"},
		{roll: 99, want: "v2"},
	}
	for _, tc := range cases {
		// Given: a handler whose random draw is pinned
		h := &Handler{pickWeight: func(n int) int {
			if n != totalRoutingWeight {
				t.Fatalf("drew from [0,%d), want [0,%d)", n, totalRoutingWeight)
			}
			return tc.roll
		}}

		// When / Then: the draw selects the version whose weight band holds it
		if got := h.pickRoute(routes); got != tc.want {
			t.Errorf("roll %d picked %q, want %q", tc.roll, got, tc.want)
		}
	}
}

func TestResolveExecutionTarget_aliasRoutesToPickedVersion(t *testing.T) {
	// Given: two versions and an alias splitting 50/50 between them
	h := newVersionsTestHandler(t, state.NewMemoryStore())
	ctx := context.Background()
	smArn := createTestStateMachine(t, h, "routed", `"one"`)
	v1 := mustPublish(t, h, smArn)
	if _, aerr := h.updateStateMachineTyped(ctx, &updateStateMachineRequest{
		StateMachineArn: smArn,
		Definition:      passASL(`"two"`),
	}); aerr != nil {
		t.Fatalf("update: %+v", aerr)
	}
	v2 := mustPublish(t, h, smArn)
	alias, aerr := h.createStateMachineAliasTyped(ctx, &createStateMachineAliasRequest{
		Name: "LIVE",
		RoutingConfiguration: []routingConfigItem{
			{StateMachineVersionArn: v1, Weight: 50},
			{StateMachineVersionArn: v2, Weight: 50},
		},
	})
	if aerr != nil {
		t.Fatalf("create alias: %+v", aerr)
	}

	for _, tc := range []struct {
		roll       int
		wantArn    string
		wantResult string
	}{{roll: 10, wantArn: v1, wantResult: `"one"`}, {roll: 60, wantArn: v2, wantResult: `"two"`}} {
		h.pickWeight = func(int) int { return tc.roll }

		// When: the alias ARN is resolved
		sm, target, aerr := h.resolveExecutionTarget(ctx, alias.StateMachineAliasArn)

		// Then: the picked version's definition runs under the base ARN
		if aerr != nil {
			t.Fatalf("resolve: %+v", aerr)
		}
		if target.versionArn != tc.wantArn || target.aliasArn != alias.StateMachineAliasArn {
			t.Errorf("roll %d: target = %+v, want version %q", tc.roll, target, tc.wantArn)
		}
		if sm.ARN != smArn || sm.Definition != passASL(tc.wantResult) {
			t.Errorf("roll %d: sm ARN %q definition %s", tc.roll, sm.ARN, sm.Definition)
		}
	}
}

func TestDefinitionDiagnostic_codesAndLocations(t *testing.T) {
	cases := []struct {
		name         string
		definition   string
		wantCode     string
		wantLocation string
	}{
		{name: "not JSON", definition: `{`, wantCode: diagnosticInvalidJSON},
		{name: "dangling StartAt", definition: `{"StartAt":"X","States":{"P":{"Type":"Pass","End":true}}}`,
			wantCode: diagnosticMissingTarget, wantLocation: "/StartAt"},
		{name: "dangling Next", definition: `{"StartAt":"P","States":{"P":{"Type":"Pass","Next":"X"}}}`,
			wantCode: diagnosticMissingTarget, wantLocation: "/States/P"},
		{name: "unknown Type", definition: `{"StartAt":"a/b","States":{"a/b":{"Type":"Nope","End":true}}}`,
			wantCode: diagnosticSchemaValidation, wantLocation: "/States/a~1b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a definition parseDefinition rejects
			_, err := parseDefinition(tc.definition)
			if err == nil {
				t.Fatal("parseDefinition accepted the definition")
			}

			// When: it is mapped to a diagnostic
			d := definitionDiagnostic(err)

			// Then: an ERROR with the expected code and location
			if d.Severity != diagnosticSeverityError || d.Code != tc.wantCode || d.Location != tc.wantLocation || d.Message == "" {
				t.Errorf("diagnostic = %+v, want code %q location %q", d, tc.wantCode, tc.wantLocation)
			}
		})
	}
}

// TestStore_versionsAndAliases exercises the version and alias persistence
// against every store backend this build has.
func TestStore_versionsAndAliases(t *testing.T) {
	for name, backend := range versionTestBackends(t) {
		t.Run(name, func(t *testing.T) {
			// Given: two versions and two aliases of one state machine, plus a
			// version of another state machine whose name shares a prefix
			st := newStore(backend, "us-east-1")
			ctx := context.Background()
			created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, v := range []*StateMachineVersion{
				{StateMachineName: "sm", ARN: "sm:1", Version: 1},
				{StateMachineName: "sm", ARN: "sm:2", Version: 2},
				{StateMachineName: "sm-other", ARN: "sm-other:1", Version: 1},
			} {
				if err := st.PutVersion(ctx, v); err != nil {
					t.Fatalf("PutVersion: %v", err)
				}
			}
			for i, name := range []string{"A", "B"} {
				at := created.Add(time.Duration(i) * time.Second)
				if err := st.PutAlias(ctx, &StateMachineAlias{StateMachineName: "sm", Name: name, ARN: "sm:" + name, CreatedAt: at, UpdatedAt: at}); err != nil {
					t.Fatalf("PutAlias: %v", err)
				}
			}

			// When / Then: lists are scoped and newest first
			versions, err := st.ListVersions(ctx, "sm")
			if err != nil || len(versions) != 2 || versions[0].Version != 2 {
				t.Fatalf("ListVersions = %+v, %v", versions, err)
			}
			aliases, err := st.ListAliases(ctx, "sm")
			if err != nil || len(aliases) != 2 || aliases[0].Name != "B" {
				t.Fatalf("ListAliases = %+v, %v", aliases, err)
			}
			if v, err := st.GetVersion(ctx, "sm", 1); err != nil || v == nil || v.ARN != "sm:1" {
				t.Errorf("GetVersion = %+v, %v", v, err)
			}
			if a, err := st.GetAlias(ctx, "sm", "A"); err != nil || a == nil || a.ARN != "sm:A" {
				t.Errorf("GetAlias = %+v, %v", a, err)
			}

			// And: deletes remove exactly the named record
			if err := st.DeleteVersion(ctx, "sm", 1); err != nil {
				t.Fatalf("DeleteVersion: %v", err)
			}
			if err := st.DeleteAlias(ctx, "sm", "A"); err != nil {
				t.Fatalf("DeleteAlias: %v", err)
			}
			if v, _ := st.GetVersion(ctx, "sm", 1); v != nil {
				t.Errorf("version 1 survived delete: %+v", v)
			}
			if a, _ := st.GetAlias(ctx, "sm", "A"); a != nil {
				t.Errorf("alias A survived delete: %+v", a)
			}
			if other, _ := st.ListVersions(ctx, "sm-other"); len(other) != 1 {
				t.Errorf("sm-other versions = %+v, want untouched", other)
			}
		})
	}
}

func TestStore_malformedVersionAndAliasRecordsAreIsolated(t *testing.T) {
	// Given: a corrupt version and a corrupt alias beside good ones
	backend := state.NewMemoryStore()
	st := newStore(backend, "us-east-1")
	ctx := context.Background()
	if err := st.PutVersion(ctx, &StateMachineVersion{StateMachineName: "sm", ARN: "sm:1", Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutAlias(ctx, &StateMachineAlias{StateMachineName: "sm", Name: "A", ARN: "sm:A"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{versionKey("sm", 2), aliasKey("sm", "B")} {
		if err := backend.Set(ctx, storeNS, serviceutil.RegionKey("us-east-1", key), "{not json"); err != nil {
			t.Fatal(err)
		}
	}

	// When: the lists are read
	versions, verr := st.ListVersions(ctx, "sm")
	aliases, aerr := st.ListAliases(ctx, "sm")

	// Then: the corrupt records are skipped, not fatal
	if verr != nil || len(versions) != 1 || aerr != nil || len(aliases) != 1 {
		t.Errorf("versions=%+v (%v) aliases=%+v (%v)", versions, verr, aliases, aerr)
	}
	// And: reading one by name reports the decode failure
	if _, err := st.GetVersion(ctx, "sm", 2); err == nil {
		t.Error("GetVersion on a corrupt record returned no error")
	}
	if _, err := st.GetAlias(ctx, "sm", "B"); err == nil {
		t.Error("GetAlias on a corrupt record returned no error")
	}
}

func TestStore_versionAndAliasErrorsPropagate(t *testing.T) {
	// Given: a store whose backend fails every call
	st := newStore(failingStore{}, "us-east-1")
	ctx := context.Background()

	// When / Then: each operation surfaces the backend error
	checks := map[string]error{
		"PutVersion": st.PutVersion(ctx, &StateMachineVersion{StateMachineName: "sm", Version: 1}),
		"PutAlias":   st.PutAlias(ctx, &StateMachineAlias{StateMachineName: "sm", Name: "A"}),
	}
	_, checks["GetVersion"] = st.GetVersion(ctx, "sm", 1)
	_, checks["GetAlias"] = st.GetAlias(ctx, "sm", "A")
	_, checks["ListVersions"] = st.ListVersions(ctx, "sm")
	_, checks["ListAliases"] = st.ListAliases(ctx, "sm")
	for name, err := range checks {
		if !errors.Is(err, errBackend) {
			t.Errorf("%s: err = %v, want the backend error", name, err)
		}
	}
}

func TestBuildExecutionStatusChangeEntry_carriesVersionAndAlias(t *testing.T) {
	cases := []struct {
		name        string
		exec        Execution
		wantVersion any
		wantAlias   any
	}{
		{name: "through an alias", exec: Execution{StateMachineVersionArn: "sm:1", StateMachineAliasArn: "sm:PROD"},
			wantVersion: "sm:1", wantAlias: "sm:PROD"},
		{name: "unqualified", exec: Execution{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an execution started through the given ARN
			tc.exec.ExecutionArn = "exec"
			tc.exec.Status = statusRunning

			// When: its status-change event is built
			entry, ok := buildExecutionStatusChangeEntry(&tc.exec)
			if !ok {
				t.Fatal("no entry built")
			}
			var detail map[string]any
			if err := json.Unmarshal([]byte(entry.Detail), &detail); err != nil {
				t.Fatalf("decode detail: %v", err)
			}

			// Then: the version and alias ARNs appear only when set
			if detail["stateMachineVersionArn"] != tc.wantVersion || detail["stateMachineAliasArn"] != tc.wantAlias {
				t.Errorf("detail version=%v alias=%v", detail["stateMachineVersionArn"], detail["stateMachineAliasArn"])
			}
		})
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

var errBackend = errors.New("backend unavailable")

// failingStore is a state.Store whose every call fails.
type failingStore struct{ state.Store }

func (failingStore) Get(context.Context, string, string) (string, bool, error) {
	return "", false, errBackend
}
func (failingStore) Set(context.Context, string, string, string) error { return errBackend }
func (failingStore) Scan(context.Context, string, string) ([]state.KV, error) {
	return nil, errBackend
}

func versionTestBackends(t *testing.T) map[string]state.Store {
	t.Helper()
	backends := map[string]state.Store{"memory": state.NewMemoryStore()}
	if !config.SQLiteSupported() {
		return backends
	}
	sqlite, err := state.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = sqlite.Close() })
	backends["sqlite"] = sqlite
	return backends
}

func newVersionsTestHandler(t *testing.T, backend state.Store) *Handler {
	t.Helper()
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	log := serviceutil.NewServiceLogger(zap.NewNop(), "stepfunctions")
	return newHandler(cfg, newStore(backend, cfg.Region), log, clock.New())
}

func passASL(result string) string {
	return `{"StartAt":"P","States":{"P":{"Type":"Pass","Result":` + result + `,"End":true}}}`
}

func createTestStateMachine(t *testing.T, h *Handler, name, result string) string {
	t.Helper()
	resp, aerr := h.createStateMachineTyped(context.Background(), &createStateMachineRequest{
		Name:       name,
		Definition: passASL(result),
		RoleArn:    "arn:aws:iam::000000000000:role/r",
	})
	if aerr != nil {
		t.Fatalf("createStateMachineTyped: %+v", aerr)
	}
	return resp.StateMachineArn
}

func mustPublish(t *testing.T, h *Handler, smArn string) string {
	t.Helper()
	resp, aerr := h.publishStateMachineVersionTyped(context.Background(), &publishStateMachineVersionRequest{StateMachineArn: smArn})
	if aerr != nil {
		t.Fatalf("publish: %+v", aerr)
	}
	return resp.StateMachineVersionArn
}
