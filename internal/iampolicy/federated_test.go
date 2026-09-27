package iampolicy

import "testing"

// oidcTrustPolicy trusts one OIDC provider's tokens for one subject.
const oidcTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
	"Principal":{"Federated":"arn:aws:iam::000000000000:oidc-provider/oidc.example.com"},
	"Action":"sts:AssumeRoleWithWebIdentity",
	"Condition":{"StringEquals":{"oidc.example.com:sub":"ci"}}}]}`

func compilePolicy(t *testing.T, doc string) []Statement {
	t.Helper()
	stmts, err := ParseDocument(doc, SourceRef{ID: "trust-policy", Type: SourceTypeResourcePolicy})
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	return stmts
}

func webIdentityRequest(provider, sub string) Request {
	return Request{
		Action:    "sts:AssumeRoleWithWebIdentity",
		Resource:  "arn:aws:iam::000000000000:role/ci",
		Context:   map[string]string{"oidc.example.com:sub": sub},
		Federated: []string{provider},
	}
}

func TestEvaluate_federatedPrincipal_callerOfThatProvider(t *testing.T) {
	// Given: a trust policy naming an OIDC provider
	trust := compilePolicy(t, oidcTrustPolicy)

	// When: a caller of that provider, with the subject it names, assumes the role
	res := Evaluate(Input{
		Request:        webIdentityRequest("arn:aws:iam::000000000000:oidc-provider/oidc.example.com", "ci"),
		ResourcePolicy: trust,
	})

	// Then: the trust policy allows it, and nothing is reported unsupported
	if res.Decision != DecisionAllowed || len(res.Unsupported) != 0 {
		t.Fatalf("Decision = %q, Unsupported = %v; want allowed with nothing unsupported", res.Decision, res.Unsupported)
	}
}

func TestEvaluate_federatedPrincipal_otherProviderSubjectOrIAMCaller(t *testing.T) {
	trust := compilePolicy(t, oidcTrustPolicy)
	cases := map[string]Request{
		"another provider": webIdentityRequest("arn:aws:iam::000000000000:oidc-provider/other.example.com", "ci"),
		"another subject":  webIdentityRequest("arn:aws:iam::000000000000:oidc-provider/oidc.example.com", "dev"),
		"an IAM user": {
			Action: "sts:AssumeRoleWithWebIdentity", Resource: "arn:aws:iam::000000000000:role/ci",
			PrincipalARN: "arn:aws:iam::000000000000:user/carol", PrincipalAccount: "000000000000",
		},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			// Given: a trust policy naming one OIDC provider and subject
			// When: a caller it does not name assumes the role
			res := Evaluate(Input{Request: req, ResourcePolicy: trust})

			// Then: the default implicit deny applies
			if res.Decision != DecisionImplicitDeny {
				t.Fatalf("Decision = %q, want %q", res.Decision, DecisionImplicitDeny)
			}
		})
	}
}

func TestEvaluate_federatedPrincipal_iamCallerInAResourcePolicy(t *testing.T) {
	carol := Request{
		Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k",
		PrincipalARN: "arn:aws:iam::000000000000:user/carol", PrincipalAccount: "000000000000",
	}
	identity := compilePolicy(t, `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	cases := []struct {
		name   string
		policy string
		want   Decision
	}{
		// A Deny naming a provider does not apply to an IAM user.
		{"Deny to a Federated principal", `{"Statement":[{"Effect":"Deny","Principal":{"Federated":"accounts.google.com"},"Action":"s3:*","Resource":"*"}]}`, DecisionAllowed},
		// Everyone but the provider's callers, carol included, is denied.
		{"Deny to everyone but a Federated principal", `{"Statement":[{"Effect":"Deny","NotPrincipal":{"Federated":"accounts.google.com"},"Action":"s3:*","Resource":"*"}]}`, DecisionExplicitDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an IAM user allowed S3, and a bucket policy naming a provider
			// When: she reads an object
			res := Evaluate(Input{Request: carol, Identity: identity, ResourcePolicy: compilePolicy(t, tc.policy)})

			// Then: the Federated principal is matched, not reported unsupported
			if res.Decision != tc.want || len(res.Unsupported) != 0 {
				t.Fatalf("Decision = %q, Unsupported = %v; want %q with nothing unsupported", res.Decision, res.Unsupported, tc.want)
			}
		})
	}
}
