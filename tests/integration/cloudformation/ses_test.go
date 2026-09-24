package cloudformation_test

// ses_test.go — #1764:
//   - AWS::SES::Template's Update used to have no Update method at all, so a
//     changed subject or body replaced the template rather than calling
//     UpdateTemplate, which AWS does not require replacement for.
//   - AWS::SES::EmailIdentity had no CFN handler at all — a template using it
//     got a synthetic physical ID and no identity behind it.

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/overcast-sh/overcast/tests/helpers"
)

// sesQueryCall issues an SES v1 Query-protocol (Action=...) form POST.
func sesQueryCall(t *testing.T, srv *helpers.TestServer, action string, params url.Values) *http.Response {
	t.Helper()
	params.Set("Action", action)
	params.Set("Version", "2010-12-01")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(params.Encode()))
	if err != nil {
		t.Fatalf("sesQueryCall: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sesQueryCall: do: %v", err)
	}
	return resp
}

// sesGetTemplate reads an SES template back through GetTemplate, the same
// operation a caller would use — not CloudFormation's own bookkeeping.
func sesGetTemplate(t *testing.T, srv *helpers.TestServer, name string) (subject, text, html string) {
	t.Helper()
	resp := sesQueryCall(t, srv, "GetTemplate", url.Values{"TemplateName": {name}})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GetTemplate body: %v", err)
	}
	var result struct {
		XMLName xml.Name `xml:"GetTemplateResponse"`
		Result  struct {
			Template struct {
				SubjectPart string `xml:"SubjectPart"`
				TextPart    string `xml:"TextPart"`
				HtmlPart    string `xml:"HtmlPart"`
			} `xml:"Template"`
		} `xml:"GetTemplateResult"`
	}
	if err := xml.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode GetTemplate: %v\nbody: %s", err, body)
	}
	return result.Result.Template.SubjectPart, result.Result.Template.TextPart, result.Result.Template.HtmlPart
}

func sesTemplateStackTemplate(subject string) string {
	return `{
  "Resources": {
    "Tmpl": {
      "Type": "AWS::SES::Template",
      "Properties": {
        "Template": {
          "TemplateName": "cfn-update-template",
          "SubjectPart": "` + subject + `",
          "TextPart": "hello",
          "HtmlPart": "<p>hello</p>"
        }
      }
    }
  }
}`
}

// TestUpdateStack_SESTemplate_subjectChangeAppliesInPlace asserts that
// changing a template's subject on a stack update calls UpdateTemplate
// rather than replacing it: the physical ID is unchanged and GetTemplate
// reports the new subject.
func TestUpdateStack_SESTemplate_subjectChangeAppliesInPlace(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "ses-template-update-stack"

	createResp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sesTemplateStackTemplate("Original subject")},
	})
	defer createResp.Body.Close()
	helpers.AssertStatus(t, createResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	originalPhysicalID := describeStackResourceIDs(t, srv, stackName)["Tmpl"]
	if subj, _, _ := sesGetTemplate(t, srv, "cfn-update-template"); subj != "Original subject" {
		t.Fatalf("subject before update = %q, want %q", subj, "Original subject")
	}

	updateResp := cfnQuery(t, srv, "UpdateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {sesTemplateStackTemplate("Updated subject")},
	})
	defer updateResp.Body.Close()
	helpers.AssertStatus(t, updateResp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "UPDATE_COMPLETE")

	if got := describeStackResourceIDs(t, srv, stackName)["Tmpl"]; got != originalPhysicalID {
		t.Fatalf("Template physical ID changed from %q to %q on a subject-only update; template should not have been replaced", originalPhysicalID, got)
	}
	if subj, text, html := sesGetTemplate(t, srv, "cfn-update-template"); subj != "Updated subject" || text != "hello" || html != "<p>hello</p>" {
		t.Fatalf("template after update = subject=%q text=%q html=%q, want subject=Updated subject text=hello html=<p>hello</p>", subj, text, html)
	}
}

// ── AWS::SES::EmailIdentity ─────────────────────────────────────────────────

// sesV2Get issues an SES v2 REST-JSON GET.
func sesV2Get(t *testing.T, srv *helpers.TestServer, path string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("SES v2 GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("SES v2 GET %s: decode: %v", path, err)
	}
	return resp.StatusCode, body
}

// TestCreateStack_SESEmailIdentity_provisionsAndReadsBack asserts that a
// template declaring AWS::SES::EmailIdentity actually reaches
// CreateEmailIdentity — readable through GetEmailIdentity — with its Tags
// applied, rather than a synthetic physical ID with nothing behind it.
func TestCreateStack_SESEmailIdentity_provisionsAndReadsBack(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "ses-email-identity-stack"
	const template = `{
  "Resources": {
    "Identity": {
      "Type": "AWS::SES::EmailIdentity",
      "Properties": {
        "EmailIdentity": "sender@example.com",
        "Tags": [{"Key": "owner", "Value": "resource"}]
      }
    }
  },
  "Outputs": {
    "IdentityRef": {"Value": {"Ref": "Identity"}}
  }
}`

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	outputs := describeStackOutputs(t, srv, stackName)
	if outputs["IdentityRef"] != "sender@example.com" {
		t.Fatalf("Ref(Identity) = %q, want sender@example.com", outputs["IdentityRef"])
	}

	status, identity := sesV2Get(t, srv, "/v2/email/identities/sender@example.com")
	if status != http.StatusOK {
		t.Fatalf("GetEmailIdentity: HTTP %d: %#v", status, identity)
	}
	tags, _ := identity["Tags"].([]any)
	found := false
	for _, raw := range tags {
		tag, _ := raw.(map[string]any)
		if tag["Key"] == "owner" && tag["Value"] == "resource" {
			found = true
		}
	}
	if !found {
		t.Fatalf("GetEmailIdentity Tags = %#v, want to contain owner=resource", tags)
	}

	// And: deleting the stack removes the identity.
	del := cfnQuery(t, srv, "DeleteStack", url.Values{"StackName": {stackName}})
	defer del.Body.Close()
	helpers.AssertStatus(t, del, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "DELETE_COMPLETE")

	status, _ = sesV2Get(t, srv, "/v2/email/identities/sender@example.com")
	if status == http.StatusOK {
		t.Fatalf("GetEmailIdentity still returns 200 after DeleteStack")
	}
}

// TestCreateStack_SESEmailIdentity_unsupportedPropertiesAreReported asserts
// that DkimAttributes/DkimSigningAttributes/ConfigurationSetAttributes/
// FeedbackAttributes/MailFromAttributes are accepted (the stack still
// deploys) but named in ResourceStatusReason rather than silently dropped —
// Overcast's SES v2 identity store has no fields for any of them.
func TestCreateStack_SESEmailIdentity_unsupportedPropertiesAreReported(t *testing.T) {
	srv := helpers.NewTestServer(t)
	const stackName = "ses-email-identity-props-stack"
	const template = `{
  "Resources": {
    "Identity": {
      "Type": "AWS::SES::EmailIdentity",
      "Properties": {
        "EmailIdentity": "props@example.com",
        "DkimSigningAttributes": {"NextSigningKeyLength": "RSA_2048_BIT"},
        "MailFromAttributes": {"MailFromDomain": "mail.example.com"},
        "FeedbackAttributes": {"EmailForwardingEnabled": false}
      }
    }
  }
}`

	resp := cfnQuery(t, srv, "CreateStack", url.Values{
		"StackName":    {stackName},
		"TemplateBody": {template},
	})
	defer resp.Body.Close()
	helpers.AssertStatus(t, resp, http.StatusOK)
	waitForStackStatus(t, srv, stackName, "CREATE_COMPLETE")

	reasons := describeStackResourceReasons(t, srv, stackName)
	for _, prop := range []string{"DkimSigningAttributes", "MailFromAttributes", "FeedbackAttributes"} {
		if !strings.Contains(reasons, prop) {
			t.Errorf("expected the identity's ResourceStatusReason to name the unapplied %s, got: %s", prop, reasons)
		}
	}
}
