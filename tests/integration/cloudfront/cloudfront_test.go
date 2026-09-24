// Package cloudfront_test contains integration tests for the CloudFront emulator.
//
// Run: go test ./tests/integration/cloudfront/...
package cloudfront_test

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// distributionConfigXML returns a DistributionConfig with the given caller reference.
func distributionConfigXML(callerRef string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>compat test distribution</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>origin-1</Id>
        <DomainName>example.com</DomainName>
        <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>origin-1</TargetOriginId>
    <ViewerProtocolPolicy>redirect-to-https</ViewerProtocolPolicy>
    <ForwardedValues>
      <QueryString>false</QueryString>
      <Cookies><Forward>none</Forward></Cookies>
    </ForwardedValues>
    <MinTTL>0</MinTTL>
    <TrustedSigners><Enabled>false</Enabled><Quantity>0</Quantity></TrustedSigners>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef)
}

// disabledDistributionConfigXML returns a DistributionConfig with Enabled=false.
func disabledDistributionConfigXML(callerRef string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>compat test distribution</Comment>
  <Enabled>false</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>origin-1</Id>
        <DomainName>example.com</DomainName>
        <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>origin-1</TargetOriginId>
    <ViewerProtocolPolicy>redirect-to-https</ViewerProtocolPolicy>
    <ForwardedValues>
      <QueryString>false</QueryString>
      <Cookies><Forward>none</Forward></Cookies>
    </ForwardedValues>
    <MinTTL>0</MinTTL>
    <TrustedSigners><Enabled>false</Enabled><Quantity>0</Quantity></TrustedSigners>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef)
}

// distributionConfigWithOriginGroupTargetXML returns a DistributionConfig whose
// cache behavior targets an OriginGroup Id for CloudFront origin failover.
func distributionConfigWithOriginGroupTargetXML(callerRef string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>origin group compat test distribution</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>2</Quantity>
    <Items>
      <Origin>
        <Id>origin-primary</Id>
        <DomainName>primary.example.com</DomainName>
        <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
      </Origin>
      <Origin>
        <Id>origin-failover</Id>
        <DomainName>failover.example.com</DomainName>
        <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
      </Origin>
    </Items>
  </Origins>
  <OriginGroups>
    <Quantity>1</Quantity>
    <Items>
      <OriginGroup>
        <Id>origin-group-1</Id>
        <FailoverCriteria>
          <StatusCodes>
            <Quantity>2</Quantity>
            <Items><StatusCode>500</StatusCode><StatusCode>502</StatusCode></Items>
          </StatusCodes>
        </FailoverCriteria>
        <Members>
          <Quantity>2</Quantity>
          <Items>
            <OriginGroupMember><OriginId>origin-primary</OriginId></OriginGroupMember>
            <OriginGroupMember><OriginId>origin-failover</OriginId></OriginGroupMember>
          </Items>
        </Members>
      </OriginGroup>
    </Items>
  </OriginGroups>
  <DefaultCacheBehavior>
    <TargetOriginId>origin-group-1</TargetOriginId>
    <ViewerProtocolPolicy>redirect-to-https</ViewerProtocolPolicy>
    <ForwardedValues>
      <QueryString>false</QueryString>
      <Cookies><Forward>none</Forward></Cookies>
    </ForwardedValues>
    <MinTTL>0</MinTTL>
    <TrustedSigners><Enabled>false</Enabled><Quantity>0</Quantity></TrustedSigners>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef)
}

// parsedDist holds the common fields extracted from a Distribution XML response.
type parsedDist struct {
	XMLName    xml.Name `xml:"Distribution"`
	ID         string   `xml:"Id"`
	ARN        string   `xml:"ARN"`
	Status     string   `xml:"Status"`
	DomainName string   `xml:"DomainName"`
}

// cfCreate sends a POST /2020-05-31/distribution request with the given body XML.
func cfCreate(t *testing.T, srv *helpers.TestServer, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/distribution",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreate: %v", err)
	}
	return resp
}

// cfCreateAndParse creates a distribution and returns the parsed response + ETag.
func cfCreateAndParse(t *testing.T, srv *helpers.TestServer, callerRef string) (parsedDist, string) {
	t.Helper()
	resp := cfCreate(t, srv, distributionConfigXML(callerRef))
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	return dist, etag
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("readBody: %v", err)
	}
	return b
}

// cfCreateInvalidation creates an invalidation and returns the invalidation ID.
func cfCreateInvalidation(t *testing.T, srv *helpers.TestServer, distID, callerRef string) string {
	t.Helper()
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Paths><Quantity>1</Quantity><Items><Path>/*</Path></Items></Paths>
</InvalidationBatch>`, callerRef)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/"+distID+"/invalidation",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateInvalidation: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)
	var inv struct {
		ID string `xml:"Id"`
	}
	if err := xml.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal Invalidation: %v\nbody: %s", err, b)
	}
	return inv.ID
}

// cfTagResource tags a resource with a single key-value pair.
func cfTagResource(t *testing.T, srv *helpers.TestServer, arn, key, value string) {
	t.Helper()
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Tags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items><Tag><Key>%s</Key><Value>%s</Value></Tag></Items>
</Tags>`, key, value)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Tag&Resource="+arn,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfTagResource: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

// distributionConfigWithTagsXML returns a DistributionConfigWithTags XML body.
func distributionConfigWithTagsXML(callerRef, tagKey, tagValue string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfigWithTags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <DistributionConfig>
    <CallerReference>%s</CallerReference>
    <Comment>distribution with tags</Comment>
    <Enabled>true</Enabled>
    <Origins>
      <Quantity>1</Quantity>
      <Items>
        <Origin>
          <Id>origin-1</Id>
          <DomainName>example.com</DomainName>
          <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
        </Origin>
      </Items>
    </Origins>
    <DefaultCacheBehavior>
      <TargetOriginId>origin-1</TargetOriginId>
      <ViewerProtocolPolicy>redirect-to-https</ViewerProtocolPolicy>
      <ForwardedValues>
        <QueryString>false</QueryString>
        <Cookies><Forward>none</Forward></Cookies>
      </ForwardedValues>
      <MinTTL>0</MinTTL>
      <TrustedSigners><Enabled>false</Enabled><Quantity>0</Quantity></TrustedSigners>
    </DefaultCacheBehavior>
  </DistributionConfig>
  <Tags>
    <Items>
      <Tag><Key>%s</Key><Value>%s</Value></Tag>
    </Items>
  </Tags>
</DistributionConfigWithTags>`, callerRef, tagKey, tagValue)
}

// oacConfigXML returns an OriginAccessControlConfig XML body.
func oacConfigXML(name, signingProtocol, signingBehavior, originType string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<OriginAccessControlConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <SigningProtocol>%s</SigningProtocol>
  <SigningBehavior>%s</SigningBehavior>
  <OriginAccessControlOriginType>%s</OriginAccessControlOriginType>
</OriginAccessControlConfig>`, name, signingProtocol, signingBehavior, originType)
}

// parsedOAC holds the common fields extracted from an OriginAccessControl XML response.
type parsedOAC struct {
	XMLName xml.Name `xml:"OriginAccessControl"`
	ID      string   `xml:"Id"`
}

// cfCreateOAC creates an OAC and returns the parsed response + ETag.
func cfCreateOAC(t *testing.T, srv *helpers.TestServer, name string) (parsedOAC, string) {
	t.Helper()
	body := oacConfigXML(name, "sigv4", "always", "s3")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-access-control",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateOAC: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var oac parsedOAC
	if err := xml.Unmarshal(b, &oac); err != nil {
		t.Fatalf("unmarshal OriginAccessControl: %v\nbody: %s", err, b)
	}
	return oac, etag
}

// ─── CreateDistribution ───────────────────────────────────────────────────────

func TestCreateDistribution_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDistribution is called
	resp := cfCreate(t, srv, distributionConfigXML("create-test-1"))
	defer resp.Body.Close()

	// Then: 201 with Distribution element containing Id, ARN, Status, DomainName, and ETag header
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Error("expected ETag header")
	}
	if resp.Header.Get("Location") == "" {
		t.Error("expected Location header")
	}
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	if dist.ID == "" {
		t.Errorf("expected Distribution.Id to be set, body: %s", b)
	}
	if dist.Status != "Deployed" {
		t.Errorf("expected Status=Deployed, got %q", dist.Status)
	}
	if dist.ARN == "" {
		t.Error("expected ARN to be set")
	}
	if dist.DomainName == "" {
		t.Error("expected DomainName to be set")
	}
}

func TestCreateDistribution_originGroupTarget(t *testing.T) {
	// Given: a distribution config with an origin group for failover
	srv := helpers.NewTestServer(t)

	// When: CreateDistribution targets the origin group from DefaultCacheBehavior
	resp := cfCreate(t, srv, distributionConfigWithOriginGroupTargetXML("origin-group-target"))
	defer resp.Body.Close()

	// Then: CloudFront accepts the origin group as the cache behavior target
	helpers.AssertStatus(t, resp, http.StatusCreated)
	helpers.AssertRequestID(t, resp)
}

func TestCreateDistribution_callerReferenceIdempotent(t *testing.T) {
	// Given: a distribution already exists with a specific CallerReference
	srv := helpers.NewTestServer(t)
	dist1, _ := cfCreateAndParse(t, srv, "idempotent-ref")

	// When: CreateDistribution is called with the same CallerReference and identical config
	resp := cfCreate(t, srv, distributionConfigXML("idempotent-ref"))
	defer resp.Body.Close()

	// Then: 201 with the same distribution ID (idempotent return)
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)
	var dist2 parsedDist
	if err := xml.Unmarshal(b, &dist2); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	if dist2.ID != dist1.ID {
		t.Errorf("expected idempotent return of same distribution %q, got %q", dist1.ID, dist2.ID)
	}
}

func TestCreateDistribution_callerReferenceDuplicate(t *testing.T) {
	// Given: a distribution already exists with a specific CallerReference
	srv := helpers.NewTestServer(t)
	cfCreateAndParse(t, srv, "dup-ref")

	// When: CreateDistribution is called with the same CallerReference but different config
	resp := cfCreate(t, srv, disabledDistributionConfigXML("dup-ref"))
	defer resp.Body.Close()

	// Then: 409 DistributionAlreadyExists
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

// ─── GetDistribution ──────────────────────────────────────────────────────────

func TestGetDistribution_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "get-test-1")

	// When: GetDistribution is called
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID and ETag header
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header on GetDistribution")
	}
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	if dist.ID != created.ID {
		t.Errorf("expected Distribution.Id=%q, got %q", created.ID, dist.ID)
	}
}

func TestGetDistribution_notFound(t *testing.T) {
	// Given: no distributions
	srv := helpers.NewTestServer(t)

	// When: GetDistribution is called with a non-existent ID
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/ENONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

// ─── XML response fidelity (issue #76 / tracker #14, XML half only) ──────────
//
// AWS ground truth, established against the pinned CloudFront Smithy model
// (models/cloudfront/service/2020-05-31/cloudfront-2020-05-31.json) and the
// API reference before writing these tests:
//
//   - The service shape carries "aws.protocols#restXml": {} with no
//     noErrorWrapping — the plain (non-S3) rest-xml protocol, which wraps
//     errors as <ErrorResponse><Error><Type>…<Code>…<Message>…</Error><RequestId>…</RequestId></ErrorResponse>.
//     Route53 — the other non-S3 REST-XML service in this codebase — already
//     models this, per the error format table in CONTRIBUTING.md.
//   - The service shape also carries "smithy.api#xmlNamespace":
//     {"uri": "http://cloudfront.amazonaws.com/doc/2020-05-31/"}, so every
//     response root — success and error alike — carries that namespace as
//     its default xmlns. Nested elements inherit it and never repeat it.
//   - Distribution's members are modeled in the order Id, ARN, Status,
//     LastModifiedTime, InProgressInvalidationBatches, DomainName,
//     ActiveTrustedSigners, ActiveTrustedKeyGroups, DistributionConfig,
//     AliasICPRecordals, and rest-xml emits a structure's members in model
//     order.
//
// All three were divergences when these tests were first written: no root
// carried the namespace, every error came back in S3's bare <Error> format,
// and Distribution emitted DomainName before LastModifiedTime. #2010 fixed
// all three; the assertions below pin the fixed shape.

// cfXMLNS is the namespace the pinned model's service shape declares in
// smithy.api#xmlNamespace.
const cfXMLNS = "http://cloudfront.amazonaws.com/doc/2020-05-31/"

// rootElement returns the document's root start element, so a test can assert
// on its name and on the attributes declared on it.
func rootElement(t *testing.T, xmlBody []byte) xml.StartElement {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(xmlBody))
	for {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("no root element in body: %s", xmlBody)
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se
		}
	}
}

// assertRootNamespace fails unless the document root declares the CloudFront
// service namespace as its default xmlns.
func assertRootNamespace(t *testing.T, xmlBody []byte) {
	t.Helper()
	root := rootElement(t, xmlBody)
	for _, a := range root.Attr {
		if a.Name.Space == "" && a.Name.Local == "xmlns" {
			if a.Value != cfXMLNS {
				t.Errorf("root <%s> xmlns = %q, want %q", root.Name.Local, a.Value, cfXMLNS)
			}
			return
		}
	}
	t.Errorf("root <%s> declares no xmlns attribute; body: %s", root.Name.Local, xmlBody)
}

// assertModelOrder fails unless every name in want appears in names, in that
// relative order. names comes from topLevelElementNames, so this is an
// assertion about the order the service emitted its members in.
func assertModelOrder(t *testing.T, names, want []string) {
	t.Helper()
	prev := -1
	for _, name := range want {
		idx := indexOf(names, name)
		if idx < 0 {
			t.Errorf("expected element %q, got elements: %v", name, names)
			continue
		}
		if idx < prev {
			t.Errorf("element %q is out of model order, order was: %v (want %v)", name, names, want)
		}
		prev = idx
	}
}

// assertErrorEnvelope fails unless body is the rest-xml wrapped error
// envelope the model's protocol declares, carrying wantCode. It returns the
// Message so a caller can assert on its content.
func assertErrorEnvelope(t *testing.T, body []byte, wantCode string) string {
	t.Helper()
	if root := rootElement(t, body); root.Name.Local != "ErrorResponse" {
		t.Fatalf("error root = <%s>, want <ErrorResponse>; body: %s", root.Name.Local, body)
	}
	assertRootNamespace(t, body)
	assertModelOrder(t, topLevelElementNames(t, body), []string{"Error", "RequestId"})

	var env struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Type    string `xml:"Type"`
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
		RequestID string `xml:"RequestId"`
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal error envelope: %v\nbody: %s", err, body)
	}
	// Every CloudFront error shape in the pinned model carries
	// "smithy.api#error": "client", which rest-xml reports as Sender.
	if env.Error.Type != "Sender" {
		t.Errorf("Error/Type = %q, want %q", env.Error.Type, "Sender")
	}
	if env.Error.Code != wantCode {
		t.Errorf("Error/Code = %q, want %q", env.Error.Code, wantCode)
	}
	if env.RequestID == "" {
		t.Error("expected RequestId to be set in the error body")
	}
	return env.Error.Message
}

// topLevelElementNames walks xmlBody and returns the local names of the
// direct children of the document's root element, in document order. Used to
// check element presence/order without depending on Go's encoding/xml
// (which is name-addressed and does not expose sibling order).
func topLevelElementNames(t *testing.T, xmlBody []byte) []string {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(xmlBody))
	var names []string
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch se := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 2 {
				names = append(names, se.Name.Local)
			}
		case xml.EndElement:
			depth--
		}
	}
	return names
}

// indexOf returns the index of name in names, or -1.
func indexOf(names []string, name string) int {
	for i, n := range names {
		if n == name {
			return i
		}
	}
	return -1
}

func TestGetDistribution_xmlShape(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "xml-shape-test-1")

	// When: GetDistribution is called
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	defer resp.Body.Close()
	b := readBody(t, resp)

	// Then: the root element is named Distribution (per
	// API_GetDistribution.html's Response Syntax: "Root level tag for the
	// Distribution parameters")
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(b, &root); err != nil {
		t.Fatalf("unmarshal root: %v\nbody: %s", err, b)
	}
	if root.XMLName.Local != "Distribution" {
		t.Errorf("root element = %q, want %q", root.XMLName.Local, "Distribution")
	}

	// And: the root declares the service namespace
	assertRootNamespace(t, b)

	// And: the documented top-level elements are present, in the order the
	// pinned model declares Distribution's members
	names := topLevelElementNames(t, b)
	assertModelOrder(t, names, []string{
		"Id", "ARN", "Status", "LastModifiedTime",
		"InProgressInvalidationBatches", "DomainName", "DistributionConfig",
	})

	// And: the nested DistributionConfig inherits the namespace rather than
	// redeclaring it — only the root carries the attribute.
	if bytes.Count(b, []byte(cfXMLNS)) != 1 {
		t.Errorf("expected the namespace to be declared once, on the root; body: %s", b)
	}
}

// TestResponseRoots_carryServiceNamespace covers one operation per resource
// family, because the namespace is a property of the service shape rather
// than of any one response: a fix applied per-struct would leave the
// families nobody happened to test behind.
func TestResponseRoots_carryServiceNamespace(t *testing.T) {
	// Given: one distribution, one origin access control and one cache policy
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "xmlns-test-1")
	oac, _ := cfCreateOAC(t, srv, "xmlns-test-oac")
	policy, _ := cfCreateCachePolicy(t, srv, "xmlns-test-policy")

	tests := []struct {
		op       string
		path     string
		wantRoot string
	}{
		{"GetDistribution", "/2020-05-31/distribution/" + dist.ID, "Distribution"},
		{"GetDistributionConfig", "/2020-05-31/distribution/" + dist.ID + "/config", "DistributionConfig"},
		{"ListDistributions", "/2020-05-31/distribution", "DistributionList"},
		{"GetOriginAccessControl", "/2020-05-31/origin-access-control/" + oac.ID, "OriginAccessControl"},
		{"ListOriginAccessControls", "/2020-05-31/origin-access-control", "OriginAccessControlList"},
		{"GetCachePolicy", "/2020-05-31/cache-policy/" + policy.ID, "CachePolicy"},
		{"ListCachePolicies", "/2020-05-31/cache-policy", "CachePolicyList"},
	}
	for _, tc := range tests {
		t.Run(tc.op, func(t *testing.T) {
			// When: the operation is called
			req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			defer resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)
			b := readBody(t, resp)

			// Then: the root is the modeled element, carrying the namespace
			if root := rootElement(t, b); root.Name.Local != tc.wantRoot {
				t.Errorf("root element = %q, want %q", root.Name.Local, tc.wantRoot)
			}
			assertRootNamespace(t, b)
		})
	}
}

func TestGetDistribution_notFound_errorShape(t *testing.T) {
	// Given: no distributions
	srv := helpers.NewTestServer(t)

	// When: GetDistribution is called with a non-existent ID
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/ENONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404 (NoSuchDistribution carries "smithy.api#httpError": 404) with
	// a request ID header and the wrapped rest-xml error envelope naming the
	// unknown ID
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	helpers.AssertRequestID(t, resp)
	b := readBody(t, resp)
	msg := assertErrorEnvelope(t, b, "NoSuchDistribution")
	if !strings.Contains(msg, "ENONEXISTENT") {
		t.Errorf("expected Message to name the unknown ID, got: %q", msg)
	}
}

// TestUpdateDistribution_missingIfMatch_errorShape pins the envelope on a
// validation error as well as on a not-found: the two travel different
// paths through the handlers, and only the writer they share makes the
// shape uniform.
func TestUpdateDistribution_missingIfMatch_errorShape(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "errshape-nomatch-1")

	// When: UpdateDistribution is called without the required If-Match header
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/config",
		bytes.NewReader([]byte(disabledDistributionConfigXML("errshape-nomatch-1"))))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 400 (InvalidIfMatchVersion carries "smithy.api#httpError": 400)
	// in the same wrapped envelope
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertRequestID(t, resp)
	assertErrorEnvelope(t, readBody(t, resp), "InvalidIfMatchVersion")
}

// ─── GetDistributionConfig ────────────────────────────────────────────────────

func TestGetDistributionConfig_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "getconfig-test-1")

	// When: GetDistributionConfig is called
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/"+created.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetDistributionConfig: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with DistributionConfig and ETag header
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName         xml.Name `xml:"DistributionConfig"`
		CallerReference string   `xml:"CallerReference"`
		Comment         string   `xml:"Comment"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal DistributionConfig: %v\nbody: %s", err, b)
	}
	if cfg.CallerReference != "getconfig-test-1" {
		t.Errorf("expected CallerReference=getconfig-test-1, got %q", cfg.CallerReference)
	}
}

// ─── UpdateDistribution ───────────────────────────────────────────────────────

func TestUpdateDistribution_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, etag := cfCreateAndParse(t, srv, "update-test-1")

	// When: UpdateDistribution is called with If-Match and updated config
	updateBody := disabledDistributionConfigXML("update-test-1")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/config",
		bytes.NewReader([]byte(updateBody)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with new ETag (different from original)
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == "" {
		t.Error("expected ETag header on update response")
	}
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestUpdateDistribution_missingIfMatch(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "update-nomatch-1")

	// When: UpdateDistribution is called without If-Match header
	updateBody := disabledDistributionConfigXML("update-nomatch-1")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/config",
		bytes.NewReader([]byte(updateBody)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 400 InvalidIfMatchVersion
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestUpdateDistribution_etagMismatch(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "update-mismatch-1")

	// When: UpdateDistribution is called with a stale ETag
	updateBody := disabledDistributionConfigXML("update-mismatch-1")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/config",
		bytes.NewReader([]byte(updateBody)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", `"99999"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 412 PreconditionFailed
	helpers.AssertStatus(t, resp, http.StatusPreconditionFailed)
}

// ─── ListDistributions ────────────────────────────────────────────────────────

func TestListDistributions_success(t *testing.T) {
	// Given: two distributions with different caller references
	srv := helpers.NewTestServer(t)
	cfCreateAndParse(t, srv, "list-test-1")
	cfCreateAndParse(t, srv, "list-test-2")

	// When: ListDistributions is called
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListDistributions: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with DistributionList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"DistributionList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal DistributionList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

func TestListDistributions_empty(t *testing.T) {
	// Given: no distributions
	srv := helpers.NewTestServer(t)

	// When: ListDistributions is called
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListDistributions: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with Quantity=0
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"DistributionList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal DistributionList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 0 {
		t.Errorf("expected Quantity=0, got %d", result.Quantity)
	}
}

// ─── DeleteDistribution ───────────────────────────────────────────────────────

func TestDeleteDistribution_success(t *testing.T) {
	// Given: an existing disabled distribution
	srv := helpers.NewTestServer(t)
	created, etag := cfCreateAndParse(t, srv, "delete-test-1")

	// First disable the distribution (required before delete)
	updateBody := disabledDistributionConfigXML("delete-test-1")
	updateReq, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/config",
		bytes.NewReader([]byte(updateBody)))
	updateReq.Header.Set("Content-Type", "application/xml")
	updateReq.Header.Set("If-Match", etag)
	updateResp, err := http.DefaultClient.Do(updateReq)
	if err != nil {
		t.Fatalf("disable distribution: %v", err)
	}
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	newETag := updateResp.Header.Get("ETag")

	// When: DeleteDistribution is called with the new ETag
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	req.Header.Set("If-Match", newETag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent GET returns 404
	getReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetDistribution after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestDeleteDistribution_notDisabled(t *testing.T) {
	// Given: an enabled distribution
	srv := helpers.NewTestServer(t)
	created, etag := cfCreateAndParse(t, srv, "delete-notdisabled-1")

	// When: DeleteDistribution is called (distribution is still enabled)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 409 DistributionNotDisabled
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestDeleteDistribution_missingIfMatch(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "delete-nomatch-1")

	// When: DeleteDistribution is called without If-Match
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/2020-05-31/distribution/"+created.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteDistribution: %v", err)
	}
	defer resp.Body.Close()

	// Then: 400 InvalidIfMatchVersion
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

// ─── CreateInvalidation ───────────────────────────────────────────────────────

func TestCreateInvalidation_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "inv-test-1")

	// When: CreateInvalidation is called
	body := `<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>inv-ref-1</CallerReference>
  <Paths><Quantity>1</Quantity><Items><Path>/*</Path></Items></Paths>
</InvalidationBatch>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/invalidation",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with Invalidation element containing Id, Status=Completed, and Location header
	helpers.AssertStatus(t, resp, http.StatusCreated)
	if resp.Header.Get("Location") == "" {
		t.Error("expected Location header")
	}
	b := readBody(t, resp)
	var inv struct {
		XMLName xml.Name `xml:"Invalidation"`
		ID      string   `xml:"Id"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal Invalidation: %v\nbody: %s", err, b)
	}
	if inv.ID == "" {
		t.Error("expected Invalidation.Id to be set")
	}
	if inv.Status != "Completed" {
		t.Errorf("expected Status=Completed, got %q", inv.Status)
	}
}

func TestCreateInvalidation_distNotFound(t *testing.T) {
	// Given: no distributions
	srv := helpers.NewTestServer(t)

	// When: CreateInvalidation is called for a non-existent distribution
	body := `<InvalidationBatch><CallerReference>test</CallerReference><Paths><Quantity>1</Quantity><Items><Path>/*</Path></Items></Paths></InvalidationBatch>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/ENONEXISTENT/invalidation",
		bytes.NewReader([]byte(body)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetInvalidation_success(t *testing.T) {
	// Given: a distribution with an invalidation
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "getinv-test-1")
	invID := cfCreateInvalidation(t, srv, dist.ID, "getinv-ref-1")

	// When: GetInvalidation is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distribution/"+dist.ID+"/invalidation/"+invID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID
	helpers.AssertStatus(t, resp, http.StatusOK)
	b := readBody(t, resp)
	var inv struct {
		XMLName xml.Name `xml:"Invalidation"`
		ID      string   `xml:"Id"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal Invalidation: %v\nbody: %s", err, b)
	}
	if inv.ID != invID {
		t.Errorf("expected ID=%q, got %q", invID, inv.ID)
	}
}

func TestGetInvalidation_notFound(t *testing.T) {
	// Given: a distribution with no invalidations
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "getinv-nf-1")

	// When: GetInvalidation is called with a non-existent invalidation ID
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distribution/"+dist.ID+"/invalidation/INONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestListInvalidations_success(t *testing.T) {
	// Given: a distribution with two invalidations
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "listinv-test-1")
	cfCreateInvalidation(t, srv, dist.ID, "listinv-ref-1")
	cfCreateInvalidation(t, srv, dist.ID, "listinv-ref-2")

	// When: ListInvalidations is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distribution/"+dist.ID+"/invalidation", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListInvalidations: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with InvalidationList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"InvalidationList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal InvalidationList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── Tag invalidations ───────────────────────────────────────────────────────

func TestCreateInvalidation_tag_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "tag-inv-test-1")

	// When: CreateInvalidation is called with a tag path
	body := `<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>tag-inv-ref-1</CallerReference>
  <Paths><Quantity>1</Quantity><Items><Path>#product:electronics</Path></Items></Paths>
</InvalidationBatch>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/invalidation",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with Invalidation element containing Id, Status=Completed, and Location header
	helpers.AssertStatus(t, resp, http.StatusCreated)
	if resp.Header.Get("Location") == "" {
		t.Error("expected Location header")
	}
	b := readBody(t, resp)
	var inv struct {
		XMLName xml.Name `xml:"Invalidation"`
		ID      string   `xml:"Id"`
		Status  string   `xml:"Status"`
	}
	if err := xml.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal Invalidation: %v\nbody: %s", err, b)
	}
	if inv.ID == "" {
		t.Error("expected Invalidation.Id to be set")
	}
	if inv.Status != "Completed" {
		t.Errorf("expected Status=Completed, got %q", inv.Status)
	}
}

func TestCreateInvalidation_mixedPathsAndTags(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "mixed-test-1")

	// When: CreateInvalidation is called with both path and tag patterns
	body := `<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>mixed-ref-1</CallerReference>
  <Paths><Quantity>3</Quantity><Items>
    <Path>/index.html</Path>
    <Path>#user1</Path>
    <Path>/images/*</Path>
  </Items></Paths>
</InvalidationBatch>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/invalidation",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with success
	helpers.AssertStatus(t, resp, http.StatusCreated)
}

func TestCreateInvalidation_invalidTag(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	created, _ := cfCreateAndParse(t, srv, "badtag-test-1")

	// When: CreateInvalidation is called with an invalid tag (contains space)
	body := `<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>badtag-ref-1</CallerReference>
  <Paths><Quantity>1</Quantity><Items><Path>#invalid tag here</Path></Items></Paths>
</InvalidationBatch>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution/"+created.ID+"/invalidation",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateInvalidation: %v", err)
	}
	defer resp.Body.Close()

	// Then: 400 InvalidArgument
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	assertErrorEnvelope(t, readBody(t, resp), "InvalidArgument")
}

// ─── Tagging ──────────────────────────────────────────────────────────────────

func TestTagResource_success(t *testing.T) {
	// Given: an existing distribution
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "tag-test-1")

	// When: TagResource is called
	body := `<?xml version="1.0" encoding="UTF-8"?>
<Tags xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items>
    <Tag><Key>env</Key><Value>test</Value></Tag>
    <Tag><Key>team</Key><Value>platform</Value></Tag>
  </Items>
</Tags>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Tag&Resource="+dist.ARN,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("TagResource: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)
}

func TestListTagsForResource_success(t *testing.T) {
	// Given: a distribution with tags
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "listtag-test-1")
	cfTagResource(t, srv, dist.ARN, "env", "test")

	// When: ListTagsForResource is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/tagging?Resource="+dist.ARN, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with Tagging element containing tags
	helpers.AssertStatus(t, resp, http.StatusOK)
	b := readBody(t, resp)
	var result struct {
		XMLName xml.Name `xml:"Tagging"`
		Tags    struct {
			Items []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Items>Tag"`
		} `xml:"Tags"`
	}
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal Tagging: %v\nbody: %s", err, b)
	}
	if len(result.Tags.Items) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(result.Tags.Items))
	}
	if result.Tags.Items[0].Key != "env" || result.Tags.Items[0].Value != "test" {
		t.Errorf("expected tag env=test, got %s=%s", result.Tags.Items[0].Key, result.Tags.Items[0].Value)
	}
}

func TestListTagsForResource_empty(t *testing.T) {
	// Given: a distribution with no tags
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "listtag-empty-1")

	// When: ListTagsForResource is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/tagging?Resource="+dist.ARN, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with empty Tags
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestUntagResource_success(t *testing.T) {
	// Given: a distribution with two tags
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateAndParse(t, srv, "untag-test-1")
	cfTagResource(t, srv, dist.ARN, "env", "test")
	cfTagResource(t, srv, dist.ARN, "team", "platform")

	// When: UntagResource removes one tag
	body := `<?xml version="1.0" encoding="UTF-8"?>
<TagKeys xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Items><Key>env</Key></Items>
</TagKeys>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/tagging?Operation=Untag&Resource="+dist.ARN,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UntagResource: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: ListTagsForResource returns only the remaining tag
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/tagging?Resource="+dist.ARN, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)
	b := readBody(t, getResp)
	var result struct {
		XMLName xml.Name `xml:"Tagging"`
		Tags    struct {
			Items []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Items>Tag"`
		} `xml:"Tags"`
	}
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal Tagging: %v\nbody: %s", err, b)
	}
	if len(result.Tags.Items) != 1 {
		t.Fatalf("expected 1 tag remaining, got %d", len(result.Tags.Items))
	}
	if result.Tags.Items[0].Key != "team" {
		t.Errorf("expected remaining tag key=team, got %q", result.Tags.Items[0].Key)
	}
}

// ─── CreateDistributionWithTags ───────────────────────────────────────────────

func TestCreateDistributionWithTags_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDistributionWithTags is called
	body := distributionConfigWithTagsXML("withtags-test-1", "env", "staging")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution?WithTags",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateDistributionWithTags: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with Distribution
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	if dist.ID == "" {
		t.Error("expected Distribution.Id to be set")
	}

	// And: tags are stored
	tagReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/tagging?Resource="+dist.ARN, nil)
	tagResp, err := http.DefaultClient.Do(tagReq)
	if err != nil {
		t.Fatalf("ListTagsForResource: %v", err)
	}
	defer tagResp.Body.Close()
	helpers.AssertStatus(t, tagResp, http.StatusOK)
	tb := readBody(t, tagResp)
	var tagging struct {
		XMLName xml.Name `xml:"Tagging"`
		Tags    struct {
			Items []struct {
				Key   string `xml:"Key"`
				Value string `xml:"Value"`
			} `xml:"Items>Tag"`
		} `xml:"Tags"`
	}
	if err := xml.Unmarshal(tb, &tagging); err != nil {
		t.Fatalf("unmarshal Tagging: %v\nbody: %s", err, tb)
	}
	if len(tagging.Tags.Items) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tagging.Tags.Items))
	}
}

func TestCreateDistributionWithTags_customID(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDistributionWithTags is called with _custom_id_ tag
	body := distributionConfigWithTagsXML("customid-test-1", "_custom_id_", "ECUSTOM1234567")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution?WithTags",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateDistributionWithTags: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with the custom distribution ID
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	if dist.ID != "ECUSTOM1234567" {
		t.Errorf("expected ID=ECUSTOM1234567, got %q", dist.ID)
	}
}

// ─── Origin Access Control ────────────────────────────────────────────────────

func TestCreateOriginAccessControl_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateOriginAccessControl is called
	body := oacConfigXML("test-oac", "sigv4", "always", "s3")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-access-control",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOriginAccessControl: %v", err)
	}
	defer resp.Body.Close()

	// Then: 201 with OriginAccessControl and ETag
	helpers.AssertStatus(t, resp, http.StatusCreated)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	if resp.Header.Get("Location") == "" {
		t.Error("expected Location header")
	}
	b := readBody(t, resp)
	var oac parsedOAC
	if err := xml.Unmarshal(b, &oac); err != nil {
		t.Fatalf("unmarshal OriginAccessControl: %v\nbody: %s", err, b)
	}
	if oac.ID == "" {
		t.Error("expected OriginAccessControl.Id to be set")
	}
}

func TestGetOriginAccessControl_success(t *testing.T) {
	// Given: an existing OAC
	srv := helpers.NewTestServer(t)
	oac, _ := cfCreateOAC(t, srv, "get-oac-1")

	// When: GetOriginAccessControl is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-control/"+oac.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOriginAccessControl: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var got parsedOAC
	if err := xml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal OriginAccessControl: %v\nbody: %s", err, b)
	}
	if got.ID != oac.ID {
		t.Errorf("expected ID=%q, got %q", oac.ID, got.ID)
	}
}

func TestGetOriginAccessControl_notFound(t *testing.T) {
	// Given: no OACs
	srv := helpers.NewTestServer(t)

	// When: GetOriginAccessControl is called for a non-existent ID
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-control/ENONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOriginAccessControl: %v", err)
	}
	defer resp.Body.Close()

	// Then: 404
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateOriginAccessControl_success(t *testing.T) {
	// Given: an existing OAC
	srv := helpers.NewTestServer(t)
	oac, etag := cfCreateOAC(t, srv, "update-oac-1")

	// When: UpdateOriginAccessControl is called
	body := oacConfigXML("updated-oac", "sigv4", "never", "s3")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/origin-access-control/"+oac.ID+"/config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateOriginAccessControl: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with updated ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteOriginAccessControl_success(t *testing.T) {
	// Given: an existing OAC
	srv := helpers.NewTestServer(t)
	oac, etag := cfCreateOAC(t, srv, "delete-oac-1")

	// When: DeleteOriginAccessControl is called
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/origin-access-control/"+oac.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteOriginAccessControl: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent GET returns 404
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-control/"+oac.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetOriginAccessControl after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListOriginAccessControls_success(t *testing.T) {
	// Given: two OACs
	srv := helpers.NewTestServer(t)
	cfCreateOAC(t, srv, "listoac-1")
	cfCreateOAC(t, srv, "listoac-2")

	// When: ListOriginAccessControls is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-control", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListOriginAccessControls: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with OriginAccessControlList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"OriginAccessControlList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal OriginAccessControlList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── Cache Policy helpers ─────────────────────────────────────────────────────

// cachePolicyConfigXML returns a CachePolicyConfig XML body.
func cachePolicyConfigXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CachePolicyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <Comment>test cache policy</Comment>
  <MinTTL>0</MinTTL>
  <DefaultTTL>86400</DefaultTTL>
  <MaxTTL>31536000</MaxTTL>
  <ParametersInCacheKeyAndForwardedToOrigin>
    <EnableAcceptEncodingGzip>true</EnableAcceptEncodingGzip>
    <EnableAcceptEncodingBrotli>true</EnableAcceptEncodingBrotli>
    <CookiesConfig><CookieBehavior>none</CookieBehavior></CookiesConfig>
    <HeadersConfig><HeaderBehavior>none</HeaderBehavior></HeadersConfig>
    <QueryStringsConfig><QueryStringBehavior>none</QueryStringBehavior></QueryStringsConfig>
  </ParametersInCacheKeyAndForwardedToOrigin>
</CachePolicyConfig>`, name)
}

type parsedCachePolicy struct {
	XMLName xml.Name `xml:"CachePolicy"`
	ID      string   `xml:"Id"`
}

func cfCreateCachePolicy(t *testing.T, srv *helpers.TestServer, name string) (parsedCachePolicy, string) {
	t.Helper()
	body := cachePolicyConfigXML(name)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/cache-policy",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateCachePolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b2 := readBody(t, resp)
	var cp parsedCachePolicy
	if err := xml.Unmarshal(b2, &cp); err != nil {
		t.Fatalf("unmarshal CachePolicy: %v\nbody: %s", err, b2)
	}
	return cp, etag
}

// ─── Cache Policy Tests ───────────────────────────────────────────────────────

func TestCreateCachePolicy_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateCachePolicy is called
	cp, etag := cfCreateCachePolicy(t, srv, "test-cache-policy")

	// Then: ID and ETag are set
	if cp.ID == "" {
		t.Error("expected CachePolicy.Id to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetCachePolicy_success(t *testing.T) {
	// Given: an existing cache policy
	srv := helpers.NewTestServer(t)
	cp, _ := cfCreateCachePolicy(t, srv, "get-cp-1")

	// When: GetCachePolicy is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/cache-policy/"+cp.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetCachePolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b2 := readBody(t, resp)
	var got parsedCachePolicy
	if err := xml.Unmarshal(b2, &got); err != nil {
		t.Fatalf("unmarshal CachePolicy: %v\nbody: %s", err, b2)
	}
	if got.ID != cp.ID {
		t.Errorf("expected ID=%q, got %q", cp.ID, got.ID)
	}
}

func TestGetCachePolicy_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/cache-policy/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetCachePolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateCachePolicy_success(t *testing.T) {
	// Given: an existing cache policy
	srv := helpers.NewTestServer(t)
	cp, etag := cfCreateCachePolicy(t, srv, "update-cp-1")

	// When: UpdateCachePolicy is called
	body := cachePolicyConfigXML("update-cp-1-updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/cache-policy/"+cp.ID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateCachePolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with updated ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteCachePolicy_success(t *testing.T) {
	// Given: an existing cache policy
	srv := helpers.NewTestServer(t)
	cp, etag := cfCreateCachePolicy(t, srv, "delete-cp-1")

	// When: DeleteCachePolicy is called
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/cache-policy/"+cp.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteCachePolicy: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent GET returns 404
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/cache-policy/"+cp.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetCachePolicy after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListCachePolicies_success(t *testing.T) {
	// Given: two cache policies
	srv := helpers.NewTestServer(t)
	cfCreateCachePolicy(t, srv, "listcp-1")
	cfCreateCachePolicy(t, srv, "listcp-2")

	// When: ListCachePolicies is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/cache-policy", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListCachePolicies: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with CachePolicyList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"CachePolicyList"`
		Quantity int      `xml:"Quantity"`
	}
	b2 := readBody(t, resp)
	if err := xml.Unmarshal(b2, &result); err != nil {
		t.Fatalf("unmarshal CachePolicyList: %v\nbody: %s", err, b2)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

func TestGetCachePolicyConfig_success(t *testing.T) {
	// Given: an existing cache policy
	srv := helpers.NewTestServer(t)
	cp, _ := cfCreateCachePolicy(t, srv, "getconfig-cp-1")

	// When: GetCachePolicyConfig is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/cache-policy/"+cp.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetCachePolicyConfig: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with CachePolicyConfig and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b2 := readBody(t, resp)
	var cfg struct {
		XMLName xml.Name `xml:"CachePolicyConfig"`
		Name    string   `xml:"Name"`
	}
	if err := xml.Unmarshal(b2, &cfg); err != nil {
		t.Fatalf("unmarshal CachePolicyConfig: %v\nbody: %s", err, b2)
	}
	if cfg.Name != "getconfig-cp-1" {
		t.Errorf("expected Name=getconfig-cp-1, got %q", cfg.Name)
	}
}

// ─── Origin Request Policy helpers ────────────────────────────────────────────

func originRequestPolicyConfigXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<OriginRequestPolicyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <Comment>test origin request policy</Comment>
</OriginRequestPolicyConfig>`, name)
}

type parsedOriginRequestPolicy struct {
	XMLName xml.Name `xml:"OriginRequestPolicy"`
	ID      string   `xml:"Id"`
}

// originRequestPolicyConfigWithBehaviorsXML returns an OriginRequestPolicyConfig
// body that exercises the real wire shape: each of HeadersConfig, CookiesConfig,
// and QueryStringsConfig carries a required *Behavior member alongside its list.
func originRequestPolicyConfigWithBehaviorsXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<OriginRequestPolicyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <Comment>full origin request policy</Comment>
  <HeadersConfig>
    <HeaderBehavior>whitelist</HeaderBehavior>
    <Headers><Quantity>1</Quantity><Items><Item>X-Custom-Header</Item></Items></Headers>
  </HeadersConfig>
  <CookiesConfig>
    <CookieBehavior>whitelist</CookieBehavior>
    <Cookies><Quantity>1</Quantity><Items><Item>session-id</Item></Items></Cookies>
  </CookiesConfig>
  <QueryStringsConfig>
    <QueryStringBehavior>whitelist</QueryStringBehavior>
    <QueryStrings><Quantity>1</Quantity><Items><Item>utm_source</Item></Items></QueryStrings>
  </QueryStringsConfig>
</OriginRequestPolicyConfig>`, name)
}

// parsedOriginRequestPolicyFull captures the modeled response shape for an
// OriginRequestPolicy, including the behavior members that a bare StringList
// would silently drop.
type parsedOriginRequestPolicyFull struct {
	XMLName xml.Name `xml:"OriginRequestPolicy"`
	ID      string   `xml:"Id"`
	Config  struct {
		Name          string `xml:"Name"`
		HeadersConfig struct {
			HeaderBehavior string   `xml:"HeaderBehavior"`
			Headers        []string `xml:"Headers>Items>Item"`
		} `xml:"HeadersConfig"`
		CookiesConfig struct {
			CookieBehavior string   `xml:"CookieBehavior"`
			Cookies        []string `xml:"Cookies>Items>Item"`
		} `xml:"CookiesConfig"`
		QueryStringsConfig struct {
			QueryStringBehavior string   `xml:"QueryStringBehavior"`
			QueryStrings        []string `xml:"QueryStrings>Items>Item"`
		} `xml:"QueryStringsConfig"`
	} `xml:"OriginRequestPolicyConfig"`
}

func cfCreateOriginRequestPolicy(t *testing.T, srv *helpers.TestServer, name string) (parsedOriginRequestPolicy, string) {
	t.Helper()
	body := originRequestPolicyConfigXML(name)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-request-policy",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b2 := readBody(t, resp)
	var p parsedOriginRequestPolicy
	if err := xml.Unmarshal(b2, &p); err != nil {
		t.Fatalf("unmarshal OriginRequestPolicy: %v\nbody: %s", err, b2)
	}
	return p, etag
}

// ─── Origin Request Policy Tests ──────────────────────────────────────────────

func TestCreateOriginRequestPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateOriginRequestPolicy(t, srv, "test-orp")
	if p.ID == "" {
		t.Error("expected OriginRequestPolicy.Id to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetOriginRequestPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, _ := cfCreateOriginRequestPolicy(t, srv, "get-orp-1")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-request-policy/"+p.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestGetOriginRequestPolicy_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-request-policy/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateOriginRequestPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateOriginRequestPolicy(t, srv, "update-orp-1")

	body := originRequestPolicyConfigXML("update-orp-1-updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/origin-request-policy/"+p.ID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteOriginRequestPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateOriginRequestPolicy(t, srv, "delete-orp-1")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/origin-request-policy/"+p.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-request-policy/"+p.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetOriginRequestPolicy after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListOriginRequestPolicies_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateOriginRequestPolicy(t, srv, "listorp-1")
	cfCreateOriginRequestPolicy(t, srv, "listorp-2")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-request-policy", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListOriginRequestPolicies: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"OriginRequestPolicyList"`
		Quantity int      `xml:"Quantity"`
	}
	b2 := readBody(t, resp)
	if err := xml.Unmarshal(b2, &result); err != nil {
		t.Fatalf("unmarshal OriginRequestPolicyList: %v\nbody: %s", err, b2)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// TestCreateOriginRequestPolicy_behaviorsAndLists round-trips an origin request
// policy whose HeadersConfig/CookiesConfig/QueryStringsConfig carry the modeled
// *Behavior member alongside their lists. Overcast used to model these as bare
// StringList{Quantity,Items}, which silently dropped the behavior and mismatched
// the real OriginRequestPolicyHeadersConfig/CookiesConfig/QueryStringsConfig shapes.
func TestCreateOriginRequestPolicy_behaviorsAndLists(t *testing.T) {
	srv := helpers.NewTestServer(t)
	body := originRequestPolicyConfigWithBehaviorsXML("orp-behaviors-1")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-request-policy",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOriginRequestPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)

	// The response XML must use the modeled element names: a HeaderBehavior /
	// CookieBehavior / QueryStringBehavior member, not a bare Quantity+Items list.
	for _, want := range []string{
		"<HeaderBehavior>whitelist</HeaderBehavior>",
		"<CookieBehavior>whitelist</CookieBehavior>",
		"<QueryStringBehavior>whitelist</QueryStringBehavior>",
	} {
		if !bytes.Contains(b, []byte(want)) {
			t.Errorf("expected response XML to contain %q\nbody: %s", want, b)
		}
	}

	var p parsedOriginRequestPolicyFull
	if err := xml.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal OriginRequestPolicy: %v\nbody: %s", err, b)
	}
	if p.Config.HeadersConfig.HeaderBehavior != "whitelist" {
		t.Errorf("expected HeaderBehavior=whitelist, got %q", p.Config.HeadersConfig.HeaderBehavior)
	}
	if got := p.Config.HeadersConfig.Headers; len(got) != 1 || got[0] != "X-Custom-Header" {
		t.Errorf("expected Headers=[X-Custom-Header], got %v", got)
	}
	if p.Config.CookiesConfig.CookieBehavior != "whitelist" {
		t.Errorf("expected CookieBehavior=whitelist, got %q", p.Config.CookiesConfig.CookieBehavior)
	}
	if got := p.Config.CookiesConfig.Cookies; len(got) != 1 || got[0] != "session-id" {
		t.Errorf("expected Cookies=[session-id], got %v", got)
	}
	if p.Config.QueryStringsConfig.QueryStringBehavior != "whitelist" {
		t.Errorf("expected QueryStringBehavior=whitelist, got %q", p.Config.QueryStringsConfig.QueryStringBehavior)
	}
	if got := p.Config.QueryStringsConfig.QueryStrings; len(got) != 1 || got[0] != "utm_source" {
		t.Errorf("expected QueryStrings=[utm_source], got %v", got)
	}

	// GetOriginRequestPolicyConfig must round-trip the same shape.
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-request-policy/"+p.ID+"/config", nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetOriginRequestPolicyConfig: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)
	getBody := readBody(t, getResp)
	if !bytes.Contains(getBody, []byte("<HeaderBehavior>whitelist</HeaderBehavior>")) {
		t.Errorf("expected config XML to contain HeaderBehavior\nbody: %s", getBody)
	}
}

// ─── Response Headers Policy helpers ──────────────────────────────────────────

func responseHeadersPolicyConfigXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ResponseHeadersPolicyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <Comment>test response headers policy</Comment>
</ResponseHeadersPolicyConfig>`, name)
}

type parsedResponseHeadersPolicy struct {
	XMLName xml.Name `xml:"ResponseHeadersPolicy"`
	ID      string   `xml:"Id"`
}

func cfCreateResponseHeadersPolicy(t *testing.T, srv *helpers.TestServer, name string) (parsedResponseHeadersPolicy, string) {
	t.Helper()
	body := responseHeadersPolicyConfigXML(name)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/response-headers-policy",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateResponseHeadersPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b2 := readBody(t, resp)
	var p parsedResponseHeadersPolicy
	if err := xml.Unmarshal(b2, &p); err != nil {
		t.Fatalf("unmarshal ResponseHeadersPolicy: %v\nbody: %s", err, b2)
	}
	return p, etag
}

// ─── Response Headers Policy Tests ────────────────────────────────────────────

func TestCreateResponseHeadersPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateResponseHeadersPolicy(t, srv, "test-rhp")
	if p.ID == "" {
		t.Error("expected ResponseHeadersPolicy.Id to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetResponseHeadersPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, _ := cfCreateResponseHeadersPolicy(t, srv, "get-rhp-1")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/response-headers-policy/"+p.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetResponseHeadersPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestGetResponseHeadersPolicy_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/response-headers-policy/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetResponseHeadersPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateResponseHeadersPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateResponseHeadersPolicy(t, srv, "update-rhp-1")

	body := responseHeadersPolicyConfigXML("update-rhp-1-updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/response-headers-policy/"+p.ID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateResponseHeadersPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteResponseHeadersPolicy_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateResponseHeadersPolicy(t, srv, "delete-rhp-1")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/response-headers-policy/"+p.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteResponseHeadersPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/response-headers-policy/"+p.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetResponseHeadersPolicy after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListResponseHeadersPolicies_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateResponseHeadersPolicy(t, srv, "listrhp-1")
	cfCreateResponseHeadersPolicy(t, srv, "listrhp-2")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/response-headers-policy", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListResponseHeadersPolicies: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"ResponseHeadersPolicyList"`
		Quantity int      `xml:"Quantity"`
	}
	b2 := readBody(t, resp)
	if err := xml.Unmarshal(b2, &result); err != nil {
		t.Fatalf("unmarshal ResponseHeadersPolicyList: %v\nbody: %s", err, b2)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── Legacy OAI helpers ───────────────────────────────────────────────────────

func oaiConfigXML(callerRef, comment string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CloudFrontOriginAccessIdentityConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>%s</Comment>
</CloudFrontOriginAccessIdentityConfig>`, callerRef, comment)
}

type parsedOAI struct {
	XMLName           xml.Name `xml:"CloudFrontOriginAccessIdentity"`
	ID                string   `xml:"Id"`
	S3CanonicalUserId string   `xml:"S3CanonicalUserId"`
}

func cfCreateOAI(t *testing.T, srv *helpers.TestServer, callerRef, comment string) (parsedOAI, string) {
	t.Helper()
	body := oaiConfigXML(callerRef, comment)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b2 := readBody(t, resp)
	var oai parsedOAI
	if err := xml.Unmarshal(b2, &oai); err != nil {
		t.Fatalf("unmarshal OAI: %v\nbody: %s", err, b2)
	}
	return oai, etag
}

// ─── Legacy OAI Tests ─────────────────────────────────────────────────────────

func TestCreateCloudFrontOriginAccessIdentity_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	oai, etag := cfCreateOAI(t, srv, "oai-create-1", "test OAI")
	if oai.ID == "" {
		t.Error("expected OAI.Id to be set")
	}
	if oai.S3CanonicalUserId == "" {
		t.Error("expected S3CanonicalUserId to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestCreateCloudFrontOriginAccessIdentity_missingCallerRef(t *testing.T) {
	srv := helpers.NewTestServer(t)
	body := `<CloudFrontOriginAccessIdentityConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference></CallerReference>
  <Comment>test</Comment>
</CloudFrontOriginAccessIdentityConfig>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
}

func TestGetCloudFrontOriginAccessIdentity_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	oai, _ := cfCreateOAI(t, srv, "oai-get-1", "test OAI")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/"+oai.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b2 := readBody(t, resp)
	var got parsedOAI
	if err := xml.Unmarshal(b2, &got); err != nil {
		t.Fatalf("unmarshal OAI: %v\nbody: %s", err, b2)
	}
	if got.ID != oai.ID {
		t.Errorf("expected ID=%q, got %q", oai.ID, got.ID)
	}
}

func TestGetCloudFrontOriginAccessIdentity_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/ENONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetCloudFrontOriginAccessIdentityConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	oai, _ := cfCreateOAI(t, srv, "oai-getconfig-1", "test OAI config")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/"+oai.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetOAIConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b2 := readBody(t, resp)
	var cfg struct {
		XMLName         xml.Name `xml:"CloudFrontOriginAccessIdentityConfig"`
		CallerReference string   `xml:"CallerReference"`
		Comment         string   `xml:"Comment"`
	}
	if err := xml.Unmarshal(b2, &cfg); err != nil {
		t.Fatalf("unmarshal OAIConfig: %v\nbody: %s", err, b2)
	}
	if cfg.CallerReference != "oai-getconfig-1" {
		t.Errorf("expected CallerReference=oai-getconfig-1, got %q", cfg.CallerReference)
	}
}

func TestUpdateCloudFrontOriginAccessIdentity_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	oai, etag := cfCreateOAI(t, srv, "oai-update-1", "original")

	body := oaiConfigXML("oai-update-1", "updated comment")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/"+oai.ID+"/config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteCloudFrontOriginAccessIdentity_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	oai, etag := cfCreateOAI(t, srv, "oai-delete-1", "test OAI")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/"+oai.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteOAI: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront/"+oai.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetOAI after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListCloudFrontOriginAccessIdentities_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateOAI(t, srv, "oai-list-1", "test OAI 1")
	cfCreateOAI(t, srv, "oai-list-2", "test OAI 2")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/origin-access-identity/cloudfront", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListOAIs: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"CloudFrontOriginAccessIdentityList"`
		Quantity int      `xml:"Quantity"`
	}
	b2 := readBody(t, resp)
	if err := xml.Unmarshal(b2, &result); err != nil {
		t.Fatalf("unmarshal OAI list: %v\nbody: %s", err, b2)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── Key Group Helpers ────────────────────────────────────────────────────────

func keyGroupConfigXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<KeyGroupConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <Comment>test key group</Comment>
  <Items><PublicKey>K1234567890ABC</PublicKey></Items>
</KeyGroupConfig>`, name)
}

type parsedKeyGroup struct {
	XMLName xml.Name `xml:"KeyGroup"`
	ID      string   `xml:"Id"`
}

func cfCreateKeyGroup(t *testing.T, srv *helpers.TestServer, name string) (parsedKeyGroup, string) {
	t.Helper()
	body := keyGroupConfigXML(name)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/key-group",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateKeyGroup: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var kg parsedKeyGroup
	if err := xml.Unmarshal(b, &kg); err != nil {
		t.Fatalf("unmarshal KeyGroup: %v\nbody: %s", err, b)
	}
	return kg, etag
}

// ─── Key Group Tests ──────────────────────────────────────────────────────────

func TestCreateKeyGroup_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateKeyGroup is called
	kg, etag := cfCreateKeyGroup(t, srv, "test-kg-1")

	// Then: ID and ETag are set
	if kg.ID == "" {
		t.Error("expected KeyGroup.Id to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetKeyGroup_success(t *testing.T) {
	// Given: an existing key group
	srv := helpers.NewTestServer(t)
	kg, _ := cfCreateKeyGroup(t, srv, "get-kg-1")

	// When: GetKeyGroup is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/key-group/"+kg.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetKeyGroup: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var got parsedKeyGroup
	if err := xml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal KeyGroup: %v\nbody: %s", err, b)
	}
	if got.ID != kg.ID {
		t.Errorf("expected ID=%q, got %q", kg.ID, got.ID)
	}
}

func TestGetKeyGroup_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/key-group/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetKeyGroup: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetKeyGroupConfig_success(t *testing.T) {
	// Given: an existing key group
	srv := helpers.NewTestServer(t)
	kg, _ := cfCreateKeyGroup(t, srv, "getconfig-kg-1")

	// When: GetKeyGroupConfig is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/key-group/"+kg.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetKeyGroupConfig: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with KeyGroupConfig and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName xml.Name `xml:"KeyGroupConfig"`
		Name    string   `xml:"Name"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal KeyGroupConfig: %v\nbody: %s", err, b)
	}
	if cfg.Name != "getconfig-kg-1" {
		t.Errorf("expected Name=getconfig-kg-1, got %q", cfg.Name)
	}
}

func TestUpdateKeyGroup_success(t *testing.T) {
	// Given: an existing key group
	srv := helpers.NewTestServer(t)
	kg, etag := cfCreateKeyGroup(t, srv, "update-kg-1")

	// When: UpdateKeyGroup is called
	body := keyGroupConfigXML("update-kg-1-updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/key-group/"+kg.ID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateKeyGroup: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with updated ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteKeyGroup_success(t *testing.T) {
	// Given: an existing key group
	srv := helpers.NewTestServer(t)
	kg, etag := cfCreateKeyGroup(t, srv, "delete-kg-1")

	// When: DeleteKeyGroup is called
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/key-group/"+kg.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteKeyGroup: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent GET returns 404
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/key-group/"+kg.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetKeyGroup after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListKeyGroups_success(t *testing.T) {
	// Given: two key groups
	srv := helpers.NewTestServer(t)
	cfCreateKeyGroup(t, srv, "listkg-1")
	cfCreateKeyGroup(t, srv, "listkg-2")

	// When: ListKeyGroups is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/key-group", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListKeyGroups: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with KeyGroupList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"KeyGroupList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal KeyGroupList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── Public Key Helpers ───────────────────────────────────────────────────────

func publicKeyConfigXML(name, callerRef string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<PublicKeyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Name>%s</Name>
  <Comment>test public key</Comment>
  <EncodedKey>MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA0example</EncodedKey>
</PublicKeyConfig>`, callerRef, name)
}

type parsedPublicKey struct {
	XMLName xml.Name `xml:"PublicKey"`
	ID      string   `xml:"Id"`
}

func cfCreatePublicKey(t *testing.T, srv *helpers.TestServer, name, callerRef string) (parsedPublicKey, string) {
	t.Helper()
	body := publicKeyConfigXML(name, callerRef)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/public-key",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreatePublicKey: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var pk parsedPublicKey
	if err := xml.Unmarshal(b, &pk); err != nil {
		t.Fatalf("unmarshal PublicKey: %v\nbody: %s", err, b)
	}
	return pk, etag
}

// ─── Public Key Tests ─────────────────────────────────────────────────────────

func TestCreatePublicKey_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreatePublicKey is called
	pk, etag := cfCreatePublicKey(t, srv, "test-pk-1", "pk-ref-1")

	// Then: ID and ETag are set
	if pk.ID == "" {
		t.Error("expected PublicKey.Id to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetPublicKey_success(t *testing.T) {
	// Given: an existing public key
	srv := helpers.NewTestServer(t)
	pk, _ := cfCreatePublicKey(t, srv, "get-pk-1", "pk-ref-get-1")

	// When: GetPublicKey is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/public-key/"+pk.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching ID and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var got parsedPublicKey
	if err := xml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal PublicKey: %v\nbody: %s", err, b)
	}
	if got.ID != pk.ID {
		t.Errorf("expected ID=%q, got %q", pk.ID, got.ID)
	}
}

func TestGetPublicKey_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/public-key/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetPublicKey: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetPublicKeyConfig_success(t *testing.T) {
	// Given: an existing public key
	srv := helpers.NewTestServer(t)
	pk, _ := cfCreatePublicKey(t, srv, "getconfig-pk-1", "pk-ref-getcfg-1")

	// When: GetPublicKeyConfig is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/public-key/"+pk.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetPublicKeyConfig: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with PublicKeyConfig and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName xml.Name `xml:"PublicKeyConfig"`
		Name    string   `xml:"Name"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal PublicKeyConfig: %v\nbody: %s", err, b)
	}
	if cfg.Name != "getconfig-pk-1" {
		t.Errorf("expected Name=getconfig-pk-1, got %q", cfg.Name)
	}
}

func TestUpdatePublicKey_success(t *testing.T) {
	// Given: an existing public key
	srv := helpers.NewTestServer(t)
	pk, etag := cfCreatePublicKey(t, srv, "update-pk-1", "pk-ref-upd-1")

	// When: UpdatePublicKey is called
	body := publicKeyConfigXML("update-pk-1-updated", "pk-ref-upd-1")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/public-key/"+pk.ID+"/config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdatePublicKey: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with updated ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeletePublicKey_success(t *testing.T) {
	// Given: an existing public key
	srv := helpers.NewTestServer(t)
	pk, etag := cfCreatePublicKey(t, srv, "delete-pk-1", "pk-ref-del-1")

	// When: DeletePublicKey is called
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/public-key/"+pk.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeletePublicKey: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent GET returns 404
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/public-key/"+pk.ID, nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetPublicKey after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListPublicKeys_success(t *testing.T) {
	// Given: two public keys
	srv := helpers.NewTestServer(t)
	cfCreatePublicKey(t, srv, "listpk-1", "pk-ref-list-1")
	cfCreatePublicKey(t, srv, "listpk-2", "pk-ref-list-2")

	// When: ListPublicKeys is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/public-key", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListPublicKeys: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with PublicKeyList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"PublicKeyList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal PublicKeyList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

// ─── CloudFront Function Helpers ──────────────────────────────────────────────

func cfFunctionCreateXML(name string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CreateFunctionRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <FunctionConfig>
    <Comment>test function</Comment>
    <Runtime>cloudfront-js-2.0</Runtime>
  </FunctionConfig>
  <FunctionCode>ZnVuY3Rpb24gaGFuZGxlcihldmVudCkgeyByZXR1cm4gZXZlbnQucmVxdWVzdDsgfQ==</FunctionCode>
</CreateFunctionRequest>`, name)
}

type parsedFunctionSummary struct {
	XMLName xml.Name `xml:"FunctionSummary"`
	Name    string   `xml:"Name"`
	Status  string   `xml:"Status"`
}

func cfCreateFunction(t *testing.T, srv *helpers.TestServer, name string) (parsedFunctionSummary, string) {
	t.Helper()
	body := cfFunctionCreateXML(name)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/function",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateFunction: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var fn parsedFunctionSummary
	if err := xml.Unmarshal(b, &fn); err != nil {
		t.Fatalf("unmarshal FunctionSummary: %v\nbody: %s", err, b)
	}
	return fn, etag
}

// ─── CloudFront Function Tests ────────────────────────────────────────────────

func TestCreateFunction_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateFunction is called
	fn, etag := cfCreateFunction(t, srv, "test-func-1")

	// Then: Name and ETag are set
	if fn.Name != "test-func-1" {
		t.Errorf("expected Name=test-func-1, got %q", fn.Name)
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestCreateFunction_duplicate(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	cfCreateFunction(t, srv, "dup-func-1")

	// When: creating again with the same name
	body := cfFunctionCreateXML("dup-func-1")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/function",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateFunction duplicate: %v", err)
	}
	defer resp.Body.Close()

	// Then: 409 Conflict
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestDescribeFunction_success(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	cfCreateFunction(t, srv, "describe-func-1")

	// When: DescribeFunction is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/function/describe-func-1/describe", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DescribeFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with matching Name and ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var got parsedFunctionSummary
	if err := xml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal FunctionSummary: %v\nbody: %s", err, b)
	}
	if got.Name != "describe-func-1" {
		t.Errorf("expected Name=describe-func-1, got %q", got.Name)
	}
}

func TestDescribeFunction_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/function/NONEXISTENT/describe", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DescribeFunction: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetFunction_success(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	cfCreateFunction(t, srv, "get-func-1")

	// When: GetFunction is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/function/get-func-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with ETag and Content-Type containing the function code
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestUpdateFunction_success(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	_, etag := cfCreateFunction(t, srv, "update-func-1")

	// When: UpdateFunction is called
	body := `<?xml version="1.0" encoding="UTF-8"?>
<UpdateFunctionRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <FunctionConfig>
    <Comment>updated function</Comment>
    <Runtime>cloudfront-js-2.0</Runtime>
  </FunctionConfig>
  <FunctionCode>ZnVuY3Rpb24gaGFuZGxlcihldmVudCkgeyByZXR1cm4gZXZlbnQucmVzcG9uc2U7IH0=</FunctionCode>
</UpdateFunctionRequest>`
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/function/update-func-1",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with updated ETag
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteFunction_success(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	_, etag := cfCreateFunction(t, srv, "delete-func-1")

	// When: DeleteFunction is called
	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/function/delete-func-1", nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 204
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// And: subsequent Describe returns 404
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/function/delete-func-1/describe", nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("DescribeFunction after delete: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListFunctions_success(t *testing.T) {
	// Given: two functions
	srv := helpers.NewTestServer(t)
	cfCreateFunction(t, srv, "listfn-1")
	cfCreateFunction(t, srv, "listfn-2")

	// When: ListFunctions is called
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/function", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with FunctionList containing 2 items
	helpers.AssertStatus(t, resp, http.StatusOK)
	var result struct {
		XMLName  xml.Name `xml:"FunctionList"`
		Quantity int      `xml:"Quantity"`
	}
	b := readBody(t, resp)
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal FunctionList: %v\nbody: %s", err, b)
	}
	if result.Quantity != 2 {
		t.Errorf("expected Quantity=2, got %d", result.Quantity)
	}
}

func TestPublishFunction_success(t *testing.T) {
	// Given: an existing DEVELOPMENT function
	srv := helpers.NewTestServer(t)
	_, etag := cfCreateFunction(t, srv, "publish-func-1")

	// When: PublishFunction is called
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/function/publish-func-1/publish", nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PublishFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with FunctionSummary showing LIVE stage
	helpers.AssertStatus(t, resp, http.StatusOK)
	b := readBody(t, resp)
	var fn parsedFunctionSummary
	if err := xml.Unmarshal(b, &fn); err != nil {
		t.Fatalf("unmarshal FunctionSummary: %v\nbody: %s", err, b)
	}
	if fn.Name != "publish-func-1" {
		t.Errorf("expected Name=publish-func-1, got %q", fn.Name)
	}
}

func TestTestFunction_success(t *testing.T) {
	// Given: an existing function
	srv := helpers.NewTestServer(t)
	_, etag := cfCreateFunction(t, srv, "test-func-exec")

	// When: TestFunction is called
	body := `<?xml version="1.0" encoding="UTF-8"?>
<TestFunctionRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Stage>DEVELOPMENT</Stage>
  <EventObject>eyJ2ZXJzaW9uIjoiMS4wIn0=</EventObject>
</TestFunctionRequest>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/function/test-func-exec/test",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("TestFunction: %v", err)
	}
	defer resp.Body.Close()

	// Then: 200 with TestResult
	helpers.AssertStatus(t, resp, http.StatusOK)
	b := readBody(t, resp)
	var result struct {
		XMLName xml.Name `xml:"TestResult"`
	}
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal TestResult: %v\nbody: %s", err, b)
	}
}

// ─── Proxy Tests ──────────────────────────────────────────────────────────

func TestProxy_customOrigin(t *testing.T) {
	// Given: a local HTTP server acting as the origin
	originBody := "hello from origin"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin-Path", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(originBody))
	}))
	defer origin.Close()

	// Parse the origin's host:port so we can use it as the DomainName.
	originURL := origin.URL // e.g. "http://127.0.0.1:PORT"
	originHost := originURL[len("http://"):]
	colonIdx := len(originHost) - 1
	for colonIdx >= 0 && originHost[colonIdx] != ':' {
		colonIdx--
	}
	originDomain := originHost[:colonIdx]
	originPort := originHost[colonIdx+1:]

	var port int
	fmt.Sscanf(originPort, "%d", &port)

	srv := helpers.NewTestServer(t)

	// Create a distribution with the custom origin.
	distConfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>proxy-custom-origin</CallerReference>
  <Comment>proxy test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>custom-1</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>custom-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, originDomain, port)

	dist, _ := cfCreateDistFromXML(t, srv, distConfig)

	// When: a request is proxied through CloudFront
	proxyReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/hello", nil)
	proxyResp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer proxyResp.Body.Close()

	// Then: response is from the origin with CloudFront headers
	helpers.AssertStatus(t, proxyResp, http.StatusOK)
	body := string(readBody(t, proxyResp))
	if body != originBody {
		t.Errorf("expected body=%q, got %q", originBody, body)
	}
	if proxyResp.Header.Get("X-Amz-Cf-Pop") == "" {
		t.Error("expected X-Amz-Cf-Pop header")
	}
	if proxyResp.Header.Get("Via") == "" {
		t.Error("expected Via header")
	}
	if proxyResp.Header.Get("X-Cache") == "" {
		t.Error("expected X-Cache header")
	}
}

func TestProxy_defaultRootObject(t *testing.T) {
	// Given: an origin that echoes the path
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.URL.Path))
	}))
	defer origin.Close()

	originHost := origin.URL[len("http://"):]
	colonIdx := len(originHost) - 1
	for colonIdx >= 0 && originHost[colonIdx] != ':' {
		colonIdx--
	}
	originDomain := originHost[:colonIdx]
	originPort := originHost[colonIdx+1:]
	var port int
	fmt.Sscanf(originPort, "%d", &port)

	srv := helpers.NewTestServer(t)

	distConfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>proxy-root-object</CallerReference>
  <Comment>root object test</Comment>
  <Enabled>true</Enabled>
  <DefaultRootObject>index.html</DefaultRootObject>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>origin-1</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>origin-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, originDomain, port)

	dist, _ := cfCreateDistFromXML(t, srv, distConfig)

	// When: the root path "/" is requested
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	// Then: DefaultRootObject is used — origin receives "/index.html"
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := string(readBody(t, resp))
	if body != "/index.html" {
		t.Errorf("expected body=/index.html (from DefaultRootObject), got %q", body)
	}
}

func TestProxy_cacheBehaviorPathPattern(t *testing.T) {
	// Given: an origin that echoes the path
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handled-By", "origin")
		w.Write([]byte(r.URL.Path))
	}))
	defer origin.Close()

	originHost := origin.URL[len("http://"):]
	colonIdx := len(originHost) - 1
	for colonIdx >= 0 && originHost[colonIdx] != ':' {
		colonIdx--
	}
	originDomain := originHost[:colonIdx]
	originPort := originHost[colonIdx+1:]
	var port int
	fmt.Sscanf(originPort, "%d", &port)

	srv := helpers.NewTestServer(t)

	// Create a distribution with a path-pattern behavior.
	distConfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>proxy-path-pattern</CallerReference>
  <Comment>path pattern test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>main-origin</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>main-origin</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
  <CacheBehaviors>
    <Quantity>1</Quantity>
    <Items>
      <CacheBehavior>
        <PathPattern>/api/*</PathPattern>
        <TargetOriginId>main-origin</TargetOriginId>
        <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
        <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
      </CacheBehavior>
    </Items>
  </CacheBehaviors>
</DistributionConfig>`, originDomain, port)

	dist, _ := cfCreateDistFromXML(t, srv, distConfig)

	// When: /api/users is requested (matches path pattern)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/api/users", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	// Then: request is proxied to the origin
	helpers.AssertStatus(t, resp, http.StatusOK)
	body := string(readBody(t, resp))
	if body != "/api/users" {
		t.Errorf("expected body=/api/users, got %q", body)
	}
}

func TestProxy_distNotFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/_overcast/cloudfront/distributions/NONEXISTENT/hello", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("expected 502, got %d", resp.StatusCode)
	}
}

func TestProxy_disabledDistribution(t *testing.T) {
	// Given: a disabled distribution
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("should not reach"))
	}))
	defer origin.Close()

	originHost := origin.URL[len("http://"):]
	colonIdx := len(originHost) - 1
	for colonIdx >= 0 && originHost[colonIdx] != ':' {
		colonIdx--
	}
	originDomain := originHost[:colonIdx]
	originPort := originHost[colonIdx+1:]
	var port int
	fmt.Sscanf(originPort, "%d", &port)

	srv := helpers.NewTestServer(t)

	distConfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>proxy-disabled</CallerReference>
  <Comment>disabled dist</Comment>
  <Enabled>false</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>origin-1</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>origin-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, originDomain, port)

	dist, _ := cfCreateDistFromXML(t, srv, distConfig)

	// When: proxy request to disabled distribution
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/hello", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()

	// Then: 503 Service Unavailable
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
}

// ─── Monitoring Subscription Tests ────────────────────────────────────────────

func monitoringSubscriptionXMLBody(status string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<MonitoringSubscription xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <RealtimeMetricsSubscriptionConfig>
    <RealtimeMetricsSubscriptionStatus>%s</RealtimeMetricsSubscriptionStatus>
  </RealtimeMetricsSubscriptionConfig>
</MonitoringSubscription>`, status)
}

func cfCreateDist(t *testing.T, srv *helpers.TestServer, callerRef string) string {
	t.Helper()
	dist, _ := cfCreateDistFromXML(t, srv, distributionConfigXML(callerRef))
	return dist.ID
}

func TestCreateMonitoringSubscription_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	distID := cfCreateDist(t, srv, "mon-sub-create")

	body := monitoringSubscriptionXMLBody("Enabled")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateMonitoringSubscription: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
}

func TestGetMonitoringSubscription_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	distID := cfCreateDist(t, srv, "mon-sub-get")

	body := monitoringSubscriptionXMLBody("Enabled")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription", nil)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatalf("GetMonitoringSubscription: %v", err)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusOK)

	b := readBody(t, getResp)
	var got struct {
		XMLName xml.Name `xml:"MonitoringSubscription"`
		Config  struct {
			Status string `xml:"RealtimeMetricsSubscriptionStatus"`
		} `xml:"RealtimeMetricsSubscriptionConfig"`
	}
	if err := xml.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if got.Config.Status != "Enabled" {
		t.Errorf("expected status=Enabled, got %q", got.Config.Status)
	}
}

func TestGetMonitoringSubscription_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	distID := cfCreateDist(t, srv, "mon-sub-get-404")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetMonitoringSubscription: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestDeleteMonitoringSubscription_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	distID := cfCreateDist(t, srv, "mon-sub-del")

	body := monitoringSubscriptionXMLBody("Enabled")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()

	delReq, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DeleteMonitoringSubscription: %v", err)
	}
	defer delResp.Body.Close()
	helpers.AssertStatus(t, delResp, http.StatusNoContent)

	// Verify it's gone
	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/distributions/"+distID+"/monitoring-subscription", nil)
	getResp, getErr := http.DefaultClient.Do(getReq)
	if getErr != nil {
		t.Fatalf("GetMonitoringSubscription after delete: %v", getErr)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

// ─── Realtime Log Config Tests ────────────────────────────────────────────────

func realtimeLogConfigXMLBody(name string, samplingRate int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<RealtimeLogConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <SamplingRate>%d</SamplingRate>
  <Fields><Field>timestamp</Field><Field>c-ip</Field></Fields>
  <EndPoints>
    <EndPoint>
      <StreamType>Kinesis</StreamType>
      <KinesisStreamConfig>
        <RoleARN>arn:aws:iam::000000000000:role/test-role</RoleARN>
        <StreamARN>arn:aws:kinesis:us-east-1:000000000000:stream/test-stream</StreamARN>
      </KinesisStreamConfig>
    </EndPoint>
  </EndPoints>
</RealtimeLogConfig>`, name, samplingRate)
}

type parsedRealtimeLogConfig struct {
	XMLName      xml.Name `xml:"RealtimeLogConfig"`
	ARN          string   `xml:"ARN"`
	Name         string   `xml:"Name"`
	SamplingRate int64    `xml:"SamplingRate"`
	// Fields uses the modeled FieldList element name (<Fields><Field>...).
	Fields []string `xml:"Fields>Field"`
}

type parsedCreateRealtimeLogConfigResult struct {
	XMLName xml.Name                `xml:"CreateRealtimeLogConfigResult"`
	RLC     parsedRealtimeLogConfig `xml:"RealtimeLogConfig"`
}

func cfCreateRealtimeLogConfig(t *testing.T, srv *helpers.TestServer, name string) parsedRealtimeLogConfig {
	t.Helper()
	body := realtimeLogConfigXMLBody(name, 100)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)
	var result parsedCreateRealtimeLogConfigResult
	if err := xml.Unmarshal(b, &result); err != nil {
		t.Fatalf("unmarshal CreateRealtimeLogConfigResult: %v\nbody: %s", err, b)
	}
	return result.RLC
}

func TestCreateRealtimeLogConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	rlc := cfCreateRealtimeLogConfig(t, srv, "test-rlc-1")
	if rlc.Name != "test-rlc-1" {
		t.Errorf("expected Name=test-rlc-1, got %q", rlc.Name)
	}
	if rlc.ARN == "" {
		t.Error("expected ARN to be set")
	}
	if rlc.SamplingRate != 100 {
		t.Errorf("expected SamplingRate=100, got %d", rlc.SamplingRate)
	}
	if got := rlc.Fields; len(got) != 2 || got[0] != "timestamp" || got[1] != "c-ip" {
		t.Errorf("expected Fields=[timestamp c-ip], got %v", got)
	}
}

// TestCreateRealtimeLogConfig_fieldElementNames asserts that Fields uses the
// modeled FieldList element name (<Field>), matching the request shape a real
// client sends, not the generic <member> that a bare Quantity/Items list would emit.
func TestCreateRealtimeLogConfig_fieldElementNames(t *testing.T) {
	srv := helpers.NewTestServer(t)
	body := realtimeLogConfigXMLBody("rlc-field-names-1", 100)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	b := readBody(t, resp)

	if !bytes.Contains(b, []byte("<Field>timestamp</Field>")) {
		t.Errorf("expected response XML to contain <Field>timestamp</Field>\nbody: %s", b)
	}
	if !bytes.Contains(b, []byte("<Field>c-ip</Field>")) {
		t.Errorf("expected response XML to contain <Field>c-ip</Field>\nbody: %s", b)
	}
	// EndPoints legitimately uses <member> (EndPointList has no xmlName override);
	// only Fields must have moved off it.
	if bytes.Contains(b, []byte("<Fields><member>")) {
		t.Errorf("expected Fields not to use <member>\nbody: %s", b)
	}
}

func TestCreateRealtimeLogConfig_duplicate(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateRealtimeLogConfig(t, srv, "dup-rlc")

	body := realtimeLogConfigXMLBody("dup-rlc", 50)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateRealtimeLogConfig duplicate: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

func TestGetRealtimeLogConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	created := cfCreateRealtimeLogConfig(t, srv, "get-rlc-1")

	body := fmt.Sprintf(`<GetRealtimeLogConfigRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><Name>%s</Name></GetRealtimeLogConfigRequest>`, "get-rlc-1")
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/get-realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var wrapper struct {
		XMLName xml.Name                `xml:"GetRealtimeLogConfigResult"`
		RLC     parsedRealtimeLogConfig `xml:"RealtimeLogConfig"`
	}
	if err := xml.Unmarshal(b, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	got := wrapper.RLC
	if got.Name != created.Name {
		t.Errorf("expected Name=%q, got %q", created.Name, got.Name)
	}
}

func TestGetRealtimeLogConfig_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)

	body := `<GetRealtimeLogConfigRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><Name>nonexistent</Name></GetRealtimeLogConfigRequest>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/get-realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestUpdateRealtimeLogConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateRealtimeLogConfig(t, srv, "update-rlc-1")

	body := realtimeLogConfigXMLBody("update-rlc-1", 50)
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var wrapper struct {
		XMLName xml.Name                `xml:"UpdateRealtimeLogConfigResult"`
		RLC     parsedRealtimeLogConfig `xml:"RealtimeLogConfig"`
	}
	if err := xml.Unmarshal(b, &wrapper); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	got := wrapper.RLC
	if got.SamplingRate != 50 {
		t.Errorf("expected SamplingRate=50, got %d", got.SamplingRate)
	}
}

func TestDeleteRealtimeLogConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateRealtimeLogConfig(t, srv, "del-rlc-1")

	body := `<DeleteRealtimeLogConfigRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><Name>del-rlc-1</Name></DeleteRealtimeLogConfigRequest>`
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/delete-realtime-log-config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteRealtimeLogConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	// Verify gone
	getBody := `<GetRealtimeLogConfigRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"><Name>del-rlc-1</Name></GetRealtimeLogConfigRequest>`
	getReq, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/get-realtime-log-config",
		bytes.NewReader([]byte(getBody)))
	getReq.Header.Set("Content-Type", "application/xml")
	getResp, getErr := http.DefaultClient.Do(getReq)
	if getErr != nil {
		t.Fatalf("GetRealtimeLogConfig after delete: %v", getErr)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListRealtimeLogConfigs_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateRealtimeLogConfig(t, srv, "list-rlc-1")
	cfCreateRealtimeLogConfig(t, srv, "list-rlc-2")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/realtime-log-config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListRealtimeLogConfigs: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var list struct {
		XMLName xml.Name `xml:"RealtimeLogConfigs"`
		Items   []struct {
			Name string `xml:"Name"`
		} `xml:"Items>RealtimeLogConfig"`
	}
	if err := xml.Unmarshal(b, &list); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if len(list.Items) < 2 {
		t.Errorf("expected at least 2 items, got %d", len(list.Items))
	}
}

// ─── Field-Level Encryption Config Tests ──────────────────────────────────────

func fleConfigXMLBody(callerRef, comment string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<FieldLevelEncryptionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>%s</Comment>
</FieldLevelEncryptionConfig>`, callerRef, comment)
}

type parsedFLEConfig struct {
	XMLName xml.Name `xml:"FieldLevelEncryption"`
	ID      string   `xml:"Id"`
}

func cfCreateFLEConfig(t *testing.T, srv *helpers.TestServer, callerRef, comment string) (parsedFLEConfig, string) {
	t.Helper()
	body := fleConfigXMLBody(callerRef, comment)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/field-level-encryption",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateFieldLevelEncryptionConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var c parsedFLEConfig
	if err := xml.Unmarshal(b, &c); err != nil {
		t.Fatalf("unmarshal FieldLevelEncryption: %v\nbody: %s", err, b)
	}
	return c, etag
}

func TestCreateFLEConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c, etag := cfCreateFLEConfig(t, srv, "fle-cfg-1", "test")
	if c.ID == "" {
		t.Error("expected ID to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetFieldLevelEncryption_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c, _ := cfCreateFLEConfig(t, srv, "fle-get-1", "test")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption/"+c.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryption: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestGetFieldLevelEncryption_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryption: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetFLEConfigConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c, _ := cfCreateFLEConfig(t, srv, "fle-gcfg-1", "getconfig test")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption/"+c.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryptionConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName         xml.Name `xml:"FieldLevelEncryptionConfig"`
		CallerReference string   `xml:"CallerReference"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if cfg.CallerReference != "fle-gcfg-1" {
		t.Errorf("expected CallerReference=fle-gcfg-1, got %q", cfg.CallerReference)
	}
}

func TestUpdateFLEConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c, etag := cfCreateFLEConfig(t, srv, "fle-upd-1", "original")

	body := fleConfigXMLBody("fle-upd-1", "updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/field-level-encryption/"+c.ID+"/config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateFieldLevelEncryptionConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteFLEConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	c, etag := cfCreateFLEConfig(t, srv, "fle-del-1", "test")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/field-level-encryption/"+c.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteFieldLevelEncryption: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption/"+c.ID, nil)
	getResp, getErr := http.DefaultClient.Do(getReq)
	if getErr != nil {
		t.Fatalf("GetFieldLevelEncryption after delete: %v", getErr)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListFLEConfigs_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateFLEConfig(t, srv, "fle-list-1", "a")
	cfCreateFLEConfig(t, srv, "fle-list-2", "b")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListFieldLevelEncryptionConfigs: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var list struct {
		Quantity int `xml:"Quantity"`
	}
	if err := xml.Unmarshal(b, &list); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if list.Quantity < 2 {
		t.Errorf("expected Quantity >= 2, got %d", list.Quantity)
	}
}

// ─── Field-Level Encryption Profile Tests ─────────────────────────────────────

func fleProfileXMLBody(callerRef, name, comment string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<FieldLevelEncryptionProfileConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Name>%s</Name>
  <Comment>%s</Comment>
</FieldLevelEncryptionProfileConfig>`, callerRef, name, comment)
}

type parsedFLEProfile struct {
	XMLName xml.Name `xml:"FieldLevelEncryptionProfile"`
	ID      string   `xml:"Id"`
}

func cfCreateFLEProfile(t *testing.T, srv *helpers.TestServer, callerRef, name, comment string) (parsedFLEProfile, string) {
	t.Helper()
	body := fleProfileXMLBody(callerRef, name, comment)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/field-level-encryption-profile",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateFieldLevelEncryptionProfile: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var p parsedFLEProfile
	if err := xml.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal FieldLevelEncryptionProfile: %v\nbody: %s", err, b)
	}
	return p, etag
}

func TestCreateFLEProfile_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateFLEProfile(t, srv, "flep-1", "profile-1", "test")
	if p.ID == "" {
		t.Error("expected ID to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetFLEProfile_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, _ := cfCreateFLEProfile(t, srv, "flep-get-1", "get-profile", "test")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption-profile/"+p.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryptionProfile: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestGetFLEProfile_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption-profile/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryptionProfile: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetFLEProfileConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, _ := cfCreateFLEProfile(t, srv, "flep-gcfg-1", "gcfg-profile", "test")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption-profile/"+p.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetFieldLevelEncryptionProfileConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName xml.Name `xml:"FieldLevelEncryptionProfileConfig"`
		Name    string   `xml:"Name"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if cfg.Name != "gcfg-profile" {
		t.Errorf("expected Name=gcfg-profile, got %q", cfg.Name)
	}
}

func TestUpdateFLEProfile_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateFLEProfile(t, srv, "flep-upd-1", "upd-profile", "original")

	body := fleProfileXMLBody("flep-upd-1", "upd-profile-v2", "updated")
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/field-level-encryption-profile/"+p.ID+"/config",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateFieldLevelEncryptionProfile: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteFLEProfile_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	p, etag := cfCreateFLEProfile(t, srv, "flep-del-1", "del-profile", "test")

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/field-level-encryption-profile/"+p.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteFieldLevelEncryptionProfile: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption-profile/"+p.ID, nil)
	getResp, getErr := http.DefaultClient.Do(getReq)
	if getErr != nil {
		t.Fatalf("GetFieldLevelEncryptionProfile after delete: %v", getErr)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListFLEProfiles_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateFLEProfile(t, srv, "flep-list-1", "list-1", "a")
	cfCreateFLEProfile(t, srv, "flep-list-2", "list-2", "b")

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/field-level-encryption-profile", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListFieldLevelEncryptionProfiles: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var list struct {
		Quantity int `xml:"Quantity"`
	}
	if err := xml.Unmarshal(b, &list); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if list.Quantity < 2 {
		t.Errorf("expected Quantity >= 2, got %d", list.Quantity)
	}
}

// ─── Continuous Deployment Policy Tests ───────────────────────────────────────

func cdpConfigXMLBody(enabled bool) string {
	e := "false"
	if enabled {
		e = "true"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ContinuousDeploymentPolicyConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <StagingDistributionDnsNames>
    <Quantity>1</Quantity>
    <Items><DnsName>d1234.cloudfront.net</DnsName></Items>
  </StagingDistributionDnsNames>
  <Enabled>%s</Enabled>
</ContinuousDeploymentPolicyConfig>`, e)
}

type parsedCDP struct {
	XMLName xml.Name `xml:"ContinuousDeploymentPolicy"`
	ID      string   `xml:"Id"`
}

func cfCreateCDP(t *testing.T, srv *helpers.TestServer, enabled bool) (parsedCDP, string) {
	t.Helper()
	body := cdpConfigXMLBody(enabled)
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/continuous-deployment-policy",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("CreateContinuousDeploymentPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var cdp parsedCDP
	if err := xml.Unmarshal(b, &cdp); err != nil {
		t.Fatalf("unmarshal ContinuousDeploymentPolicy: %v\nbody: %s", err, b)
	}
	return cdp, etag
}

func TestCreateCDP_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cdp, etag := cfCreateCDP(t, srv, true)
	if cdp.ID == "" {
		t.Error("expected ID to be set")
	}
	if etag == "" {
		t.Error("expected ETag header")
	}
}

func TestGetCDP_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cdp, _ := cfCreateCDP(t, srv, true)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/continuous-deployment-policy/"+cdp.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetContinuousDeploymentPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
}

func TestGetCDP_notFound(t *testing.T) {
	srv := helpers.NewTestServer(t)
	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/continuous-deployment-policy/NONEXISTENT", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetContinuousDeploymentPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNotFound)
}

func TestGetCDPConfig_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cdp, _ := cfCreateCDP(t, srv, true)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/continuous-deployment-policy/"+cdp.ID+"/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GetContinuousDeploymentPolicyConfig: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	if resp.Header.Get("ETag") == "" {
		t.Error("expected ETag header")
	}
	b := readBody(t, resp)
	var cfg struct {
		XMLName xml.Name `xml:"ContinuousDeploymentPolicyConfig"`
		Enabled bool     `xml:"Enabled"`
	}
	if err := xml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if !cfg.Enabled {
		t.Error("expected Enabled=true")
	}
}

func TestUpdateCDP_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cdp, etag := cfCreateCDP(t, srv, true)

	body := cdpConfigXMLBody(false)
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/2020-05-31/continuous-deployment-policy/"+cdp.ID,
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UpdateContinuousDeploymentPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	newETag := resp.Header.Get("ETag")
	if newETag == etag {
		t.Error("expected ETag to change after update")
	}
}

func TestDeleteCDP_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cdp, etag := cfCreateCDP(t, srv, true)

	req, _ := http.NewRequest(http.MethodDelete,
		srv.URL+"/2020-05-31/continuous-deployment-policy/"+cdp.ID, nil)
	req.Header.Set("If-Match", etag)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DeleteContinuousDeploymentPolicy: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusNoContent)

	getReq, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/continuous-deployment-policy/"+cdp.ID, nil)
	getResp, getErr := http.DefaultClient.Do(getReq)
	if getErr != nil {
		t.Fatalf("GetContinuousDeploymentPolicy after delete: %v", getErr)
	}
	defer getResp.Body.Close()
	helpers.AssertStatus(t, getResp, http.StatusNotFound)
}

func TestListCDPs_success(t *testing.T) {
	srv := helpers.NewTestServer(t)
	cfCreateCDP(t, srv, true)
	cfCreateCDP(t, srv, false)

	req, _ := http.NewRequest(http.MethodGet,
		srv.URL+"/2020-05-31/continuous-deployment-policy", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("ListContinuousDeploymentPolicies: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)

	b := readBody(t, resp)
	var list struct {
		Quantity int `xml:"Quantity"`
	}
	if err := xml.Unmarshal(b, &list); err != nil {
		t.Fatalf("unmarshal: %v\nbody: %s", err, b)
	}
	if list.Quantity < 2 {
		t.Errorf("expected Quantity >= 2, got %d", list.Quantity)
	}
}

// cfCreateDistFromXML creates a distribution from raw XML config and returns the parsed dist + ETag.
func cfCreateDistFromXML(t *testing.T, srv *helpers.TestServer, xmlBody string) (parsedDist, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		srv.URL+"/2020-05-31/distribution",
		bytes.NewReader([]byte(xmlBody)))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cfCreateDistFromXML: %v", err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	etag := resp.Header.Get("ETag")
	b := readBody(t, resp)
	var dist parsedDist
	if err := xml.Unmarshal(b, &dist); err != nil {
		t.Fatalf("unmarshal Distribution: %v\nbody: %s", err, b)
	}
	return dist, etag
}

// TestProxy_hostRoutedInvokeAcrossResolvableBases proves a distribution is
// reachable on the DomainName the service mints for it — the CloudFront twin of
// TestExecuteRestAPI_hostBasedInvokeAcrossResolvableBases — on every base a
// client can actually resolve, and in the lowercased form a browser sends.
func TestProxy_hostRoutedInvokeAcrossResolvableBases(t *testing.T) {
	originBody := "hello from origin"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(originBody))
	}))
	defer origin.Close()

	originHost := origin.URL[len("http://"):]
	colonIdx := strings.LastIndexByte(originHost, ':')
	originDomain := originHost[:colonIdx]
	var port int
	fmt.Sscanf(originHost[colonIdx+1:], "%d", &port)

	srv := helpers.NewTestServer(t)
	distConfig := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>proxy-host-routed</CallerReference>
  <Comment>host route test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>custom-1</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>custom-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, originDomain, port)

	dist, _ := cfCreateDistFromXML(t, srv, distConfig)
	t.Logf("minted DomainName = %q", dist.DomainName)

	hosts := []struct{ name, host string }{
		{"minted DomainName", dist.DomainName},
		{"cloudfront.net", dist.ID + ".cloudfront.net"},
		{"localhost", dist.ID + ".cloudfront.localhost:4566"},
		{"overcast.sh wildcard", dist.ID + ".cloudfront.localhost.overcast.sh:4566"},
		{"browser-lowercased host", strings.ToLower(dist.ID + ".cloudfront.localhost.overcast.sh:4566")},
		{"mixed case", dist.ID + ".CloudFront.localhost.overcast.sh:4566"},
	}

	for _, h := range hosts {
		t.Run(h.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/hello", nil)
			req.Host = h.host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("proxy request: %v", err)
			}
			defer resp.Body.Close()
			body := string(readBody(t, resp))
			if resp.StatusCode != http.StatusOK || body != originBody {
				t.Errorf("Host %q: got %d %q, want 200 %q", h.host, resp.StatusCode, body, originBody)
			}
		})
	}
}

// TestProxy_hostRoutedInvokeKeepsEncodedSlash: CloudFront hands the request
// URI to the origin as the viewer sent it, so a %2f inside a segment must
// reach a custom origin still encoded rather than as a separator (#2136).
func TestProxy_hostRoutedInvokeKeepsEncodedSlash(t *testing.T) {
	// Given: a distribution in front of an origin that records the wire path
	var seen string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
	}))
	defer origin.Close()
	originHost := origin.URL[len("http://"):]
	colonIdx := strings.LastIndexByte(originHost, ':')
	var port int
	fmt.Sscanf(originHost[colonIdx+1:], "%d", &port)

	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateDistFromXML(t, srv, viewerPolicyDistXML("proxy-encoded-slash", "allow-all", originHost[:colonIdx], port))

	// When: a viewer requests a path with an encoded slash via the dist's host
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/pkgs/@scope%2fpkg", nil)
	req.Host = dist.ID + ".cloudfront.localhost:4566"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	resp.Body.Close()

	// Then: the origin sees the path byte-for-byte as the viewer sent it
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if want := "/pkgs/@scope%2fpkg"; seen != want {
		t.Errorf("origin saw path %q, want %q", seen, want)
	}
}

// splitOriginURL returns the host and port of an httptest server, for pointing
// a distribution's custom origin at it.
func splitOriginURL(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	hostPort := strings.TrimPrefix(rawURL, "http://")
	colonIdx := strings.LastIndexByte(hostPort, ':')
	var port int
	if _, err := fmt.Sscanf(hostPort[colonIdx+1:], "%d", &port); err != nil {
		t.Fatalf("parse origin port from %q: %v", rawURL, err)
	}
	return hostPort[:colonIdx], port
}

// hostRoutedGet requests rawPath, sent exactly as written, on a distribution's
// own hostname.
func hostRoutedGet(t *testing.T, srv *helpers.TestServer, distID, rawPath string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+rawPath, nil)
	if err != nil {
		t.Fatalf("build request for %q: %v", rawPath, err)
	}
	req.Host = distID + ".cloudfront.localhost:4566"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("proxy request %q: %v", rawPath, err)
	}
	return resp
}

// TestProxy_hostRoutedInvokeKeepsPercentEncoding: CloudFront forwards the URI
// it received to the origin unchanged (edge-function-restrictions-all, "URI,
// query string, and headers encoding"), whatever the viewer percent-encoded.
// A path Go's default escaping spells the same way ("/100%25") used to reach
// the proxy decoded, and re-parsing "/100%" as a URL failed with a 502.
func TestProxy_hostRoutedInvokeKeepsPercentEncoding(t *testing.T) {
	// Given: a distribution in front of an origin that records the wire path
	var seen string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
	}))
	defer origin.Close()
	originDomain, port := splitOriginURL(t, origin.URL)

	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateDistFromXML(t, srv, viewerPolicyDistXML("proxy-percent-encoding", "allow-all", originDomain, port))

	for _, path := range []string{
		"/100%25",              // an encoded "%": Go's default spelling, so no RawPath
		"/a%20b",               // an encoded space
		"/caf%C3%A9",           // encoded UTF-8, upper-case hex
		"/caf%c3%a9",           // the same, lower-case hex: not normalised
		"/%7Euser/profile",     // an encoded unreserved character, left as sent
		"/a%2fb%25c",           // an encoded slash beside an encoded "%"
		"/reports/50%25-off/q", // an encoded "%" mid-path
	} {
		t.Run(path, func(t *testing.T) {
			seen = ""

			// When: a viewer requests the path via the dist's host
			resp := hostRoutedGet(t, srv, dist.ID, path)
			resp.Body.Close()

			// Then: the origin sees the path byte-for-byte as the viewer sent it
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if seen != path {
				t.Errorf("origin saw path %q, want %q", seen, path)
			}
		})
	}
}

// TestProxy_encodedPathReachesAnEmulatedOrigin covers the other branch of
// buildOriginRequest: an S3 origin is served by re-entering this emulator, so
// the encoded path has to survive that hop too, or the wrong key is read.
func TestProxy_encodedPathReachesAnEmulatedOrigin(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Given: a bucket holding objects whose keys need percent-encoding
	putBucket, _ := http.NewRequest(http.MethodPut, srv.URL+"/cf-encoded-bucket", nil)
	bResp, err := http.DefaultClient.Do(putBucket)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	bResp.Body.Close()
	objects := map[string]string{
		"/100%25.txt":   "one hundred percent",
		"/sp%20ace.txt": "a space",
	}
	for path, body := range objects {
		putObj, _ := http.NewRequest(http.MethodPut, srv.URL+"/cf-encoded-bucket"+path, strings.NewReader(body))
		oResp, err := http.DefaultClient.Do(putObj)
		if err != nil {
			t.Fatalf("put object %s: %v", path, err)
		}
		oResp.Body.Close()
		helpers.AssertStatus(t, oResp, http.StatusOK)
	}
	dist, _ := cfCreateDistFromXML(t, srv,
		singleOriginDistXML("cf-encoded-s3", "cf-encoded-bucket.s3.amazonaws.com", ""))

	for path, want := range objects {
		t.Run(path, func(t *testing.T) {
			// When: the object is fetched through the distribution's host
			resp := hostRoutedGet(t, srv, dist.ID, path)
			defer resp.Body.Close()

			// Then: the emulator's S3 served that exact key
			helpers.AssertStatus(t, resp, http.StatusOK)
			if body := string(readBody(t, resp)); body != want {
				t.Errorf("body = %q, want %q", body, want)
			}
		})
	}
}

// cfCreateFunctionWithCode creates a CloudFront Function running js and returns
// its ARN, for a behavior's FunctionAssociations.
func cfCreateFunctionWithCode(t *testing.T, srv *helpers.TestServer, name, js string) string {
	t.Helper()
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<CreateFunctionRequest xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <Name>%s</Name>
  <FunctionConfig>
    <Comment>encoded uri test</Comment>
    <Runtime>cloudfront-js-2.0</Runtime>
  </FunctionConfig>
  <FunctionCode>%s</FunctionCode>
</CreateFunctionRequest>`, name, base64.StdEncoding.EncodeToString([]byte(js)))
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/function", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create function %s: %v", name, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var fn struct {
		ARN string `xml:"FunctionMetadata>FunctionARN"`
	}
	if b := readBody(t, resp); xml.Unmarshal(b, &fn) != nil || fn.ARN == "" {
		t.Fatalf("no FunctionARN in CreateFunction response: %s", b)
	}
	return fn.ARN
}

// encodedPathDistXML builds a distribution whose default behavior targets the
// "default" origin and whose one cache behavior, for pathPattern, targets the
// "patterned" origin. A non-empty fnARN is associated with both behaviors as a
// viewer-request function.
func encodedPathDistXML(callerRef, originDomain string, defaultPort, patternedPort int, pathPattern, fnARN string) string {
	fas := ""
	if fnARN != "" {
		fas = fmt.Sprintf(`<FunctionAssociations><Quantity>1</Quantity><Items><FunctionAssociation>
      <FunctionARN>%s</FunctionARN><EventType>viewer-request</EventType>
    </FunctionAssociation></Items></FunctionAssociations>`, fnARN)
	}
	origin := func(id string, port int) string {
		return fmt.Sprintf(`<Origin>
        <Id>%s</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>`, id, originDomain, port)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>encoded path test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>2</Quantity>
    <Items>
      %s
      %s
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>default</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
    %s
  </DefaultCacheBehavior>
  <CacheBehaviors>
    <Quantity>1</Quantity>
    <Items>
      <CacheBehavior>
        <PathPattern>%s</PathPattern>
        <TargetOriginId>patterned</TargetOriginId>
        <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
        <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
        %s
      </CacheBehavior>
    </Items>
  </CacheBehaviors>
</DistributionConfig>`, callerRef, origin("default", defaultPort), origin("patterned", patternedPort), fas, pathPattern, fas)
}

// TestProxy_pathPatternMatchesTheNormalisedEncodedPath: CloudFront normalises
// the URI path per RFC 3986 section 6 and then matches cache behaviors against
// it (DownloadDistValuesCacheBehavior, "Path normalization"). That
// normalisation decodes percent-encoded UNRESERVED characters only, so "%7E"
// matches a "~" in a pattern while a reserved "%40" stays distinct from "@".
func TestProxy_pathPatternMatchesTheNormalisedEncodedPath(t *testing.T) {
	// Given: two origins, one behind a path pattern, each recording what it saw
	var hitBy, seen string
	record := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitBy, seen = name, r.URL.EscapedPath()
		})
	}
	defaultOrigin := httptest.NewServer(record("default"))
	defer defaultOrigin.Close()
	patternedOrigin := httptest.NewServer(record("patterned"))
	defer patternedOrigin.Close()
	originDomain, defaultPort := splitOriginURL(t, defaultOrigin.URL)
	_, patternedPort := splitOriginURL(t, patternedOrigin.URL)

	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateDistFromXML(t, srv,
		encodedPathDistXML("proxy-pattern-encoded", originDomain, defaultPort, patternedPort, "/~team/@*", ""))

	for _, tc := range []struct{ path, wantOrigin string }{
		{"/~team/@home", "patterned"},
		{"/%7Eteam/@home", "patterned"}, // unreserved: normalised to "~"
		{"/%7eteam/@home", "patterned"}, // hex case is not significant
		{"/~team/%40home", "default"},   // reserved: "%40" is not "@"
		{"/~team/@100%25", "patterned"}, // an encoded "%" after the match
		{"/%7Eteam%2F@home", "default"}, // "%2F" is not a separator
	} {
		t.Run(tc.path, func(t *testing.T) {
			hitBy, seen = "", ""

			// When: a viewer requests the path
			resp := hostRoutedGet(t, srv, dist.ID, tc.path)
			resp.Body.Close()

			// Then: the behavior is chosen on the normalised path, and the
			// origin still receives the raw one
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if hitBy != tc.wantOrigin {
				t.Errorf("served by the %q origin, want %q", hitBy, tc.wantOrigin)
			}
			if seen != tc.path {
				t.Errorf("origin saw path %q, want the raw %q", seen, tc.path)
			}
		})
	}
}

// TestProxy_pathPatternMatchesAfterDotSegmentsAndSlashes: CloudFront normalises
// the path "consistent with RFC 3986" before matching, and "multiple slashes
// (//) or periods (..)" are "normalized and removed" — with behaviors "/a/b*"
// and "/a*", "/a/b/.." matches "/a*" (DownloadDistValuesCacheBehavior, "Path
// normalization"). Here "/a/b*" is the one pattern and the default behavior
// stands in for "/a*". The origin still receives the raw path.
func TestProxy_pathPatternMatchesAfterDotSegmentsAndSlashes(t *testing.T) {
	// Given: two origins, one behind "/a/b*", each recording what it saw
	var hitBy, seen string
	record := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hitBy, seen = name, r.URL.EscapedPath()
		})
	}
	defaultOrigin := httptest.NewServer(record("default"))
	defer defaultOrigin.Close()
	patternedOrigin := httptest.NewServer(record("patterned"))
	defer patternedOrigin.Close()
	originDomain, defaultPort := splitOriginURL(t, defaultOrigin.URL)
	_, patternedPort := splitOriginURL(t, patternedOrigin.URL)

	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateDistFromXML(t, srv,
		encodedPathDistXML("proxy-pattern-dot-segments", originDomain, defaultPort, patternedPort, "/a/b*", ""))

	for _, tc := range []struct{ path, wantOrigin string }{
		{"/a/b/x", "patterned"},      // control
		{"/a/b/..", "default"},       // the AWS example: "/a/"
		{"/a/b/../", "default"},      // "/a/"
		{"/a/b/%2E%2E", "default"},   // "." is unreserved: decoded, then resolved
		{"/a/b/%2e%2E/", "default"},  // hex case is not significant
		{"/a/./b/x", "patterned"},    // "/a/b/x"
		{"/a//b/x", "patterned"},     // "/a/b/x"
		{"//a/b/x", "patterned"},     // "/a/b/x"
		{"/x/../a/b/x", "patterned"}, // "/a/b/x"
		{"/../a/b/x", "patterned"},   // ".." above the root stays at the root
		{"/a/x/..%2Fb/q", "default"}, // "%2F" is not a separator: no dot segment
		{"/a/b.../x", "patterned"},   // "..." is an ordinary segment
		{"/a/x/../../a/b", "patterned"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			hitBy, seen = "", ""

			// When: a viewer requests the path
			resp := hostRoutedGet(t, srv, dist.ID, tc.path)
			resp.Body.Close()

			// Then: the behavior is chosen on the normalised path, and the
			// origin still receives the raw one
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			if hitBy != tc.wantOrigin {
				t.Errorf("served by the %q origin, want %q", hitBy, tc.wantOrigin)
			}
			if seen != tc.path {
				t.Errorf("origin saw path %q, want the raw %q", seen, tc.path)
			}
		})
	}
}

// TestProxy_viewerRequestFunctionSeesTheEncodedURI: a viewer-request function
// is handed the URI "without changing" it (edge-function-restrictions-all,
// "URI, query string, and headers encoding"), i.e. percent-encoded as the
// viewer sent it, not decoded.
func TestProxy_viewerRequestFunctionSeesTheEncodedURI(t *testing.T) {
	// Given: a function that answers with the uri it was given
	srv := helpers.NewTestServer(t)
	fnARN := cfCreateFunctionWithCode(t, srv, "echo-uri", `function handler(event) {
  return { statusCode: 200, statusDescription: 'OK',
    headers: { 'x-seen-uri': { value: event.request.uri } } };
}`)
	dist, _ := cfCreateDistFromXML(t, srv,
		encodedPathDistXML("proxy-fn-echo-uri", "127.0.0.1", 9, 9, "/never/*", fnARN))

	for _, path := range []string{"/100%25", "/a%20b", "/caf%C3%A9", "/a%2Fb", "/plain/path"} {
		t.Run(path, func(t *testing.T) {
			// When: a viewer requests the path
			resp := hostRoutedGet(t, srv, dist.ID, path)
			resp.Body.Close()

			// Then: the function saw the encoded form
			helpers.AssertStatus(t, resp, http.StatusOK)
			if got := resp.Header.Get("X-Seen-Uri"); got != path {
				t.Errorf("function saw uri %q, want %q", got, path)
			}
		})
	}
}

// TestProxy_viewerRequestFunctionURIReachesTheOrigin: the uri a function hands
// back is what the origin is asked for. AWS recommends percent-encoding it and
// forwards a changed URI as UTF-8, so an encoded uri passes through untouched
// and a raw UTF-8 or space character reaches the origin percent-encoded, the
// only way to put it on an HTTP/1.1 request line.
func TestProxy_viewerRequestFunctionURIReachesTheOrigin(t *testing.T) {
	// Given: an origin that records the wire path
	var seen string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
	}))
	defer origin.Close()
	originDomain, port := splitOriginURL(t, origin.URL)
	srv := helpers.NewTestServer(t)

	for i, tc := range []struct{ name, uri, want string }{
		{"encoded percent", "/100%25", "/100%25"},
		{"encoded UTF-8", "/caf%C3%A9", "/caf%C3%A9"},
		{"encoded slash", "/a%2Fb", "/a%2Fb"},
		{"raw UTF-8", "/café", "/caf%C3%A9"},
		{"raw space", "/a b", "/a%20b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = ""
			// And: a distribution whose function rewrites every uri to tc.uri
			fnARN := cfCreateFunctionWithCode(t, srv, fmt.Sprintf("rewrite-uri-%d", i), fmt.Sprintf(`function handler(event) {
  var request = event.request;
  request.uri = %q;
  return request;
}`, tc.uri))
			dist, _ := cfCreateDistFromXML(t, srv,
				encodedPathDistXML(fmt.Sprintf("proxy-fn-rewrite-%d", i), originDomain, port, port, "/never/*", fnARN))

			// When: a viewer requests any path
			resp := hostRoutedGet(t, srv, dist.ID, "/index.html")
			resp.Body.Close()

			// Then: the origin is asked for the function's uri
			helpers.AssertStatus(t, resp, http.StatusOK)
			if seen != tc.want {
				t.Errorf("origin saw path %q, want %q", seen, tc.want)
			}
		})
	}
}

// proxyDistSpec describes a single-origin distribution for the behavior, cache
// TTL and access-log tests: a default behavior and one cache behavior for
// PathPattern, each with its own cache policy, and FnARN (when set) associated
// with both for FnEventType (viewer-request when empty).
type proxyDistSpec struct {
	CallerRef       string
	OriginDomain    string
	OriginPort      int
	PathPattern     string
	DefaultPolicyID string
	PatternPolicyID string
	FnARN           string
	FnEventType     string
	LogBucket       string // a bucket name; logging is off when empty
	LogPrefix       string
}

func proxyDistXML(s proxyDistSpec) string {
	fas := ""
	if s.FnARN != "" {
		eventType := s.FnEventType
		if eventType == "" {
			eventType = "viewer-request"
		}
		fas = fmt.Sprintf(`<FunctionAssociations><Quantity>1</Quantity><Items><FunctionAssociation>
      <FunctionARN>%s</FunctionARN><EventType>%s</EventType>
    </FunctionAssociation></Items></FunctionAssociations>`, s.FnARN, eventType)
	}
	policy := func(id string) string {
		if id == "" {
			return `<ForwardedValues><QueryString>false</QueryString></ForwardedValues>`
		}
		return "<CachePolicyId>" + id + "</CachePolicyId>"
	}
	logging := ""
	if s.LogBucket != "" {
		logging = fmt.Sprintf(`<Logging>
    <Enabled>true</Enabled>
    <IncludeCookies>false</IncludeCookies>
    <Bucket>%s.s3.amazonaws.com</Bucket>
    <Prefix>%s</Prefix>
  </Logging>`, s.LogBucket, s.LogPrefix)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>proxy behavior test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>origin</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>origin</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    %s
    %s
  </DefaultCacheBehavior>
  <CacheBehaviors>
    <Quantity>1</Quantity>
    <Items>
      <CacheBehavior>
        <PathPattern>%s</PathPattern>
        <TargetOriginId>origin</TargetOriginId>
        <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
        %s
        %s
      </CacheBehavior>
    </Items>
  </CacheBehaviors>
  %s
</DistributionConfig>`, s.CallerRef, s.OriginDomain, s.OriginPort,
		policy(s.DefaultPolicyID), fas, s.PathPattern, policy(s.PatternPolicyID), fas, logging)
}

// cfCreateCachePolicyWithTTL creates a cache policy whose DefaultTTL is
// defaultTTL seconds and returns its ID.
func cfCreateCachePolicyWithTTL(t *testing.T, srv *helpers.TestServer, name string, defaultTTL int) string {
	t.Helper()
	body := strings.Replace(cachePolicyConfigXML(name),
		"<DefaultTTL>86400</DefaultTTL>", fmt.Sprintf("<DefaultTTL>%d</DefaultTTL>", defaultTTL), 1)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/2020-05-31/cache-policy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create cache policy %s: %v", name, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusCreated)
	var cp parsedCachePolicy
	if b := readBody(t, resp); xml.Unmarshal(b, &cp) != nil || cp.ID == "" {
		t.Fatalf("no cache policy Id in response: %s", b)
	}
	return cp.ID
}

// TestProxy_functionRewrittenURIKeepsTheBehaviorsCacheTTL: when a viewer-request
// function changes the uri, "it doesn't change the cache behavior for the
// request" (functions-event-structure, request object). The cache policy — and
// so the TTL a response is cached for — is the one of the behavior the VIEWER's
// path matched, not of whichever behavior the rewritten uri would match.
func TestProxy_functionRewrittenURIKeepsTheBehaviorsCacheTTL(t *testing.T) {
	// Given: an origin
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("cached body"))
	}))
	defer origin.Close()
	originDomain, port := splitOriginURL(t, origin.URL)

	// And: a long-TTL default behavior, a short-TTL "/short/*" behavior, and a
	// function that moves each request across to the other behavior's paths
	srv := helpers.NewTestServer(t, helpers.WithMockClock())
	longPolicy := cfCreateCachePolicyWithTTL(t, srv, "ttl-long", 86400)
	shortPolicy := cfCreateCachePolicyWithTTL(t, srv, "ttl-short", 60)
	fnARN := cfCreateFunctionWithCode(t, srv, "cross-behaviors", `function handler(event) {
  var request = event.request;
  request.uri = request.uri.indexOf('/short/') === 0 ? '/elsewhere' : '/short/rewritten';
  return request;
}`)
	dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
		CallerRef: "proxy-fn-rewrite-ttl", OriginDomain: originDomain, OriginPort: port,
		PathPattern: "/short/*", DefaultPolicyID: longPolicy, PatternPolicyID: shortPolicy, FnARN: fnARN,
	}))

	for _, tc := range []struct {
		name, path, wantXCache string
	}{
		// The viewer matched the default behavior (86400s): still cached after
		// 120s, though the rewritten "/short/rewritten" would match the 60s one.
		{"viewer path on the long-TTL behavior", "/index.html", "Hit from cloudfront"},
		// The viewer matched "/short/*" (60s): expired after 120s, though the
		// rewritten "/elsewhere" would fall to the 86400s default.
		{"viewer path on the short-TTL behavior", "/short/page", "Miss from cloudfront"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// When: the path is fetched, the clock passes the short TTL, and it is
			// fetched again
			first := hostRoutedGet(t, srv, dist.ID, tc.path)
			first.Body.Close()
			helpers.AssertStatus(t, first, http.StatusOK)
			if got := first.Header.Get("X-Cache"); got != "Miss from cloudfront" {
				t.Fatalf("first fetch X-Cache = %q, want a miss", got)
			}
			srv.AdvanceClock(120 * time.Second)
			second := hostRoutedGet(t, srv, dist.ID, tc.path)
			second.Body.Close()

			// Then: the entry lived for the viewer's behavior's TTL
			helpers.AssertStatus(t, second, http.StatusOK)
			if got := second.Header.Get("X-Cache"); got != tc.wantXCache {
				t.Errorf("second fetch X-Cache = %q, want %q", got, tc.wantXCache)
			}
		})
	}
}

// TestProxy_viewerRequestFunctionURIMustBeginWithSlash: "The new uri value must
// begin with a forward slash" (functions-event-structure). A function that runs
// but returns an invalid event object is a validation error
// (viewing-cloudfront-metrics, FunctionValidationErrors), and a CloudFront
// function validation error reaches the viewer as a 502
// (http-502-bad-gateway). The origin is never asked.
func TestProxy_viewerRequestFunctionURIMustBeginWithSlash(t *testing.T) {
	// Given: an origin that counts the requests it answers
	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
	}))
	defer origin.Close()
	originDomain, port := splitOriginURL(t, origin.URL)
	srv := helpers.NewTestServer(t)

	for i, tc := range []struct {
		name, uri  string
		wantStatus int
	}{
		{"relative path", `'index.html'`, http.StatusBadGateway},
		{"empty", `''`, http.StatusBadGateway},
		{"absolute URL", `'https://example.com/x'`, http.StatusBadGateway},
		{"not a string", `42`, http.StatusBadGateway},
		{"leading slash", `'/index.html'`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originHits.Store(0)
			// And: a distribution whose function sets the uri to tc.uri
			fnARN := cfCreateFunctionWithCode(t, srv, fmt.Sprintf("bad-uri-%d", i), fmt.Sprintf(`function handler(event) {
  var request = event.request;
  request.uri = %s;
  return request;
}`, tc.uri))
			dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
				CallerRef: fmt.Sprintf("proxy-fn-bad-uri-%d", i), OriginDomain: originDomain, OriginPort: port,
				PathPattern: "/never/*", FnARN: fnARN,
			}))

			// When: a viewer requests a path
			resp := hostRoutedGet(t, srv, dist.ID, "/page")
			resp.Body.Close()

			// Then: an invalid uri is a 502 and never reaches the origin
			helpers.AssertStatus(t, resp, tc.wantStatus)
			wantHits := int32(0)
			if tc.wantStatus == http.StatusOK {
				wantHits = 1
			}
			if got := originHits.Load(); got != wantHits {
				t.Errorf("origin answered %d requests, want %d", got, wantHits)
			}
		})
	}
}

// functionErrorOrigin starts an origin that counts its requests and answers
// with status, and returns its host, port and hit counter.
func functionErrorOrigin(t *testing.T, status int) (string, int, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(origin.Close)
	domain, port := splitOriginURL(t, origin.URL)
	return domain, port, hits
}

// TestProxy_viewerRequestFunctionExecutionErrorIs503: a function that "fails to
// complete successfully" is an execution error (viewing-cloudfront-metrics,
// FunctionExecutionErrors), and "an HTTP 503 status code can indicate that
// your function returned an execution error" (http-503-service-unavailable).
// The request goes no further: the origin is not called.
func TestProxy_viewerRequestFunctionExecutionErrorIs503(t *testing.T) {
	originDomain, port, hits := functionErrorOrigin(t, http.StatusOK)
	srv := helpers.NewTestServer(t)

	for i, tc := range []struct{ name, code string }{
		{"throws", `function handler(event) { throw new Error('boom'); }`},
		{"syntax error", `function handler(event) { return event.request`},
		{"no handler", `function notTheHandler(event) { return event.request; }`},
		{"calls an undefined function", `function handler(event) { return missing(event.request); }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			// Given: a distribution whose viewer-request function cannot complete
			fnARN := cfCreateFunctionWithCode(t, srv, fmt.Sprintf("exec-error-req-%d", i), tc.code)
			dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
				CallerRef: fmt.Sprintf("proxy-fn-exec-error-req-%d", i), OriginDomain: originDomain, OriginPort: port,
				PathPattern: "/never/*", FnARN: fnARN,
			}))

			// When: a viewer requests a path
			resp := hostRoutedGet(t, srv, dist.ID, "/page")
			resp.Body.Close()

			// Then: 503, and the origin was never asked
			helpers.AssertStatus(t, resp, http.StatusServiceUnavailable)
			if got := hits.Load(); got != 0 {
				t.Errorf("origin answered %d requests, want 0", got)
			}
		})
	}
}

// TestProxy_viewerRequestFunctionInvalidReturnIs502: a function that runs but
// "returns invalid data (an invalid event object)" is a validation error
// (viewing-cloudfront-metrics, FunctionValidationErrors), which reaches the
// viewer as a 502 (http-502-bad-gateway). A viewer-request function must
// return a request or a response object.
func TestProxy_viewerRequestFunctionInvalidReturnIs502(t *testing.T) {
	originDomain, port, hits := functionErrorOrigin(t, http.StatusOK)
	srv := helpers.NewTestServer(t)

	for i, tc := range []struct{ name, ret string }{
		{"undefined", `undefined`},
		{"null", `null`},
		{"a string", `'/page'`},
		{"a number", `42`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			// Given: a distribution whose viewer-request function returns tc.ret
			fnARN := cfCreateFunctionWithCode(t, srv, fmt.Sprintf("invalid-ret-req-%d", i),
				fmt.Sprintf(`function handler(event) { return %s; }`, tc.ret))
			dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
				CallerRef: fmt.Sprintf("proxy-fn-invalid-ret-req-%d", i), OriginDomain: originDomain, OriginPort: port,
				PathPattern: "/never/*", FnARN: fnARN,
			}))

			// When: a viewer requests a path
			resp := hostRoutedGet(t, srv, dist.ID, "/page")
			resp.Body.Close()

			// Then: 502, and the origin was never asked
			helpers.AssertStatus(t, resp, http.StatusBadGateway)
			if got := hits.Load(); got != 0 {
				t.Errorf("origin answered %d requests, want 0", got)
			}
		})
	}
}

// TestProxy_viewerResponseFunctionErrors: the same two failure classes apply to
// a viewer-response function — an execution error is a 503 and an invalid
// return a 502 — in place of the origin's response.
func TestProxy_viewerResponseFunctionErrors(t *testing.T) {
	originDomain, port, _ := functionErrorOrigin(t, http.StatusOK)
	srv := helpers.NewTestServer(t)

	for i, tc := range []struct {
		name, code string
		wantStatus int
		wantHeader string
	}{
		{"throws", `function handler(event) { throw new Error('boom'); }`, http.StatusServiceUnavailable, ""},
		{"syntax error", `function handler(event) { return event.response`, http.StatusServiceUnavailable, ""},
		{"returns undefined", `function handler(event) { }`, http.StatusBadGateway, ""},
		{"healthy", `function handler(event) {
  var response = event.response;
  response.headers['x-fn'] = { value: 'ran' };
  return response;
}`, http.StatusOK, "ran"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a distribution with tc.code as its viewer-response function
			fnARN := cfCreateFunctionWithCode(t, srv, fmt.Sprintf("resp-fn-%d", i), tc.code)
			dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
				CallerRef: fmt.Sprintf("proxy-fn-resp-%d", i), OriginDomain: originDomain, OriginPort: port,
				PathPattern: "/never/*", FnARN: fnARN, FnEventType: "viewer-response",
			}))

			// When: a viewer requests a path
			resp := hostRoutedGet(t, srv, dist.ID, "/page")
			resp.Body.Close()

			// Then: the function's failure, not the origin's 200, is the answer
			helpers.AssertStatus(t, resp, tc.wantStatus)
			if got := resp.Header.Get("X-Fn"); got != tc.wantHeader {
				t.Errorf("X-Fn = %q, want %q", got, tc.wantHeader)
			}
		})
	}
}

// TestProxy_viewerResponseFunctionSkippedOnOriginError: "If the origin returns
// an HTTP error of 400 and above, the CloudFront Function will not run"
// (functions-event-structure, "Status code and body").
func TestProxy_viewerResponseFunctionSkippedOnOriginError(t *testing.T) {
	// Given: an origin that answers 404, and a viewer-response function that
	// would both mark the response and fail if it ran
	originDomain, port, _ := functionErrorOrigin(t, http.StatusNotFound)
	srv := helpers.NewTestServer(t)
	fnARN := cfCreateFunctionWithCode(t, srv, "resp-fn-origin-error", `function handler(event) {
  var response = event.response;
  response.headers['x-fn'] = { value: 'ran' };
  return response;
}`)
	dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
		CallerRef: "proxy-fn-resp-origin-error", OriginDomain: originDomain, OriginPort: port,
		PathPattern: "/never/*", FnARN: fnARN, FnEventType: "viewer-response",
	}))

	// When: a viewer requests a path
	resp := hostRoutedGet(t, srv, dist.ID, "/page")
	resp.Body.Close()

	// Then: the origin's 404 is passed through untouched by the function
	helpers.AssertStatus(t, resp, http.StatusNotFound)
	if got := resp.Header.Get("X-Fn"); got != "" {
		t.Errorf("X-Fn = %q, want the function not to have run", got)
	}
}

// TestProxy_viewerResponseFunctionRunsOnACacheHit: a viewer-response function
// "executes regardless of whether the file is already in the CloudFront cache"
// (lambda-cloudfront-trigger-events, "Viewer response"; CloudFront Functions
// share the event). So on a hit it runs again, against THIS request, over the
// origin's cached response — not a replay of what it did for the viewer that
// filled the cache.
func TestProxy_viewerResponseFunctionRunsOnACacheHit(t *testing.T) {
	// Given: an origin that counts its requests
	originDomain, port, hits := functionErrorOrigin(t, http.StatusOK)
	srv := helpers.NewTestServer(t)

	// And: a viewer-response function that echoes the request's x-v header,
	// and throws when the request carries x-fail
	fnARN := cfCreateFunctionWithCode(t, srv, "resp-fn-cache-hit", `function handler(event) {
  var response = event.response;
  if (event.request.headers['x-fail']) { throw new Error('boom'); }
  var v = event.request.headers['x-v'];
  if (v) { response.headers['x-echo'] = { value: v.value }; }
  return response;
}`)
	dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
		CallerRef: "proxy-fn-resp-cache-hit", OriginDomain: originDomain, OriginPort: port,
		PathPattern: "/never/*", FnARN: fnARN, FnEventType: "viewer-response",
	}))

	get := func(t *testing.T, path string, headers map[string]string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Host = dist.ID + ".cloudfront.localhost:4566"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		resp.Body.Close()
		return resp
	}

	for _, tc := range []struct {
		name          string
		path          string
		second        map[string]string
		wantStatus    int
		wantEcho      string
		wantXCacheHit bool
	}{
		// The function sees the second viewer's header, not the first's.
		{"runs against this request", "/echo", map[string]string{"X-V": "second"}, http.StatusOK, "second", true},
		// The cache holds the origin's headers: the first viewer's x-echo is
		// not replayed to a viewer the function adds nothing for.
		{"cache holds the origin's headers", "/pure", nil, http.StatusOK, "", true},
		// A function that fails on a hit fails the hit, as on a miss.
		{"execution error on a hit", "/fail", map[string]string{"X-Fail": "1"}, http.StatusServiceUnavailable, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)

			// When: the path is fetched once to fill the cache...
			first := get(t, tc.path, map[string]string{"X-V": "first"})
			helpers.AssertStatus(t, first, http.StatusOK)
			if got := first.Header.Get("X-Echo"); got != "first" {
				t.Fatalf("first fetch X-Echo = %q, want %q", got, "first")
			}

			// ...and again, by a different viewer
			second := get(t, tc.path, tc.second)

			// Then: the second answer is the function's work on this request
			helpers.AssertStatus(t, second, tc.wantStatus)
			if got := second.Header.Get("X-Echo"); got != tc.wantEcho {
				t.Errorf("second fetch X-Echo = %q, want %q", got, tc.wantEcho)
			}
			if tc.wantXCacheHit {
				if got := second.Header.Get("X-Cache"); got != "Hit from cloudfront" {
					t.Errorf("second fetch X-Cache = %q, want a hit", got)
				}
			}
			if got := hits.Load(); got != 1 {
				t.Errorf("origin answered %d requests, want 1: the second must come from the cache", got)
			}
		})
	}
}

// accessLogLine waits for the single access-log object a distribution writes
// under prefix in bucket and returns its tab-separated fields.
func accessLogLine(t *testing.T, srv *helpers.TestServer, bucket, prefix string) []string {
	t.Helper()
	var key string
	helpers.Eventually(t, 5*time.Second, 20*time.Millisecond, func() bool {
		resp, err := http.Get(srv.URL + "/" + bucket + "?list-type=2&prefix=" + prefix)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var list struct {
			Keys []string `xml:"Contents>Key"`
		}
		if xml.Unmarshal(readBody(t, resp), &list) != nil || len(list.Keys) == 0 {
			return false
		}
		key = list.Keys[0]
		return true
	}, "access log object under "+prefix)
	resp, err := http.Get(srv.URL + "/" + bucket + "/" + key)
	if err != nil {
		t.Fatalf("get access log %s: %v", key, err)
	}
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return strings.Split(strings.TrimRight(string(readBody(t, resp)), "\n"), "\t")
}

// TestProxy_accessLogRecordsTheViewersURI: cs-uri-stem is "the portion of the
// request URL that identifies the path and object", with no query string
// (standard-logs-reference) — the URL the viewer asked for, not an internal
// route and not a function's rewrite of it. Log field values carry
// "URL-encoded equivalents" for spaces, bytes above 126 and the characters
// < > " # % { } | \ ^ ~ [ ] ` ' (standard-logging-legacy-s3, "Standard log
// file format"), so an already-encoded "%20" in the URI is logged as "%2520".
func TestProxy_accessLogRecordsTheViewersURI(t *testing.T) {
	// Given: an origin, and a bucket to receive the logs
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer origin.Close()
	originDomain, port := splitOriginURL(t, origin.URL)
	srv := helpers.NewTestServer(t)
	putBucket, _ := http.NewRequest(http.MethodPut, srv.URL+"/cf-access-logs", nil)
	bResp, err := http.DefaultClient.Do(putBucket)
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	bResp.Body.Close()
	rewriteARN := cfCreateFunctionWithCode(t, srv, "log-rewrite", `function handler(event) {
  var request = event.request;
  request.uri = '/rewritten/object';
  return request;
}`)

	const (
		csURIStem   = 7
		csUserAgent = 10
		csURIQuery  = 11
	)
	for i, tc := range []struct {
		name, rawURL, fnARN, userAgent string
		wantStem, wantQuery, wantUA    string
	}{
		{name: "plain path", rawURL: "/images/cat.jpg",
			wantStem: "/images/cat.jpg", wantQuery: "-"},
		{name: "query string excluded", rawURL: "/search?q=cat&n=1",
			wantStem: "/search", wantQuery: "q=cat&n=1"},
		{name: "percent-encoded path", rawURL: "/a%20b/100%25",
			wantStem: "/a%2520b/100%2525", wantQuery: "-"},
		{name: "tilde", rawURL: "/~user/home",
			wantStem: "/%7Euser/home", wantQuery: "-"},
		{name: "function rewrite", rawURL: "/index.html", fnARN: rewriteARN,
			wantStem: "/index.html", wantQuery: "-"},
		{name: "user agent space", rawURL: "/ua", userAgent: "test agent/1.0",
			wantStem: "/ua", wantQuery: "-", wantUA: "test%20agent/1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// And: a logging distribution of its own, so its log object is alone
			prefix := fmt.Sprintf("case%d/", i)
			dist, _ := cfCreateDistFromXML(t, srv, proxyDistXML(proxyDistSpec{
				CallerRef: fmt.Sprintf("proxy-access-log-%d", i), OriginDomain: originDomain, OriginPort: port,
				PathPattern: "/never/*", FnARN: tc.fnARN, LogBucket: "cf-access-logs", LogPrefix: prefix,
			}))

			// When: a viewer requests the URL on the distribution's host
			req, _ := http.NewRequest(http.MethodGet, srv.URL+tc.rawURL, nil)
			req.Host = dist.ID + ".cloudfront.localhost:4566"
			if tc.userAgent != "" {
				req.Header.Set("User-Agent", tc.userAgent)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("proxy request: %v", err)
			}
			resp.Body.Close()
			helpers.AssertStatus(t, resp, http.StatusOK)

			// Then: the log line names the viewer's URI, log-encoded
			fields := accessLogLine(t, srv, "cf-access-logs", prefix)
			if len(fields) <= csURIQuery {
				t.Fatalf("log line has %d fields: %q", len(fields), fields)
			}
			if got := fields[csURIStem]; got != tc.wantStem {
				t.Errorf("cs-uri-stem = %q, want %q", got, tc.wantStem)
			}
			if got := fields[csURIQuery]; got != tc.wantQuery {
				t.Errorf("cs-uri-query = %q, want %q", got, tc.wantQuery)
			}
			if tc.wantUA != "" && fields[csUserAgent] != tc.wantUA {
				t.Errorf("cs(User-Agent) = %q, want %q", fields[csUserAgent], tc.wantUA)
			}
		})
	}
}

// viewerPolicyDistXML returns a distribution whose DefaultCacheBehavior carries
// the given ViewerProtocolPolicy, pointed at the given custom origin.
func viewerPolicyDistXML(callerRef, policy, originDomain string, originPort int) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>viewer protocol policy test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>custom-1</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig>
          <HTTPPort>%d</HTTPPort>
          <HTTPSPort>443</HTTPSPort>
          <OriginProtocolPolicy>http-only</OriginProtocolPolicy>
        </CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>custom-1</TargetOriginId>
    <ViewerProtocolPolicy>%s</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef, originDomain, originPort, policy)
}

// TestProxy_viewerProtocolPolicyIsGatedOnServableTLS covers the rule that an
// emulator must not point a client at a scheme it does not serve.
//
// ViewerProtocolPolicy was enforced unconditionally: "redirect-to-https" issued
// a 301 to https://{host}:{port} and "https-only" a 403, on a server that only
// listens for plain HTTP unless OVERCAST_TLS_CERT and OVERCAST_TLS_KEY are both
// set. Real AWS always serves HTTPS so both are satisfiable there; on Overcast
// the redirect handed the browser a TLS handshake against an HTTP listener, and
// https-only made the distribution permanently unreachable — no client could
// ever satisfy either.
//
// This is the same call docs/plans made for AppSync's uris map: advertising an
// endpoint the caller cannot dial is worse in practice than the shape
// difference. So both policies are enforced only when TLS is actually
// configured, and are otherwise served as allow-all.
func TestProxy_viewerProtocolPolicyIsGatedOnServableTLS(t *testing.T) {
	const originBody = "hello from origin"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(originBody))
	}))
	defer origin.Close()

	originHost := origin.URL[len("http://"):]
	colonIdx := strings.LastIndexByte(originHost, ':')
	originDomain := originHost[:colonIdx]
	var originPort int
	fmt.Sscanf(originHost[colonIdx+1:], "%d", &originPort)

	cases := []struct {
		name        string
		policy      string
		tlsEnabled  bool
		fwdProto    string
		wantStatus  int
		wantBody    string
		description string
	}{
		{
			name:   "redirect-to-https without TLS serves the request",
			policy: "redirect-to-https", tlsEnabled: false,
			wantStatus: http.StatusOK, wantBody: originBody,
			description: "the 301 target would be a TLS handshake against an HTTP listener",
		},
		{
			name:   "redirect-to-https with TLS still redirects",
			policy: "redirect-to-https", tlsEnabled: true,
			wantStatus:  http.StatusMovedPermanently,
			description: "parity is kept wherever it is actually reachable",
		},
		{
			name:   "redirect-to-https behind a TLS-terminating proxy does not redirect",
			policy: "redirect-to-https", tlsEnabled: false, fwdProto: "https",
			wantStatus: http.StatusOK, wantBody: originBody,
			description: "X-Forwarded-Proto already says the hop was https",
		},
		{
			name:   "https-only without TLS serves the request",
			policy: "https-only", tlsEnabled: false,
			wantStatus: http.StatusOK, wantBody: originBody,
			description: "otherwise the distribution can never be reached at all",
		},
		{
			name:   "https-only with TLS still refuses plain HTTP",
			policy: "https-only", tlsEnabled: true,
			wantStatus:  http.StatusForbidden,
			description: "parity is kept wherever it is actually reachable",
		},
		{
			name:   "allow-all is unaffected either way",
			policy: "allow-all", tlsEnabled: true,
			wantStatus: http.StatusOK, wantBody: originBody,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opts []helpers.Option
			if tc.tlsEnabled {
				opts = append(opts, helpers.WithTLS())
			}
			srv := helpers.NewTestServer(t, opts...)

			dist, _ := cfCreateDistFromXML(t, srv,
				viewerPolicyDistXML("viewer-policy-"+tc.name, tc.policy, originDomain, originPort))

			req, _ := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/hello", nil)
			if tc.fwdProto != "" {
				req.Header.Set("X-Forwarded-Proto", tc.fwdProto)
			}
			// Do not follow the redirect — the point is what Overcast returns.
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("proxy request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, tc.description)
			}
			if tc.wantBody != "" {
				if body := string(readBody(t, resp)); body != tc.wantBody {
					t.Errorf("body = %q, want %q", body, tc.wantBody)
				}
			}
		})
	}
}

// originGroupDistXML builds a distribution with two custom origins and one
// origin group whose primary is origin-a and whose failover is origin-b. The
// DefaultCacheBehavior targets the GROUP, which is the shape CDK emits for
// CloudFront origin failover and the shape that 502'd.
func originGroupDistXML(callerRef, host string, portA, portB int, failoverCodes []int) string {
	var codes strings.Builder
	for _, c := range failoverCodes {
		fmt.Fprintf(&codes, "<StatusCode>%d</StatusCode>", c)
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>origin group test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>2</Quantity>
    <Items>
      <Origin>
        <Id>origin-a</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig><HTTPPort>%d</HTTPPort><HTTPSPort>443</HTTPSPort><OriginProtocolPolicy>http-only</OriginProtocolPolicy></CustomOriginConfig>
      </Origin>
      <Origin>
        <Id>origin-b</Id>
        <DomainName>%s</DomainName>
        <CustomOriginConfig><HTTPPort>%d</HTTPPort><HTTPSPort>443</HTTPSPort><OriginProtocolPolicy>http-only</OriginProtocolPolicy></CustomOriginConfig>
      </Origin>
    </Items>
  </Origins>
  <OriginGroups>
    <Quantity>1</Quantity>
    <Items>
      <OriginGroup>
        <Id>group-1</Id>
        <FailoverCriteria><StatusCodes><Quantity>%d</Quantity><Items>%s</Items></StatusCodes></FailoverCriteria>
        <Members>
          <Quantity>2</Quantity>
          <Items>
            <OriginGroupMember><OriginId>origin-a</OriginId></OriginGroupMember>
            <OriginGroupMember><OriginId>origin-b</OriginId></OriginGroupMember>
          </Items>
        </Members>
      </OriginGroup>
    </Items>
  </OriginGroups>
  <DefaultCacheBehavior>
    <TargetOriginId>group-1</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef, host, portA, host, portB, len(failoverCodes), codes.String())
}

// TestProxy_originGroupFailover covers cache behaviors that target an origin
// GROUP rather than an origin.
//
// CreateDistribution has always accepted this — validateOriginRefs adds every
// OriginGroup Id to the set of legal TargetOriginIds, citing the AWS origin
// failover docs — but the proxy resolved TargetOriginId against Origins only.
// So Overcast accepted a distribution it could not serve, and every viewer
// request returned "502 Origin not found". CDK emits exactly this shape when a
// Distribution is given an origin group, so a real stack could deploy clean and
// then fail on every request.
//
// Failover semantics follow AWS: the group's members are tried in order, a
// response whose status is listed in FailoverCriteria (or a connection failure)
// moves to the next member, and failover happens ONLY for GET, HEAD and OPTIONS
// — for any other method CloudFront returns the primary's response as-is.
func TestProxy_originGroupFailover(t *testing.T) {
	newOrigin := func(name string, status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(name))
		}))
	}

	cases := []struct {
		name          string
		primaryStatus int
		killPrimary   bool
		method        string
		failoverCodes []int
		wantStatus    int
		wantBody      string
		why           string
	}{
		{
			name: "healthy primary serves the request", primaryStatus: 200, method: http.MethodGet,
			failoverCodes: []int{502, 504}, wantStatus: 200, wantBody: "primary",
			why: "the bug: this returned 502 Origin not found",
		},
		{
			name: "POST to a group resolves the primary", primaryStatus: 200, method: http.MethodPost,
			failoverCodes: []int{502, 504}, wantStatus: 200, wantBody: "primary",
			why: "the reported case was a POST",
		},
		{
			name: "failover status on GET moves to the secondary", primaryStatus: 502, method: http.MethodGet,
			failoverCodes: []int{502, 504}, wantStatus: 200, wantBody: "secondary",
		},
		{
			name: "failover status on POST does NOT fail over", primaryStatus: 502, method: http.MethodPost,
			failoverCodes: []int{502, 504}, wantStatus: 502, wantBody: "primary",
			why: "AWS fails over only for GET, HEAD and OPTIONS",
		},
		{
			name: "status not in the criteria is returned as-is", primaryStatus: 404, method: http.MethodGet,
			failoverCodes: []int{502, 504}, wantStatus: 404, wantBody: "primary",
		},
		{
			name: "unreachable primary fails over on GET", killPrimary: true, method: http.MethodGet,
			failoverCodes: []int{502}, wantStatus: 200, wantBody: "secondary",
			why: "a connection failure is a failover trigger, not just a status code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.primaryStatus
			if status == 0 {
				status = 200
			}
			primary := newOrigin("primary", status)
			secondary := newOrigin("secondary", 200)
			defer secondary.Close()

			portOf := func(s *httptest.Server) int {
				hostPort := s.URL[len("http://"):]
				var p int
				fmt.Sscanf(hostPort[strings.LastIndexByte(hostPort, ':')+1:], "%d", &p)
				return p
			}
			primaryPort := portOf(primary)
			if tc.killPrimary {
				primary.Close() // nothing is listening on that port any more
			} else {
				defer primary.Close()
			}

			srv := helpers.NewTestServer(t)
			dist, _ := cfCreateDistFromXML(t, srv,
				originGroupDistXML("origin-group-"+tc.name, "127.0.0.1", primaryPort, portOf(secondary), tc.failoverCodes))

			req, _ := http.NewRequest(tc.method, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/thing", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("proxy request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, tc.why)
			}
			if body := string(readBody(t, resp)); body != tc.wantBody {
				t.Errorf("served by %q, want %q (%s)", body, tc.wantBody, tc.why)
			}
		})
	}
}

// TestProxy_unknownTargetOriginIsStillAnError keeps the genuine
// misconfiguration case reporting, so resolving groups does not turn a broken
// distribution into a silent one.
func TestProxy_unknownTargetOriginIsStillAnError(t *testing.T) {
	srv := helpers.NewTestServer(t)
	dist, _ := cfCreateDistFromXML(t, srv, distributionConfigXML("origin-group-unknown-target"))

	// Rewrite the stored config to target something that exists nowhere by
	// going through UpdateDistribution would require a valid ref, so instead
	// assert the shape that reaches the proxy: a distribution whose behavior
	// names a missing origin cannot be created at all.
	resp := cfCreate(t, srv, strings.Replace(
		distributionConfigXML("origin-group-missing-origin"),
		"<TargetOriginId>origin-1</TargetOriginId>",
		"<TargetOriginId>no-such-origin</TargetOriginId>", 1))
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusBadRequest)

	_ = dist
}

// singleOriginDistXML builds a distribution with one origin at the given
// domain, no CustomOriginConfig, so the origin is resolved purely from its
// DomainName — the shape CDK emits for an S3 or service-backed origin.
func singleOriginDistXML(callerRef, originDomain, originPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>%s</CallerReference>
  <Comment>origin resolution test</Comment>
  <Enabled>true</Enabled>
  <Origins>
    <Quantity>1</Quantity>
    <Items>
      <Origin>
        <Id>the-origin</Id>
        <DomainName>%s</DomainName>
        <OriginPath>%s</OriginPath>
        <S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig>
      </Origin>
    </Items>
  </Origins>
  <DefaultCacheBehavior>
    <TargetOriginId>the-origin</TargetOriginId>
    <ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy>
    <ForwardedValues><QueryString>false</QueryString></ForwardedValues>
  </DefaultCacheBehavior>
</DistributionConfig>`, callerRef, originDomain, originPath)
}

// TestProxy_originsBackedByAnEmulatedServiceStayLocal covers the rule that an
// origin naming an AWS endpoint Overcast emulates must be served by Overcast,
// not dialled on the public internet.
//
// Only the "{bucket}.s3.{...}.amazonaws.com" spelling was recognised, by a
// bespoke prefix check, and everything else fell through to "custom origin" and
// was dialled at its literal domain. So a distribution fronting an S3 website
// endpoint, a legacy dash-region bucket, or an API Gateway — all of which
// Overcast serves — left the emulator and hit real AWS or failed to resolve.
//
// Resolution now goes through the same HostClassifier the router uses for
// inbound requests, so an origin is recognised by exactly the rules that decide
// who serves a Host, and the two cannot drift.
func TestProxy_originsBackedByAnEmulatedServiceStayLocal(t *testing.T) {
	const objectBody = "served by the emulator"

	t.Run("S3 origin spellings", func(t *testing.T) {
		for _, tc := range []struct{ name, domain string }{
			{"virtual-hosted", "cf-origin-bucket.s3.amazonaws.com"},
			{"virtual-hosted with region", "cf-origin-bucket.s3.us-east-1.amazonaws.com"},
			{"legacy dash region", "cf-origin-bucket.s3-us-west-2.amazonaws.com"},
			{"website endpoint", "cf-origin-bucket.s3-website-us-east-1.amazonaws.com"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				srv := helpers.NewTestServer(t)

				// Given: a bucket holding one object
				putBucket, _ := http.NewRequest(http.MethodPut, srv.URL+"/cf-origin-bucket", nil)
				bResp, err := http.DefaultClient.Do(putBucket)
				if err != nil {
					t.Fatalf("create bucket: %v", err)
				}
				bResp.Body.Close()
				putObj, _ := http.NewRequest(http.MethodPut,
					srv.URL+"/cf-origin-bucket/index.html", strings.NewReader(objectBody))
				oResp, err := http.DefaultClient.Do(putObj)
				if err != nil {
					t.Fatalf("put object: %v", err)
				}
				oResp.Body.Close()

				dist, _ := cfCreateDistFromXML(t, srv, singleOriginDistXML("cf-origin-"+tc.name, tc.domain, ""))

				// When: the object is fetched through the distribution
				req, _ := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/index.html", nil)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("proxy request: %v", err)
				}
				defer resp.Body.Close()

				// Then: the emulator's own S3 served it
				helpers.AssertStatus(t, resp, http.StatusOK)
				if body := string(readBody(t, resp)); body != objectBody {
					t.Errorf("body = %q, want %q — origin %q was not resolved to the emulator", body, objectBody, tc.domain)
				}
			})
		}
	})

	t.Run("OriginPath is prepended to the request path", func(t *testing.T) {
		srv := helpers.NewTestServer(t)
		putBucket, _ := http.NewRequest(http.MethodPut, srv.URL+"/cf-origin-bucket", nil)
		bResp, _ := http.DefaultClient.Do(putBucket)
		bResp.Body.Close()
		putObj, _ := http.NewRequest(http.MethodPut,
			srv.URL+"/cf-origin-bucket/sub/dir/index.html", strings.NewReader(objectBody))
		oResp, _ := http.DefaultClient.Do(putObj)
		oResp.Body.Close()

		dist, _ := cfCreateDistFromXML(t, srv,
			singleOriginDistXML("cf-origin-path", "cf-origin-bucket.s3.amazonaws.com", "/sub/dir"))

		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/index.html", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		defer resp.Body.Close()

		helpers.AssertStatus(t, resp, http.StatusOK)
		if body := string(readBody(t, resp)); body != objectBody {
			t.Errorf("body = %q, want %q", body, objectBody)
		}
	})

	// NOT covered here, and deliberately: a bucket whose own NAME contains
	// ".s3" (e.g. "my.s3.archive", addressed as
	// "my.s3.archive.s3.us-east-1.amazonaws.com") is truncated to "my",
	// because HostClassifier tier A takes everything before the FIRST ".s3."
	// separator while AWS parses from the right. That is pre-existing behaviour
	// of the shared classifier, documented in
	// docs/plans/host-routing-precedence.md §4, and it affects inbound requests
	// identically — so it belongs in a change to that rule, not to origin
	// resolution, which now simply defers to it.
	t.Run("API Gateway origin reaches API Gateway", func(t *testing.T) {
		// Asserts on API Gateway's own AWS-shaped 403 for an unknown API rather
		// than a successful invoke: reaching that error is the proof the request
		// stayed inside the emulator. Before this, the origin was dialled at
		// execute-api.us-east-1.amazonaws.com on the public internet.
		srv := helpers.NewTestServer(t)
		dist, _ := cfCreateDistFromXML(t, srv,
			singleOriginDistXML("cf-origin-apigw", "nosuchapi.execute-api.us-east-1.amazonaws.com", ""))

		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/_overcast/cloudfront/distributions/"+dist.ID+"/prod/hello", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403 — the origin should have been served by the emulator's API Gateway",
				resp.StatusCode)
		}
	})
}
