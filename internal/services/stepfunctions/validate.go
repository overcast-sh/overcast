package stepfunctions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ValidateStateMachineDefinition: the same structural validation
// CreateStateMachine applies (parseDefinition, asl.go), reported as
// diagnostics instead of an InvalidDefinition error.
//
// https://docs.aws.amazon.com/step-functions/latest/apireference/API_ValidateStateMachineDefinition.html

const (
	validationResultOK   = "OK"
	validationResultFail = "FAIL"

	diagnosticSeverityError   = "ERROR"
	diagnosticSeverityWarning = "WARNING"

	// Diagnostic codes AWS uses for the classes of problem parseDefinition
	// detects. AWS documents that codes and wording may change, and that
	// only result is contractual.
	diagnosticInvalidJSON      = "INVALID_JSON_DESCRIPTION"
	diagnosticMissingTarget    = "MISSING_TRANSITION_TARGET"
	diagnosticSchemaValidation = "SCHEMA_VALIDATION_FAILED"

	// maxValidationDiagnostics is maxResults' default and ceiling.
	maxValidationDiagnostics = 100
)

type validateStateMachineDefinitionRequest struct {
	Definition string `json:"definition" cbor:"definition"`
	Type       string `json:"type" cbor:"type"`
	Severity   string `json:"severity" cbor:"severity"`
	MaxResults int    `json:"maxResults" cbor:"maxResults"`
}

type validationDiagnostic struct {
	Severity string `json:"severity" cbor:"severity"`
	Code     string `json:"code" cbor:"code"`
	Message  string `json:"message" cbor:"message"`
	Location string `json:"location,omitempty" cbor:"location,omitempty"`
}

type validateStateMachineDefinitionResponse struct {
	Result      string                 `json:"result" cbor:"result"`
	Diagnostics []validationDiagnostic `json:"diagnostics" cbor:"diagnostics"`
	Truncated   bool                   `json:"truncated" cbor:"truncated"`
}

// validateStateMachineDefinitionTyped never answers an invalid definition
// with an error — that is a 200 with result FAIL. Only a request that breaks
// the operation's own constraints is a ValidationException.
//
// parseDefinition stops at the first structural problem, so a FAIL carries a
// single ERROR diagnostic, and Overcast has no WARNING-level static analysis:
// severity=WARNING is accepted and returns the same ERRORs.
func (h *Handler) validateStateMachineDefinitionTyped(_ context.Context, req *validateStateMachineDefinitionRequest) (*validateStateMachineDefinitionResponse, *protocol.AWSError) {
	if req.Definition == "" {
		return nil, errValidation("1 validation error detected: Value null at 'definition' failed to satisfy constraint: Member must not be null")
	}
	if req.Type != "" && req.Type != "STANDARD" && req.Type != "EXPRESS" {
		return nil, errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'type' failed to satisfy constraint: Member must satisfy enum value set: [STANDARD, EXPRESS]", req.Type))
	}
	if req.Severity != "" && req.Severity != diagnosticSeverityError && req.Severity != diagnosticSeverityWarning {
		return nil, errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%s' at 'severity' failed to satisfy constraint: Member must satisfy enum value set: [ERROR, WARNING]", req.Severity))
	}
	if req.MaxResults < 0 || req.MaxResults > maxValidationDiagnostics {
		return nil, errValidation(fmt.Sprintf(
			"1 validation error detected: Value '%d' at 'maxResults' failed to satisfy constraint: Member must have value between 0 and %d",
			req.MaxResults, maxValidationDiagnostics))
	}

	resp := &validateStateMachineDefinitionResponse{Result: validationResultOK, Diagnostics: []validationDiagnostic{}}
	if _, err := parseDefinition(req.Definition); err != nil {
		resp.Result = validationResultFail
		resp.Diagnostics = append(resp.Diagnostics, definitionDiagnostic(err))
	}
	return resp, nil
}

// definitionDiagnostic maps a parseDefinition failure to an ERROR diagnostic.
// location is a JSON pointer into the definition where the message makes it
// unambiguous — top-level StartAt and top-level states; problems inside a
// Parallel branch or Map processor carry no location.
func definitionDiagnostic(err error) validationDiagnostic {
	msg := err.Error()
	var invalid *invalidDefinitionError
	if errors.As(err, &invalid) {
		msg = invalid.msg
	}
	d := validationDiagnostic{Severity: diagnosticSeverityError, Code: diagnosticSchemaValidation, Message: msg}
	switch {
	case strings.HasPrefix(msg, "definition is not valid JSON"):
		d.Code = diagnosticInvalidJSON
	case strings.Contains(msg, "does not name a state"):
		d.Code = diagnosticMissingTarget
	}
	switch {
	case strings.HasPrefix(msg, "StartAt "):
		d.Location = "/StartAt"
	case strings.HasPrefix(msg, "state "):
		name, _, found := strings.Cut(strings.TrimPrefix(msg, "state "), ": ")
		if found {
			d.Location = "/States/" + escapeJSONPointer(name)
		}
	}
	return d
}

// escapeJSONPointer escapes a JSON pointer reference token (RFC 6901).
func escapeJSONPointer(token string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(token)
}
