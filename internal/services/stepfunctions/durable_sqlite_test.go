//go:build !nosqlite

package stepfunctions

// The SQLite half of the park checkpoint store contract. Under -tags nosqlite
// state.NewSQLiteStore is a stub that always errors, so this lives in its own
// file with the matching constraint; the memory-store half is in
// durable_test.go.

import (
	"testing"

	"github.com/overcast-sh/overcast/internal/state"
)

func TestParkCheckpoint_sqliteStore(t *testing.T) {
	store, err := state.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	parkCheckpointStoreContract(t, store)
}
