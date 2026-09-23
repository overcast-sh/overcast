package autoscaling

// Typed request/response shapes and the single implementation of every
// Auto Scaling operation. The Query codec decodes the form into the request
// struct (json tags) and marshals the response struct (xml tags).

import (
	"context"
	"encoding/xml"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// ── Request types (json tags used by codec.Decode for form mapping) ───

type launchTemplateSpec struct {
	LaunchTemplateId   string `json:"LaunchTemplateId"`
	LaunchTemplateName string `json:"LaunchTemplateName"`
	Version            string `json:"Version"`
}

func (l launchTemplateSpec) present() bool {
	return l.LaunchTemplateId != "" || l.LaunchTemplateName != ""
}

type mixedInstancesLaunchTemplate struct {
	LaunchTemplateSpecification launchTemplateSpec `json:"LaunchTemplateSpecification"`
}

type mixedInstancesPolicySpec struct {
	LaunchTemplate mixedInstancesLaunchTemplate `json:"LaunchTemplate"`
}

func (m mixedInstancesPolicySpec) present() bool {
	return m.LaunchTemplate.LaunchTemplateSpecification.present()
}

type createASGReq struct {
	AutoScalingGroupName             string                   `json:"AutoScalingGroupName"`
	AvailabilityZones                []string                 `json:"AvailabilityZones"`
	LaunchConfigurationName          string                   `json:"LaunchConfigurationName"`
	LaunchTemplate                   launchTemplateSpec       `json:"LaunchTemplate"`
	MixedInstancesPolicy             mixedInstancesPolicySpec `json:"MixedInstancesPolicy"`
	InstanceId                       string                   `json:"InstanceId"`
	MinSize                          int                      `json:"MinSize"`
	MaxSize                          int                      `json:"MaxSize"`
	DesiredCapacity                  *int                     `json:"DesiredCapacity"`
	DefaultCooldown                  *int                     `json:"DefaultCooldown"`
	VPCZoneIdentifier                string                   `json:"VPCZoneIdentifier"`
	HealthCheckType                  string                   `json:"HealthCheckType"`
	HealthCheckGracePeriod           int                      `json:"HealthCheckGracePeriod"`
	TerminationPolicies              []string                 `json:"TerminationPolicies"`
	NewInstancesProtectedFromScaleIn bool                     `json:"NewInstancesProtectedFromScaleIn"`
	Tags                             []asgTagMember           `json:"Tags"`
}

type updateASGReq struct {
	AutoScalingGroupName             string                   `json:"AutoScalingGroupName"`
	MinSize                          *int                     `json:"MinSize"`
	MaxSize                          *int                     `json:"MaxSize"`
	DesiredCapacity                  *int                     `json:"DesiredCapacity"`
	LaunchConfigurationName          string                   `json:"LaunchConfigurationName"`
	LaunchTemplate                   launchTemplateSpec       `json:"LaunchTemplate"`
	MixedInstancesPolicy             mixedInstancesPolicySpec `json:"MixedInstancesPolicy"`
	DefaultCooldown                  *int                     `json:"DefaultCooldown"`
	AvailabilityZones                []string                 `json:"AvailabilityZones"`
	VPCZoneIdentifier                string                   `json:"VPCZoneIdentifier"`
	HealthCheckType                  string                   `json:"HealthCheckType"`
	HealthCheckGracePeriod           *int                     `json:"HealthCheckGracePeriod"`
	TerminationPolicies              []string                 `json:"TerminationPolicies"`
	NewInstancesProtectedFromScaleIn *bool                    `json:"NewInstancesProtectedFromScaleIn"`
}

type describeASGsReq struct {
	AutoScalingGroupNames []string `json:"AutoScalingGroupNames"`
}

type deleteASGReq struct {
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
	ForceDelete          bool   `json:"ForceDelete"`
}

type setDesiredCapacityReq struct {
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
	DesiredCapacity      int    `json:"DesiredCapacity"`
	HonorCooldown        bool   `json:"HonorCooldown"`
}

type terminateInstanceReq struct {
	AutoScalingGroupName           string `json:"AutoScalingGroupName"`
	InstanceId                     string `json:"InstanceId"`
	ShouldDecrementDesiredCapacity bool   `json:"ShouldDecrementDesiredCapacity"`
}

type createLaunchConfigReq struct {
	LaunchConfigurationName string   `json:"LaunchConfigurationName"`
	ImageId                 string   `json:"ImageId"`
	InstanceType            string   `json:"InstanceType"`
	KeyName                 string   `json:"KeyName"`
	SecurityGroups          []string `json:"SecurityGroups"`
	IamInstanceProfile      string   `json:"IamInstanceProfile"`
	UserData                string   `json:"UserData"`
}

type describeLaunchConfigsReq struct {
	LaunchConfigurationNames []string `json:"LaunchConfigurationNames"`
}

type deleteLaunchConfigReq struct {
	LaunchConfigurationName string `json:"LaunchConfigurationName"`
}

type stepAdjustmentMember struct {
	MetricIntervalLowerBound string `json:"MetricIntervalLowerBound"`
	MetricIntervalUpperBound string `json:"MetricIntervalUpperBound"`
	ScalingAdjustment        int    `json:"ScalingAdjustment"`
}

type putScalingPolicyReq struct {
	AutoScalingGroupName    string                 `json:"AutoScalingGroupName"`
	PolicyName              string                 `json:"PolicyName"`
	PolicyType              string                 `json:"PolicyType"`
	AdjustmentType          string                 `json:"AdjustmentType"`
	ScalingAdjustment       int                    `json:"ScalingAdjustment"`
	MinAdjustmentMagnitude  int                    `json:"MinAdjustmentMagnitude"`
	Cooldown                int                    `json:"Cooldown"`
	StepAdjustments         []stepAdjustmentMember `json:"StepAdjustments"`
	MetricAggregationType   string                 `json:"MetricAggregationType"`
	EstimatedInstanceWarmup int                    `json:"EstimatedInstanceWarmup"`
}

type describePoliciesReq struct {
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
}

type deletePolicyReq struct {
	PolicyName           string `json:"PolicyName"`
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
}

type executePolicyReq struct {
	AutoScalingGroupName string  `json:"AutoScalingGroupName"`
	PolicyName           string  `json:"PolicyName"`
	HonorCooldown        bool    `json:"HonorCooldown"`
	MetricValue          float64 `json:"MetricValue"`
	BreachThreshold      float64 `json:"BreachThreshold"`
}

type putLifecycleHookReq struct {
	AutoScalingGroupName  string `json:"AutoScalingGroupName"`
	LifecycleHookName     string `json:"LifecycleHookName"`
	LifecycleTransition   string `json:"LifecycleTransition"`
	DefaultResult         string `json:"DefaultResult"`
	HeartbeatTimeout      int    `json:"HeartbeatTimeout"`
	NotificationTargetARN string `json:"NotificationTargetARN"`
	RoleARN               string `json:"RoleARN"`
}

type describeLifecycleHooksReq struct {
	AutoScalingGroupName string   `json:"AutoScalingGroupName"`
	LifecycleHookNames   []string `json:"LifecycleHookNames"`
}

type deleteLifecycleHookReq struct {
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
	LifecycleHookName    string `json:"LifecycleHookName"`
}

type completeLifecycleActionReq struct {
	AutoScalingGroupName  string `json:"AutoScalingGroupName"`
	LifecycleHookName     string `json:"LifecycleHookName"`
	LifecycleActionToken  string `json:"LifecycleActionToken"`
	InstanceId            string `json:"InstanceId"`
	LifecycleActionResult string `json:"LifecycleActionResult"`
}

type recordHeartbeatReq struct {
	AutoScalingGroupName string `json:"AutoScalingGroupName"`
	LifecycleHookName    string `json:"LifecycleHookName"`
	LifecycleActionToken string `json:"LifecycleActionToken"`
	InstanceId           string `json:"InstanceId"`
}

type setInstanceHealthReq struct {
	InstanceId               string `json:"InstanceId"`
	HealthStatus             string `json:"HealthStatus"`
	ShouldRespectGracePeriod bool   `json:"ShouldRespectGracePeriod"`
}

type setInstanceProtectionReq struct {
	AutoScalingGroupName string   `json:"AutoScalingGroupName"`
	InstanceIds          []string `json:"InstanceIds"`
	ProtectedFromScaleIn bool     `json:"ProtectedFromScaleIn"`
}

type describeInstancesReq struct {
	InstanceIds []string `json:"InstanceIds"`
	MaxRecords  int      `json:"MaxRecords"`
}

type describeActivitiesReq struct {
	AutoScalingGroupName string   `json:"AutoScalingGroupName"`
	ActivityIds          []string `json:"ActivityIds"`
	MaxRecords           int      `json:"MaxRecords"`
}

type asgTagMember struct {
	ResourceId        string `json:"ResourceId"`
	ResourceType      string `json:"ResourceType"`
	Key               string `json:"Key"`
	Value             string `json:"Value"`
	PropagateAtLaunch string `json:"PropagateAtLaunch"`
}

type createOrUpdateTagsReq struct {
	Tags []asgTagMember `json:"Tags"`
}

type deleteTagsReq struct {
	Tags []asgTagMember `json:"Tags"`
}

type asgFilterMember struct {
	Name   string   `json:"Name"`
	Values []string `json:"Values"`
}

type describeTagsReq struct {
	Filters []asgFilterMember `json:"Filters"`
}

// ── Response types (xml tags for QueryXML codec WriteResponse) ─────

type asgResponseMeta struct {
	RequestId string `xml:"RequestId"`
}

// asgEmptyBody is the payload of every void Auto Scaling response. It is
// embedded rather than reused directly so each operation carries its own
// XMLName — AWS names the root element after the operation, and an SDK that
// checks it must see CreateAutoScalingGroupResponse, not a Go type name.
type asgEmptyBody struct {
	Xmlns string          `xml:"xmlns,attr"`
	Meta  asgResponseMeta `xml:"ResponseMetadata"`
}

type createASGResp struct {
	XMLName xml.Name `xml:"CreateAutoScalingGroupResponse"`
	asgEmptyBody
}

type updateASGResp struct {
	XMLName xml.Name `xml:"UpdateAutoScalingGroupResponse"`
	asgEmptyBody
}

type deleteASGResp struct {
	XMLName xml.Name `xml:"DeleteAutoScalingGroupResponse"`
	asgEmptyBody
}

type setDesiredCapacityResp struct {
	XMLName xml.Name `xml:"SetDesiredCapacityResponse"`
	asgEmptyBody
}

type createLaunchConfigResp struct {
	XMLName xml.Name `xml:"CreateLaunchConfigurationResponse"`
	asgEmptyBody
}

type deleteLaunchConfigResp struct {
	XMLName xml.Name `xml:"DeleteLaunchConfigurationResponse"`
	asgEmptyBody
}

type deletePolicyResp struct {
	XMLName xml.Name `xml:"DeletePolicyResponse"`
	asgEmptyBody
}

type executePolicyResp struct {
	XMLName xml.Name `xml:"ExecutePolicyResponse"`
	asgEmptyBody
}

type putLifecycleHookResp struct {
	XMLName xml.Name        `xml:"PutLifecycleHookResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  struct{}        `xml:"PutLifecycleHookResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type deleteLifecycleHookResp struct {
	XMLName xml.Name        `xml:"DeleteLifecycleHookResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  struct{}        `xml:"DeleteLifecycleHookResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type completeLifecycleActionResp struct {
	XMLName xml.Name        `xml:"CompleteLifecycleActionResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  struct{}        `xml:"CompleteLifecycleActionResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type recordHeartbeatResp struct {
	XMLName xml.Name        `xml:"RecordLifecycleActionHeartbeatResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  struct{}        `xml:"RecordLifecycleActionHeartbeatResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type setInstanceHealthResp struct {
	XMLName xml.Name `xml:"SetInstanceHealthResponse"`
	asgEmptyBody
}

type setInstanceProtectionResp struct {
	XMLName xml.Name        `xml:"SetInstanceProtectionResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  struct{}        `xml:"SetInstanceProtectionResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type createOrUpdateTagsResp struct {
	XMLName xml.Name `xml:"CreateOrUpdateTagsResponse"`
	asgEmptyBody
}

type deleteTagsResp struct {
	XMLName xml.Name `xml:"DeleteTagsResponse"`
	asgEmptyBody
}

type describeASGsResp struct {
	XMLName xml.Name        `xml:"DescribeAutoScalingGroupsResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  asgGroupsResult `xml:"DescribeAutoScalingGroupsResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type asgGroupsResult struct {
	AutoScalingGroups []asgXMLGroup `xml:"AutoScalingGroups>member"`
}

// asgXMLLaunchTemplate is AWS's LaunchTemplateSpecification, reported on a
// group and on each instance launched from one.
type asgXMLLaunchTemplate struct {
	LaunchTemplateId   string `xml:"LaunchTemplateId,omitempty"`
	LaunchTemplateName string `xml:"LaunchTemplateName,omitempty"`
	Version            string `xml:"Version,omitempty"`
}

// xmlLaunchTemplate renders a stored specification for a response.
func xmlLaunchTemplate(lt *ASGLaunchTemplate) *asgXMLLaunchTemplate {
	if lt == nil {
		return nil
	}
	return &asgXMLLaunchTemplate{
		LaunchTemplateId:   lt.LaunchTemplateId,
		LaunchTemplateName: lt.LaunchTemplateName,
		Version:            lt.Version,
	}
}

type asgXMLInstance struct {
	InstanceId              string                `xml:"InstanceId"`
	InstanceType            string                `xml:"InstanceType,omitempty"`
	AvailabilityZone        string                `xml:"AvailabilityZone,omitempty"`
	LifecycleState          string                `xml:"LifecycleState"`
	HealthStatus            string                `xml:"HealthStatus"`
	LaunchConfigurationName string                `xml:"LaunchConfigurationName,omitempty"`
	LaunchTemplate          *asgXMLLaunchTemplate `xml:"LaunchTemplate,omitempty"`
	ProtectedFromScaleIn    bool                  `xml:"ProtectedFromScaleIn"`
}

// asgXMLGroupInstance is the DescribeAutoScalingInstances shape: the same
// fields plus the owning group, which the nested form does not repeat.
type asgXMLGroupInstance struct {
	InstanceId              string                `xml:"InstanceId"`
	InstanceType            string                `xml:"InstanceType,omitempty"`
	AutoScalingGroupName    string                `xml:"AutoScalingGroupName"`
	AvailabilityZone        string                `xml:"AvailabilityZone,omitempty"`
	LifecycleState          string                `xml:"LifecycleState"`
	HealthStatus            string                `xml:"HealthStatus"`
	LaunchConfigurationName string                `xml:"LaunchConfigurationName,omitempty"`
	LaunchTemplate          *asgXMLLaunchTemplate `xml:"LaunchTemplate,omitempty"`
	ProtectedFromScaleIn    bool                  `xml:"ProtectedFromScaleIn"`
}

type asgXMLGroup struct {
	AutoScalingGroupName             string                `xml:"AutoScalingGroupName"`
	AutoScalingGroupARN              string                `xml:"AutoScalingGroupARN"`
	LaunchConfigurationName          string                `xml:"LaunchConfigurationName,omitempty"`
	LaunchTemplate                   *asgXMLLaunchTemplate `xml:"LaunchTemplate,omitempty"`
	MinSize                          int                   `xml:"MinSize"`
	MaxSize                          int                   `xml:"MaxSize"`
	DesiredCapacity                  int                   `xml:"DesiredCapacity"`
	DefaultCooldown                  int                   `xml:"DefaultCooldown"`
	AvailabilityZones                []string              `xml:"AvailabilityZones>member"`
	LoadBalancerNames                struct{}              `xml:"LoadBalancerNames"`
	TargetGroupARNs                  struct{}              `xml:"TargetGroupARNs"`
	HealthCheckType                  string                `xml:"HealthCheckType"`
	HealthCheckGracePeriod           int                   `xml:"HealthCheckGracePeriod"`
	Instances                        []asgXMLInstance      `xml:"Instances>member"`
	CreatedTime                      string                `xml:"CreatedTime"`
	SuspendedProcesses               struct{}              `xml:"SuspendedProcesses"`
	VPCZoneIdentifier                string                `xml:"VPCZoneIdentifier,omitempty"`
	EnabledMetrics                   struct{}              `xml:"EnabledMetrics"`
	Status                           string                `xml:"Status,omitempty"`
	Tags                             []asgXMLTag           `xml:"Tags>member"`
	TerminationPolicies              []string              `xml:"TerminationPolicies>member"`
	NewInstancesProtectedFromScaleIn bool                  `xml:"NewInstancesProtectedFromScaleIn"`
}

type terminateInstanceResp struct {
	XMLName xml.Name                `xml:"TerminateInstanceInAutoScalingGroupResponse"`
	Xmlns   string                  `xml:"xmlns,attr"`
	Result  terminateInstanceResult `xml:"TerminateInstanceInAutoScalingGroupResult"`
	Meta    asgResponseMeta         `xml:"ResponseMetadata"`
}

type terminateInstanceResult struct {
	Activity asgActivityXML `xml:"Activity"`
}

type asgActivityXML struct {
	ActivityId           string `xml:"ActivityId"`
	AutoScalingGroupName string `xml:"AutoScalingGroupName"`
	AutoScalingGroupARN  string `xml:"AutoScalingGroupARN,omitempty"`
	Description          string `xml:"Description"`
	Cause                string `xml:"Cause"`
	StartTime            string `xml:"StartTime"`
	EndTime              string `xml:"EndTime,omitempty"`
	StatusCode           string `xml:"StatusCode"`
	StatusMessage        string `xml:"StatusMessage,omitempty"`
	Progress             int    `xml:"Progress"`
	Details              string `xml:"Details,omitempty"`
}

type describeActivitiesResp struct {
	XMLName xml.Name         `xml:"DescribeScalingActivitiesResponse"`
	Xmlns   string           `xml:"xmlns,attr"`
	Result  activitiesResult `xml:"DescribeScalingActivitiesResult"`
	Meta    asgResponseMeta  `xml:"ResponseMetadata"`
}

type activitiesResult struct {
	Activities []asgActivityXML `xml:"Activities>member"`
}

type describeLaunchConfigsResp struct {
	XMLName xml.Name            `xml:"DescribeLaunchConfigurationsResponse"`
	Xmlns   string              `xml:"xmlns,attr"`
	Result  launchConfigsResult `xml:"DescribeLaunchConfigurationsResult"`
	Meta    asgResponseMeta     `xml:"ResponseMetadata"`
}

type launchConfigsResult struct {
	LaunchConfigurations []asgXMLLaunchConfig `xml:"LaunchConfigurations>member"`
}

type asgXMLLaunchConfig struct {
	LaunchConfigurationName string   `xml:"LaunchConfigurationName"`
	LaunchConfigurationARN  string   `xml:"LaunchConfigurationARN"`
	ImageId                 string   `xml:"ImageId"`
	InstanceType            string   `xml:"InstanceType"`
	KeyName                 string   `xml:"KeyName,omitempty"`
	SecurityGroups          []string `xml:"SecurityGroups>member,omitempty"`
	IamInstanceProfile      string   `xml:"IamInstanceProfile,omitempty"`
	UserData                string   `xml:"UserData,omitempty"`
	CreatedTime             string   `xml:"CreatedTime"`
}

type putScalingPolicyResp struct {
	XMLName xml.Name               `xml:"PutScalingPolicyResponse"`
	Xmlns   string                 `xml:"xmlns,attr"`
	Result  putScalingPolicyResult `xml:"PutScalingPolicyResult"`
	Meta    asgResponseMeta        `xml:"ResponseMetadata"`
}

type putScalingPolicyResult struct {
	PolicyARN string   `xml:"PolicyARN"`
	Alarms    struct{} `xml:"Alarms"`
}

type describePoliciesResp struct {
	XMLName xml.Name        `xml:"DescribePoliciesResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  policiesResult  `xml:"DescribePoliciesResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type policiesResult struct {
	ScalingPolicies []asgXMLPolicy `xml:"ScalingPolicies>member"`
}

type asgXMLStepAdjustment struct {
	MetricIntervalLowerBound string `xml:"MetricIntervalLowerBound,omitempty"`
	MetricIntervalUpperBound string `xml:"MetricIntervalUpperBound,omitempty"`
	ScalingAdjustment        int    `xml:"ScalingAdjustment"`
}

type asgXMLPolicy struct {
	PolicyARN               string                 `xml:"PolicyARN"`
	PolicyName              string                 `xml:"PolicyName"`
	AutoScalingGroupName    string                 `xml:"AutoScalingGroupName"`
	PolicyType              string                 `xml:"PolicyType,omitempty"`
	AdjustmentType          string                 `xml:"AdjustmentType,omitempty"`
	ScalingAdjustment       int                    `xml:"ScalingAdjustment,omitempty"`
	MinAdjustmentMagnitude  int                    `xml:"MinAdjustmentMagnitude,omitempty"`
	Cooldown                int                    `xml:"Cooldown,omitempty"`
	StepAdjustments         []asgXMLStepAdjustment `xml:"StepAdjustments>member,omitempty"`
	MetricAggregationType   string                 `xml:"MetricAggregationType,omitempty"`
	EstimatedInstanceWarmup int                    `xml:"EstimatedInstanceWarmup,omitempty"`
	Alarms                  struct{}               `xml:"Alarms"`
}

type describeLifecycleHooksResp struct {
	XMLName xml.Name             `xml:"DescribeLifecycleHooksResponse"`
	Xmlns   string               `xml:"xmlns,attr"`
	Result  lifecycleHooksResult `xml:"DescribeLifecycleHooksResult"`
	Meta    asgResponseMeta      `xml:"ResponseMetadata"`
}

type lifecycleHooksResult struct {
	LifecycleHooks []asgXMLHook `xml:"LifecycleHooks>member"`
}

type asgXMLHook struct {
	LifecycleHookName     string `xml:"LifecycleHookName"`
	AutoScalingGroupName  string `xml:"AutoScalingGroupName"`
	LifecycleTransition   string `xml:"LifecycleTransition,omitempty"`
	DefaultResult         string `xml:"DefaultResult,omitempty"`
	HeartbeatTimeout      int    `xml:"HeartbeatTimeout"`
	GlobalTimeout         int    `xml:"GlobalTimeout"`
	NotificationTargetARN string `xml:"NotificationTargetARN,omitempty"`
	RoleARN               string `xml:"RoleARN,omitempty"`
}

type describeTagsResp struct {
	XMLName xml.Name        `xml:"DescribeTagsResponse"`
	Xmlns   string          `xml:"xmlns,attr"`
	Result  tagsResult      `xml:"DescribeTagsResult"`
	Meta    asgResponseMeta `xml:"ResponseMetadata"`
}

type tagsResult struct {
	Tags []asgXMLTag `xml:"Tags>member"`
}

type asgXMLTag struct {
	ResourceId        string `xml:"ResourceId"`
	ResourceType      string `xml:"ResourceType,omitempty"`
	Key               string `xml:"Key"`
	Value             string `xml:"Value,omitempty"`
	PropagateAtLaunch bool   `xml:"PropagateAtLaunch"`
}

type describeInstancesResp struct {
	XMLName xml.Name          `xml:"DescribeAutoScalingInstancesResponse"`
	Xmlns   string            `xml:"xmlns,attr"`
	Result  asgInstanceResult `xml:"DescribeAutoScalingInstancesResult"`
	Meta    asgResponseMeta   `xml:"ResponseMetadata"`
}

type asgInstanceResult struct {
	AutoScalingInstances []asgXMLGroupInstance `xml:"AutoScalingInstances>member"`
}

// ── Shared helpers ─────────────────────────────────────────────────

func asgMetaFromCtx(ctx context.Context) asgResponseMeta {
	return asgResponseMeta{RequestId: protocol.RequestIDFromContext(ctx)}
}

func (h *Handler) emptyBody(ctx context.Context) asgEmptyBody {
	return asgEmptyBody{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}
}

func asgInternalError(what string) *protocol.AWSError {
	return &protocol.AWSError{Code: "InternalFailure", Message: "failed to persist " + what, HTTPStatus: http.StatusInternalServerError}
}

func groupNotFound(name string) *protocol.AWSError {
	return asgValidationError("AutoScalingGroup name not found - AutoScalingGroup %s not found", name)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(awsTimeLayout)
}

// ── Auto Scaling groups ────────────────────────────────────────────

// validateGroupInput refuses the group shapes the reconciler cannot converge.
// A group accepted here is one that can really reach its desired capacity.
func validateGroupInput(name string, lt launchTemplateSpec, mip mixedInstancesPolicySpec, instanceID, launchConfig string, requireSource bool) *protocol.AWSError {
	if name == "" {
		return asgValidationError("AutoScalingGroupName is required")
	}
	switch {
	case mip.present():
		return asgUnsupported("Overcast does not implement MixedInstancesPolicy: the emulator has no instance-type fleet or spot allocation to distribute over, so the group would report a desired capacity it could not satisfy.")
	case instanceID != "":
		return asgUnsupported("Overcast does not implement creating an Auto Scaling group from an existing instance (InstanceId): the launch parameters cannot be derived from a running instance. Use CreateLaunchConfiguration and LaunchConfigurationName instead.")
	}
	if requireSource && launchConfig == "" && !lt.present() {
		return asgValidationError("Valid requests must contain either LaunchTemplate, LaunchConfigurationName, InstanceId or MixedInstancesPolicy parameter.")
	}
	return nil
}

// resolveGroupPlacement settles a group's AvailabilityZones against its
// VPCZoneIdentifier and returns the zones to store.
//
// The subnets win. CreateAutoScalingGroup and UpdateAutoScalingGroup both
// document AvailabilityZones as a list "used for launching into the default VPC
// subnet in each Availability Zone *when not using the VPCZoneIdentifier
// property*", so a group that names subnets takes its zones from them — which
// is what makes the parameter optional for the CDK-shaped request that passes
// VPCZoneIdentifier alone. Given both, the reference requires that "the subnets
// that you specify must reside in those Availability Zones", and real Auto
// Scaling compares the two as *sets*: a group listing four zones with subnets
// in only three of them is refused too, which is the shape most of the reported
// failures are (an Fn::GetAZs zone list against a hand-written subnet list).
// moto compares the same way (moto/autoscaling/models.py, _set_azs_and_vpcs).
//
// A VPCZoneIdentifier EC2 cannot fully resolve is left alone rather than
// refused — see subnetZones.
func (s *Service) resolveGroupPlacement(ctx context.Context, zones []string, vpcZoneIdentifier string) ([]string, *protocol.AWSError) {
	if vpcZoneIdentifier == "" {
		return zones, nil
	}
	derived, ok := s.subnetZones(ctx, vpcZoneIdentifier)
	if !ok {
		return zones, nil
	}
	if len(zones) > 0 && !sameZoneSet(zones, derived) {
		return nil, zonesDoNotMatchError()
	}
	return derived, nil
}

// zonesDoNotMatchError is the error real Auto Scaling answers when a group's
// zones and its subnets' zones are not the same set.
//
// Neither operation's reference lists an error for breaking the rule, so the
// code is the one the Auto Scaling common-error list defines for input that
// "doesn't meet the required format or constraints" — ValidationError, HTTP
// 400 — and the message is the one real AWS returns, pasted from a
// CloudFormation failure event in aws/amazon-ecs-cli#47 and reported in the
// same words wherever the failure is discussed. moto raises the same
// ValidationError with "the Auto Scaling group" where the real responses say
// "the AutoScalingGroup"; the real responses are what is followed here.
func zonesDoNotMatchError() *protocol.AWSError {
	return asgValidationError("The availability zones of the specified subnets and the AutoScalingGroup do not match")
}

// validatePlacement refuses a group that names neither AvailabilityZones nor
// any VPCZoneIdentifier subnet. Such a group has nowhere to launch: the
// reconciler's nextPlacement returns an empty subnet and an empty zone, and
// EC2's RunInstances then falls back to <region>a — a shape real AWS cannot
// produce, because it refuses the group instead.
//
// Neither parameter is marked Required in the CreateAutoScalingGroup or
// UpdateAutoScalingGroup reference, and neither Errors section names a code
// for the combination, so the code is the one the Auto Scaling common-error
// list defines for input that "doesn't meet the required format or
// constraints" — ValidationError, HTTP 400 — the same reasoning as
// zonesDoNotMatchError. The conditionality is documented rather than stated:
// AvailabilityZones is "used for launching into the default VPC subnet in each
// Availability Zone *when not using the VPCZoneIdentifier property*", and
// CloudFormation types VPCZoneIdentifier "Required: Conditional — Required to
// launch instances into a nondefault VPC".
//
// The message is real Auto Scaling's, reported verbatim through the Ruby SDK
// as "AWS::AutoScaling::Errors::ValidationError: At least one Availability
// Zone or VPC Subnet is required." in chef-boneyard/chef-provisioning-aws#135;
// moto raises the identical string, trailing full stop included, from
// _set_azs_and_vpcs in moto/autoscaling/models.py.
//
// The subnets are counted with subnetIDs rather than by the raw string, so a
// VPCZoneIdentifier that is only separators and spaces is the no-placement
// case it really is — the same split the reconciler launches by.
func validatePlacement(zones []string, vpcZoneIdentifier string) *protocol.AWSError {
	if len(zones) > 0 || len(subnetIDs(vpcZoneIdentifier)) > 0 {
		return nil
	}
	return asgValidationError("At least one Availability Zone or VPC Subnet is required.")
}

// sameZoneSet reports whether two zone lists name the same zones, ignoring
// order and repeats: a group may list one zone twice, or hold two subnets in
// one zone, without that being a mismatch.
func sameZoneSet(a, b []string) bool {
	set := func(zones []string) map[string]struct{} {
		m := make(map[string]struct{}, len(zones))
		for _, z := range zones {
			m[z] = struct{}{}
		}
		return m
	}
	return maps.Equal(set(a), set(b))
}

// validateCapacity applies AWS's min/max/desired constraints.
func validateCapacity(min, max, desired int) *protocol.AWSError {
	if min < 0 {
		return asgValidationError("Value '%d' at 'minSize' failed to satisfy constraint: Member must have value greater than or equal to 0", min)
	}
	if max < min {
		return asgValidationError("Min size:%d must be less than or equal to max size:%d", min, max)
	}
	if desired < min || desired > max {
		return asgValidationError("Desired capacity:%d must be between the min size:%d and the max size:%d", desired, min, max)
	}
	return nil
}

func (h *Handler) createASGTyped(ctx context.Context, req *createASGReq) (*createASGResp, *protocol.AWSError) {
	s := h.svc
	if aerr := validateGroupInput(req.AutoScalingGroupName, req.LaunchTemplate, req.MixedInstancesPolicy,
		req.InstanceId, req.LaunchConfigurationName, true); aerr != nil {
		return nil, aerr
	}

	desired := req.MinSize
	if req.DesiredCapacity != nil {
		desired = *req.DesiredCapacity
	}
	if aerr := validateCapacity(req.MinSize, req.MaxSize, desired); aerr != nil {
		return nil, aerr
	}

	// Checked against the request, not against what resolveGroupPlacement
	// settles on: a group naming subnets EC2 cannot resolve keeps an empty
	// zone list on purpose (subnetZones), and it is placed all the same.
	if aerr := validatePlacement(req.AvailabilityZones, req.VPCZoneIdentifier); aerr != nil {
		return nil, aerr
	}

	// Settled before the group is stored: a group whose zones and subnets
	// contradict each other would otherwise be accepted and then have every
	// launch refused by EC2 one at a time (#1839), never reaching its desired
	// capacity for a reason only the activity log carries.
	zones, aerr := s.resolveGroupPlacement(ctx, req.AvailabilityZones, req.VPCZoneIdentifier)
	if aerr != nil {
		return nil, aerr
	}

	cooldown := defaultCooldown
	if req.DefaultCooldown != nil {
		cooldown = *req.DefaultCooldown
	}
	healthCheck := req.HealthCheckType
	if healthCheck == "" {
		healthCheck = "EC2"
	}

	// The template is resolved through EC2 before the group is stored, so a
	// group naming one that does not exist is refused rather than left
	// reporting a desired capacity the reconciler could never satisfy.
	var launchTemplate *ASGLaunchTemplate
	if req.LaunchTemplate.present() {
		resolved, aerr := s.resolveLaunchTemplate(ctx, req.LaunchTemplate)
		if aerr != nil {
			return nil, aerr
		}
		launchTemplate = resolved
	}

	asg := AutoScalingGroup{
		AutoScalingGroupName:             req.AutoScalingGroupName,
		AutoScalingGroupARN:              s.asgARN(req.AutoScalingGroupName),
		LaunchConfigurationName:          req.LaunchConfigurationName,
		LaunchTemplate:                   launchTemplate,
		MinSize:                          req.MinSize,
		MaxSize:                          req.MaxSize,
		DesiredCapacity:                  desired,
		DefaultCooldown:                  cooldown,
		AvailabilityZones:                zones,
		VPCZoneIdentifier:                req.VPCZoneIdentifier,
		HealthCheckType:                  healthCheck,
		HealthCheckGracePeriod:           req.HealthCheckGracePeriod,
		TerminationPolicies:              req.TerminationPolicies,
		NewInstancesProtectedFromScaleIn: req.NewInstancesProtectedFromScaleIn,
		CreatedTime:                      s.clk.Now(),
	}

	s.mu.Lock()
	if _, exists := s.st.getGroup(ctx, req.AutoScalingGroupName); exists {
		s.mu.Unlock()
		return nil, &protocol.AWSError{
			Code: "AlreadyExists", Message: fmt.Sprintf("AutoScalingGroup by this name already exists - A group with the name %s already exists", req.AutoScalingGroupName),
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if err := s.st.putGroup(ctx, &asg); err != nil {
		s.mu.Unlock()
		return nil, asgInternalError("group")
	}
	for _, tm := range req.Tags {
		resourceID := tm.ResourceId
		if resourceID == "" {
			resourceID = req.AutoScalingGroupName
		}
		_ = putJSON(ctx, s.st.store, nsGroupTags, resourceID+"/"+tm.Key, &GroupTag{
			ResourceId: resourceID, ResourceType: "auto-scaling-group", Key: tm.Key,
			Value: tm.Value, PropagateAtLaunch: tm.PropagateAtLaunch == "true",
		})
	}
	s.mu.Unlock()

	s.invalidateGroupCount()
	s.poke()
	return &createASGResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) updateASGTyped(ctx context.Context, req *updateASGReq) (*updateASGResp, *protocol.AWSError) {
	s := h.svc
	if aerr := validateGroupInput(req.AutoScalingGroupName, req.LaunchTemplate, req.MixedInstancesPolicy,
		"", req.LaunchConfigurationName, false); aerr != nil {
		return nil, aerr
	}

	// Resolved before the group is locked, for the same reason as on create: a
	// template that does not exist must not become a group's launch source.
	var launchTemplate *ASGLaunchTemplate
	if req.LaunchTemplate.present() {
		resolved, aerr := s.resolveLaunchTemplate(ctx, req.LaunchTemplate)
		if aerr != nil {
			return nil, aerr
		}
		launchTemplate = resolved
	}

	// Zones and subnets are settled before the group is locked too, and against
	// whatever the group already has: an update that changes only the zones is
	// checked against the stored subnets, so it cannot walk a group into the
	// mismatch create refuses.
	subnets := req.VPCZoneIdentifier
	if len(req.AvailabilityZones) > 0 && subnets == "" {
		s.mu.Lock()
		stored, exists := s.st.getGroup(ctx, req.AutoScalingGroupName)
		s.mu.Unlock()
		if exists {
			subnets = stored.VPCZoneIdentifier
		}
	}
	zones, aerr := s.resolveGroupPlacement(ctx, req.AvailabilityZones, subnets)
	if aerr != nil {
		return nil, aerr
	}

	s.mu.Lock()
	g, found := s.st.getGroup(ctx, req.AutoScalingGroupName)
	if !found {
		s.mu.Unlock()
		return nil, groupNotFound(req.AutoScalingGroupName)
	}

	before := g.DesiredCapacity
	if req.MinSize != nil {
		g.MinSize = *req.MinSize
	}
	if req.MaxSize != nil {
		g.MaxSize = *req.MaxSize
	}
	if req.DesiredCapacity != nil {
		g.DesiredCapacity = *req.DesiredCapacity
	} else {
		// AWS pulls the desired capacity into the new bounds rather than
		// leaving a group outside its own min/max.
		g.DesiredCapacity = clampCapacity(g.DesiredCapacity, g.MinSize, g.MaxSize)
	}
	// A group has one launch source, so setting either clears the other, as
	// AWS does when an update moves a group between the two.
	if req.LaunchConfigurationName != "" {
		g.LaunchConfigurationName = req.LaunchConfigurationName
		g.LaunchTemplate = nil
	}
	if launchTemplate != nil {
		g.LaunchTemplate = launchTemplate
		g.LaunchConfigurationName = ""
	}
	if req.DefaultCooldown != nil {
		g.DefaultCooldown = *req.DefaultCooldown
	}
	// zones is what resolveGroupPlacement settled on: the request's own zones
	// when it named no subnets, and the subnets' zones whenever it did — so an
	// update that swaps a group onto subnets in other zones moves the zone list
	// with them, as AWS does.
	if len(zones) > 0 {
		g.AvailabilityZones = zones
	}
	if req.VPCZoneIdentifier != "" {
		g.VPCZoneIdentifier = req.VPCZoneIdentifier
	}
	if req.HealthCheckType != "" {
		g.HealthCheckType = req.HealthCheckType
	}
	if req.HealthCheckGracePeriod != nil {
		g.HealthCheckGracePeriod = *req.HealthCheckGracePeriod
	}
	if len(req.TerminationPolicies) > 0 {
		g.TerminationPolicies = req.TerminationPolicies
	}
	if req.NewInstancesProtectedFromScaleIn != nil {
		g.NewInstancesProtectedFromScaleIn = *req.NewInstancesProtectedFromScaleIn
	}

	if aerr := validateCapacity(g.MinSize, g.MaxSize, g.DesiredCapacity); aerr != nil {
		s.mu.Unlock()
		return nil, aerr
	}
	// Applied to the group as the update would leave it, not to the request:
	// an update naming no placement is the ordinary case and must stay
	// accepted, while a group that would be left with neither is refused
	// wherever it got into that state. Today that is a group persisted by an
	// Overcast from before create validated this, and an update supplying
	// either parameter heals it.
	if aerr := validatePlacement(g.AvailabilityZones, g.VPCZoneIdentifier); aerr != nil {
		s.mu.Unlock()
		return nil, aerr
	}
	if err := s.st.putGroup(ctx, g); err != nil {
		s.mu.Unlock()
		return nil, asgInternalError("group")
	}
	if g.DesiredCapacity != before {
		now := s.clk.Now().UTC()
		s.recordActivity(ctx, g, &Activity{
			Description: fmt.Sprintf("Setting desired capacity to %d.", g.DesiredCapacity),
			Cause:       causeUserRequest(now, before, g.DesiredCapacity, g.MinSize, g.MaxSize),
			StartTime:   now, EndTime: now, StatusCode: "Successful", Progress: 100,
		})
	}
	s.mu.Unlock()

	s.poke()
	return &updateASGResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) describeASGsTyped(ctx context.Context, req *describeASGsReq) (*describeASGsResp, *protocol.AWSError) {
	s := h.svc
	filterSet := make(map[string]bool, len(req.AutoScalingGroupNames))
	for _, n := range req.AutoScalingGroupNames {
		filterSet[n] = true
	}

	groups, err := s.st.listGroups(ctx)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan groups", HTTPStatus: http.StatusInternalServerError}
	}

	xmlGroups := make([]asgXMLGroup, 0, len(groups))
	for _, g := range groups {
		if len(filterSet) > 0 && !filterSet[g.AutoScalingGroupName] {
			continue
		}
		instances, _ := s.st.listInstances(ctx, g.AutoScalingGroupName)
		xmlInstances := make([]asgXMLInstance, 0, len(instances))
		for _, inst := range instances {
			xmlInstances = append(xmlInstances, asgXMLInstance{
				InstanceId:              inst.InstanceId,
				InstanceType:            inst.InstanceType,
				AvailabilityZone:        inst.AvailabilityZone,
				LifecycleState:          inst.LifecycleState,
				HealthStatus:            inst.HealthStatus,
				LaunchConfigurationName: inst.LaunchConfigurationName,
				LaunchTemplate:          xmlLaunchTemplate(inst.LaunchTemplate),
				ProtectedFromScaleIn:    inst.ProtectedFromScaleIn,
			})
		}
		tags, _ := s.st.listTagsForGroup(ctx, g.AutoScalingGroupName)
		xmlTags := make([]asgXMLTag, 0, len(tags))
		for _, t := range tags {
			xmlTags = append(xmlTags, asgXMLTag(*t))
		}
		xmlGroups = append(xmlGroups, asgXMLGroup{
			AutoScalingGroupName:             g.AutoScalingGroupName,
			AutoScalingGroupARN:              g.AutoScalingGroupARN,
			LaunchConfigurationName:          g.LaunchConfigurationName,
			LaunchTemplate:                   xmlLaunchTemplate(g.LaunchTemplate),
			MinSize:                          g.MinSize,
			MaxSize:                          g.MaxSize,
			DesiredCapacity:                  g.DesiredCapacity,
			DefaultCooldown:                  g.DefaultCooldown,
			AvailabilityZones:                g.AvailabilityZones,
			HealthCheckType:                  g.HealthCheckType,
			HealthCheckGracePeriod:           g.HealthCheckGracePeriod,
			Instances:                        xmlInstances,
			CreatedTime:                      formatTime(g.CreatedTime),
			VPCZoneIdentifier:                g.VPCZoneIdentifier,
			Status:                           g.Status,
			Tags:                             xmlTags,
			TerminationPolicies:              g.TerminationPolicies,
			NewInstancesProtectedFromScaleIn: g.NewInstancesProtectedFromScaleIn,
		})
	}

	return &describeASGsResp{
		Xmlns:  asXMLNS,
		Result: asgGroupsResult{AutoScalingGroups: xmlGroups},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func (h *Handler) deleteASGTyped(ctx context.Context, req *deleteASGReq) (*deleteASGResp, *protocol.AWSError) {
	s := h.svc
	if req.AutoScalingGroupName == "" {
		return nil, asgValidationError("AutoScalingGroupName is required")
	}

	s.mu.Lock()
	g, found := s.st.getGroup(ctx, req.AutoScalingGroupName)
	if !found {
		s.mu.Unlock()
		return nil, groupNotFound(req.AutoScalingGroupName)
	}
	instances, _ := s.st.listInstances(ctx, req.AutoScalingGroupName)
	if len(instances) > 0 && !req.ForceDelete {
		s.mu.Unlock()
		return nil, &protocol.AWSError{
			Code:       "ResourceInUse",
			Message:    "You cannot delete an AutoScalingGroup while there are instances or pending Spot instance request(s) still in the group.",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	// Mark the group as deleting so a concurrent reconciler pass stops
	// launching into it while the instances are being torn down.
	g.Status = "Delete in progress"
	g.DesiredCapacity, g.MinSize, g.MaxSize = 0, 0, 0
	_ = s.st.putGroup(ctx, g)
	s.mu.Unlock()

	for _, inst := range instances {
		s.finishTermination(ctx, g, inst)
	}

	s.mu.Lock()
	s.st.deleteGroupChildren(ctx, req.AutoScalingGroupName)
	s.st.deleteGroup(ctx, req.AutoScalingGroupName)
	s.mu.Unlock()

	s.invalidateGroupCount()
	return &deleteASGResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) setDesiredCapacityTyped(ctx context.Context, req *setDesiredCapacityReq) (*setDesiredCapacityResp, *protocol.AWSError) {
	s := h.svc

	s.mu.Lock()
	g, found := s.st.getGroup(ctx, req.AutoScalingGroupName)
	if !found {
		s.mu.Unlock()
		return nil, groupNotFound(req.AutoScalingGroupName)
	}
	// SetDesiredCapacity does not move the group's bounds — unlike
	// UpdateAutoScalingGroup, a value outside them is an error, with AWS's own
	// wording.
	if req.DesiredCapacity > g.MaxSize {
		s.mu.Unlock()
		return nil, asgValidationError("New SetDesiredCapacity value %d is above max value %d for the AutoScalingGroup.", req.DesiredCapacity, g.MaxSize)
	}
	if req.DesiredCapacity < g.MinSize {
		s.mu.Unlock()
		return nil, asgValidationError("New SetDesiredCapacity value %d is below min value %d for the AutoScalingGroup.", req.DesiredCapacity, g.MinSize)
	}

	now := s.clk.Now().UTC()
	if req.HonorCooldown && !g.CooldownUntil.IsZero() && now.Before(g.CooldownUntil) {
		s.mu.Unlock()
		return nil, &protocol.AWSError{
			Code:       "ScalingActivityInProgress",
			Message:    fmt.Sprintf("Cannot set desired capacity for AutoScalingGroup %s: the group is in cooldown.", req.AutoScalingGroupName),
			HTTPStatus: http.StatusBadRequest,
		}
	}

	before := g.DesiredCapacity
	g.DesiredCapacity = req.DesiredCapacity
	if g.DefaultCooldown > 0 {
		g.CooldownUntil = now.Add(time.Duration(g.DefaultCooldown) * time.Second)
	}
	if err := s.st.putGroup(ctx, g); err != nil {
		s.mu.Unlock()
		return nil, asgInternalError("group")
	}
	if before != g.DesiredCapacity {
		s.recordActivity(ctx, g, &Activity{
			Description: fmt.Sprintf("Setting desired capacity to %d.", g.DesiredCapacity),
			Cause:       causeUserRequest(now, before, g.DesiredCapacity, g.MinSize, g.MaxSize),
			StartTime:   now, EndTime: now, StatusCode: "Successful", Progress: 100,
		})
	}
	s.mu.Unlock()

	s.poke()
	return &setDesiredCapacityResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) terminateInstanceTyped(ctx context.Context, req *terminateInstanceReq) (*terminateInstanceResp, *protocol.AWSError) {
	s := h.svc
	if req.InstanceId == "" {
		return nil, asgValidationError("InstanceId is required")
	}

	s.mu.Lock()
	inst, found := s.st.findInstance(ctx, req.InstanceId)
	if !found {
		s.mu.Unlock()
		return nil, asgValidationError("Instance Id not found - No managed instance found for instance ID %s", req.InstanceId)
	}
	g, ok := s.st.getGroup(ctx, inst.AutoScalingGroupName)
	if !ok {
		s.mu.Unlock()
		return nil, groupNotFound(inst.AutoScalingGroupName)
	}

	now := s.clk.Now().UTC()
	cause := fmt.Sprintf("At %s instance %s was taken out of service in response to a user request.", now.Format(awsTimeLayout), req.InstanceId)
	s.startTerminateActivity(ctx, g, inst, now, cause)
	activityID := inst.TerminateActivityId
	s.beginTermination(ctx, inst, now)
	if req.ShouldDecrementDesiredCapacity && g.DesiredCapacity > g.MinSize {
		g.DesiredCapacity--
		_ = s.st.putGroup(ctx, g)
	}
	s.mu.Unlock()

	s.poke()
	return &terminateInstanceResp{
		Xmlns: asXMLNS,
		Result: terminateInstanceResult{
			Activity: asgActivityXML{
				ActivityId:           activityID,
				AutoScalingGroupName: inst.AutoScalingGroupName,
				AutoScalingGroupARN:  g.AutoScalingGroupARN,
				Description:          "Terminating EC2 instance: " + req.InstanceId,
				Cause:                cause,
				StatusCode:           "InProgress",
				Progress:             50,
				StartTime:            now.Format(awsTimeLayout),
			},
		},
		Meta: asgMetaFromCtx(ctx),
	}, nil
}

// ── Launch configurations ──────────────────────────────────────────

func (h *Handler) createLaunchConfigTyped(ctx context.Context, req *createLaunchConfigReq) (*createLaunchConfigResp, *protocol.AWSError) {
	s := h.svc
	if req.LaunchConfigurationName == "" {
		return nil, asgValidationError("LaunchConfigurationName is required")
	}
	if req.ImageId == "" {
		return nil, asgValidationError("Valid requests must contain the ImageId parameter.")
	}

	instanceType := req.InstanceType
	if instanceType == "" {
		instanceType = "t3.micro"
	}

	lc := LaunchConfiguration{
		LaunchConfigurationName: req.LaunchConfigurationName,
		LaunchConfigurationARN:  s.lcARN(req.LaunchConfigurationName),
		ImageId:                 req.ImageId,
		InstanceType:            instanceType,
		KeyName:                 req.KeyName,
		SecurityGroups:          req.SecurityGroups,
		IamInstanceProfile:      req.IamInstanceProfile,
		UserData:                req.UserData,
		CreatedTime:             s.clk.Now(),
	}
	if err := putJSON(ctx, s.st.store, nsLaunchCfgs, req.LaunchConfigurationName, &lc); err != nil {
		return nil, asgInternalError("launch configuration")
	}
	return &createLaunchConfigResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) describeLaunchConfigsTyped(ctx context.Context, req *describeLaunchConfigsReq) (*describeLaunchConfigsResp, *protocol.AWSError) {
	s := h.svc
	filterSet := make(map[string]bool, len(req.LaunchConfigurationNames))
	for _, n := range req.LaunchConfigurationNames {
		filterSet[n] = true
	}

	pairs, err := s.st.store.Scan(ctx, nsLaunchCfgs, "")
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan launch configurations", HTTPStatus: http.StatusInternalServerError}
	}

	xmlLCs := make([]asgXMLLaunchConfig, 0, len(pairs))
	for _, kv := range pairs {
		if len(filterSet) > 0 && !filterSet[kv.Key] {
			continue
		}
		lc, ok := s.st.getLaunchConfig(ctx, kv.Key)
		if !ok {
			continue
		}
		xmlLCs = append(xmlLCs, asgXMLLaunchConfig{
			LaunchConfigurationName: lc.LaunchConfigurationName,
			LaunchConfigurationARN:  lc.LaunchConfigurationARN,
			ImageId:                 lc.ImageId,
			InstanceType:            lc.InstanceType,
			KeyName:                 lc.KeyName,
			SecurityGroups:          lc.SecurityGroups,
			IamInstanceProfile:      lc.IamInstanceProfile,
			UserData:                lc.UserData,
			CreatedTime:             formatTime(lc.CreatedTime),
		})
	}

	return &describeLaunchConfigsResp{
		Xmlns:  asXMLNS,
		Result: launchConfigsResult{LaunchConfigurations: xmlLCs},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func (h *Handler) deleteLaunchConfigTyped(ctx context.Context, req *deleteLaunchConfigReq) (*deleteLaunchConfigResp, *protocol.AWSError) {
	s := h.svc
	if req.LaunchConfigurationName == "" {
		return nil, asgValidationError("LaunchConfigurationName is required")
	}
	// AWS refuses to delete a launch configuration a group still points at.
	groups, _ := s.st.listGroups(ctx)
	for _, g := range groups {
		if g.LaunchConfigurationName == req.LaunchConfigurationName {
			return nil, &protocol.AWSError{
				Code:       "ResourceInUse",
				Message:    fmt.Sprintf("Cannot delete launch configuration %s because it is attached to AutoScalingGroup %s", req.LaunchConfigurationName, g.AutoScalingGroupName),
				HTTPStatus: http.StatusBadRequest,
			}
		}
	}
	_ = s.st.store.Delete(ctx, nsLaunchCfgs, req.LaunchConfigurationName)
	return &deleteLaunchConfigResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

// ── Scaling policies ───────────────────────────────────────────────

func (h *Handler) putScalingPolicyTyped(ctx context.Context, req *putScalingPolicyReq) (*putScalingPolicyResp, *protocol.AWSError) {
	s := h.svc
	if req.AutoScalingGroupName == "" || req.PolicyName == "" {
		return nil, asgValidationError("AutoScalingGroupName and PolicyName are required")
	}
	if _, found := s.st.getGroup(ctx, req.AutoScalingGroupName); !found {
		return nil, groupNotFound(req.AutoScalingGroupName)
	}
	if aerr := validatePolicyInput(req); aerr != nil {
		return nil, aerr
	}

	policyType := req.PolicyType
	if policyType == "" {
		policyType = policySimpleScaling
	}
	steps := make([]StepAdjustment, 0, len(req.StepAdjustments))
	for _, sa := range req.StepAdjustments {
		step := StepAdjustment{ScalingAdjustment: sa.ScalingAdjustment}
		if v, err := parseOptionalFloat(sa.MetricIntervalLowerBound); err == nil && v != nil {
			step.MetricIntervalLowerBound = v
		}
		if v, err := parseOptionalFloat(sa.MetricIntervalUpperBound); err == nil && v != nil {
			step.MetricIntervalUpperBound = v
		}
		steps = append(steps, step)
	}

	arn := s.policyARN(req.AutoScalingGroupName, req.PolicyName)
	if existing, found := s.st.getPolicy(ctx, req.AutoScalingGroupName, req.PolicyName); found {
		// PutScalingPolicy is an upsert; the ARN is stable across updates.
		arn = existing.PolicyARN
	}
	policy := ScalingPolicy{
		PolicyARN:               arn,
		PolicyName:              req.PolicyName,
		AutoScalingGroupName:    req.AutoScalingGroupName,
		PolicyType:              policyType,
		AdjustmentType:          req.AdjustmentType,
		ScalingAdjustment:       req.ScalingAdjustment,
		MinAdjustmentMagnitude:  req.MinAdjustmentMagnitude,
		Cooldown:                req.Cooldown,
		StepAdjustments:         steps,
		MetricAggregationType:   req.MetricAggregationType,
		EstimatedInstanceWarmup: req.EstimatedInstanceWarmup,
	}
	if err := s.st.putPolicy(ctx, &policy); err != nil {
		return nil, asgInternalError("scaling policy")
	}

	return &putScalingPolicyResp{
		Xmlns:  asXMLNS,
		Result: putScalingPolicyResult{PolicyARN: arn},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func parseOptionalFloat(s string) (*float64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func formatOptionalFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func (h *Handler) describePoliciesTyped(ctx context.Context, req *describePoliciesReq) (*describePoliciesResp, *protocol.AWSError) {
	s := h.svc
	policies, err := s.st.listPolicies(ctx, req.AutoScalingGroupName)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan policies", HTTPStatus: http.StatusInternalServerError}
	}

	xmlPolicies := make([]asgXMLPolicy, 0, len(policies))
	for _, p := range policies {
		steps := make([]asgXMLStepAdjustment, 0, len(p.StepAdjustments))
		for _, step := range p.StepAdjustments {
			steps = append(steps, asgXMLStepAdjustment{
				MetricIntervalLowerBound: formatOptionalFloat(step.MetricIntervalLowerBound),
				MetricIntervalUpperBound: formatOptionalFloat(step.MetricIntervalUpperBound),
				ScalingAdjustment:        step.ScalingAdjustment,
			})
		}
		xmlPolicies = append(xmlPolicies, asgXMLPolicy{
			PolicyARN:               p.PolicyARN,
			PolicyName:              p.PolicyName,
			AutoScalingGroupName:    p.AutoScalingGroupName,
			PolicyType:              p.PolicyType,
			AdjustmentType:          p.AdjustmentType,
			ScalingAdjustment:       p.ScalingAdjustment,
			MinAdjustmentMagnitude:  p.MinAdjustmentMagnitude,
			Cooldown:                p.Cooldown,
			StepAdjustments:         steps,
			MetricAggregationType:   p.MetricAggregationType,
			EstimatedInstanceWarmup: p.EstimatedInstanceWarmup,
		})
	}

	return &describePoliciesResp{
		Xmlns:  asXMLNS,
		Result: policiesResult{ScalingPolicies: xmlPolicies},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func (h *Handler) deletePolicyTyped(ctx context.Context, req *deletePolicyReq) (*deletePolicyResp, *protocol.AWSError) {
	s := h.svc
	group, policyName := req.AutoScalingGroupName, req.PolicyName
	if g, p, ok := parsePolicyARN(req.PolicyName); ok {
		group, policyName = g, p
	}
	if group != "" {
		_ = s.st.store.Delete(ctx, nsPolicies, policyKey(group, policyName))
		return &deletePolicyResp{asgEmptyBody: h.emptyBody(ctx)}, nil
	}
	policies, _ := s.st.listPolicies(ctx, "")
	for _, p := range policies {
		if p.PolicyName == policyName || p.PolicyARN == req.PolicyName {
			_ = s.st.store.Delete(ctx, nsPolicies, policyKey(p.AutoScalingGroupName, p.PolicyName))
			break
		}
	}
	return &deletePolicyResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) executePolicyTyped(ctx context.Context, req *executePolicyReq) (*executePolicyResp, *protocol.AWSError) {
	s := h.svc
	group, policyName := req.AutoScalingGroupName, req.PolicyName
	if g, p, ok := parsePolicyARN(req.PolicyName); ok {
		group, policyName = g, p
	}
	if group == "" || policyName == "" {
		return nil, asgValidationError("AutoScalingGroupName and PolicyName are required")
	}
	policy, found := s.st.getPolicy(ctx, group, policyName)
	if !found {
		return nil, asgValidationError("Scaling policy name not found - Policy %s not found for group %s", policyName, group)
	}
	// AWS measures a step-scaling breach relative to the alarm threshold; when
	// a caller drives ExecutePolicy by hand it supplies both numbers.
	breach := req.MetricValue - req.BreachThreshold
	if aerr := s.executePolicy(ctx, policy, breach, "", req.HonorCooldown); aerr != nil {
		return nil, aerr
	}
	return &executePolicyResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

// ── Lifecycle hooks ────────────────────────────────────────────────

func (h *Handler) putLifecycleHookTyped(ctx context.Context, req *putLifecycleHookReq) (*putLifecycleHookResp, *protocol.AWSError) {
	s := h.svc
	if req.AutoScalingGroupName == "" || req.LifecycleHookName == "" {
		return nil, asgValidationError("AutoScalingGroupName and LifecycleHookName are required")
	}
	if _, found := s.st.getGroup(ctx, req.AutoScalingGroupName); !found {
		return nil, groupNotFound(req.AutoScalingGroupName)
	}

	transition := req.LifecycleTransition
	existing, hadHook := s.st.hookByName(ctx, req.AutoScalingGroupName, req.LifecycleHookName)
	if transition == "" && hadHook {
		transition = existing.LifecycleTransition
	}
	if transition != transitionLaunching && transition != transitionTerminating {
		return nil, asgValidationError("Value '%s' at 'lifecycleTransition' failed to satisfy constraint: Member must satisfy enum value set: [autoscaling:EC2_INSTANCE_LAUNCHING, autoscaling:EC2_INSTANCE_TERMINATING]", req.LifecycleTransition)
	}

	result := req.DefaultResult
	if result == "" {
		result = lifecycleResultAbandon
	}
	if result != lifecycleResultContinue && result != lifecycleResultAbandon {
		return nil, asgValidationError("Value '%s' at 'defaultResult' failed to satisfy constraint: Member must satisfy enum value set: [CONTINUE, ABANDON]", req.DefaultResult)
	}
	timeout := req.HeartbeatTimeout
	if timeout == 0 {
		timeout = defaultHeartbeatTimeout
	}

	hook := LifecycleHook{
		LifecycleHookName:     req.LifecycleHookName,
		AutoScalingGroupName:  req.AutoScalingGroupName,
		LifecycleTransition:   transition,
		DefaultResult:         result,
		HeartbeatTimeout:      timeout,
		GlobalTimeout:         timeout * 100,
		NotificationTargetARN: req.NotificationTargetARN,
		RoleARN:               req.RoleARN,
	}
	if err := putJSON(ctx, s.st.store, nsHooks, req.AutoScalingGroupName+"/"+req.LifecycleHookName, &hook); err != nil {
		return nil, asgInternalError("lifecycle hook")
	}
	return &putLifecycleHookResp{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}, nil
}

func (h *Handler) describeLifecycleHooksTyped(ctx context.Context, req *describeLifecycleHooksReq) (*describeLifecycleHooksResp, *protocol.AWSError) {
	s := h.svc
	filterSet := make(map[string]bool, len(req.LifecycleHookNames))
	for _, n := range req.LifecycleHookNames {
		filterSet[n] = true
	}
	hooks, err := s.st.listHooks(ctx, req.AutoScalingGroupName)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan lifecycle hooks", HTTPStatus: http.StatusInternalServerError}
	}
	xmlHooks := make([]asgXMLHook, 0, len(hooks))
	for _, hk := range hooks {
		if len(filterSet) > 0 && !filterSet[hk.LifecycleHookName] {
			continue
		}
		xmlHooks = append(xmlHooks, asgXMLHook(*hk))
	}
	return &describeLifecycleHooksResp{
		Xmlns:  asXMLNS,
		Result: lifecycleHooksResult{LifecycleHooks: xmlHooks},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func (h *Handler) deleteLifecycleHookTyped(ctx context.Context, req *deleteLifecycleHookReq) (*deleteLifecycleHookResp, *protocol.AWSError) {
	s := h.svc
	_ = s.st.store.Delete(ctx, nsHooks, req.AutoScalingGroupName+"/"+req.LifecycleHookName)
	// Any instance parked on that hook is released, rather than left waiting
	// for a heartbeat that can no longer arrive. AWS completes an outstanding
	// action with ABANDON for a launching instance and CONTINUE for a
	// terminating one — not with the hook's own DefaultResult — so a deleted
	// hook never strands an instance mid-transition.
	s.mu.Lock()
	instances, _ := s.st.listInstances(ctx, req.AutoScalingGroupName)
	for _, inst := range instances {
		if inst.PendingHookName != req.LifecycleHookName || !inst.waiting() {
			continue
		}
		if inst.LifecycleState == lifecyclePendingWait {
			inst.HookDefaultResult = lifecycleResultAbandon
		} else {
			inst.HookDefaultResult = lifecycleResultContinue
		}
		inst.HookDeadline = s.clk.Now().UTC()
		_ = s.st.putInstance(ctx, inst)
	}
	s.mu.Unlock()
	s.poke()
	return &deleteLifecycleHookResp{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}, nil
}

func (h *Handler) completeLifecycleActionTyped(ctx context.Context, req *completeLifecycleActionReq) (*completeLifecycleActionResp, *protocol.AWSError) {
	s := h.svc
	if req.AutoScalingGroupName == "" || req.LifecycleHookName == "" {
		return nil, asgValidationError("AutoScalingGroupName and LifecycleHookName are required")
	}
	if req.LifecycleActionResult != lifecycleResultContinue && req.LifecycleActionResult != lifecycleResultAbandon {
		return nil, asgValidationError("Value '%s' at 'lifecycleActionResult' failed to satisfy constraint: Member must satisfy enum value set: [CONTINUE, ABANDON]", req.LifecycleActionResult)
	}

	s.mu.Lock()
	inst, aerr := s.pendingActionInstance(ctx, req.AutoScalingGroupName, req.LifecycleHookName, req.InstanceId, req.LifecycleActionToken)
	if aerr != nil {
		s.mu.Unlock()
		return nil, aerr
	}
	// Completing a lifecycle action is exactly "the heartbeat window is over
	// now, with this result" — the reconciler already knows how to act on
	// that, so there is one code path for both.
	inst.HookDefaultResult = req.LifecycleActionResult
	inst.HookDeadline = s.clk.Now().UTC()
	_ = s.st.putInstance(ctx, inst)
	s.mu.Unlock()

	s.poke()
	return &completeLifecycleActionResp{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}, nil
}

func (h *Handler) recordHeartbeatTyped(ctx context.Context, req *recordHeartbeatReq) (*recordHeartbeatResp, *protocol.AWSError) {
	s := h.svc
	s.mu.Lock()
	inst, aerr := s.pendingActionInstance(ctx, req.AutoScalingGroupName, req.LifecycleHookName, req.InstanceId, req.LifecycleActionToken)
	if aerr != nil {
		s.mu.Unlock()
		return nil, aerr
	}
	hook, found := s.st.hookByName(ctx, req.AutoScalingGroupName, req.LifecycleHookName)
	timeout := defaultHeartbeatTimeout
	if found && hook.HeartbeatTimeout > 0 {
		timeout = hook.HeartbeatTimeout
	}
	inst.HookDeadline = s.clk.Now().UTC().Add(time.Duration(timeout) * time.Second)
	_ = s.st.putInstance(ctx, inst)
	s.mu.Unlock()
	return &recordHeartbeatResp{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}, nil
}

// pendingActionInstance resolves the instance a lifecycle-action call names,
// by instance id or by the token the lifecycle event carried. Callers hold
// s.mu.
func (s *Service) pendingActionInstance(ctx context.Context, group, hookName, instanceID, token string) (*ASGInstance, *protocol.AWSError) {
	instances, err := s.st.listInstances(ctx, group)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan instances", HTTPStatus: http.StatusInternalServerError}
	}
	for _, inst := range instances {
		if !inst.waiting() || inst.PendingHookName != hookName {
			continue
		}
		if instanceID != "" && inst.InstanceId != instanceID {
			continue
		}
		if token != "" && inst.LifecycleActionToken != token {
			continue
		}
		return inst, nil
	}
	return nil, asgValidationError("No active Lifecycle Action found with instance ID %s", instanceID)
}

// ── Instances ──────────────────────────────────────────────────────

func (h *Handler) describeInstancesTyped(ctx context.Context, req *describeInstancesReq) (*describeInstancesResp, *protocol.AWSError) {
	s := h.svc
	filterSet := make(map[string]bool, len(req.InstanceIds))
	for _, id := range req.InstanceIds {
		filterSet[id] = true
	}
	instances, err := s.st.listInstances(ctx, "")
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan instances", HTTPStatus: http.StatusInternalServerError}
	}
	out := make([]asgXMLGroupInstance, 0, len(instances))
	for _, inst := range instances {
		if len(filterSet) > 0 && !filterSet[inst.InstanceId] {
			continue
		}
		out = append(out, asgXMLGroupInstance{
			InstanceId:              inst.InstanceId,
			InstanceType:            inst.InstanceType,
			AutoScalingGroupName:    inst.AutoScalingGroupName,
			AvailabilityZone:        inst.AvailabilityZone,
			LifecycleState:          inst.LifecycleState,
			HealthStatus:            inst.HealthStatus,
			LaunchConfigurationName: inst.LaunchConfigurationName,
			LaunchTemplate:          xmlLaunchTemplate(inst.LaunchTemplate),
			ProtectedFromScaleIn:    inst.ProtectedFromScaleIn,
		})
	}
	return &describeInstancesResp{
		Xmlns:  asXMLNS,
		Result: asgInstanceResult{AutoScalingInstances: out},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

func (h *Handler) setInstanceHealthTyped(ctx context.Context, req *setInstanceHealthReq) (*setInstanceHealthResp, *protocol.AWSError) {
	s := h.svc
	if req.HealthStatus != healthStatusHealthy && req.HealthStatus != healthStatusUnhealthy {
		return nil, asgValidationError("Value '%s' at 'healthStatus' failed to satisfy constraint: Member must satisfy enum value set: [Healthy, Unhealthy]", req.HealthStatus)
	}
	s.mu.Lock()
	inst, found := s.st.findInstance(ctx, req.InstanceId)
	if !found {
		s.mu.Unlock()
		return nil, asgValidationError("Instance Id not found - No managed instance found for instance ID %s", req.InstanceId)
	}
	inst.HealthStatus = req.HealthStatus
	_ = s.st.putInstance(ctx, inst)
	s.mu.Unlock()
	s.poke()
	return &setInstanceHealthResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) setInstanceProtectionTyped(ctx context.Context, req *setInstanceProtectionReq) (*setInstanceProtectionResp, *protocol.AWSError) {
	s := h.svc
	if req.AutoScalingGroupName == "" {
		return nil, asgValidationError("AutoScalingGroupName is required")
	}
	if len(req.InstanceIds) == 0 {
		return nil, asgValidationError("InstanceIds is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The instance must belong to the named group: AWS scopes this call to one
	// group, so an id from a different group is not found rather than silently
	// protected.
	for _, id := range req.InstanceIds {
		inst, ok := s.st.getInstance(ctx, req.AutoScalingGroupName, id)
		if !ok {
			return nil, asgValidationError("Instance Id not found - No managed instance found for instance ID %s", id)
		}
		inst.ProtectedFromScaleIn = req.ProtectedFromScaleIn
		_ = s.st.putInstance(ctx, inst)
	}
	return &setInstanceProtectionResp{Xmlns: asXMLNS, Meta: asgMetaFromCtx(ctx)}, nil
}

// ── Scaling activities ─────────────────────────────────────────────

func (h *Handler) describeActivitiesTyped(ctx context.Context, req *describeActivitiesReq) (*describeActivitiesResp, *protocol.AWSError) {
	s := h.svc
	filterSet := make(map[string]bool, len(req.ActivityIds))
	for _, id := range req.ActivityIds {
		filterSet[id] = true
	}
	activities, err := s.st.listActivities(ctx, req.AutoScalingGroupName)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan scaling activities", HTTPStatus: http.StatusInternalServerError}
	}
	limit := req.MaxRecords
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	out := make([]asgActivityXML, 0, len(activities))
	for _, a := range activities {
		if len(filterSet) > 0 && !filterSet[a.ActivityId] {
			continue
		}
		out = append(out, asgActivityXML{
			ActivityId:           a.ActivityId,
			AutoScalingGroupName: a.AutoScalingGroupName,
			AutoScalingGroupARN:  a.AutoScalingGroupARN,
			Description:          a.Description,
			Cause:                a.Cause,
			StartTime:            formatTime(a.StartTime),
			EndTime:              formatTime(a.EndTime),
			StatusCode:           a.StatusCode,
			StatusMessage:        a.StatusMessage,
			Progress:             a.Progress,
			Details:              a.Details,
		})
		if len(out) >= limit {
			break
		}
	}
	return &describeActivitiesResp{
		Xmlns:  asXMLNS,
		Result: activitiesResult{Activities: out},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

// ── Tags ───────────────────────────────────────────────────────────

func (h *Handler) createOrUpdateTagsTyped(ctx context.Context, req *createOrUpdateTagsReq) (*createOrUpdateTagsResp, *protocol.AWSError) {
	s := h.svc
	for _, tm := range req.Tags {
		if tm.ResourceId == "" {
			continue
		}
		tag := GroupTag{
			ResourceId:        tm.ResourceId,
			ResourceType:      tm.ResourceType,
			Key:               tm.Key,
			Value:             tm.Value,
			PropagateAtLaunch: tm.PropagateAtLaunch == "true",
		}
		if err := putJSON(ctx, s.st.store, nsGroupTags, tm.ResourceId+"/"+tm.Key, &tag); err != nil {
			return nil, asgInternalError("tag")
		}
	}
	return &createOrUpdateTagsResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

func (h *Handler) deleteTagsTyped(ctx context.Context, req *deleteTagsReq) (*deleteTagsResp, *protocol.AWSError) {
	s := h.svc
	for _, tm := range req.Tags {
		if tm.ResourceId == "" {
			continue
		}
		_ = s.st.store.Delete(ctx, nsGroupTags, tm.ResourceId+"/"+tm.Key)
	}
	return &deleteTagsResp{asgEmptyBody: h.emptyBody(ctx)}, nil
}

// tagFilterValues maps each DescribeTags filter this handler implements to the
// value it reads off a tag. These four are the whole of what AWS documents for
// the operation, so the set is complete rather than a subset.
//
// A name outside it is refused, not ignored. Ignoring one answered with every
// tag in the account presented as a filtered result — the divergence #1032
// fixed for EC2, and the reason `key`, `value` and `propagate-at-launch` looked
// like they worked.
var tagFilterValues = map[string]func(GroupTag) string{
	"auto-scaling-group":  func(t GroupTag) string { return t.ResourceId },
	"key":                 func(t GroupTag) string { return t.Key },
	"propagate-at-launch": func(t GroupTag) string { return strconv.FormatBool(t.PropagateAtLaunch) },
	"value":               func(t GroupTag) string { return t.Value },
}

func (h *Handler) describeTagsTyped(ctx context.Context, req *describeTagsReq) (*describeTagsResp, *protocol.AWSError) {
	s := h.svc
	for _, f := range req.Filters {
		if _, ok := tagFilterValues[f.Name]; !ok {
			return nil, asgValidationError(
				"Value (%s) for parameter Filters is invalid. Valid filter names are: %s",
				f.Name, strings.Join(slices.Sorted(maps.Keys(tagFilterValues)), ", "))
		}
	}

	// One group is the common shape and the only one the tag key's prefix can
	// answer; several fall back to a full scan and are matched below like every
	// other filter. Reading only Values[0] used to be the whole of the filter,
	// so a caller asking about three groups was answered about the first.
	scanPrefix := ""
	for _, f := range req.Filters {
		if f.Name == "auto-scaling-group" && len(f.Values) == 1 {
			scanPrefix = f.Values[0]
		}
	}
	tags, err := s.st.listTagsForGroup(ctx, scanPrefix)
	if err != nil {
		return nil, &protocol.AWSError{Code: "InternalFailure", Message: "failed to scan tags", HTTPStatus: http.StatusInternalServerError}
	}

	xmlTags := make([]asgXMLTag, 0, len(tags))
	for _, t := range tags {
		if tagMatchesFilters(*t, req.Filters) {
			xmlTags = append(xmlTags, asgXMLTag(*t))
		}
	}
	return &describeTagsResp{
		Xmlns:  asXMLNS,
		Result: tagsResult{Tags: xmlTags},
		Meta:   asgMetaFromCtx(ctx),
	}, nil
}

// tagMatchesFilters reports whether a tag satisfies every filter. Filters AND
// together and the values within one OR together, which is AWS's rule.
func tagMatchesFilters(tag GroupTag, filters []asgFilterMember) bool {
	for _, f := range filters {
		read, ok := tagFilterValues[f.Name]
		if !ok {
			continue // refused before the scan
		}
		if !slices.Contains(f.Values, read(tag)) {
			return false
		}
	}
	return true
}
