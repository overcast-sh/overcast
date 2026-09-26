package lambda

// authorize_invoke.go — the one resource-policy decision every service-originated
// invocation goes through.
//
// AWS lets another service invoke a function only when the function's
// resource-based policy says so: an S3 notification needs a statement naming
// `s3.amazonaws.com`, an API Gateway integration one naming
// `apigateway.amazonaws.com`, and so on. Overcast stores those statements
// (AddPermission, PutResourcePolicy) and, with
// OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY set, consults them here before a
// caller delivers. The knob is off by default because Overcast is not a
// security boundary — see AGENTS.md § Non-goals — so a stack that never called
// AddPermission keeps working.
//
// Direct client Invoke calls are deliberately *not* gated. They are
// credential-stubbed, so there is no caller identity to authorise; only an
// invocation Overcast itself originates on a service's behalf has a principal
// worth checking.
//
// https://docs.aws.amazon.com/lambda/latest/dg/access-control-resource-based.html
// https://docs.aws.amazon.com/lambda/latest/dg/lambda-api-permissions-ref.html

import (
	"context"
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/middleware"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// invokeFunctionAction is the action a service-originated invocation needs.
const invokeFunctionAction = "lambda:InvokeFunction"

// AuthorizeServiceInvoke reports whether principal may invoke the function
// functionRef names, according to that function's resource-based policy.
// functionRef is a bare name or a function ARN, qualified or not; sourceARN and
// sourceAccount are the `aws:SourceArn` and `aws:SourceAccount` condition-key
// values the calling service contributes, either of which may be empty.
//
// A nil return means the invocation is authorised. It is also what an unset
// OVERCAST_ENFORCE_LAMBDA_RESOURCE_POLICY returns, before any work at all: with
// the knob off this costs one field read, no store access and no allocation.
//
// Satisfies events.FunctionInvokeAuthorizer. Callers translate a refusal into
// whatever their own service does when a permission is missing — S3 refuses the
// notification configuration, SNS dead-letters the delivery, API Gateway
// answers 500 — rather than surfacing this error verbatim.
func (s *Service) AuthorizeServiceInvoke(ctx context.Context, functionRef, principal, sourceARN, sourceAccount string) *protocol.AWSError {
	if s == nil || s.cfg == nil || !s.cfg.EnforceLambdaResourcePolicy {
		return nil
	}

	name, qualifier := splitFunctionIdentifier(functionRef, "")
	// An unqualified invoke resolves to $LATEST, and Lambda refuses to store a
	// policy against the qualifier $LATEST, so both spellings read the policy
	// held against the function itself.
	if qualifier == "$LATEST" {
		qualifier = ""
	}
	// Read the policy in the function's own region, not the caller's: a
	// notification may be dispatched from a goroutine carrying whatever region
	// the originating request ran in.
	if region := regionFromFunctionARN(functionRef); region != "" {
		ctx = middleware.ContextWithRegion(ctx, region)
	}
	targetARN := s.invokeTargetARN(ctx, functionRef, name, qualifier)

	policy, found, aerr := s.ls.getFunctionPolicy(ctx, name, qualifier)
	if aerr != nil {
		return aerr
	}
	if !found {
		return invokeAccessDenied(principal, targetARN)
	}
	if invokePolicyVerdict(policy.Statements, principal, targetARN, sourceARN, sourceAccount) != verdictAllow {
		return invokeAccessDenied(principal, targetARN)
	}
	return nil
}

// invokeTargetARN renders the function ARN the decision is made against: the
// ARN the caller itself named, or the one this account and region would mint
// for a bare function name.
func (s *Service) invokeTargetARN(ctx context.Context, functionRef, name, qualifier string) string {
	if marker := strings.Index(functionRef, ":function:"); strings.HasPrefix(functionRef, "arn:") && marker >= 0 {
		return policyResourceARN(functionRef[:marker+len(":function:")]+name, qualifier)
	}
	return policyResourceARN(
		protocol.ARN(middleware.RegionFromContext(ctx, s.cfg.Region), s.cfg.AccountID, "lambda", "function:"+name),
		qualifier)
}

// verdict is the outcome of evaluating a policy document.
type verdict int

const (
	// verdictImplicitDeny is the absence of any matching Allow — AWS's default.
	verdictImplicitDeny verdict = iota
	// verdictAllow is at least one matching Allow and no matching Deny.
	verdictAllow
	// verdictExplicitDeny is a matching Deny, which beats every Allow.
	verdictExplicitDeny
)

// invokePolicyVerdict evaluates a resource policy for one invocation. It is one
// pass over the statements — an explicit Deny wins wherever it appears, so the
// whole document is read before an Allow is honoured.
//
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic.html
func invokePolicyVerdict(statements []permissionStatement, principal, targetARN, sourceARN, sourceAccount string) verdict {
	result := verdictImplicitDeny
	for _, statement := range statements {
		if !statementMatchesInvoke(statement, principal, targetARN, sourceARN, sourceAccount) {
			continue
		}
		if strings.EqualFold(statement.Effect, "Deny") {
			return verdictExplicitDeny
		}
		if strings.EqualFold(statement.Effect, "Allow") {
			result = verdictAllow
		}
	}
	return result
}

// statementMatchesInvoke reports whether one statement's Action, Principal,
// Resource and Condition elements all apply to this invocation. Effect is the
// caller's business.
func statementMatchesInvoke(statement permissionStatement, principal, targetARN, sourceARN, sourceAccount string) bool {
	return actionAllowsInvoke(statement.Action) &&
		servicePrincipalMatches(statement.Principal, principal) &&
		resourceMatchesARN(statement.Resource, targetARN) &&
		conditionsSatisfied(statement.Condition, sourceARN, sourceAccount)
}

// actionAllowsInvoke reports whether an Action element covers
// lambda:InvokeFunction. IAM action names are case-insensitive and may carry
// wildcards, so "lambda:*", "Lambda:Invoke*" and "*" all qualify.
func actionAllowsInvoke(action any) bool {
	want := strings.ToLower(invokeFunctionAction)
	for _, candidate := range policyStrings(action) {
		if wildcardMatch(strings.ToLower(candidate), want) {
			return true
		}
	}
	return false
}

// servicePrincipalMatches reports whether a Principal element names the AWS
// service principal making this call.
//
// A bare "*" grants everyone, services included. {"AWS": …} does not: that
// element names accounts and IAM principals, and a service principal is
// neither — which is why an AddPermission statement for an account cannot
// stand in for one naming a service.
func servicePrincipalMatches(principal any, want string) bool {
	switch value := principal.(type) {
	case string:
		return value == "*"
	case []any:
		for _, item := range value {
			if servicePrincipalMatches(item, want) {
				return true
			}
		}
		return false
	}
	for key, entry := range policyMap(principal) {
		if !strings.EqualFold(key, "Service") {
			continue
		}
		for _, candidate := range policyStrings(entry) {
			if strings.EqualFold(candidate, want) {
				return true
			}
		}
	}
	return false
}

// resourceMatchesARN reports whether a Resource element covers the function
// being invoked, honouring the wildcards AWS documents for a Lambda resource
// policy: an unqualified ARN matches only an unqualified invoke, ":*" matches
// any qualified invoke but not the unqualified one, and a trailing "*" matches
// both.
//
// An absent Resource matches. A resource-based policy is already attached to
// exactly one function, version or alias, so a statement that names no resource
// can only mean that one.
//
// https://docs.aws.amazon.com/lambda/latest/dg/lambda-api-permissions-ref.html
func resourceMatchesARN(resource any, targetARN string) bool {
	values := policyStrings(resource)
	if len(values) == 0 {
		return true
	}
	for _, candidate := range values {
		if wildcardMatch(candidate, targetARN) {
			return true
		}
	}
	return false
}

// conditionsSatisfied evaluates a statement's Condition element against the two
// context keys a service-originated invocation carries.
//
// Every operator and every key must hold; the values inside one key are an OR.
// A key the request does not supply fails the condition, as it does on AWS. So
// does a key or operator Overcast cannot evaluate — an emulator that ignored an
// unknown condition would authorise an invocation AWS refuses, and the
// divergence that says yes here and no in an AWS account is the expensive one.
//
// https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html
func conditionsSatisfied(condition any, sourceARN, sourceAccount string) bool {
	for operator, keys := range policyMap(condition) {
		for key, values := range policyMap(keys) {
			var actual string
			switch strings.ToLower(key) {
			case "aws:sourcearn":
				actual = sourceARN
			case "aws:sourceaccount":
				actual = sourceAccount
			default:
				return false
			}
			if actual == "" || !conditionValueMatches(operator, policyStrings(values), actual) {
				return false
			}
		}
	}
	return true
}

// conditionValueMatches applies one condition operator to the request's value.
// ArnEquals and ArnLike behave identically on AWS — both wildcard-aware — and
// AddPermission's own SourceArn is documented as a StringLike comparison, so
// the three wildcard operators are grouped together and StringEquals alone is
// exact.
func conditionValueMatches(operator string, patterns []string, actual string) bool {
	for _, pattern := range patterns {
		switch {
		case strings.EqualFold(operator, "StringEquals"):
			if pattern == actual {
				return true
			}
		case strings.EqualFold(operator, "ArnLike"),
			strings.EqualFold(operator, "ArnEquals"),
			strings.EqualFold(operator, "StringLike"):
			if wildcardMatch(pattern, actual) {
				return true
			}
		default:
			return false
		}
	}
	return false
}

// policyStrings flattens a policy element JSON may spell as one string or a
// list of them. The map cases are the in-memory shapes AddPermission builds,
// which a unit test may hand over without a store round trip.
func policyStrings(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, policyStrings(item)...)
		}
		return out
	}
	return nil
}

// policyMap normalises the object shapes a policy element arrives in: decoded
// from the store it is always map[string]any, but a statement built in memory
// carries the concrete maps AddPermission renders.
func policyMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case map[string]map[string]string:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	}
	return nil
}

// wildcardMatch reports whether value matches an IAM pattern, in which "*"
// stands for any run of characters and "?" for exactly one. It backtracks over
// the last star rather than recursing, so it allocates nothing and stays linear
// in the common case.
func wildcardMatch(pattern, value string) bool {
	patternIdx, valueIdx := 0, 0
	star, resume := -1, 0
	for valueIdx < len(value) {
		switch {
		case patternIdx < len(pattern) && (pattern[patternIdx] == '?' || pattern[patternIdx] == value[valueIdx]):
			patternIdx++
			valueIdx++
		case patternIdx < len(pattern) && pattern[patternIdx] == '*':
			star = patternIdx
			patternIdx++
			resume = valueIdx
		case star >= 0:
			patternIdx = star + 1
			resume++
			valueIdx = resume
		default:
			return false
		}
	}
	for patternIdx < len(pattern) && pattern[patternIdx] == '*' {
		patternIdx++
	}
	return patternIdx == len(pattern)
}

// invokeAccessDenied is what Lambda's Invoke answers a caller whose request no
// resource-based policy allows.
func invokeAccessDenied(principal, targetARN string) *protocol.AWSError {
	return &protocol.AWSError{
		Code: "AccessDeniedException",
		Message: iampolicy.Denial{
			Principal: principal,
			Action:    invokeFunctionAction,
			Resource:  targetARN,
			Policy:    iampolicy.PolicyTypeResourceBased,
		}.Message(),
		HTTPStatus: http.StatusForbidden,
	}
}
