package iampolicy

import "strings"

// PolicyType names the kind of policy an access-denied message blames, in the
// words AWS's messages use for it.
type PolicyType string

const (
	PolicyTypeIdentityBased       PolicyType = "identity-based policy"
	PolicyTypeResourceBased       PolicyType = "resource-based policy"
	PolicyTypePermissionsBoundary PolicyType = "permissions boundary"
)

// Denial is what AWS tells a caller whose request a policy refused: who asked,
// for what, on what, and which kind of policy said no.
type Denial struct {
	// Principal is the caller's ARN.
	Principal string
	// Action is the IAM action, "<prefix>:<Op>".
	Action string
	// Resource is the ARN the action was authorized against. "" and "*" name
	// no particular resource and are left out of the message, as AWS leaves
	// them out of its own.
	Resource string
	// QuoteResource wraps the resource in double quotes, as S3 does.
	QuoteResource bool
	// Policy is the kind of policy that denied the request, and Explicit
	// whether it did so with a Deny statement rather than by allowing nothing.
	Policy   PolicyType
	Explicit bool
}

// Message renders the denial in the format the IAM User Guide documents
// ("Access denied error message examples", troubleshoot_access-denied.html):
//
//	User: <principal> is not authorized to perform: <action> on resource: <resource> because no <policy> allows the <action> action
//	User: <principal> is not authorized to perform: <action> on resource: <resource> with an explicit deny in a <policy>
func (d Denial) Message() string {
	var b strings.Builder
	b.WriteString("User: ")
	b.WriteString(d.Principal)
	b.WriteString(" is not authorized to perform: ")
	b.WriteString(d.Action)
	if d.Resource != "" && d.Resource != "*" {
		b.WriteString(" on resource: ")
		if d.QuoteResource {
			b.WriteString(`"` + d.Resource + `"`)
		} else {
			b.WriteString(d.Resource)
		}
	}
	if d.Explicit {
		b.WriteString(" with an explicit deny in ")
		b.WriteString(indefiniteArticle(string(d.Policy)))
		b.WriteString(" ")
		b.WriteString(string(d.Policy))
		return b.String()
	}
	b.WriteString(" because no ")
	b.WriteString(string(d.Policy))
	b.WriteString(" allows the ")
	b.WriteString(d.Action)
	b.WriteString(" action")
	return b.String()
}

// indefiniteArticle is "an" before a vowel and "a" otherwise, which is all the
// policy type names above need.
func indefiniteArticle(word string) string {
	if word != "" && strings.ContainsRune("aeiou", rune(word[0])) {
		return "an"
	}
	return "a"
}

// DeniedBy names the kind of policy that denied a request r did not allow, and
// whether it denied it explicitly. AWS names one policy type even when several
// denied, so this names the one a developer has to change first:
//
//   - an explicit deny blames the policy whose Deny statement matched, and the
//     permissions boundary when no identity or resource statement did;
//   - an implicit deny blames the permissions boundary only when an identity
//     statement allowed the action and the boundary did not, because widening
//     the identity policies is otherwise the fix.
//
// It is meaningless for an allowed result.
func (r Result) DeniedBy() (PolicyType, bool) {
	if r.Decision == DecisionExplicitDeny {
		if m, ok := r.firstMatch("Deny"); ok {
			return matchPolicyType(m), true
		}
		return PolicyTypePermissionsBoundary, true
	}
	if r.BoundaryApplied && !r.AllowedByBoundary {
		if _, ok := r.firstMatch("Allow"); ok {
			return PolicyTypePermissionsBoundary, false
		}
	}
	return PolicyTypeIdentityBased, false
}

// firstMatch returns the first matched statement with the given effect.
func (r Result) firstMatch(effect string) (Match, bool) {
	for _, m := range r.Matched {
		if m.Effect == effect {
			return m, true
		}
	}
	return Match{}, false
}

// matchPolicyType is the kind of policy a matched statement came from.
func matchPolicyType(m Match) PolicyType {
	if m.Source.Type == SourceTypeResourcePolicy {
		return PolicyTypeResourceBased
	}
	return PolicyTypeIdentityBased
}
