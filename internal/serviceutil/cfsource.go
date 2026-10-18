package serviceutil

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
)

// Internal service-to-service identity for requests CloudFront makes on behalf
// of a distribution.
//
// CloudFront reaches an in-emulator origin by dialling the emulator's own
// listener (loopback), so the request re-enters the front door exactly like a
// client request would. AWS authorises such a request through the
// aws:SourceArn condition key (the distribution's ARN), which CloudFront's
// service principal carries in the signed request context. Overcast has no
// such out-of-band channel, so it carries the same fact in a header pair:
//
//   - CloudFrontSourceARNHeader: the distribution ARN, the value of
//     aws:SourceArn.
//   - InternalTrustHeader: a per-process secret proving the header pair came
//     from inside this process.
//
// The pair is trustworthy only because the receiver calls
// TakeCloudFrontSource first: it deletes both headers from the inbound request
// unconditionally and reports the ARN only when the secret matches. An
// external client can send both headers, but cannot guess the secret, so its
// claimed ARN is dropped and the request is treated as a direct caller.
// Consumers MUST call TakeCloudFrontSource on every inbound request, before
// anything else reads the headers, so a spoofed value never reaches an
// application container.
const (
	// CloudFrontSourceARNHeader carries the aws:SourceArn of a request that
	// CloudFront signed on a distribution's behalf.
	CloudFrontSourceARNHeader = "X-Overcast-Source-Arn"

	// InternalTrustHeader carries the per-process secret that authenticates
	// CloudFrontSourceARNHeader as written by this process.
	InternalTrustHeader = "X-Overcast-Internal-Token"
)

var internalTrustSecret = sync.OnceValue(func() string {
	var b [16]byte
	// crypto/rand does not fail on supported platforms; a zero secret would
	// still be unguessable-by-accident, and the only effect of a failure is a
	// weaker spoofing guard in a non-security-boundary emulator.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
})

// SetCloudFrontSource stamps h with the distribution identity for a request
// CloudFront is about to send to an in-emulator origin.
func SetCloudFrontSource(h http.Header, distributionARN string) {
	h.Set(CloudFrontSourceARNHeader, distributionARN)
	h.Set(InternalTrustHeader, internalTrustSecret())
}

// TakeCloudFrontSource removes the internal identity headers from r and
// returns the distribution ARN they carried, if and only if the request came
// from this process (the trust secret matched). ok is false for an external
// request, including one that spoofed the headers.
func TakeCloudFrontSource(r *http.Request) (distributionARN string, ok bool) {
	arn := r.Header.Get(CloudFrontSourceARNHeader)
	tok := r.Header.Get(InternalTrustHeader)
	r.Header.Del(CloudFrontSourceARNHeader)
	r.Header.Del(InternalTrustHeader)
	if arn == "" || tok == "" {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(internalTrustSecret())) != 1 {
		return "", false
	}
	return arn, true
}
