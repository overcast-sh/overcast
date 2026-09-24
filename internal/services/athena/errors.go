package athena

import (
	"fmt"
	"net/http"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Athena's modeled client errors. None carries an @httpError trait, so each
// takes the awsJson1_1 client default, and every operation's Errors section
// says so outright: "HTTP Status Code: 400".
const (
	codeInvalidRequest   = "InvalidRequestException"
	codeMetadata         = "MetadataException"
	codeResourceNotFound = "ResourceNotFoundException"
)

func athenaError(code, format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: code, Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusBadRequest}
}

func errInvalidRequest(format string, args ...any) *protocol.AWSError {
	return athenaError(codeInvalidRequest, format, args...)
}

// errQueryNotFound and errWorkGroupNotFound report an identifier Athena does
// not know. InvalidRequestException is the only client error
// GetQueryExecution, GetQueryResults, GetWorkGroup, DeleteWorkGroup and
// StopQueryExecution model (#2009). The tag operations also model
// ResourceNotFoundException; which of the two AWS picks there is unsettled,
// so they keep answering like the rest.
func errQueryNotFound(id string) *protocol.AWSError {
	return errInvalidRequest("QueryExecution %s was not found", id)
}

func errWorkGroupNotFound(name string) *protocol.AWSError {
	return errInvalidRequest("WorkGroup %s is not found.", name)
}

func errNamedQueryNotFound(id string) *protocol.AWSError {
	return errInvalidRequest("NamedQuery %s was not found", id)
}

func errDataCatalogNotFound(name string) *protocol.AWSError {
	return errInvalidRequest("DataCatalog %s was not found.", name)
}

// errPreparedStatementNotFound is the ResourceNotFoundException Get-, Update-
// and DeletePreparedStatement model for a missing statement.
func errPreparedStatementNotFound(name, workGroup string) *protocol.AWSError {
	return athenaError(codeResourceNotFound, "Prepared statement %s was not found in workgroup %s.", name, workGroup)
}

func errRequired(member string) *protocol.AWSError {
	return errInvalidRequest("%s is required.", member)
}

// errNotEmulated is a 501 for a request AWS accepts but Overcast cannot act
// on, such as a catalog backed by a Lambda connector. An honest 501 is better
// than a 200 that pretends to have done it.
func errNotEmulated(format string, args ...any) *protocol.AWSError {
	return &protocol.AWSError{Code: protocol.ErrNotImplemented.Code, Message: fmt.Sprintf(format, args...), HTTPStatus: http.StatusNotImplemented}
}

func errInternal(err error) *protocol.AWSError {
	return protocol.Wrap(protocol.ErrInternalError, err)
}
