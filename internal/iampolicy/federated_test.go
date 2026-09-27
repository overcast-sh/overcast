package iampolicy

import "testing"

// oidcTrustPolicy trusts one OIDC provider's tokens for one subject.
const oidcTrustPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
	"Principal":{"Federated":"arn:aws:iam::000000000000:oidc-provider/oidc.example.com"},
	"Action":"sts:AssumeRoleWithWebIdentity",
	"Condition":{"StringEquals":{"oidc.example.com:sub":"ci"}}}]}`

func compileTrustPolicy(t *testing.T, doc string) []Statement {
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

func TestEvaluate_federatedPrincipal_matchesACallerOfThatProvider(t *testing.T) {
	// Given: a trust policy naming an OIDC provider
	trust := compileTrustPolicy(t, oidcTrustPolicy)

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

func TestEvaluate_federatedPrincipal_refusesAnotherProviderOrSubject(t *testing.T) {
	trust := compileTrustPolicy(t, oidcTrustPolicy)
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
