package athenaengine_test

// iam_test.go — Athena on the real engine with IAM enforcement on. The
// engine reads Glue and S3 through its own gateway, signed with a key no
// principal holds; the query that asks it to is authorised when it starts.

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/overcast-sh/overcast/tests/helpers"
)

func TestAthenaEngine_IAMEnforcement(t *testing.T) {
	// Given: IAM enforcement on, and principals allowed Athena with and
	// without Glue and S3, and one allowed Glue and S3 but not Athena
	requireEngineImage(t)
	srv := helpers.NewTestServer(t, helpers.WithAthenaEngine(), helpers.WithEnforceIAM(true))
	helpers.SeedIAMUser(t, srv, "admin", allow(t, "*"))
	helpers.SeedIAMUser(t, srv, "analyst", allow(t, "athena:*", "glue:*", "s3:*"))
	helpers.SeedIAMUser(t, srv, "athena-only", allow(t, "athena:*"))
	helpers.SeedIAMUser(t, srv, "no-athena", allow(t, "glue:*", "s3:*"))

	// And: a CSV table, arranged by a principal allowed everything
	admin := newEnvAs(t, srv, "admin")
	admin.waitForDocker()
	admin.createBucket("athena-engine-results")
	admin.createBucket(bucket)
	admin.put("people/part-1.csv", "id,name\n1,alice\n2,bob\n")
	admin.mustQuery("CREATE DATABASE IF NOT EXISTS demo")
	admin.mustQuery(`CREATE EXTERNAL TABLE demo.people (id string, name string)
		ROW FORMAT DELIMITED FIELDS TERMINATED BY ','
		LOCATION 's3://` + bucket + `/people/'
		TBLPROPERTIES ('skip.header.line.count' = '1')`)
	const query = "SELECT id, name FROM demo.people ORDER BY id"

	t.Run("a principal allowed Athena, Glue and S3", func(t *testing.T) {
		// When: it queries the table
		e := newEnvAs(t, srv, "analyst")
		id := e.mustQuery(query)

		// Then: the engine read the table, through Glue and S3
		if got := rowsOf(e.results(id, 0, "")); len(got) != 3 || got[1][1] != "alice" || got[2][1] != "bob" {
			t.Fatalf("rows = %v, want the header, alice and bob", got)
		}
	})

	t.Run("a principal allowed Athena alone", func(t *testing.T) {
		// When: it queries the same table
		e := newEnvAs(t, srv, "athena-only")
		id := e.start(query)

		// Then: the query succeeds. On AWS it fails, for want of glue:GetTable
		// and s3:GetObject: the engine's calls are not authorised as the
		// principal that started the query (docs/services/athena/limitations.md,
		// #2258).
		if qe := e.wait(id); qe.Status.State != types.QueryExecutionStateSucceeded {
			t.Fatalf("state = %s: %s, want SUCCEEDED", qe.Status.State, aws.ToString(qe.Status.StateChangeReason))
		}
	})

	t.Run("a principal not allowed Athena", func(t *testing.T) {
		// When: it starts a query
		e := newEnvAs(t, srv, "no-athena")
		_, err := e.athena.StartQueryExecution(e.ctx, &athena.StartQueryExecutionInput{
			QueryString:         aws.String(query),
			ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(results)},
		})

		// Then: StartQueryExecution itself is denied. Its AccessDeniedException
		// code cannot be read until the denial is in Athena's JSON envelope
		// (#2259).
		var re *smithyhttp.ResponseError
		if !errors.As(err, &re) || re.HTTPStatusCode() != http.StatusForbidden {
			t.Fatalf("StartQueryExecution error = %v, want 403", err)
		}
	})
}

// allow is a policy allowing actions on every resource.
func allow(t *testing.T, actions ...string) string {
	t.Helper()
	policy, err := json.Marshal(map[string]any{
		"Version":   "2012-10-17",
		"Statement": []map[string]any{{"Effect": "Allow", "Action": actions, "Resource": "*"}},
	})
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	return string(policy)
}
