// Tests for #887: a request at a modeled REST binding whose SigV4 credential
// scope names a different, real AWS service must get AWS's own
// scoped-credential answer, not S3's bucket/object wildcard.
//
// Repro paths are legacy Elasticsearch Service bindings — restJson1, signing
// name "es", the same signing name OpenSearch shares (see the issue's
// evidence: both model services collapse to one signing name). Overcast has
// no elasticsearch-service package, so these are guaranteed unimplemented
// regardless of how much of the newer OpenSearch surface gets built out:
//
//	GET /2015-01-01/es/domain/{DomainName}  -> DescribeElasticsearchDomain
//	GET /2015-01-01/es/versions              -> ListElasticsearchVersions
//
// A correctly scoped, unclaimed request already reached the generated 501
// before this change (claimAnswersCaller matched). The defect was only
// visible with a *mismatched* scope: it fell past restFallback into S3,
// which read "2015-01-01" as a bucket name and "es/domain/probe" /
// "es/versions" as an object key, answering NoSuchBucket (that bucket was
// never created either — #1635) for a question nobody asked.
//
// Run: go test ./tests/integration/router/...
package router_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// scopeMismatchSigV4 builds a syntactically valid Authorization header naming
// service as the credential scope's service component. Ownership here is
// decided from the scope alone (signature validation is off by default), so
// a dummy signature is enough to drive every path this file exercises.
func scopeMismatchSigV4(service string) string {
	return "AWS4-HMAC-SHA256 Credential=test/20260821/us-east-1/" + service +
		"/aws4_request, SignedHeaders=host, Signature=test"
}

func scopeMismatchRequest(t *testing.T, srv *helpers.TestServer, method, path, authService string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authService != "" {
		req.Header.Set("Authorization", scopeMismatchSigV4(authService))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// jsonErrorBody decodes the common AWS JSON error envelope.
func jsonErrorBody(t *testing.T, resp *http.Response) (code, message string) {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Type    string `json:"__type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding JSON error body %s: %v", raw, err)
	}
	return body.Type, body.Message
}

// ── The fix: a real, different scope gets AWS's scoped-credential answer ────

// TestScopeMismatch_realForeignScope_answersScopedCredentialError is the
// issue's repro: a caller signed for Lambda (a real, unrelated AWS service)
// reaches an unclaimed path that is a modeled OpenSearch/legacy-Elasticsearch
// binding (signing name "es"). AWS itself validates the credential scope
// against the endpoint before ever computing a signature, and answers
// InvalidSignatureException naming the service it expected — never routes
// the request to a different service's handler. The old behaviour dropped
// this into S3's wildcard and answered NoSuchBucket/NoSuchKey.
func TestScopeMismatch_realForeignScope_answersScopedCredentialError(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"DescribeElasticsearchDomain", http.MethodGet, "/2015-01-01/es/domain/probe"},
		{"ListElasticsearchVersions", http.MethodGet, "/2015-01-01/es/versions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := scopeMismatchRequest(t, srv, tc.method, tc.path, "lambda")
			defer resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusForbidden)
			code, message := jsonErrorBody(t, resp)
			if code != "InvalidSignatureException" {
				t.Errorf("code = %q, want InvalidSignatureException (message: %s)", code, message)
			}
			const want = "Credential should be scoped to correct service: 'es'."
			if message != want {
				t.Errorf("message = %q, want %q", message, want)
			}
		})
	}
}

// TestScopeMismatch_signingNameDiffersFromOvercastKey covers the acceptance
// criterion that a service whose Overcast key differs from its real AWS
// signing name is exercised: MSK's Overcast key is "msk", but every AWS SDK
// signs its calls "kafka" (#887's evidence section). A foreign real scope
// against an MSK-modeled, unimplemented path must name "kafka" — the modeled
// signing name — not "msk", which is Overcast's key and not anything an SDK
// would ever send or expect back.
func TestScopeMismatch_signingNameDiffersFromOvercastKey(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// GetCompatibleKafkaVersions: GET /v1/compatible-kafka-versions, unimplemented.
	resp := scopeMismatchRequest(t, srv, http.MethodGet, "/v1/compatible-kafka-versions", "lambda")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusForbidden)
	code, message := jsonErrorBody(t, resp)
	if code != "InvalidSignatureException" {
		t.Errorf("code = %q, want InvalidSignatureException (message: %s)", code, message)
	}
	const want = "Credential should be scoped to correct service: 'kafka'."
	if message != want {
		t.Errorf("message = %q, want %q — a caller must never be told to scope to Overcast's internal key", message, want)
	}
}

// ── Regression: everything the issue says must stay unchanged ───────────────

// TestScopeMismatch_unsignedTraffic_stillFallsThroughToS3 pins the issue's
// explicit constraint: unsigned traffic is S3's by design (addressesNonS3),
// and this change must not touch that. Both repro paths, unsigned, must keep
// answering as S3 reading "2015-01-01/es" as a bucket/key pair — an XML
// NoSuchBucket, since "2015-01-01" was never created as a bucket (#1635:
// GetObject checks the bucket before the key) — not the new JSON 403.
func TestScopeMismatch_unsignedTraffic_stillFallsThroughToS3(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"DescribeElasticsearchDomain", http.MethodGet, "/2015-01-01/es/domain/probe"},
		{"ListElasticsearchVersions", http.MethodGet, "/2015-01-01/es/versions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := scopeMismatchRequest(t, srv, tc.method, tc.path, "")
			defer resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusNotFound)
			helpers.AssertXMLError(t, resp, "NoSuchBucket")
		})
	}
}

// TestScopeMismatch_unrecognisedScope_stillFallsThroughToS3 pins the issue's
// narrower constraint: only a scope naming a real, different AWS signing name
// earns the new answer. A scope that is not a signing name any pinned model
// declares is not evidence a genuine SDK produced the request — Overcast does
// not validate signatures, so it cannot tell a typo or a non-AWS SigV4
// client's own convention apart from one — and keeps today's S3 fallback.
//
// This is deliberately the issue's own headline probe value: "opensearch" is
// Overcast's service key, not the signing name any OpenSearch SDK actually
// sends ("es" — see the manifest). It demonstrates the fix does not fire on
// the exact string that motivated it, which is the point: the fix answers
// for a caller AWS itself would recognise as another service, not for any
// string that merely fails to match.
// The third scope, "dynamodb", is a real AWS service but not a REST one:
// DynamoDB is AWS JSON, so no restOperation ever carries it and
// awsapi.IsSigningName cannot recognise it. That is deliberate rather than a
// gap — see claimScopeMismatchesCaller's doc comment — because a JSON/Query
// protocol SDK's own requests never reach this fallback in the first place
// (they self-identify their operation via X-Amz-Target or Action/Version and
// are claimed earlier), so the only credential scopes this code ever needs to
// recognise as "real" are the REST ones the collapsed path space can actually
// collide with.
func TestScopeMismatch_unrecognisedScope_stillFallsThroughToS3(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, scope := range []string{"opensearch", "not-a-real-aws-service", "dynamodb"} {
		t.Run(scope, func(t *testing.T) {
			resp := scopeMismatchRequest(t, srv, http.MethodGet, "/2015-01-01/es/versions", scope)
			defer resp.Body.Close()

			helpers.AssertStatus(t, resp, http.StatusNotFound)
			helpers.AssertXMLError(t, resp, "NoSuchBucket")
		})
	}
}

// TestScopeMismatch_correctlyScopedTraffic_stillAnswers501 is the case that
// worked before this change and must keep working identically: a caller
// signed with the binding's own modeled signing name reaches the generated
// 501, never S3 and never the new 403.
func TestScopeMismatch_correctlyScopedTraffic_stillAnswers501(t *testing.T) {
	srv := helpers.NewTestServer(t)

	resp := scopeMismatchRequest(t, srv, http.MethodGet, "/2015-01-01/es/versions", "es")
	defer resp.Body.Close()

	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
	code, _ := jsonErrorBody(t, resp)
	if code != "NotImplemented" {
		t.Errorf("code = %q, want NotImplemented", code)
	}
}

// TestScopeMismatch_unsignedS3VirtualHostRequest_remainsUnaffected
// regression-tests the constraint's trickiest edge, named explicitly in the
// brief: host-routed S3 virtual-host requests are unsigned-scope edge cases.
//
// internal/middleware/hostaddressing.go rewrites a virtual-hosted request's
// path to "/{bucket}{key}" before the request ever reaches chi's routing —
// restFallback sees the *rewritten* path like any other, so if that rewritten
// path happens to collide with some other modeled binding's shape (here,
// MediaStore Data's root-level greedy PutObject label matches almost any
// path), the request is only safe from the new 403 because addressesNonS3
// still rules S3 out first for an unsigned request. A real S3 vhost caller is
// exactly this: unsigned, or scoped "s3" — never scoped to a foreign real
// service — so this is the case worth pinning, not a scoped one.
func TestScopeMismatch_unsignedS3VirtualHostRequest_remainsUnaffected(t *testing.T) {
	srv := helpers.NewTestServer(t)

	create, err := http.NewRequest(http.MethodPut, srv.URL+"/scope-mismatch-vhost", nil)
	if err != nil {
		t.Fatal(err)
	}
	createResp, err := http.DefaultClient.Do(create)
	if err != nil {
		t.Fatal(err)
	}
	createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)

	// A path shaped like a modeled binding's root-level wildcard, addressed
	// unsigned through the bucket's virtual host — exactly what `curl` or
	// `aws --no-sign-request` sends.
	put, err := http.NewRequest(http.MethodPut, srv.URL+"/2015-01-01/es/versions", bytes.NewBufferString("vhost object"))
	if err != nil {
		t.Fatal(err)
	}
	put.Host = "scope-mismatch-vhost.localhost"

	putResp, err := http.DefaultClient.Do(put)
	if err != nil {
		t.Fatal(err)
	}
	defer putResp.Body.Close()

	helpers.AssertStatus(t, putResp, http.StatusOK)
}

// ── A root catch-all binding is no evidence of the caller's service (#2264) ──

// TestScopeMismatch_rootCatchAllBinding_answersCallersOwn501 pins the half of
// #2264 that is not S3 Tables' own. MediaStore Data binds GET, PUT, DELETE and
// HEAD to "/{Path+}", the only root greedy bindings in the non-S3 models, so
// the trie matches every path to MediaStore when no more specific binding does.
// #887's rule treated that match as proof that the caller meant MediaStore. An
// s3tables-signed GetTable from an SDK older than the pinned model then got
// "Credential should be scoped to correct service: 'mediastore'", which
// misattributes every signed call on a binding Overcast's models do not know,
// retired or newer.
//
// A catch-all proves nothing about the caller, so the caller's own scope
// decides. The request gets the generated 501 in its own service's protocol:
// JSON for a restJson1 service, XML for a restXml one. That is still loud, and
// it names no service the caller never addressed.
func TestScopeMismatch_rootCatchAllBinding_answersCallersOwn501(t *testing.T) {
	srv := helpers.NewTestServer(t)

	for _, tc := range []struct {
		scope  string
		method string
		xml    bool
	}{
		{"s3tables", http.MethodGet, false},
		{"lambda", http.MethodPut, false},
		{"route53", http.MethodDelete, true},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			// Given: a path that only MediaStore Data's /{Path+} binds
			// When: a caller signed for another real service sends it
			resp := scopeMismatchRequest(t, srv, tc.method, "/scope-probe-2264/one/two", tc.scope)
			defer resp.Body.Close()

			// Then: it gets its own service's 501, not a MediaStore scope error
			helpers.AssertStatus(t, resp, http.StatusNotImplemented)
			helpers.AssertHeader(t, resp, "x-emulator-unsupported", "true")
			if tc.xml {
				helpers.AssertXMLError(t, resp, "NotImplemented")
				return
			}
			code, message := jsonErrorBody(t, resp)
			if code != "NotImplemented" {
				t.Errorf("code = %q, want NotImplemented (message: %s)", code, message)
			}
		})
	}
}

// TestScopeMismatch_rootCatchAllBinding_ownersAndUnrecognisedScopesUnchanged
// is the counterweight: the change reads the caller's scope only where it is
// a real signing name that the catch-all does not belong to. A MediaStore
// caller still reaches MediaStore's 501. An unrecognised scope and unsigned
// traffic still reach S3.
func TestScopeMismatch_rootCatchAllBinding_ownersAndUnrecognisedScopesUnchanged(t *testing.T) {
	srv := helpers.NewTestServer(t)

	// Given: a path that only MediaStore Data's /{Path+} binds
	// When: MediaStore Data's own caller sends it
	resp := scopeMismatchRequest(t, srv, http.MethodGet, "/scope-probe-2264/one/two", "mediastore")

	// Then: it still reaches MediaStore's 501
	helpers.AssertStatus(t, resp, http.StatusNotImplemented)
	if code, _ := jsonErrorBody(t, resp); code != "NotImplemented" {
		t.Errorf("mediastore caller: code = %q, want NotImplemented", code)
	}

	// And: a scope no pinned model declares, and unsigned traffic, still reach S3
	for _, scope := range []string{"not-a-real-aws-service", ""} {
		resp := scopeMismatchRequest(t, srv, http.MethodGet, "/scope-probe-2264/one/two", scope)
		helpers.AssertStatus(t, resp, http.StatusNotFound)
		helpers.AssertXMLError(t, resp, "NoSuchBucket")
		resp.Body.Close()
	}
}
