// Package awsshapestest points a test binary at the committed aws-sdk shape
// tables, so tests that run Step Functions aws-sdk integrations pass on a bare
// checkout — before `make aws-sdk-shapes` has built the compressed copies the
// awsshapes package embeds. See awsshapes.EnvTablesDir.
//
// Call it from TestMain:
//
//	func TestMain(m *testing.M) {
//		awsshapestest.UseCommittedTables()
//		os.Exit(m.Run())
//	}
package awsshapestest

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/overcast-sh/overcast/internal/awsshapes"
)

// UseCommittedTables sets awsshapes.EnvTablesDir to the repository's
// internal/awsshapes/tables, found by walking up from the working directory
// (a test's is its package directory) to go.mod. An EnvTablesDir the caller
// already set is left alone, so a developer can still point tests elsewhere.
// It panics when the tables cannot be found: every test after it would fail
// with a less useful message.
func UseCommittedTables() {
	if os.Getenv(awsshapes.EnvTablesDir) != "" {
		return
	}
	dir, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("awsshapestest: %v", err))
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("awsshapestest: no go.mod above the working directory")
		}
		dir = parent
	}
	tables := filepath.Join(dir, "internal", "awsshapes", "tables")
	if _, err := os.Stat(tables); err != nil {
		panic(fmt.Sprintf("awsshapestest: committed tables not found: %v", err))
	}
	if err := os.Setenv(awsshapes.EnvTablesDir, tables); err != nil {
		panic(fmt.Sprintf("awsshapestest: %v", err))
	}
}
