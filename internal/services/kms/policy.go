package kms

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

const (
	maxKeyPolicyCharacters = 32768
	policyLockoutMessage   = "The new key policy will not allow you to update the key policy in the future."
)

var (
	errMalformedKeyPolicy = &protocol.AWSError{
		Code:       "MalformedPolicyDocumentException",
		Message:    "The policy document is not valid.",
		HTTPStatus: http.StatusBadRequest,
	}
	errPolicyLockout = &protocol.AWSError{
		Code:       "MalformedPolicyDocumentException",
		Message:    policyLockoutMessage,
		HTTPStatus: http.StatusBadRequest,
	}
	errInvalidKeyPolicyPrincipal = &protocol.AWSError{
		Code:       "MalformedPolicyDocumentException",
		Message:    "Policy contains a statement with one or more invalid principals",
		HTTPStatus: http.StatusBadRequest,
	}
	errKeyPolicyLimit = &protocol.AWSError{
		Code:       "LimitExceededException",
		Message:    "The request was rejected because a limit was exceeded.",
		HTTPStatus: http.StatusBadRequest,
	}
)

func (h *Handler) validateKeyPolicy(ctx context.Context, policy, keyARN string, bypass bool) *protocol.AWSError {
	if policy == "" || !validKeyPolicyCharacters(policy) {
		return errMalformedKeyPolicy
	}
	if utf8.RuneCountInString(policy) > maxKeyPolicyCharacters {
		return errKeyPolicyLimit
	}

	statements, err := iampolicy.ParseDocumentWithOptions(policy, iampolicy.SourceRef{
		ID:   keyARN,
		Type: iampolicy.SourceTypeResourcePolicy,
	}, iampolicy.ParseOptions{
		AllowMissingAction:  true,
		AllowedVersions:     []string{"2008-10-17", "2012-10-17"},
		RequireStatements:   true,
		RequirePrincipal:    true,
		RejectEmptyElements: true,
	})
	if err != nil {
		if strings.Contains(err.Error(), "Principal") {
			return protocol.Wrap(errInvalidKeyPolicyPrincipal, err)
		}
		return protocol.Wrap(errMalformedKeyPolicy, err)
	}
	if aerr := h.validateKeyPolicyPrincipals(ctx, statements); aerr != nil {
		return aerr
	}
	if bypass {
		return nil
	}

	principal := h.callerIdentity(ctx)
	effectiveStatements := make([]iampolicy.Statement, 0, len(statements))
	for _, statement := range statements {
		if statement.ResourceSpecified {
			effectiveStatements = append(effectiveStatements, statement)
		}
	}
	result := iampolicy.Evaluate(iampolicy.Input{
		Request: iampolicy.Request{
			Action:           "kms:PutKeyPolicy",
			Resource:         keyARN,
			PrincipalARN:     principal.ARN,
			PrincipalAccount: principal.Account,
			Context: map[string]string{
				"aws:principalarn":     principal.ARN,
				"aws:principalaccount": principal.Account,
				"aws:requestedregion":  middleware.RegionFromContext(ctx, h.cfg.Region),
				"kms:calleraccount":    principal.Account,
			},
		},
		ResourcePolicy: effectiveStatements,
	})
	if result.Decision != iampolicy.DecisionAllowed {
		return errPolicyLockout
	}
	return nil
}

func (h *Handler) validateKeyPolicyPrincipals(ctx context.Context, statements []iampolicy.Statement) *protocol.AWSError {
	for i := range statements {
		principal := statements[i].Principal
		if principal == nil {
			return errInvalidKeyPolicyPrincipal
		}
		// A key policy's principals are AWS principals and services; an
		// identity provider (Federated) is refused like a type the evaluator
		// does not know.
		if len(principal.Unsupported) > 0 || len(principal.Federated) > 0 {
			return errInvalidKeyPolicyPrincipal
		}
		for _, pattern := range principal.AWS {
			value := strings.TrimSpace(pattern.String())
			if isAccountID(value) {
				continue
			}
			parts := strings.SplitN(value, ":", 6)
			if len(parts) < 6 || parts[0] != "arn" || (parts[2] != "iam" && parts[2] != "sts") ||
				!isAccountID(parts[4]) || strings.TrimSpace(parts[5]) == "" || strings.ContainsAny(value, "*?") {
				return errInvalidKeyPolicyPrincipal
			}
			if parts[2] == "iam" && parts[4] == h.cfg.AccountID && parts[5] != "root" {
				// Overcast can prove existence only for principals in its own IAM
				// account. Remote-account principals remain syntax-validated because
				// their IAM state is intentionally unavailable to this emulator.
				exists, err := middleware.LocalIAMPrincipalExists(ctx, h.store.s, value)
				if err != nil {
					return protocol.Wrap(protocol.ErrInternalError, err)
				}
				if !exists {
					return errInvalidKeyPolicyPrincipal
				}
			}
		}
		for _, service := range principal.Service {
			if !strings.Contains(strings.TrimSpace(service), ".") {
				return errInvalidKeyPolicyPrincipal
			}
		}
	}
	return nil
}

func isAccountID(value string) bool {
	if len(value) != 12 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (h *Handler) callerIdentity(ctx context.Context) middleware.PrincipalIdentity {
	accessKey := middleware.SigV4AccessKeyFromContext(ctx)
	if principal, found := middleware.ResolvePrincipalIdentity(ctx, h.store.s, accessKey); found {
		return principal
	}
	return middleware.PrincipalIdentity{
		ARN:     "arn:aws:iam::" + h.cfg.AccountID + ":root",
		Account: h.cfg.AccountID,
	}
}

func validKeyPolicyCharacters(policy string) bool {
	for _, r := range policy {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xff) {
			continue
		}
		return false
	}
	return strings.TrimSpace(policy) != ""
}
