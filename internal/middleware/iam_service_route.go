package middleware

import (
	"net/http"
	"strings"
)

// serviceRouteIAMOperation names the operation service's own route serves r
// as. The route is service's whatever r's credential scope names, so r is
// read under service's rules alone: GET /clusters signed for sts is EKS's
// ListClusters (#2283).
//
// An operation the route serves that cannot be named is authorised as every
// action of the service (unnamedIAMAction) rather than let through. AWS
// authorises every operation it serves, so being unable to name one is a gap
// in Overcast, and enforcement fails closed on gaps, as iamDenialReason does
// for a policy it cannot evaluate. A principal allowed the whole service is
// still served; one allowed less is refused rather than granted something
// its policy never named.
func serviceRouteIAMOperation(r *http.Request, service string) iamOperation {
	op := iamOperation{service: service, action: requestIAMAction(r, service)}
	if op.action == "" && !invokesAPIGatewayAPI(r, service) {
		op.action = unnamedIAMAction(service)
	}
	return op
}

// unnamedIAMAction is the action an operation of service's that cannot be
// named is authorised as: "<prefix>:*", which only a statement granting the
// whole service, or every service, matches.
func unnamedIAMAction(service string) string {
	return iamActionPrefix(service) + ":*"
}

// invokesAPIGatewayAPI reports whether r, which API Gateway's own route
// serves and no operation of API Gateway's names, invokes a deployed API: a
// REST API beneath /restapis/{restApiId}/{stageName}/_user_request_/, or an
// HTTP API beneath /v2/apis/{apiId}/stages/{stageName}/.
//
// An invocation is not gated, as it is not on the execute-api host, whose
// addresses are rewritten into the /_overcast/ namespace shouldBypassIAM
// exempts. It is no operation of API Gateway's: AWS authorises it by the
// method's authorization type, with execute-api:Invoke on the method's ARN
// only for an AWS_IAM method, which neither this middleware nor API Gateway's
// handler checks yet (#2291). Checking it as apigateway:* instead would refuse
// the invocations AWS serves to anyone.
func invokesAPIGatewayAPI(r *http.Request, service string) bool {
	if service != "apigateway" {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case len(segments) >= 5 && segments[0] == "restapis":
		return segments[3] == "_user_request_"
	case len(segments) >= 6 && segments[0] == "v2" && segments[1] == "apis":
		return segments[3] == "stages"
	}
	return false
}
