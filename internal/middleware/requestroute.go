package middleware

import (
	"net/http"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// QueryRoute is how the router serves an AWS Query request: the key of the
// service that answers it and the Action it dispatches that service on.
// Service is "" when no enabled service owns the Action, and the router then
// answers the request itself without serving any operation.
type QueryRoute struct {
	Service string
	Action  string
}

// RESTOutcome is how the router serves a request by its path: through the
// REST fallback, which serves a request no service's own route claims, or by
// a service's route.
type RESTOutcome uint8

const (
	// RESTServedByS3: no modeled binding answers the caller, so S3 serves the
	// request, as the owner of every path no other service claims.
	RESTServedByS3 RESTOutcome = iota
	// RESTNotImplemented: a modeled binding answers the caller, with the
	// generated 501 of an operation Overcast does not implement.
	RESTNotImplemented
	// RESTScopeMismatch: the binding is real, but the caller's credential
	// scope names a different real AWS service. The router refuses the
	// request, as AWS does, and serves no operation.
	RESTScopeMismatch
	// RESTServedByService: a service's own route serves the request, or a
	// router-owned dispatcher hands it to one of the service's routers:
	// Service names it.
	RESTServedByService
)

// RESTRoute is how the router serves a request by its path.
type RESTRoute struct {
	Outcome RESTOutcome
	// Claim is the modeled binding the fallback answers from. It is zero when
	// S3 or a service's route serves the request, and names no Service when
	// the binding is shared by several services or is a root catch-all: the
	// caller's own credential scope then names the service whose 501 it gets.
	Claim awsapi.Claim
	// Service is the key of the service whose route serves the request, for
	// RESTServedByService.
	Service string
}

// RequestRouter is the router's dispatch, as IAM enforcement reads it.
//
// It exists because only the router knows which service and operation serve a
// request, and the request's own content can say otherwise. A Query request
// names its service in its Version and Action, which the router resolves
// through ownership rules the services declare; authorising it by the service
// detectService reads from the credential scope evaluated one operation and
// served another — an IAM CreateUser signed for s3 was checked as
// s3:CreateUser (#2229). A request the router serves by its path can carry an
// Action and a credential scope too, and the router serves it by neither: a
// PUT /bucket?Action=GetFederationToken signed for sts is S3's CreateBucket,
// and was checked as sts:GetFederationToken (#2271); GET /clusters signed for
// sts is EKS's ListClusters, and was checked as an STS call it could not name
// (#2283). IAM enforcement asks this instead, so the action it evaluates is the
// operation the router serves.
type RequestRouter interface {
	// RouteQuery reports how r is served when the router dispatches it as AWS
	// Query traffic, and false when it does not. It parses r's form the way the
	// router does, and err is the failure the router refuses the request with.
	RouteQuery(w http.ResponseWriter, r *http.Request) (route QueryRoute, isQuery bool, err error)
	// RouteREST reports how r is served by its path: through the router's REST
	// fallback, or by a service's own route. It reports false when r reaches
	// neither, such as a route the router answers itself, or no route.
	RouteREST(r *http.Request) (route RESTRoute, routed bool)
}
