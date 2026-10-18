package iampolicy

import (
	"strings"
	"testing"
)

// identityFrom parses raw documents as identity policies, failing the test on
// any parse error.
func identityFrom(t *testing.T, raws ...string) []Statement {
	t.Helper()
	docs := make([]SourcedDocument, 0, len(raws))
	for i, raw := range raws {
		docs = append(docs, SourcedDocument{
			Source:   SourceRef{ID: "policy-" + string(rune('a'+i)), Type: SourceTypeIAMPolicy},
			Document: raw,
		})
	}
	stmts, err := Compile(docs)
	if err != nil {
		t.Fatalf("Compile: unexpected error: %v", err)
	}
	return stmts
}

func TestEvaluate_noStatements_isImplicitDeny(t *testing.T) {
	// Given: a principal with no policies at all
	// When: an action is evaluated
	res := Evaluate(Input{Request: Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}})

	// Then: AWS's default applies — implicit deny, nothing matched
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
	if len(res.Matched) != 0 {
		t.Fatalf("Matched = %#v, want none", res.Matched)
	}
}

func TestEvaluate_matchingAllow_isAllowed(t *testing.T) {
	// Given: an identity policy allowing s3:GetObject on one bucket
	stmts := identityFrom(t, `{"Version":"2012-10-17","Statement":[
		{"Sid":"AllowRead","Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`)

	// When: a matching request is evaluated
	res := Evaluate(Input{
		Request:  Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/nested/key.txt"},
		Identity: stmts,
	})

	// Then: it is allowed and the matching statement is reported
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}
	if len(res.Matched) != 1 || res.Matched[0].Sid != "AllowRead" {
		t.Fatalf("Matched = %#v, want the AllowRead statement", res.Matched)
	}
}

func TestEvaluate_actionCaseInsensitive_isAllowed(t *testing.T) {
	// Given: a policy whose action casing differs from the request's
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:getobject","Resource":"*"}]}`)

	// When: the request uses the canonical casing
	res := Evaluate(Input{Request: Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}, Identity: stmts})

	// Then: AWS matches actions case-insensitively
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}
}

func TestEvaluate_explicitDeny_overridesAllow(t *testing.T) {
	// Given: an allow and an explicit deny for the same action
	stmts := identityFrom(t,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
		`{"Statement":[{"Sid":"Blocked","Effect":"Deny","Action":"s3:DeleteObject","Resource":"*"}]}`)

	// When: the denied action is evaluated
	res := Evaluate(Input{Request: Request{Action: "s3:DeleteObject", Resource: "arn:aws:s3:::b/k"}, Identity: stmts})

	// Then: explicit deny wins
	if res.Decision != DecisionExplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionExplicitDeny)
	}
}

func TestEvaluate_resourceMismatch_isImplicitDeny(t *testing.T) {
	// Given: an allow scoped to one bucket
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"arn:aws:s3:::allowed/*"}]}`)

	// When: another bucket is requested
	res := Evaluate(Input{Request: Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::other/k"}, Identity: stmts})

	// Then: nothing matched, so the default deny applies
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
}

func TestEvaluate_notAction_allowsUnlistedAction(t *testing.T) {
	// Given: an allow on everything except iam:*
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","NotAction":"iam:*","Resource":"*"}]}`)

	// When: a non-excluded action is evaluated
	res := Evaluate(Input{Request: Request{Action: "sqs:SendMessage", Resource: "*"}, Identity: stmts})

	// Then: it is allowed
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}

	// When: the excluded action is evaluated
	res = Evaluate(Input{Request: Request{Action: "iam:CreateUser", Resource: "*"}, Identity: stmts})

	// Then: the statement does not match, so implicit deny applies
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
}

func TestEvaluate_notResource_excludesListedResource(t *testing.T) {
	// Given: an allow on every resource except one bucket
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:*","NotResource":"arn:aws:s3:::secret/*"}]}`)

	// When: the excluded resource is requested
	res := Evaluate(Input{Request: Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::secret/k"}, Identity: stmts})

	// Then: the statement does not match
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
}

func TestEvaluate_conditionMet_isAllowed(t *testing.T) {
	// Given: an allow conditional on the requested region
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*",
		"Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"}}}]}`)

	// When: the request is in that region
	res := Evaluate(Input{
		Request:  Request{Action: "sqs:SendMessage", Resource: "*", Context: map[string]string{"aws:requestedregion": "eu-west-1"}},
		Identity: stmts,
	})

	// Then: it is allowed
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}
}

func TestEvaluate_conditionKeyMissing_reportsMissingContextKey(t *testing.T) {
	// Given: an allow conditional on a key the request does not carry
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*",
		"Condition":{"StringEquals":{"aws:SourceVpc":"vpc-1234"}}}]}`)

	// When: the request is evaluated without that key
	res := Evaluate(Input{Request: Request{Action: "sqs:SendMessage", Resource: "*"}, Identity: stmts})

	// Then: the decision is implicit deny and the missing key is reported,
	// not silently swallowed
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
	if len(res.MissingContextKeys) != 1 || res.MissingContextKeys[0] != "aws:SourceVpc" {
		t.Fatalf("MissingContextKeys = %#v, want [aws:SourceVpc]", res.MissingContextKeys)
	}
}

func TestEvaluate_unknownConditionOperator_reportsUnsupported(t *testing.T) {
	// Given: a statement using a condition operator the evaluator does not know
	stmts := identityFrom(t, `{"Statement":[{"Sid":"Weird","Effect":"Allow","Action":"sqs:*","Resource":"*",
		"Condition":{"StringPalindrome":{"aws:RequestedRegion":"eu-west-1"}}}]}`)

	// When: the request is evaluated
	res := Evaluate(Input{
		Request:  Request{Action: "sqs:SendMessage", Resource: "*", Context: map[string]string{"aws:requestedregion": "eu-west-1"}},
		Identity: stmts,
	})

	// Then: the construct is reported explicitly rather than being resolved to
	// an allow or a deny
	if len(res.Unsupported) == 0 {
		t.Fatalf("Unsupported = %#v, want the unknown operator reported", res.Unsupported)
	}
	if !strings.Contains(res.Unsupported[0], "StringPalindrome") {
		t.Fatalf("Unsupported[0] = %q, want it to name the operator", res.Unsupported[0])
	}
	if res.Decision == DecisionAllowed {
		t.Fatalf("Decision = %q, want the unevaluable statement not to grant an allow", res.Decision)
	}
}

func TestEvaluate_policyVariable_expandsFromContext(t *testing.T) {
	// Given: a home-directory policy using ${aws:username}
	stmts := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:GetObject",
		"Resource":"arn:aws:s3:::home/${aws:username}/*"}]}`)
	ctx := map[string]string{"aws:username": "carol"}

	// When: carol reads her own prefix
	res := Evaluate(Input{
		Request:  Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::home/carol/f.txt", Context: ctx},
		Identity: stmts,
	})
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}

	// When: carol reads someone else's prefix
	res = Evaluate(Input{
		Request:  Request{Action: "s3:GetObject", Resource: "arn:aws:s3:::home/mallory/f.txt", Context: ctx},
		Identity: stmts,
	})
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
}

func TestEvaluate_resourcePolicyAllow_isAllowedWithoutIdentityAllow(t *testing.T) {
	// Given: a bucket policy granting the caller access, and no identity policy
	resourceStmts, err := Compile([]SourcedDocument{{
		Source: SourceRef{ID: "bucket-policy", Type: SourceTypeResourcePolicy},
		Document: `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:user/carol"},
			"Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`,
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// When: carol reads the object
	res := Evaluate(Input{
		Request: Request{
			Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k",
			PrincipalARN: "arn:aws:iam::000000000000:user/carol", PrincipalAccount: "000000000000",
		},
		ResourcePolicy: resourceStmts,
	})

	// Then: within one account a resource-policy allow is sufficient
	if res.Decision != DecisionAllowed {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionAllowed)
	}
}

func TestEvaluate_resourcePolicyPrincipalMismatch_isImplicitDeny(t *testing.T) {
	// Given: a bucket policy granting a different principal
	resourceStmts, err := Compile([]SourcedDocument{{
		Source: SourceRef{ID: "bucket-policy", Type: SourceTypeResourcePolicy},
		Document: `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:user/dave"},
			"Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`,
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// When: carol reads the object
	res := Evaluate(Input{
		Request: Request{
			Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k",
			PrincipalARN: "arn:aws:iam::000000000000:user/carol", PrincipalAccount: "000000000000",
		},
		ResourcePolicy: resourceStmts,
	})

	// Then: the statement does not apply to her
	if res.Decision != DecisionImplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
	}
}

func TestEvaluate_resourcePolicyExplicitDeny_overridesIdentityAllow(t *testing.T) {
	// Given: an identity allow and a resource-policy deny
	identity := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	resourceStmts, err := Compile([]SourcedDocument{{
		Source:   SourceRef{ID: "bucket-policy", Type: SourceTypeResourcePolicy},
		Document: `{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::b/*"}]}`,
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// When: the object is read
	res := Evaluate(Input{
		Request: Request{
			Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k",
			PrincipalARN: "arn:aws:iam::000000000000:user/carol", PrincipalAccount: "000000000000",
		},
		Identity:       identity,
		ResourcePolicy: resourceStmts,
	})

	// Then: the deny wins wherever it comes from
	if res.Decision != DecisionExplicitDeny {
		t.Fatalf("Decision = %q, want %q", res.Decision, DecisionExplicitDeny)
	}
}

func TestCompile_malformedDocument_returnsError(t *testing.T) {
	// Given: a policy document that is not valid JSON
	// When: it is compiled
	_, err := Compile([]SourcedDocument{{Source: SourceRef{ID: "bad"}, Document: "{not json"}})

	// Then: the failure is reported rather than the document being skipped
	if err == nil {
		t.Fatal("Compile: expected an error for a malformed document")
	}
}

func TestCompile_statementWithActionAndNotAction_returnsError(t *testing.T) {
	// Given: a statement AWS itself rejects
	// When: it is compiled
	_, err := Compile([]SourcedDocument{{
		Source:   SourceRef{ID: "bad"},
		Document: `{"Statement":[{"Effect":"Allow","Action":"s3:*","NotAction":"s3:Delete*","Resource":"*"}]}`,
	}})

	// Then: it is rejected loudly
	if err == nil {
		t.Fatal("Compile: expected an error for Action + NotAction in one statement")
	}
}

func TestCompile_singleStatementObject_isAccepted(t *testing.T) {
	// Given: the single-object Statement form AWS also accepts
	stmts, err := Compile([]SourcedDocument{{
		Source:   SourceRef{ID: "p", Type: SourceTypeIAMPolicy},
		Document: `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Resource":"*"}}`,
	}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	// Then: one statement is compiled
	if len(stmts) != 1 {
		t.Fatalf("len(stmts) = %d, want 1", len(stmts))
	}
}

func TestEvaluate_servicePrincipal_matchesOnlyNamedService(t *testing.T) {
	// Given: a resource policy that allows cloudfront.amazonaws.com
	policy := identityFrom(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
		"Principal":{"Service":"cloudfront.amazonaws.com"},"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*"}]}`)
	req := Request{Action: "lambda:InvokeWebFunctionEndpoint", Resource: "arn:aws:lambda:us-east-1:111122223333:web-function/f/endpoint/e"}

	// When: the service, another service and an IAM user are evaluated
	req.ServicePrincipal = "cloudfront.amazonaws.com"
	named := Evaluate(Input{Request: req, ResourcePolicy: policy})
	req.ServicePrincipal = "apigateway.amazonaws.com"
	other := Evaluate(Input{Request: req, ResourcePolicy: policy})
	req.ServicePrincipal = ""
	req.PrincipalARN = "arn:aws:iam::111122223333:user/u"
	req.PrincipalAccount = "111122223333"
	user := Evaluate(Input{Request: req, ResourcePolicy: policy})

	// Then: only the named service is allowed
	if named.Decision != DecisionAllowed || other.Decision != DecisionImplicitDeny || user.Decision != DecisionImplicitDeny {
		t.Fatalf("decisions = %q/%q/%q, want allowed/implicitDeny/implicitDeny", named.Decision, other.Decision, user.Decision)
	}
}
