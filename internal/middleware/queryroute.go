package middleware

import "net/http"

// QueryRoute is how the router serves an AWS Query request: the key of the
// service that answers it and the Action it dispatches that service on.
// Service is "" when no enabled service owns the Action, and the router then
// answers the request itself without serving any operation.
type QueryRoute struct {
	Service string
	Action  string
}

// QueryRouter is the router's AWS Query dispatch, as IAM enforcement reads it.
//
// It exists because a Query request names its own service in its content, and
// only the router knows which service takes it: the Version and Action pair
// decides, through ownership rules the services declare. The credential scope
// plays no part, so authorising a Query call by the service detectService reads
// from the scope evaluated one operation and served another — an IAM
// CreateUser signed for s3 was checked as s3:CreateUser (#2229). IAM
// enforcement asks this instead, so the action it evaluates is the operation
// the router serves.
type QueryRouter interface {
	// RouteQuery reports how r is served when the router dispatches it as AWS
	// Query traffic, and false when it does not. It parses r's form the way the
	// router does, and err is the failure the router refuses the request with.
	RouteQuery(w http.ResponseWriter, r *http.Request) (route QueryRoute, isQuery bool, err error)
}
