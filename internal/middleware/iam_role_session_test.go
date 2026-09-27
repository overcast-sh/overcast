package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// These tests pin #2272: a role session is named by its assumed-role session
// ARN, as AWS names it, while the condition keys that name the role keep
// naming the role.

const (
	sessionAccessKey = "ASIA-session-key"
	sessionRoleARN   = "arn:aws:iam::000000000000:role/team/app"
	// sessionAssumedRoleID is the AssumedRoleId STS returned for the session.
	sessionAssumedRoleID = "AROAEXAMPLEROLEID:alice"
)

// roleSessionStores returns one store per state backend this build provides.
func roleSessionStores(t *testing.T) map[string]state.Store {
	t.Helper()
	stores := map[string]state.Store{"memory": state.NewMemoryStore()}
	if !config.SQLiteSupported() {
		return stores
	}
	sqlStore, err := state.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("state.NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() {
		if err := sqlStore.Close(); err != nil {
			t.Logf("sqlStore.Close: %v", err)
		}
	})
	stores["sql"] = sqlStore
	return stores
}

// seedRoleSession stores the "app" role with policy inline, and a session of
// it under sessionAccessKey. session is the record's RoleSessionName; "" seeds
// a session recorded before STS kept it or its AssumedRoleId.
func seedRoleSession(t *testing.T, st state.Store, session, policy string) {
	t.Helper()
	ctx := context.Background()
	role, err := json.Marshal(map[string]any{
		"RoleName":       "app",
		"Arn":            sessionRoleARN,
		"InlinePolicies": map[string]string{"inline": policy},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, iamRolesNamespace, "app", string(role)); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	record := RoleSessionRecord{RoleArn: sessionRoleARN, RoleName: "app", SecretAccessKey: "secret"}
	if session != "" {
		record.RoleSessionName = session
		record.AssumedRoleID = sessionAssumedRoleID
	}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, iamSessionsNamespace, sessionAccessKey, string(b)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// listTopicsAsSession sends a signed SNS ListTopics as sessionAccessKey
// through enforcement over st.
func listTopicsAsSession(t *testing.T, st state.Store) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=ListTopics&Version=2010-03-31"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+sessionAccessKey+
		"/20260423/us-east-1/sns/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc")
	r.Header.Set("X-Amz-Date", "20260423T000000Z")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}
	return enforceSigned(t, st, stubQueryRouter{route: QueryRoute{Service: "sns", Action: "ListTopics"}, isQuery: true}, r)
}

func TestIAMEnforce_roleSessionDenialNamesTheCaller(t *testing.T) {
	cases := []struct {
		name    string
		session string
		caller  string
	}{
		{"a session is named by its assumed-role ARN, without the role's path", "alice", "arn:aws:sts::000000000000:assumed-role/app/alice"},
		{"a session recorded without its name is named by its role ARN", "", sessionRoleARN},
	}
	for _, tc := range cases {
		for backend, st := range roleSessionStores(t) {
			t.Run(tc.name+"/"+backend, func(t *testing.T) {
				// Given: a role session whose role denies everything
				seedRoleSession(t, st, tc.session, denyAllPolicy)

				// When: it lists SNS topics
				rec, served := listTopicsAsSession(t, st)

				// Then: the denial names the caller as AWS does
				want := "User: " + tc.caller + " is not authorized to perform: sns:ListTopics with an explicit deny in an identity-based policy"
				if served || !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("served=%v body=%s\nwant the message %q", served, rec.Body.String(), want)
				}
			})
		}
	}
}

func TestIAMEnforce_roleSessionConditionKeys(t *testing.T) {
	cases := []struct {
		name, session, key, value string
	}{
		// The IAM User Guide: for a role, aws:PrincipalArn is the role's ARN,
		// not the assumed-role session's.
		{"aws:PrincipalArn is the role ARN", "alice", "aws:PrincipalArn", sessionRoleARN},
		// The IAM User Guide: for an assumed role, aws:userid is
		// "<role ID>:<caller-specified role session name>": the AssumedRoleId.
		{"aws:userid is the AssumedRoleId", "alice", "aws:userid", sessionAssumedRoleID},
		{"aws:userid of a session recorded without its name is its access key", "", "aws:userid", sessionAccessKey},
	}
	for _, tc := range cases {
		for backend, st := range roleSessionStores(t) {
			t.Run(tc.name+"/"+backend, func(t *testing.T) {
				// Given: a role session allowed sns:ListTopics only when the key
				// has the value AWS gives it
				policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sns:ListTopics","Resource":"*",` +
					`"Condition":{"StringEquals":{"` + tc.key + `":"` + tc.value + `"}}}]}`
				seedRoleSession(t, st, tc.session, policy)

				// When: it lists SNS topics
				rec, served := listTopicsAsSession(t, st)

				// Then: the condition matched, so the call is served
				if !served {
					t.Fatalf("denied (status %d, body %s); want %s = %s", rec.Code, rec.Body.String(), tc.key, tc.value)
				}
			})
		}
	}
}

func TestIAMEnforce_malformedRoleSessionIsDeniedUnattributed(t *testing.T) {
	for backend, st := range roleSessionStores(t) {
		t.Run(backend, func(t *testing.T) {
			// Given: a session record whose session name does not decode
			seedRoleSession(t, st, "alice", denyAllPolicy)
			malformed := `{"RoleArn":"` + sessionRoleARN + `","RoleName":"app","RoleSessionName":7}`
			if err := st.Set(context.Background(), iamSessionsNamespace, sessionAccessKey, malformed); err != nil {
				t.Fatalf("seed malformed session: %v", err)
			}

			// When: its access key makes a call
			rec, served := listTopicsAsSession(t, st)

			// Then: the key names no principal, so the call is refused with the
			// generic denial rather than failing the request
			if served || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), iamUnattributedDeniedMessage) {
				t.Fatalf("served=%v status=%d body=%s; want the generic 403 denial", served, rec.Code, rec.Body.String())
			}
		})
	}
}
