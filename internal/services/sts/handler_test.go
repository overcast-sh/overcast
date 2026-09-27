package sts

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/state"
)

// TestAssumedRoleArn verifies AssumedRoleUser.Arn is built from the role name
// parsed out of RoleArn, not the session name repeated in both path
// segments — see https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumedRoleUser.html.
func TestAssumedRoleArn(t *testing.T) {
	tests := []struct {
		name        string
		roleArn     string
		sessionName string
		want        string
	}{
		{
			name:        "simple role",
			roleArn:     "arn:aws:iam::123456789012:role/demo",
			sessionName: "Bob",
			want:        "arn:aws:sts::123456789012:assumed-role/demo/Bob",
		},
		{
			name:        "role arn with a path",
			roleArn:     "arn:aws:iam::123456789012:role/some/nested/path/MyRole",
			sessionName: "test-session",
			want:        "arn:aws:sts::123456789012:assumed-role/MyRole/test-session",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := assumedRoleArn("123456789012", tt.roleArn, tt.sessionName); got != tt.want {
				t.Errorf("assumedRoleArn(%q, %q) = %q, want %q", tt.roleArn, tt.sessionName, got, tt.want)
			}
		})
	}
}

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
