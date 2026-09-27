package serviceutil

import "testing"

func TestDecodeJWTClaims(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantSub string
		wantErr bool
	}{
		// {"sub":"c"}, without and with base64 padding
		{"unpadded payload", "e30.eyJzdWIiOiJjIn0.sig", "c", false},
		{"padded payload", "e30.eyJzdWIiOiJjIn0=.sig", "c", false},
		{"no payload section", "opaque-access-token", "", true},
		{"payload not base64url", "e30.!!!.sig", "", true},
		{"payload not JSON", "e30.bm90LWpzb24.sig", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a token
			// When: its claims are decoded
			claims, err := DecodeJWTClaims(tc.token)

			// Then: the payload's claims, or why there are none
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got, _ := claims["sub"].(string); got != tc.wantSub {
				t.Fatalf("sub = %q, want %q", got, tc.wantSub)
			}
		})
	}
}
