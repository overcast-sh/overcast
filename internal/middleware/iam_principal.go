package middleware

import (
	"context"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/state"
)

// IAMPrincipal is an access key's compiled identity policies and identity, as
// IAM enforcement evaluates them. It is immutable and shared: callers must not
// modify it.
type IAMPrincipal struct {
	// Known is false when the access key names no IAM user or role session;
	// every other field is then zero.
	Known bool
	// ARN and Account are aws:PrincipalArn and aws:PrincipalAccount (a role
	// session's ARN is its role's).
	ARN     string
	Account string
	// Context holds the principal's own condition keys, lower-cased
	// (aws:principalarn, aws:userid, aws:username, aws:principaltype, …).
	Context map[string]string
	// Statements are the compiled identity policies, and Boundary the
	// permissions boundary (nil when none).
	Statements []iampolicy.Statement
	Boundary   *iampolicy.Boundary
	// Err is set when the principal's policies or boundary could not be read;
	// a caller must fail closed on it rather than evaluate a partial set.
	Err error
}

// IAMPrincipalResolver resolves access keys to their compiled policies through
// the same cache IAMEnforce uses, invalidated by InvalidateIAMEnforceCache. It
// serves callers outside the request chain that make their own authorisation
// decision (a data plane that sees a request after the chain rewrote it).
type IAMPrincipalResolver struct {
	st    state.Store
	cache *iamEnforceCache
}

// NewIAMPrincipalResolver returns a resolver over st. It does no work until
// Resolve is called. A nil st resolves nothing.
func NewIAMPrincipalResolver(st state.Store) *IAMPrincipalResolver {
	return &IAMPrincipalResolver{st: st, cache: &iamEnforceCache{}}
}

// Resolve returns accessKeyID's principal. The result is cached until the next
// IAM mutation, including a negative one for an unknown key.
func (p *IAMPrincipalResolver) Resolve(ctx context.Context, accessKeyID string) *IAMPrincipal {
	if p == nil || p.st == nil || accessKeyID == "" {
		return &IAMPrincipal{}
	}
	cached, generation := p.cache.load(accessKeyID)
	if cached == nil {
		principal := collectPrincipalPolicies(ctx, p.st, accessKeyID)
		statements, err := iampolicy.Compile(principal.docs)
		boundary, boundaryErr := resolveIAMPermissionsBoundary(ctx, p.st, principal.boundaryArn)
		cached = &iamEnforceCacheEntry{
			statements:   statements,
			principalCtx: principal.context,
			callerARN:    principal.callerArn,
			boundary:     boundary,
			boundaryErr:  boundaryErr,
			compileErr:   err,
		}
		p.cache.store(generation, accessKeyID, cached)
	}
	out := &IAMPrincipal{
		ARN:        cached.principalCtx["aws:principalarn"],
		Account:    cached.principalCtx["aws:principalaccount"],
		Context:    cached.principalCtx,
		Statements: cached.statements,
		Boundary:   cached.boundary,
	}
	out.Known = out.ARN != ""
	switch {
	case cached.compileErr != nil:
		out.Err = cached.compileErr
	case cached.boundaryErr != nil:
		out.Err = cached.boundaryErr
	}
	return out
}
