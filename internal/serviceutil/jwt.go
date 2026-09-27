package serviceutil

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeJWTClaims base64url-decodes the payload section of a JWT (between the
// first two dots) and reports why it could not. It verifies nothing — not the
// signature, the issuer nor the expiry — so a caller that needs any of those
// checks them itself. The payload is accepted with or without base64 padding.
func DecodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("malformed JWT: expected at least 2 parts, got %d", len(parts))
	}
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("JWT payload decode: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("JWT payload JSON: %w", err)
	}
	return claims, nil
}
