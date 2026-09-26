package iampolicy

import "testing"

func TestDenialMessage_formats(t *testing.T) {
	const user = "arn:aws:iam::123456789012:user/John"
	cases := []struct {
		name   string
		denial Denial
		want   string
	}{
		{
			name:   "implicit, identity-based, no resource",
			denial: Denial{Principal: user, Action: "codecommit:ListRepositories", Resource: "*", Policy: PolicyTypeIdentityBased},
			want:   "User: " + user + " is not authorized to perform: codecommit:ListRepositories because no identity-based policy allows the codecommit:ListRepositories action",
		},
		{
			name:   "explicit, identity-based, with resource",
			denial: Denial{Principal: user, Action: "codedeploy:ListDeployments", Resource: "arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:*", Policy: PolicyTypeIdentityBased, Explicit: true},
			want:   "User: " + user + " is not authorized to perform: codedeploy:ListDeployments on resource: arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:* with an explicit deny in an identity-based policy",
		},
		{
			name:   "implicit, permissions boundary",
			denial: Denial{Principal: user, Action: "codedeploy:ListDeployments", Resource: "arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:*", Policy: PolicyTypePermissionsBoundary},
			want:   "User: " + user + " is not authorized to perform: codedeploy:ListDeployments on resource: arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:* because no permissions boundary allows the codedeploy:ListDeployments action",
		},
		{
			name:   "explicit, permissions boundary, no resource",
			denial: Denial{Principal: user, Action: "sagemaker:ListModels", Policy: PolicyTypePermissionsBoundary, Explicit: true},
			want:   "User: " + user + " is not authorized to perform: sagemaker:ListModels with an explicit deny in a permissions boundary",
		},
		{
			name:   "implicit, resource-based",
			denial: Denial{Principal: "events.amazonaws.com", Action: "lambda:InvokeFunction", Resource: "arn:aws:lambda:us-east-1:123456789012:function:f", Policy: PolicyTypeResourceBased},
			want:   "User: events.amazonaws.com is not authorized to perform: lambda:InvokeFunction on resource: arn:aws:lambda:us-east-1:123456789012:function:f because no resource-based policy allows the lambda:InvokeFunction action",
		},
		{
			name:   "S3 quotes the resource",
			denial: Denial{Principal: user, Action: "s3:GetObject", Resource: "arn:aws:s3:::bucket/key", QuoteResource: true, Policy: PolicyTypeIdentityBased},
			want:   "User: " + user + ` is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::bucket/key" because no identity-based policy allows the s3:GetObject action`,
		},
		{
			name:   "S3 leaves out a wildcard resource unquoted",
			denial: Denial{Principal: user, Action: "s3:ListAllMyBuckets", Resource: "*", QuoteResource: true, Policy: PolicyTypeIdentityBased},
			want:   "User: " + user + " is not authorized to perform: s3:ListAllMyBuckets because no identity-based policy allows the s3:ListAllMyBuckets action",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// When: the denial is rendered
			got := tc.denial.Message()

			// Then: it reads as AWS's documented message does
			if got != tc.want {
				t.Fatalf("Message() =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

func TestResultDeniedBy_blamesThePolicyToChange(t *testing.T) {
	cases := []struct {
		name         string
		identity     string
		boundary     string
		wantPolicy   PolicyType
		wantExplicit bool
	}{
		{
			name:       "no identity statement allows",
			identity:   `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			wantPolicy: PolicyTypeIdentityBased,
		},
		{
			name:         "an identity statement denies",
			identity:     `{"Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`,
			wantPolicy:   PolicyTypeIdentityBased,
			wantExplicit: true,
		},
		{
			name:       "the identity allows and the boundary does not",
			identity:   `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
			boundary:   `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			wantPolicy: PolicyTypePermissionsBoundary,
		},
		{
			name:       "neither the identity nor the boundary allows",
			identity:   `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			boundary:   `{"Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			wantPolicy: PolicyTypeIdentityBased,
		},
		{
			name:         "the boundary denies",
			identity:     `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`,
			boundary:     `{"Statement":[{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}]}`,
			wantPolicy:   PolicyTypePermissionsBoundary,
			wantExplicit: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: the principal's identity policy and, optionally, a boundary
			in := Input{Request: getObject(), Identity: identityFrom(t, tc.identity)}
			if tc.boundary != "" {
				in.Boundary = boundaryFrom(t, tc.boundary)
			}

			// When: the request is evaluated and the denial attributed
			res := Evaluate(in)
			policy, explicit := res.DeniedBy()

			// Then: the policy a developer has to change is the one named
			if res.Decision == DecisionAllowed {
				t.Fatal("the request was allowed; want it denied")
			}
			if policy != tc.wantPolicy || explicit != tc.wantExplicit {
				t.Fatalf("DeniedBy() = %q, %v; want %q, %v", policy, explicit, tc.wantPolicy, tc.wantExplicit)
			}
		})
	}
}

func TestResultDeniedBy_resourcePolicyDeny(t *testing.T) {
	// Given: an identity allow, and a resource-based policy denying the caller
	identity := identityFrom(t, `{"Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	resource, err := ParseDocument(`{"Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"*"}]}`,
		SourceRef{ID: "bucket-policy", Type: SourceTypeResourcePolicy})
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	// When: the request is evaluated and the denial attributed
	res := Evaluate(Input{Request: getObject(), Identity: identity, ResourcePolicy: resource})
	policy, explicit := res.DeniedBy()

	// Then: the resource-based policy is named, as denying explicitly
	if policy != PolicyTypeResourceBased || !explicit {
		t.Fatalf("DeniedBy() = %q, %v; want %q, true", policy, explicit, PolicyTypeResourceBased)
	}
}
