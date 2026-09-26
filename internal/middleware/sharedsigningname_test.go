package middleware

// sharedsigningname_test.go — a SigV4 signing name that two AWS services share
// is not a service identity, and classification must not treat it as one.
//
// serviceKeyForSigningName answers with one Overcast key per signing name, so
// where AWS gives the same name to two services it can only name one of them.
// For most of those pairs something earlier in detectService has already
// decided — an X-Amz-Target prefix, a dated path prefix — and the mapping is
// reached only by the survivor. Elastic Load Balancing is the pair where
// nothing earlier decides: Classic (2012-06-01) and v2 (2015-12-01) are both
// AWS Query on POST "/", both sign as "elasticloadbalancing", and they share
// the whole vocabulary a load balancer needs — DescribeTags,
// CreateLoadBalancer, DescribeLoadBalancerAttributes. The mapping answered
// "elbv2" for every one of them, so a Classic request was labelled, traced and
// IAM-authorised as a request to a different service (#1884).
//
// The discriminator is the Query API version, which the pinned models attribute
// exactly — awsapi's ClaimQuery resolves (Version, Action) to one service — and
// which is also what the router dispatches a Query request on. Reading it here
// is what makes the label agree with the handler that answers.
//
// TestClassifiesEveryDeclaredOperationOnItsContentWire sweeps the same wire but
// cannot cover this: it enumerates the *capability registry*, and ELB Classic
// declares no capabilities. That is the whole point of the case — an undeclared
// service must not be answered by a declared one — so the case is stated here
// instead, next to the mapping it constrains.

import (
	"net/http"
	"testing"
)

// TestSharedSigningNameIsSeparatedByTheQueryAPIVersion pins the ELB pair, in
// both directions and with the no-version fallback intact.
//
// The "was" column records the behaviour on the commit this fixes.
func TestSharedSigningNameIsSeparatedByTheQueryAPIVersion(t *testing.T) {
	tests := []struct {
		name string
		body string
		was  string
		want string
	}{
		// ELB Classic. DescribeTags is the case the issue was filed on: ELBv2
		// implements it, so the caller got a 200 shaped for the wrong service
		// rather than the 501 an unimplemented service owes it.
		{"classic describe tags", "Action=DescribeTags&Version=2012-06-01", "elbv2", "elastic-load-balancing"},
		{"classic create load balancer", "Action=CreateLoadBalancer&Version=2012-06-01", "elbv2", "elastic-load-balancing"},
		{"classic describe load balancer attributes",
			"Action=DescribeLoadBalancerAttributes&Version=2012-06-01", "elbv2", "elastic-load-balancing"},
		// An operation only Classic declares. It already answered 501, because
		// ELBv2 has no handler for it — but it was counted against ELBv2 the
		// whole way there.
		{"classic describe account limits", "Action=DescribeAccountLimits&Version=2012-06-01", "elbv2", "elastic-load-balancing"},
		// Version before Action, which is the order some clients encode.
		{"classic version first", "Version=2012-06-01&Action=DescribeTags", "elbv2", "elastic-load-balancing"},

		// ELBv2, which the same signing name and the same action names must
		// keep reaching. These are the controls that make the fix a split
		// rather than a swap.
		{"v2 describe tags", "Action=DescribeTags&Version=2015-12-01", "elbv2", "elbv2"},
		{"v2 create load balancer", "Action=CreateLoadBalancer&Version=2015-12-01", "elbv2", "elbv2"},
		{"v2 describe load balancers", "Action=DescribeLoadBalancers&Version=2015-12-01", "elbv2", "elbv2"},

		// No version, so the models can attribute nothing and the signing-name
		// mapping is all there is. ELBv2 is the right answer there: it is the
		// ELB service Overcast implements, and it is what this request reached
		// before the version was consulted.
		{"no version", "Action=DescribeTags", "elbv2", "elbv2"},
		// A version no model carries, which is the same situation arriving by
		// a different route.
		{"unmodeled version", "Action=DescribeTags&Version=1999-01-01", "elbv2", "elbv2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given: a Query request signed the way both ELB SDKs sign.
			r, body := signedFormRequest("elasticloadbalancing", tt.body)

			// When: it is classified.
			got := detectService(r, body)

			// Then: the API version decides, and the signing name only where
			// there is no version to read.
			if got != tt.want {
				t.Errorf("detectService(%s, scope %q) = %q, want %q (was %q)",
					tt.body, "elasticloadbalancing", got, tt.want, tt.was)
			}
		})
	}
}

// TestELBClassicInfersItsOwnIAMAction is why the label is not cosmetic. The
// action IAMEnforce evaluates is built from the classification, and AWS
// authorises both ELB services under the one prefix "elasticloadbalancing:" —
// so a Classic call has to arrive at that prefix by way of its own service key
// rather than by borrowing ELBv2's.
func TestELBClassicInfersItsOwnIAMAction(t *testing.T) {
	for _, tt := range []struct {
		body string
		want string
	}{
		{"Action=DescribeTags&Version=2012-06-01", "elasticloadbalancing:DescribeTags"},
		{"Action=DescribeAccountLimits&Version=2012-06-01", "elasticloadbalancing:DescribeAccountLimits"},
		{"Action=DescribeTags&Version=2015-12-01", "elasticloadbalancing:DescribeTags"},
	} {
		t.Run(tt.body, func(t *testing.T) {
			r, _ := signedFormRequest("elasticloadbalancing", tt.body)
			if got := classifyIAM(r).action; got != tt.want {
				t.Errorf("requestIAMAction(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestSharedSigningNamesAreDecidedBeforeTheMapping is the audit the ELB fix
// asked for: every signing name in serviceKeyForSigningName that AWS gives to
// more than one modeled service, and the signal that separates the pair.
//
// Only Elastic Load Balancing needed code. The rest are recorded here because
// "something earlier happens to decide it" is a property of today's models and
// today's prefix switch, not of this mapping — a model refresh that moved one
// of them onto a shared wire would otherwise repeat #1884 silently.
func TestSharedSigningNamesAreDecidedBeforeTheMapping(t *testing.T) {
	tests := []struct {
		name string
		// why records which signal decides the pair, for the reader of a
		// failure rather than for the assertion.
		why     string
		request func() *http.Request
		body    []byte
		want    string
	}{
		{
			name: "waf classic is separated by its target prefix",
			why: "AWS gives WAF Classic the signing name \"waf\", which is also Overcast's key for " +
				"WAF v2. Both are JSON, so step 1 reads X-Amz-Target and the mapping is never asked.",
			request: func() *http.Request {
				return signedTargetRequest("waf", "AWSWAF_20150824.ListWebACLs")
			},
			want: "waf-classic",
		},
		{
			name:    "waf v2 keeps its own key",
			why:     "The control for the row above: the same path must still answer for the service Overcast implements.",
			request: func() *http.Request { return signedTargetRequest("wafv2", "AWSWAF_20190729.ListWebACLs") },
			want:    "waf",
		},
		{
			name: "service catalog proper is separated by its target prefix",
			why: "\"servicecatalog\" is shared by Service Catalog and AppRegistry. Service Catalog is " +
				"awsJson1_1, AppRegistry is REST, so the target decides before the mapping does.",
			request: func() *http.Request {
				return signedTargetRequest("servicecatalog", "AWS242ServiceCatalogService.ListPortfolios")
			},
			want: "service-catalog",
		},
		{
			name: "legacy elasticsearch service shares \"es\" with opensearch",
			why: "Both are REST and both are unimplemented at this binding, so the answer is a modeled " +
				"501 either way and the label picks the same IAM prefix (\"es:\") for both. Recorded, " +
				"not corrected: there is no observable divergence to correct.",
			request: func() *http.Request {
				return signedRequest(http.MethodGet, "/2015-01-01/es/versions", "es")
			},
			want: "opensearch",
		},
		{
			name:    "monitoring names one service only",
			why:     "CloudWatch Events signs as \"events\" and CloudWatch Logs as \"logs\"; nothing else signs as \"monitoring\".",
			request: func() *http.Request { return signedRequest(http.MethodPost, "/", "monitoring") },
			want:    "cloudwatch",
		},
		{
			name:    "states names one service only",
			why:     "Step Functions is the only modeled service signing as \"states\".",
			request: func() *http.Request { return signedRequest(http.MethodPost, "/", "states") },
			want:    "stepfunctions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectService(tt.request(), tt.body); got != tt.want {
				t.Errorf("detectService = %q, want %q\n\t%s", got, tt.want, tt.why)
			}
		})
	}
}
