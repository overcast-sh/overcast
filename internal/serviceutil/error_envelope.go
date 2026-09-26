package serviceutil

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
)

// WriteError answers r with aerr in the error envelope profile names, so the
// caller's SDK reads aerr's code rather than failing to decode the body.
//
// It is the one place an awsapi.ErrorProfile becomes bytes on the wire. The
// router's 501 and scope-mismatch answers and middleware's IAM denial and
// SigV4 errors all come through here, each with a profile the generated
// registry derived from the pinned models (#2265).
//
// It lives in serviceutil because every caller can reach it there without a
// cycle. Router and middleware both import serviceutil. A writer kept in
// middleware would be out of WriteNotImplemented's reach, since middleware
// imports serviceutil. Protocol and codec sit below the registry and do not
// import it.
//
// A rest-xml error is wrapped in <ErrorResponse> unless the service's
// noErrorWrapping trait asks for a bare <Error>. The wrapped envelope carries
// no xmlns: no AWS SDK reads it, and the registry does not carry each
// service's namespace.
func WriteError(w http.ResponseWriter, r *http.Request, profile awsapi.ErrorProfile, aerr *protocol.AWSError) {
	switch profile {
	case awsapi.ErrorProfileJSON:
		protocol.WriteJSONError(w, r, aerr)
	case awsapi.ErrorProfileQueryXML:
		protocol.WriteQueryXMLError(w, r, aerr)
	case awsapi.ErrorProfileEC2QueryXML:
		protocol.WriteEC2QueryXMLError(w, r, aerr)
	case awsapi.ErrorProfileBareXML:
		protocol.WriteXMLError(w, r, aerr)
	case awsapi.ErrorProfileRESTXML:
		protocol.WriteRESTXMLError(w, r, "", aerr)
	case awsapi.ErrorProfileRPCV2CBOR:
		codec.RPCv2CBOR.WriteError(w, r, aerr)
	case awsapi.ErrorProfileRPCV2JSON:
		codec.RPCv2JSON.WriteError(w, r, aerr)
	default:
		// A zero-value profile, from a Claim no registry lookup produced,
		// keeps the JSON answer these writers have always given one.
		protocol.WriteJSONError(w, r, aerr)
	}
}
