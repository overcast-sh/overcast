package middleware

import (
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/iampolicy"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
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
	message := iamDeniedMessage(denial)
	switch iamDenialProtocol(r, op) {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11:
		protocol.WriteJSONError(w, r, accessDeniedException(http.StatusBadRequest, message))
	case awsapi.ProtocolRPCV2CBOR:
		codec.RPCv2CBOR.WriteError(w, r, accessDeniedException(http.StatusBadRequest, message))
	case awsapi.ProtocolRPCV2JSON:
		codec.RPCv2JSON.WriteError(w, r, accessDeniedException(http.StatusBadRequest, message))
	case awsapi.ProtocolAWSQuery:
		protocol.WriteQueryXMLError(w, r, queryAccessDenied(op.service, message))
	case awsapi.ProtocolEC2Query:
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "UnauthorizedOperation",
			Message:    ec2DeniedMessage(denial),
			HTTPStatus: http.StatusForbidden,
		})
	case awsapi.ProtocolRESTXML:
		writeRESTXMLAccessDenied(w, r, op, denial)
	case awsapi.ProtocolRESTJSON, awsapi.ProtocolUnknown:
		// A request no protocol could be read from gets the answer this
		// middleware has always given one.
		protocol.WriteJSONError(w, r, accessDeniedException(http.StatusForbidden, message))
	}
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

// writeRESTXMLAccessDenied answers a denied REST-XML request. S3 is modeled
// with noErrorWrapping, so its error is a bare <Error>; every other REST-XML
// service (CloudFront, Route 53) wraps it in <ErrorResponse>, and an SDK
// reading a bare <Error> from one of them finds no error code at all. The
// registry does not carry noErrorWrapping yet, so S3 is named here (#2265).
//
// S3 also words its message its own way: the IAM sentence with the resource
// quoted, or a bare "Access Denied" when no policy decided the denial.
func writeRESTXMLAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation, denial *iampolicy.Denial) {
	if op.service == "s3" {
		message := "Access Denied"
		if denial != nil {
			quoted := *denial
			quoted.QuoteResource = true
			message = quoted.Message()
		}
		protocol.WriteXMLError(w, r, accessDenied(message))
		return
	}
	protocol.WriteRESTXMLError(w, r, "", accessDenied(iamDeniedMessage(denial)))
}

// iamDenialProtocol names the wire protocol r is served over, from the same
// signals the router dispatches it by: the Query route the router resolved,
// an X-Amz-Target, a Smithy RPC v2 URI, a modeled Query Action, and finally a
// modeled REST binding. It answers ProtocolUnknown when none applies.
//
// It asks the request, not the service, because one service can answer on
// several protocols — SQS on awsJson1_0 and awsQuery, CloudWatch on awsQuery
// and rpcv2Cbor — and its caller reads only the one it sent.
func iamDenialProtocol(r *http.Request, op iamOperation) awsapi.Protocol {
	if op.query {
		claim, _ := queryOperationClaim(r)
		return queryDialect(claim)
	}
	if strings.TrimSpace(r.Header.Get("X-Amz-Target")) != "" {
		// An awsJson1_0 or awsJson1_1 call; the two share an error envelope.
		return awsapi.ProtocolAWSJSON10
	}
	if claim, ok := smithyRPCClaim(r); ok {
		return claim.Protocol
	}
	if claim, ok := queryOperationClaim(r); ok {
		// Query traffic no QueryRouter resolved.
		return queryDialect(claim)
	}
	if op.service == "s3" {
		// The fallback owner of every path no model gives another service,
		// so no REST claim can speak for it.
		return awsapi.ProtocolRESTXML
	}
	if claim, ok := awsapi.NewRegistry().ClaimRESTQuery(r.Method, r.URL.Path, r.URL.RawQuery); ok {
		return claim.Protocol
	}
	return awsapi.ProtocolUnknown
}

// queryDialect names the Query protocol a Query call to claim's operation is
// served over: EC2's own dialect for EC2, and awsQuery for everything else,
// including an Action no model carries. It reads the claim's ErrorProfile, not
// its Protocol: Protocol is the model's canonical protocol, which for a service
// that also answers Query — CloudWatch's rpcv2Cbor, SQS's awsJson1_0 — is not
// the one called.
func queryDialect(claim awsapi.Claim) awsapi.Protocol {
	if claim.ErrorProfile == awsapi.ErrorProfileEC2QueryXML {
		return awsapi.ProtocolEC2Query
	}
	return awsapi.ProtocolAWSQuery
}

// queryOperationClaim names the modeled Query operation r's Version and Action
// select. It reads a form the router or requestIAMAction has already parsed
// and never parses one itself: parsing here would consume a body the handler
// still has to read.
func queryOperationClaim(r *http.Request) (awsapi.Claim, bool) {
	values := r.Form
	if values == nil {
		values = r.URL.Query()
	}
	return awsapi.NewRegistry().ClaimQuery(values.Get("Version"), values.Get("Action"))
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
