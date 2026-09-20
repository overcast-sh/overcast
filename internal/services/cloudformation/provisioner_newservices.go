package cloudformation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/overcast-sh/overcast/internal/clock"
	"github.com/overcast-sh/overcast/internal/config"
	"github.com/overcast-sh/overcast/internal/protocol"
)

// ── RDS stabilization ──────────────────────────────────────────────────────
//
// CreateDBInstance and CreateDBCluster are asynchronous: they answer with the
// database in "creating" and it comes up behind them. CloudFormation does not
// pass that on. An AWS::RDS::DBInstance is not CREATE_COMPLETE until the
// instance reaches "available", and everything downstream of it — a DependsOn,
// a GetAtt on Endpoint.Address, a secret attachment — waits behind that.
//
// Returning as soon as the API answered made `cdk deploy` report success while
// the engine was still initialising its data directory, which against a real
// MySQL container is around half a minute after the create returns: a migration
// or an app started off the back of a green deploy was refused a connection.
// It broke the failure case in the other direction too — an instance whose
// container never came up settled into "failed" behind a stack that had already
// claimed CREATE_COMPLETE, where AWS fails the resource and rolls the stack
// back.
//
// Create and update share the wait, as they do for an ECS service and for the
// same reason: they are the same question about the same resource, and the bug
// is what happens when the two answers drift apart.

const (
	// rdsStabilizeTimeout bounds the wait for a database to come up. It has to
	// exceed what the RDS service itself will spend before giving up — a
	// five-minute health-check budget that only starts once the container is
	// running, behind an image pull that may be cold — or a stack would report a
	// timeout over an instance that was still legitimately coming up and blame
	// the wrong thing for it. CloudFormation waits hours; the instance's own
	// "failed" status is the fast path out of here, so this only ever bites on a
	// database that is genuinely wedged.
	rdsStabilizeTimeout = 15 * time.Minute
	// rdsStabilizePollInterval is how often the status is re-read. A database
	// takes tens of seconds at the very best, so there is nothing to gain from
	// asking faster; the calls are in-process, so there is little to lose either.
	rdsStabilizePollInterval = 250 * time.Millisecond
)

// rdsStatusOutcome is what a DB instance or cluster status means for the
// resource waiting on it.
type rdsStatusOutcome int

const (
	rdsStatusWaiting rdsStatusOutcome = iota
	rdsStatusAvailable
	rdsStatusFailed
)

// rdsFailedStatuses are the statuses AWS documents as "the database is not
// coming up". Overcast only ever produces "failed" itself; the rest are here so
// that a status arriving from anywhere else is not mistaken for progress and
// waited out for the full budget.
var rdsFailedStatuses = map[string]bool{
	"cloning-failed":                      true,
	"failed":                              true,
	"inaccessible-encryption-credentials": true,
	"incompatible-create":                 true,
	"incompatible-network":                true,
	"incompatible-option-group":           true,
	"incompatible-parameters":             true,
	"incompatible-restore":                true,
	"insufficient-capacity":               true,
	"migration-failed":                    true,
	"restore-error":                       true,
	"upgrade-failed":                      true,
}

// rdsOutcome classifies a status. Anything unrecognised keeps the resource
// waiting: AWS adds statuses, and treating an unknown one as done would
// complete the resource over a database in an unknown state — the failure this
// whole path exists to prevent.
func rdsOutcome(status string) rdsStatusOutcome {
	switch {
	case status == "available":
		return rdsStatusAvailable
	case rdsFailedStatuses[status]:
		return rdsStatusFailed
	default:
		return rdsStatusWaiting
	}
}

// awaitRDSAvailable polls describe until the database reports "available".
// subject names the resource in every message this produces ("DB instance
// appdb"); reason, which may be nil, supplies the database's own account of why
// it failed.
func awaitRDSAvailable(ctx context.Context, clk clock.Clock, subject string, describe func() (string, error), reason func() string) error {
	deadline := clk.Now().Add(rdsStabilizeTimeout)
	for {
		status, err := describe()
		if err != nil {
			return err
		}
		switch rdsOutcome(status) {
		case rdsStatusAvailable:
			return nil
		case rdsStatusFailed:
			// The status alone says nothing useful — "failed" is the same word
			// for an image that would not pull and an engine that would not
			// start. RDS records the difference as an event, which is where the
			// container's own exit reason ends up.
			if reason != nil {
				if r := reason(); r != "" {
					return fmt.Errorf("%s failed to come up: %s", subject, r)
				}
			}
			return fmt.Errorf("%s failed to come up (status %q)", subject, status)
		case rdsStatusWaiting:
			// Still coming up — fall through to the deadline check and poll again.
		}
		if clk.Now().After(deadline) {
			return fmt.Errorf("%s did not become available within %s (status %q)",
				subject, rdsStabilizeTimeout, status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clk.After(rdsStabilizePollInterval):
		}
	}
}

// describedRDSDatabases is the projection a status poll reads. Both describe
// calls are decoded through it: a response carries exactly one of the two
// element paths, and encoding/xml leaves the field the response does not
// mention empty.
type describedRDSDatabases struct {
	Instances []struct {
		Status string `xml:"DBInstanceStatus"`
	} `xml:"DescribeDBInstancesResult>DBInstances>DBInstance"`
	Clusters []struct {
		Status string `xml:"Status"`
	} `xml:"DescribeDBClustersResult>DBClusters>DBCluster"`
}

// describeRDSDatabases runs an RDS describe call and decodes it. The two calls
// differ in nothing but the name of the action and of the identifier parameter.
func describeRDSDatabases(ctx context.Context, router http.Handler, region, action, idParam, id string) (describedRDSDatabases, error) {
	var decoded describedRDSDatabases
	rec, err := internalQuery(ctx, router, region, map[string]string{
		"Action":  action,
		"Version": "2014-10-31",
		idParam:   id,
	})
	if err != nil {
		return decoded, fmt.Errorf("%s: %w", action, err)
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		return decoded, fmt.Errorf("%s: parse response: %w", action, err)
	}
	return decoded, nil
}

// waitForDBInstanceAvailable blocks until a DB instance's database answers, so
// the resource is not complete until the thing it stands for is usable.
func waitForDBInstanceAvailable(ctx context.Context, clk clock.Clock, router http.Handler, region, instanceID string) error {
	subject := fmt.Sprintf("DB instance %s", instanceID)
	return awaitRDSAvailable(ctx, clk, subject, func() (string, error) {
		decoded, err := describeRDSDatabases(ctx, router, region,
			"DescribeDBInstances", "DBInstanceIdentifier", instanceID)
		if err != nil {
			return "", err
		}
		if len(decoded.Instances) == 0 {
			// Deleted from under the stack. It is not going to become available,
			// and waiting out the budget for one only delays saying so.
			return "", fmt.Errorf("%s no longer exists", subject)
		}
		return decoded.Instances[0].Status, nil
	}, func() string {
		return latestDBInstanceEvent(ctx, router, region, instanceID)
	})
}

// waitForDBClusterAvailable is waitForDBInstanceAvailable for a cluster, which
// reports its state as "Status" rather than "DBInstanceStatus". Cluster events
// are not modelled, so a failure here has only its status to report.
func waitForDBClusterAvailable(ctx context.Context, clk clock.Clock, router http.Handler, region, clusterID string) error {
	subject := fmt.Sprintf("DB cluster %s", clusterID)
	return awaitRDSAvailable(ctx, clk, subject, func() (string, error) {
		decoded, err := describeRDSDatabases(ctx, router, region,
			"DescribeDBClusters", "DBClusterIdentifier", clusterID)
		if err != nil {
			return "", err
		}
		if len(decoded.Clusters) == 0 {
			return "", fmt.Errorf("%s no longer exists", subject)
		}
		return decoded.Clusters[0].Status, nil
	}, nil)
}

// latestDBInstanceEvent returns the most recent thing RDS recorded against an
// instance, which for one that has just failed is why. Best-effort: a stack
// failing for the reason it has is better than one failing because the reason
// could not be fetched, so every error here yields an empty string and lets the
// caller fall back to the status.
func latestDBInstanceEvent(ctx context.Context, router http.Handler, region, instanceID string) string {
	rec, err := internalQuery(ctx, router, region, map[string]string{
		"Action":           "DescribeEvents",
		"Version":          "2014-10-31",
		"SourceIdentifier": instanceID,
		"SourceType":       "db-instance",
	})
	if err != nil {
		return ""
	}
	var resp struct {
		Events []struct {
			Message string `xml:"Message"`
		} `xml:"DescribeEventsResult>Events>Event"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &resp); err != nil || len(resp.Events) == 0 {
		return ""
	}
	// RDS returns events oldest-first, so the newest is the last one — and the
	// failure is the last thing that happened to an instance that just failed.
	return resp.Events[len(resp.Events)-1].Message
}

// ── AWS::RDS::DBInstance ───────────────────────────────────────────────────

type rdsDBInstanceHandler struct{}

func (h *rdsDBInstanceHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	id, _ := props["DBInstanceIdentifier"].(string)
	if id == "" {
		// A name derived from the stack alone is the same name on every
		// provision, so a replacement collides with the instance it is meant to
		// stand alongside — and replacement creates before it deletes precisely
		// so a failed update can roll back. generatedName carries the random
		// component that makes the two coexist. RDS lowercases identifiers, so
		// match it rather than handing the engine a name it would rewrite.
		id = strings.ToLower(rCtx.generatedNameWithin(maxNameLenRDS))
	}

	params := map[string]string{
		"Action":               "CreateDBInstance",
		"Version":              "2014-10-31",
		"DBInstanceIdentifier": id,
	}
	if v, _ := props["Engine"].(string); v != "" {
		params["Engine"] = v
	}
	if v, _ := props["MasterUsername"].(string); v != "" {
		params["MasterUsername"] = v
	}
	if v, _ := props["MasterUserPassword"].(string); v != "" {
		params["MasterUserPassword"] = v
	}
	if v, _ := props["DBInstanceClass"].(string); v != "" {
		params["DBInstanceClass"] = v
	}
	if v, _ := props["EngineVersion"].(string); v != "" {
		params["EngineVersion"] = v
	}
	if v := fmtPropString(props, "AllocatedStorage"); v != "" {
		params["AllocatedStorage"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	}
	if v, _ := props["StorageType"].(string); v != "" {
		params["StorageType"] = v
	}
	if v, _ := props["DBName"].(string); v != "" {
		params["DBName"] = v
	}
	if v, _ := props["DBClusterIdentifier"].(string); v != "" {
		params["DBClusterIdentifier"] = v
	}
	if v, _ := props["DBSubnetGroupName"].(string); v != "" {
		params["DBSubnetGroupName"] = v
	}
	if v, _ := props["DBParameterGroupName"].(string); v != "" {
		params["DBParameterGroupName"] = v
	}
	if v, ok := props["MultiAZ"]; ok {
		params["MultiAZ"] = cfnScalarString(v)
	}
	// The escape hatch that keeps a subnet-group instance reachable from the
	// default plane. Dropping it here left the template's own opt-out with no
	// effect, and the instance private with no way to say otherwise.
	if v, ok := props["PubliclyAccessible"]; ok {
		params["PubliclyAccessible"] = cfnScalarString(v)
	}
	// VPCSecurityGroups
	if sgs, ok := props["VPCSecurityGroups"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("VpcSecurityGroupIds.member.%d", i+1)] = s
			}
		}
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDBInstance: %w", err)
	}

	body := rec.Body.String()
	arn := extractXMLValue(body, "DBInstanceArn")
	endpointAddr := extractXMLValue(body, "Address")
	endpointPort := extractXMLValue(body, "Port")

	if arn == "" {
		arn = fmt.Sprintf("arn:aws:rds:%s:%s:db:%s", rCtx.Region, rCtx.AccountID, id)
	}

	attrs := map[string]string{
		"DBInstanceArn":    arn,
		"Endpoint.Address": endpointAddr,
		"Endpoint.Port":    endpointPort,
	}
	return id, attrs, nil
}

// Stabilize holds the resource open until the database answers. The endpoint
// attributes above are minted at create time, so they are known before the
// database is — and a GetAtt on Endpoint.Address is exactly the dependency that
// must not run early. See resourceStabilizer.
func (h *rdsDBInstanceHandler) Stabilize(ctx context.Context, router http.Handler, _ *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	return waitForDBInstanceAvailable(ctx, clk, router, rCtx.Region, physicalID)
}

func (h *rdsDBInstanceHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":               "DeleteDBInstance",
		"Version":              "2014-10-31",
		"DBInstanceIdentifier": physicalID,
		"SkipFinalSnapshot":    "true",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteDBInstance", rec, err)
}

// rdsInstanceReplaceOnChange are the AWS::RDS::DBInstance properties AWS
// documents as "Update requires: Replacement". None of them can be applied by
// ModifyDBInstance, so an update that changes one and reports success leaves
// the instance holding its old value behind a stack that claims otherwise —
// which is how an instance kept an unresolved "{{resolve:secretsmanager:...}}"
// master username across redeploys long after the reference itself resolved.
//
// A property is only compared when the template carried it both times: a value
// appearing or disappearing between templates is far more often a template
// being tidied than an intent to rebuild the database.
var rdsInstanceReplaceOnChange = []string{
	"DBInstanceIdentifier",
	"Engine",
	"MasterUsername",
	"DBName",
}

func (h *rdsDBInstanceHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		for _, name := range rdsInstanceReplaceOnChange {
			newVal, _ := props[name].(string)
			oldVal, _ := oldProps[name].(string)
			if newVal != "" && oldVal != "" && newVal != oldVal {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":               "ModifyDBInstance",
		"Version":              "2014-10-31",
		"DBInstanceIdentifier": physicalID,
	}
	if v := fmtPropString(props, "AllocatedStorage"); v != "" {
		params["AllocatedStorage"] = v
	}
	if v, _ := props["DBInstanceClass"].(string); v != "" {
		params["DBInstanceClass"] = v
	}
	if v, _ := props["MasterUserPassword"].(string); v != "" {
		params["MasterUserPassword"] = v
	}

	// ModifyDBInstance puts the instance into "modifying" and settles it
	// afterwards, so the resource is no more complete when this returns than it
	// is on the create path. The provisioner waits for it — see Stabilize.
	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyDBInstance: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::RDS::DBCluster ───────────────────────────────────────────────────

type rdsDBClusterHandler struct{}

func (h *rdsDBClusterHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	id, _ := props["DBClusterIdentifier"].(string)
	if id == "" {
		// Same rule as the DB instance identifier above: 63 characters,
		// stored lowercase, and no trailing or doubled hyphen — which is why
		// generatedNameWithin trims a truncation that lands on one.
		id = rCtx.generatedNameLowerWithin(maxNameLenRDS)
	}

	params := map[string]string{
		"Action":              "CreateDBCluster",
		"Version":             "2014-10-31",
		"DBClusterIdentifier": id,
	}
	if v, _ := props["Engine"].(string); v != "" {
		params["Engine"] = v
	}
	if v, _ := props["MasterUsername"].(string); v != "" {
		params["MasterUsername"] = v
	}
	if v, _ := props["MasterUserPassword"].(string); v != "" {
		params["MasterUserPassword"] = v
	}
	if v, _ := props["EngineVersion"].(string); v != "" {
		params["EngineVersion"] = v
	}
	if v, _ := props["DatabaseName"].(string); v != "" {
		params["DatabaseName"] = v
	}
	if v, _ := props["StorageType"].(string); v != "" {
		params["StorageType"] = v
	}
	if v, _ := props["DBSubnetGroupName"].(string); v != "" {
		params["DBSubnetGroupName"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	}
	if sgs, ok := props["VpcSecurityGroupIds"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("VpcSecurityGroupIds.member.%d", i+1)] = s
			}
		}
	}
	// The settings ModifyDBCluster can change are set at create too. Reading
	// them only on the update path would leave a template that sets them once
	// and never touches them again with a cluster that has never had them
	// applied — and would make the first no-op update look like a change.
	if v := fmtPropString(props, "BackupRetentionPeriod"); v != "" {
		params["BackupRetentionPeriod"] = v
	}
	if v, _ := props["PreferredBackupWindow"].(string); v != "" {
		params["PreferredBackupWindow"] = v
	}
	if v, _ := props["PreferredMaintenanceWindow"].(string); v != "" {
		params["PreferredMaintenanceWindow"] = v
	}
	if v, _ := props["DBClusterParameterGroupName"].(string); v != "" {
		params["DBClusterParameterGroupName"] = v
	}
	if v, ok := props["DeletionProtection"]; ok {
		params["DeletionProtection"] = cfnScalarString(v)
	}
	if logs, ok := props["EnableCloudwatchLogsExports"].([]any); ok {
		for i, l := range logs {
			if s, _ := l.(string); s != "" {
				params[fmt.Sprintf("EnableCloudwatchLogsExports.member.%d", i+1)] = s
			}
		}
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDBCluster: %w", err)
	}

	body := rec.Body.String()
	arn := extractXMLValue(body, "DBClusterArn")
	endpoint := extractXMLValue(body, "Endpoint")
	readerEndpoint := extractXMLValue(body, "ReaderEndpoint")
	port := extractXMLValue(body, "Port")

	if arn == "" {
		arn = fmt.Sprintf("arn:aws:rds:%s:%s:cluster:%s", rCtx.Region, rCtx.AccountID, id)
	}

	attrs := map[string]string{
		"DBClusterArn":         arn,
		"Endpoint.Address":     endpoint,
		"Endpoint.Port":        port,
		"ReadEndpoint.Address": readerEndpoint,
		"DBClusterResourceId":  fmt.Sprintf("cluster-%s", id),
	}
	return id, attrs, nil
}

// Stabilize holds the resource open until the cluster is available, for the
// same reason a DB instance does. See resourceStabilizer.
func (h *rdsDBClusterHandler) Stabilize(ctx context.Context, router http.Handler, _ *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	return waitForDBClusterAvailable(ctx, clk, router, rCtx.Region, physicalID)
}

func (h *rdsDBClusterHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":              "DeleteDBCluster",
		"Version":             "2014-10-31",
		"DBClusterIdentifier": physicalID,
		"SkipFinalSnapshot":   "true",
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err == nil {
		return nil
	}
	// Deletion protection is a refusal, not a failure, and it is the one that
	// has to stop a DeleteStack outright. On AWS a stack cannot delete a
	// protected cluster: the delete fails, the cluster survives, and the
	// operator disables the flag and tries again. Reporting it as an ordinary
	// error would let a deliberate teardown record DELETE_COMPLETE over a
	// cluster that is still there, which is the failure the flag exists to
	// prevent.
	if rec != nil && strings.Contains(rec.Body.String(), "InvalidParameterCombination") {
		return fmt.Errorf("%w: DB cluster %s has deletion protection enabled", errDeletionBlocked, physicalID)
	}
	return teardownError("DeleteDBCluster", rec, err)
}

// rdsClusterReplaceOnChange are the AWS::RDS::DBCluster properties AWS
// documents as "Update requires: Replacement". ModifyDBCluster cannot apply
// any of them, so an update that changes one and reports success leaves the
// cluster holding its old value behind a stack that claims otherwise — the
// same defect rdsInstanceReplaceOnChange exists to prevent one level down.
//
// As there, a property is only compared when the template carried it both
// times: a value appearing or disappearing between templates is far more often
// a template being tidied than an intent to rebuild the database.
var rdsClusterReplaceOnChange = []string{
	"Engine",
	"MasterUsername",
	"DatabaseName",
	"DBSubnetGroupName",
}

func (h *rdsDBClusterHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if newID, _ := props["DBClusterIdentifier"].(string); newID != "" && newID != physicalID {
		return "", nil, errReplacementRequired
	}
	if oldProps != nil {
		for _, name := range rdsClusterReplaceOnChange {
			newVal, _ := props[name].(string)
			oldVal, _ := oldProps[name].(string)
			if newVal != "" && oldVal != "" && newVal != oldVal {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":              "ModifyDBCluster",
		"Version":             "2014-10-31",
		"DBClusterIdentifier": physicalID,
	}
	if v, _ := props["MasterUserPassword"].(string); v != "" {
		params["MasterUserPassword"] = v
	}
	if v := fmtPropString(props, "BackupRetentionPeriod"); v != "" {
		params["BackupRetentionPeriod"] = v
	}
	if v := fmtPropString(props, "Port"); v != "" {
		params["Port"] = v
	}
	if v, _ := props["PreferredBackupWindow"].(string); v != "" {
		params["PreferredBackupWindow"] = v
	}
	if v, _ := props["PreferredMaintenanceWindow"].(string); v != "" {
		params["PreferredMaintenanceWindow"] = v
	}
	if v, _ := props["DBClusterParameterGroupName"].(string); v != "" {
		params["DBClusterParameterGroupName"] = v
	}
	if sgs, ok := props["VpcSecurityGroupIds"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("VpcSecurityGroupIds.VpcSecurityGroupId.%d", i+1)] = s
			}
		}
	}
	if v, ok := props["EnableCloudwatchLogsExports"]; ok {
		if logs, ok := v.([]any); ok {
			for i, l := range logs {
				if s, _ := l.(string); s != "" {
					params[fmt.Sprintf("CloudwatchLogsExportConfiguration.EnableLogTypes.member.%d", i+1)] = s
				}
			}
		}
	}
	if v, ok := props["DeletionProtection"]; ok {
		params["DeletionProtection"] = cfnScalarString(v)
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyDBCluster: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::RDS::DBSubnetGroup ───────────────────────────────────────────────

type rdsDBSubnetGroupHandler struct{}

func (h *rdsDBSubnetGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["DBSubnetGroupName"].(string)
	if name == "" {
		// 255 characters rather than the identifier's 63, but stored
		// lowercase the same way.
		name = rCtx.generatedNameLowerWithin(maxNameLenDefault)
	}
	desc, _ := props["DBSubnetGroupDescription"].(string)
	if desc == "" {
		desc = name
	}

	params := map[string]string{
		"Action":                   "CreateDBSubnetGroup",
		"Version":                  "2014-10-31",
		"DBSubnetGroupName":        name,
		"DBSubnetGroupDescription": desc,
	}
	if subnets, ok := props["SubnetIds"].([]any); ok {
		for i, s := range subnets {
			if v, _ := s.(string); v != "" {
				params[fmt.Sprintf("SubnetIds.member.%d", i+1)] = v
			}
		}
	}

	_, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDBSubnetGroup: %w", err)
	}
	return name, nil, nil
}

func (h *rdsDBSubnetGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":            "DeleteDBSubnetGroup",
		"Version":           "2014-10-31",
		"DBSubnetGroupName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteDBSubnetGroup", rec, err)
}

func (h *rdsDBSubnetGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the group name. DBSubnetGroupName is the only
	// replacement property on real AWS, and CreateDBSubnetGroup rejects
	// duplicates, so replacing under an unchanged name can never succeed.
	// The emulated RDS has no ModifyDBSubnetGroup, so keeping the group is
	// the closest available in-place update.
	if n, ok := props["DBSubnetGroupName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	return physicalID, nil, nil
}

// ── AWS::RDS::DBParameterGroup ────────────────────────────────────────────

type rdsDBParameterGroupHandler struct{}

func (h *rdsDBParameterGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["DBParameterGroupName"].(string)
	if name == "" {
		// 1-255, stored lowercase, no trailing or doubled hyphen.
		name = rCtx.generatedNameLowerWithin(maxNameLenDefault)
	}

	params := map[string]string{
		"Action":               "CreateDBParameterGroup",
		"Version":              "2014-10-31",
		"DBParameterGroupName": name,
	}
	if v, _ := props["Family"].(string); v != "" {
		params["DBParameterGroupFamily"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		params["Description"] = v
	}

	_, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDBParameterGroup: %w", err)
	}
	return name, nil, nil
}

func (h *rdsDBParameterGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":               "DeleteDBParameterGroup",
		"Version":              "2014-10-31",
		"DBParameterGroupName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteDBParameterGroup", rec, err)
}

func (h *rdsDBParameterGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if newName, _ := props["DBParameterGroupName"].(string); newName != "" && newName != physicalID {
		return "", nil, errReplacementRequired
	}
	if oldProps != nil {
		if newFamily, _ := props["Family"].(string); newFamily != "" {
			if oldFamily, _ := oldProps["Family"].(string); oldFamily != "" && newFamily != oldFamily {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":               "ModifyDBParameterGroup",
		"Version":              "2014-10-31",
		"DBParameterGroupName": physicalID,
	}

	if ps, ok := props["Parameters"]; ok {
		switch v := ps.(type) {
		case map[string]any:
			idx := 0
			for k, val := range v {
				idx++
				params[fmt.Sprintf("Parameters.member.%d.ParameterName", idx)] = k
				params[fmt.Sprintf("Parameters.member.%d.ParameterValue", idx)] = fmt.Sprintf("%v", val)
				params[fmt.Sprintf("Parameters.member.%d.ApplyMethod", idx)] = "immediate"
			}
		case []any:
			for i, p := range v {
				if pm, ok := p.(map[string]any); ok {
					if name, _ := pm["ParameterName"].(string); name != "" {
						params[fmt.Sprintf("Parameters.member.%d.ParameterName", i+1)] = name
					}
					if val := pm["ParameterValue"]; val != nil {
						params[fmt.Sprintf("Parameters.member.%d.ParameterValue", i+1)] = fmt.Sprintf("%v", val)
					}
					if apply, _ := pm["ApplyMethod"].(string); apply != "" {
						params[fmt.Sprintf("Parameters.member.%d.ApplyMethod", i+1)] = apply
					}
				}
			}
		}
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyDBParameterGroup: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::Kinesis::Stream ──────────────────────────────────────────────────

type kinesisStreamHandler struct{}

// kinesisStreamDefaultRetentionHours is what CreateStream leaves an
// AWS::Kinesis::Stream at when the template does not set
// RetentionPeriodHours (kinesis/handler.go's CreateStream/createStreamTyped
// hardcode the same value).
const kinesisStreamDefaultRetentionHours = 24

func (h *kinesisStreamHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["Name"].(string)
	if name == "" {
		// A generated, per-resource-instance name — not a deterministic
		// "<StackName>-stream" — so a replacement (Name unset, ShardCount
		// changed) can create the new stream before the old one is torn
		// down instead of colliding with it under the same name. Mirrors
		// every other unnamed-resource handler (SNS topics, SQS queues,
		// Lambda functions, ...).
		name = rCtx.generatedNameWithin(maxNameLenKinesis)
	}

	body := map[string]any{
		"StreamName": name,
	}
	if v, ok := props["ShardCount"]; ok {
		body["ShardCount"] = v
	} else {
		body["ShardCount"] = 1
	}
	// StreamModeDetails is a genuine CreateStream parameter on real Kinesis
	// (unlike StreamEncryption below, which has no create-time equivalent) —
	// its wire shape ({StreamMode: ...}) already matches the CFN property, so
	// it forwards as-is.
	if smd, ok := props["StreamModeDetails"]; ok {
		body["StreamModeDetails"] = smd
	}
	if tags := mergeResourceTags(rCtx.StackTags, props["Tags"]); len(tags) > 0 {
		body["Tags"] = tags
	}

	_, err := internalJSON(ctx, router, rCtx.Region, "Kinesis_20131202.CreateStream", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateStream: %w", err)
	}

	if err := reconcileKinesisRetention(ctx, router, rCtx.Region, name, kinesisEffectiveRetentionHours(props), kinesisStreamDefaultRetentionHours); err != nil {
		return "", nil, err
	}
	// StreamEncryption has no CreateStream parameter on real Kinesis — a
	// template that sets it goes through StartStreamEncryption right after
	// the stream comes up, same as the AWS::Kinesis::Stream resource
	// provider does.
	if err := reconcileKinesisStreamEncryption(ctx, router, rCtx.Region, name, props["StreamEncryption"], nil); err != nil {
		return "", nil, err
	}

	arn := fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/%s", rCtx.Region, rCtx.AccountID, name)
	attrs := map[string]string{
		"Arn":  arn,
		"Name": name,
	}
	return name, attrs, nil
}

func (h *kinesisStreamHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"StreamName": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "Kinesis_20131202.DeleteStream", body)
	return teardownError("DeleteStream", rec, err)
}

// Update reconciles everything Kinesis can change on a live stream in
// place — RetentionPeriodHours, Tags, StreamEncryption and StreamModeDetails
// — and only forces replacement for Name and ShardCount, neither of which
// has an in-place equivalent here: ShardCount specifically needs
// UpdateShardCount, which Overcast does not implement yet (#1096).
func (h *kinesisStreamHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newName, _ := props["Name"].(string); newName != "" {
			if oldName, _ := oldProps["Name"].(string); oldName != "" && newName != oldName {
				return "", nil, errReplacementRequired
			}
		}
		if !reflect.DeepEqual(props["ShardCount"], oldProps["ShardCount"]) {
			return "", nil, errReplacementRequired
		}
	}

	name := physicalID
	if n, _ := props["Name"].(string); n != "" {
		name = n
	}
	arn := fmt.Sprintf("arn:aws:kinesis:%s:%s:stream/%s", rCtx.Region, rCtx.AccountID, name)

	oldHours := kinesisStreamDefaultRetentionHours
	var oldTags, oldEncryption, oldMode any
	if oldProps != nil {
		oldHours = kinesisEffectiveRetentionHours(oldProps)
		oldTags = oldProps["Tags"]
		oldEncryption = oldProps["StreamEncryption"]
		oldMode = oldProps["StreamModeDetails"]
	}

	if err := reconcileKinesisRetention(ctx, router, rCtx.Region, name, kinesisEffectiveRetentionHours(props), oldHours); err != nil {
		return "", nil, err
	}
	if err := reconcileKinesisStreamTags(ctx, router, rCtx.Region, name, rCtx.StackTags, rCtx.PreviousStackTags, props["Tags"], oldTags); err != nil {
		return "", nil, err
	}
	if err := reconcileKinesisStreamEncryption(ctx, router, rCtx.Region, name, props["StreamEncryption"], oldEncryption); err != nil {
		return "", nil, err
	}
	if err := reconcileKinesisStreamMode(ctx, router, rCtx.Region, arn, props["StreamModeDetails"], oldMode); err != nil {
		return "", nil, err
	}

	attrs := map[string]string{
		"Arn":  arn,
		"Name": name,
	}
	return name, attrs, nil
}

// kinesisEffectiveRetentionHours reads RetentionPeriodHours off a Kinesis
// stream's properties, defaulting to Kinesis' own 24-hour default when the
// template does not set it — including when it is dropped from a template on
// update, which real CloudFormation also resolves back to the default.
func kinesisEffectiveRetentionHours(props map[string]any) int {
	if v, ok := props["RetentionPeriodHours"]; ok {
		if n, err := cfnInt64(v); err == nil {
			return int(n)
		}
	}
	return kinesisStreamDefaultRetentionHours
}

// reconcileKinesisRetention threads AWS::Kinesis::Stream's
// RetentionPeriodHours to the stream via Increase/DecreaseStreamRetentionPeriod
// — CreateStream itself has no retention parameter on real Kinesis, and every
// stream starts at the 24-hour default, so applying a non-default value is
// itself a reconciliation against that starting point.
func reconcileKinesisRetention(ctx context.Context, router http.Handler, region, streamName string, newHours, oldHours int) error {
	if newHours == oldHours {
		return nil
	}
	target := "Kinesis_20131202.IncreaseStreamRetentionPeriod"
	if newHours < oldHours {
		target = "Kinesis_20131202.DecreaseStreamRetentionPeriod"
	}
	body := map[string]any{"StreamName": streamName, "RetentionPeriodHours": newHours}
	if _, err := internalJSON(ctx, router, region, target, body); err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	return nil
}

// reconcileKinesisStreamTags reconciles a stream's tags via
// AddTagsToStream/RemoveTagsFromStream: added/changed keys go through
// AddTagsToStream, keys dropped from the template go through
// RemoveTagsFromStream. Mirrors updateSQSQueueTags' diff shape.
func reconcileKinesisStreamTags(ctx context.Context, router http.Handler, region, streamName string, stackTags, priorStackTags []Tag, rawTags, rawPrior any) error {
	tags := mergeResourceTags(stackTags, rawTags)
	prior := mergeResourceTags(priorStackTags, rawPrior)

	added := make(map[string]string)
	for key, value := range tags {
		if prior[key] != value {
			added[key] = value
		}
	}
	removed := make([]string, 0)
	for key := range prior {
		if _, ok := tags[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)

	if len(added) > 0 {
		body := map[string]any{"StreamName": streamName, "Tags": added}
		if _, err := internalJSON(ctx, router, region, "Kinesis_20131202.AddTagsToStream", body); err != nil {
			return fmt.Errorf("kinesis AddTagsToStream: %w", err)
		}
	}
	if len(removed) > 0 {
		body := map[string]any{"StreamName": streamName, "TagKeys": removed}
		if _, err := internalJSON(ctx, router, region, "Kinesis_20131202.RemoveTagsFromStream", body); err != nil {
			return fmt.Errorf("kinesis RemoveTagsFromStream: %w", err)
		}
	}
	return nil
}

// reconcileKinesisStreamEncryption starts or stops a stream's server-side
// encryption when AWS::Kinesis::Stream's StreamEncryption property changed.
// A template that never sets it leaves the stream at CreateStream's
// EncryptionType="NONE" default; removing it on update stops encryption the
// same way real CloudFormation does.
func reconcileKinesisStreamEncryption(ctx context.Context, router http.Handler, region, streamName string, newRaw, oldRaw any) error {
	newEnc, _ := newRaw.(map[string]any)
	oldEnc, _ := oldRaw.(map[string]any)
	if reflect.DeepEqual(newEnc, oldEnc) {
		return nil
	}
	if len(newEnc) > 0 {
		encryptionType, _ := newEnc["EncryptionType"].(string)
		if encryptionType == "" {
			encryptionType = "KMS"
		}
		keyID, _ := newEnc["KeyId"].(string)
		body := map[string]any{"StreamName": streamName, "EncryptionType": encryptionType, "KeyId": keyID}
		if _, err := internalJSON(ctx, router, region, "Kinesis_20131202.StartStreamEncryption", body); err != nil {
			return fmt.Errorf("StartStreamEncryption: %w", err)
		}
		return nil
	}
	if len(oldEnc) > 0 {
		encryptionType, _ := oldEnc["EncryptionType"].(string)
		keyID, _ := oldEnc["KeyId"].(string)
		body := map[string]any{"StreamName": streamName, "EncryptionType": encryptionType, "KeyId": keyID}
		if _, err := internalJSON(ctx, router, region, "Kinesis_20131202.StopStreamEncryption", body); err != nil {
			return fmt.Errorf("StopStreamEncryption: %w", err)
		}
	}
	return nil
}

// reconcileKinesisStreamMode applies a StreamModeDetails change via
// UpdateStreamMode (ARN-addressed, matching real Kinesis — it has no
// StreamName alternative). A template that drops StreamModeDetails on update
// leaves the stream's mode alone: Kinesis has no "reset to default" call,
// and real CloudFormation does the same for an already-provisioned stream.
func reconcileKinesisStreamMode(ctx context.Context, router http.Handler, region, streamARN string, newRaw, oldRaw any) error {
	if reflect.DeepEqual(newRaw, oldRaw) {
		return nil
	}
	smd, ok := newRaw.(map[string]any)
	if !ok || len(smd) == 0 {
		return nil
	}
	body := map[string]any{"StreamARN": streamARN, "StreamModeDetails": smd}
	if _, err := internalJSON(ctx, router, region, "Kinesis_20131202.UpdateStreamMode", body); err != nil {
		return fmt.Errorf("UpdateStreamMode: %w", err)
	}
	return nil
}

// ── AWS::Cognito::UserPool ────────────────────────────────────────────────

// cognitoUserPoolMutableProps lists the AWS::Cognito::UserPool properties
// that CreateUserPool and UpdateUserPool both accept (per the CreateUserPool/
// UpdateUserPool API shapes), so a template change reconciles onto the
// existing pool instead of forcing replacement. AliasAttributes,
// UsernameAttributes, Schema, and UsernameConfiguration are absent from
// UpdateUserPoolRequest on real Cognito and are handled separately.
var cognitoUserPoolMutableProps = []string{
	"Policies",
	"VerificationMessageTemplate",
	"AdminCreateUserConfig",
	"EmailConfiguration",
	"UserAttributeUpdateSettings",
	"DeviceConfiguration",
	"AccountRecoverySetting",
	"SmsConfiguration",
	"SmsAuthenticationMessage",
	"SmsVerificationMessage",
	"EmailVerificationMessage",
	"EmailVerificationSubject",
	"LambdaConfig",
}

type cognitoUserPoolHandler struct{}

func (h *cognitoUserPoolHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	poolName, _ := props["UserPoolName"].(string)
	if poolName == "" {
		poolName = rCtx.generatedNameWithin(maxNameLenCognito)
	}

	body := map[string]any{
		"PoolName": poolName,
	}
	for _, prop := range cognitoUserPoolMutableProps {
		if v, ok := props[prop]; ok {
			body[prop] = v
		}
	}
	// AliasAttributes and UsernameAttributes are create-only on real Cognito —
	// UpdateUserPool does not accept either — so they are threaded here but not
	// reconciled by Update (see cognitoUserPoolHandler.Update).
	if v, ok := props["UsernameAttributes"]; ok {
		body["UsernameAttributes"] = v
	}
	if v, ok := props["AliasAttributes"]; ok {
		body["AliasAttributes"] = v
	}
	// AutoVerifiedAttributes is stored and enforced by the Cognito service as
	// of issue #84. Schema is still threaded onto the wire for
	// forward-compatibility only (see issue #536 disposition notes in
	// capabilities_dev.go) and round-trips as dropped.
	if v, ok := props["AutoVerifiedAttributes"]; ok {
		body["AutoVerifiedAttributes"] = v
	}
	if v, ok := props["Schema"]; ok {
		body["Schema"] = v
	}
	if v, ok := props["MfaConfiguration"]; ok {
		body["MfaConfiguration"] = v
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.CreateUserPool", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateUserPool: %w", err)
	}

	var resp struct {
		UserPool struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
			Arn  string `json:"Arn"`
		} `json:"UserPool"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateUserPool: parse response: %w", err)
	}

	poolID := resp.UserPool.ID
	arn := resp.UserPool.Arn
	providerName := fmt.Sprintf("cognito-idp.%s.amazonaws.com/%s", rCtx.Region, poolID)

	attrs := map[string]string{
		"ProviderName": providerName,
		"ProviderURL":  fmt.Sprintf("https://%s", providerName),
		"Arn":          arn,
		"UserPoolId":   poolID,
	}
	return poolID, attrs, nil
}

func (h *cognitoUserPoolHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	body := map[string]any{"UserPoolId": physicalID}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.DeleteUserPool", body)
	return teardownError("DeleteUserPool", rec, err)
}

// Update reconciles the mutable AWS::Cognito::UserPool properties in place.
// AliasAttributes and UsernameAttributes are create-only on real Cognito —
// UpdateUserPoolRequest has no member for either — so a template change to
// them replaces the pool, matching real CloudFormation.
func (h *cognitoUserPoolHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	for _, immutable := range []string{"AliasAttributes", "UsernameAttributes"} {
		changed, err := cfnPropertyChanged(props, oldProps, immutable)
		if err != nil {
			return "", nil, failUpdate(err)
		}
		if changed {
			return "", nil, errReplacementRequired
		}
	}

	body := map[string]any{"UserPoolId": physicalID}
	haveMutable := false
	for _, prop := range cognitoUserPoolMutableProps {
		if v, ok := props[prop]; ok {
			body[prop] = v
			haveMutable = true
		}
	}
	if haveMutable {
		if _, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.UpdateUserPool", body); err != nil {
			return "", nil, fmt.Errorf("UpdateUserPool: %w", err)
		}
	}

	providerName := fmt.Sprintf("cognito-idp.%s.amazonaws.com/%s", rCtx.Region, physicalID)
	attrs := map[string]string{
		"ProviderName": providerName,
		"ProviderURL":  fmt.Sprintf("https://%s", providerName),
		"Arn":          fmt.Sprintf("arn:aws:cognito-idp:%s:%s:userpool/%s", rCtx.Region, cfg.AccountID, physicalID),
		"UserPoolId":   physicalID,
	}
	return physicalID, attrs, nil
}

// ── AWS::Cognito::UserPoolClient ──────────────────────────────────────────

// cognitoUserPoolClientMutableProps lists the AWS::Cognito::UserPoolClient
// properties that both CreateUserPoolClient and UpdateUserPoolClient accept.
// Every UserPoolClient property CloudFormation exposes is mutable on real
// Cognito except GenerateSecret (secret generation only happens at create).
var cognitoUserPoolClientMutableProps = []string{
	"ClientName",
	"ExplicitAuthFlows",
	"SupportedIdentityProviders",
	"CallbackURLs",
	"LogoutURLs",
	"AllowedOAuthFlows",
	"AllowedOAuthScopes",
	"AllowedOAuthFlowsUserPoolClient",
	"AccessTokenValidity",
	"IdTokenValidity",
	"RefreshTokenValidity",
	"TokenValidityUnits",
	"PreventUserExistenceErrors",
	"ReadAttributes",
	"WriteAttributes",
}

type cognitoUserPoolClientHandler struct{}

func (h *cognitoUserPoolClientHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	userPoolID, _ := props["UserPoolId"].(string)
	clientName, _ := props["ClientName"].(string)
	if clientName == "" {
		clientName = rCtx.generatedNameWithin(maxNameLenCognito)
	}

	body := map[string]any{
		"UserPoolId": userPoolID,
		"ClientName": clientName,
	}
	if v, ok := props["GenerateSecret"]; ok {
		body["GenerateSecret"] = v
	}
	for _, prop := range cognitoUserPoolClientMutableProps {
		if v, ok := props[prop]; ok {
			body[prop] = v
		}
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.CreateUserPoolClient", body)
	if err != nil {
		return "", nil, fmt.Errorf("CreateUserPoolClient: %w", err)
	}

	var resp struct {
		UserPoolClient struct {
			ClientID     string `json:"ClientId"`
			ClientName   string `json:"ClientName"`
			ClientSecret string `json:"ClientSecret"`
		} `json:"UserPoolClient"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("CreateUserPoolClient: parse response: %w", err)
	}

	clientID := resp.UserPoolClient.ClientID
	attrs := map[string]string{
		"ClientId":     clientID,
		"Name":         resp.UserPoolClient.ClientName,
		"ClientSecret": resp.UserPoolClient.ClientSecret,
		"Ref":          clientID,
	}
	// Encode UserPoolId in physicalID so Delete can recover it.
	physicalID := userPoolID + "/" + clientID
	return physicalID, attrs, nil
}

// Update reconciles the mutable AWS::Cognito::UserPoolClient properties in
// place. Every property CloudFormation exposes for this resource maps to an
// UpdateUserPoolClient member except GenerateSecret, which only applies at
// creation, so no property change forces replacement.
func (h *cognitoUserPoolClientHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return "", nil, failUpdate(fmt.Errorf("malformed UserPoolClient physical ID %q", physicalID))
	}
	userPoolID, clientID := parts[0], parts[1]

	body := map[string]any{
		"UserPoolId": userPoolID,
		"ClientId":   clientID,
	}
	for _, prop := range cognitoUserPoolClientMutableProps {
		if v, ok := props[prop]; ok {
			body[prop] = v
		}
	}

	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.UpdateUserPoolClient", body)
	if err != nil {
		return "", nil, fmt.Errorf("UpdateUserPoolClient: %w", err)
	}

	var resp struct {
		UserPoolClient struct {
			ClientID     string `json:"ClientId"`
			ClientName   string `json:"ClientName"`
			ClientSecret string `json:"ClientSecret"`
		} `json:"UserPoolClient"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		return "", nil, fmt.Errorf("UpdateUserPoolClient: parse response: %w", err)
	}
	attrs := map[string]string{
		"ClientId":     clientID,
		"Name":         resp.UserPoolClient.ClientName,
		"ClientSecret": resp.UserPoolClient.ClientSecret,
		"Ref":          clientID,
	}
	return physicalID, attrs, nil
}

func (h *cognitoUserPoolClientHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts := strings.SplitN(physicalID, "/", 2)
	if len(parts) != 2 {
		return nil
	}
	body := map[string]any{
		"UserPoolId": parts[0],
		"ClientId":   parts[1],
	}
	rec, err := internalJSON(ctx, router, rCtx.Region, "AWSCognitoIdentityProviderService.DeleteUserPoolClient", body)
	return teardownError("DeleteUserPoolClient", rec, err)
}

// ── AWS::AppSync::GraphQLApi ──────────────────────────────────────────────

type appsyncGraphQLApiHandler struct{}

type appsyncGraphQLApiResponse struct {
	GraphqlApi struct {
		ApiID string            `json:"apiId"`
		Arn   string            `json:"arn"`
		Uris  map[string]string `json:"uris"`
		Dns   map[string]string `json:"dns"`
	} `json:"graphqlApi"`
}

type appsyncApiKeyResponse struct {
	ApiKey struct {
		ID string `json:"id"`
	} `json:"apiKey"`
}

type appsyncFunctionResponse struct {
	FunctionConfiguration struct {
		FunctionID     string `json:"functionId"`
		FunctionArn    string `json:"functionArn"`
		Name           string `json:"name"`
		DataSourceName string `json:"dataSourceName"`
	} `json:"functionConfiguration"`
}

type appsyncDataSourceResponse struct {
	DataSource struct {
		DataSourceArn string `json:"dataSourceArn"`
		Name          string `json:"name"`
	} `json:"dataSource"`
}

type appsyncResolverResponse struct {
	Resolver struct {
		ResolverArn string `json:"resolverArn"`
		FieldName   string `json:"fieldName"`
		TypeName    string `json:"typeName"`
	} `json:"resolver"`
}

type appsyncDomainNameResponse struct {
	DomainNameConfig struct {
		DomainName        string `json:"domainName"`
		AppsyncDomainName string `json:"appsyncDomainName"`
		HostedZoneID      string `json:"hostedZoneId"`
	} `json:"domainNameConfig"`
}

type appsyncApiAssociationResponse struct {
	ApiAssociation struct {
		DomainName        string `json:"domainName"`
		ApiID             string `json:"apiId"`
		AssociationStatus string `json:"associationStatus"`
	} `json:"apiAssociation"`
}

type appsyncApiCacheResponse struct {
	ApiCache struct {
		Type               string `json:"type"`
		ApiCachingBehavior string `json:"apiCachingBehavior"`
		Status             string `json:"status"`
	} `json:"apiCache"`
}

type appsyncSourceApiAssociationResponse struct {
	SourceApiAssociation struct {
		AssociationID                    string `json:"associationId"`
		AssociationArn                   string `json:"associationArn"`
		SourceApiID                      string `json:"sourceApiId"`
		SourceApiArn                     string `json:"sourceApiArn"`
		MergedApiID                      string `json:"mergedApiId"`
		MergedApiArn                     string `json:"mergedApiArn"`
		SourceApiAssociationStatus       string `json:"sourceApiAssociationStatus"`
		SourceApiAssociationStatusDetail string `json:"sourceApiAssociationStatusDetail"`
		LastSuccessfulMergeDate          int64  `json:"lastSuccessfulMergeDate"`
	} `json:"sourceApiAssociation"`
}

type appsyncEventsApiResponse struct {
	API struct {
		ApiID  string            `json:"apiId"`
		ApiArn string            `json:"apiArn"`
		Name   string            `json:"name"`
		Dns    map[string]string `json:"dns"`
	} `json:"api"`
}

type appsyncChannelNamespaceResponse struct {
	ChannelNamespace struct {
		ApiID               string `json:"apiId"`
		Name                string `json:"name"`
		ChannelNamespaceArn string `json:"channelNamespaceArn"`
	} `json:"channelNamespace"`
}

func appsyncEventsRESTJSON(ctx context.Context, router http.Handler, region, method, path, opName string, body map[string]any, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal request: %w", opName, err)
		}
	}
	rec, err := internalAppSyncEventsRequest(ctx, router, region, method, path, "application/json", data)
	if err != nil {
		return fmt.Errorf("%s: %w", opName, err)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			return fmt.Errorf("%s: parse response: %w", opName, err)
		}
	}
	return nil
}

// internalAppSyncEventsRequest dispatches an AppSync Events request. It differs
// from a bare internalRequest only by the SigV4 scope header the router needs
// to see to claim the request for AppSync rather than fall through to S3.
//
// It used to build and dispatch its own request, which meant every AppSync
// Events call CloudFormation made was invisible in a trace and linked to no
// parent. Going through internalRequest is what fixes that.
func internalAppSyncEventsRequest(ctx context.Context, router http.Handler, region, method, path, contentType string, body []byte) (*httptest.ResponseRecorder, error) {
	return internalRequest(ctx, router, region, method, path, contentType, body, http.Header{
		"Authorization": []string{"AWS4-HMAC-SHA256 Credential=overcast/20250101/" + region + "/appsync/aws4_request, SignedHeaders=host, Signature=overcast"},
	})
}

func appsyncRESTJSON(ctx context.Context, router http.Handler, region, method, path, opName string, body map[string]any, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal request: %w", opName, err)
		}
	}
	rec, err := internalRequest(ctx, router, region, method, path, "application/json", data)
	if err != nil {
		return fmt.Errorf("%s: %w", opName, err)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			return fmt.Errorf("%s: parse response: %w", opName, err)
		}
	}
	return nil
}

func appsyncSplitPhysicalID(resource, physicalID string, want int) ([]string, error) {
	parts := strings.SplitN(physicalID, "/", want)
	if len(parts) != want {
		return nil, fmt.Errorf("%s: invalid physical ID %q", resource, physicalID)
	}
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("%s: invalid physical ID %q", resource, physicalID)
		}
	}
	return parts, nil
}

func appsyncGraphQLApiBody(props map[string]any) map[string]any {
	body := map[string]any{}
	copyStringProp(body, props, "Name", "name")
	copyStringProp(body, props, "AuthenticationType", "authenticationType")
	copyStringProp(body, props, "ApiType", "apiType")
	copyStringProp(body, props, "Visibility", "visibility")
	copyStringProp(body, props, "OwnerContact", "ownerContact")
	copyStringProp(body, props, "WafWebAclArn", "wafWebAclArn")
	copyStringProp(body, props, "MergedApiExecutionRoleArn", "mergedApiExecutionRoleArn")
	copyStringProp(body, props, "IntrospectionConfig", "introspectionConfig")
	copyAnyProp(body, props, "XrayEnabled", "xrayEnabled")
	copyAnyProp(body, props, "QueryDepthLimit", "queryDepthLimit")
	copyAnyProp(body, props, "ResolverCountLimit", "resolverCountLimit")
	copyAnyProp(body, props, "AdditionalAuthenticationProviders", "additionalAuthenticationProviders")
	copyAnyProp(body, props, "LogConfig", "logConfig")
	copyAnyProp(body, props, "UserPoolConfig", "userPoolConfig")
	copyAnyProp(body, props, "OpenIDConnectConfig", "openIDConnectConfig")
	copyAnyProp(body, props, "LambdaAuthorizerConfig", "lambdaAuthorizerConfig")
	copyAnyProp(body, props, "EnhancedMetricsConfig", "enhancedMetricsConfig")
	if tags := cfnTagListToMap(props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	return body
}

func cfnTagListToMap(raw any) map[string]string {
	if raw == nil {
		return nil
	}
	out := map[string]string{}
	add := func(key string, value any) {
		if key == "" {
			return
		}
		out[key] = fmt.Sprintf("%v", value)
	}
	switch tags := raw.(type) {
	case []any:
		for _, item := range tags {
			switch tag := item.(type) {
			case map[string]any:
				key, _ := tag["Key"].(string)
				add(key, tag["Value"])
			case Tag:
				add(tag.Key, tag.Value)
			}
		}
	case []Tag:
		for _, tag := range tags {
			add(tag.Key, tag.Value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func copyStringProp(dst map[string]any, props map[string]any, cfnName, jsonName string) {
	if v, _ := props[cfnName].(string); v != "" {
		dst[jsonName] = v
	}
}

func copyAnyProp(dst map[string]any, props map[string]any, cfnName, jsonName string) {
	if v, ok := props[cfnName]; ok {
		dst[jsonName] = v
	}
}

func copyConvertedCFProp(dst map[string]any, props map[string]any, cfnName, jsonName string) {
	if v, ok := props[cfnName]; ok {
		dst[jsonName] = convertCFKeysToAPI(v)
	}
}

func appsyncGraphQLApiAttrs(resp appsyncGraphQLApiResponse) map[string]string {
	api := resp.GraphqlApi
	return map[string]string{
		"Ref":                api.Arn,
		"ApiId":              api.ApiID,
		"Arn":                api.Arn,
		"GraphQLEndpointArn": api.Arn,
		"GraphQLUrl":         api.Uris["GRAPHQL"],
		"RealtimeUrl":        api.Uris["REALTIME"],
		"GraphQLDns":         api.Dns["GRAPHQL"],
		"RealtimeDns":        api.Dns["REALTIME"],
	}
}

func appsyncApiKeyAttrs(cfg *config.Config, region, apiID, keyID string) map[string]string {
	arn := protocol.ARN(region, cfg.AccountID, "appsync", fmt.Sprintf("apis/%s/apikeys/%s", apiID, keyID))
	return map[string]string{"Ref": arn, "Arn": arn, "ApiKey": keyID, "ApiKeyId": keyID}
}

func appsyncFunctionAttrs(resp appsyncFunctionResponse) map[string]string {
	fn := resp.FunctionConfiguration
	return map[string]string{
		"Ref":            fn.FunctionArn,
		"FunctionArn":    fn.FunctionArn,
		"FunctionId":     fn.FunctionID,
		"Name":           fn.Name,
		"DataSourceName": fn.DataSourceName,
	}
}

func appsyncDataSourceBody(props map[string]any) map[string]any {
	body := map[string]any{}
	copyStringProp(body, props, "Name", "name")
	copyStringProp(body, props, "Type", "type")
	copyStringProp(body, props, "ServiceRoleArn", "serviceRoleArn")
	copyStringProp(body, props, "Description", "description")
	copyAnyProp(body, props, "LambdaConfig", "lambdaConfig")
	copyAnyProp(body, props, "DynamoDBConfig", "dynamodbConfig")
	copyAnyProp(body, props, "HttpConfig", "httpConfig")
	copyAnyProp(body, props, "OpenSearchServiceConfig", "openSearchServiceConfig")
	copyAnyProp(body, props, "ElasticsearchConfig", "elasticsearchConfig")
	copyAnyProp(body, props, "RelationalDatabaseConfig", "relationalDatabaseConfig")
	copyAnyProp(body, props, "EventBridgeConfig", "eventBridgeConfig")
	copyAnyProp(body, props, "MetricsConfig", "metricsConfig")
	return body
}

func appsyncDataSourceAttrs(resp appsyncDataSourceResponse) map[string]string {
	ds := resp.DataSource
	return map[string]string{"Ref": ds.DataSourceArn, "DataSourceArn": ds.DataSourceArn, "Name": ds.Name}
}

func appsyncResolverBody(ctx context.Context, router http.Handler, region string, props map[string]any) (map[string]any, error) {
	body := map[string]any{}
	copyStringProp(body, props, "FieldName", "fieldName")
	copyStringProp(body, props, "DataSourceName", "dataSourceName")
	copyStringProp(body, props, "Kind", "kind")
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "RequestMappingTemplate", "RequestMappingTemplateS3Location", "requestMappingTemplate"); err != nil {
		return nil, fmt.Errorf("Resolver RequestMappingTemplateS3Location: %w", err)
	}
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "ResponseMappingTemplate", "ResponseMappingTemplateS3Location", "responseMappingTemplate"); err != nil {
		return nil, fmt.Errorf("Resolver ResponseMappingTemplateS3Location: %w", err)
	}
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "Code", "CodeS3Location", "code"); err != nil {
		return nil, fmt.Errorf("Resolver CodeS3Location: %w", err)
	}
	copyAnyProp(body, props, "PipelineConfig", "pipelineConfig")
	copyAnyProp(body, props, "Runtime", "runtime")
	copyAnyProp(body, props, "MaxBatchSize", "maxBatchSize")
	copyAnyProp(body, props, "SyncConfig", "syncConfig")
	copyAnyProp(body, props, "CachingConfig", "cachingConfig")
	copyAnyProp(body, props, "MetricsConfig", "metricsConfig")
	return body, nil
}

func appsyncResolverAttrs(resp appsyncResolverResponse) map[string]string {
	res := resp.Resolver
	return map[string]string{"Ref": res.ResolverArn, "ResolverArn": res.ResolverArn, "FieldName": res.FieldName, "TypeName": res.TypeName}
}

// ── AWS::AppSync::Api (Events API) ─────────────────────────────────────────

type appsyncEventsApiHandler struct{}

func (h *appsyncEventsApiHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	var resp appsyncEventsApiResponse
	if err := appsyncEventsRESTJSON(ctx, router, rCtx.Region, http.MethodPost, "/v2/apis", "CreateApi", appsyncEventsApiBody(props), &resp); err != nil {
		return "", nil, err
	}
	apiID := resp.API.ApiID
	return apiID, appsyncEventsApiAttrs(resp), nil
}

func (h *appsyncEventsApiHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalAppSyncEventsRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v2/apis/"+url.PathEscape(physicalID), "", nil)
	return teardownError("DeleteApi", rec, err)
}

func (h *appsyncEventsApiHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	var resp appsyncEventsApiResponse
	path := "/v2/apis/" + url.PathEscape(physicalID)
	if err := appsyncEventsRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateApi", appsyncEventsApiBody(props), &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncEventsApiAttrs(resp), nil
}

func appsyncEventsApiBody(props map[string]any) map[string]any {
	body := map[string]any{}
	copyStringProp(body, props, "Name", "name")
	copyStringProp(body, props, "OwnerContact", "ownerContact")
	copyStringProp(body, props, "WafWebAclArn", "wafWebAclArn")
	copyConvertedCFProp(body, props, "EventConfig", "eventConfig")
	copyAnyProp(body, props, "XrayEnabled", "xrayEnabled")
	if tags := cfnTagListToMap(props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	return body
}

func appsyncEventsApiAttrs(resp appsyncEventsApiResponse) map[string]string {
	api := resp.API
	attrs := map[string]string{
		"Ref":    api.ApiArn,
		"ApiId":  api.ApiID,
		"ApiArn": api.ApiArn,
		"Name":   api.Name,
	}
	if api.Dns != nil {
		attrs["Dns.Http"] = api.Dns["HTTP"]
		attrs["Dns.Realtime"] = api.Dns["REALTIME"]
	}
	return attrs
}

// ── AWS::AppSync::ChannelNamespace ─────────────────────────────────────────

type appsyncChannelNamespaceHandler struct{}

func (h *appsyncChannelNamespaceHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	name, _ := props["Name"].(string)
	if apiID == "" || name == "" {
		return "", nil, fmt.Errorf("ChannelNamespace: ApiId and Name are required")
	}
	path := fmt.Sprintf("/v2/apis/%s/channelNamespaces", url.PathEscape(apiID))
	var resp appsyncChannelNamespaceResponse
	body, err := appsyncChannelNamespaceBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	if err := appsyncEventsRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateChannelNamespace", body, &resp); err != nil {
		return "", nil, err
	}
	return apiID + "/" + name, appsyncChannelNamespaceAttrs(resp), nil
}

func (h *appsyncChannelNamespaceHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("ChannelNamespace", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v2/apis/%s/channelNamespaces/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	rec, err := internalAppSyncEventsRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteChannelNamespace", rec, err)
}

func (h *appsyncChannelNamespaceHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("ChannelNamespace", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	if name, _ := props["Name"].(string); name != "" && name != parts[1] {
		return "", nil, errReplacementRequired
	}
	path := fmt.Sprintf("/v2/apis/%s/channelNamespaces/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	var resp appsyncChannelNamespaceResponse
	body, err := appsyncChannelNamespaceBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	if err := appsyncEventsRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateChannelNamespace", body, &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncChannelNamespaceAttrs(resp), nil
}

func appsyncChannelNamespaceBody(ctx context.Context, router http.Handler, region string, props map[string]any) (map[string]any, error) {
	body := map[string]any{}
	copyStringProp(body, props, "Name", "name")
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "CodeHandlers", "CodeS3Location", "codeHandlers"); err != nil {
		return nil, fmt.Errorf("ChannelNamespace CodeS3Location: %w", err)
	}
	copyConvertedCFProp(body, props, "HandlerConfigs", "handlerConfigs")
	copyConvertedCFProp(body, props, "PublishAuthModes", "publishAuthModes")
	copyConvertedCFProp(body, props, "SubscribeAuthModes", "subscribeAuthModes")
	if tags := cfnTagListToMap(props["Tags"]); len(tags) > 0 {
		body["tags"] = tags
	}
	return body, nil
}

func appsyncChannelNamespaceAttrs(resp appsyncChannelNamespaceResponse) map[string]string {
	ns := resp.ChannelNamespace
	return map[string]string{
		"Ref":                 ns.ChannelNamespaceArn,
		"ApiId":               ns.ApiID,
		"Name":                ns.Name,
		"ChannelNamespaceArn": ns.ChannelNamespaceArn,
	}
}

func (h *appsyncGraphQLApiHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	var resp appsyncGraphQLApiResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, "/v1/apis", "CreateGraphqlApi", appsyncGraphQLApiBody(props), &resp); err != nil {
		return "", nil, err
	}

	apiID := resp.GraphqlApi.ApiID
	if err := appsyncPutEnvironmentVariables(ctx, router, rCtx.Region, apiID, props); err != nil {
		return "", nil, err
	}
	return apiID, appsyncGraphQLApiAttrs(resp), nil
}

func (h *appsyncGraphQLApiHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/apis/"+physicalID, "", nil)
	return teardownError("DeleteGraphqlApi", rec, err)
}

func (h *appsyncGraphQLApiHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if oldProps != nil {
		if newType, _ := props["ApiType"].(string); newType != "" {
			if oldType, _ := oldProps["ApiType"].(string); oldType != "" && newType != oldType {
				return "", nil, errReplacementRequired
			}
		}
	}

	var resp appsyncGraphQLApiResponse
	path := "/v1/apis/" + url.PathEscape(physicalID)
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateGraphqlApi", appsyncGraphQLApiBody(props), &resp); err != nil {
		return "", nil, err
	}
	if err := appsyncPutEnvironmentVariables(ctx, router, rCtx.Region, physicalID, props); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncGraphQLApiAttrs(resp), nil
}

func appsyncPutEnvironmentVariables(ctx context.Context, router http.Handler, region, apiID string, props map[string]any) error {
	raw, ok := props["EnvironmentVariables"]
	if !ok {
		return nil
	}
	vars, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	converted := make(map[string]string, len(vars))
	for key, value := range vars {
		converted[key] = fmt.Sprintf("%v", value)
	}
	path := fmt.Sprintf("/v1/apis/%s/environmentVariables", url.PathEscape(apiID))
	return appsyncRESTJSON(ctx, router, region, http.MethodPut, path, "PutGraphqlApiEnvironmentVariables", map[string]any{"environmentVariables": converted}, nil)
}

// ── AWS::AppSync::GraphQLSchema ────────────────────────────────────────────

type appsyncGraphQLSchemaHandler struct{}

func (h *appsyncGraphQLSchemaHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	definition, _ := props["Definition"].(string)
	if apiID == "" {
		return "", nil, fmt.Errorf("GraphQLSchema: ApiId is required")
	}
	if definition == "" {
		if location, _ := props["DefinitionS3Location"].(string); location != "" {
			fetched, err := appsyncFetchS3BackedProperty(ctx, router, rCtx.Region, location)
			if err != nil {
				return "", nil, fmt.Errorf("GraphQLSchema DefinitionS3Location: %w", err)
			}
			definition = fetched
		}
	}
	if definition == "" {
		return "", nil, fmt.Errorf("GraphQLSchema: Definition is required")
	}
	body := map[string]any{"definition": base64.StdEncoding.EncodeToString([]byte(definition))}
	path := fmt.Sprintf("/v1/apis/%s/schemacreation", url.PathEscape(apiID))
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "StartSchemaCreation", body, nil); err != nil {
		return "", nil, err
	}
	physicalID := apiID + "/schema"
	return physicalID, map[string]string{
		"Ref": apiID + "GraphQLSchema",
		"Id":  physicalID,
	}, nil
}

func (h *appsyncGraphQLSchemaHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	return nil
}

func (h *appsyncGraphQLSchemaHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("GraphQLSchema", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	return h.Create(ctx, router, cfg, props, rCtx)
}

// ── AWS::AppSync::ApiKey ───────────────────────────────────────────────────

type appsyncApiKeyHandler struct{}

func (h *appsyncApiKeyHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	if apiID == "" {
		return "", nil, fmt.Errorf("ApiKey: ApiId is required")
	}
	body := map[string]any{}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	if v, ok := props["Expires"]; ok {
		body["expires"] = v
	}
	path := fmt.Sprintf("/v1/apis/%s/apikeys", url.PathEscape(apiID))
	var resp appsyncApiKeyResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateApiKey", body, &resp); err != nil {
		return "", nil, err
	}
	keyID := resp.ApiKey.ID
	return apiID + "/" + keyID, appsyncApiKeyAttrs(cfg, rCtx.Region, apiID, keyID), nil
}

func (h *appsyncApiKeyHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("ApiKey", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/apis/%s/apikeys/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteApiKey", rec, err)
}

func (h *appsyncApiKeyHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("ApiKey", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	body := map[string]any{}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	if v, ok := props["Expires"]; ok {
		body["expires"] = v
	}
	path := fmt.Sprintf("/v1/apis/%s/apikeys/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateApiKey", body, nil); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncApiKeyAttrs(cfg, rCtx.Region, parts[0], parts[1]), nil
}

// ── AWS::AppSync::FunctionConfiguration ───────────────────────────────────

type appsyncFunctionConfigurationHandler struct{}

func (h *appsyncFunctionConfigurationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	if apiID == "" {
		return "", nil, fmt.Errorf("FunctionConfiguration: ApiId is required")
	}
	body, err := appsyncFunctionBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	path := fmt.Sprintf("/v1/apis/%s/functions", url.PathEscape(apiID))
	var resp appsyncFunctionResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateFunction", body, &resp); err != nil {
		return "", nil, err
	}
	fn := resp.FunctionConfiguration
	return apiID + "/" + fn.FunctionID, appsyncFunctionAttrs(resp), nil
}

func (h *appsyncFunctionConfigurationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("FunctionConfiguration", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/apis/%s/functions/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteFunction", rec, err)
}

func (h *appsyncFunctionConfigurationHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("FunctionConfiguration", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	body, err := appsyncFunctionBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	path := fmt.Sprintf("/v1/apis/%s/functions/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	var resp appsyncFunctionResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateFunction", body, &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncFunctionAttrs(resp), nil
}

func appsyncFunctionBody(ctx context.Context, router http.Handler, region string, props map[string]any) (map[string]any, error) {
	body := map[string]any{}
	if v, _ := props["Name"].(string); v != "" {
		body["name"] = v
	}
	if v, _ := props["DataSourceName"].(string); v != "" {
		body["dataSourceName"] = v
	}
	if v, _ := props["Description"].(string); v != "" {
		body["description"] = v
	}
	if v, _ := props["FunctionVersion"].(string); v != "" {
		body["functionVersion"] = v
	}
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "RequestMappingTemplate", "RequestMappingTemplateS3Location", "requestMappingTemplate"); err != nil {
		return nil, fmt.Errorf("FunctionConfiguration RequestMappingTemplateS3Location: %w", err)
	}
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "ResponseMappingTemplate", "ResponseMappingTemplateS3Location", "responseMappingTemplate"); err != nil {
		return nil, fmt.Errorf("FunctionConfiguration ResponseMappingTemplateS3Location: %w", err)
	}
	if v, ok := props["MaxBatchSize"]; ok {
		body["maxBatchSize"] = v
	}
	if err := copyStringOrS3Prop(ctx, router, region, body, props, "Code", "CodeS3Location", "code"); err != nil {
		return nil, fmt.Errorf("FunctionConfiguration CodeS3Location: %w", err)
	}
	if v, ok := props["Runtime"]; ok {
		body["runtime"] = v
	}
	if v, ok := props["SyncConfig"]; ok {
		body["syncConfig"] = v
	}
	return body, nil
}

func copyStringOrS3Prop(ctx context.Context, router http.Handler, region string, dst map[string]any, props map[string]any, inlineName, s3Name, jsonName string) error {
	if v, _ := props[inlineName].(string); v != "" {
		dst[jsonName] = v
		return nil
	}
	location, _ := props[s3Name].(string)
	if location == "" {
		return nil
	}
	fetched, err := appsyncFetchS3BackedProperty(ctx, router, region, location)
	if err != nil {
		return err
	}
	dst[jsonName] = fetched
	return nil
}

func appsyncFetchS3BackedProperty(ctx context.Context, router http.Handler, region, location string) (string, error) {
	u, err := url.Parse(location)
	if err != nil {
		return "", fmt.Errorf("parse location %q: %w", location, err)
	}
	path := u.Path
	if u.Scheme == "s3" {
		path = "/" + u.Host + u.Path
	}
	if path == "" || path == "/" {
		return "", fmt.Errorf("invalid S3 location %q", location)
	}
	rec, err := internalRequest(ctx, router, region, http.MethodGet, path, "", nil)
	if err != nil {
		return "", err
	}
	return rec.Body.String(), nil
}

// ── AWS::AppSync::DataSource ──────────────────────────────────────────────

type appsyncDataSourceHandler struct{}

func (h *appsyncDataSourceHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	if apiID == "" {
		return "", nil, fmt.Errorf("DataSource: ApiId is required")
	}
	name, _ := props["Name"].(string)
	path := fmt.Sprintf("/v1/apis/%s/datasources", url.PathEscape(apiID))
	var resp appsyncDataSourceResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateDataSource", appsyncDataSourceBody(props), &resp); err != nil {
		return "", nil, err
	}
	return apiID + "/" + name, appsyncDataSourceAttrs(resp), nil
}

func (h *appsyncDataSourceHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("DataSource", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/apis/%s/datasources/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteDataSource", rec, err)
}

func (h *appsyncDataSourceHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("DataSource", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	if name, _ := props["Name"].(string); name != "" && name != parts[1] {
		return "", nil, errReplacementRequired
	}
	path := fmt.Sprintf("/v1/apis/%s/datasources/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	var resp appsyncDataSourceResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateDataSource", appsyncDataSourceBody(props), &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncDataSourceAttrs(resp), nil
}

// ── AWS::AppSync::Resolver ────────────────────────────────────────────────

type appsyncResolverHandler struct{}

func (h *appsyncResolverHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	typeName, _ := props["TypeName"].(string)
	fieldName, _ := props["FieldName"].(string)
	if apiID == "" || typeName == "" || fieldName == "" {
		return "", nil, fmt.Errorf("Resolver: ApiId, TypeName, and FieldName are required")
	}
	body, err := appsyncResolverBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	path := fmt.Sprintf("/v1/apis/%s/types/%s/resolvers", url.PathEscape(apiID), url.PathEscape(typeName))
	var resp appsyncResolverResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateResolver", body, &resp); err != nil {
		return "", nil, err
	}
	return apiID + "/" + typeName + "/" + fieldName, appsyncResolverAttrs(resp), nil
}

func (h *appsyncResolverHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("Resolver", physicalID, 3)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/apis/%s/types/%s/resolvers/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(parts[2]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DeleteResolver", rec, err)
}

func (h *appsyncResolverHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("Resolver", physicalID, 3)
	if err != nil {
		return "", nil, err
	}
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != parts[0] {
		return "", nil, errReplacementRequired
	}
	if typeName, _ := props["TypeName"].(string); typeName != "" && typeName != parts[1] {
		return "", nil, errReplacementRequired
	}
	if fieldName, _ := props["FieldName"].(string); fieldName != "" && fieldName != parts[2] {
		return "", nil, errReplacementRequired
	}
	body, err := appsyncResolverBody(ctx, router, rCtx.Region, props)
	if err != nil {
		return "", nil, err
	}
	path := fmt.Sprintf("/v1/apis/%s/types/%s/resolvers/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(parts[2]))
	var resp appsyncResolverResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateResolver", body, &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncResolverAttrs(resp), nil
}

// ── AWS::AppSync::DomainName ───────────────────────────────────────────────

type appsyncDomainNameHandler struct{}

func (h *appsyncDomainNameHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	domainName, _ := props["DomainName"].(string)
	if domainName == "" {
		return "", nil, fmt.Errorf("DomainName: DomainName is required")
	}
	body := map[string]any{}
	copyStringProp(body, props, "DomainName", "domainName")
	copyStringProp(body, props, "CertificateArn", "certificateArn")
	copyStringProp(body, props, "Description", "description")
	var resp appsyncDomainNameResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, "/v1/domainnames", "CreateDomainName", body, &resp); err != nil {
		return "", nil, err
	}
	attrs := appsyncDomainNameAttrs(cfg, rCtx.Region, resp)
	return domainName, attrs, nil
}

func (h *appsyncDomainNameHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/domainnames/"+url.PathEscape(physicalID), "", nil)
	return teardownError("DeleteDomainName", rec, err)
}

func (h *appsyncDomainNameHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if domainName, _ := props["DomainName"].(string); domainName != "" && domainName != physicalID {
		return "", nil, errReplacementRequired
	}
	body := map[string]any{}
	copyStringProp(body, props, "Description", "description")
	var resp appsyncDomainNameResponse
	path := "/v1/domainnames/" + url.PathEscape(physicalID)
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateDomainName", body, &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncDomainNameAttrs(cfg, rCtx.Region, resp), nil
}

func appsyncDomainNameAttrs(cfg *config.Config, region string, resp appsyncDomainNameResponse) map[string]string {
	dn := resp.DomainNameConfig
	arn := protocol.ARN(region, cfg.AccountID, "appsync", "domainnames/"+dn.DomainName)
	return map[string]string{
		"Ref":               dn.DomainName,
		"DomainName":        dn.DomainName,
		"DomainNameArn":     arn,
		"AppSyncDomainName": dn.AppsyncDomainName,
		"AppsyncDomainName": dn.AppsyncDomainName,
		"HostedZoneId":      dn.HostedZoneID,
	}
}

// ── AWS::AppSync::DomainNameApiAssociation ─────────────────────────────────

type appsyncDomainNameApiAssociationHandler struct{}

func (h *appsyncDomainNameApiAssociationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	domainName, _ := props["DomainName"].(string)
	apiID, _ := props["ApiId"].(string)
	if domainName == "" || apiID == "" {
		return "", nil, fmt.Errorf("DomainNameApiAssociation: DomainName and ApiId are required")
	}
	path := fmt.Sprintf("/v1/domainnames/%s/apiassociation", url.PathEscape(domainName))
	var resp appsyncApiAssociationResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "AssociateApi", map[string]any{"apiId": apiID}, &resp); err != nil {
		return "", nil, err
	}
	physicalID := domainName + "/" + apiID
	return physicalID, map[string]string{"Ref": physicalID, "DomainName": resp.ApiAssociation.DomainName, "ApiId": resp.ApiAssociation.ApiID, "AssociationStatus": resp.ApiAssociation.AssociationStatus}, nil
}

func (h *appsyncDomainNameApiAssociationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("DomainNameApiAssociation", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/domainnames/%s/apiassociation", url.PathEscape(parts[0]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DisassociateApi", rec, err)
}

func (h *appsyncDomainNameApiAssociationHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	parts, err := appsyncSplitPhysicalID("DomainNameApiAssociation", physicalID, 2)
	if err != nil {
		return "", nil, err
	}
	if domainName, _ := props["DomainName"].(string); domainName != "" && domainName != parts[0] {
		return "", nil, errReplacementRequired
	}
	return h.Create(ctx, router, cfg, props, rCtx)
}

// ── AWS::AppSync::ApiCache ─────────────────────────────────────────────────

type appsyncApiCacheHandler struct{}

func (h *appsyncApiCacheHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	apiID, _ := props["ApiId"].(string)
	if apiID == "" {
		return "", nil, fmt.Errorf("ApiCache: ApiId is required")
	}
	path := fmt.Sprintf("/v1/apis/%s/ApiCaches", url.PathEscape(apiID))
	var resp appsyncApiCacheResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "CreateApiCache", appsyncApiCacheBody(props), &resp); err != nil {
		return "", nil, err
	}
	return apiID, appsyncApiCacheAttrs(apiID, resp), nil
}

func (h *appsyncApiCacheHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, "/v1/apis/"+url.PathEscape(physicalID)+"/ApiCaches", "", nil)
	return teardownError("DeleteApiCache", rec, err)
}

func (h *appsyncApiCacheHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if apiID, _ := props["ApiId"].(string); apiID != "" && apiID != physicalID {
		return "", nil, errReplacementRequired
	}
	path := fmt.Sprintf("/v1/apis/%s/ApiCaches/update", url.PathEscape(physicalID))
	var resp appsyncApiCacheResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "UpdateApiCache", appsyncApiCacheBody(props), &resp); err != nil {
		return "", nil, err
	}
	return physicalID, appsyncApiCacheAttrs(physicalID, resp), nil
}

func appsyncApiCacheBody(props map[string]any) map[string]any {
	body := map[string]any{}
	copyStringProp(body, props, "Type", "type")
	copyStringProp(body, props, "ApiCachingBehavior", "apiCachingBehavior")
	copyStringProp(body, props, "HealthMetricsConfig", "healthMetricsConfig")
	copyAnyProp(body, props, "Ttl", "ttl")
	copyAnyProp(body, props, "TransitEncryptionEnabled", "transitEncryptionEnabled")
	copyAnyProp(body, props, "AtRestEncryptionEnabled", "atRestEncryptionEnabled")
	return body
}

func appsyncApiCacheAttrs(apiID string, resp appsyncApiCacheResponse) map[string]string {
	cache := resp.ApiCache
	return map[string]string{"Ref": apiID, "ApiId": apiID, "Status": cache.Status, "Type": cache.Type, "ApiCachingBehavior": cache.ApiCachingBehavior}
}

// ── AWS::AppSync::SourceApiAssociation ─────────────────────────────────────

type appsyncSourceApiAssociationHandler struct{}

func (h *appsyncSourceApiAssociationHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	mergedAPI, _ := props["MergedApiIdentifier"].(string)
	sourceAPI, _ := props["SourceApiIdentifier"].(string)
	if mergedAPI == "" || sourceAPI == "" {
		return "", nil, fmt.Errorf("SourceApiAssociation: MergedApiIdentifier and SourceApiIdentifier are required")
	}
	body := map[string]any{"sourceApiIdentifier": sourceAPI}
	copyStringProp(body, props, "Description", "description")
	copyAnyProp(body, props, "SourceApiAssociationConfig", "sourceApiAssociationConfig")
	path := fmt.Sprintf("/v1/mergedApis/%s/sourceApiAssociations", url.PathEscape(mergedAPI))
	var resp appsyncSourceApiAssociationResponse
	if err := appsyncRESTJSON(ctx, router, rCtx.Region, http.MethodPost, path, "AssociateSourceGraphqlApi", body, &resp); err != nil {
		return "", nil, err
	}
	assoc := resp.SourceApiAssociation
	return mergedAPI + "/" + assoc.AssociationID, appsyncSourceApiAssociationAttrs(resp), nil
}

func (h *appsyncSourceApiAssociationHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	parts, err := appsyncSplitPhysicalID("SourceApiAssociation", physicalID, 2)
	if err != nil {
		return nil
	}
	path := fmt.Sprintf("/v1/mergedApis/%s/sourceApiAssociations/%s", url.PathEscape(parts[0]), url.PathEscape(parts[1]))
	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodDelete, path, "", nil)
	return teardownError("DisassociateSourceGraphqlApi", rec, err)
}

func (h *appsyncSourceApiAssociationHandler) Update(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

func appsyncSourceApiAssociationAttrs(resp appsyncSourceApiAssociationResponse) map[string]string {
	assoc := resp.SourceApiAssociation
	return map[string]string{
		"Ref":                              assoc.AssociationArn,
		"AssociationArn":                   assoc.AssociationArn,
		"AssociationId":                    assoc.AssociationID,
		"SourceApiId":                      assoc.SourceApiID,
		"SourceApiArn":                     assoc.SourceApiArn,
		"MergedApiId":                      assoc.MergedApiID,
		"MergedApiArn":                     assoc.MergedApiArn,
		"SourceApiAssociationStatus":       assoc.SourceApiAssociationStatus,
		"SourceApiAssociationStatusDetail": assoc.SourceApiAssociationStatusDetail,
		"LastSuccessfulMergeDate":          strconv.FormatInt(assoc.LastSuccessfulMergeDate, 10),
	}
}

// ── AWS::CloudFront::Distribution ─────────────────────────────────────────

type cloudfrontDistributionHandler struct{}

func (h *cloudfrontDistributionHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	distConfig, _ := props["DistributionConfig"].(map[string]any)
	if distConfig == nil {
		distConfig = props // Some templates put config at the top level.
	}

	if _, ok := distConfig["CallerReference"].(string); !ok {
		distConfig["CallerReference"] = fmt.Sprintf("%s-%d", rCtx.StackName, len(rCtx.Resources))
	}
	if _, ok := distConfig["Enabled"].(bool); !ok {
		distConfig["Enabled"] = true
	}
	ensureCloudFrontDistributionDefaults(distConfig)

	xmlData, err := marshalCloudFrontDistributionConfigXML(distConfig)
	if err != nil {
		return "", nil, fmt.Errorf("CloudFront: marshal config: %w", err)
	}

	rec, err := internalRequest(ctx, router, rCtx.Region, http.MethodPost, "/2020-05-31/distribution", "application/xml", xmlData)
	if err != nil {
		return "", nil, fmt.Errorf("CreateDistribution: %w", err)
	}

	body := rec.Body.String()
	id := extractXMLValue(body, "Id")
	domainName := extractXMLValue(body, "DomainName")

	attrs := map[string]string{
		"DomainName": domainName,
		"Id":         id,
	}
	return id, attrs, nil
}

func ensureCloudFrontDistributionDefaults(distConfig map[string]any) {
	origins := cloudFrontListItems(distConfig["Origins"])
	if len(origins) == 0 {
		distConfig["Origins"] = []any{map[string]any{"Id": "default", "DomainName": "localhost"}}
		origins = cloudFrontListItems(distConfig["Origins"])
	}
	dcb, _ := distConfig["DefaultCacheBehavior"].(map[string]any)
	if dcb == nil {
		dcb = map[string]any{}
		distConfig["DefaultCacheBehavior"] = dcb
	}
	if _, ok := dcb["ViewerProtocolPolicy"].(string); !ok {
		dcb["ViewerProtocolPolicy"] = "allow-all"
	}
	if target, _ := dcb["TargetOriginId"].(string); target == "" && len(origins) > 0 {
		if first, _ := origins[0].(map[string]any); first != nil {
			dcb["TargetOriginId"], _ = first["Id"].(string)
		}
	}
}

func marshalCloudFrontDistributionConfigXML(distConfig map[string]any) ([]byte, error) {
	return marshalCFNXML("DistributionConfig", distConfig, cloudFrontTopLevelList, cloudFrontItemName, cfnXMLItemsWrapper)
}

func cloudFrontTopLevelList(name string, value any) ([]any, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	switch name {
	case "Aliases", "CacheBehaviors", "CustomErrorResponses", "Origins", "OriginGroups":
		return items, true
	}
	return nil, false
}

func cloudFrontListItems(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case map[string]any:
		if items, ok := v["Items"].([]any); ok {
			return items
		}
	}
	return nil
}

func cloudFrontItemName(parent string) string {
	switch parent {
	case "Aliases":
		return "CNAME"
	case "AllowedMethods", "CachedMethods":
		return "Method"
	case "CacheBehaviors":
		return "CacheBehavior"
	case "CustomErrorResponses":
		return "CustomErrorResponse"
	case "CustomHeaders":
		return "OriginCustomHeader"
	case "FunctionAssociations":
		return "FunctionAssociation"
	case "GeoRestriction":
		return "Location"
	case "LambdaFunctionAssociations":
		return "LambdaFunctionAssociation"
	case "OriginGroups":
		return "OriginGroup"
	case "Origins":
		return "Origin"
	case "Members":
		return "OriginGroupMember"
	case "StatusCodes":
		return "StatusCode"
	}
	return "Item"
}

func (h *cloudfrontDistributionHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	basePath := "/2020-05-31/distribution/" + physicalID

	// Step 1: GET distribution to get current ETag. A distribution that is
	// already gone is a finished teardown; a read that failed for any other
	// reason is not, and reporting it as one would claim a delete this never
	// went on to make.
	rec, err := cfInternalRequest(ctx, router, rCtx.Region, http.MethodGet, basePath, "", nil, nil)
	if err != nil {
		return teardownError("GetDistribution", rec, err)
	}
	etag := rec.Header().Get("ETag")

	// Step 2: If distribution is enabled, disable it first.
	//
	// "Is it enabled" reads the distribution's own DistributionConfig/Enabled
	// rather than looking for the first "<Enabled>true</Enabled>" anywhere in
	// the document: Logging, TrustedSigners, TrustedKeyGroups and OriginShield
	// each carry an Enabled of their own.
	var dist struct {
		Config struct {
			Enabled bool `xml:"Enabled"`
		} `xml:"DistributionConfig"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &dist); err != nil {
		return fmt.Errorf("GetDistribution: parse response: %w", err)
	}
	if dist.Config.Enabled {
		// Get config to modify.
		cfgRec, err := cfInternalRequest(ctx, router, rCtx.Region, http.MethodGet, basePath+"/config", "", nil, nil)
		if err != nil {
			return fmt.Errorf("GetDistributionConfig: %w", err)
		}
		cfgEtag := cfgRec.Header().Get("ETag")

		cfgBody, err := setCloudFrontConfigEnabled(cfgRec.Body.Bytes(), false)
		if err != nil {
			return fmt.Errorf("UpdateDistribution (disable): rewrite config: %w", err)
		}

		// PUT updated config.
		putRec, err := cfInternalRequest(ctx, router, rCtx.Region, http.MethodPut, basePath+"/config", "application/xml", cfgBody, map[string]string{"If-Match": cfgEtag})
		if err != nil {
			return fmt.Errorf("UpdateDistribution (disable): %w", err)
		}
		etag = putRec.Header().Get("ETag")
	}

	// Step 3: DELETE with If-Match.
	delRec, err := cfInternalRequest(ctx, router, rCtx.Region, http.MethodDelete, basePath, "", nil, map[string]string{"If-Match": etag})
	return teardownError("DeleteDistribution", delRec, err)
}

func (h *cloudfrontDistributionHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	return "", nil, errReplacementRequired
}

// setCloudFrontConfigEnabled rewrites a DistributionConfig document's own
// <Enabled> element — the one directly under the root — and leaves the
// Enabled elements nested inside Logging, TrustedSigners, TrustedKeyGroups
// and OriginShield alone. Everything else is re-emitted unchanged, so the
// result is the config the caller just read, with one value flipped.
//
// It replaced a strings.Replace of the first "<Enabled>true</Enabled>", which
// was only ever right because DistributionConfig happened to emit its own
// Enabled third. #2010 put its members in AWS's modeled order, where Logging
// and DefaultCacheBehavior come first.
func setCloudFrontConfigEnabled(doc []byte, enabled bool) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	depth := 0
	inTopLevelEnabled := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		// The decoder resolves the document's default xmlns onto every
		// element's Name.Space, and the encoder would then re-declare it on
		// each one. Clearing Space leaves the root's own xmlns attribute —
		// which the decoder keeps in Attr — as the single declaration.
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			inTopLevelEnabled = depth == 2 && t.Name.Local == "Enabled"
			t.Name.Space = ""
			tok = t
		case xml.EndElement:
			depth--
			inTopLevelEnabled = false
			t.Name.Space = ""
			tok = t
		case xml.CharData:
			if inTopLevelEnabled {
				tok = xml.CharData(strconv.FormatBool(enabled))
			}
		}
		if err := enc.EncodeToken(tok); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cfInternalRequest dispatches a CloudFront request carrying the If-Match /
// ETag headers its disable-then-delete flow requires.
//
// It used to build and dispatch its own request — a second copy of
// internalRequest that predated hop recording and never gained it, so every
// CloudFront distribution call was missing from the trace. internalRequest
// already accepts extra headers; this is now only the map-to-Header adapter.
func cfInternalRequest(ctx context.Context, router http.Handler, region, method, path, contentType string, body []byte, headers map[string]string) (*httptest.ResponseRecorder, error) {
	extra := make(http.Header, len(headers))
	for k, v := range headers {
		extra.Set(k, v)
	}
	return internalRequest(ctx, router, region, method, path, contentType, body, extra)
}

// ── AWS::SES::Template ────────────────────────────────────────────────────

type sesTemplateHandler struct{}

func (h *sesTemplateHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// CloudFormation's AWS::SES::Template wraps the template inside a
	// "Template" property.
	tmpl, _ := props["Template"].(map[string]any)
	if tmpl == nil {
		tmpl = props
	}

	name, _ := tmpl["TemplateName"].(string)
	if name == "" {
		name = rCtx.generatedNameWithin(maxNameLenSES)
	}

	params := map[string]string{
		"Action":                "CreateTemplate",
		"Template.TemplateName": name,
	}
	if v, _ := tmpl["SubjectPart"].(string); v != "" {
		params["Template.SubjectPart"] = v
	}
	if v, _ := tmpl["TextPart"].(string); v != "" {
		params["Template.TextPart"] = v
	}
	if v, _ := tmpl["HtmlPart"].(string); v != "" {
		params["Template.HtmlPart"] = v
	}

	_, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateTemplate: %w", err)
	}

	attrs := map[string]string{
		"Id": name,
	}
	return name, attrs, nil
}

func (h *sesTemplateHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":       "DeleteTemplate",
		"TemplateName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteTemplate", rec, err)
}

// ── Helpers ───────────────────────────────────────────────────────────────

// ── ElastiCache stabilization ──────────────────────────────────────────────
//
// CreateCacheCluster, CreateReplicationGroup and CreateServerlessCache are all
// asynchronous: each answers with the cache in "creating" and the engine comes
// up behind it, which against a real Redis container is an image pull plus a
// TCP health check. CloudFormation does not pass that on — an
// AWS::ElastiCache::CacheCluster is not CREATE_COMPLETE until the cluster is
// "available" — and the handlers below export ConfigurationEndpoint.Address out
// of the create response, so the endpoint a dependent resource reads with a
// GetAtt was known long before anything was listening on it.
//
// The statuses are AWS's, from the API reference: CacheCluster's
// CacheClusterStatus is "available, creating, deleted, deleting,
// incompatible-network, modifying, rebooting cluster nodes, restore-failed,
// snapshotting"; a ReplicationGroup's Status is "creating, available,
// modifying, deleting, create-failed, snapshotting"; a ServerlessCache's is
// "CREATING, AVAILABLE, DELETING, CREATE-FAILED and MODIFYING".
//
// The three do not share a vocabulary, and assuming they did was a real mistake
// here: a cache cluster has no "create-failed" status at all, so classifying
// one would have been inventing a status AWS does not define. Each resource is
// therefore read against its own set, taken from AWS's own machine-readable
// answer to "what does a client wait for" — botocore's
// elasticache/2015-02-02/waiters-2.json — with the API reference filling the
// gaps a waiter leaves.
//
// None of the three carries a reason on the wire: ElastiCache models no
// equivalent of an RDS instance event. A failure here therefore reports the
// status the cache reached and what it was being waited for, and nothing more.

// elastiCacheStabilizeTimeout bounds the wait for a cache to come up. AWS's own
// waiters allow ten minutes (15s × 40 attempts) for both a cache cluster and a
// replication group; this is half as much again, because Overcast's cache has
// to pull an image before the engine being waited on exists at all. A cache
// that has given up says so in its status, so this only bites on one that is
// genuinely wedged.
const elastiCacheStabilizeTimeout = 15 * time.Minute

// elastiCacheClusterStatuses is the CacheClusterAvailable waiter's acceptors,
// verbatim: success on "available", failure on the four below.
var elastiCacheClusterStatuses = statusVocabulary{
	ready:  []string{"available"},
	failed: []string{"deleted", "deleting", "incompatible-network", "restore-failed"},
}

// elastiCacheReplicationGroupStatuses is the ReplicationGroupAvailable waiter's
// acceptors — success on "available", failure on "deleted" — plus the
// "create-failed" that API_ReplicationGroup documents and the waiter predates.
// A group that has already given up is what Overcast reports when an engine
// never answers, and following the waiter alone would leave it looking like one
// still working: the stack would spend its whole budget and then blame a
// timeout for a failure the group had already named.
var elastiCacheReplicationGroupStatuses = statusVocabulary{
	ready:  []string{"available"},
	failed: []string{"deleted", "create-failed"},
}

// elastiCacheServerlessStatuses is API_ServerlessCache's documented set; there
// is no waiter for a serverless cache. AWS documents these in upper case and
// Overcast writes them lower — which is one of the reasons the match folds case.
var elastiCacheServerlessStatuses = statusVocabulary{
	ready:  []string{"available"},
	failed: []string{"create-failed", "deleting"},
}

// describedElastiCaches is the projection a cache status poll reads. All three
// describes decode through it: a response carries exactly one of the element
// paths, and encoding/xml leaves the others empty.
type describedElastiCaches struct {
	Clusters []struct {
		Status string `xml:"CacheClusterStatus"`
	} `xml:"DescribeCacheClustersResult>CacheClusters>CacheCluster"`
	ReplicationGroups []struct {
		Status string `xml:"Status"`
	} `xml:"DescribeReplicationGroupsResult>ReplicationGroups>ReplicationGroup"`
	ServerlessCaches []struct {
		Status string `xml:"Status"`
	} `xml:"DescribeServerlessCachesResult>ServerlessCaches>ServerlessCache"`
}

// describeElastiCaches runs one ElastiCache describe and decodes it. The three
// calls differ in nothing but the action and the name of the identifier.
func describeElastiCaches(ctx context.Context, router http.Handler, region, action, idParam, id string) (describedElastiCaches, error) {
	var decoded describedElastiCaches
	rec, err := internalQuery(ctx, router, region, map[string]string{
		"Action":  action,
		"Version": "2015-02-02",
		idParam:   id,
	})
	if err != nil {
		return decoded, fmt.Errorf("%s: %w", action, err)
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		return decoded, fmt.Errorf("%s: parse response: %w", action, err)
	}
	return decoded, nil
}

// elastiCacheWait builds the wait for one cache resource. statusOf pulls the
// status out of whichever of the three describe shapes was asked for, and
// reports false when the cache is not in the answer at all — deleted from under
// the stack, which ends the wait rather than being polled over.
func elastiCacheWait(router http.Handler, region, subject, action, idParam, id string,
	statuses statusVocabulary, statusOf func(describedElastiCaches) (string, bool),
) stabilizeWait {
	return stabilizeWait{
		subject:  subject,
		goal:     "become available",
		timeout:  elastiCacheStabilizeTimeout,
		statuses: statuses,
		describe: func(ctx context.Context) (string, string, error) {
			decoded, err := describeElastiCaches(ctx, router, region, action, idParam, id)
			if err != nil {
				return "", "", err
			}
			status, found := statusOf(decoded)
			if !found {
				return "", "", fmt.Errorf("%s no longer exists", subject)
			}
			// ElastiCache models no per-cache failure reason — there is no
			// DescribeEvents equivalent here — so the status the cache reached
			// is the whole of what can be reported.
			return status, "", nil
		},
	}
}

// setEndpointAttrs writes one address/port pair into attrs as the Fn::GetAtt
// attributes of prefix — `RedisEndpoint.Address`, `PrimaryEndPoint.Port`, and
// so on.
//
// Write every prefix the resource documents, including one this resource does
// not populate: an attribute left out of the map does not resolve to the empty
// string, it falls through to resolveGetAtt's physical-ID fallback. That is how
// a bare cluster ID reaches a template as a hostname, and nothing resolves it —
// an ECS task with REDIS_HOST baked in at deploy time above all.
func setEndpointAttrs(attrs map[string]string, prefix, address, port string) {
	attrs[prefix+".Address"] = address
	attrs[prefix+".Port"] = port
}

// ── AWS::ElastiCache::CacheCluster ───────────────────────────────────────────

type elastiCacheCacheClusterHandler struct{}

func (h *elastiCacheCacheClusterHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// ClusterName, not CacheClusterId: the latter is what the *API* calls the
	// parameter, and the resource has no property by that name at all. Reading
	// it meant a template that named its cluster got the generated name below
	// instead, so every endpoint the stack advertised was for a cluster nobody
	// had asked for.
	id, _ := props["ClusterName"].(string)
	if id == "" {
		// CloudFormation's own generated name, not "<stack>-cache": that
		// carried neither the logical ID nor the random suffix, so two unnamed
		// clusters in one stack were handed the same ID and the second create
		// failed on a name the template never mentions. Lowercase and capped
		// because ElastiCache stores a cluster ID that way.
		id = rCtx.generatedNameLowerWithin(maxNameLenCache)
	}
	params := map[string]string{
		"Action":         "CreateCacheCluster",
		"Version":        "2015-02-02",
		"CacheClusterId": id,
	}
	if v, _ := props["Engine"].(string); v != "" {
		params["Engine"] = v
	}
	if v, _ := props["EngineVersion"].(string); v != "" {
		params["EngineVersion"] = v
	}
	if v, _ := props["CacheNodeType"].(string); v != "" {
		params["CacheNodeType"] = v
	}
	if v := fmtPropString(props, "NumCacheNodes"); v != "" {
		params["NumCacheNodes"] = v
	}
	if v, _ := props["CacheSubnetGroupName"].(string); v != "" {
		params["CacheSubnetGroupName"] = v
	}
	if v, _ := props["ReplicationGroupId"].(string); v != "" {
		params["ReplicationGroupId"] = v
	}
	if v, _ := props["PreferredAvailabilityZone"].(string); v != "" {
		params["PreferredAvailabilityZone"] = v
	}
	if v, _ := props["CacheParameterGroupName"].(string); v != "" {
		params["CacheParameterGroupName"] = v
	}
	if tags, ok := props["Tags"].([]any); ok {
		for i, item := range tags {
			if tag, ok := item.(map[string]any); ok {
				if key, _ := tag["Key"].(string); key != "" {
					params[fmt.Sprintf("Tags.Tag.%d.Key", i+1)] = key
					params[fmt.Sprintf("Tags.Tag.%d.Value", i+1)] = fmt.Sprintf("%v", tag["Value"])
				}
			}
		}
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateCacheCluster: %w", err)
	}
	body := rec.Body.String()
	arn := extractXMLValue(body, "ARN")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticache:%s:%s:cluster:%s", rCtx.Region, rCtx.AccountID, id)
	}
	// One pair per engine, as AWS populates them: RedisEndpoint for Redis and
	// Valkey, ConfigurationEndpoint for Memcached. Filling both in would be the
	// more forgiving thing to do and the wrong one — a template reading the
	// pair its engine does not have would deploy here and get nothing back on
	// AWS, which is the direction of divergence that costs a production
	// incident rather than a local one. The other pair is written empty rather
	// than omitted; see setEndpointAttrs for why that distinction matters.
	engine, _ := props["Engine"].(string)
	populated, absent := "RedisEndpoint", "ConfigurationEndpoint"
	if strings.EqualFold(engine, "memcached") {
		populated, absent = absent, populated
	}
	attrs := map[string]string{"Arn": arn}
	setEndpointAttrs(attrs, populated, extractXMLValue(body, "Address"), extractXMLValue(body, "Port"))
	setEndpointAttrs(attrs, absent, "", "")
	return id, attrs, nil
}

// Stabilize holds the resource open until the cache answers. The endpoint
// attributes above are minted at create time, so they are known before the
// engine is — and a GetAtt on ConfigurationEndpoint.Address is exactly the
// dependency that must not run early. See resourceStabilizer.
func (h *elastiCacheCacheClusterHandler) Stabilize(ctx context.Context, router http.Handler, cfg *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	return awaitResourceReady(ctx, clk, elastiCacheWait(router, rCtx.Region,
		fmt.Sprintf("cache cluster %s", physicalID),
		"DescribeCacheClusters", "CacheClusterId", physicalID,
		elastiCacheClusterStatuses,
		func(d describedElastiCaches) (string, bool) {
			if len(d.Clusters) == 0 {
				return "", false
			}
			return d.Clusters[0].Status, true
		}))
}

func (h *elastiCacheCacheClusterHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":         "DeleteCacheCluster",
		"Version":        "2015-02-02",
		"CacheClusterId": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteCacheCluster", rec, err)
}

// ── AWS::ElastiCache::ServerlessCache ────────────────────────────────────────

type elastiCacheServerlessCacheHandler struct{}

func (h *elastiCacheServerlessCacheHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["ServerlessCacheName"].(string)
	if name == "" {
		name = rCtx.generatedNameLowerWithin(maxNameLenCacheGroup)
	}
	params := map[string]string{
		"Action":              "CreateServerlessCache",
		"Version":             "2015-02-02",
		"ServerlessCacheName": name,
	}
	addElastiCacheServerlessParams(params, props)
	if tags, ok := props["Tags"].([]any); ok {
		for i, item := range tags {
			if tag, ok := item.(map[string]any); ok {
				if key, _ := tag["Key"].(string); key != "" {
					params[fmt.Sprintf("Tags.Tag.%d.Key", i+1)] = key
					params[fmt.Sprintf("Tags.Tag.%d.Value", i+1)] = fmt.Sprintf("%v", tag["Value"])
				}
			}
		}
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateServerlessCache: %w", err)
	}
	body := rec.Body.String()
	arn := extractXMLValue(body, "ARN")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticache:%s:%s:serverlesscache:%s", rCtx.Region, rCtx.AccountID, name)
	}
	return name, map[string]string{
		"ARN":                    arn,
		"Arn":                    arn,
		"Endpoint.Address":       extractXMLValue(body, "Address"),
		"Endpoint.Port":          extractXMLValue(body, "Port"),
		"ReaderEndpoint.Address": extractXMLValue(body, "Address"),
		"ReaderEndpoint.Port":    extractXMLValue(body, "Port"),
		"FullEngineVersion":      extractXMLValue(body, "FullEngineVersion"),
		"Status":                 extractXMLValue(body, "Status"),
		"CreateTime":             extractXMLValue(body, "CreateTime"),
	}, nil
}

// Stabilize holds the resource open until the serverless cache answers, on the
// same rule and for the same reason as a cache cluster — the endpoint it
// exports is minted before there is anything behind it. See resourceStabilizer.
func (h *elastiCacheServerlessCacheHandler) Stabilize(ctx context.Context, router http.Handler, cfg *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	return awaitResourceReady(ctx, clk, elastiCacheWait(router, rCtx.Region,
		fmt.Sprintf("serverless cache %s", physicalID),
		"DescribeServerlessCaches", "ServerlessCacheName", physicalID,
		elastiCacheServerlessStatuses,
		func(d describedElastiCaches) (string, bool) {
			if len(d.ServerlessCaches) == 0 {
				return "", false
			}
			return d.ServerlessCaches[0].Status, true
		}))
}

func (h *elastiCacheServerlessCacheHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":              "DeleteServerlessCache",
		"Version":             "2015-02-02",
		"ServerlessCacheName": physicalID,
	}
	if v := fmtPropString(map[string]any{}, "FinalSnapshotName"); v != "" {
		params["FinalSnapshotName"] = v
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteServerlessCache", rec, err)
}

func (h *elastiCacheServerlessCacheHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if newName, _ := props["ServerlessCacheName"].(string); newName != "" && newName != physicalID {
		return "", nil, errReplacementRequired
	}
	if oldProps != nil {
		for _, key := range []string{"KmsKeyId", "SubnetIds", "SnapshotArnsToRestore"} {
			if fmt.Sprintf("%v", props[key]) != fmt.Sprintf("%v", oldProps[key]) && props[key] != nil {
				return "", nil, errReplacementRequired
			}
		}
	}
	params := map[string]string{
		"Action":              "ModifyServerlessCache",
		"Version":             "2015-02-02",
		"ServerlessCacheName": physicalID,
	}
	addElastiCacheServerlessParams(params, props)
	delete(params, "KmsKeyId")
	delete(params, "NetworkType")
	for key := range params {
		if strings.HasPrefix(key, "SubnetIds.") || strings.HasPrefix(key, "SnapshotArnsToRestore.") {
			delete(params, key)
		}
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("ModifyServerlessCache: %w", err)
	}
	body := rec.Body.String()
	arn := extractXMLValue(body, "ARN")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticache:%s:%s:serverlesscache:%s", rCtx.Region, rCtx.AccountID, physicalID)
	}
	return physicalID, map[string]string{
		"ARN":                    arn,
		"Arn":                    arn,
		"Endpoint.Address":       extractXMLValue(body, "Address"),
		"Endpoint.Port":          extractXMLValue(body, "Port"),
		"ReaderEndpoint.Address": extractXMLValue(body, "Address"),
		"ReaderEndpoint.Port":    extractXMLValue(body, "Port"),
		"FullEngineVersion":      extractXMLValue(body, "FullEngineVersion"),
		"Status":                 extractXMLValue(body, "Status"),
		"CreateTime":             extractXMLValue(body, "CreateTime"),
	}, nil
}

func addElastiCacheServerlessParams(params map[string]string, props map[string]any) {
	for _, key := range []string{"Engine", "MajorEngineVersion", "Description", "DailySnapshotTime", "KmsKeyId", "NetworkType", "SnapshotRetentionLimit", "UserGroupId"} {
		if v := fmtPropString(props, key); v != "" {
			params[key] = v
		}
	}
	if limits, ok := props["CacheUsageLimits"].(map[string]any); ok {
		if data, ok := limits["DataStorage"].(map[string]any); ok {
			if v := fmtPropString(data, "Maximum"); v != "" {
				params["CacheUsageLimits.DataStorage.Maximum"] = v
			}
			if v := fmtPropString(data, "Unit"); v != "" {
				params["CacheUsageLimits.DataStorage.Unit"] = v
			}
		}
		if ecpu, ok := limits["ECPUPerSecond"].(map[string]any); ok {
			if v := fmtPropString(ecpu, "Maximum"); v != "" {
				params["CacheUsageLimits.ECPUPerSecond.Maximum"] = v
			}
		}
	}
	if subnets, ok := props["SubnetIds"].([]any); ok {
		for i, subnet := range subnets {
			if id, _ := subnet.(string); id != "" {
				params[fmt.Sprintf("SubnetIds.SubnetId.%d", i+1)] = id
			}
		}
	}
	if groups, ok := props["SecurityGroupIds"].([]any); ok {
		for i, group := range groups {
			if id, _ := group.(string); id != "" {
				params[fmt.Sprintf("SecurityGroupIds.SecurityGroupId.%d", i+1)] = id
			}
		}
	}
	if snapshots, ok := props["SnapshotArnsToRestore"].([]any); ok {
		for i, snapshot := range snapshots {
			if arn, _ := snapshot.(string); arn != "" {
				params[fmt.Sprintf("SnapshotArnsToRestore.SnapshotArn.%d", i+1)] = arn
			}
		}
	}
}

// ── AWS::ElastiCache::ReplicationGroup ────────────────────────────────────────

type elastiCacheReplicationGroupHandler struct{}

func (h *elastiCacheReplicationGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	id, _ := props["ReplicationGroupId"].(string)
	if id == "" {
		// 40 characters — the tightest cap of any identifier here — stored
		// lowercase, and no trailing or doubled hyphen.
		id = rCtx.generatedNameLowerWithin(maxNameLenCacheGroup)
	}
	params := map[string]string{
		"Action":                      "CreateReplicationGroup",
		"Version":                     "2015-02-02",
		"ReplicationGroupId":          id,
		"ReplicationGroupDescription": fmt.Sprintf("%v", props["ReplicationGroupDescription"]),
	}
	// Engine and EngineVersion pick the Docker image CreateReplicationGroup
	// starts (engineImage in internal/services/elasticache/handler.go) — a
	// replication group declared for valkey or memcached, or pinned to a
	// specific redis version, silently got a redis:7 container without these.
	// Update already treats an Engine change as replacement, so the handler
	// has always known the property exists; only Create was dropping it.
	if v, _ := props["Engine"].(string); v != "" {
		params["Engine"] = v
	}
	if v, _ := props["EngineVersion"].(string); v != "" {
		params["EngineVersion"] = v
	}
	if v, _ := props["CacheNodeType"].(string); v != "" {
		params["CacheNodeType"] = v
	}
	if v := props["AutomaticFailoverEnabled"]; v != nil {
		params["AutomaticFailoverEnabled"] = cfnScalarString(v)
	}
	if v := props["MultiAZEnabled"]; v != nil {
		params["MultiAZEnabled"] = cfnScalarString(v)
	}
	if v := props["SnapshotRetentionLimit"]; v != nil {
		params["SnapshotRetentionLimit"] = cfnScalarString(v)
	}
	if v, _ := props["PrimaryClusterId"].(string); v != "" {
		params["PrimaryClusterId"] = v
	}
	// What places the group in a VPC. CDK's elasticache constructs put the
	// subnet group on the replication group itself, so dropping it here left
	// the cache outside the VPC the rest of the stack was in.
	if v, _ := props["CacheSubnetGroupName"].(string); v != "" {
		params["CacheSubnetGroupName"] = v
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateReplicationGroup: %w", err)
	}
	body := rec.Body.String()
	arn := extractXMLValue(body, "ARN")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticache:%s:%s:replicationgroup:%s", rCtx.Region, rCtx.AccountID, id)
	}
	// Both pairs carry the endpoint. AWS populates ConfigurationEndPoint only
	// for a cluster-mode-enabled group and PrimaryEndPoint only for a
	// cluster-mode-disabled one, which Overcast cannot yet tell apart — it
	// models neither node groups nor replicas. Splitting these belongs with
	// that, not here.
	address, port := extractXMLValue(body, "Address"), extractXMLValue(body, "Port")
	attrs := map[string]string{"Arn": arn}
	setEndpointAttrs(attrs, "PrimaryEndPoint", address, port)
	setEndpointAttrs(attrs, "ConfigurationEndPoint", address, port)
	return id, attrs, nil
}

// Stabilize holds the resource open until the replication group answers. Its
// ConfigurationEndpoint and PrimaryEndPoint attributes are minted at create
// time, so the wait is what stands between a GetAtt on one of them and an
// endpoint with no engine behind it. See resourceStabilizer.
func (h *elastiCacheReplicationGroupHandler) Stabilize(ctx context.Context, router http.Handler, cfg *config.Config, clk clock.Clock, physicalID string, rCtx *resolveContext) error {
	return awaitResourceReady(ctx, clk, elastiCacheWait(router, rCtx.Region,
		fmt.Sprintf("replication group %s", physicalID),
		"DescribeReplicationGroups", "ReplicationGroupId", physicalID,
		elastiCacheReplicationGroupStatuses,
		func(d describedElastiCaches) (string, bool) {
			if len(d.ReplicationGroups) == 0 {
				return "", false
			}
			return d.ReplicationGroups[0].Status, true
		}))
}

func (h *elastiCacheReplicationGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":             "DeleteReplicationGroup",
		"Version":            "2015-02-02",
		"ReplicationGroupId": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteReplicationGroup", rec, err)
}

func (h *elastiCacheReplicationGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	if newID, _ := props["ReplicationGroupId"].(string); newID != "" && newID != physicalID {
		return "", nil, errReplacementRequired
	}
	if oldProps != nil {
		if newEngine, _ := props["Engine"].(string); newEngine != "" {
			if oldEngine, _ := oldProps["Engine"].(string); oldEngine != "" && newEngine != oldEngine {
				return "", nil, errReplacementRequired
			}
		}
		if newSubnet, _ := props["CacheSubnetGroupName"].(string); newSubnet != "" {
			if oldSubnet, _ := oldProps["CacheSubnetGroupName"].(string); oldSubnet != "" && newSubnet != oldSubnet {
				return "", nil, errReplacementRequired
			}
		}
	}

	params := map[string]string{
		"Action":             "ModifyReplicationGroup",
		"Version":            "2015-02-02",
		"ReplicationGroupId": physicalID,
	}
	if v, _ := props["ReplicationGroupDescription"].(string); v != "" {
		params["ReplicationGroupDescription"] = v
	}
	if v, _ := props["CacheNodeType"].(string); v != "" {
		params["CacheNodeType"] = v
	}
	if v := props["AutomaticFailoverEnabled"]; v != nil {
		params["AutomaticFailoverEnabled"] = cfnScalarString(v)
	}
	if v := props["MultiAZEnabled"]; v != nil {
		params["MultiAZEnabled"] = cfnScalarString(v)
	}
	if v, _ := props["NotificationTopicArn"].(string); v != "" {
		params["NotificationTopicArn"] = v
	}
	if v := props["SnapshotRetentionLimit"]; v != nil {
		params["SnapshotRetentionLimit"] = cfnScalarString(v)
	}
	if v, _ := props["SnapshotWindow"].(string); v != "" {
		params["SnapshotWindow"] = v
	}
	if v, _ := props["PreferredMaintenanceWindow"].(string); v != "" {
		params["PreferredMaintenanceWindow"] = v
	}
	if sgs, ok := props["SecurityGroupIds"].([]any); ok {
		for i, sg := range sgs {
			if s, _ := sg.(string); s != "" {
				params[fmt.Sprintf("SecurityGroupIds.SecurityGroupId.%d", i+1)] = s
			}
		}
	}

	if _, err := internalQuery(ctx, router, rCtx.Region, params); err != nil {
		return "", nil, fmt.Errorf("ModifyReplicationGroup: %w", err)
	}
	return physicalID, nil, nil
}

// ── AWS::ElastiCache::SubnetGroup ─────────────────────────────────────────────

type elastiCacheSubnetGroupHandler struct{}

func (h *elastiCacheSubnetGroupHandler) Create(ctx context.Context, router http.Handler, cfg *config.Config, props map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	name, _ := props["CacheSubnetGroupName"].(string)
	if name == "" {
		// 255 rather than the replication group's 40, still stored lowercase.
		name = rCtx.generatedNameLowerWithin(maxNameLenDefault)
	}
	desc, _ := props["CacheSubnetGroupDescription"].(string)
	params := map[string]string{
		"Action":                      "CreateCacheSubnetGroup",
		"Version":                     "2015-02-02",
		"CacheSubnetGroupName":        name,
		"CacheSubnetGroupDescription": desc,
	}
	if subnets, ok := props["SubnetIds"].([]any); ok {
		for i, s := range subnets {
			if id, _ := s.(string); id != "" {
				params[fmt.Sprintf("SubnetIds.SubnetIdentifier.%d", i+1)] = id
			}
		}
	}

	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	if err != nil {
		return "", nil, fmt.Errorf("CreateCacheSubnetGroup: %w", err)
	}
	body := rec.Body.String()
	arn := extractXMLValue(body, "ARN")
	if arn == "" {
		arn = fmt.Sprintf("arn:aws:elasticache:%s:%s:subnetgroup:%s", rCtx.Region, rCtx.AccountID, name)
	}
	return name, map[string]string{"Arn": arn}, nil
}

func (h *elastiCacheSubnetGroupHandler) Delete(ctx context.Context, router http.Handler, cfg *config.Config, physicalID string, rCtx *resolveContext) error {
	params := map[string]string{
		"Action":               "DeleteCacheSubnetGroup",
		"Version":              "2015-02-02",
		"CacheSubnetGroupName": physicalID,
	}
	rec, err := internalQuery(ctx, router, rCtx.Region, params)
	return teardownError("DeleteCacheSubnetGroup", rec, err)
}

func (h *elastiCacheSubnetGroupHandler) Update(ctx context.Context, router http.Handler, _ *config.Config, physicalID string, props map[string]any, oldProps map[string]any, rCtx *resolveContext) (string, map[string]string, error) {
	// Physical ID is the group name. CacheSubnetGroupName is the only
	// replacement property on real AWS, and CreateCacheSubnetGroup rejects
	// duplicates, so replacing under an unchanged name can never succeed.
	// The emulated ElastiCache has no ModifyCacheSubnetGroup, so keeping the
	// group is the closest available in-place update.
	if n, ok := props["CacheSubnetGroupName"].(string); ok && n != "" && n != physicalID {
		return "", nil, errReplacementRequired
	}
	return physicalID, nil, nil
}

// ── fmtPropString ──────────────────────────────────────────────────────────────

// fmtPropString converts a numeric or string property to a string suitable
// for Query-protocol form params (e.g. AllocatedStorage might be float64
// from JSON unmarshalling).
func fmtPropString(props map[string]any, key string) string {
	v, ok := props[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		return cfnScalarString(v)
	}
}
