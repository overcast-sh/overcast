package middleware

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// iamUnattributedDeniedMessage is what a denial says when no policy decided
// it — an unsigned request, an access key that names no principal, or policies
// that could not be read or evaluated — so there is no principal or policy to
// name.
const iamUnattributedDeniedMessage = "User is not authorized to perform this action"

// ec2UnauthorizedMessage opens every EC2 denial; AWS appends the IAM sentence
// after it.
const ec2UnauthorizedMessage = "You are not authorized to perform this operation."

// queryDenialCodes holds the awsQuery services whose model names their own
// IAM denial code. Every other awsQuery service answers AccessDenied. SNS's
// is its AuthorizationErrorException (@awsQueryError AuthorizationError,
// 403); TestQueryDenialCodes_matchTheModels checks the entries against the
// pinned models, and that no other Query service needs one.
var queryDenialCodes = map[string]string{
	"sns": "AuthorizationError",
}

// writeIAMAccessDenied answers a denied request with the access-denied error
// of the wire protocol it is served over, in that protocol's envelope, so the
// caller's SDK reads the error code rather than failing to deserialize it
// (#2259). denial names the principal, action, resource and policy the
// message reports; it is nil when no policy decided the denial.
func writeIAMAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation, denial *iampolicy.Denial) {
	served := op.servedProtocol(r)
	serviceutil.WriteError(w, r, served.errors, iamAccessDeniedError(served, op.service, denial))
}

// iamAccessDeniedError is the access-denied error a caller of service, served
// over served.protocol, reads.
func iamAccessDeniedError(served servedProtocol, service string, denial *iampolicy.Denial) *protocol.AWSError {
	message := iamDeniedMessage(denial)
	switch served.protocol {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11, awsapi.ProtocolRPCV2CBOR, awsapi.ProtocolRPCV2JSON:
		return accessDeniedException(http.StatusBadRequest, message)
	case awsapi.ProtocolAWSQuery:
		return queryAccessDenied(service, message)
	case awsapi.ProtocolEC2Query:
		return &protocol.AWSError{
			Code:       "UnauthorizedOperation",
			Message:    ec2DeniedMessage(denial),
			HTTPStatus: http.StatusForbidden,
		}
	case awsapi.ProtocolRESTXML:
		if served.errors == awsapi.ErrorProfileBareXML {
			// S3, the one noErrorWrapping service, words its denial its own way.
			return accessDenied(s3DeniedMessage(denial))
		}
		return accessDenied(message)
	case awsapi.ProtocolRESTJSON, awsapi.ProtocolUnknown:
		// A request no protocol could be read from gets the answer this
		// middleware has always given one.
		return accessDeniedException(http.StatusForbidden, message)
	}
	return accessDeniedException(http.StatusForbidden, message)
}

// denial is what the caller of a denied request is told: the principal, the
// action and resource authorized, and the policy that refused them. It is nil
// when no policy decided the denial — the access key names no principal, or
// its policies could not be read or evaluated — because there is then no
// policy AWS would name.
//
// It is built per request from the evaluation, never cached: the compiled
// policies are shared by every request an access key makes, but the action
// and resource are this request's own.
func (res iamEnforceResult) denial(action, resource string) *iampolicy.Denial {
	if res.principalARN == "" || res.compileErr != nil || res.boundaryErr != nil || len(res.Unsupported) > 0 {
		return nil
	}
	policy, explicit := res.DeniedBy()
	return &iampolicy.Denial{
		Principal: res.principalARN,
		Action:    action,
		Resource:  resource,
		Policy:    policy,
		Explicit:  explicit,
	}
}

// iamDeniedMessage is the message a denial carries: AWS's sentence naming the
// principal, action, resource and policy when a policy decided it.
func iamDeniedMessage(denial *iampolicy.Denial) string {
	if denial == nil {
		return iamUnattributedDeniedMessage
	}
	return denial.Message()
}

// ec2DeniedMessage is EC2's fixed sentence, followed by the IAM one when a
// policy decided the denial.
func ec2DeniedMessage(denial *iampolicy.Denial) string {
	if denial == nil {
		return ec2UnauthorizedMessage
	}
	return ec2UnauthorizedMessage + " " + denial.Message()
}

// s3DeniedMessage is S3's wording: the IAM sentence with the resource quoted,
// or a bare "Access Denied" when no policy decided the denial.
func s3DeniedMessage(denial *iampolicy.Denial) string {
	if denial == nil {
		return "Access Denied"
	}
	quoted := *denial
	quoted.QuoteResource = true
	return quoted.Message()
}

// accessDeniedException is the AccessDeniedException the JSON protocols answer
// a denial with: 400 for awsJson and Smithy RPC v2, as AWS's common errors
// document it, and 403 for restJson1.
func accessDeniedException(status int, message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessDeniedException", Message: message, HTTPStatus: status}
}

// accessDenied is the 403 AccessDenied the XML protocols answer a denial with.
func accessDenied(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessDenied", Message: message, HTTPStatus: http.StatusForbidden}
}

// queryAccessDenied is the 403 an awsQuery service answers a denial with:
// AccessDenied, or the service's own code from queryDenialCodes.
func queryAccessDenied(service, message string) *protocol.AWSError {
	denial := accessDenied(message)
	if code, ok := queryDenialCodes[service]; ok {
		denial.Code = code
	}
	return denial
}
