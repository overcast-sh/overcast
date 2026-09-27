package sts

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/state"
)

// TestAssumeRole_wireParity asserts the legacy (Query XML) and typed
// (JSON/CBOR) handlers for AssumeRole and AssumeRoleWithWebIdentity build the
// identical AssumedRoleUser.Arn shape — a field added or fixed on one path
// must land on both, since callers can hit either depending on which
// protocol their SDK speaks.
func TestAssumeRole_wireParity(t *testing.T) {
	cfg := &config.Config{Region: "us-east-1", AccountID: "000000000000"}
	h := newHandler(cfg, nil, clock.New(), state.NewMemoryStore())

	const roleArn = "arn:aws:iam::000000000000:role/path/to/MyRole"
	const sessionName = "parity-session"
	const wantArn = "arn:aws:sts::000000000000:assumed-role/MyRole/parity-session"

	t.Run("AssumeRole", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/?Action=AssumeRole&RoleArn="+roleArn+"&RoleSessionName="+sessionName, nil)
		w := httptest.NewRecorder()
		h.AssumeRole(w, req)
		var legacy struct {
			Result struct {
				AssumedRoleUser struct {
					Arn string `xml:"Arn"`
				} `xml:"AssumedRoleUser"`
			} `xml:"AssumeRoleResult"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
			t.Fatalf("decode legacy AssumeRole response: %v\nbody: %s", err, w.Body.String())
		}
		legacyArn := legacy.Result.AssumedRoleUser.Arn
		if legacyArn != wantArn {
			t.Errorf("legacy AssumeRole Arn = %q, want %q", legacyArn, wantArn)
		}

		resp, aerr := h.assumeRoleTyped(context.Background(), &assumeRoleReq{RoleArn: roleArn, RoleSessionName: sessionName})
		if aerr != nil {
			t.Fatalf("assumeRoleTyped: %s", aerr.Message)
		}
		if resp.Result.AssumedRoleUser.Arn != wantArn {
			t.Errorf("typed AssumeRole Arn = %q, want %q", resp.Result.AssumedRoleUser.Arn, wantArn)
		}
		if legacyArn != resp.Result.AssumedRoleUser.Arn {
			t.Errorf("legacy/typed AssumeRole Arn mismatch: %q vs %q", legacyArn, resp.Result.AssumedRoleUser.Arn)
		}
	})

	t.Run("AssumeRoleWithWebIdentity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/?Action=AssumeRoleWithWebIdentity&RoleArn="+roleArn+"&RoleSessionName="+sessionName+"&WebIdentityToken=a.b.c", nil)
		w := httptest.NewRecorder()
		h.AssumeRoleWithWebIdentity(w, req)
		var legacy struct {
			Result struct {
				AssumedRoleUser struct {
					Arn string `xml:"Arn"`
				} `xml:"AssumedRoleUser"`
			} `xml:"AssumeRoleWithWebIdentityResult"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
			t.Fatalf("decode legacy AssumeRoleWithWebIdentity response: %v\nbody: %s", err, w.Body.String())
		}
		legacyArn := legacy.Result.AssumedRoleUser.Arn
		if legacyArn != wantArn {
			t.Errorf("legacy AssumeRoleWithWebIdentity Arn = %q, want %q", legacyArn, wantArn)
		}

		resp, aerr := h.assumeRoleWithWebIdentityTyped(context.Background(), &assumeRoleWithWebIdentityReq{RoleArn: roleArn, RoleSessionName: sessionName, WebIdentityToken: "a.b.c"})
		if aerr != nil {
			t.Fatalf("assumeRoleWithWebIdentityTyped: %s", aerr.Message)
		}
		if resp.Result.AssumedRoleUser.Arn != wantArn {
			t.Errorf("typed AssumeRoleWithWebIdentity Arn = %q, want %q", resp.Result.AssumedRoleUser.Arn, wantArn)
		}
		if legacyArn != resp.Result.AssumedRoleUser.Arn {
			t.Errorf("legacy/typed AssumeRoleWithWebIdentity Arn mismatch: %q vs %q", legacyArn, resp.Result.AssumedRoleUser.Arn)
		}
	})
}

// TestAssumeRole_persistsTheSession pins #2272: every path that issues
// role-session credentials records the session name and the AssumedRoleId it
// returned beside the role, so IAM enforcement can name the caller by its
// assumed-role session ARN and give it the aws:userid AWS gives it.
func TestAssumeRole_persistsTheSession(t *testing.T) {
	const roleArn = "arn:aws:iam::000000000000:role/team/app"
	const sessionName = "alice"
	issuers := map[string]func(*Handler) issuedSession{
		"AssumeRole (Query)": func(h *Handler) issuedSession {
			return sessionFromQuery(t, h.AssumeRole, "AssumeRole", "RoleArn="+roleArn+"&RoleSessionName="+sessionName)
		},
		"AssumeRoleWithWebIdentity (Query)": func(h *Handler) issuedSession {
			return sessionFromQuery(t, h.AssumeRoleWithWebIdentity, "AssumeRoleWithWebIdentity", "RoleArn="+roleArn+"&RoleSessionName="+sessionName+"&WebIdentityToken=a.b.c")
		},
		"AssumeRole (typed)": func(h *Handler) issuedSession {
			resp, aerr := h.assumeRoleTyped(context.Background(), &assumeRoleReq{RoleArn: roleArn, RoleSessionName: sessionName})
			if aerr != nil {
				t.Fatalf("assumeRoleTyped: %s", aerr.Message)
			}
			return issuedSession{resp.Result.Credentials.AccessKeyId, resp.Result.AssumedRoleUser.AssumedRoleId}
		},
		"AssumeRoleWithWebIdentity (typed)": func(h *Handler) issuedSession {
			resp, aerr := h.assumeRoleWithWebIdentityTyped(context.Background(), &assumeRoleWithWebIdentityReq{RoleArn: roleArn, RoleSessionName: sessionName, WebIdentityToken: "a.b.c"})
			if aerr != nil {
				t.Fatalf("assumeRoleWithWebIdentityTyped: %s", aerr.Message)
			}
			return issuedSession{resp.Result.Credentials.AccessKeyId, resp.Result.AssumedRoleUser.AssumedRoleId}
		},
	}
	for name, issue := range issuers {
		t.Run(name, func(t *testing.T) {
			// Given: an STS handler over a store
			st := state.NewMemoryStore()
			h := newHandler(&config.Config{Region: "us-east-1", AccountID: "000000000000"}, nil, clock.New(), st)

			// When: it issues credentials for a role session
			issued := issue(h)

			// Then: the session record names the role, the session, and the
			// AssumedRoleId the caller was given
			raw, found, err := st.Get(context.Background(), "iam:sessions", issued.accessKeyID)
			if err != nil || !found {
				t.Fatalf("iam:sessions[%s]: found=%v err=%v", issued.accessKeyID, found, err)
			}
			var record middleware.RoleSessionRecord
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				t.Fatalf("decode session record %s: %v", raw, err)
			}
			if record.RoleArn != roleArn || record.RoleName != "app" || record.RoleSessionName != sessionName {
				t.Fatalf("session record = %+v, want role %s (app) and session %s", record, roleArn, sessionName)
			}
			if !strings.HasSuffix(issued.assumedRoleID, ":"+sessionName) || record.AssumedRoleID != issued.assumedRoleID {
				t.Fatalf("recorded AssumedRoleId %q, returned %q; want the returned <role ID>:%s", record.AssumedRoleID, issued.assumedRoleID, sessionName)
			}
		})
	}
}

// issuedSession is what an STS call issuing role-session credentials told its
// caller.
type issuedSession struct {
	accessKeyID, assumedRoleID string
}

// sessionFromQuery calls a Query-protocol role-session handler and returns
// the session it issued.
func sessionFromQuery(t *testing.T, handler http.HandlerFunc, action, params string) issuedSession {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/?Action="+action+"&"+params, nil)
	w := httptest.NewRecorder()
	handler(w, req)
	var resp struct {
		// The result element is named for the action, so match it as any.
		Result struct {
			AccessKeyID   string `xml:"Credentials>AccessKeyId"`
			AssumedRoleID string `xml:"AssumedRoleUser>AssumedRoleId"`
		} `xml:",any"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s response: %v\nbody: %s", action, err, w.Body.String())
	}
	return issuedSession{resp.Result.AccessKeyID, resp.Result.AssumedRoleID}
}
