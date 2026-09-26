package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	awsxml "github.com/aws/aws-sdk-go-v2/aws/protocol/xml"

	"github.com/overcast-sh/overcast/internal/awsapi"
)

// TestRESTXMLClaimErrors_decodeWithTheWrappedDecoder holds the router's two
// generated answers to a modeled CloudFront or Route 53 binding — the 501 and
// the scope mismatch — to the <ErrorResponse> envelope those services' SDKs
// decode. Both used to write S3's bare <Error>, from which the AWS SDK for Go
// v2's wrapped rest-xml decoder reads no code at all (#2265).
func TestRESTXMLClaimErrors_decodeWithTheWrappedDecoder(t *testing.T) {
	writers := map[string]struct {
		write func(http.ResponseWriter, *http.Request, awsapi.Claim)
		code  string
	}{
		"not implemented": {writeNotImplemented, "NotImplemented"},
		"scope mismatch":  {writeScopeMismatch, "InvalidSignatureException"},
	}
	for _, path := range []string{"/2020-05-31/distribution", "/2013-04-01/hostedzone"} {
		claim, ok := awsapi.NewRegistry().ClaimREST(http.MethodGet, path)
		if !ok {
			t.Fatalf("ClaimREST(GET %s) matched nothing", path)
		}
		for name, writer := range writers {
			t.Run(name+" "+path, func(t *testing.T) {
				// Given: a claim on a modeled CloudFront or Route 53 binding
				r := httptest.NewRequest(http.MethodGet, path, nil)
				w := httptest.NewRecorder()

				// When: the router answers it
				writer.write(w, r, claim)

				// Then: the SDK's wrapped rest-xml decoder reads the code
				body := w.Body.String()
				got, err := awsxml.GetErrorResponseComponents(strings.NewReader(body), false)
				if err != nil || got.Code != writer.code {
					t.Fatalf("code = %q (%v), want %s; body %s", got.Code, err, writer.code, body)
				}
			})
		}
	}
}
