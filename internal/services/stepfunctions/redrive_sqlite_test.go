//go:build !nosqlite

package stepfunctions

// The SQLite half of the redrive checkpoint store contract. Under -tags
// nosqlite state.NewSQLiteStore is a stub that always errors, so this lives
// in its own file with the matching constraint; the memory-store half is in
// redrive_test.go.

import (
	"testing"

	"github.com/overcast-sh/overcast/internal/state"
)

func TestRedriveCheckpoint_sqliteStore(t *testing.T) {
	store, err := state.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	checkpointStoreContract(t, store)
}
