package groups

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast-compat-cli/internal/harness"
)

// encodeBase64 returns the standard base64 encoding of b.
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// statusRunner runs a CLI command and reports the HTTP status of the last
// response alongside the error. Both awscli.RunStatus and its signed
// counterpart satisfy it, which is what lets one assertion serve a service
// reachable unsigned and one that is not.
type statusRunner func(endpoint, region string, args ...string) (int, error)

// assertAWSFailure runs a command that must fail, and asserts both halves of
// the AWS error contract: the error code the model names, and the HTTP status
// it is bound to. Checking only the code would miss a status change, which is
// exactly what alpha.35 did to several services.
func assertAWSFailure(run statusRunner, t *harness.TestContext, testName, wantCode string, wantStatus int, args ...string) error {
	status, err := run(t.Endpoint, t.Region, args...)
	if err == nil {
		return fmt.Errorf("%s: expected %s, got success", testName, wantCode)
	}
	if !strings.Contains(err.Error(), wantCode) {
		return fmt.Errorf("%s: expected %s, got %v", testName, wantCode, err)
	}
	if status != wantStatus {
		return fmt.Errorf("%s: expected HTTP %d for %s, got %d", testName, wantStatus, wantCode, status)
	}
	return nil
}

// isAlreadyExists reports whether the error is an AWS "already exists" error,
// used to make create-* operations idempotent across test runs when the
// emulator retains state from a previous run.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "EntityAlreadyExists") ||
		strings.Contains(s, "BucketAlreadyOwnedByYou") ||
		strings.Contains(s, "ResourceInUseException") ||
		strings.Contains(s, "StateMachineAlreadyExists") ||
		strings.Contains(s, "AlreadyExistsException") ||
		strings.Contains(s, "already exists")
}
