package cloudformation

import (
	"fmt"
	"maps"
)

// provisioner_athena_config.go — AWS::Athena::WorkGroup's
// WorkGroupConfiguration on its way to the Athena API: coerced to the types
// the API declares, and, on an update, turned into ConfigurationUpdates.

// athenaConfigurationBooleans are the WorkGroupConfiguration members the API
// types as booleans, and the value each reverts to when a template drops it.
var athenaConfigurationBooleans = []string{
	"EnableMinimumEncryptionConfiguration", "EnforceWorkGroupConfiguration",
	"PublishCloudWatchMetricsEnabled", "RequesterPaysEnabled",
}

// athenaCoerceConfiguration returns a copy of a template's
// WorkGroupConfiguration with its scalar members converted to the types the
// API declares. A template may legitimately carry "true" or a String-typed
// Parameter where the schema says Boolean or Integer, and CloudFormation
// converts it against the resource schema before calling the service.
func athenaCoerceConfiguration(cfg map[string]any) (map[string]any, error) {
	if cfg == nil {
		return nil, nil
	}
	out := maps.Clone(cfg)
	if v, ok := out["BytesScannedCutoffPerQuery"]; ok && v != nil {
		n, err := cfnInt64(v)
		if err != nil {
			return nil, fmt.Errorf("WorkGroupConfiguration.BytesScannedCutoffPerQuery: %w", err)
		}
		out["BytesScannedCutoffPerQuery"] = n
	}
	for _, name := range athenaConfigurationBooleans {
		if v, ok := out[name]; ok && v != nil {
			b, err := cfnBool(v)
			if err != nil {
				return nil, fmt.Errorf("WorkGroupConfiguration.%s: %w", name, err)
			}
			out[name] = b
		}
	}
	if managed, ok := out["ManagedQueryResultsConfiguration"].(map[string]any); ok {
		managed = maps.Clone(managed)
		if v, ok := managed["Enabled"]; ok && v != nil {
			b, err := cfnBool(v)
			if err != nil {
				return nil, fmt.Errorf("WorkGroupConfiguration.ManagedQueryResultsConfiguration.Enabled: %w", err)
			}
			managed["Enabled"] = b
		}
		out["ManagedQueryResultsConfiguration"] = managed
	}
	return out, nil
}

// cfnStringMap reads a map of strings — a DataCatalog's Parameters — whose
// values a YAML template may have written as numbers or booleans.
func cfnStringMap(raw any) map[string]string {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = cfnScalarString(v)
	}
	return out
}

// athenaSettableConfiguration are the WorkGroupConfiguration members
// ConfigurationUpdates takes under the same name.
var athenaSettableConfiguration = append([]string{
	"AdditionalConfiguration", "BytesScannedCutoffPerQuery", "CustomerContentEncryptionConfiguration",
	"EngineConfiguration", "EngineVersion", "ExecutionRole", "MonitoringConfiguration",
	"QueryResultsS3AccessGrantsConfiguration",
}, athenaConfigurationBooleans...)

// athenaRemovableConfiguration maps each member that has a Remove* flag to
// that flag.
var athenaRemovableConfiguration = map[string]string{
	"BytesScannedCutoffPerQuery":             "RemoveBytesScannedCutoffPerQuery",
	"CustomerContentEncryptionConfiguration": "RemoveCustomerContentEncryptionConfiguration",
}

// athenaRevertedConfiguration is what a dropped member without a Remove*
// flag is set back to: the value a workgroup that never set it reports.
func athenaRevertedConfiguration() map[string]any {
	reverted := map[string]any{"EngineVersion": map[string]any{"SelectedEngineVersion": "AUTO"}}
	for _, name := range athenaConfigurationBooleans {
		reverted[name] = false
	}
	return reverted
}

var athenaRemovableResultConfiguration = map[string]string{
	"OutputLocation":          "RemoveOutputLocation",
	"EncryptionConfiguration": "RemoveEncryptionConfiguration",
	"ExpectedBucketOwner":     "RemoveExpectedBucketOwner",
	"AclConfiguration":        "RemoveAclConfiguration",
}

// athenaConfigurationUpdates turns the template's desired
// WorkGroupConfiguration into UpdateWorkGroup's ConfigurationUpdates, so the
// workgroup converges on the template: every member it sets, a Remove* flag
// for each dropped member that has one, and the default for each dropped
// member that does not.
func athenaConfigurationUpdates(next, prev map[string]any) map[string]any {
	updates := setAndRemove(next, prev, athenaSettableConfiguration, athenaRemovableConfiguration, athenaRevertedConfiguration())
	nextResults, _ := next["ResultConfiguration"].(map[string]any)
	prevResults, _ := prev["ResultConfiguration"].(map[string]any)
	if nextResults != nil || prevResults != nil {
		names := make([]string, 0, len(athenaRemovableResultConfiguration))
		for name := range athenaRemovableResultConfiguration {
			names = append(names, name)
		}
		updates["ResultConfigurationUpdates"] = setAndRemove(nextResults, prevResults, names, athenaRemovableResultConfiguration, nil)
	}
	nextManaged, _ := next["ManagedQueryResultsConfiguration"].(map[string]any)
	prevManaged, _ := prev["ManagedQueryResultsConfiguration"].(map[string]any)
	if nextManaged != nil || prevManaged != nil {
		updates["ManagedQueryResultsConfigurationUpdates"] = setAndRemove(nextManaged, prevManaged,
			[]string{"Enabled", "EncryptionConfiguration"},
			map[string]string{"EncryptionConfiguration": "RemoveEncryptionConfiguration"},
			map[string]any{"Enabled": false})
	}
	return updates
}

// setAndRemove builds one level of an update: the settable members next
// sets, then for each member prev set and next dropped, its Remove* flag or
// else its reverted value.
func setAndRemove(next, prev map[string]any, settable []string, removable map[string]string, reverted map[string]any) map[string]any {
	out := map[string]any{}
	for _, name := range settable {
		if v, ok := next[name]; ok && v != nil {
			out[name] = v
		}
	}
	for name := range prev {
		if next[name] != nil {
			continue
		}
		if flag, ok := removable[name]; ok {
			out[flag] = true
		} else if v, ok := reverted[name]; ok {
			out[name] = v
		}
	}
	return out
}
