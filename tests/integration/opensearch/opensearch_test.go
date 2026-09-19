// Package opensearch_test contains integration tests for the OpenSearch emulator.
//
// Every request below is addressed at the binding the pinned AWS model gives
// the operation (opensearch-2021-01-01), because that is the only address an
// AWS SDK will ever use. #856: the whole service used to be served under an
// Overcast-invented /_opensearch prefix, so none of these paths answered.
//
// Run: go test ./tests/integration/opensearch/...
package opensearch_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// The modeled bindings. ListDomainNames and the tag operations sit directly
// under the API version prefix; the rest of the domain surface sits under
// /opensearch. See internal/awsapi/manifest.gen.go.
const (
	pathDomains     = "/2021-01-01/opensearch/domain"
	pathDomainInfo  = "/2021-01-01/opensearch/domain-info"
	pathDomainNames = "/2021-01-01/domain"
	pathTags        = "/2021-01-01/tags"
	pathTagsRemoval = "/2021-01-01/tags-removal"

	defaultRegion = "us-east-1"
)

// osDo performs an OpenSearch REST-JSON request in region, signed the way an
// SDK signs one: the credential scope names OpenSearch's signing name, "es".
func osDo(t *testing.T, srv *helpers.TestServer, method, path, region string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential=test/20260811/"+region+"/es/aws4_request, SignedHeaders=host, Signature=x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// domainStatusOf decodes a CreateDomain/DescribeDomain/DeleteDomain response.
func domainStatusOf(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var body struct {
		DomainStatus map[string]any `json:"DomainStatus"`
	}
	helpers.DecodeJSON(t, resp, &body)
	return body.DomainStatus
}

func createDomain(t *testing.T, srv *helpers.TestServer, name, engineVersion string) map[string]any {
	t.Helper()
	req := map[string]any{"DomainName": name}
	if engineVersion != "" {
		req["EngineVersion"] = engineVersion
	}
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, req)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	return domainStatusOf(t, resp)
}

// ─── CreateDomain ─────────────────────────────────────────────────────────────

func TestCreateDomain_success(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDomain is called at POST /2021-01-01/opensearch/domain
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
		"DomainName": "test-domain",
	})
	defer resp.Body.Close()

	// Then: the modeled DomainStatus comes back
	helpers.AssertStatus(t, resp, http.StatusOK)
	status := domainStatusOf(t, resp)
	if got := status["DomainName"]; got != "test-domain" {
		t.Errorf("DomainName = %v, want test-domain", got)
	}
	if got := status["ARN"]; got != "arn:aws:es:us-east-1:000000000000:domain/test-domain" {
		t.Errorf("ARN = %v", got)
	}
	if got := status["EngineVersion"]; got != "OpenSearch_2.11" {
		t.Errorf("EngineVersion = %v, want the OpenSearch default", got)
	}
	if got := status["Created"]; got != true {
		t.Errorf("Created = %v, want true", got)
	}
}

func TestCreateDomain_missingDomainName(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: CreateDomain omits the required DomainName
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{})
	defer resp.Body.Close()

	// Then: the modeled ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestCreateDomain_nameAlreadyInUse(t *testing.T) {
	// Given: a domain exists
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: the same name is created again
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
		"DomainName": "test-domain",
	})
	defer resp.Body.Close()

	// Then: ResourceAlreadyExistsException, which AWS answers with 409
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ResourceAlreadyExistsException")
}

// ─── DescribeDomain ───────────────────────────────────────────────────────────

func TestDescribeDomain_success(t *testing.T) {
	// Given: a domain exists
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: DescribeDomain is called at its modeled path
	resp := osDo(t, srv, http.MethodGet, pathDomains+"/test-domain", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the stored domain comes back
	helpers.AssertStatus(t, resp, http.StatusOK)
	if got := domainStatusOf(t, resp)["DomainName"]; got != "test-domain" {
		t.Errorf("DomainName = %v, want test-domain", got)
	}
}

func TestDescribeDomain_unknownDomain(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: an unknown domain is described
	resp := osDo(t, srv, http.MethodGet, pathDomains+"/absent", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ResourceNotFoundException, which OpenSearch answers with 409
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

func TestDescribeDomain_otherRegion(t *testing.T) {
	// Given: a domain created in us-east-1
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: it is described in eu-west-1
	resp := osDo(t, srv, http.MethodGet, pathDomains+"/test-domain", "eu-west-1", nil)
	defer resp.Body.Close()

	// Then: it is not there — domain names are unique per region, not globally
	helpers.AssertStatus(t, resp, http.StatusConflict)
	helpers.AssertJSONError(t, resp, "ResourceNotFoundException")
}

// ─── DeleteDomain ─────────────────────────────────────────────────────────────

func TestDeleteDomain_success(t *testing.T) {
	// Given: a domain exists
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: DeleteDomain is called
	del := osDo(t, srv, http.MethodDelete, pathDomains+"/test-domain", defaultRegion, nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	if got := domainStatusOf(t, del)["Deleted"]; got != true {
		t.Errorf("Deleted = %v, want true", got)
	}

	// Then: describing it no longer finds it
	resp := osDo(t, srv, http.MethodGet, pathDomains+"/test-domain", defaultRegion, nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusConflict)
}

// ─── ListDomainNames ──────────────────────────────────────────────────────────

func TestListDomainNames_success(t *testing.T) {
	// Given: a domain exists
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: ListDomainNames is called at GET /2021-01-01/domain — a different
	// path from CreateDomain's, which shares its shape but not its prefix
	resp := osDo(t, srv, http.MethodGet, pathDomainNames, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the domain is listed with its engine type
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		DomainNames []struct {
			DomainName string `json:"DomainName"`
			EngineType string `json:"EngineType"`
		} `json:"DomainNames"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.DomainNames) != 1 {
		t.Fatalf("DomainNames = %+v, want one entry", body.DomainNames)
	}
	if body.DomainNames[0].DomainName != "test-domain" || body.DomainNames[0].EngineType != "OpenSearch" {
		t.Errorf("DomainNames[0] = %+v", body.DomainNames[0])
	}
}

func TestListDomainNames_engineTypeFilter(t *testing.T) {
	// Given: one OpenSearch domain and one legacy Elasticsearch domain
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "search-domain", "OpenSearch_2.11")
	createDomain(t, srv, "legacy-domain", "Elasticsearch_7.10")

	// When: the list is filtered by the modeled engineType query parameter
	resp := osDo(t, srv, http.MethodGet, pathDomainNames+"?engineType=Elasticsearch", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: only the Elasticsearch domain is returned
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		DomainNames []struct {
			DomainName string `json:"DomainName"`
			EngineType string `json:"EngineType"`
		} `json:"DomainNames"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.DomainNames) != 1 || body.DomainNames[0].DomainName != "legacy-domain" {
		t.Fatalf("DomainNames = %+v, want only legacy-domain", body.DomainNames)
	}
	if body.DomainNames[0].EngineType != "Elasticsearch" {
		t.Errorf("EngineType = %q, want Elasticsearch", body.DomainNames[0].EngineType)
	}
}

// ─── DescribeDomains ──────────────────────────────────────────────────────────

func TestDescribeDomains_success(t *testing.T) {
	// Given: one domain exists
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: DescribeDomains is asked for it and for a name that does not exist
	resp := osDo(t, srv, http.MethodPost, pathDomainInfo, defaultRegion, map[string]any{
		"DomainNames": []string{"test-domain", "absent"},
	})
	defer resp.Body.Close()

	// Then: only the domain that exists is listed; a missing name is omitted
	// rather than raised, because AWS models no not-found error here
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		DomainStatusList []struct {
			DomainName string `json:"DomainName"`
		} `json:"DomainStatusList"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.DomainStatusList) != 1 || body.DomainStatusList[0].DomainName != "test-domain" {
		t.Fatalf("DomainStatusList = %+v", body.DomainStatusList)
	}
}

func TestDescribeDomains_noDomainNames(t *testing.T) {
	// Given: an empty store
	srv := helpers.NewTestServer(t)

	// When: DescribeDomains omits the required DomainNames
	resp := osDo(t, srv, http.MethodPost, pathDomainInfo, defaultRegion, map[string]any{})
	defer resp.Body.Close()

	// Then: the modeled ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// ─── Tags ─────────────────────────────────────────────────────────────────────

func TestListTags_arnIsAQueryParameter(t *testing.T) {
	// Given: a domain with tags added through POST /2021-01-01/tags
	srv := helpers.NewTestServer(t)
	arn, _ := createDomain(t, srv, "test-domain", "")["ARN"].(string)

	add := osDo(t, srv, http.MethodPost, pathTags, defaultRegion, map[string]any{
		"ARN":     arn,
		"TagList": []map[string]string{{"Key": "team", "Value": "search"}},
	})
	defer add.Body.Close()
	helpers.AssertStatus(t, add, http.StatusOK)

	// When: ListTags addresses the domain by the ?arn query parameter the model
	// binds it to — not by a path segment and not in a body
	resp := osDo(t, srv, http.MethodGet, pathTags+"?arn="+url.QueryEscape(arn), defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the tag comes back
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.TagList) != 1 || body.TagList[0].Key != "team" || body.TagList[0].Value != "search" {
		t.Fatalf("TagList = %+v, want the tag added above", body.TagList)
	}
}

// AWS's two models of this service disagree about ListTags' URI, so both
// spellings have to answer. The pinned Smithy model binds GET
// /2021-01-01/tags, and no operation in it carries a trailing slash; botocore
// spells the same operation "/2021-01-01/tags/", so the AWS CLI and boto3 send
// the slash. Overcast registered only the slash-less spelling, so the CLI
// could not reach the operation: signed, it answered 501; unsigned, it fell
// past OpenSearch into S3's wildcard object route and returned HTTP 404 with
// <Error><Code>NoSuchKey</Code>…</Error>, an S3 error for an OpenSearch call.
// Same fault as #963, same fix as #966. Both spellings are asserted here.
func TestListTags_trailingSlashBinding(t *testing.T) {
	// Given: a tagged domain
	srv := helpers.NewTestServer(t)
	arn, _ := createDomain(t, srv, "test-domain", "")["ARN"].(string)
	add := osDo(t, srv, http.MethodPost, pathTags, defaultRegion, map[string]any{
		"ARN":     arn,
		"TagList": []map[string]string{{"Key": "team", "Value": "search"}},
	})
	defer add.Body.Close()
	helpers.AssertStatus(t, add, http.StatusOK)

	// When: the tags are listed at each spelling of the collection URI
	for _, path := range []string{pathTags, pathTags + "/"} {
		t.Run(path, func(t *testing.T) {
			resp := osDo(t, srv, http.MethodGet, path+"?arn="+url.QueryEscape(arn), defaultRegion, nil)
			defer resp.Body.Close()

			// Then: OpenSearch answers with its own modeled output, not S3's
			helpers.AssertStatus(t, resp, http.StatusOK)
			var body struct {
				TagList []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"TagList"`
			}
			helpers.DecodeJSON(t, resp, &body)
			if len(body.TagList) != 1 || body.TagList[0].Key != "team" {
				t.Fatalf("GET %s: TagList = %+v, want the tag added above", path, body.TagList)
			}
		})
	}
}

// Adding a second spelling of a collection URI must not cost the fallback.
// OpenSearch implements eight of the ~96 operations AWS binds under
// /2021-01-01, and the other eighty-odd have to keep reaching a
// protocol-correct 501 — swallowing them is exactly what a chi sub-router
// over the prefix would have done, which is why these routes are registered
// absolutely rather than nested.
func TestUnimplementedOperations_stillReachTheGenerated501(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	// When: modeled operations Overcast does not implement are called at the
	// bindings AWS gives them, including one directly beside the tag routes
	for _, path := range []string{
		"/2021-01-01/opensearch/versions",           // ListVersions
		"/2021-01-01/opensearch/compatibleVersions", // GetCompatibleVersions
		"/2021-01-01/domain/test-domain/packages",   // ListPackagesForDomain
	} {
		t.Run(path, func(t *testing.T) {
			resp := osDo(t, srv, http.MethodGet, path, defaultRegion, nil)
			defer resp.Body.Close()

			// Then: the protocol-correct not-implemented answer
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
		})
	}
}

func TestListTags_missingArn(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	// When: ListTags omits the required arn parameter
	resp := osDo(t, srv, http.MethodGet, pathTags, defaultRegion, nil)
	defer resp.Body.Close()

	// Then: the modeled ValidationException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestCreateDomain_inlineTagList(t *testing.T) {
	// Given: a domain created with a TagList, as AWS accepts
	srv := helpers.NewTestServer(t)
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
		"DomainName": "test-domain",
		"TagList":    []map[string]string{{"Key": "env", "Value": "dev"}},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	arn, _ := domainStatusOf(t, resp)["ARN"].(string)

	// When: the tags are listed
	tags := osDo(t, srv, http.MethodGet, pathTags+"?arn="+url.QueryEscape(arn), defaultRegion, nil)
	defer tags.Body.Close()

	// Then: the creation-time tag is there
	helpers.AssertStatus(t, tags, http.StatusOK)
	var body struct {
		TagList []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"TagList"`
	}
	helpers.DecodeJSON(t, tags, &body)
	if len(body.TagList) != 1 || body.TagList[0].Key != "env" {
		t.Fatalf("TagList = %+v, want the inline tag", body.TagList)
	}
}

func TestRemoveTags_success(t *testing.T) {
	// Given: a domain with two tags
	srv := helpers.NewTestServer(t)
	arn, _ := createDomain(t, srv, "test-domain", "")["ARN"].(string)
	add := osDo(t, srv, http.MethodPost, pathTags, defaultRegion, map[string]any{
		"ARN": arn,
		"TagList": []map[string]string{
			{"Key": "team", "Value": "search"},
			{"Key": "env", "Value": "dev"},
		},
	})
	defer add.Body.Close()
	helpers.AssertStatus(t, add, http.StatusOK)

	// When: one key is removed through POST /2021-01-01/tags-removal
	remove := osDo(t, srv, http.MethodPost, pathTagsRemoval, defaultRegion, map[string]any{
		"ARN":     arn,
		"TagKeys": []string{"env"},
	})
	defer remove.Body.Close()
	helpers.AssertStatus(t, remove, http.StatusOK)

	// Then: only the other tag remains
	resp := osDo(t, srv, http.MethodGet, pathTags+"?arn="+url.QueryEscape(arn), defaultRegion, nil)
	defer resp.Body.Close()
	var body struct {
		TagList []struct {
			Key string `json:"Key"`
		} `json:"TagList"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.TagList) != 1 || body.TagList[0].Key != "team" {
		t.Fatalf("TagList = %+v, want only team", body.TagList)
	}
}

func TestDeleteDomain_dropsItsTags(t *testing.T) {
	// Given: a tagged domain
	srv := helpers.NewTestServer(t)
	arn, _ := createDomain(t, srv, "test-domain", "")["ARN"].(string)
	add := osDo(t, srv, http.MethodPost, pathTags, defaultRegion, map[string]any{
		"ARN":     arn,
		"TagList": []map[string]string{{"Key": "team", "Value": "search"}},
	})
	defer add.Body.Close()
	helpers.AssertStatus(t, add, http.StatusOK)

	// When: the domain is deleted and recreated under the same name
	del := osDo(t, srv, http.MethodDelete, pathDomains+"/test-domain", defaultRegion, nil)
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	createDomain(t, srv, "test-domain", "")

	// Then: the new domain does not inherit the old one's tags
	resp := osDo(t, srv, http.MethodGet, pathTags+"?arn="+url.QueryEscape(arn), defaultRegion, nil)
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	var body struct {
		TagList []json.RawMessage `json:"TagList"`
	}
	helpers.DecodeJSON(t, resp, &body)
	if len(body.TagList) != 0 {
		t.Fatalf("TagList = %v, want none", body.TagList)
	}
}

// ─── Input constraints ────────────────────────────────────────────────────────

// AWS constrains DomainName in the model itself: @length(min: 3, max: 28) and
// @pattern("^[a-z][a-z0-9\\-]+$") on com.amazonaws.opensearch#DomainName, the
// shape every operation that names a domain shares. Neither constraint is
// checked client-side — botocore validates types and required members, not
// patterns — so a name AWS rejects reaches the service and comes back as
// ValidationException, HTTP 400.
func TestCreateDomain_invalidDomainName(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	for name, why := range map[string]string{
		"ab":                              "shorter than the modeled minimum of 3",
		"this-domain-name-is-way-too-lon": "longer than the modeled maximum of 28",
		"Logs":                            "uppercase, which the pattern excludes",
		"1logs":                           "starts with a digit, not [a-z]",
		"-logs":                           "starts with a hyphen, not [a-z]",
		"logs_1":                          "underscore is outside [a-z0-9-]",
		"logs.1":                          "dot is outside [a-z0-9-], and lands in the endpoint hostname",
	} {
		t.Run(name, func(t *testing.T) {
			// When: a name that violates the modeled constraint is created
			resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
				"DomainName": name,
			})
			defer resp.Body.Close()

			// Then: the modeled ValidationException, not a domain
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 — %q is %s", resp.StatusCode, name, why)
			}
			helpers.AssertJSONError(t, resp, "ValidationException")
		})
	}
}

// A malformed name is a bad request, not a missing resource: AWS validates the
// input before it looks anything up, so DescribeDomain and DeleteDomain answer
// ValidationException (400) rather than ResourceNotFoundException (409).
func TestDescribeDomain_invalidDomainName(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	// When: a name that cannot name a domain is described
	resp := osDo(t, srv, http.MethodGet, pathDomains+"/Logs", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ValidationException, not ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

func TestDeleteDomain_invalidDomainName(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	// When: a name that cannot name a domain is deleted
	resp := osDo(t, srv, http.MethodDelete, pathDomains+"/ab", defaultRegion, nil)
	defer resp.Body.Close()

	// Then: ValidationException, not ResourceNotFoundException
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// DescribeDomains' DomainNames members are DomainName-shaped too, so the same
// constraint applies to each element. A malformed element is a bad request —
// it must not be quietly omitted the way a well-formed but unknown name is.
func TestDescribeDomains_invalidDomainName(t *testing.T) {
	// Given: one real domain
	srv := helpers.NewTestServer(t)
	createDomain(t, srv, "test-domain", "")

	// When: the batch names it alongside a malformed name
	resp := osDo(t, srv, http.MethodPost, pathDomainInfo, defaultRegion, map[string]any{
		"DomainNames": []string{"test-domain", "Not_A_Name"},
	})
	defer resp.Body.Close()

	// Then: the whole request is rejected
	helpers.AssertStatus(t, resp, http.StatusBadRequest)
	helpers.AssertJSONError(t, resp, "ValidationException")
}

// EngineVersion carries its own modeled constraint:
// com.amazonaws.opensearch#VersionString is
// @pattern("^Elasticsearch_[0-9]{1}\\.[0-9]{1,2}$|^OpenSearch_[0-9]{1,2}\\.[0-9]{1,2}$").
// That spelling is also what decides a domain's EngineType, so a version
// outside the pattern leaves ListDomainNames' engineType filter deriving an
// answer from a string AWS would never have stored.
func TestCreateDomain_invalidEngineVersion(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	for _, version := range []string{
		"2.11",             // no engine name
		"OpenSearch-2.11",  // hyphen where the pattern wants an underscore
		"Opensearch_2.11",  // wrong case
		"OpenSearch_2",     // no minor version
		"Elasticsearch_12", // no minor version, and a two-digit major
		"Solr_9.4",         // an engine AWS does not run
	} {
		t.Run(version, func(t *testing.T) {
			// When: an engine version outside the modeled pattern is requested
			resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
				"DomainName":    "test-domain",
				"EngineVersion": version,
			})
			defer resp.Body.Close()

			// Then: the modeled ValidationException
			helpers.AssertStatus(t, resp, http.StatusBadRequest)
			helpers.AssertJSONError(t, resp, "ValidationException")
		})
	}
}

// Both spellings the pattern admits must still work, at the shortest and
// longest strings it can produce.
func TestCreateDomain_engineVersionBoundaries(t *testing.T) {
	// Given: a running emulator
	srv := helpers.NewTestServer(t)

	cases := []struct{ domain, version string }{
		{"shortest-opensearch", "OpenSearch_1.0"},
		{"longest-elastic", "Elasticsearch_7.10"},
		{"two-digit-major", "OpenSearch_10.11"},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			// When: each boundary spelling is created
			// Then: it is accepted and echoed back
			if got := createDomain(t, srv, tc.domain, tc.version)["EngineVersion"]; got != tc.version {
				t.Errorf("EngineVersion = %v, want %q", got, tc.version)
			}
		})
	}
}

// ─── Response shape ───────────────────────────────────────────────────────────

// DomainStatus declares four required members: DomainId, DomainName, ARN and
// ClusterConfig. AWS always sends all four, so a client is entitled to read
// ClusterConfig without testing for it first. Overcast runs no cluster, so the
// honest value is the configuration the caller asked for — and an empty object
// when it asked for none, which the model permits because ClusterConfig has no
// required members of its own.
func TestDomainStatus_carriesEveryRequiredMember(t *testing.T) {
	// Given: a domain created with a ClusterConfig, as CreateDomain accepts
	srv := helpers.NewTestServer(t)
	resp := osDo(t, srv, http.MethodPost, pathDomains, defaultRegion, map[string]any{
		"DomainName":    "test-domain",
		"ClusterConfig": map[string]any{"InstanceType": "r6g.large.search", "InstanceCount": 2},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	created := domainStatusOf(t, resp)

	// When: the describe that follows is read as well
	described := osDo(t, srv, http.MethodGet, pathDomains+"/test-domain", defaultRegion, nil)
	defer described.Body.Close()
	helpers.AssertStatus(t, described, http.StatusOK)

	for label, status := range map[string]map[string]any{
		"CreateDomain":   created,
		"DescribeDomain": domainStatusOf(t, described),
	} {
		t.Run(label, func(t *testing.T) {
			// Then: every required member is present
			for _, member := range []string{"DomainId", "DomainName", "ARN", "ClusterConfig"} {
				if _, ok := status[member]; !ok {
					t.Errorf("required member %s is missing from DomainStatus: %v", member, status)
				}
			}
			cluster, ok := status["ClusterConfig"].(map[string]any)
			if !ok {
				t.Fatalf("ClusterConfig = %#v, want the requested object", status["ClusterConfig"])
			}
			if cluster["InstanceType"] != "r6g.large.search" {
				t.Errorf("ClusterConfig = %v, want the InstanceType the create asked for", cluster)
			}
		})
	}
}

// A domain created without a ClusterConfig still carries the member, because
// it is required — an empty object, not a null and not an absence.
func TestDomainStatus_clusterConfigDefaultsToAnEmptyObject(t *testing.T) {
	// Given: a domain created with no cluster configuration
	srv := helpers.NewTestServer(t)

	// When: its DomainStatus is read
	status := createDomain(t, srv, "test-domain", "")

	// Then: ClusterConfig is an object rather than null or absent
	cluster, ok := status["ClusterConfig"].(map[string]any)
	if !ok {
		t.Fatalf("ClusterConfig = %#v, want an empty object", status["ClusterConfig"])
	}
	if len(cluster) != 0 {
		t.Errorf("ClusterConfig = %v, want no members — Overcast configures no cluster", cluster)
	}
}

// DomainStatus.Endpoint is a ServiceUrl, and the model's own example is
// "search-imdb-movies-oopcnjfn6ugo.eu-west-1.es.amazonaws.com": a bare
// hostname, with no scheme, no port and no path. A domain's data plane is a
// host of its own — index, search and bulk requests go to that host, never to
// a path beneath the control-plane endpoint. That is the shape issue #165 was
// filed about, and this test is the guard against a data-plane prefix being
// invented on the control-plane host again.
func TestDescribeDomain_endpointIsABareHostname(t *testing.T) {
	// Given: a domain
	srv := helpers.NewTestServer(t)

	// When: its endpoint is read
	endpoint, _ := createDomain(t, srv, "test-domain", "")["Endpoint"].(string)

	// Then: it is a hostname in AWS's search-<domain>.<region>.es.<suffix> shape
	if endpoint == "" {
		t.Fatal("Endpoint is empty")
	}
	if strings.ContainsAny(endpoint, "/:") {
		t.Errorf("Endpoint = %q, want a bare hostname with no scheme, port or path", endpoint)
	}
	if !strings.HasPrefix(endpoint, "search-test-domain.") {
		t.Errorf("Endpoint = %q, want it to start with search-<domain-name>.", endpoint)
	}
	if !strings.Contains(endpoint, ".us-east-1.es.") {
		t.Errorf("Endpoint = %q, want the region and the es. suffix AWS uses", endpoint)
	}
}
