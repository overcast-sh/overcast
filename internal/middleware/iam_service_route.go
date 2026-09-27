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
	if op.action == "" && !authorisedByAPIGateway(r, service) {
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

// authorisedByAPIGateway reports whether r, which API Gateway's own route
// serves and no operation of API Gateway's names, invokes a deployed API: a
// REST API at /restapis/{restApiId}/{stageName}/_user_request_/, or an HTTP
// API beneath /v2/apis/{apiId}/stages/{stageName}/. An invocation is no
// operation of API Gateway's: AWS authorises it by the method's authorization
// type, with execute-api:Invoke for an AWS_IAM method, and API Gateway's
// handler does that itself, as it does for the same invocation on the
// execute-api host. So enforcement names no action for it and leaves it to
// the handler.
func authorisedByAPIGateway(r *http.Request, service string) bool {
	if service != "apigateway" {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	switch {
	case len(segments) >= 4 && segments[0] == "restapis":
		return segments[3] == "_user_request_"
	case len(segments) >= 6 && segments[0] == "v2" && segments[1] == "apis":
		return segments[3] == "stages"
	}
	return false
}
