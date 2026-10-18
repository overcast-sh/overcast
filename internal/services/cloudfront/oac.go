package cloudfront

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/overcast-sh/overcast/internal/protocol"
	"github.com/overcast-sh/overcast/internal/serviceutil"
)

// Origin access control (OAC) values. Sources: CreateOriginAccessControl API
// reference and "Restrict access to an AWS Lambda Web Function origin" in the
// CloudFront Developer Guide.
const (
	oacTypeS3             = "s3"
	oacTypeMediaStore     = "mediastore"
	oacTypeMediaPackageV2 = "mediapackagev2"
	oacTypeLambda         = "lambda"

	oacSigningProtocolV4  = "sigv4"
	oacSigningProtocolV4a = "sigv4a"

	oacBehaviorNever         = "never"
	oacBehaviorAlways        = "always"
	oacBehaviorNoOverride    = "no-override"
	oacBehaviorAlwaysAmzAuth = "always-amz-auth"

	// lambdaWebLabel is the DNS label that marks a Lambda Web Function endpoint
	// domain: {id}.{shard}.lambda-web.{region}.{base} (global: ...lambda-web.global...).
	lambdaWebLabel = "lambda-web"

	// cfOriginAuthHeader carries CloudFront's origin-request signature to a
	// Web Function endpoint. AWS adds it "in a dedicated CloudFront header" so
	// the viewer's Authorization header can pass through untouched; the header
	// name is not documented, so this one is Overcast's.
	cfOriginAuthHeader = "X-Amz-Cf-Authorization"

	unsignedPayload = "UNSIGNED-PAYLOAD"
	emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// oacSigningCredentials are the fixed credentials the emulator's CloudFront
// service principal signs with. cloudfront.amazonaws.com has no IAM secret on
// AWS; the receiving endpoint authorises it through the resource policy and the
// aws:SourceArn identity (serviceutil.CloudFrontSourceARNHeader), not the key.
var oacSigningCredentials = aws.Credentials{
	AccessKeyID:     "ASIAOVERCASTCLOUDFRONT",
	SecretAccessKey: "overcast-cloudfront-service-principal",
	Source:          "overcast-cloudfront-oac",
}

func errInvalidOACArgument(msg string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InvalidArgument", Message: msg, HTTPStatus: 400}
}

// validateOACConfig applies the documented OriginAccessControlConfig rules.
func validateOACConfig(c *OriginAccessControlConfig) *protocol.AWSError {
	switch c.OriginAccessControlOriginType {
	case oacTypeS3, oacTypeMediaStore, oacTypeMediaPackageV2, oacTypeLambda:
	default:
		return errInvalidOACArgument(fmt.Sprintf(
			"OriginAccessControlOriginType %q is not valid; expected one of s3, mediastore, mediapackagev2, lambda.",
			c.OriginAccessControlOriginType))
	}
	switch c.SigningProtocol {
	case oacSigningProtocolV4, oacSigningProtocolV4a:
	default:
		return errInvalidOACArgument(fmt.Sprintf(
			"SigningProtocol %q is not valid; expected sigv4 or sigv4a.", c.SigningProtocol))
	}
	switch c.SigningBehavior {
	case oacBehaviorNever, oacBehaviorAlways, oacBehaviorNoOverride, oacBehaviorAlwaysAmzAuth:
	default:
		return errInvalidOACArgument(fmt.Sprintf(
			"SigningBehavior %q is not valid; expected one of never, always, no-override, always-amz-auth.",
			c.SigningBehavior))
	}
	// "Lambda Web Function origins support only the always-amz-auth signing
	// behavior ... The always-amz-auth signing behavior is not supported for
	// any other origin type."
	if c.SigningBehavior == oacBehaviorAlwaysAmzAuth && c.OriginAccessControlOriginType != oacTypeLambda {
		return errInvalidOACArgument("The always-amz-auth signing behavior is supported only for the lambda origin type.")
	}
	return nil
}

// isLambdaWebDomain reports whether a domain names a Lambda Web Function
// endpoint (the "lambda-web" label followed by a region or "global").
func isLambdaWebDomain(domain string) bool {
	_, ok := lambdaWebRegion(domain)
	return ok
}

// lambdaWebRegion extracts the endpoint Region label ("global" for a
// MultiRegion endpoint) from a Web Function endpoint domain.
func lambdaWebRegion(domain string) (string, bool) {
	labels := strings.Split(serviceutil.FoldHostname(hostWithoutPort(domain)), ".")
	for i := 1; i+1 < len(labels); i++ {
		if labels[i] == lambdaWebLabel {
			return labels[i+1], true
		}
	}
	return "", false
}

// validateOriginAccessControls checks each origin's OriginAccessControlId.
// A lambda-type OAC may only be attached to a Web Function endpoint origin
// (AWS: InvalidDomainNameForOriginAccessControl). An unknown OAC id is left
// alone, as before: Overcast has never resolved it on this path.
func (h *Handler) validateOriginAccessControls(ctx context.Context, cfg *DistributionConfig) *protocol.AWSError {
	for _, o := range cfg.Origins.Items {
		if o.OriginAccessControlId == "" {
			continue
		}
		oac, err := h.store.GetOAC(ctx, o.OriginAccessControlId)
		if err != nil || oac == nil {
			continue
		}
		if oac.OriginAccessControlConfig.OriginAccessControlOriginType == oacTypeLambda && !isLambdaWebDomain(o.DomainName) {
			return &protocol.AWSError{
				Code: "InvalidDomainNameForOriginAccessControl",
				Message: fmt.Sprintf("The domain name %q of origin %q is not a Lambda Web Function endpoint, "+
					"which an origin access control of type lambda requires.", o.DomainName, o.ID),
				HTTPStatus: 400,
			}
		}
	}
	return nil
}

// signLambdaWebOrigin applies a lambda-type OAC to an outbound origin request.
//
// It stamps the distribution identity (aws:SourceArn), signs the request with
// SigV4 for service "lambda" in the endpoint's Region, and applies the
// always-amz-auth rule: the signature lives in a dedicated header and the
// viewer's Authorization passes through, except an AWS signature, which is
// dropped. When the viewer sent no usable Authorization, the signature is also
// set as Authorization so a plain SigV4 validator accepts the request.
//
// The body is never read: PUT/POST/PATCH use UNSIGNED-PAYLOAD as CloudFront
// does, other methods the empty-body hash.
func (h *Handler) signLambdaWebOrigin(ctx context.Context, req *http.Request, domain, distributionARN string, now time.Time) error {
	viewerAuth := req.Header.Get("Authorization")
	req.Header.Del("Authorization")
	req.Header.Del(cfOriginAuthHeader)
	if strings.HasPrefix(viewerAuth, "AWS4-") {
		viewerAuth = "" // Web Functions strip an AWS signature; so does the emulated edge.
	}
	// Never let a viewer or custom header impersonate the signer's output.
	for _, name := range []string{"X-Amz-Security-Token", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		req.Header.Del(name)
	}

	payload := emptyBodySHA256
	switch req.Method {
	case http.MethodPut, http.MethodPost, http.MethodPatch:
		payload = unsignedPayload
	}
	req.Header.Set("X-Amz-Content-Sha256", payload)
	serviceutil.SetCloudFrontSource(req.Header, distributionARN)

	region, _ := lambdaWebRegion(domain)
	if region == "" || region == "global" {
		region = h.signingRegion()
	}

	// A MultiRegion ("global") endpoint wants SigV4a, which the SDK exposes no
	// public signer for; it is signed with SigV4 in the default Region instead
	// (see docs/services/cloudfront/limitations.md).
	if err := v4.NewSigner().SignHTTP(ctx, oacSigningCredentials, req, payload, "lambda", region, now.UTC()); err != nil {
		return fmt.Errorf("sign lambda web origin request: %w", err)
	}
	sig := req.Header.Get("Authorization")
	req.Header.Del("Authorization")
	req.Header.Set(cfOriginAuthHeader, sig)
	switch {
	case viewerAuth != "":
		req.Header.Set("Authorization", viewerAuth)
	default:
		req.Header.Set("Authorization", sig)
	}
	return nil
}

// signingRegion is the Region used when the endpoint domain does not name one
// (a MultiRegion "global" endpoint): the emulator's default Region.
func (h *Handler) signingRegion() string {
	if h.cfg != nil && h.cfg.Region != "" {
		return h.cfg.Region
	}
	return "us-east-1"
}
