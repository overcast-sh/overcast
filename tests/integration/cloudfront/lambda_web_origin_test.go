package cloudfront_test

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
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
// endpoint domain with the given OAC. The request path through a real endpoint
// is tested in tests/integration/lambdawebdocker.
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
