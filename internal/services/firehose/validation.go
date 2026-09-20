package firehose

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Request constraints, from the pinned Firehose model
// (models/firehose/service/2015-08-04/firehose-2015-08-04.json):
//
//	DeliveryStreamName             @length(1, 64), @pattern ^[a-zA-Z0-9_.-]+$
//	Data (blob, Record.Data)       @length(0, 1024000), required
//	PutRecordBatchRequestEntryList @length(1, 500)
//
// maxBatchBytes is not a model constraint: it is the per-call limit the
// PutRecordBatch reference documents alongside the 500-record one ("up to 500
// records, or 4 MiB per call, whichever is smaller"). It is enforced because
// client batching code is written against it, the same reason SQS's batch
// limits are enforced — not as quota emulation.
const (
	minDeliveryStreamNameLen = 1
	maxDeliveryStreamNameLen = 64
	maxRecordDataBytes       = 1024000
	minRecordsPerBatch       = 1
	maxRecordsPerBatch       = 500
	maxBatchBytes            = 4 * 1024 * 1024
)

var deliveryStreamNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// errInvalidArgument returns Firehose's documented error for a request
// parameter whose value is not valid: "InvalidArgumentException — The
// specified input parameter has a value that is not valid", which
// CreateDeliveryStream, PutRecord and PutRecordBatch all list.
//
// Firehose models no ValidationException and its API Reference names none for
// these operations, so a constraint violation is reported under the modeled
// code, carrying AWS's constraint-violation message shape — which names the
// member and the bound that was broken.
//
// 400: every exception in the Firehose model is a client error with no
// httpError override, so the AWS JSON protocol default applies and each
// operation's Errors section states it outright. The same holds for
// ResourceInUseException and ResourceNotFoundException below.
func errInvalidArgument(message string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "InvalidArgumentException",
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
	}
}

// errStreamNotFound reports a delivery stream that does not exist. 400, not
// 404 — see errInvalidArgument; Kinesis's errNoSuchStream answers the same way
// for the same reason.
func errStreamNotFound(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceNotFoundException",
		Message:    fmt.Sprintf("Firehose %s under account not found.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// errStreamInUse reports a CreateDeliveryStream for a name already taken. A
// delivery stream name is unique per account per Region, and
// CreateDeliveryStream lists ResourceInUseException for the clash.
func errStreamInUse(name string) *protocol.AWSError {
	return &protocol.AWSError{
		Code:       "ResourceInUseException",
		Message:    fmt.Sprintf("Firehose %s under account already exists.", name),
		HTTPStatus: http.StatusBadRequest,
	}
}

// validateDeliveryStreamName enforces DeliveryStreamName's length and pattern.
// It runs before the store lookup on every operation that takes the name,
// because AWS validates the request shape before resolving the resource: a
// name that cannot name a delivery stream is an invalid argument rather than a
// missing resource.
func validateDeliveryStreamName(name string) *protocol.AWSError {
	bound := fmt.Sprintf("Member must have length greater than or equal to %d", minDeliveryStreamNameLen)
	if len(name) > maxDeliveryStreamNameLen {
		bound = fmt.Sprintf("Member must have length less than or equal to %d", maxDeliveryStreamNameLen)
	}
	return serviceutil.ResourceName(name, serviceutil.NameRule{
		MinLength:     minDeliveryStreamNameLen,
		MaxLength:     maxDeliveryStreamNameLen,
		Pattern:       deliveryStreamNamePattern,
		ErrorCode:     "InvalidArgumentException",
		HTTPStatus:    http.StatusBadRequest,
		LengthMessage: constraintViolation(name, "deliveryStreamName", bound),
		PatternMessage: constraintViolation(name, "deliveryStreamName",
			"Member must satisfy regular expression pattern: "+deliveryStreamNamePattern.String()),
	})
}

// validateRecord enforces Record's required Data member and its size cap.
// member names the request member under validation so the message points at
// the offending entry the way AWS's does: "record" for PutRecord,
// "records.3.member" for the third entry of a PutRecordBatch.
func validateRecord(member string, rec *firehoseRecord) *protocol.AWSError {
	if rec == nil {
		return errInvalidArgument(fmt.Sprintf(
			"1 validation error detected: Value null at '%s' failed to satisfy constraint: Member must not be null", member))
	}
	if len(rec.Data) > maxRecordDataBytes {
		return errInvalidArgument(blobConstraintViolation(member+".data",
			fmt.Sprintf("Member must have length less than or equal to %d", maxRecordDataBytes)))
	}
	return nil
}

// validateRecordBatch enforces the Records list length, each entry's Data cap,
// and the documented 4 MiB per-call payload limit.
func validateRecordBatch(recs []firehoseRecord) *protocol.AWSError {
	switch {
	case len(recs) < minRecordsPerBatch:
		return errInvalidArgument(blobConstraintViolation("records",
			fmt.Sprintf("Member must have length greater than or equal to %d", minRecordsPerBatch)))
	case len(recs) > maxRecordsPerBatch:
		return errInvalidArgument(blobConstraintViolation("records",
			fmt.Sprintf("Member must have length less than or equal to %d", maxRecordsPerBatch)))
	}
	total := 0
	for i := range recs {
		if aerr := validateRecord(fmt.Sprintf("records.%d.member", i+1), &recs[i]); aerr != nil {
			return aerr
		}
		total += len(recs[i].Data)
	}
	if total > maxBatchBytes {
		return errInvalidArgument(fmt.Sprintf(
			"The PutRecordBatch request carries %d bytes of record data, above the limit of %d bytes per call.",
			total, maxBatchBytes))
	}
	return nil
}

// constraintViolation builds AWS's constraint-violation wording for a member
// whose value is worth echoing back.
func constraintViolation(value, member, bound string) string {
	return fmt.Sprintf("1 validation error detected: Value '%s' at '%s' failed to satisfy constraint: %s",
		value, member, bound)
}

// blobConstraintViolation is constraintViolation for a member whose value is a
// blob or a list, which AWS leaves out of the message rather than printing.
func blobConstraintViolation(member, bound string) string {
	return fmt.Sprintf("1 validation error detected: Value at '%s' failed to satisfy constraint: %s", member, bound)
}
