package lambda

// store_publish_on_create_test.go — createFunctionPublishing, the store half
// of CreateFunction with Publish=true: the function and its version 1 land
// together or not at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/serviceutil"
	"github.com/overcast-sh/overcast/internal/state"
)

// failingSetStore refuses every write to one namespace, so a test can make
// the version write fail while the function write succeeds.
type failingSetStore struct {
	state.Store
	namespace string
}

func (s *failingSetStore) Set(ctx context.Context, namespace, key, value string) error {
	if namespace == s.namespace {
		return errors.New("injected: " + namespace + " is read-only")
	}
	return s.Store.Set(ctx, namespace, key, value)
}

func publishOnCreateFunction(name string) *Function {
	return &Function{
		Name:          name,
		ARN:           "arn:aws:lambda:us-east-1:000000000000:function:" + name,
		Runtime:       "nodejs22.x",
		Handler:       "index.handler",
		PackageType:   "Zip",
		Architectures: []string{"x86_64"},
		State:         "Pending",
		RevisionId:    name + "-revision",
		CreationID:    name + "-creation",
	}
}

func TestCreateFunctionPublishing_commitsVersionOneWithTheFunction(t *testing.T) {
	// Given: an empty store.
	ctx := context.Background()
	ls := newLambdaStore(state.NewMemoryStore(), "us-east-1", clock.NewMock())
	fn := publishOnCreateFunction("publish-fn")

	// When: the function is created with its initial snapshot.
	initial := &FunctionVersion{Function: *fn, CodeSha256: "sha"}
	created, aerr := ls.createFunctionPublishing(ctx, fn, initial)
	if aerr != nil || !created {
		t.Fatalf("createFunctionPublishing: created=%v err=%v", created, aerr)
	}

	// Then: the store allocated version 1 and both records are readable.
	if initial.Version != 1 {
		t.Fatalf("initial.Version = %d, want 1 (allocated by the store)", initial.Version)
	}
	if got, aerr := ls.getFunction(ctx, "publish-fn"); aerr != nil || got == nil {
		t.Fatalf("getFunction after create = %v, %v", got, aerr)
	}
	versions, aerr := ls.listVersions(ctx, "publish-fn")
	if aerr != nil || len(versions) != 1 || versions[0].Version != 1 || versions[0].CodeSha256 != "sha" {
		t.Fatalf("listVersions = %+v, %v; want exactly version 1 with the snapshot's CodeSha256", versions, aerr)
	}

	// And: the counter continues from the version the create allocated.
	next := &FunctionVersion{Function: *fn}
	if aerr := ls.publishVersion(ctx, next); aerr != nil || next.Version != 2 {
		t.Fatalf("publishVersion after create = version %d, err %v; want 2", next.Version, aerr)
	}

	// And: a second create of the same name is refused without publishing.
	again, aerr := ls.createFunctionPublishing(ctx, publishOnCreateFunction("publish-fn"), &FunctionVersion{Function: *fn})
	if aerr != nil || again {
		t.Fatalf("second createFunctionPublishing: created=%v err=%v, want false/nil", again, aerr)
	}
	if versions, _ := ls.listVersions(ctx, "publish-fn"); len(versions) != 2 {
		t.Fatalf("versions after a refused create = %d, want the 2 already published", len(versions))
	}
}

func TestCreateFunction_publishTrueRollsBackWhenTheVersionCannotBeStored(t *testing.T) {
	// Given: a handler over a store that accepts the function record but
	// refuses to write a version.
	clk := clock.NewMock()
	backing := state.NewMemoryStore()
	ls := newLambdaStore(&failingSetStore{Store: backing, namespace: nsVersions}, "us-east-1", clk)
	h := &Handler{
		cfg: &config.Config{Region: "us-east-1"},
		log: serviceutil.NewServiceLogger(zap.NewNop(), "lambda"),
		clk: clk,
		ls:  ls,
	}
	body, _ := json.Marshal(map[string]any{
		"FunctionName": "rollback-fn",
		"Runtime":      "nodejs22.x",
		"Handler":      "index.handler",
		"Role":         "arn:aws:iam::000000000000:role/lambda-role",
		"Code":         map[string]any{"ZipFile": []byte("rollback-zip-bytes")},
		"Publish":      true,
	})
	req := httptest.NewRequest(http.MethodPost, "/2015-03-31/functions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When: the function is created with Publish=true.
	h.CreateFunction(rec, req)

	// Then: the create fails as a whole and persists nothing — no function
	// record, no package, no version — so the name is free to create again.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("CreateFunction status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
	ctx := context.Background()
	if fn, aerr := ls.getFunction(ctx, "rollback-fn"); aerr != nil || fn != nil {
		t.Fatalf("getFunction after a failed publishing create = %v, %v; want absent", fn, aerr)
	}
	if packages, err := backing.Scan(ctx, nsFunctionCode, serviceutil.RegionKey("us-east-1", "rollback-fn")); err != nil || len(packages) != 0 {
		t.Fatalf("packages left behind = %d, %v; want none", len(packages), err)
	}
	if versions, aerr := ls.listVersions(ctx, "rollback-fn"); aerr != nil || len(versions) != 0 {
		t.Fatalf("versions left behind = %d, %v; want none", len(versions), aerr)
	}
	created, aerr := ls.createFunction(ctx, publishOnCreateFunction("rollback-fn"))
	if aerr != nil || !created {
		t.Fatalf("plain create after the rollback: created=%v err=%v, want the name to be free", created, aerr)
	}
}
