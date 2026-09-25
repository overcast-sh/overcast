package athena

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/zap"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/events"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/state"
)

// generatedResult is a SELECT of id, name and note whose pages are made as
// the engine is asked for them.
type generatedResult struct {
	pages, rowsPerPage int
	// onPage, when set, is called as each page is made.
	onPage func(n int)
}

var generatedColumns = []trinoColumn{
	{Name: "id", Type: "bigint", TypeSignature: trinoTypeSignature{RawType: "bigint"}},
	{Name: "name", Type: "varchar", TypeSignature: trinoTypeSignature{RawType: "varchar"}},
	{Name: "note", Type: "varchar", TypeSignature: trinoTypeSignature{RawType: "varchar"}},
}

// data is page n's rows, with quotes, separators, empty strings and NULLs
// among them.
func (g generatedResult) data(n int) [][]any {
	data := make([][]any, g.rowsPerPage)
	for i := range data {
		id := n*g.rowsPerPage + i
		var note any = "row " + strconv.Itoa(id)
		switch {
		case id%5 == 0:
			note = nil
		case id%11 == 0:
			note = ""
		}
		data[i] = []any{json.Number(strconv.Itoa(id)), fmt.Sprintf(`name "%d", tab	here`, id%97), note}
	}
	return data
}

func (g generatedResult) script() trinoScript {
	return trinoScript{count: g.pages, page: func(n int) trinoResponse {
		if g.onPage != nil {
			g.onPage(n)
		}
		state := "RUNNING"
		if n == g.pages-1 {
			state = "FINISHED"
		}
		return trinoResponse{Columns: generatedColumns, Data: g.data(n), Stats: trinoStats{State: state}}
	}}
}

// legacyCSV is the result as the buffered encoder wrote it: its size and
// digest, computed a page at a time, as every line stands alone.
func (g generatedResult) legacyCSV() (int, [sha256.Size]byte) {
	h := sha256.New()
	size, _ := h.Write(legacyEncodeCSV([][]*string{textRow("id", "name", "note")}))
	for n := range g.pages {
		rows := make([][]*string, 0, g.rowsPerPage)
		for _, values := range g.data(n) {
			rows = append(rows, formatRow(values, generatedColumns))
		}
		written, _ := h.Write(legacyEncodeCSV(rows))
		size += written
	}
	return size, [sha256.Size]byte(h.Sum(nil))
}

// digestS3 is an S3 accessor that keeps only the size and digest of what is
// written to it.
type digestS3 struct {
	mu      sync.Mutex
	objects map[string]objectDigest
}

type objectDigest struct {
	size int
	sum  [sha256.Size]byte
}

func (d *digestS3) put(_ context.Context, bucket, key string, body io.Reader, _ events.S3PutObjectOptions) (events.S3PutObjectResult, *protocol.AWSError) {
	h := sha256.New()
	n, err := io.Copy(h, body)
	if err != nil {
		return events.S3PutObjectResult{}, protocol.Wrap(protocol.ErrInternalError, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.objects[bucket+"/"+key] = objectDigest{size: int(n), sum: [sha256.Size]byte(h.Sum(nil))}
	return events.S3PutObjectResult{}, nil
}

// chunklessStore drops a result's row chunks, so the heap a test measures is
// the stream's own rather than the memory store's copy of the rows.
type chunklessStore struct{ state.Store }

func (s chunklessStore) Set(ctx context.Context, ns, key, value string) error {
	if ns == nsResults && strings.Contains(key, "/") {
		return nil
	}
	return s.Store.Set(ctx, ns, key, value)
}

// finalTransition runs qe on r through the executor and returns how it ended.
func finalTransition(s *Service, qe QueryExecution, r queryRunner) queryTransition {
	var last queryTransition
	s.statements.execute(context.Background(), qe, r, func(_ context.Context, _ string, t queryTransition) { last = t })
	return last
}

func TestResultStream_millionsOfRowsInBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("streams two million rows")
	}
	// Given: a SELECT of two million rows, served a page at a time, with the
	// heap sampled as each tenth page is made
	var peak atomic.Uint64
	sample := func() {
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		for old := peak.Load(); m.HeapAlloc > old && !peak.CompareAndSwap(old, m.HeapAlloc); old = peak.Load() {
		}
	}
	g := generatedResult{pages: 200, rowsPerPage: 10_000}
	if raceDetectorEnabled {
		g.pages = 20
	}
	wantSize, wantSum := g.legacyCSV()
	g.onPage = func(n int) {
		if n%10 == 0 {
			sample()
		}
	}
	f := newFakeTrino(t)
	f.generate("SELECT big", g.script())
	cfg := &config.Config{Region: "us-east-1", AccountID: "123456789012", AthenaMaxResultBytes: config.DefaultAthenaMaxResultBytes}
	s := New(cfg, chunklessStore{state.NewMemoryStore()}, zap.NewNop(), clock.New())
	s3 := &digestS3{objects: map[string]objectDigest{}}
	s.InitS3Access(s3.put, nil)
	qe := QueryExecution{QueryExecutionId: "big", ResultConfiguration: ResultConfiguration{OutputLocation: "s3://results/big.csv"}}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)

	// When: it runs
	final := finalTransition(s, qe, testRunner(readyEngine(t, f.srv.URL), "SELECT big"))

	// Then: it succeeded, the result object is byte for byte what the
	// buffered encoder wrote, and every row was counted
	if final.State != stateSucceeded {
		t.Fatalf("state = %s: %s", final.State, final.StateChangeReason)
	}
	if got := s3.objects["results/big.csv"]; got.size != wantSize || got.sum != wantSum {
		t.Fatalf("result object is %d bytes (sha256 %x), want %d (%x)", got.size, got.sum, wantSize, wantSum)
	}
	res, err := s.statements.results.header(context.Background(), "big")
	if rows := g.pages * g.rowsPerPage; err != nil || res == nil || res.RowCount != rows+1 || res.Runtime.OutputRows != int64(rows) {
		t.Fatalf("stored result = %+v, %v", res, err)
	}

	// And: the heap never grew by more than a few pages' worth. Held whole,
	// the rows take far more: the memory store's JSON copy of them alone,
	// were it kept here, grows the heap by some 90 MiB.
	const bound = 32 << 20
	if grew := int64(peak.Load()) - int64(base.HeapAlloc); !raceDetectorEnabled && grew > bound {
		t.Fatalf("heap grew %d MiB while streaming, want under %d MiB", grew>>20, bound>>20)
	}
}

func TestResultStream_resultOverTheLimitFailsAndLeavesNothing(t *testing.T) {
	// Given: a limit the result passes on its third thousand rows, after a
	// chunk has been stored and the object started
	ctx := context.Background()
	s, s3 := newCatalogService(t)
	s.statements.maxResultBytes = 80_000
	f := newFakeTrino(t)
	f.generate("SELECT big", generatedResult{pages: 3, rowsPerPage: 1500}.script())
	qe := QueryExecution{QueryExecutionId: "big", ResultConfiguration: ResultConfiguration{OutputLocation: "s3://results/big.csv"}}

	// When: it runs
	final := finalTransition(s, qe, testRunner(readyEngine(t, f.srv.URL), "SELECT big"))

	// Then: it failed as the user's error, saying which limit, and was
	// cancelled on the engine
	if e := final.AthenaError; final.State != stateFailed || e == nil || e.ErrorCategory != errorCategoryUser ||
		e.ErrorType != errorTypeResourcesExhausted || !strings.Contains(e.ErrorMessage, "ATHENA_MAX_RESULT_BYTES") {
		t.Fatalf("transition = %+v / %+v", final, final.AthenaError)
	}
	if len(f.deletes()) != 1 {
		t.Fatalf("DELETEs = %v, want the query cancelled", f.deletes())
	}

	// And: nothing of the result is left in S3 or the store
	if len(s3.objects) != 0 {
		t.Fatalf("objects = %v", s3.objects)
	}
	if keys, err := s.store.store.List(ctx, nsResults, ""); err != nil || len(keys) != 0 {
		t.Fatalf("stored result keys = %v, %v", keys, err)
	}
}
