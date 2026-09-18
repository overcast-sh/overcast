package stepfunctions_test

import (
	"os"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsshapes/awsshapestest"
)

// TestMain reads the committed aws-sdk shape tables: a test binary compiled
// from a bare checkout has none embedded until `make aws-sdk-shapes` runs.
func TestMain(m *testing.M) {
	awsshapestest.UseCommittedTables()
	os.Exit(m.Run())
}
