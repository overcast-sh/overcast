package cloudfront_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// postOAC creates an OAC from raw fields and returns the response.
func postOAC(t *testing.T, srv *helpers.TestServer, protocol, behavior, originType string) *http.Response {
	t.Helper()
	body := oacConfigXML("lw-oac", protocol, behavior, originType)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/origin-access-control", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOriginAccessControl: %v", err)
	}
	return resp
}

func errorCode(t *testing.T, resp *http.Response) string {
	t.Helper()
	b := readBody(t, resp)
	var e struct {
		Error struct {
			Code string `xml:"Code"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(b, &e); err != nil {
		t.Fatalf("unmarshal error: %v\n%s", err, b)
	}
	return e.Error.Code
}

func TestCreateOriginAccessControl_lambdaOriginTypeEchoed(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an OAC for a Web Function (lambda, always-amz-auth) is created, then read back
	resp := postOAC(t, srv, "sigv4", "always-amz-auth", "lambda")
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var created struct {
		ID     string `xml:"Id"`
		Config struct {
			SigningBehavior string `xml:"SigningBehavior"`
			OriginType      string `xml:"OriginAccessControlOriginType"`
		} `xml:"OriginAccessControlConfig"`
	}
	if err := xml.Unmarshal(readBody(t, resp), &created); err != nil {
		t.Fatal(err)
	}
	get, err := http.Get(srv.URL + "/2020-05-31/origin-access-control/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer get.Body.Close()

	// Then: both responses carry the lambda type and the new signing behavior
	helpers.AssertStatus(t, get, http.StatusOK)
	var got struct {
		Config struct {
			SigningBehavior string `xml:"SigningBehavior"`
			OriginType      string `xml:"OriginAccessControlOriginType"`
		} `xml:"OriginAccessControlConfig"`
	}
	if err := xml.Unmarshal(readBody(t, get), &got); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ b, o string }{
		{created.Config.SigningBehavior, created.Config.OriginType},
		{got.Config.SigningBehavior, got.Config.OriginType},
	} {
		if c.b != "always-amz-auth" || c.o != "lambda" {
			t.Errorf("echoed behavior=%q type=%q, want always-amz-auth/lambda", c.b, c.o)
		}
	}
}

func TestCreateOriginAccessControl_signingRules(t *testing.T) {
	cases := []struct {
		name, protocol, behavior, originType string
		wantStatus                           int
	}{
		{"always-amz-auth on s3 rejected", "sigv4", "always-amz-auth", "s3", http.StatusBadRequest},
		{"unknown behavior rejected", "sigv4", "sometimes", "lambda", http.StatusBadRequest},
		{"unknown origin type rejected", "sigv4", "always", "ec2", http.StatusBadRequest},
		{"sigv4a global endpoint accepted", "sigv4a", "always-amz-auth", "lambda", http.StatusCreated},
		{"lambda function URL OAC accepted", "sigv4", "always", "lambda", http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: an empty store
			srv := helpers.NewTestServer(t)

			// When: the OAC is created
			resp := postOAC(t, srv, tc.protocol, tc.behavior, tc.originType)
			defer resp.Body.Close()

			// Then: the documented status, with InvalidArgument for a rejection
			helpers.AssertStatus(t, resp, tc.wantStatus)
			if tc.wantStatus == http.StatusBadRequest {
				if code := errorCode(t, resp); code != "InvalidArgument" {
					t.Errorf("code = %q, want InvalidArgument", code)
				}
			}
		})
	}
}

func TestUpdateOriginAccessControl_rejectsAlwaysAmzAuthOnS3(t *testing.T) {
	// Given: an existing s3 OAC
	srv := helpers.NewTestServer(t)
	oac, etag := cfCreateOAC(t, srv, "to-update")

	// When: it is updated to always-amz-auth
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/2020-05-31/origin-access-control/"+oac.ID+"/config",
		strings.NewReader(oacConfigXML("to-update", "sigv4", "always-amz-auth", "s3")))
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Then: it is refused
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// lambdaWebDistXML is a distribution whose only origin is a Web Function
// endpoint domain with the given OAC. The stub port stands in for the
// endpoint's HTTP port.
func lambdaWebDistXML(ref, domain, oacID string, port string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>lambda web origin</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>web-1</Id>
        <DomainName>%s</DomainName>
        <OriginAccessControlId>%s</OriginAccessControlId>
        <CustomOriginConfig>
          <HTTPPort>%s</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>web-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString><Headers><Quantity>1</Quantity><Items><Name>Authorization</Name></Items></Headers></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, ref, domain, oacID, port)
}

func TestCreateDistribution_lambdaOACRequiresWebFunctionDomain(t *testing.T) {
	// Given: a lambda OAC
	srv := helpers.NewTestServer(t)
	resp := postOAC(t, srv, "sigv4", "always-amz-auth", "lambda")
	var oac parsedOAC
	_ = xml.Unmarshal(readBody(t, resp), &oac)
	resp.Body.Close()

	// When: a distribution attaches it to an ordinary custom origin
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/distribution",
		strings.NewReader(lambdaWebDistXML("lw-bad", "example.com", oac.ID, "80")))
	req.Header.Set("Content-Type", "application/xml")
	bad, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()

	// Then: InvalidDomainNameForOriginAccessControl
	helpers.AssertStatus(t, bad, http.StatusBadRequest)
	if code := errorCode(t, bad); code != "InvalidDomainNameForOriginAccessControl" {
		t.Errorf("code = %q", code)
	}
}

func TestProxy_lambdaWebOriginSignedByOAC(t *testing.T) {
	// Given: a stub Web Function endpoint (the real data plane is not wired here)
	var host string
	var hdr http.Header
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, hdr = r.Host, r.Header.Clone()
		_, _ = io.WriteString(w, "from web function")
	}))
	defer stub.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(stub.URL, "http://"))
	if _, err := net.LookupHost("probe.lambda-web.us-east-1.localhost"); err != nil {
		t.Skipf("*.localhost does not resolve on this host: %v", err)
	}

	srv := helpers.NewTestServer(t)
	resp := postOAC(t, srv, "sigv4", "always-amz-auth", "lambda")
	var oac parsedOAC
	_ = xml.Unmarshal(readBody(t, resp), &oac)
	resp.Body.Close()
	domain := "abc123.def456.lambda-web.us-east-1.localhost"
	dist, _ := cfCreateDistFromXML(t, srv, lambdaWebDistXML("lw-ok", domain, oac.ID, port))

	// When: a viewer with an application token requests through the distribution
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/hello", nil)
	req.Header.Set("Authorization", "Bearer app-jwt")
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer got.Body.Close()

	// Then: the endpoint is reached with its own domain as Host
	helpers.AssertStatus(t, got, http.StatusOK)
	if h, _, _ := net.SplitHostPort(host); h != domain {
		t.Errorf("Host = %q, want %q", host, domain)
	}
	// And: CloudFront signed for service lambda and kept the viewer's Authorization
	sig := hdr.Get("X-Amz-Cf-Authorization")
	if !strings.Contains(sig, "/us-east-1/lambda/aws4_request") {
		t.Errorf("signature = %q, want SigV4 scope us-east-1/lambda", sig)
	}
	if hdr.Get("Authorization") != "Bearer app-jwt" {
		t.Errorf("Authorization = %q, want the viewer's", hdr.Get("Authorization"))
	}
	if hdr.Get("X-Amz-Content-Sha256") == "" {
		t.Error("X-Amz-Content-Sha256 missing")
	}
}
