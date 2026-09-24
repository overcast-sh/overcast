package athena_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/athena"
	"github.com/aws/aws-sdk-go-v2/service/athena/types"
	"github.com/aws/smithy-go"

	"github.com/overcast-sh/overcast/tests/helpers"
)

const resultsLocation = "s3://athena-results/queries/"

func athenaClient(t *testing.T, srv *helpers.TestServer) *athena.Client {
	t.Helper()
	return athena.New(athena.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(srv.URL),
		HTTPClient:   http.DefaultClient,
	})
}

// wantAPIError asserts err is the modeled Athena error code.
func wantAPIError(t *testing.T, what string, err error, code string) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: err = %v, want API error %s", what, err, code)
	}
	if apiErr.ErrorCode() != code {
		t.Fatalf("%s: code = %s (%s), want %s", what, apiErr.ErrorCode(), apiErr.ErrorMessage(), code)
	}
}

// must returns the value of a call that has to succeed.
func must[T any](t *testing.T, what string) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return v
	}
}

// startSDKQuery starts query in workGroup with a client-side result location.
func startSDKQuery(t *testing.T, c *athena.Client, workGroup, query string) string {
	t.Helper()
	out := must[*athena.StartQueryExecutionOutput](t, "StartQueryExecution")(c.StartQueryExecution(context.Background(), &athena.StartQueryExecutionInput{
		QueryString:         aws.String(query),
		WorkGroup:           aws.String(workGroup),
		ResultConfiguration: &types.ResultConfiguration{OutputLocation: aws.String(resultsLocation)},
	}))
	return aws.ToString(out.QueryExecutionId)
}
