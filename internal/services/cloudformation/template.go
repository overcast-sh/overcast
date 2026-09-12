package cloudformation

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseTemplate parses a JSON or YAML CloudFormation template.
// Both formats are fully supported. YAML short-form intrinsic-function tags
// (e.g. !Ref, !Sub, !GetAtt, !Join) are normalised to their canonical long-form
// map equivalents so the rest of the resolution code works identically for
// both input formats.
func parseTemplate(body string) (*Template, error) {
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "{") {
		return parseTemplateJSON(body)
	}
	return parseTemplateYAML(body)
}

func parseTemplateJSON(body string) (*Template, error) {
	var tmpl Template
	if err := json.Unmarshal([]byte(body), &tmpl); err != nil {
		return nil, fmt.Errorf("cfn: parse template (json): %w", err)
	}
	if tmpl.Resources == nil {
		return nil, fmt.Errorf("cfn: template has no Resources section")
	}
	return &tmpl, nil
}

func parseTemplateYAML(body string) (*Template, error) {
	// Decode into a yaml.Node tree so we can handle CloudFormation-specific
	// tag aliases (e.g. !Ref, !Sub, !GetAtt) before unmarshalling into Template.
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(body), &root); err != nil {
		return nil, fmt.Errorf("cfn: parse template (yaml): %w", err)
	}
	if root.Kind == 0 {
		return nil, fmt.Errorf("cfn: empty YAML template")
	}

	// Convert the node tree to a plain Go value, translating CFN tags along the way.
	raw := yamlNodeToValue(&root)

	// Round-trip through JSON to populate the typed Template struct, reusing the
	// existing JSON struct tags.
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("cfn: parse template (yaml→json): %w", err)
	}
	var tmpl Template
	if err := json.Unmarshal(jsonBytes, &tmpl); err != nil {
		return nil, fmt.Errorf("cfn: parse template (yaml→json unmarshal): %w", err)
	}
	if tmpl.Resources == nil {
		return nil, fmt.Errorf("cfn: template has no Resources section")
	}
	return &tmpl, nil
}

// yamlNodeToValue converts a *yaml.Node tree to a plain Go value
// (map[string]any, []any, string, bool, float64, nil).
//
// CloudFormation short-form tag aliases are expanded to their canonical
// long-form map equivalents:
//
//	!Ref Foo            → {"Ref": "Foo"}
//	!Sub "x-${Foo}"    → {"Fn::Sub": "x-${Foo}"}
//	!GetAtt Res.Attr   → {"Fn::GetAtt": ["Res", "Attr"]}
//	!Join [-, [a, b]]  → {"Fn::Join": ["-", ["a","b"]]}
//	!Select [0, [a]]   → {"Fn::Select": [0, ["a"]]}
//	!Split [,, str]    → {"Fn::Split": [",", "str"]}
//	!If [c, a, b]      → {"Fn::If": ["c","a","b"]}
//	!Base64 v          → {"Fn::Base64": "v"}
//	!GetAZs ""         → {"Fn::GetAZs": ""}
//	!ImportValue v     → {"Fn::ImportValue": "v"}
//	!GetStackOutput {…} → {"Fn::GetStackOutput": {…}}
//	!Condition c       → {"Condition": "c"}
func yamlNodeToValue(n *yaml.Node) any {
	if n == nil {
		return nil
	}

	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return yamlNodeToValue(n.Content[0])

	case yaml.MappingNode:
		m := make(map[string]any, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, _ := yamlNodeToValue(n.Content[i]).(string)
			m[key] = yamlNodeToValue(n.Content[i+1])
		}
		// The one intrinsic whose short form tags a mapping:
		//   Value: !GetStackOutput
		//     StackName: Producer
		//     OutputName: VpcId
		if n.Tag == "!GetStackOutput" {
			return map[string]any{"Fn::GetStackOutput": m}
		}
		return m

	case yaml.SequenceNode:
		// Sequence nodes can carry a CFN tag when written as:
		//   Key: !Join
		//     - "-"
		//     - [a, b]
		s := make([]any, len(n.Content))
		for i, child := range n.Content {
			s[i] = yamlNodeToValue(child)
		}
		switch n.Tag {
		case "!Join":
			return map[string]any{"Fn::Join": s}
		case "!Select":
			return map[string]any{"Fn::Select": s}
		case "!Split":
			return map[string]any{"Fn::Split": s}
		case "!FindInMap":
			return map[string]any{"Fn::FindInMap": s}
		case "!Cidr":
			return map[string]any{"Fn::Cidr": s}
		case "!If":
			return map[string]any{"Fn::If": s}
		case "!Sub":
			// !Sub [template, vars] — array form
			return map[string]any{"Fn::Sub": s}
		case "!GetAtt":
			// !GetAtt [Logical, Attr] — array form
			return map[string]any{"Fn::GetAtt": s}
		}
		return s

	case yaml.ScalarNode:
		tag := n.Tag
		val := n.Value
		switch tag {
		case "!Ref":
			return map[string]any{"Ref": val}
		case "!Sub":
			return map[string]any{"Fn::Sub": val}
		case "!Base64":
			return map[string]any{"Fn::Base64": val}
		case "!GetAZs":
			return map[string]any{"Fn::GetAZs": val}
		case "!ImportValue":
			return map[string]any{"Fn::ImportValue": val}
		case "!Condition":
			return map[string]any{"Condition": val}
		case "!GetAtt":
			// Scalar form: "LogicalId.Attribute"
			parts := strings.SplitN(val, ".", 2)
			if len(parts) == 2 {
				return map[string]any{"Fn::GetAtt": []any{parts[0], parts[1]}}
			}
			return map[string]any{"Fn::GetAtt": val}
		default:
			// Decode to native Go type (bool, int, float64, string, nil).
			var v any
			if err := n.Decode(&v); err != nil {
				return val
			}
			return v
		}

	case yaml.AliasNode:
		return yamlNodeToValue(n.Alias)
	}

	return nil
}

// resolveIntrinsics recursively resolves CloudFormation intrinsic functions in a value.
// Supported intrinsics:
//   - Ref (parameters, pseudo-parameters, logical resource IDs)
//   - Fn::Sub (simple variable substitution)
//   - Fn::Join
//   - Fn::Select
//   - Fn::GetAtt (returns PhysicalResourceId for now)
//   - Fn::If (evaluates conditions)
//   - Fn::Split
//   - Fn::GetAZs
//   - Fn::ImportValue (cross-stack references)
//   - Fn::GetStackOutput (weak cross-stack references, any region)
func resolveIntrinsics(v any, ctx *resolveContext) any {
	switch val := v.(type) {
	case map[string]any:
		return resolveMap(val, ctx)
	case []any:
		resolved := make([]any, len(val))
		for i, item := range val {
			resolved[i] = resolveIntrinsics(item, ctx)
		}
		return resolved
	default:
		return v
	}
}

func resolveMap(m map[string]any, ctx *resolveContext) any {
	// Check for intrinsic functions (single-key maps with known keys).
	if len(m) == 1 {
		if ref, ok := m["Ref"]; ok {
			return resolveRef(ref, ctx)
		}
		if sub, ok := m["Fn::Sub"]; ok {
			return resolveSub(sub, ctx)
		}
		if join, ok := m["Fn::Join"]; ok {
			return resolveJoin(join, ctx)
		}
		if sel, ok := m["Fn::Select"]; ok {
			return resolveSelect(sel, ctx)
		}
		if getAtt, ok := m["Fn::GetAtt"]; ok {
			return resolveGetAtt(getAtt, ctx)
		}
		if ifCond, ok := m["Fn::If"]; ok {
			return resolveIf(ifCond, ctx)
		}
		if split, ok := m["Fn::Split"]; ok {
			return resolveSplit(split, ctx)
		}
		if _, ok := m["Fn::GetAZs"]; ok {
			return resolveGetAZs(ctx)
		}
		if findInMap, ok := m["Fn::FindInMap"]; ok {
			return resolveFindInMap(findInMap, ctx)
		}
		if b64, ok := m["Fn::Base64"]; ok {
			return resolveBase64(b64, ctx)
		}
		if cidr, ok := m["Fn::Cidr"]; ok {
			return resolveCidr(cidr, ctx)
		}
		if impVal, ok := m["Fn::ImportValue"]; ok {
			return resolveImportValue(impVal, ctx)
		}
		if gso, ok := m["Fn::GetStackOutput"]; ok {
			return resolveGetStackOutput(gso, ctx)
		}
	}

	// Not an intrinsic — recurse into all values.
	result := make(map[string]any, len(m))
	for k, val := range m {
		result[k] = resolveIntrinsics(val, ctx)
	}
	return result
}

// resolveContext holds the resolution state.
type resolveContext struct {
	Region    string
	AccountID string
	StackName string
	StackID   string
	// LogicalID is the template logical ID of the resource currently being
	// provisioned. Set by provisionResource before dispatching to a handler,
	// so handlers can name a resource whose template does not name itself.
	// Empty outside provisioning (property resolution does not need it).
	LogicalID string
	// EmulationLimitation is what the services reported they would not do with
	// the resource just provisioned, for the caller to record as its
	// ResourceStatusReason. Set by provisionResource for each resource in turn
	// — same one-at-a-time reasoning as LogicalID — and empty for a resource
	// with nothing to declare. See limitation.go.
	EmulationLimitation string
	// ClientRequestToken is the token of the stack operation being resolved,
	// carried here so a nested stack records its events under the same token as
	// the parent that provisioned it. A nested stack is part of one operation,
	// and its events have to be findable from the same request as the rest.
	ClientRequestToken string
	StackTags          []Tag
	PreviousStackTags  []Tag
	Params             map[string]string            // parameter name → value
	ParamTypes         map[string]string            // parameter name → CloudFormation type
	Resources          map[string]string            // logical ID → physical ID
	Conditions         map[string]bool              // condition name → evaluated value
	Mappings           map[string]any               // raw mappings from template
	Attributes         map[string]map[string]string // logical ID → attributes
	Exports            map[string]string            // export name → value (cross-stack)

	// DynamicRef resolves a {{resolve:...}} dynamic reference against the
	// emulated services. Nil outside provisioning — a template resolved
	// without one records a failure per reference rather than silently
	// leaving the reference text in a property value. See dynamic_refs.go.
	DynamicRef func(ref dynamicRef) (string, error)
	// StackOutput resolves an Fn::GetStackOutput reference against the stacks
	// the emulator holds. Nil outside provisioning, with the same consequence
	// as DynamicRef: a failure is recorded per reference rather than the
	// property silently reading as empty. See stack_output_refs.go.
	StackOutput func(ref stackOutputRef) (string, error)
	// dynamicRefErr holds the first reference — dynamic, or an
	// Fn::GetStackOutput — that could not be resolved,
	// until the provisioner takes it and fails the resource.
	dynamicRefErr error
}

// Maximum physical name lengths, per service. CloudFormation truncates a
// generated name to fit the service that will hold it; exceeding these is a
// validation error from the service, not from CloudFormation.
const (
	maxNameLenLambda      = 64  // function names
	maxNameLenLambdaLayer = 140 // layer names, which Lambda limits separately from function names
	// IAM sets two limits rather than one: 64 for roles and users, 128 for
	// groups, policies, managed policies and instance profiles. One constant
	// used to carry 64 while its comment claimed it covered policies and
	// instance profiles too, so those were truncated to half what IAM takes.
	maxNameLenIAM       = 64  // role and user names
	maxNameLenIAMPolicy = 128 // group, policy, managed-policy and instance-profile names
	maxNameLenS3        = 63  // bucket names
	maxNameLenSQS       = 80  // queue names (the ".fifo" suffix counts against this)
	maxNameLenSNS       = 256 // topic names
	maxNameLenRDS       = 63  // DB instance and cluster identifiers
	maxNameLenCache     = 50  // ElastiCache cache cluster IDs (lowercase only)
	maxNameLenECR       = 256 // repository names (lowercase only)
	maxNameLenEvents    = 64  // EventBridge rule names
	maxNameLenScheduler = 64  // EventBridge Scheduler schedule and schedule-group names
	maxNameLenSFN       = 80  // Step Functions state-machine names

	maxNameLenELBv2       = 32  // ELBv2 load balancer and target group names
	maxNameLenCacheGroup  = 40  // ElastiCache replication group IDs and serverless cache names (lowercase only)
	maxNameLenBackup      = 50  // Backup vault and backup plan names
	maxNameLenFirehose    = 64  // Firehose delivery stream names
	maxNameLenMSK         = 64  // MSK cluster names
	maxNameLenPipes       = 64  // EventBridge Pipes pipe names
	maxNameLenSES         = 64  // SES email template names
	maxNameLenEKS         = 100 // EKS cluster names
	maxNameLenAthena      = 128 // Athena work group names
	maxNameLenCloudTrail  = 128 // CloudTrail trail names
	maxNameLenCognito     = 128 // Cognito user pool and user pool client names
	maxNameLenKinesis     = 128 // Kinesis data stream names
	maxNameLenShield      = 128 // Shield protection names
	maxNameLenWAFv2       = 128 // WAFv2 web ACL names
	maxNameLenAppRegistry = 256 // Service Catalog AppRegistry application names

	// The remaining handlers on maxNameLenDefault fall into two groups.
	//
	// Correct on the default, because the service's own limit is at least as
	// large: DynamoDB tables (1024), CloudWatch alarms (255), log groups and
	// log streams (512), SSM parameters (2048), Secrets Manager secrets (512),
	// EventBridge buses (256), Auto Scaling groups and launch configurations
	// (255), and EC2 security groups (255, stated in the model's own
	// documentation rather than a length trait).
	//
	// Unverified against real AWS, and so left alone: EKS node groups and
	// Fargate profiles, MSK configurations, the four API Gateway names (REST
	// API, authorizer, model, request validator) and ELBv2 listeners. The
	// pinned models carry no length trait for any of them, and the models are
	// the authority the caps above are drawn from — AWS's web documentation
	// puts EKS names at 63 and MSK configurations at 64, which would make
	// three more overruns, but a constant minted from a doc page nobody
	// checked is how the IAM comment came to claim 64 covered policies.
	maxNameLenDefault = 255
)

// physicalIDSuffixLen matches the length of the random component real
// CloudFormation appends to a generated physical ID.
const physicalIDSuffixLen = 12

// physicalIDSuffixAlphabet is CloudFormation's: uppercase letters and digits.
const physicalIDSuffixAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// generatedName returns the physical name for a resource whose template does
// not name it, shaped like CloudFormation's own: "{StackName}-{LogicalID}-{RANDOM}".
//
// Both trailing parts matter. The logical ID makes the name unique within the
// stack: built from the stack name alone, every unnamed resource of a type got
// the same name and the second collided with the first — and since CDK almost
// never names its resources, a stack with two Lambda functions could not deploy
// at all.
//
// The random component makes it unique across *instances* of the same logical
// resource, which is what lets a replacement's new resource exist alongside the
// one it replaces. Replacement creates before it deletes precisely so a failed
// update can roll back to the original, and that is impossible if both want the
// same name.
func (c *resolveContext) generatedName() string {
	return c.generatedNameWithin(maxNameLenDefault)
}

// generatedNameWithin is generatedName capped to a service's maximum name
// length. A stack name and a CDK logical ID are each long enough that the pair
// can exceed what a service accepts, and CloudFormation truncates to fit.
//
// The random suffix is always preserved — it is what guarantees uniqueness, so
// the head is what gets truncated.
func (c *resolveContext) generatedNameWithin(max int) string {
	if c == nil {
		return ""
	}
	// Trimmed before the join as well as after the truncation below: both are
	// ways the assembled name can end up with two consecutive hyphens in it.
	base := strings.TrimRight(c.StackName, "-")
	if c.LogicalID != "" {
		base = base + "-" + c.LogicalID
	}
	suffix := randomPhysicalIDSuffix()
	if max <= 0 {
		return base + "-" + suffix
	}
	// Room for the separator and suffix; if even that does not fit, the
	// suffix alone is the most useful thing to keep.
	keep := max - physicalIDSuffixLen - 1
	if keep < 1 {
		if max < physicalIDSuffixLen {
			return suffix[:max]
		}
		return suffix
	}
	if len(base) > keep {
		base = base[:keep]
		// Truncation can land on a separator, and "stack-" + "-" + suffix is a
		// name with two consecutive hyphens in it. CloudFormation does not mint
		// those, and several services reject them outright — RDS documents
		// "can't end with a hyphen or contain two consecutive hyphens" for both
		// DB identifiers, so the truncation, not the template, would be what
		// made the stack undeployable.
		base = strings.TrimRight(base, "-")
	}
	if base == "" {
		return suffix
	}
	return base + "-" + suffix
}

// generatedNameLowerWithin is generatedNameWithin lowercased, for the services
// whose names may not contain uppercase — ECR repositories being the one that
// bites, since both a CDK stack name and a CDK logical ID are mixed case and
// the random suffix is uppercase by construction.
func (c *resolveContext) generatedNameLowerWithin(max int) string {
	return strings.ToLower(c.generatedNameWithin(max))
}

// generatedNameELBv2 is generatedName shaped to what ELBv2 accepts for a load
// balancer or a target group name, which is the tightest rule in the
// provisioner: at most 32 characters, alphanumerics and hyphens only, no
// leading or trailing hyphen, and not beginning with "internal-" — the prefix
// ELBv2 reserves for the DNS name of an internal load balancer.
//
// 32 characters is short enough that a realistic CDK stack name plus a CDK
// logical ID always overruns it, so the truncation is the normal case rather
// than the edge one, and a truncation that lands on a hyphen would produce a
// name the API refuses. generatedNameWithin already caps the length and trims
// a trailing hyphen; this adds the character set and the two prefix rules.
//
// The two types share the length and the character set. Only the load
// balancer documents the "internal-" rule, but a target group is held to it as
// well rather than carrying a near-copy of this helper for one clause — no
// template asks for a generated target group name beginning with "internal-",
// so nothing is lost by not minting one.
func (c *resolveContext) generatedNameELBv2() string {
	var b strings.Builder
	for _, r := range c.generatedNameWithin(maxNameLenELBv2) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), "-")
	name = strings.TrimLeft(strings.TrimPrefix(name, "internal-"), "-")
	if name == "" {
		// Only reachable for a stack named entirely out of characters ELBv2
		// rejects; the suffix alone is still a usable, unique name.
		return randomPhysicalIDSuffix()
	}
	return name
}

// randomPhysicalIDSuffix returns CloudFormation's random physical-ID component.
func randomPhysicalIDSuffix() string {
	buf := make([]byte, physicalIDSuffixLen)
	// crypto/rand.Read does not fail on any platform Go supports.
	_, _ = rand.Read(buf)
	out := make([]byte, physicalIDSuffixLen)
	for i, b := range buf {
		out[i] = physicalIDSuffixAlphabet[int(b)%len(physicalIDSuffixAlphabet)]
	}
	return string(out)
}

// resolveRef resolves Ref to a parameter value, pseudo-parameter, or logical resource ID.
func resolveRef(ref any, ctx *resolveContext) any {
	name, ok := ref.(string)
	if !ok {
		return ref
	}

	// Pseudo-parameters
	switch name {
	case "AWS::Region":
		return ctx.Region
	case "AWS::AccountId":
		return ctx.AccountID
	case "AWS::StackName":
		return ctx.StackName
	case "AWS::StackId":
		return ctx.StackID
	case "AWS::NoValue":
		return ""
	case "AWS::URLSuffix":
		return "amazonaws.com"
	case "AWS::Partition":
		return "aws"
	case "AWS::NotificationARNs":
		return []any{}
	}

	// Template parameters
	if v, ok := ctx.Params[name]; ok {
		return resolveParameterValue(v, ctx.ParamTypes[name])
	}

	// Logical resource → Ref value.
	// Handlers may set a "Ref" attribute override when the physical ID
	// (needed for Delete) differs from what Ref should return (e.g. API
	// Gateway resources where the physical ID is "restApiId/resourceId"
	// but Ref should return just the resource ID).
	if attrMap, ok := ctx.Attributes[name]; ok {
		if refOverride, ok := attrMap["Ref"]; ok {
			return refOverride
		}
	}
	if v, ok := ctx.Resources[name]; ok {
		return v
	}

	return name
}

// resolveParameterValue gives a parameter's stored string the shape its
// declared type promises: a CommaDelimitedList or List<...> parameter resolves
// to a list, as AWS's Ref does, and an empty value to the empty list. CDK's
// bootstrap template depends on this — its CloudFormationExecutionPolicies
// parameter is a CommaDelimitedList that feeds ManagedPolicyArns directly.
func resolveParameterValue(value, parameterType string) any {
	if parameterType != "CommaDelimitedList" && !strings.HasPrefix(parameterType, "List<") {
		return value
	}
	if strings.TrimSpace(value) == "" {
		return []any{}
	}
	items := strings.Split(value, ",")
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, strings.TrimSpace(item))
	}
	return out
}

// resolveSub handles Fn::Sub — either a string or [string, map].
func resolveSub(sub any, ctx *resolveContext) any {
	var tmplStr string
	extraVars := map[string]string{}

	switch val := sub.(type) {
	case string:
		tmplStr = val
	case []any:
		if len(val) >= 1 {
			if s, ok := val[0].(string); ok {
				tmplStr = s
			}
		}
		if len(val) >= 2 {
			if m, ok := val[1].(map[string]any); ok {
				for k, v := range m {
					resolved := resolveIntrinsics(v, ctx)
					extraVars[k] = fmt.Sprintf("%v", resolved)
				}
			}
		}
	default:
		return sub
	}

	// Replace ${VarName} patterns.
	result := tmplStr
	// First apply extra vars, then try params/resources/pseudo-params.
	for k, v := range extraVars {
		result = strings.ReplaceAll(result, "${"+k+"}", v)
	}

	// Replace remaining ${...} with known values.
	for {
		start := strings.Index(result, "${")
		if start < 0 {
			break
		}
		end := strings.Index(result[start:], "}")
		if end < 0 {
			break
		}
		varName := result[start+2 : start+end]
		resolved := resolveRef(varName, ctx)
		if strings.Contains(varName, ".") {
			resolved = resolveGetAtt(varName, ctx)
		}
		result = result[:start] + fmt.Sprintf("%v", resolved) + result[start+end+1:]
	}

	return result
}

// resolveJoin handles Fn::Join: [delimiter, [values]].
func resolveJoin(join any, ctx *resolveContext) any {
	arr, ok := join.([]any)
	if !ok || len(arr) != 2 {
		return join
	}
	delimiter, _ := arr[0].(string)
	// The value list may itself be an intrinsic that yields a list — Fn::Split,
	// Fn::GetAZs, Fn::Cidr, Fn::If. Resolving it first is what makes those
	// compose; asserting on the raw argument instead left the unresolved map to
	// be formatted into the output by %v. resolveIntrinsics recurses into a
	// list, so its elements come back resolved too.
	values, ok := resolveIntrinsics(arr[1], ctx).([]any)
	if !ok {
		return join
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%v", v))
	}
	return strings.Join(parts, delimiter)
}

// resolveSelect handles Fn::Select: [index, [values]].
func resolveSelect(sel any, ctx *resolveContext) any {
	arr, ok := sel.([]any)
	if !ok || len(arr) != 2 {
		return sel
	}
	idx := 0
	// The index is usually a literal but may be a Ref to a parameter.
	switch v := resolveIntrinsics(arr[0], ctx).(type) {
	case float64:
		idx = int(v)
	case int:
		idx = v
	case string:
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return ""
		}
		idx = parsed
	}
	// As in resolveJoin, the list may be an intrinsic — Fn::Split above all,
	// which is how CDK builds a nested stack's asset key.
	values, ok := resolveIntrinsics(arr[1], ctx).([]any)
	if !ok || idx < 0 || idx >= len(values) {
		return ""
	}
	return values[idx]
}

// resolveGetAtt handles Fn::GetAtt: [logicalId, attribute] or "logicalId.attribute".
func resolveGetAtt(getAtt any, ctx *resolveContext) any {
	var logicalID, attr string
	switch val := getAtt.(type) {
	case []any:
		if len(val) >= 2 {
			logicalID, _ = val[0].(string)
			attr, _ = val[1].(string)
		}
	case string:
		parts := strings.SplitN(val, ".", 2)
		if len(parts) == 2 {
			logicalID = parts[0]
			attr = parts[1]
		}
	}
	if logicalID == "" {
		return getAtt
	}

	// Look up real attribute value first.
	if attrMap, ok := ctx.Attributes[logicalID]; ok {
		if val, ok := attrMap[attr]; ok {
			return val
		}
	}
	// Fallback: return physical ID (which is often an ARN).
	if physID, ok := ctx.Resources[logicalID]; ok {
		return physID
	}
	return fmt.Sprintf("%s.%s", logicalID, attr)
}

// resolveIf handles Fn::If: [conditionName, trueValue, falseValue].
func resolveIf(ifCond any, ctx *resolveContext) any {
	arr, ok := ifCond.([]any)
	if !ok || len(arr) != 3 {
		return ifCond
	}
	condName, _ := arr[0].(string)
	if ctx.Conditions[condName] {
		return resolveIntrinsics(arr[1], ctx)
	}
	return resolveIntrinsics(arr[2], ctx)
}

// resolveSplit handles Fn::Split: [delimiter, string].
func resolveSplit(split any, ctx *resolveContext) any {
	arr, ok := split.([]any)
	if !ok || len(arr) != 2 {
		return split
	}
	delimiter, _ := arr[0].(string)
	source := resolveIntrinsics(arr[1], ctx)
	sourceStr, _ := source.(string)
	parts := strings.Split(sourceStr, delimiter)
	result := make([]any, len(parts))
	for i, p := range parts {
		result[i] = p
	}
	return result
}

// resolveGetAZs returns AZs for the region.
func resolveGetAZs(ctx *resolveContext) any {
	return []any{
		ctx.Region + "a",
		ctx.Region + "b",
		ctx.Region + "c",
	}
}

// resolveImportValue handles Fn::ImportValue: exportName.
// Looks up the export from the Exports map populated by the provisioner.
func resolveImportValue(impVal any, ctx *resolveContext) any {
	// The export name may itself be an intrinsic (e.g. Fn::Sub).
	resolved := resolveIntrinsics(impVal, ctx)
	name, ok := resolved.(string)
	if !ok {
		return fmt.Sprintf("%v", resolved)
	}
	if ctx.Exports != nil {
		if val, found := ctx.Exports[name]; found {
			return val
		}
	}
	// Export not found — return the name as-is (best-effort).
	return name
}

// resolveAllProperties resolves the intrinsic functions in a resource's
// properties. Dynamic references are deliberately left as written — expanding
// them is a separate pass, and which form a caller wants depends on what it is
// for. See provisioner.resolveProperties.
func resolveAllProperties(props map[string]any, ctx *resolveContext) map[string]any {
	if props == nil {
		return nil
	}
	resolved := resolveIntrinsics(props, ctx)
	if m, ok := resolved.(map[string]any); ok {
		return m
	}
	return props
}

// resolveFindInMap handles Fn::FindInMap: [MapName, TopLevelKey, SecondLevelKey].
//
// The keys are commonly intrinsics themselves — {"Ref": "AWS::Region"} against a
// region-keyed map is the oldest CloudFormation idiom there is — so each is
// resolved before lookup.
//
// A miss resolves to the empty string. Real CloudFormation rejects the template
// ("Unable to get mapping for FindInMap"), but the resolver has no way to raise
// a template error, and an empty value is far less harmful than embedding Go's
// formatting of the unresolved map into a resource property.
func resolveFindInMap(findInMap any, ctx *resolveContext) any {
	arr, ok := findInMap.([]any)
	if !ok || len(arr) < 3 || ctx == nil || ctx.Mappings == nil {
		return ""
	}
	name, _ := resolveIntrinsics(arr[0], ctx).(string)
	topKey, _ := resolveIntrinsics(arr[1], ctx).(string)
	secondKey, _ := resolveIntrinsics(arr[2], ctx).(string)

	topLevel, ok := ctx.Mappings[name].(map[string]any)
	if !ok {
		return ""
	}
	secondLevel, ok := topLevel[topKey].(map[string]any)
	if !ok {
		return ""
	}
	value, ok := secondLevel[secondKey]
	if !ok {
		return ""
	}
	return value
}

// resolveBase64 handles Fn::Base64, which AWS defines over the already-resolved
// value — EC2 UserData is nearly always an Fn::Sub underneath.
func resolveBase64(value any, ctx *resolveContext) any {
	resolved := resolveIntrinsics(value, ctx)
	str, ok := resolved.(string)
	if !ok {
		str = fmt.Sprintf("%v", resolved)
	}
	return base64.StdEncoding.EncodeToString([]byte(str))
}

// resolveCidr handles Fn::Cidr: [ipBlock, count, cidrBits], returning count
// consecutive subnets of the block. cidrBits is the number of *host* bits in
// each subnet, so 8 yields /24s — the parameter is a size, not a prefix length,
// which is the easy thing to get backwards.
//
// IPv4 only: Overcast has no IPv6 networking for a template to describe.
func resolveCidr(cidr any, ctx *resolveContext) any {
	arr, ok := cidr.([]any)
	if !ok || len(arr) < 3 {
		return ""
	}
	block, _ := resolveIntrinsics(arr[0], ctx).(string)
	count := intFromTemplateValue(resolveIntrinsics(arr[1], ctx))
	hostBits := intFromTemplateValue(resolveIntrinsics(arr[2], ctx))

	prefix, err := netip.ParsePrefix(block)
	if err != nil || !prefix.Addr().Is4() || count <= 0 || hostBits <= 0 || hostBits >= 32 {
		return ""
	}
	subnetBits := 32 - hostBits
	if subnetBits < prefix.Bits() {
		return "" // the requested subnets are larger than the block itself
	}

	start := prefix.Masked().Addr().As4()
	base := binary.BigEndian.Uint32(start[:])
	step := uint32(1) << uint(hostBits)

	out := make([]any, 0, count)
	for i := range count {
		var octets [4]byte
		binary.BigEndian.PutUint32(octets[:], base+uint32(i)*step)
		out = append(out, fmt.Sprintf("%s/%d", netip.AddrFrom4(octets), subnetBits))
	}
	return out
}

// intFromTemplateValue reads a count from a template, which may arrive as a JSON
// number or as a string (YAML, or a Ref to a parameter).
func intFromTemplateValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		parsed, err := strconv.Atoi(n)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
