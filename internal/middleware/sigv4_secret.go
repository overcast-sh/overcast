package middleware

import (
	"context"
	"encoding/json"

	"github.com/overcast-sh/overcast/internal/state"
)

// SecretResolver resolves the secret access key for a given access key ID.
// The default implementation looks up IAM user access keys and STS role
// session credentials stored in the emulator's state store.
//
// If no secret is found, implementations should return ("", false, nil) so
// the middleware can fall back to the default secret.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, accessKeyID string) (secret string, found bool, err error)
}

// defaultSecretResolver resolves secrets by scanning IAM user records and
// STS role sessions stored in the state.Store. It follows the same namespace
// conventions used by the IAM enforcement middleware.
type defaultSecretResolver struct {
	st state.Store
}

// NewSecretResolver returns a SecretResolver backed by the emulator's state
// store. When st is nil the returned resolver never finds a secret so the
// middleware falls back to the hardcoded default ("test").
func NewSecretResolver(st state.Store) SecretResolver {
	if st == nil {
		return nil
	}
	return &defaultSecretResolver{st: st}
}

func (r *defaultSecretResolver) ResolveSecret(ctx context.Context, accessKeyID string) (string, bool, error) {
	// 1) IAM user access keys — scan all users, find the matching key.
	if secret, found := r.resolveUserSecret(ctx, accessKeyID); found {
		return secret, true, nil
	}
	// 2) STS role sessions — temporary credentials issued by AssumeRole.
	if secret, found := r.resolveSessionSecret(ctx, accessKeyID); found {
		return secret, true, nil
	}
	return "", false, nil
}

func (r *defaultSecretResolver) resolveUserSecret(ctx context.Context, accessKeyID string) (string, bool) {
	users, err := r.st.Scan(ctx, iamUsersNamespace, "")
	if err != nil {
		return "", false
	}
	for _, kv := range users {
		var user iamUserRecord
		if err := json.Unmarshal([]byte(kv.Value), &user); err != nil {
			continue
		}
		for _, key := range user.AccessKeys {
			if key.AccessKeyID == accessKeyID {
				if key.SecretAccessKey != "" {
					return key.SecretAccessKey, true
				}
				return "", false
			}
		}
	}
	return "", false
}

func (r *defaultSecretResolver) resolveSessionSecret(ctx context.Context, accessKeyID string) (string, bool) {
	raw, found, err := r.st.Get(ctx, iamSessionsNamespace, accessKeyID)
	if err != nil || !found {
		return "", false
	}
	var session RoleSessionRecord
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return "", false
	}
	if session.SecretAccessKey != "" {
		return session.SecretAccessKey, true
	}
	return "", false
}
