package cloudformation

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
)

// ── AWS::ElasticLoadBalancingV2::LoadBalancer ──────────────────────────────

type elbv2LoadBalancerHandler struct{}

func (h *elbv2LoadBalancerHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameELBv2()
	}

	params := map[string]string{
		"Action":  "CreateLoadBalancer",
		"Version": "2015-12-01",
		"Name":    name,
	}
	if v, _ := props["Type"].(string); v != "" {
		params["Type"] = v
	} else {
		params["Type"] = "application"
	}
	// Scheme defaults to internet-facing on the service side too (handler.go),
	// but only when the parameter is entirely absent — sending nothing here
	// for a template that asked for "internal" silently promoted every ALB to
	// internet-facing regardless of what it declared.
	if v, _ := props["Scheme"].(string); v != "" {
		params["Scheme"] = v
	}
	if v, _ := props["IpAddressType"].(string); v != "" {
		params["IpAddressType"] = v
	}
	if subnets, ok := props["Subnets"].([]any); ok {
		for i, sn := range subnets {
			if s, _ := sn.(string); s != "" {
				params[fmt.Sprintf("Subnets.member.%d", i+1)] = s
			}
		}
	}
	if sgs, ok := props["SecurityGroups"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("SecurityGroups.member.%d", i+1)] = s
			}
		}
	}
	addELBv2TagParams(params, props, "Tags")

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateLoadBalancer: %w", err)
	}

	body := rec.Body.String()
	arn := extractXMLValue(body, "LoadBalancerArn")
	dnsName := extractXMLValue(body, "DNSName")
	canonicalHostedZoneID := extractXMLValue(body, "CanonicalHostedZoneId")

	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:loadbalancer/%s", rCtx.Region, rCtx.AccountID, name)
	}

	attrs := map[string]string{
		"LoadBalancerArn":       arn,
		"DNSName":               dnsName,
		"CanonicalHostedZoneID": canonicalHostedZoneID,
	}
	return arn, attrs, nil
}

func (h *elbv2LoadBalancerHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":          "DeleteLoadBalancer",
		"Version":         "2015-12-01",
		"LoadBalancerArn": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteLoadBalancer", rec, err)
}

func (h *elbv2LoadBalancerHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newName, _ := props["Name"].(string); newName != "" {
			if oldName, _ := oldProps["Name"].(string); oldName != "" && newName != oldName {
				return "", nil, errReplacementRequired
			}
		}
		// Type and Scheme cannot be changed on a live load balancer in real
		// AWS — both require a new resource.
		for _, immutable := range []string{"Type", "Scheme"} {
			newV, _ := props[immutable].(string)
			oldV, _ := oldProps[immutable].(string)
			if newV != "" && oldV != "" && newV != oldV {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":          "ModifyLoadBalancerAttributes",
		"Version":         "2015-12-01",
		"LoadBalancerArn": physicalID,
	}
	if v, ok := props["LoadBalancerAttributes"]; ok {
		if attrs, ok := v.([]any); ok {
			for _, a := range attrs {
				if am, ok := a.(map[string]any); ok {
					if key, _ := am["Key"].(string); key != "" {
						if val, _ := am["Value"].(string); val != "" {
							params[fmt.Sprintf("Attributes.member.%d.Key", len(params))] = key
							params[fmt.Sprintf("Attributes.member.%d.Value", len(params))] = val
						}
					}
				}
			}
		}
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyLoadBalancerAttributes: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::ElasticLoadBalancingV2::TargetGroup ───────────────────────────────

type elbv2TargetGroupHandler struct{}

// elbv2TargetGroupCreateOnlyProps are the TargetGroup properties AWS
// requires replacement for — CreateTargetGroup accepts them but
// ModifyTargetGroup has no field for any of them.
var elbv2TargetGroupCreateOnlyProps = []string{"TargetType", "Protocol", "Port", "VpcId"}

// addELBv2TargetGroupHealthCheckParams copies the CFN HealthCheck*/Matcher
// properties onto a CreateTargetGroup or ModifyTargetGroup params map. A
// property that is absent from props is left out of params entirely — both
// operations treat an absent parameter as "leave the service default (on
// Create) or leave it unchanged (on Modify)", so a zero value has to reach
// the wire distinctly from "not set" for HealthCheckEnabled and the
// interval/threshold counts, which is why this checks presence with `ok`
// rather than testing for a zero value.
func addELBv2TargetGroupHealthCheckParams(params map[string]string, props map[string]any) {
	if _, ok := props["HealthCheckEnabled"]; ok {
		params["HealthCheckEnabled"] = fmtPropString(props, "HealthCheckEnabled")
	}
	if v, _ := props["HealthCheckProtocol"].(string); v != "" {
		params["HealthCheckProtocol"] = v
	}
	if v := fmtPropString(props, "HealthCheckPort"); v != "" {
		params["HealthCheckPort"] = v
	}
	if v, _ := props["HealthCheckPath"].(string); v != "" {
		params["HealthCheckPath"] = v
	}
	if _, ok := props["HealthCheckIntervalSeconds"]; ok {
		params["HealthCheckIntervalSeconds"] = fmtPropString(props, "HealthCheckIntervalSeconds")
	}
	if _, ok := props["HealthCheckTimeoutSeconds"]; ok {
		params["HealthCheckTimeoutSeconds"] = fmtPropString(props, "HealthCheckTimeoutSeconds")
	}
	if _, ok := props["HealthyThresholdCount"]; ok {
		params["HealthyThresholdCount"] = fmtPropString(props, "HealthyThresholdCount")
	}
	if _, ok := props["UnhealthyThresholdCount"]; ok {
		params["UnhealthyThresholdCount"] = fmtPropString(props, "UnhealthyThresholdCount")
	}
	if m, ok := props["Matcher"].(map[string]any); ok {
		if v, _ := m["HttpCode"].(string); v != "" {
			params["Matcher.HttpCode"] = v
		}
		if v, _ := m["GrpcCode"].(string); v != "" {
			params["Matcher.GrpcCode"] = v
		}
	}
}

// addELBv2TagParams copies a CFN Tags property (a list of {Key, Value}
// objects) onto params as a Query-protocol Tags.member.N list under the
// given field name.
func addELBv2TagParams(params map[string]string, props map[string]any, field string) {
	tags, ok := props[field].([]any)
	if !ok {
		return
	}
	idx := 1
	for _, raw := range tags {
		tag, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key, _ := tag["Key"].(string)
		if key == "" {
			continue
		}
		params[fmt.Sprintf("Tags.member.%d.Key", idx)] = key
		params[fmt.Sprintf("Tags.member.%d.Value", idx)] = fmtPropString(tag, "Value")
		idx++
	}
}

// modifyELBv2TargetGroupAttributes applies a CFN TargetGroupAttributes
// property (a list of {Key, Value} objects) via ModifyTargetGroupAttributes.
// CreateTargetGroup has no field for attributes in the real API — they only
// ever reach a target group through this call, so a template that sets
// TargetGroupAttributes needs it run once right after Create too.
func modifyELBv2TargetGroupAttributes(ctx context.Context, router http.Handler, region, tgArn string, props map[string]any) error {
	attrs, ok := props["TargetGroupAttributes"].([]any)
	if !ok || len(attrs) == 0 {
		return nil
	}
	params := map[string]string{
		"Action":         "ModifyTargetGroupAttributes",
		"Version":        "2015-12-01",
		"TargetGroupArn": tgArn,
	}
	idx := 1
	for _, raw := range attrs {
		a, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key, _ := a["Key"].(string)
		if key == "" {
			continue
		}
		params[fmt.Sprintf("Attributes.member.%d.Key", idx)] = key
		params[fmt.Sprintf("Attributes.member.%d.Value", idx)] = fmtPropString(a, "Value")
		idx++
	}
	if _, err := internalQuery(ctx, router, region, params); err != nil {
		return fmt.Errorf("ModifyTargetGroupAttributes: %w", err)
	}
	return nil
}

func (h *elbv2TargetGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameELBv2()
	}

	params := map[string]string{
		"Action":  "CreateTargetGroup",
		"Version": "2015-12-01",
		"Name":    name,
	}
	if v, _ := props["Protocol"].(string); v != "" {
		params["Protocol"] = v
	} else {
		params["Protocol"] = "HTTP"
	}
	if v, _ := props["ProtocolVersion"].(string); v != "" {
		params["ProtocolVersion"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	} else {
		params["Port"] = "80"
	}
	if v, _ := props["VpcId"].(string); v != "" {
		params["VpcId"] = v
	}
	// TargetType matters for the Fargate path specifically: an awsvpc
	// service registers "ip" targets, and a target group provisioned
	// without TargetType came back as the "instance" default regardless of
	// what the template asked for.
	if v, _ := props["TargetType"].(string); v != "" {
		params["TargetType"] = v
	}
	if v, _ := props["IpAddressType"].(string); v != "" {
		params["IpAddressType"] = v
	}
	addELBv2TargetGroupHealthCheckParams(params, props)
	addELBv2TagParams(params, props, "Tags")

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateTargetGroup: %w", err)
	}

	body := rec.Body.String()
	arn := extractXMLValue(body, "TargetGroupArn")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:targetgroup/%s", rCtx.Region, rCtx.AccountID, name)
	}

	if err := modifyELBv2TargetGroupAttributes(ctx, router, rCtx.Region, arn, props); err != nil {
		return "", nil, err
	}

	return arn, map[string]string{"TargetGroupArn": arn}, nil
}

func (h *elbv2TargetGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":         "DeleteTargetGroup",
		"Version":        "2015-12-01",
		"TargetGroupArn": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteTargetGroup", rec, err)
}

func (h *elbv2TargetGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newName, _ := props["Name"].(string); newName != "" {
			if oldName, _ := oldProps["Name"].(string); oldName != "" && newName != oldName {
				return "", nil, errReplacementRequired
			}
		}
		for _, immutable := range elbv2TargetGroupCreateOnlyProps {
			newV := fmtPropString(props, immutable)
			oldV := fmtPropString(oldProps, immutable)
			if newV != "" && oldV != "" && newV != oldV {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":         "ModifyTargetGroup",
		"Version":        "2015-12-01",
		"TargetGroupArn": physicalID,
	}
	addELBv2TargetGroupHealthCheckParams(params, props)
	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyTargetGroup: %w", err)
	}

	if err := modifyELBv2TargetGroupAttributes(ctx, router, rCtx.Region, physicalID, props); err != nil {
		return "", nil, err
	}

	return physicalID, nil, nil
}

// ── AWS::ElasticLoadBalancingV2::Listener ──────────────────────────────────

type elbv2ListenerHandler struct{}

// addELBv2ListenerActionParams forwards a CFN DefaultActions property onto
// params. Type, TargetGroupArn and Order thread through as before;
// RedirectConfig and FixedResponseConfig now forward every member of their
// object wholesale instead of being dropped, which is what turned a CDK
// HTTP→HTTPS redirect listener into a bare "redirect" action with nowhere
// to redirect to. ForwardConfig's weighted-target-group form and the
// Cognito/OIDC auth actions are not modelled — see the Action doc comment
// in service.go.
func addELBv2ListenerActionParams(params map[string]string, actions []any) {
	for i, a := range actions {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		prefix := fmt.Sprintf("DefaultActions.member.%d.", i+1)
		if t, _ := am["Type"].(string); t != "" {
			params[prefix+"Type"] = t
		}
		if targetGroupArn, _ := am["TargetGroupArn"].(string); targetGroupArn != "" {
			params[prefix+"TargetGroupArn"] = targetGroupArn
		}
		if order := fmtPropString(am, "Order"); order != "" {
			params[prefix+"Order"] = order
		}
		if rc, ok := am["RedirectConfig"].(map[string]any); ok {
			for _, field := range []string{"Protocol", "Port", "Host", "Path", "Query", "StatusCode"} {
				if v, _ := rc[field].(string); v != "" {
					params[prefix+"RedirectConfig."+field] = v
				}
			}
		}
		if fr, ok := am["FixedResponseConfig"].(map[string]any); ok {
			for _, field := range []string{"MessageBody", "StatusCode", "ContentType"} {
				if v, _ := fr[field].(string); v != "" {
					params[prefix+"FixedResponseConfig."+field] = v
				}
			}
		}
	}
}

func (h *elbv2ListenerHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	params := map[string]string{
		"Action":  "CreateListener",
		"Version": "2015-12-01",
	}
	if v, _ := props["LoadBalancerArn"].(string); v != "" {
		params["LoadBalancerArn"] = v
	}
	if v, _ := props["Protocol"].(string); v != "" {
		params["Protocol"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	}
	if actions, ok := props["DefaultActions"].([]any); ok {
		addELBv2ListenerActionParams(params, actions)
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateListener: %w", err)
	}

	body := rec.Body.String()
	arn := extractXMLValue(body, "ListenerArn")
	if arn == "" {
		// A listener has no name property on AWS — this is a last-resort
		// physical ID for a CreateListener that answered without an ARN. It
		// still has to be unique per resource: two listeners on one load
		// balancer (an HTTP one and an HTTPS one) are the common shape, and
		// sharing a physical ID makes the stack delete one of them twice and
		// the other never.
		arn = rCtx.generatedName()
	}

	return arn, map[string]string{"ListenerArn": arn}, nil
}

func (h *elbv2ListenerHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":      "DeleteListener",
		"Version":     "2015-12-01",
		"ListenerArn": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteListener", rec, err)
}

func (h *elbv2ListenerHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newLBArn, _ := props["LoadBalancerArn"].(string); newLBArn != "" {
			if oldLBArn, _ := oldProps["LoadBalancerArn"].(string); oldLBArn != "" && newLBArn != oldLBArn {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":      "ModifyListener",
		"Version":     "2015-12-01",
		"ListenerArn": physicalID,
	}
	if v, _ := props["Protocol"].(string); v != "" {
		params["Protocol"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	}
	if actions, ok := props["DefaultActions"].([]any); ok {
		addELBv2ListenerActionParams(params, actions)
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyListener: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::AutoScaling::AutoScalingGroup ─────────────────────────────────────

type autoscalingASGHandler struct{}

// autoscalingASGCreateProperties is everything AWS::AutoScaling::
// AutoScalingGroup's Create acts on. The resource type has more
// (CapacityRebalance, Context, DefaultInstanceWarmup, DesiredCapacityType,
// InstanceId, LifecycleHookSpecificationList, LoadBalancerNames,
// MaxInstanceLifetime, MetricsCollection, NewInstancesProtectedFromScaleIn,
// NotificationConfigurations, PlacementGroup, ServiceLinkedRoleARN,
// TargetGroupARNs), and those reach the user as the resource's
// ResourceStatusReason rather than being dropped in silence — see
// noteUnconsumedProperties in provisioner_properties.go and #540.
var autoscalingASGCreateProperties = []string{
	"AutoScalingGroupName", "MinSize", "MaxSize", "DesiredCapacity", "AvailabilityZones",
	"VPCZoneIdentifier", "LaunchConfigurationName", "LaunchTemplate", "MixedInstancesPolicy",
	"Tags", "Cooldown", "HealthCheckType", "HealthCheckGracePeriod", "TerminationPolicies",
}

// vpcZoneIdentifierParam renders the VPCZoneIdentifier property as the
// comma-separated string CreateAutoScalingGroup takes.
//
// CloudFormation types the property as "Array of String" while the API types it
// as a single comma-separated string, and the resource reference's own examples
// show it both ways — `"VPCZoneIdentifier": ["subnetIdAz1", "subnetIdAz2"]` and
// `{ "Ref": "Subnets" }` resolving to a list. CDK emits the list. Reading only
// the string form dropped the list one silently, which left the group launching
// into no subnet at all rather than failing where a reader would see it.
func vpcZoneIdentifierParam(props map[string]any) string {
	switch v := props["VPCZoneIdentifier"].(type) {
	case string:
		return v
	case []any:
		ids := make([]string, 0, len(v))
		for _, raw := range v {
			if s, _ := raw.(string); s != "" {
				ids = append(ids, s)
			}
		}
		return strings.Join(ids, ",")
	}
	return ""
}

func (h *autoscalingASGHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["AutoScalingGroupName"].(string)
	if name == "" {
		// AutoScalingGroupName carries no documented character rules beyond
		// "ASCII 33-126, no colon", and CreateAutoScalingGroup caps it at 255.
		name = rCtx.generatedName()
	}

	params := map[string]string{
		"Action":               "CreateAutoScalingGroup",
		"Version":              "2011-01-01",
		"AutoScalingGroupName": name,
	}
	if v := fmtPropString(props, "MinSize"); v != "" {
		params["MinSize"] = v
	} else {
		params["MinSize"] = "0"
	}
	if v := fmtPropString(props, "MaxSize"); v != "" {
		params["MaxSize"] = v
	} else {
		params["MaxSize"] = "1"
	}
	if v := fmtPropString(props, "DesiredCapacity"); v != "" {
		params["DesiredCapacity"] = v
	}
	if azs, ok := props["AvailabilityZones"].([]any); ok {
		for i, az := range azs {
			if s, _ := az.(string); s != "" {
				params[fmt.Sprintf("AvailabilityZones.member.%d", i+1)] = s
			}
		}
	}
	if v := vpcZoneIdentifierParam(props); v != "" {
		params["VPCZoneIdentifier"] = v
	}
	if v, _ := props["LaunchConfigurationName"].(string); v != "" {
		params["LaunchConfigurationName"] = v
	}
	// Forward the launch source the template actually named. Auto Scaling
	// refuses launch templates and mixed-instances policies with a 501 that
	// names the gap (its EC2 emulation has no CreateLaunchTemplate to resolve
	// them against, issue #474) — dropping the property here instead would
	// turn that honest refusal into a confusing "no launch source" error, or
	// worse, a group that provisions and never launches anything.
	if lt, ok := props["LaunchTemplate"].(map[string]any); ok {
		if v, _ := lt["LaunchTemplateId"].(string); v != "" {
			params["LaunchTemplate.LaunchTemplateId"] = v
		}
		if v, _ := lt["LaunchTemplateName"].(string); v != "" {
			params["LaunchTemplate.LaunchTemplateName"] = v
		}
		if v, _ := lt["Version"].(string); v != "" {
			params["LaunchTemplate.Version"] = v
		}
	}
	if mip, ok := props["MixedInstancesPolicy"].(map[string]any); ok {
		if lt, ok := mip["LaunchTemplate"].(map[string]any); ok {
			if spec, ok := lt["LaunchTemplateSpecification"].(map[string]any); ok {
				if v, _ := spec["LaunchTemplateId"].(string); v != "" {
					params["MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.LaunchTemplateId"] = v
				}
				if v, _ := spec["LaunchTemplateName"].(string); v != "" {
					params["MixedInstancesPolicy.LaunchTemplate.LaunchTemplateSpecification.LaunchTemplateName"] = v
				}
			}
		}
	}
	if v := fmtPropString(props, "Cooldown"); v != "" {
		// CreateAutoScalingGroupInput.DefaultCooldown is spelled differently
		// than the template's Cooldown property.
		params["DefaultCooldown"] = v
	}
	if v, _ := props["HealthCheckType"].(string); v != "" {
		params["HealthCheckType"] = v
	}
	if v := fmtPropString(props, "HealthCheckGracePeriod"); v != "" {
		params["HealthCheckGracePeriod"] = v
	}
	if policies, ok := props["TerminationPolicies"].([]any); ok {
		for i, p := range policies {
			if s, _ := p.(string); s != "" {
				params[fmt.Sprintf("TerminationPolicies.member.%d", i+1)] = s
			}
		}
	}
	// Tags with PropagateAtLaunch are applied to every instance the
	// reconciler launches, so they have to reach the service.
	if tags, ok := props["Tags"].([]any); ok {
		idx := 1
		for _, raw := range tags {
			tag, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			key, _ := tag["Key"].(string)
			if key == "" {
				continue
			}
			prefix := fmt.Sprintf("Tags.member.%d.", idx)
			params[prefix+"Key"] = key
			params[prefix+"Value"] = fmtPropString(tag, "Value")
			params[prefix+"ResourceId"] = name
			params[prefix+"ResourceType"] = "auto-scaling-group"
			params[prefix+"PropagateAtLaunch"] = fmtPropString(tag, "PropagateAtLaunch")
			idx++
		}
	}
	noteUnconsumedProperties(ctx, "AWS::AutoScaling::AutoScalingGroup", props, autoscalingASGCreateProperties...)

	_, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateAutoScalingGroup: %w", err)
	}

	return name, nil, nil
}

func (h *autoscalingASGHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":               "DeleteAutoScalingGroup",
		"Version":              "2011-01-01",
		"AutoScalingGroupName": physicalID,
		"ForceDelete":          "true",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteAutoScalingGroup", rec, err)
}

func (h *autoscalingASGHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newName, _ := props["AutoScalingGroupName"].(string); newName != "" {
			if oldName, _ := oldProps["AutoScalingGroupName"].(string); oldName != "" && newName != oldName {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":               "UpdateAutoScalingGroup",
		"Version":              "2011-01-01",
		"AutoScalingGroupName": physicalID,
	}
	if v := fmtPropString(props, "MinSize"); v != "" {
		params["MinSize"] = v
	}
	if v := fmtPropString(props, "MaxSize"); v != "" {
		params["MaxSize"] = v
	}
	if v := fmtPropString(props, "DesiredCapacity"); v != "" {
		params["DesiredCapacity"] = v
	}
	if v := vpcZoneIdentifierParam(props); v != "" {
		params["VPCZoneIdentifier"] = v
	}
	if azs, ok := props["AvailabilityZones"].([]any); ok {
		for i, az := range azs {
			if s, _ := az.(string); s != "" {
				params[fmt.Sprintf("AvailabilityZones.member.%d", i+1)] = s
			}
		}
	}
	// UpdateAutoScalingGroup also accepts LaunchConfigurationName,
	// LaunchTemplate and MixedInstancesPolicy on real AWS; this handler
	// leaves those three Create-only, matching Create's own list of what it
	// forwards (autoscalingASGCreateProperties).
	if v := fmtPropString(props, "Cooldown"); v != "" {
		params["DefaultCooldown"] = v
	}
	if v, _ := props["HealthCheckType"].(string); v != "" {
		params["HealthCheckType"] = v
	}
	if v := fmtPropString(props, "HealthCheckGracePeriod"); v != "" {
		params["HealthCheckGracePeriod"] = v
	}
	if policies, ok := props["TerminationPolicies"].([]any); ok {
		for i, p := range policies {
			if s, _ := p.(string); s != "" {
				params[fmt.Sprintf("TerminationPolicies.member.%d", i+1)] = s
			}
		}
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("UpdateAutoScalingGroup: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::AutoScaling::LaunchConfiguration ──────────────────────────────────

type autoscalingLaunchConfigHandler struct{}

// autoscalingLaunchConfigCreateProperties is everything AWS::AutoScaling::
// LaunchConfiguration's Create acts on. The resource type has more
// (AssociatePublicIpAddress, BlockDeviceMappings, ClassicLinkVPCId,
// ClassicLinkVPCSecurityGroups, EbsOptimized, InstanceMonitoring, KernelId,
// PlacementTenancy, RamDiskId, SpotPrice), and those reach the user as the
// resource's ResourceStatusReason rather than being dropped in silence — see
// noteUnconsumedProperties in provisioner_properties.go and #540.
//
// UserData reaches CreateLaunchConfiguration and is stored (LaunchConfig.UserData,
// internal/services/autoscaling/store.go), but the service's own
// DescribeLaunchConfigurations does not echo it back — asgXMLLaunchConfig in
// internal/services/autoscaling/typed_logic.go has no UserData field, unlike
// real AWS's response. That is a pre-existing gap in the AutoScaling service
// itself, outside this package.
var autoscalingLaunchConfigCreateProperties = []string{
	"LaunchConfigurationName", "ImageId", "InstanceType", "SecurityGroups",
	"KeyName", "IamInstanceProfile", "UserData",
}

func (h *autoscalingLaunchConfigHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["LaunchConfigurationName"].(string)
	if name == "" {
		// CreateLaunchConfiguration documents a 1-255 length and no character
		// constraints at all.
		name = rCtx.generatedName()
	}

	params := map[string]string{
		"Action":                  "CreateLaunchConfiguration",
		"Version":                 "2011-01-01",
		"LaunchConfigurationName": name,
	}
	if v, _ := props["ImageId"].(string); v != "" {
		params["ImageId"] = v
	} else {
		params["ImageId"] = "ami-dummy"
	}
	if v, _ := props["InstanceType"].(string); v != "" {
		params["InstanceType"] = v
	} else {
		params["InstanceType"] = "t2.micro"
	}
	if sgs, ok := props["SecurityGroups"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("SecurityGroups.member.%d", i+1)] = s
			}
		}
	}
	if v, _ := props["KeyName"].(string); v != "" {
		params["KeyName"] = v
	}
	if v, _ := props["IamInstanceProfile"].(string); v != "" {
		params["IamInstanceProfile"] = v
	}
	if v, _ := props["UserData"].(string); v != "" {
		params["UserData"] = v
	}
	noteUnconsumedProperties(ctx, "AWS::AutoScaling::LaunchConfiguration", props, autoscalingLaunchConfigCreateProperties...)

	_, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateLaunchConfiguration: %w", err)
	}

	return name, nil, nil
}

func (h *autoscalingLaunchConfigHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":                  "DeleteLaunchConfiguration",
		"Version":                 "2011-01-01",
		"LaunchConfigurationName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteLaunchConfiguration", rec, err)
}

func (h *autoscalingLaunchConfigHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::Route53::HostedZone ───────────────────────────────────────────────

type route53HostedZoneHandler struct{}

// route53ZoneName resolves and normalises the zone name from template
// properties, mirroring the service's canonical form (trailing dot; the
// service also lowercases).
func route53ZoneName(props map[string]any, rCtx *resolveContext) string {
	name, _ := props["Name"].(string)
	if name == "" {
		name = fmt.Sprintf("%s.example.com", rCtx.StackName)
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

func (h *route53HostedZoneHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name := route53ZoneName(props, rCtx)

	req := copyStringAnyMap(props)
	req["Name"] = name
	if _, ok := req["CallerReference"].(string); !ok {
		// CloudFormation itself generates a unique caller reference per
		// create; a stack-derived value would collide with the service's
		// caller-reference dedupe on re-creates.
		req["CallerReference"] = uuid.NewString()
	}
	// The CloudFormation property is a VPCs list; CreateHostedZone takes a
	// single VPC element (further associations use AssociateVPCWithHostedZone).
	if vpcs, ok := req["VPCs"].([]any); ok && len(vpcs) > 0 {
		req["VPC"] = vpcs[0]
	}
	delete(req, "VPCs")
	tags, _ := req["HostedZoneTags"].([]any)
	delete(req, "HostedZoneTags")
	xmlBytes, err := marshalCFNXML("CreateHostedZoneRequest", req, nil, route53ItemName, cfnXMLItemsWrapper)
	if err != nil {
		return "", nil, fmt.Errorf("Route53: marshal request: %w", err)
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2013-04-01/hostedzone", "application/xml", xmlBytes)
	if err != nil {
		return "", nil, fmt.Errorf("CreateHostedZone: %w", err)
	}

	body := rec.Body.String()
	id := extractXMLValue(body, "Id")
	zoneID := id
	if idx := strings.LastIndex(id, "/"); idx >= 0 {
		zoneID = id[idx+1:]
	}

	if len(tags) > 0 {
		if err := route53ChangeTags(ctx, router, rCtx, "hostedzone", zoneID, tags); err != nil {
			return "", nil, err
		}
	}

	// NameServers is not exposed as an attribute: Fn::GetAtt attributes are
	// scalar strings here, and NameServers is list-valued on real AWS.
	attrs := map[string]string{
		"Id":   id,
		"Name": name,
	}
	return zoneID, attrs, nil
}

func (h *route53HostedZoneHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/2013-04-01/hostedzone/"+physicalID, "", nil)
	return teardownError("DeleteHostedZone", rec, err)
}

func (h *route53HostedZoneHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Name identifies the zone — changing it is a replacement on real AWS.
	// A comment-only change updates in place via UpdateHostedZoneComment.
	name := route53ZoneName(props, rCtx)
	if !strings.EqualFold(name, route53ZoneName(oldProps, rCtx)) {
		return "", nil, errReplacementRequired
	}
	var comment string
	if hzc, ok := props["HostedZoneConfig"].(map[string]any); ok {
		comment, _ = hzc["Comment"].(string)
	}
	xmlBytes, err := marshalCFNXML("UpdateHostedZoneCommentRequest", map[string]any{"Comment": comment}, nil, route53ItemName, cfnXMLItemsWrapper)
	if err != nil {
		return "", nil, fmt.Errorf("Route53: marshal UpdateHostedZoneComment: %w", err)
	}
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2013-04-01/hostedzone/"+physicalID, "application/xml", xmlBytes); err != nil {
		return "", nil, fmt.Errorf("UpdateHostedZoneComment: %w", err)
	}
	return physicalID, map[string]string{"Id": "/hostedzone/" + physicalID, "Name": name}, nil
}

// route53ChangeTags applies CloudFormation HostedZoneTags/HealthCheckTags
// entries ({Key, Value} maps) through ChangeTagsForResource.
func route53ChangeTags(ctx context.Context, router http.Handler, rCtx *resolveContext, resourceType, resourceID string, tags []any) error {
	xmlBytes, err := marshalCFNXML("ChangeTagsForResourceRequest", map[string]any{"AddTags": tags}, nil, route53ItemName, route53ListWrapper)
	if err != nil {
		return fmt.Errorf("Route53: marshal ChangeTagsForResource: %w", err)
	}
	path := fmt.Sprintf("/2013-04-01/tags/%s/%s", resourceType, resourceID)
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/xml", xmlBytes); err != nil {
		return fmt.Errorf("ChangeTagsForResource: %w", err)
	}
	return nil
}

// ── AWS::Route53::RecordSet ────────────────────────────────────────────────

type route53RecordSetHandler struct{}

// route53RecordSetKey resolves the record's identifying triple from template
// properties, applying the same defaults and trailing-dot normalisation the
// physical ID ("zone/name/type") is built from.
func route53RecordSetKey(props map[string]any, rCtx *resolveContext) (hostedZoneID, recordName, recordType string) {
	hostedZoneID, _ = props["HostedZoneId"].(string)
	recordName, _ = props["Name"].(string)
	recordType, _ = props["Type"].(string)
	if recordName == "" {
		recordName = fmt.Sprintf("%s.example.com", rCtx.StackName)
	}
	if !strings.HasSuffix(recordName, ".") {
		recordName += "."
	}
	if recordType == "" {
		recordType = "A"
	}
	return hostedZoneID, recordName, recordType
}

// changeRecordSet issues a single-change ChangeResourceRecordSets batch built
// from template properties. Create and Update differ only in the action.
func (h *route53RecordSetHandler) changeRecordSet(ctx context.Context, router http.Handler, rCtx *resolveContext, action, hostedZoneID, recordName, recordType string, props map[string]any) error {
	ttl := int64(300)
	if v := fmtPropInt(props, "TTL"); v != 0 {
		ttl = v
	}

	var resourceRecords []string
	if records, ok := props["ResourceRecords"].([]any); ok {
		for _, rr := range records {
			if s, _ := rr.(string); s != "" {
				resourceRecords = append(resourceRecords, s)
			}
		}
	}

	record := route53RecordSetRequestFromCFN(props, recordName, recordType, ttl, resourceRecords)
	batch := map[string]any{"ChangeBatch": map[string]any{"Changes": []any{map[string]any{"Action": action, "ResourceRecordSet": record}}}}
	xmlBytes, err := marshalCFNXML("ChangeResourceRecordSetsRequest", batch, nil, route53ItemName, route53ListWrapper)
	if err != nil {
		return fmt.Errorf("Route53: marshal ChangeResourceRecordSets: %w", err)
	}

	path := fmt.Sprintf("/2013-04-01/hostedzone/%s/rrset", hostedZoneID)
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/xml", xmlBytes); err != nil {
		return fmt.Errorf("ChangeResourceRecordSets: %w", err)
	}
	return nil
}

func (h *route53RecordSetHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	hostedZoneID, recordName, recordType := route53RecordSetKey(props, rCtx)
	if err := h.changeRecordSet(ctx, router, rCtx, "CREATE", hostedZoneID, recordName, recordType, props); err != nil {
		return "", nil, err
	}
	physicalID := fmt.Sprintf("%s/%s/%s", hostedZoneID, recordName, recordType)
	return physicalID, nil, nil
}

func (h *route53RecordSetHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 3)
	if len(parts) < 3 {
		return nil
	}
	hostedZoneID, recordName, recordType := parts[0], parts[1], parts[2]

	// The service enforces AWS's exact-match delete contract, so fetch the
	// stored record first and delete precisely what exists. A record that is
	// already gone (or a zone already deleted) counts as deleted.
	stored, found, err := route53FetchRecordSet(ctx, router, rCtx, hostedZoneID, recordName, recordType)
	if err != nil || !found {
		return err
	}

	batch := route53ChangeBatchXML{
		Xmlns:   "https://route53.amazonaws.com/doc/2013-04-01/",
		Changes: []route53ChangeXML{{Action: "DELETE", ResourceRecordSet: *stored}},
	}
	xmlBytes, err := xml.Marshal(batch)
	if err != nil {
		return fmt.Errorf("Route53: marshal ChangeResourceRecordSets (delete): %w", err)
	}

	// The record was there a moment ago, but the fetch and the change are two
	// calls: a record that went between them is gone, which is what the delete
	// was for.
	path := fmt.Sprintf("/2013-04-01/hostedzone/%s/rrset", hostedZoneID)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/xml", xmlBytes)
	return teardownError("ChangeResourceRecordSets (delete)", rec, err)
}

// route53RecordSetXML is the wire shape shared by the record fetch and the
// exact-match delete.
type route53RecordSetXML struct {
	Name            string `xml:"Name"`
	Type            string `xml:"Type"`
	SetIdentifier   string `xml:"SetIdentifier,omitempty"`
	TTL             *int64 `xml:"TTL,omitempty"`
	ResourceRecords []struct {
		Value string `xml:"Value"`
	} `xml:"ResourceRecords>ResourceRecord,omitempty"`
	AliasTarget *struct {
		HostedZoneId         string `xml:"HostedZoneId"`
		DNSName              string `xml:"DNSName"`
		EvaluateTargetHealth bool   `xml:"EvaluateTargetHealth"`
	} `xml:"AliasTarget,omitempty"`
}

type route53ChangeXML struct {
	Action            string              `xml:"Action"`
	ResourceRecordSet route53RecordSetXML `xml:"ResourceRecordSet"`
}

type route53ChangeBatchXML struct {
	XMLName xml.Name           `xml:"ChangeResourceRecordSetsRequest"`
	Xmlns   string             `xml:"xmlns,attr"`
	Changes []route53ChangeXML `xml:"ChangeBatch>Changes>Change"`
}

// route53FetchRecordSet looks up one record set by name and type. It returns
// found=false (with no error) when the record or its zone no longer exists.
func route53FetchRecordSet(ctx context.Context, router http.Handler, rCtx *resolveContext, hostedZoneID, recordName, recordType string) (*route53RecordSetXML, bool, error) {
	path := fmt.Sprintf("/2013-04-01/hostedzone/%s/rrset?name=%s&type=%s&maxitems=1",
		hostedZoneID, url.QueryEscape(recordName), url.QueryEscape(recordType))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodGet, path, "", nil)
	if err != nil {
		if rec != nil && rec.Code == http.StatusNotFound {
			return nil, false, nil // zone already deleted
		}
		return nil, false, fmt.Errorf("ListResourceRecordSets: %w", err)
	}
	var out struct {
		ResourceRecordSets []route53RecordSetXML `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		return nil, false, fmt.Errorf("Route53: decode ListResourceRecordSets: %w", err)
	}
	if len(out.ResourceRecordSets) == 0 {
		return nil, false, nil
	}
	got := out.ResourceRecordSets[0]
	if !strings.EqualFold(strings.TrimSuffix(got.Name, "."), strings.TrimSuffix(recordName, ".")) || got.Type != recordType {
		return nil, false, nil // listing continued past the requested record: it does not exist
	}
	return &got, true, nil
}

func (h *route53RecordSetHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts := strings.SplitN(physicalID, "/", 3)
	if len(parts) < 3 {
		return "", nil, errReplacementRequired
	}

	// Zone, name and type identify the record — a change to any of them is a
	// different record (Replacement: Yes on real AWS). Everything else (TTL,
	// ResourceRecords, alias target) updates the record in place.
	hostedZoneID, recordName, recordType := route53RecordSetKey(props, rCtx)
	if hostedZoneID != parts[0] || recordName != parts[1] || recordType != parts[2] {
		return "", nil, errReplacementRequired
	}

	if err := h.changeRecordSet(ctx, router, rCtx, "UPSERT", hostedZoneID, recordName, recordType, props); err != nil {
		return "", nil, err
	}
	return physicalID, nil, nil
}

func route53RecordSetRequestFromCFN(props map[string]any, recordName, recordType string, ttl int64, resourceRecords []string) map[string]any {
	record := copyStringAnyMap(props)
	delete(record, "HostedZoneId")
	delete(record, "HostedZoneName")
	record["Name"] = recordName
	record["Type"] = recordType
	if _, ok := record["AliasTarget"]; !ok {
		record["TTL"] = ttl
		if _, ok := record["ResourceRecords"]; !ok {
			items := make([]any, 0, len(resourceRecords))
			for _, value := range resourceRecords {
				items = append(items, map[string]any{"Value": value})
			}
			record["ResourceRecords"] = items
		} else if records, ok := record["ResourceRecords"].([]any); ok {
			items := make([]any, 0, len(records))
			for _, value := range records {
				if s, ok := value.(string); ok {
					items = append(items, map[string]any{"Value": s})
				} else {
					items = append(items, value)
				}
			}
			record["ResourceRecords"] = items
		}
	}
	return record
}

func route53ItemName(parent string) string {
	switch parent {
	case "Changes":
		return "Change"
	case "ResourceRecords":
		return "ResourceRecord"
	case "AddTags":
		return "Tag"
	case "ChildHealthChecks":
		return "ChildHealthCheck"
	case "Regions":
		return "Region"
	}
	return "Item"
}

func route53ListWrapper(parent string) string {
	switch parent {
	case "Changes", "ResourceRecords", "AddTags", "ChildHealthChecks", "Regions":
		return ""
	}
	return "Items"
}

// ── AWS::Route53::HealthCheck ──────────────────────────────────────────────

type route53HealthCheckHandler struct{}

// route53HealthCheckRequest builds the CreateHealthCheck/UpdateHealthCheck
// payload from the CloudFormation HealthCheckConfig property.
func route53HealthCheckConfig(props map[string]any) map[string]any {
	cfg, _ := props["HealthCheckConfig"].(map[string]any)
	return cfg
}

func (h *route53HealthCheckHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	req := map[string]any{
		// CloudFormation generates a unique caller reference per create.
		"CallerReference":   uuid.NewString(),
		"HealthCheckConfig": route53HealthCheckConfig(props),
	}
	xmlBytes, err := marshalCFNXML("CreateHealthCheckRequest", req, nil, route53ItemName, route53ListWrapper)
	if err != nil {
		return "", nil, fmt.Errorf("Route53: marshal CreateHealthCheck: %w", err)
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2013-04-01/healthcheck", "application/xml", xmlBytes)
	if err != nil {
		return "", nil, fmt.Errorf("CreateHealthCheck: %w", err)
	}
	id := extractXMLValue(rec.Body.String(), "Id")

	if tags, _ := props["HealthCheckTags"].([]any); len(tags) > 0 {
		if err := route53ChangeTags(ctx, router, rCtx, "healthcheck", id, tags); err != nil {
			return "", nil, err
		}
	}
	return id, map[string]string{"HealthCheckId": id}, nil
}

func (h *route53HealthCheckHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/2013-04-01/healthcheck/"+physicalID, "", nil)
	return teardownError("DeleteHealthCheck", rec, err)
}

func (h *route53HealthCheckHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// UpdateHealthCheck accepts the mutable HealthCheckConfig fields
	// directly; Type is immutable (replacement on real AWS).
	newCfg := route53HealthCheckConfig(props)
	oldCfg := route53HealthCheckConfig(oldProps)
	newType, _ := newCfg["Type"].(string)
	oldType, _ := oldCfg["Type"].(string)
	if newType != oldType {
		return "", nil, errReplacementRequired
	}
	req := copyStringAnyMap(newCfg)
	delete(req, "Type")
	delete(req, "MeasureLatency") // immutable on real AWS
	delete(req, "RequestInterval")
	xmlBytes, err := marshalCFNXML("UpdateHealthCheckRequest", req, nil, route53ItemName, route53ListWrapper)
	if err != nil {
		return "", nil, fmt.Errorf("Route53: marshal UpdateHealthCheck: %w", err)
	}
	if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2013-04-01/healthcheck/"+physicalID, "application/xml", xmlBytes); err != nil {
		return "", nil, fmt.Errorf("UpdateHealthCheck: %w", err)
	}
	return physicalID, map[string]string{"HealthCheckId": physicalID}, nil
}

func copyStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// ── AWS::EKS::Cluster ──────────────────────────────────────────────────────
//
// EKS's handlers dispatch over its own REST-JSON bindings (/clusters/{name},
// /clusters/{name}/node-groups/..., etc.) instead of the invented "EKS.<Op>"
// X-Amz-Target prefix #1226 retired: EKS is restJson1 and the pinned models
// never gave it one, so nothing but this provisioner and Overcast's own tests
// ever spoke it.

// eksJSON POSTs/PUTs a JSON body to one EKS REST path.
func eksJSON(ctx context.Context, router http.Handler, region, method, path string, body map[string]any) (*httptest.ResponseRecorder, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return internalRequest(ctx, router, region, method, path, "application/json", data)
}

type eksClusterHandler struct{}

// eksClusterNameFromPhysicalID extracts the cluster name from a physical ID.
//
// Per AWS docs (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-eks-cluster.html#aws-resource-eks-cluster-return-values-ref):
// "Ref returns the resource name" for AWS::EKS::Cluster, so Create below now
// records the cluster name — not the ARN — as the physical ID.
//
// Stacks whose state was persisted before that fix still have the ARN
// ("arn:...:cluster/<name>") recorded as the physical ID. EKS cluster names
// can never contain "/" (CreateCluster's Name pattern is
// `^[0-9A-Za-z][A-Za-z0-9\-_]*`), so a physical ID containing one is
// unambiguously an old ARN-shaped ID, and the segment after the last "/" is
// the name. This keeps Stabilize/Delete/Update working against a store
// written by an older Overcast build without a migration.
func eksClusterNameFromPhysicalID(physicalID string) string {
	if idx := strings.LastIndex(physicalID, "/"); idx >= 0 {
		return physicalID[idx+1:]
	}
	return physicalID
}

// eksClusterCreateProperties is everything AWS::EKS::Cluster's Create acts on.
// The resource type has more (OutpostConfig, UpgradePolicy, ZonalShiftConfig,
// BootstrapSelfManagedAddons, ComputeConfig, RemoteNetworkConfig,
// StorageConfig, Force), and those reach the user as the resource's
// ResourceStatusReason rather than being dropped in silence — see
// noteUnconsumedProperties in provisioner_properties.go and #540.
var eksClusterCreateProperties = []string{
	"Name", "RoleArn", "Version", "ResourcesVpcConfig", "KubernetesNetworkConfig",
	"AccessConfig", "EncryptionConfig", "Logging", "Tags",
}

// eksClusterLogging translates AWS::EKS::Cluster's Logging property into
// CreateCluster's `logging` member, one of the few places a handler has to
// translate rather than forward: the template nests
// `{ClusterLogging: {EnabledTypes: [{Type: "api"}]}}` while the API models
// `{clusterLogging: [{types: ["api"], enabled: true}]}`.
//
// Only the enabled types are sent. AWS also accepts a second LogSetup with
// `enabled: false` naming the rest, but that carries no information the first
// does not — a type absent from EnabledTypes is off — and CreateCluster treats
// an unnamed type as disabled either way.
func eksClusterLogging(raw any) map[string]any {
	logging, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	clusterLogging, ok := logging["ClusterLogging"].(map[string]any)
	if !ok {
		return nil
	}
	enabledTypes, ok := clusterLogging["EnabledTypes"].([]any)
	if !ok {
		return nil
	}
	types := make([]any, 0, len(enabledTypes))
	for _, item := range enabledTypes {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if logType, ok := entry["Type"].(string); ok && logType != "" {
			types = append(types, logType)
		}
	}
	if len(types) == 0 {
		return nil
	}
	return map[string]any{
		"clusterLogging": []any{map[string]any{"types": types, "enabled": true}},
	}
}

func (h *eksClusterHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenEKS)
	}

	body := map[string]any{
		"name": name,
	}
	// ResourcesVpcConfig is forwarded with its nested keys converted, not
	// verbatim: the runtime reads `resourcesVpcConfig.subnetIds` to decide
	// which VPC an EKS control plane's k3s container joins
	// (clusterSubnetIDs, internal/services/eks/live_runtime.go). Handing it
	// the template's `SubnetIds` left every CDK-provisioned cluster on the
	// default network plane while VPC-placed resources in the same stack
	// landed on the VPC's.
	forwardProperties(props, body, "RoleArn", "Version", "ResourcesVpcConfig",
		"KubernetesNetworkConfig", "AccessConfig", "EncryptionConfig")
	if logging := eksClusterLogging(props["Logging"]); logging != nil {
		body["logging"] = logging
	}
	// AWS::EKS::Cluster models Tags as an object, not the usual
	// [{Key,Value}] list, and CreateCluster takes the same object — see
	// cfnTagMap. Stack tags merge in the way every propagating type's do.
	if tags := mergeStackTags(rCtx.StackTags, cfnTagMap(props["Tags"])); len(tags) > 0 {
		body["tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::EKS::Cluster", props, eksClusterCreateProperties...)

	rec, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost, "/clusters", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateCluster: %w", err)
	}

	var resp struct {
		Cluster struct {
			Arn string `json:"arn"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateCluster: parse response: %w", err)
	}

	arn := resp.Cluster.Arn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:eks:%s:%s:cluster/%s", rCtx.Region, rCtx.AccountID, name)
	}

	// Ref returns the cluster name on AWS, not the ARN — see
	// eksClusterNameFromPhysicalID above. The ARN stays available as the Arn
	// GetAtt attribute.
	return name, map[string]string{"Arn": arn}, nil
}

// eksStabilizeTimeout bounds the wait for an EKS control plane to come up. It
// is AWS's own budget for the same wait: botocore's ClusterActive waiter allows
// 30s × 40 attempts. Overcast's live mode starts a k3s container behind an
// image pull, which fits inside that; the cluster's own FAILED status ends the
// wait early, so this is the budget for one that never answers either way.
const eksStabilizeTimeout = 20 * time.Minute

// eksClusterStatuses is botocore's ClusterActive waiter, whose acceptors are
// success on ACTIVE and failure on DELETING and FAILED. The cluster status
// enum has three more — CREATING, UPDATING, PENDING — and the waiter treats
// each as work in progress, which is what an unlisted status does here.
var eksClusterStatuses = statusVocabulary{
	ready:  []string{"ACTIVE"},
	failed: []string{"FAILED", "DELETING"},
}

// Stabilize holds the resource open until the control plane answers. Nothing
// downstream of an EKS cluster works before that: in live mode the endpoint is
// empty until the cluster is ACTIVE, and a kubeconfig fetched against a cluster
// that is still CREATING is refused outright. See resourceStabilizer.
func (h *eksClusterHandler) Stabilize(ctx context.Context, router http.Handler, _ *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	// The physical ID is the cluster name (see eksClusterNameFromPhysicalID);
	// every call after create takes the name, the same way Update reads it
	// back.
	name := eksClusterNameFromPhysicalID(physicalID)
	subject := fmt.Sprintf("EKS cluster %s", name)
	return awaitResourceReady(ctx, clk, stabilizeWait{
		subject:  subject,
		goal:     "become ACTIVE",
		timeout:  eksStabilizeTimeout,
		statuses: eksClusterStatuses,
		describe: func(ctx context.Context) (string, string, error) {
			rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodGet,
				"/clusters/"+url.PathEscape(name), "", nil)
			if err != nil {
				return "", "", fmt.Errorf("DescribeCluster: %s: %w", subject, err)
			}
			var resp struct {
				Cluster struct {
					Status string `json:"status"`
					Health struct {
						Issues []struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"issues"`
					} `json:"health"`
				} `json:"cluster"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				return "", "", fmt.Errorf("DescribeCluster: parse response: %w", err)
			}
			// health.issues is where DescribeCluster carries what went wrong
			// with a cluster, as a code and a message. Prefer the message; a
			// code alone still beats reporting nothing.
			var reason string
			if issues := resp.Cluster.Health.Issues; len(issues) > 0 {
				reason = issues[0].Message
				if reason == "" {
					reason = issues[0].Code
				}
			}
			return resp.Cluster.Status, reason, nil
		},
	})
}

func (h *eksClusterHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Physical ID is the cluster name (see eksClusterNameFromPhysicalID).
	name := eksClusterNameFromPhysicalID(physicalID)
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+url.PathEscape(name), "", nil)
	return teardownError("DeleteCluster", rec, err)
}

func (h *eksClusterHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the cluster name (see eksClusterNameFromPhysicalID).
	// Name and RoleArn replace on real AWS; CreateCluster rejects duplicate
	// names, so replacing under an unchanged name can never succeed.
	oldName := eksClusterNameFromPhysicalID(physicalID)
	if n, ok := props["Name"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	if r, ok := props["RoleArn"].(string); ok && r != "" {
		if or, _ := oldProps["RoleArn"].(string); or != "" && or != r {
			return "", nil, errReplacementRequired
		}
	}

	// UpdateClusterConfig takes resourcesVpcConfig, kubernetesNetworkConfig
	// and logging, so all three go in one call — Create applies the same
	// three, which is what keeps the first deploy and a re-deploy converging
	// on the same cluster (#540 pattern 2).
	clusterConfig := map[string]any{}
	forwardProperties(props, clusterConfig, "ResourcesVpcConfig", "KubernetesNetworkConfig")
	if logging := eksClusterLogging(props["Logging"]); logging != nil {
		clusterConfig["logging"] = logging
	}
	if len(clusterConfig) > 0 {
		if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
			"/clusters/"+url.PathEscape(oldName)+"/update-config", clusterConfig); err != nil {
			return "", nil, fmt.Errorf("UpdateClusterConfig: %w", err)
		}
	}
	if v, ok := props["Version"].(string); ok && v != "" {
		if ov, _ := oldProps["Version"].(string); ov != v {
			body := map[string]any{"version": v}
			if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
				"/clusters/"+url.PathEscape(oldName)+"/updates", body); err != nil {
				return "", nil, fmt.Errorf("UpdateClusterVersion: %w", err)
			}
		}
	}
	// Return the name, not the incoming physicalID: this also normalizes a
	// physical ID persisted as the ARN by a pre-fix Overcast build back to
	// the name on the next successful update.
	return oldName, nil, nil
}

// ── AWS::EKS::Nodegroup ────────────────────────────────────────────────────

type eksNodegroupHandler struct{}

// eksNodegroupCreateProperties is everything AWS::EKS::Nodegroup's Create acts
// on. RemoteAccess, NodeRepairConfig and ForceUpdateEnabled are the remainder,
// and they surface as the resource's ResourceStatusReason rather than being
// dropped in silence — see noteUnconsumedProperties and #540.
var eksNodegroupCreateProperties = []string{
	"ClusterName", "NodegroupName", "NodeRole", "Subnets", "ScalingConfig",
	"InstanceTypes", "AmiType", "CapacityType", "DiskSize", "Labels", "Taints",
	"LaunchTemplate", "UpdateConfig", "Version", "ReleaseVersion", "Tags",
}

func (h *eksNodegroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	nodegroupName, _ := props["NodegroupName"].(string)
	if nodegroupName == "" {
		// EKS documents neither a length nor a character constraint for
		// NodegroupName, on the CloudFormation page or on CreateNodegroup.
		nodegroupName = rCtx.generatedName()
	}

	body := map[string]any{
		"nodegroupName": nodegroupName,
	}
	forwardProperties(props, body, "NodeRole", "Subnets", "ScalingConfig",
		"InstanceTypes", "AmiType", "CapacityType", "DiskSize", "Taints",
		"LaunchTemplate", "UpdateConfig", "Version", "ReleaseVersion")
	// Labels is a map of the user's own keys, so it is forwarded verbatim:
	// the nested-key conversion the members above need would lower-case the
	// first letter of every label the template set.
	forwardPropertiesAs(props, body, map[string]string{"Labels": "labels"})
	if tags := mergeStackTags(rCtx.StackTags, cfnTagMap(props["Tags"])); len(tags) > 0 {
		body["tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::EKS::Nodegroup", props, eksNodegroupCreateProperties...)

	rec, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/clusters/"+url.PathEscape(clusterName)+"/node-groups", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateNodegroup: %w", err)
	}

	var resp struct {
		Nodegroup struct {
			NodegroupArn string `json:"nodegroupArn"`
		} `json:"nodegroup"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateNodegroup: parse response: %w", err)
	}

	arn := resp.Nodegroup.NodegroupArn
	if arn == "" {
		arn = fmt.Sprintf("%s/%s", clusterName, nodegroupName)
	}

	physicalID := fmt.Sprintf("%s/%s", clusterName, nodegroupName)
	return physicalID, map[string]string{"Arn": arn}, nil
}

func (h *eksNodegroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return nil
	}
	clusterName, nodegroupName := parts[0], parts[1]

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+url.PathEscape(clusterName)+"/node-groups/"+url.PathEscape(nodegroupName), "", nil)
	return teardownError("DeleteNodegroup", rec, err)
}

func (h *eksNodegroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is "{clusterName}/{nodegroupName}"; both replace on real
	// AWS, as do NodeRole and Subnets. CreateNodegroup rejects duplicates, so
	// replacing under an unchanged pair can never succeed.
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return "", nil, errReplacementRequired
	}
	if cn, ok := props["ClusterName"].(string); ok && cn != "" && cn != parts[0] {
		return "", nil, errReplacementRequired
	}
	if ngn, ok := props["NodegroupName"].(string); ok && ngn != "" && ngn != parts[1] {
		return "", nil, errReplacementRequired
	}
	if nr, ok := props["NodeRole"].(string); ok && nr != "" {
		if or, _ := oldProps["NodeRole"].(string); or != "" && or != nr {
			return "", nil, errReplacementRequired
		}
	}

	// UpdateNodegroupConfig takes scalingConfig and updateConfig as the same
	// shapes Create sends, so both are applied here. Labels and Taints are
	// not: AWS models them on this operation as add/remove payloads
	// (UpdateLabelsPayload, UpdateTaintsPayload) rather than the plain
	// map and list Create takes, and inventing that translation over a
	// service that models neither would put AWS semantics in the handler.
	// They are applied at Create, and a change to them on a live nodegroup
	// is a gap #540's tail tracks.
	nodegroupConfig := map[string]any{}
	forwardProperties(props, nodegroupConfig, "ScalingConfig", "UpdateConfig")
	if len(nodegroupConfig) > 0 {
		path := "/clusters/" + url.PathEscape(parts[0]) + "/node-groups/" + url.PathEscape(parts[1]) + "/update-config"
		if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost, path, nodegroupConfig); err != nil {
			return "", nil, fmt.Errorf("UpdateNodegroupConfig: %w", err)
		}
	}
	return physicalID, nil, nil
}

// ── AWS::EKS::FargateProfile ───────────────────────────────────────────────

type eksFargateProfileHandler struct{}

func (h *eksFargateProfileHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	fargateProfileName, _ := props["FargateProfileName"].(string)
	if fargateProfileName == "" {
		// As with NodegroupName, EKS documents no constraint on this name.
		fargateProfileName = rCtx.generatedName()
	}

	body := map[string]any{
		"fargateProfileName": fargateProfileName,
	}
	if v, ok := props["PodExecutionRoleArn"].(string); ok && v != "" {
		body["podExecutionRoleArn"] = v
	}
	if v, ok := props["Selectors"].([]any); ok {
		body["selectors"] = v
	}

	_, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/clusters/"+url.PathEscape(clusterName)+"/fargate-profiles", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateFargateProfile: %w", err)
	}

	physicalID := fmt.Sprintf("%s/%s", clusterName, fargateProfileName)
	return physicalID, nil, nil
}

func (h *eksFargateProfileHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return nil
	}
	clusterName, fargateProfileName := parts[0], parts[1]

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+url.PathEscape(clusterName)+"/fargate-profiles/"+url.PathEscape(fargateProfileName), "", nil)
	return teardownError("DeleteFargateProfile", rec, err)
}

func (h *eksFargateProfileHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::EKS::Addon ────────────────────────────────────────────────────────

type eksAddonHandler struct{}

func (h *eksAddonHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	addonName, _ := props["AddonName"].(string)
	if addonName == "" {
		// AddonName is Required: Yes on AWS::EKS::Addon
		// (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-eks-addon.html),
		// unlike almost every other AWS::*::* name property, so there is no
		// AWS behaviour to emulate by minting a placeholder here — a template
		// that omits it fails template validation on real CloudFormation
		// before any resource is touched. #788 made the old placeholder
		// unique per resource; this issue (#1692) removes it rather than
		// generating a name real AWS would never accept.
		return "", nil, requiredPropertyMissing(rCtx.LogicalID, "AWS::EKS::Addon", "AddonName")
	}

	body := map[string]any{
		"addonName": addonName,
	}
	if v, ok := props["AddonVersion"].(string); ok && v != "" {
		body["addonVersion"] = v
	}

	_, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/clusters/"+url.PathEscape(clusterName)+"/addons", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateAddon: %w", err)
	}

	// Ref on AWS::EKS::Addon returns "<cluster-name>|<addon-name>" — pipe
	// separated, unlike the sibling Nodegroup/FargateProfile handlers below,
	// which legitimately use "/" because that is what AWS documents for them.
	// https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-eks-addon.html#aws-resource-eks-addon-return-values
	physicalID := clusterName + "|" + addonName
	return physicalID, nil, nil
}

// eksAddonPhysicalIDParts splits an AWS::EKS::Addon physical ID into its
// cluster and addon name. The current form is "<cluster>|<addon>" (see
// Create above); a stack whose add-on was created before that fix (#1692)
// still holds the old "<cluster>/<addon>" form in its state, so a physical ID
// with no "|" falls back to splitting on "/" rather than failing its next
// delete or update.
func eksAddonPhysicalIDParts(physicalID string) (clusterName, addonName string) {
	if idx := strings.Index(physicalID, "|"); idx >= 0 {
		return physicalID[:idx], physicalID[idx+1:]
	}
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func (h *eksAddonHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	clusterName, addonName := eksAddonPhysicalIDParts(physicalID)
	if clusterName == "" || addonName == "" {
		return nil
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+url.PathEscape(clusterName)+"/addons/"+url.PathEscape(addonName), "", nil)
	return teardownError("DeleteAddon", rec, err)
}

func (h *eksAddonHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// ClusterName and AddonName both replace on real AWS
	// (https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-resource-eks-addon.html);
	// CreateAddon rejects a duplicate name, so replacing under an unchanged
	// pair can never succeed.
	clusterName, addonName := eksAddonPhysicalIDParts(physicalID)
	if cn, ok := props["ClusterName"].(string); ok && cn != "" && cn != clusterName {
		return "", nil, errReplacementRequired
	}
	if an, ok := props["AddonName"].(string); ok && an != "" && an != addonName {
		return "", nil, errReplacementRequired
	}

	body := map[string]any{}
	if v, ok := props["AddonVersion"].(string); ok && v != "" {
		if ov, _ := oldProps["AddonVersion"].(string); ov != v {
			body["addonVersion"] = v
		}
	}
	if v, ok := props["ConfigurationValues"].(string); ok && v != "" {
		if ov, _ := oldProps["ConfigurationValues"].(string); ov != v {
			body["configurationValues"] = v
		}
	}
	if v, ok := props["ServiceAccountRoleArn"].(string); ok && v != "" {
		if ov, _ := oldProps["ServiceAccountRoleArn"].(string); ov != v {
			body["serviceAccountRoleArn"] = v
		}
	}
	if v, ok := props["ResolveConflicts"].(string); ok && v != "" {
		body["resolveConflicts"] = v
	}
	if len(body) > 0 {
		if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
			"/clusters/"+url.PathEscape(clusterName)+"/addons/"+url.PathEscape(addonName)+"/update", body); err != nil {
			return "", nil, fmt.Errorf("UpdateAddon: %w", err)
		}
	}

	// Normalises a pre-#1692 "<cluster>/<addon>" physical ID to the correct
	// "<cluster>|<addon>" form on the next update.
	return clusterName + "|" + addonName, nil, nil
}

// ── AWS::EKS::AccessEntry ──────────────────────────────────────────────────

type eksAccessEntryHandler struct{}

func (h *eksAccessEntryHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	principalArn, _ := props["PrincipalArn"].(string)

	body := map[string]any{
		"principalArn": principalArn,
	}

	_, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/clusters/"+url.PathEscape(clusterName)+"/access-entries", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateAccessEntry: %w", err)
	}

	physicalID := fmt.Sprintf("%s/%s", clusterName, principalArn)
	return physicalID, nil, nil
}

func (h *eksAccessEntryHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return nil
	}
	clusterName, principalArn := parts[0], parts[1]

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+url.PathEscape(clusterName)+"/access-entries/"+url.PathEscape(principalArn), "", nil)
	return teardownError("DeleteAccessEntry", rec, err)
}

func (h *eksAccessEntryHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is "{clusterName}/{principalArn}"; both replace on real
	// AWS, and CreateAccessEntry rejects a duplicate principal, so replacing
	// under an unchanged pair can never succeed.
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) < 2 {
		return "", nil, errReplacementRequired
	}
	if cn, ok := props["ClusterName"].(string); ok && cn != "" && cn != parts[0] {
		return "", nil, errReplacementRequired
	}
	if pa, ok := props["PrincipalArn"].(string); ok && pa != "" && pa != parts[1] {
		return "", nil, errReplacementRequired
	}

	body := map[string]any{}
	send := false
	if v, _ := props["Username"].(string); v != "" {
		body["username"] = v
		send = true
	}
	if v, ok := props["KubernetesGroups"].([]any); ok {
		body["kubernetesGroups"] = v
		send = true
	}
	if send {
		path := "/clusters/" + url.PathEscape(parts[0]) + "/access-entries/" + url.PathEscape(parts[1])
		if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost, path, body); err != nil {
			return "", nil, fmt.Errorf("UpdateAccessEntry: %w", err)
		}
	}
	return physicalID, nil, nil
}

// ── AWS::EKS::PodIdentityAssociation ───────────────────────────────────────

type eksPodIdentityAssociationHandler struct{}

func (h *eksPodIdentityAssociationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	namespace, _ := props["Namespace"].(string)
	serviceAccount, _ := props["ServiceAccount"].(string)
	roleArn, _ := props["RoleArn"].(string)

	body := map[string]any{
		"namespace":      namespace,
		"serviceAccount": serviceAccount,
		"roleArn":        roleArn,
	}

	rec, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost,
		"/clusters/"+url.PathEscape(clusterName)+"/pod-identity-associations", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreatePodIdentityAssociation: %w", err)
	}

	var resp struct {
		AssociationId string `json:"associationId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreatePodIdentityAssociation: parse response: %w", err)
	}

	physicalID := resp.AssociationId
	if physicalID == "" {
		physicalID = fmt.Sprintf("%s/%s/%s", clusterName, namespace, serviceAccount)
	}

	return physicalID, nil, nil
}

func (h *eksPodIdentityAssociationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// Physical ID is the opaque association ID alone (see Create) — the
	// owning cluster's name is not recoverable from it, and Delete is never
	// handed the resource's properties to read ClusterName back out of. Real
	// AWS's DeleteAssociation binding requires the cluster name as an
	// httpLabel regardless, so a placeholder that names no real cluster is
	// used here; it 404s the same "no cluster found" way an empty ClusterName
	// always has, which teardownError already treats as already-gone. See
	// #1226 — this predates the REST migration and is not this issue's fix.
	const noClusterOwnerRecorded = "-"
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete,
		"/clusters/"+noClusterOwnerRecorded+"/pod-identity-associations/"+url.PathEscape(physicalID), "", nil)
	return teardownError("DeletePodIdentityAssociation", rec, err)
}

func (h *eksPodIdentityAssociationHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the opaque association ID; the identifying triple
	// (cluster, namespace, service account — all Replacement: Yes) lives in
	// the stored properties. CreatePodIdentityAssociation rejects a duplicate
	// binding, so replacing under an unchanged triple can never succeed.
	for _, key := range []string{"ClusterName", "Namespace", "ServiceAccount"} {
		nv, _ := props[key].(string)
		ov, _ := oldProps[key].(string)
		if nv != ov {
			return "", nil, errReplacementRequired
		}
	}

	clusterName, _ := props["ClusterName"].(string)
	if v, _ := props["RoleArn"].(string); v != "" {
		body := map[string]any{"roleArn": v}
		path := "/clusters/" + url.PathEscape(clusterName) + "/pod-identity-associations/" + url.PathEscape(physicalID)
		if _, err := eksJSON(ctx, router, rCtx.Region, http.MethodPost, path, body); err != nil {
			return "", nil, fmt.Errorf("UpdatePodIdentityAssociation: %w", err)
		}
	}
	return physicalID, nil, nil
}

// ── AWS::MSK::Cluster ──────────────────────────────────────────────────────

type mskClusterHandler struct{}

// mskClusterCreateProperties is everything AWS::MSK::Cluster's Create acts on,
// which since #540 is every property the resource type has. CurrentVersion is
// in the list because it is an update-only property with no CreateCluster
// member — naming it here says Create considered it, rather than leaving it to
// surface as an ignored property on every stack that sets it.
var mskClusterCreateProperties = []string{
	"ClusterName", "KafkaVersion", "NumberOfBrokerNodes", "BrokerNodeGroupInfo",
	"ClientAuthentication", "EncryptionInfo", "ConfigurationInfo", "LoggingInfo",
	"EnhancedMonitoring", "OpenMonitoring", "StorageMode", "Tags", "CurrentVersion",
}

func (h *mskClusterHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	clusterName, _ := props["ClusterName"].(string)
	if clusterName == "" {
		clusterName = rCtx.generatedNameWithin(maxNameLenMSK)
	}

	body := map[string]any{
		"clusterName": clusterName,
	}
	if v, ok := props["KafkaVersion"].(string); ok && v != "" {
		body["kafkaVersion"] = v
	} else {
		body["kafkaVersion"] = "2.8.1"
	}
	if v := fmtPropInt(props, "NumberOfBrokerNodes"); v != 0 {
		body["numberOfBrokerNodes"] = v
	} else {
		body["numberOfBrokerNodes"] = 3
	}
	forwardProperties(props, body, "BrokerNodeGroupInfo", "ClientAuthentication",
		"EncryptionInfo", "ConfigurationInfo", "LoggingInfo", "EnhancedMonitoring",
		"OpenMonitoring", "StorageMode")
	// AWS::MSK::Cluster models Tags as an object rather than the usual
	// [{Key,Value}] list, and CreateCluster takes the same object.
	if tags := mergeStackTags(rCtx.StackTags, cfnTagMap(props["Tags"])); len(tags) > 0 {
		body["tags"] = tags
	}
	noteUnconsumedProperties(ctx, "AWS::MSK::Cluster", props, mskClusterCreateProperties...)

	// MSK is restJson1 throughout: these four calls used an X-Amz-Target
	// namespace ("Kafka.") that no client sends and that Overcast no longer
	// registers — see internal/services/msk.
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("MSK: marshal request: %w", err)
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/v1/clusters", "application/json", jsonBytes)
	if err != nil {
		return "", nil, fmt.Errorf("CreateCluster: %w", err)
	}

	var resp struct {
		ClusterArn string `json:"clusterArn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateCluster: parse response: %w", err)
	}

	arn := resp.ClusterArn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:kafka:%s:%s:cluster/%s", rCtx.Region, rCtx.AccountID, clusterName)
	}

	return arn, map[string]string{"Arn": arn}, nil
}

// mskStabilizeTimeout bounds the wait for an MSK cluster to come up. Real MSK
// is the slowest resource here by a wide margin — a provisioned cluster takes
// fifteen to thirty minutes to report ACTIVE — and a budget shorter than the
// service's own working case would fail deploys that were going to succeed.
// Overcast's is a broker container behind an image pull, minutes on a cold one.
// Either way the cluster's own FAILED state is the fast way out, so this only
// bites on a cluster that is genuinely wedged.
const mskStabilizeTimeout = 45 * time.Minute

// mskClusterStatuses is the vocabulary an MSK cluster is read against. botocore
// ships no waiters for kafka, so this is the documented ClusterState enum:
// ACTIVE, CREATING, UPDATING, DELETING, FAILED, MAINTENANCE, REBOOTING_BROKER,
// HEALING.
//
// FAILED is what Overcast now reports for a cluster whose brokers never answer,
// and it is terminal: waiting one out would spend the whole budget and then
// report a timeout over a failure the cluster had already named. DELETING is
// terminal for the same reason it is for a cache — a cluster being torn down
// while a stack waits for it will never be ACTIVE. MAINTENANCE,
// REBOOTING_BROKER and HEALING are states a working cluster passes through and
// comes back from, so they keep the wait going.
var mskClusterStatuses = statusVocabulary{
	ready:  []string{"ACTIVE"},
	failed: []string{"FAILED", "DELETING"},
}

// Stabilize holds the resource open until the cluster's brokers are up, so a
// stack cannot complete around a cluster nothing can produce to. See
// resourceStabilizer.
func (h *mskClusterHandler) Stabilize(ctx context.Context, router http.Handler, cfg *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	subject := fmt.Sprintf("MSK cluster %s", mskClusterNameFromARN(physicalID))
	return awaitResourceReady(ctx, clk, stabilizeWait{
		subject:  subject,
		goal:     "become ACTIVE",
		timeout:  mskStabilizeTimeout,
		statuses: mskClusterStatuses,
		describe: func(ctx context.Context) (string, string, error) {
			// The ARN is a non-greedy httpLabel, so it goes into one escaped
			// path segment exactly as an SDK would send it — the same shape
			// Delete uses.
			rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodGet,
				"/v1/clusters/"+url.PathEscape(physicalID), "", nil)
			if err != nil {
				return "", "", fmt.Errorf("DescribeCluster: %s: %w", subject, err)
			}
			var resp struct {
				ClusterInfo struct {
					State     string `json:"state"`
					StateInfo struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"stateInfo"`
				} `json:"clusterInfo"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				return "", "", fmt.Errorf("DescribeCluster: parse response: %w", err)
			}
			// stateInfo is what AWS documents as the code and message for a
			// cluster "in an unusable state", which is where a broker's own
			// failure ends up. Prefer the message; fall back to the code.
			reason := resp.ClusterInfo.StateInfo.Message
			if reason == "" {
				reason = resp.ClusterInfo.StateInfo.Code
			}
			return resp.ClusterInfo.State, reason, nil
		},
	})
}

// mskClusterNameFromARN reads the cluster name out of a cluster ARN
// (arn:aws:kafka:region:account:cluster/name/uuid), so a failure names the
// cluster the template asked for rather than reciting an ARN back.
func mskClusterNameFromARN(clusterARN string) string {
	if parts := strings.Split(clusterARN, "/"); len(parts) >= 2 {
		return parts[1]
	}
	return clusterARN
}

func (h *mskClusterHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	// The ARN is a non-greedy httpLabel, so it goes into one escaped path
	// segment exactly as an SDK would send it.
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/clusters/"+url.PathEscape(physicalID), "", nil)
	return teardownError("DeleteCluster", rec, err)
}

func (h *mskClusterHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::MSK::Configuration ────────────────────────────────────────────────

type mskConfigurationHandler struct{}

func (h *mskConfigurationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		// MSK documents no maximum for a configuration name, only the pattern
		// ^[0-9A-Za-z][0-9A-Za-z-]{0,}$ — alphanumerics and hyphens, starting
		// alphanumeric, which a stack name and a logical ID already satisfy.
		name = rCtx.generatedName()
	}

	body := map[string]any{
		"name": name,
	}
	if v, ok := props["KafkaVersions"].([]any); ok {
		body["kafkaVersions"] = v
	}
	if v, ok := props["ServerProperties"].(string); ok && v != "" {
		body["serverProperties"] = v
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("MSK: marshal request: %w", err)
	}
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/v1/configurations", "application/json", jsonBytes)
	if err != nil {
		return "", nil, fmt.Errorf("CreateConfiguration: %w", err)
	}

	var resp struct {
		Arn string `json:"arn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateConfiguration: parse response: %w", err)
	}

	arn := resp.Arn
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:kafka:%s:%s:configuration/%s", rCtx.Region, rCtx.AccountID, name)
	}

	return arn, map[string]string{"Arn": arn}, nil
}

func (h *mskConfigurationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/configurations/"+url.PathEscape(physicalID), "", nil)
	return teardownError("DeleteConfiguration", rec, err)
}

func (h *mskConfigurationHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::Pipes::Pipe ───────────────────────────────────────────────────────

type pipesPipeHandler struct{}

func (h *pipesPipeHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenPipes)
	}

	body := map[string]any{}
	if v, ok := props["Source"].(string); ok && v != "" {
		body["source"] = v
	}
	if v, ok := props["Target"].(string); ok && v != "" {
		body["target"] = v
	}
	if v, ok := props["RoleArn"].(string); ok && v != "" {
		body["roleArn"] = v
	}

	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("Pipes: marshal request: %w", err)
	}

	path := "/v1/pipes/" + name
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, path, "application/json", jsonBytes)
	if err != nil {
		return "", nil, fmt.Errorf("CreatePipe: %w", err)
	}

	var resp struct {
		Arn string `json:"Arn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreatePipe: parse response: %w", err)
	}

	physicalID := resp.Arn
	if physicalID == "" {
		physicalID = name
	}

	return physicalID, nil, nil
}

func (h *pipesPipeHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/pipes/"+physicalID, "", nil)
	return teardownError("DeletePipe", rec, err)
}

func (h *pipesPipeHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the pipe ARN, "arn:…:pipe/{name}" (or the bare name).
	// Name and Source replace on real AWS; CreatePipe rejects duplicates, so
	// replacing under an unchanged name can never succeed.
	oldName := physicalID
	if idx := strings.LastIndex(oldName, "/"); idx >= 0 {
		oldName = oldName[idx+1:]
	}
	if n, ok := props["Name"].(string); ok && n != "" && n != oldName {
		return "", nil, errReplacementRequired
	}
	if src, ok := props["Source"].(string); ok && src != "" {
		if osrc, _ := oldProps["Source"].(string); osrc != "" && osrc != src {
			return "", nil, errReplacementRequired
		}
	}

	// The emulated pipe PATCHes state and description; the other mutable
	// properties (Target, RoleArn, enrichment) have no update surface yet.
	body := map[string]any{}
	if v, _ := props["DesiredState"].(string); v != "" {
		body["DesiredState"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["Description"] = v
	}
	if len(body) > 0 {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return "", nil, fmt.Errorf("Pipes: marshal update request: %w", err)
		}
		if _, err := internalRequest(ctx, router, rCtx.Region, http.MethodPatch, "/v1/pipes/"+oldName, "application/json", jsonBytes); err != nil {
			return "", nil, fmt.Errorf("UpdatePipe: %w", err)
		}
	}
	return physicalID, nil, nil
}

// ── AWS::IAM::User ─────────────────────────────────────────────────────────

type iamUserHandler struct{}

func (h *iamUserHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidatePrincipalProperties(props, true); err != nil {
		return "", nil, err
	}
	userName, _ := props["UserName"].(string)
	if userName == "" {
		userName = rCtx.generatedNameWithin(maxNameLenIAM)
	}
	loginParams, err := iamLoginProfileParams(userName, props)
	if err != nil {
		return "", nil, err
	}

	params := map[string]string{
		"Action":   "CreateUser",
		"Version":  "2010-05-08",
		"UserName": userName,
	}
	if v, _ := props["Path"].(string); v != "" {
		params["Path"] = v
	}
	if v, _ := props["PermissionsBoundary"].(string); v != "" {
		params["PermissionsBoundary"] = v
	}
	tags, err := iamEffectiveTags(props, rCtx.StackTags)
	if err != nil {
		return "", nil, err
	}
	iamTagParams(params, tags)

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("CreateUser: %w", err)
	}

	// The user's memberships and permissions (#521): AddUserToGroup per Groups
	// entry, AttachUserPolicy per ManagedPolicyArns entry, PutUserPolicy per
	// Policies entry. LoginProfile is dispatched too — IAM does not emulate
	// login profiles, so the create fails loudly instead of silently dropping
	// a property the template set.
	mutations, err := iamUserGroupMutations(userName, props, nil)
	if err != nil {
		return "", nil, err
	}
	managed, err := iamManagedPolicyMutations("User", userName, props, nil)
	if err != nil {
		return "", nil, err
	}
	inline, err := iamInlinePolicyMutations("User", userName, props, nil)
	if err != nil {
		return "", nil, err
	}
	mutations = append(mutations, managed...)
	mutations = append(mutations, inline...)
	if loginParams != nil {
		mutations = append(mutations, iamMutation{action: "CreateLoginProfile", params: loginParams, undoAction: "DeleteLoginProfile", undoParams: map[string]string{"UserName": userName}})
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(mutations); err != nil {
		if cleanupErr := iamQuery(ctx, router, rCtx.Region, "DeleteUser", map[string]string{"UserName": userName}); cleanupErr != nil {
			return "", nil, fmt.Errorf("%w; cleanup newly-created user: %v", err, cleanupErr)
		}
		return "", nil, err
	}
	return userName, nil, nil
}

// iamLoginProfileParams renders the LoginProfile property as CreateLoginProfile
// parameters, or nil when the template does not set one.
func iamLoginProfileParams(userName string, props map[string]any) (map[string]string, error) {
	raw, present := props["LoginProfile"]
	if !present || raw == nil {
		return nil, nil
	}
	profile, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("LoginProfile must be an object")
	}
	params := map[string]string{"UserName": userName}
	if password, _ := profile["Password"].(string); password != "" {
		params["Password"] = password
	}
	if reset, ok := profile["PasswordResetRequired"]; ok {
		params["PasswordResetRequired"] = cfnScalarString(reset)
	}
	return params, nil
}

func (h *iamUserHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":   "DeleteUser",
		"Version":  "2010-05-08",
		"UserName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return iamTeardownError("DeleteUser", physicalID, rec, err)
}

// DeleteWithProperties removes the user's template-declared group memberships,
// managed-policy attachments and inline policies before deleting it — IAM
// answers DeleteConflict while any remain (#710). Out-of-band relationships
// are untouched: only what the stored properties record is removed.
func (h *iamUserHandler) DeleteWithProperties(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, rCtx *resolveContext) error {
	return iamPrincipalTeardown(ctx, router, rCtx, func() error {
		return h.Delete(ctx, router, cfg, physicalID, rCtx)
	}, func() ([]iamMutation, error) {
		mutations, err := iamUserGroupMutations(physicalID, nil, props)
		if err != nil {
			return nil, err
		}
		managed, err := iamManagedPolicyMutations("User", physicalID, nil, props)
		if err != nil {
			return nil, err
		}
		inline, err := iamInlinePolicyMutations("User", physicalID, nil, props)
		if err != nil {
			return nil, err
		}
		mutations = append(mutations, managed...)
		return append(mutations, inline...), nil
	})
}

func (h *iamUserHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if err := iamValidatePrincipalProperties(props, true); err != nil {
		return "", nil, failUpdate(err)
	}
	if _, err := iamLoginProfileParams(physicalID, props); err != nil {
		return "", nil, failUpdate(err)
	}
	if oldProps != nil {
		if newName, _ := props["UserName"].(string); newName != "" {
			if oldName, _ := oldProps["UserName"].(string); oldName != "" && newName != oldName {
				return "", nil, errReplacementRequired
			}
		}
	}

	mutations := make([]iamMutation, 0)
	if iamJSONPropertyChanged(props, oldProps, "Path") {
		newPath, _ := props["Path"].(string)
		if newPath == "" {
			newPath = "/"
		}
		oldPath, _ := oldProps["Path"].(string)
		if oldPath == "" {
			oldPath = "/"
		}
		mutations = append(mutations, iamMutation{
			action: "UpdateUser", params: map[string]string{"UserName": physicalID, "NewPath": newPath},
			undoAction: "UpdateUser", undoParams: map[string]string{"UserName": physicalID, "NewPath": oldPath},
		})
	}
	if iamJSONPropertyChanged(props, oldProps, "PermissionsBoundary") {
		mutations = append(mutations, iamBoundaryMutation("User", physicalID, props, oldProps))
	}
	groups, err := iamUserGroupMutations(physicalID, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	managed, err := iamManagedPolicyMutations("User", physicalID, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	inline, err := iamInlinePolicyMutations("User", physicalID, props, oldProps)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	tags, err := iamTagMutations("User", physicalID, props, oldProps, rCtx.StackTags, rCtx.PreviousStackTags)
	if err != nil {
		return "", nil, failUpdate(err)
	}
	mutations = append(mutations, groups...)
	mutations = append(mutations, managed...)
	mutations = append(mutations, inline...)
	mutations = append(mutations, tags...)
	// A LoginProfile change dispatches the modeled IAM operations; the
	// emulator does not implement them, so the change fails loudly rather
	// than being silently dropped.
	if iamJSONPropertyChanged(props, oldProps, "LoginProfile") {
		loginParams, _ := iamLoginProfileParams(physicalID, props)
		if loginParams != nil {
			mutations = append(mutations, iamMutation{action: "UpdateLoginProfile", params: loginParams})
		} else {
			mutations = append(mutations, iamMutation{action: "DeleteLoginProfile", params: map[string]string{"UserName": physicalID}})
		}
	}
	if err := newIAMTransaction(ctx, router, rCtx.Region).apply(mutations); err != nil {
		return "", nil, classifyIAMTransactionFailure(err)
	}
	return physicalID, nil, nil
}

// ── AWS::IAM::AccessKey ────────────────────────────────────────────────────

type iamAccessKeyHandler struct{}

func (h *iamAccessKeyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	userName, _ := props["UserName"].(string)

	params := map[string]string{
		"Action":   "CreateAccessKey",
		"Version":  "2010-05-08",
		"UserName": userName,
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateAccessKey: %w", err)
	}

	body := rec.Body.String()
	accessKeyID := extractXMLValue(body, "AccessKeyId")

	physicalID := fmt.Sprintf("%s/%s", userName, accessKeyID)
	return physicalID, nil, nil
}

func (h *iamAccessKeyHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	userName := ""
	accessKeyID := physicalID
	if len(parts) == 2 {
		userName = parts[0]
		accessKeyID = parts[1]
	}

	params := map[string]string{
		"Action":      "DeleteAccessKey",
		"Version":     "2010-05-08",
		"AccessKeyId": accessKeyID,
	}
	if userName != "" {
		params["UserName"] = userName
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteAccessKey", rec, err)
}

func (h *iamAccessKeyHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── AWS::WAFv2::WebACL ─────────────────────────────────────────────────────

type wafv2WebACLHandler struct{}

// wafv2WebACLCreateProperties is everything AWS::WAFv2::WebACL's Create acts
// on. The resource type has more (CustomResponseBodies, AssociationConfig,
// CaptchaConfig, ChallengeConfig, TokenDomains, DataProtectionConfig, and the
// scope-adjacent OnSourceDDoSProtectionConfig), and those reach the user as
// the resource's ResourceStatusReason rather than being dropped in silence —
// see noteUnconsumedProperties in provisioner_properties.go and #540.
var wafv2WebACLCreateProperties = []string{
	"Name", "Scope", "DefaultAction", "VisibilityConfig", "Rules", "Description", "Tags",
}

func (h *wafv2WebACLHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenWAFv2)
	}

	scope, _ := props["Scope"].(string)
	if scope == "" {
		scope = "REGIONAL"
	}

	body := map[string]any{
		"Name":  name,
		"Scope": scope,
	}
	if v, ok := props["DefaultAction"].(map[string]any); ok && v != nil {
		body["DefaultAction"] = v
	}
	if v, ok := props["VisibilityConfig"].(map[string]any); ok && v != nil {
		body["VisibilityConfig"] = v
	}
	if v, ok := props["Rules"].([]any); ok {
		body["Rules"] = v
	}
	// CreateWebACL is AWSWAF_20190729's own JSON-target protocol, whose
	// members are the template's own PascalCase names, not restJson1's
	// lowerCamel — forwardProperties would rewrite "Description" to
	// "description" and break the wire shape, so this uses forwardPropertiesAs
	// with an identity mapping instead. The request's Tags member (see
	// createWebACLRequest in internal/services/waf/typed_logic.go) is already
	// the CloudFormation [{Key,Value}] shape, so it forwards untouched too.
	forwardPropertiesAs(props, body, map[string]string{"Description": "Description", "Tags": "Tags"})
	noteUnconsumedProperties(ctx, "AWS::WAFv2::WebACL", props, wafv2WebACLCreateProperties...)

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSWAF_20190729.CreateWebACL", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateWebACL: %w", err)
	}

	var resp struct {
		Summary struct {
			ARN string `json:"ARN"`
			Id  string `json:"Id"`
		} `json:"Summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateWebACL: parse response: %w", err)
	}

	physicalID := fmt.Sprintf("%s/%s", scope, resp.Summary.Id)
	// CreateWebACL answered with no attributes at all, so Fn::GetAtt Arn and
	// Id never resolved. Capacity and LabelNamespace are also documented
	// GetAtt attributes, but CreateWebACL's response — and the WebACL type
	// itself, internal/services/waf/service.go — carries neither, so there is
	// nothing to return them from.
	attrs := map[string]string{
		"Arn": resp.Summary.ARN,
		"Id":  resp.Summary.Id,
	}
	return physicalID, attrs, nil
}

func (h *wafv2WebACLHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	scope := "REGIONAL"
	id := physicalID
	if len(parts) == 2 {
		scope = parts[0]
		id = parts[1]
	}

	body := map[string]any{
		"Id":        id,
		"Scope":     scope,
		"LockToken": fmt.Sprintf("%s-%d", rCtx.StackName, len(rCtx.Resources)),
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSWAF_20190729.DeleteWebACL", body)
	return teardownError("DeleteWebACL", rec, err)
}

func (h *wafv2WebACLHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// ── Helpers ────────────────────────────────────────────────────────────────

func fmtPropInt(props map[string]any, key string) int64 {
	v, ok := props[key]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		if x != "" {
			var i int64
			_, _ = fmt.Sscanf(x, "%d", &i)
			return i
		}
	}
	return 0
}
