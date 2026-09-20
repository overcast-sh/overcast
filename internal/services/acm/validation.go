package acm

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// domainNameConstraint is the pattern AWS pins on DomainNameString — the shape
// behind RequestCertificate's DomainName, every SubjectAlternativeNames entry
// and DomainValidation.DomainName (acm-2015-12-08.json, `smithy.api#pattern`).
// It is kept verbatim because AWS echoes the pattern itself back in the
// constraint-violation message, so the bytes a caller sees have to be the
// model's, not a paraphrase.
const domainNameConstraint = `^(\*\.)?(((?!-)[A-Za-z0-9-]{0,62}[A-Za-z0-9])\.)+((?!-)[A-Za-z0-9-]{1,62}[A-Za-z0-9])$`

// maxDomainNameLen is DomainNameString's `smithy.api#length` maximum.
const maxDomainNameLen = 253

// domainNamePattern accepts exactly the language domainNameConstraint
// describes, rewritten without the negative lookaheads Go's RE2 engine does
// not implement. `(?!-)[A-Za-z0-9-]{0,62}[A-Za-z0-9]` is "1 to 63 characters,
// first not a hyphen, last alphanumeric", which is what the repeated group
// below spells out; the final label is the same with a minimum of 2. Keeping
// the two in one file is deliberate: a model refresh that changes the pattern
// must change both, and the unit table in validation_test.go is what proves
// they still agree.
var domainNamePattern = regexp.MustCompile(
	`^(\*\.)?([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z0-9][A-Za-z0-9-]{0,61}[A-Za-z0-9]$`)

// validationMethods is ACM's ValidationMethod enum, in model order — the order
// AWS's constraint message prints them in.
var validationMethods = []string{"EMAIL", "DNS", "HTTP"}

// certificateStatuses is ACM's CertificateStatus enum, in model order.
var certificateStatuses = []string{
	"PENDING_VALIDATION", "ISSUED", "INACTIVE", "EXPIRED",
	"VALIDATION_TIMED_OUT", "REVOKED", "FAILED",
}

// errValidation is the answer AWS's front-end constraint validator gives for a
// member that breaks a `length`, `pattern` or `enum` trait: ValidationException
// ("The supplied input failed to satisfy constraints of an Amazon Web Services
// service"), HTTP 400.
//
// ACM's own model does not list ValidationException among RequestCertificate's
// errors — it lists it on nearly every other operation, ListCertificates
// included — but that list covers the errors the *service* raises, not the
// ones the framework raises before the request ever reaches it. Throttling is
// missing from the same list for the same reason. The modeled
// InvalidParameterException is the service's own "an input parameter was
// invalid", raised after dispatch (Overcast uses it for tag violations, which
// is where ACM raises it); InvalidDomainValidationOptionsException is narrower
// still — it is about the DomainValidationOptions structure, not about a
// domain that is not a domain.
func errValidation(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ValidationException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// constraintViolation builds AWS's constraint-violation wording. The member
// path is spelled the way the validator spells it: lowerCamelCase, and
// `<list>.<1-based index>.member` for an entry of a list.
func constraintViolation(value, member, bound string) string {
	return fmt.Sprintf("1 validation error detected: Value '%s' at '%s' failed to satisfy constraint: %s",
		value, member, bound)
}

// enumConstraint is the bound AWS prints for a value outside an enum.
func enumConstraint(allowed []string) string {
	return "Member must satisfy enum value set: [" + strings.Join(allowed, ", ") + "]"
}

// validateDomainName enforces DomainNameString's length and pattern traits.
// member is the request path the value arrived on, so the message points at
// the offending entry rather than at "a domain name somewhere".
func validateDomainName(value, member string) *protocol.AWSError {
	return serviceutil.ResourceName(value, serviceutil.NameRule{
		MinLength:  1,
		MaxLength:  maxDomainNameLen,
		Pattern:    domainNamePattern,
		ErrorCode:  "ValidationException",
		HTTPStatus: http.StatusBadRequest,
		LengthMessage: constraintViolation(value, member, fmt.Sprintf(
			"Member must have length greater than or equal to 1, Member must have length less than or equal to %d",
			maxDomainNameLen)),
		PatternMessage: constraintViolation(value, member,
			"Member must satisfy regular expression pattern: "+domainNameConstraint),
	})
}

// validateEnum enforces one of ACM's string enums.
func validateEnum(value, member string, allowed []string) *protocol.AWSError {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return errValidation(constraintViolation(value, member, enumConstraint(allowed)))
}
