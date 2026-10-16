package lambdaweb

// ARNs. The model's ResourceArn pattern fixes the function's form,
// arn:aws:lambda:{region}:{account}:web-function/{name}; AWS's permissions
// guide (web-functions-permissions.md) gives the endpoint's as
// .../web-function/{name}/endpoint/{endpoint}. The revision ARN's form is
// inferred by symmetry, .../web-function/{name}/revision/{id}: no document
// shows one.

import (
	"fmt"
	"strings"
)

func (s *Service) functionARN(region, name string) string {
	return fmt.Sprintf("arn:aws:lambda:%s:%s:web-function/%s", region, s.accountID(), name)
}

func (s *Service) endpointARN(region, function, name string) string {
	return s.functionARN(region, function) + "/endpoint/" + name
}

func (s *Service) revisionARN(region, function, id string) string {
	return s.functionARN(region, function) + "/revision/" + id
}

// arnParts is a parsed web-function ARN. Kind is "" for the function itself,
// "endpoint" or "revision" for a child; Child is the child's name or ID.
type arnParts struct {
	Region, Account, Function, Kind, Child string
}

// parseARN reads a web-function, endpoint or revision ARN. ok is false for
// anything else, including an ARN of another service or resource type.
func parseARN(arn string) (arnParts, bool) {
	fields := strings.SplitN(arn, ":", 6)
	if len(fields) != 6 || fields[0] != "arn" || fields[2] != "lambda" {
		return arnParts{}, false
	}
	segs := strings.Split(fields[5], "/")
	switch {
	case len(segs) == 2 && segs[0] == "web-function" && segs[1] != "":
		return arnParts{Region: fields[3], Account: fields[4], Function: segs[1]}, true
	case len(segs) == 4 && segs[0] == "web-function" && segs[1] != "" &&
		(segs[2] == "endpoint" || segs[2] == "revision") && segs[3] != "":
		return arnParts{Region: fields[3], Account: fields[4], Function: segs[1], Kind: segs[2], Child: segs[3]}, true
	}
	return arnParts{}, false
}
