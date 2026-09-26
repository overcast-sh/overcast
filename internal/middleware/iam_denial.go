package middleware

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// iamDeniedMessage is the message every IAM denial carries except S3's and
// EC2's, which AWS words differently.
const iamDeniedMessage = "User is not authorized to perform this action"

// writeIAMAccessDenied answers a denied request with the access-denied error
// of the wire protocol it is served over, in that protocol's envelope, so the
// caller's SDK reads the error code rather than failing to deserialize it
// (#2259).
func writeIAMAccessDenied(w http.ResponseWriter, r *http.Request, op iamOperation) {
	served := requestProtocol(r, op.service, op.query)
	serviceutil.WriteError(w, r, served.errors, iamAccessDeniedError(served))
}

// iamAccessDeniedError is the access-denied error a caller served over
// served.protocol reads.
func iamAccessDeniedError(served servedProtocol) *protocol.AWSError {
	switch served.protocol {
	case awsapi.ProtocolAWSJSON10, awsapi.ProtocolAWSJSON11, awsapi.ProtocolRPCV2CBOR, awsapi.ProtocolRPCV2JSON:
		return accessDeniedException(http.StatusBadRequest)
	case awsapi.ProtocolAWSQuery:
		return accessDenied(iamDeniedMessage)
	case awsapi.ProtocolEC2Query:
		return &protocol.AWSError{
			Code:       "UnauthorizedOperation",
			Message:    "You are not authorized to perform this operation.",
			HTTPStatus: http.StatusForbidden,
		}
	case awsapi.ProtocolRESTXML:
		if served.errors == awsapi.ErrorProfileBareXML {
			// S3, the one noErrorWrapping service, words its denial its own way.
			return accessDenied("Access Denied")
		}
		return accessDenied(iamDeniedMessage)
	case awsapi.ProtocolRESTJSON, awsapi.ProtocolUnknown:
		// A request no protocol could be read from gets the answer this
		// middleware has always given one.
		return accessDeniedException(http.StatusForbidden)
	}
	return accessDeniedException(http.StatusForbidden)
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
