package serviceutil

import (
	"net/http"
	"strconv"

	"github.com/overcast-sh/overcast/internal/awsapi"
	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/protocol/codec"
)

// operationRegistry is the immutable generated AWS operation registry. It holds
// no state, so one package-level value serves every caller and no lookup
// allocates.
var operationRegistry = awsapi.NewRegistry()

// WriteNotImplemented writes the standard not-implemented response for a
// modeled AWS operation, in the error envelope that operation's wire protocol
// expects. The 501 status, the x-emulator-unsupported marker, and the request
// ID are all applied by the protocol layer — never set them by hand.
//
// The router uses this for operations no service handler claimed; a service
// that dispatches its own operations uses NotImplementedTarget.
func WriteNotImplemented(w http.ResponseWriter, r *http.Request, claim awsapi.Claim) {
	WriteError(w, r, claim.ErrorProfile, protocol.ErrNotImplemented)
}

// NotImplementedTarget answers an X-Amz-Target that names a modeled AWS
// operation the calling service has not implemented, and reports whether it
// did. It reports false without writing anything when the target names no
// modeled operation, leaving the service free to answer with the
// unknown-operation error AWS uses for a target it does not recognise.
//
// This is the DRY way for any X-Amz-Target dispatcher to satisfy the repo rule
// that a recognized AWS operation returns 501 rather than a 400: the operation
// list comes from the generated registry in internal/awsapi, so it cannot rot
// the way a hand-maintained list of stubs does. Services other than DynamoDB
// still answer their own unknown targets directly and could adopt this the
// same way.
func NotImplementedTarget(w http.ResponseWriter, r *http.Request, target string) bool {
	claim, ok := operationRegistry.ClaimTarget(target)
	if !ok {
		return false
	}
	WriteNotImplemented(w, r, claim)
	return true
}

// AWSService is an established Overcast service key — the spelling
// awsapi.KnownOperation indexes the model corpus by. It is a distinct type so
// that a service key and an operation name cannot be swapped at a call site,
// and so that the only way to obtain one is MustAWSService, which rejects a
// key the models do not carry.
type AWSService string

// MustAWSService converts an Overcast service key to an AWSService, panicking
// if it is not one.
//
// The panic is the point. WriteUnhandledOperation asks the model corpus about
// a (key, operation) pair; a wrong key matches nothing, so every real
// operation silently keeps the 400 the house rule exists to replace.
// CloudWatch Logs hit exactly that — its service key is "cloudwatch-logs",
// not the "logs" its Service.Name() answers — and only a test noticed.
// Callers assign the result to a package-level var, so a typo fails at
// package initialisation, before the emulator serves anything, rather than as
// a wrong status on the wire.
func MustAWSService(key string) AWSService {
	if !awsapi.IsServiceKey(key) {
		panic("serviceutil: " + strconv.Quote(key) + " is not an established Overcast service key (awsapi.IsServiceKey); a modeled identity that aliases to one, such as \"logs\" for \"cloudwatch-logs\", is not a key")
	}
	return AWSService(key)
}

// WriteUnhandledOperation answers an operation name the calling service has
// no handler for, in the wire format c writes, separating two claims that
// used to share one 400:
//
//   - A name the pinned AWS models carry for service is a real operation
//     Overcast has not implemented. It gets protocol.ErrNotImplemented (501),
//     the house rule cloudwatch/service.go's dispatchJSON states: the caller
//     reads "not emulated here" and goes to the support matrix, rather than
//     "no such operation" and off to check their SDK version or spelling.
//   - Any other name gets unknown — whatever unknown-operation error the
//     service has always answered with, unchanged, since AWS itself refuses
//     a target it does not recognise that way.
//
// The real-operation list is the generated model corpus, so it cannot rot the
// way a hand-maintained list of stubs does. The lookup is
// awsapi.KnownOperation — one read of an index built once — rather than
// awsapi.Operations' scan of the whole corpus, because this runs on a live
// request path: the refused name is caller-controlled, so a scan would hand
// an arbitrary client the corpus' full length per request.
//
// c is the codec the request was identified as, so an RPC v2 CBOR caller gets
// a CBOR 501 where the fixed-format NotImplementedJSON would hand it JSON. A
// legacy dispatcher running without a codec in context passes the codec of
// the one protocol it serves; the JSON codecs' WriteError is
// protocol.WriteJSONError and QueryXML's is protocol.WriteQueryXMLError, so a
// service that adopts this keeps its unknown-operation bytes exactly as they
// were.
func WriteUnhandledOperation(w http.ResponseWriter, r *http.Request, c codec.Codec, service AWSService, operation string, unknown *protocol.AWSError) {
	if awsapi.KnownOperation(string(service), operation) {
		c.WriteError(w, r, protocol.ErrNotImplemented)
		return
	}
	c.WriteError(w, r, unknown)
}
