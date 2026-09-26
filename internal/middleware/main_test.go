package middleware

import (
	"os"
	"testing"

	"github.com/overcast-sh/overcast/internal/awsshapes/awsshapestest"
)

// TestMain reads the committed aws-sdk shape tables, which
// TestQueryDenialCodes_matchTheModels checks the IAM denial codes against: a
// test binary compiled from a bare checkout has none embedded until
// `make aws-sdk-shapes` runs.
func TestMain(m *testing.M) {
	awsshapestest.UseCommittedTables()
	os.Exit(m.Run())
}
