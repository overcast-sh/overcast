package helpers

import (
	"context"
	"encoding/json"
	"testing"
)

// SeedIAMUser writes an IAM user named after accessKey, holding that key,
// whose one inline policy is policy. With IAM enforcement on, the IAM API
// denies a caller no principal holds the key of, so a test's first principal
// can only be arranged in the store.
func SeedIAMUser(t *testing.T, srv *TestServer, accessKey, policy string) {
	t.Helper()
	if srv.Store == nil {
		t.Fatal("test server store is nil")
	}
	user, err := json.Marshal(map[string]any{
		"UserName":       accessKey,
		"AccessKeys":     []map[string]string{{"AccessKeyId": accessKey}},
		"InlinePolicies": map[string]string{"inline-1": policy},
	})
	if err != nil {
		t.Fatalf("marshal user %s: %v", accessKey, err)
	}
	if err := srv.Store.Set(context.Background(), "iam:users", accessKey, string(user)); err != nil {
		t.Fatalf("seed user %s: %v", accessKey, err)
	}
}
