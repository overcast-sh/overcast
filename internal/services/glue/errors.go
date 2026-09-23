package glue

import (
	"fmt"
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Glue's modeled client errors all answer HTTP 400 with the code in __type;
// only InternalServiceException is a 500.
const (
	codeAlreadyExists          = "AlreadyExistsException"
	codeConcurrentModification = "ConcurrentModificationException"
	codeEntityNotFound         = "EntityNotFoundException"
	codeInvalidInput           = "InvalidInputException"
)

func glueError(code, format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: code, Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusBadRequest}
}

func errInvalidInput(format string, args ...any) *protocol.AWSError {
	return glueError(codeInvalidInput, format, args...)
}

func errDatabaseNotFound(name string) *protocol.AWSError {
	return glueError(codeEntityNotFound, "Database %s not found.", name)
}

func errTableNotFound(name string) *protocol.AWSError {
	return glueError(codeEntityNotFound, "Table %s not found.", name)
}

func errPartitionNotFound() *protocol.AWSError {
	return glueError(codeEntityNotFound, "Cannot find partition.")
}

func errInternal(err error) *protocol.AWSError {
	return protocol.Wrap(protocol.ErrInternalError, err)
}

// errorDetail renders an AWSError as a batch operation's per-item ErrorDetail.
func errorDetail(aerr *protocol.AWSError) *ErrorDetail {
	return &ErrorDetail{ErrorCode: aerr.Code, ErrorMessage: aerr.Message}
}
