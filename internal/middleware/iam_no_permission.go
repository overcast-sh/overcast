package middleware

// iamActionsNeedingNoPermission are the actions AWS serves whatever the
// caller's policies say, an explicit Deny included. An action is listed only
// where AWS's documentation says policies do not control it:
//
//   - sts:GetCallerIdentity. The STS API Reference: "No permissions are
//     required to perform this operation. If an administrator attaches a
//     policy to your identity that explicitly denies access to the
//     sts:GetCallerIdentity action, you can still perform this operation."
//   - sts:GetSessionToken. The STS API Reference: "No permissions are required
//     for users to perform this operation … You cannot use policies to control
//     authentication operations", and the IAM User Guide (Permissions for
//     GetSessionToken): including the action in a policy "has no effect on a
//     user's ability to perform the GetSessionToken operation".
//
// sts:GetAccessKeyInfo is deliberately absent: its API reference makes no such
// statement, so it is authorized like every other action.
var iamActionsNeedingNoPermission = map[string]struct{}{
	"sts:GetCallerIdentity": {},
	"sts:GetSessionToken":   {},
}

// iamServedWithoutPermission reports whether AWS serves op to callerARN
// whatever its policies say. Three things must hold:
//
//   - op's action is one of iamActionsNeedingNoPermission;
//   - the router serves the request as that Query call (op.query). The router
//     names the operation of a Query call and of a request its REST fallback
//     serves, but one a service's own route serves is still read from the
//     request's content under the service its credential scope names, which
//     need not be the operation served: GET /clusters?Action=GetCallerIdentity
//     signed for sts is EKS's ListClusters, and must not borrow STS's
//     exemption;
//   - the access key names a principal. AWS refuses a key it does not know
//     before it authorizes anything, so an unknown key stays denied.
func iamServedWithoutPermission(op iamOperation, callerARN string) bool {
	if !op.query || callerARN == "" {
		return false
	}
	_, ok := iamActionsNeedingNoPermission[op.action]
	return ok
}
