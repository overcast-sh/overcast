package middleware

import (
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
)

// iamDeniedMessage is the message every IAM denial carries except S3's and
// EC2's, which AWS words differently.
const iamDeniedMessage = "User is not authorized to perform this action"

// writeIAMAccessDenied answers a denied request with the access-denied error
// of the wire protocol it is served over, in that protocol's envelope, so the
// caller's SDK reads the error code rather than failing to deserialize it
// (#2259).
func writeIAMAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation) {
	switch iamDenialProtocol(r, op) {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11:
		protocol.WriteJSONError(w, r, accessDeniedException(http.StatusBadRequest))
	case awsapi.ProtocolRPCV2CBOR:
		codec.RPCv2CBOR.WriteError(w, r, accessDeniedException(http.StatusBadRequest))
	case awsapi.ProtocolRPCV2JSON:
		codec.RPCv2JSON.WriteError(w, r, accessDeniedException(http.StatusBadRequest))
	case awsapi.ProtocolAWSQuery:
		protocol.WriteQueryXMLError(w, r, accessDenied(iamDeniedMessage))
	case awsapi.ProtocolEC2Query:
		protocol.WriteEC2QueryXMLError(w, r, &protocol.AWSError{
			Code:       "UnauthorizedOperation",
			Message:    "You are not authorized to perform this operation.",
			HTTPStatus: http.StatusForbidden,
		})
	case awsapi.ProtocolRESTXML:
		writeRESTXMLAccessDenied(w, r, op)
	case awsapi.ProtocolRESTJSON, awsapi.ProtocolUnknown:
		// A request no protocol could be read from gets the answer this
		// middleware has always given one.
		protocol.WriteJSONError(w, r, accessDeniedException(http.StatusForbidden))
	}
}

// writeRESTXMLAccessDenied answers a denied REST-XML request. S3 is modeled
// with noErrorWrapping, so its error is a bare <Error>; every other REST-XML
// service (CloudFront, Route 53) wraps it in <ErrorResponse>, and an SDK
// reading a bare <Error> from one of them finds no error code at all. The
// registry does not carry noErrorWrapping yet, so S3 is named here (#2265).
func writeRESTXMLAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation) {
	if op.service == "s3" {
		protocol.WriteXMLError(w, r, accessDenied("Access Denied"))
		return
	}
	protocol.WriteRESTXMLError(w, r, "", accessDenied(iamDeniedMessage))
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
func accessDeniedException(status int) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessDeniedException", Message: iamDeniedMessage, HTTPStatus: status}
}

// accessDenied is the 403 AccessDenied the XML protocols answer a denial with.
func accessDenied(message string) *protocol.AWSError {
	return &protocol.AWSError{Code: "AccessDenied", Message: message, HTTPStatus: http.StatusForbidden}
}
