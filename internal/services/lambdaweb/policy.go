package lambdaweb

// Resource-based policy validation. Per AWS docs
// (https://docs.aws.amazon.com/lambda/latest/dg/web-functions-permissions.md,
// "Public policies are not allowed"), a policy that grants access to an
// unspecified set of principals is rejected with a ValidationException. Lambda
// assumes a policy is public and then looks for what makes it not: fully
// specified principals, or a trusted condition key constrained to a fixed
// value.

import (
	"encoding/json"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// trustedConditionKeys identify the authenticated caller and can't be set by
// the requester; naming one with a fixed value makes a wildcard principal safe.
var trustedConditionKeys = map[string]bool{
	"aws:principalaccount": true, "aws:principalarn": true, "aws:principalorgid": true,
	"aws:principalorgpaths": true, "aws:sourceaccount": true, "aws:sourcearn": true,
	"aws:sourceorgid": true, "aws:sourceorgpaths": true, "aws:sourcevpc": true,
	"aws:sourcevpcarn": true, "aws:sourcevpce": true, "aws:vpceaccount": true,
	"aws:vpceorgid": true, "aws:vpceorgpaths": true, "aws:ec2instancesourcevpc": true,
	"aws:userid": true, "aws:principalservicename": true, "aws:principalservicenameslist": true,
	"aws:principalisawsservice": true, "lambda:sourcefunctionarn": true,
}

type policyStatement struct {
	Effect    string `json:"Effect"`
	Principal any    `json:"Principal"`
	Condition any    `json:"Condition"`
}

func validatePolicy(doc string) *protocol.AWSError {
	var parsed struct {
		Statement json.RawMessage `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(doc), &parsed); err != nil || len(parsed.Statement) == 0 {
		return validationError("The policy is not a valid JSON policy document.")
	}
	var stmts []policyStatement
	if err := json.Unmarshal(parsed.Statement, &stmts); err != nil {
		var one policyStatement
		if err := json.Unmarshal(parsed.Statement, &one); err != nil {
			return validationError("The policy is not a valid JSON policy document.")
		}
		stmts = []policyStatement{one}
	}
	if len(stmts) == 0 {
		return validationError("The policy must have at least one statement.")
	}
	for _, st := range stmts {
		if st.Effect != "Allow" && st.Effect != "Deny" {
			return validationError("Each policy statement needs an Effect of Allow or Deny.")
		}
		if st.Effect == "Allow" && serviceutil.PrincipalIsWildcard(st.Principal) && !hasTrustedCondition(st.Condition) {
			return validationError("The policy is public: it grants access to an unspecified set of principals. " +
				"Name the principals, or constrain a trusted condition key such as aws:PrincipalOrgID.")
		}
	}
	return nil
}

// hasTrustedCondition reports whether any operator of a Condition block pins a
// trusted key to a value without wildcards.
func hasTrustedCondition(cond any) bool {
	ops, ok := cond.(map[string]any)
	if !ok {
		return false
	}
	for _, keys := range ops {
		km, ok := keys.(map[string]any)
		if !ok {
			continue
		}
		for key, val := range km {
			if trustedConditionKeys[strings.ToLower(key)] && fixedValue(val) {
				return true
			}
		}
	}
	return false
}

func fixedValue(v any) bool {
	switch t := v.(type) {
	case string:
		return t != "" && !strings.ContainsAny(t, "*?$")
	case bool:
		return true
	case []any:
		if len(t) == 0 {
			return false
		}
		for _, item := range t {
			if !fixedValue(item) {
				return false
			}
		}
		return true
	}
	return false
}
