package cloudformation

import (
	"strings"
	"testing"
)

// TestSetCloudFrontConfigEnabled_targetsTheDistributionsOwnElement pins the
// step AWS::CloudFront::Distribution's teardown depends on: a distribution
// must be disabled before it can be deleted, and the element to flip is the
// DistributionConfig's own <Enabled>, not the first one in the document.
//
// Logging, TrustedSigners, TrustedKeyGroups and OriginShield each carry an
// Enabled of their own, and #2010 reordered DistributionConfig's members into
// AWS's modeled order, where Logging and DefaultCacheBehavior are emitted
// before Enabled. The strings.Replace this replaced would now disable the
// access log instead of the distribution, and the delete would fail with
// DistributionNotDisabled.
func TestSetCloudFrontConfigEnabled_targetsTheDistributionsOwnElement(t *testing.T) {
	// Given: a config whose nested elements carry Enabled=true ahead of the
	// distribution's own, as the modeled member order produces
	doc := []byte(`<DistributionConfig xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">` +
		`<CallerReference>ref-1</CallerReference>` +
		`<Origins><Quantity>1</Quantity><Items><Origin><Id>o1</Id>` +
		`<DomainName>example.com</DomainName>` +
		`<OriginShield><Enabled>true</Enabled><OriginShieldRegion>us-east-1</OriginShieldRegion></OriginShield>` +
		`</Origin></Items></Origins>` +
		`<DefaultCacheBehavior><TargetOriginId>o1</TargetOriginId>` +
		`<TrustedSigners><Enabled>true</Enabled><Quantity>0</Quantity></TrustedSigners>` +
		`</DefaultCacheBehavior>` +
		`<Comment>c</Comment>` +
		`<Logging><Enabled>true</Enabled><Bucket>b</Bucket><Prefix></Prefix></Logging>` +
		`<Enabled>true</Enabled>` +
		`</DistributionConfig>`)

	// When: the distribution is disabled
	got, err := setCloudFrontConfigEnabled(doc, false)
	if err != nil {
		t.Fatalf("setCloudFrontConfigEnabled: %v", err)
	}

	// Then: only the top-level Enabled flipped
	if !strings.HasSuffix(string(got), `<Enabled>false</Enabled></DistributionConfig>`) {
		t.Errorf("the distribution's own Enabled must be false, got: %s", got)
	}
	if n := strings.Count(string(got), "<Enabled>true</Enabled>"); n != 3 {
		t.Errorf("nested Enabled elements must be untouched: %d of 3 remain true\n%s", n, got)
	}

	// And: everything else round-trips, the namespace included
	for _, want := range []string{
		`xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/"`,
		"<CallerReference>ref-1</CallerReference>",
		"<DomainName>example.com</DomainName>",
		"<Bucket>b</Bucket>",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("expected the rewritten config to keep %s, got: %s", want, got)
		}
	}
}
