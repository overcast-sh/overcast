package athena

import (
	"context"

	"github.com/overcast-sh/overcast/internal/protocol"
)

// Engine versions. "AUTO" lets Athena pick, and it picks engine version 3:
// version 2 is retired. PySpark engines belong to Spark workgroups, which
// Overcast does not emulate.
const (
	engineVersionAuto    = "AUTO"
	engineVersion3       = "Athena engine version 3"
	engineVersionPySpark = "PySpark engine version 3"
)

// minBytesScannedCutoff is BytesScannedCutoffValue's documented minimum.
const minBytesScannedCutoff = 10000000

// engineVersions is what ListEngineVersions returns: "a list of engine
// versions that are available to choose from, including the Auto option,"
// per ListEngineVersions.
var engineVersions = []EngineVersion{
	{SelectedEngineVersion: engineVersionAuto, EffectiveEngineVersion: engineVersion3},
	{SelectedEngineVersion: engineVersion3, EffectiveEngineVersion: engineVersion3},
}

// resolveEngineVersion fills in the engine a selection runs on. An unset
// selection is AUTO.
func resolveEngineVersion(ev *EngineVersion) (*EngineVersion, *protocol.AWSError) {
	selected := engineVersionAuto
	if ev != nil && ev.SelectedEngineVersion != "" {
		selected = ev.SelectedEngineVersion
	}
	for _, known := range engineVersions {
		if known.SelectedEngineVersion == selected {
			return &known, nil
		}
	}
	if selected == engineVersionPySpark {
		return nil, errNotEmulated("Apache Spark workgroups (%s) are not emulated.", engineVersionPySpark)
	}
	return nil, errInvalidRequest("Engine version %q is not supported.", selected)
}

// validateConfiguration checks a configuration and resolves its engine
// version in place.
func validateConfiguration(cfg *WorkGroupConfiguration) *protocol.AWSError {
	if cfg.BytesScannedCutoffPerQuery != nil && *cfg.BytesScannedCutoffPerQuery < minBytesScannedCutoff {
		return errInvalidRequest("BytesScannedCutoffPerQuery must be at least %d.", minBytesScannedCutoff)
	}
	ev, aerr := resolveEngineVersion(cfg.EngineVersion)
	if aerr != nil {
		return aerr
	}
	cfg.EngineVersion = ev
	return nil
}

// applyConfigurationUpdates returns cur with UpdateWorkGroup's
// ConfigurationUpdates applied. A member the updates leave unset keeps its
// value; a Remove* flag clears its member.
func applyConfigurationUpdates(cur *WorkGroupConfiguration, u *WorkGroupConfigurationUpdates) (*WorkGroupConfiguration, *protocol.AWSError) {
	next := WorkGroupConfiguration{}
	if cur != nil {
		next = *cur
	}
	setIf(&next.AdditionalConfiguration, u.AdditionalConfiguration, u.AdditionalConfiguration != "")
	setIf(&next.ExecutionRole, u.ExecutionRole, u.ExecutionRole != "")
	setIf(&next.EnableMinimumEncryptionConfiguration, u.EnableMinimumEncryptionConfiguration, u.EnableMinimumEncryptionConfiguration != nil)
	setIf(&next.EnforceWorkGroupConfiguration, u.EnforceWorkGroupConfiguration, u.EnforceWorkGroupConfiguration != nil)
	setIf(&next.PublishCloudWatchMetricsEnabled, u.PublishCloudWatchMetricsEnabled, u.PublishCloudWatchMetricsEnabled != nil)
	setIf(&next.RequesterPaysEnabled, u.RequesterPaysEnabled, u.RequesterPaysEnabled != nil)
	setIf(&next.EngineVersion, u.EngineVersion, u.EngineVersion != nil)
	setIf(&next.EngineConfiguration, u.EngineConfiguration, u.EngineConfiguration != nil)
	setIf(&next.MonitoringConfiguration, u.MonitoringConfiguration, u.MonitoringConfiguration != nil)
	setIf(&next.QueryResultsS3AccessGrantsConfiguration, u.QueryResultsS3AccessGrantsConfiguration, u.QueryResultsS3AccessGrantsConfiguration != nil)

	setIf(&next.BytesScannedCutoffPerQuery, u.BytesScannedCutoffPerQuery, u.BytesScannedCutoffPerQuery != nil)
	setIf(&next.BytesScannedCutoffPerQuery, nil, isTrue(u.RemoveBytesScannedCutoffPerQuery))
	setIf(&next.CustomerContentEncryptionConfiguration, u.CustomerContentEncryptionConfiguration, u.CustomerContentEncryptionConfiguration != nil)
	setIf(&next.CustomerContentEncryptionConfiguration, nil, isTrue(u.RemoveCustomerContentEncryptionConfiguration))

	if u.ResultConfigurationUpdates != nil {
		next.ResultConfiguration = applyResultConfigurationUpdates(next.ResultConfiguration, u.ResultConfigurationUpdates)
	}
	if u.ManagedQueryResultsConfigurationUpdates != nil {
		next.ManagedQueryResultsConfiguration = applyManagedResultsUpdates(next.ManagedQueryResultsConfiguration, u.ManagedQueryResultsConfigurationUpdates)
	}
	if aerr := validateConfiguration(&next); aerr != nil {
		return nil, aerr
	}
	return &next, nil
}

func applyResultConfigurationUpdates(cur *ResultConfiguration, u *ResultConfigurationUpdates) *ResultConfiguration {
	next := ResultConfiguration{}
	if cur != nil {
		next = *cur
	}
	setIf(&next.OutputLocation, u.OutputLocation, u.OutputLocation != "")
	setIf(&next.OutputLocation, "", isTrue(u.RemoveOutputLocation))
	setIf(&next.ExpectedBucketOwner, u.ExpectedBucketOwner, u.ExpectedBucketOwner != "")
	setIf(&next.ExpectedBucketOwner, "", isTrue(u.RemoveExpectedBucketOwner))
	setIf(&next.AclConfiguration, u.AclConfiguration, u.AclConfiguration != nil)
	setIf(&next.AclConfiguration, nil, isTrue(u.RemoveAclConfiguration))
	setIf(&next.EncryptionConfiguration, u.EncryptionConfiguration, u.EncryptionConfiguration != nil)
	setIf(&next.EncryptionConfiguration, nil, isTrue(u.RemoveEncryptionConfiguration))
	return &next
}

func applyManagedResultsUpdates(cur *ManagedQueryResultsConfiguration, u *ManagedQueryResultsConfigurationUpdates) *ManagedQueryResultsConfiguration {
	next := ManagedQueryResultsConfiguration{}
	if cur != nil {
		next = *cur
	}
	if u.Enabled != nil {
		next.Enabled = *u.Enabled
	}
	setIf(&next.EncryptionConfiguration, u.EncryptionConfiguration, u.EncryptionConfiguration != nil)
	setIf(&next.EncryptionConfiguration, nil, isTrue(u.RemoveEncryptionConfiguration))
	return &next
}

// setIf assigns v to *dst when ok.
func setIf[T any](dst *T, v T, ok bool) {
	if ok {
		*dst = v
	}
}

func isTrue(b *bool) bool { return b != nil && *b }

// ─── ListEngineVersions ───────────────────────────────────────

type listEngineVersionsReq struct {
	MaxResults int32  `json:"MaxResults"`
	NextToken  string `json:"NextToken"`
}

type listEngineVersionsResp struct {
	EngineVersions []EngineVersion `json:"EngineVersions"`
	NextToken      string          `json:"NextToken,omitempty"`
}

func (s *Service) listEngineVersionsTyped(_ context.Context, req *listEngineVersionsReq) (*listEngineVersionsResp, *protocol.AWSError) {
	page, aerr := paginate(engineVersions, req.MaxResults, req.NextToken, engineVersionsPageSize)
	if aerr != nil {
		return nil, aerr
	}
	return &listEngineVersionsResp{EngineVersions: page.Items, NextToken: page.NextToken}, nil
}

func ptr[T any](v T) *T { return &v }
