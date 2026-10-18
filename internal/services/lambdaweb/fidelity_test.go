package lambdaweb

import (
	"strings"
	"testing"
)

func TestValidateRevision_onlyNodejs24Runs(t *testing.T) {
	cases := []struct {
		runtime string
		ok      bool
	}{
		{"nodejs24.x", true},
		{"nodejs22.x", false},
		{"nodejs20.x", false},
		{"python3.13", false},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			// Given: a revision on the runtime
			rc := testRevisionConfig("code", "app.zip")
			rc.BuildConfig.RuntimeConfig.Runtime = tc.runtime

			// When / Then: only the one runtime AWS lists is accepted
			aerr := validateRevision(rc.BuildConfig, rc.Service, "", "")
			if tc.ok != (aerr == nil) {
				t.Errorf("validateRevision(%s) = %v, want ok=%v", tc.runtime, aerr, tc.ok)
			}
		})
	}
}

func TestValidateRevision_environmentVariablesAreLimitedTo32KB(t *testing.T) {
	// Given: variables just under, and just over, 32 KB in aggregate
	under := map[string]string{"BIG": strings.Repeat("x", maxEnvVarBytes-len("BIG"))}
	over := map[string]string{"BIG": strings.Repeat("x", maxEnvVarBytes-len("BIG")+1)}
	for name, env := range map[string]map[string]string{"under": under, "over": over} {
		rc := testRevisionConfig("code", "app.zip")
		rc.Service.EnvironmentVariables = env

		// When / Then: the limit is on names plus values together
		aerr := validateRevision(rc.BuildConfig, rc.Service, "", "")
		if (name == "under") != (aerr == nil) {
			t.Errorf("%s: validateRevision = %v", name, aerr)
		}
	}
}

func TestValidatePolicy_publicOrNot(t *testing.T) {
	stmt := func(principal, cond string) string {
		c := ""
		if cond != "" {
			c = `,"Condition":` + cond
		}
		return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":` + principal +
			`,"Action":"lambda:InvokeWebFunctionEndpoint","Resource":"*"` + c + `}]}`
	}
	cases := []struct {
		name   string
		policy string
		ok     bool
	}{
		{"named service principal", stmt(`{"Service":"cloudfront.amazonaws.com"}`, ""), true},
		{"bare wildcard", stmt(`"*"`, ""), false},
		{"wildcard pinned to a fixed account", stmt(`"*"`, `{"StringEquals":{"aws:PrincipalAccount":"111122223333"}}`), true},
		{"wildcard pinned to a distribution", stmt(`"*"`, `{"ArnEquals":{"aws:SourceArn":"arn:aws:cloudfront::111122223333:distribution/E123"}}`), true},
		{"wildcard in the resource part of a source ARN", stmt(`"*"`, `{"ArnLike":{"aws:SourceArn":"arn:aws:cloudfront::111122223333:distribution/*"}}`), true},
		{"wildcard in the account of a source ARN", stmt(`"*"`, `{"ArnLike":{"aws:SourceArn":"arn:aws:cloudfront::*:distribution/*"}}`), false},
		{"wildcard in a plain value", stmt(`"*"`, `{"StringLike":{"aws:PrincipalAccount":"1111*"}}`), false},
		{"negated operator pins nothing", stmt(`"*"`, `{"StringNotEquals":{"aws:PrincipalAccount":"111122223333"}}`), false},
		{"IfExists passes when the key is absent", stmt(`"*"`, `{"StringEqualsIfExists":{"aws:PrincipalAccount":"111122223333"}}`), false},
		{"wildcard inside a principal ARN", stmt(`{"AWS":"arn:aws:iam::*:root"}`, ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the policy is validated
			aerr := validatePolicy(tc.policy)

			// Then: it is accepted or refused as AWS would
			if tc.ok != (aerr == nil) {
				t.Errorf("validatePolicy = %v, want ok=%v", aerr, tc.ok)
			}
		})
	}
}
