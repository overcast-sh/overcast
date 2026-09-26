package middleware

import (
	"net/http"
	"strings"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// servedProtocol is the wire protocol a request is served over, and the error
// envelope its caller reads an error from. The two differ in grain: every
// rest-xml request is one protocol, but S3's errors are a bare <Error> and
// every other rest-xml service's are wrapped (#2265).
type servedProtocol struct {
	protocol awsapi.Protocol
	errors   awsapi.ErrorProfile
}

// servedOver is modelService's envelope over protocol p.
func servedOver(p awsapi.Protocol, modelService string) servedProtocol {
	return servedProtocol{protocol: p, errors: awsapi.ErrorProfileFor(p, modelService)}
}

// writeUnroutedError answers a request that middleware rejects without the
// router's resolution of it — a failed signature, a store still migrating, a
// recovered panic — in the error envelope of the protocol it is served over.
//
// No codec or resolved Query route is on this request: middleware.Protocol
// and the router run inside these middlewares, not before them. So a Query
// call is recognised by its modeled Action, which for a form-encoded POST
// means reading the form here; ParseFormPreservingBody leaves the body intact
// for anything that still reads it. Only a POST carries a Query form: an S3
// PUT sent with curl's default form content type is an object body.
//
// A recovered panic is classified from what is left of the request. The
// handler has usually drained a Query POST's body by then, so that one falls
// back to the attributed service's envelope, as it did before #2265.
func writeUnroutedError(w http.ResponseWriter, r *http.Request, aerr *protocol.AWSError) {
	if r.Method == http.MethodPost && strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/x-www-form-urlencoded") {
		_ = protocol.ParseFormPreservingBody(r)
	}
	serviceutil.WriteError(w, r, requestProtocol(r, detectService(r), false).errors, aerr)
}

// requestProtocol names the wire protocol r is served over, from the same
// signals the router dispatches it by: the Query route the router resolved,
// an X-Amz-Target, a Smithy RPC v2 URI, a modeled Query Action, and finally a
// modeled REST binding. It answers ProtocolUnknown, in the JSON envelope, when
// none applies.
//
// service is the key of the service r is attributed to, and routedQuery
// reports that the router resolved r as a Query route. Middleware that runs
// before routing (SigV4) passes false and relies on the modeled Action.
//
// It asks the request, not the service, because one service can answer on
// several protocols — SQS on awsJson1_0 and awsQuery, CloudWatch on awsQuery
// and rpcv2Cbor — and its caller reads only the one it sent. Every middleware
// error that must reach an SDK chooses its envelope here, so no list of
// service names does (#2259, #2265).
func requestProtocol(r *http.Request, service string, routedQuery bool) servedProtocol {
	if routedQuery {
		claim, _ := queryOperationClaim(r)
		return queryDialect(claim)
	}
	if strings.TrimSpace(r.Header.Get("X-Amz-Target")) != "" {
		// An awsJson1_0 or awsJson1_1 call; the two share an error envelope.
		return servedOver(awsapi.ProtocolAWSJSON10, "")
	}
	if claim, ok := smithyRPCClaim(r); ok {
		return servedProtocol{protocol: claim.Protocol, errors: claim.ErrorProfile}
	}
	if claim, ok := queryOperationClaim(r); ok {
		// Query traffic no QueryRouter resolved.
		return queryDialect(claim)
	}
	if service == "s3" {
		// The fallback owner of every path no model gives another service,
		// so no REST claim can speak for it.
		return servedOver(awsapi.ProtocolRESTXML, "s3")
	}
	if claim, ok := awsapi.NewRegistry().ClaimRESTQuery(r.Method, r.URL.Path, r.URL.RawQuery); ok && !claim.CatchAll {
		return servedProtocol{protocol: claim.Protocol, errors: claim.ErrorProfile}
	}
	if served, ok := signingNameProtocol(ServiceFromCredential(r)); ok {
		return served
	}
	return servedOver(awsapi.ProtocolUnknown, "")
}

// signingNameProtocol names the REST protocol a SigV4 signing name's modeled
// bindings speak, for a request no more specific binding claimed. A root
// catch-all binding ("/{Path+}", MediaStore Data's) is no such claim: it
// matches every path, so it says nothing about the service the caller
// addressed, and the caller's credential scope is the better evidence. The
// router's callerClaim follows the same reasoning (#2264).
func signingNameProtocol(signingName string) (servedProtocol, bool) {
	profile, ok := awsapi.SigningNameErrorProfile(signingName)
	if !ok {
		return servedProtocol{}, false
	}
	p := awsapi.ProtocolRESTJSON
	if profile == awsapi.ErrorProfileRESTXML || profile == awsapi.ErrorProfileBareXML {
		p = awsapi.ProtocolRESTXML
	}
	return servedProtocol{protocol: p, errors: profile}, true
}

// queryDialect names the Query protocol a Query call to claim's operation is
// served over: EC2's own dialect for EC2, and awsQuery for everything else,
// including an Action no model carries. It reads the claim's ErrorProfile, not
// its Protocol: Protocol is the model's canonical protocol, which for a service
// that also answers Query — CloudWatch's rpcv2Cbor, SQS's awsJson1_0 — is not
// the one called.
func queryDialect(claim awsapi.Claim) servedProtocol {
	if claim.ErrorProfile == awsapi.ErrorProfileEC2QueryXML {
		return servedOver(awsapi.ProtocolEC2Query, claim.ModelService)
	}
	return servedOver(awsapi.ProtocolAWSQuery, claim.ModelService)
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
