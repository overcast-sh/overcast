package iampolicy

import (
	"fmt"
	"sort"
	"strings"
)

// Decision is the outcome of an evaluation, using AWS's
// PolicyEvaluationDecisionType vocabulary.
type Decision string

const (
	DecisionAllowed      Decision = "allowed"
	DecisionExplicitDeny Decision = "explicitDeny"
	DecisionImplicitDeny Decision = "implicitDeny"
)

// Request describes the API call being authorized.
type Request struct {
	// Action is the IAM action key, e.g. "s3:GetObject".
	Action string
	// Resource is the ARN the action is being performed on, or "*".
	Resource string
	// Context holds IAM condition keys, keyed lower-case
	// (e.g. "aws:requestedregion").
	Context map[string]string
	// PrincipalARN and PrincipalAccount identify the caller; they are used to
	// match a resource-based policy's Principal block.
	PrincipalARN     string
	PrincipalAccount string
	// Federated names the identity provider a web identity caller
	// authenticated with, in every form a trust policy's Federated principal
	// may name it. It is empty for an IAM user or role caller.
	Federated []string
}

// Input is one evaluation: a request, the principal's identity policies, and
// optionally the resource-based policy attached to the resource and the
// permissions boundary attached to the principal.
type Input struct {
	Request        Request
	Identity       []Statement
	ResourcePolicy []Statement
	// Boundary is the principal's permissions boundary, or nil when it has
	// none. A non-nil boundary always takes part in the decision — including
	// one carrying no statements, which allows nothing. See [Boundary].
	Boundary *Boundary
}

// Boundary is a principal's permissions boundary: the managed policy that caps
// what its identity policies can grant.
//
// It is a distinct type rather than a bare statement slice because "no
// boundary" and "a boundary that allows nothing" are opposite outcomes and a
// nil slice cannot tell them apart. A boundary whose document could not be
// resolved or parsed is deliberately represented as an empty one — see
// [NewBoundary].
type Boundary struct {
	Statements []Statement
}

// NewBoundary compiles a permissions-boundary policy document. The boundary it
// returns is always usable; the error is there to be reported, not to decide
// what happens next.
//
// A document that cannot be parsed yields a boundary with no statements, which
// allows nothing. That is the only safe reading: a boundary exists to withhold
// permissions, so a boundary that cannot be read must never be read as absent —
// that would grant exactly what it was attached to prevent.
func NewBoundary(document string, src SourceRef) (*Boundary, error) {
	stmts, err := ParseDocument(document, src)
	if err != nil {
		return &Boundary{}, err
	}
	return &Boundary{Statements: stmts}, nil
}

// Match records a statement that applied to the request. It maps onto AWS's
// MatchedStatements.
type Match struct {
	Source SourceRef
	Sid    string
	Effect string
	// Index is the statement's 0-based position within its source document.
	Index int
	// StartPosition/EndPosition locate the statement within its source
	// document text; see [Position]. Nil if the statement's position could
	// not be determined (see [Statement.StartPosition]).
	StartPosition *Position
	EndPosition   *Position
}

// Result is the outcome of an evaluation.
type Result struct {
	Decision Decision
	// Matched lists the statements that applied, deny statements first, in the
	// order they were evaluated.
	Matched []Match
	// MissingContextKeys names condition keys a matching statement needed that
	// the request did not carry. AWS reports these as MissingContextValues.
	MissingContextKeys []string
	// Unsupported names policy constructs this evaluator does not implement.
	// It is never empty-and-ignored: callers must surface it rather than treat
	// the decision as authoritative.
	Unsupported []string
	// BoundaryApplied reports whether a permissions boundary took part in the
	// decision, and AllowedByBoundary whether that boundary allowed the action.
	// AWS reports the pair as PermissionsBoundaryDecisionDetail.
	BoundaryApplied   bool
	AllowedByBoundary bool
}

// Evaluate applies AWS's evaluation logic: an explicit deny anywhere wins;
// otherwise an allow from either the identity policies or (within the same
// account) the resource-based policy grants access; otherwise the default of
// implicit deny applies. A permissions boundary, when the principal has one,
// then caps the result — see [capWithBoundary].
//
// Evaluation allocates only when something has to be reported, so a caller
// that caches compiled statements can re-evaluate them on a hot request path.
func Evaluate(in Input) Result {
	var (
		res          Result
		rep          conditionReport
		allowed      bool
		explicitDeny bool
	)

	evaluateSet := func(stmts []Statement, resourceBased bool) {
		for i := range stmts {
			stmt := &stmts[i]
			if !stmt.appliesTo(in.Request, resourceBased, &rep) {
				continue
			}
			if !evaluateConditions(stmt.Condition, in.Request.Context, &rep) {
				continue
			}
			res.Matched = append(res.Matched, Match{
				Source:        stmt.Source,
				Sid:           stmt.Sid,
				Effect:        stmt.Effect,
				Index:         stmt.Index,
				StartPosition: stmt.StartPosition,
				EndPosition:   stmt.EndPosition,
			})
			if stmt.Effect == "Deny" {
				explicitDeny = true
				continue
			}
			allowed = true
		}
	}

	evaluateSet(in.Identity, false)
	evaluateSet(in.ResourcePolicy, true)

	// Condition blocks are maps, so what got reported depends on map iteration
	// order. Sorting keeps the response wire-stable across identical calls.
	sort.Strings(rep.missing)
	sort.Strings(rep.unsupported)
	res.MissingContextKeys = rep.missing
	res.Unsupported = rep.unsupported

	switch {
	case explicitDeny:
		res.Decision = DecisionExplicitDeny
	case allowed:
		res.Decision = DecisionAllowed
	default:
		res.Decision = DecisionImplicitDeny
	}
	if in.Boundary == nil {
		return res
	}
	return capWithBoundary(res, in.Request, in.Boundary)
}

// capWithBoundary intersects a decision with the principal's permissions
// boundary, which is how AWS computes a bounded entity's effective
// permissions: the action must be allowed by the identity policies *and* by
// the boundary, and an explicit deny in either is final.
//
// An action the identity policies allow but the boundary does not becomes an
// implicit deny — the boundary grants nothing of its own, so there is no
// statement to point at, which is exactly what AWS reports.
func capWithBoundary(res Result, req Request, boundary *Boundary) Result {
	boundaryRes := Evaluate(Input{Request: req, Identity: boundary.Statements})
	for _, msg := range boundaryRes.Unsupported {
		if !containsString(res.Unsupported, msg) {
			res.Unsupported = append(res.Unsupported, msg)
		}
	}

	res.BoundaryApplied = true
	res.AllowedByBoundary = boundaryRes.Decision == DecisionAllowed
	switch {
	case boundaryRes.Decision == DecisionExplicitDeny:
		res.Decision = DecisionExplicitDeny
	case !res.AllowedByBoundary && res.Decision == DecisionAllowed:
		res.Decision = DecisionImplicitDeny
	}
	return res
}

// appliesTo reports whether the statement's Action/Resource (and, for a
// resource-based policy, Principal) elements select this request.
func (s *Statement) appliesTo(req Request, resourceBased bool, rep *conditionReport) bool {
	if !s.matchesAction(req.Action) {
		return false
	}
	if !s.matchesResource(req.Resource, req.Context) {
		return false
	}
	if resourceBased && !s.matchesPrincipal(req, rep) {
		return false
	}
	return true
}

func (s *Statement) matchesAction(action string) bool {
	// Action names are matched case-insensitively by AWS.
	if len(s.Action) > 0 {
		return matchAny(s.Action, action, nil, true)
	}
	if len(s.NotAction) > 0 {
		return !matchAny(s.NotAction, action, nil, true)
	}
	return false
}

func (s *Statement) matchesResource(resource string, ctx map[string]string) bool {
	// A resource-based policy statement may omit Resource entirely, in which
	// case it applies to the resource the policy is attached to.
	if len(s.Resource) == 0 && len(s.NotResource) == 0 {
		return s.Principal != nil || s.NotPrincipal != nil
	}
	if len(s.Resource) > 0 {
		return matchAny(s.Resource, resource, ctx, false)
	}
	return !matchAny(s.NotResource, resource, ctx, false)
}

func (s *Statement) matchesPrincipal(req Request, rep *conditionReport) bool {
	if s.NotPrincipal != nil {
		reportPrincipalGaps(s.NotPrincipal, rep)
		return !principalSetMatches(s.NotPrincipal, req)
	}
	if s.Principal == nil {
		// A resource-based policy statement without a Principal grants nothing.
		return false
	}
	reportPrincipalGaps(s.Principal, rep)
	return principalSetMatches(s.Principal, req)
}

func reportPrincipalGaps(set *PrincipalSet, rep *conditionReport) {
	for _, msg := range set.Unsupported {
		rep.addUnsupported(msg)
	}
}

func principalSetMatches(set *PrincipalSet, req Request) bool {
	if set.Anonymous {
		return true
	}
	arn := strings.TrimSpace(req.PrincipalARN)
	account := strings.TrimSpace(req.PrincipalAccount)
	for _, p := range set.AWS {
		raw := p.String()
		if arn != "" && p.Match(arn) {
			return true
		}
		if account == "" {
			continue
		}
		// A bare account ID or the account root ARN both mean "any principal
		// in that account", which within one account is the caller.
		if raw == account || raw == fmt.Sprintf("arn:aws:iam::%s:root", account) {
			return true
		}
	}
	for _, provider := range set.Federated {
		if containsString(req.Federated, strings.TrimSpace(provider)) {
			return true
		}
	}
	// Service principals never match an IAM user or assumed-role caller.
	return false
}
